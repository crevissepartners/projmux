package app

import (
	"bytes"
	"context"
	"errors"
	"testing"

	intfzf "github.com/es5h/projmux/internal/ui/fzf"
)

func TestAppRunSessionsDefaultsToPopupAndOpensSelectedSession(t *testing.T) {
	t.Parallel()

	var gotOptions intfzf.Options
	app := &App{
		sessions: &sessionsCommand{
			recent: sessionsRecentFunc(func(context.Context) ([]string, error) {
				return []string{"repo-b", "home"}, nil
			}),
			runner: sessionsRunnerFunc(func(options intfzf.Options) (intfzf.Result, error) {
				gotOptions = options
				return intfzf.Result{Value: "repo-b"}, nil
			}),
			opener: &recordingSessionsOpener{},
		},
	}

	opener := app.sessions.opener.(*recordingSessionsOpener)
	if err := app.Run([]string{"sessions"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := gotOptions.UI, switchUIPopup; got != want {
		t.Fatalf("runner UI = %q, want %q", got, want)
	}
	if got, want := gotOptions.Entries, []intfzf.Entry{
		{Label: "repo-b", Value: "repo-b"},
		{Label: "home", Value: "home"},
	}; !equalEntries(got, want) {
		t.Fatalf("runner entries = %#v, want %#v", got, want)
	}
	if got, want := opener.openSessionName, "repo-b"; got != want {
		t.Fatalf("open session = %q, want %q", got, want)
	}
}

func TestSessionsCommandSupportsSidebarUI(t *testing.T) {
	t.Parallel()

	var gotOptions intfzf.Options
	cmd := &sessionsCommand{
		recent: sessionsRecentFunc(func(context.Context) ([]string, error) {
			return []string{"repo-b"}, nil
		}),
		runner: sessionsRunnerFunc(func(options intfzf.Options) (intfzf.Result, error) {
			gotOptions = options
			return intfzf.Result{}, nil
		}),
		opener: &recordingSessionsOpener{},
	}

	if err := cmd.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := gotOptions.UI, switchUISidebar; got != want {
		t.Fatalf("runner UI = %q, want %q", got, want)
	}
}

func TestSessionsCommandAllowsEmptySelection(t *testing.T) {
	t.Parallel()

	opener := &recordingSessionsOpener{}
	cmd := &sessionsCommand{
		recent: sessionsRecentFunc(func(context.Context) ([]string, error) {
			return []string{"repo-b"}, nil
		}),
		runner: sessionsRunnerFunc(func(intfzf.Options) (intfzf.Result, error) {
			return intfzf.Result{}, nil
		}),
		opener: opener,
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := opener.openSessionName; got != "" {
		t.Fatalf("OpenSession called unexpectedly: %q", got)
	}
}

func TestSessionsCommandReturnsWithoutPickerWhenRecentListIsEmpty(t *testing.T) {
	t.Parallel()

	called := false
	cmd := &sessionsCommand{
		recent: sessionsRecentFunc(func(context.Context) ([]string, error) {
			return nil, nil
		}),
		runner: sessionsRunnerFunc(func(intfzf.Options) (intfzf.Result, error) {
			called = true
			return intfzf.Result{}, nil
		}),
		opener: &recordingSessionsOpener{},
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if called {
		t.Fatal("runner called unexpectedly")
	}
}

func TestSessionsCommandRejectsInvalidUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "invalid ui", args: []string{"--ui=dialog"}, want: "invalid --ui value"},
		{name: "positional args", args: []string{"extra"}, want: "sessions does not accept positional arguments"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stderr bytes.Buffer
			err := (&sessionsCommand{}).Run(tt.args, &bytes.Buffer{}, &stderr)
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

func TestSessionsCommandPropagatesSetupErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cmd  *sessionsCommand
		want string
	}{
		{name: "recent resolver", cmd: &sessionsCommand{}, want: "recent tmux session resolver is not configured"},
		{
			name: "recent sessions",
			cmd: &sessionsCommand{
				recent: sessionsRecentFunc(func(context.Context) ([]string, error) {
					return nil, errors.New("tmux failed")
				}),
			},
			want: "resolve recent tmux sessions",
		},
		{
			name: "runner",
			cmd: &sessionsCommand{
				recent: sessionsRecentFunc(func(context.Context) ([]string, error) { return []string{"repo-b"}, nil }),
				runner: sessionsRunnerFunc(func(intfzf.Options) (intfzf.Result, error) {
					return intfzf.Result{}, errors.New("fzf failed")
				}),
			},
			want: "run sessions picker",
		},
		{
			name: "missing opener",
			cmd: &sessionsCommand{
				recent: sessionsRecentFunc(func(context.Context) ([]string, error) { return []string{"repo-b"}, nil }),
				runner: sessionsRunnerFunc(func(intfzf.Options) (intfzf.Result, error) {
					return intfzf.Result{Value: "repo-b"}, nil
				}),
			},
			want: "sessions opener is not configured",
		},
		{
			name: "open session",
			cmd: &sessionsCommand{
				recent: sessionsRecentFunc(func(context.Context) ([]string, error) { return []string{"repo-b"}, nil }),
				runner: sessionsRunnerFunc(func(intfzf.Options) (intfzf.Result, error) {
					return intfzf.Result{Value: "repo-b"}, nil
				}),
				opener: &recordingSessionsOpener{openErr: errors.New("attach failed")},
			},
			want: "open tmux session",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil {
				t.Fatal("expected error")
			}
			if !contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

type sessionsRecentFunc func(context.Context) ([]string, error)

func (f sessionsRecentFunc) RecentSessions(ctx context.Context) ([]string, error) {
	return f(ctx)
}

type sessionsRunnerFunc func(options intfzf.Options) (intfzf.Result, error)

func (f sessionsRunnerFunc) Run(options intfzf.Options) (intfzf.Result, error) {
	return f(options)
}

type recordingSessionsOpener struct {
	openSessionName string
	openErr         error
}

func (o *recordingSessionsOpener) OpenSession(_ context.Context, sessionName string) error {
	o.openSessionName = sessionName
	return o.openErr
}
