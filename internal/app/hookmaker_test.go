package app

import (
	"bytes"
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

func TestSettingsProjectConfigEditorWritesAndTrustsConfig(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configHome := t.TempDir()
	stateHome := t.TempDir()
	repo := t.TempDir()
	configPath := filepath.Join(repo, ".projmux", "config.toml")
	writeFile(t, configPath, `
[hooks.post-create]
run = "echo post"
`)

	var calls int
	runner := switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			return intpickercompat.Result{Key: "ctrl-p"}, nil
		case 2:
			if !hasEntryValue(options.Entries, settingsSectionProjectConfig) {
				t.Fatalf("project tab entries = %#v, want config.toml editor row", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsSectionProjectConfig}, nil
		case 3:
			if got, want := options.UI, "settings-project-config"; got != want {
				t.Fatalf("project config UI = %q, want %q", got, want)
			}
			if !hasEntryValue(options.Entries, settingsActionPrefixProjectConfig+"startup:set") {
				t.Fatalf("project config entries = %#v, want startup set action", options.Entries)
			}
			// Phase 2.5 (A): hook commands are authored from the Hooks page,
			// so the config.toml section must not advertise them. The labels
			// "Hook commands" / "preserved" only appear when the legacy row
			// is rendered.
			if hasEntryLabelContaining(options.Entries, "Hook commands") {
				t.Fatalf("project config entries = %#v, want no hook commands row", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixProjectConfig + "startup:set"}, nil
		case 4:
			if got, want := options.UI, "settings-project-config-typed"; got != want {
				t.Fatalf("typed UI = %q, want %q", got, want)
			}
			return intpickercompat.Result{Key: "enter", Query: "codex"}, nil
		case 5:
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixProjectConfig + "kube:context:set"}, nil
		case 6:
			return intpickercompat.Result{Key: "enter", Query: "dev"}, nil
		case 7:
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixProjectConfig + "env:add"}, nil
		case 8:
			return intpickercompat.Result{Key: "enter", Query: "FOO"}, nil
		case 9:
			return intpickercompat.Result{Key: "enter", Query: "bar"}, nil
		case 10:
			if !hasEntryLabelContaining(options.Entries, "codex") || !hasEntryLabelContaining(options.Entries, "dev") || !hasEntryLabelContaining(options.Entries, "FOO") {
				t.Fatalf("refreshed project config entries = %#v, want saved values", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		case 11:
			return intpickercompat.Result{}, nil
		default:
			t.Fatalf("unexpected settings picker call %d", calls)
			return intpickercompat.Result{}, nil
		}
	})
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
		runner:       runner,
		nativePicker: nativePickerFromCompatRunner(runner),
	}

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	got := readFile(t, configPath)
	for _, want := range []string{
		"[hooks.post-create]",
		`run = "echo post"`,
		"[startup]",
		`run = "codex"`,
		"[env]",
		`FOO = "bar"`,
		"[kube]",
		`context = "dev"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("config.toml =\n%s\nwant containing %q", got, want)
		}
	}
	trustStore := readFile(t, filepath.Join(stateHome, "projmux", "trusted-projects.json"))
	if !strings.Contains(trustStore, repo) || !strings.Contains(trustStore, ".projmux/config.toml") {
		t.Fatalf("trust store = %s, want repo config hash", trustStore)
	}
	if count := strings.Count(stdout.String(), "trusted "+configPath); count != 3 {
		t.Fatalf("stdout = %q, want three trust writes", stdout.String())
	}
}

func TestSettingsProjectConfigEditorRejectsInvalidEnvKey(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	stateHome := t.TempDir()
	repo := t.TempDir()
	var calls int
	runner := switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixProjectConfig + "env:add"}, nil
		case 2:
			return intpickercompat.Result{Key: "enter", Query: "1BAD"}, nil
		case 3:
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		default:
			t.Fatalf("unexpected settings picker call %d", calls)
			return intpickercompat.Result{}, nil
		}
	})
	cmd := &settingsCommand{
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			switch name {
			case "PROJMUX_CWD":
				return repo
			case "XDG_STATE_HOME":
				return stateHome
			default:
				return ""
			}
		},
		runner:       runner,
		nativePicker: nativePickerFromCompatRunner(runner),
	}

	var stderr bytes.Buffer
	if err := cmd.runProjectConfigSection(&bytes.Buffer{}, &stderr); err != nil {
		t.Fatalf("runProjectConfigSection() error = %v", err)
	}
	if !strings.Contains(stderr.String(), "invalid env key") {
		t.Fatalf("stderr = %q, want invalid env key warning", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(repo, ".projmux", "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("config.toml stat error = %v, want missing file", err)
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

// --- Phase 2.5 hook maker tests -------------------------------------------

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

func TestSettingsHookMakerProjectAddDeclarativeWritesConfigAndTrusts(t *testing.T) {
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
			// Page render: expect [+ Add] declarative row for post-create.
			if !hasEntryValue(options.Entries, settingsActionPrefixHookAdd+"project:post-create") {
				t.Fatalf("hook entries = %#v, want declarative add for post-create", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixHookAdd + "project:post-create"}, nil
		case 2:
			// Add branch picker.
			if !hasEntryValue(options.Entries, settingsActionPrefixHookAdd+"project:post-create:declarative") {
				t.Fatalf("add picker = %#v, want declarative branch", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixHookAdd + "project:post-create:declarative"}, nil
		case 3:
			// Typed field for [hooks.post-create] run.
			if options.UI != "settings-project-config-typed" {
				t.Fatalf("typed UI = %q", options.UI)
			}
			return intpickercompat.Result{Key: "enter", Query: "echo from-declarative"}, nil
		case 4:
			// Page refresh — declarative row is now active.
			if !hasEntryLabelContaining(options.Entries, "echo from-declarative") {
				t.Fatalf("refreshed entries = %#v, want declarative active", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		default:
			t.Fatalf("unexpected runner call %d", calls)
			return intpickercompat.Result{}, nil
		}
	})

	var stdout, stderr bytes.Buffer
	if err := cmd.runProjectHooksSection(&stdout, &stderr); err != nil {
		t.Fatalf("runProjectHooksSection: %v", err)
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

func TestSettingsHookMakerProjectAddScriptCreatesAndTrusts(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configHome := t.TempDir()
	stateHome := t.TempDir()
	repo := t.TempDir()

	var editedPath string
	var calls int
	cmd := hookMakerTestSettings(t, home, configHome, stateHome, repo, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			if !hasEntryValue(options.Entries, settingsActionPrefixHookAdd+"project:post-create") {
				t.Fatalf("page entries = %#v, want post-create add", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixHookAdd + "project:post-create"}, nil
		case 2:
			if !hasEntryValue(options.Entries, settingsActionPrefixHookAdd+"project:post-create:script") {
				t.Fatalf("add picker = %#v, want script branch", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixHookAdd + "project:post-create:script"}, nil
		case 3:
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		default:
			t.Fatalf("unexpected runner call %d", calls)
			return intpickercompat.Result{}, nil
		}
	})
	cmd.runCommand = func(name string, args ...string) error {
		if len(args) == 0 {
			t.Fatalf("editor invoked without path: %s", name)
		}
		editedPath = args[len(args)-1]
		// Simulate a user save: append a comment line.
		body, err := os.ReadFile(editedPath)
		if err != nil {
			return err
		}
		return os.WriteFile(editedPath, append(body, []byte("# edited by test\n")...), 0o755)
	}

	var stdout, stderr bytes.Buffer
	if err := cmd.runProjectHooksSection(&stdout, &stderr); err != nil {
		t.Fatalf("runProjectHooksSection: %v", err)
	}
	wantPath := filepath.Join(repo, ".projmux", "hooks", "post-create")
	if editedPath != wantPath {
		t.Fatalf("editor opened %q, want %q", editedPath, wantPath)
	}
	info, err := os.Stat(wantPath)
	if err != nil {
		t.Fatalf("stat hook: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("hook %s not executable: %v", wantPath, info.Mode())
	}
	trustStore := readFile(t, filepath.Join(stateHome, "projmux", "trusted-projects.json"))
	if !strings.Contains(trustStore, ".projmux/hooks/post-create") {
		t.Fatalf("trust store = %s, want hook script trusted", trustStore)
	}
	if !strings.Contains(trustStore, repo) {
		t.Fatalf("trust store = %s, want repo entry", trustStore)
	}
}

func TestSettingsHookMakerEditScriptUpdatesTrustHash(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configHome := t.TempDir()
	stateHome := t.TempDir()
	repo := t.TempDir()

	hookPath := filepath.Join(repo, ".projmux", "hooks", "post-create")
	mkdirAll(t, filepath.Dir(hookPath))
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\necho original\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Pre-seed trust with an outdated hash so we can verify the edit updates it.
	trustPath := filepath.Join(stateHome, "projmux", "trusted-projects.json")
	mkdirAll(t, filepath.Dir(trustPath))
	const staleHash = "0000000000000000000000000000000000000000000000000000000000000000"
	staleTrust := `{"` + repo + `":{"trusted_at":"2024-01-01T00:00:00Z","files":{".projmux/hooks/post-create":{"sha256":"` + staleHash + `","trusted_at":"2024-01-01T00:00:00Z"}}}}`
	if err := os.WriteFile(trustPath, []byte(staleTrust), 0o600); err != nil {
		t.Fatal(err)
	}

	var calls int
	cmd := hookMakerTestSettings(t, home, configHome, stateHome, repo, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			if !hasEntryValue(options.Entries, settingsActionPrefixHookEdit+"script:project:post-create") {
				t.Fatalf("entries = %#v, want script edit row", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixHookEdit + "script:project:post-create"}, nil
		case 2:
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		default:
			t.Fatalf("unexpected runner call %d", calls)
			return intpickercompat.Result{}, nil
		}
	})
	cmd.runCommand = func(name string, args ...string) error {
		if len(args) == 0 {
			return nil
		}
		path := args[len(args)-1]
		return os.WriteFile(path, []byte("#!/bin/sh\necho updated\n"), 0o755)
	}

	var stdout, stderr bytes.Buffer
	if err := cmd.runProjectHooksSection(&stdout, &stderr); err != nil {
		t.Fatalf("runProjectHooksSection: %v", err)
	}
	trustBody := readFile(t, trustPath)
	if strings.Contains(trustBody, staleHash) {
		t.Fatalf("trust store still has stale hash:\n%s", trustBody)
	}
	if !strings.Contains(trustBody, ".projmux/hooks/post-create") {
		t.Fatalf("trust store missing entry:\n%s", trustBody)
	}
}

func TestSettingsHookMakerHooksPageRendersBothSources(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	// Active script hook.
	scriptPath := filepath.Join(repo, ".projmux", "hooks", "post-create")
	mkdirAll(t, filepath.Dir(scriptPath))
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Declarative hook for the same event.
	cfgPath := filepath.Join(repo, ".projmux", "config.toml")
	if err := os.WriteFile(cfgPath, []byte(`
[hooks.post-create]
run = "echo declared"
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
	entries := cmd.projectHookEntries(cmd.resolveSettingsProjectContext())
	// Expect a script row (active) and a declarative row (active) for
	// post-create — distinguishable by their source label suffixes.
	if !hasEntryValue(entries, settingsActionPrefixHookEdit+"script:project:post-create") {
		t.Fatalf("entries missing script edit row: %#v", entries)
	}
	if !hasEntryValue(entries, settingsActionPrefixHookEdit+"declarative:project:post-create") {
		t.Fatalf("entries missing declarative edit row: %#v", entries)
	}
	assertEntryLabelContainsAll(t, entries, "post-create", "(script)")
	assertEntryLabelContainsAll(t, entries, "post-create", "(declarative)", "echo declared")
}

func TestSettingsHookMakerGlobalPageSkipsTrust(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configHome := t.TempDir()
	stateHome := t.TempDir()

	var editedPath string
	var calls int
	cmd := hookMakerTestSettings(t, home, configHome, stateHome, "", func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			if !hasEntryValue(options.Entries, settingsActionPrefixHookAdd+"global:post-create") {
				t.Fatalf("global hook entries = %#v, want add row", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixHookAdd + "global:post-create"}, nil
		case 2:
			// Branch picker still appears, but declarative is disabled for
			// global. Pick script.
			if !hasEntryValue(options.Entries, settingsActionPrefixHookAdd+"global:post-create:script") {
				t.Fatalf("branch picker = %#v, want script", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixHookAdd + "global:post-create:script"}, nil
		case 3:
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		default:
			t.Fatalf("unexpected runner call %d", calls)
			return intpickercompat.Result{}, nil
		}
	})
	cmd.runCommand = func(name string, args ...string) error {
		if len(args) == 0 {
			return nil
		}
		editedPath = args[len(args)-1]
		return nil
	}

	var stdout, stderr bytes.Buffer
	if err := cmd.runGlobalHooksSection(&stdout, &stderr); err != nil {
		t.Fatalf("runGlobalHooksSection: %v", err)
	}
	wantPath := filepath.Join(configHome, "projmux", "hooks", "post-create")
	if editedPath != wantPath {
		t.Fatalf("editor opened %q, want %q", editedPath, wantPath)
	}
	info, err := os.Stat(wantPath)
	if err != nil {
		t.Fatalf("stat global hook: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("global hook not executable: %v", info.Mode())
	}
	// Global edits must not write a trust store entry.
	if _, err := os.Stat(filepath.Join(stateHome, "projmux", "trusted-projects.json")); !os.IsNotExist(err) {
		t.Fatalf("trust store stat = %v, want missing (global hooks bypass trust)", err)
	}
}

func TestSettingsHookMakerExcludesInternalEventsFromAdd(t *testing.T) {
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

func TestSettingsHookMakerConfigSectionHasNoHookCommandsRow(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	cfgPath := filepath.Join(repo, ".projmux", "config.toml")
	writeFile(t, cfgPath, `
[hooks.post-create]
run = "echo a"

[hooks.pane-startup]
run = "echo b"
`)
	cmd := &settingsCommand{
		lookupEnv: func(name string) string {
			if name == "PROJMUX_CWD" {
				return repo
			}
			return ""
		},
	}
	ctx := cmd.resolveSettingsProjectContext()
	entries := cmd.projectConfigEntries(ctx)
	for _, entry := range entries {
		if strings.Contains(entry.Label, "Hook commands") {
			t.Fatalf("config.toml section still renders hook commands row: %#v", entry)
		}
	}
}
