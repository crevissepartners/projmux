package app

import (
	"bytes"
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
	if got, want := rootOptions.Footer, "[projmux]\nEnter: open  |  Esc/Alt+5/Ctrl+Alt+S: close"; got != want {
		t.Fatalf("root settings footer = %q, want %q", got, want)
	}
	if !hasEntryValue(rootOptions.Entries, settingsSectionAI) {
		t.Fatalf("root settings entries = %#v, want AI section", rootOptions.Entries)
	}
	if !hasEntryValue(rootOptions.Entries, settingsSectionProject) {
		t.Fatalf("root settings entries = %#v, want project picker section", rootOptions.Entries)
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

	return &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{"/home/tester/source/repos/app"}, nil
		},
		pinStore: func() (switchPinStore, error) { return store, nil },
		runner: switchRunnerFunc(func(intfzf.Options) (intfzf.Result, error) {
			return intfzf.Result{}, nil
		}),
		sessions:   &capturingSwitchSessionExecutor{},
		identity:   stubSwitchIdentityResolver{name: "app"},
		validate:   func(string) error { return nil },
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/home/tester/source/repos/app", nil },
		lookupEnv: func(name string) string {
			if name == repoRootEnvVar {
				return "/home/tester/source/repos"
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
