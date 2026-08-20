package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/diagnostics"
)

type doctorCommand struct {
	lookPath               func(string) (string, error)
	goos                   func() string
	getenv                 func(string) string
	commandVersion         func(name string) string
	aiDiagnostics          func() []doctorAINotifyIntegration
	resumeDiagnostics      func() []doctorSessionStateResumeDiagnostic
	readRuntimeHealth      func(diagnostics.ReadOnlyStore) (diagnostics.RuntimeHealth, error)
	resolveOperationsPath  func() (string, error)
	runtimeProbe           func() doctorRuntimeProbe
	resolveGeneratedConfig func() (string, error)
	readGeneratedConfig    func(string, int64) ([]byte, error)
	// readRegistry is the zero-write Registry read the invariant audit plans
	// from. It is the snapshot read rather than the ordinary load so running
	// diagnostics on a machine that never created a Project does not create the
	// state directory as a side effect.
	readRegistry func() (coremetadata.Registry, error)
}

func newDoctorCommand() *doctorCommand {
	c := &doctorCommand{
		lookPath: exec.LookPath,
		goos:     func() string { return runtime.GOOS },
		getenv:   os.Getenv,
	}
	c.commandVersion = func(name string) string {
		return defaultCommandVersion(name)
	}
	c.aiDiagnostics = func() []doctorAINotifyIntegration {
		return doctorAINotifyDiagnostics(newAICommand())
	}
	c.resumeDiagnostics = doctorSessionStateResumeDiagnostics
	c.readRuntimeHealth = diagnostics.ReadRuntimeHealth
	c.resolveOperationsPath = func() (string, error) { return diagnostics.DefaultPath(c.getenv, os.UserHomeDir) }
	c.runtimeProbe = defaultDoctorRuntimeProbe
	c.resolveGeneratedConfig = func() (string, error) { return doctorGeneratedConfigPath(c.getenv, os.UserHomeDir) }
	c.readGeneratedConfig = doctorReadRegularFileBounded
	c.readRegistry = snapshotResourceRegistry
	return c
}

type doctorDepCategory string

const (
	doctorCategoryCore     doctorDepCategory = "core"
	doctorCategoryWorkflow doctorDepCategory = "workflow"
)

type doctorDep struct {
	Name     string
	Required bool
	Category doctorDepCategory
	// SkipOnWindows marks deps that are not applicable on windows hosts
	// (e.g. POSIX-only stty).
	SkipOnWindows bool
	// PackageNames maps a package-manager key (apt, brew, pacman, dnf,
	// zypper, apk, scoop) to the install package name when it differs from
	// the binary name. Missing entries default to the binary name.
	PackageNames map[string]string
	// FallbackHint is a non-OS-specific extra suggestion appended after the
	// detected install command.
	FallbackHint string
	// ManualInstallHint is rendered as guidance but is never treated as an
	// automatically runnable install command.
	ManualInstallHint string
	// OptionalNote is the human-readable explanation rendered for optional
	// deps regardless of presence.
	OptionalNote string
	// MinVersion is the inclusive minimum semver-ish version required.
	// Empty means no version check is performed.
	MinVersion string
}

type doctorStatus string

const (
	doctorStatusOK      doctorStatus = "ok"
	doctorStatusMissing doctorStatus = "missing"
	doctorStatusStale   doctorStatus = "stale"
	doctorStatusHint    doctorStatus = "hint"
	doctorStatusSkip    doctorStatus = "skip"
)

type doctorResult struct {
	Name     string       `json:"name"`
	Required bool         `json:"required"`
	Status   doctorStatus `json:"status"`
	Version  string       `json:"version,omitempty"`
	Hint     string       `json:"hint,omitempty"`
	Install  string       `json:"install,omitempty"`
}

type doctorReport struct {
	SchemaVersion        int                                  `json:"schema_version"`
	Dependencies         []doctorResult                       `json:"dependencies"`
	AINotifyIntegrations []doctorAINotifyIntegration          `json:"ai_notify_integrations"`
	SessionStateResume   []doctorSessionStateResumeDiagnostic `json:"session_state_resume,omitempty"`
	SessionStatePrune    string                               `json:"session_state_prune"`
	Runtime              []doctorFinding                      `json:"runtime"`
	Logs                 []doctorFinding                      `json:"logs"`
	RegistryInvariants   []doctorFinding                      `json:"registry_invariants"`
}

const doctorSchemaVersion = 2

const doctorSessionStatePruneGuidance = "Snapshots are never automatically pruned; inspect stale candidates with `projmux prune snapshot` and delete only by explicit name."

type doctorSection string

const (
	doctorSectionAll          doctorSection = ""
	doctorSectionDeps         doctorSection = "deps"
	doctorSectionRuntime      doctorSection = "runtime"
	doctorSectionIntegrations doctorSection = "integrations"
	doctorSectionSessionState doctorSection = "session-state"
	doctorSectionLogs         doctorSection = "logs"
	doctorSectionRegistry     doctorSection = "registry"
)

var doctorSections = []doctorSection{
	doctorSectionDeps,
	doctorSectionRuntime,
	doctorSectionIntegrations,
	doctorSectionSessionState,
	doctorSectionLogs,
	doctorSectionRegistry,
}

func doctorDeps() []doctorDep {
	return []doctorDep{
		{Name: "tmux", Required: true, Category: doctorCategoryCore, MinVersion: "3.4"},
		{Name: "git", Required: true, Category: doctorCategoryWorkflow},
		{Name: "stty", Required: true, Category: doctorCategoryWorkflow, SkipOnWindows: true},
	}
}

// Run executes the projmux doctor diagnostics flow.
func (c *doctorCommand) Run(args []string, stdout, stderr io.Writer) error {
	if removed := doctorRemovedFlag(args); removed != "" {
		return usageError(fmt.Sprintf("flag provided but not defined: -%s\nprojmux doctor is read-only; remove --%s and run displayed remediation guidance explicitly outside doctor", removed, removed))
	}

	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON instead of the text report")
	sectionName := fs.String("section", "", "filter diagnostics: deps|runtime|integrations|session-state|logs|registry")
	verbose := fs.Bool("verbose", false, "include successful checks and full detail in the text report")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError(err.Error())
	}
	if fs.NArg() != 0 {
		return usageError("doctor does not accept positional arguments")
	}
	section, ok := parseDoctorSection(*sectionName)
	if !ok {
		return usageError("doctor --section must be one of deps, runtime, integrations, session-state, logs, or registry")
	}

	report := c.evaluateReport(section)

	if *jsonOut {
		return writeDoctorJSON(stdout, report, section)
	}

	if err := writeDoctorText(stdout, report, section, *verbose); err != nil {
		return err
	}
	for _, r := range report.Dependencies {
		if r.Required && (r.Status == doctorStatusMissing || r.Status == doctorStatusStale) {
			return fmt.Errorf("missing required dependencies; see report above")
		}
	}
	return nil
}

func parseDoctorSection(raw string) (doctorSection, bool) {
	section := doctorSection(strings.TrimSpace(raw))
	if section == doctorSectionAll {
		return section, true
	}
	if slices.Contains(doctorSections, section) {
		return section, true
	}
	return "", false
}

func doctorRemovedFlag(args []string) string {
	removed := map[string]struct{}{
		"install-missing":  {},
		"include-optional": {},
		"dry-run":          {},
	}
	for _, arg := range args {
		if arg == "--" {
			break
		}
		name := strings.TrimLeft(arg, "-")
		if before, _, ok := strings.Cut(name, "="); ok {
			name = before
		}
		if _, ok := removed[name]; ok && strings.HasPrefix(arg, "-") {
			return name
		}
	}
	return ""
}

func (c *doctorCommand) evaluateReport(section doctorSection) doctorReport {
	report := doctorReport{SchemaVersion: doctorSchemaVersion}
	if section == doctorSectionAll || section == doctorSectionDeps {
		report.Dependencies = c.evaluate()
	}
	if section == doctorSectionAll || section == doctorSectionIntegrations {
		report.AINotifyIntegrations = c.evaluateAINotifyIntegrations()
	}
	if section == doctorSectionAll || section == doctorSectionSessionState {
		report.SessionStateResume = c.evaluateSessionStateResume()
		report.SessionStatePrune = doctorSessionStatePruneGuidance
	}
	if section == doctorSectionAll || section == doctorSectionRuntime {
		report.Runtime = c.evaluateRuntimeFindings()
	}
	if section == doctorSectionAll || section == doctorSectionLogs {
		report.Logs = c.evaluateLogFindings()
	}
	if section == doctorSectionAll || section == doctorSectionRegistry {
		report.RegistryInvariants = c.evaluateRegistryInvariants()
	}
	return report
}

func doctorGeneratedConfigPath(lookupEnv func(string) string, homeDir func() (string, error)) (string, error) {
	configHome := ""
	if lookupEnv != nil {
		configHome = strings.TrimSpace(lookupEnv("XDG_CONFIG_HOME"))
	}
	if configHome == "" {
		if homeDir == nil {
			homeDir = os.UserHomeDir
		}
		home, err := homeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return "", errors.New("resolve generated config home")
		}
		configHome = filepath.Join(home, ".config")
	}
	return filepath.Join(configHome, "projmux", "tmux.conf"), nil
}

func (c *doctorCommand) evaluateAINotifyIntegrations() []doctorAINotifyIntegration {
	if c.aiDiagnostics == nil {
		return nil
	}
	return c.aiDiagnostics()
}

func (c *doctorCommand) evaluateSessionStateResume() []doctorSessionStateResumeDiagnostic {
	if c.resumeDiagnostics == nil {
		return nil
	}
	return c.resumeDiagnostics()
}

func (c *doctorCommand) evaluate() []doctorResult {
	host := c.hostGOOS()
	deps := doctorDeps()
	out := make([]doctorResult, 0, len(deps))
	for _, dep := range deps {
		out = append(out, c.evaluateDep(dep, host))
	}
	return out
}

func (c *doctorCommand) hostGOOS() string {
	if c.goos == nil {
		return runtime.GOOS
	}
	return c.goos()
}

func (c *doctorCommand) evaluateDep(dep doctorDep, host string) doctorResult {
	res := doctorResult{
		Name:     dep.Name,
		Required: dep.Required,
	}

	if dep.SkipOnWindows && host == "windows" {
		res.Status = doctorStatusSkip
		res.Hint = "windows host"
		return res
	}

	if _, err := c.lookPath(dep.Name); err == nil {
		res.Status = doctorStatusOK
		if c.commandVersion != nil {
			res.Version = c.commandVersion(dep.Name)
		}
		if dep.MinVersion != "" {
			atLeast, parsed := versionAtLeast(res.Version, dep.MinVersion)
			if parsed && !atLeast {
				res.Status = doctorStatusStale
				res.Hint = fmt.Sprintf("minimum %s; found %s", dep.MinVersion, res.Version)
				res.Install = detectInstallHint(dep, host, c.lookPath)
			}
		}
		return res
	}

	install := detectInstallHint(dep, host, c.lookPath)

	if !dep.Required {
		res.Status = doctorStatusHint
		res.Hint = dep.OptionalNote
		res.Install = install
		return res
	}

	res.Status = doctorStatusMissing
	res.Install = install
	return res
}

func writeDoctorText(w io.Writer, report doctorReport, section doctorSection, verbose bool) error {
	var buf bytes.Buffer
	buf.WriteString("projmux doctor\n")
	buf.WriteString("read-only diagnostics; displayed remediation is never executed\n")
	if section == doctorSectionAll || section == doctorSectionDeps {
		writeDoctorDependenciesText(&buf, report.Dependencies, verbose)
	}
	if section == doctorSectionAll || section == doctorSectionRuntime {
		writeDoctorFindingsText(&buf, "Runtime", report.Runtime, verbose)
	}
	if section == doctorSectionAll || section == doctorSectionIntegrations {
		writeDoctorIntegrationsText(&buf, report.AINotifyIntegrations, verbose)
	}
	if section == doctorSectionAll || section == doctorSectionSessionState {
		writeDoctorSessionStateText(&buf, report, verbose)
	}
	if section == doctorSectionAll || section == doctorSectionLogs {
		writeDoctorFindingsText(&buf, "Logs", report.Logs, verbose)
	}
	if section == doctorSectionAll || section == doctorSectionRegistry {
		writeDoctorRegistryInvariantsText(&buf, report.RegistryInvariants, verbose)
	}
	_, err := w.Write(buf.Bytes())
	return err
}

func writeDoctorFindingsText(buf *bytes.Buffer, title string, findings []doctorFinding, verbose bool) {
	buf.WriteString("\n" + title + "\n")
	counts := map[doctorFindingSeverity]int{}
	for _, finding := range findings {
		counts[finding.Severity]++
	}
	fmt.Fprintf(buf, "  Summary: %d info, %d warning, %d error.\n", counts[doctorSeverityInfo], counts[doctorSeverityWarning], counts[doctorSeverityError])
	for _, finding := range findings {
		if !verbose && finding.Severity == doctorSeverityInfo {
			continue
		}
		fmt.Fprintf(buf, "  [%-7s] %s", finding.Severity, finding.Code)
		if finding.Count > 0 {
			fmt.Fprintf(buf, "; count: %d", finding.Count)
		}
		if len(finding.SafeCodes) > 0 {
			fmt.Fprintf(buf, "; safe codes: %s", diagnosticsCodesText(finding.SafeCodes))
		}
		fmt.Fprintf(buf, "; remediation: %s\n", finding.Remediation)
	}
}

// writeDoctorRegistryInvariantsText renders the Registry materialization
// invariant section.
//
// It does not reuse writeDoctorFindingsText for one reason: that renderer hides
// info findings unless --verbose, and the zero case is the whole point of this
// section. A clean audit that printed nothing would be indistinguishable from a
// section that never ran, which is the silence C-1 Failure.Detection exists to
// end. Refusal reasons are the opposite: they quote stored absolute paths, so
// they follow the report's established rule that path detail is --verbose only.
func writeDoctorRegistryInvariantsText(buf *bytes.Buffer, findings []doctorFinding, verbose bool) {
	buf.WriteString("\nRegistry materialization invariants\n")
	counts := map[doctorFindingSeverity]int{}
	for _, finding := range findings {
		counts[finding.Severity]++
	}
	fmt.Fprintf(buf, "  Summary: %d info, %d warning, %d error.\n", counts[doctorSeverityInfo], counts[doctorSeverityWarning], counts[doctorSeverityError])
	for _, finding := range findings {
		fmt.Fprintf(buf, "  [%-7s] %s", finding.Severity, finding.Code)
		if finding.Count > 0 {
			fmt.Fprintf(buf, "; count: %d", finding.Count)
		}
		fmt.Fprintf(buf, "; remediation: %s\n", finding.Remediation)
		if !verbose {
			continue
		}
		for _, detail := range finding.Details {
			fmt.Fprintf(buf, "             reason: %s\n", detail)
		}
	}
}

func diagnosticsCodesText(codes []diagnostics.Code) string {
	values := make([]string, len(codes))
	for i, code := range codes {
		values[i] = string(code)
	}
	return strings.Join(values, ",")
}

func writeDoctorDependenciesText(buf *bytes.Buffer, results []doctorResult, verbose bool) {
	buf.WriteString("\nDependencies\n")
	var ok, missing, stale, skipped, hints int
	for _, r := range results {
		switch r.Status {
		case doctorStatusOK:
			ok++
		case doctorStatusMissing:
			missing++
		case doctorStatusStale:
			stale++
		case doctorStatusHint:
			hints++
		case doctorStatusSkip:
			skipped++
		}
	}
	fmt.Fprintf(buf, "  Summary: %d ok, %d missing, %d stale, %d skipped, %d hint.\n", ok, missing, stale, skipped, hints)
	for _, r := range results {
		if !verbose && r.Status == doctorStatusOK {
			continue
		}
		tag := fmt.Sprintf("[%s]", r.Status)
		// Why: pad tag column to fit "[missing]" so subsequent columns line up.
		fmt.Fprintf(buf, "  %-10s%-10s", tag, r.Name)

		switch r.Status {
		case doctorStatusOK:
			if r.Version != "" {
				buf.WriteString(r.Version)
			}
		case doctorStatusMissing:
			buf.WriteString("- install: ")
			if r.Install != "" {
				buf.WriteString(r.Install)
			} else {
				buf.WriteString("see https://github.com/crevissepartners/projmux for guidance")
			}
		case doctorStatusStale:
			buf.WriteString("- ")
			if r.Hint != "" {
				buf.WriteString(r.Hint)
			}
			if r.Install != "" {
				buf.WriteString("; install: ")
				buf.WriteString(r.Install)
			}
		case doctorStatusHint:
			buf.WriteString("- ")
			if r.Hint != "" {
				buf.WriteString(r.Hint)
			}
			if r.Install != "" {
				buf.WriteString("; install: ")
				buf.WriteString(r.Install)
			}
		case doctorStatusSkip:
			buf.WriteString("- ")
			if r.Hint != "" {
				buf.WriteString(r.Hint)
			}
		}
		buf.WriteString("\n")
	}
}

func writeDoctorIntegrationsText(buf *bytes.Buffer, results []doctorAINotifyIntegration, verbose bool) {
	buf.WriteString("\nAI notify integrations\n")
	counts := map[doctorAINotifyStatus]int{}
	for _, result := range results {
		counts[result.Status]++
	}
	fmt.Fprintf(buf, "  Summary: %d installed, %d missing, %d stale, %d conflict, %d skipped.\n",
		counts[doctorAINotifyStatusInstalled], counts[doctorAINotifyStatusMissing], counts[doctorAINotifyStatusStale], counts[doctorAINotifyStatusConflict], counts[doctorAINotifyStatusSkip])
	for _, r := range results {
		if !verbose && r.Status == doctorAINotifyStatusInstalled {
			continue
		}
		tag := fmt.Sprintf("[%s]", r.Status)
		fmt.Fprintf(buf, "  %-11s%-22s", tag, r.Name)
		if !verbose {
			buf.WriteString("\n")
			continue
		}
		if r.ProviderID != "" {
			state := "disabled"
			if r.ProviderEnabled != nil && *r.ProviderEnabled {
				state = "enabled"
			}
			fmt.Fprintf(buf, "provider: %s (%s)", r.ProviderID, state)
		}
		if r.ConfigPath != "" {
			if r.ProviderID != "" {
				buf.WriteString("; ")
			}
			fmt.Fprintf(buf, "config: %s", r.ConfigPath)
		}
		if r.StatusLinePath != "" {
			if r.ProviderID != "" || r.ConfigPath != "" {
				buf.WriteString("; ")
			}
			fmt.Fprintf(buf, "statusline config: %s", r.StatusLinePath)
		}
		if r.ConflictReason != "" {
			if r.ProviderID != "" || r.ConfigPath != "" || r.StatusLinePath != "" {
				buf.WriteString("; ")
			}
			buf.WriteString(r.ConflictReason)
		}
		if r.TestedVersion != "" {
			if r.ProviderID != "" || r.ConfigPath != "" || r.StatusLinePath != "" || r.ConflictReason != "" {
				buf.WriteString("; ")
			}
			buf.WriteString("tested: ")
			buf.WriteString(r.TestedVersion)
		}
		if r.Guidance != "" {
			if r.ProviderID != "" || r.ConfigPath != "" || r.StatusLinePath != "" || r.ConflictReason != "" || r.TestedVersion != "" {
				buf.WriteString("; ")
			}
			buf.WriteString("notice: ")
			buf.WriteString(r.Guidance)
		}
		if r.InstallCommand != "" {
			if r.ProviderID != "" || r.ConfigPath != "" || r.ConflictReason != "" || r.TestedVersion != "" || r.Guidance != "" {
				buf.WriteString("; ")
			}
			buf.WriteString("install: ")
			buf.WriteString(r.InstallCommand)
		}
		if r.DryRunCommand != "" {
			if r.ProviderID != "" || r.ConfigPath != "" || r.ConflictReason != "" || r.TestedVersion != "" || r.Guidance != "" || r.InstallCommand != "" {
				buf.WriteString("; ")
			}
			buf.WriteString("dry-run: ")
			buf.WriteString(r.DryRunCommand)
		}
		if r.RemoveCommand != "" {
			if r.ProviderID != "" || r.ConfigPath != "" || r.ConflictReason != "" || r.TestedVersion != "" || r.Guidance != "" || r.InstallCommand != "" || r.DryRunCommand != "" {
				buf.WriteString("; ")
			}
			buf.WriteString("remove: ")
			buf.WriteString(r.RemoveCommand)
		}
		buf.WriteString("\n")
	}
}

func writeDoctorSessionStateText(buf *bytes.Buffer, report doctorReport, verbose bool) {
	buf.WriteString("\nSession State resume metadata\n")
	counts := map[string]int{}
	for _, result := range report.SessionStateResume {
		counts[result.Status]++
	}
	fmt.Fprintf(buf, "  Summary: %d available, %d stale, %d unavailable.\n", counts["available"], counts["stale"], counts["unavailable"])
	for _, r := range report.SessionStateResume {
		if !verbose && r.Status == "available" {
			continue
		}
		tag := fmt.Sprintf("[%s]", r.Status)
		fmt.Fprintf(buf, "  %-15s%-8s %s:%d.%d", tag, r.Agent, r.Session, r.WindowIndex, r.PaneIndex)
		if !verbose {
			if r.Reason != "" {
				fmt.Fprintf(buf, "; %s", r.Reason)
			}
			buf.WriteString("\n")
			continue
		}
		if r.Confidence != "" {
			fmt.Fprintf(buf, "; confidence: %s", r.Confidence)
		}
		if r.ResumeSource != "" {
			fmt.Fprintf(buf, "; source: %s", r.ResumeSource)
		}
		if r.ResumeUpdatedAt != "" {
			fmt.Fprintf(buf, "; updated: %s", r.ResumeUpdatedAt)
		}
		if r.Reason != "" {
			fmt.Fprintf(buf, "; %s", r.Reason)
		}
		if r.SnapshotPath != "" {
			fmt.Fprintf(buf, "; snapshot: %s", r.SnapshotPath)
		}
		buf.WriteString("\n")
	}
	if report.SessionStatePrune != "" {
		buf.WriteString("\nSession State retention\n")
		fmt.Fprintf(buf, "  %s\n", report.SessionStatePrune)
	}
}

type doctorJSONReport struct {
	SchemaVersion        int                                   `json:"schema_version"`
	Dependencies         *[]doctorResult                       `json:"dependencies,omitempty"`
	AINotifyIntegrations *[]doctorAINotifyIntegration          `json:"ai_notify_integrations,omitempty"`
	SessionStateResume   *[]doctorSessionStateResumeDiagnostic `json:"session_state_resume,omitempty"`
	SessionStatePrune    *string                               `json:"session_state_prune,omitempty"`
	Runtime              *[]doctorFinding                      `json:"runtime,omitempty"`
	Logs                 *[]doctorFinding                      `json:"logs,omitempty"`
	RegistryInvariants   *[]doctorFinding                      `json:"registry_invariants,omitempty"`
}

func writeDoctorJSON(w io.Writer, report doctorReport, section doctorSection) error {
	out := doctorJSONReport{SchemaVersion: report.SchemaVersion}
	if section == doctorSectionAll || section == doctorSectionDeps {
		out.Dependencies = &report.Dependencies
	}
	if section == doctorSectionAll || section == doctorSectionIntegrations {
		out.AINotifyIntegrations = &report.AINotifyIntegrations
	}
	if (section == doctorSectionAll && len(report.SessionStateResume) > 0) || section == doctorSectionSessionState {
		out.SessionStateResume = &report.SessionStateResume
	}
	if section == doctorSectionAll || section == doctorSectionSessionState {
		out.SessionStatePrune = &report.SessionStatePrune
	}
	if section == doctorSectionAll || section == doctorSectionRuntime {
		out.Runtime = &report.Runtime
	}
	if section == doctorSectionAll || section == doctorSectionLogs {
		out.Logs = &report.Logs
	}
	if section == doctorSectionAll || section == doctorSectionRegistry {
		out.RegistryInvariants = &report.RegistryInvariants
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func detectInstallHint(dep doctorDep, host string, lookPath func(string) (string, error)) string {
	if lookPath == nil {
		return ""
	}
	if dep.ManualInstallHint != "" {
		return "manual: " + dep.ManualInstallHint
	}
	pkg := func(key string) string {
		if name, ok := dep.PackageNames[key]; ok && name != "" {
			return name
		}
		return dep.Name
	}

	var primary string
	switch host {
	case "darwin":
		if _, err := lookPath("brew"); err == nil {
			primary = "brew install " + pkg("brew")
		}
	case "linux":
		switch {
		case hasOnPath(lookPath, "apt-get"):
			primary = "sudo apt-get install -y " + pkg("apt")
		case hasOnPath(lookPath, "pacman"):
			primary = "sudo pacman -S " + pkg("pacman")
		case hasOnPath(lookPath, "dnf"):
			primary = "sudo dnf install " + pkg("dnf")
		case hasOnPath(lookPath, "zypper"):
			primary = "sudo zypper install " + pkg("zypper")
		case hasOnPath(lookPath, "apk"):
			primary = "sudo apk add " + pkg("apk")
		}
	case "windows":
		primary = "scoop install " + pkg("scoop")
	}

	if dep.FallbackHint != "" {
		if primary == "" {
			return dep.FallbackHint
		}
		return primary + " (" + dep.FallbackHint + ")"
	}
	return primary
}

func hasOnPath(lookPath func(string) (string, error), name string) bool {
	if lookPath == nil {
		return false
	}
	_, err := lookPath(name)
	return err == nil
}

// versionProbeArgs maps a binary name to the argv that prints its version.
// Most tools accept --version, but tmux uses -V.
var versionProbeArgs = map[string][]string{
	"tmux": {"-V"},
}

// doctorVersionPattern matches the first version-looking token in a string,
// tolerating a trailing single-letter suffix (tmux uses 3.4a/3.4b convention).
var doctorVersionPattern = regexp.MustCompile(`\d+(\.\d+){0,2}[a-z]?`)

// parseDoctorVersion extracts a (major, minor, patch) tuple from a tool's
// version-output line. Missing components default to 0. Trailing letter
// suffixes on tmux (e.g. "3.4a") are tolerated. Returns ok=false when no
// version-looking token is found.
func parseDoctorVersion(raw string) (major, minor, patch int, ok bool) {
	tok := doctorVersionPattern.FindString(raw)
	if tok == "" {
		return 0, 0, 0, false
	}
	if last := tok[len(tok)-1]; last >= 'a' && last <= 'z' {
		tok = tok[:len(tok)-1]
	}
	if tok == "" {
		return 0, 0, 0, false
	}
	parts := strings.Split(tok, ".")
	out := [3]int{}
	for i := 0; i < len(parts) && i < 3; i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return 0, 0, 0, false
		}
		out[i] = n
	}
	return out[0], out[1], out[2], true
}

// versionAtLeast reports whether got >= want using lexicographic
// (major, minor, patch) comparison. Both inputs are parsed via
// parseDoctorVersion. If parsing fails on either side, returns true,
// false (i.e. cannot determine — treat as ok rather than failing
// loudly on parse glitches).
func versionAtLeast(got, want string) (atLeast bool, parsed bool) {
	gM, gm, gp, gok := parseDoctorVersion(got)
	wM, wm, wp, wok := parseDoctorVersion(want)
	if !gok || !wok {
		return true, false
	}
	g := [3]int{gM, gm, gp}
	w := [3]int{wM, wm, wp}
	for i := range 3 {
		if g[i] != w[i] {
			return g[i] > w[i], true
		}
	}
	return true, true
}

func defaultCommandVersion(name string) string {
	args := versionProbeArgs[name]
	if len(args) == 0 {
		args = []string{"--version"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	first := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	return first
}
