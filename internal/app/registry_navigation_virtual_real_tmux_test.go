package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/registryview"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/testutil/liveguard"
)

func TestRegistryNavigationOpenVirtualWindowMovesRealClient(t *testing.T) {
	liveguard.RequireActive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	server := startRealTmuxNameHandoffServer(t, ctx)
	if out, err := server.tmux("new-session", "-d", "-s", "bootstrap", "tail", "-f", "/dev/null"); err != nil {
		t.Fatalf("bootstrap: %s %v", out, err)
	}
	t.Cleanup(server.killServer)
	server.seed(t)
	root := filepath.Join(server.root, "project")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	store := newFakeResourceStore(t)
	project, _ := store.registry.Project("prj-alpha")
	project.Spec.Root = root
	store.dirs[root] = true
	value := virtualPrimaryProjectFixture(t, store)
	project, _ = store.registry.Project(value.Metadata.UID)
	project.Status.Session.SocketPath = server.socket
	runner := shellTmuxExecRunner{env: func() []string { return server.environment }}
	create, err := virtualWindowCreator(runner, func(string) string { return "" }, store.store(), server.socket)
	if err != nil {
		t.Fatal(err)
	}
	create.reconciler.discoverRoots = func() ([]string, error) { return nil, nil }
	create.runtime.executable = func() (string, error) { return "", errors.New("fixture has no supervisor") }
	client := exec.CommandContext(ctx, "tmux", "-S", server.socket, "-f", "/dev/null", "-C", "attach-session", "-t", "bootstrap")
	client.Env = server.environment
	stdin, err := client.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = client.Process.Kill(); _ = client.Wait() })
	clientName := ""
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		clientName, err = server.tmux("list-clients", "-F", "#{client_name}")
		if err == nil && clientName != "" {
			break
		}
	}
	if clientName == "" {
		t.Fatal("control client did not attach")
	}
	focus := newFocusCommand()
	focus.runner, focus.lookupEnv, focus.loadRegistry = runner, func(string) string { return "" }, store.store().load
	focus.materializeVirtualWindow = func(_ context.Context, uid, socket string) error {
		if uid != value.Spec.PrimaryWindowRef || socket != server.socket {
			t.Fatal("materializer changed exact route")
		}
		return create.materializeVirtualShell(uid)
	}
	row, ok := registryview.Build(registryview.Input{Graph: resourcegraph.Resolve(store.registry, resourcegraph.Inventory{})}).Row("uid:" + value.Spec.PrimaryWindowRef)
	if !ok || !row.Allows(registryview.ActionOpen) || row.Status != registryview.StatusVirtual {
		t.Fatalf("row=%+v", row)
	}
	nav := &registryNavigationCommand{focus: focus}
	var out, stderr bytes.Buffer
	if err := nav.runFocus(row, server.socket, &out, &stderr); err != nil {
		t.Fatalf("Open: %v %s", err, stderr.String())
	}
	window, _ := store.registry.Window(row.UID)
	current, err := server.tmux("display-message", "-p", "-c", clientName, "#{session_name}\t#{window_id}")
	if err != nil || current != "alpha\t"+window.Status.RuntimeID {
		t.Fatalf("client landed %q, window=%+v err=%v", current, window, err)
	}
	panes, err := server.tmux("list-panes", "-t", window.Status.RuntimeID, "-F", "#{pane_id}")
	if err != nil || len(strings.Fields(panes)) != 1 || virtualTestPaneCount(store.registry, row.UID) != 2 || store.registry.IsVirtualWindow(row.UID) {
		t.Fatalf("Open topology Panes=%q Window=%+v err=%v", panes, window, err)
	}
	// The cached virtual row still routes by UID; opening it again must reuse the
	// materialized Window without adding a second shell.
	if err := nav.runFocus(row, server.socket, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	if virtualTestPaneCount(store.registry, row.UID) != 2 {
		t.Fatal("repeated Open created another shell")
	}
}
