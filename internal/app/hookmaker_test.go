package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// --- Page-render tests ------------------------------------------------------

func TestSettingsGlobalHooksListShowsConfigPathAndAddRows(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configHome := t.TempDir()

	cmd := &settingsCommand{
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			if name == "XDG_CONFIG_HOME" {
				return configHome
			}
			return ""
		},
	}

	wantConfig := filepath.Join(configHome, "projmux", "config.toml")
	entries := cmd.hookEventDetailEntries(hookScopeGlobal, "post-create")
	assertEntryLabelContainsAll(t, entries, "Command", "no command", wantConfig, "[hooks.post-create]")
	assertEntryLabelContainsAll(t, entries, "Add or edit command", "read-only here")
	assertEntryLabelContainsAll(t, entries, "Remove command", "read-only here")
	if hasEntryValue(entries, settingsActionPrefixHookAdd+"global:post-create") {
		t.Fatalf("entries = %#v, did not expect editable global add row", entries)
	}
	sendNoti := cmd.hookEventDetailEntries(hookScopeGlobal, "send-noti")
	assertEntryLabelContainsAll(t, sendNoti, "Command", "no command", "[hooks.send-noti]")
}

func TestSettingsGlobalHooksListShowsActiveDeclarativeEntry(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "projmux", "config.toml")
	mkdirAll(t, filepath.Dir(configPath))
	if err := os.WriteFile(configPath, []byte(`
[hooks.post-create]
run = "echo global-active"
`), 0o644); err != nil {
		t.Fatal(err)
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
	entries := cmd.hookEventDetailEntries(hookScopeGlobal, "post-create")
	assertEntryLabelContainsAll(t, entries, "Command", "run = echo global-active", "[hooks.post-create]")
	assertEntryLabelContainsAll(t, entries, "Add or edit command", "read-only here")
	if hasEntryValue(entries, settingsActionPrefixHookEdit+"global:post-create") {
		t.Fatalf("entries = %#v, did not expect an editable global row", entries)
	}
}

func TestSettingsProjectHooksListAllMissingRendersAddRows(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	cmd := &settingsCommand{
		lookupEnv: func(name string) string {
			if name == "PROJMUX_CWD" {
				return repo
			}
			return ""
		},
	}
	for _, event := range []string{"pre-create", "post-create", "post-attach", "send-noti"} {
		entries := cmd.hookEventDetailEntries(hookScopeProject, event)
		assertEntryLabelContainsAll(t, entries, "Command", "no command", "[hooks."+event+"]")
		assertEntryLabelContainsAll(t, entries, "Add or edit command", "[hooks."+event+"]")
		if !hasEntryValue(entries, settingsActionPrefixHookAdd+"project:"+event) {
			t.Fatalf("entries = %#v, want the project add action for %q", entries, event)
		}
	}
}

func TestSettingsProjectHooksListNoProjectUsesDisabledState(t *testing.T) {
	t.Parallel()

	cmd := &settingsCommand{lookupEnv: func(string) string { return "" }}
	options, err := cmd.sectionOptions(settingsSectionProjectHooks)
	if err != nil {
		t.Fatalf("sectionOptions(project hooks) error = %v", err)
	}
	assertEntryLabelContainsAll(t, options.Entries, "Project hooks", "disabled", "no project context")
	if hasEntryLabelContaining(options.Entries, "post-create") {
		t.Fatalf("project hooks entries = %#v, want no hook event rows without project context", options.Entries)
	}
}

// TestSettingsProjectHooksListOmitsProjectContextRow is a Phase 2.7
// regression guard: the Hooks (project) page used to render a "Project
// context" info row both with and without a project. The frame title
// chip strip (Phase 2.5) is now the source of truth, so the redundant
// row is dropped on both branches.
func TestSettingsProjectHooksListOmitsProjectContextRow(t *testing.T) {
	t.Parallel()

	t.Run("no project", func(t *testing.T) {
		cmd := &settingsCommand{lookupEnv: func(string) string { return "" }}
		options, err := cmd.sectionOptions(settingsSectionProjectHooks)
		if err != nil {
			t.Fatalf("sectionOptions(project hooks) error = %v", err)
		}
		if hasEntryLabelContaining(options.Entries, "Project context") {
			t.Fatalf("project hooks entries (no project) = %#v, want no \"Project context\" row", options.Entries)
		}
	})

	t.Run("with project", func(t *testing.T) {
		repo := t.TempDir()
		if err := os.MkdirAll(filepath.Join(repo, ".projmux"), 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := &settingsCommand{
			lookupEnv: func(name string) string {
				if name == "PROJMUX_CWD" {
					return repo
				}
				return ""
			},
		}
		options, err := cmd.sectionOptions(settingsSectionProjectHooks)
		if err != nil {
			t.Fatalf("sectionOptions(project hooks) error = %v", err)
		}
		if hasEntryLabelContaining(options.Entries, "Project context") {
			t.Fatalf("project hooks entries (with project) = %#v, want no \"Project context\" info row", options.Entries)
		}
	})
}

func TestSettingsHookmakerStateColors(t *testing.T) {
	t.Parallel()

	// The per-event Automation detail owns the mutation rows now: the add/edit
	// row keeps the action colour and the remove row the destructive one.
	repo := t.TempDir()
	cmd := &settingsCommand{
		lookupEnv: func(name string) string {
			if name == "PROJMUX_CWD" {
				return repo
			}
			return ""
		},
	}
	addEntries := cmd.hookEventDetailEntries(hookScopeProject, "post-create")
	assertEntryColorForValue(t, addEntries, settingsActionPrefixHookAdd+"project:post-create", settingsColorAdd)

	writeFile(t, filepath.Join(repo, ".projmux", "config.toml"), `
[hooks.post-create]
run = "echo ok"
`)
	activeEntries := cmd.hookEventDetailEntries(hookScopeProject, "post-create")
	assertEntryColorForValue(t, activeEntries, settingsActionPrefixHookEdit+"project:post-create", settingsColorAdd)
	assertEntryColorForValue(t, activeEntries, settingsActionPrefixHookRemove+"project:post-create", settingsColorRemove)
}

func TestSettingsProjectHooksListWithDeclarativeContext(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".projmux"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".projmux", "config.toml"), []byte(`
[hooks.post-create]
run = "echo declared-post"

[startup]
run = "codex"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := &settingsCommand{
		lookupEnv: func(name string) string {
			if name == "PROJMUX_CWD" {
				return repo
			}
			return ""
		},
	}
	hookOptions, err := cmd.sectionOptions(settingsSectionProjectHooks)
	if err != nil {
		t.Fatalf("sectionOptions(project hooks) error = %v", err)
	}
	assertEntryLabelContainsAll(t, cmd.hookEventDetailEntries(hookScopeProject, "post-create"), "Command", "run = echo declared-post")
	assertEntryLabelContainsAll(t, cmd.hookEventDetailEntries(hookScopeProject, "pre-create"), "Command", "no command")
	for _, entry := range hookOptions.Entries {
		if entry.Value == "section:project-config" || strings.Contains(entry.Label, "Project recipe") {
			t.Fatalf("hook options = %#v, want retired Project recipe route absent", hookOptions.Entries)
		}
	}
}

func TestSettingsProjectHooksAddPickerHasNoBranch(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	cmd := &settingsCommand{
		lookupEnv: func(name string) string {
			if name == "PROJMUX_CWD" {
				return repo
			}
			return ""
		},
	}
	entries := cmd.projectHookEntries(cmd.resolveSettingsProjectContext())
	for _, entry := range entries {
		if strings.Contains(entry.Value, ":script") || strings.Contains(entry.Value, ":declarative") {
			t.Fatalf("entry %#v still encodes a script/declarative branch", entry)
		}
	}
}

func TestSettingsHookMakerExcludesInternalEvents(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	cmd := &settingsCommand{
		lookupEnv: func(name string) string {
			if name == "PROJMUX_CWD" {
				return repo
			}
			return ""
		},
	}
	entries := cmd.projectHookEntries(cmd.resolveSettingsProjectContext())
	for _, banned := range []string{"after-select-pane", "pane-focus-in", "pane-focus-out"} {
		for _, entry := range entries {
			if strings.Contains(entry.Value, banned) {
				t.Fatalf("entry %#v exposes internal event %q", entry, banned)
			}
		}
	}
}

func TestSettingsHookMakerLegacyMultiLineScriptRendersLegacyRow(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	scriptPath := filepath.Join(repo, ".projmux", "post-create")
	mkdirAll(t, filepath.Dir(scriptPath))
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\necho one\necho two\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := &settingsCommand{
		lookupEnv: func(name string) string {
			if name == "PROJMUX_CWD" {
				return repo
			}
			return ""
		},
	}
	entries := cmd.hookEventDetailEntries(hookScopeProject, "post-create")
	assertEntryLabelContainsAll(t, entries, "legacy script", scriptPath, "not executed")
}

func TestSettingsHookMakerLegacySymlinkRendersDotfilesNotice(t *testing.T) {
	t.Parallel()

	// A dotfiles-managed global hook arrives as a symlink whose source lives
	// outside the projmux config dir. The UI must (a) surface a dedicated
	// "symlink" message, (b) not pretend to count lines, and (c) leave the
	// link untouched after rendering.
	repo := t.TempDir()
	external := t.TempDir()
	target := filepath.Join(external, "post-create-source.sh")
	if err := os.WriteFile(target, []byte("#!/bin/sh\necho dotfiles\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(repo, ".projmux", "post-create")
	mkdirAll(t, filepath.Dir(scriptPath))
	if err := os.Symlink(target, scriptPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	cmd := &settingsCommand{
		lookupEnv: func(name string) string {
			if name == "PROJMUX_CWD" {
				return repo
			}
			return ""
		},
	}
	entries := cmd.hookEventDetailEntries(hookScopeProject, "post-create")
	assertEntryLabelContainsAll(t, entries, "post-create", "legacy script: symlink", scriptPath)
	assertEntryLabelContainsAll(t, entries, "post-create", "dotfiles repo")

	// Link untouched.
	if got, err := os.Readlink(scriptPath); err != nil || got != target {
		t.Fatalf("readlink got=%q err=%v, want %q", got, err, target)
	}
}

func TestSettingsHookMakerHooksPageDoesNotMutateTrustStore(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configHome := t.TempDir()
	stateHome := t.TempDir()
	repo := t.TempDir()
	trustStore := filepath.Join(stateHome, "projmux", "trusted-projects.json")
	mkdirAll(t, filepath.Dir(trustStore))
	const sentinel = `{"sentinel":true}`
	if err := os.WriteFile(trustStore, []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := &settingsCommand{
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			switch name {
			case "PROJMUX_CWD":
				return repo
			case "XDG_CONFIG_HOME":
				return configHome
			case "XDG_STATE_HOME":
				return stateHome
			default:
				return ""
			}
		},
		runCommand: func(name string, args ...string) error {
			t.Fatalf("read-only hook maker invoked command %s %#v", name, args)
			return nil
		},
	}

	for _, event := range settingsAutomationLifecycleEvents {
		_ = cmd.hookEventDetailEntries(hookScopeGlobal, event)
		_ = cmd.hookEventDetailEntries(hookScopeProject, event)
	}
	_ = cmd.projectHookEntries(cmd.resolveSettingsProjectContext())

	got, err := os.ReadFile(trustStore)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != sentinel {
		t.Fatalf("trust store = %q, want unchanged %q", got, sentinel)
	}
}

// --- Interactive add/edit/remove flows -------------------------------------

func TestSettingsHookMakerProjectAddDeclarativeOpensInlineForm(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configHome := t.TempDir()
	stateHome := t.TempDir()
	repo := t.TempDir()

	var calls int
	cmd := hookMakerTestSettings(t, home, configHome, stateHome, repo, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			// Page render: declarative add row only (no branch picker).
			if !hasEntryValue(options.Entries, settingsActionPrefixHookAdd+"project:post-create") {
				t.Fatalf("entries = %#v, want post-create add row", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixHookAdd + "project:post-create"}, nil
		case 2:
			// Inline typed form, NOT a branch picker.
			if options.UI != "settings-project-config-typed" {
				t.Fatalf("UI = %q, want settings-project-config-typed (branch picker removed)", options.UI)
			}
			return intpickercompat.Result{Key: "enter", Query: "echo from-declarative"}, nil
		case 3:
			if !hasEntryLabelContaining(options.Entries, "echo from-declarative") {
				t.Fatalf("refreshed entries missing declarative: %#v", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		default:
			t.Fatalf("unexpected runner call %d", calls)
			return intpickercompat.Result{}, nil
		}
	})

	var stdout, stderr bytes.Buffer
	if err := cmd.runHookEventDetailSection(hookScopeProject, "post-create", &stdout, &stderr); err != nil {
		t.Fatalf("runHookEventDetailSection: %v", err)
	}
	configPath := filepath.Join(repo, ".projmux", "config.toml")
	body := readFile(t, configPath)
	if !strings.Contains(body, "[hooks.post-create]") || !strings.Contains(body, `run = "echo from-declarative"`) {
		t.Fatalf("config.toml =\n%s\nwant [hooks.post-create] run line", body)
	}
	trustStore := readFile(t, filepath.Join(stateHome, "projmux", "trusted-projects.json"))
	if !strings.Contains(trustStore, ".projmux/config.toml") {
		t.Fatalf("trust store = %s, want config.toml trust", trustStore)
	}
}

func TestSettingsHookMakerProjectEditDeclarativeUpdatesEntry(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configHome := t.TempDir()
	stateHome := t.TempDir()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".projmux"), 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(repo, ".projmux", "config.toml")
	writeFile(t, configPath, `
[hooks.post-create]
run = "echo original"
`)

	var calls int
	cmd := hookMakerTestSettings(t, home, configHome, stateHome, repo, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			if !hasEntryValue(options.Entries, settingsActionPrefixHookEdit+"project:post-create") {
				t.Fatalf("entries = %#v, want post-create edit row", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixHookEdit + "project:post-create"}, nil
		case 2:
			return intpickercompat.Result{Key: "enter", Query: "echo updated"}, nil
		case 3:
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		default:
			t.Fatalf("unexpected runner call %d", calls)
			return intpickercompat.Result{}, nil
		}
	})

	var stdout, stderr bytes.Buffer
	if err := cmd.runHookEventDetailSection(hookScopeProject, "post-create", &stdout, &stderr); err != nil {
		t.Fatalf("runHookEventDetailSection: %v", err)
	}
	body := readFile(t, configPath)
	if !strings.Contains(body, `run = "echo updated"`) {
		t.Fatalf("config.toml =\n%s\nwant updated run", body)
	}
}

func TestSettingsHookMakerGlobalHooksAreReadonlyInApp(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configHome := t.TempDir()
	stateHome := t.TempDir()

	writeFile(t, filepath.Join(configHome, "projmux", "config.toml"), `
[hooks.post-create]
run = "echo global-declarative"
`)
	var calls int
	cmd := hookMakerTestSettings(t, home, configHome, stateHome, "", func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			if got, want := options.UI, "settings-automation"; got != want {
				t.Fatalf("UI = %q, want %q", got, want)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsAutomationLifecycle}, nil
		case 2:
			if got, want := options.UI, "settings-automation-lifecycle"; got != want {
				t.Fatalf("UI = %q, want %q", got, want)
			}
			if !hasEntryValue(options.Entries, settingsHookEventValue(hookScopeGlobal, "post-create")) {
				t.Fatalf("entries = %#v, want an After session create view row", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsHookEventValue(hookScopeGlobal, "post-create")}, nil
		case 3:
			if got, want := options.UI, "settings-automation-event"; got != want {
				t.Fatalf("UI = %q, want %q", got, want)
			}
			// Global `[hooks.*]` stays read-only in app: the mutation rows
			// are disabled with a reason instead of offering an edit that
			// cannot run.
			if hasEntryValue(options.Entries, settingsActionPrefixHookAdd+"global:post-create") ||
				hasEntryValue(options.Entries, settingsActionPrefixHookEdit+"global:post-create") ||
				hasEntryValue(options.Entries, settingsActionPrefixHookRemove+"global:post-create") {
				t.Fatalf("entries = %#v, did not expect a global mutation row", options.Entries)
			}
			if !hasEntryLabelContaining(options.Entries, "read-only here") {
				t.Fatalf("entries = %#v, want the read-only reason", options.Entries)
			}
			if !hasEntryLabelContaining(options.Entries, "projmux hook edit post-create") {
				t.Fatalf("entries = %#v, want the canonical next step", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		case 4:
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		case 5:
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		default:
			t.Fatalf("unexpected runner call %d", calls)
			return intpickercompat.Result{}, nil
		}
	})

	var stdout, stderr bytes.Buffer
	if err := cmd.runAutomationSection(&stdout, &stderr); err != nil {
		t.Fatalf("runAutomationSection: %v", err)
	}
	if got := readFile(t, filepath.Join(configHome, "projmux", "config.toml")); !strings.Contains(got, `run = "echo global-declarative"`) {
		t.Fatalf("global config changed unexpectedly: %s", got)
	}
}

// --- Project config section tests preserved from Phase 2.5 -----------------

func TestSettingsProjectHookWriterPreservesSymlinkAndRefreshesTrust(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configHome := filepath.Join(home, "config")
	stateHome := filepath.Join(home, "state")
	repo := filepath.Join(home, "repo")
	path := filepath.Join(repo, ".projmux", "config.toml")
	target := filepath.Join(home, "config-targets", "project.toml")
	writeFile(t, target, "[hooks.post-create]\nrun = \"old\"\n")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(filepath.Dir(path), target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(rel, path); err != nil {
		t.Fatal(err)
	}
	linkBefore, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}

	cmd := &settingsCommand{
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			switch name {
			case "XDG_CONFIG_HOME":
				return configHome
			case "XDG_STATE_HOME":
				return stateHome
			default:
				return ""
			}
		},
	}
	var stdout bytes.Buffer
	err = cmd.saveProjectConfig(settingsProjectContext{Path: repo, Name: "repo"}, &stdout, func(cfg *hooks.ProjectConfig) error {
		cfg.Hooks[hooks.EventPostCreate] = "make settings-smoke"
		return nil
	})
	if err != nil {
		t.Fatalf("saveProjectConfig() error = %v", err)
	}

	linkAfter, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if linkAfter.Mode()&os.ModeSymlink == 0 || !os.SameFile(linkBefore, linkAfter) {
		t.Fatal("Settings project writer replaced the config symlink")
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); !strings.Contains(got, `run = "make settings-smoke"`) {
		t.Fatalf("project target content = %q, want project hook update", got)
	}
	if _, err := os.Stat(filepath.Join(stateHome, "projmux", "trusted-projects.json")); err != nil {
		t.Fatalf("Settings trust-store smoke: %v", err)
	}
}

// --- helpers ----------------------------------------------------------------

func assertEntryLabelContainsAll(t *testing.T, entries []intpickercompat.Entry, parts ...string) {
	t.Helper()
	for _, entry := range entries {
		matches := true
		for _, part := range parts {
			if !strings.Contains(entry.Label, part) {
				matches = false
				break
			}
		}
		if matches {
			return
		}
	}
	t.Fatalf("entries = %#v, want one label containing all %#v", entries, parts)
}

func assertEntryColorForValue(t *testing.T, entries []intpickercompat.Entry, value, color string) {
	t.Helper()
	for _, entry := range entries {
		if entry.Value != value {
			continue
		}
		if !strings.Contains(entry.Label, color) {
			t.Fatalf("entry %q label = %q, want color %q", value, entry.Label, color)
		}
		return
	}
	t.Fatalf("entries = %#v, want entry value %q", entries, value)
}

// hookMakerTestSettings builds a settingsCommand wired so it can drive the
// project hook page using a scripted runner. The runner is the same one
// returned, so callers can mutate it between calls if needed.
func hookMakerTestSettings(t *testing.T, home, configHome, stateHome, repo string, run func(intpickercompat.Options) (intpickercompat.Result, error)) *settingsCommand {
	t.Helper()
	runner := switchRunnerFunc(run)
	return &settingsCommand{
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			switch name {
			case "PROJMUX_CWD":
				return repo
			case "XDG_CONFIG_HOME":
				return configHome
			case "XDG_STATE_HOME":
				return stateHome
			default:
				return ""
			}
		},
		runner:       runner,
		nativePicker: nativePickerFromCompatRunner(runner),
	}
}
