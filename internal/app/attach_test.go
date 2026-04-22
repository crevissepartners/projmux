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

func TestAppRunAttachAutoReusesEphemeralSession(t *testing.T) {
	t.Parallel()

	client := &recordingAttachClient{
		inventory: []lifecycle.SessionInventory{
			{Name: "ephemeral", Ephemeral: true, LastAttached: 20},
			{Name: "older", Ephemeral: true, LastAttached: 10},
		},
	}
	app := &App{
		attach: &attachCommand{
			inventory: client,
			sessions:  client,
			killer:    client,
			homeDir:   func() (string, error) { return "/home/tester", nil },
		},
	}

	if err := app.Run([]string{"attach", "auto"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if client.ensured != nil {
		t.Fatalf("EnsureSession calls = %#v, want none", client.ensured)
	}
	if got, want := client.opened, []string{"ephemeral"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("OpenSession calls = %#v, want %#v", got, want)
	}
	if client.killed != nil {
		t.Fatalf("KillSession calls = %#v, want none", client.killed)
	}
}

func TestAppRunAttachAutoPrunesAndEnsuresHome(t *testing.T) {
	t.Parallel()

	client := &recordingAttachClient{
		inventory: []lifecycle.SessionInventory{
			{Name: "newest", Ephemeral: true, Attached: true, LastAttached: 30},
			{Name: "middle", Ephemeral: true, Attached: true, LastAttached: 20},
			{Name: "older", Ephemeral: true, Attached: true, LastAttached: 10},
		},
	}
	app := &App{
		attach: &attachCommand{
			inventory: client,
			sessions:  client,
			killer:    client,
			homeDir:   func() (string, error) { return "/home/tester", nil },
		},
	}

	if err := app.Run([]string{"attach", "auto", "--keep=1"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	wantEnsured := []ensuredSession{{name: "home", cwd: "/home/tester"}}
	if !reflect.DeepEqual(client.ensured, wantEnsured) {
		t.Fatalf("EnsureSession calls = %#v, want %#v", client.ensured, wantEnsured)
	}
	if got, want := client.opened, []string{"home"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("OpenSession calls = %#v, want %#v", got, want)
	}
	if client.killed != nil {
		t.Fatalf("KillSession calls = %#v, want none", client.killed)
	}
}

func TestAttachCommandRejectsInvalidUsage(t *testing.T) {
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
			want:      "attach requires a subcommand",
			wantUsage: true,
		},
		{
			name:      "unknown subcommand",
			args:      []string{"nope"},
			want:      "unknown attach subcommand: nope",
			wantUsage: true,
		},
		{
			name:      "positional arguments",
			args:      []string{"auto", "extra"},
			want:      "attach auto does not accept positional arguments",
			wantUsage: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stderr bytes.Buffer
			err := (&attachCommand{}).Run(tt.args, &bytes.Buffer{}, &stderr)
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

func TestAttachCommandPropagatesSetupErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cmd  *attachCommand
		want string
	}{
		{
			name: "home resolver",
			cmd: &attachCommand{
				inventory: &recordingAttachClient{},
				sessions:  &recordingAttachClient{},
				killer:    &recordingAttachClient{},
				homeDir:   func() (string, error) { return "", errors.New("no home") },
			},
			want: "resolve auto-attach home directory",
		},
		{
			name: "inventory resolver",
			cmd: &attachCommand{
				inventory: attachInventoryResolverFunc(func(context.Context) ([]lifecycle.SessionInventory, error) {
					return nil, errors.New("tmux exploded")
				}),
				sessions: &recordingAttachClient{},
				killer:   &recordingAttachClient{},
				homeDir:  func() (string, error) { return "/home/tester", nil },
			},
			want: "resolve auto-attach inventory",
		},
		{
			name: "ensure missing",
			cmd: &attachCommand{
				inventory: attachInventoryResolverFunc(func(context.Context) ([]lifecycle.SessionInventory, error) {
					return nil, nil
				}),
				killer:  &recordingAttachClient{},
				homeDir: func() (string, error) { return "/home/tester", nil },
			},
			want: "ensure auto-attach home session",
		},
		{
			name: "open missing",
			cmd: &attachCommand{
				inventory: attachInventoryResolverFunc(func(context.Context) ([]lifecycle.SessionInventory, error) {
					return []lifecycle.SessionInventory{{Name: "ephemeral", Ephemeral: true, LastAttached: 10}}, nil
				}),
				killer:  &recordingAttachClient{},
				homeDir: func() (string, error) { return "/home/tester", nil },
			},
			want: "open auto-attach target",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.cmd.Run([]string{"auto", "--keep=0"}, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

type ensuredSession struct {
	name string
	cwd  string
}

type recordingAttachClient struct {
	inventory []lifecycle.SessionInventory
	ensured   []ensuredSession
	opened    []string
	killed    []string
}

func (c *recordingAttachClient) ListEphemeralSessions(context.Context) ([]lifecycle.SessionInventory, error) {
	return c.inventory, nil
}

func (c *recordingAttachClient) EnsureSession(_ context.Context, sessionName, cwd string) error {
	c.ensured = append(c.ensured, ensuredSession{name: sessionName, cwd: cwd})
	return nil
}

func (c *recordingAttachClient) OpenSession(_ context.Context, sessionName string) error {
	c.opened = append(c.opened, sessionName)
	return nil
}

func (c *recordingAttachClient) KillSession(_ context.Context, sessionName string) error {
	c.killed = append(c.killed, sessionName)
	return nil
}

type attachInventoryResolverFunc func(context.Context) ([]lifecycle.SessionInventory, error)

func (fn attachInventoryResolverFunc) ListEphemeralSessions(ctx context.Context) ([]lifecycle.SessionInventory, error) {
	return fn(ctx)
}
