package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/diagnostics"
)

type doctorFindingSeverity string

const (
	doctorSeverityInfo    doctorFindingSeverity = "info"
	doctorSeverityWarning doctorFindingSeverity = "warning"
	doctorSeverityError   doctorFindingSeverity = "error"
)

var doctorFindingCodeInventory = []string{
	"runtime.backend.tmux", "runtime.backend.unknown",
	"runtime.socket.reachable", "runtime.socket.unreachable", "runtime.socket.probe-failed",
	"runtime.config.generated-current", "runtime.config.generated-missing", "runtime.config.generated-unreadable", "runtime.config.generated-invalid",
	"runtime.config.applied-current", "runtime.config.applied-stale", "runtime.config.applied-unknown",
	"logs.state.ready", "logs.state.missing", "logs.state.unreadable", "logs.state.unsafe-type", "logs.state.insecure-permissions", "logs.state.privacy-unverified", "logs.state.not-writable", "logs.state.unresolved",
	"logs.directory.ready", "logs.directory.missing", "logs.directory.unreadable", "logs.directory.unsafe-type", "logs.directory.insecure-permissions", "logs.directory.privacy-unverified", "logs.directory.not-writable", "logs.directory.unresolved",
	"logs.journal.ready", "logs.journal.missing", "logs.journal.unreadable", "logs.journal.unsafe-type", "logs.journal.insecure-permissions", "logs.journal.privacy-unverified", "logs.journal.not-writable", "logs.journal.unavailable", "logs.journal.malformed",
	"logs.recent-errors.none", "logs.recent-errors.present", "logs.recent-errors.bounded", "logs.recent-errors.unavailable",
}

var doctorFindingRemediationInventory = []string{
	doctorRemediationNone, doctorRemediationInspectState, doctorRemediationInspectLogs,
	doctorRemediationInspectJournal, doctorRemediationRunTmuxApply, doctorRemediationStartRuntime,
	doctorRemediationInspectRuntimeLogs,
}

type doctorFinding struct {
	Severity    doctorFindingSeverity `json:"severity"`
	Code        string                `json:"code"`
	Remediation string                `json:"remediation"`
	Count       int                   `json:"count,omitempty"`
	SafeCodes   []diagnostics.Code    `json:"safe_codes,omitempty"`
}

const (
	doctorRemediationNone               = "none"
	doctorRemediationInspectState       = "inspect-state-permissions"
	doctorRemediationInspectLogs        = "inspect-log-permissions"
	doctorRemediationInspectJournal     = "inspect-operational-journal"
	doctorRemediationRunTmuxApply       = "run-projmux-tmux-apply"
	doctorRemediationStartRuntime       = "start-projmux-runtime"
	doctorRemediationInspectRuntimeLogs = "inspect-operational-errors"
)

type doctorRuntimeProbe struct {
	SocketState   diagnostics.RuntimeState
	AppliedDigest string
}

var doctorDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

const (
	doctorGeneratedConfigMaxBytes = 1 << 20
	doctorProbeOutputMaxBytes     = 4 << 10
)

var (
	errDoctorUnsafeFileType          = errors.New("unsafe file type")
	errDoctorInputTooLarge           = errors.New("input exceeds diagnostic bound")
	errDoctorUnsupportedProbeCommand = errors.New("unsupported doctor probe command")
)

func (c *doctorCommand) evaluateRuntimeFindings() []doctorFinding {
	path, pathErr := c.operationsPath()
	health := diagnostics.RuntimeHealth{MuxBackend: diagnostics.MuxBackend(), Socket: diagnostics.RuntimeUnknown, Apply: diagnostics.RuntimeUnknown}
	if pathErr == nil && c.readRuntimeHealth != nil {
		if read, err := c.readRuntimeHealth(diagnostics.NewStore(path)); err == nil {
			health = read
		}
	}
	findings := make([]doctorFinding, 0, 4)
	if health.MuxBackend == diagnostics.MuxBackend() {
		findings = append(findings, doctorFinding{Severity: doctorSeverityInfo, Code: "runtime.backend.tmux", Remediation: doctorRemediationNone})
	} else {
		findings = append(findings, doctorFinding{Severity: doctorSeverityWarning, Code: "runtime.backend.unknown", Remediation: doctorRemediationInspectRuntimeLogs})
	}
	probe := doctorRuntimeProbe{SocketState: diagnostics.RuntimeUnknown}
	if c.runtimeProbe != nil {
		probe = c.runtimeProbe()
	}
	switch probe.SocketState {
	case diagnostics.RuntimeHealthy:
		findings = append(findings, doctorFinding{Severity: doctorSeverityInfo, Code: "runtime.socket.reachable", Remediation: doctorRemediationNone})
	case diagnostics.RuntimeUnreachable:
		findings = append(findings, doctorFinding{Severity: doctorSeverityWarning, Code: "runtime.socket.unreachable", Remediation: doctorRemediationStartRuntime})
	default:
		findings = append(findings, doctorFinding{Severity: doctorSeverityWarning, Code: "runtime.socket.probe-failed", Remediation: doctorRemediationInspectRuntimeLogs})
	}

	generatedState, generatedDigest := c.generatedConfigHealth()
	switch generatedState {
	case "current":
		findings = append(findings, doctorFinding{Severity: doctorSeverityInfo, Code: "runtime.config.generated-current", Remediation: doctorRemediationNone})
	case "missing":
		findings = append(findings, doctorFinding{Severity: doctorSeverityWarning, Code: "runtime.config.generated-missing", Remediation: doctorRemediationRunTmuxApply})
	case "unreadable":
		findings = append(findings, doctorFinding{Severity: doctorSeverityError, Code: "runtime.config.generated-unreadable", Remediation: doctorRemediationInspectLogs})
	default:
		findings = append(findings, doctorFinding{Severity: doctorSeverityWarning, Code: "runtime.config.generated-invalid", Remediation: doctorRemediationRunTmuxApply})
	}

	switch {
	case probe.SocketState != diagnostics.RuntimeHealthy:
		findings = append(findings, doctorFinding{Severity: doctorSeverityInfo, Code: "runtime.config.applied-unknown", Remediation: doctorRemediationStartRuntime})
	case generatedState == "current" && probe.AppliedDigest == generatedDigest:
		findings = append(findings, doctorFinding{Severity: doctorSeverityInfo, Code: "runtime.config.applied-current", Remediation: doctorRemediationNone})
	default:
		findings = append(findings, doctorFinding{Severity: doctorSeverityWarning, Code: "runtime.config.applied-stale", Remediation: doctorRemediationRunTmuxApply})
	}
	return findings
}

func (c *doctorCommand) evaluateLogFindings() []doctorFinding {
	path, err := c.operationsPath()
	if err != nil {
		return []doctorFinding{
			{Severity: doctorSeverityError, Code: "logs.state.unresolved", Remediation: doctorRemediationInspectState},
			{Severity: doctorSeverityError, Code: "logs.directory.unresolved", Remediation: doctorRemediationInspectLogs},
			{Severity: doctorSeverityWarning, Code: "logs.journal.unavailable", Remediation: doctorRemediationInspectJournal},
			{Severity: doctorSeverityWarning, Code: "logs.recent-errors.unavailable", Remediation: doctorRemediationInspectJournal},
		}
	}
	stateDir := filepath.Dir(filepath.Dir(path))
	logDir := filepath.Dir(path)
	stateFindings := doctorPathFindings(stateDir, true, "logs.state", doctorRemediationInspectState)
	directoryFindings := doctorPathFindings(logDir, true, "logs.directory", doctorRemediationInspectLogs)
	journalFindings := doctorPathFindings(path, false, "logs.journal", doctorRemediationInspectJournal)
	findings := append(append(stateFindings, directoryFindings...), journalFindings...)
	if doctorFindingsContainCode(journalFindings, "logs.journal.unsafe-type") {
		return append(findings, doctorFinding{Severity: doctorSeverityWarning, Code: "logs.recent-errors.unavailable", Remediation: doctorRemediationInspectJournal})
	}
	if c.readRuntimeHealth == nil {
		return append(findings, doctorFinding{Severity: doctorSeverityWarning, Code: "logs.recent-errors.unavailable", Remediation: doctorRemediationInspectJournal})
	}
	health, readErr := c.readRuntimeHealth(diagnostics.NewStore(path))
	switch {
	case readErr != nil:
		findings = append(findings, doctorFinding{Severity: doctorSeverityWarning, Code: "logs.recent-errors.unavailable", Remediation: doctorRemediationInspectJournal})
	case health.Missing:
		findings = append(findings, doctorFinding{Severity: doctorSeverityInfo, Code: "logs.recent-errors.none", Remediation: doctorRemediationNone})
	case health.Malformed > 0 || health.Truncated:
		findings = append(findings, doctorFinding{Severity: doctorSeverityWarning, Code: "logs.journal.malformed", Remediation: doctorRemediationInspectJournal, Count: health.Malformed})
		if health.RecentErrorCount > 0 {
			findings = append(findings, doctorRecentErrorsFinding(health))
		}
	case health.RecentErrorCount > 0:
		findings = append(findings, doctorRecentErrorsFinding(health))
	default:
		findings = append(findings, doctorFinding{Severity: doctorSeverityInfo, Code: "logs.recent-errors.none", Remediation: doctorRemediationNone})
	}
	return findings
}

func doctorRecentErrorsFinding(health diagnostics.RuntimeHealth) doctorFinding {
	code := "logs.recent-errors.present"
	if health.RecentErrorsBounded {
		code = "logs.recent-errors.bounded"
	}
	allowed := map[diagnostics.Code]bool{}
	for _, safe := range diagnostics.AllowedCodes() {
		allowed[safe] = true
	}
	safeCodes := make([]diagnostics.Code, 0, len(health.RecentFailureCodes))
	for _, candidate := range health.RecentFailureCodes {
		if allowed[candidate] {
			safeCodes = append(safeCodes, candidate)
		}
	}
	finding := doctorFinding{Severity: doctorSeverityWarning, Code: code, Remediation: doctorRemediationInspectRuntimeLogs, Count: health.RecentErrorCount}
	if len(safeCodes) > 0 {
		finding.SafeCodes = safeCodes
	}
	return finding
}

func doctorPathFindings(path string, directory bool, prefix, remediation string) []doctorFinding {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return []doctorFinding{{Severity: doctorSeverityWarning, Code: prefix + ".missing", Remediation: remediation}}
	case err != nil:
		return []doctorFinding{{Severity: doctorSeverityError, Code: prefix + ".unreadable", Remediation: remediation}}
	case info.Mode()&os.ModeSymlink != 0 || directory != info.IsDir() || (!directory && !info.Mode().IsRegular()):
		return []doctorFinding{{Severity: doctorSeverityError, Code: prefix + ".unsafe-type", Remediation: remediation}}
	}
	private, privacyKnown := doctorPathPrivacyPrivate(info)
	switch {
	case !privacyKnown:
		findings := []doctorFinding{{Severity: doctorSeverityWarning, Code: prefix + ".privacy-unverified", Remediation: remediation}}
		if doctorPathWritable(path, directory) {
			return append(findings, doctorFinding{Severity: doctorSeverityInfo, Code: prefix + ".ready", Remediation: doctorRemediationNone})
		}
		return append(findings, doctorFinding{Severity: doctorSeverityWarning, Code: prefix + ".not-writable", Remediation: remediation})
	case !private:
		return []doctorFinding{{Severity: doctorSeverityWarning, Code: prefix + ".insecure-permissions", Remediation: remediation}}
	case !doctorPathWritable(path, directory):
		return []doctorFinding{{Severity: doctorSeverityWarning, Code: prefix + ".not-writable", Remediation: remediation}}
	default:
		return []doctorFinding{{Severity: doctorSeverityInfo, Code: prefix + ".ready", Remediation: doctorRemediationNone}}
	}
}

func doctorFindingsContainCode(findings []doctorFinding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func (c *doctorCommand) operationsPath() (string, error) {
	if c.resolveOperationsPath != nil {
		return c.resolveOperationsPath()
	}
	return diagnostics.DefaultPath(c.getenv, os.UserHomeDir)
}

func (c *doctorCommand) generatedConfigHealth() (string, string) {
	if c.resolveGeneratedConfig == nil || c.readGeneratedConfig == nil {
		return "unreadable", ""
	}
	path, err := c.resolveGeneratedConfig()
	if err != nil {
		return "unreadable", ""
	}
	body, err := c.readGeneratedConfig(path, doctorGeneratedConfigMaxBytes)
	if errors.Is(err, os.ErrNotExist) {
		return "missing", ""
	}
	if err != nil {
		return "unreadable", ""
	}
	marker := []byte("set -g " + tmuxConfigDigestOption + " ")
	index := strings.LastIndex(string(body), string(marker))
	if index < 0 || index+len(marker)+64+1 != len(body) || body[len(body)-1] != '\n' {
		return "invalid", ""
	}
	digest := string(body[index+len(marker) : len(body)-1])
	if !doctorDigestPattern.MatchString(digest) {
		return "invalid", ""
	}
	sum := sha256.Sum256(body[:index])
	if hex.EncodeToString(sum[:]) != digest {
		return "invalid", ""
	}
	return "current", digest
}

func defaultDoctorRuntimeProbe() doctorRuntimeProbe {
	return doctorRuntimeProbeWith(doctorExecBoundedRunner{}, time.Second)
}

type doctorBoundedRunner interface {
	RunBounded(context.Context, string, []string, int) ([]byte, []byte, error)
}

type doctorExecBoundedRunner struct{}

type doctorBoundedBuffer struct {
	data  []byte
	limit int
}

func (b *doctorBoundedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - len(b.data)
	if remaining <= 0 {
		return 0, errDoctorInputTooLarge
	}
	if len(p) > remaining {
		b.data = append(b.data, p[:remaining]...)
		return remaining, errDoctorInputTooLarge
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func (doctorExecBoundedRunner) RunBounded(ctx context.Context, name string, args []string, limit int) ([]byte, []byte, error) {
	if limit <= 0 {
		return nil, nil, errDoctorInputTooLarge
	}
	if name != "tmux" || !doctorRuntimeProbeArgsMatch(args) {
		return nil, nil, errDoctorUnsupportedProbeCommand
	}
	cmd := exec.CommandContext(ctx, "tmux", "-L", "projmux", "show-options", "-gqv", "@projmux_config_digest")
	return doctorRunCommandBounded(cmd, limit)
}

func doctorRunCommandBounded(cmd *exec.Cmd, limit int) ([]byte, []byte, error) {
	stdout := doctorBoundedBuffer{limit: limit / 2}
	stderr := doctorBoundedBuffer{limit: limit - limit/2}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.data, stderr.data, err
}

func doctorRuntimeProbeArgsMatch(args []string) bool {
	return len(args) == 5 &&
		args[0] == "-L" && args[1] == defaultAppSocket &&
		args[2] == "show-options" && args[3] == "-gqv" && args[4] == tmuxConfigDigestOption
}

func doctorRuntimeProbeWith(runner doctorBoundedRunner, timeout time.Duration) doctorRuntimeProbe {
	if timeout <= 0 {
		timeout = time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	args := []string{"-L", defaultAppSocket, "show-options", "-gqv", tmuxConfigDigestOption}
	out, stderr, err := runner.RunBounded(ctx, "tmux", args, doctorProbeOutputMaxBytes)
	if err != nil {
		if doctorProbeNoServer(err, stderr) {
			return doctorRuntimeProbe{SocketState: diagnostics.RuntimeUnreachable}
		}
		return doctorRuntimeProbe{SocketState: diagnostics.RuntimeUnknown}
	}
	digest := strings.TrimSpace(string(out))
	if !doctorDigestPattern.MatchString(digest) {
		digest = ""
	}
	return doctorRuntimeProbe{SocketState: diagnostics.RuntimeHealthy, AppliedDigest: digest}
}

func doctorProbeNoServer(err error, stderr []byte) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(string(stderr)))
	return strings.HasPrefix(message, "no server running on ") && len(message) > len("no server running on ") ||
		message == "failed to connect to server: connection refused" ||
		strings.HasPrefix(message, "error connecting to ") && strings.HasSuffix(message, " (no such file or directory)")
}
