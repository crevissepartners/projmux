package app

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

func TestQuitCommandPickerShowsQuitAndCancel(t *testing.T) {
	t.Parallel()

	var got intpickercompat.Options
	cmd := &quitCommand{
		runner: &recordingTmuxRunner{},
		nativePicker: nativePickerFromCompatRunner(switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			got = options
			return intpickercompat.Result{Key: "enter", Value: quitActionCancel}, nil
		})),
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got.UI != "quit" {
		t.Fatalf("quit picker UI = %q, want quit", got.UI)
	}
	if got.DisableSearch != true {
		t.Fatalf("quit picker DisableSearch = false, want true")
	}
	for _, want := range []string{"Quit projmux", "Cancel"} {
		if !hasEntryLabelContaining(got.Entries, want) {
			t.Fatalf("quit picker entries = %#v, want label containing %q", got.Entries, want)
		}
	}
	if got, want := entryValues(got.Entries), []string{quitActionQuit, quitActionCancel}; !reflect.DeepEqual(got, want) {
		t.Fatalf("quit picker entry values = %#v, want %#v", got, want)
	}
}

func TestQuitCommandSelectionKillsOnlyAppOwnedRuntime(t *testing.T) {
	t.Parallel()

	runner := &recordingTmuxRunner{
		outputs: map[string]string{
			strings.Join([]string{"tmux", "-L", defaultAppSocket, "show-option", "-gv", "@projmux_app"}, "\x00"): "1\n",
		},
	}
	cmd := &quitCommand{
		runner: runner,
		nativePicker: nativePickerFromCompatRunner(switchRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
			return intpickercompat.Result{Key: "enter", Value: quitActionQuit}, nil
		})),
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := []recordedTmuxCall{
		{name: "tmux", args: []string{"-L", defaultAppSocket, "show-option", "-gv", "@projmux_app"}},
		{name: "tmux", args: []string{"-L", defaultAppSocket, "kill-server"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("tmux calls = %#v, want %#v", runner.calls, want)
	}
}

func TestQuitCommandCancelAndCloseDoNotShutdown(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		result intpickercompat.Result
		err    error
	}{
		{name: "cancel action", result: intpickercompat.Result{Key: "enter", Value: quitActionCancel}},
		{name: "escape result", result: intpickercompat.Result{Key: "esc", Value: quitActionQuit}},
		{name: "closed picker", err: errors.New("exit status 130")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			runner := &recordingTmuxRunner{}
			cmd := &quitCommand{
				runner: runner,
				nativePicker: nativePickerFromCompatRunner(switchRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
					return tc.result, tc.err
				})),
			}
			if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("tmux calls = %#v, want none", runner.calls)
			}
		})
	}
}

func TestQuitCommandNonAppRuntimeIsNoop(t *testing.T) {
	t.Parallel()

	runner := &recordingTmuxRunner{
		outputs: map[string]string{
			strings.Join([]string{"tmux", "-L", defaultAppSocket, "show-option", "-gv", "@projmux_app"}, "\x00"): "0\n",
		},
	}
	cmd := &quitCommand{runner: runner}

	if err := cmd.Run([]string{"--yes"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := []recordedTmuxCall{
		{name: "tmux", args: []string{"-L", defaultAppSocket, "show-option", "-gv", "@projmux_app"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("tmux calls = %#v, want only ownership check", runner.calls)
	}
}

func TestQuitCommandMissingRuntimeIsNoop(t *testing.T) {
	t.Parallel()

	runner := &recordingTmuxRunner{err: errors.New("no server running on /tmp/tmux-1000/projmux")}
	cmd := &quitCommand{runner: runner}

	if err := cmd.Run([]string{"--force"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := []recordedTmuxCall{
		{name: "tmux", args: []string{"-L", defaultAppSocket, "show-option", "-gv", "@projmux_app"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("tmux calls = %#v, want only ownership check", runner.calls)
	}
}

func TestAppRunDispatchesQuit(t *testing.T) {
	t.Parallel()

	var called bool
	app := &App{
		quit: &quitCommand{
			runner: &recordingTmuxRunner{},
			nativePicker: nativePickerFromCompatRunner(switchRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
				called = true
				return intpickercompat.Result{Key: "enter", Value: quitActionCancel}, nil
			})),
		},
	}

	if err := app.Run([]string{"quit"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !called {
		t.Fatal("quit picker was not called")
	}
}
