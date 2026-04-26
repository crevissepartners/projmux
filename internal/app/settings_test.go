package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/es5h/projmux/internal/core/candidates"
	intfzf "github.com/es5h/projmux/internal/ui/fzf"
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
		"projmux 0.1.1",
		"https://github.com/es5h/projmux",
		"go install github.com/es5h/projmux/cmd/projmux@latest",
		"sidebar, sessions, projects",
		"new window, rename window/pane",
		"terminal sends CSI-u keys",
		"Ctrl-M sends 9011u",
		"bind alt/ctrl keys",
		"sendInput",
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
