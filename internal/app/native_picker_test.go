package app

import (
	"os"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	intfzf "github.com/crevissepartners/projmux/internal/ui/fzf"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
)

func TestRunPickerOptionBackendUsesNativeWhenRequested(t *testing.T) {
	t.Parallel()

	var fzfCalled bool
	native := pickerRunnerFunc(func(options intpicker.Options) (intpicker.Result, error) {
		if options.UI != "settings" {
			t.Fatalf("native UI = %q, want settings", options.UI)
		}
		if len(options.Items) != 1 || options.Items[0].Value != "ai" {
			t.Fatalf("native items = %#v, want fzf entries converted", options.Items)
		}
		return intpicker.Result{Key: "enter", Value: "ai"}, nil
	})

	result, err := runPickerOptionBackend(
		func(name string) string {
			if name == intpicker.BackendEnv {
				return "native"
			}
			return ""
		},
		native,
		switchRunnerFunc(func(intfzf.Options) (intfzf.Result, error) {
			fzfCalled = true
			return intfzf.Result{}, nil
		}),
		intfzf.Options{
			UI:      "settings",
			Entries: []intfzf.Entry{{Label: "AI Settings", Value: "ai"}},
		},
	)
	if err != nil {
		t.Fatalf("runPickerOptionBackend() error = %v", err)
	}
	if fzfCalled {
		t.Fatal("fzf runner was called for native backend")
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

	var fzfCalled bool
	nativeCalled := false
	result, err := runPickerOptionBackend(
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
		switchRunnerFunc(func(intfzf.Options) (intfzf.Result, error) {
			fzfCalled = true
			return intfzf.Result{}, nil
		}),
		intfzf.Options{
			UI:      "settings",
			Entries: []intfzf.Entry{{Label: "AI Settings", Value: "ai"}},
		},
	)
	if err != nil {
		t.Fatalf("runPickerOptionBackend() error = %v", err)
	}
	if !nativeCalled {
		t.Fatal("native runner was not called for saved native picker backend")
	}
	if fzfCalled {
		t.Fatal("fzf runner was called for saved native picker backend")
	}
	if result.Value != "ai" {
		t.Fatalf("result = %#v, want native selection", result)
	}
}

func TestRunPickerOptionBackendDefaultsToNativeBackend(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	var fzfCalled bool
	var nativeCalled bool
	result, err := runPickerOptionBackend(
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
		switchRunnerFunc(func(intfzf.Options) (intfzf.Result, error) {
			fzfCalled = true
			return intfzf.Result{}, nil
		}),
		intfzf.Options{UI: "settings"},
	)
	if err != nil {
		t.Fatalf("runPickerOptionBackend() error = %v", err)
	}
	if !nativeCalled {
		t.Fatal("native runner was not called for default picker backend")
	}
	if fzfCalled {
		t.Fatal("fzf runner was called for default picker backend")
	}
	if result.Value != "ai" {
		t.Fatalf("result = %#v, want native selection", result)
	}
}

func TestRunPickerOptionBackendEnvOverridesSavedNativeBackend(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	paths := config.DefaultPaths(configHome, t.TempDir())
	if err := config.SavePickerBackendFile(paths.PickerBackendFile(), config.PickerBackendNative); err != nil {
		t.Fatalf("SavePickerBackendFile() error = %v", err)
	}

	var nativeCalled bool
	result, err := runPickerOptionBackend(
		func(name string) string {
			if name == intpicker.BackendEnv {
				return string(intpicker.BackendFZF)
			}
			return os.Getenv(name)
		},
		pickerRunnerFunc(func(intpicker.Options) (intpicker.Result, error) {
			nativeCalled = true
			return intpicker.Result{}, nil
		}),
		switchRunnerFunc(func(intfzf.Options) (intfzf.Result, error) {
			return intfzf.Result{Key: "enter", Value: "ai"}, nil
		}),
		intfzf.Options{UI: "settings"},
	)
	if err != nil {
		t.Fatalf("runPickerOptionBackend() error = %v", err)
	}
	if nativeCalled {
		t.Fatal("native runner was called despite env forcing fzf")
	}
	if result.Value != "ai" {
		t.Fatalf("result = %#v, want fzf selection", result)
	}
}

func TestPickerOptionsFromFZFMapsCandidatesWhenEntriesAreEmpty(t *testing.T) {
	t.Parallel()

	options := pickerOptionsFromFZF(intfzf.Options{
		UI:         "legacy",
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

func TestSettingsNativeBackendDoesNotCallFZF(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	var out strings.Builder
	var fzfCalled bool
	cmd := &settingsCommand{
		ai:       testAICommand(home),
		switcher: testSettingsSwitchCommand(t, &stubSwitchPinStore{}),
		lookupEnv: func(name string) string {
			if name == intpicker.BackendEnv {
				return "native"
			}
			return ""
		},
		nativePicker: intpicker.NativeRunner{In: strings.NewReader("2\n4\n"), Out: &out},
		runner: switchRunnerFunc(func(intfzf.Options) (intfzf.Result, error) {
			fzfCalled = true
			return intfzf.Result{}, nil
		}),
	}

	if err := cmd.Run(nil, &strings.Builder{}, &strings.Builder{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if fzfCalled {
		t.Fatal("fzf runner was called for native settings backend")
	}
	if got, want := readModeFile(t, home), "codex\n"; got != want {
		t.Fatalf("mode file = %q, want %q", got, want)
	}
	if !strings.Contains(out.String(), "Settings > ") {
		t.Fatalf("native output = %q, want settings prompt", out.String())
	}
}

func TestPickerActionsFromFZFPreservesExecuteSilentCommand(t *testing.T) {
	t.Parallel()

	options := pickerOptionsFromFZF(intfzf.Options{
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

func TestPickerOptionsFromFZFMapsStartPosToInitialIndex(t *testing.T) {
	t.Parallel()

	options := pickerOptionsFromFZF(intfzf.Options{
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
