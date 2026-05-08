package app

import (
	"strings"
	"testing"

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
		nativePicker: intpicker.NativeRunner{In: strings.NewReader("1\n4\n"), Out: &out},
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
