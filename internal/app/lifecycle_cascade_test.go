package app

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/pins"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
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
	cleanupTarget  paneLiveDeleteTarget
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
	i.cleanupTarget = target
	if i.cleanupErr != nil {
		return i.cleanupErr
	}
	delete(i.uids, target.PaneUID)
	delete(i.dead, target.PaneUID)
	if target.EndsWindow {
		delete(i.windows, target.WindowUID)
		if i.windowSessions[target.SessionID] > 0 {
			i.windowSessions[target.SessionID]--
		}
	}
	return nil
}

type stableDeadPaneInventory struct {
	*exactPaneExitInventory
	observed []intmetadata.DeadPaneObservation
}

func (i *stableDeadPaneInventory) DeadPaneObservations(context.Context) ([]intmetadata.DeadPaneObservation, error) {
	if i.err != nil {
		return nil, i.err
	}
	return slices.Clone(i.observed), nil
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
		target:        tmuxTransport{Kind: tmuxSocketPath, Value: "/tmp/phase2-exact.sock", Source: tmuxSocketPathSource},
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
		windows: map[string]bool{"win-beta-main": true}, windowSessions: map[string]int{"$1": 1},
	}
	event := exactPaneExitDirty(receipt)
	return store, inventory, event
}

func TestLastPaneExitAndMatchingWindowUnlinkDeleteWindowAndRetainProject(t *testing.T) {
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
	event.pinStore = pinStore
	result, err := reconcileLifecycle(context.Background(), event, inventory, store.store())
	if err != nil {
		t.Fatalf("record last-Pane receipt: %v", err)
	}
	if len(result.pending) != 1 || !result.pending[0].Changed || len(result.rootCascaded) != 0 || result.transactions != 1 {
		t.Fatalf("pending Window lifecycle result = %+v", result)
	}
	if inventory.prepares != 0 || inventory.cleanups != 1 {
		t.Fatalf("replacement prepares=%d cleanups=%d, want 0/1", inventory.prepares, inventory.cleanups)
	}
	for _, uid := range []string{"proj-beta", "win-beta-main"} {
		if _, ok := registryUIDs(store.registry)[uid]; !ok {
			t.Fatalf("retained identity %s disappeared", uid)
		}
	}
	if pane, ok := store.registry.Pane(deadPaneUID); !ok || pane.Status.Teardown == nil {
		t.Fatal("causal Pane receipt or retained subtree disappeared before unlink")
	}
	unlinked := event
	unlinked.teardownKind = coremetadata.TeardownEventWindowUnlinked
	unlinked.runtimePaneID = ""
	unlinked.runtimeSessionID = "$1"
	unlinked.runtimeWindowID = "@4"
	closed, err := reconcileLifecycle(context.Background(), unlinked, inventory, store.store())
	if err != nil || len(closed.rootCascaded) != 1 || closed.rootCascaded[0].DeletedWindows != 1 ||
		closed.rootCascaded[0].DeletedAgents != 1 {
		t.Fatalf("matching Window close = %+v, %v", closed, err)
	}
	project, ok := store.registry.Project("proj-beta")
	if !ok || project.Spec.Root != "/srv/beta" || project.Spec.PrimaryWindowRef != "" ||
		project.Status.Session == nil || project.Status.Session.Live || len(store.registry.WindowsOf("proj-beta")) != 0 {
		t.Fatalf("retained zero-Window Project = %+v", project)
	}
	for _, uid := range []string{"win-beta-main", deadPaneUID, "agt-beta-codex"} {
		if _, ok := registryUIDs(store.registry)[uid]; ok {
			t.Fatalf("target Window subtree residual %s survived", uid)
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
	if pinStore.saves != 0 || !reflect.DeepEqual(pinStore.set, pins.Set{Format: pins.FormatTyped, Pins: []pins.Pin{other, managed}}) {
		t.Fatalf("automatic retention touched pins = %+v, saves=%d", pinStore.set, pinStore.saves)
	}
	settled := store.snapshot()
	if repeat, err := reconcileLifecycle(context.Background(), unlinked, inventory, store.store()); err != nil || repeat.transactions != 0 || store.snapshot() != settled {
		t.Fatalf("duplicate unlink = %+v, %v", repeat, err)
	}
}

func TestAutomaticWindowClosureNeverUsesProjectPinAuthority(t *testing.T) {
	t.Parallel()
	managed, _ := pins.ProjectPin("proj-beta")
	other, _ := pins.CandidatePin("/srv/other")
	initial := pins.Set{Format: pins.FormatTyped, Pins: []pins.Pin{other, managed}}
	store, inventory, event := prepareLastBetaProjectCascade(t)
	pinStore := &lifecyclePinStore{set: initial, loadErr: errors.New("must not load"), saveErr: errors.New("must not save")}
	event.pinStore = pinStore
	result, err := reconcileLifecycle(context.Background(), event, inventory, store.store())
	if err != nil || len(result.pending) != 1 {
		t.Fatalf("pending Window lifecycle = %+v, %v", result, err)
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

func TestWindowUnlinkedConsumesStoredExactTeardownEvidenceOnlyForTargetWindow(t *testing.T) {
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
			SocketIdentity: event.target.Label(), RuntimeSessionID: "$1", RuntimePaneID: "%9", RuntimeWindowID: "@4",
			WindowUID: "win-alpha-review", RootKind: coremetadata.KindProject, RootUID: "prj-alpha",
			Generation: "gen-legacy-unlink", Classification: coremetadata.TerminationNormal,
			ObservedAt: time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC),
		}
	}
	if err := store.registry.Validate(); err != nil {
		t.Fatalf("legacy teardown fixture: %v", err)
	}
	allocationsBefore := len(store.newUIDs)
	inventory := &exactPaneExitInventory{
		uids: map[string]bool{}, windows: map[string]bool{}, windowSessions: map[string]int{},
	}
	result, err := reconcileLifecycle(context.Background(), event, inventory, store.store())
	if err != nil {
		t.Fatal(err)
	}
	if len(store.newUIDs) != allocationsBefore || result.transactions != 1 ||
		len(result.cascaded) != 0 || len(result.rootCascaded) != 1 || len(result.pending) != 0 ||
		inventory.prepares != 0 || inventory.cleanups != 0 || inventory.rollbacks != 0 || inventory.calls != 2 {
		t.Fatalf("exact window-unlinked result=%+v allocations=%d->%d inventory=%+v",
			result, allocationsBefore, len(store.newUIDs), inventory)
	}
	if _, ok := store.registry.Window("win-alpha-review"); ok {
		t.Fatal("exact target Window survived")
	}
	project, ok := store.registry.Project("prj-alpha")
	if !ok || project.Spec.PrimaryWindowRef != "win-alpha-main" {
		t.Fatalf("target closure changed Project/sibling primary = %+v", project)
	}
	if _, ok := store.registry.Window("win-alpha-main"); !ok {
		t.Fatal("sibling Window disappeared")
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
			target := lifecycleDeadPaneTarget(event, lifecycleRootSessionName(registry, tc.root), coremetadata.Pane{})
			if target.SessionName != tc.want || target.SessionID != "$1" || target.PaneID != "%9" {
				t.Fatalf("cleanup target = %+v, want session name %q with exact handles", target, tc.want)
			}
		})
	}
}

func TestLastShellExitStoresPendingEvidenceWithoutReplacementAuthority(t *testing.T) {
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
	if len(result.pending) != 1 || len(result.cascaded) != 0 || len(result.rootCascaded) != 0 || inventory.prepares != 0 || inventory.cleanups != 0 {
		t.Fatalf("last shell pending result=%+v prepares=%d cleanups=%d", result, inventory.prepares, inventory.cleanups)
	}
	window, ok := store.registry.Window("win-alpha-review")
	if !ok || window.Metadata.Name != "review" {
		t.Fatalf("same Window identity/name did not survive: %+v", window)
	}
	if project, ok := store.registry.Project("prj-alpha"); !ok || project.Spec.PrimaryWindowRef != "win-alpha-main" {
		t.Fatalf("Project root/sibling changed: %+v", project)
	}
	if window.Spec.AnchorPaneRef != "pan-alpha-review" {
		t.Fatalf("pending shell changed anchor: %+v", window)
	}
	pane, _ := store.registry.Pane("pan-alpha-review")
	if pane.Status.Teardown == nil {
		t.Fatal("last shell did not retain exact pending teardown evidence")
	}
}

func TestLastPaneClosureDoesNotInvokeReplacementPreparation(t *testing.T) {
	t.Parallel()
	store, inventory, event := prepareLastBetaProjectCascade(t)
	inventory.replacementErr = errors.New("injected live replacement failure")
	result, err := reconcileLifecycle(context.Background(), event, inventory, store.store())
	if err != nil {
		t.Fatalf("replacement seam affected closure: %v", err)
	}
	if len(result.pending) != 1 || inventory.prepares != 0 || inventory.rollbacks != 0 || inventory.cleanups != 1 || inventory.dead[event.receipts[0].PaneUID] {
		t.Fatalf("closure result=%+v prepares=%d rollbacks=%d cleanups=%d", result, inventory.prepares, inventory.rollbacks, inventory.cleanups)
	}
}

func TestLastPaneRegistryCommitFailureLeavesReceiptUncommittedAfterExactCleanup(t *testing.T) {
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
	if fixture.snapshot() != before || inventory.prepares != 0 || inventory.rollbacks != 0 || inventory.cleanups != 1 || inventory.dead[event.receipts[0].PaneUID] {
		t.Fatalf("commit failure result=%+v prepares=%d rollbacks=%d cleanups=%d changed Registry or dead Pane", result, inventory.prepares, inventory.rollbacks, inventory.cleanups)
	}
}

func TestDeadPaneCleanupFailureExposesTypedRetryAndNextPassConverges(t *testing.T) {
	t.Parallel()
	store, inventory, event := prepareLastBetaProjectCascade(t)
	deadPaneUID := event.receipts[0].PaneUID
	before := store.snapshot()
	inventory.cleanupErr = errors.New("injected exact dead Pane cleanup failure")
	result, err := reconcileLifecycle(context.Background(), event, inventory, store.store())
	var retryErr *lifecycleCleanupRetryError
	if err == nil || !errors.As(err, &retryErr) ||
		retryErr.Reason != coremetadata.TeardownReasonDeadPaneCleanupRetry ||
		retryErr.Target.PaneID != event.runtimePaneID || retryErr.Target.PaneUID != deadPaneUID ||
		!strings.Contains(err.Error(), string(coremetadata.TeardownReasonDeadPaneCleanupRetry)) ||
		!strings.Contains(err.Error(), "injected exact dead Pane cleanup failure") {
		t.Fatalf("cleanup failure = %v", err)
	}
	if len(result.cascaded) != 0 || len(result.pending) != 0 || inventory.prepares != 0 || inventory.cleanups != 1 || inventory.rollbacks != 0 {
		t.Fatalf("cleanup failure result=%+v prepares=%d cleanups=%d rollbacks=%d", result, inventory.prepares, inventory.cleanups, inventory.rollbacks)
	}
	if store.snapshot() != before {
		t.Fatal("cleanup failure committed the desired Registry before exact dead runtime cleanup")
	}
	if !inventory.dead[deadPaneUID] || !inventory.uids[deadPaneUID] {
		t.Fatal("failed exact cleanup did not leave the owned dead runtime Pane for retry")
	}

	inventory.cleanupErr = nil
	second, err := reconcileLifecycle(context.Background(), event, inventory, store.store())
	if err != nil {
		t.Fatalf("strict cleanup retry: %v", err)
	}
	if len(second.pending) != 1 || len(second.cascaded) != 0 || inventory.prepares != 0 || inventory.cleanups != 2 || inventory.rollbacks != 0 {
		t.Fatalf("retry result=%+v prepares=%d cleanups=%d rollbacks=%d", second, inventory.prepares, inventory.cleanups, inventory.rollbacks)
	}
	if pane, ok := store.registry.Pane(deadPaneUID); !ok || pane.Status.Teardown == nil || inventory.dead[deadPaneUID] || inventory.uids[deadPaneUID] {
		t.Fatal("successful retry did not retain only the Registry receipt while removing the dead runtime Pane")
	}
	if inventory.windows["win-beta-main"] || inventory.windowSessions["$1"] != 0 {
		t.Fatalf("exact cleanup did not observe Window unlink: windows=%v sessions=%v", inventory.windows, inventory.windowSessions)
	}
	unlinked := event
	unlinked.teardownKind = coremetadata.TeardownEventWindowUnlinked
	unlinked.runtimePaneID = ""
	unlinked.runtimeSessionID = "$1"
	unlinked.runtimeWindowID = "@4"
	closed, err := reconcileLifecycle(context.Background(), unlinked, inventory, store.store())
	if err != nil || len(closed.rootCascaded) != 1 {
		t.Fatalf("matching unlink after cleanup retry = %+v, %v", closed, err)
	}
	if _, ok := store.registry.Window("win-beta-main"); ok {
		t.Fatal("matching unlink left target Window")
	}
	project, ok := store.registry.Project("proj-beta")
	if !ok || project.Spec.PrimaryWindowRef != "" {
		t.Fatalf("matching unlink changed retained Project = %+v", project)
	}
	settled := store.snapshot()
	repeat, err := reconcileLifecycle(context.Background(), unlinked, inventory, store.store())
	if err != nil || repeat.transactions != 0 || store.snapshot() != settled {
		t.Fatalf("retry repeat = %+v, %v", repeat, err)
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

func TestReleasedSameGenerationDeadPaneRetryConvergesAndPreservesSessionRef(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	activateExactPane(t, store, "pan-alpha-codex", "agt-alpha-codex", "gen-released", "%9")
	wantSession := resumeFixtureRef(resourceFixtureClock)
	setFixtureSessionRef(t, store, "agt-alpha-codex", wantSession)
	receipt := phase2NormalReceipt("pan-alpha-codex", "agt-alpha-codex", "gen-released")
	if _, err := store.mutator().RecordTermination(&store.registry, receipt); err != nil {
		t.Fatalf("RecordTermination: %v", err)
	}
	projection, err := store.mutator().ProjectTermination(&store.registry, coremetadata.TerminationProjectionInput{
		PaneUID: "pan-alpha-codex", Generation: "gen-released", ObservedAt: resourceFixtureClock.Add(time.Minute),
	})
	if err != nil || !projection.Changed {
		t.Fatalf("release-first projection = %+v, %v", projection, err)
	}
	released, _ := store.registry.Agent("agt-alpha-codex")
	if released.Status.Phase != coremetadata.PhaseOffline || released.Status.PaneRef != "" ||
		!released.Status.SessionRef.SameConversation(wantSession) {
		t.Fatalf("released Agent = %+v", released.Status)
	}

	live := exitReconcileFixtureLiveExcept("pan-alpha-codex")
	live["pan-alpha-codex"] = true
	inventory := &exactPaneExitInventory{
		uids: live, dead: map[string]bool{"pan-alpha-codex": true}, windowUID: "win-alpha-main",
	}
	result, err := reconcileLifecycle(context.Background(), exactPaneExitDirty(receipt), inventory, store.store())
	if err != nil {
		t.Fatalf("released same-generation retry: %v", err)
	}
	if len(result.cascaded) != 1 || inventory.cleanups != 1 || inventory.prepares != 0 {
		t.Fatalf("released retry result=%+v inventory=%+v", result, inventory)
	}
	if _, ok := store.registry.Pane("pan-alpha-codex"); ok || inventory.dead["pan-alpha-codex"] || inventory.uids["pan-alpha-codex"] {
		t.Fatal("released retry left a Registry or tmux dead Pane residual")
	}
	retained, ok := store.registry.Agent("agt-alpha-codex")
	if !ok || retained.Status.Phase != coremetadata.PhaseOffline || retained.Status.PaneRef != "" ||
		!retained.Status.SessionRef.SameConversation(wantSession) {
		t.Fatalf("retained Agent after retry = %+v", retained)
	}
}

func TestExactDeadPaneUIDDisambiguatesAReusedRuntimeHandle(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	activateExactPane(t, store, "pan-alpha-codex", "agt-alpha-codex", "gen-current", "%9")
	currentReceipt := phase2NormalReceipt("pan-alpha-codex", "agt-alpha-codex", "gen-current")

	stale, err := store.mutator().AttachAgentPane(&store.registry, "agt-beta-codex",
		coremetadata.BootstrapPane{CWD: "/srv/beta"}, "attach-stale-agent")
	if err != nil {
		t.Fatalf("attach stale Agent Pane: %v", err)
	}
	activateExactPane(t, store, stale.Metadata.UID, "agt-beta-codex", "gen-stale", "%9")
	staleReceipt := phase2NormalReceipt(stale.Metadata.UID, "agt-beta-codex", "gen-stale")
	if _, err := store.mutator().RecordTermination(&store.registry, staleReceipt); err != nil {
		t.Fatalf("record stale termination: %v", err)
	}
	if projection, err := store.mutator().ProjectTermination(&store.registry, coremetadata.TerminationProjectionInput{
		PaneUID: stale.Metadata.UID, Generation: "gen-stale", ObservedAt: resourceFixtureClock.Add(time.Minute),
	}); err != nil || !projection.Changed {
		t.Fatalf("release stale Pane = %+v, %v", projection, err)
	}
	stalePaneBefore, _ := store.registry.Pane(stale.Metadata.UID)
	staleAgentBefore, _ := store.registry.Agent("agt-beta-codex")

	live := exitReconcileFixtureLiveExcept("pan-alpha-codex")
	delete(live, stale.Metadata.UID)
	live["pan-alpha-codex"] = true
	inventory := &exactPaneExitInventory{
		uids: live, dead: map[string]bool{"pan-alpha-codex": true}, windowUID: "win-alpha-main",
	}
	result, err := reconcileLifecycle(context.Background(), exactPaneExitDirty(currentReceipt), inventory, store.store())
	if err != nil || len(result.cascaded) != 1 || inventory.cleanups != 1 {
		t.Fatalf("reused-handle convergence result=%+v inventory=%+v err=%v", result, inventory, err)
	}
	if _, ok := store.registry.Pane("pan-alpha-codex"); ok || inventory.dead["pan-alpha-codex"] {
		t.Fatal("current exact dead Pane survived reused-handle convergence")
	}
	stalePaneAfter, paneOK := store.registry.Pane(stale.Metadata.UID)
	staleAgentAfter, agentOK := store.registry.Agent("agt-beta-codex")
	if !paneOK || !agentOK || !reflect.DeepEqual(stalePaneAfter, stalePaneBefore) || !reflect.DeepEqual(staleAgentAfter, staleAgentBefore) {
		t.Fatalf("historical Pane/Agent changed: pane=%+v agent=%+v", stalePaneAfter, staleAgentAfter)
	}
}

func TestC1C2CurrentUIDContainmentRebindsCachedLocatorsForRetainedDeadPaneCleanup(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	activateExactPane(t, store, "pan-alpha-codex", "agt-alpha-codex", "gen-current", "%6")
	window, _ := store.registry.Window("win-alpha-main")
	window.Status.RuntimeSessionID, window.Status.RuntimeID = "$2", "@6"
	receipt := phase2NormalReceipt("pan-alpha-codex", "agt-alpha-codex", "gen-current")
	live := exitReconcileFixtureLiveExcept("pan-alpha-codex")
	live["pan-alpha-codex"] = true
	base := &exactPaneExitInventory{
		uids: live, dead: map[string]bool{"pan-alpha-codex": true}, windowUID: "win-alpha-main",
	}
	inventory := &stableDeadPaneInventory{exactPaneExitInventory: base, observed: []intmetadata.DeadPaneObservation{{
		SessionID: "$2", SessionName: "alpha", WindowID: "@2", PaneID: "%2",
		ProjectUID: "prj-alpha", WindowUID: "win-alpha-main", PaneUID: "pan-alpha-codex",
		OwnerKind: string(coremetadata.KindAgent), OwnerUID: "agt-alpha-codex", AgentUID: "agt-alpha-codex",
		PaneRole: string(coremetadata.PaneRoleAgent),
	}}}
	event := exactPaneExitDirty(receipt)
	event.runtimePaneID = "%2"
	event.runtimeSessionID = "$2"

	result, err := reconcileLifecycle(context.Background(), event, inventory, store.store())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.cascaded) != 1 || base.cleanups != 1 || base.cleanupTarget.SessionID != "$2" ||
		base.cleanupTarget.WindowID != "@2" || base.cleanupTarget.PaneID != "%2" {
		t.Fatalf("rebound cleanup result=%+v target=%+v", result, base.cleanupTarget)
	}
	if _, ok := store.registry.Pane("pan-alpha-codex"); ok {
		t.Fatal("current same-UID dead Pane row survived")
	}
	agent, _ := store.registry.Agent("agt-alpha-codex")
	if agent.Status.Phase != coremetadata.PhaseOffline || agent.Status.PaneRef != "" {
		t.Fatalf("resumable Agent=%+v", agent.Status)
	}
}

func TestC2StableAuthorityConflictsWriteZeroAndReturnRetryErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mutate    func(*intmetadata.DeadPaneObservation, *lifecycleDirtyEvent)
		duplicate bool
	}{
		{name: "missing Pane UID", mutate: func(observed *intmetadata.DeadPaneObservation, _ *lifecycleDirtyEvent) {
			observed.PaneUID = ""
		}},
		{name: "foreign Pane UID", mutate: func(observed *intmetadata.DeadPaneObservation, _ *lifecycleDirtyEvent) {
			observed.PaneUID = "pan-foreign"
		}},
		{name: "foreign Pane ownerRef", mutate: func(observed *intmetadata.DeadPaneObservation, _ *lifecycleDirtyEvent) {
			observed.OwnerUID = "agt-foreign"
		}},
		{name: "foreign Agent UID", mutate: func(observed *intmetadata.DeadPaneObservation, _ *lifecycleDirtyEvent) {
			observed.AgentUID = "agt-foreign"
		}},
		{name: "foreign Window UID", mutate: func(observed *intmetadata.DeadPaneObservation, _ *lifecycleDirtyEvent) {
			observed.WindowUID = "win-foreign"
		}},
		{name: "foreign Project UID", mutate: func(observed *intmetadata.DeadPaneObservation, _ *lifecycleDirtyEvent) {
			observed.ProjectUID = "prj-foreign"
		}},
		{name: "stale activation generation", mutate: func(_ *intmetadata.DeadPaneObservation, event *lifecycleDirtyEvent) {
			event.generation = "gen-stale"
		}},
		{name: "ambiguous duplicate current locator", duplicate: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			store := newFakeResourceStore(t)
			activateExactPane(t, store, "pan-alpha-codex", "agt-alpha-codex", "gen-current", "%9")
			receipt := phase2NormalReceipt("pan-alpha-codex", "agt-alpha-codex", "gen-current")
			live := exitReconcileFixtureLiveExcept("pan-alpha-codex")
			live["pan-alpha-codex"] = true
			base := &exactPaneExitInventory{
				uids: live, dead: map[string]bool{"pan-alpha-codex": true}, windowUID: "win-alpha-main",
			}
			observed := intmetadata.DeadPaneObservation{
				SessionID: "$1", SessionName: "alpha", WindowID: "@4", PaneID: "%9",
				ProjectUID: "prj-alpha", WindowUID: "win-alpha-main", PaneUID: "pan-alpha-codex",
				OwnerKind: string(coremetadata.KindAgent), OwnerUID: "agt-alpha-codex", AgentUID: "agt-alpha-codex",
				PaneRole: string(coremetadata.PaneRoleAgent),
			}
			event := exactPaneExitDirty(receipt)
			if test.mutate != nil {
				test.mutate(&observed, &event)
			}
			observations := []intmetadata.DeadPaneObservation{observed}
			if test.duplicate {
				observations = append(observations, observed)
			}
			inventory := &stableDeadPaneInventory{exactPaneExitInventory: base, observed: observations}
			before := store.registry.Clone()
			transactionsBefore, writesBefore := store.transactions, store.writes

			result, err := reconcileLifecycle(context.Background(), event, inventory, store.store())
			if err == nil || !strings.Contains(err.Error(), "stable dead Pane authority conflict") {
				t.Fatalf("conflict error = %v", err)
			}
			if result.transactions != 0 || store.transactions != transactionsBefore || store.writes != writesBefore || base.cleanups != 0 {
				t.Fatalf("conflict wrote state: result=%+v transactions=%d writes=%d cleanups=%d",
					result, store.transactions-transactionsBefore, store.writes-writesBefore, base.cleanups)
			}
			if !reflect.DeepEqual(store.registry, before) || !base.dead["pan-alpha-codex"] || !base.uids["pan-alpha-codex"] {
				t.Fatal("conflict changed the Registry or retained dead-Pane retry evidence")
			}
		})
	}
}

func TestC2StableAuthorityConflictRemainsInDurableControllerRetryQueue(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	activateExactPane(t, store, "pan-alpha-codex", "agt-alpha-codex", "gen-current", "%9")
	receipt := phase2NormalReceipt("pan-alpha-codex", "agt-alpha-codex", "gen-current")
	live := exitReconcileFixtureLiveExcept("pan-alpha-codex")
	live["pan-alpha-codex"] = true
	base := &exactPaneExitInventory{
		uids: live, dead: map[string]bool{"pan-alpha-codex": true}, windowUID: "win-alpha-main",
	}
	inventory := &stableDeadPaneInventory{exactPaneExitInventory: base, observed: []intmetadata.DeadPaneObservation{{
		SessionID: "$1", SessionName: "alpha", WindowID: "@4", PaneID: "%9",
		ProjectUID: "prj-alpha", WindowUID: "win-alpha-main", PaneUID: "pan-alpha-codex",
		OwnerKind: string(coremetadata.KindAgent), OwnerUID: "agt-foreign", AgentUID: "agt-alpha-codex",
		PaneRole: string(coremetadata.PaneRoleAgent),
	}}}
	target, err := tmuxSocketPathTarget(filepath.Join(t.TempDir(), "stable-conflict.sock"))
	if err != nil {
		t.Fatal(err)
	}
	runner := &controllerTriggerRunner{
		runner: &routedTmuxRunner{}, store: store.store(), events: controllerEventLog{dir: t.TempDir()},
		pass: func(ctx context.Context, trigger controllerTrigger) (controllerPassResult, error) {
			dirty := exactPaneExitDirty(receipt)
			dirty.target = trigger.target
			dirty.runtimePaneID = trigger.hookPane
			result, reconcileErr := reconcileLifecycle(ctx, dirty, inventory, store.store())
			return controllerPassResult{residualExits: result.changed()}, reconcileErr
		},
	}
	before := store.registry.Clone()
	transactionsBefore, writesBefore := store.transactions, store.writes

	_, err = runner.run(context.Background(), controllerTrigger{
		reason: controllerTriggerPaneExited, target: target, hookPane: "%9",
	})
	if err == nil || !strings.Contains(err.Error(), "stable dead Pane authority conflict") {
		t.Fatalf("terminal controller conflict = %v", err)
	}
	queued, drainErr := runner.events.drain(target)
	if drainErr != nil || len(queued) != 1 || queued[0].retry != controllerTriggerMaxRetries || queued[0].hookPane != "%9" {
		t.Fatalf("durable terminal retry = %+v, err=%v", queued, drainErr)
	}
	if store.transactions != transactionsBefore || store.writes != writesBefore || base.cleanups != 0 ||
		!reflect.DeepEqual(store.registry, before) || !base.dead["pan-alpha-codex"] || !base.uids["pan-alpha-codex"] {
		t.Fatalf("controller conflict changed authority: transactions=%d writes=%d cleanups=%d",
			store.transactions-transactionsBefore, store.writes-writesBefore, base.cleanups)
	}
}

func TestSimultaneousCleanAgentExitsConvergeWithoutDeadOrMirroredResiduals(t *testing.T) {
	t.Parallel()

	orders := [][]string{{"%9", "%10"}, {"%10", "%9"}}
	var want string
	for orderIndex, order := range orders {
		store := newFakeResourceStore(t)
		activateExactPane(t, store, "pan-alpha-codex", "agt-alpha-codex", "gen-one", "%9")
		secondAgent, err := store.mutator().CreateAgent(&store.registry, "win-alpha-main",
			coremetadata.CreateAgentOptions{Provider: "claude", OperationID: "simultaneous-agent"})
		if err != nil {
			t.Fatal(err)
		}
		secondPane, err := store.mutator().AttachAgentPane(&store.registry, secondAgent.Metadata.UID,
			coremetadata.BootstrapPane{CWD: "/srv/alpha"}, "simultaneous-pane")
		if err != nil {
			t.Fatal(err)
		}
		activateExactPane(t, store, secondPane.Metadata.UID, secondAgent.Metadata.UID, "gen-two", "%10")

		receipts := map[string]coremetadata.TerminationEvidence{
			"%9":  phase2NormalReceipt("pan-alpha-codex", "agt-alpha-codex", "gen-one"),
			"%10": phase2NormalReceipt(secondPane.Metadata.UID, secondAgent.Metadata.UID, "gen-two"),
		}
		live := make(map[string]bool, len(store.registry.Panes))
		for _, pane := range store.registry.Panes {
			live[pane.Metadata.UID] = true
		}
		inventory := &exactPaneExitInventory{uids: live, dead: map[string]bool{
			"pan-alpha-codex": true, secondPane.Metadata.UID: true,
		}, windowUID: "win-alpha-main"}
		for _, runtimeID := range order {
			event := exactPaneExitDirty(receipts[runtimeID])
			event.runtimePaneID = runtimeID
			result, err := reconcileLifecycle(context.Background(), event, inventory, store.store())
			if err != nil || len(result.cascaded) != 1 {
				t.Fatalf("order %d runtime %s result=%+v err=%v", orderIndex, runtimeID, result, err)
			}
		}
		for runtimeID, receipt := range receipts {
			if inventory.dead[receipt.PaneUID] || inventory.uids[receipt.PaneUID] {
				t.Fatalf("order %d runtime %s left dead/mirrored residual", orderIndex, runtimeID)
			}
			if _, ok := store.registry.Pane(receipt.PaneUID); ok {
				t.Fatalf("order %d Pane %s survived", orderIndex, receipt.PaneUID)
			}
			agent, ok := store.registry.Agent(receipt.AgentUID)
			if !ok || agent.Status.Phase != coremetadata.PhaseOffline || agent.Status.PaneRef != "" {
				t.Fatalf("order %d Agent %s = %+v", orderIndex, receipt.AgentUID, agent)
			}
		}
		if _, ok := store.registry.Pane("pan-alpha-zsh"); !ok {
			t.Fatal("simultaneous exits changed the sibling shell")
		}
		if inventory.cleanups != 2 || inventory.prepares != 0 {
			t.Fatalf("order %d cleanups=%d prepares=%d", orderIndex, inventory.cleanups, inventory.prepares)
		}
		if orderIndex == 0 {
			want = store.snapshot()
		} else if got := store.snapshot(); got != want {
			t.Fatalf("simultaneous event order changed final Registry:\nwant:\n%s\ngot:\n%s", want, got)
		}
		settled := store.snapshot()
		for runtimeID, receipt := range receipts {
			event := exactPaneExitDirty(receipt)
			event.runtimePaneID = runtimeID
			result, err := reconcileLifecycle(context.Background(), event, inventory, store.store())
			if err != nil || result.transactions != 0 || store.snapshot() != settled {
				t.Fatalf("order %d repeat %s = %+v, %v", orderIndex, runtimeID, result, err)
			}
		}
	}
}

func TestCoalescedCleanAgentExitEventsConvergeBothExactDeadPanes(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	activateExactPane(t, store, "pan-alpha-codex", "agt-alpha-codex", "gen-coalesced-one", "%9")
	secondAgent, err := store.mutator().CreateAgent(&store.registry, "win-alpha-main",
		coremetadata.CreateAgentOptions{Provider: "claude", OperationID: "coalesced-agent"})
	if err != nil {
		t.Fatal(err)
	}
	secondPane, err := store.mutator().AttachAgentPane(&store.registry, secondAgent.Metadata.UID,
		coremetadata.BootstrapPane{CWD: "/srv/alpha"}, "coalesced-pane")
	if err != nil {
		t.Fatal(err)
	}
	activateExactPane(t, store, secondPane.Metadata.UID, secondAgent.Metadata.UID, "gen-coalesced-two", "%10")
	receipts := map[string]coremetadata.TerminationEvidence{
		"%9":  phase2NormalReceipt("pan-alpha-codex", "agt-alpha-codex", "gen-coalesced-one"),
		"%10": phase2NormalReceipt(secondPane.Metadata.UID, secondAgent.Metadata.UID, "gen-coalesced-two"),
	}
	live := make(map[string]bool, len(store.registry.Panes))
	for _, pane := range store.registry.Panes {
		live[pane.Metadata.UID] = true
	}
	inventory := &exactPaneExitInventory{uids: live, dead: map[string]bool{
		"pan-alpha-codex": true, secondPane.Metadata.UID: true,
	}, windowUID: "win-alpha-main"}
	target, err := tmuxSocketPathTarget(filepath.Join(t.TempDir(), "coalesced.sock"))
	if err != nil {
		t.Fatal(err)
	}
	events := controllerEventLog{dir: t.TempDir()}
	var runner *controllerTriggerRunner
	passNumber := 0
	var losing controllerTriggerOutcome
	runner = &controllerTriggerRunner{
		runner: &routedTmuxRunner{}, store: store.store(), events: events,
		pass: func(ctx context.Context, trigger controllerTrigger) (controllerPassResult, error) {
			passNumber++
			if passNumber == 1 {
				var nestedErr error
				losing, nestedErr = runner.run(ctx, controllerTrigger{
					reason: controllerTriggerPaneExited, target: target, hookPane: "%10", hookWindow: "@4",
				})
				if nestedErr != nil {
					return controllerPassResult{}, nestedErr
				}
			}
			if trigger.fullReobserve {
				return controllerPassResult{}, nil
			}
			receipt, ok := receipts[trigger.hookPane]
			if !ok {
				return controllerPassResult{}, errors.New("coalesced pass lost exact hook Pane")
			}
			dirty := exactPaneExitDirty(receipt)
			dirty.target = trigger.target
			dirty.runtimePaneID = trigger.hookPane
			result, reconcileErr := reconcileLifecycle(ctx, dirty, inventory, store.store())
			if reconcileErr != nil {
				return controllerPassResult{}, reconcileErr
			}
			return controllerPassResult{residualExits: result.changed()}, nil
		},
	}
	outcome, err := runner.run(context.Background(), controllerTrigger{
		reason: controllerTriggerPaneExited, target: target, hookPane: "%9", hookWindow: "@4",
	})
	if err != nil {
		t.Fatal(err)
	}
	if losing.passes != 0 || !strings.Contains(losing.deferred, "another controller worker holds") {
		t.Fatalf("coalesced loser = %s", losing.describe())
	}
	if outcome.events != 2 || outcome.passes != 3 || outcome.changed != 2 || !outcome.converged {
		t.Fatalf("coalesced exact exits = %s", outcome.describe())
	}
	for _, receipt := range receipts {
		if _, ok := store.registry.Pane(receipt.PaneUID); ok || inventory.dead[receipt.PaneUID] || inventory.uids[receipt.PaneUID] {
			t.Fatalf("coalesced Pane %s left a residual", receipt.PaneUID)
		}
		agent, ok := store.registry.Agent(receipt.AgentUID)
		if !ok || agent.Status.Phase != coremetadata.PhaseOffline || agent.Status.PaneRef != "" {
			t.Fatalf("coalesced Agent %s = %+v", receipt.AgentUID, agent)
		}
	}
	if _, ok := store.registry.Pane("pan-alpha-zsh"); !ok || inventory.cleanups != 2 {
		t.Fatalf("coalesced exits changed sibling or cleanup count: inventory=%+v", inventory)
	}
}

func TestControllerRetriesTypedDeadPaneCleanupReasonAndConvergesNextPass(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	activateExactPane(t, store, "pan-alpha-codex", "agt-alpha-codex", "gen-controller-retry", "%9")
	receipt := phase2NormalReceipt("pan-alpha-codex", "agt-alpha-codex", "gen-controller-retry")
	live := exitReconcileFixtureLiveExcept("pan-alpha-codex")
	live["pan-alpha-codex"] = true
	inventory := &exactPaneExitInventory{
		uids: live, dead: map[string]bool{"pan-alpha-codex": true}, windowUID: "win-alpha-main",
		cleanupErr: errors.New("injected first cleanup failure"),
	}
	target, err := tmuxSocketPathTarget(filepath.Join(t.TempDir(), "retry.sock"))
	if err != nil {
		t.Fatal(err)
	}
	runner := &controllerTriggerRunner{
		runner: &routedTmuxRunner{}, store: store.store(), events: controllerEventLog{dir: t.TempDir()},
		pass: func(ctx context.Context, trigger controllerTrigger) (controllerPassResult, error) {
			if trigger.fullReobserve {
				return controllerPassResult{}, nil
			}
			dirty := exactPaneExitDirty(receipt)
			dirty.target = trigger.target
			dirty.runtimePaneID = trigger.hookPane
			result, reconcileErr := reconcileLifecycle(ctx, dirty, inventory, store.store())
			if reconcileErr != nil {
				var retryErr *lifecycleCleanupRetryError
				if errors.As(reconcileErr, &retryErr) {
					inventory.cleanupErr = nil
				}
				return controllerPassResult{}, reconcileErr
			}
			return controllerPassResult{residualExits: result.changed()}, nil
		},
	}
	outcome, err := runner.run(context.Background(), controllerTrigger{
		reason: controllerTriggerPaneExited, target: target, hookPane: "%9",
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.retryReason != coremetadata.TeardownReasonDeadPaneCleanupRetry ||
		!strings.Contains(outcome.describe(), "retry: "+string(coremetadata.TeardownReasonDeadPaneCleanupRetry)) ||
		outcome.events != 2 || outcome.passes != 3 || outcome.changed != 1 || !outcome.converged {
		t.Fatalf("typed cleanup retry outcome = %s", outcome.describe())
	}
	if inventory.cleanups != 2 || inventory.dead["pan-alpha-codex"] || inventory.uids["pan-alpha-codex"] {
		t.Fatalf("typed cleanup retry inventory = %+v", inventory)
	}
	if _, ok := store.registry.Pane("pan-alpha-codex"); ok {
		t.Fatal("typed cleanup retry left the managed Pane row")
	}
	agent, ok := store.registry.Agent("agt-alpha-codex")
	if !ok || agent.Status.Phase != coremetadata.PhaseOffline || agent.Status.PaneRef != "" {
		t.Fatalf("typed cleanup retry Agent = %+v", agent)
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
		live := exitReconcileFixtureLiveExcept("pan-alpha-codex")
		live["pan-alpha-codex"] = true
		result, err := reconcileLifecycle(context.Background(), exactPaneExitDirty(receipts...),
			&exactPaneExitInventory{uids: live, dead: map[string]bool{"pan-alpha-codex": true}, windowUID: "win-alpha-main"},
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
