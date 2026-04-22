package tmux

import (
	"context"
	"errors"
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

type staticRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

func (fn staticRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return fn(ctx, name, args...)
}
