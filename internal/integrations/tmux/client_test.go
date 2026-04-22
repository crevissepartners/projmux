package tmux

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestClientCurrentPanePathTrimsOutput(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("/tmp/projmux\n"), nil
	}))

	path, err := client.CurrentPanePath(context.Background())
	if err != nil {
		t.Fatalf("CurrentPanePath returned error: %v", err)
	}
	if path != "/tmp/projmux" {
		t.Fatalf("unexpected path %q", path)
	}
}

func TestClientCurrentPanePathRejectsEmptyOutput(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte(" \n"), nil
	}))

	_, err := client.CurrentPanePath(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, errCurrentPanePathUnavailable) {
		t.Fatalf("expected unavailable error, got %v", err)
	}
}

func TestClientCurrentPanePathWrapsRunnerError(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("tmux failed")
	}))

	_, err := client.CurrentPanePath(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "resolve current tmux pane path") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientCurrentSessionNameTrimsOutput(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("dotfiles\n"), nil
	}))

	sessionName, err := client.CurrentSessionName(context.Background())
	if err != nil {
		t.Fatalf("CurrentSessionName returned error: %v", err)
	}
	if sessionName != "dotfiles" {
		t.Fatalf("unexpected session name %q", sessionName)
	}
}

func TestClientCurrentSessionNameRejectsEmptyOutput(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte(" \n"), nil
	}))

	_, err := client.CurrentSessionName(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, errCurrentSessionUnavailable) {
		t.Fatalf("expected unavailable error, got %v", err)
	}
}

func TestClientCurrentSessionNameWrapsRunnerError(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("tmux failed")
	}))

	_, err := client.CurrentSessionName(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "resolve current tmux session") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientRecentSessionsSortsByActivityDescending(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("10\tstale\n35\tfresh\n35\ttie-kept-order\n"), nil
	}))

	sessions, err := client.RecentSessions(context.Background())
	if err != nil {
		t.Fatalf("RecentSessions returned error: %v", err)
	}

	want := []string{"fresh", "tie-kept-order", "stale"}
	if !reflect.DeepEqual(sessions, want) {
		t.Fatalf("RecentSessions = %#v, want %#v", sessions, want)
	}
}

func TestClientRecentSessionsReturnsEmptyListForNoOutput(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte(""), nil
	}))

	sessions, err := client.RecentSessions(context.Background())
	if err != nil {
		t.Fatalf("RecentSessions returned error: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("RecentSessions = %#v, want empty", sessions)
	}
}

func TestClientRecentSessionsWrapsRunnerError(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("tmux failed")
	}))

	_, err := client.RecentSessions(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "list recent tmux sessions") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientRecentSessionsRejectsMalformedRows(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("missing-tab-row"), nil
	}))

	_, err := client.RecentSessions(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "malformed row") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientRecentSessionsRejectsInvalidActivity(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("oops\tdotfiles"), nil
	}))

	_, err := client.RecentSessions(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, errSessionActivityInvalid) {
		t.Fatalf("RecentSessions error = %v, want %v", err, errSessionActivityInvalid)
	}
}

func TestClientRecentSessionsRejectsEmptySessionNames(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("10\t \n"), nil
	}))

	_, err := client.RecentSessions(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, errSessionNameRequired) {
		t.Fatalf("RecentSessions error = %v, want %v", err, errSessionNameRequired)
	}
}

func TestClientEnsureSessionCreatesMissingSession(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		t: t,
		steps: []scriptedStep{
			{err: exitError(t, 1)},
			{},
		},
	}
	client := NewClient(runner)

	if err := client.EnsureSession(context.Background(), "dotfiles", "/tmp/projmux"); err != nil {
		t.Fatalf("EnsureSession returned error: %v", err)
	}

	want := []commandCall{
		{name: "tmux", args: []string{"has-session", "-t", "dotfiles"}},
		{name: "tmux", args: []string{"new-session", "-d", "-s", "dotfiles", "-c", "/tmp/projmux"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("unexpected calls %#v", runner.calls)
	}
}

func TestClientEnsureSessionSkipsCreateWhenSessionExists(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		t:     t,
		steps: []scriptedStep{{}},
	}
	client := NewClient(runner)

	if err := client.EnsureSession(context.Background(), "dotfiles", "/tmp/projmux"); err != nil {
		t.Fatalf("EnsureSession returned error: %v", err)
	}

	want := []commandCall{
		{name: "tmux", args: []string{"has-session", "-t", "dotfiles"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("unexpected calls %#v", runner.calls)
	}
}

func TestClientEnsureSessionWrapsLookupError(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("tmux failed")
	}))

	err := client.EnsureSession(context.Background(), "dotfiles", "/tmp/projmux")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "check tmux session") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientSessionExistsReturnsTrueWhenSessionExists(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		t:     t,
		steps: []scriptedStep{{}},
	}
	client := NewClient(runner)

	exists, err := client.SessionExists(context.Background(), "dotfiles")
	if err != nil {
		t.Fatalf("SessionExists returned error: %v", err)
	}
	if !exists {
		t.Fatal("SessionExists = false, want true")
	}
}

func TestClientSessionExistsReturnsFalseWhenSessionIsMissing(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		t:     t,
		steps: []scriptedStep{{err: exitError(t, 1)}},
	}
	client := NewClient(runner)

	exists, err := client.SessionExists(context.Background(), "dotfiles")
	if err != nil {
		t.Fatalf("SessionExists returned error: %v", err)
	}
	if exists {
		t.Fatal("SessionExists = true, want false")
	}
}

func TestClientOpenSessionSwitchesInsideTmux(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		t:     t,
		steps: []scriptedStep{{}},
	}
	client := newClientWithEnv(runner, func(string) string { return "/tmp/tmux-sock" })

	if err := client.OpenSession(context.Background(), "dotfiles"); err != nil {
		t.Fatalf("OpenSession returned error: %v", err)
	}

	want := []commandCall{
		{name: "tmux", args: []string{"switch-client", "-t", "dotfiles"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("unexpected calls %#v", runner.calls)
	}
}

func TestClientOpenSessionAttachesOutsideTmux(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		t:     t,
		steps: []scriptedStep{{}},
	}
	client := newClientWithEnv(runner, func(string) string { return "" })

	if err := client.OpenSession(context.Background(), "dotfiles"); err != nil {
		t.Fatalf("OpenSession returned error: %v", err)
	}

	want := []commandCall{
		{name: "tmux", args: []string{"attach-session", "-t", "dotfiles"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("unexpected calls %#v", runner.calls)
	}
}

func TestClientOpenSessionRequiresSessionName(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("runner should not be called")
		return nil, nil
	}))

	err := client.OpenSession(context.Background(), "  ")
	if !errors.Is(err, errSessionNameRequired) {
		t.Fatalf("OpenSession error = %v, want %v", err, errSessionNameRequired)
	}
}

func TestClientSwitchClientRunsTmuxSwitch(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		t:     t,
		steps: []scriptedStep{{}},
	}
	client := NewClient(runner)

	if err := client.SwitchClient(context.Background(), "dotfiles"); err != nil {
		t.Fatalf("SwitchClient returned error: %v", err)
	}

	want := []commandCall{
		{name: "tmux", args: []string{"switch-client", "-t", "dotfiles"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("unexpected calls %#v", runner.calls)
	}
}

func TestClientSwitchClientRequiresSessionName(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("runner should not be called")
		return nil, nil
	}))

	err := client.SwitchClient(context.Background(), "")
	if !errors.Is(err, errSessionNameRequired) {
		t.Fatalf("SwitchClient error = %v, want %v", err, errSessionNameRequired)
	}
}

func TestClientSwitchClientWrapsRunnerError(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("tmux failed")
	}))

	err := client.SwitchClient(context.Background(), "dotfiles")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "switch tmux client") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientKillSessionRunsTmuxKill(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		t:     t,
		steps: []scriptedStep{{}},
	}
	client := NewClient(runner)

	if err := client.KillSession(context.Background(), "dotfiles"); err != nil {
		t.Fatalf("KillSession returned error: %v", err)
	}

	want := []commandCall{
		{name: "tmux", args: []string{"kill-session", "-t", "dotfiles"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("unexpected calls %#v", runner.calls)
	}
}

func TestClientKillSessionRequiresSessionName(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("runner should not be called")
		return nil, nil
	}))

	err := client.KillSession(context.Background(), "  ")
	if !errors.Is(err, errSessionNameRequired) {
		t.Fatalf("KillSession error = %v, want %v", err, errSessionNameRequired)
	}
}

func TestClientKillSessionWrapsRunnerError(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("tmux failed")
	}))

	err := client.KillSession(context.Background(), "dotfiles")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "kill tmux session") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientEnsureSessionRequiresCWD(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("runner should not be called")
		return nil, nil
	}))

	err := client.EnsureSession(context.Background(), "dotfiles", "")
	if !errors.Is(err, errSessionCWDRequired) {
		t.Fatalf("EnsureSession error = %v, want %v", err, errSessionCWDRequired)
	}
}

type staticRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

func (fn staticRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return fn(ctx, name, args...)
}

type commandCall struct {
	name string
	args []string
}

type scriptedStep struct {
	output []byte
	err    error
}

type scriptedRunner struct {
	t     *testing.T
	steps []scriptedStep
	calls []commandCall
}

func (r *scriptedRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, commandCall{name: name, args: append([]string(nil), args...)})
	if len(r.steps) == 0 {
		r.t.Fatalf("unexpected command %s %v", name, args)
	}

	step := r.steps[0]
	r.steps = r.steps[1:]
	return step.output, step.err
}

func exitError(t *testing.T, code int) error {
	t.Helper()

	err := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
	if err == nil {
		t.Fatalf("expected exit error for status %d", code)
	}

	return err
}
