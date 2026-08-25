package app

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/pins"
)

type exactPaneExitInventory struct {
	uids           map[string]bool
	dead           map[string]bool
	windowUID      string
	windows        map[string]bool
	windowSessions map[string]int
	err            error
	calls          int
	hostPanes      int
	replacementErr error
	prepares       int
	rollbacks      int
	cleanups       int
	cleanupErr     error
	prepareHook    func([]paneReplacementShell)
}

func (i *exactPaneExitInventory) PrepareLifecycleReplacements(_ context.Context, replacements []paneReplacementShell) (paneReplacementReceipt, error) {
	i.prepares++
	if i.replacementErr != nil {
		return paneReplacementReceipt{}, i.replacementErr
	}
	if len(replacements) != 1 {
		return paneReplacementReceipt{}, errors.New("test lifecycle replacement requires exactly one shell")
	}
	if i.prepareHook != nil {
		i.prepareHook(replacements)
	}
	return paneReplacementReceipt{}, nil
}

func (i *exactPaneExitInventory) RollbackLifecycleReplacements(context.Context, paneReplacementReceipt) error {
	i.rollbacks++
	return nil
}

func (i *exactPaneExitInventory) CleanupLifecycleDeadPane(_ context.Context, target paneLiveDeleteTarget) error {
	i.cleanups++
	if i.cleanupErr != nil {
		return i.cleanupErr
	}
	delete(i.uids, target.PaneUID)
	delete(i.dead, target.PaneUID)
	return nil
}

func (i *exactPaneExitInventory) DeadPaneUIDs(context.Context) (map[string]bool, error) {
	if i.err != nil {
		return nil, i.err
	}
	return i.dead, nil
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
	pane, err := store.mutator().AttachAgentPane(&store.registry, "agt-beta-codex", coremetadata.BootstrapPane{CWD: "/srv/beta"}, "attach-last-agent")
	if err != nil {
		t.Fatalf("attach last Agent Pane: %v", err)
	}
	if err := store.mutator().DeletePane(&store.registry, "pan-beta-zsh"); err != nil {
		t.Fatalf("delete prior beta shell: %v", err)
	}
	activateExactPane(t, store, pane.Metadata.UID, "agt-beta-codex", "gen-last", "%9")
	receipt := phase2NormalReceipt(pane.Metadata.UID, "agt-beta-codex", "gen-last")
	live := map[string]bool{}
	for _, candidate := range store.registry.Panes {
		live[candidate.Metadata.UID] = true
	}
	inventory := &exactPaneExitInventory{
		uids: live, dead: map[string]bool{pane.Metadata.UID: true}, windowUID: "win-beta-main",
	}
	event := exactPaneExitDirty(receipt)
	return store, inventory, event
}

func TestLastPaneExitRetainsProjectWindowAndCreatesLiveReplacementBeforeCommit(t *testing.T) {
	t.Parallel()
	store, inventory, event := prepareLastBetaProjectCascade(t)
	alphaBefore, _ := store.registry.Project("prj-alpha")
	alphaWindowsBefore := store.registry.WindowsOf("prj-alpha")
	alphaPanesBefore := []coremetadata.Pane{}
	for _, window := range alphaWindowsBefore {
		alphaPanesBefore = append(alphaPanesBefore, store.registry.PanesOf(window.Metadata.UID)...)
	}
	deadPaneUID := event.receipts[0].PaneUID
	managed, _ := pins.ProjectPin("proj-beta")
	other, _ := pins.CandidatePin("/srv/other")
	pinStore := &lifecyclePinStore{set: pins.Set{Format: pins.FormatTyped, Pins: []pins.Pin{other, managed}}}
	inventory.prepareHook = func(replacements []paneReplacementShell) {
		if _, ok := store.registry.Pane(replacements[0].Pane.Metadata.UID); ok {
			t.Fatal("replacement Registry row committed before its live shell was prepared")
		}
		if _, ok := store.registry.Pane(deadPaneUID); !ok {
			t.Fatal("exited Pane Registry row disappeared before its live replacement was prepared")
		}
	}
	event.pinStore = pinStore
	result, err := reconcileLifecycle(context.Background(), event, inventory, store.store())
	if err != nil {
		t.Fatalf("retain last-Pane Window: %v", err)
	}
	if len(result.cascaded) != 1 || !result.cascaded[0].Changed || len(result.rootCascaded) != 0 || result.transactions != 1 {
		t.Fatalf("retained Window lifecycle result = %+v", result)
	}
	if inventory.prepares != 1 || inventory.cleanups != 1 {
		t.Fatalf("live replacement prepares=%d cleanups=%d, want 1/1", inventory.prepares, inventory.cleanups)
	}
	for _, uid := range []string{"proj-beta", "win-beta-main"} {
		if _, ok := registryUIDs(store.registry)[uid]; !ok {
			t.Fatalf("retained identity %s disappeared", uid)
		}
	}
	if _, ok := store.registry.Pane(deadPaneUID); ok {
		t.Fatal("exited Pane row survived")
	}
	retained, _ := store.registry.Window("win-beta-main")
	replacement, ok := store.registry.Pane(retained.Spec.AnchorPaneRef)
	if !ok || replacement.Spec.Role != coremetadata.PaneRoleShell || replacement.Metadata.OwnerUID() != retained.Metadata.UID {
		t.Fatalf("replacement shell = %+v", replacement)
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
	if pinStore.saves != 0 || !reflect.DeepEqual(pinStore.set, pins.Set{Format: pins.FormatTyped, Pins: []pins.Pin{other, managed}}) {
		t.Fatalf("automatic retention touched pins = %+v, saves=%d", pinStore.set, pinStore.saves)
	}
	if repeat, err := reconcileLifecycle(context.Background(), event, inventory, store.store()); err != nil || repeat.transactions != 0 || inventory.prepares != 1 || inventory.cleanups != 1 {
		t.Fatalf("duplicate pane-exit = %+v, prepares=%d, %v", repeat, inventory.prepares, err)
	}
}

func TestAutomaticWindowRetentionNeverUsesProjectPinAuthority(t *testing.T) {
	t.Parallel()
	managed, _ := pins.ProjectPin("proj-beta")
	other, _ := pins.CandidatePin("/srv/other")
	initial := pins.Set{Format: pins.FormatTyped, Pins: []pins.Pin{other, managed}}
	store, inventory, event := prepareLastBetaProjectCascade(t)
	pinStore := &lifecyclePinStore{set: initial, loadErr: errors.New("must not load"), saveErr: errors.New("must not save")}
	event.pinStore = pinStore
	result, err := reconcileLifecycle(context.Background(), event, inventory, store.store())
	if err != nil || len(result.cascaded) != 1 {
		t.Fatalf("retained Window lifecycle = %+v, %v", result, err)
	}
	if !reflect.DeepEqual(pinStore.set, initial) || pinStore.saves != 0 {
		t.Fatalf("automatic retention touched pins: %+v saves=%d", pinStore.set, pinStore.saves)
	}
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

func TestWindowUnlinkedWithLegacyExactTeardownEvidenceHasZeroLifecycleAuthority(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	activateExactPane(t, store, "pan-alpha-review", "", "gen-legacy-unlink", "%9")
	event := exactPaneExitDirty()
	event.teardownKind = coremetadata.TeardownEventWindowUnlinked
	event.runtimePaneID = ""
	event.runtimeSessionID = "$1"
	event.runtimeWindowID = "@4"
	for i := range store.registry.Panes {
		if store.registry.Panes[i].Metadata.UID != "pan-alpha-review" {
			continue
		}
		store.registry.Panes[i].Status.Teardown = &coremetadata.PaneTeardownEvidence{
			SocketIdentity: event.target.label(), RuntimeSessionID: "$1", RuntimePaneID: "%9", RuntimeWindowID: "@4",
			WindowUID: "win-alpha-review", RootKind: coremetadata.KindProject, RootUID: "prj-alpha",
			Generation: "gen-legacy-unlink", Classification: coremetadata.TerminationNormal,
			ObservedAt: time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC),
		}
	}
	if err := store.registry.Validate(); err != nil {
		t.Fatalf("legacy teardown fixture: %v", err)
	}
	before := store.snapshot()
	allocationsBefore := len(store.newUIDs)
	inventory := &exactPaneExitInventory{
		uids: map[string]bool{}, windows: map[string]bool{}, windowSessions: map[string]int{},
	}
	result, err := reconcileLifecycle(context.Background(), event, inventory, store.store())
	if err != nil {
		t.Fatal(err)
	}
	if store.snapshot() != before || len(store.newUIDs) != allocationsBefore || result.transactions != 0 ||
		len(result.cascaded) != 0 || len(result.rootCascaded) != 0 || len(result.pending) != 0 ||
		inventory.prepares != 0 || inventory.cleanups != 0 || inventory.rollbacks != 0 || inventory.calls != 0 {
		t.Fatalf("legacy window-unlinked gained lifecycle authority: result=%+v allocations=%d->%d inventory=%+v",
			result, allocationsBefore, len(store.newUIDs), inventory)
	}
}

func TestLifecycleDeadPaneCleanupUsesExactRootSessionName(t *testing.T) {
	t.Parallel()
	registry := resourceFixtureRegistry(t)
	registry.ControlSessions = append(registry.ControlSessions, coremetadata.ControlSession{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindControlSession,
		Metadata: coremetadata.ObjectMeta{UID: "ctl-home", Name: "home", CreatedAt: resourceFixtureClock},
		Spec:     coremetadata.ControlSessionSpec{Session: "projmux-home"},
	})
	for _, tc := range []struct {
		name string
		root coremetadata.OwnerRef
		want string
	}{
		{name: "Project projection", root: coremetadata.OwnerRef{Kind: coremetadata.KindProject, UID: "prj-alpha"}, want: "alpha"},
		{name: "ControlSession spec", root: coremetadata.OwnerRef{Kind: coremetadata.KindControlSession, UID: "ctl-home"}, want: "projmux-home"},
		{name: "missing root", root: coremetadata.OwnerRef{Kind: coremetadata.KindProject, UID: "prj-missing"}, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := coremetadata.TeardownEvent{Chain: coremetadata.TeardownOwnerChain{
				PaneUID: "pane-dead", PaneHandle: "%9", WindowUID: "window-owner", WindowHandle: "@3",
				SessionHandle: "$1", RootKind: tc.root.Kind, RootUID: tc.root.UID,
			}}
			target := lifecycleDeadPaneTarget(event, lifecycleRootSessionName(registry, tc.root))
			if target.SessionName != tc.want || target.SessionID != "$1" || target.PaneID != "%9" {
				t.Fatalf("cleanup target = %+v, want session name %q with exact handles", target, tc.want)
			}
		})
	}
}

func TestUnprotectedLastShellExitHasZeroReplacementAuthority(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	activateExactPane(t, store, "pan-alpha-review", "", "gen-review", "%9")
	receipt := phase2NormalReceipt("pan-alpha-review", "", "gen-review")
	live := exitReconcileFixtureLiveExcept("pan-alpha-review")
	live["pan-gone-zsh"] = true
	inventory := &exactPaneExitInventory{uids: live, windowUID: "win-alpha-review"}
	result, err := reconcileLifecycle(context.Background(), exactPaneExitDirty(receipt), inventory, store.store())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.cascaded) != 0 || len(result.rootCascaded) != 0 || inventory.prepares != 0 || inventory.cleanups != 0 {
		t.Fatalf("unprotected shell gained replacement authority: result=%+v prepares=%d cleanups=%d", result, inventory.prepares, inventory.cleanups)
	}
	window, ok := store.registry.Window("win-alpha-review")
	if !ok || window.Metadata.Name != "review" {
		t.Fatalf("same Window identity/name did not survive: %+v", window)
	}
	if project, ok := store.registry.Project("prj-alpha"); !ok || project.Spec.PrimaryWindowRef != "win-alpha-main" {
		t.Fatalf("Project root/sibling changed: %+v", project)
	}
	if window.Spec.AnchorPaneRef != "pan-alpha-review" {
		t.Fatalf("unprotected shell changed anchor: %+v", window)
	}
}

func TestLastPaneReplacementFailureRestoresExactRegistry(t *testing.T) {
	t.Parallel()
	store, inventory, event := prepareLastBetaProjectCascade(t)
	before := store.snapshot()
	inventory.replacementErr = errors.New("injected live replacement failure")
	result, err := reconcileLifecycle(context.Background(), event, inventory, store.store())
	if err == nil || !strings.Contains(err.Error(), "injected live replacement failure") {
		t.Fatalf("replacement failure = %v", err)
	}
	if store.snapshot() != before || inventory.prepares != 1 || inventory.rollbacks != 1 || inventory.cleanups != 0 || !inventory.dead[event.receipts[0].PaneUID] {
		t.Fatalf("replacement failure result=%+v prepares=%d rollbacks=%d cleanups=%d changed Registry or dead Pane", result, inventory.prepares, inventory.rollbacks, inventory.cleanups)
	}
}

func TestLastPaneRegistryCommitFailureRollsBackExactLiveReplacement(t *testing.T) {
	t.Parallel()
	fixture, inventory, event := prepareLastBetaProjectCascade(t)
	before := fixture.snapshot()
	base := fixture.store()
	failing := *base
	failing.updateConvergent = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error) {
		working := fixture.registry.Clone()
		if err := fn(&working); err != nil {
			return coremetadata.Registry{}, false, err
		}
		if err := working.Validate(); err != nil {
			return coremetadata.Registry{}, false, err
		}
		return coremetadata.Registry{}, false, errors.New("injected Registry commit failure")
	}
	result, err := reconcileLifecycle(context.Background(), event, inventory, &failing)
	if err == nil || !strings.Contains(err.Error(), "injected Registry commit failure") {
		t.Fatalf("commit failure = %v", err)
	}
	if fixture.snapshot() != before || inventory.prepares != 1 || inventory.rollbacks != 1 || inventory.cleanups != 0 || !inventory.dead[event.receipts[0].PaneUID] {
		t.Fatalf("commit failure result=%+v prepares=%d rollbacks=%d cleanups=%d changed Registry or dead Pane", result, inventory.prepares, inventory.rollbacks, inventory.cleanups)
	}
}

func TestDeadPaneCleanupFailureKeepsCommittedReplacementGraph(t *testing.T) {
	t.Parallel()
	store, inventory, event := prepareLastBetaProjectCascade(t)
	deadPaneUID := event.receipts[0].PaneUID
	inventory.cleanupErr = errors.New("injected exact dead Pane cleanup failure")
	result, err := reconcileLifecycle(context.Background(), event, inventory, store.store())
	if err == nil || !strings.Contains(err.Error(), "exact dead Agent Pane cleanup remains") ||
		!strings.Contains(err.Error(), event.runtimePaneID) || !strings.Contains(err.Error(), deadPaneUID) ||
		!strings.Contains(err.Error(), "repeated lifecycle pass cannot infer ownership") ||
		!strings.Contains(err.Error(), "injected exact dead Pane cleanup failure") {
		t.Fatalf("cleanup failure = %v", err)
	}
	if len(result.cascaded) != 1 || inventory.prepares != 1 || inventory.cleanups != 1 || inventory.rollbacks != 0 {
		t.Fatalf("cleanup failure result=%+v prepares=%d cleanups=%d rollbacks=%d", result, inventory.prepares, inventory.cleanups, inventory.rollbacks)
	}
	if _, ok := store.registry.Pane(deadPaneUID); ok {
		t.Fatal("committed graph retained dead Agent Pane row")
	}
	window, ok := store.registry.Window("win-beta-main")
	if !ok {
		t.Fatal("retained Window disappeared")
	}
	anchor, ok := store.registry.Pane(window.Spec.AnchorPaneRef)
	if !ok || anchor.Spec.Role != coremetadata.PaneRoleShell {
		t.Fatalf("committed replacement anchor = %+v", anchor)
	}
	agent, ok := store.registry.Agent("agt-beta-codex")
	if !ok || agent.Status.Phase != coremetadata.PhaseOffline || agent.Status.PaneRef != "" {
		t.Fatalf("retained Agent = %+v", agent)
	}
	if !inventory.dead[deadPaneUID] || !inventory.uids[deadPaneUID] {
		t.Fatal("failed exact cleanup did not leave the owned dead runtime Pane for retry")
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
			dead := map[string]bool{}
			if test.agentUID != "" {
				live[test.paneUID] = true
				dead[test.paneUID] = true
			}
			inventory := &exactPaneExitInventory{uids: live, dead: dead, windowUID: "win-alpha-main"}
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
				if agent, ok := store.registry.Agent(test.agentUID); !ok || agent.Status.Phase != coremetadata.PhaseOffline || agent.Status.PaneRef != "" {
					t.Fatalf("clean provider Agent row was not retained Offline: %+v", agent)
				}
				if inventory.cleanups != 1 || inventory.prepares != 0 {
					t.Fatalf("dead sibling provider cleanups=%d prepares=%d, want 1/0", inventory.cleanups, inventory.prepares)
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
			if err != nil || repeat.transactions != 0 || store.snapshot() != settled || (test.agentUID != "" && inventory.cleanups != 1) {
				t.Fatalf("duplicate receipt result=%+v cleanups=%d err=%v", repeat, inventory.cleanups, err)
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
		{name: "abnormal dead retained", classification: coremetadata.TerminationAbnormal, source: coremetadata.TerminationSourceSupervisor, live: exitReconcileFixtureLiveExcept("pan-alpha-codex"), windowUID: "win-alpha-main"},
		{name: "killed dead retained", classification: coremetadata.TerminationKilled, source: coremetadata.TerminationSourceSupervisor, live: exitReconcileFixtureLiveExcept("pan-alpha-codex"), windowUID: "win-alpha-main"},
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
			dead := map[string]bool{}
			if strings.Contains(test.name, "dead retained") {
				test.live["pan-alpha-codex"] = true
				dead["pan-alpha-codex"] = true
			}
			inventory := &exactPaneExitInventory{uids: test.live, dead: dead, windowUID: test.windowUID, err: test.inventoryErr}
			result, err := reconcileLifecycle(context.Background(), exactPaneExitDirty(receipt), inventory, store.store())
			if err != nil {
				t.Fatalf("negative exact exit: %v", err)
			}
			if len(result.cascaded) != 0 {
				t.Fatalf("negative result created delete plan: %+v", result.cascaded)
			}
			if inventory.prepares != 0 || inventory.cleanups != 0 {
				t.Fatalf("negative authority prepared/cleaned runtime: prepares=%d cleanups=%d", inventory.prepares, inventory.cleanups)
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

func TestCleanDeadSiblingCleanupPreservesPriorAbnormalAgentPhase(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	activateExactPane(t, store, "pan-alpha-codex", "agt-alpha-codex", "gen-abnormal", "%8")
	abnormal := phase2NormalReceipt("pan-alpha-codex", "agt-alpha-codex", "gen-abnormal")
	abnormal.Classification = coremetadata.TerminationAbnormal
	code := 42
	abnormal.ExitCode = &code
	live := exitReconcileFixtureLiveExcept("pan-alpha-codex")
	live["pan-alpha-codex"] = true
	firstInventory := &exactPaneExitInventory{uids: live, dead: map[string]bool{"pan-alpha-codex": true}, windowUID: "win-alpha-main"}
	firstEvent := exactPaneExitDirty(abnormal)
	firstEvent.runtimePaneID = "%8"
	if _, err := reconcileLifecycle(context.Background(), firstEvent, firstInventory, store.store()); err != nil {
		t.Fatal(err)
	}
	failed, _ := store.registry.Agent("agt-alpha-codex")
	if failed.Status.Phase != coremetadata.PhaseFailed {
		t.Fatalf("abnormal Agent phase = %s", failed.Status.Phase)
	}
	created, err := store.mutator().CreateAgent(&store.registry, "win-alpha-main", coremetadata.CreateAgentOptions{Provider: "claude", OperationID: "clean-sibling-agent"})
	if err != nil {
		t.Fatal(err)
	}
	cleanPane, err := store.mutator().AttachAgentPane(&store.registry, created.Metadata.UID, coremetadata.BootstrapPane{CWD: "/srv/alpha"}, "clean-sibling-pane")
	if err != nil {
		t.Fatal(err)
	}
	activateExactPane(t, store, cleanPane.Metadata.UID, created.Metadata.UID, "gen-clean-sibling", "%9")
	clean := phase2NormalReceipt(cleanPane.Metadata.UID, created.Metadata.UID, "gen-clean-sibling")
	live[cleanPane.Metadata.UID] = true
	secondInventory := &exactPaneExitInventory{uids: live, dead: map[string]bool{"pan-alpha-codex": true, cleanPane.Metadata.UID: true}, windowUID: "win-alpha-main"}
	if _, err := reconcileLifecycle(context.Background(), exactPaneExitDirty(clean), secondInventory, store.store()); err != nil {
		t.Fatal(err)
	}
	failed, _ = store.registry.Agent("agt-alpha-codex")
	if failed.Status.Phase != coremetadata.PhaseFailed {
		t.Fatalf("clean sibling lifecycle changed abnormal Agent phase to %s", failed.Status.Phase)
	}
	if secondInventory.cleanups != 1 || secondInventory.prepares != 0 {
		t.Fatalf("clean sibling cleanup=%d prepares=%d", secondInventory.cleanups, secondInventory.prepares)
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
