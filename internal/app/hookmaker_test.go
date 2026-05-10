package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

func TestSettingsGlobalHooksListShowsActiveAndMissingPaths(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configHome := t.TempDir()
	active := filepath.Join(configHome, "projmux", "hooks", "post-create")
	mkdirAll(t, filepath.Dir(active))
	if err := os.WriteFile(active, []byte("#!/bin/sh\n"), 0o755); err != nil {
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

	entries := cmd.globalHookEntries()
	assertEntryLabelContainsAll(t, entries, "post-create", "active", active)
	assertEntryLabelContainsAll(t, entries, "pane-startup", "missing", filepath.Join(configHome, "projmux", "hooks", "pane-startup"))
}

func TestSettingsProjectHooksListWithContext(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	postCreate := filepath.Join(repo, ".projmux", "post-create")
	paneStartup := filepath.Join(repo, ".projmux", "hooks", "pane-startup")
	mkdirAll(t, filepath.Dir(paneStartup))
	if err := os.WriteFile(postCreate, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paneStartup, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".projmux", "config.toml"), []byte(`
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

	options := cmd.rootOptions(settingsRootTabProject)
	if !hasEntryValue(options.Entries, settingsSectionProjectHooks) {
		t.Fatalf("project tab entries = %#v, want project Hooks page row", options.Entries)
	}

	hookOptions, err := cmd.sectionOptions(settingsSectionProjectHooks)
	if err != nil {
		t.Fatalf("sectionOptions(project hooks) error = %v", err)
	}
	assertEntryLabelContainsAll(t, hookOptions.Entries, "post-create", "active", postCreate)
	assertEntryLabelContainsAll(t, hookOptions.Entries, "pane-startup", "active", paneStartup)
	assertEntryLabelContainsAll(t, hookOptions.Entries, "pre-create", "missing", filepath.Join(repo, ".projmux", "pre-create"))
	assertEntryLabelContainsAll(t, hookOptions.Entries, "config.toml", "present", "startup")
}

func TestSettingsProjectHooksNoProjectUsesDisabledState(t *testing.T) {
	t.Parallel()

	cmd := &settingsCommand{lookupEnv: func(string) string { return "" }}
	options, err := cmd.sectionOptions(settingsSectionProjectHooks)
	if err != nil {
		t.Fatalf("sectionOptions(project hooks) error = %v", err)
	}
	assertEntryLabelContainsAll(t, options.Entries, "Hooks (project)", "disabled", "no project context")
	if hasEntryLabelContaining(options.Entries, "post-create") {
		t.Fatalf("project hooks entries = %#v, want no hook event rows without project context", options.Entries)
	}
}

func TestSettingsHookMakerExcludesInternalTmuxHooks(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	internalHook := filepath.Join(repo, ".projmux", "hooks", "after-select-pane")
	mkdirAll(t, filepath.Dir(internalHook))
	if err := os.WriteFile(internalHook, []byte("#!/bin/sh\n"), 0o755); err != nil {
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
	entries := cmd.projectHookEntries(cmd.resolveSettingsProjectContext())
	for _, label := range []string{"after-select-pane", "pane-focus-in", "pane-focus-out"} {
		if hasEntryLabelContaining(entries, label) {
			t.Fatalf("project hook entries = %#v, want no internal tmux hook %q", entries, label)
		}
	}
}

func TestSettingsHookMakerDoesNotMutateTrustStore(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configHome := t.TempDir()
	stateHome := t.TempDir()
	repo := t.TempDir()
	active := filepath.Join(repo, ".projmux", "hooks", "post-create")
	mkdirAll(t, filepath.Dir(active))
	if err := os.WriteFile(active, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
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

	_ = cmd.globalHookEntries()
	_ = cmd.projectHookEntries(cmd.resolveSettingsProjectContext())

	got, err := os.ReadFile(trustStore)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != sentinel {
		t.Fatalf("trust store = %q, want unchanged %q", got, sentinel)
	}
}

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
