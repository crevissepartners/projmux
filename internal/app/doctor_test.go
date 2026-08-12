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
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
)

func newStubDoctorCommand(host string, present map[string]bool) *doctorCommand {
	return &doctorCommand{
		lookPath: func(name string) (string, error) {
			if present[name] {
				return "/usr/bin/" + name, nil
			}
			return "", errors.New("not found")
		},
		goos:   func() string { return host },
		getenv: func(string) string { return "" },
		commandVersion: func(name string) string {
			if present[name] {
				switch name {
				case "tmux":
					return "tmux 3.6"
				case "psmux":
					return "psmux 3.3.4"
				}
				return name + " 1.2.3"
			}
			return ""
		},
		aiDiagnostics: func() []doctorAINotifyIntegration { return nil },
	}
}

func TestDoctorRunAllRequiredPresentSucceeds(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("linux", map[string]bool{
		"tmux": true, "git": true, "stty": true, "kubectl": true,
	})

	var stdout, stderr bytes.Buffer
	if err := cmd.Run(nil, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v\nstderr=%s", err, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"[ok]", "tmux", "git", "stty", "kubectl"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q\nfull output:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "4 ok, 0 missing, 0 stale, 0 skipped, 0 hint.") {
		t.Fatalf("summary line wrong:\n%s", out)
	}
}

func TestDoctorRunRequiredMissingReturnsError(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("linux", map[string]bool{
		"tmux": true, "stty": true, "apt-get": true,
	})

	var stdout, stderr bytes.Buffer
	err := cmd.Run(nil, &stdout, &stderr)
	if err == nil {
		t.Fatalf("Run() error = nil, want missing required failure")
	}
	out := stdout.String()
	if !strings.Contains(out, "[missing]") || !strings.Contains(out, "git") {
		t.Fatalf("output missing expected missing line:\n%s", out)
	}
	if !strings.Contains(out, "sudo apt-get install -y git") {
		t.Fatalf("apt-get install hint not rendered:\n%s", out)
	}
}

func TestDoctorInstallMissingDryRunPrintsCommands(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("linux", map[string]bool{
		"tmux": true, "stty": true, "apt-get": true,
	})

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"--install-missing", "--dry-run"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "would install git: sudo apt-get install -y git") {
		t.Fatalf("dry-run install command missing:\n%s", out)
	}
}

func TestDoctorInstallMissingRunsCommands(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("linux", map[string]bool{
		"tmux": true, "stty": true, "apt-get": true,
	})
	var ran []string
	cmd.runExternal = func(name string, args []string, stdout, stderr io.Writer) error {
		ran = append(ran, strings.Join(append([]string{name}, args...), " "))
		return nil
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"--install-missing"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := []string{"sudo apt-get install -y git"}
	if !equalStrings(ran, want) {
		t.Fatalf("ran = %#v, want %#v", ran, want)
	}
	if !strings.Contains(stdout.String(), "install commands completed; rerun projmux doctor to verify") {
		t.Fatalf("completion hint missing:\n%s", stdout.String())
	}
}

func TestDoctorInstallMissingCanIncludeOptional(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("darwin", map[string]bool{
		"tmux": true, "git": true, "stty": true, "brew": true,
	})

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"--install-missing", "--include-optional", "--dry-run"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "would install kubectl: brew install kubectl") {
		t.Fatalf("optional install command missing:\n%s", out)
	}
}

func TestDoctorDeprecatedInstallFlagWarningCombinations(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		args []string
	}{
		{name: "install missing", args: []string{"--install-missing"}},
		{name: "dry run alone", args: []string{"--dry-run"}},
		{name: "include optional alone", args: []string{"--include-optional"}},
		{name: "install and dry run", args: []string{"--install-missing", "--dry-run"}},
		{name: "install and include optional", args: []string{"--install-missing", "--include-optional"}},
		{name: "dry run and include optional", args: []string{"--dry-run", "--include-optional"}},
		{name: "all deprecated flags", args: []string{"--install-missing", "--dry-run", "--include-optional"}},
		{name: "explicit false still uses flag", args: []string{"--install-missing=false"}},
		{name: "json conflict", args: []string{"--json", "--install-missing"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cmd := newStubDoctorCommand("linux", map[string]bool{
				"tmux": true, "git": true, "stty": true, "kubectl": true,
			})
			var stdout, stderr bytes.Buffer
			_ = cmd.Run(tc.args, &stdout, &stderr)
			if got, want := stderr.String(), doctorInstallFlagsDeprecationWarning+"\n"; got != want {
				t.Fatalf("stderr = %q, want exactly one warning %q", got, want)
			}
			if strings.Contains(stdout.String(), doctorInstallFlagsDeprecationWarning) {
				t.Fatalf("stdout contains deprecation warning:\n%s", stdout.String())
			}
		})
	}
}

func TestDoctorDeprecatedWarningPrecedesCompatibilityExecution(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("linux", map[string]bool{
		"tmux": true, "stty": true, "apt-get": true,
	})
	cmd.runExternal = func(_ string, _ []string, _, stderr io.Writer) error {
		_, err := fmt.Fprintln(stderr, "runner invoked")
		return err
	}

	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"--install-missing"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := stderr.String(), doctorInstallFlagsDeprecationWarning+"\nrunner invoked\n"; got != want {
		t.Fatalf("stderr ordering = %q, want %q", got, want)
	}
}

func TestDoctorReadOnlyBaselineFixtures(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		args    []string
		fixture string
	}{
		{name: "plain", fixture: "testdata/doctor/plain.golden"},
		{name: "json", args: []string{"--json"}, fixture: "testdata/doctor/report.golden.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cmd := newStubDoctorCommand("linux", map[string]bool{
				"tmux": true, "git": true, "stty": true, "kubectl": true,
			})
			var externalCalls int
			cmd.runExternal = func(string, []string, io.Writer, io.Writer) error {
				externalCalls++
				return nil
			}

			var stdout, stderr bytes.Buffer
			if err := cmd.Run(tc.args, &stdout, &stderr); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			want, err := os.ReadFile(tc.fixture)
			if err != nil {
				t.Fatal(err)
			}
			if got := stdout.Bytes(); !bytes.Equal(got, want) {
				t.Fatalf("stdout fixture drift\ngot:\n%s\nwant:\n%s", got, want)
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
			if externalCalls != 0 {
				t.Fatalf("external calls = %d, want zero", externalCalls)
			}
		})
	}
}

func TestDoctorDeprecatedDryRunCompatibilityStdoutFixture(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("darwin", map[string]bool{
		"tmux": true, "stty": true, "brew": true,
	})
	var externalCalls int
	cmd.runExternal = func(string, []string, io.Writer, io.Writer) error {
		externalCalls++
		return nil
	}

	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"--install-missing", "--dry-run", "--include-optional"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want, err := os.ReadFile("testdata/doctor/install-dry-run.golden")
	if err != nil {
		t.Fatal(err)
	}
	if got := stdout.Bytes(); !bytes.Equal(got, want) {
		t.Fatalf("compatibility stdout fixture drift\ngot:\n%s\nwant:\n%s", got, want)
	}
	if got, want := stderr.String(), doctorInstallFlagsDeprecationWarning+"\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
	if externalCalls != 0 {
		t.Fatalf("external calls = %d, want zero for dry-run", externalCalls)
	}
}

func TestDoctorCanonicalAndDeprecatedHelpContract(t *testing.T) {
	t.Parallel()

	var root bytes.Buffer
	printUsage(&root)
	if !strings.Contains(root.String(), "doctor    Run read-only runtime and integration diagnostics") {
		t.Fatalf("root help does not present doctor as read-only:\n%s", root.String())
	}
	if strings.Contains(root.String(), "doctor    Diagnose runtime dependencies and suggest installs") {
		t.Fatalf("root help retains install recommendation:\n%s", root.String())
	}

	cmd := newStubDoctorCommand("linux", map[string]bool{})
	var stderr bytes.Buffer
	err := cmd.Run([]string{"--help"}, io.Discard, &stderr)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("Run(--help) error = %v, want flag.ErrHelp", err)
	}
	for _, want := range []string{
		"deprecated compatibility: install missing or stale required dependencies",
		"deprecated compatibility: include optional missing dependencies with --install-missing",
		"deprecated compatibility: print install commands without running them",
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("doctor flag help missing %q:\n%s", want, stderr.String())
		}
	}
}

func TestDoctorCompatibilityExitAndPlanBaselines(t *testing.T) {
	t.Parallel()

	results := []doctorResult{
		{Name: "git", Required: true, Status: doctorStatusMissing, Install: "sudo apt-get install -y git"},
		{Name: "manual-tool", Required: true, Status: doctorStatusMissing, Install: "manual: follow vendor guidance"},
		{Name: "unresolved", Required: true, Status: doctorStatusMissing},
		{Name: "kubectl", Required: false, Status: doctorStatusHint, Install: "brew install kubectl"},
	}
	withoutOptional := buildDoctorCompatibilityInstallPlan(results, false)
	if got, want := len(withoutOptional.Commands), 1; got != want {
		t.Fatalf("required commands = %d, want %d", got, want)
	}
	if got, want := doctorResultNames(withoutOptional.Manual), "manual-tool"; got != want {
		t.Fatalf("manual results = %q, want %q", got, want)
	}
	if got, want := doctorResultNames(withoutOptional.Unresolved), "manual-tool, unresolved"; got != want {
		t.Fatalf("unresolved results = %q, want %q", got, want)
	}
	withOptional := buildDoctorCompatibilityInstallPlan(results, true)
	if got, want := len(withOptional.Commands), 2; got != want {
		t.Fatalf("commands with optional = %d, want %d", got, want)
	}

	cmd := newStubDoctorCommand("linux", map[string]bool{})
	var ran []string
	cmd.runExternal = func(name string, args []string, _, _ io.Writer) error {
		ran = append(ran, strings.Join(append([]string{name}, args...), " "))
		return nil
	}
	var stdout bytes.Buffer
	err := cmd.installMissing(results, doctorInstallOptions{}, &stdout, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "manual-tool, unresolved") {
		t.Fatalf("installMissing() error = %v, want unresolved required dependencies", err)
	}
	if got, want := ran, []string{"sudo apt-get install -y git"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("executed commands = %#v, want %#v", got, want)
	}
	if !strings.Contains(stdout.String(), "manual install required for manual-tool: manual: follow vendor guidance") {
		t.Fatalf("manual guidance missing:\n%s", stdout.String())
	}
}

func TestDoctorCurrentExitSemanticsAndNoWrite(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		args    []string
		present map[string]bool
		wantErr bool
	}{
		{name: "plain healthy", present: map[string]bool{"tmux": true, "git": true, "stty": true}},
		{name: "plain required missing", present: map[string]bool{"tmux": true, "stty": true}, wantErr: true},
		{name: "json healthy", args: []string{"--json"}, present: map[string]bool{"tmux": true, "git": true, "stty": true}},
		{name: "json required missing preserves successful exit", args: []string{"--json"}, present: map[string]bool{"tmux": true, "stty": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cmd := newStubDoctorCommand("linux", tc.present)
			var externalCalls int
			cmd.runExternal = func(string, []string, io.Writer, io.Writer) error {
				externalCalls++
				return nil
			}
			err := cmd.Run(tc.args, io.Discard, io.Discard)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Run() error = %v, wantErr %v", err, tc.wantErr)
			}
			if externalCalls != 0 {
				t.Fatalf("external calls = %d, want zero", externalCalls)
			}
		})
	}
}

func TestDoctorInstallFlagsRejectInvalidCombinations(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("linux", map[string]bool{})
	cases := []struct {
		args []string
		want string
	}{
		{args: []string{"--json", "--install-missing"}, want: "--json cannot be combined"},
		{args: []string{"--dry-run"}, want: "require --install-missing"},
		{args: []string{"--include-optional"}, want: "require --install-missing"},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			t.Parallel()
			err := cmd.Run(tc.args, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil {
				t.Fatalf("Run() error = nil, want %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Run() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestDoctorRunRejectsPositionalArguments(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("linux", map[string]bool{})
	err := cmd.Run([]string{"extra"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("Run() error = nil, want positional argument rejection")
	}
	if !strings.Contains(err.Error(), "positional") {
		t.Fatalf("Run() error = %v, want mention of positional arguments", err)
	}
}

func TestDoctorEvaluateOptionalMissingIsHintNotError(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("linux", map[string]bool{
		"tmux": true, "git": true, "stty": true,
	})

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "[hint]") || !strings.Contains(out, "kubectl") {
		t.Fatalf("output missing kubectl hint line:\n%s", out)
	}
	if !strings.Contains(out, "optional; install if you use the kubectl switcher") {
		t.Fatalf("hint note not rendered:\n%s", out)
	}
	if !strings.Contains(out, "3 ok, 0 missing, 0 stale, 0 skipped, 1 hint.") {
		t.Fatalf("summary line wrong:\n%s", out)
	}
}

func TestDoctorEvaluateSkipsSttyOnWindows(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("windows", map[string]bool{
		"psmux": true, "git": true, "kubectl": true,
	})

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "[skip]") || !strings.Contains(out, "stty") {
		t.Fatalf("stty should be skipped on windows:\n%s", out)
	}
	if !strings.Contains(out, "windows host") {
		t.Fatalf("skip reason missing:\n%s", out)
	}
}

func TestDoctorWindowsPsmuxTrackDoesNotRequireTmux(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("windows", map[string]bool{
		"psmux": true, "git": true, "kubectl": true,
	})

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v\noutput=%s", err, stdout.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "[ok]      psmux") {
		t.Fatalf("windows native core dependency should be psmux:\n%s", out)
	}
	if strings.Contains(out, "[missing] tmux") || strings.Contains(out, "[stale]   tmux") || strings.Contains(out, "[ok]      tmux") {
		t.Fatalf("windows native psmux track should not report tmux dependency:\n%s", out)
	}
	if !strings.Contains(out, "[skip]    stty") {
		t.Fatalf("stty should remain skipped on windows:\n%s", out)
	}

	stdout.Reset()
	if err := cmd.Run([]string{"--json"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run(--json) error = %v\noutput=%s", err, stdout.String())
	}
	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("json.Unmarshal error = %v\noutput=%s", err, stdout.String())
	}
	byName := doctorResultsByName(report.Dependencies)
	if _, ok := byName["tmux"]; ok {
		t.Fatalf("windows native dependencies include tmux: %#v", report.Dependencies)
	}
	psmux, ok := byName["psmux"]
	if !ok {
		t.Fatalf("windows native dependencies missing psmux: %#v", report.Dependencies)
	}
	if !psmux.Required || psmux.Status != doctorStatusOK {
		t.Fatalf("psmux dependency = %#v, want required ok", psmux)
	}
	stty, ok := byName["stty"]
	if !ok || stty.Status != doctorStatusSkip {
		t.Fatalf("stty dependency = %#v, want skipped", stty)
	}
}

func TestDoctorWindowsPsmuxTrackChecksPsmuxDependency(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("windows", map[string]bool{
		"git": true, "kubectl": true,
	})

	var stdout bytes.Buffer
	err := cmd.Run(nil, &stdout, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("Run() error = nil, want missing psmux failure")
	}
	out := stdout.String()
	if !strings.Contains(out, "[missing] psmux") || !strings.Contains(out, "scoop install psmux") {
		t.Fatalf("missing psmux dependency should be reported with install hint:\n%s", out)
	}
	if strings.Contains(out, "[missing] tmux") || strings.Contains(out, "[stale]   tmux") {
		t.Fatalf("windows native psmux track should not require tmux:\n%s", out)
	}
}

func TestDoctorWindowsPsmuxTrackIgnoresMissingOrStaleTmux(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		present  map[string]bool
		versions map[string]string
	}{
		{
			name:    "tmux missing",
			present: map[string]bool{"psmux": true, "git": true, "kubectl": true},
		},
		{
			name:     "tmux stale",
			present:  map[string]bool{"psmux": true, "tmux": true, "git": true, "kubectl": true},
			versions: map[string]string{"tmux": "tmux 3.2", "psmux": "psmux 3.3.4"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cmd := newStubDoctorCommandWithVersions("windows", tc.present, tc.versions)
			var stdout bytes.Buffer
			if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
				t.Fatalf("Run() error = %v\noutput=%s", err, stdout.String())
			}
			out := stdout.String()
			if strings.Contains(out, "[missing] tmux") || strings.Contains(out, "[stale]   tmux") || strings.Contains(out, "minimum 3.4; found tmux 3.2") {
				t.Fatalf("windows native psmux track should ignore tmux dependency health:\n%s", out)
			}
		})
	}
}

func TestDoctorLinuxTmuxCoreDependencyAndStaleCheckRemainActive(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommandWithVersions(
		"linux",
		map[string]bool{"tmux": true, "psmux": true, "git": true, "stty": true, "kubectl": true},
		map[string]string{"tmux": "tmux 3.2", "psmux": "psmux 3.3.4"},
	)

	var stdout bytes.Buffer
	err := cmd.Run(nil, &stdout, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("Run() error = nil, want stale tmux failure")
	}
	out := stdout.String()
	for _, want := range []string{"[stale]   tmux", "minimum 3.4; found tmux 3.2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("linux tmux output missing %q:\n%s", want, out)
		}
	}

	stdout.Reset()
	_ = cmd.Run([]string{"--json"}, &stdout, &bytes.Buffer{})
	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("json.Unmarshal error = %v\noutput=%s", err, stdout.String())
	}
	byName := doctorResultsByName(report.Dependencies)
	tmux, ok := byName["tmux"]
	if !ok {
		t.Fatalf("linux dependencies missing tmux: %#v", report.Dependencies)
	}
	if !tmux.Required || tmux.Status != doctorStatusStale {
		t.Fatalf("tmux dependency = %#v, want required stale", tmux)
	}
	if _, ok := byName["psmux"]; ok {
		t.Fatalf("linux dependencies should not include psmux: %#v", report.Dependencies)
	}
}

func TestDoctorRunJSONOutputIsValid(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("linux", map[string]bool{
		"tmux": true, "git": true, "stty": true,
	})

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"--json"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("json.Unmarshal error = %v\noutput=%s", err, stdout.String())
	}
	if len(report.Dependencies) != 4 {
		t.Fatalf("len(report.Dependencies) = %d, want 4", len(report.Dependencies))
	}
	byName := map[string]doctorResult{}
	for _, r := range report.Dependencies {
		byName[r.Name] = r
	}
	if byName["tmux"].Status != doctorStatusOK {
		t.Fatalf("tmux status = %q, want ok", byName["tmux"].Status)
	}
	if byName["kubectl"].Status != doctorStatusHint {
		t.Fatalf("kubectl status = %q, want hint", byName["kubectl"].Status)
	}
	if !byName["tmux"].Required {
		t.Fatalf("tmux Required = false, want true")
	}
	if byName["kubectl"].Required {
		t.Fatalf("kubectl Required = true, want false")
	}
	if report.SessionStatePrune != doctorSessionStatePruneGuidance {
		t.Fatalf("session-state prune guidance = %q, want %q", report.SessionStatePrune, doctorSessionStatePruneGuidance)
	}
}

func TestDoctorRunIncludesManualSessionStatePruneGuidance(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("linux", map[string]bool{
		"tmux": true, "git": true, "stty": true,
	})
	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, want := range []string{
		"Session State retention",
		"projmux prune session-state",
		"delete only by explicit name",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("doctor output missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestDoctorRunIncludesAINotifyDiagnostics(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("linux", map[string]bool{
		"tmux": true, "git": true, "stty": true,
	})
	cmd.aiDiagnostics = func() []doctorAINotifyIntegration {
		return []doctorAINotifyIntegration{
			{
				ID:             "codex-hooks",
				Name:           "Codex hooks",
				Status:         doctorAINotifyStatusConflict,
				ConfigPath:     "/home/tester/.codex/config.toml",
				ConflictReason: "Codex hooks are already configured outside a projmux-managed block",
				InstallCommand: "projmux ai integrate codex",
				RemoveCommand:  "projmux ai integrate codex --remove",
				DryRunCommand:  "projmux ai integrate codex --dry-run",
			},
		}
	}

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	out := stdout.String()
	for _, want := range []string{
		"AI notify integrations",
		"[conflict]",
		"Codex hooks",
		"/home/tester/.codex/config.toml",
		"install: projmux ai integrate codex",
		"remove: projmux ai integrate codex --remove",
		"dry-run: projmux ai integrate codex --dry-run",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q\nfull output:\n%s", want, out)
		}
	}
}

func TestDoctorJSONIncludesAINotifyDiagnostics(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("linux", map[string]bool{
		"tmux": true, "git": true, "stty": true,
	})
	cmd.aiDiagnostics = func() []doctorAINotifyIntegration {
		return []doctorAINotifyIntegration{
			{
				ID:             "tmux-bell",
				Name:           "tmux bell fallback",
				Status:         doctorAINotifyStatusMissing,
				InstallCommand: "projmux ai integrate tmux-bell",
				RemoveCommand:  "projmux ai integrate tmux-bell --remove",
				DryRunCommand:  "projmux ai integrate tmux-bell --dry-run",
			},
		}
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"--json"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("json.Unmarshal error = %v\noutput=%s", err, stdout.String())
	}
	if len(report.Dependencies) != 4 {
		t.Fatalf("len(report.Dependencies) = %d, want 4", len(report.Dependencies))
	}
	if len(report.AINotifyIntegrations) != 1 {
		t.Fatalf("len(report.AINotifyIntegrations) = %d, want 1", len(report.AINotifyIntegrations))
	}
	got := report.AINotifyIntegrations[0]
	if got.ID != "tmux-bell" || got.Status != doctorAINotifyStatusMissing {
		t.Fatalf("AI diagnostic = %#v, want tmux-bell missing", got)
	}
	if got.DryRunCommand != "projmux ai integrate tmux-bell --dry-run" {
		t.Fatalf("DryRunCommand = %q", got.DryRunCommand)
	}
}

func TestDoctorWindowsPsmuxTrackSkipsTmuxBellFallback(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("windows", map[string]bool{
		"psmux": true, "git": true, "kubectl": true,
	})
	cmd.aiDiagnostics = func() []doctorAINotifyIntegration {
		return []doctorAINotifyIntegration{
			{
				ID:             "tmux-bell",
				Name:           "tmux bell fallback",
				Status:         doctorAINotifyStatusMissing,
				InstallCommand: "projmux ai integrate tmux-bell",
				RemoveCommand:  "projmux ai integrate tmux-bell --remove",
				DryRunCommand:  "projmux ai integrate tmux-bell --dry-run",
			},
		}
	}

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v\noutput=%s", err, stdout.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"AI notify integrations",
		"[skip]",
		"tmux bell fallback",
		"unsupported on the native Windows psmux track",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q\nfull output:\n%s", want, out)
		}
	}
	if strings.Contains(out, "install: projmux ai integrate tmux-bell") {
		t.Fatalf("unsupported tmux bell fallback should not render install command:\n%s", out)
	}

	stdout.Reset()
	if err := cmd.Run([]string{"--json"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run(--json) error = %v\noutput=%s", err, stdout.String())
	}
	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("json.Unmarshal error = %v\noutput=%s", err, stdout.String())
	}
	if len(report.AINotifyIntegrations) != 1 {
		t.Fatalf("len(report.AINotifyIntegrations) = %d, want 1", len(report.AINotifyIntegrations))
	}
	got := report.AINotifyIntegrations[0]
	if got.ID != "tmux-bell" || got.Status != doctorAINotifyStatusSkip {
		t.Fatalf("AI diagnostic = %#v, want tmux-bell skip", got)
	}
	if got.InstallCommand != "" || got.RemoveCommand != "" || got.DryRunCommand != "" {
		t.Fatalf("unsupported tmux bell commands = %#v, want no commands", got)
	}
}

func TestDoctorLinuxTmuxBellFallbackRemainsMissingWhenNotInstalled(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("linux", map[string]bool{
		"tmux": true, "git": true, "stty": true,
	})
	cmd.aiDiagnostics = func() []doctorAINotifyIntegration {
		return []doctorAINotifyIntegration{
			{
				ID:             "tmux-bell",
				Name:           "tmux bell fallback",
				Status:         doctorAINotifyStatusMissing,
				InstallCommand: "projmux ai integrate tmux-bell",
				RemoveCommand:  "projmux ai integrate tmux-bell --remove",
				DryRunCommand:  "projmux ai integrate tmux-bell --dry-run",
			},
		}
	}

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v\noutput=%s", err, stdout.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"[missing]  tmux bell fallback",
		"install: projmux ai integrate tmux-bell",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q\nfull output:\n%s", want, out)
		}
	}
	if strings.Contains(out, "unsupported on the native Windows psmux track") {
		t.Fatalf("linux tmux bell fallback should not be rewritten as unsupported:\n%s", out)
	}
}

func doctorResultsByName(results []doctorResult) map[string]doctorResult {
	byName := make(map[string]doctorResult, len(results))
	for _, result := range results {
		byName[result.Name] = result
	}
	return byName
}

func TestDoctorReportsSessionStateResumeDiagnostics(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("linux", map[string]bool{
		"tmux": true, "git": true, "stty": true,
	})
	cmd.resumeDiagnostics = func() []doctorSessionStateResumeDiagnostic {
		return []doctorSessionStateResumeDiagnostic{
			{
				Session:         "workspace",
				WindowIndex:     0,
				PaneIndex:       1,
				Agent:           "codex",
				Status:          "stale",
				Confidence:      "medium",
				ResumeSource:    "codex-log",
				ResumeUpdatedAt: "2026-05-12T03:04:05Z",
				Reason:          "resume metadata older than 24h0m0s",
				SnapshotPath:    "/tmp/workspace.json",
			},
			{
				Session:         "workspace",
				WindowIndex:     0,
				PaneIndex:       2,
				Agent:           "antigravity",
				Status:          "available",
				Confidence:      "high",
				ResumeSource:    "hook",
				ResumeUpdatedAt: "2026-06-04T03:04:05Z",
				SnapshotPath:    "/tmp/workspace.json",
			},
		}
	}

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	out := stdout.String()
	for _, want := range []string{
		"Session State resume metadata",
		"[stale]",
		"codex",
		"workspace:0.1",
		"confidence: medium",
		"source: codex-log",
		"resume metadata older than 24h0m0s",
		"antigravity",
		"workspace:0.2",
		"confidence: high",
		"source: hook",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, out)
		}
	}

	stdout.Reset()
	if err := cmd.Run([]string{"--json"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run(--json) error = %v", err)
	}
	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("json unmarshal error = %v\n%s", err, stdout.String())
	}
	if len(report.SessionStateResume) != 2 || report.SessionStateResume[0].Status != "stale" || report.SessionStateResume[1].Agent != "antigravity" || report.SessionStateResume[1].Status != "available" {
		t.Fatalf("SessionStateResume = %#v, want codex stale and antigravity available diagnostics", report.SessionStateResume)
	}
}

func TestDoctorAINotifyDiagnosticsReuseReadOnlyPlans(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	writeCodexTestFile(t, filepath.Join(home, codexConfigRelativePath), `[features]
hooks = true

[[hooks.Stop]]
matcher = "*"
[[hooks.Stop.hooks]]
type = "command"
command = "projmux ai ingest codex-hook"
`)
	writeCodexTestFile(t, filepath.Join(home, claudeSettingsRelativePath), `{
  "hooks": {
    "Notification": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "projmux ai ingest claude-hook"
          }
        ]
      }
    ]
  }
}
`)
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && reflect.DeepEqual(args, []string{"show-hooks", "-g", tmuxBellHookName}) {
			return []byte("alert-bell[1] " + tmuxBellHookCommand + "\n"), nil
		}
		return nil, os.ErrNotExist
	}

	diagnostics := doctorAINotifyDiagnostics(cmd)
	byID := map[string]doctorAINotifyIntegration{}
	for _, diagnostic := range diagnostics {
		byID[diagnostic.ID] = diagnostic
	}

	if byID["codex-hooks"].Status != doctorAINotifyStatusConflict {
		t.Fatalf("codex hooks status = %#v, want conflict", byID["codex-hooks"])
	}
	if byID["claude-hooks"].Status != doctorAINotifyStatusConflict {
		t.Fatalf("claude hooks status = %#v, want conflict", byID["claude-hooks"])
	}
	if byID["tmux-bell"].Status != doctorAINotifyStatusInstalled {
		t.Fatalf("tmux bell status = %#v, want installed", byID["tmux-bell"])
	}
	if byID["antigravity-hooks"].Status != doctorAINotifyStatusMissing {
		t.Fatalf("antigravity hooks status = %#v, want missing managed diagnostic", byID["antigravity-hooks"])
	}
	if byID["codex-hooks"].InstallCommand != "projmux ai integrate codex" {
		t.Fatalf("codex hooks InstallCommand = %q", byID["codex-hooks"].InstallCommand)
	}
	if byID["codex-hooks"].DryRunCommand != "projmux ai integrate codex --dry-run" {
		t.Fatalf("codex hooks DryRunCommand = %q", byID["codex-hooks"].DryRunCommand)
	}
	if !strings.Contains(byID["codex-hooks"].Guidance, "/hooks") {
		t.Fatalf("codex hooks Guidance = %q, want /hooks review notice", byID["codex-hooks"].Guidance)
	}
	if byID["codex-hooks"].TestedVersion != "codex-cli 0.130.0" {
		t.Fatalf("codex hooks TestedVersion = %q", byID["codex-hooks"].TestedVersion)
	}
	if byID["claude-hooks"].TestedVersion != "Claude Code 2.1.140" {
		t.Fatalf("claude hooks TestedVersion = %q", byID["claude-hooks"].TestedVersion)
	}
	if byID["antigravity-hooks"].ProviderID != "antigravity" || byID["antigravity-hooks"].InstallCommand != "projmux ai integrate antigravity" || byID["antigravity-hooks"].RemoveCommand != "projmux ai integrate antigravity --remove" || byID["antigravity-hooks"].DryRunCommand != "projmux ai integrate antigravity --dry-run" {
		t.Fatalf("antigravity hooks diagnostic = %#v", byID["antigravity-hooks"])
	}
	if !strings.Contains(byID["antigravity-hooks"].Guidance, "install source of truth") || !strings.Contains(byID["antigravity-hooks"].Guidance, "/hooks") || !strings.Contains(byID["antigravity-hooks"].Guidance, "PreToolUse") {
		t.Fatalf("antigravity hooks Guidance = %q, want source-of-truth/read-only/permission notice", byID["antigravity-hooks"].Guidance)
	}
	if len(cmdRecorder(cmd).commands) != 0 {
		t.Fatalf("commands = %#v, want read-only diagnostics", cmdRecorder(cmd).commands)
	}
}

func TestDoctorAINotifyDiagnosticsProviderMetadataShowsDisabledProviders(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	paths := config.DefaultPaths(configHome, t.TempDir())
	if err := config.SaveAIEnabledAgentsFile(paths.AIEnabledAgentsFile(), nil); err != nil {
		t.Fatalf("SaveAIEnabledAgentsFile() error = %v", err)
	}

	cmd := testAICommand(t.TempDir())
	cmd.readFile = os.ReadFile
	diagnostics := doctorAINotifyDiagnostics(cmd)
	byID := map[string]doctorAINotifyIntegration{}
	for _, diagnostic := range diagnostics {
		byID[diagnostic.ID] = diagnostic
	}

	for _, tc := range []struct {
		id       string
		provider string
	}{
		{id: "codex-hooks", provider: "codex"},
		{id: "claude-hooks", provider: "claude"},
		{id: "antigravity-hooks", provider: "antigravity"},
	} {
		diagnostic, ok := byID[tc.id]
		if !ok {
			t.Fatalf("diagnostics = %#v, want %s even when provider is disabled", diagnostics, tc.id)
		}
		if diagnostic.ProviderID != tc.provider {
			t.Fatalf("%s ProviderID = %q, want %q", tc.id, diagnostic.ProviderID, tc.provider)
		}
		if diagnostic.ProviderEnabled == nil || *diagnostic.ProviderEnabled {
			t.Fatalf("%s ProviderEnabled = %#v, want disabled false", tc.id, diagnostic.ProviderEnabled)
		}
		if !strings.Contains(diagnostic.Guidance, "provider disabled") || !strings.Contains(diagnostic.Guidance, "explicit diagnostics") {
			t.Fatalf("%s Guidance = %q, want disabled-provider diagnostic policy", tc.id, diagnostic.Guidance)
		}
	}
}

func TestDoctorAntigravityIntegrationDiagnosticManagedStates(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile

	missing := doctorAntigravityIntegrationDiagnostic(cmd)
	if missing.Status != doctorAINotifyStatusMissing || missing.ConfigPath != filepath.Join(home, antigravityHooksRelativePath) {
		t.Fatalf("missing diagnostic = %#v", missing)
	}
	for _, want := range []string{"projmux ai integrate antigravity", "projmux ai integrate antigravity --remove", "projmux ai integrate antigravity --dry-run"} {
		if missing.InstallCommand != want && missing.RemoveCommand != want && missing.DryRunCommand != want {
			t.Fatalf("missing diagnostic = %#v, want command %q", missing, want)
		}
	}

	if err := cmd.Run([]string{"integrate", "antigravity"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	installed := doctorAntigravityIntegrationDiagnostic(cmd)
	if installed.Status != doctorAINotifyStatusInstalled || installed.ConflictReason != "" {
		t.Fatalf("installed diagnostic = %#v", installed)
	}

	path := filepath.Join(home, antigravityHooksRelativePath)
	installedData := readCodexTestFile(t, path)
	writeCodexTestFile(t, path, strings.ReplaceAll(installedData, "/tmp/projmux", "/old/projmux"))
	stale := doctorAntigravityIntegrationDiagnostic(cmd)
	if stale.Status != doctorAINotifyStatusStale || !strings.Contains(stale.ConflictReason, "absolute executable") || !strings.Contains(stale.ConflictReason, "run the install command") {
		t.Fatalf("stale diagnostic = %#v", stale)
	}

	writeCodexTestFile(t, path, `{"projmux":{"Stop":[{"command":"echo unmanaged"}]}}`)
	conflict := doctorAntigravityIntegrationDiagnostic(cmd)
	if conflict.Status != doctorAINotifyStatusConflict || !strings.Contains(conflict.ConflictReason, "unmanaged named entry") {
		t.Fatalf("conflict diagnostic = %#v", conflict)
	}
}

func TestDetectInstallHintByOSAndPM(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		dep      doctorDep
		host     string
		present  map[string]bool
		want     string
		contains []string
	}{
		{
			name:    "linux apt-get",
			dep:     doctorDep{Name: "git"},
			host:    "linux",
			present: map[string]bool{"apt-get": true},
			want:    "sudo apt-get install -y git",
		},
		{
			name:    "linux pacman",
			dep:     doctorDep{Name: "git"},
			host:    "linux",
			present: map[string]bool{"pacman": true},
			want:    "sudo pacman -S git",
		},
		{
			name:    "linux dnf",
			dep:     doctorDep{Name: "git"},
			host:    "linux",
			present: map[string]bool{"dnf": true},
			want:    "sudo dnf install git",
		},
		{
			name:    "linux zypper",
			dep:     doctorDep{Name: "git"},
			host:    "linux",
			present: map[string]bool{"zypper": true},
			want:    "sudo zypper install git",
		},
		{
			name:    "linux apk",
			dep:     doctorDep{Name: "git"},
			host:    "linux",
			present: map[string]bool{"apk": true},
			want:    "sudo apk add git",
		},
		{
			name:    "darwin brew",
			dep:     doctorDep{Name: "git"},
			host:    "darwin",
			present: map[string]bool{"brew": true},
			want:    "brew install git",
		},
		{
			name:    "windows scoop default",
			dep:     doctorDep{Name: "git"},
			host:    "windows",
			present: map[string]bool{},
			want:    "scoop install git",
		},
		{
			name:    "linux no package manager returns empty",
			dep:     doctorDep{Name: "git"},
			host:    "linux",
			present: map[string]bool{},
			want:    "",
		},
		{
			name:    "darwin without brew returns empty",
			dep:     doctorDep{Name: "git"},
			host:    "darwin",
			present: map[string]bool{},
			want:    "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lookPath := func(name string) (string, error) {
				if tc.present[name] {
					return "/usr/bin/" + name, nil
				}
				return "", errors.New("not found")
			}
			got := detectInstallHint(tc.dep, tc.host, lookPath)
			if len(tc.contains) > 0 {
				for _, want := range tc.contains {
					if !strings.Contains(got, want) {
						t.Fatalf("detectInstallHint = %q, want substring %q", got, want)
					}
				}
				return
			}
			if got != tc.want {
				t.Fatalf("detectInstallHint = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDoctorRunWindowsMissingHintIncludesScoop(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("windows", map[string]bool{
		"tmux": true, "kubectl": true,
	})

	var stdout bytes.Buffer
	err := cmd.Run(nil, &stdout, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("Run() error = nil, want missing required (git) on windows")
	}
	if !strings.Contains(stdout.String(), "scoop install git") {
		t.Fatalf("windows install hint missing scoop:\n%s", stdout.String())
	}
}

func TestDoctorRunDarwinMissingHintIncludesBrew(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("darwin", map[string]bool{
		"tmux": true, "stty": true, "brew": true,
	})

	var stdout bytes.Buffer
	err := cmd.Run(nil, &stdout, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("Run() error = nil, want missing required (git) on darwin")
	}
	if !strings.Contains(stdout.String(), "brew install git") {
		t.Fatalf("darwin install hint missing brew:\n%s", stdout.String())
	}
}

func TestDoctorRunLinuxPacmanMissingHint(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("linux", map[string]bool{
		"tmux": true, "stty": true, "pacman": true,
	})

	var stdout bytes.Buffer
	err := cmd.Run(nil, &stdout, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("Run() error = nil, want missing required (git)")
	}
	if !strings.Contains(stdout.String(), "sudo pacman -S git") {
		t.Fatalf("pacman install hint not rendered:\n%s", stdout.String())
	}
}

func newStubDoctorCommandWithVersions(host string, present map[string]bool, versions map[string]string) *doctorCommand {
	return &doctorCommand{
		lookPath: func(name string) (string, error) {
			if present[name] {
				return "/usr/bin/" + name, nil
			}
			return "", errors.New("not found")
		},
		goos:   func() string { return host },
		getenv: func(string) string { return "" },
		commandVersion: func(name string) string {
			if v, ok := versions[name]; ok {
				return v
			}
			if !present[name] {
				return ""
			}
			switch name {
			case "tmux":
				return "tmux 3.6"
			case "psmux":
				return "psmux 3.3.4"
			}
			return name + " 1.2.3"
		},
		aiDiagnostics: func() []doctorAINotifyIntegration { return nil },
	}
}

func TestParseDoctorVersion(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		in    string
		major int
		minor int
		patch int
		ok    bool
	}{
		{"tmux 3.6", "tmux 3.6", 3, 6, 0, true},
		{"tmux 3.4a", "tmux 3.4a", 3, 4, 0, true},
		{"plain 3.4", "3.4", 3, 4, 0, true},
		{"semver with suffix", "0.71.0 (62899fd7)", 0, 71, 0, true},
		{"minor with suffix", "0.54 (devel)", 0, 54, 0, true},
		{"git long", "git version 2.53.0", 2, 53, 0, true},
		{"empty", "", 0, 0, 0, false},
		{"unrecognized", "unrecognized", 0, 0, 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			major, minor, patch, ok := parseDoctorVersion(tc.in)
			if major != tc.major || minor != tc.minor || patch != tc.patch || ok != tc.ok {
				t.Fatalf("parseDoctorVersion(%q) = (%d, %d, %d, %v), want (%d, %d, %d, %v)",
					tc.in, major, minor, patch, ok, tc.major, tc.minor, tc.patch, tc.ok)
			}
		})
	}
}

func TestVersionAtLeast(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		got     string
		want    string
		atLeast bool
		parsed  bool
	}{
		{"3.6 >= 3.4", "3.6", "3.4", true, true},
		{"3.4 >= 3.4", "3.4", "3.4", true, true},
		{"3.3 < 3.4", "3.3", "3.4", false, true},
		{"3.4a >= 3.4", "3.4a", "3.4", true, true},
		{"0.65.0 >= 0.65.0", "0.65.0", "0.65.0", true, true},
		{"0.64.0 < 0.65.0", "0.64.0", "0.65.0", false, true},
		{"0.71.0 >= 0.65.0", "0.71.0", "0.65.0", true, true},
		{"garbage lenient", "garbage", "0.65.0", true, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			atLeast, parsed := versionAtLeast(tc.got, tc.want)
			if atLeast != tc.atLeast || parsed != tc.parsed {
				t.Fatalf("versionAtLeast(%q, %q) = (%v, %v), want (%v, %v)",
					tc.got, tc.want, atLeast, parsed, tc.atLeast, tc.parsed)
			}
		})
	}
}

func TestDoctorStaleTmuxFailsRequired(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommandWithVersions(
		"linux",
		map[string]bool{"tmux": true, "git": true, "stty": true, "kubectl": true, "apt-get": true},
		map[string]string{"tmux": "tmux 3.2"},
	)

	var stdout, stderr bytes.Buffer
	err := cmd.Run(nil, &stdout, &stderr)
	if err == nil {
		t.Fatalf("Run() error = nil, want stale-required failure")
	}
	if !strings.Contains(err.Error(), "missing required dependencies") {
		t.Fatalf("error = %v, want mention of missing required dependencies", err)
	}
	out := stdout.String()
	for _, want := range []string{"[stale]", "tmux", "minimum 3.4; found tmux 3.2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q\nfull output:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "1 stale") {
		t.Fatalf("summary line missing stale count:\n%s", out)
	}
}

func TestDoctorNoMinVersionSkipsCheck(t *testing.T) {
	t.Parallel()

	// Confirms that when MinVersion == "" (e.g. git, stty, kubectl), no
	// version comparison happens even if the version output is empty.
	cmd := newStubDoctorCommandWithVersions(
		"linux",
		map[string]bool{"tmux": true, "git": true, "stty": true, "kubectl": true},
		map[string]string{"git": "", "stty": "", "kubectl": ""},
	)

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v\noutput=%s", err, stdout.String())
	}
	out := stdout.String()
	if strings.Contains(out, "[stale]") {
		t.Fatalf("output should not flag any dep as stale:\n%s", out)
	}
	if !strings.Contains(out, "4 ok, 0 missing, 0 stale, 0 skipped, 0 hint.") {
		t.Fatalf("summary line wrong:\n%s", out)
	}
}

func TestDoctorVersionParseFailureDoesNotMarkStale(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommandWithVersions(
		"linux",
		map[string]bool{"tmux": true, "git": true, "stty": true, "kubectl": true},
		map[string]string{"tmux": ""},
	)

	results := cmd.evaluate()
	var tmux doctorResult
	for _, r := range results {
		if r.Name == "tmux" {
			tmux = r
			break
		}
	}
	if tmux.Name == "" {
		t.Fatalf("tmux result missing")
	}
	if tmux.Status != doctorStatusOK {
		t.Fatalf("tmux status = %q, want ok (parse glitch should not mark stale)", tmux.Status)
	}
}

func TestDoctorStaleTmuxSerializesToJSON(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommandWithVersions(
		"linux",
		map[string]bool{"tmux": true, "git": true, "stty": true, "kubectl": true, "apt-get": true},
		map[string]string{"tmux": "tmux 3.2"},
	)

	var stdout bytes.Buffer
	// Run returns the stale-required error but JSON output is still written
	// to stdout before the error return path checks status.
	_ = cmd.Run([]string{"--json"}, &stdout, &bytes.Buffer{})

	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("json.Unmarshal error = %v\noutput=%s", err, stdout.String())
	}
	byName := map[string]doctorResult{}
	for _, r := range report.Dependencies {
		byName[r.Name] = r
	}
	tmux, ok := byName["tmux"]
	if !ok {
		t.Fatalf("tmux result missing from JSON output")
	}
	if tmux.Status != doctorStatusStale {
		t.Fatalf("tmux JSON status = %q, want stale", tmux.Status)
	}
	if tmux.Hint == "" {
		t.Fatalf("tmux JSON hint unset, want minimum/found message")
	}
	if !strings.Contains(string(stdout.Bytes()), `"status": "stale"`) {
		t.Fatalf("raw JSON missing %q:\n%s", `"status": "stale"`, stdout.String())
	}
}
