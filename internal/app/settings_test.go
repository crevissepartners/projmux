package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/candidates"
	intfzf "github.com/crevissepartners/projmux/internal/ui/fzf"
	"github.com/crevissepartners/projmux/internal/version"
)

func TestSettingsHubSetsAIDefaultMode(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	ai := testAICommand(home)
	switcher := testSettingsSwitchCommand(t, &stubSwitchPinStore{})
	var calls int
	var rootOptions intfzf.Options
	var aiOptions intfzf.Options
	cmd := &settingsCommand{
		ai:       ai,
		switcher: switcher,
		runner: switchRunnerFunc(func(options intfzf.Options) (intfzf.Result, error) {
			calls++
			if calls == 1 {
				rootOptions = options
				return intfzf.Result{Key: "enter", Value: settingsSectionAI}, nil
			}
			if calls == 2 {
				aiOptions = options
				return intfzf.Result{Key: "enter", Value: settingsActionPrefixAI + "codex"}, nil
			}
			return intfzf.Result{}, nil
		}),
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := rootOptions.UI, "settings"; got != want {
		t.Fatalf("root settings UI = %q, want %q", got, want)
	}
	if got, want := rootOptions.Prompt, "Settings > "; got != want {
		t.Fatalf("root settings prompt = %q, want %q", got, want)
	}
	if got, want := rootOptions.Footer, "Enter: open  |  Esc/Alt+5/Ctrl+Alt+S: close"; got != want {
		t.Fatalf("root settings footer = %q, want %q", got, want)
	}
	if !hasEntryValue(rootOptions.Entries, settingsSectionAI) {
		t.Fatalf("root settings entries = %#v, want AI section", rootOptions.Entries)
	}
	if !hasEntryValue(rootOptions.Entries, settingsSectionProject) {
		t.Fatalf("root settings entries = %#v, want project picker section", rootOptions.Entries)
	}
	if !hasEntryValue(rootOptions.Entries, settingsSectionAbout) {
		t.Fatalf("root settings entries = %#v, want about section", rootOptions.Entries)
	}
	if got, want := aiOptions.UI, "settings-ai"; got != want {
		t.Fatalf("AI settings UI = %q, want %q", got, want)
	}
	if got, want := aiOptions.Prompt, "Settings > AI Settings > "; got != want {
		t.Fatalf("AI settings prompt = %q, want %q", got, want)
	}
	if !hasEntryValue(aiOptions.Entries, settingsBackValue) {
		t.Fatalf("AI settings entries = %#v, want back entry", aiOptions.Entries)
	}
	if got, want := readModeFile(t, home), "codex\n"; got != want {
		t.Fatalf("mode file = %q, want %q", got, want)
	}
}

func TestSettingsHubRunsProjectPickerActions(t *testing.T) {
	t.Parallel()

	store := &stubSwitchPinStore{}
	switcher := testSettingsSwitchCommand(t, store)
	var calls int
	cmd := &settingsCommand{
		ai:       testAICommand(t.TempDir()),
		switcher: switcher,
		runner: switchRunnerFunc(func(options intfzf.Options) (intfzf.Result, error) {
			calls++
			if calls == 1 {
				if got, want := options.UI, "settings"; got != want {
					t.Fatalf("settings UI = %q, want %q", got, want)
				}
				return intfzf.Result{Key: "enter", Value: settingsSectionProject}, nil
			}
			if calls == 2 {
				if got, want := options.UI, "settings-project-picker"; got != want {
					t.Fatalf("project settings UI = %q, want %q", got, want)
				}
				if !hasEntryValue(options.Entries, settingsBackValue) {
					t.Fatalf("project settings entries = %#v, want back entry", options.Entries)
				}
				return intfzf.Result{Key: "enter", Value: settingsActionPrefixSwitch + "add:/home/tester/source/repos/app"}, nil
			}
			return intfzf.Result{}, nil
		}),
	}

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := store.addCalls, []string{"/home/tester/source/repos/app"}; !equalStrings(got, want) {
		t.Fatalf("add calls = %q, want %q", got, want)
	}
	if got, want := stdout.String(), "pinned: /home/tester/source/repos/app\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestSettingsHubShowsAboutSection(t *testing.T) {
	t.Parallel()

	var calls int
	var aboutOptions intfzf.Options
	cmd := &settingsCommand{
		ai:       testAICommand(t.TempDir()),
		switcher: testSettingsSwitchCommand(t, &stubSwitchPinStore{}),
		runner: switchRunnerFunc(func(options intfzf.Options) (intfzf.Result, error) {
			calls++
			switch calls {
			case 1:
				return intfzf.Result{Key: "enter", Value: settingsSectionAbout}, nil
			case 2:
				aboutOptions = options
				return intfzf.Result{Key: "enter", Value: settingsNoopValue}, nil
			case 3:
				if got, want := options.UI, "settings-about"; got != want {
					t.Fatalf("settings about UI after noop = %q, want %q", got, want)
				}
				return intfzf.Result{Key: "enter", Value: settingsBackValue}, nil
			case 4:
				return intfzf.Result{}, nil
			default:
				t.Fatalf("unexpected settings picker call %d", calls)
				return intfzf.Result{}, nil
			}
		}),
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := aboutOptions.UI, "settings-about"; got != want {
		t.Fatalf("settings about UI = %q, want %q", got, want)
	}
	if got, want := aboutOptions.Prompt, "Settings > About > "; got != want {
		t.Fatalf("settings about prompt = %q, want %q", got, want)
	}
	if !hasEntryValue(aboutOptions.Entries, settingsBackValue) {
		t.Fatalf("settings about entries = %#v, want back entry", aboutOptions.Entries)
	}
	for _, want := range []string{
		"projmux " + version.String(),
		"https://github.com/crevissepartners/projmux",
		"go install github.com/crevissepartners/projmux/cmd/projmux@latest",
		"sidebar, sessions, projects",
		"new window, rename window/pane",
		"terminal sends CSI-u keys",
		"Ctrl-M sends 9011u",
		"bind alt/ctrl keys",
		"tmux/meta sequences",
		"docs/keybindings.md",
	} {
		if !hasEntryLabelContaining(aboutOptions.Entries, want) {
			t.Fatalf("settings about entries = %#v, want label containing %q", aboutOptions.Entries, want)
		}
	}
}

func TestSettingsHubAddProjectScansFilesystem(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	mkdirAll(t, filepath.Join(home, "source", "repos", "app"))
	mkdirAll(t, filepath.Join(home, "work", "service", "nested"))
	mkdirAll(t, filepath.Join(home, ".config"))
	mkdirAll(t, filepath.Join(home, ".cache"))

	store := &stubSwitchPinStore{}
	switcher := testSettingsSwitchCommandWithHome(t, home, store)
	var calls int
	cmd := &settingsCommand{
		ai:       testAICommand(t.TempDir()),
		switcher: switcher,
		runner: switchRunnerFunc(func(options intfzf.Options) (intfzf.Result, error) {
			calls++
			switch calls {
			case 1:
				return intfzf.Result{Key: "enter", Value: settingsSectionProject}, nil
			case 2:
				if !hasEntryValue(options.Entries, settingsProjectAdd) {
					t.Fatalf("project settings entries = %#v, want Add Project", options.Entries)
				}
				if !hasEntryValue(options.Entries, settingsProjectPins) {
					t.Fatalf("project settings entries = %#v, want Pinned Projects", options.Entries)
				}
				return intfzf.Result{Key: "enter", Value: settingsProjectAdd}, nil
			case 3:
				if got, want := options.UI, "settings-project-add"; got != want {
					t.Fatalf("add project UI = %q, want %q", got, want)
				}
				app := filepath.Join(home, "source", "repos", "app")
				if !hasEntryValue(options.Entries, settingsActionPrefixSwitch+"add:"+app) {
					t.Fatalf("add project entries = %#v, want scanned app", options.Entries)
				}
				if !hasEntryValue(options.Entries, settingsActionPrefixSwitch+"add:"+filepath.Join(home, ".config")) {
					t.Fatalf("add project entries = %#v, want hidden whitelist entry", options.Entries)
				}
				if hasEntryValue(options.Entries, settingsActionPrefixSwitch+"add:"+filepath.Join(home, ".cache")) {
					t.Fatalf("add project entries = %#v, want hidden non-whitelist skipped", options.Entries)
				}
				return intfzf.Result{Key: "enter", Value: settingsActionPrefixSwitch + "add:" + app}, nil
			default:
				return intfzf.Result{}, nil
			}
		}),
	}

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := store.addCalls, []string{filepath.Join(home, "source", "repos", "app")}; !equalStrings(got, want) {
		t.Fatalf("add calls = %q, want %q", got, want)
	}
}

func TestSettingsHubPinnedProjectsRemovesPins(t *testing.T) {
	t.Parallel()

	pin := "/home/tester/source/repos/app"
	store := &stubSwitchPinStore{list: []string{pin}}
	switcher := testSettingsSwitchCommand(t, store)
	var calls int
	cmd := &settingsCommand{
		ai:       testAICommand(t.TempDir()),
		switcher: switcher,
		runner: switchRunnerFunc(func(options intfzf.Options) (intfzf.Result, error) {
			calls++
			switch calls {
			case 1:
				return intfzf.Result{Key: "enter", Value: settingsSectionProject}, nil
			case 2:
				return intfzf.Result{Key: "enter", Value: settingsProjectPins}, nil
			case 3:
				if got, want := options.UI, "settings-project-pins"; got != want {
					t.Fatalf("pinned projects UI = %q, want %q", got, want)
				}
				if !hasEntryValue(options.Entries, settingsActionPrefixSwitch+"clear") {
					t.Fatalf("pinned project entries = %#v, want clear", options.Entries)
				}
				return intfzf.Result{Key: "enter", Value: settingsActionPrefixSwitch + "pin:" + pin}, nil
			default:
				return intfzf.Result{}, nil
			}
		}),
	}

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := store.toggleCalls, []string{pin}; !equalStrings(got, want) {
		t.Fatalf("toggle calls = %q, want %q", got, want)
	}
}

func TestProjectPickerEntriesIncludesWorkdirsRows(t *testing.T) {
	t.Parallel()

	const home = "/home/tester"
	cmd := &settingsCommand{
		switcher: &switchCommand{
			homeDir:      func() (string, error) { return home, nil },
			lookupEnv:    func(string) string { return "" },
			tmuxProjdir:  emptyTmuxOption,
			loadProjdir:  func(string) (string, error) { return "", nil },
			saveProjdir:  func(string, string) error { return nil },
			loadWorkdirs: func(string) ([]string, error) { return nil, nil },
		},
	}

	entries := cmd.projectPickerEntries()
	if !hasEntryValue(entries, settingsWorkdirAdd) {
		t.Fatalf("project picker entries = %#v, want Add Workdir entry", entries)
	}
	if !hasEntryValue(entries, settingsWorkdirList) {
		t.Fatalf("project picker entries = %#v, want Workdirs entry", entries)
	}
	if !hasEntryLabelContaining(entries, "Add Workdir...") {
		t.Fatalf("project picker entries = %#v, want 'Add Workdir...' label", entries)
	}
	if !hasEntryLabelContaining(entries, "Workdirs") {
		t.Fatalf("project picker entries = %#v, want 'Workdirs' label", entries)
	}
}

func TestSettingsHubAddWorkdirAppendsToSavedFile(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	mkdirAll(t, filepath.Join(home, "source", "repos", "app"))

	switcher := testSettingsSwitchCommandWithHome(t, home, &stubSwitchPinStore{})
	switcher.loadWorkdirs = func(string) ([]string, error) { return nil, nil }

	var calls int
	cmd := &settingsCommand{
		ai:       testAICommand(t.TempDir()),
		switcher: switcher,
		runner: switchRunnerFunc(func(options intfzf.Options) (intfzf.Result, error) {
			calls++
			switch calls {
			case 1:
				return intfzf.Result{Key: "enter", Value: settingsSectionProject}, nil
			case 2:
				if !hasEntryValue(options.Entries, settingsWorkdirAdd) {
					t.Fatalf("project settings entries = %#v, want Add Workdir", options.Entries)
				}
				return intfzf.Result{Key: "enter", Value: settingsWorkdirAdd}, nil
			case 3:
				if got, want := options.UI, "settings-workdir-add"; got != want {
					t.Fatalf("add workdir UI = %q, want %q", got, want)
				}
				app := filepath.Join(home, "source", "repos", "app")
				want := settingsActionPrefixWorkdir + "add:" + app
				if !hasEntryValue(options.Entries, want) {
					t.Fatalf("add workdir entries = %#v, want value %q", options.Entries, want)
				}
				return intfzf.Result{Key: "enter", Value: want}, nil
			default:
				return intfzf.Result{}, nil
			}
		}),
	}

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	saved, err := readWorkdirsFile(t, home)
	if err != nil {
		t.Fatalf("readWorkdirsFile() error = %v", err)
	}
	app := filepath.Join(home, "source", "repos", "app")
	if !equalStrings(saved, []string{app}) {
		t.Fatalf("saved workdirs = %#v, want [%q]", saved, app)
	}
	if got, want := stdout.String(), "added workdir: "+app+"\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestSettingsHubWorkdirsListRemovesSavedEntry(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	target := filepath.Join(home, "source", "repos", "app")
	if err := os.MkdirAll(filepath.Join(home, ".config", "projmux"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "projmux", "workdirs"), []byte(target+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	switcher := testSettingsSwitchCommandWithHome(t, home, &stubSwitchPinStore{})
	switcher.loadWorkdirs = func(homeDir string) ([]string, error) {
		// Use the real loader so removal is observed end-to-end via the saved file.
		return loadSavedWorkdirsFromFile(homeDir), nil
	}

	var calls int
	cmd := &settingsCommand{
		ai:       testAICommand(t.TempDir()),
		switcher: switcher,
		runner: switchRunnerFunc(func(options intfzf.Options) (intfzf.Result, error) {
			calls++
			switch calls {
			case 1:
				return intfzf.Result{Key: "enter", Value: settingsSectionProject}, nil
			case 2:
				return intfzf.Result{Key: "enter", Value: settingsWorkdirList}, nil
			case 3:
				if got, want := options.UI, "settings-workdirs"; got != want {
					t.Fatalf("workdirs list UI = %q, want %q", got, want)
				}
				want := settingsActionPrefixWorkdir + "remove:" + target
				if !hasEntryValue(options.Entries, want) {
					t.Fatalf("workdirs list entries = %#v, want %q", options.Entries, want)
				}
				return intfzf.Result{Key: "enter", Value: want}, nil
			case 4:
				// After remove, list should be empty (just back + placeholder).
				return intfzf.Result{Key: "enter", Value: settingsBackValue}, nil
			default:
				return intfzf.Result{}, nil
			}
		}),
	}

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	saved, err := readWorkdirsFile(t, home)
	if err != nil {
		t.Fatalf("readWorkdirsFile() error = %v", err)
	}
	if len(saved) != 0 {
		t.Fatalf("saved workdirs = %#v, want empty", saved)
	}
	if got, want := stdout.String(), "removed workdir: "+target+"\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestWorkdirListEntriesSurfacesEnvSources(t *testing.T) {
	t.Parallel()

	cmd := &settingsCommand{
		switcher: &switchCommand{
			homeDir: func() (string, error) { return "/home/tester", nil },
			lookupEnv: func(name string) string {
				if name == managedRootsEnvVar {
					return "/env/one:/env/two"
				}
				return ""
			},
			tmuxProjdir:  emptyTmuxOption,
			loadProjdir:  func(string) (string, error) { return "", nil },
			saveProjdir:  func(string, string) error { return nil },
			loadWorkdirs: func(string) ([]string, error) { return []string{"/saved/a"}, nil },
		},
	}

	entries, err := cmd.workdirListEntries()
	if err != nil {
		t.Fatalf("workdirListEntries() error = %v", err)
	}
	if !hasEntryLabelContaining(entries, "/saved/a") {
		t.Fatalf("workdir list entries = %#v, want saved entry", entries)
	}
	// The env source row now renders the variable name in the label column
	// and the colon-separated value in the value column, with a "(env, ...)"
	// source annotation. Verify the parts appear; the exact spacing comes
	// from settingsLabelInfo padding.
	if !hasEntryLabelContaining(entries, managedRootsEnvVar) {
		t.Fatalf("workdir list entries = %#v, want env variable name", entries)
	}
	if !hasEntryLabelContaining(entries, "/env/one:/env/two") {
		t.Fatalf("workdir list entries = %#v, want env value", entries)
	}
	if !hasEntryLabelContaining(entries, "(env, read-only)") {
		t.Fatalf("workdir list entries = %#v, want env source annotation", entries)
	}
}

func TestAddWorkdirEntriesIncludesTypedRow(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv(projdirEnvVar, "")

	home := t.TempDir()
	mkdirAll(t, filepath.Join(home, "source", "repos", "app"))

	switcher := testSettingsSwitchCommandWithHome(t, home, &stubSwitchPinStore{})
	switcher.loadWorkdirs = func(string) ([]string, error) { return nil, nil }

	var addOptions intfzf.Options
	var calls int
	cmd := &settingsCommand{
		ai:       testAICommand(t.TempDir()),
		switcher: switcher,
		runner: switchRunnerFunc(func(options intfzf.Options) (intfzf.Result, error) {
			calls++
			switch calls {
			case 1:
				return intfzf.Result{Key: "enter", Value: settingsSectionProject}, nil
			case 2:
				return intfzf.Result{Key: "enter", Value: settingsWorkdirAdd}, nil
			case 3:
				addOptions = options
				return intfzf.Result{Key: "enter", Value: settingsBackValue}, nil
			default:
				return intfzf.Result{}, nil
			}
		}),
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := addOptions.UI, "settings-workdir-add"; got != want {
		t.Fatalf("add workdir UI = %q, want %q", got, want)
	}
	if !hasEntryValue(addOptions.Entries, settingsWorkdirTyped) {
		t.Fatalf("add workdir entries = %#v, want typed-entry row", addOptions.Entries)
	}
	if !hasEntryLabelContaining(addOptions.Entries, "Type path manually") {
		t.Fatalf("add workdir entries = %#v, want 'Type path manually' label", addOptions.Entries)
	}
}

func TestSettingsHubAddWorkdirTypedAppendsTypedPath(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv(projdirEnvVar, "")

	home := t.TempDir()
	typed := filepath.Join(home, "mnt", "c", "Users", "me", "code")
	mkdirAll(t, typed)

	switcher := testSettingsSwitchCommandWithHome(t, home, &stubSwitchPinStore{})
	switcher.loadWorkdirs = func(string) ([]string, error) { return nil, nil }

	var typedOptions intfzf.Options
	var calls int
	cmd := &settingsCommand{
		ai:       testAICommand(t.TempDir()),
		switcher: switcher,
		runner: switchRunnerFunc(func(options intfzf.Options) (intfzf.Result, error) {
			calls++
			switch calls {
			case 1:
				return intfzf.Result{Key: "enter", Value: settingsSectionProject}, nil
			case 2:
				return intfzf.Result{Key: "enter", Value: settingsWorkdirAdd}, nil
			case 3:
				if !hasEntryValue(options.Entries, settingsWorkdirTyped) {
					t.Fatalf("add workdir entries = %#v, want typed row", options.Entries)
				}
				return intfzf.Result{Key: "enter", Value: settingsWorkdirTyped}, nil
			case 4:
				typedOptions = options
				return intfzf.Result{Key: "enter", Query: typed}, nil
			case 5:
				// After typed flow returns, the project picker reopens. Close it.
				return intfzf.Result{Key: "enter", Value: settingsBackValue}, nil
			default:
				return intfzf.Result{}, nil
			}
		}),
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := cmd.Run(nil, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := typedOptions.UI, "settings-workdir-typed"; got != want {
		t.Fatalf("typed picker UI = %q, want %q", got, want)
	}
	if !typedOptions.AcceptQuery {
		t.Fatalf("typed picker AcceptQuery = false, want true")
	}
	if got, want := typedOptions.Prompt, "Type workdir path > "; got != want {
		t.Fatalf("typed picker prompt = %q, want %q", got, want)
	}

	saved, err := readWorkdirsFile(t, home)
	if err != nil {
		t.Fatalf("readWorkdirsFile() error = %v", err)
	}
	if !equalStrings(saved, []string{typed}) {
		t.Fatalf("saved workdirs = %#v, want [%q]", saved, typed)
	}
	if got, want := stdout.String(), "added workdir: "+typed+"\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestSettingsHubAddWorkdirTypedRejectsRelativePath(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv(projdirEnvVar, "")

	home := t.TempDir()
	switcher := testSettingsSwitchCommandWithHome(t, home, &stubSwitchPinStore{})
	switcher.loadWorkdirs = func(string) ([]string, error) { return nil, nil }

	var calls int
	cmd := &settingsCommand{
		ai:       testAICommand(t.TempDir()),
		switcher: switcher,
		runner: switchRunnerFunc(func(options intfzf.Options) (intfzf.Result, error) {
			calls++
			switch calls {
			case 1:
				return intfzf.Result{Key: "enter", Value: settingsSectionProject}, nil
			case 2:
				return intfzf.Result{Key: "enter", Value: settingsWorkdirAdd}, nil
			case 3:
				return intfzf.Result{Key: "enter", Value: settingsWorkdirTyped}, nil
			case 4:
				return intfzf.Result{Key: "enter", Query: "relative/path"}, nil
			case 5:
				// After typed-flow falls back, settings should return to the
				// project picker section. Close to terminate the run.
				return intfzf.Result{Key: "enter", Value: settingsBackValue}, nil
			default:
				return intfzf.Result{}, nil
			}
		}),
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := cmd.Run(nil, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := stderr.String(); !strings.Contains(got, "absolute path") {
		t.Fatalf("stderr = %q, want absolute-path error", got)
	}
	saved, err := readWorkdirsFile(t, home)
	if err != nil {
		t.Fatalf("readWorkdirsFile() error = %v", err)
	}
	if len(saved) != 0 {
		t.Fatalf("saved workdirs = %#v, want empty after rejected typed input", saved)
	}
}

func TestSettingsHubBackReturnsToRoot(t *testing.T) {
	t.Parallel()

	var calls int
	cmd := &settingsCommand{
		ai:       testAICommand(t.TempDir()),
		switcher: testSettingsSwitchCommand(t, &stubSwitchPinStore{}),
		runner: switchRunnerFunc(func(options intfzf.Options) (intfzf.Result, error) {
			calls++
			switch calls {
			case 1:
				return intfzf.Result{Key: "enter", Value: settingsSectionAI}, nil
			case 2:
				return intfzf.Result{Key: "enter", Value: settingsBackValue}, nil
			case 3:
				if got, want := options.UI, "settings"; got != want {
					t.Fatalf("settings UI after back = %q, want %q", got, want)
				}
				return intfzf.Result{}, nil
			default:
				t.Fatalf("unexpected settings picker call %d", calls)
				return intfzf.Result{}, nil
			}
		}),
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestSettingsHubRejectsArguments(t *testing.T) {
	t.Parallel()

	cmd := &settingsCommand{}
	var stderr bytes.Buffer
	err := cmd.Run([]string{"extra"}, &bytes.Buffer{}, &stderr)
	if err == nil {
		t.Fatal("Run() error = nil, want error")
	}
	if !strings.Contains(stderr.String(), "projmux settings") {
		t.Fatalf("stderr = %q, want usage", stderr.String())
	}
}

func testSettingsSwitchCommand(t *testing.T, store *stubSwitchPinStore) *switchCommand {
	t.Helper()
	return testSettingsSwitchCommandWithHome(t, "/home/tester", store)
}

func testSettingsSwitchCommandWithHome(t *testing.T, home string, store *stubSwitchPinStore) *switchCommand {
	t.Helper()

	return &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{filepath.Join(home, "source", "repos", "app")}, nil
		},
		pinStore: func() (switchPinStore, error) { return store, nil },
		runner: switchRunnerFunc(func(intfzf.Options) (intfzf.Result, error) {
			return intfzf.Result{}, nil
		}),
		sessions:   &capturingSwitchSessionExecutor{},
		identity:   stubSwitchIdentityResolver{name: "app"},
		validate:   func(string) error { return nil },
		homeDir:    func() (string, error) { return home, nil },
		workingDir: func() (string, error) { return filepath.Join(home, "source", "repos", "app"), nil },
		lookupEnv: func(name string) string {
			if name == repoRootEnvVar {
				return filepath.Join(home, "source", "repos")
			}
			return ""
		},
	}
}

func TestCurrentProjdirInfoSourcePriority(t *testing.T) {
	t.Parallel()

	const home = "/home/tester"
	tests := []struct {
		name       string
		lookup     func(string) string
		tmuxOption func() string
		load       func(string) (string, error)
		wantValue  string
		wantSource string
	}{
		{
			name: "PROJDIR env wins",
			lookup: func(name string) string {
				switch name {
				case projdirEnvVar:
					return "/from/projdir"
				case repoRootEnvVar:
					return "/from/rp"
				}
				return ""
			},
			tmuxOption: func() string { return "/from/tmux" },
			load:       func(string) (string, error) { return "/from/saved", nil },
			wantValue:  "/from/projdir",
			wantSource: projdirSourcePROJDIRenv,
		},
		{
			name: "tmux option used when PROJDIR empty",
			lookup: func(name string) string {
				if name == repoRootEnvVar {
					return "/from/rp"
				}
				return ""
			},
			tmuxOption: func() string { return "/from/tmux" },
			load:       func(string) (string, error) { return "/from/saved", nil },
			wantValue:  "/from/tmux",
			wantSource: projdirSourceTmuxOption,
		},
		{
			name: "RP env used when PROJDIR and tmux empty",
			lookup: func(name string) string {
				if name == repoRootEnvVar {
					return "/from/rp"
				}
				return ""
			},
			tmuxOption: emptyTmuxOption,
			load:       func(string) (string, error) { return "/from/saved", nil },
			wantValue:  "/from/rp",
			wantSource: projdirSourceRPEnv,
		},
		{
			name:       "saved file used when env unset",
			lookup:     func(string) string { return "" },
			tmuxOption: emptyTmuxOption,
			load:       func(string) (string, error) { return "/from/saved", nil },
			wantValue:  "/from/saved",
			wantSource: projdirSourceSaved,
		},
		{
			name:       "default fallback when nothing set",
			lookup:     func(string) string { return "" },
			tmuxOption: emptyTmuxOption,
			load:       func(string) (string, error) { return "", nil },
			wantValue:  filepath.Clean(filepath.Join(home, "source", "repos")),
			wantSource: projdirSourceDefault,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			saveCalls := 0
			cmd := &switchCommand{
				homeDir:     func() (string, error) { return home, nil },
				lookupEnv:   tc.lookup,
				tmuxProjdir: tc.tmuxOption,
				loadProjdir: tc.load,
				saveProjdir: func(string, string) error {
					saveCalls++
					return nil
				},
			}

			value, source, err := cmd.currentProjdirInfo()
			if err != nil {
				t.Fatalf("currentProjdirInfo() error = %v", err)
			}
			if value != tc.wantValue {
				t.Fatalf("value = %q, want %q", value, tc.wantValue)
			}
			if source != tc.wantSource {
				t.Fatalf("source = %q, want %q", source, tc.wantSource)
			}
			if saveCalls != 0 {
				t.Fatalf("save calls = %d, want 0 (currentProjdirInfo must not memoize)", saveCalls)
			}
		})
	}
}

func TestProjectPickerEntriesIncludesProjdirRow(t *testing.T) {
	t.Parallel()

	const home = "/home/tester"
	cmd := &settingsCommand{
		switcher: &switchCommand{
			homeDir: func() (string, error) { return home, nil },
			lookupEnv: func(name string) string {
				if name == projdirEnvVar {
					return "/from/projdir"
				}
				return ""
			},
			tmuxProjdir: emptyTmuxOption,
			loadProjdir: func(string) (string, error) { return "", nil },
			saveProjdir: func(string, string) error { return nil },
		},
	}

	entries := cmd.projectPickerEntries()
	if !hasEntryLabelContaining(entries, "Project Root") {
		t.Fatalf("project picker entries = %#v, want Project Root row", entries)
	}
	if !hasEntryLabelContaining(entries, "/from/projdir") {
		t.Fatalf("project picker entries = %#v, want resolved value in label", entries)
	}
	if !hasEntryLabelContaining(entries, "("+projdirSourcePROJDIRenv+")") {
		t.Fatalf("project picker entries = %#v, want source label", entries)
	}
	if !hasEntryLabelContaining(entries, "Override via PROJDIR env") {
		t.Fatalf("project picker entries = %#v, want override hint row", entries)
	}
}

func hasEntryValue(entries []intfzf.Entry, value string) bool {
	for _, entry := range entries {
		if entry.Value == value {
			return true
		}
	}
	return false
}

func hasEntryLabelContaining(entries []intfzf.Entry, value string) bool {
	for _, entry := range entries {
		if strings.Contains(entry.Label, value) {
			return true
		}
	}
	return false
}

func mkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", path, err)
	}
}

func readWorkdirsFile(t *testing.T, home string) ([]string, error) {
	t.Helper()
	path := filepath.Join(home, ".config", "projmux", "workdirs")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := []string{}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out, nil
}

func loadSavedWorkdirsFromFile(home string) []string {
	path := filepath.Join(home, ".config", "projmux", "workdirs")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	out := []string{}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}
