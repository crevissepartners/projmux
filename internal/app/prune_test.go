package app

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/lifecycle"
)

func TestAppRunPruneEphemeralKillsTargetsBeyondKeep(t *testing.T) {
	t.Parallel()

	client := &recordingPruneClient{
		inventory: []lifecycle.SessionInventory{
			{Name: "newest", Ephemeral: true, LastAttached: 30},
			{Name: "middle", Ephemeral: true, LastAttached: 20},
			{Name: "older", Ephemeral: true, LastAttached: 10},
		},
	}
	reconcileCalls := 0
	app := &App{
		prune: &pruneCommand{
			inventory:       client,
			killer:          client,
			reconcileNotify: func() { reconcileCalls++ },
		},
	}

	if err := app.Run([]string{"prune", "ephemeral", "--keep=2"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := client.killed, []string{"older"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("KillSession calls = %#v, want %#v", got, want)
	}
	if reconcileCalls != 1 {
		t.Fatalf("notify reconcile calls = %d, want 1 after all kills", reconcileCalls)
	}
}

func TestPruneDoesNotReconcileNotifyWithoutKilledTargets(t *testing.T) {
	t.Parallel()

	client := &recordingPruneClient{
		inventory: []lifecycle.SessionInventory{{Name: "only", Ephemeral: true, LastAttached: 10}},
	}
	reconcileCalls := 0
	cmd := &pruneCommand{
		inventory:       client,
		killer:          client,
		reconcileNotify: func() { reconcileCalls++ },
	}
	if err := cmd.Run([]string{"ephemeral", "--keep=1"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if reconcileCalls != 0 {
		t.Fatalf("notify reconcile calls = %d, want 0", reconcileCalls)
	}
}

func TestPruneReconcilesNotifyAfterPartialKillFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		failAt             int
		wantReconcileCalls int
		wantSuccesses      int
	}{
		{
			name:               "partial success then failure",
			failAt:             1,
			wantReconcileCalls: 1,
			wantSuccesses:      1,
		},
		{
			name:          "first kill fails",
			failAt:        0,
			wantSuccesses: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			inventory := pruneInventoryResolverFunc(func(context.Context) ([]lifecycle.SessionInventory, error) {
				return []lifecycle.SessionInventory{
					{Name: "newest", Ephemeral: true, LastAttached: 30},
					{Name: "middle", Ephemeral: true, LastAttached: 20},
					{Name: "oldest", Ephemeral: true, LastAttached: 10},
				}, nil
			})
			killer := &failingPruneKiller{failAt: tt.failAt}
			reconcileCalls := 0
			cmd := &pruneCommand{
				inventory:       inventory,
				killer:          killer,
				reconcileNotify: func() { reconcileCalls++ },
			}

			err := cmd.Run([]string{"ephemeral", "--keep=0"}, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "kill ephemeral session") {
				t.Fatalf("Run() error = %v, want kill failure", err)
			}
			if reconcileCalls != tt.wantReconcileCalls {
				t.Fatalf("notify reconcile calls = %d, want %d", reconcileCalls, tt.wantReconcileCalls)
			}
			if len(killer.successes) != tt.wantSuccesses {
				t.Fatalf("successful kills = %v, want count %d", killer.successes, tt.wantSuccesses)
			}
		})
	}
}

func TestPruneCommandRejectsInvalidUsage(t *testing.T) {
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
			want:      "prune requires a subcommand",
			wantUsage: true,
		},
		{
			name:      "unknown subcommand",
			args:      []string{"nope"},
			want:      "unknown prune subcommand: nope",
			wantUsage: true,
		},
		{
			name:      "positional arguments",
			args:      []string{"ephemeral", "extra"},
			want:      "prune ephemeral does not accept positional arguments",
			wantUsage: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stderr bytes.Buffer
			err := (&pruneCommand{}).Run(tt.args, &bytes.Buffer{}, &stderr)
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

func TestPruneCommandPropagatesSetupErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cmd  *pruneCommand
		want string
	}{
		{
			name: "inventory missing",
			cmd:  &pruneCommand{},
			want: "resolve ephemeral sessions to prune",
		},
		{
			name: "inventory error",
			cmd: &pruneCommand{
				inventory: pruneInventoryResolverFunc(func(context.Context) ([]lifecycle.SessionInventory, error) {
					return nil, errors.New("tmux exploded")
				}),
				killer: &recordingPruneClient{},
			},
			want: "resolve ephemeral sessions to prune",
		},
		{
			name: "plan error",
			cmd: &pruneCommand{
				inventory: pruneInventoryResolverFunc(func(context.Context) ([]lifecycle.SessionInventory, error) {
					return nil, nil
				}),
				killer: &recordingPruneClient{},
			},
			want: "plan ephemeral prune",
		},
		{
			name: "killer missing",
			cmd: &pruneCommand{
				inventory: pruneInventoryResolverFunc(func(context.Context) ([]lifecycle.SessionInventory, error) {
					return []lifecycle.SessionInventory{{Name: "older", Ephemeral: true, LastAttached: 10}}, nil
				}),
			},
			want: "kill ephemeral sessions to prune",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.cmd.Run([]string{"ephemeral", "--keep=-1"}, &bytes.Buffer{}, &bytes.Buffer{})
			if tt.name == "killer missing" {
				err = tt.cmd.Run([]string{"ephemeral", "--keep=0"}, &bytes.Buffer{}, &bytes.Buffer{})
			}
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

type recordingPruneClient struct {
	inventory []lifecycle.SessionInventory
	killed    []string
}

type failingPruneKiller struct {
	failAt    int
	calls     int
	successes []string
}

func (k *failingPruneKiller) KillSession(_ context.Context, sessionName string) error {
	call := k.calls
	k.calls++
	if call == k.failAt {
		return errors.New("kill failed")
	}
	k.successes = append(k.successes, sessionName)
	return nil
}

func (c *recordingPruneClient) ListEphemeralSessions(context.Context) ([]lifecycle.SessionInventory, error) {
	return c.inventory, nil
}

func (c *recordingPruneClient) KillSession(_ context.Context, sessionName string) error {
	c.killed = append(c.killed, sessionName)
	return nil
}

type pruneInventoryResolverFunc func(context.Context) ([]lifecycle.SessionInventory, error)

func (fn pruneInventoryResolverFunc) ListEphemeralSessions(ctx context.Context) ([]lifecycle.SessionInventory, error) {
	return fn(ctx)
}
