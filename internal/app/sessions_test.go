package app

import (
	"bytes"
	"context"
	"errors"
	"testing"

	corepreview "github.com/es5h/projmux/internal/core/preview"
	inttmux "github.com/es5h/projmux/internal/integrations/tmux"
	intfzf "github.com/es5h/projmux/internal/ui/fzf"
)

func TestAppRunSessionsDefaultsToPopupAndOpensSelectedSession(t *testing.T) {
	t.Parallel()

	var gotOptions intfzf.Options
	app := &App{
		sessions: &sessionsCommand{
			recent: sessionsRecentFunc(func(context.Context) ([]inttmux.RecentSessionSummary, error) {
				return []inttmux.RecentSessionSummary{
					{Name: "repo-b", Attached: true, WindowCount: 3, PaneCount: 4, Path: "/tmp/repo-b"},
					{Name: "home", Attached: false, WindowCount: 1, PaneCount: 1, Path: "/home/tester"},
				}, nil
			}),
			store: &recordingSessionsStore{
				selections: map[string]corepreview.Selection{
					"repo-b": {
						SessionName: "repo-b",
						WindowIndex: "3",
						PaneIndex:   "1",
					},
				},
			},
			runner: sessionsRunnerFunc(func(options intfzf.Options) (intfzf.Result, error) {
				gotOptions = options
				return intfzf.Result{Value: "repo-b"}, nil
			}),
			executable: func() (string, error) { return "/tmp/proj mux/bin/projmux", nil },
			opener:     &recordingSessionsOpener{},
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
		{Label: "repo-b  [attached]  3w  4p  w3.p1  /tmp/repo-b", Value: "repo-b"},
		{Label: "home  [detached]  1w  1p  /home/tester", Value: "home"},
	}; !equalEntries(got, want) {
		t.Fatalf("runner entries = %#v, want %#v", got, want)
	}
	if got, want := gotOptions.PreviewCommand, "exec '/tmp/proj mux/bin/projmux' 'session-popup' 'preview' {2}"; got != want {
		t.Fatalf("runner preview command = %q, want %q", got, want)
	}
	if got, want := gotOptions.PreviewWindow, "down,45%,border-top"; got != want {
		t.Fatalf("runner preview window = %q, want %q", got, want)
	}
	if got, want := gotOptions.Bindings, []string{
		"left:execute-silent(exec '/tmp/proj mux/bin/projmux' 'session-popup' 'cycle-window' {2} 'prev')+refresh-preview",
		"right:execute-silent(exec '/tmp/proj mux/bin/projmux' 'session-popup' 'cycle-window' {2} 'next')+refresh-preview",
		"alt-up:execute-silent(exec '/tmp/proj mux/bin/projmux' 'session-popup' 'cycle-pane' {2} 'prev')+refresh-preview",
		"alt-down:execute-silent(exec '/tmp/proj mux/bin/projmux' 'session-popup' 'cycle-pane' {2} 'next')+refresh-preview",
	}; !equalStrings(got, want) {
		t.Fatalf("runner bindings = %q, want %q", got, want)
	}
	if got, want := opener.openSessionName, "repo-b"; got != want {
		t.Fatalf("open session = %q, want %q", got, want)
	}
	if got, want := opener.windowIndex, "3"; got != want {
		t.Fatalf("open window = %q, want %q", got, want)
	}
	if got, want := opener.paneIndex, "1"; got != want {
		t.Fatalf("open pane = %q, want %q", got, want)
	}
}

func TestSessionsCommandSupportsSidebarUI(t *testing.T) {
	t.Parallel()

	var gotOptions intfzf.Options
	cmd := &sessionsCommand{
		recent: sessionsRecentFunc(func(context.Context) ([]inttmux.RecentSessionSummary, error) {
			return []inttmux.RecentSessionSummary{{Name: "repo-b"}}, nil
		}),
		runner: sessionsRunnerFunc(func(options intfzf.Options) (intfzf.Result, error) {
			gotOptions = options
			return intfzf.Result{}, nil
		}),
		executable: func() (string, error) { return "/tmp/projmux", nil },
		opener:     &recordingSessionsOpener{},
	}

	if err := cmd.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := gotOptions.UI, switchUISidebar; got != want {
		t.Fatalf("runner UI = %q, want %q", got, want)
	}
	if got, want := gotOptions.PreviewWindow, "right,60%,border-left"; got != want {
		t.Fatalf("runner preview window = %q, want %q", got, want)
	}
}

func TestSessionsCommandAllowsEmptySelection(t *testing.T) {
	t.Parallel()

	opener := &recordingSessionsOpener{}
	cmd := &sessionsCommand{
		recent: sessionsRecentFunc(func(context.Context) ([]inttmux.RecentSessionSummary, error) {
			return []inttmux.RecentSessionSummary{{Name: "repo-b"}}, nil
		}),
		runner: sessionsRunnerFunc(func(intfzf.Options) (intfzf.Result, error) {
			return intfzf.Result{}, nil
		}),
		executable: func() (string, error) { return "/tmp/projmux", nil },
		opener:     opener,
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
		recent: sessionsRecentFunc(func(context.Context) ([]inttmux.RecentSessionSummary, error) {
			return nil, nil
		}),
		runner: sessionsRunnerFunc(func(intfzf.Options) (intfzf.Result, error) {
			called = true
			return intfzf.Result{}, nil
		}),
		executable: func() (string, error) { return "/tmp/projmux", nil },
		opener:     &recordingSessionsOpener{},
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if called {
		t.Fatal("runner called unexpectedly")
	}
}

func TestFormatStoredTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		window     string
		pane       string
		wantTarget string
	}{
		{name: "window and pane", window: "3", pane: "1", wantTarget: "w3.p1"},
		{name: "window only", window: "3", pane: "", wantTarget: "w3"},
		{name: "missing window", window: "", pane: "1", wantTarget: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := formatStoredTarget(tt.window, tt.pane); got != tt.wantTarget {
				t.Fatalf("formatStoredTarget(%q, %q) = %q, want %q", tt.window, tt.pane, got, tt.wantTarget)
			}
		})
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
				recent: sessionsRecentFunc(func(context.Context) ([]inttmux.RecentSessionSummary, error) {
					return nil, errors.New("tmux failed")
				}),
			},
			want: "resolve recent tmux sessions",
		},
		{
			name: "executable resolver",
			cmd: &sessionsCommand{
				recent: sessionsRecentFunc(func(context.Context) ([]inttmux.RecentSessionSummary, error) {
					return []inttmux.RecentSessionSummary{{Name: "repo-b"}}, nil
				}),
				runner: sessionsRunnerFunc(func(intfzf.Options) (intfzf.Result, error) {
					return intfzf.Result{}, nil
				}),
			},
			want: "sessions executable resolver is not configured",
		},
		{
			name: "resolve executable",
			cmd: &sessionsCommand{
				recent: sessionsRecentFunc(func(context.Context) ([]inttmux.RecentSessionSummary, error) {
					return []inttmux.RecentSessionSummary{{Name: "repo-b"}}, nil
				}),
				runner:     sessionsRunnerFunc(func(intfzf.Options) (intfzf.Result, error) { return intfzf.Result{}, nil }),
				executable: func() (string, error) { return "", errors.New("not found") },
			},
			want: "resolve sessions executable",
		},
		{
			name: "runner",
			cmd: &sessionsCommand{
				recent: sessionsRecentFunc(func(context.Context) ([]inttmux.RecentSessionSummary, error) {
					return []inttmux.RecentSessionSummary{{Name: "repo-b"}}, nil
				}),
				runner: sessionsRunnerFunc(func(intfzf.Options) (intfzf.Result, error) {
					return intfzf.Result{}, errors.New("fzf failed")
				}),
				executable: func() (string, error) { return "/tmp/projmux", nil },
			},
			want: "run sessions picker",
		},
		{
			name: "missing opener",
			cmd: &sessionsCommand{
				recent: sessionsRecentFunc(func(context.Context) ([]inttmux.RecentSessionSummary, error) {
					return []inttmux.RecentSessionSummary{{Name: "repo-b"}}, nil
				}),
				runner: sessionsRunnerFunc(func(intfzf.Options) (intfzf.Result, error) {
					return intfzf.Result{Value: "repo-b"}, nil
				}),
				executable: func() (string, error) { return "/tmp/projmux", nil },
			},
			want: "sessions opener is not configured",
		},
		{
			name: "load selection",
			cmd: &sessionsCommand{
				recent: sessionsRecentFunc(func(context.Context) ([]inttmux.RecentSessionSummary, error) {
					return []inttmux.RecentSessionSummary{{Name: "repo-b"}}, nil
				}),
				store:      &recordingSessionsStore{err: errors.New("state failed")},
				runner:     sessionsRunnerFunc(func(intfzf.Options) (intfzf.Result, error) { return intfzf.Result{Value: "repo-b"}, nil }),
				executable: func() (string, error) { return "/tmp/projmux", nil },
				opener:     &recordingSessionsOpener{},
			},
			want: "load sessions preview selection",
		},
		{
			name: "open session",
			cmd: &sessionsCommand{
				recent: sessionsRecentFunc(func(context.Context) ([]inttmux.RecentSessionSummary, error) {
					return []inttmux.RecentSessionSummary{{Name: "repo-b"}}, nil
				}),
				runner: sessionsRunnerFunc(func(intfzf.Options) (intfzf.Result, error) {
					return intfzf.Result{Value: "repo-b"}, nil
				}),
				executable: func() (string, error) { return "/tmp/projmux", nil },
				opener:     &recordingSessionsOpener{openErr: errors.New("attach failed")},
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

type sessionsRecentFunc func(context.Context) ([]inttmux.RecentSessionSummary, error)

func (f sessionsRecentFunc) RecentSessionSummaries(ctx context.Context) ([]inttmux.RecentSessionSummary, error) {
	return f(ctx)
}

type sessionsRunnerFunc func(options intfzf.Options) (intfzf.Result, error)

func (f sessionsRunnerFunc) Run(options intfzf.Options) (intfzf.Result, error) {
	return f(options)
}

type recordingSessionsOpener struct {
	openSessionName string
	windowIndex     string
	paneIndex       string
	openErr         error
}

func (o *recordingSessionsOpener) OpenSessionTarget(_ context.Context, sessionName, windowIndex, paneIndex string) error {
	o.openSessionName = sessionName
	o.windowIndex = windowIndex
	o.paneIndex = paneIndex
	return o.openErr
}

type recordingSessionsStore struct {
	selections map[string]corepreview.Selection
	found      bool
	err        error
}

func (s *recordingSessionsStore) ReadSelection(sessionName string) (corepreview.Selection, bool, error) {
	if s.err != nil {
		return corepreview.Selection{}, false, s.err
	}
	if s.selections != nil {
		selection, ok := s.selections[sessionName]
		return selection, ok, nil
	}
	return corepreview.Selection{}, s.found, nil
}
