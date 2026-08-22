package app

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/pins"
)

type exactPaneExitInventory struct {
	uids           map[string]bool
	windowUID      string
	windows        map[string]bool
	windowSessions map[string]int
	err            error
	calls          int
	hostPanes      int
}

func (i *exactPaneExitInventory) LiveWindowUIDs(context.Context) (map[string]bool, error) {
	if i.err != nil {
		return nil, i.err
	}
	return i.windows, nil
}

func (i *exactPaneExitInventory) LiveWindowSessionCounts(context.Context) (map[string]int, error) {
	if i.err != nil {
		return nil, i.err
	}
	return i.windowSessions, nil
}

func (i *exactPaneExitInventory) LivePaneUIDs(context.Context) (map[string]bool, error) {
	i.calls++
	if i.err != nil {
		return nil, i.err
	}
	return i.uids, nil
}

func (i *exactPaneExitInventory) LivePaneCount(context.Context) (int, error) {
	if i.err != nil {
		return 0, i.err
	}
	if i.hostPanes != 0 {
		return i.hostPanes, nil
	}
	return len(i.uids), nil
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
	pane, ok := store.registry.Pane(paneUID)
	if !ok {
		t.Fatalf("Pane %q disappeared", paneUID)
	}
	windowUID, ok := paneWindowUID(store.registry, *pane)
	if !ok {
		t.Fatalf("Pane %q has no Window owner", paneUID)
	}
	for i := range store.registry.Windows {
		if store.registry.Windows[i].Metadata.UID == windowUID {
			store.registry.Windows[i].Status.RuntimeSessionID = "$1"
			store.registry.Windows[i].Status.RuntimeID = "@4"
			return
		}
	}
	t.Fatalf("Window %q disappeared", windowUID)
}

func exactPaneExitDirty(receipts ...coremetadata.TerminationEvidence) lifecycleDirtyEvent {
	return lifecycleDirtyEvent{
		target:        explicitTmuxTarget{flag: "-S", value: "/tmp/phase2-exact.sock"},
		runtimePaneID: "%9",
		teardownKind:  coremetadata.TeardownEventPaneExited,
		receipts:      receipts,
	}
}

type lifecyclePinStore struct {
	set     pins.Set
	saves   int
	loadErr error
	saveErr error
}

func (s *lifecyclePinStore) Path() string            { return "/tmp/phase3-pins" }
func (s *lifecyclePinStore) Load() (pins.Set, error) { return s.set, s.loadErr }
func (s *lifecyclePinStore) Save(set pins.Set) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.set = set
	s.saves++
	return nil
}

func renameFixtureProjectUID(t *testing.T, registry *coremetadata.Registry, oldUID, newUID string) {
	t.Helper()
	for i := range registry.Projects {
		if registry.Projects[i].Metadata.UID == oldUID {
			registry.Projects[i].Metadata.UID = newUID
		}
	}
	for i := range registry.Windows {
		owner := registry.Windows[i].Metadata.OwnerRef
		if owner != nil && owner.Kind == coremetadata.KindProject && owner.UID == oldUID {
			owner.UID = newUID
		}
	}
	for i := range registry.NameReservations {
		if registry.NameReservations[i].Scope == oldUID {
			registry.NameReservations[i].Scope = newUID
		}
		if registry.NameReservations[i].UID == oldUID {
			registry.NameReservations[i].UID = newUID
		}
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("renamed fixture Project uid: %v", err)
	}
}

func prepareLastBetaProjectCascade(t *testing.T) (*fakeResourceStore, *exactPaneExitInventory, lifecycleDirtyEvent) {
	t.Helper()
	store := newFakeResourceStore(t)
	renameFixtureProjectUID(t, &store.registry, "prj-beta", "proj-beta")
	activateExactPane(t, store, "pan-beta-zsh", "", "gen-last", "%9")
	receipt := phase2NormalReceipt("pan-beta-zsh", "", "gen-last")
	live := exitReconcileFixtureLiveExcept("pan-beta-zsh")
	live["pan-gone-zsh"] = true
	inventory := &exactPaneExitInventory{uids: live, windowUID: "win-beta-main"}
	if _, err := reconcileLifecycle(context.Background(), exactPaneExitDirty(receipt), inventory, store.store()); err != nil {
		t.Fatal(err)
	}
	inventory.windows = map[string]bool{"win-alpha-main": true, "win-alpha-review": true}
	inventory.windowSessions = map[string]int{}
	event := exactPaneExitDirty()
	event.teardownKind = coremetadata.TeardownEventWindowUnlinked
	event.runtimePaneID = ""
	event.runtimeSessionID = "$1"
	event.runtimeWindowID = "@4"
	return store, inventory, event
}

func TestLastPaneThenWindowUnlinkedDeletesLastProjectGraphAndConvertsPin(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	renameFixtureProjectUID(t, &store.registry, "prj-beta", "proj-beta")
	alphaBefore, _ := store.registry.Project("prj-alpha")
	alphaWindowsBefore := store.registry.WindowsOf("prj-alpha")
	alphaPanesBefore := []coremetadata.Pane{}
	for _, window := range alphaWindowsBefore {
		alphaPanesBefore = append(alphaPanesBefore, store.registry.PanesOf(window.Metadata.UID)...)
	}
	activateExactPane(t, store, "pan-beta-zsh", "", "gen-last", "%9")
	receipt := phase2NormalReceipt("pan-beta-zsh", "", "gen-last")
	live := exitReconcileFixtureLiveExcept("pan-beta-zsh")
	live["pan-gone-zsh"] = true
	inventory := &exactPaneExitInventory{uids: live, windowUID: "win-beta-main"}

	pending, err := reconcileLifecycle(context.Background(), exactPaneExitDirty(receipt), inventory, store.store())
	if err != nil {
		t.Fatalf("record last-Pane evidence: %v", err)
	}
	if len(pending.pending) != 1 || pending.transactions != 1 {
		t.Fatalf("pending result = %+v", pending)
	}
	if _, ok := store.registry.Project("proj-beta"); !ok {
		t.Fatal("pane-exited deleted the Project before window-unlinked")
	}

	managed, _ := pins.ProjectPin("proj-beta")
	other, _ := pins.CandidatePin("/srv/other")
	pinStore := &lifecyclePinStore{set: pins.Set{Format: pins.FormatTyped, Pins: []pins.Pin{other, managed}}}
	inventory.windows = map[string]bool{"win-alpha-main": true, "win-alpha-review": true}
	inventory.windowSessions = map[string]int{}
	unlinked := exactPaneExitDirty()
	unlinked.teardownKind = coremetadata.TeardownEventWindowUnlinked
	unlinked.runtimePaneID = ""
	unlinked.runtimeSessionID = "$1"
	unlinked.runtimeWindowID = "@4"
	unlinked.pinStore = pinStore
	result, err := reconcileLifecycle(context.Background(), unlinked, inventory, store.store())
	if err != nil {
		t.Fatalf("cascade last Project boundary: %v", err)
	}
	if len(result.rootCascaded) != 1 || !result.rootCascaded[0].Changed || result.transactions != 1 {
		t.Fatalf("root cascade result = %+v", result)
	}
	for _, uid := range []string{"proj-beta", "win-beta-main", "pan-beta-zsh"} {
		if _, ok := registryUIDs(store.registry)[uid]; ok {
			t.Fatalf("deleted Project descendant %s survived", uid)
		}
	}
	for _, uid := range []string{"prj-alpha", "win-alpha-main", "pan-alpha-zsh"} {
		if _, ok := registryUIDs(store.registry)[uid]; !ok {
			t.Fatalf("sibling resource %s was deleted", uid)
		}
	}
	alphaAfter, _ := store.registry.Project("prj-alpha")
	alphaWindowsAfter := store.registry.WindowsOf("prj-alpha")
	alphaPanesAfter := []coremetadata.Pane{}
	for _, window := range alphaWindowsAfter {
		alphaPanesAfter = append(alphaPanesAfter, store.registry.PanesOf(window.Metadata.UID)...)
	}
	if !reflect.DeepEqual(alphaBefore, alphaAfter) || !reflect.DeepEqual(alphaWindowsBefore, alphaWindowsAfter) || !reflect.DeepEqual(alphaPanesBefore, alphaPanesAfter) {
		t.Fatal("last-Project cascade changed the sibling Project graph")
	}
	if pinStore.saves != 1 || len(pinStore.set.Pins) != 2 || pinStore.set.Pins[1].Kind != pins.KindCandidate || pinStore.set.Pins[1].Value != "/srv/beta" {
		t.Fatalf("post-delete pins = %+v, saves=%d", pinStore.set, pinStore.saves)
	}
	if repeat, err := reconcileLifecycle(context.Background(), unlinked, inventory, store.store()); err != nil || repeat.transactions != 0 {
		t.Fatalf("duplicate unlink = %+v, %v", repeat, err)
	}
}

func TestProjectCascadePinFailuresRetainRegistryAndRollbackPreference(t *testing.T) {
	t.Parallel()
	managed, _ := pins.ProjectPin("proj-beta")
	other, _ := pins.CandidatePin("/srv/other")
	initial := pins.Set{Format: pins.FormatTyped, Pins: []pins.Pin{other, managed}}

	t.Run("pin save failure", func(t *testing.T) {
		store, inventory, event := prepareLastBetaProjectCascade(t)
		pinStore := &lifecyclePinStore{set: initial, saveErr: errors.New("pin read-only")}
		event.pinStore = pinStore
		before := store.snapshot()
		if _, err := reconcileLifecycle(context.Background(), event, inventory, store.store()); err == nil {
			t.Fatal("pin failure did not refuse the cascade")
		}
		if store.snapshot() != before || !reflect.DeepEqual(pinStore.set, initial) {
			t.Fatal("pin failure changed Registry or preferences")
		}
	})

	t.Run("Registry commit failure rolls pin back", func(t *testing.T) {
		store, inventory, event := prepareLastBetaProjectCascade(t)
		pinStore := &lifecyclePinStore{set: initial}
		event.pinStore = pinStore
		failing := store.store()
		failing.updateConvergent = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error) {
			working := store.registry.Clone()
			if err := fn(&working); err != nil {
				return coremetadata.Registry{}, false, err
			}
			return coremetadata.Registry{}, false, errors.New("Registry replace failed")
		}
		before := store.snapshot()
		if _, err := reconcileLifecycle(context.Background(), event, inventory, failing); err == nil {
			t.Fatal("Registry failure did not surface")
		}
		if store.snapshot() != before || !reflect.DeepEqual(pinStore.set, initial) || pinStore.saves != 2 {
			t.Fatalf("rollback Registry=%t pins=%+v saves=%d", store.snapshot() == before, pinStore.set, pinStore.saves)
		}
	})
}

func TestWindowUnlinkedWithoutExactPendingEvidenceWritesNothing(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	before := store.snapshot()
	live := make(map[string]bool, len(store.registry.Panes))
	for _, pane := range store.registry.Panes {
		live[pane.Metadata.UID] = true
	}
	event := exactPaneExitDirty()
	event.teardownKind = coremetadata.TeardownEventWindowUnlinked
	event.runtimePaneID = ""
	event.runtimeSessionID = "$1"
	event.runtimeWindowID = "@4"
	result, err := reconcileLifecycle(context.Background(), event,
		&exactPaneExitInventory{uids: live, windows: map[string]bool{}, windowSessions: map[string]int{}}, store.store())
	if err != nil {
		t.Fatal(err)
	}
	if result.transactions != 0 || store.snapshot() != before {
		t.Fatalf("unpaired window-unlinked result=%+v changed Registry", result)
	}
}

func TestLastPaneOfNonLastProjectWindowDeletesOnlyThatWindow(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	activateExactPane(t, store, "pan-alpha-review", "", "gen-review", "%9")
	receipt := phase2NormalReceipt("pan-alpha-review", "", "gen-review")
	live := exitReconcileFixtureLiveExcept("pan-alpha-review")
	live["pan-gone-zsh"] = true
	inventory := &exactPaneExitInventory{uids: live, windowUID: "win-alpha-review"}
	if _, err := reconcileLifecycle(context.Background(), exactPaneExitDirty(receipt), inventory, store.store()); err != nil {
		t.Fatal(err)
	}
	inventory.windows = map[string]bool{"win-alpha-main": true, "win-beta-main": true}
	inventory.windowSessions = map[string]int{"$1": 1}
	event := exactPaneExitDirty()
	event.teardownKind = coremetadata.TeardownEventWindowUnlinked
	event.runtimePaneID = ""
	event.runtimeSessionID = "$1"
	event.runtimeWindowID = "@4"
	result, err := reconcileLifecycle(context.Background(), event, inventory, store.store())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.rootCascaded) != 1 || result.rootCascaded[0].Decision.RootAction != coremetadata.RootTeardownRetainProject {
		t.Fatalf("non-last Window cascade = %+v", result)
	}
	if _, ok := store.registry.Window("win-alpha-review"); ok {
		t.Fatal("unlinked Window survived")
	}
	if project, ok := store.registry.Project("prj-alpha"); !ok || project.Spec.PrimaryWindowRef != "win-alpha-main" {
		t.Fatalf("Project root/sibling changed: %+v", project)
	}
}

func TestUnmirroredSiblingWindowRefusesRootCascade(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	activateExactPane(t, store, "pan-beta-zsh", "", "gen-foreign", "%9")
	receipt := phase2NormalReceipt("pan-beta-zsh", "", "gen-foreign")
	live := exitReconcileFixtureLiveExcept("pan-beta-zsh")
	live["pan-gone-zsh"] = true
	inventory := &exactPaneExitInventory{uids: live, windowUID: "win-beta-main"}
	if _, err := reconcileLifecycle(context.Background(), exactPaneExitDirty(receipt), inventory, store.store()); err != nil {
		t.Fatal(err)
	}
	before := store.snapshot()
	inventory.windows = map[string]bool{"win-alpha-main": true}
	inventory.windowSessions = map[string]int{"$1": 1}
	event := exactPaneExitDirty()
	event.teardownKind = coremetadata.TeardownEventWindowUnlinked
	event.runtimePaneID = ""
	event.runtimeSessionID = "$1"
	event.runtimeWindowID = "@4"
	result, err := reconcileLifecycle(context.Background(), event, inventory, store.store())
	if err != nil {
		t.Fatal(err)
	}
	if result.transactions != 0 || store.snapshot() != before {
		t.Fatalf("unmirrored sibling result=%+v changed Registry", result)
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
