package app

import (
	"bytes"
	"context"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	"github.com/crevissepartners/projmux/internal/testutil/liveguard"
)

func addVirtualLifecycleProcess(t *testing.T, store *fakeResourceStore) (string, string) {
	t.Helper()
	mut := store.mutator()
	a, err := mut.CreateAgent(&store.registry, "win-name-handoff", coremetadata.CreateAgentOptions{Name: "headless", Provider: "codex", Workspace: coremetadata.AgentWorkspace{CWD: store.registry.Projects[0].Spec.Root}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := mut.AttachAgentPane(&store.registry, a.Metadata.UID, coremetadata.BootstrapPane{}, "op-headless")
	if err != nil {
		t.Fatal(err)
	}
	if err := mut.ReserveProcessBinding(&store.registry, coremetadata.ProcessBinding{HostInstanceID: "host", ProjectUID: "prj-name-handoff", WindowUID: "win-name-handoff", AgentUID: a.Metadata.UID, PaneUID: p.Metadata.UID, Generation: "gen", OperationID: "op-headless"}); err != nil {
		t.Fatal(err)
	}
	return a.Metadata.UID, p.Metadata.UID
}

type virtualDeleteForbiddenTmuxRunner struct{ calls int }

func (r *virtualDeleteForbiddenTmuxRunner) Run(context.Context, string, ...string) ([]byte, error) {
	r.calls++
	return nil, io.ErrUnexpectedEOF
}

func TestVirtualWindowDeletePlanAndNoTmux(t *testing.T) {
	for _, kind := range []string{"agent", "pane", "window"} {
		t.Run(kind, func(t *testing.T) {
			store := realTmuxNameHandoffRegistry(t, t.TempDir(), "virtual", false)
			agent, pane := addVirtualLifecycleProcess(t, store)
			if err := store.mutator().DeletePane(&store.registry, "pan-name-handoff"); err != nil {
				t.Fatal(err)
			}
			c := newTestDeleteCommand(store, false, false, nil)
			c.windows, c.panes = nil, nil // any accidental tmux route fails
			runner := &virtualDeleteForbiddenTmuxRunner{}
			c.actorRunner = runner
			c.lookupEnv = func(string) string { return "" }
			c.processDeleter = &processAgentDeleter{alive: func(coremetadata.ProcessIdentity) bool { return false }}
			uid := agent
			if kind == "pane" {
				uid = pane
			}
			if kind == "window" {
				uid = "win-name-handoff"
			}
			before := store.registry.Clone()
			var out bytes.Buffer
			if err := c.Run([]string{kind, "uid:" + uid, "--dry-run"}, &out, io.Discard); err != nil {
				t.Fatal(err)
			}
			if kind != "window" && !strings.Contains(out.String(), "cascade window/main uid=win-name-handoff") {
				t.Fatalf("missing Window cascade: %s", &out)
			}
			if len(store.registry.Windows) != len(before.Windows) {
				t.Fatal("preview mutated")
			}
			if err := c.Run([]string{kind, "uid:" + uid, "--yes"}, io.Discard, io.Discard); err != nil {
				t.Fatal(err)
			}
			if len(store.registry.Windows) != 0 || len(store.registry.Panes) != 0 || len(store.registry.Agents) != 0 || store.registry.Projects[0].Spec.PrimaryWindowRef != "" {
				t.Fatal("virtual cascade left descendants")
			}
			if err := store.registry.Validate(); err != nil {
				t.Fatal(err)
			}
			if runner.calls != 0 {
				t.Fatalf("virtual deletion invoked tmux %d times", runner.calls)
			}
		})
	}
}

func TestVirtualWindowDoctorAndReconcileReports(t *testing.T) {
	store := realTmuxNameHandoffRegistry(t, t.TempDir(), "virtual", false)
	_, pane := addVirtualLifecycleProcess(t, store)
	if err := store.mutator().DeletePane(&store.registry, "pan-name-handoff"); err != nil {
		t.Fatal(err)
	}
	doctor := &doctorCommand{readRegistry: func() (coremetadata.Registry, error) { return store.registry.Clone(), nil }}
	for _, finding := range doctor.evaluateRegistryInvariants() {
		if strings.Contains(finding.Code, "skipped.window") || strings.Contains(finding.Code, "fatal.window") {
			t.Fatal(finding)
		}
	}
	for _, item := range resourcegraph.ClassifyDivergences(store.registry, resourcegraph.Inventory{}) {
		if item.Key == "window:win-name-handoff" {
			t.Fatal(item)
		}
	}
	plan, err := planRegistryTopology(context.Background(), doctorOfflineTmuxRunner{}, store.registry, "uid:prj-name-handoff", newRegistryReconciler(doctorOfflineTmuxRunner{}, inttmux.NewClient(doctorOfflineTmuxRunner{})), nil, tmuxTransport{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range plan.items {
		if strings.EqualFold(item.Kind, "window") && item.Outcome != "skipped" {
			t.Fatal(item)
		}
	}
	if !store.registry.IsVirtualWindow("win-name-handoff") || store.registry.Windows[0].Spec.AnchorPaneRef != pane {
		t.Fatal("lost virtual state")
	}
}

func TestVirtualWindowLastTmuxPaneRealLifecycle(t *testing.T) {
	liveguard.RequireActive(t)
	for _, action := range []string{"delete-pane", "delete-agent", "delete-shell-priority", "external-exit"} {
		t.Run(action, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			server := startRealTmuxNameHandoffServer(t, ctx)
			run := func(args ...string) string {
				t.Helper()
				out, err := server.tmux(args...)
				if err != nil {
					t.Fatalf("tmux %v: %v %s", args, err, out)
				}
				return out
			}
			run("new-session", "-d", "-s", "virtual-test", "-P", "-F", "#{session_id}", "/bin/sh")
			t.Cleanup(server.killServer)
			server.seed(t)
			// Keep a separate Window so the exact server remains available for observation.
			run("new-window", "-d", "-t", "virtual-test", "-n", "keeper", "sleep 300")
			live := run("display-message", "-p", "-t", "virtual-test:0", "-F", "#{session_id}|#{window_id}|#{pane_id}")
			ids := strings.Split(live, "|")
			store := realTmuxNameHandoffRegistry(t, server.root, "virtual-test", true)
			_, process := addVirtualLifecycleProcess(t, store)
			target, kind := "pan-name-handoff", "pane"
			var remainingAgent, remainingPane string
			if action == "delete-shell-priority" {
				mut := store.mutator()
				a, err := mut.CreateAgent(&store.registry, "win-name-handoff", coremetadata.CreateAgentOptions{Name: "tmux-sibling", Provider: "codex", Workspace: coremetadata.AgentWorkspace{CWD: server.root}})
				if err != nil {
					t.Fatal(err)
				}
				p, err := mut.AttachAgentPane(&store.registry, a.Metadata.UID, coremetadata.BootstrapPane{}, "op-tmux-sibling")
				if err != nil {
					t.Fatal(err)
				}
				remainingAgent, remainingPane = a.Metadata.UID, p.Metadata.UID
				livePane := run("split-window", "-d", "-t", ids[2], "-P", "-F", "#{pane_id}", "sleep 300")
				run("set-option", "-p", "-t", livePane, tmuxopts.PaneUID, p.Metadata.UID)
			}
			if action == "delete-agent" {
				mut := store.mutator()
				a, err := mut.CreateAgent(&store.registry, "win-name-handoff", coremetadata.CreateAgentOptions{Name: "tmux-owner", Provider: "codex", Workspace: coremetadata.AgentWorkspace{CWD: server.root}})
				if err != nil {
					t.Fatal(err)
				}
				p, err := mut.AttachAgentPane(&store.registry, a.Metadata.UID, coremetadata.BootstrapPane{}, "op-tmux")
				if err != nil {
					t.Fatal(err)
				}
				if err := mut.DeletePane(&store.registry, "pan-name-handoff"); err != nil {
					t.Fatal(err)
				}
				target, kind = a.Metadata.UID, "agent"
				run("set-option", "-p", "-t", ids[2], tmuxopts.PaneUID, p.Metadata.UID)
			} else {
				run("set-option", "-p", "-t", ids[2], tmuxopts.PaneUID, target)
			}
			w, _ := store.registry.Window("win-name-handoff")
			w.Status.RuntimeSessionID, w.Status.RuntimeID = ids[0], ids[1]
			run("set-option", "-t", ids[0], tmuxopts.ProjectUIDSession, "prj-name-handoff")
			run("set-option", "-w", "-t", ids[1], tmuxopts.WindowUID, "win-name-handoff")
			if action == "external-exit" {
				run("send-keys", "-t", ids[2], "exit", "Enter")
				for {
					out, err := server.tmux("list-windows", "-a", "-F", "#{window_id}")
					if err == nil && !strings.Contains(out, ids[1]) {
						break
					}
					select {
					case <-ctx.Done():
						t.Fatal("shell exit did not remove Window")
					case <-time.After(10 * time.Millisecond):
					}
				}
				command := &resourceReconcileCommand{runner: inttmux.ExecRunner{}, resources: store.store(), lookupEnv: func(string) string { return "" }}
				if err := command.Run([]string{"resources", "--socket-path", server.socket}, io.Discard, io.Discard); err != nil {
					t.Fatal(err)
				}
			} else {
				c := newTestDeleteCommand(store, false, false, nil)
				c.panes = &tmuxPaneDeleteRuntime{runner: inttmux.ExecRunner{}, getenv: func(string) string { return "" }}
				c.lookupEnv = func(string) string { return "" }
				if err := c.Run([]string{kind, "uid:" + target, "--socket-path", server.socket, "--yes"}, io.Discard, io.Discard); err != nil {
					t.Fatal(err)
				}
				if remainingAgent != "" {
					w, _ := store.registry.Window("win-name-handoff")
					if w.Spec.AnchorPaneRef != remainingPane || store.registry.IsVirtualWindow(w.Metadata.UID) {
						t.Fatalf("tmux sibling lost priority: %+v", w)
					}
					if err := c.Run([]string{"agent", "uid:" + remainingAgent, "--socket-path", server.socket, "--yes"}, io.Discard, io.Discard); err != nil {
						t.Fatal(err)
					}
				}
			}
			w, _ = store.registry.Window("win-name-handoff")
			if !store.registry.IsVirtualWindow(w.Metadata.UID) || w.Spec.AnchorPaneRef != process || w.Status.RuntimeID != "" || w.Status.RuntimeSessionID != "" || w.Spec.DefaultShellPaneRef != "" || len(store.registry.PanesOf(w.Metadata.UID)) != 0 {
				t.Fatalf("wrong return: %+v", w)
			}
			for id := range strings.FieldsSeq(run("list-windows", "-a", "-F", "#{window_id}")) {
				if id == ids[1] {
					t.Fatal("tmux Window survived")
				}
			}
			if err := store.registry.Validate(); err != nil {
				t.Fatal(err)
			}
			t.Logf("action=%s socket=%s retired-window=%s shell=0", action, server.socket, ids[1])
		})
	}
}

func TestVirtualWindowAbsentServerReconcileIsScoped(t *testing.T) {
	store := realTmuxNameHandoffRegistry(t, t.TempDir(), "virtual", true)
	_, process := addVirtualLifecycleProcess(t, store)
	project, _ := store.registry.Project("prj-name-handoff")
	project.Status.Session.SocketPath = "/tmp/virtual-absent/socket"
	window, _ := store.registry.Window("win-name-handoff")
	window.Status.RuntimeID, window.Status.RuntimeSessionID = "@1", "$1"
	plan, count, err := planAbsentServerSessionLower(store.mutator(), store.registry, "/tmp/other-server/socket")
	if err != nil || count != 0 || plan.registry.IsVirtualWindow(window.Metadata.UID) {
		t.Fatalf("foreign socket converted: %d %v", count, err)
	}
	plan, count, err = planAbsentServerSessionLower(store.mutator(), store.registry, "/tmp/virtual-absent/socket")
	if err != nil || count != 1 {
		t.Fatalf("lower: %d %v", count, err)
	}
	w, _ := plan.registry.Window(window.Metadata.UID)
	if !plan.registry.IsVirtualWindow(w.Metadata.UID) || w.Spec.AnchorPaneRef != process || w.Status.RuntimeID != "" || w.Status.RuntimeSessionID != "" || len(plan.registry.PanesOf(w.Metadata.UID)) != 0 {
		t.Fatalf("absent server left tmux topology: %+v", w)
	}
	if err := plan.registry.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestVirtualWindowDeleteRetainedTmuxRowsWithoutTmux(t *testing.T) {
	store := realTmuxNameHandoffRegistry(t, t.TempDir(), "virtual", false)
	addVirtualLifecycleProcess(t, store)
	mut := store.mutator()
	a, err := mut.CreateAgent(&store.registry, "win-name-handoff", coremetadata.CreateAgentOptions{Name: "retained", Provider: "codex", Workspace: coremetadata.AgentWorkspace{CWD: store.registry.Projects[0].Spec.Root}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mut.AttachAgentPane(&store.registry, a.Metadata.UID, coremetadata.BootstrapPane{}, "op-retained"); err != nil {
		t.Fatal(err)
	}
	if err := mut.DeletePane(&store.registry, "pan-name-handoff"); err != nil {
		t.Fatal(err)
	}
	if _, err := mut.TransitionAgent(&store.registry, a.Metadata.UID, coremetadata.PhaseOffline, "exited"); err != nil {
		t.Fatal(err)
	}
	c := newTestDeleteCommand(store, false, false, nil)
	c.windows, c.panes = nil, nil
	c.lookupEnv = func(string) string { return "" }
	c.processDeleter = &processAgentDeleter{alive: func(coremetadata.ProcessIdentity) bool { return false }}
	if err := c.Run([]string{"window", "uid:win-name-handoff", "--yes"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(store.registry.Windows) != 0 || len(store.registry.Panes) != 0 || len(store.registry.Agents) != 0 {
		t.Fatal("retained rows survive")
	}
}

func TestVirtualWindowMissingMirrorsDoNotRetireLivingShell(t *testing.T) {
	liveguard.RequireActive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	server := startRealTmuxNameHandoffServer(t, ctx)
	out, err := server.tmux("new-session", "-d", "-s", "mirror-loss", "-P", "-F", "#{session_id}|#{window_id}", "sleep 300")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.killServer)
	server.seed(t)
	ids := strings.Split(out, "|")
	store := realTmuxNameHandoffRegistry(t, server.root, "mirror-loss", true)
	addVirtualLifecycleProcess(t, store)
	w, _ := store.registry.Window("win-name-handoff")
	w.Status.RuntimeSessionID, w.Status.RuntimeID = ids[0], ids[1]
	project, _ := store.registry.Project("prj-name-handoff")
	project.Status.Session.SocketPath = server.socket
	before := store.registry.Clone()
	runner := explicitTmuxRunner{runner: inttmux.ExecRunner{}, target: tmuxTransport{Kind: tmuxSocketPath, Value: server.socket, Source: tmuxSocketPathSource}}
	inventory := intmetadata.Mirror{Runner: runner}
	if err := returnAbsentProcessWindowProjection(ctx, &store.registry, store.mutator(), inventory, coremetadata.RuntimeObservation{Windows: map[string]bool{}, Panes: map[string]bool{}}, server.socket); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, store.registry) {
		t.Fatal("lost mirrors retired living shell")
	}
	w, _ = store.registry.Window("win-name-handoff")
	w.Status.RuntimeID = "@999999"
	project, _ = store.registry.Project("prj-name-handoff")
	project.Status.Session.SocketPath = "/tmp/foreign/socket"
	before = store.registry.Clone()
	if err := returnAbsentProcessWindowProjection(ctx, &store.registry, store.mutator(), inventory, coremetadata.RuntimeObservation{Windows: map[string]bool{}, Panes: map[string]bool{}}, server.socket); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, store.registry) {
		t.Fatal("foreign socket retired shell")
	}
}

func TestVirtualWindowDeleteWaitsForOwnedProcess(t *testing.T) {
	for _, kind := range []string{"window", "pane"} {
		t.Run(kind, func(t *testing.T) {
			reg, agentUID, paneUID := processDeleteFixture(t, aiModeCodex)
			agent, _ := reg.Agent(agentUID)
			windowUID := agent.Metadata.OwnerUID()
			for _, shell := range reg.PanesOf(windowUID) {
				if err := (coremetadata.Mutator{}).DeletePane(reg, shell.Metadata.UID); err != nil {
					t.Fatal(err)
				}
			}
			if !reg.IsVirtualWindow(windowUID) {
				t.Fatal("fixture is not virtual")
			}
			probe := &processDeleteProbe{host: true, child: true}
			probe.onStop = func() error {
				if _, exists := reg.Window(windowUID); !exists {
					t.Fatal("Window deleted before Wait")
				}
				recordFixtureWait(t, reg, paneUID)
				probe.host, probe.child = false, false
				return nil
			}
			d := probe.deleter(reg)
			c := &deleteCommand{store: d.store, processDeleter: d, confirm: &confirmer{}, resolveKinds: deleteRegistryKinds,
				lookupEnv: func(string) string { return "" }, actorRunner: doctorOfflineTmuxRunner{},
				newOperationID: func() (string, error) { return "op-virtual-delete", nil }}
			uid := windowUID
			if kind == "pane" {
				uid = paneUID
			}
			if err := c.Run([]string{kind, "uid:" + uid, "--dry-run"}, io.Discard, io.Discard); err != nil || probe.stops != 0 {
				t.Fatalf("preview err=%v stops=%d", err, probe.stops)
			}
			if err := c.Run([]string{kind, "uid:" + uid, "--yes"}, io.Discard, io.Discard); err != nil {
				t.Fatal(err)
			}
			if _, exists := reg.Window(windowUID); exists || probe.stops != 1 {
				t.Fatalf("delete exists=%v stops=%d", exists, probe.stops)
			}
		})
	}
}

func TestVirtualWindowBatchPaneDeletePreviewsParentCascade(t *testing.T) {
	store := realTmuxNameHandoffRegistry(t, t.TempDir(), "virtual", false)
	addVirtualLifecycleProcess(t, store)
	a, err := store.mutator().CreateAgent(&store.registry, "win-name-handoff", coremetadata.CreateAgentOptions{Name: "second", Provider: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.mutator().AttachAgentPane(&store.registry, a.Metadata.UID, coremetadata.BootstrapPane{}, "second")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.mutator().ReserveProcessBinding(&store.registry, coremetadata.ProcessBinding{HostInstanceID: "second", ProjectUID: "prj-name-handoff", WindowUID: "win-name-handoff", AgentUID: a.Metadata.UID, PaneUID: p.Metadata.UID, Generation: "second", OperationID: "second"}); err != nil {
		t.Fatal(err)
	}
	if err := store.mutator().DeletePane(&store.registry, "pan-name-handoff"); err != nil {
		t.Fatal(err)
	}
	c := newTestDeleteCommand(store, false, false, nil)
	c.windows, c.panes = nil, nil
	c.actorRunner = doctorOfflineTmuxRunner{}
	c.lookupEnv = func(string) string { return "" }
	c.processDeleter = &processAgentDeleter{alive: func(coremetadata.ProcessIdentity) bool { return false }}
	var out bytes.Buffer
	if err := c.Run([]string{"pane", "--all", "--dry-run"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "cascade window/main uid=win-name-handoff") != 1 {
		t.Fatalf("missing collective cascade: %s", &out)
	}
	if err := c.Run([]string{"pane", "--all", "--yes"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(store.registry.Windows) != 0 || len(store.registry.Agents) != 0 || len(store.registry.Panes) != 0 {
		t.Fatal("batch left virtual resources")
	}
}

func TestVirtualWindowDeleteRejectsProjectionRace(t *testing.T) {
	store := realTmuxNameHandoffRegistry(t, t.TempDir(), "virtual", false)
	addVirtualLifecycleProcess(t, store)
	if err := store.mutator().DeletePane(&store.registry, "pan-name-handoff"); err != nil {
		t.Fatal(err)
	}
	c := newTestDeleteCommand(store, false, false, nil)
	c.windows, c.panes = nil, nil
	c.actorRunner = &virtualDeleteForbiddenTmuxRunner{}
	c.lookupEnv = func(string) string { return "" }
	c.processDeleter = &processAgentDeleter{alive: func(coremetadata.ProcessIdentity) bool { return false }}
	update := c.store.update
	c.store.update = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
		window, _ := store.registry.Window("win-name-handoff")
		window.Status.RuntimeID = "@raced"
		return update(fn)
	}
	err := c.Run([]string{"window", "uid:win-name-handoff", "--yes"}, io.Discard, io.Discard)
	requireProcessDeleteToken(t, err, processDeleteRefusedToken)
	if len(store.registry.Windows) != 1 || len(store.registry.Agents) != 1 || len(store.registry.Panes) != 1 {
		t.Fatal("projection race deleted resources")
	}
}
