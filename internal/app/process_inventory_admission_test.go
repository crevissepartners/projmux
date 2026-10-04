package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/registryview"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/core/selector"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	intmux "github.com/crevissepartners/projmux/internal/integrations/mux"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func processInventoryFixture(t *testing.T) (coremetadata.Registry, *processPaneRuntime, string) {
	t.Helper()
	h := newSessionRefHarness(t, "claude")
	reg := h.registry.Clone()
	pane, _ := reg.Pane(h.paneUID)
	pane.Status.Activation.RuntimeID = ""
	binding := processhost.Binding{Host: "host", Pane: h.paneUID, Generation: pane.Status.Activation.Generation}
	return reg, &processPaneRuntime{targets: []processhost.InventoryTarget{{Binding: binding}}}, h.paneUID
}

func TestProcessInvocationSnapshotKeepsSelectorAndNavigationConsistent(t *testing.T) {
	reg, runtime, paneUID := processInventoryFixture(t)
	pane, _ := reg.Pane(paneUID)
	agent, _ := reg.Agent(pane.Metadata.OwnerUID())
	window, _ := reg.Window(agent.Metadata.OwnerUID())
	project, _ := reg.Project(window.Metadata.OwnerUID())
	project.Status.Conditions = []coremetadata.Condition{{Type: coremetadata.ConditionMissingRoot, Status: coremetadata.ConditionTrue}}
	key := runtime.inventory().Declared[0]
	for _, status := range []resourcegraph.Status{resourcegraph.StatusUnknown, resourcegraph.StatusLive} {
		reader := &runtimeDiagnosticsReader{processes: func(context.Context, coremetadata.Registry) resourcegraph.ProcessInventory {
			return resourcegraph.ProcessInventory{Declared: []resourcegraph.ProcessKey{key}, Observed: []resourcegraph.ProcessObservation{{Key: key, Status: status}}}
		}}
		snapshot := runtimeResourceReadLookup(reader)(reg)
		resolution, err := selector.NewObservedWithContext(reg, snapshot.runtime, snapshot.contexts).ResolvePanes(selector.Query{})
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range resolution.Matches {
			if match.UID == paneUID && string(match.Status) != string(status) {
				t.Fatalf("selector=%s graph=%s", match.Status, status)
			}
		}
		row := snapshot.navigation[paneUID]
		if row.Process == nil || row.Runtime != nil || row.Status != status || row.Allows(registryview.ActionOpen) || row.Allows(registryview.ActionStart) {
			t.Fatalf("navigation gained tmux action: %+v", row)
		}
		agents, err := selector.NewObservedWithContext(reg, snapshot.runtime, snapshot.contexts).ResolveAgents(selector.Query{})
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range agents.Matches {
			if match.UID == agent.Metadata.UID && string(match.Status) != string(status) {
				t.Fatalf("agent selector=%s graph=%s", match.Status, status)
			}
		}
	}
}

func TestProcessAdmissionRefusesBeforeTmuxOrRegistryWrites(t *testing.T) {
	reg, runtime, paneUID := processInventoryFixture(t)
	before, _ := json.Marshal(reg)
	for _, action := range []resourcegraph.ProcessAction{resourcegraph.ProcessAttach, resourcegraph.ProcessFocus, resourcegraph.ProcessKeys, resourcegraph.ProcessCapture, resourcegraph.ProcessPopup, resourcegraph.ProcessRelaunch} {
		_, handled, err := runtime.admit(reg, paneUID, action)
		if !handled || err == nil || !strings.Contains(err.Error(), "process-"+string(action)+"-unsupported") || !strings.Contains(err.Error(), "interrupt, or Stop") {
			t.Fatalf("%s: %v %v", action, handled, err)
		}
	}
	runner := newFakeTmux()
	focus := newFocusCommand()
	focus.processRuntime = runtime
	focus.loadRegistry = func() (coremetadata.Registry, error) { return reg, nil }
	focus.runner = runner
	if err := focus.Run([]string{"pane", "uid:" + paneUID}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "process-focus-unsupported") {
		t.Fatalf("focus: %v", err)
	}
	pane, _ := reg.Pane(paneUID)
	runtime.controlOverride = runtime.control
	command := &agentCommand{processRuntime: runtime, loadRegistry: func() (coremetadata.Registry, error) { return reg, nil }}
	if err := command.runRelaunch([]string{"uid:" + pane.Metadata.OwnerUID(), "--yes"}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "process-relaunch-unsupported") {
		t.Fatalf("relaunch: %v", err)
	}
	after, _ := json.Marshal(reg)
	if len(runner.calls) != 0 || !bytes.Equal(before, after) {
		t.Fatalf("calls=%v Registry changed=%v", runner.calls, !bytes.Equal(before, after))
	}
}

func TestProcessConvergenceDoesNotProjectAbsenceOrMaterialize(t *testing.T) {
	reg, runtime, paneUID := processInventoryFixture(t)
	inventory := runtime.inventory()
	pane, _ := reg.Pane(paneUID)
	before := pane.Clone()
	inputs := lifecycleProjectionTargets(reg, map[string]bool{}, nil, lifecycleDirtyEvent{processes: inventory})
	for _, input := range inputs {
		if input.PaneUID == paneUID {
			t.Fatal("process entered tmux absence projection")
		}
	}
	mutator := coremetadata.Mutator{Now: func() time.Time { return time.Unix(1, 0) }}
	mutator.ObserveRuntimeBindings(&reg, coremetadata.RuntimeObservation{ProcessPanes: processPaneUIDs(reg, inventory)})
	pane, _ = reg.Pane(paneUID)
	if !reflect.DeepEqual(before, *pane) {
		t.Fatal("process MissingRuntime was mutated")
	}
	agent, _ := reg.Agent(pane.Metadata.OwnerUID())
	window, _ := reg.Window(agent.Metadata.OwnerUID())
	project, _ := reg.Project(window.Metadata.OwnerUID())
	plan := &registryTopologyPlan{processes: inventory}
	agents := planTopologyWindowAgents(plan, reg, *project, *window, 1, nil, nil, paneUID)
	if len(agents) != 0 || len(plan.items) != 0 {
		t.Fatalf("process materialized: %+v %+v", agents, plan.items)
	}
	window.Status.RuntimeID = "@7"
	if !processInWindow(reg, inventory, window.Metadata.UID) {
		t.Fatal("tmux teardown gained authority over process child")
	}
	// Pruning is also unable to discard an unavailable process child.
	liveness := observePruneAgentPanes(reg, func(context.Context) resourcegraph.Inventory { return resourcegraph.Inventory{Processes: inventory} })
	if !liveness.live[paneUID] {
		t.Fatal("prune lost unavailable process identity")
	}
}

func TestProcessSupportedControlUsesExactOwnedHost(t *testing.T) {
	f := newProcessClaudeFixture(t, nil)
	f.turn(t, "bootstrap-inventory", "ready")
	f.wait(t, func(s processhost.Snapshot) bool { return s.State == "ready" && s.Turn == "" })
	reg, err := f.store.LoadDegradedReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	runtime := &processPaneRuntime{targets: []processhost.InventoryTarget{{Binding: f.binding, Handle: f.handle}}}
	if err := runtime.control(context.Background(), reg, f.binding.Pane, resourcegraph.ProcessTurn, "typed-turn", "interrupt"); err != nil {
		t.Fatal(err)
	}
	f.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "typed-turn" })
	agent, _ := reg.Agent(f.binding.Agent)
	agent.Status.Progress.TurnRef = "typed-turn"
	runtime.controlOverride = runtime.control
	command := &agentCommand{processRuntime: runtime}
	if err := command.interruptClaudeTurn(reg, *agent, "fixture", io.Discard); err != nil {
		t.Fatal(err)
	}
	f.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" })
	changed := reg.Clone()
	p, _ := changed.Pane(f.binding.Pane)
	p.Status.Activation.Generation = "replacement"
	if err := runtime.control(context.Background(), changed, f.binding.Pane, resourcegraph.ProcessStop, "", ""); err == nil {
		t.Fatal("old generation controlled replacement")
	}
	if err := runtime.control(context.Background(), reg, f.binding.Pane, resourcegraph.ProcessStop, "", ""); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	snapshot, err := f.handle.Wait(ctx, f.binding)
	if err != nil || snapshot.Exit == nil {
		t.Fatalf("actual Wait: %+v %v", snapshot, err)
	}
	if got := runtime.inventory().Observed[0].Status; got != resourcegraph.StatusOffline {
		t.Fatalf("Wait projection: %s", got)
	}
}

func TestProcessConvergencePlanExcludesTmuxBindingAndBootstrap(t *testing.T) {
	_, store, server, runner, root := newReconcileFixture(t, "-L", "primary")
	project, _ := store.registry.ProjectByRoot(root)
	window := store.registry.WindowsOf(project.Metadata.UID)[0]
	pane := store.registry.PanesOf(window.Metadata.UID)[0]
	stored, _ := store.registry.Pane(pane.Metadata.UID)
	stored.Status.Activation.Generation = "process-generation"
	process := resourcegraph.ProcessInventory{Declared: []resourcegraph.ProcessKey{{Host: "unavailable-host", Pane: pane.Metadata.UID, Generation: "process-generation"}}}
	reconciler := &registryReconciler{processes: process}
	matcher := coremetadata.NewBindingMatcher(coremetadata.RuntimeObservation{})
	uid, _, ok := reconciler.paneBindingFor(&store.registry, store.mutator(), "bind", window.Metadata.UID, coremetadata.LegacyPane{UID: pane.Metadata.UID}, matcher)
	if ok || uid != "" {
		t.Fatalf("process binding=%s %v", uid, ok)
	}
	target := tmuxTransport{Kind: tmuxSocketName, Value: "primary", Source: tmuxSocketNameSource}
	planner := resourceReconcilePlanner{reader: explicitTmuxRunner{runner: runner, target: target}, store: store.store(), newReconciler: reconcileFixtureReconciler(root, "alpha"), materializeProject: "uid:" + project.Metadata.UID, exactTarget: target}
	kernel := newResourceControllerKernel(runner, store.store(), planner, target)
	kernel.observe = func(ctx context.Context) resourcegraph.Inventory {
		inventory := intmetadata.NewInventoryObserver(runner, target.ExplicitProjection()).Observe(ctx)
		inventory.Processes = process
		return inventory
	}
	before, _ := json.Marshal(store.registry)
	pass, err := kernel.plan(context.Background(), store.registry)
	if err != nil {
		t.Fatal(err)
	}
	if pass.registry.materialization == nil || len(pass.registry.materialization.windows) != 0 {
		t.Fatalf("process bootstrap planned: %+v", pass.registry.materialization)
	}
	for _, action := range pass.plan.Actions {
		if action.Allowed() && strings.Contains(action.Key, pane.Metadata.UID) {
			t.Fatalf("process write: %+v", action)
		}
	}
	after, _ := json.Marshal(store.registry)
	if lifecycleVerbCount(server) != 0 || !bytes.Equal(before, after) {
		t.Fatalf("lifecycle calls=%v Registry changed=%v", server.calls, !bytes.Equal(before, after))
	}
}

func TestProcessConvergenceKeepsTmuxExactExitReceipts(t *testing.T) {
	for _, tc := range []struct{ sameWindow, joinAfterExit bool }{{false, false}, {true, false}, {true, true}} {
		t.Run(fmt.Sprint("same-window-", tc.sameWindow, "-late-", tc.joinAfterExit), func(t *testing.T) {
			store := newFakeResourceStore(t)
			activateExactPane(t, store, "pan-alpha-review", "", "tmux-generation", "%9")
			pane, _ := store.registry.Pane("pan-alpha-review")
			process := pane.Clone()
			process.Metadata.UID, process.Metadata.Name = "pan-process", "process"
			if !tc.sameWindow {
				process.Metadata.OwnerRef = &coremetadata.OwnerRef{Kind: coremetadata.KindWindow, UID: "win-alpha-main"}
			}
			process.Status.Activation.Generation, process.Status.Activation.RuntimeID = "process-generation", ""
			process.Status.Teardown, process.Status.LastTermination = nil, nil
			join := func() {
				store.registry.Panes = append(store.registry.Panes, process)
				store.registry.NameReservations = append(store.registry.NameReservations, coremetadata.NameReservation{Scope: "prj-alpha", Kind: coremetadata.KindPane, Name: "process", UID: "pan-process"})
			}
			if !tc.joinAfterExit {
				join()
			}
			inventory := resourcegraph.ProcessInventory{Declared: []resourcegraph.ProcessKey{{Host: "unavailable", Pane: "pan-process", Generation: "process-generation"}}}
			before := process.Clone()
			event := exactPaneExitDirty(phase2NormalReceipt("pan-alpha-review", "", "tmux-generation"))
			event.processes = inventory
			observations := &exactPaneExitInventory{uids: map[string]bool{"pan-alpha-review": true}, dead: map[string]bool{"pan-alpha-review": true}, windows: map[string]bool{"win-alpha-review": true}, windowSessions: map[string]int{"$1": 1}}
			result, err := reconcileLifecycle(context.Background(), event, observations, store.store())
			if err != nil {
				t.Fatal(err)
			}
			if tc.sameWindow && !tc.joinAfterExit {
				if len(result.cascaded) != 1 || !result.cascaded[0].Changed {
					t.Fatalf("tmux sibling lost exact cascade: %+v", result)
				}
			} else {
				if len(result.pending) != 1 || !result.pending[0].Changed {
					t.Fatalf("tmux last Pane lost exact receipt: %+v", result)
				}
				unlinked := event
				if tc.joinAfterExit {
					join()
				}
				unlinked.teardownKind, unlinked.runtimePaneID = coremetadata.TeardownEventWindowUnlinked, ""
				unlinked.runtimeSessionID, unlinked.runtimeWindowID = "$1", "@4"
				closed, err := reconcileLifecycle(context.Background(), unlinked, observations, store.store())
				converged := len(closed.rootCascaded) == 1 && closed.rootCascaded[0].Changed
				if tc.joinAfterExit {
					converged = len(closed.cascaded) == 1 && closed.cascaded[0].Changed
				}
				if err != nil || !converged {
					t.Fatalf("tmux sibling Window lost exact unlink: %+v %v", closed, err)
				}
			}
			after, ok := store.registry.Pane("pan-process")
			if !ok || !reflect.DeepEqual(before, *after) {
				t.Fatalf("process identity changed: %+v", after)
			}
			if _, ok := store.registry.Pane("pan-alpha-review"); ok {
				t.Fatal("exact tmux Pane exit did not converge")
			}
		})
	}
}

func TestProcessAdmissionTerminalConsumersRefuseBeforeSideEffects(t *testing.T) {
	reg, runtime, paneUID := processInventoryFixture(t)
	target := &processTerminalTarget{runtime: runtime, registry: reg, paneUID: paneUID}
	before, _ := json.Marshal(reg)
	runner := newFakeTmux()
	writes := 0
	assertRefusal := func(action resourcegraph.ProcessAction, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "process-"+string(action)+"-unsupported:") {
			t.Fatalf("%s: %v", action, err)
		}
	}
	attach := &attachCommand{processTarget: target}
	assertRefusal(resourcegraph.ProcessAttach, attach.Run([]string{"project", "uid:unused"}, io.Discard, io.Discard))
	_, err := exactClaudePane(context.Background(), runner, paneUID, "", target)
	assertRefusal(resourcegraph.ProcessKeys, err)
	ai := (&aiCommand{runCommand: func(ctx context.Context, name string, args ...string) error {
		writes++
		_, err := runner.Run(ctx, name, args...)
		return err
	}, readCommand: runner.Run}).withProcessTarget(target)
	_, err = ai.muxRunner().CapturePane(context.Background(), intmux.CapturePaneOptions{Target: paneUID})
	assertRefusal(resourcegraph.ProcessCapture, err)
	assertRefusal(resourcegraph.ProcessPopup, ai.run("tmux", "display-popup", "-E", "fixture"))
	popup := tmuxClaudeQuestionPopup{processTarget: target, runner: runner, executable: func() (string, error) { writes++; return "fixture", nil }}
	_, err = popup.ViewingClient(context.Background(), paneUID)
	assertRefusal(resourcegraph.ProcessPopup, err)
	assertRefusal(resourcegraph.ProcessPopup, popup.Open(context.Background(), claudeQuestionPopupTarget{PaneID: paneUID}))
	backend := aiCommandMuxBackend{processTarget: target, runCommand: ai.runCommand, readCommand: ai.readCommand}
	_, err = backend.Run(context.Background(), "tmux", "-L", "isolated", "send-keys", "-t", paneUID, "Escape")
	assertRefusal(resourcegraph.ProcessKeys, err)
	pane, _ := reg.Pane(paneUID)
	agent, _ := reg.Agent(pane.Metadata.OwnerUID())
	runtime.controlOverride = runtime.control
	command := &agentCommand{processRuntime: runtime, controlPaths: func() (config.Paths, error) { writes++; return config.Paths{}, nil }}
	if err := command.interruptClaudeTurn(reg, *agent, "fixture", io.Discard); err == nil || !strings.Contains(err.Error(), "process-host-unavailable") {
		t.Fatalf("process interrupt fell back to terminal: %v", err)
	}
	after, _ := json.Marshal(reg)
	if len(runner.calls) != 0 || writes != 0 || !bytes.Equal(before, after) {
		t.Fatalf("calls=%v writes=%d Registry changed=%v", runner.calls, writes, !bytes.Equal(before, after))
	}
}

func TestProcessRuntimeProductionConstructorHasNoControlOverride(t *testing.T) {
	runtime := newProcessPaneRuntime()
	if runtime.observe == nil || runtime.controlOverride != nil {
		t.Fatal("production runtime must observe exact hosts without local control override")
	}
}
