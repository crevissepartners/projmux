package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

type exactManagedStopRunner struct {
	physical   string
	logical    string
	rootUID    string
	killed     bool
	applyErr   error
	applyKills bool
	listReads  int
	onListRead func(int)
	calls      []recordedTmuxCall
}

func (r *exactManagedStopRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, recordedTmuxCall{name: name, args: slices.Clone(args)})
	if name != "tmux" || len(args) < 3 || args[0] != "-S" || args[1] != r.physical {
		return nil, fmt.Errorf("managed stop escaped printed physical route: %s %v", name, args)
	}
	if r.killed {
		return nil, appTypedCommandFailure{inttmux.CommandFailure{
			Kind: inttmux.CommandFailureExit, Stderr: "no server running on " + r.physical,
		}}
	}
	switch args[2] {
	case "display-message":
		if args[len(args)-1] == "#{pid}" {
			return []byte("4242\n"), nil
		}
		return []byte(r.physical + "\n"), nil
	case "show-options":
		switch args[len(args)-1] {
		case tmuxopts.AppGlobal:
			return []byte("1\n"), nil
		case runtimeMutationSocketNameOption:
			return []byte(r.logical + "\n"), nil
		}
	case "list-sessions":
		r.listReads++
		if r.onListRead != nil {
			r.onListRead(r.listReads)
		}
		rootUID := r.rootUID
		if rootUID == "" {
			rootUID = "uid:project"
		}
		return []byte(tmuxRowFormat("$1", "alpha", rootUID, "") + "\n"), nil
	case "kill-session":
		if flagValue(args[3:], "-t") != "$1" {
			return nil, fmt.Errorf("managed stop targeted %v", args)
		}
		if r.applyKills || r.applyErr == nil {
			r.killed = true
		}
		return nil, r.applyErr
	}
	return nil, fmt.Errorf("unexpected managed stop command: %v", args)
}

func activateRuntimeStopAgent(t *testing.T, store *fakeResourceStore, generation string) {
	t.Helper()
	if _, err := store.mutator().RecordPaneActivation(&store.registry, "pan-alpha-codex", coremetadata.PaneActivationOptions{
		Generation: generation, RuntimeID: "%3", AgentUID: "agt-alpha-codex", OperationID: "op-create",
	}); err != nil {
		t.Fatal(err)
	}
}

func addSecondRuntimeStopAgent(t *testing.T, store *fakeResourceStore) {
	t.Helper()
	baseAgent, _ := store.registry.Agent("agt-alpha-codex")
	basePane, _ := store.registry.Pane("pan-alpha-codex")
	secondAgent := baseAgent.Clone()
	secondAgent.Metadata.UID = "agt-alpha-review"
	secondAgent.Metadata.Name = "reviewer"
	secondAgent.Status.PaneRef = "pan-alpha-reviewer"
	secondAgent.Status.LastTermination = nil
	secondPane := basePane.Clone()
	secondPane.Metadata.UID = "pan-alpha-reviewer"
	secondPane.Metadata.Name = "reviewer-pane"
	secondPane.Metadata.OwnerRef = &coremetadata.OwnerRef{Kind: coremetadata.KindAgent, UID: secondAgent.Metadata.UID}
	secondPane.Status.Activation = coremetadata.PaneActivation{
		Generation: "gen-alpha-2", RuntimeID: "%4", AgentUID: secondAgent.Metadata.UID, OperationID: "op-create-2",
	}
	secondPane.Status.LastTermination = nil
	store.registry.Agents = append(store.registry.Agents, secondAgent)
	store.registry.Panes = append(store.registry.Panes, secondPane)
	store.registry.NameReservations = append(store.registry.NameReservations,
		coremetadata.NameReservation{Scope: "prj-alpha", Kind: coremetadata.KindAgent, Name: "reviewer", UID: secondAgent.Metadata.UID},
		coremetadata.NameReservation{Scope: "prj-alpha", Kind: coremetadata.KindPane, Name: "reviewer-pane", UID: secondPane.Metadata.UID})
	if err := store.registry.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestManagedRuntimeStopUsesOnePrintedPhysicalObservationAndRegistryAuthority(t *testing.T) {
	store := freshStartFixtureStore(t)
	activateRuntimeStopAgent(t, store, "gen-alpha")
	runner := &exactManagedStopRunner{physical: "/tmp/projmux-stop", logical: defaultAppSocket, rootUID: "prj-alpha"}
	target := managedRuntimeStopTarget{
		SessionID: "$1", SessionName: "alpha", RootKind: coremetadata.KindProject, RootUID: "prj-alpha",
		Route: runtimeMutationRoute{
			target: tmuxTransport{Kind: tmuxSocketName, Value: defaultAppSocket, Source: tmuxSocketNameSource}, socketName: defaultAppSocket,
			expectedSocketPath: runner.physical,
			authority:          &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: "4242"},
		},
	}
	authorityReads := 0
	registryAuthority := managedRuntimeStopRegistryAuthority(store.store().snapshot)
	authoritative := func(ctx context.Context, kind coremetadata.Kind, uid, session string) (bool, error) {
		authorityReads++
		return registryAuthority(ctx, kind, uid, session)
	}
	stopStore := store.store()
	if err := executeManagedRuntimeStop(context.Background(), runner, target, authoritative, stopStore); err != nil {
		t.Fatalf("executeManagedRuntimeStop() error = %v", err)
	}
	if !runner.killed || authorityReads != 3 {
		t.Fatalf("managed stop = killed %t, Registry reads %d; want one exact kill after three authority reads", runner.killed, authorityReads)
	}
	pane, _ := store.registry.Pane("pan-alpha-codex")
	agent, _ := store.registry.Agent("agt-alpha-codex")
	if store.writes != 1 || pane.Status.LastTermination == nil || agent.Status.LastTermination == nil ||
		pane.Status.LastTermination.Classification != coremetadata.TerminationInterrupted ||
		pane.Status.LastTermination.Source != coremetadata.TerminationSourceControlAction ||
		pane.Status.LastTermination.Generation != "gen-alpha" ||
		pane.Status.LastTermination.OperationID != agent.Status.LastTermination.OperationID {
		t.Fatalf("managed stop interruption evidence = writes=%d pane=%+v agent=%+v", store.writes,
			pane.Status.LastTermination, agent.Status.LastTermination)
	}
	for _, call := range runner.calls {
		if len(call.args) < 2 || call.args[0] != "-S" || call.args[1] != runner.physical {
			t.Fatalf("managed stop mixed logical/ambient route: %#v", runner.calls)
		}
	}
}

func TestProjectStopInterruptionPrewriteSelectsOnlyExactRunningAgentsAtomically(t *testing.T) {
	t.Parallel()
	store := freshStartFixtureStore(t)
	activateRuntimeStopAgent(t, store, "gen-alpha-1")
	addSecondRuntimeStopAgent(t, store)
	pending, err := store.mutator().CreateAgent(&store.registry, "win-alpha-main", coremetadata.CreateAgentOptions{
		Name: "pending", Provider: "codex", OperationID: "op-pending",
	})
	if err != nil {
		t.Fatal(err)
	}

	panes, err := recordProjectStopInterruptions(store.store(), "prj-alpha", "op-project-stop")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(panes, []projectStopInterruption{
		{PaneUID: "pan-alpha-codex", AgentUID: "agt-alpha-codex", Generation: "gen-alpha-1"},
		{PaneUID: "pan-alpha-reviewer", AgentUID: "agt-alpha-review", Generation: "gen-alpha-2"},
	}) || store.writes != 1 {
		t.Fatalf("prewrite panes=%v writes=%d", panes, store.writes)
	}
	for _, pair := range []struct{ agent, pane, generation string }{
		{"agt-alpha-codex", "pan-alpha-codex", "gen-alpha-1"},
		{"agt-alpha-review", "pan-alpha-reviewer", "gen-alpha-2"},
	} {
		agent, _ := store.registry.Agent(pair.agent)
		pane, _ := store.registry.Pane(pair.pane)
		for _, stored := range []*coremetadata.TerminationEvidence{agent.Status.LastTermination, pane.Status.LastTermination} {
			if stored == nil || stored.Source != coremetadata.TerminationSourceControlAction ||
				stored.Classification != coremetadata.TerminationInterrupted || stored.Generation != pair.generation ||
				stored.OperationID != "op-project-stop" {
				t.Fatalf("receipt for %s/%s = %+v", pair.agent, pair.pane, stored)
			}
		}
	}
	offline, _ := store.registry.Agent("agt-beta-codex")
	if offline.Status.LastTermination != nil {
		t.Fatalf("offline Agent gained interruption evidence: %+v", offline.Status.LastTermination)
	}
	pendingStored, _ := store.registry.Agent(pending.Metadata.UID)
	if pendingStored.Status.Phase != coremetadata.PhasePending || pendingStored.Status.LastTermination != nil {
		t.Fatalf("same-Project non-Running Agent gained interruption evidence: %+v", pendingStored.Status)
	}
}

func TestTwoAgentProjectStopPrewriteFailureCommitsNoPartialRegistryAndKillsNothing(t *testing.T) {
	t.Parallel()
	backing := freshStartFixtureStore(t)
	activateRuntimeStopAgent(t, backing, "gen-alpha-1")
	addSecondRuntimeStopAgent(t, backing)
	before := backing.snapshot()
	failed := &resourceStore{
		load:     func() (coremetadata.Registry, error) { return backing.registry.Clone(), nil },
		snapshot: func() (coremetadata.Registry, error) { return backing.registry.Clone(), nil },
		mutator:  backing.mutator,
		update: func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
			working := backing.registry.Clone()
			if err := fn(&working); err != nil {
				return coremetadata.Registry{}, err
			}
			return coremetadata.Registry{}, errors.New("injected durable prewrite failure")
		},
	}
	runner := &exactManagedStopRunner{physical: "/tmp/projmux-stop", logical: defaultAppSocket, rootUID: "prj-alpha"}
	target := managedRuntimeStopTarget{SessionID: "$1", SessionName: "alpha", RootKind: coremetadata.KindProject, RootUID: "prj-alpha",
		Route: runtimeMutationRoute{target: tmuxTransport{Kind: tmuxSocketName, Value: defaultAppSocket, Source: tmuxSocketNameSource},
			socketName: defaultAppSocket, expectedSocketPath: runner.physical,
			authority: &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: "4242"}}}
	err := executeManagedRuntimeStop(context.Background(), runner, target,
		managedRuntimeStopRegistryAuthority(failed.snapshot), failed)
	if err == nil || !strings.Contains(err.Error(), "zero tmux mutations") || runner.killed {
		t.Fatalf("prewrite failure = killed:%t err:%v", runner.killed, err)
	}
	if backing.snapshot() != before {
		t.Fatal("two-Agent prewrite failure committed a partial Registry mutation")
	}
	for _, call := range runner.calls {
		if len(call.args) > 2 && call.args[2] == "kill-session" {
			t.Fatalf("prewrite failure reached tmux mutation: %#v", runner.calls)
		}
	}
}

func TestProjectStopFailureCompensatesOnlyWhenExactSessionIsProvedLive(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		applyKills bool
		wantStored bool
	}{
		{name: "apply error and live session compensates", wantStored: false},
		{name: "apply error after absence retains", applyKills: true, wantStored: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := freshStartFixtureStore(t)
			activateRuntimeStopAgent(t, store, "gen-compensate")
			runner := &exactManagedStopRunner{
				physical: "/tmp/projmux-stop", logical: defaultAppSocket, rootUID: "prj-alpha",
				applyErr: errors.New("injected kill-session failure"), applyKills: test.applyKills,
			}
			target := managedRuntimeStopTarget{SessionID: "$1", SessionName: "alpha", RootKind: coremetadata.KindProject, RootUID: "prj-alpha",
				Route: runtimeMutationRoute{target: tmuxTransport{Kind: tmuxSocketName, Value: defaultAppSocket, Source: tmuxSocketNameSource},
					socketName: defaultAppSocket, expectedSocketPath: runner.physical,
					authority: &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: "4242"}}}
			stopStore := store.store()
			err := executeManagedRuntimeStop(context.Background(), runner, target,
				managedRuntimeStopRegistryAuthority(stopStore.snapshot), stopStore)
			if err == nil || !strings.Contains(err.Error(), "injected kill-session failure") {
				t.Fatalf("stop error = %v", err)
			}
			pane, _ := store.registry.Pane("pan-alpha-codex")
			agent, _ := store.registry.Agent("agt-alpha-codex")
			stored := pane.Status.LastTermination != nil || agent.Status.LastTermination != nil
			if stored != test.wantStored {
				t.Fatalf("stored=%t pane=%+v agent=%+v, want %t", stored, pane.Status.LastTermination, agent.Status.LastTermination, test.wantStored)
			}
			if test.wantStored && (pane.Status.LastTermination.OperationID == "" ||
				pane.Status.LastTermination.OperationID != agent.Status.LastTermination.OperationID) {
				t.Fatalf("retained receipts do not share operation: pane=%+v agent=%+v", pane.Status.LastTermination, agent.Status.LastTermination)
			}
		})
	}
}

func TestProjectStopGenerationDriftAfterPrewriteRefusesKill(t *testing.T) {
	t.Parallel()
	store := freshStartFixtureStore(t)
	activateRuntimeStopAgent(t, store, "gen-before-stop")
	runner := &exactManagedStopRunner{
		physical: "/tmp/projmux-stop", logical: defaultAppSocket, rootUID: "prj-alpha",
		onListRead: func(read int) {
			if read != 3 {
				return
			}
			if _, err := store.mutator().RecordPaneActivation(&store.registry, "pan-alpha-codex", coremetadata.PaneActivationOptions{
				Generation: "gen-racing-activation", RuntimeID: "%9", AgentUID: "agt-alpha-codex", OperationID: "op-racing-activation",
			}); err != nil {
				t.Fatalf("race activation: %v", err)
			}
		},
	}
	target := managedRuntimeStopTarget{SessionID: "$1", SessionName: "alpha", RootKind: coremetadata.KindProject, RootUID: "prj-alpha",
		Route: runtimeMutationRoute{target: tmuxTransport{Kind: tmuxSocketName, Value: defaultAppSocket, Source: tmuxSocketNameSource},
			socketName: defaultAppSocket, expectedSocketPath: runner.physical,
			authority: &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: "4242"}}}
	stopStore := store.store()
	err := executeManagedRuntimeStop(context.Background(), runner, target,
		managedRuntimeStopRegistryAuthority(stopStore.snapshot), stopStore)
	if err == nil || !strings.Contains(err.Error(), "authority drifted before kill") || runner.killed {
		t.Fatalf("generation drift = killed:%t err:%v", runner.killed, err)
	}
	pane, _ := store.registry.Pane("pan-alpha-codex")
	agent, _ := store.registry.Agent("agt-alpha-codex")
	if pane.Status.Activation.Generation != "gen-racing-activation" || pane.Status.LastTermination != nil || agent.Status.LastTermination != nil {
		t.Fatalf("generation drift compensation touched foreign activation: pane=%+v agent=%+v", pane.Status, agent.Status)
	}
	for _, call := range runner.calls {
		if len(call.args) > 2 && call.args[2] == "kill-session" {
			t.Fatalf("generation drift reached tmux mutation: %#v", runner.calls)
		}
	}
}

func TestManagedRuntimeStopRegistryAuthorityDriftRefusesBeforeWrite(t *testing.T) {
	store := freshStartFixtureStore(t)
	runner := &exactManagedStopRunner{physical: "/tmp/projmux-stop", logical: defaultAppSocket}
	target := managedRuntimeStopTarget{
		SessionID: "$1", SessionName: "alpha", RootKind: coremetadata.KindProject, RootUID: "uid:project",
		Route: runtimeMutationRoute{
			target: tmuxTransport{Kind: tmuxSocketName, Value: defaultAppSocket, Source: tmuxSocketNameSource}, socketName: defaultAppSocket,
			expectedSocketPath: runner.physical,
			authority:          &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: "4242"},
		},
	}
	err := executeManagedRuntimeStop(context.Background(), runner, target,
		func(context.Context, coremetadata.Kind, string, string) (bool, error) { return false, nil }, store.store())
	if err == nil || !strings.Contains(err.Error(), "Registry authority") || runner.killed {
		t.Fatalf("Registry authority drift = killed %t, err %v; want residual refusal and zero kill", runner.killed, err)
	}
}

func TestManagedRuntimeStopAuthorityRejectsZeroWindowNoWriteCell(t *testing.T) {
	t.Parallel()
	store := freshStartFixtureStore(t)
	mutator := store.mutator()
	for _, window := range store.registry.WindowsOf("prj-alpha") {
		if err := mutator.DeleteWindow(&store.registry, window.Metadata.UID); err != nil {
			t.Fatal(err)
		}
	}
	authoritative := managedRuntimeStopRegistryAuthority(store.store().snapshot)
	ok, err := authoritative(context.Background(), coremetadata.KindProject, "prj-alpha", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("zero-Window Project authorized a runtime stop despite the table's no-write cell")
	}
}

func TestProjectStopSurfaceExecutorMatchesLifecycleTableAndGenericCopyStaysGeneric(t *testing.T) {
	t.Parallel()
	decision := coremetadata.DecideProjectLifecycle(coremetadata.ProjectLifecycleRetainedWindows,
		coremetadata.ProjectLifecycleStop, coremetadata.ProjectLifecyclePreconditions{})
	if err := requireProjectLifecyclePlan(decision, coremetadata.ProjectLifecycleOperationStop,
		coremetadata.ProjectUIDPreserved, coremetadata.ProjectDescendantUIDsPreserved,
		coremetadata.ProjectStartupWriteStopRuntime); err != nil {
		t.Fatal(err)
	}
	var projectSurface runtimeMutationSurface
	for _, surface := range runtimeMutationSurfaces {
		if surface.ID == "catalog.project-sidebar.runtime.stop" {
			projectSurface = surface
			break
		}
	}
	if projectSurface.Handler != "executeManagedRuntimeStop" || projectSurface.PlanVerb != string(mutationStopManagedSession) ||
		!strings.Contains(projectSurface.Guard, "Stop table cell") || !strings.Contains(projectSurface.Effect, "Project UID") ||
		!strings.Contains(projectSurface.Effect, "desired Window/Pane topology unchanged") {
		t.Fatalf("Project Stop production surface diverged from lifecycle table: %+v", projectSurface)
	}
	generic, ok := keyBindingActionByID(defaultKeyBindingCatalog(), "SessionPopup:KillSession")
	if !ok {
		t.Fatal("generic Session stop action missing")
	}
	if strings.Contains(generic.Description, "Project UID") || strings.Contains(keyBindingDisplayName(generic), "UID/topology") {
		t.Fatalf("generic Session stop copy incorrectly claims Project identity semantics: label=%q description=%q",
			keyBindingDisplayName(generic), generic.Description)
	}
	if !strings.Contains(generic.Description, "Stop only") || !strings.Contains(generic.Description, "managed Registry identity") ||
		!strings.Contains(generic.Description, "desired topology") {
		t.Fatalf("generic Session stop copy does not state runtime-only managed-identity preservation: label=%q description=%q",
			keyBindingDisplayName(generic), generic.Description)
	}
}
