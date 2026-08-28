package app

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

type driftingPreexistingInventory struct {
	*stableDeadPaneInventory
	deadCalls int
}

type convergingPreexistingInventory struct{ *stableDeadPaneInventory }

type controlledPreexistingInventory struct {
	*stableDeadPaneInventory
	hostCount     int
	hostErr       error
	deadErr       error
	deadCalls     int
	disappearCall int
}

func (i *controlledPreexistingInventory) LivePaneCount(context.Context) (int, error) {
	if i.hostErr != nil {
		return 0, i.hostErr
	}
	return i.hostCount, nil
}

func (i *controlledPreexistingInventory) DeadPaneObservations(context.Context) ([]intmetadata.DeadPaneObservation, error) {
	i.deadCalls++
	if i.deadErr != nil {
		return nil, i.deadErr
	}
	if i.disappearCall > 0 && i.deadCalls >= i.disappearCall {
		return nil, nil
	}
	return slices.Clone(i.observed), nil
}

func (i *convergingPreexistingInventory) DeadPaneObservations(context.Context) ([]intmetadata.DeadPaneObservation, error) {
	rows, err := i.stableDeadPaneInventory.DeadPaneObservations(context.Background())
	if err != nil {
		return nil, err
	}
	out := rows[:0]
	for _, row := range rows {
		if i.dead[row.PaneUID] {
			out = append(out, row)
		}
	}
	return out, nil
}

func (i *driftingPreexistingInventory) DeadPaneObservations(context.Context) ([]intmetadata.DeadPaneObservation, error) {
	i.deadCalls++
	rows, err := i.stableDeadPaneInventory.DeadPaneObservations(context.Background())
	if err == nil && i.deadCalls > 1 && len(rows) != 0 {
		rows[0].PaneID = "%20"
	}
	return rows, err
}

const (
	preexistingPaneUID  = "pan-alpha-codex"
	preexistingAgentUID = "agt-alpha-codex"
	preexistingGen      = "gen-preexisting"
	preexistingOp       = "op-preexisting"
	preexistingRuntime  = "%19"
	preexistingPID      = 648421
	secondAgentUID      = "agt-zeta-codex"
	secondPaneUID       = "pan-zeta-codex"
	secondGeneration    = "gen-zeta"
	secondOperation     = "op-zeta"
	secondRuntime       = "%27"
	secondSupervisorPID = 648422
)

func preexistingRecoveryFixture(t *testing.T) (*fakeResourceStore, *stableDeadPaneInventory, lifecycleDirtyEvent) {
	t.Helper()
	store := newFakeResourceStore(t)
	activatePaneFixtureForOperation(t, store, preexistingPaneUID, preexistingAgentUID,
		preexistingGen, preexistingOp, preexistingRuntime)
	window, _ := store.registry.Window("win-alpha-main")
	window.Status.RuntimeSessionID, window.Status.RuntimeID = "$1", "@4"
	base := &exactPaneExitInventory{
		uids: exitReconcileFixtureLive(), dead: map[string]bool{preexistingPaneUID: true},
		windowUID: "win-alpha-main", hostPanes: 5,
	}
	inventory := &stableDeadPaneInventory{exactPaneExitInventory: base, observed: []intmetadata.DeadPaneObservation{{
		SessionID: "$1", SessionName: "alpha", WindowID: "@4", PaneID: preexistingRuntime,
		PanePID: "648421", ProjectUID: "prj-alpha", WindowUID: "win-alpha-main", PaneUID: preexistingPaneUID,
		OwnerKind: string(coremetadata.KindAgent), OwnerUID: preexistingAgentUID,
		AgentUID: preexistingAgentUID, PaneRole: string(coremetadata.PaneRoleAgent),
	}}}
	event := lifecycleDirtyEvent{
		target:  tmuxTransport{Kind: tmuxSocketPath, Value: "/tmp/preexisting.sock", Source: tmuxSocketPathSource},
		paneUID: preexistingPaneUID, runtimePaneID: preexistingRuntime,
		generation: preexistingGen, operationID: preexistingOp,
		teardownKind:        coremetadata.TeardownEventPaneExited,
		preexistingRecovery: true, supervisorPID: preexistingPID,
		processAlive: func(int) bool { return false },
	}
	store.writes, store.transactions = 0, 0
	return store, inventory, event
}

func addSecondPreexistingRecoveryCandidate(store *fakeResourceStore, stable *stableDeadPaneInventory) {
	agent, _ := store.registry.Agent(preexistingAgentUID)
	agentClone := agent.Clone()
	agentClone.Metadata.UID, agentClone.Metadata.Name = secondAgentUID, "zeta-codex"
	agentClone.Status.PaneRef = secondPaneUID
	pane, _ := store.registry.Pane(preexistingPaneUID)
	paneClone := pane.Clone()
	paneClone.Metadata.UID, paneClone.Metadata.Name = secondPaneUID, "zeta-codex"
	paneClone.Metadata.OwnerRef = &coremetadata.OwnerRef{Kind: coremetadata.KindAgent, UID: secondAgentUID}
	paneClone.Status.Activation.AgentUID = secondAgentUID
	paneClone.Status.Activation.Generation = secondGeneration
	paneClone.Status.Activation.OperationID = secondOperation
	paneClone.Status.Activation.RuntimeID = secondRuntime
	store.registry.Agents = append(store.registry.Agents, agentClone)
	store.registry.Panes = append(store.registry.Panes, paneClone)
	store.registry.NameReservations = append(store.registry.NameReservations,
		coremetadata.NameReservation{Scope: agentClone.Metadata.OwnerUID(), Kind: coremetadata.KindAgent, Name: agentClone.Metadata.Name, UID: secondAgentUID},
		coremetadata.NameReservation{Scope: secondAgentUID, Kind: coremetadata.KindPane, Name: paneClone.Metadata.Name, UID: secondPaneUID},
	)
	stable.dead[secondPaneUID] = true
	secondRow := stable.observed[0]
	secondRow.PaneID, secondRow.PanePID, secondRow.PaneUID = secondRuntime, "648422", secondPaneUID
	secondRow.OwnerUID, secondRow.AgentUID = secondAgentUID, secondAgentUID
	// Put the lexicographically later row first. Observation order remains no
	// authority; the stable UID sort only makes bounded processing repeatable.
	stable.observed = []intmetadata.DeadPaneObservation{secondRow, stable.observed[0]}
}

func preexistingTmuxWriteCount(tmux *fakeTmux) int {
	writes := 0
	for _, recorded := range tmux.calls {
		call := recorded
		if len(call) >= 3 && (call[0] == "-L" || call[0] == "-S") {
			call = call[2:]
		}
		if len(call) >= 3 && call[0] == "-f" {
			call = call[2:]
		}
		if len(call) == 0 {
			continue
		}
		switch call[0] {
		case "new-session", "new-window", "split-window", "kill-session", "kill-window", "kill-pane",
			"set-option", "set-environment", "rename-window", "select-pane", "resize-pane", "set-hook", "source-file", "run-shell":
			writes++
		}
	}
	return writes
}

func preexistingSupervisorReceipt(classification coremetadata.TerminationClassification) coremetadata.TerminationEvidence {
	receipt := coremetadata.TerminationEvidence{
		Source: coremetadata.TerminationSourceSupervisor, Classification: classification,
		PaneUID: preexistingPaneUID, AgentUID: preexistingAgentUID,
		Generation: preexistingGen, OperationID: preexistingOp,
	}
	switch classification {
	case coremetadata.TerminationNormal:
		zero := 0
		receipt.ExitCode = &zero
	case coremetadata.TerminationAbnormal:
		code := 42
		receipt.ExitCode = &code
	case coremetadata.TerminationKilled:
		receipt.Signal = "HUP"
	}
	return receipt
}

func TestPreexistingDeadAgentPaneCandidateAuthorityTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		mutate     func(*coremetadata.Registry, *intmetadata.DeadPaneObservation, *[]coremetadata.TerminationEvidence)
		alive      bool
		wantReason preexistingDeadPaneBlockerReason
		want       bool
	}{
		{name: "exact positive dead and absent supervisor", want: true},
		{name: "active supervisor", alive: true, wantReason: preexistingBlockerSupervisorActive},
		{name: "missing supervisor PID", mutate: func(_ *coremetadata.Registry, row *intmetadata.DeadPaneObservation, _ *[]coremetadata.TerminationEvidence) {
			row.PanePID = ""
		}, wantReason: preexistingBlockerSupervisorUnknown},
		{name: "foreign Pane UID", mutate: func(_ *coremetadata.Registry, row *intmetadata.DeadPaneObservation, _ *[]coremetadata.TerminationEvidence) {
			row.PaneUID = "pan-foreign"
		}, wantReason: preexistingBlockerForeignPane},
		{name: "owner drift", mutate: func(_ *coremetadata.Registry, row *intmetadata.DeadPaneObservation, _ *[]coremetadata.TerminationEvidence) {
			row.OwnerUID = "agt-foreign"
		}, wantReason: preexistingBlockerOwnerDrift},
		{name: "locator drift", mutate: func(_ *coremetadata.Registry, row *intmetadata.DeadPaneObservation, _ *[]coremetadata.TerminationEvidence) {
			row.PaneID = "%20"
		}, wantReason: preexistingBlockerLocatorDrift},
		{name: "generation incomplete", mutate: func(reg *coremetadata.Registry, _ *intmetadata.DeadPaneObservation, _ *[]coremetadata.TerminationEvidence) {
			pane, _ := reg.Pane(preexistingPaneUID)
			pane.Status.Activation.Generation = ""
		}, wantReason: preexistingBlockerActivationDrift},
		{name: "operation incomplete", mutate: func(reg *coremetadata.Registry, _ *intmetadata.DeadPaneObservation, _ *[]coremetadata.TerminationEvidence) {
			pane, _ := reg.Pane(preexistingPaneUID)
			pane.Status.Activation.OperationID = ""
		}, wantReason: preexistingBlockerActivationDrift},
		{name: "current receipt operation conflict", mutate: func(_ *coremetadata.Registry, _ *intmetadata.DeadPaneObservation, receipts *[]coremetadata.TerminationEvidence) {
			receipt := preexistingSupervisorReceipt(coremetadata.TerminationNormal)
			receipt.OperationID = "op-foreign"
			*receipts = []coremetadata.TerminationEvidence{receipt}
		}, wantReason: preexistingBlockerReceiptConflict},
		{name: "stale receipt is a no-op", mutate: func(_ *coremetadata.Registry, _ *intmetadata.DeadPaneObservation, receipts *[]coremetadata.TerminationEvidence) {
			receipt := preexistingSupervisorReceipt(coremetadata.TerminationNormal)
			receipt.Generation = "gen-stale"
			*receipts = []coremetadata.TerminationEvidence{receipt}
		}, want: true},
		{name: "matching duplicate receipt", mutate: func(_ *coremetadata.Registry, _ *intmetadata.DeadPaneObservation, receipts *[]coremetadata.TerminationEvidence) {
			receipt := preexistingSupervisorReceipt(coremetadata.TerminationNormal)
			*receipts = []coremetadata.TerminationEvidence{receipt, receipt}
		}, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, inventory, _ := preexistingRecoveryFixture(t)
			registry := store.registry.Clone()
			row := inventory.observed[0]
			var receipts []coremetadata.TerminationEvidence
			if test.mutate != nil {
				test.mutate(&registry, &row, &receipts)
			}
			candidate, blocker := selectPreexistingDeadAgentPaneCandidate(registry,
				[]intmetadata.DeadPaneObservation{row}, receipts, func(int) bool { return test.alive }, store.mutator())
			if test.want {
				if blocker != nil || candidate == nil || candidate.pane.Metadata.UID != preexistingPaneUID || candidate.supervisor != preexistingPID {
					t.Fatalf("candidate=%+v blocker=%v", candidate, blocker)
				}
				return
			}
			if blocker == nil || blocker.Reason != test.wantReason || candidate != nil {
				t.Fatalf("candidate=%+v blocker=%+v, want reason %s", candidate, blocker, test.wantReason)
			}
		})
	}
}

func TestPreexistingDeadAgentPaneForeignRouteIsTypedFirstWriteZero(t *testing.T) {
	t.Parallel()

	store, inventory, _ := preexistingRecoveryFixture(t)
	tmux := newFakeTmux()
	tmux.appMarker = ""
	target, err := tmuxSocketPathTarget(tmux.socketPath)
	if err != nil {
		t.Fatal(err)
	}
	runner := &controllerTriggerRunner{
		runner: tmux, store: store.store(), receipts: terminationJournal{},
		observe:      func(tmuxTransport) livePaneInventory { return inventory },
		processAlive: func(int) bool { return false },
	}
	before := store.snapshot()
	pass, handled, err := runner.reconcileOnePreexistingDeadAgentPane(context.Background(), target)
	if err != nil || !handled || !strings.Contains(pass.refused, string(preexistingBlockerForeignSocket)) {
		t.Fatalf("pass=%+v handled=%t err=%v", pass, handled, err)
	}
	if store.transactions != 0 || store.writes != 0 || store.snapshot() != before || preexistingTmuxWriteCount(tmux) != 0 || inventory.cleanups != 0 {
		t.Fatalf("foreign route mutated state: transactions=%d writes=%d tmux-writes=%d cleanups=%d", store.transactions, store.writes, preexistingTmuxWriteCount(tmux), inventory.cleanups)
	}
}

func TestPreexistingDeadAgentPaneObservationFailureTableIsTypedFirstWriteZero(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		hostCount   int
		hostErr     error
		deadErr     error
		noDead      bool
		wantReason  preexistingDeadPaneBlockerReason
		wantHandled bool
	}{
		{name: "unreadable host", hostCount: 5, hostErr: errors.New("host inventory unreadable"), wantReason: preexistingBlockerObservation, wantHandled: true},
		{name: "empty host", hostCount: 0, wantReason: preexistingBlockerEmptyObservation, wantHandled: true},
		{name: "unreadable dead inventory", hostCount: 5, deadErr: errors.New("dead inventory unreadable"), wantReason: preexistingBlockerObservation, wantHandled: true},
		{name: "empty dead inventory on ordinary all-live host falls through", hostCount: 5, noDead: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, stable, _ := preexistingRecoveryFixture(t)
			if test.noDead {
				stable.observed = nil
				stable.dead = map[string]bool{}
			}
			inventory := &controlledPreexistingInventory{
				stableDeadPaneInventory: stable, hostCount: test.hostCount,
				hostErr: test.hostErr, deadErr: test.deadErr,
			}
			tmux := newFakeTmux()
			target, err := tmuxSocketPathTarget(tmux.socketPath)
			if err != nil {
				t.Fatal(err)
			}
			runner := &controllerTriggerRunner{
				runner: tmux, store: store.store(), receipts: terminationJournal{},
				observe:      func(tmuxTransport) livePaneInventory { return inventory },
				processAlive: func(int) bool { return false },
			}
			before := store.snapshot()
			pass, handled, err := runner.reconcileOnePreexistingDeadAgentPane(context.Background(), target)
			if err != nil || handled != test.wantHandled {
				t.Fatalf("pass=%+v handled=%t want=%t err=%v", pass, handled, test.wantHandled, err)
			}
			if test.wantReason != "" && !strings.Contains(pass.refused+pass.unobserved, string(test.wantReason)) {
				t.Fatalf("pass=%+v want typed reason %s", pass, test.wantReason)
			}
			if test.noDead && (pass.refused != "" || pass.unobserved != "") {
				t.Fatalf("ordinary no-dead observation was blocked: %+v", pass)
			}
			if store.transactions != 0 || store.writes != 0 || store.snapshot() != before || preexistingTmuxWriteCount(tmux) != 0 || stable.cleanups != 0 {
				t.Fatalf("observation cell mutated state: transactions=%d writes=%d tmux-writes=%d cleanups=%d", store.transactions, store.writes, preexistingTmuxWriteCount(tmux), stable.cleanups)
			}
		})
	}
}

func TestPreexistingDeadAgentPaneDuplicateStableIdentityIsAmbiguous(t *testing.T) {
	t.Parallel()

	t.Run("stable UID", func(t *testing.T) {
		store, inventory, _ := preexistingRecoveryFixture(t)
		duplicate := inventory.observed[0]
		duplicate.PaneID = "%20"
		candidate, blocker := selectPreexistingDeadAgentPaneCandidate(store.registry,
			[]intmetadata.DeadPaneObservation{inventory.observed[0], duplicate}, nil,
			func(int) bool { return false }, store.mutator())
		if candidate != nil || blocker == nil || blocker.Reason != preexistingBlockerAmbiguousPane {
			t.Fatalf("candidate=%+v blocker=%+v", candidate, blocker)
		}
	})
	t.Run("runtime locator", func(t *testing.T) {
		store, inventory, _ := preexistingRecoveryFixture(t)
		addSecondPreexistingRecoveryCandidate(store, inventory)
		rows := slices.Clone(inventory.observed)
		for i := range rows {
			if rows[i].PaneUID == secondPaneUID {
				rows[i].PaneID = preexistingRuntime
			}
		}
		candidate, blocker := selectPreexistingDeadAgentPaneCandidate(store.registry, rows, nil,
			func(int) bool { return false }, store.mutator())
		if candidate != nil || blocker == nil || blocker.Reason != preexistingBlockerAmbiguousPane {
			t.Fatalf("candidate=%+v blocker=%+v", candidate, blocker)
		}
	})
}

func TestPreexistingDeadAgentPaneLockedPositiveDeadBecomesLiveIsFirstWriteZero(t *testing.T) {
	t.Parallel()

	store, stable, _ := preexistingRecoveryFixture(t)
	inventory := &controlledPreexistingInventory{
		stableDeadPaneInventory: stable, hostCount: 5, disappearCall: 3,
	}
	tmux := newFakeTmux()
	target, err := tmuxSocketPathTarget(tmux.socketPath)
	if err != nil {
		t.Fatal(err)
	}
	runner := &controllerTriggerRunner{
		runner: tmux, store: store.store(), receipts: terminationJournal{},
		observe:      func(tmuxTransport) livePaneInventory { return inventory },
		processAlive: func(int) bool { return false },
	}
	before := store.snapshot()
	pass, handled, err := runner.reconcileOnePreexistingDeadAgentPane(context.Background(), target)
	if err != nil || !handled || !strings.Contains(pass.refused, string(preexistingBlockerLocatorDrift)) || inventory.deadCalls != 3 {
		t.Fatalf("pass=%+v handled=%t dead-calls=%d err=%v", pass, handled, inventory.deadCalls, err)
	}
	if store.writes != 0 || store.snapshot() != before || preexistingTmuxWriteCount(tmux) != 0 || stable.cleanups != 0 {
		t.Fatalf("locked live drift mutated state: writes=%d tmux-writes=%d cleanups=%d", store.writes, preexistingTmuxWriteCount(tmux), stable.cleanups)
	}
}

func TestPreexistingDeadAgentPaneLockedActivationDriftTableIsFirstWriteZero(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		mutate     func(*coremetadata.Pane)
		wantReason preexistingDeadPaneBlockerReason
	}{
		{name: "generation", mutate: func(pane *coremetadata.Pane) { pane.Status.Activation.Generation = "gen-resumed" }, wantReason: preexistingBlockerContainmentDrift},
		{name: "operation", mutate: func(pane *coremetadata.Pane) { pane.Status.Activation.OperationID = "op-resumed" }, wantReason: preexistingBlockerActivationDrift},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, inventory, _ := preexistingRecoveryFixture(t)
			lockedStore := store.store()
			lockedStore.updateConvergent = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error) {
				store.transactions++
				working := store.registry.Clone()
				pane, _ := working.Pane(preexistingPaneUID)
				test.mutate(pane)
				if err := fn(&working); err != nil {
					return coremetadata.Registry{}, false, err
				}
				return coremetadata.Registry{}, false, errors.New("locked activation drift unexpectedly planned a write")
			}
			tmux := newFakeTmux()
			target, err := tmuxSocketPathTarget(tmux.socketPath)
			if err != nil {
				t.Fatal(err)
			}
			runner := &controllerTriggerRunner{
				runner: tmux, store: lockedStore, receipts: terminationJournal{},
				observe:      func(tmuxTransport) livePaneInventory { return inventory },
				processAlive: func(int) bool { return false },
			}
			before := store.snapshot()
			pass, handled, err := runner.reconcileOnePreexistingDeadAgentPane(context.Background(), target)
			if err != nil || !handled || !strings.Contains(pass.refused, string(test.wantReason)) {
				t.Fatalf("pass=%+v handled=%t err=%v", pass, handled, err)
			}
			if store.writes != 0 || store.snapshot() != before || preexistingTmuxWriteCount(tmux) != 0 || inventory.cleanups != 0 {
				t.Fatalf("locked %s drift mutated state: writes=%d tmux-writes=%d cleanups=%d", test.name, store.writes, preexistingTmuxWriteCount(tmux), inventory.cleanups)
			}
		})
	}
}

func TestPreexistingDeadAgentPaneReceiptConflictMatrixIsFirstWriteZero(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		mutate func(*coremetadata.TerminationEvidence)
	}{
		{name: "source classification", mutate: func(receipt *coremetadata.TerminationEvidence) {
			receipt.Source = coremetadata.TerminationSourceControlAction
		}},
		{name: "Agent UID", mutate: func(receipt *coremetadata.TerminationEvidence) { receipt.AgentUID = "agt-foreign" }},
		{name: "operation", mutate: func(receipt *coremetadata.TerminationEvidence) { receipt.OperationID = "op-foreign" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, inventory, event := preexistingRecoveryFixture(t)
			receipt := preexistingSupervisorReceipt(coremetadata.TerminationNormal)
			test.mutate(&receipt)
			event.receipts = []coremetadata.TerminationEvidence{receipt}
			before := store.snapshot()
			_, err := reconcileLifecycle(context.Background(), event, inventory, store.store())
			if err == nil || !strings.Contains(err.Error(), string(preexistingBlockerReceiptConflict)) {
				t.Fatalf("error=%v, want receipt conflict", err)
			}
			if store.writes != 0 || inventory.cleanups != 0 || store.snapshot() != before {
				t.Fatalf("conflict wrote state: writes=%d cleanups=%d", store.writes, inventory.cleanups)
			}
		})
	}
}

func TestPreexistingDeadAgentPaneDispositionSessionAndRepeatMatrix(t *testing.T) {
	t.Parallel()

	for _, classification := range []coremetadata.TerminationClassification{
		coremetadata.TerminationNormal, coremetadata.TerminationAbnormal, coremetadata.TerminationUnknown,
	} {
		for _, withSession := range []bool{false, true} {
			name := string(classification) + "/session-absent"
			if withSession {
				name = string(classification) + "/session-present"
			}
			t.Run(name, func(t *testing.T) {
				store, inventory, event := preexistingRecoveryFixture(t)
				if withSession {
					setFixtureSessionRef(t, store, preexistingAgentUID, resumeFixtureRef(resourceFixtureClock))
					store.writes, store.transactions = 0, 0
				}
				if classification != coremetadata.TerminationUnknown {
					event.receipts = []coremetadata.TerminationEvidence{preexistingSupervisorReceipt(classification)}
				}
				beforeProject, _ := store.registry.Project("prj-alpha")
				beforeWindow, _ := store.registry.Window("win-alpha-main")
				beforeShell, _ := store.registry.Pane("pan-alpha-zsh")
				beforeSession := store.registry.Agents[0].Status.SessionRef.Clone()
				result, err := reconcileLifecycle(context.Background(), event, inventory, store.store())
				if err != nil || len(result.cascaded) != 1 || inventory.cleanups != 1 || store.writes != 1 {
					t.Fatalf("result=%+v cleanups=%d writes=%d err=%v", result, inventory.cleanups, store.writes, err)
				}
				if _, exists := store.registry.Pane(preexistingPaneUID); exists || inventory.dead[preexistingPaneUID] || inventory.uids[preexistingPaneUID] {
					t.Fatal("dead Pane runtime or row survived")
				}
				agent, exists := store.registry.Agent(preexistingAgentUID)
				wantPhase := coremetadata.PhaseOffline
				if classification == coremetadata.TerminationAbnormal {
					wantPhase = coremetadata.PhaseFailed
				}
				if !exists || agent.Status.Phase != wantPhase || agent.Status.PaneRef != "" || !reflect.DeepEqual(agent.Status.SessionRef, beforeSession) {
					t.Fatalf("Agent=%+v want phase=%s preserved session=%+v", agent, wantPhase, beforeSession)
				}
				afterProject, _ := store.registry.Project("prj-alpha")
				afterWindow, _ := store.registry.Window("win-alpha-main")
				afterShell, _ := store.registry.Pane("pan-alpha-zsh")
				if !reflect.DeepEqual(beforeProject, afterProject) || !reflect.DeepEqual(beforeWindow, afterWindow) || !reflect.DeepEqual(beforeShell, afterShell) {
					t.Fatal("Project, Window, or shell anchor changed")
				}

				settled := store.snapshot()
				writes := store.writes
				candidate, blocker := selectPreexistingDeadAgentPaneCandidate(store.registry, nil, event.receipts,
					event.processAlive, store.mutator())
				if candidate != nil || blocker != nil || store.writes != writes || inventory.cleanups != 1 || store.snapshot() != settled {
					t.Fatalf("repeat candidate=%+v blocker=%v writes=%d cleanups=%d", candidate, blocker, store.writes, inventory.cleanups)
				}
			})
		}
	}
}

func TestPreexistingDeadAgentPaneLockedSupervisorAndActivationDriftWriteZero(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		drift bool
		alive func(int) bool
		want  string
	}{
		{name: "supervisor becomes active", alive: func() func(int) bool { calls := 0; return func(int) bool { calls++; return calls > 1 } }(), want: string(preexistingBlockerSupervisorActive)},
		{name: "locator drifts", drift: true, alive: func(int) bool { return false }, want: string(preexistingBlockerLocatorDrift)},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, inventory, event := preexistingRecoveryFixture(t)
			event.processAlive = test.alive
			var observed livePaneInventory = inventory
			if test.drift {
				observed = &driftingPreexistingInventory{stableDeadPaneInventory: inventory}
			}
			before := store.snapshot()
			_, err := reconcileLifecycle(context.Background(), event, observed, store.store())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v want %s", err, test.want)
			}
			if store.writes != 0 || inventory.cleanups != 0 || store.snapshot() != before {
				t.Fatalf("TOCTOU drift wrote state: writes=%d cleanups=%d", store.writes, inventory.cleanups)
			}
		})
	}
}

func TestConfigApplyProducerConvergesExactlyOnePreexistingCandidateThenRepeatsNoOp(t *testing.T) {
	t.Parallel()

	store, stable, _ := preexistingRecoveryFixture(t)
	inventory := &convergingPreexistingInventory{stableDeadPaneInventory: stable}
	tmux := newFakeTmux()
	target, err := tmuxSocketPathTarget(tmux.socketPath)
	if err != nil {
		t.Fatal(err)
	}
	runner := &controllerTriggerRunner{
		runner: tmux, store: store.store(), receipts: terminationJournal{},
		observe: func(got tmuxTransport) livePaneInventory {
			if got != target {
				t.Fatalf("producer target=%+v want=%+v", got, target)
			}
			return inventory
		},
		processAlive: func(pid int) bool {
			if pid != preexistingPID {
				t.Fatalf("supervisor PID=%d", pid)
			}
			return false
		},
	}
	first, handled, err := runner.reconcileOnePreexistingDeadAgentPane(context.Background(), target)
	if err != nil || !handled || first.residualExits != 1 || first.refused != "" || inventory.cleanups != 1 || store.writes != 1 {
		t.Fatalf("first pass=%+v handled=%t cleanups=%d writes=%d err=%v", first, handled, inventory.cleanups, store.writes, err)
	}
	writes, calls, settled := store.writes, len(tmux.calls), store.snapshot()
	repeat, handled, err := runner.reconcileOnePreexistingDeadAgentPane(context.Background(), target)
	if err != nil || handled || repeat.changed() || repeat.refused != "" || inventory.cleanups != 1 || store.writes != writes || store.snapshot() != settled {
		t.Fatalf("repeat=%+v handled=%t cleanups=%d writes=%d err=%v", repeat, handled, inventory.cleanups, store.writes, err)
	}
	if len(tmux.calls) <= calls {
		t.Fatal("repeat did not perform its bounded exact-socket observation")
	}
}

func TestConfigApplyProducerContainmentDriftIsTypedFirstWriteZero(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		mutate func(*intmetadata.DeadPaneObservation)
	}{
		{name: "Project UID", mutate: func(row *intmetadata.DeadPaneObservation) { row.ProjectUID = "prj-foreign" }},
		{name: "Window UID", mutate: func(row *intmetadata.DeadPaneObservation) { row.WindowUID = "win-foreign" }},
		{name: "app SessionRole marker", mutate: func(row *intmetadata.DeadPaneObservation) { row.SessionRole = "home" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, inventory, _ := preexistingRecoveryFixture(t)
			test.mutate(&inventory.observed[0])
			tmux := newFakeTmux()
			target, err := tmuxSocketPathTarget(tmux.socketPath)
			if err != nil {
				t.Fatal(err)
			}
			runner := &controllerTriggerRunner{
				runner: tmux, store: store.store(), receipts: terminationJournal{},
				observe:      func(tmuxTransport) livePaneInventory { return inventory },
				processAlive: func(int) bool { return false },
			}
			before := store.snapshot()
			pass, handled, err := runner.reconcileOnePreexistingDeadAgentPane(context.Background(), target)
			if err != nil || !handled || !strings.Contains(pass.refused, string(preexistingBlockerContainmentDrift)) {
				t.Fatalf("pass=%+v handled=%t err=%v", pass, handled, err)
			}
			if store.writes != 0 || inventory.cleanups != 0 || store.snapshot() != before {
				t.Fatalf("containment drift wrote state: writes=%d cleanups=%d", store.writes, inventory.cleanups)
			}
		})
	}
}

func TestConfigApplyProducerBuildsOneDeterministicCandidatePerBoundedPass(t *testing.T) {
	t.Parallel()

	store, stable, _ := preexistingRecoveryFixture(t)
	addSecondPreexistingRecoveryCandidate(store, stable)
	store.writes, store.transactions = 0, 0

	inventory := &convergingPreexistingInventory{stableDeadPaneInventory: stable}
	tmux := newFakeTmux()
	target, err := tmuxSocketPathTarget(tmux.socketPath)
	if err != nil {
		t.Fatal(err)
	}
	runner := &controllerTriggerRunner{
		runner: tmux, store: store.store(), receipts: terminationJournal{},
		observe: func(tmuxTransport) livePaneInventory { return inventory },
		processAlive: func(pid int) bool {
			if pid != preexistingPID && pid != secondSupervisorPID {
				t.Fatalf("unexpected supervisor PID %d", pid)
			}
			return false
		},
	}
	first, handled, err := runner.reconcileOnePreexistingDeadAgentPane(context.Background(), target)
	if err != nil || !handled || first.residualExits != 1 || stable.cleanupTarget.PaneUID != preexistingPaneUID ||
		stable.cleanups != 1 || store.writes != 1 {
		t.Fatalf("first=%+v handled=%t target=%+v cleanups=%d writes=%d err=%v", first, handled, stable.cleanupTarget, stable.cleanups, store.writes, err)
	}
	if _, exists := store.registry.Pane(secondPaneUID); !exists {
		t.Fatal("first bounded pass also deleted its second exact candidate")
	}
	second, handled, err := runner.reconcileOnePreexistingDeadAgentPane(context.Background(), target)
	if err != nil || !handled || second.residualExits != 1 || stable.cleanupTarget.PaneUID != secondPaneUID ||
		stable.cleanups != 2 || store.writes != 2 {
		t.Fatalf("second=%+v handled=%t target=%+v cleanups=%d writes=%d err=%v", second, handled, stable.cleanupTarget, stable.cleanups, store.writes, err)
	}
	settled := store.snapshot()
	repeat, handled, err := runner.reconcileOnePreexistingDeadAgentPane(context.Background(), target)
	if err != nil || handled || repeat.changed() || stable.cleanups != 2 || store.writes != 2 || store.snapshot() != settled {
		t.Fatalf("repeat=%+v handled=%t cleanups=%d writes=%d err=%v", repeat, handled, stable.cleanups, store.writes, err)
	}
}

func TestConfigApplyControllerLeaseBoundsOneCandidatePassesAndProvesFixedPoint(t *testing.T) {
	t.Parallel()

	store, stable, _ := preexistingRecoveryFixture(t)
	addSecondPreexistingRecoveryCandidate(store, stable)
	store.writes, store.transactions = 0, 0
	inventory := &convergingPreexistingInventory{stableDeadPaneInventory: stable}
	tmux := newFakeTmux()
	target, err := tmuxSocketPathTarget(tmux.socketPath)
	if err != nil {
		t.Fatal(err)
	}
	runner := &controllerTriggerRunner{
		runner: tmux, store: store.store(), receipts: terminationJournal{},
		events: controllerEventLog{dir: t.TempDir()}, maxPasses: 3,
		observe: func(tmuxTransport) livePaneInventory { return inventory },
		processAlive: func(pid int) bool {
			if pid != preexistingPID && pid != secondSupervisorPID {
				t.Fatalf("unexpected supervisor PID %d", pid)
			}
			return false
		},
	}
	// Exercise the real run/lease/event/max-pass loop while keeping the final
	// all-live/no-dead fallthrough a no-op. Candidate production and lifecycle
	// execution remain the production methods rather than a canned pass result.
	runner.pass = func(ctx context.Context, trigger controllerTrigger) (controllerPassResult, error) {
		pass, handled, err := runner.reconcileOnePreexistingDeadAgentPane(ctx, trigger.target)
		if err != nil || handled {
			return pass, err
		}
		return controllerPassResult{}, nil
	}
	trigger := controllerTrigger{reason: controllerTriggerConfigApply, target: target}
	first, err := runner.run(context.Background(), trigger)
	if err != nil || first.passes != 3 || first.changed != 2 || first.events != 1 || !first.converged || first.refused != "" {
		t.Fatalf("first outcome=%+v err=%v", first, err)
	}
	if stable.cleanups != 2 || store.writes != 2 || preexistingTmuxWriteCount(tmux) != 0 {
		t.Fatalf("bounded run cleanups=%d Registry-writes=%d tmux-writes=%d", stable.cleanups, store.writes, preexistingTmuxWriteCount(tmux))
	}
	if pending, err := runner.events.pending(target, false); err != nil || pending {
		t.Fatalf("bounded run left pending event=%t err=%v", pending, err)
	}
	settled := store.snapshot()
	repeat, err := runner.run(context.Background(), trigger)
	if err != nil || repeat.passes != 1 || repeat.changed != 0 || repeat.events != 1 || !repeat.converged ||
		stable.cleanups != 2 || store.writes != 2 || store.snapshot() != settled || preexistingTmuxWriteCount(tmux) != 0 {
		t.Fatalf("repeat outcome=%+v cleanups=%d writes=%d tmux-writes=%d err=%v", repeat, stable.cleanups, store.writes, preexistingTmuxWriteCount(tmux), err)
	}
}
