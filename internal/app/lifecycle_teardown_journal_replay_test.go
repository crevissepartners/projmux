package app

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// lifecycle_teardown_journal_replay_test.go pins the teardown decisions for a
// Window that a Project start/Continue topology replay re-materialized. The
// stored row of such a Window carries the $N/@N binding of a runtime that no
// longer exists; the replay must replace it with the live pair in its own
// commit, or no clean exit can ever pair with the real window-unlinked.

const (
	replayedWindowUID = "win-replayed"
	replayedPaneUID   = "pane-replayed"
)

// replayedWindowFixture is the closed beta Project of the startup topology
// fixture plus one disposable one-shell Window. Every beta Window row carries
// an old `$0/@4N` binding and MissingRuntime, which is what a Registry looks
// like after its tmux server went away.
type replayedWindowFixture struct {
	activation *registryProjectTopologyMaterializer
	store      *fakeResourceStore
	server     *fakeTmux
	root       string
}

var replayedWindowOldBindings = map[string][2]string{
	"win-beta-main":   {"$0", "@40"},
	replayedWindowUID: {"$0", "@41"},
}

func newReplayedWindowFixture(t *testing.T) replayedWindowFixture {
	t.Helper()
	activation, store, server, root, _ := newProjectStartupTopologyFixture(t)
	mutator := store.mutator()
	mutator.NewUID = func(kind coremetadata.Kind) (string, error) {
		switch kind {
		case coremetadata.KindWindow:
			return replayedWindowUID, nil
		case coremetadata.KindPane:
			return replayedPaneUID, nil
		}
		return "", errors.New("replayed Window fixture mints only one Window and one Pane")
	}
	if _, panes, err := mutator.AddWindow(&store.registry, "prj-beta", coremetadata.BootstrapWindow{Name: "disposable"}, "", "op-replayed-fixture"); err != nil || len(panes) != 1 {
		t.Fatalf("add disposable shell Window = %+v, %v", panes, err)
	}
	for _, window := range store.registry.WindowsOf("prj-beta") {
		old, ok := replayedWindowOldBindings[window.Metadata.UID]
		if !ok {
			continue
		}
		setWindowOldRuntimeBinding(t, &store.registry, window.Metadata.UID, old[0], old[1])
	}
	if err := store.registry.Validate(); err != nil {
		t.Fatalf("replayed Window fixture: %v", err)
	}
	return replayedWindowFixture{activation: activation, store: store, server: server, root: root}
}

// setWindowOldRuntimeBinding writes the retained last-positive binding and the
// MissingRuntime condition an earlier inventory left on an offline Window row.
func setWindowOldRuntimeBinding(t *testing.T, registry *coremetadata.Registry, windowUID, sessionID, windowID string) {
	t.Helper()
	window, ok := registry.Window(windowUID)
	if !ok {
		t.Fatalf("missing Window fixture %s", windowUID)
	}
	window.Status.RuntimeSessionID, window.Status.RuntimeID = sessionID, windowID
	window.Status.Conditions = []coremetadata.Condition{{
		Type: coremetadata.ConditionMissingRuntime, Status: coremetadata.ConditionTrue,
		Reason: coremetadata.ReasonRuntimeUnbound, FirstObservedAt: resourceFixtureClock,
		LastTransitionAt: resourceFixtureClock,
	}}
}

func (fx replayedWindowFixture) replay(t *testing.T) {
	t.Helper()
	materialized, err := fx.activation.MaterializeProjectTopology(context.Background(), projectTopologyMaterializeRequest{Root: fx.root, SessionName: "beta"})
	if err != nil || !materialized {
		t.Fatalf("MaterializeProjectTopology() = %t, %v; want true, nil", materialized, err)
	}
}

// liveWindowHandles returns the exact live `$N/@N` of the tmux Window that
// carries windowUID, read from the runtime rather than from the Registry.
func (fx replayedWindowFixture) liveWindowHandles(t *testing.T, windowUID string) (string, string) {
	t.Helper()
	for _, session := range fx.server.sessions {
		for _, window := range session.windows {
			if window.opts[tmuxopts.WindowUID] == windowUID {
				return session.id, window.id
			}
		}
	}
	t.Fatalf("Window %s is not live:\n%s", windowUID, fx.server.state())
	return "", ""
}

func (fx replayedWindowFixture) removeLiveWindow(t *testing.T, windowUID string) {
	t.Helper()
	for _, session := range fx.server.sessions {
		before := len(session.windows)
		session.windows = slices.DeleteFunc(session.windows, func(window *fakeTmuxWindow) bool {
			return window.opts[tmuxopts.WindowUID] == windowUID
		})
		if len(session.windows) != before {
			return
		}
	}
	t.Fatalf("Window %s is not live", windowUID)
}

// inventory answers the lifecycle observations from the fake runtime, with
// the dead Pane removed from the live set (and kept as a retained dead Pane
// when retainDead) and endedWindow, if any, already gone.
func (fx replayedWindowFixture) inventory(deadPaneUID, endedWindowUID string, retainDead bool) *exactPaneExitInventory {
	live, dead, windows, sessions := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]int{}
	windowUID := ""
	for _, session := range fx.server.sessions {
		for _, window := range session.windows {
			uid := window.opts[tmuxopts.WindowUID]
			if uid == "" || uid == endedWindowUID {
				continue
			}
			windows[uid] = true
			sessions[session.id]++
			for _, pane := range window.panes {
				paneUID := pane.opts[tmuxopts.PaneUID]
				if paneUID == "" {
					continue
				}
				if paneUID == deadPaneUID {
					windowUID = uid
					dead[paneUID] = true
					if !retainDead {
						continue
					}
				}
				live[paneUID] = true
			}
		}
	}
	return &exactPaneExitInventory{uids: live, dead: dead, windowUID: windowUID, windows: windows, windowSessions: sessions}
}

func (fx replayedWindowFixture) paneExited(t *testing.T, paneUID, agentUID string) lifecycleDirtyEvent {
	t.Helper()
	pane, ok := fx.store.registry.Pane(paneUID)
	if !ok || pane.Status.Activation.Generation == "" || exactTmuxHandle(pane.Status.Activation.RuntimeID, "%") == "" {
		t.Fatalf("replayed Pane %s has no exact activation: %+v", paneUID, pane)
	}
	return lifecycleDirtyEvent{
		target:        tmuxTransport{Kind: tmuxSocketPath, Value: fx.server.socketPath, Source: tmuxSocketPathSource},
		runtimePaneID: pane.Status.Activation.RuntimeID,
		teardownKind:  coremetadata.TeardownEventPaneExited,
		receipts:      []coremetadata.TerminationEvidence{phase2NormalReceipt(paneUID, agentUID, pane.Status.Activation.Generation)},
	}
}

func windowUnlinkedOf(event lifecycleDirtyEvent, sessionID, windowID string) lifecycleDirtyEvent {
	unlinked := event
	unlinked.teardownKind = coremetadata.TeardownEventWindowUnlinked
	unlinked.runtimePaneID = ""
	unlinked.runtimeSessionID = sessionID
	unlinked.runtimeWindowID = windowID
	return unlinked
}

// Acceptance 6 and 7: the replay commit itself carries the live binding of
// every Window it materialized — the new-session-adopted first Window and a
// new-window one alike — and clears MissingRuntime, while a Window row the
// replay did not materialize keeps its runtime ids byte-for-byte.
func TestTopologyReplayCommitsLiveBindingForEveryMaterializedWindowOnly(t *testing.T) {
	fx := newReplayedWindowFixture(t)
	setWindowOldRuntimeBinding(t, &fx.store.registry, "win-alpha-review", "$0", "@42")
	outside := map[string]coremetadata.Window{}
	for _, window := range fx.store.registry.Windows {
		if window.Metadata.OwnerUID() != "prj-beta" {
			outside[window.Metadata.UID] = window.Clone()
		}
	}
	writesBefore := fx.store.writes

	fx.replay(t)

	// Only the materialization commit ran: there was no reconcile or hook pass
	// between the replay and this read.
	if fx.store.writes != writesBefore+1 {
		t.Fatalf("replay committed %d Registry writes, want exactly 1", fx.store.writes-writesBefore)
	}
	materialized := fx.store.registry.WindowsOf("prj-beta")
	if len(materialized) != 3 {
		t.Fatalf("beta Windows after replay = %+v", materialized)
	}
	for _, window := range materialized {
		sessionID, windowID := fx.liveWindowHandles(t, window.Metadata.UID)
		if window.Status.RuntimeSessionID != sessionID || window.Status.RuntimeID != windowID {
			t.Fatalf("Window %s binding after replay = %s/%s, want live %s/%s",
				window.Metadata.UID, window.Status.RuntimeSessionID, window.Status.RuntimeID, sessionID, windowID)
		}
		if old, ok := replayedWindowOldBindings[window.Metadata.UID]; ok && old[1] == windowID {
			t.Fatalf("fixture old binding %v collides with the live handle", old)
		}
		if _, ok := window.HasCondition(coremetadata.ConditionMissingRuntime); ok {
			t.Fatalf("Window %s retained MissingRuntime after its replay", window.Metadata.UID)
		}
	}
	for uid, before := range outside {
		after, ok := fx.store.registry.Window(uid)
		if !ok || !reflect.DeepEqual(before, after.Clone()) {
			t.Fatalf("Window %s outside the replay changed:\nbefore %+v\nafter  %+v", uid, before, after)
		}
	}
	if review, _ := fx.store.registry.Window("win-alpha-review"); review.Status.RuntimeSessionID != "$0" || review.Status.RuntimeID != "@42" {
		t.Fatalf("offline sibling Window binding = %s/%s, want $0/@42", review.Status.RuntimeSessionID, review.Status.RuntimeID)
	}
}

// Acceptance 7 for already-live Windows: a second replay under the existing
// session re-creates only the raw-killed first Window. The live Windows it
// plans with a preset live id keep their rows byte-for-byte — including a
// deliberately wrong sentinel binding, which proves the writer never ran for
// them — while the re-created first Window takes its new live pair.
func TestTopologyReplayUnderLiveSessionLeavesPresetLiveWindowBindingsUntouched(t *testing.T) {
	fx := newReplayedWindowFixture(t)
	fx.replay(t)
	_, killedWindowID := fx.liveWindowHandles(t, "win-beta-main")
	fx.removeLiveWindow(t, "win-beta-main")
	sentinel, _ := fx.store.registry.Window(replayedWindowUID)
	sentinel.Status.RuntimeSessionID, sentinel.Status.RuntimeID = "$9", "@99"
	preset := map[string]coremetadata.Window{}
	for _, window := range fx.store.registry.Windows {
		if window.Metadata.UID != "win-beta-main" {
			preset[window.Metadata.UID] = window.Clone()
		}
	}

	fx.replay(t)

	for uid, before := range preset {
		after, ok := fx.store.registry.Window(uid)
		if !ok || !reflect.DeepEqual(before, after.Clone()) {
			t.Fatalf("Window %s not materialized by the second replay changed:\nbefore %+v\nafter  %+v", uid, before, after)
		}
	}
	main, _ := fx.store.registry.Window("win-beta-main")
	sessionID, windowID := fx.liveWindowHandles(t, "win-beta-main")
	if windowID == killedWindowID || main.Status.RuntimeSessionID != sessionID || main.Status.RuntimeID != windowID {
		t.Fatalf("re-created first Window binding = %s/%s, want new live %s/%s (killed %s)",
			main.Status.RuntimeSessionID, main.Status.RuntimeID, sessionID, windowID, killedWindowID)
	}
}

// Acceptance 1: a re-materialized shell Window whose sole Pane exits 0 is
// removed by its exact pane-exited plus the window-unlinked that names the live
// handles, and the Project stays. The journal records the pending half and one
// delete-window for that Window. The control runs the same pair against the
// stale pre-fix row shape and must not pair.
func TestReplayedWindowCleanLastPaneExitAndLiveUnlinkDeleteWindowAndJournalOnce(t *testing.T) {
	fx := newReplayedWindowFixture(t)
	fx.replay(t)
	journal := newTeardownJournal(t)
	sessionID, windowID := fx.liveWindowHandles(t, replayedWindowUID)
	inventory := fx.inventory(replayedPaneUID, "", false)
	event := fx.paneExited(t, replayedPaneUID, "")
	event.decisions = journal.recorder

	pending, err := reconcileLifecycle(context.Background(), event, inventory, fx.store.store())
	if err != nil || len(pending.pending) != 1 || inventory.cleanups != 1 {
		t.Fatalf("replayed pane-exited = %+v cleanups=%d err=%v", pending, inventory.cleanups, err)
	}
	stored, ok := fx.store.registry.Pane(replayedPaneUID)
	if !ok || stored.Status.Teardown == nil || stored.Status.Teardown.RuntimeSessionID != sessionID || stored.Status.Teardown.RuntimeWindowID != windowID {
		t.Fatalf("pending teardown evidence = %+v, want live %s/%s", stored.Status.Teardown, sessionID, windowID)
	}

	closed, err := reconcileLifecycle(context.Background(), windowUnlinkedOf(event, sessionID, windowID), inventory, fx.store.store())
	if err != nil || len(closed.rootCascaded) != 1 || closed.rootCascaded[0].WindowUID != replayedWindowUID || closed.rootCascaded[0].DeletedWindows != 1 {
		t.Fatalf("replayed window-unlinked = %+v, %v", closed, err)
	}
	if _, ok := fx.store.registry.Window(replayedWindowUID); ok {
		t.Fatal("re-materialized Window survived its clean causal pair")
	}
	if _, ok := fx.store.registry.Pane(replayedPaneUID); ok {
		t.Fatal("re-materialized Pane survived its clean causal pair")
	}
	if _, ok := fx.store.registry.Project("prj-beta"); !ok {
		t.Fatal("owning Project disappeared")
	}
	for _, uid := range []string{"win-beta-main"} {
		if _, ok := fx.store.registry.Window(uid); !ok {
			t.Fatalf("sibling Window %s disappeared", uid)
		}
	}
	recorded := journal.decisions(t)
	if len(recorded) != 2 {
		t.Fatalf("clean replayed pair journaled %d decisions, want 2: %+v", len(recorded), recorded)
	}
	requireTeardownDecision(t, recorded[0], "delete-pane-agent", "topology.teardown.awaiting-window-unlink", "normal", replayedWindowUID, replayedPaneUID)
	requireTeardownDecision(t, recorded[1], "delete-window", "topology.teardown.window-teardown", "normal", replayedWindowUID, replayedPaneUID)

	// Pre-fix control: the same replay with its row put back to the old binding
	// stores stale evidence, so the real unlink finds nothing to pair with.
	stale := newReplayedWindowFixture(t)
	stale.replay(t)
	staleSession, staleWindow := stale.liveWindowHandles(t, replayedWindowUID)
	old := replayedWindowOldBindings[replayedWindowUID]
	row, _ := stale.store.registry.Window(replayedWindowUID)
	row.Status.RuntimeSessionID, row.Status.RuntimeID = old[0], old[1]
	staleInventory := stale.inventory(replayedPaneUID, "", false)
	staleEvent := stale.paneExited(t, replayedPaneUID, "")
	if _, err := reconcileLifecycle(context.Background(), staleEvent, staleInventory, stale.store.store()); err != nil {
		t.Fatalf("stale-shape pane-exited: %v", err)
	}
	if _, err := reconcileLifecycle(context.Background(), windowUnlinkedOf(staleEvent, staleSession, staleWindow), staleInventory, stale.store.store()); err != nil {
		t.Fatalf("stale-shape window-unlinked: %v", err)
	}
	if _, ok := stale.store.registry.Window(replayedWindowUID); !ok {
		t.Fatal("stale pre-fix binding shape paired with the live window-unlinked")
	}
}

// runReplayedWindowUnlinkHook drives the real controller convergence body for
// one window-unlinked hook naming exact live handles.
func runReplayedWindowUnlinkHook(t *testing.T, store *fakeResourceStore, inventory livePaneInventory,
	recorder *diagnostics.TeardownRecorder, sessionID, windowID string) controllerTriggerOutcome {
	t.Helper()
	tmux := newFakeTmux()
	target, err := tmuxSocketPathTarget(tmux.socketPath)
	if err != nil {
		t.Fatal(err)
	}
	runner := &controllerTriggerRunner{
		runner: &routedTmuxRunner{servers: map[string]*fakeTmux{"-S\x00" + tmux.socketPath: tmux}},
		store:  store.store(), events: controllerEventLog{dir: t.TempDir()}, receipts: terminationJournal{},
		observe:  func(tmuxTransport) livePaneInventory { return inventory },
		teardown: recorder,
	}
	outcome, err := runner.run(context.Background(), controllerTrigger{
		reason: controllerTriggerWindowUnlinked, target: target, session: sessionID, hookWindow: windowID,
	})
	if err != nil {
		t.Fatalf("replayed Window unlink hook: %v", err)
	}
	return outcome
}

// Acceptance 3: a raw kill-window of a re-materialized Window fires only
// window-unlinked and leaves a killed receipt. The Window is retained, and the
// first wait resolves the exact Window, Pane, and killed classification from
// the live handles the replay recorded.
func TestReplayedWindowKillWindowRetainsWindowAndJournalsAwaitingPaneExitKilled(t *testing.T) {
	fx := newReplayedWindowFixture(t)
	fx.replay(t)
	journal := newTeardownJournal(t)
	sessionID, windowID := fx.liveWindowHandles(t, replayedWindowUID)
	pane, _ := fx.store.registry.Pane(replayedPaneUID)
	killed := phase2NormalReceipt(replayedPaneUID, "", pane.Status.Activation.Generation)
	killed.Classification, killed.ExitCode, killed.Signal = coremetadata.TerminationKilled, nil, "HUP"
	if _, err := absorbTerminationReceipts(&fx.store.registry, fx.store.mutator(), []coremetadata.TerminationEvidence{killed}); err != nil {
		t.Fatalf("absorb killed receipt: %v", err)
	}
	fx.removeLiveWindow(t, replayedWindowUID)
	inventory := fx.inventory("", "", false)
	before := fx.store.snapshot()

	outcome := runReplayedWindowUnlinkHook(t, fx.store, inventory, journal.recorder, sessionID, windowID)
	if !strings.Contains(outcome.deferred, "window-unlinked is awaiting its causal pane-exited event") {
		t.Fatalf("replayed kill-window outcome = %s, want the causal wait", outcome.describe())
	}
	if _, ok := fx.store.registry.Window(replayedWindowUID); !ok {
		t.Fatal("raw kill-window deleted the re-materialized Window")
	}
	if fx.store.snapshot() != before {
		t.Fatal("raw kill-window unlink changed Registry bytes")
	}
	recorded := journal.decisions(t)
	if len(recorded) != 1 {
		t.Fatalf("replayed kill-window journaled %d decisions, want 1: %+v", len(recorded), recorded)
	}
	requireTeardownDecision(t, recorded[0], "retain", "topology.teardown.awaiting-pane-exit", "killed", replayedWindowUID, replayedPaneUID)
}

// Acceptance 4: a clean exit of a non-last Agent Pane in a re-materialized
// Window removes only that Pane. The Agent stays Offline with its conversation
// and the Window keeps its replayed binding and remaining Panes.
func TestReplayedWindowNonLastAgentPaneCleanExitRemovesOnlyPaneAndKeepsAgentOffline(t *testing.T) {
	fx := newReplayedWindowFixture(t)
	agent := addTopologyFixtureAgent(t, fx.store, topologyFixtureAgent{
		name: "claude", provider: "claude", cwd: fx.root, ref: claudeConversationRef("conv-replayed"),
	})
	markTopologyAgentInterrupted(t, fx.store, agent.Metadata.UID, "")
	fx.replay(t)
	replayed, ok := fx.store.registry.Agent(agent.Metadata.UID)
	if !ok || replayed.Status.Phase != coremetadata.PhaseRunning || replayed.Status.PaneRef == "" {
		t.Fatalf("replayed Agent = %+v", replayed)
	}
	agentPaneUID := replayed.Status.PaneRef
	windowBefore, _ := fx.store.registry.Window("win-beta-main")
	siblings := []string{}
	for _, pane := range fx.store.registry.PanesOf("win-beta-main") {
		siblings = append(siblings, pane.Metadata.UID)
	}
	if len(siblings) < 2 {
		t.Fatalf("re-materialized main Window has no sibling Pane: %v", siblings)
	}
	inventory := fx.inventory(agentPaneUID, "", true)
	event := fx.paneExited(t, agentPaneUID, agent.Metadata.UID)

	result, err := reconcileLifecycle(context.Background(), event, inventory, fx.store.store())
	if err != nil || len(result.cascaded) != 1 || !result.cascaded[0].Changed || len(result.rootCascaded) != 0 || inventory.cleanups != 1 {
		t.Fatalf("non-last Agent Pane clean exit = %+v cleanups=%d err=%v", result, inventory.cleanups, err)
	}
	if _, ok := fx.store.registry.Pane(agentPaneUID); ok {
		t.Fatal("clean Agent Pane row survived")
	}
	stored, ok := fx.store.registry.Agent(agent.Metadata.UID)
	if !ok || stored.Status.Phase != coremetadata.PhaseOffline || stored.Status.PaneRef != "" ||
		stored.Status.SessionRef.ConversationID() != "conv-replayed" {
		t.Fatalf("Agent after clean exit = %+v, want Offline with its conversation", stored.Status)
	}
	window, ok := fx.store.registry.Window("win-beta-main")
	if !ok || window.Status.RuntimeSessionID != windowBefore.Status.RuntimeSessionID || window.Status.RuntimeID != windowBefore.Status.RuntimeID {
		t.Fatalf("re-materialized Window after non-last exit = %+v", window)
	}
	for _, uid := range siblings {
		if uid == agentPaneUID {
			continue
		}
		if _, ok := fx.store.registry.Pane(uid); !ok {
			t.Fatalf("sibling Pane %s was deleted", uid)
		}
	}
}

// Acceptance 5: a past offline Window with retained teardown evidence and
// MissingRuntime is never deleted by absence alone. The replay does not
// materialize it, and neither the replay nor a clean causal pair for another,
// live Window pairs with or removes it.
func TestPastOfflineWindowWithUnpairedTeardownEvidenceSurvivesReplayAndOtherPairs(t *testing.T) {
	fx := newReplayedWindowFixture(t)
	mutator := fx.store.mutator()
	activatePaneFixture(t, fx.store, "pan-alpha-review", "", "gen-past-offline")
	if _, err := mutator.ObservePaneActivationRuntime(&fx.store.registry, "pan-alpha-review", "gen-past-offline", "%90"); err != nil {
		t.Fatal(err)
	}
	setWindowOldRuntimeBinding(t, &fx.store.registry, "win-alpha-review", "$0", "@90")
	past, _ := fx.store.registry.Pane("pan-alpha-review")
	past.Status.Teardown = &coremetadata.PaneTeardownEvidence{
		SocketIdentity:   tmuxTransport{Kind: tmuxSocketPath, Value: fx.server.socketPath, Source: tmuxSocketPathSource}.Label(),
		RuntimeSessionID: "$0", RuntimePaneID: "%90", RuntimeWindowID: "@90",
		WindowUID: "win-alpha-review", RootKind: coremetadata.KindProject, RootUID: "prj-alpha",
		Generation: "gen-past-offline", Classification: coremetadata.TerminationNormal,
		ObservedAt: time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC),
	}
	if err := fx.store.registry.Validate(); err != nil {
		t.Fatalf("past offline Window fixture: %v", err)
	}
	windowBefore, _ := fx.store.registry.Window("win-alpha-review")
	windowBeforeClone := windowBefore.Clone()
	evidenceBefore := *past.Status.Teardown

	fx.replay(t)
	sessionID, windowID := fx.liveWindowHandles(t, replayedWindowUID)
	inventory := fx.inventory(replayedPaneUID, "", false)
	event := fx.paneExited(t, replayedPaneUID, "")
	if pending, err := reconcileLifecycle(context.Background(), event, inventory, fx.store.store()); err != nil || len(pending.pending) != 1 {
		t.Fatalf("live pane-exited = %+v, %v", pending, err)
	}
	closed, err := reconcileLifecycle(context.Background(), windowUnlinkedOf(event, sessionID, windowID), inventory, fx.store.store())
	if err != nil || len(closed.rootCascaded) != 1 || closed.rootCascaded[0].WindowUID != replayedWindowUID {
		t.Fatalf("live pair = %+v, %v", closed, err)
	}

	window, ok := fx.store.registry.Window("win-alpha-review")
	if !ok || !reflect.DeepEqual(window.Clone(), windowBeforeClone) {
		t.Fatalf("past offline Window changed or disappeared: ok=%t\nbefore %+v\nafter  %+v", ok, windowBeforeClone, window)
	}
	pane, ok := fx.store.registry.Pane("pan-alpha-review")
	if !ok || pane.Status.Teardown == nil || !reflect.DeepEqual(*pane.Status.Teardown, evidenceBefore) {
		t.Fatalf("past offline Pane or its unpaired evidence changed: ok=%t %+v", ok, pane)
	}
	if _, ok := fx.store.registry.Project("prj-alpha"); !ok {
		t.Fatal("past offline Window's Project disappeared")
	}
}
