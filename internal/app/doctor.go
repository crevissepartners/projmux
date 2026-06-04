package app

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type doctorCommand struct {
	lookPath          func(string) (string, error)
	goos              func() string
	getenv            func(string) string
	commandVersion    func(name string) string
	runExternal       func(name string, args []string, stdout, stderr io.Writer) error
	aiDiagnostics     func() []doctorAINotifyIntegration
	resumeDiagnostics func() []doctorSessionStateResumeDiagnostic
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
	c.runExternal = runDoctorExternal
	c.aiDiagnostics = func() []doctorAINotifyIntegration {
		return doctorAINotifyDiagnostics(newAICommand())
	}
	c.resumeDiagnostics = doctorSessionStateResumeDiagnostics
	return c
}

type doctorDepCategory string

const (
	doctorCategoryCore     doctorDepCategory = "core"
	doctorCategoryWorkflow doctorDepCategory = "workflow"
	doctorCategoryOptional doctorDepCategory = "optional"
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
	Dependencies         []doctorResult                       `json:"dependencies"`
	AINotifyIntegrations []doctorAINotifyIntegration          `json:"ai_notify_integrations"`
	SessionStateResume   []doctorSessionStateResumeDiagnostic `json:"session_state_resume,omitempty"`
}

func doctorDeps() []doctorDep {
	return []doctorDep{
		{Name: "tmux", Required: true, Category: doctorCategoryCore, MinVersion: "3.4"},
		{Name: "git", Required: true, Category: doctorCategoryWorkflow},
		{Name: "stty", Required: true, Category: doctorCategoryWorkflow, SkipOnWindows: true},
		{
			Name:         "kubectl",
			Required:     false,
			Category:     doctorCategoryOptional,
			OptionalNote: "optional; install if you use the kubectl switcher",
		},
	}
}

func doctorDepsForHost(host string) []doctorDep {
	deps := doctorDeps()
	if doctorUsesPsmuxTrack(host) {
		for i, dep := range deps {
			if dep.Category == doctorCategoryCore {
				deps[i] = doctorDep{Name: "psmux", Required: true, Category: doctorCategoryCore, MinVersion: "3.3.4"}
				break
			}
		}
	}
	return deps
}

func doctorUsesPsmuxTrack(host string) bool {
	return host == "windows"
}

// Run executes the projmux doctor diagnostics flow.
func (c *doctorCommand) Run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON instead of the text report")
	installMissing := fs.Bool("install-missing", false, "install missing or stale required dependencies")
	includeOptional := fs.Bool("include-optional", false, "include optional missing dependencies with --install-missing")
	dryRun := fs.Bool("dry-run", false, "print install commands without running them")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("doctor does not accept positional arguments")
	}
	if *jsonOut && (*installMissing || *dryRun || *includeOptional) {
		return fmt.Errorf("doctor --json cannot be combined with install flags")
	}
	if (*dryRun || *includeOptional) && !*installMissing {
		return fmt.Errorf("doctor --dry-run and --include-optional require --install-missing")
	}

	results := c.evaluate()
	report := doctorReport{
		Dependencies:         results,
		AINotifyIntegrations: c.evaluateAINotifyIntegrations(),
		SessionStateResume:   c.evaluateSessionStateResume(),
	}

	if *jsonOut {
		return writeDoctorJSON(stdout, report)
	}

	if err := writeDoctorText(stdout, report); err != nil {
		return err
	}
	if *installMissing {
		return c.installMissing(results, doctorInstallOptions{
			DryRun:          *dryRun,
			IncludeOptional: *includeOptional,
		}, stdout, stderr)
	}

	for _, r := range results {
		if r.Required && (r.Status == doctorStatusMissing || r.Status == doctorStatusStale) {
			return fmt.Errorf("missing required dependencies; see report above")
		}
	}
	return nil
}

func (c *doctorCommand) evaluateAINotifyIntegrations() []doctorAINotifyIntegration {
	if c.aiDiagnostics == nil {
		return nil
	}
	return doctorApplyMuxTrackToAINotify(c.hostGOOS(), c.aiDiagnostics())
}

func (c *doctorCommand) evaluateSessionStateResume() []doctorSessionStateResumeDiagnostic {
	if c.resumeDiagnostics == nil {
		return nil
	}
	return c.resumeDiagnostics()
}

type doctorInstallOptions struct {
	DryRun          bool
	IncludeOptional bool
}

func (c *doctorCommand) installMissing(results []doctorResult, opts doctorInstallOptions, stdout, stderr io.Writer) error {
	commands := doctorInstallCommands(results, opts.IncludeOptional)
	manual := doctorManualInstallResults(results, opts.IncludeOptional)
	unresolved := doctorUnresolvedInstallResults(results, opts.IncludeOptional)
	if len(commands) == 0 {
		for _, r := range manual {
			if _, err := fmt.Fprintf(stdout, "manual install required for %s: %s\n", r.Name, r.Install); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(stdout, "no automatically installable missing dependencies; follow the guidance above"); err != nil {
			return err
		}
		if hasRequiredDoctorResults(unresolved) {
			return fmt.Errorf("missing required dependencies; manual install required for %s", doctorResultNames(unresolved))
		}
		return nil
	}

	if len(manual) > 0 {
		for _, r := range manual {
			if _, err := fmt.Fprintf(stdout, "manual install required for %s: %s\n", r.Name, r.Install); err != nil {
				return err
			}
		}
	}
	for _, command := range commands {
		if opts.DryRun {
			if _, err := fmt.Fprintf(stdout, "would install %s: %s\n", command.Dep, command.String()); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(stdout, ">> installing %s: %s\n", command.Dep, command.String()); err != nil {
			return err
		}
		if err := c.externalRunner()(command.Name, command.Args, stdout, stderr); err != nil {
			return fmt.Errorf("install %s via %s: %w", command.Dep, command.String(), err)
		}
	}
	if !opts.DryRun {
		if _, err := fmt.Fprintln(stdout, "install commands completed; rerun projmux doctor to verify"); err != nil {
			return err
		}
	}
	if hasRequiredDoctorResults(unresolved) {
		return fmt.Errorf("missing required dependencies; manual install required for %s", doctorResultNames(unresolved))
	}
	return nil
}

type doctorInstallCommand struct {
	Dep  string
	Name string
	Args []string
}

func (c doctorInstallCommand) String() string {
	return strings.Join(append([]string{c.Name}, c.Args...), " ")
}

func doctorInstallCommands(results []doctorResult, includeOptional bool) []doctorInstallCommand {
	var out []doctorInstallCommand
	for _, r := range results {
		installableStatus := r.Status == doctorStatusMissing || r.Status == doctorStatusStale
		if includeOptional {
			installableStatus = installableStatus || r.Status == doctorStatusHint
		}
		if !installableStatus || (!r.Required && !includeOptional) {
			continue
		}
		cmd, ok := parseDoctorInstallCommand(r.Name, r.Install)
		if ok {
			out = append(out, cmd)
		}
	}
	return out
}

func doctorManualInstallResults(results []doctorResult, includeOptional bool) []doctorResult {
	var out []doctorResult
	for _, r := range results {
		if !doctorInstallEligible(r, includeOptional) {
			continue
		}
		if _, ok := parseDoctorInstallCommand(r.Name, r.Install); !ok && strings.TrimSpace(r.Install) != "" {
			out = append(out, r)
		}
	}
	return out
}

func doctorUnresolvedInstallResults(results []doctorResult, includeOptional bool) []doctorResult {
	var out []doctorResult
	for _, r := range results {
		if !doctorInstallEligible(r, includeOptional) {
			continue
		}
		if _, ok := parseDoctorInstallCommand(r.Name, r.Install); !ok {
			out = append(out, r)
		}
	}
	return out
}

func doctorInstallEligible(r doctorResult, includeOptional bool) bool {
	installableStatus := r.Status == doctorStatusMissing || r.Status == doctorStatusStale
	if includeOptional {
		installableStatus = installableStatus || r.Status == doctorStatusHint
	}
	return installableStatus && (r.Required || includeOptional)
}

func hasRequiredDoctorResults(results []doctorResult) bool {
	for _, r := range results {
		if r.Required {
			return true
		}
	}
	return false
}

func doctorResultNames(results []doctorResult) string {
	names := make([]string, 0, len(results))
	for _, r := range results {
		names = append(names, r.Name)
	}
	return strings.Join(names, ", ")
}

func parseDoctorInstallCommand(dep, hint string) (doctorInstallCommand, bool) {
	command := strings.TrimSpace(hint)
	if command == "" {
		return doctorInstallCommand{}, false
	}
	if strings.HasPrefix(command, "manual:") {
		return doctorInstallCommand{}, false
	}
	if before, _, ok := strings.Cut(command, ";"); ok {
		command = strings.TrimSpace(before)
	}
	command = strings.TrimPrefix(command, "or:")
	command = strings.TrimSpace(command)
	if command == "" {
		return doctorInstallCommand{}, false
	}
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return doctorInstallCommand{}, false
	}
	return doctorInstallCommand{Dep: dep, Name: parts[0], Args: parts[1:]}, true
}

func (c *doctorCommand) externalRunner() func(string, []string, io.Writer, io.Writer) error {
	if c.runExternal != nil {
		return c.runExternal
	}
	return runDoctorExternal
}

func runDoctorExternal(name string, args []string, stdout, stderr io.Writer) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func (c *doctorCommand) evaluate() []doctorResult {
	host := c.hostGOOS()
	deps := doctorDepsForHost(host)
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

func doctorApplyMuxTrackToAINotify(host string, diagnostics []doctorAINotifyIntegration) []doctorAINotifyIntegration {
	if !doctorUsesPsmuxTrack(host) || len(diagnostics) == 0 {
		return diagnostics
	}
	out := make([]doctorAINotifyIntegration, 0, len(diagnostics))
	for _, diag := range diagnostics {
		if diag.ID == "tmux-bell" {
			diag = doctorTmuxBellUnsupportedDiagnostic()
		}
		out = append(out, diag)
	}
	return out
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

func writeDoctorText(w io.Writer, report doctorReport) error {
	var buf bytes.Buffer
	buf.WriteString("projmux doctor\n")
	buf.WriteString("dependency and AI notify integration diagnostics; use `projmux setup` for terminal key delivery\n")

	var ok, missing, stale, skipped, hints int
	for _, r := range report.Dependencies {
		tag := fmt.Sprintf("[%s]", r.Status)
		// Why: pad tag column to fit "[missing]" so subsequent columns line up.
		fmt.Fprintf(&buf, "  %-10s%-10s", tag, r.Name)

		switch r.Status {
		case doctorStatusOK:
			ok++
			if r.Version != "" {
				buf.WriteString(r.Version)
			}
		case doctorStatusMissing:
			missing++
			buf.WriteString("- install: ")
			if r.Install != "" {
				buf.WriteString(r.Install)
			} else {
				buf.WriteString("see https://github.com/crevissepartners/projmux for guidance")
			}
		case doctorStatusStale:
			stale++
			buf.WriteString("- ")
			if r.Hint != "" {
				buf.WriteString(r.Hint)
			}
			if r.Install != "" {
				buf.WriteString("; install: ")
				buf.WriteString(r.Install)
			}
		case doctorStatusHint:
			hints++
			buf.WriteString("- ")
			if r.Hint != "" {
				buf.WriteString(r.Hint)
			}
			if r.Install != "" {
				buf.WriteString("; install: ")
				buf.WriteString(r.Install)
			}
		case doctorStatusSkip:
			skipped++
			buf.WriteString("- ")
			if r.Hint != "" {
				buf.WriteString(r.Hint)
			}
		}
		buf.WriteString("\n")
	}

	fmt.Fprintf(&buf, "\n%d ok, %d missing, %d stale, %d skipped, %d hint.\n", ok, missing, stale, skipped, hints)
	if len(report.AINotifyIntegrations) > 0 {
		buf.WriteString("\nAI notify integrations\n")
		for _, r := range report.AINotifyIntegrations {
			tag := fmt.Sprintf("[%s]", r.Status)
			fmt.Fprintf(&buf, "  %-11s%-22s", tag, r.Name)
			if r.ProviderID != "" {
				state := "disabled"
				if r.ProviderEnabled != nil && *r.ProviderEnabled {
					state = "enabled"
				}
				fmt.Fprintf(&buf, "provider: %s (%s)", r.ProviderID, state)
			}
			if r.ConfigPath != "" {
				if r.ProviderID != "" {
					buf.WriteString("; ")
				}
				fmt.Fprintf(&buf, "config: %s", r.ConfigPath)
			}
			if r.ConflictReason != "" {
				if r.ProviderID != "" || r.ConfigPath != "" {
					buf.WriteString("; ")
				}
				buf.WriteString(r.ConflictReason)
			}
			if r.TestedVersion != "" {
				if r.ProviderID != "" || r.ConfigPath != "" || r.ConflictReason != "" {
					buf.WriteString("; ")
				}
				buf.WriteString("tested: ")
				buf.WriteString(r.TestedVersion)
			}
			if r.Guidance != "" {
				if r.ProviderID != "" || r.ConfigPath != "" || r.ConflictReason != "" || r.TestedVersion != "" {
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
	if len(report.SessionStateResume) > 0 {
		buf.WriteString("\nSession State resume metadata\n")
		for _, r := range report.SessionStateResume {
			tag := fmt.Sprintf("[%s]", r.Status)
			fmt.Fprintf(&buf, "  %-15s%-8s %s:%d.%d", tag, r.Agent, r.Session, r.WindowIndex, r.PaneIndex)
			if r.Confidence != "" {
				fmt.Fprintf(&buf, "; confidence: %s", r.Confidence)
			}
			if r.ResumeSource != "" {
				fmt.Fprintf(&buf, "; source: %s", r.ResumeSource)
			}
			if r.ResumeUpdatedAt != "" {
				fmt.Fprintf(&buf, "; updated: %s", r.ResumeUpdatedAt)
			}
			if r.Reason != "" {
				fmt.Fprintf(&buf, "; %s", r.Reason)
			}
			if r.SnapshotPath != "" {
				fmt.Fprintf(&buf, "; snapshot: %s", r.SnapshotPath)
			}
			buf.WriteString("\n")
		}
	}
	_, err := w.Write(buf.Bytes())
	return err
}

func writeDoctorJSON(w io.Writer, report doctorReport) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
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
// Most tools accept --version, but tmux uses -V and kubectl uses
// `version --client` (its --version flag is unsupported).
var versionProbeArgs = map[string][]string{
	"tmux":    {"-V"},
	"kubectl": {"version", "--client"},
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
