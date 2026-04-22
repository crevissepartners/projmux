package app

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/es5h/projmux/internal/core/lifecycle"
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
	app := &App{
		prune: &pruneCommand{
			inventory: client,
			killer:    client,
		},
	}

	if err := app.Run([]string{"prune", "ephemeral", "--keep=2"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := client.killed, []string{"older"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("KillSession calls = %#v, want %#v", got, want)
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
