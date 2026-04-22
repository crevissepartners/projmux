package app

import (
	"bytes"
	"context"
	"errors"
	"testing"

	inttmux "github.com/es5h/projmux/internal/integrations/tmux"
)

func TestAppRunTmuxPopupPreviewUsesDefaultOptions(t *testing.T) {
	t.Parallel()

	popup := &stubTmuxPopupClient{}
	app := &App{
		tmux: &tmuxCommand{
			popup: popup,
			executable: func() (string, error) {
				return "/tmp/proj mux/bin/projmux", nil
			},
			popupOptions: defaultPopupPreviewOptions,
		},
	}

	var stdout bytes.Buffer
	if err := app.Run([]string{"tmux", "popup-preview", "dev"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	const wantCommand = "exec '/tmp/proj mux/bin/projmux' 'session-popup' 'preview' 'dev'"
	if popup.command != wantCommand {
		t.Fatalf("popup command = %q, want %q", popup.command, wantCommand)
	}

	wantOptions := inttmux.PopupOptions{
		Width:         "80%",
		Height:        "80%",
		Title:         "projmux: dev",
		CloseBehavior: inttmux.PopupCloseOnExit,
	}
	if popup.options != wantOptions {
		t.Fatalf("popup options = %#v, want %#v", popup.options, wantOptions)
	}
}

func TestTmuxCommandRejectsInvalidUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing subcommand", args: nil, want: "tmux requires a subcommand"},
		{name: "unknown subcommand", args: []string{"nope"}, want: "unknown tmux subcommand: nope"},
		{name: "missing popup args", args: []string{"popup-preview"}, want: "tmux popup-preview requires exactly 1 argument"},
		{name: "blank session", args: []string{"popup-preview", " "}, want: "tmux popup-preview requires a non-empty <session> argument"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stderr bytes.Buffer
			err := (&tmuxCommand{}).Run(tt.args, &bytes.Buffer{}, &stderr)
			if err == nil {
				t.Fatal("expected error")
			}
			if !contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
			if !contains(stderr.String(), "Usage:") {
				t.Fatalf("stderr = %q, want usage text", stderr.String())
			}
		})
	}
}

func TestTmuxCommandReportsConfigurationAndRuntimeErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cmd  *tmuxCommand
		want string
	}{
		{name: "missing popup client", cmd: &tmuxCommand{executable: func() (string, error) { return "/tmp/projmux", nil }}, want: "configure tmux popup client"},
		{name: "missing executable resolver", cmd: &tmuxCommand{popup: &stubTmuxPopupClient{}}, want: "configure tmux popup executable"},
		{name: "resolve executable", cmd: &tmuxCommand{popup: &stubTmuxPopupClient{}, executable: func() (string, error) { return "", errors.New("not found") }}, want: "resolve tmux popup executable"},
		{name: "display popup", cmd: &tmuxCommand{popup: &stubTmuxPopupClient{err: errors.New("tmux failed")}, executable: func() (string, error) { return "/tmp/projmux", nil }}, want: "display tmux popup preview"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.cmd.Run([]string{"popup-preview", "dev"}, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil {
				t.Fatal("expected error")
			}
			if !contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

type stubTmuxPopupClient struct {
	command string
	options inttmux.PopupOptions
	err     error
}

func (s *stubTmuxPopupClient) DisplayPopupWithOptions(_ context.Context, command string, options inttmux.PopupOptions) error {
	s.command = command
	s.options = options
	return s.err
}
