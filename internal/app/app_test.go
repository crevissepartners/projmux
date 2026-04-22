package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	coresessions "github.com/es5h/projmux/internal/core/sessions"
)

func TestAppRunCurrentEnsuresAndOpensDerivedSession(t *testing.T) {
	t.Parallel()

	executor := &recordingCurrentSessionExecutor{}

	app := &App{
		current: &currentCommand{
			currentPath: staticCurrentPath("/tmp/projmux"),
			sessions:    executor,
			identity:    staticIdentity("dotfiles"),
			validate:    func(string) error { return nil },
		},
	}

	if err := app.Run([]string{"current"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if executor.ensureSessionName != "dotfiles" || executor.ensureCWD != "/tmp/projmux" {
		t.Fatalf("EnsureSession called with %q %q", executor.ensureSessionName, executor.ensureCWD)
	}
	if executor.openSessionName != "dotfiles" {
		t.Fatalf("OpenSession called with %q", executor.openSessionName)
	}
}

func TestAppRunCurrentRequiresSessionIdentity(t *testing.T) {
	t.Parallel()

	app := &App{
		current: &currentCommand{
			currentPath: staticCurrentPath("/tmp/projmux"),
			sessions:    &recordingCurrentSessionExecutor{},
			validate:    func(string) error { return nil },
		},
	}

	err := app.Run([]string{"current"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "current command requires a target session" {
		t.Fatalf("unexpected error: %v", err)
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
	if !contains(err.Error(), "configure session identity resolver") {
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
	if !contains(err.Error(), "tmux unavailable") {
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
	if !contains(err.Error(), "does not accept positional arguments") {
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
	if !contains(err.Error(), "stat current pane path") {
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
	if !contains(err.Error(), "current pane path is not a directory") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAppRunCurrentPropagatesEnsureSessionError(t *testing.T) {
	t.Parallel()

	app := &App{
		current: &currentCommand{
			currentPath: staticCurrentPath("/tmp/projmux"),
			sessions: &recordingCurrentSessionExecutor{
				ensureErr: errors.New("create failed"),
			},
			identity: staticIdentity("dotfiles"),
			validate: func(string) error { return nil },
		},
	}

	err := app.Run([]string{"current"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !contains(err.Error(), "ensure tmux session") || !contains(err.Error(), "create failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAppRunCurrentPropagatesOpenSessionError(t *testing.T) {
	t.Parallel()

	app := &App{
		current: &currentCommand{
			currentPath: staticCurrentPath("/tmp/projmux"),
			sessions: &recordingCurrentSessionExecutor{
				openErr: errors.New("attach failed"),
			},
			identity: staticIdentity("dotfiles"),
			validate: func(string) error { return nil },
		},
	}

	err := app.Run([]string{"current"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !contains(err.Error(), "open tmux session") || !contains(err.Error(), "attach failed") {
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

type recordingCurrentSessionExecutor struct {
	ensureSessionName string
	ensureCWD         string
	openSessionName   string
	ensureErr         error
	openErr           error
}

func (r *recordingCurrentSessionExecutor) EnsureSession(_ context.Context, sessionName, cwd string) error {
	r.ensureSessionName = sessionName
	r.ensureCWD = cwd
	return r.ensureErr
}

func (r *recordingCurrentSessionExecutor) OpenSession(_ context.Context, sessionName string) error {
	r.openSessionName = sessionName
	return r.openErr
}

func contains(haystack, needle string) bool {
	return bytes.Contains([]byte(haystack), []byte(needle))
}
