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
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// exit_reconciliation_test.go covers the one-shot exit reconciliation: a dirty
// event in, at most one lifecycle transition out.
//
// The core classification rules live in internal/core/metadata; these tests own
// the seam around them -- which host is observed, which Panes an event selects,
// how many transactions a pass costs, and what the read verbs then show.

// exitReconcileFixtureLive is the observation of a machine where every managed
// Pane in the resource fixture is still mirrored.
func exitReconcileFixtureLive() map[string]bool {
	return map[string]bool{
		"pan-alpha-zsh": true, "pan-alpha-log": true, "pan-alpha-codex": true,
		"pan-alpha-review": true, "pan-beta-zsh": true,
	}
}

// exitReconcileFixtureLiveExcept is that observation with one Pane missing,
// which is what one death looks like.
func exitReconcileFixtureLiveExcept(paneUIDs ...string) map[string]bool {
	live := exitReconcileFixtureLive()
	for _, uid := range paneUIDs {
		delete(live, uid)
	}
	return live
}

// stampFixtureActivation gives one fixture Pane an activation generation, the way
// its supervised materialization would have.
func stampFixtureActivation(t *testing.T, store *fakeResourceStore, paneUID, generation, agentUID string) {
	t.Helper()
	if _, err := store.mutator().RecordPaneActivation(&store.registry, paneUID,
		coremetadata.PaneActivationOptions{
			Generation: generation, RuntimeID: "%9", AgentUID: agentUID, OperationID: "op-launch",
		}); err != nil {
		t.Fatalf("RecordPaneActivation(%s): %v", paneUID, err)
	}
}

// TestANarrowedEventProjectsOnlyThePaneItNames pins that a producer able to name
// the pane that died gets exactly that pane reconciled.
//
// Both halves matter. A hook that can narrow must not pay for the whole registry,
// and it must not be able to reconcile a Pane it did not name -- an event is a
// hint about one runtime object, not a licence to sweep.
func TestANarrowedEventProjectsOnlyThePaneItNames(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	// Two Panes are gone on the machine; the event names one of them.
	inventory := &stubPaneInventory{uids: exitReconcileFixtureLiveExcept("pan-alpha-codex", "pan-alpha-log")}

	result, err := reconcileLifecycle(context.Background(),
		lifecycleDirtyEvent{paneUID: "pan-alpha-codex"}, inventory, store.store())
	if err != nil {
		t.Fatalf("reconcileLifecycle: %v", err)
	}
	if len(result.projected) != 1 || result.projected[0].PaneUID != "pan-alpha-codex" {
		t.Fatalf("projected = %+v, want exactly the named Pane", result.projected)
	}
	if result.transactions != 1 {
		t.Fatalf("transactions = %d, want exactly one", result.transactions)
	}

	agent, ok := store.registry.Agent("agt-alpha-codex")
	if !ok {
		t.Fatal("the Agent did not survive its released Pane")
	}
	if agent.Status.Phase != coremetadata.PhaseOffline || agent.Status.PaneRef != "" {
		t.Fatalf("agent status = %+v, want Offline with no paneRef", agent.Status)
	}
	// The other dead Pane was not named and must be untouched, evidence included.
	other, ok := store.registry.Pane("pan-alpha-log")
	if !ok {
		t.Fatal("an unnamed Pane was deleted")
	}
	if other.Status.LastTermination != nil {
		t.Fatalf("an unnamed Pane gained evidence: %+v", other.Status.LastTermination)
	}
}

// TestAStaleGenerationEventOpensNoTransaction is the event-level generation
// guard.
//
// The producer quotes the materialization it observed. If a resume has already
// replaced it, the honest cost of that event is zero: no observation to act on,
// no transaction, no write.
func TestAStaleGenerationEventOpensNoTransaction(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	stampFixtureActivation(t, store, "pan-alpha-codex", "gen-current", "agt-alpha-codex")
	before := store.snapshot()
	inventory := &stubPaneInventory{uids: exitReconcileFixtureLiveExcept("pan-alpha-codex")}

	result, err := reconcileLifecycle(context.Background(),
		lifecycleDirtyEvent{paneUID: "pan-alpha-codex", generation: "gen-replaced"}, inventory, store.store())
	if err != nil {
		t.Fatalf("reconcileLifecycle: %v", err)
	}
	if len(result.projected) != 0 {
		t.Fatalf("projected = %+v, want nothing from a stale event", result.projected)
	}
	if store.writes != 0 {
		t.Fatalf("writes = %d, want a stale event to write nothing", store.writes)
	}
	agent, _ := store.registry.Agent("agt-alpha-codex")
	if agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.PaneRef != "pan-alpha-codex" {
		t.Fatalf("agent status = %+v, want the current binding untouched", agent.Status)
	}
	if store.snapshot() != before {
		t.Fatal("a stale generation event rewrote the registry")
	}
}

// TestARepeatReconciliationOpensNoTransaction is the cost half of acceptance
// criterion 2 at the seam.
//
// The first pass reconciles the death. The second must decide, from the read-only
// snapshot alone, that there is nothing left -- so it never takes the write lock.
// This is the property that lets both pane-exit hooks fire on every pane exit in
// every session without turning the registry file into a write amplifier.
func TestARepeatReconciliationOpensNoTransaction(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	resources := store.store()
	inventory := &stubPaneInventory{uids: exitReconcileFixtureLiveExcept("pan-alpha-codex", "pan-alpha-log")}

	if _, err := reconcileLifecycle(context.Background(), lifecycleDirtyEvent{}, inventory, resources); err != nil {
		t.Fatalf("first reconcileLifecycle: %v", err)
	}
	if store.transactions != 1 || store.writes != 1 {
		t.Fatalf("first pass: transactions = %d writes = %d, want one of each", store.transactions, store.writes)
	}
	settled := store.snapshot()

	result, err := reconcileLifecycle(context.Background(), lifecycleDirtyEvent{}, inventory, resources)
	if err != nil {
		t.Fatalf("second reconcileLifecycle: %v", err)
	}
	if result.transactions != 0 {
		t.Fatalf("second pass opened %d transactions, want zero", result.transactions)
	}
	if result.skipped == "" {
		t.Fatal("a write-free pass reported no reason for declining")
	}
	if store.transactions != 1 || store.writes != 1 {
		t.Fatalf("second pass: transactions = %d writes = %d, want the first pass's counts unchanged",
			store.transactions, store.writes)
	}
	if store.snapshot() != settled {
		t.Fatal("a repeat reconciliation changed the registry")
	}
}

// TestTheReconciliationObservesOneExactHostAndLeavesSiblingsAlone is acceptance
// criterion 4.
//
// Two servers are running. The event's server has lost the pane; the sibling has
// a live pane carrying the *same* mirrored uid, which is the shape a second
// isolated server or a stray copy of the same metadata produces. The projection
// must judge the event's server only, and the sibling must receive zero calls --
// not a read, not a write. A reconciliation that summed two servers could never
// report a death at all.
func TestTheReconciliationObservesOneExactHostAndLeavesSiblingsAlone(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		target explicitTmuxTarget
		other  explicitTmuxTarget
	}{
		{
			name:   "an explicit socket name",
			target: explicitTmuxTarget{flag: "-L", value: "event-host"},
			other:  explicitTmuxTarget{flag: "-L", value: "sibling-host"},
		},
		{
			name:   "an explicit socket path",
			target: explicitTmuxTarget{flag: "-S", value: "/tmp/event/socket"},
			other:  explicitTmuxTarget{flag: "-S", value: "/tmp/sibling/socket"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			// The event's host still mirrors every fixture Pane except the Agent's.
			eventHost := newFakeTmux()
			eventSession := eventHost.addSession("alpha")
			seedLiveWindow(t, eventHost, eventSession, "win-alpha-main", "pan-alpha-zsh")
			for _, uid := range []string{"pan-alpha-log", "pan-alpha-review", "pan-beta-zsh"} {
				seedLiveWindow(t, eventHost, eventSession, "win-"+uid, uid)
			}

			// The sibling mirrors the dead Pane's uid on a live pane of its own,
			// and reuses the same `%N` handle space.
			siblingHost := newFakeTmux()
			siblingSession := siblingHost.addSession("alpha")
			seedLiveWindow(t, siblingHost, siblingSession, "win-alpha-main", "pan-alpha-codex")

			runner := &routedTmuxRunner{servers: map[string]*fakeTmux{
				test.target.flag + "\x00" + test.target.value: eventHost,
				test.other.flag + "\x00" + test.other.value:   siblingHost,
			}}

			store := newFakeResourceStore(t)
			event := lifecycleDirtyEvent{target: test.target}
			result, err := reconcileLifecycle(context.Background(), event,
				lifecycleInventory(runner, test.target), store.store())
			if err != nil {
				t.Fatalf("reconcileLifecycle: %v", err)
			}
			if result.changed() == 0 {
				t.Fatalf("nothing was projected on the event host: %+v", result)
			}

			agent, ok := store.registry.Agent("agt-alpha-codex")
			if !ok {
				t.Fatal("the Agent did not survive")
			}
			if agent.Status.Phase != coremetadata.PhaseOffline || agent.Status.PaneRef != "" {
				t.Fatalf("agent status = %+v; the sibling's identical pane uid decided the outcome",
					agent.Status)
			}

			for _, call := range runner.calls {
				if call.flag == test.other.flag && call.value == test.other.value {
					t.Fatalf("the sibling server was addressed: %s %s %v", call.flag, call.value, call.args)
				}
				if call.flag != test.target.flag || call.value != test.target.value {
					t.Fatalf("an unexpected server was addressed: %s %s %v", call.flag, call.value, call.args)
				}
			}
			if len(runner.calls) == 0 {
				t.Fatal("the event host was never observed")
			}
			// The event describes the exact target it routed, so a diagnostic line
			// names the server the pass actually read.
			if !strings.Contains(event.describe(), test.target.label()) {
				t.Fatalf("event.describe() = %q, want it to name %q", event.describe(), test.target.label())
			}
		})
	}
}

// TestTheReconciliationNeverMutatesTmux is the forbidden-call audit.
//
// The projection's whole job is to write down what already happened. It may read
// the mirrored-uid inventory of one host and nothing else: a pass that respawned,
// split, killed, or attached anything would be taking an activation decision from
// an observation, which is exactly what the phase boundary forbids.
func TestTheReconciliationNeverMutatesTmux(t *testing.T) {
	t.Parallel()

	forbidden := []string{
		"split-window", "new-window", "new-session", "respawn-pane", "respawn-window",
		"kill-pane", "kill-window", "kill-session", "kill-server", "send-keys",
		"switch-client", "attach-session", "select-pane", "select-window", "set-option",
	}

	host := newFakeTmux()
	session := host.addSession("alpha")
	seedLiveWindow(t, host, session, "win-alpha-main", "pan-alpha-zsh")
	target := explicitTmuxTarget{flag: "-L", value: "audit-host"}
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{
		target.flag + "\x00" + target.value: host,
	}}

	store := newFakeResourceStore(t)
	if _, err := reconcileLifecycle(context.Background(), lifecycleDirtyEvent{target: target},
		lifecycleInventory(runner, target), store.store()); err != nil {
		t.Fatalf("reconcileLifecycle: %v", err)
	}
	if len(runner.calls) == 0 {
		t.Fatal("the audit observed no tmux call at all, so it proves nothing")
	}
	for _, call := range runner.calls {
		if len(call.args) == 0 {
			t.Fatalf("a tmux call carried no verb: %+v", call)
		}
		if slices.Contains(forbidden, call.args[0]) {
			t.Fatalf("the reconciliation issued a mutating tmux call: %v", call.args)
		}
	}
}

// TestAnUnreadableObservationReconcilesNothing is the fail-closed rule at the new
// seam.
//
// An observation that could not be taken is indistinguishable from one that found
// nothing, and reading it as empty would file an unknown termination against every
// managed Pane on a machine whose tmux server simply is not up. It is not an error
// either: the reconciliation rides along inside other operations.
func TestAnUnreadableObservationReconcilesNothing(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		inventory livePaneInventory
		wantCalls int
	}{
		{
			name:      "the preflight observation fails",
			inventory: &stubPaneInventory{err: errors.New("no server on /tmp/tmux-1000/absent")},
		},
		{
			name: "the locked observation fails",
			inventory: &sequencedPaneInventory{
				uids: []map[string]bool{exitReconcileFixtureLiveExcept("pan-alpha-codex")},
				errs: []error{nil, errors.New("the server went away mid-transaction")},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			store := newFakeResourceStore(t)
			before := store.snapshot()

			result, err := reconcileLifecycle(context.Background(), lifecycleDirtyEvent{},
				test.inventory, store.store())
			if err != nil {
				t.Fatalf("reconcileLifecycle = %v, want a silent no-op", err)
			}
			if result.skipped == "" {
				t.Fatal("a fail-closed pass reported no reason")
			}
			if len(result.projected) != 0 || result.transactions != 0 {
				t.Fatalf("result = %+v, want nothing projected and no transaction counted", result)
			}
			if store.writes != 0 {
				t.Fatalf("writes = %d, want an unreadable observation to write nothing", store.writes)
			}
			if store.snapshot() != before {
				t.Fatal("an unreadable observation rewrote the registry")
			}
		})
	}
}

// TestBothTriggerPathsClassifyTheSameDeathIdentically is the seam-level half of
// order independence.
//
// The reconciler's observation step and the standalone one-shot are two different
// entrypoints into the same transition body. If they could disagree, the phase an
// operator reads would depend on whether a hook fired before the next mutation
// route did.
func TestBothTriggerPathsClassifyTheSameDeathIdentically(t *testing.T) {
	t.Parallel()

	fileReceipt := func(t *testing.T, store *fakeResourceStore) {
		t.Helper()
		stampFixtureActivation(t, store, "pan-alpha-codex", "gen-current", "agt-alpha-codex")
		code := 42
		outcome, err := store.mutator().RecordTermination(&store.registry, coremetadata.TerminationEvidence{
			Source:         coremetadata.TerminationSourceSupervisor,
			Classification: coremetadata.TerminationAbnormal,
			PaneUID:        "pan-alpha-codex",
			AgentUID:       "agt-alpha-codex",
			Generation:     "gen-current",
			ExitCode:       &code,
		})
		if err != nil || !outcome.Applied {
			t.Fatalf("RecordTermination outcome = %+v err = %v", outcome, err)
		}
	}

	viaOneShot := newFakeResourceStore(t)
	fileReceipt(t, viaOneShot)
	if _, err := reconcileLifecycle(context.Background(), lifecycleDirtyEvent{},
		&stubPaneInventory{uids: exitReconcileFixtureLiveExcept("pan-alpha-codex")}, viaOneShot.store()); err != nil {
		t.Fatalf("one-shot reconcileLifecycle: %v", err)
	}

	viaReconciler := newFakeResourceStore(t)
	fileReceipt(t, viaReconciler)
	projectTerminations(&viaReconciler.registry, viaReconciler.mutator(),
		lifecycleProjectionTargets(viaReconciler.registry,
			exitReconcileFixtureLiveExcept("pan-alpha-codex"), lifecycleDirtyEvent{}))

	oneShot, _ := viaOneShot.registry.Agent("agt-alpha-codex")
	reconciled, _ := viaReconciler.registry.Agent("agt-alpha-codex")
	if oneShot.Status.Phase != coremetadata.PhaseFailed {
		t.Fatalf("one-shot phase = %q, want Failed for an exit 42", oneShot.Status.Phase)
	}
	if !reflect.DeepEqual(oneShot.Status.Phase, reconciled.Status.Phase) ||
		oneShot.Status.Reason != reconciled.Status.Reason {
		t.Fatalf("the two trigger paths disagreed: %+v vs %+v", oneShot.Status, reconciled.Status)
	}
	if oneShot.Status.LastTermination == nil || oneShot.Status.LastTermination.ExitCode == nil ||
		*oneShot.Status.LastTermination.ExitCode != 42 {
		t.Fatalf("evidence = %+v, want the supervised exit code preserved", oneShot.Status.LastTermination)
	}
}

// TestTheHookRouteReconcilesThroughTheSharedSeam pins that the hidden pane-exit
// route is the widest legal event rather than a second implementation.
//
// It also pins what the route still is: a whole-host pass, because
// `after-kill-pane` fires with an empty `#{hook_pane}` and there is nothing
// narrower to honestly say.
func TestTheHookRouteReconcilesThroughTheSharedSeam(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	inventory := &stubPaneInventory{uids: exitReconcileFixtureLiveExcept("pan-alpha-codex")}
	if err := runDeadAgentPaneSweep(context.Background(), inventory, store.store()); err != nil {
		t.Fatalf("runDeadAgentPaneSweep: %v", err)
	}
	agent, _ := store.registry.Agent("agt-alpha-codex")
	if agent.Status.Phase != coremetadata.PhaseOffline || agent.Status.PaneRef != "" {
		t.Fatalf("agent status = %+v, want the hook route to have projected the death", agent.Status)
	}
	if agent.Status.LastTermination == nil ||
		agent.Status.LastTermination.Source != coremetadata.TerminationSourceReconcile {
		t.Fatalf("evidence = %+v, want the reconciler's unknown receipt", agent.Status.LastTermination)
	}
}

// TestAMirroredInventoryDrivesTheProjection proves the observation seam is the
// real mirrored-uid read rather than a test-only stub.
//
// The Mirror is what writes `@projmux_pane_uid` in the first place, so reading the
// diff through it is what keeps the observation and the writes it is compared
// against from disagreeing about which Panes still have a transport binding.
func TestAMirroredInventoryDrivesTheProjection(t *testing.T) {
	t.Parallel()

	host := newFakeTmux()
	session := host.addSession("alpha")
	// Only the shell Pane is mirrored; the Agent's managed Pane is not.
	window := seedLiveWindow(t, host, session, "win-alpha-main", "pan-alpha-zsh")
	if window.panes[0].opts[tmuxopts.PaneUID] != "pan-alpha-zsh" {
		t.Fatalf("seeded pane uid = %q", window.panes[0].opts[tmuxopts.PaneUID])
	}

	uids, err := intmetadata.NewMirror(host).LivePaneUIDs(context.Background())
	if err != nil {
		t.Fatalf("LivePaneUIDs: %v", err)
	}
	if !uids["pan-alpha-zsh"] || uids["pan-alpha-codex"] {
		t.Fatalf("mirrored uids = %v, want only the seeded shell Pane", uids)
	}

	store := newFakeResourceStore(t)
	if _, err := reconcileLifecycle(context.Background(), lifecycleDirtyEvent{},
		intmetadata.NewMirror(host), store.store()); err != nil {
		t.Fatalf("reconcileLifecycle: %v", err)
	}
	agent, _ := store.registry.Agent("agt-alpha-codex")
	if agent.Status.Phase != coremetadata.PhaseOffline {
		t.Fatalf("agent phase = %q, want Offline from the mirrored diff", agent.Status.Phase)
	}
	shell, ok := store.registry.Pane("pan-alpha-zsh")
	if !ok {
		t.Fatal("the mirrored shell Pane was reconciled away")
	}
	if shell.Status.LastTermination != nil {
		t.Fatalf("a live shell Pane gained termination evidence: %+v", shell.Status.LastTermination)
	}
}
