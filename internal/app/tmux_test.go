package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestAppRunTmuxPopupPreview(t *testing.T) {
	t.Parallel()

	popup := &stubPopupDisplayer{}
	app := &App{
		tmux: &tmuxCommand{
			popup: popup,
			executable: func() (string, error) {
				return "/tmp/projmux", nil
			},
		},
	}

	if err := app.Run([]string{"tmux", "popup-preview", "dev"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	const want = "exec '/tmp/projmux' 'session-popup' 'preview' 'dev'"
	if popup.command != want {
		t.Fatalf("popup command = %q, want %q", popup.command, want)
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
		{name: "missing session", args: []string{"popup-preview"}, want: "tmux popup-preview requires exactly 1 argument"},
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
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
			if !strings.Contains(stderr.String(), "Usage:") {
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
		{name: "popup setup", cmd: &tmuxCommand{popupErr: errors.New("missing tmux client")}, want: "configure tmux popup entry"},
		{
			name: "executable setup",
			cmd: &tmuxCommand{
				popup: &stubPopupDisplayer{},
				executable: func() (string, error) {
					return "", errors.New("no executable")
				},
			},
			want: "resolve tmux popup executable",
		},
		{
			name: "popup run",
			cmd: &tmuxCommand{
				popup: &stubPopupDisplayer{err: errors.New("tmux failed")},
				executable: func() (string, error) {
					return "/tmp/projmux", nil
				},
			},
			want: "open tmux popup preview",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.cmd.Run([]string{"popup-preview", "dev"}, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

type stubPopupDisplayer struct {
	command string
	err     error
}

func (s *stubPopupDisplayer) DisplayPopup(_ context.Context, command string) error {
	s.command = command
	return s.err
}
