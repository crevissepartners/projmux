package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	"github.com/crevissepartners/projmux/internal/testutil/liveguard"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func runtimeSDKClient(t *testing.T, socket string) *projmux.Client {
	t.Helper()
	c, err := projmux.NewClient(projmux.Options{SocketPath: socket})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func compareRuntimeSDK(t *testing.T, c *projmux.Client, req projmux.RuntimeRequest, flags ...string) core.RuntimeReport {
	t.Helper()
	var out, stderr bytes.Buffer
	args := append([]string{string(req.Kind), "-o", "json"}, flags...)
	if err := newGetCommand().runRuntime(args, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	report, err := c.ObserveRuntime(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got := sdkJSON(t, report); got != out.String() {
		t.Fatalf("runtime bytes differ:\nCLI %s\nSDK %s", out.String(), got)
	}
	return report
}

func TestRuntimeSDKCLIParity(t *testing.T) {
	liveguard.RequireActive(t)
	requireRealTmux(t)
	_, _ = sdkResourceFixture(t)
	// The guard already created this private directory; no default -L route is used.
	root, err := os.MkdirTemp(os.Getenv("TMUX_TMPDIR"), "sdk-")
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "runtime.sock")
	runner := tmux.ExecRunner{}
	run := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := runner.Run(ctx, "tmux", append([]string{"-S", socket}, args...)...)
		if err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("-f", "/dev/null", "new-session", "-d", "-s", "alpha", "-n", "main", "tail -f /dev/null")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := runner.Run(ctx, "tmux", "-S", socket, "display-message", "-p", "#{socket_path}")
		if err != nil || strings.TrimSpace(string(out)) != socket {
			t.Errorf("exact socket cleanup proof: %s %v", out, err)
			return
		}
		if _, err := runner.Run(ctx, "tmux", "-S", socket, "kill-server"); err != nil {
			t.Error(err)
			return
		}
		if _, err := runner.Run(ctx, "tmux", "-S", socket, "has-session"); err == nil {
			t.Error("isolated server survived cleanup")
			return
		}
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	run("set-option", "-g", "@projmux_app", "1")
	run("set-option", "-t", "alpha", tmuxopts.ProjectUIDSession, "prj-alpha")
	run("set-option", "-w", "-t", "alpha:main", tmuxopts.WindowUID, "win-alpha-main")
	run("set-option", "-p", "-t", "alpha:main.0", tmuxopts.PaneUID, "pan-alpha-zsh")
	run("new-window", "-d", "-t", "alpha", "-n", "unknown", "tail -f /dev/null")
	run("set-option", "-w", "-t", "alpha:unknown", tmuxopts.WindowUID, "win-missing")
	run("new-window", "-d", "-t", "alpha", "-n", "duplicate", "tail -f /dev/null")
	run("set-option", "-p", "-t", "alpha:duplicate.0", tmuxopts.PaneUID, "pan-alpha-zsh")
	run("new-session", "-d", "-s", "Home", "tail -f /dev/null")
	run("set-option", "-t", "Home", tmuxopts.SessionRole, "control")
	run("new-session", "-d", "-s", "scratch", "tail -f /dev/null")
	run("set-option", "-t", "scratch", tmuxopts.EphemeralSession, "1")
	client := runtimeSDKClient(t, "")
	for _, kind := range []core.RuntimeObjectKind{core.RuntimeSessions, core.RuntimeWindows, core.RuntimePanes} {
		t.Run(string(kind), func(t *testing.T) {
			request := projmux.RuntimeRequest{Kind: kind, SocketPath: socket}
			report := compareRuntimeSDK(t, client, request, "--socket-path", socket)
			if len(report.Items) == 0 || report.HostMode != "app-owned" {
				t.Fatalf("missing observation: %+v", report)
			}
			compareRuntimeSDK(t, runtimeSDKClient(t, socket), projmux.RuntimeRequest{Kind: kind}, "--socket-path", socket)
			absent := filepath.Join(root, "absent.sock")
			unavailable := compareRuntimeSDK(t, client, projmux.RuntimeRequest{Kind: kind, SocketPath: absent}, "--socket-path", absent)
			if len(unavailable.Unavailable) == 0 || unavailable.Items == nil || len(unavailable.Items) != 0 {
				t.Fatalf("bad unavailable: %+v", unavailable)
			}
			compareRuntimeSDK(t, client, projmux.RuntimeRequest{Kind: kind})
			compareRuntimeSDK(t, runtimeSDKClient(t, socket), projmux.RuntimeRequest{Kind: kind, SocketName: "sdk-absent"}, "--socket", "sdk-absent")
			// Request routing wins over Options, including an explicit absent socket.
			compareRuntimeSDK(t, runtimeSDKClient(t, socket), projmux.RuntimeRequest{Kind: kind, SocketPath: absent}, "--socket-path", absent)
		})
	}
	// Every mutable nested member can be changed without affecting either Client.
	req := projmux.RuntimeRequest{Kind: core.RuntimePanes, SocketPath: socket}
	report, err := client.ObserveRuntime(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	want := sdkJSON(t, report)
	foundConflict := false
	for i := range report.Items {
		row := &report.Items[i]
		row.Name = "changed"
		if row.Resource != nil {
			row.Resource.UID = "changed"
		}
		for j := range row.Conflicts {
			foundConflict = true
			row.Conflicts[j].Targets[0] = "changed"
		}
	}
	if !foundConflict {
		t.Fatal("fixture did not exercise nested conflicts")
	}
	for _, c := range []*projmux.Client{client, runtimeSDKClient(t, "")} {
		next, err := c.ObserveRuntime(context.Background(), req)
		if err != nil || sdkJSON(t, next) != want {
			t.Fatal("returned data changed subsequent read", err)
		}
	}
	run("set-option", "-g", "@projmux_app", "0")
	compareRuntimeSDK(t, client, projmux.RuntimeRequest{Kind: core.RuntimeWindows, SocketPath: socket}, "--socket-path", socket)
}

func TestRuntimeSDKRefusalParity(t *testing.T) {
	_, _ = sdkResourceFixture(t)
	c := runtimeSDKClient(t, "")
	for _, test := range []struct {
		req  projmux.RuntimeRequest
		args []string
	}{
		{projmux.RuntimeRequest{Kind: core.RuntimeSessions, SocketPath: "relative"}, []string{"sessions", "--socket-path", "relative"}},
		{projmux.RuntimeRequest{Kind: core.RuntimeWindows, SocketName: "one", SocketPath: "/absolute/two"}, []string{"windows", "--socket", "one", "--socket-path", "/absolute/two"}},
		{projmux.RuntimeRequest{}, nil},
		{projmux.RuntimeRequest{Kind: "unsupported"}, []string{"unsupported"}},
	} {
		var out, stderr bytes.Buffer
		cliErr := newGetCommand().runRuntime(test.args, &out, &stderr)
		_, sdkErr := c.ObserveRuntime(context.Background(), test.req)
		var refusal *projmux.Refusal
		if !errors.As(sdkErr, &refusal) || refusal.Code != projmux.CodeRuntimeRequestInvalid || cliErr == nil || cliErr.Error() != refusal.Error() || !IsUsageError(cliErr) || out.Len() != 0 {
			t.Fatalf("CLI=%v SDK=%v", cliErr, sdkErr)
		}
	}
}

func TestRuntimeSDKCanceledBeforeTmux(t *testing.T) {
	_, _ = sdkResourceFixture(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "called")
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte("#!/bin/sh\nprintf called > '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := runtimeSDKClient(t, filepath.Join(dir, "server.sock")).ObserveRuntime(ctx, projmux.RuntimeRequest{Kind: core.RuntimeSessions})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled observation called tmux", err)
	}
}

func TestRuntimeSDKNoRouteIgnoresEnvironmentWithoutTmux(t *testing.T) {
	_, _ = sdkResourceFixture(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "called")
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte("#!/bin/sh\nprintf called > '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("TMUX", filepath.Join(dir, "inherited.sock")+",123,0")
	c := runtimeSDKClient(t, "")
	req := projmux.RuntimeRequest{Kind: core.RuntimeSessions}
	report, err := c.ObserveRuntime(context.Background(), req)
	if err != nil || report.Transport.Kind != "none" || len(report.Unavailable) == 0 {
		t.Fatalf("%+v %v", report, err)
	}
	want := sdkJSON(t, report)
	report.Unavailable[0].Reason = "changed"
	report.Items = append(report.Items, core.RuntimeRow{ID: "changed"})
	next, err := c.ObserveRuntime(context.Background(), req)
	if err != nil || sdkJSON(t, next) != want {
		t.Fatal("unavailability mutation changed next call", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("no-route observation called tmux", err)
	}
}
