package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	coresessions "github.com/es5h/projmux/internal/core/sessions"
)

func TestAppRunCurrentWithoutSessionIdentity(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	app := &App{
		current: &currentCommand{
			currentPath: staticCurrentPath("/tmp/projmux"),
			validate:    func(string) error { return nil },
		},
	}

	if err := app.Run([]string{"current"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	got := stdout.String()
	if !strings.Contains(got, "current pane path: /tmp/projmux") {
		t.Fatalf("stdout missing current path:\n%s", got)
	}
	if !strings.Contains(got, "TODO: wire internal/core/sessions before switch/create is implemented") {
		t.Fatalf("stdout missing TODO note:\n%s", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected empty stderr, got %q", stderr.String())
	}
}

func TestAppRunCurrentWithSessionIdentity(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer

	app := &App{
		current: &currentCommand{
			currentPath: staticCurrentPath("/tmp/projmux"),
			identity:    staticIdentity("dotfiles"),
			validate:    func(string) error { return nil },
		},
	}

	if err := app.Run([]string{"current"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	got := stdout.String()
	if !strings.Contains(got, "target session: dotfiles") {
		t.Fatalf("stdout missing target session:\n%s", got)
	}
}

func TestCurrentCommandPlanUsesSessionNamer(t *testing.T) {
	t.Parallel()

	cmd := &currentCommand{
		currentPath: staticCurrentPath("/home/tester/worktrees/projmux"),
		identity: currentIdentityResolver{
			namer: coresessions.NewNamer("/home/tester"),
		},
		validate: func(string) error { return nil },
	}

	plan, err := cmd.plan(context.Background())
	if err != nil {
		t.Fatalf("plan returned error: %v", err)
	}
	if plan.SessionName != "worktrees-projmux" {
		t.Fatalf("unexpected session name %q", plan.SessionName)
	}
}

func TestCurrentCommandPlanPropagatesIdentitySetupError(t *testing.T) {
	t.Parallel()

	cmd := &currentCommand{
		currentPath: staticCurrentPath("/tmp/projmux"),
		identityErr: errors.New("no home directory"),
		validate:    func(string) error { return nil },
	}

	_, err := cmd.plan(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "configure session identity resolver") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAppRunCurrentPropagatesResolverError(t *testing.T) {
	t.Parallel()

	app := &App{
		current: &currentCommand{
			currentPath: currentPathResolverFunc(func(context.Context) (string, error) {
				return "", errors.New("tmux unavailable")
			}),
			validate: func(string) error { return nil },
		},
	}

	err := app.Run([]string{"current"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "tmux unavailable") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAppRunCurrentRejectsPositionalArgs(t *testing.T) {
	t.Parallel()

	app := &App{
		current: &currentCommand{
			currentPath: staticCurrentPath("/tmp/projmux"),
			validate:    func(string) error { return nil },
		},
	}

	err := app.Run([]string{"current", "extra"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "does not accept positional arguments") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCurrentCommandPlanRejectsMissingDirectory(t *testing.T) {
	t.Parallel()

	cmd := &currentCommand{
		currentPath: staticCurrentPath("/tmp/projmux-does-not-exist"),
		validate:    validateDirectory,
	}

	_, err := cmd.plan(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "stat current pane path") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCurrentCommandPlanRejectsFilePath(t *testing.T) {
	t.Parallel()

	file, err := os.CreateTemp(t.TempDir(), "current-path-file")
	if err != nil {
		t.Fatalf("CreateTemp returned error: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	cmd := &currentCommand{
		currentPath: staticCurrentPath(file.Name()),
		validate:    validateDirectory,
	}

	_, err = cmd.plan(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "current pane path is not a directory") {
		t.Fatalf("unexpected error: %v", err)
	}
}

type currentPathResolverFunc func(ctx context.Context) (string, error)

func (fn currentPathResolverFunc) CurrentPanePath(ctx context.Context) (string, error) {
	return fn(ctx)
}

func staticCurrentPath(path string) currentPathResolver {
	return currentPathResolverFunc(func(context.Context) (string, error) {
		return path, nil
	})
}

type staticIdentity string

func (s staticIdentity) SessionIdentityForPath(string) (string, error) {
	return string(s), nil
}
