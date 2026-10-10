package app

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	"github.com/crevissepartners/projmux/internal/testutil/liveguard"
)

type virtualTmuxTestLauncher struct{ *fakeAgentLauncher }

func (l virtualTmuxTestLauncher) PlanAgentLaunch(string, coremetadata.AgentWorkspace, []string) (string, []string, error) {
	return "fixture", []string{"tail", "-f", "/dev/null"}, nil
}

func TestVirtualWindowRequestedPaneRealTmux(t *testing.T) {
	liveguard.RequireActive(t)
	for _, agent := range []bool{false, true} {
		t.Run(map[bool]string{false: "shell", true: "agent"}[agent], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			server := startRealTmuxNameHandoffServer(t, ctx)
			if out, err := server.tmux("new-session", "-d", "-s", "bootstrap", "tail", "-f", "/dev/null"); err != nil {
				t.Fatalf("bootstrap: %v %s", err, out)
			}
			t.Cleanup(server.killServer)
			server.seed(t)
			root := filepath.Join(server.root, "project")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			store := newFakeResourceStore(t)
			p, _ := store.registry.Project("prj-alpha")
			p.Spec.Root = root
			store.dirs[root] = true
			project := virtualPrimaryProjectFixture(t, store)
			runner := shellTmuxExecRunner{env: func() []string { return server.environment }}
			command, err := virtualWindowCreator(runner, func(string) string { return "" }, store.store(), server.socket)
			if err != nil {
				t.Fatal(err)
			}
			command.reconciler.discoverRoots = func() ([]string, error) { return nil, nil }
			command.runtime.executable = func() (string, error) { return "", errors.New("fixture has no supervisor binary") }
			command.agents = virtualTmuxTestLauncher{newFakeAgentLauncher()}
			args := []string{"pane", "--project", "uid:" + project.Metadata.UID, "--window", "uid:" + project.Spec.PrimaryWindowRef}
			if agent {
				args = append([]string{"agent", "--provider", "claude", "--profile", "none"}, args[1:]...)
			} else {
				args = append(args, "--", "tail", "-f", "/dev/null")
			}
			stdout, stderr, err := runRoute(t, command, args...)
			if err != nil {
				t.Fatalf("create: %v %s %s", err, stdout, stderr)
			}
			window, _ := store.registry.Window(project.Spec.PrimaryWindowRef)
			panes, err := server.tmux("list-panes", "-t", window.Status.RuntimeID, "-F", "#{pane_id}")
			if err != nil || len(strings.Split(panes, "\n")) != 1 {
				t.Fatalf("runtime Panes=%q %v", panes, err)
			}
			anchor, _ := store.registry.WindowAnchor(window.Metadata.UID)
			if anchor == nil || anchor.Status.Activation.RuntimeID != panes {
				t.Fatal("requested Pane is not the anchor")
			}
			if virtualTestPaneCount(store.registry, window.Metadata.UID) != 2 {
				t.Fatal("unexpected Registry Pane")
			}
			if agent && window.Spec.DefaultShellPaneRef != "" {
				t.Fatal("Agent got an extra shell")
			}
			// This stopped Project acquired its session in the create transaction.
			current, _ := store.registry.Project(project.Metadata.UID)
			if current.Status.Session == nil || !current.Status.Session.Live || current.Status.Session.SocketPath != server.socket {
				t.Fatalf("session=%+v", current.Status.Session)
			}
			if err := store.registry.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func virtualWindowMissingServerFixture(t *testing.T, ctx context.Context, store *fakeResourceStore) (realTmuxNameHandoffServer, coremetadata.Project, string) {
	t.Helper()
	server := startRealTmuxNameHandoffServer(t, ctx)
	server.socket = filepath.Join(filepath.Dir(server.socket), defaultAppSocket)
	t.Cleanup(server.killServer)
	root := filepath.Join(server.root, "project")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(server.root, "generated.conf")
	config := "set -g " + tmuxopts.AppGlobal + " 1\nset -g " + runtimeMutationSocketNameOption + " " + defaultAppSocket + "\nset -g default-shell /bin/sh\n"
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	p, _ := store.registry.Project("prj-alpha")
	p.Spec.Root = root
	store.dirs[root] = true
	project := virtualPrimaryProjectFixture(t, store)
	p, _ = store.registry.Project(project.Metadata.UID)
	p.Status.Session = &coremetadata.SessionProjection{Name: "alpha", SocketPath: server.socket}
	return server, project, configPath
}

func TestVirtualWindowFocusMissingServerRefusesBeforeMaterializationRealTmux(t *testing.T) {
	liveguard.RequireActive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	store := newFakeResourceStore(t)
	server, project, _ := virtualWindowMissingServerFixture(t, ctx, store)
	runner := shellTmuxExecRunner{env: func() []string { return server.environment }}
	before := store.registry.Clone()
	called := false
	focus := newFocusCommand()
	focus.runner, focus.lookupEnv, focus.loadRegistry = runner, func(string) string { return "" }, store.store().load
	focus.materializeVirtualWindow = func(context.Context, string, string) error { called = true; return nil }
	_, _, err := focus.resolveUIDNavigation(ctx, focusOptions{NavKind: "window", NavRef: "uid:" + project.Spec.PrimaryWindowRef})
	if err == nil || !strings.Contains(err.Error(), "projmux attach project uid:"+project.Metadata.UID) {
		t.Fatalf("focus error=%v", err)
	}
	if called || store.writes != 0 || !reflect.DeepEqual(before, store.registry) {
		t.Fatal("missing-server focus attempted materialization or changed Registry")
	}
	if _, err := os.Stat(server.socket); !os.IsNotExist(err) {
		t.Fatalf("focus created a server socket: %v", err)
	}
}

func TestOpenAttachVirtualWindowStartsMissingServerRealTmux(t *testing.T) {
	liveguard.RequireActive(t)
	for _, route := range []string{"open", "attach"} {
		t.Run(route, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			live := map[string]bool{}
			verb, store, executor := lifecycleVerbFixture(t, projectLifecycleOpen, true, live)
			server, project, configPath := virtualWindowMissingServerFixture(t, ctx, store)
			runner := shellTmuxExecRunner{env: func() []string { return server.environment }}
			create, err := virtualWindowCreator(runner, func(string) string { return "" }, store.store(), "")
			if err != nil {
				t.Fatal(err)
			}
			create.runtime.configPath = configPath
			create.runtime.executable = func() (string, error) { return "", errors.New("fixture has no supervisor binary") }
			verb.switcher.managedStopStore = store.store()
			verb.switcher.materializeVirtualWindow = func(_ context.Context, uid string) (virtualWindowShellMaterialization, error) {
				result, err := create.materializeVirtualShellResult(uid)
				if err == nil {
					live["alpha"] = true
				}
				return result, err
			}
			if route == "open" {
				_, _, err = runRoute(t, verb, "project", "uid:"+project.Metadata.UID)
			} else {
				attach := &attachCommand{store: store.store(), switcher: verb.switcher, lookupEnv: func(string) string { return "" }}
				_, _, err = runRoute(t, attach, "project", "uid:"+project.Metadata.UID)
			}
			if err != nil {
				t.Fatal(err)
			}
			window, _ := store.registry.Window(project.Spec.PrimaryWindowRef)
			list := exec.CommandContext(ctx, "tmux", "-S", server.socket, "list-panes", "-t", window.Status.RuntimeID, "-F", "#{pane_id}")
			list.Env = server.environment
			panes, err := list.Output()
			if err != nil || len(strings.Fields(string(panes))) != 1 {
				t.Fatalf("Panes=%q %v", panes, err)
			}
			p, _ := store.registry.Project(project.Metadata.UID)
			if store.registry.IsVirtualWindow(window.Metadata.UID) || window.Spec.DefaultShellPaneRef == "" || p.Status.Session == nil || !p.Status.Session.Live || p.Status.Session.SocketPath != server.socket || len(executor.calls) == 0 {
				t.Fatalf("Project=%+v Window=%+v handoff=%v", p.Status.Session, window, executor.calls)
			}
			if err := store.registry.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestVirtualWindowMaterializeExitAndFocusAgainRealTmux(t *testing.T) {
	liveguard.RequireActive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	server := startRealTmuxNameHandoffServer(t, ctx)
	if out, err := server.tmux("new-session", "-d", "-s", "bootstrap", "tail", "-f", "/dev/null"); err != nil {
		t.Fatalf("bootstrap: %v %s", err, out)
	}
	t.Cleanup(server.killServer)
	server.seed(t)
	root := filepath.Join(server.root, "project")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	store := newFakeResourceStore(t)
	p, _ := store.registry.Project("prj-alpha")
	p.Spec.Root = root
	store.dirs[root] = true
	project := virtualPrimaryProjectFixture(t, store)
	windowUID := project.Spec.PrimaryWindowRef
	processPane := store.registry.AgentsOf(windowUID)[0].Status.PaneRef
	runner := shellTmuxExecRunner{env: func() []string { return server.environment }}
	create, err := virtualWindowCreator(runner, func(string) string { return "" }, store.store(), server.socket)
	if err != nil {
		t.Fatal(err)
	}
	create.reconciler.discoverRoots = func() ([]string, error) { return nil, nil }
	create.runtime.executable = func() (string, error) { return "", errors.New("fixture has no supervisor binary") }
	_, _, err = runRoute(t, create, "pane", "--project", "uid:"+project.Metadata.UID, "--window", "uid:"+windowUID, "--", "/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	window, _ := store.registry.Window(windowUID)
	firstWindow, firstPane := window.Status.RuntimeID, window.Spec.AnchorPaneRef
	anchor, _ := store.registry.WindowAnchor(windowUID)
	if firstPane == processPane || store.registry.IsVirtualWindow(windowUID) {
		t.Fatal("first materialization did not select the tmux anchor")
	}
	// Preserve the Project session while its last Pane in this Window exits.
	if out, err := server.tmux("new-window", "-d", "-t", window.Status.RuntimeSessionID, "-n", "keeper", "tail", "-f", "/dev/null"); err != nil {
		t.Fatalf("keeper: %v %s", err, out)
	}
	if out, err := server.tmux("send-keys", "-t", anchor.Status.Activation.RuntimeID, "exit", "Enter"); err != nil {
		t.Fatalf("shell exit: %v %s", err, out)
	}
	for {
		out, err := server.tmux("list-windows", "-a", "-F", "#{window_id}")
		if err == nil && !slices.Contains(strings.Fields(out), firstWindow) {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("shell exit did not remove its Window")
		case <-time.After(10 * time.Millisecond):
		}
	}
	reconcile := &resourceReconcileCommand{runner: runner, resources: store.store(), lookupEnv: func(string) string { return "" }}
	if err := reconcile.Run([]string{"resources", "--socket-path", server.socket}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	window, _ = store.registry.Window(windowUID)
	if !store.registry.IsVirtualWindow(windowUID) || window.Spec.AnchorPaneRef != processPane || window.Status.RuntimeID != "" || window.Status.RuntimeSessionID != "" || window.Spec.DefaultShellPaneRef != "" {
		t.Fatalf("last tmux exit did not restore virtual Window: %+v", window)
	}
	if _, present := store.registry.Pane(firstPane); present {
		t.Fatal("exited shell metadata remains")
	}
	focus := newFocusCommand()
	focus.runner, focus.lookupEnv, focus.loadRegistry = runner, func(string) string { return "" }, store.store().load
	focus.materializeVirtualWindow = func(_ context.Context, uid, socket string) error {
		if uid != windowUID || socket != server.socket {
			t.Fatal("focus changed the Window or physical socket")
		}
		return create.materializeVirtualShell(uid)
	}
	coordinate, socket, err := focus.resolveUIDNavigation(ctx, focusOptions{NavKind: "window", NavRef: "uid:" + windowUID})
	if err != nil {
		t.Fatal(err)
	}
	window, _ = store.registry.Window(windowUID)
	panes, err := server.tmux("list-panes", "-t", window.Status.RuntimeID, "-F", "#{pane_id}")
	if err != nil || len(strings.Fields(panes)) != 1 || window.Status.RuntimeID == firstWindow || window.Spec.AnchorPaneRef == processPane || window.Spec.DefaultShellPaneRef != window.Spec.AnchorPaneRef || store.registry.IsVirtualWindow(windowUID) || virtualTestPaneCount(store.registry, windowUID) != 2 || socket != server.socket || coordinate != "alpha:"+window.Status.RuntimeID {
		t.Fatalf("second materialization: coordinate=%s socket=%s Window=%+v Panes=%q err=%v", coordinate, socket, window, panes, err)
	}
	if _, present := store.registry.Pane(processPane); !present {
		t.Fatal("round trip removed the process Pane")
	}
	if err := store.registry.Validate(); err != nil {
		t.Fatal(err)
	}
}
