package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
)

func newStubDoctorCommand(host string, present map[string]bool) *doctorCommand {
	c := &doctorCommand{
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
				if name == "tmux" {
					return "tmux 3.6"
				}
				return name + " 1.2.3"
			}
			return ""
		},
		aiDiagnostics: func() []doctorAINotifyIntegration { return nil },
	}
	c.resolveOperationsPath = func() (string, error) {
		return filepath.Join("/projmux-doctor-missing-fixture", "state", "projmux", "logs", diagnostics.LogFileName), nil
	}
	c.readRuntimeHealth = diagnostics.ReadRuntimeHealth
	c.runtimeProbe = func() doctorRuntimeProbe { return doctorRuntimeProbe{SocketState: diagnostics.RuntimeUnreachable} }
	c.resolveGeneratedConfig = func() (string, error) {
		return filepath.Join("/projmux-doctor-missing-fixture", "config", "projmux", "tmux.conf"), nil
	}
	c.readGeneratedConfig = doctorReadRegularFileBounded
	return c
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
				"tmux": true, "git": true, "stty": true,
			})
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
		})
	}
}

func TestDoctorSectionJSONProjectsOneTypedInventory(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("linux", map[string]bool{"tmux": true, "stty": true, "apt-get": true})
	cmd.aiDiagnostics = func() []doctorAINotifyIntegration {
		return []doctorAINotifyIntegration{{ID: "codex-hooks", Name: "Codex hooks", Status: doctorAINotifyStatusMissing}}
	}
	cmd.resumeDiagnostics = func() []doctorSessionStateResumeDiagnostic {
		return []doctorSessionStateResumeDiagnostic{{Session: "work", Agent: "codex", Status: "stale"}}
	}

	cases := []struct {
		section string
		field   string
	}{
		{section: "deps", field: "dependencies"},
		{section: "integrations", field: "ai_notify_integrations"},
		{section: "session-state", field: "session_state_resume"},
		{section: "runtime", field: "runtime"},
		{section: "logs", field: "logs"},
	}
	for _, tc := range cases {
		t.Run(tc.section, func(t *testing.T) {
			var stdout bytes.Buffer
			if err := cmd.Run([]string{"--json", "--section", tc.section}, &stdout, io.Discard); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			var root map[string]json.RawMessage
			if err := json.Unmarshal(stdout.Bytes(), &root); err != nil {
				t.Fatal(err)
			}
			if got := string(root["schema_version"]); got != "2" {
				t.Fatalf("schema_version = %s, want 2", got)
			}
			if _, ok := root[tc.field]; !ok {
				t.Fatalf("section JSON = %s, want %q", stdout.String(), tc.field)
			}
			for _, forbidden := range []string{"dependencies", "ai_notify_integrations", "session_state_resume", "session_state_prune", "runtime", "logs"} {
				if forbidden != tc.field && !(tc.section == "session-state" && forbidden == "session_state_prune") {
					if _, ok := root[forbidden]; ok {
						t.Fatalf("section %s leaked %q: %s", tc.section, forbidden, stdout.String())
					}
				}
			}
		})
	}
}

func TestDoctorVerboseOwnsTextDetailButDoesNotChangeJSON(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("linux", map[string]bool{"tmux": true, "git": true, "stty": true})
	cmd.aiDiagnostics = func() []doctorAINotifyIntegration {
		return []doctorAINotifyIntegration{{ID: "codex-hooks", Name: "Codex hooks", Status: doctorAINotifyStatusMissing, ConfigPath: "/private/config", InstallCommand: "projmux agent integrate codex"}}
	}
	var plain, verbose bytes.Buffer
	if err := cmd.Run(nil, &plain, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Run([]string{"--verbose"}, &verbose, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, detail := range []string{"tmux 3.6", "/private/config", "install: projmux agent integrate codex"} {
		if strings.Contains(plain.String(), detail) || !strings.Contains(verbose.String(), detail) {
			t.Fatalf("detail %q boundary wrong\nplain:\n%s\nverbose:\n%s", detail, plain.String(), verbose.String())
		}
	}
	var compactJSON, verboseJSON bytes.Buffer
	if err := cmd.Run([]string{"--json"}, &compactJSON, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Run([]string{"--json", "--verbose"}, &verboseJSON, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(compactJSON.Bytes(), verboseJSON.Bytes()) {
		t.Fatalf("--verbose changed JSON\nplain:\n%s\nverbose:\n%s", compactJSON.String(), verboseJSON.String())
	}
}

func TestDoctorTextAndJSONSectionUseSameDependencyInventory(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("linux", map[string]bool{"tmux": true, "stty": true, "apt-get": true})
	var textOut, jsonOut bytes.Buffer
	if err := cmd.Run([]string{"--section", "deps", "--verbose"}, &textOut, io.Discard); err == nil {
		t.Fatal("text deps exit = success, want required git failure")
	}
	if err := cmd.Run([]string{"--section", "deps", "--json"}, &jsonOut, io.Discard); err != nil {
		t.Fatalf("JSON deps error = %v", err)
	}
	var report doctorReport
	if err := json.Unmarshal(jsonOut.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	for _, result := range report.Dependencies {
		if !strings.Contains(textOut.String(), result.Name) {
			t.Fatalf("text projection omitted JSON dependency %q\ntext:\n%s\nJSON:\n%s", result.Name, textOut.String(), jsonOut.String())
		}
	}
	for _, other := range []string{"Runtime", "AI notify integrations", "Session State", "Logs"} {
		if strings.Contains(textOut.String(), other) {
			t.Fatalf("deps text leaked section %q:\n%s", other, textOut.String())
		}
	}
}

func TestDoctorSectionCollectsOnlySelectedInventory(t *testing.T) {
	t.Parallel()

	for _, section := range []string{"runtime", "logs"} {
		cmd := newStubDoctorCommand("linux", map[string]bool{})
		cmd.lookPath = func(string) (string, error) { t.Fatal("dependency inventory evaluated"); return "", nil }
		cmd.aiDiagnostics = func() []doctorAINotifyIntegration { t.Fatal("integration inventory evaluated"); return nil }
		cmd.resumeDiagnostics = func() []doctorSessionStateResumeDiagnostic { t.Fatal("session inventory evaluated"); return nil }
		if err := cmd.Run([]string{"--section", section}, io.Discard, io.Discard); err != nil {
			t.Fatalf("Run(--section %s) error = %v", section, err)
		}
	}
}

func TestDoctorRemovedFlagsFailWithExactReadOnlyMigration(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("linux", map[string]bool{})
	for _, name := range []string{"install-missing", "include-optional", "dry-run"} {
		for _, arg := range []string{"--" + name, "--" + name + "=false", "--" + name + "=true"} {
			t.Run(arg, func(t *testing.T) {
				var stdout, stderr bytes.Buffer
				err := cmd.Run([]string{arg}, &stdout, &stderr)
				want := "flag provided but not defined: -" + name + "\nprojmux doctor is read-only; remove --" + name + " and run displayed remediation guidance explicitly outside doctor"
				if err == nil || err.Error() != want || !IsUsageError(err) {
					t.Fatalf("Run(%q) error = %#v, want exact UsageError %q", arg, err, want)
				}
				if stdout.Len() != 0 || stderr.Len() != 0 {
					t.Fatalf("Run(%q) stdout=%q stderr=%q, want no partial report", arg, stdout.String(), stderr.String())
				}
			})
		}
	}
}

func TestDoctorAllFlagFormsBypassLegacyMigration(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"doctor"},
		{"doctor", "--json"},
		{"doctor", "--verbose"},
		{"doctor", "--section", "deps"},
		{"doctor", "--json", "--section=integrations", "--verbose"},
		{"doctor", "--install-missing"},
		{"doctor", "--dry-run=false"},
		{"doctor", "--include-optional=true"},
		{"doctor", "--unknown"},
	} {
		if shouldRunLegacyHookMigrations(args) {
			t.Fatalf("shouldRunLegacyHookMigrations(%q) = true, want false", args)
		}
	}
}

func TestDoctorCanonicalHelpContract(t *testing.T) {
	t.Parallel()

	var root bytes.Buffer
	if err := cli.RenderRootHelp(&root); err != nil {
		t.Fatalf("RenderRootHelp returned error: %v", err)
	}
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
		"-json",
		"-section string",
		"deps|runtime|integrations|session-state|logs",
		"-verbose",
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("doctor flag help missing %q:\n%s", want, stderr.String())
		}
	}
}

func TestDoctorExitSemanticsAcrossFormatsAndSections(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		args    []string
		present map[string]bool
		wantErr bool
	}{
		{name: "plain required missing", present: map[string]bool{"tmux": true, "stty": true}, wantErr: true},
		{name: "verbose required missing", args: []string{"--verbose"}, present: map[string]bool{"tmux": true, "stty": true}, wantErr: true},
		{name: "deps required missing", args: []string{"--section", "deps"}, present: map[string]bool{"tmux": true, "stty": true}, wantErr: true},
		{name: "json required missing preserves successful exit", args: []string{"--json"}, present: map[string]bool{"tmux": true, "stty": true}},
		{name: "integrations ignores deps", args: []string{"--section", "integrations"}, present: map[string]bool{}},
		{name: "runtime empty", args: []string{"--section", "runtime"}, present: map[string]bool{}},
		{name: "logs empty", args: []string{"--section", "logs"}, present: map[string]bool{}},
		{name: "session state", args: []string{"--section", "session-state"}, present: map[string]bool{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cmd := newStubDoctorCommand("linux", tc.present)
			err := cmd.Run(tc.args, io.Discard, io.Discard)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Run() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestDoctorSectionRejectsUnknownValue(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("linux", map[string]bool{})
	err := cmd.Run([]string{"--section", "future"}, io.Discard, io.Discard)
	if err == nil || !IsUsageError(err) || err.Error() != "doctor --section must be one of deps, runtime, integrations, session-state, or logs" {
		t.Fatalf("Run() error = %#v, want exact section UsageError", err)
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

func TestDoctorWindowsRequiresTmuxAndSkipsStty(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("windows", map[string]bool{
		"tmux": true, "git": true,
	})

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"--verbose"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "[skip]") || !strings.Contains(out, "stty") {
		t.Fatalf("stty should be skipped on windows:\n%s", out)
	}
	if !strings.Contains(out, "windows host") {
		t.Fatalf("skip reason missing:\n%s", out)
	}
	if !strings.Contains(out, "[ok]      tmux") {
		t.Fatalf("tmux should remain the core dependency on windows:\n%s", out)
	}
}

func TestDoctorLinuxTmuxCoreDependencyAndStaleCheckRemainActive(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommandWithVersions(
		"linux",
		map[string]bool{"tmux": true, "git": true, "stty": true},
		map[string]string{"tmux": "tmux 3.2"},
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
	if report.SchemaVersion != doctorSchemaVersion {
		t.Fatalf("schema_version = %d, want %d", report.SchemaVersion, doctorSchemaVersion)
	}
	byName := doctorResultsByName(report.Dependencies)
	tmux, ok := byName["tmux"]
	if !ok {
		t.Fatalf("linux dependencies missing tmux: %#v", report.Dependencies)
	}
	if !tmux.Required || tmux.Status != doctorStatusStale {
		t.Fatalf("tmux dependency = %#v, want required stale", tmux)
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
	if len(report.Dependencies) != 3 {
		t.Fatalf("len(report.Dependencies) = %d, want 3", len(report.Dependencies))
	}
	byName := map[string]doctorResult{}
	for _, r := range report.Dependencies {
		byName[r.Name] = r
	}
	if byName["tmux"].Status != doctorStatusOK {
		t.Fatalf("tmux status = %q, want ok", byName["tmux"].Status)
	}
	if !byName["tmux"].Required {
		t.Fatalf("tmux Required = false, want true")
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
	if err := cmd.Run([]string{"--verbose"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, want := range []string{
		"Session State retention",
		"projmux prune snapshot",
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
				InstallCommand: "projmux agent integrate codex",
				RemoveCommand:  "projmux agent integrate codex --remove",
				DryRunCommand:  "projmux agent integrate codex --dry-run",
			},
		}
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"--verbose"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	out := stdout.String()
	for _, want := range []string{
		"AI notify integrations",
		"[conflict]",
		"Codex hooks",
		"/home/tester/.codex/config.toml",
		"install: projmux agent integrate codex",
		"remove: projmux agent integrate codex --remove",
		"dry-run: projmux agent integrate codex --dry-run",
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
				InstallCommand: "projmux agent integrate tmux-bell",
				RemoveCommand:  "projmux agent integrate tmux-bell --remove",
				DryRunCommand:  "projmux agent integrate tmux-bell --dry-run",
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
	if len(report.Dependencies) != 3 {
		t.Fatalf("len(report.Dependencies) = %d, want 3", len(report.Dependencies))
	}
	if len(report.AINotifyIntegrations) != 1 {
		t.Fatalf("len(report.AINotifyIntegrations) = %d, want 1", len(report.AINotifyIntegrations))
	}
	got := report.AINotifyIntegrations[0]
	if got.ID != "tmux-bell" || got.Status != doctorAINotifyStatusMissing {
		t.Fatalf("AI diagnostic = %#v, want tmux-bell missing", got)
	}
	if got.DryRunCommand != "projmux agent integrate tmux-bell --dry-run" {
		t.Fatalf("DryRunCommand = %q", got.DryRunCommand)
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
				InstallCommand: "projmux agent integrate tmux-bell",
				RemoveCommand:  "projmux agent integrate tmux-bell --remove",
				DryRunCommand:  "projmux agent integrate tmux-bell --dry-run",
			},
		}
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"--verbose"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v\noutput=%s", err, stdout.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"[missing]  tmux bell fallback",
		"install: projmux agent integrate tmux-bell",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q\nfull output:\n%s", want, out)
		}
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
				Confidence:      "medium",
				ResumeSource:    aisessions.SourceAntigravityLastConversation,
				ResumeUpdatedAt: "2026-06-04T03:04:05Z",
				SnapshotPath:    "/tmp/workspace.json",
			},
			{
				Session:         "workspace",
				WindowIndex:     0,
				PaneIndex:       3,
				Agent:           "antigravity",
				Status:          "available",
				Confidence:      "low",
				ResumeSource:    aisessions.SourceAntigravityHistory,
				ResumeUpdatedAt: "2026-06-03T03:04:05Z",
				SnapshotPath:    "/tmp/workspace.json",
			},
		}
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"--verbose"}, &stdout, &bytes.Buffer{}); err != nil {
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
		"confidence: medium",
		"source: " + aisessions.SourceAntigravityLastConversation,
		"workspace:0.3",
		"confidence: low",
		"source: " + aisessions.SourceAntigravityHistory,
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
	if len(report.SessionStateResume) != 3 || report.SessionStateResume[0].Status != "stale" || report.SessionStateResume[1].ResumeSource != aisessions.SourceAntigravityLastConversation || report.SessionStateResume[1].Confidence != "medium" || report.SessionStateResume[2].ResumeSource != aisessions.SourceAntigravityHistory || report.SessionStateResume[2].Confidence != "low" {
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
	if byID["codex-hooks"].InstallCommand != "projmux agent integrate codex" {
		t.Fatalf("codex hooks InstallCommand = %q", byID["codex-hooks"].InstallCommand)
	}
	if byID["codex-hooks"].DryRunCommand != "projmux agent integrate codex --dry-run" {
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
	if byID["antigravity-hooks"].ProviderID != "antigravity" || byID["antigravity-hooks"].InstallCommand != "projmux agent integrate antigravity" || byID["antigravity-hooks"].RemoveCommand != "projmux agent integrate antigravity --remove" || byID["antigravity-hooks"].DryRunCommand != "projmux agent integrate antigravity --dry-run" {
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
	if missing.Status != doctorAINotifyStatusMissing || missing.ConfigPath != filepath.Join(home, antigravityHooksRelativePath) || missing.StatusLinePath != filepath.Join(home, antigravitySettingsRelativePath) {
		t.Fatalf("missing diagnostic = %#v", missing)
	}
	for _, want := range []string{"projmux agent integrate antigravity", "projmux agent integrate antigravity --remove", "projmux agent integrate antigravity --dry-run"} {
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

	// A separately missing statusline is partial/stale, not fully installed.
	if err := os.Remove(filepath.Join(home, antigravitySettingsRelativePath)); err != nil {
		t.Fatal(err)
	}
	writeCodexTestFile(t, path, installedData)
	partial := doctorAntigravityIntegrationDiagnostic(cmd)
	if partial.Status != doctorAINotifyStatusStale || !strings.Contains(partial.ConflictReason, "partial") {
		t.Fatalf("partial diagnostic = %#v", partial)
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
		"tmux": true,
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
			if name == "tmux" {
				return "tmux 3.6"
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
		map[string]bool{"tmux": true, "git": true, "stty": true, "apt-get": true},
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

	// Confirms that when MinVersion == "" (e.g. git and stty), no
	// version comparison happens even if the version output is empty.
	cmd := newStubDoctorCommandWithVersions(
		"linux",
		map[string]bool{"tmux": true, "git": true, "stty": true},
		map[string]string{"git": "", "stty": ""},
	)

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v\noutput=%s", err, stdout.String())
	}
	out := stdout.String()
	if strings.Contains(out, "[stale]") {
		t.Fatalf("output should not flag any dep as stale:\n%s", out)
	}
	if !strings.Contains(out, "3 ok, 0 missing, 0 stale, 0 skipped, 0 hint.") {
		t.Fatalf("summary line wrong:\n%s", out)
	}
}

func TestDoctorVersionParseFailureDoesNotMarkStale(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommandWithVersions(
		"linux",
		map[string]bool{"tmux": true, "git": true, "stty": true},
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
		map[string]bool{"tmux": true, "git": true, "stty": true, "apt-get": true},
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
