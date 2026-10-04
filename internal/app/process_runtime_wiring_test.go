package app

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

func TestProcessRuntimeDefaultCreateRejectsBeforeRoute(t *testing.T) {
	store := newFakeResourceStore(t)
	pane, _ := store.registry.Pane("pan-beta-zsh")
	pane.Spec.Runtime.Kind = coremetadata.RuntimeProcess
	pane.Status.Activation = coremetadata.PaneActivation{}
	tmux := newFakeTmux()
	create, _ := newTestResourceCreateCommand(t, store, tmux)
	// Consume the production constructor's seam, without a fixture declaration.
	create.processRuntime = newCreateCommandOn(tmux, func(string) string { return "" }).processRuntime
	before, _ := json.Marshal(store.registry)
	stdout, _, err := runRoute(t, create, "pane", "--project", "beta", "--window", "main", "--pane", "uid:"+pane.Metadata.UID)
	after, _ := json.Marshal(store.registry)
	if err == nil || !strings.Contains(err.Error(), "process-create-pane-unsupported") || stdout != "" || len(tmux.calls) != 0 || store.writes != 0 || string(before) != string(after) {
		t.Fatalf("err=%v calls=%v writes=%d", err, tmux.calls, store.writes)
	}
}

func TestProcessRuntimeDefaultApplicationRefusals(t *testing.T) {
	app := New()
	reg, _, paneUID := processInventoryFixture(t)
	pane, _ := reg.Pane(paneUID)
	pane.Spec.Runtime.Kind = coremetadata.RuntimeProcess
	pane.Status.Activation = coremetadata.PaneActivation{}
	before := reg.Clone()
	runner := newFakeTmux()
	app.focus.loadRegistry = func() (coremetadata.Registry, error) { return reg, nil }
	app.focus.runner = runner
	app.agent.loadRegistry = app.focus.loadRegistry
	if err := app.focus.Run([]string{"pane", "uid:" + paneUID}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "process-focus-unsupported") {
		t.Fatalf("focus: %v", err)
	}
	if err := app.agent.runRelaunch([]string{"uid:" + pane.Metadata.OwnerUID(), "--yes"}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "process-relaunch-unsupported") {
		t.Fatalf("relaunch: %v", err)
	}
	if len(runner.calls) != 0 || !reflect.DeepEqual(reg, before) {
		t.Fatal("refusal used transport or changed Registry")
	}
}

func TestProcessRuntimeOperationalSplitAnchorRefuses(t *testing.T) {
	store := newFakeResourceStore(t)
	pane, _ := store.registry.Pane("pan-beta-zsh")
	pane.Spec.Runtime.Kind = coremetadata.RuntimeProcess
	pane.Status.Activation = coremetadata.PaneActivation{}
	tmux := newFakeTmux()
	create, _ := newTestResourceCreateCommand(t, store, tmux)
	create.processRuntime = newCreateCommandOn(tmux, func(string) string { return "" }).processRuntime
	project, _ := store.registry.Project("prj-beta")
	_, err := create.ensureAnchorPane(context.Background(), &store.registry, store.mutator(), nil, *project, "beta", "operation", paneTarget{windowUID: "win-beta-main", anchorUID: pane.Metadata.UID})
	if err == nil || !strings.Contains(err.Error(), "process-split-unsupported") || len(tmux.calls) != 0 || store.writes != 0 {
		t.Fatalf("err=%v calls=%v writes=%d", err, tmux.calls, store.writes)
	}
}

func TestProcessRuntimeControllerDeclarationsUseSameSnapshot(t *testing.T) {
	_, store, _, runner, root := newReconcileFixture(t, "-L", "primary")
	project, _ := store.registry.ProjectByRoot(root)
	window := store.registry.WindowsOf(project.Metadata.UID)[0]
	pane := store.registry.PanesOf(window.Metadata.UID)[0]
	stored, _ := store.registry.Pane(pane.Metadata.UID)
	stored.Spec.Runtime.Kind = coremetadata.RuntimeProcess
	stored.Status.Activation.RuntimeID = ""
	stored.Status.Activation.Generation = "process-generation"
	process := resourcegraph.ProcessInventory{Declared: []resourcegraph.ProcessKey{{Host: "unknown-host", Pane: pane.Metadata.UID, Generation: "process-generation"}}}
	target := tmuxTransport{Kind: tmuxSocketName, Value: "primary", Source: tmuxSocketNameSource}
	planner := resourceReconcilePlanner{reader: explicitTmuxRunner{runner: runner, target: target}, store: store.store(), newReconciler: reconcileFixtureReconciler(root, "alpha"), materializeProject: "uid:" + project.Metadata.UID, exactTarget: target}
	kernel := newResourceControllerKernel(runner, store.store(), planner, target)
	kernel.observe = func(ctx context.Context) resourcegraph.Inventory {
		return intmetadata.NewInventoryObserver(runner, target.ExplicitProjection()).Observe(ctx)
	}
	pass, err := kernel.plan(context.Background(), store.registry)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	found := false
	for _, node := range pass.graph.Panes {
		if node.Pane.Metadata.UID == pane.Metadata.UID {
			found = node.Process != nil && node.Runtime == nil && node.Status == resourcegraph.StatusUnknown
		}
	}
	if !found {
		t.Fatal("controller graph lost unavailable process declaration")
	}
	kernel.observe = func(ctx context.Context) resourcegraph.Inventory {
		inventory := intmetadata.NewInventoryObserver(runner, target.ExplicitProjection()).Observe(ctx)
		inventory.Processes = process
		return inventory
	}
	if _, err := kernel.plan(context.Background(), store.registry); err != nil {
		t.Fatalf("fixture override replaced: %v", err)
	}
}

func TestProcessRuntimeAdmissionConsumesCurrentBinding(t *testing.T) {
	raw, err := os.ReadFile("../core/metadata/testdata/registry-v5-process.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var reg coremetadata.Registry
	if err := json.Unmarshal(raw, &reg); err != nil {
		t.Fatal(err)
	}
	pane, _ := reg.Pane("pane-02")
	binding := pane.Status.Activation.Process.Binding
	key := resourcegraph.ProcessKey{Host: binding.HostInstanceID, Pane: binding.PaneUID, Generation: binding.Generation}
	calls := 0
	runtime := &processPaneRuntime{observe: func(context.Context, coremetadata.Registry) resourcegraph.ProcessInventory {
		calls++
		return resourcegraph.ProcessInventory{Declared: []resourcegraph.ProcessKey{key}, Observed: []resourcegraph.ProcessObservation{{Key: key, Status: resourcegraph.StatusLive}}}
	}}
	got, handled, err := runtime.admit(reg, binding.PaneUID, resourcegraph.ProcessTurn)
	if err != nil || !handled || got != key || calls != 1 {
		t.Fatalf("current binding: %v %v %+v calls=%d", err, handled, got, calls)
	}
	agent, _ := reg.Agent(binding.AgentUID)
	agent.Status.PaneRef = "replacement"
	if _, handled, err := runtime.admit(reg, binding.PaneUID, resourcegraph.ProcessTurn); !handled || err == nil || !strings.Contains(err.Error(), "process-host-unavailable") || calls != 1 {
		t.Fatalf("stale binding probed or authorized: %v %v calls=%d", err, handled, calls)
	}
	if _, handled, err := runtime.admit(reg, binding.PaneUID, resourcegraph.ProcessFocus); !handled || err == nil || !strings.Contains(err.Error(), "process-focus-unsupported") || calls != 1 {
		t.Fatalf("terminal refusal probed: %v %v calls=%d", err, handled, calls)
	}
}

func TestProcessRuntimeLockedAdmissionRejectsReplacedAnchor(t *testing.T) {
	fixture := newCallBudgetFixture(t, 0)
	update := fixture.create.store.update
	fixture.create.store.update = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
		pane, _ := fixture.store.registry.Pane("pan-target-shell")
		pane.Spec.Runtime.Kind = coremetadata.RuntimeProcess
		pane.Status.Activation = coremetadata.PaneActivation{}
		return update(fn)
	}
	_, _, err := runRoute(t, fixture.create, "pane", "--project", "uid:prj-target", "--window", "main")
	if err == nil || !strings.Contains(err.Error(), "process-create-pane-unsupported") || fixture.store.writes != 0 {
		t.Fatalf("locked replacement: %v writes=%d", err, fixture.store.writes)
	}
	for _, call := range fixture.tmux.calls {
		if len(call) > 0 && (call[0] == "split-window" || call[0] == "new-window" || call[0] == "new-session" || call[0] == "set-option" || call[0] == "set-environment") {
			t.Fatalf("replaced process anchor mutated tmux: %v", call)
		}
	}
}

func TestProcessRuntimeReconcileLockNeverDialsHost(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "pl-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	reg, _, uid := processInventoryFixture(t)
	pane, _ := reg.Pane(uid)
	pane.Spec.Runtime.Kind = coremetadata.RuntimeProcess
	identity, _, err := localipc.Process(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	pane.Status.Activation.Process = &coremetadata.ProcessActivation{Binding: coremetadata.ProcessBinding{HostInstanceID: "host", PaneUID: uid, AgentUID: pane.Metadata.OwnerUID(), Generation: pane.Status.Activation.Generation}, HostProcess: identity, Child: identity}
	socket := processClaudeHostSocket(intmetadata.PathFor(paths.StateDir), uid, pane.Status.Activation.Generation)
	if err := os.MkdirAll(filepath.Dir(socket), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(socket, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := localipc.InspectOwnedSocket(socket); err != nil {
		t.Fatal(err)
	}
	var dials atomic.Int32
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			dials.Add(1)
			go func() { defer conn.Close(); <-done }()
		}
	}()
	runner := newFakeTmux()
	reconciler := newRegistryReconciler(runner, inttmux.NewClient(runner))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	// observeRuntime executes in the Registry transaction's locked callback.
	start := time.Now()
	abort := errors.New("observation-only lock probe")
	store := intmetadata.NewStore(intmetadata.PathFor(paths.StateDir))
	_, err = store.Update(func(*coremetadata.Registry) error {
		reconciler.observeRuntime(ctx, &reg, coremetadata.Mutator{}, nil)
		return abort
	})
	if !errors.Is(err, abort) {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	t.Logf("locked reconciliation elapsed=%s host dials=%d", elapsed, dials.Load())
	if dials.Load() != 0 {
		t.Fatal("reconciliation dialed an unresponsive host inside the Registry lock")
	}
}
