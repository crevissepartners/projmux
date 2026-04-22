package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/es5h/projmux/internal/core/lifecycle"
)

func TestAppRunKillTaggedExecutesOrchestrator(t *testing.T) {
	t.Parallel()

	exec := &recordingTaggedKillExecutor{}
	app := &App{
		kill: &killCommand{
			current: currentSessionResolverFunc(func(context.Context) (string, error) {
				return "work-a", nil
			}),
			recent: recentSessionsResolverFunc(func(context.Context) ([]string, error) {
				return []string{"work-b", "home"}, nil
			}),
			exec: exec,
			homeDir: func() (string, error) {
				return "/home/tester", nil
			},
		},
	}

	if err := app.Run([]string{"kill", "tagged", " work-a ", "work-b", "work-a"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := exec.inputs.CurrentSession, "work-a"; got != want {
		t.Fatalf("current session = %q, want %q", got, want)
	}
	if got, want := exec.inputs.RecentSessions, []string{"work-b", "home"}; !equalStrings(got, want) {
		t.Fatalf("recent sessions = %q, want %q", got, want)
	}
	if got, want := exec.inputs.KillTargets, []string{"work-a", "work-b"}; !equalStrings(got, want) {
		t.Fatalf("kill targets = %q, want %q", got, want)
	}
	if got, want := exec.inputs.HomeSession, "home"; got != want {
		t.Fatalf("home session = %q, want %q", got, want)
	}
}

func TestAppRunKillTaggedLoadsTargetsFromTagStore(t *testing.T) {
	t.Parallel()

	exec := &recordingTaggedKillExecutor{}
	app := &App{
		kill: &killCommand{
			current: currentSessionResolverFunc(func(context.Context) (string, error) {
				return "work-a", nil
			}),
			recent: recentSessionsResolverFunc(func(context.Context) ([]string, error) {
				return []string{"work-b", "home"}, nil
			}),
			exec:     exec,
			homeDir:  func() (string, error) { return "/home/tester", nil },
			tagStore: staticKillTagStore{items: []string{" work-a ", "work-b", "work-a"}},
		},
	}

	if err := app.Run([]string{"kill", "tagged"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := exec.inputs.KillTargets, []string{"work-a", "work-b"}; !equalStrings(got, want) {
		t.Fatalf("kill targets = %q, want %q", got, want)
	}
}

func TestKillCommandRejectsInvalidUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		args      []string
		want      string
		wantUsage bool
	}{
		{
			name:      "missing subcommand",
			args:      nil,
			want:      "kill requires a subcommand",
			wantUsage: true,
		},
		{
			name:      "unknown subcommand",
			args:      []string{"nope"},
			want:      "unknown kill subcommand: nope",
			wantUsage: true,
		},
		{
			name: "missing tagged targets",
			args: []string{"tagged"},
			want: "configure kill tag store: tag store is not configured",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stderr bytes.Buffer
			err := (&killCommand{}).Run(tt.args, &bytes.Buffer{}, &stderr)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
			if tt.wantUsage && !strings.Contains(stderr.String(), "Usage:") {
				t.Fatalf("stderr = %q, want usage text", stderr.String())
			}
		})
	}
}

func TestKillCommandRunTaggedRejectsBlankPositionalTarget(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	err := (&killCommand{}).Run([]string{"tagged", "  "}, &bytes.Buffer{}, &stderr)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "kill tagged requires non-empty tagged sessions") {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("stderr = %q, want usage text", stderr.String())
	}
}

func TestKillCommandPropagatesSetupErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cmd  *killCommand
		want string
	}{
		{
			name: "current session",
			cmd: &killCommand{
				current: currentSessionResolverFunc(func(context.Context) (string, error) {
					return "", errors.New("no tmux")
				}),
				recent:  recentSessionsResolverFunc(func(context.Context) ([]string, error) { return nil, nil }),
				exec:    &recordingTaggedKillExecutor{},
				homeDir: func() (string, error) { return "/home/tester", nil },
			},
			want: "resolve current tmux session",
		},
		{
			name: "recent sessions",
			cmd: &killCommand{
				current: currentSessionResolverFunc(func(context.Context) (string, error) { return "work", nil }),
				recent: recentSessionsResolverFunc(func(context.Context) ([]string, error) {
					return nil, errors.New("list exploded")
				}),
				exec:    &recordingTaggedKillExecutor{},
				homeDir: func() (string, error) { return "/home/tester", nil },
			},
			want: "resolve recent tmux sessions",
		},
		{
			name: "home session",
			cmd: &killCommand{
				current: currentSessionResolverFunc(func(context.Context) (string, error) { return "work", nil }),
				recent:  recentSessionsResolverFunc(func(context.Context) ([]string, error) { return []string{"home"}, nil }),
				exec:    &recordingTaggedKillExecutor{},
				homeDir: func() (string, error) { return "", errors.New("no home") },
			},
			want: "resolve home session identity",
		},
		{
			name: "executor missing",
			cmd: &killCommand{
				current: currentSessionResolverFunc(func(context.Context) (string, error) { return "work", nil }),
				recent:  recentSessionsResolverFunc(func(context.Context) ([]string, error) { return []string{"home"}, nil }),
				homeDir: func() (string, error) { return "/home/tester", nil },
			},
			want: "kill tagged executor is not configured",
		},
		{
			name: "execute failure",
			cmd: &killCommand{
				current: currentSessionResolverFunc(func(context.Context) (string, error) { return "work", nil }),
				recent:  recentSessionsResolverFunc(func(context.Context) ([]string, error) { return []string{"home"}, nil }),
				exec: &recordingTaggedKillExecutor{
					err: errors.New("switch failed"),
				},
				homeDir: func() (string, error) { return "/home/tester", nil },
			},
			want: "kill tagged sessions",
		},
		{
			name: "tag store paths",
			cmd: &killCommand{
				storeErr: errors.New("no home"),
			},
			want: "configure kill tag store",
		},
		{
			name: "tag store read",
			cmd: &killCommand{
				tagStore: staticKillTagStore{err: errors.New("read failed")},
			},
			want: "load kill tags",
		},
		{
			name: "tag store empty",
			cmd: &killCommand{
				tagStore: staticKillTagStore{},
			},
			want: "load kill tags: kill tagged requires at least 1 tagged session",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			args := []string{"tagged", "work"}
			if strings.Contains(tt.name, "tag store") {
				args = []string{"tagged"}
			}

			err := tt.cmd.Run(args, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

type currentSessionResolverFunc func(context.Context) (string, error)

func (fn currentSessionResolverFunc) CurrentSessionName(ctx context.Context) (string, error) {
	return fn(ctx)
}

type recentSessionsResolverFunc func(context.Context) ([]string, error)

func (fn recentSessionsResolverFunc) RecentSessions(ctx context.Context) ([]string, error) {
	return fn(ctx)
}

type recordingTaggedKillExecutor struct {
	inputs lifecycle.TaggedKillInputs
	result lifecycle.TaggedKillResult
	err    error
}

func (r *recordingTaggedKillExecutor) Execute(_ context.Context, inputs lifecycle.TaggedKillInputs) (lifecycle.TaggedKillResult, error) {
	r.inputs = inputs
	if r.err != nil {
		return lifecycle.TaggedKillResult{}, r.err
	}
	return r.result, nil
}

type staticKillTagStore struct {
	items []string
	err   error
}

func (s staticKillTagStore) List() ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.items, nil
}
