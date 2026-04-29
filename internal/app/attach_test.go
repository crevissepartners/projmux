package app

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/lifecycle"
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
			inventory:  client,
			sessions:   client,
			killer:     client,
			homeDir:    func() (string, error) { return "/home/tester", nil },
			workingDir: func() (string, error) { return "/tmp/current", nil },
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

func TestAppRunAttachAutoCreatesEphemeralFallback(t *testing.T) {
	t.Parallel()

	client := &recordingAttachClient{
		inventory: []lifecycle.SessionInventory{
			{Name: "busy", Ephemeral: true, Attached: true, LastAttached: 30},
		},
	}
	app := &App{
		attach: &attachCommand{
			inventory:  client,
			sessions:   client,
			killer:     client,
			homeDir:    func() (string, error) { return "/home/tester", nil },
			workingDir: func() (string, error) { return "/work/hello world", nil },
			now: func() time.Time {
				return time.Date(2026, time.April, 23, 12, 34, 56, 0, time.UTC)
			},
		},
	}

	if err := app.Run([]string{"attach", "auto", "--fallback=ephemeral"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if client.ensured != nil {
		t.Fatalf("EnsureSession calls = %#v, want none", client.ensured)
	}
	wantCreated := []ensuredSession{{name: "hello-world-20260423-123456", cwd: "/work/hello world"}}
	if !reflect.DeepEqual(client.created, wantCreated) {
		t.Fatalf("CreateEphemeralSession calls = %#v, want %#v", client.created, wantCreated)
	}
	if got, want := client.opened, []string{"hello-world-20260423-123456"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("OpenSession calls = %#v, want %#v", got, want)
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
		{
			name:      "invalid fallback",
			args:      []string{"auto", "--fallback=sideways"},
			want:      "attach auto fallback must be one of: home, ephemeral",
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
				inventory:  &recordingAttachClient{},
				sessions:   &recordingAttachClient{},
				killer:     &recordingAttachClient{},
				homeDir:    func() (string, error) { return "", errors.New("no home") },
				workingDir: func() (string, error) { return "/tmp", nil },
				now:        time.Now,
			},
			want: "resolve auto-attach home directory",
		},
		{
			name: "inventory resolver",
			cmd: &attachCommand{
				inventory: attachInventoryResolverFunc(func(context.Context) ([]lifecycle.SessionInventory, error) {
					return nil, errors.New("tmux exploded")
				}),
				sessions:   &recordingAttachClient{},
				killer:     &recordingAttachClient{},
				homeDir:    func() (string, error) { return "/home/tester", nil },
				workingDir: func() (string, error) { return "/tmp", nil },
				now:        time.Now,
			},
			want: "resolve auto-attach inventory",
		},
		{
			name: "ensure missing",
			cmd: &attachCommand{
				inventory: attachInventoryResolverFunc(func(context.Context) ([]lifecycle.SessionInventory, error) {
					return nil, nil
				}),
				killer:     &recordingAttachClient{},
				homeDir:    func() (string, error) { return "/home/tester", nil },
				workingDir: func() (string, error) { return "/tmp", nil },
				now:        time.Now,
			},
			want: "ensure auto-attach home session",
		},
		{
			name: "open missing",
			cmd: &attachCommand{
				inventory: attachInventoryResolverFunc(func(context.Context) ([]lifecycle.SessionInventory, error) {
					return []lifecycle.SessionInventory{{Name: "ephemeral", Ephemeral: true, LastAttached: 10}}, nil
				}),
				killer:     &recordingAttachClient{},
				homeDir:    func() (string, error) { return "/home/tester", nil },
				workingDir: func() (string, error) { return "/tmp", nil },
				now:        time.Now,
			},
			want: "open auto-attach target",
		},
		{
			name: "ephemeral cwd",
			cmd: &attachCommand{
				inventory: attachInventoryResolverFunc(func(context.Context) ([]lifecycle.SessionInventory, error) {
					return nil, nil
				}),
				sessions: &recordingAttachClient{},
				killer:   &recordingAttachClient{},
				homeDir:  func() (string, error) { return "/home/tester", nil },
				workingDir: func() (string, error) {
					return "", errors.New("no cwd")
				},
				now: time.Now,
			},
			want: "resolve auto-attach working directory",
		},
		{
			name: "ephemeral create",
			cmd: &attachCommand{
				inventory: attachInventoryResolverFunc(func(context.Context) ([]lifecycle.SessionInventory, error) {
					return nil, nil
				}),
				sessions: &recordingAttachClient{createErr: errors.New("create failed")},
				killer:   &recordingAttachClient{},
				homeDir:  func() (string, error) { return "/home/tester", nil },
				workingDir: func() (string, error) {
					return "/tmp/work", nil
				},
				now: time.Now,
			},
			want: "create auto-attach ephemeral session",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			args := []string{"auto", "--keep=0"}
			if strings.Contains(tt.name, "ephemeral") {
				args = append(args, "--fallback=ephemeral")
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

type ensuredSession struct {
	name string
	cwd  string
}

type recordingAttachClient struct {
	inventory []lifecycle.SessionInventory
	ensured   []ensuredSession
	created   []ensuredSession
	opened    []string
	killed    []string
	createErr error
}

func (c *recordingAttachClient) ListEphemeralSessions(context.Context) ([]lifecycle.SessionInventory, error) {
	return c.inventory, nil
}

func (c *recordingAttachClient) EnsureSession(_ context.Context, sessionName, cwd string) error {
	c.ensured = append(c.ensured, ensuredSession{name: sessionName, cwd: cwd})
	return nil
}

func (c *recordingAttachClient) CreateEphemeralSession(_ context.Context, sessionName, cwd string) error {
	if c.createErr != nil {
		return c.createErr
	}
	c.created = append(c.created, ensuredSession{name: sessionName, cwd: cwd})
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
