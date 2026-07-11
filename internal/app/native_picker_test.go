package app

import (
	"os"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/theme"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

func nativePickerFromCompatRunner(r intpickercompat.Runner) intpicker.Runner {
	return pickerRunnerFunc(func(options intpicker.Options) (intpicker.Result, error) {
		result, err := r.Run(intpickercompat.OptionsFromPicker(options))
		if err != nil {
			return intpicker.Result{}, err
		}
		return intpickercompat.ResultToPicker(result), nil
	})
}

func TestRunPickerOptionBackendUsesNativeWhenRequested(t *testing.T) {
	t.Parallel()

	var compatCalled bool
	native := pickerRunnerFunc(func(options intpicker.Options) (intpicker.Result, error) {
		if options.UI != "settings" {
			t.Fatalf("native UI = %q, want settings", options.UI)
		}
		if len(options.Items) != 1 || options.Items[0].Value != "ai" {
			t.Fatalf("native items = %#v, want compat entries converted", options.Items)
		}
		return intpicker.Result{Key: "enter", Value: "ai"}, nil
	})

	result, err := runPickerOptionBackend(
		func() (string, error) { return "", nil },
		func(name string) string {
			if name == intpicker.BackendEnv {
				return "native"
			}
			return ""
		},
		native,
		switchRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
			compatCalled = true
			return intpickercompat.Result{}, nil
		}),
		intpickercompat.Options{
			UI:      "settings",
			Entries: []intpickercompat.Entry{{Label: "AI Settings", Value: "ai"}},
		},
	)
	if err != nil {
		t.Fatalf("runPickerOptionBackend() error = %v", err)
	}
	if compatCalled {
		t.Fatal("compat runner was called for native backend")
	}
	if result.Key != "enter" || result.Value != "ai" {
		t.Fatalf("result = %#v, want native selection", result)
	}
}

func TestRunPickerOptionBackendUsesSavedNativeBackend(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	paths := config.DefaultPaths(configHome, t.TempDir())
	if err := config.SavePickerBackendFile(paths.PickerBackendFile(), config.PickerBackendNative); err != nil {
		t.Fatalf("SavePickerBackendFile() error = %v", err)
	}

	var compatCalled bool
	nativeCalled := false
	result, err := runPickerOptionBackend(
		func() (string, error) { return "", nil },
		func(name string) string {
			switch name {
			case intpicker.BackendEnv:
				return ""
			case "XDG_CONFIG_HOME":
				return configHome
			case "XDG_STATE_HOME":
				return t.TempDir()
			default:
				return os.Getenv(name)
			}
		},
		pickerRunnerFunc(func(options intpicker.Options) (intpicker.Result, error) {
			nativeCalled = true
			return intpicker.Result{Key: "enter", Value: "ai"}, nil
		}),
		switchRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
			compatCalled = true
			return intpickercompat.Result{}, nil
		}),
		intpickercompat.Options{
			UI:      "settings",
			Entries: []intpickercompat.Entry{{Label: "AI Settings", Value: "ai"}},
		},
	)
	if err != nil {
		t.Fatalf("runPickerOptionBackend() error = %v", err)
	}
	if !nativeCalled {
		t.Fatal("native runner was not called for saved native picker backend")
	}
	if compatCalled {
		t.Fatal("compat runner was called for saved native picker backend")
	}
	if result.Value != "ai" {
		t.Fatalf("result = %#v, want native selection", result)
	}
}

func TestRunPickerOptionBackendDefaultsToNativeBackend(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	var compatCalled bool
	var nativeCalled bool
	result, err := runPickerOptionBackend(
		func() (string, error) { return "", nil },
		func(name string) string {
			switch name {
			case intpicker.BackendEnv:
				return ""
			case "XDG_CONFIG_HOME":
				return configHome
			case "XDG_STATE_HOME":
				return t.TempDir()
			default:
				return os.Getenv(name)
			}
		},
		pickerRunnerFunc(func(options intpicker.Options) (intpicker.Result, error) {
			nativeCalled = true
			return intpicker.Result{Key: "enter", Value: "ai"}, nil
		}),
		switchRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
			compatCalled = true
			return intpickercompat.Result{}, nil
		}),
		intpickercompat.Options{UI: "settings"},
	)
	if err != nil {
		t.Fatalf("runPickerOptionBackend() error = %v", err)
	}
	if !nativeCalled {
		t.Fatal("native runner was not called for default picker backend")
	}
	if compatCalled {
		t.Fatal("compat runner was called for default picker backend")
	}
	if result.Value != "ai" {
		t.Fatalf("result = %#v, want native selection", result)
	}
}

func TestRunPickerOptionBackendPopulatesFallbackTheme(t *testing.T) {
	t.Parallel()

	var gotTheme *theme.EffectiveTheme
	_, err := runPickerOptionBackend(
		func() (string, error) { return "", nil },
		func(string) string { return "" },
		pickerRunnerFunc(func(options intpicker.Options) (intpicker.Result, error) {
			gotTheme = options.Theme
			return intpicker.Result{Key: "enter", Value: "ai"}, nil
		}),
		nil,
		intpickercompat.Options{UI: "settings"},
	)
	if err != nil {
		t.Fatalf("runPickerOptionBackend() error = %v", err)
	}
	if gotTheme == nil {
		t.Fatal("native picker Theme = nil, want fallback effective theme")
	}
	if gotTheme.Background.Source != theme.SourceFallback || gotTheme.Foreground.Source != theme.SourceFallback {
		t.Fatalf("native picker Theme = %#v, want fallback effective theme", gotTheme)
	}
}

func TestRunPickerOptionBackendPreservesSuppliedTheme(t *testing.T) {
	t.Parallel()

	supplied := theme.ResolveTheme(theme.ThemeConfig{
		Background: "#010203",
		Foreground: "#aabbcc",
	})
	var gotTheme *theme.EffectiveTheme
	_, err := runPickerOptionBackend(
		func() (string, error) { return "", nil },
		func(string) string { return "" },
		pickerRunnerFunc(func(options intpicker.Options) (intpicker.Result, error) {
			gotTheme = options.Theme
			return intpicker.Result{Key: "enter", Value: "ai"}, nil
		}),
		nil,
		intpickercompat.Options{UI: "settings", Theme: &supplied},
	)
	if err != nil {
		t.Fatalf("runPickerOptionBackend() error = %v", err)
	}
	if gotTheme == nil {
		t.Fatal("native picker Theme = nil, want supplied effective theme")
	}
	if gotTheme.Background.Value.Hex != "#010203" || gotTheme.Foreground.Value.Hex != "#aabbcc" {
		t.Fatalf("native picker Theme = %#v, want supplied effective theme", gotTheme)
	}
}

func TestRunPickerOptionBackendIgnoresDeprecatedBackendEnvOverride(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	paths := config.DefaultPaths(configHome, t.TempDir())
	if err := config.SavePickerBackendFile(paths.PickerBackendFile(), config.PickerBackendNative); err != nil {
		t.Fatalf("SavePickerBackendFile() error = %v", err)
	}

	var nativeCalled bool
	var compatCalled bool
	result, err := runPickerOptionBackend(
		func() (string, error) { return "", nil },
		func(name string) string {
			if name == intpicker.BackendEnv {
				return "fzf"
			}
			return os.Getenv(name)
		},
		pickerRunnerFunc(func(intpicker.Options) (intpicker.Result, error) {
			nativeCalled = true
			return intpicker.Result{Key: "enter", Value: "ai"}, nil
		}),
		switchRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
			compatCalled = true
			return intpickercompat.Result{Key: "enter", Value: "fzf"}, nil
		}),
		intpickercompat.Options{UI: "settings"},
	)
	if err != nil {
		t.Fatalf("runPickerOptionBackend() error = %v", err)
	}
	if !nativeCalled {
		t.Fatal("native runner was not called despite deprecated backend env")
	}
	if compatCalled {
		t.Fatal("compat runner was called despite deprecated backend env")
	}
	if result.Value != "ai" {
		t.Fatalf("result = %#v, want native selection", result)
	}
}

func TestRunPickerOptionBackendErrorsWhenNativeMissingWithoutCallingCompatRunner(t *testing.T) {
	t.Parallel()

	var compatCalled bool
	_, err := runPickerOptionBackend(
		func() (string, error) { return "", nil },
		func(name string) string {
			if name == intpicker.BackendEnv {
				return "fzf"
			}
			return ""
		},
		nil,
		switchRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
			compatCalled = true
			return intpickercompat.Result{Key: "enter", Value: "fzf"}, nil
		}),
		intpickercompat.Options{UI: "settings"},
	)

	if err == nil || !strings.Contains(err.Error(), "native picker is not configured") {
		t.Fatalf("runPickerOptionBackend() error = %v, want native picker error", err)
	}
	if compatCalled {
		t.Fatal("compat runner was called when native picker was missing")
	}
}

func TestProductionPickerConstructorsDoNotCreateCompatRunner(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	if cmd := newAICommand(); cmd.runner != nil {
		t.Fatal("newAICommand() created compat runner")
	}
	if cmd := newSettingsCommand(testAICommand(t.TempDir()), testSettingsSwitchCommand(t, &stubSwitchPinStore{}), nil, nil); cmd.runner != nil {
		t.Fatal("newSettingsCommand() created compat runner")
	}
	if cmd := newSwitchCommand(); cmd.runner != nil {
		t.Fatal("newSwitchCommand() created compat runner")
	}
	if cmd := newSessionsCommand(); cmd.runner != nil {
		t.Fatal("newSessionsCommand() created compat runner")
	}
	if cmd := newNotifyCommand(newDefaultLivePaneLister()); cmd.picker != nil {
		t.Fatal("newNotifyCommand() created compat runner")
	}
}

func TestPickerOptionsFromCompatPickerMapsCandidatesWhenEntriesAreEmpty(t *testing.T) {
	t.Parallel()

	options := pickerOptionsFromCompatPicker(intpickercompat.Options{
		UI:         "compat",
		Candidates: []string{"/tmp/project-a", " ", "/tmp/project-b"},
	})

	if len(options.Items) != 2 {
		t.Fatalf("Items = %#v, want two candidate-backed items", options.Items)
	}
	if options.Items[0].Value != "/tmp/project-a" || options.Items[0].SearchText != "/tmp/project-a" {
		t.Fatalf("first item = %#v, want candidate as label/value/search text", options.Items[0])
	}
	if options.Items[1].Value != "/tmp/project-b" {
		t.Fatalf("second item = %#v, want second candidate", options.Items[1])
	}
}

func TestPickerOptionsFromCompatPickerPreservesTheme(t *testing.T) {
	t.Parallel()

	effective := theme.ResolveTheme(theme.ThemeConfig{Background: "#010203"})
	options := pickerOptionsFromCompatPicker(intpickercompat.Options{Theme: &effective})
	if options.Theme != &effective {
		t.Fatalf("Theme = %p, want %p", options.Theme, &effective)
	}
	roundTrip := intpickercompat.OptionsFromPicker(options)
	if roundTrip.Theme != &effective {
		t.Fatalf("round-trip Theme = %p, want %p", roundTrip.Theme, &effective)
	}
}

func TestSettingsNativeBackendDoesNotCallCompatRunner(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	var out strings.Builder
	var compatCalled bool
	cmd := &settingsCommand{
		ai:       testAICommand(home),
		switcher: testSettingsSwitchCommand(t, &stubSwitchPinStore{}),
		lookupEnv: func(name string) string {
			if name == intpicker.BackendEnv {
				return "native"
			}
			return ""
		},
		// Root tab chrome moved out of the entry list in Phase 2.5, so AI
		// Settings is now the second row in the Global tab. AI Settings
		// opens Default split mode, whose detail keeps codex on row 4.
		nativePicker: intpicker.NativeRunner{In: strings.NewReader("2\n2\n4\n"), Out: &out},
		runner: switchRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
			compatCalled = true
			return intpickercompat.Result{}, nil
		}),
	}

	if err := cmd.Run(nil, &strings.Builder{}, &strings.Builder{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if compatCalled {
		t.Fatal("compat runner was called for native settings backend")
	}
	if got, want := readModeFile(t, home), "codex\n"; got != want {
		t.Fatalf("mode file = %q, want %q", got, want)
	}
	if !strings.Contains(out.String(), "Settings > ") {
		t.Fatalf("native output = %q, want settings prompt", out.String())
	}
}

func TestPickerActionsFromCompatBindingPreservesExecuteSilentCommand(t *testing.T) {
	t.Parallel()

	options := pickerOptionsFromCompatPicker(intpickercompat.Options{
		Bindings: []string{"right:execute-silent(exec '/tmp/projmux' 'switch' 'cycle-window' {2} 'next')+refresh-preview"},
	})
	if len(options.Actions) != 1 {
		t.Fatalf("actions = %#v, want one action", options.Actions)
	}
	action := options.Actions[0]
	if action.Key != "right" || action.Command != "exec '/tmp/projmux' 'switch' 'cycle-window' {2} 'next'" {
		t.Fatalf("action = %#v, want execute-silent command preserved", action)
	}
}

func TestPickerOptionsFromCompatPickerMapsStartPosToInitialIndex(t *testing.T) {
	t.Parallel()

	options := pickerOptionsFromCompatPicker(intpickercompat.Options{
		Bindings: []string{
			"focus:execute-silent(exec '/tmp/projmux' 'switch' 'sidebar-focus' {2})",
			"start:pos(3)",
		},
	})
	if options.InitialIndex != 2 {
		t.Fatalf("InitialIndex = %d, want 2", options.InitialIndex)
	}
	for _, action := range options.Actions {
		if action.Key == "start" {
			t.Fatalf("actions = %#v, want start binding consumed as initial index", options.Actions)
		}
	}
}
