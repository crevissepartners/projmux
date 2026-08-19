package app

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// agent_liveness_test.go covers the Agent liveness transition: when the tmux
// pane behind an Agent's managed Pane is gone, the Agent moves to Offline and
// drops its paneRef, so `agent resume` reaches its normal path without an
// explicit `delete pane` first.
//
// The subject is an inventory diff rather than an event handler, and it has to
// be one: `after-kill-pane` fires with an empty #{hook_pane}, so no hook can
// name the pane that died. Every test here therefore states the machine as a
// mirrored-uid set and the registry as the thing being judged against it.

// seedLiveAgentPane binds a registry Window and its Panes to a live tmux window
// the way an earlier create or a legacy import would have, so the mirrored-uid
// inventory reports those Pane uids as still transport-bound.
//
// The first pane uid is the Window's primary Pane; the rest are additional
// mirrored panes in the same window, which is the shape a managed Agent Pane
// has.
func seedLiveAgentPane(t *testing.T, tmux *fakeTmux, sessionName, windowUID string, paneUIDs ...string) *fakeTmuxSession {
	t.Helper()
	if len(paneUIDs) == 0 {
		t.Fatal("seedLiveAgentPane needs at least the primary Pane uid")
	}
	session := tmux.addSession(sessionName)
	window := seedLiveWindow(t, tmux, session, windowUID, paneUIDs[0])
	for _, uid := range paneUIDs[1:] {
		window.panes = append(window.panes, &fakeTmuxPane{
			id:   tmux.mint("%"),
			opts: map[string]string{tmuxopts.PaneUID: uid},
		})
	}
	return session
}

// stubPaneInventory is the mirrored-uid inventory seam with a scripted answer.
// It counts its calls because the common no-death path must stop after one
// query even though a candidate death requires a locked refresh.
type stubPaneInventory struct {
	uids  map[string]bool
	err   error
	calls int
}

func (s *stubPaneInventory) LivePaneUIDs(context.Context) (map[string]bool, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.uids, nil
}

type sequencedPaneInventory struct {
	uids  []map[string]bool
	errs  []error
	calls int
}

func (s *sequencedPaneInventory) LivePaneUIDs(context.Context) (map[string]bool, error) {
	index := s.calls
	s.calls++
	if index < len(s.errs) && s.errs[index] != nil {
		return nil, s.errs[index]
	}
	if index >= len(s.uids) {
		return nil, nil
	}
	return s.uids[index], nil
}

// livenessTestMutator is the fixture clock's mutator. Nothing here mints uids,
// so only the clock is supplied.
func livenessTestMutator() coremetadata.Mutator {
	return coremetadata.Mutator{Now: func() time.Time { return resourceFixtureClock }}
}

// projectLifecycle applies the shared exit-reconciliation transition to registry
// against one mirrored-uid observation and returns how many projections changed
// it.
//
// It is the same two calls both production trigger paths make -- select the
// absent Panes, project each one -- so a test that drives this drives exactly
// what the reconciler and the pane-exit hook drive.
func projectLifecycle(registry *coremetadata.Registry, mutator coremetadata.Mutator, live map[string]bool) int {
	changed := 0
	for _, projection := range projectTerminations(registry, mutator,
		lifecycleProjectionTargets(*registry, live, lifecycleDirtyEvent{})) {
		if projection.Changed {
			changed++
		}
	}
	return changed
}

// livenessRegistry builds a one-Agent registry whose Agent owns one managed
// Pane, in the given starting phase.
func livenessRegistry(phase coremetadata.AgentPhase, sessionRef *coremetadata.AgentSessionRef) coremetadata.Registry {
	registry := coremetadata.NewRegistry()
	registry.Panes = []coremetadata.Pane{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
		Metadata: coremetadata.ObjectMeta{
			UID: "pan-managed", Name: "codex-pane",
			OwnerRef:  &coremetadata.OwnerRef{Kind: coremetadata.KindAgent, UID: "agt-codex"},
			CreatedAt: resourceFixtureClock,
		},
		Spec: coremetadata.PaneSpec{Role: coremetadata.PaneRoleAgent, CWD: "/srv/alpha"},
	}}
	registry.Agents = []coremetadata.Agent{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindAgent,
		Metadata: coremetadata.ObjectMeta{
			UID: "agt-codex", Name: "codex",
			OwnerRef:  &coremetadata.OwnerRef{Kind: coremetadata.KindWindow, UID: "win-alpha-main"},
			CreatedAt: resourceFixtureClock,
		},
		Spec: coremetadata.AgentSpec{Provider: "codex"},
		Status: coremetadata.AgentStatus{
			Phase: phase, PaneRef: "pan-managed", SessionRef: sessionRef,
			LastTransitionAt: resourceFixtureClock,
		},
	}}
	return registry
}

// TestADeadManagedPaneReleasesItsAgentAndKeepsTheConversationPointer is the
// core contract: Offline, no paneRef, managed Pane gone, sessionRef intact.
//
// sessionRef surviving is the whole point of releasing rather than deleting.
// PaneRef answers "which tmux pane is this Agent in right now" and must go the
// moment that pane does; sessionRef answers "which provider conversation is
// this Agent" and is what `agent resume` needs to reattach. An Agent that lost
// its conversation pointer when its pane closed would be unresumable.
func TestADeadManagedPaneReleasesItsAgentAndKeepsTheConversationPointer(t *testing.T) {
	t.Parallel()

	ref := &coremetadata.AgentSessionRef{
		Provider:   "codex",
		ObservedAt: resourceFixtureClock,
		Codex:      &coremetadata.CodexSessionRef{ThreadID: "thr-7", SessionID: "ses-7"},
	}
	registry := livenessRegistry(coremetadata.PhaseRunning, ref)

	// The machine mirrors some other Pane, so this Agent's managed Pane is the
	// one that is absent rather than the inventory being empty.
	released := projectLifecycle(&registry, livenessTestMutator(), map[string]bool{"pan-somebody-else": true})
	if released != 1 {
		t.Fatalf("released = %d, want 1", released)
	}

	agent, ok := registry.Agent("agt-codex")
	if !ok {
		t.Fatal("the sweep deleted the Agent; a released Agent must survive as a resumable resource")
	}
	if agent.Status.Phase != coremetadata.PhaseOffline {
		t.Fatalf("phase = %q, want Offline", agent.Status.Phase)
	}
	if agent.Status.PaneRef != "" {
		t.Fatalf("paneRef = %q, want it cleared", agent.Status.PaneRef)
	}
	if agent.Status.Reason != coremetadata.TerminationReasonUnknown {
		t.Fatalf("reason = %q, want %q", agent.Status.Reason, coremetadata.TerminationReasonUnknown)
	}
	// The Agent keeps the evidence its Pane resource carried, which is the only
	// place it can survive: releasing the Agent deletes that Pane.
	receipt := agent.Status.LastTermination
	if receipt == nil {
		t.Fatal("a released Agent carries no termination evidence at all")
	}
	if receipt.Classification != coremetadata.TerminationUnknown ||
		receipt.Source != coremetadata.TerminationSourceReconcile {
		t.Fatalf("evidence = %+v, want an unknown receipt from the reconciler", receipt)
	}
	if agent.Status.SessionRef != ref {
		t.Fatalf("sessionRef = %+v, want the observed ref preserved verbatim", agent.Status.SessionRef)
	}
	if _, ok := registry.Pane("pan-managed"); ok {
		t.Fatal("the managed Pane outlived the tmux pane it was bound to")
	}
}

// TestTheProjectionNeverForcesATransitionTheTableForbids pins that the closed
// Agent lifecycle table stays the authority.
//
// Every real phase can legally reach Offline today, so the interesting row is
// the last one: a phase the table does not know about keeps its phase, its
// paneRef, and its managed Pane. That is the difference between a projection
// that respects the table and one that writes Offline because it decided the
// pane was gone.
//
// A refused transition still records the evidence. The two are different
// statements -- "here is what was observed" and "here is the phase that implies"
// -- and refusing the second is not a reason to discard the first, which is the
// only record an operator has of why an Agent in an unmovable phase is offline.
func TestTheProjectionNeverForcesATransitionTheTableForbids(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		phase coremetadata.AgentPhase
	}{
		{name: "running", phase: coremetadata.PhaseRunning},
		{name: "pending", phase: coremetadata.PhasePending},
		{name: "offline", phase: coremetadata.PhaseOffline},
		{name: "failed", phase: coremetadata.PhaseFailed},
		{name: "a phase outside the table", phase: coremetadata.AgentPhase("Quarantined")},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			registry := livenessRegistry(test.phase, nil)
			permitted := coremetadata.CanTransitionAgent(test.phase, coremetadata.PhaseOffline)

			// The dead Pane is always selected: unrecorded evidence is itself
			// outstanding work, whatever the Agent's phase allows.
			selected := lifecycleProjectionTargets(registry, nil, lifecycleDirtyEvent{})
			if len(selected) != 1 || selected[0].PaneUID != "pan-managed" {
				t.Fatalf("selected %+v, want exactly the dead managed Pane", selected)
			}

			if changed := projectLifecycle(&registry, livenessTestMutator(), nil); changed != 1 {
				t.Fatalf("changed = %d, want the projection to record its evidence once", changed)
			}
			agent, _ := registry.Agent("agt-codex")
			if !permitted {
				if agent.Status.Phase != test.phase || agent.Status.PaneRef != "pan-managed" {
					t.Fatalf("a forbidden transition still mutated the Agent: %+v", agent.Status)
				}
				pane, ok := registry.Pane("pan-managed")
				if !ok {
					t.Fatal("a forbidden transition still deleted the managed Pane")
				}
				if pane.Status.LastTermination == nil {
					t.Fatal("a forbidden transition discarded the evidence it observed")
				}
				// A second pass has nothing left: the evidence is stored and the
				// transition is still forbidden.
				if remaining := lifecycleProjectionTargets(registry, nil, lifecycleDirtyEvent{}); len(remaining) != 0 {
					t.Fatalf("remaining = %+v, want a forbidden transition to stop being re-projected", remaining)
				}
				return
			}
			if agent.Status.Phase != coremetadata.PhaseOffline || agent.Status.PaneRef != "" {
				t.Fatalf("status = %+v, want Offline with no paneRef", agent.Status)
			}
		})
	}
}

// TestAnAgentWithNoManagedPaneIsNotSwept covers the other skip: an Agent that
// holds no paneRef at all has nothing to release, so it must not be handed a
// reason and a fresh transition timestamp on every reconcile.
func TestAnAgentWithNoManagedPaneIsNotSwept(t *testing.T) {
	t.Parallel()

	registry := livenessRegistry(coremetadata.PhaseOffline, nil)
	registry.Agents[0].Status.PaneRef = "   "
	registry.Panes = nil

	if inputs := lifecycleProjectionTargets(registry, nil, lifecycleDirtyEvent{}); len(inputs) != 0 {
		t.Fatalf("targets = %+v, want none: an Agent without a managed Pane has no Pane to project", inputs)
	}
	if released := projectLifecycle(&registry, livenessTestMutator(), nil); released != 0 {
		t.Fatalf("released = %d, want 0", released)
	}
	if reason := registry.Agents[0].Status.Reason; reason != "" {
		t.Fatalf("reason = %q, want the Agent left untouched", reason)
	}
}

// TestALiveManagedPaneCostsTheRegistryNothing is the negative case and the
// no-op cost budget in one.
//
// Both tmux pane-exit hooks call this on every pane exit in every session, most
// of which have nothing to do with a managed Agent. The common path must
// therefore read the inventory, decide there is nothing to do, and never take
// the registry write lock -- so this asserts zero transactions, not just an
// unchanged registry.
func TestALiveManagedPaneCostsTheRegistryNothing(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	before := store.snapshot()
	// Every managed Pane in the fixture is mirrored. An observation that is
	// missing one is a death, not a no-op, so the whole set has to be present
	// for this to be the negative case it claims to be.
	inventory := &stubPaneInventory{uids: map[string]bool{
		"pan-alpha-zsh": true, "pan-alpha-log": true, "pan-alpha-codex": true,
		"pan-alpha-review": true, "pan-beta-zsh": true,
	}}

	if err := runDeadAgentPaneSweep(context.Background(), inventory, store.store()); err != nil {
		t.Fatalf("runDeadAgentPaneSweep: %v", err)
	}
	if inventory.calls != 1 {
		t.Fatalf("tmux inventory queried %d times, want exactly 1 per invocation", inventory.calls)
	}
	if store.transactions != 0 || store.writes != 0 {
		t.Fatalf("transactions = %d, writes = %d; a sweep with nothing to release must not open the store",
			store.transactions, store.writes)
	}
	if store.snapshot() != before {
		t.Fatalf("the registry changed:\n--- got ---\n%s\n--- want ---\n%s", store.snapshot(), before)
	}
}

// TestATmuxInventoryFailureReleasesNothingAndIsNotAnError pins the fail-closed
// rule.
//
// A machine whose tmux server is not running, or which refuses the command,
// must not have its registry rewritten: an unreadable inventory is indis-
// tinguishable from an empty one, and treating it as empty would offline every
// managed Agent at once. It is also not an error, because the sweep rides along
// inside somebody else's operation and must never fail it.
func TestATmuxInventoryFailureReleasesNothingAndIsNotAnError(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	before := store.snapshot()
	inventory := &stubPaneInventory{err: errors.New("no server running on /tmp/tmux-1000/default")}

	if err := runDeadAgentPaneSweep(context.Background(), inventory, store.store()); err != nil {
		t.Fatalf("runDeadAgentPaneSweep = %v, want a silent no-op", err)
	}
	if store.transactions != 0 || store.writes != 0 {
		t.Fatalf("transactions = %d, writes = %d; an unreadable inventory must not open the store",
			store.transactions, store.writes)
	}
	if store.snapshot() != before {
		t.Fatal("an unreadable tmux inventory rewrote the registry")
	}
}

// TestASweptDeathOpensExactlyOneWriteTransaction is the positive half of the
// cost budget: when something did die the sweep does open the store, once, and
// the release lands.
func TestASweptDeathOpensExactlyOneWriteTransaction(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	// pan-alpha-codex, the fixture's only Agent-managed Pane, is the one Pane
	// deliberately absent from the inventory.
	inventory := &stubPaneInventory{uids: map[string]bool{
		"pan-alpha-zsh": true, "pan-alpha-log": true,
		"pan-alpha-review": true, "pan-beta-zsh": true,
	}}

	if err := runDeadAgentPaneSweep(context.Background(), inventory, store.store()); err != nil {
		t.Fatalf("runDeadAgentPaneSweep: %v", err)
	}
	if store.transactions != 1 || store.writes != 1 {
		t.Fatalf("transactions = %d, writes = %d, want exactly one of each", store.transactions, store.writes)
	}

	agent, ok := store.registry.Agent("agt-alpha-codex")
	if !ok {
		t.Fatal("the Agent did not survive its released Pane")
	}
	if agent.Status.Phase != coremetadata.PhaseOffline || agent.Status.PaneRef != "" {
		t.Fatalf("status = %+v, want Offline with no paneRef", agent.Status)
	}
	if _, ok := store.registry.Pane("pan-alpha-codex"); ok {
		t.Fatal("the managed Pane survived the sweep")
	}
}

// TestAWaitingPaneExitSweepCannotReleaseANewerCreate is the create/reconcile
// race regression. A pane-exit hook first observes the old managed Pane as
// gone. While that hook waits to enter the Registry transaction, canonical
// create commits a second Running Agent and its exact managed Pane. The hook
// must judge the Registry it actually locks against a fresh live inventory;
// applying its pre-lock snapshot would release both Agents, delete the new Pane
// resource, and leave its still-live tmux pane as an orphan to be rebound on
// the next reconciliation pass.
func TestAWaitingPaneExitSweepCannotReleaseANewerCreate(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	resourceStore := store.store()
	originalLoad := resourceStore.load
	advanced := false
	newAgentUID := ""
	newPaneUID := ""
	refreshed := map[string]bool{"pan-alpha-zsh": true}
	resourceStore.load = func() (coremetadata.Registry, error) {
		before, err := originalLoad()
		if err != nil || advanced {
			return before, err
		}
		advanced = true
		mutator := store.mutator()
		agent, err := mutator.CreateAgent(&store.registry, "win-alpha-main", coremetadata.CreateAgentOptions{
			Name: "codex-new", Provider: "codex", OperationID: "op-new-create",
		})
		if err != nil {
			t.Fatalf("seed newer Agent: %v", err)
		}
		pane, err := mutator.AttachAgentPane(&store.registry, agent.Metadata.UID, coremetadata.BootstrapPane{
			CWD: "/srv/alpha",
		}, "op-new-create")
		if err != nil {
			t.Fatalf("seed newer managed Pane: %v", err)
		}
		newAgentUID = agent.Metadata.UID
		newPaneUID = pane.Metadata.UID
		if newAgentUID == "" || newPaneUID == "" {
			t.Fatal("new create fixture minted an empty Pane uid")
		}
		// The refreshed inventory is the tmux UID mirror of the exact Pane
		// canonical create just committed.
		refreshed[newPaneUID] = true
		return before, nil
	}

	inventory := &sequencedPaneInventory{uids: []map[string]bool{
		// The hook's pre-lock observation predates the new create.
		{"pan-alpha-zsh": true},
		// Once the hook owns the Registry lock, the newly created managed Pane
		// is live and must not be judged against the older snapshot.
		refreshed,
	}}

	if err := runDeadAgentPaneSweep(context.Background(), inventory, resourceStore); err != nil {
		t.Fatalf("runDeadAgentPaneSweep: %v", err)
	}
	if inventory.calls != 2 {
		t.Fatalf("tmux inventory calls = %d, want preflight plus in-transaction refresh", inventory.calls)
	}

	oldAgent, ok := store.registry.Agent("agt-alpha-codex")
	if !ok {
		t.Fatal("old Agent disappeared instead of being released")
	}
	if oldAgent.Status.Phase != coremetadata.PhaseOffline || oldAgent.Status.PaneRef != "" {
		t.Fatalf("old Agent status = %+v, want released Offline Agent", oldAgent.Status)
	}
	if !refreshed[newPaneUID] {
		t.Fatalf("refreshed tmux inventory is missing exact Pane uid %q", newPaneUID)
	}
	newAgent, ok := store.registry.Agent(newAgentUID)
	if !ok {
		t.Fatal("new Agent disappeared while the older pane-exit sweep waited")
	}
	if newAgent.Status.Phase != coremetadata.PhaseRunning || newAgent.Status.PaneRef != newPaneUID {
		t.Fatalf("new Agent status = %+v, want Running on %s", newAgent.Status, newPaneUID)
	}
	newPane, ok := store.registry.Pane(newPaneUID)
	if !ok || newPane.Metadata.OwnerUID() != newAgent.Metadata.UID {
		t.Fatalf("new managed Pane = %+v, want exact Agent-owned Pane resource", newPane)
	}
	if store.transactions != 1 || store.writes != 1 {
		t.Fatalf("transactions = %d writes = %d, want one committed sweep", store.transactions, store.writes)
	}
}

func TestASecondInventoryFailureKeepsEveryAgentAndPane(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	before := store.snapshot()
	inventory := &sequencedPaneInventory{
		uids: []map[string]bool{{"pan-alpha-zsh": true}},
		errs: []error{nil, errors.New("tmux server disappeared before locked refresh")},
	}

	if err := runDeadAgentPaneSweep(context.Background(), inventory, store.store()); err != nil {
		t.Fatalf("runDeadAgentPaneSweep = %v, want fail-closed no-op", err)
	}
	if inventory.calls != 2 {
		t.Fatalf("tmux inventory calls = %d, want preflight plus failed locked refresh", inventory.calls)
	}
	if store.transactions != 1 || store.writes != 0 {
		t.Fatalf("transactions = %d writes = %d, want one aborted transaction and zero writes", store.transactions, store.writes)
	}
	if got := store.snapshot(); got != before {
		t.Fatalf("failed locked refresh mutated registry:\n--- got ---\n%s\n--- want ---\n%s", got, before)
	}
}

// TestTheSweepRefusesToRunWithoutItsSeams pins the two misconfiguration guards.
// Neither is reachable from the hook, and that is the point: they fail loudly
// here rather than silently reporting "nothing to release" if a future call
// site forgets a seam.
func TestTheSweepRefusesToRunWithoutItsSeams(t *testing.T) {
	t.Parallel()

	if err := runDeadAgentPaneSweep(context.Background(), nil, newFakeResourceStore(t).store()); err == nil {
		t.Fatal("a missing tmux inventory was accepted")
	}
	if err := runDeadAgentPaneSweep(context.Background(), &stubPaneInventory{}, nil); err == nil {
		t.Fatal("a missing registry store was accepted")
	}
}

// TestReconciliationReleasesAnAgentWhoseManagedPaneIsGone is the reconciler
// half of the trigger, end to end through a real mutation route.
//
// The route is `create pane`, which reconciles before it resolves selectors.
// The fixture Agent's managed Pane is never mirrored onto a live tmux pane, so
// this is also the dangling-paneRef case: a paneRef that has been stale since
// before the sweep existed is resolved by the first pass, which is why no
// one-shot migration is needed.
func TestReconciliationReleasesAnAgentWhoseManagedPaneIsGone(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	// alpha's Window is live and its shell Pane is mirrored; only the Agent's
	// managed Pane is missing from the machine.
	seedOwnedSession(seedLiveAgentPane(t, tmux, "alpha", "win-alpha-main", "pan-alpha-zsh"), "prj-alpha", "/srv/alpha")
	create, _ := newTestResourceCreateCommand(t, store, tmux)

	if _, _, err := runRoute(t, create, "pane", "--project", "alpha", "--window", "main"); err != nil {
		t.Fatalf("create pane error = %v", err)
	}

	agent, ok := store.registry.Agent("agt-alpha-codex")
	if !ok {
		t.Fatal("reconciliation deleted the Agent instead of releasing it")
	}
	if agent.Status.Phase != coremetadata.PhaseOffline || agent.Status.PaneRef != "" {
		t.Fatalf("status = %+v, want reconciliation to have released the Agent", agent.Status)
	}
	if _, ok := store.registry.Pane("pan-alpha-codex"); ok {
		t.Fatal("the dangling managed Pane survived reconciliation")
	}
}

// TestReconciliationLeavesAnAgentWhoseManagedPaneIsStillLive is acceptance 4
// against the same route: a mirrored managed Pane means a live Agent, and a
// mutation elsewhere must not touch it.
func TestReconciliationLeavesAnAgentWhoseManagedPaneIsStillLive(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	seedOwnedSession(seedLiveAgentPane(t, tmux, "alpha", "win-alpha-main", "pan-alpha-zsh", "pan-alpha-codex"), "prj-alpha", "/srv/alpha")
	create, _ := newTestResourceCreateCommand(t, store, tmux)

	if _, _, err := runRoute(t, create, "pane", "--project", "alpha", "--window", "main"); err != nil {
		t.Fatalf("create pane error = %v", err)
	}

	agent, _ := store.registry.Agent("agt-alpha-codex")
	if agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.PaneRef != "pan-alpha-codex" {
		t.Fatalf("status = %+v, want the live Agent left Running on its own Pane", agent.Status)
	}
	if _, ok := store.registry.Pane("pan-alpha-codex"); !ok {
		t.Fatal("a live managed Pane was deleted")
	}
}

// TestTheReconcilerSweepsAfterImportingLiveSessions pins the ordering the
// reconcile pass depends on.
//
// Importing a pre-existing tmux session is what allocates and mirrors that
// session's Pane uids. A sweep that ran before the import would diff the
// registry against an inventory that does not yet carry the uids the same pass
// just allocated, and would offline an Agent the instant it was imported. The
// assertion is that a freshly imported session survives the pass intact.
func TestTheReconcilerSweepsAfterImportingLiveSessions(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	tmux := newFakeTmux()
	// A session with no projmux identity at all: the importer has to allocate
	// and mirror every uid in this same pass.
	session := tmux.addSession("legacy")
	window := session.windows[0]
	window.name = "editor"
	reconciler := newTestReconciler(tmux, nil)
	reconciler.observeLegacy = func(_ context.Context, name string) (coremetadata.LegacySession, intmetadata.LegacyTargets, error) {
		if name != "legacy" {
			return coremetadata.LegacySession{}, intmetadata.LegacyTargets{}, nil
		}
		return coremetadata.LegacySession{
				Session: "legacy",
				Root:    root,
				Windows: []coremetadata.LegacyWindow{
					{Name: "editor", Panes: []coremetadata.LegacyPane{{Command: "nvim"}}},
				},
			}, intmetadata.LegacyTargets{
				Windows: []string{window.id},
				Panes:   [][]string{{window.panes[0].id}},
			}, nil
	}

	registry := coremetadata.NewRegistry()
	mutator := coremetadata.Mutator{
		Now:       func() time.Time { return resourceFixtureClock },
		NewUID:    coremetadata.NewUID,
		DirExists: intmetadata.DirExists,
	}
	if err := reconciler.reconcile(context.Background(), &registry, mutator, "op-1"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(registry.Panes) == 0 {
		t.Fatal("the pass imported no Pane, so the ordering is not under test")
	}

	// The ordering is read off the recorded argv rather than off the outcome:
	// the mirrored-uid inventory must be the pass's last tmux read, after every
	// uid this pass allocated has been written onto a live pane.
	lastMirror, lastInventory := -1, -1
	for i, call := range tmux.calls {
		switch {
		case call[0] == "set-option" && slices.Contains(call, tmuxopts.PaneUID):
			lastMirror = i
		case call[0] == "list-panes" && slices.Contains(call, "-a"):
			lastInventory = i
		}
	}
	if lastMirror < 0 {
		t.Fatal("the pass mirrored no Pane uid, so the ordering is not under test")
	}
	if lastInventory < lastMirror {
		t.Fatalf("the sweep read the inventory at call %d, before the last uid was mirrored at call %d; "+
			"a freshly imported Pane would look dead", lastInventory, lastMirror)
	}
	live, err := reconciler.mirror.LivePaneUIDs(context.Background())
	if err != nil {
		t.Fatalf("LivePaneUIDs: %v", err)
	}
	for _, pane := range registry.Panes {
		if !live[pane.Metadata.UID] {
			t.Fatalf("imported Pane %q was never mirrored, so the sweep would judge it dead", pane.Metadata.UID)
		}
	}
}

// TestTheHiddenTmuxRouteRunsTheSweep is the hook half of the trigger: the
// subcommand the generated hook string invokes dispatches and does the work.
func TestTheHiddenTmuxRouteRunsTheSweep(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	// The Window's shell Pane is live; the Agent's managed Pane is the pane
	// that just exited, so it is not in the inventory.
	seedLiveAgentPane(t, tmux, "alpha", "win-alpha-main", "pan-alpha-zsh")
	cmd := &tmuxCommand{runner: tmux, resources: store.store()}

	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"release-dead-agent-panes"}, &stdout, &stderr); err != nil {
		t.Fatalf("release-dead-agent-panes error = %v (stderr %q)", err, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want a silent hook route", stdout.String())
	}

	agent, ok := store.registry.Agent("agt-alpha-codex")
	if !ok {
		t.Fatal("the hook route deleted the Agent")
	}
	if agent.Status.Phase != coremetadata.PhaseOffline || agent.Status.PaneRef != "" {
		t.Fatalf("status = %+v, want the hook route to have released the Agent", agent.Status)
	}
	if store.transactions != 1 {
		t.Fatalf("transactions = %d, want exactly 1", store.transactions)
	}
}

// TestTheHiddenTmuxRouteTakesNoArguments keeps the hook string honest: the
// route is an inventory sweep, so a caller that passes it a pane id is a
// mistake rather than a per-pane request.
func TestTheHiddenTmuxRouteTakesNoArguments(t *testing.T) {
	t.Parallel()

	cmd := &tmuxCommand{runner: newFakeTmux(), resources: newFakeResourceStore(t).store()}
	var stderr bytes.Buffer
	err := cmd.Run([]string{"release-dead-agent-panes", "%3"}, &bytes.Buffer{}, &stderr)
	if err == nil {
		t.Fatal("the route accepted a pane argument")
	}
	if !strings.Contains(err.Error(), "accepts no arguments") {
		t.Fatalf("error = %q, want the no-arguments refusal", err)
	}
}

// TestBothPaneExitHooksRebalanceThenSweep pins the generated hook strings.
//
// Both hooks need both halves: `pane-exited` covers a child process that ended
// and `after-kill-pane` covers `tmux kill-pane` and the pane close key. The
// rebalance half stays first and keeps its own `|| true`, so pane layout never
// depends on whether the registry sweep succeeded, and the sweep still runs
// when there was no layout to rebalance.
func TestBothPaneExitHooksRebalanceThenSweep(t *testing.T) {
	t.Parallel()

	cmd := &tmuxCommand{executable: func() (string, error) { return "/tmp/proj mux/bin/projmux", nil }}
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"print-config"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	const body = "sleep 0.05; '/tmp/proj mux/bin/projmux' internal tmux rebalance-panes >/dev/null 2>&1 || true; " +
		"'/tmp/proj mux/bin/projmux' internal tmux release-dead-agent-panes >/dev/null 2>&1 || true"
	for _, hook := range []string{"pane-exited", "after-kill-pane"} {
		line := hookLine(t, stdout.String(), hook)
		if !strings.Contains(line, body) {
			t.Fatalf("%s hook = %q, want it to run %q", hook, line, body)
		}
		if !strings.Contains(line, "run-shell -b ") {
			t.Fatalf("%s hook = %q, want the body backgrounded so a pane exit never blocks tmux", hook, line)
		}
	}
}

// hookLine returns the generated `set-hook -g <name>` line.
func hookLine(t *testing.T, config, name string) string {
	t.Helper()
	prefix := "set-hook -g " + name + " "
	for line := range strings.SplitSeq(config, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	t.Fatalf("generated config has no %q hook", name)
	return ""
}

// TestExplicitPaneDeletionStillOfflinesItsAgent is the regression half: the
// pre-existing `delete pane` transition is untouched by the liveness sweep.
//
// The two paths stay distinguishable in the record. An explicit deletion is
// recorded as `deleted`, a swept death as the liveness reason, so an operator
// reading `describe agent` can still tell which one happened.
func TestExplicitPaneDeletionStillOfflinesItsAgent(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	if _, _, err := runRoute(t, newTestDeleteCommand(store, false, false, nil),
		"pane", "codex-pane", "--project", "alpha", "--window", "main", "--yes"); err != nil {
		t.Fatalf("delete pane error = %v", err)
	}

	agent, ok := store.registry.Agent("agt-alpha-codex")
	if !ok {
		t.Fatal("delete pane removed the Agent")
	}
	if agent.Status.Phase != coremetadata.PhaseOffline || agent.Status.PaneRef != "" {
		t.Fatalf("status = %+v, want Offline with no paneRef", agent.Status)
	}
	if agent.Status.Reason != string(coremetadata.AgentExitDeleted) {
		t.Fatalf("reason = %q, want the explicit-deletion reason %q; the liveness sweep must not have taken this path over",
			agent.Status.Reason, coremetadata.AgentExitDeleted)
	}
	if agent.Status.Reason == coremetadata.TerminationReasonUnknown {
		t.Fatal("an explicit deletion is recorded as an unexplained disappearance")
	}
}
