package app

import (
	"bytes"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
	"github.com/crevissepartners/projmux/internal/ui/projmuxpicker"
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

func TestQuitCommandDestructiveRowKeepsDangerColorWhenSelected(t *testing.T) {
	t.Parallel()

	options := quitActionOptions()
	if len(options.Entries) == 0 {
		t.Fatal("quit picker has no entries")
	}
	quitLabel := options.Entries[0].Label
	if !strings.Contains(quitLabel, settingsColorRemove+"Quit projmux") {
		t.Fatalf("quit label = %q, want danger-colored action name", quitLabel)
	}
	selected := projmuxpicker.SelectedLine(projmuxpicker.Pointer, quitLabel)
	if !strings.Contains(selected, settingsColorRemove+"Quit projmux") {
		t.Fatalf("selected quit label = %q, destructive action lost danger color", selected)
	}
	if !strings.Contains(selected, settingsColorReset+projmuxpicker.CurrentStart) {
		t.Fatalf("selected quit label = %q, want current-row style restored after embedded color resets", selected)
	}
}

func TestQuitCommandSelectionKillsOnlyAppOwnedRuntime(t *testing.T) {
	t.Parallel()

	path := "/tmp/projmux-quit.sock"
	runner := &recordingTmuxRunner{
		outputs: map[string]string{
			recordedTmuxCallKey("tmux", "-L", defaultAppSocket, "display-message", "-p", "-F", "#{socket_path}"):         path + "\n",
			recordedTmuxCallKey("tmux", "-L", defaultAppSocket, "show-options", "-gqv", "@projmux_app"):                  "1\n",
			recordedTmuxCallKey("tmux", "-L", defaultAppSocket, "show-options", "-gqv", runtimeMutationSocketNameOption): defaultAppSocket + "\n",
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
	last := runner.calls[len(runner.calls)-1]
	if len(last.args) < 7 || !reflect.DeepEqual(last.args[:4], []string{"-S", path, "if-shell", "-F"}) || last.args[5] != "kill-server" {
		t.Fatalf("quit terminal call = %#v, want one exact guarded kill", last)
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
			recordedTmuxCallKey("tmux", "-L", defaultAppSocket, "display-message", "-p", "-F", "#{socket_path}"): "/tmp/projmux.sock\n",
			recordedTmuxCallKey("tmux", "-L", defaultAppSocket, "show-options", "-gqv", "@projmux_app"):          "0\n",
		},
	}
	cmd := &quitCommand{runner: runner}

	if err := cmd.Run([]string{"--yes"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := []recordedTmuxCall{
		{name: "tmux", args: []string{"-L", defaultAppSocket, "display-message", "-p", "-F", "#{socket_path}"}},
		{name: "tmux", args: []string{"-L", defaultAppSocket, "show-options", "-gqv", "@projmux_app"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("tmux calls = %#v, want only ownership check", runner.calls)
	}
}

func TestQuitCommandMissingRuntimeIsNoop(t *testing.T) {
	t.Parallel()

	runner := &recordingTmuxRunner{errors: map[string]error{
		recordedTmuxCallKey("tmux", "-L", defaultAppSocket, "display-message", "-p", "-F", "#{socket_path}"): errors.New("no server running on /tmp/tmux-1000/projmux"),
	}}
	cmd := &quitCommand{runner: runner}

	if err := cmd.Run([]string{"--force"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := []recordedTmuxCall{
		{name: "tmux", args: []string{"-L", defaultAppSocket, "display-message", "-p", "-F", "#{socket_path}"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("tmux calls = %#v, want only ownership check", runner.calls)
	}
}

func TestQuitRequestedAppRouteIgnoresForeignInheritedTmux(t *testing.T) {
	path := "/tmp/projmux.sock"
	runner := &recordingTmuxRunner{outputs: map[string]string{
		recordedTmuxCallKey("tmux", "-L", defaultAppSocket, "display-message", "-p", "-F", "#{socket_path}"):         path + "\n",
		recordedTmuxCallKey("tmux", "-L", defaultAppSocket, "show-options", "-gqv", "@projmux_app"):                  "1\n",
		recordedTmuxCallKey("tmux", "-L", defaultAppSocket, "show-options", "-gqv", runtimeMutationSocketNameOption): defaultAppSocket + "\n",
		recordedTmuxCallKey("tmux", "-S", path, "display-message", "-p", "-F", "#{socket_path}"):                     path + "\n",
		recordedTmuxCallKey("tmux", "-S", path, "show-options", "-gqv", "@projmux_app"):                              "1\n",
		recordedTmuxCallKey("tmux", "-S", path, "show-options", "-gqv", runtimeMutationSocketNameOption):             defaultAppSocket + "\n",
	}}
	cmd := &quitCommand{runner: runner, lookupEnv: func(key string) string {
		if key == "TMUX" {
			return "/tmp/foreign.sock,1,0"
		}
		return ""
	}}
	if err := cmd.Run([]string{"--yes"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for _, call := range runner.calls {
		if slices.Contains(call.args, "/tmp/foreign.sock") {
			t.Fatalf("quit followed inherited foreign TMUX: %#v", runner.calls)
		}
	}
	last := runner.calls[len(runner.calls)-1]
	if len(last.args) < 7 || !reflect.DeepEqual(last.args[:4], []string{"-S", path, "if-shell", "-F"}) || last.args[5] != "kill-server" {
		t.Fatalf("quit terminal write = %#v, want exact requested physical route", last)
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
