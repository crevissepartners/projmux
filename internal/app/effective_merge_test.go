package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// TestEffectiveMergeEntriesDisabledWithoutProject confirms the
// Effective merge view page degrades gracefully when no project context
// is available (e.g. user opened Settings from a non-project shell).
func TestEffectiveMergeEntriesDisabledWithoutProject(t *testing.T) {
	t.Parallel()

	cmd := &settingsCommand{lookupEnv: func(string) string { return "" }}
	entries := cmd.effectiveMergeEntries(settingsProjectContext{})

	if len(entries) == 0 {
		t.Fatalf("entries empty, want at least Back + disabled row")
	}
	if !hasEntryValue(entries, settingsBackValue) {
		t.Fatalf("entries missing Back row: %#v", entries)
	}
	if !hasEntryLabelContaining(entries, "disabled - no project context") {
		t.Fatalf("entries missing disabled row: %#v", entries)
	}
}

// TestEffectiveMergeEntriesOmitsProjectContextRow is a Phase 2.7 regression
// guard: the redundant "Project context" info row that lived above the
// Global/Project config rows is removed because the frame title chip
// strip (Phase 2.5) already announces the active scope.
func TestEffectiveMergeEntriesOmitsProjectContextRow(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	project := filepath.Join(home, "repo")
	mustMkdirAll(t, filepath.Join(project, ".projmux"))

	cmd := &settingsCommand{
		homeDir:   func() (string, error) { return home, nil },
		lookupEnv: func(string) string { return "" },
	}
	ctx := newSettingsProjectContext(project, "test")
	entries := cmd.effectiveMergeEntries(ctx)

	if hasEntryLabelContaining(entries, "Project context") {
		t.Fatalf("effective merge entries = %#v, want no \"Project context\" info row", entries)
	}
}

// TestEffectiveMergeEntriesShowSourceLabels verifies the spec requirement:
// each merged row carries one of the four source labels (project / global /
// merged / default), and the project-wins policy applies on conflict.
func TestEffectiveMergeEntriesShowSourceLabels(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configHome := filepath.Join(home, ".config")
	mustMkdirAll(t, filepath.Join(configHome, "projmux"))
	if err := os.WriteFile(filepath.Join(configHome, "projmux", "config.toml"), []byte(strings.Join([]string{
		`[env]`,
		`EDITOR = "vim"`,
		`DATABASE_URL = "postgres://global"`,
		`[kube]`,
		`namespace = "team-foo"`,
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write global config: %v", err)
	}

	project := filepath.Join(home, "repo")
	mustMkdirAll(t, filepath.Join(project, ".projmux"))
	if err := os.WriteFile(filepath.Join(project, ".projmux", "config.toml"), []byte(strings.Join([]string{
		`[env]`,
		`DATABASE_URL = "postgres://project"`,
		`GH_TOKEN = "ghp_secret"`,
		`[kube]`,
		`context = "dev-cluster"`,
		`[startup]`,
		`run = "claude"`,
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	cmd := &settingsCommand{
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			if name == "XDG_CONFIG_HOME" {
				return configHome
			}
			return ""
		},
	}
	ctx := newSettingsProjectContext(project, "test")
	entries := cmd.effectiveMergeEntries(ctx)

	// Section headers — env should report merged (both axes contributed);
	// kube also merged (project context + global namespace); startup is
	// project-only.
	requireEntryLabelContains(t, entries, "[env]", "(merged)")
	requireEntryLabelContains(t, entries, "[kube]", "(merged)")
	requireEntryLabelContains(t, entries, "[startup]", "(project)")

	// Per-row labels — conflict resolves to project; non-conflicting keys
	// keep their origin axis.
	requireEntryLabelContains(t, entries, "DATABASE_URL", "(project)")
	requireEntryLabelContains(t, entries, "DATABASE_URL", "postgres://project")
	requireEntryLabelContains(t, entries, "EDITOR", "(global)")
	requireEntryLabelContains(t, entries, "EDITOR", "vim")
	requireEntryLabelContains(t, entries, "GH_TOKEN", "(project)")
	requireEntryLabelContains(t, entries, "context", "(project)")
	requireEntryLabelContains(t, entries, "context", "dev-cluster")
	requireEntryLabelContains(t, entries, "namespace", "(global)")
	requireEntryLabelContains(t, entries, "namespace", "team-foo")
	requireEntryLabelContains(t, entries, "run", "claude")
	requireEntryLabelContains(t, entries, "run", "(project)")
}

// TestEffectiveMergeEntriesRedactSensitiveEnv verifies the spec
// requirement: env keys flagged as sensitive (TOKEN / SECRET / KEY /
// PASSWORD) never expose their value in the popup.
func TestEffectiveMergeEntriesRedactSensitiveEnv(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configHome := filepath.Join(home, ".config")
	mustMkdirAll(t, filepath.Join(configHome, "projmux"))

	project := filepath.Join(home, "repo")
	mustMkdirAll(t, filepath.Join(project, ".projmux"))
	if err := os.WriteFile(filepath.Join(project, ".projmux", "config.toml"), []byte(strings.Join([]string{
		`[env]`,
		`GH_TOKEN = "ghp_abcDEF123"`,
		`API_SECRET = "shhh"`,
		`OPENAI_API_KEY = "sk-xxxxxxxx"`,
		`DB_PASSWORD = "p4ss"`,
		`EDITOR = "vim"`,
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	cmd := &settingsCommand{
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			if name == "XDG_CONFIG_HOME" {
				return configHome
			}
			return ""
		},
	}
	ctx := newSettingsProjectContext(project, "test")
	entries := cmd.effectiveMergeEntries(ctx)

	for _, secret := range []string{"ghp_abcDEF123", "shhh", "sk-xxxxxxxx", "p4ss"} {
		for _, entry := range entries {
			if strings.Contains(entry.Label, secret) {
				t.Fatalf("entry label %q leaked sensitive value %q", entry.Label, secret)
			}
		}
	}
	// EDITOR is not sensitive — its value should appear verbatim.
	requireEntryLabelContains(t, entries, "EDITOR", "vim")
	// Each sensitive key still has a row with the redaction sentinel.
	for _, key := range []string{"GH_TOKEN", "API_SECRET", "OPENAI_API_KEY", "DB_PASSWORD"} {
		requireEntryLabelContains(t, entries, key, hooks.SensitiveRedaction)
	}
}

// TestEffectiveMergeEntriesHandleMissingGlobal verifies the spec note:
// when global config.toml is absent, every project value is labelled
// project (or default for unset scalars) — the page still renders.
func TestEffectiveMergeEntriesHandleMissingGlobal(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	project := filepath.Join(home, "repo")
	mustMkdirAll(t, filepath.Join(project, ".projmux"))
	if err := os.WriteFile(filepath.Join(project, ".projmux", "config.toml"), []byte(strings.Join([]string{
		`[env]`,
		`EDITOR = "vim"`,
		`[startup]`,
		`run = "claude"`,
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	cmd := &settingsCommand{
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(string) string {
			return ""
		},
	}
	ctx := newSettingsProjectContext(project, "test")
	entries := cmd.effectiveMergeEntries(ctx)

	// EDITOR exists only in project → project label.
	requireEntryLabelContains(t, entries, "EDITOR", "(project)")
	// kube scalars unset on both axes → default label.
	requireEntryLabelContains(t, entries, "context", "(default)")
	requireEntryLabelContains(t, entries, "context", "(unset)")
	requireEntryLabelContains(t, entries, "namespace", "(default)")
}

// TestRunSectionDispatchesEffectiveMerge confirms runSection routes the
// effective-merge section value to the new handler — guards against
// regressions where a new section is added but the dispatch table is
// forgotten.
func TestRunSectionDispatchesEffectiveMerge(t *testing.T) {
	t.Parallel()

	// Drive the picker once: first call returns the page entries (so we
	// can assert the section opened), second call returns Back to exit.
	var calls int
	runner := switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		if calls == 1 {
			// Confirm the popup UI key matches the effective merge page.
			if got, want := options.UI, "settings-effective-merge"; got != want {
				t.Fatalf("first picker UI = %q, want %q", got, want)
			}
		}
		return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
	})

	cmd := &settingsCommand{
		runner:       runner,
		nativePicker: nativePickerFromCompatRunner(runner),
		lookupEnv:    func(string) string { return "" },
	}
	if err := cmd.runSection(settingsSectionEffectiveMerge, nil, nil); err != nil {
		t.Fatalf("runSection() error = %v", err)
	}
	if calls < 1 {
		t.Fatalf("runSection did not invoke picker (calls = %d)", calls)
	}
}

// TestEffectiveMergeEntriesHooksProjectWins covers the Phase 4 spec: a hook
// defined on both axes resolves to the project value with a (project) label;
// non-conflicting events keep their origin axis label; the section header
// reads (merged) when both axes contributed.
func TestEffectiveMergeEntriesHooksProjectWins(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configHome := filepath.Join(home, ".config")
	mustMkdirAll(t, filepath.Join(configHome, "projmux"))
	if err := os.WriteFile(filepath.Join(configHome, "projmux", "config.toml"), []byte(strings.Join([]string{
		`[hooks.pre-create]`,
		`run = "echo global-pre"`,
		`[hooks.post-create]`,
		`run = "echo global-post"`,
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write global config: %v", err)
	}

	project := filepath.Join(home, "repo")
	mustMkdirAll(t, filepath.Join(project, ".projmux"))
	if err := os.WriteFile(filepath.Join(project, ".projmux", "config.toml"), []byte(strings.Join([]string{
		`[hooks.post-create]`,
		`run = "echo project-post"`,
		`[hooks.pane-startup]`,
		`run = "echo project-pane"`,
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	cmd := &settingsCommand{
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			if name == "XDG_CONFIG_HOME" {
				return configHome
			}
			return ""
		},
	}
	ctx := newSettingsProjectContext(project, "test")
	entries := cmd.effectiveMergeEntries(ctx)

	// Section header — both axes contributed hook entries → merged.
	requireEntryLabelContains(t, entries, "[hooks]", "(merged)")
	// pre-create only on global axis → global label, value rendered verbatim.
	requireEntryLabelContains(t, entries, "pre-create", "(global)")
	requireEntryLabelContains(t, entries, "pre-create", "echo global-pre")
	// post-create defined on both → project wins; row label reads project.
	requireEntryLabelContains(t, entries, "post-create", "(project)")
	requireEntryLabelContains(t, entries, "post-create", "echo project-post")
	// pane-startup only in project → project label + deprecated badge.
	requireEntryLabelContains(t, entries, "pane-startup (deprecated)", "(project)")
	requireEntryLabelContains(t, entries, "pane-startup (deprecated)", "echo project-pane")
	// post-attach and send-noti defined nowhere — confirm omission so the
	// popup stays uncluttered for unused lifecycle events.
	for _, entry := range entries {
		if strings.Contains(entry.Label, "post-attach") || strings.Contains(entry.Label, "send-noti") {
			t.Fatalf("entries leaked an undefined hook row: %q", entry.Label)
		}
	}
}

// TestEffectiveMergeEntriesHooksEmpty pins the Phase 4 display decision: when
// neither axis defines any hook, the section still renders a header row plus
// a single dim "(no hooks configured)" row, labelled as default.
func TestEffectiveMergeEntriesHooksEmpty(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	project := filepath.Join(home, "repo")
	mustMkdirAll(t, filepath.Join(project, ".projmux"))
	if err := os.WriteFile(filepath.Join(project, ".projmux", "config.toml"), []byte(`[env]
EDITOR = "vim"
`), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	cmd := &settingsCommand{
		homeDir:   func() (string, error) { return home, nil },
		lookupEnv: func(string) string { return "" },
	}
	ctx := newSettingsProjectContext(project, "test")
	entries := cmd.effectiveMergeEntries(ctx)

	requireEntryLabelContains(t, entries, "[hooks]", "(default)")
	// The empty-row uses settingsLabelDim, which renders the source slot as
	// a bare token (no parens). Match that shape so we pin the actual UX.
	requireEntryLabelContains(t, entries, "no hooks configured", "default")
}

// TestEffectiveMergeEntriesHooksDoesNotRedactRunCommand documents the
// Phase 4 decision: hook `run` strings are user-authored commands and are
// rendered verbatim, even when they contain tokens or other secrets. Only
// env values flagged by IsSensitiveEnvKey are redacted.
func TestEffectiveMergeEntriesHooksDoesNotRedactRunCommand(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	project := filepath.Join(home, "repo")
	mustMkdirAll(t, filepath.Join(project, ".projmux"))
	if err := os.WriteFile(filepath.Join(project, ".projmux", "config.toml"), []byte(strings.Join([]string{
		`[hooks.post-create]`,
		// Intentionally embed a credential-shaped value in the user's
		// shell command. The user wrote this on purpose; we surface it.
		`run = "curl -H 'Authorization: Bearer ghp_xxx' https://example.com"`,
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	cmd := &settingsCommand{
		homeDir:   func() (string, error) { return home, nil },
		lookupEnv: func(string) string { return "" },
	}
	ctx := newSettingsProjectContext(project, "test")
	entries := cmd.effectiveMergeEntries(ctx)

	requireEntryLabelContains(t, entries, "post-create", "ghp_xxx")
	requireEntryLabelContains(t, entries, "post-create", "(project)")
}

// TestHookRunDisplayValueHandlesEmpty pins the small display helper. The
// merge engine omits unset events entirely, but the helper still has to
// handle the empty case (e.g. a future caller passes through raw values).
func TestHookRunDisplayValueHandlesEmpty(t *testing.T) {
	t.Parallel()

	if got := hookRunDisplayValue("echo hi"); got != "echo hi" {
		t.Fatalf("hookRunDisplayValue plain = %q, want echo hi", got)
	}
	if got := hookRunDisplayValue("   "); got != "(unset)" {
		t.Fatalf("hookRunDisplayValue whitespace = %q, want (unset)", got)
	}
}

// --- helpers --------------------------------------------------------------

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func requireEntryLabelContains(t *testing.T, entries []intpickercompat.Entry, key, fragment string) {
	t.Helper()
	for _, entry := range entries {
		if strings.Contains(entry.Label, key) && strings.Contains(entry.Label, fragment) {
			return
		}
	}
	t.Fatalf("entries missing label containing %q + %q\nentries:\n%s", key, fragment, debugDumpEntries(entries))
}

func debugDumpEntries(entries []intpickercompat.Entry) string {
	var b strings.Builder
	for _, e := range entries {
		b.WriteString("  ")
		b.WriteString(stripAnsi(e.Label))
		b.WriteString("\n")
	}
	return b.String()
}

func stripAnsi(s string) string {
	// Cheap ANSI strip — good enough for test diagnostics.
	out := make([]byte, 0, len(s))
	i := 0
	for i < len(s) {
		if s[i] == 0x1b {
			j := i + 1
			for j < len(s) && s[j] != 'm' {
				j++
			}
			if j < len(s) {
				i = j + 1
				continue
			}
		}
		out = append(out, s[i])
		i++
	}
	return string(out)
}
