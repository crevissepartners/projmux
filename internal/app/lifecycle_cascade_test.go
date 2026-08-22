package app

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

type exactPaneExitInventory struct {
	uids      map[string]bool
	windowUID string
	err       error
	calls     int
}

func (i *exactPaneExitInventory) LivePaneUIDs(context.Context) (map[string]bool, error) {
	i.calls++
	if i.err != nil {
		return nil, i.err
	}
	return i.uids, nil
}

func (i *exactPaneExitInventory) ResolveWindowUID(context.Context, string) (string, error) {
	if i.err != nil {
		return "", i.err
	}
	return i.windowUID, nil
}

func phase2NormalReceipt(paneUID, agentUID, generation string) coremetadata.TerminationEvidence {
	zero := 0
	return coremetadata.TerminationEvidence{
		Source: coremetadata.TerminationSourceSupervisor, Classification: coremetadata.TerminationNormal,
		PaneUID: paneUID, AgentUID: agentUID, Generation: generation, ExitCode: &zero,
	}
}

func activateExactPane(t *testing.T, store *fakeResourceStore, paneUID, agentUID, generation, runtimeID string) {
	t.Helper()
	activatePaneFixture(t, store, paneUID, agentUID, generation)
	if _, err := store.mutator().ObservePaneActivationRuntime(&store.registry, paneUID, generation, runtimeID); err != nil {
		t.Fatalf("ObservePaneActivationRuntime: %v", err)
	}
}

func exactPaneExitDirty(receipts ...coremetadata.TerminationEvidence) lifecycleDirtyEvent {
	return lifecycleDirtyEvent{
		target:        explicitTmuxTarget{flag: "-S", value: "/tmp/phase2-exact.sock"},
		runtimePaneID: "%9", runtimeWindowID: "@4",
		teardownKind: coremetadata.TeardownEventPaneExited,
		receipts:     receipts,
	}
}

func TestExactCleanShellAndProviderExitCascadeRowsAfterJournalEvidence(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		paneUID  string
		agentUID string
	}{
		{name: "shell", paneUID: "pan-alpha-log"},
		{name: "provider", paneUID: "pan-alpha-codex", agentUID: "agt-alpha-codex"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newFakeResourceStore(t)
			activateExactPane(t, store, test.paneUID, test.agentUID, "gen-clean", "%9")
			receipt := phase2NormalReceipt(test.paneUID, test.agentUID, "gen-clean")
			journal := terminationJournal{path: filepath.Join(t.TempDir(), terminationJournalFile)}
			if err := journal.append(receipt); err != nil {
				t.Fatalf("append pre-delete evidence: %v", err)
			}
			receipts, err := journal.read()
			if err != nil {
				t.Fatalf("read pre-delete evidence: %v", err)
			}
			live := exitReconcileFixtureLiveExcept(test.paneUID)
			inventory := &exactPaneExitInventory{uids: live, windowUID: "win-alpha-main"}
			result, err := reconcileLifecycle(context.Background(), exactPaneExitDirty(receipts...), inventory, store.store())
			if err != nil {
				t.Fatalf("reconcile exact clean exit: %v", err)
			}
			if len(result.cascaded) != 1 || !result.cascaded[0].Changed || result.transactions != 1 {
				t.Fatalf("cascade result = %+v", result)
			}
			if _, ok := store.registry.Pane(test.paneUID); ok {
				t.Fatalf("clean %s Pane row survived", test.name)
			}
			if test.agentUID != "" {
				if _, ok := store.registry.Agent(test.agentUID); ok {
					t.Fatalf("clean provider Agent row %s survived", test.agentUID)
				}
			}
			for _, uid := range []string{"pan-alpha-zsh", "pan-alpha-review", "pan-beta-zsh"} {
				if _, ok := store.registry.Pane(uid); !ok {
					t.Fatalf("sibling Pane %s was deleted", uid)
				}
			}
			if _, ok := store.registry.Window("win-alpha-main"); !ok {
				t.Fatal("Phase 2 cascade deleted its Window")
			}
			persisted, err := journal.read()
			if err != nil || len(persisted) != 1 || persisted[0].Classification != coremetadata.TerminationNormal {
				t.Fatalf("post-delete journal = %+v, %v", persisted, err)
			}
			settled := store.snapshot()
			repeat, err := reconcileLifecycle(context.Background(), exactPaneExitDirty(receipts...), inventory, store.store())
			if err != nil || repeat.transactions != 0 || store.snapshot() != settled {
				t.Fatalf("duplicate receipt result=%+v err=%v", repeat, err)
			}
		})
	}
}

func TestExactPaneExitNegativeAuthorityRowsProduceDeletePlanZero(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name           string
		classification coremetadata.TerminationClassification
		source         coremetadata.TerminationSource
		live           map[string]bool
		windowUID      string
		inventoryErr   error
	}{
		{name: "abnormal", classification: coremetadata.TerminationAbnormal, source: coremetadata.TerminationSourceSupervisor, live: exitReconcileFixtureLiveExcept("pan-alpha-codex"), windowUID: "win-alpha-main"},
		{name: "killed", classification: coremetadata.TerminationKilled, source: coremetadata.TerminationSourceSupervisor, live: exitReconcileFixtureLiveExcept("pan-alpha-codex"), windowUID: "win-alpha-main"},
		{name: "unknown", classification: coremetadata.TerminationUnknown, source: coremetadata.TerminationSourceReconcile, live: exitReconcileFixtureLiveExcept("pan-alpha-codex"), windowUID: "win-alpha-main"},
		{name: "empty inventory", classification: coremetadata.TerminationNormal, source: coremetadata.TerminationSourceSupervisor, live: map[string]bool{}, windowUID: "win-alpha-main"},
		{name: "foreign window", classification: coremetadata.TerminationNormal, source: coremetadata.TerminationSourceSupervisor, live: exitReconcileFixtureLiveExcept("pan-alpha-codex"), windowUID: "win-alpha-review"},
		{name: "unavailable", classification: coremetadata.TerminationNormal, source: coremetadata.TerminationSourceSupervisor, inventoryErr: errors.New("permission denied")},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newFakeResourceStore(t)
			activateExactPane(t, store, "pan-alpha-codex", "agt-alpha-codex", "gen-negative", "%9")
			receipt := phase2NormalReceipt("pan-alpha-codex", "agt-alpha-codex", "gen-negative")
			receipt.Classification = test.classification
			receipt.Source = test.source
			if test.classification == coremetadata.TerminationKilled {
				receipt.ExitCode = nil
				receipt.Signal = "HUP"
			}
			if test.classification == coremetadata.TerminationUnknown {
				receipt.ExitCode = nil
			}
			beforeWindow, _ := store.registry.Window("win-alpha-main")
			inventory := &exactPaneExitInventory{uids: test.live, windowUID: test.windowUID, err: test.inventoryErr}
			result, err := reconcileLifecycle(context.Background(), exactPaneExitDirty(receipt), inventory, store.store())
			if err != nil {
				t.Fatalf("negative exact exit: %v", err)
			}
			if len(result.cascaded) != 0 {
				t.Fatalf("negative result created delete plan: %+v", result.cascaded)
			}
			if _, ok := store.registry.Pane("pan-alpha-codex"); !ok {
				t.Fatal("negative authority deleted the Pane")
			}
			if _, ok := store.registry.Agent("agt-alpha-codex"); !ok {
				t.Fatal("negative authority deleted the Agent")
			}
			afterWindow, _ := store.registry.Window("win-alpha-main")
			if !reflect.DeepEqual(beforeWindow, afterWindow) {
				t.Fatalf("negative authority changed Window: before=%+v after=%+v", beforeWindow, afterWindow)
			}
		})
	}
}

func TestLatePaneExitNeverFollowsAResumedGeneration(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	activateExactPane(t, store, "pan-alpha-codex", "agt-alpha-codex", "gen-old", "%9")
	old := phase2NormalReceipt("pan-alpha-codex", "agt-alpha-codex", "gen-old")
	activateExactPane(t, store, "pan-alpha-codex", "agt-alpha-codex", "gen-new", "%10")
	before := store.snapshot()
	result, err := reconcileLifecycle(context.Background(), exactPaneExitDirty(old),
		&exactPaneExitInventory{uids: exitReconcileFixtureLiveExcept("pan-alpha-codex"), windowUID: "win-alpha-main"}, store.store())
	if err != nil {
		t.Fatalf("late old generation: %v", err)
	}
	if len(result.cascaded) != 0 || result.transactions != 0 || store.snapshot() != before {
		t.Fatalf("late old generation result=%+v changed current Registry", result)
	}
	if agent, ok := store.registry.Agent("agt-alpha-codex"); !ok || agent.Status.PaneRef != "pan-alpha-codex" || agent.Status.Phase != coremetadata.PhaseRunning {
		t.Fatalf("resumed Agent = %+v, want current binding", agent)
	}
}

func TestExactPaneExitReceiptPermutationsConvergeToOneIdempotentPlan(t *testing.T) {
	t.Parallel()

	normal := phase2NormalReceipt("pan-alpha-codex", "agt-alpha-codex", "gen-current")
	abnormal := normal
	abnormal.Classification = coremetadata.TerminationAbnormal
	abnormal.Generation = "gen-stale"
	seven := 7
	abnormal.ExitCode = &seven
	permutations := [][]coremetadata.TerminationEvidence{
		{normal, abnormal, normal},
		{abnormal, normal},
		{normal, normal, abnormal},
	}
	var want string
	for index, receipts := range permutations {
		store := newFakeResourceStore(t)
		activateExactPane(t, store, "pan-alpha-codex", "agt-alpha-codex", "gen-current", "%9")
		result, err := reconcileLifecycle(context.Background(), exactPaneExitDirty(receipts...),
			&exactPaneExitInventory{uids: exitReconcileFixtureLiveExcept("pan-alpha-codex"), windowUID: "win-alpha-main"},
			store.store())
		if err != nil {
			t.Fatalf("permutation %d: %v", index, err)
		}
		if len(result.cascaded) != 1 {
			t.Fatalf("permutation %d result = %+v", index, result)
		}
		if index == 0 {
			want = store.snapshot()
		} else if got := store.snapshot(); got != want {
			t.Fatalf("permutation %d changed final Registry:\nwant:\n%s\ngot:\n%s", index, want, got)
		}
		repeat, err := reconcileLifecycle(context.Background(), exactPaneExitDirty(receipts...),
			&exactPaneExitInventory{uids: exitReconcileFixtureLiveExcept("pan-alpha-codex"), windowUID: "win-alpha-main"},
			store.store())
		if err != nil || repeat.transactions != 0 {
			t.Fatalf("permutation %d repeat = %+v, %v", index, repeat, err)
		}
	}
}
