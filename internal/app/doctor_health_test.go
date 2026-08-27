package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

type doctorHealthStore struct {
	result diagnostics.ReadResult
	err    error
}

func (s doctorHealthStore) ReadOnly() (diagnostics.ReadResult, error) { return s.result, s.err }

func healthDoctor(t *testing.T, result diagnostics.ReadResult, readErr error, probe doctorRuntimeProbe, config []byte) (*doctorCommand, string) {
	t.Helper()
	root := t.TempDir()
	stateDir := filepath.Join(root, "state", "projmux")
	logDir := filepath.Join(stateDir, "logs")
	logPath := filepath.Join(logDir, diagnostics.LogFileName)
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config", "projmux", "tmux.conf")
	if config != nil {
		if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(configPath, config, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c := newStubDoctorCommand("linux", map[string]bool{"tmux": true, "git": true, "stty": true})
	c.resolveOperationsPath = func() (string, error) { return logPath, nil }
	c.readRuntimeHealth = func(diagnostics.ReadOnlyStore) (diagnostics.RuntimeHealth, error) {
		return diagnostics.ReadRuntimeHealth(doctorHealthStore{result: result, err: readErr})
	}
	c.runtimeProbe = func() doctorRuntimeProbe { return probe }
	c.resolveGeneratedConfig = func() (string, error) { return configPath, nil }
	c.readGeneratedConfig = doctorReadRegularFileBounded
	return c, root
}

func TestDoctorRuntimeFindingTable(t *testing.T) {
	valid := []byte(withTmuxConfigDigest("set -g @fixture 1\n"))
	digest := strings.TrimSpace(strings.TrimPrefix(string(valid[strings.LastIndex(string(valid), "set -g "+tmuxConfigDigestOption):]), "set -g "+tmuxConfigDigestOption+" "))
	for _, tc := range []struct {
		name           string
		probe          doctorRuntimeProbe
		config         []byte
		unknownBackend bool
		want           []doctorFinding
	}{
		{name: "reachable current", probe: doctorRuntimeProbe{SocketState: diagnostics.RuntimeHealthy, AppliedDigest: digest}, config: valid, want: []doctorFinding{
			{Severity: doctorSeverityInfo, Code: "runtime.backend.tmux", Remediation: doctorRemediationNone},
			{Severity: doctorSeverityInfo, Code: "runtime.socket.reachable", Remediation: doctorRemediationNone},
			{Severity: doctorSeverityInfo, Code: "runtime.config.generated-current", Remediation: doctorRemediationNone},
			{Severity: doctorSeverityInfo, Code: "runtime.config.applied-current", Remediation: doctorRemediationNone},
		}},
		{name: "reachable stale", probe: doctorRuntimeProbe{SocketState: diagnostics.RuntimeHealthy, AppliedDigest: strings.Repeat("0", 64)}, config: valid, want: []doctorFinding{
			{Severity: doctorSeverityInfo, Code: "runtime.backend.tmux", Remediation: doctorRemediationNone},
			{Severity: doctorSeverityInfo, Code: "runtime.socket.reachable", Remediation: doctorRemediationNone},
			{Severity: doctorSeverityInfo, Code: "runtime.config.generated-current", Remediation: doctorRemediationNone},
			{Severity: doctorSeverityWarning, Code: "runtime.config.applied-stale", Remediation: doctorRemediationRunTmuxApply},
		}},
		{name: "unreachable missing", probe: doctorRuntimeProbe{SocketState: diagnostics.RuntimeUnreachable}, want: []doctorFinding{
			{Severity: doctorSeverityInfo, Code: "runtime.backend.tmux", Remediation: doctorRemediationNone},
			{Severity: doctorSeverityWarning, Code: "runtime.socket.unreachable", Remediation: doctorRemediationStartRuntime},
			{Severity: doctorSeverityWarning, Code: "runtime.config.generated-missing", Remediation: doctorRemediationRunTmuxApply},
			{Severity: doctorSeverityInfo, Code: "runtime.config.applied-unknown", Remediation: doctorRemediationStartRuntime},
		}},
		{name: "unknown backend and probe failed", unknownBackend: true, probe: doctorRuntimeProbe{SocketState: diagnostics.RuntimeUnknown}, config: []byte("raw private config"), want: []doctorFinding{
			{Severity: doctorSeverityWarning, Code: "runtime.backend.unknown", Remediation: doctorRemediationInspectRuntimeLogs},
			{Severity: doctorSeverityWarning, Code: "runtime.socket.probe-failed", Remediation: doctorRemediationInspectRuntimeLogs},
			{Severity: doctorSeverityWarning, Code: "runtime.config.generated-invalid", Remediation: doctorRemediationRunTmuxApply},
			{Severity: doctorSeverityInfo, Code: "runtime.config.applied-unknown", Remediation: doctorRemediationStartRuntime},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, _ := healthDoctor(t, diagnostics.ReadResult{Missing: true}, nil, tc.probe, tc.config)
			if tc.unknownBackend {
				cmd.readRuntimeHealth = func(diagnostics.ReadOnlyStore) (diagnostics.RuntimeHealth, error) {
					return diagnostics.RuntimeHealth{MuxBackend: "invalid"}, nil
				}
			}
			findings := cmd.evaluateRuntimeFindings()
			if !reflect.DeepEqual(findings, tc.want) {
				t.Fatalf("findings = %#v, want %#v", findings, tc.want)
			}
		})
	}
}

func TestDoctorAppRouteMarkerFindingTextJSONAndReadOnlyParity(t *testing.T) {
	valid := []byte(withTmuxConfigDigest("set -g @fixture 1\n"))
	for _, tc := range []struct {
		name  string
		state runtimeMutationMarkerDiagnosis
		code  string
	}{
		{name: "missing", state: runtimeMutationMarkerMissing, code: "runtime.route-marker.missing"},
		{name: "mismatch", state: doctorRuntimeMarkerMismatch, code: "runtime.route-marker.mismatch"},
		{name: "unreadable", state: doctorRuntimeMarkerUnreadable, code: "runtime.route-marker.unreadable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, root := healthDoctor(t, diagnostics.ReadResult{}, nil, doctorRuntimeProbe{
				SocketState: diagnostics.RuntimeHealthy,
				MarkerState: tc.state,
			}, valid)
			before := snapshotDoctorTree(t, root)
			var textOut, jsonOut bytes.Buffer
			if err := cmd.Run([]string{"--section", "runtime", "--verbose"}, &textOut, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Run([]string{"--section", "runtime", "--json"}, &jsonOut, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(textOut.String(), tc.code) || !strings.Contains(jsonOut.String(), `"code": "`+tc.code+`"`) {
				t.Fatalf("marker finding missing from projections\ntext=%s\njson=%s", textOut.String(), jsonOut.String())
			}
			after := snapshotDoctorTree(t, root)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("doctor marker diagnosis mutated source tree\nbefore=%#v\nafter=%#v", before, after)
			}
		})
	}
}

func TestTroubleshootingMarkerAndNPMRecoveryEntryPoints(t *testing.T) {
	troubleshooting, err := os.ReadFile("../../docs/troubleshooting.md")
	if err != nil {
		t.Fatal(err)
	}
	body := string(troubleshooting)
	for _, want := range []string{
		"projmux doctor --section runtime --verbose",
		"projmux diagnostics log",
		"tmux -L projmux show-options -gqv @projmux_app",
		"tmux -L projmux show-options -gqv @projmux_socket_name",
		"projmux config apply --socket projmux",
		"runtime.route-marker.missing",
		"runtime.route-marker.mismatch",
		"runtime.route-marker.unreadable",
		"unsupported or incomplete npm install",
		"npm install -g projmux@latest --include=optional",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("troubleshooting guide missing %q", want)
		}
	}
	for _, forbidden := range []string{"diagnostics log --level", "diagnostics log --tail", "tmux -L projmux set"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("troubleshooting guide contains unsupported or unsafe command %q", forbidden)
		}
	}
	for _, path := range []string{"../../README.md", "../../docs/install.md", "../../docs/upgrading.md"} {
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !strings.Contains(string(raw), "troubleshooting.md") {
			t.Errorf("%s has no troubleshooting entry link", path)
		}
	}
}

func doctorExpectedPlatformLogFindings(findings []doctorFinding) []doctorFinding {
	if runtime.GOOS != "windows" {
		return findings
	}
	out := make([]doctorFinding, 0, len(findings)+3)
	for _, finding := range findings {
		if finding.Code == "logs.state.ready" || finding.Code == "logs.directory.ready" || finding.Code == "logs.journal.ready" {
			prefix := strings.TrimSuffix(finding.Code, ".ready")
			remediation := doctorRemediationInspectJournal
			switch prefix {
			case "logs.state":
				remediation = doctorRemediationInspectState
			case "logs.directory":
				remediation = doctorRemediationInspectLogs
			}
			out = append(out, doctorFinding{Severity: doctorSeverityWarning, Code: prefix + ".privacy-unverified", Remediation: remediation})
		}
		out = append(out, finding)
	}
	return out
}

func TestTmuxConfigDigestIsStableNonRecursiveAndReadable(t *testing.T) {
	body := "set -g @fixture 1\n"
	first := withTmuxConfigDigest(body)
	second := withTmuxConfigDigest(body)
	if first != second || strings.Count(first, tmuxConfigDigestOption) != 1 || !strings.HasPrefix(first, body) {
		t.Fatalf("digest marker is not stable/non-recursive: %q", first)
	}
	cmd, _ := healthDoctor(t, diagnostics.ReadResult{Missing: true}, nil, doctorRuntimeProbe{SocketState: diagnostics.RuntimeUnreachable}, []byte(first))
	state, digest := cmd.generatedConfigHealth()
	if state != "current" || !doctorDigestPattern.MatchString(digest) || strings.Contains(digest, body) {
		t.Fatalf("generatedConfigHealth = %q %q", state, digest)
	}
}

func TestDoctorLogFindingTableAndGracefulDegrade(t *testing.T) {
	boundedErrors := make([]diagnostics.Event, 21)
	for index := range boundedErrors {
		boundedErrors[index] = diagnostics.Event{Level: "error", Result: "error"}
	}
	for _, tc := range []struct {
		name     string
		result   diagnostics.ReadResult
		readErr  error
		mutate   func(string)
		want     []doctorFinding
		nonPOSIX bool
	}{
		{name: "clean", result: diagnostics.ReadResult{}, want: []doctorFinding{
			{Severity: doctorSeverityInfo, Code: "logs.state.ready", Remediation: doctorRemediationNone},
			{Severity: doctorSeverityInfo, Code: "logs.directory.ready", Remediation: doctorRemediationNone},
			{Severity: doctorSeverityInfo, Code: "logs.journal.ready", Remediation: doctorRemediationNone},
			{Severity: doctorSeverityInfo, Code: "logs.recent-errors.none", Remediation: doctorRemediationNone},
		}},
		{name: "missing", result: diagnostics.ReadResult{Missing: true}, mutate: func(root string) { _ = os.RemoveAll(filepath.Join(root, "state")) }, want: []doctorFinding{
			{Severity: doctorSeverityWarning, Code: "logs.state.missing", Remediation: doctorRemediationInspectState},
			{Severity: doctorSeverityWarning, Code: "logs.directory.missing", Remediation: doctorRemediationInspectLogs},
			{Severity: doctorSeverityWarning, Code: "logs.journal.missing", Remediation: doctorRemediationInspectJournal},
			{Severity: doctorSeverityInfo, Code: "logs.recent-errors.none", Remediation: doctorRemediationNone},
		}},
		{name: "unwritable", nonPOSIX: true, mutate: func(root string) {
			_ = os.Chmod(filepath.Join(root, "state", "projmux"), 0o500)
			_ = os.Chmod(filepath.Join(root, "state", "projmux", "logs"), 0o500)
			_ = os.Chmod(filepath.Join(root, "state", "projmux", "logs", diagnostics.LogFileName), 0o400)
		}, want: []doctorFinding{
			{Severity: doctorSeverityWarning, Code: "logs.state.not-writable", Remediation: doctorRemediationInspectState},
			{Severity: doctorSeverityWarning, Code: "logs.directory.not-writable", Remediation: doctorRemediationInspectLogs},
			{Severity: doctorSeverityWarning, Code: "logs.journal.not-writable", Remediation: doctorRemediationInspectJournal},
			{Severity: doctorSeverityInfo, Code: "logs.recent-errors.none", Remediation: doctorRemediationNone},
		}},
		{name: "malformed", result: diagnostics.ReadResult{Malformed: 2, Truncated: true}, want: []doctorFinding{
			{Severity: doctorSeverityInfo, Code: "logs.state.ready", Remediation: doctorRemediationNone},
			{Severity: doctorSeverityInfo, Code: "logs.directory.ready", Remediation: doctorRemediationNone},
			{Severity: doctorSeverityInfo, Code: "logs.journal.ready", Remediation: doctorRemediationNone},
			{Severity: doctorSeverityWarning, Code: "logs.journal.malformed", Remediation: doctorRemediationInspectJournal, Count: 2},
		}},
		{name: "recent bounded", result: diagnostics.ReadResult{Events: boundedErrors}, want: []doctorFinding{
			{Severity: doctorSeverityInfo, Code: "logs.state.ready", Remediation: doctorRemediationNone},
			{Severity: doctorSeverityInfo, Code: "logs.directory.ready", Remediation: doctorRemediationNone},
			{Severity: doctorSeverityInfo, Code: "logs.journal.ready", Remediation: doctorRemediationNone},
			{Severity: doctorSeverityWarning, Code: "logs.recent-errors.bounded", Remediation: doctorRemediationInspectRuntimeLogs, Count: 20},
		}},
		{name: "permission read error", readErr: os.ErrPermission, want: []doctorFinding{
			{Severity: doctorSeverityInfo, Code: "logs.state.ready", Remediation: doctorRemediationNone},
			{Severity: doctorSeverityInfo, Code: "logs.directory.ready", Remediation: doctorRemediationNone},
			{Severity: doctorSeverityInfo, Code: "logs.journal.ready", Remediation: doctorRemediationNone},
			{Severity: doctorSeverityWarning, Code: "logs.recent-errors.unavailable", Remediation: doctorRemediationInspectJournal},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.nonPOSIX && (runtime.GOOS == "windows" || doctorTestIsRoot()) {
				t.Skip("POSIX non-root writability fixture")
			}
			cmd, root := healthDoctor(t, tc.result, tc.readErr, doctorRuntimeProbe{SocketState: diagnostics.RuntimeUnreachable}, nil)
			if tc.mutate != nil {
				tc.mutate(root)
			}
			if tc.nonPOSIX {
				t.Cleanup(func() {
					_ = os.Chmod(filepath.Join(root, "state", "projmux"), 0o700)
					_ = os.Chmod(filepath.Join(root, "state", "projmux", "logs"), 0o700)
					_ = os.Chmod(filepath.Join(root, "state", "projmux", "logs", diagnostics.LogFileName), 0o600)
				})
			}
			findings := cmd.evaluateLogFindings()
			want := doctorExpectedPlatformLogFindings(tc.want)
			if !reflect.DeepEqual(findings, want) {
				t.Fatalf("findings = %#v, want %#v", findings, want)
			}
		})
	}
}

func TestDoctorRecentErrorsDropsCodesOutsideClosedInventory(t *testing.T) {
	finding := doctorRecentErrorsFinding(diagnostics.RuntimeHealth{
		RecentErrorCount: 2,
		RecentFailureCodes: []diagnostics.Code{
			diagnostics.CodeSessionAttachFailed,
			"raw-private-code",
		},
	})
	if !reflect.DeepEqual(finding.SafeCodes, []diagnostics.Code{diagnostics.CodeSessionAttachFailed}) {
		t.Fatalf("safe codes = %#v", finding.SafeCodes)
	}
}

func TestDoctorPathFindingMissingPermissionsAndTypes(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	if got := doctorPathFindings(missing, true, "logs.state", doctorRemediationInspectState)[0].Code; got != "logs.state.missing" {
		t.Fatalf("missing code = %q", got)
	}
	insecure := filepath.Join(root, "insecure")
	if err := os.Mkdir(insecure, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := doctorPathFindings(insecure, true, "logs.state", doctorRemediationInspectState)[0].Code; got != "logs.state.insecure-permissions" {
		t.Fatalf("permission code = %q", got)
	}
	regular := filepath.Join(root, "regular")
	if err := os.WriteFile(regular, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := doctorPathFindings(regular, true, "logs.state", doctorRemediationInspectState)[0].Code; got != "logs.state.unsafe-type" {
		t.Fatalf("type code = %q", got)
	}
}

func TestDoctorHealthProjectionParityOrderingExitAndPrivacy(t *testing.T) {
	raw := []string{"/home/raw-user/project-RAW", "socket-RAW", "session-RAW", "window-RAW", "pane-RAW", "message-RAW", "config-RAW", "--argv-RAW"}
	event := diagnostics.Event{At: time.Now().UTC().Format(time.RFC3339Nano), Level: "error", Component: "runtime", Event: "lifecycle.outcome", Result: "error", DurationMS: 1, RunID: raw[1], Version: "v", MuxBackend: "tmux", Operation: string(diagnostics.OperationTmuxApply), Code: string(diagnostics.CodeTmuxApplySocketUnreachable), Message: strings.Join(raw, " ")}
	cmd, _ := healthDoctor(t, diagnostics.ReadResult{Events: []diagnostics.Event{event}}, nil, doctorRuntimeProbe{SocketState: diagnostics.RuntimeUnreachable}, []byte("config-RAW"))
	var textOut, verboseOut, jsonOut, verboseJSON bytes.Buffer
	if err := cmd.Run([]string{"--section", "runtime"}, &textOut, &bytes.Buffer{}); err != nil {
		t.Fatalf("runtime warning changed exit: %v", err)
	}
	if err := cmd.Run([]string{"--section", "runtime", "--verbose"}, &verboseOut, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(textOut.String(), "runtime.backend.tmux") || !strings.Contains(verboseOut.String(), "runtime.backend.tmux") {
		t.Fatalf("verbose info boundary wrong\nplain=%s\nverbose=%s", textOut.String(), verboseOut.String())
	}
	if err := cmd.Run([]string{"--section", "logs", "--json"}, &jsonOut, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Run([]string{"--section", "logs", "--json", "--verbose"}, &verboseJSON, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(jsonOut.Bytes(), verboseJSON.Bytes()) {
		t.Fatal("--verbose changed JSON")
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(jsonOut.Bytes(), &root); err != nil {
		t.Fatal(err)
	}
	if string(root["schema_version"]) != "2" || root["logs"] == nil || root["runtime"] != nil {
		t.Fatalf("section projection = %s", jsonOut.String())
	}
	combined := textOut.String() + verboseOut.String() + jsonOut.String()
	for _, secret := range raw {
		if strings.Contains(combined, secret) {
			t.Fatalf("raw value %q leaked: %s", secret, combined)
		}
	}
}

func TestDoctorTextAndJSONProjectIdenticalFindingRows(t *testing.T) {
	valid := []byte(withTmuxConfigDigest("set -g @fixture 1\n"))
	digest := strings.TrimSuffix(strings.TrimPrefix(string(valid[strings.LastIndex(string(valid), "set -g "+tmuxConfigDigestOption):]), "set -g "+tmuxConfigDigestOption+" "), "\n")
	events := []diagnostics.Event{
		{Level: "error", Result: "error", Code: string(diagnostics.CodeTmuxApplyFailed)},
		{Level: "error", Result: "error", Code: string(diagnostics.CodeTmuxApplySocketUnreachable)},
	}
	cmd, _ := healthDoctor(t, diagnostics.ReadResult{Events: events}, nil, doctorRuntimeProbe{SocketState: diagnostics.RuntimeHealthy, AppliedDigest: digest}, valid)
	for _, section := range []doctorSection{doctorSectionRuntime, doctorSectionLogs} {
		t.Run(string(section), func(t *testing.T) {
			var jsonOut, textOut bytes.Buffer
			if err := cmd.Run([]string{"--section", string(section), "--json"}, &jsonOut, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Run([]string{"--section", string(section), "--verbose"}, &textOut, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			var projected doctorJSONReport
			if err := json.Unmarshal(jsonOut.Bytes(), &projected); err != nil {
				t.Fatal(err)
			}
			var rows []doctorFinding
			if section == doctorSectionRuntime {
				rows = *projected.Runtime
			} else {
				rows = *projected.Logs
			}
			wantLines := make([]string, len(rows))
			for index, finding := range rows {
				wantLines[index] = doctorFindingTextLine(finding)
			}
			var gotLines []string
			for line := range strings.SplitSeq(strings.TrimSuffix(textOut.String(), "\n"), "\n") {
				if strings.HasPrefix(line, "  [") {
					gotLines = append(gotLines, line)
				}
			}
			if !reflect.DeepEqual(gotLines, wantLines) {
				t.Fatalf("text rows = %#v, JSON rows = %#v", gotLines, rows)
			}
		})
	}
}

func doctorFindingTextLine(finding doctorFinding) string {
	line := fmt.Sprintf("  [%-7s] %s", finding.Severity, finding.Code)
	if finding.Count > 0 {
		line += fmt.Sprintf("; count: %d", finding.Count)
	}
	if len(finding.SafeCodes) > 0 {
		line += "; safe codes: " + diagnosticsCodesText(finding.SafeCodes)
	}
	return line + "; remediation: " + finding.Remediation
}

func TestDoctorHealthDoesNotMutateSources(t *testing.T) {
	valid := []byte(withTmuxConfigDigest("set -g @fixture 1\n"))
	cmd, root := healthDoctor(t, diagnostics.ReadResult{}, nil, doctorRuntimeProbe{SocketState: diagnostics.RuntimeUnreachable}, valid)
	cmd.readRuntimeHealth = diagnostics.ReadRuntimeHealth
	before := snapshotDoctorTree(t, root)
	if err := cmd.Run([]string{"--json"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	after := snapshotDoctorTree(t, root)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("doctor mutated source tree\nbefore=%#v\nafter=%#v", before, after)
	}
}

type doctorTreeEntry struct {
	Mode  os.FileMode
	Size  int64
	MTime int64
	Body  string
}

func snapshotDoctorTree(t *testing.T, root string) map[string]doctorTreeEntry {
	t.Helper()
	out := map[string]doctorTreeEntry{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		entry := doctorTreeEntry{Mode: info.Mode(), Size: info.Size(), MTime: info.ModTime().UnixNano()}
		if info.Mode().IsRegular() {
			body, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			entry.Body = string(body)
		}
		rel, _ := filepath.Rel(root, path)
		out[rel] = entry
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

type doctorProbeRunner struct {
	calls      [][]string
	state      diagnostics.RuntimeState
	digest     string
	appMarker  string
	logical    string
	markerErr  bool
	outputSize int
	limit      int
}

func (r *doctorProbeRunner) RunBounded(ctx context.Context, name string, args []string, limit int) ([]byte, []byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	r.limit = limit
	if _, ok := ctx.Deadline(); !ok {
		return nil, nil, errors.New("missing deadline")
	}
	if r.state != diagnostics.RuntimeHealthy {
		return nil, nil, errors.New("closed probe failure")
	}
	if r.outputSize > limit {
		return make([]byte, limit), nil, errDoctorInputTooLarge
	}
	switch args[len(args)-1] {
	case tmuxConfigDigestOption:
		return []byte(r.digest + "\n"), nil, nil
	case tmuxopts.AppGlobal:
		if r.markerErr {
			return nil, nil, errors.New("closed marker read failure")
		}
		return []byte(r.appMarker + "\n"), nil, nil
	case runtimeMutationSocketNameOption:
		if r.markerErr {
			return nil, nil, errors.New("closed marker read failure")
		}
		return []byte(r.logical + "\n"), nil, nil
	default:
		return nil, nil, errors.New("unexpected probe option")
	}
}

func TestDoctorRuntimeProbeUsesFixedBoundedReadOnlyArgv(t *testing.T) {
	runner := &doctorProbeRunner{state: diagnostics.RuntimeHealthy, digest: strings.Repeat("a", 64), appMarker: "1", logical: defaultAppSocket}
	probe := doctorRuntimeProbeWith(runner, 20*time.Millisecond)
	if probe.SocketState != diagnostics.RuntimeHealthy || probe.AppliedDigest != runner.digest {
		t.Fatalf("probe = %#v", probe)
	}
	want := [][]string{
		{"tmux", "-L", "projmux", "show-options", "-gqv", "@projmux_config_digest"},
		{"tmux", "-L", "projmux", "show-options", "-gqv", "@projmux_app"},
		{"tmux", "-L", "projmux", "show-options", "-gqv", "@projmux_socket_name"},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
	if runner.limit != doctorProbeOutputMaxBytes {
		t.Fatalf("probe limit = %d, want %d", runner.limit, doctorProbeOutputMaxBytes)
	}
	if runtime.GOOS == "windows" && strings.Contains(strings.Join(runner.calls[0], " "), "sh") {
		t.Fatal("probe used a shell")
	}
}

func TestDoctorRuntimeProbeClassifiesAppRouteMarkers(t *testing.T) {
	for _, tc := range []struct {
		name      string
		app       string
		logical   string
		markerErr bool
		want      runtimeMutationMarkerDiagnosis
	}{
		{name: "current", app: "1", logical: defaultAppSocket},
		{name: "pre-0.13 missing", app: "1", want: runtimeMutationMarkerMissing},
		{name: "mismatch", app: "1", logical: "other", want: doctorRuntimeMarkerMismatch},
		{name: "unreadable", app: "1", markerErr: true, want: doctorRuntimeMarkerUnreadable},
		{name: "foreign is not app-owned finding", app: "", logical: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &doctorProbeRunner{
				state: diagnostics.RuntimeHealthy, digest: strings.Repeat("a", 64),
				appMarker: tc.app, logical: tc.logical, markerErr: tc.markerErr,
			}
			probe := doctorRuntimeProbeWith(runner, 20*time.Millisecond)
			if probe.SocketState != diagnostics.RuntimeHealthy || probe.MarkerState != tc.want {
				t.Fatalf("probe = %#v, want marker %q", probe, tc.want)
			}
		})
	}
}

func TestDoctorRuntimeProbeRejectsBoundedMaliciousOutput(t *testing.T) {
	runner := &doctorProbeRunner{state: diagnostics.RuntimeHealthy, outputSize: doctorProbeOutputMaxBytes + 1}
	probe := doctorRuntimeProbeWith(runner, 20*time.Millisecond)
	if probe.SocketState != diagnostics.RuntimeUnknown || probe.AppliedDigest != "" {
		t.Fatalf("malicious output probe = %#v", probe)
	}
}

func TestDoctorExecBoundedRunnerCapsProcessOutput(t *testing.T) {
	t.Setenv("PROJMUX_DOCTOR_OUTPUT_HELPER", "1")
	stdout, stderr, err := doctorRunCommandBounded(
		exec.CommandContext(context.Background(), os.Args[0], "-test.run=TestDoctorOutputHelperProcess"), 128,
	)
	if err == nil {
		t.Fatal("oversized helper output unexpectedly succeeded")
	}
	if len(stdout)+len(stderr) > 128 {
		t.Fatalf("captured %d bytes, want <= 128", len(stdout)+len(stderr))
	}
}

func TestDoctorExecBoundedRunnerRejectsCommandsOutsideFixedProbe(t *testing.T) {
	_, _, err := (doctorExecBoundedRunner{}).RunBounded(
		context.Background(), "tmux", []string{"display-message", "unexpected"}, doctorProbeOutputMaxBytes,
	)
	if !errors.Is(err, errDoctorUnsupportedProbeCommand) {
		t.Fatalf("unexpected argv error = %v", err)
	}
}

func TestDoctorOutputHelperProcess(t *testing.T) {
	if os.Getenv("PROJMUX_DOCTOR_OUTPUT_HELPER") != "1" {
		return
	}
	_, _ = os.Stdout.Write(bytes.Repeat([]byte("x"), 1<<20))
	os.Exit(0)
}
