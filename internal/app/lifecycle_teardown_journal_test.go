package app

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/diagnostics"
)

// lifecycle_teardown_journal_test.go pins the teardown decision journal at its
// app-layer consumption points. Every fixture runs a twin without a recorder
// and requires the identical decision result and Registry bytes: journaling is
// observation only.

type teardownJournal struct {
	recorder *diagnostics.TeardownRecorder
	store    *diagnostics.Store
}

func newTeardownJournal(t *testing.T) teardownJournal {
	t.Helper()
	store := diagnostics.NewStore(filepath.Join(t.TempDir(), "operations.jsonl"))
	return teardownJournal{
		recorder: diagnostics.NewLifecycleRecorder(store, "teardown-run", "1.0.0", "tmux").Teardown(),
		store:    store,
	}
}

func (j teardownJournal) decisions(t *testing.T) []diagnostics.Event {
	t.Helper()
	events, err := j.store.Read()
	if err != nil {
		t.Fatalf("read teardown journal: %v", err)
	}
	var out []diagnostics.Event
	for _, event := range events {
		if event.Event == "topology.teardown.decision" {
			out = append(out, event)
		}
	}
	return out
}

func requireTeardownDecision(t *testing.T, event diagnostics.Event, decision, code, classification, windowUID, paneUID string) {
	t.Helper()
	if event.Component != "topology" || event.Level != "info" || event.Result != "success" || event.RunID != "teardown-run" ||
		event.Decision != decision || event.Code != code || event.Classification != classification ||
		event.WindowUID != windowUID || event.PaneUID != paneUID {
		t.Fatalf("teardown decision = %+v, want %s/%s classification=%q window=%q pane=%q",
			event, decision, code, classification, windowUID, paneUID)
	}
}

// coreTeardownReasons returns every TeardownReason constant the core metadata
// package declares outside tests, read from source so a newly added reason is
// visible here before anyone remembers to map it.
func coreTeardownReasons(t *testing.T) []coremetadata.TeardownReason {
	t.Helper()
	dir := filepath.Join("..", "core", "metadata")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read core metadata source: %v", err)
	}
	fset := token.NewFileSet()
	var reasons []coremetadata.TeardownReason
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			value, ok := node.(*ast.ValueSpec)
			if !ok {
				return true
			}
			declared, _ := value.Type.(*ast.Ident)
			for i, ident := range value.Names {
				if i >= len(value.Values) {
					continue
				}
				expr := value.Values[i]
				typed := declared != nil && declared.Name == "TeardownReason"
				if call, ok := expr.(*ast.CallExpr); ok && len(call.Args) == 1 {
					if fun, ok := call.Fun.(*ast.Ident); ok && fun.Name == "TeardownReason" {
						expr, typed = call.Args[0], true
					}
				}
				if !typed {
					continue
				}
				literal, ok := expr.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					t.Fatalf("TeardownReason constant %s is not a string literal", ident.Name)
				}
				unquoted, err := strconv.Unquote(literal.Value)
				if err != nil {
					t.Fatalf("TeardownReason constant %s: %v", ident.Name, err)
				}
				reasons = append(reasons, coremetadata.TeardownReason(unquoted))
			}
			return true
		})
	}
	if len(reasons) == 0 {
		t.Fatal("core metadata declares no TeardownReason constants")
	}
	return reasons
}

// TestTeardownReasonJournalMappingCoversEveryCoreReason is the omission guard.
// Every core reason maps to exactly its prefixed code, the code round-trips
// through the real closed diagnostics allowlist, and the conflict error of
// every reason is recoverable without changing that error's bytes.
func TestTeardownReasonJournalMappingCoversEveryCoreReason(t *testing.T) {
	t.Parallel()
	reasons := coreTeardownReasons(t)
	if len(teardownReasonCodes) != len(reasons) {
		t.Fatalf("teardownReasonCodes has %d rows, core declares %d reasons: %v", len(teardownReasonCodes), len(reasons), reasons)
	}
	journal := newTeardownJournal(t)
	for _, reason := range reasons {
		code, ok := teardownReasonCodes[reason]
		if !ok {
			t.Fatalf("core TeardownReason %q has no journal code", reason)
		}
		if string(code) != "topology.teardown."+string(reason) {
			t.Fatalf("reason %q maps to %q", reason, code)
		}
		(&lifecycleTeardownRecord{action: coremetadata.TeardownRefuse, reason: reason}).journal(journal.recorder)

		conflict := stableDeadPaneAuthorityConflict(reason, "detail")
		if want := "stable dead Pane authority conflict (" + string(reason) + "): detail"; conflict.Error() != want {
			t.Fatalf("conflict bytes = %q, want %q", conflict.Error(), want)
		}
		if parsed, ok := stableDeadPaneAuthorityConflictReason(conflict); !ok || parsed != reason {
			t.Fatalf("conflict reason %q parsed as %q, %t", reason, parsed, ok)
		}
	}
	recorded := journal.decisions(t)
	if len(recorded) != len(reasons) {
		t.Fatalf("allowlist accepted %d of %d reason codes: %+v", len(recorded), len(reasons), recorded)
	}
	for i, reason := range reasons {
		requireTeardownDecision(t, recorded[i], "refuse", "topology.teardown."+string(reason), "", "", "")
	}

	actions := []coremetadata.TeardownAction{
		coremetadata.TeardownRetain, coremetadata.TeardownDeletePaneAgent,
		coremetadata.TeardownDeleteWindow, coremetadata.TeardownRefuse,
	}
	if len(teardownDecisionCodes) != len(actions) {
		t.Fatalf("teardownDecisionCodes = %v", teardownDecisionCodes)
	}
	for _, action := range actions {
		if decision, ok := teardownDecisionCodes[action]; !ok || string(decision) != string(action) {
			t.Fatalf("action %q maps to %q, %t", action, decision, ok)
		}
	}
	for _, err := range []error{
		nil, errors.New("reconcile lifecycle: the tmux pane inventory is not configured"),
		&lifecycleCleanupRetryError{Reason: coremetadata.TeardownReasonDeadPaneCleanupRetry, Err: errors.New("kill failed")},
		errors.New("stable dead Pane authority conflict (made-up): detail"),
	} {
		if reason, ok := stableDeadPaneAuthorityConflictReason(err); ok {
			t.Fatalf("non-conflict error %v parsed as %q", err, reason)
		}
	}
}

func unlinkOfPaneExit(event lifecycleDirtyEvent) lifecycleDirtyEvent {
	unlinked := event
	unlinked.teardownKind = coremetadata.TeardownEventWindowUnlinked
	unlinked.runtimePaneID = ""
	unlinked.runtimeSessionID = "$1"
	unlinked.runtimeWindowID = "@4"
	return unlinked
}

// requireTwinReconcile runs the same event against a journaled fixture and an
// unjournaled twin and requires identical results, errors, and Registry bytes.
func requireTwinReconcile(t *testing.T, label string,
	store *fakeResourceStore, inventory livePaneInventory, event lifecycleDirtyEvent,
	twinStore *fakeResourceStore, twinInventory livePaneInventory, twinEvent lifecycleDirtyEvent,
) (lifecycleReconcileResult, error) {
	t.Helper()
	result, err := reconcileLifecycle(context.Background(), event, inventory, store.store())
	twinResult, twinErr := reconcileLifecycle(context.Background(), twinEvent, twinInventory, twinStore.store())
	if (err == nil) != (twinErr == nil) || (err != nil && err.Error() != twinErr.Error()) {
		t.Fatalf("%s: journaled err=%v, twin err=%v", label, err, twinErr)
	}
	if !reflect.DeepEqual(result, twinResult) {
		t.Fatalf("%s: journaled result=%+v, twin result=%+v", label, result, twinResult)
	}
	if store.snapshot() != twinStore.snapshot() {
		t.Fatalf("%s: journaling changed Registry bytes", label)
	}
	return result, err
}

// Acceptance 1 and 4: a clean last-Pane pair journals its pending half once and
// the Window deletion exactly once, with the decision result and Registry bytes
// identical to an unjournaled twin.
func TestTeardownJournalCleanLastPanePairRecordsOneDeleteWindow(t *testing.T) {
	t.Parallel()
	journal := newTeardownJournal(t)
	store, inventory, event := prepareLastBetaProjectCascade(t)
	twinStore, twinInventory, twinEvent := prepareLastBetaProjectCascade(t)
	event.decisions = journal.recorder
	paneUID := event.receipts[0].PaneUID

	pending, err := requireTwinReconcile(t, "pane-exited", store, inventory, event, twinStore, twinInventory, twinEvent)
	if err != nil || len(pending.pending) != 1 {
		t.Fatalf("pending half = %+v, %v", pending, err)
	}
	recorded := journal.decisions(t)
	if len(recorded) != 1 {
		t.Fatalf("pane-exited journaled %d decisions, want 1 (candidate and locked evaluations collapse): %+v", len(recorded), recorded)
	}
	requireTeardownDecision(t, recorded[0], "delete-pane-agent", "topology.teardown.awaiting-window-unlink", "normal", "win-beta-main", paneUID)

	closed, err := requireTwinReconcile(t, "window-unlinked", store, inventory, unlinkOfPaneExit(event),
		twinStore, twinInventory, unlinkOfPaneExit(twinEvent))
	if err != nil || len(closed.rootCascaded) != 1 || closed.rootCascaded[0].DeletedWindows != 1 {
		t.Fatalf("Window close = %+v, %v", closed, err)
	}
	if _, ok := store.registry.Window("win-beta-main"); ok {
		t.Fatal("clean pair did not delete the Window")
	}
	recorded = journal.decisions(t)
	if len(recorded) != 2 {
		t.Fatalf("pair journaled %d decisions, want 2: %+v", len(recorded), recorded)
	}
	requireTeardownDecision(t, recorded[1], "delete-window", "topology.teardown.window-teardown", "normal", "win-beta-main", paneUID)

	// The duplicate unlink finds no stored evidence: it is awaiting transport
	// state, not a decision, and writes nothing.
	if _, err := requireTwinReconcile(t, "duplicate window-unlinked", store, inventory, unlinkOfPaneExit(event),
		twinStore, twinInventory, unlinkOfPaneExit(twinEvent)); err != nil {
		t.Fatal(err)
	}
	deletes := 0
	for _, event := range journal.decisions(t) {
		if event.Decision == "delete-window" {
			deletes++
		}
	}
	if got := len(journal.decisions(t)); got != 2 || deletes != 1 {
		t.Fatalf("after duplicate unlink decisions=%d delete-window=%d, want 2/1", got, deletes)
	}
}

func killedLastBetaProjectCascade(t *testing.T) (*fakeResourceStore, *exactPaneExitInventory, lifecycleDirtyEvent) {
	t.Helper()
	store, inventory, event := prepareLastBetaProjectCascade(t)
	event.receipts[0].Classification = coremetadata.TerminationKilled
	event.receipts[0].ExitCode = nil
	event.receipts[0].Signal = "HUP"
	return store, inventory, event
}

// Acceptance 2 and 4: a killed last Pane is non-causal. The retain decision is
// journaled, the owner chain survives, and a repeat delivery leaves the
// Registry byte-identical.
func TestTeardownJournalKilledLastPaneRecordsRetainAndKeepsRegistry(t *testing.T) {
	t.Parallel()
	journal := newTeardownJournal(t)
	store, inventory, event := killedLastBetaProjectCascade(t)
	twinStore, twinInventory, twinEvent := killedLastBetaProjectCascade(t)
	event.decisions = journal.recorder
	paneUID := event.receipts[0].PaneUID

	result, err := requireTwinReconcile(t, "killed pane-exited", store, inventory, event, twinStore, twinInventory, twinEvent)
	if err != nil || len(result.pending) != 0 || len(result.cascaded) != 0 || len(result.rootCascaded) != 0 || inventory.cleanups != 0 {
		t.Fatalf("killed last Pane = %+v, %v cleanups=%d", result, err, inventory.cleanups)
	}
	for _, uid := range []string{"proj-beta", "win-beta-main", paneUID, "agt-beta-codex"} {
		if _, ok := registryUIDs(store.registry)[uid]; !ok {
			t.Fatalf("killed last Pane lost %s", uid)
		}
	}
	recorded := journal.decisions(t)
	if len(recorded) != 1 {
		t.Fatalf("killed last Pane journaled %d decisions, want 1: %+v", len(recorded), recorded)
	}
	requireTeardownDecision(t, recorded[0], "retain", "topology.teardown.non-causal-termination", "killed", "win-beta-main", paneUID)

	before := store.snapshot()
	if _, err := requireTwinReconcile(t, "repeat killed pane-exited", store, inventory, event, twinStore, twinInventory, twinEvent); err != nil {
		t.Fatal(err)
	}
	if store.snapshot() != before {
		t.Fatal("repeat killed last-Pane decision changed the Registry")
	}
	recorded = journal.decisions(t)
	if len(recorded) != 2 {
		t.Fatalf("repeat journaled %d decisions, want 2: %+v", len(recorded), recorded)
	}
	requireTeardownDecision(t, recorded[1], "retain", "topology.teardown.non-causal-termination", "killed", "win-beta-main", paneUID)
}

// A dead Pane cleanup failure aborts the transaction after the planner decided.
// The authoritative outcome is the retained Registry, journaled once.
func TestTeardownJournalCleanupRetryRecordsRetainOnce(t *testing.T) {
	t.Parallel()
	journal := newTeardownJournal(t)
	store, inventory, event := prepareLastBetaProjectCascade(t)
	twinStore, twinInventory, twinEvent := prepareLastBetaProjectCascade(t)
	inventory.cleanupErr = errors.New("kill-pane failed")
	twinInventory.cleanupErr = errors.New("kill-pane failed")
	event.decisions = journal.recorder
	paneUID := event.receipts[0].PaneUID

	_, err := requireTwinReconcile(t, "cleanup retry", store, inventory, event, twinStore, twinInventory, twinEvent)
	var retry *lifecycleCleanupRetryError
	if !errors.As(err, &retry) {
		t.Fatalf("cleanup failure err = %v, want typed retry", err)
	}
	recorded := journal.decisions(t)
	if len(recorded) != 1 {
		t.Fatalf("cleanup retry journaled %d decisions, want 1: %+v", len(recorded), recorded)
	}
	requireTeardownDecision(t, recorded[0], "retain", "topology.teardown.exact-dead-pane-cleanup-retry", "normal", "win-beta-main", paneUID)
}

// A stable authority conflict is a refusal returned as an error. Its typed
// reason is journaled once, with the error bytes unchanged.
func TestTeardownJournalAuthorityConflictRecordsOneRefusal(t *testing.T) {
	t.Parallel()
	journal := newTeardownJournal(t)
	store, inventory, event := prepareLastBetaProjectCascade(t)
	twinStore, twinInventory, twinEvent := prepareLastBetaProjectCascade(t)
	event.generation, twinEvent.generation = "gen-replaced", "gen-replaced"
	event.decisions = journal.recorder
	paneUID := event.receipts[0].PaneUID
	before := store.snapshot()

	_, err := requireTwinReconcile(t, "stale generation", store, inventory, event, twinStore, twinInventory, twinEvent)
	if err == nil || !strings.HasPrefix(err.Error(), "stable dead Pane authority conflict (stale-generation): ") {
		t.Fatalf("stale generation err = %v", err)
	}
	if store.snapshot() != before {
		t.Fatal("authority conflict changed the Registry")
	}
	recorded := journal.decisions(t)
	if len(recorded) != 1 {
		t.Fatalf("authority conflict journaled %d decisions, want 1: %+v", len(recorded), recorded)
	}
	requireTeardownDecision(t, recorded[0], "refuse", "topology.teardown.stale-generation", "normal", "win-beta-main", paneUID)
}

// Acceptance 3 and 4: an unpaired window-unlinked records one retain when its
// own hook pass first waits, nothing while carried causal retries are transport
// state, one retain at exhaustion with the final pass subject, and nothing
// after. Every outcome equals an unjournaled twin's.
func TestTeardownJournalWindowUnlinkRecordsFirstWaitAndExhaustion(t *testing.T) {
	t.Parallel()
	journal := newTeardownJournal(t)
	subject := lifecycleTeardownSubject{windowUID: "win-abc234", paneUID: "pane-xyz567", classification: coremetadata.TerminationKilled}
	fixture := newTriggerFixture(t, controllerPassResult{awaitingPaneExit: true, awaitingSubject: subject})
	twin := newTriggerFixture(t, controllerPassResult{awaitingPaneExit: true, awaitingSubject: subject})
	twin.target = fixture.target
	fixture.runner.teardown = journal.recorder

	unlink := controllerTrigger{reason: controllerTriggerWindowUnlinked, target: fixture.target, session: "$1", hookWindow: "@4"}
	producer := controllerTrigger{reason: controllerTriggerPaneKilled, target: fixture.target}
	for invocation, trigger := range []controllerTrigger{unlink, producer, producer, producer, producer} {
		outcome, err := fixture.runner.run(context.Background(), trigger)
		twinOutcome, twinErr := twin.runner.run(context.Background(), trigger)
		if err != nil || twinErr != nil || !reflect.DeepEqual(outcome, twinOutcome) {
			t.Fatalf("invocation %d: journaled %s (%v), twin %s (%v)", invocation+1, outcome.describe(), err, twinOutcome.describe(), twinErr)
		}
		want := 1
		switch {
		case invocation < 3:
			if !strings.Contains(outcome.deferred, "window-unlinked is awaiting its causal pane-exited event") {
				t.Fatalf("invocation %d = %s, want a carried causal retry", invocation+1, outcome.describe())
			}
		case invocation == 3:
			want = 2
			if !outcome.converged {
				t.Fatalf("exhausting invocation = %s, want the unlink dropped and converged", outcome.describe())
			}
		default:
			want = 2
		}
		recorded := journal.decisions(t)
		if len(recorded) != want {
			t.Fatalf("after invocation %d journaled %d decisions, want %d: %+v", invocation+1, len(recorded), want, recorded)
		}
		for _, record := range recorded {
			requireTeardownDecision(t, record, "retain", "topology.teardown.awaiting-pane-exit", "killed", "win-abc234", "pane-xyz567")
		}
	}
	if !slices.ContainsFunc(fixture.triggers, func(trigger controllerTrigger) bool {
		return trigger.reason == controllerTriggerWindowUnlinked && trigger.retry == controllerTriggerMaxRetries
	}) {
		t.Fatalf("no pass reached the retry bound: %+v", fixture.triggers)
	}
}

// killWindowShapeFixture is the Registry a `tmux kill-window` of one managed
// shell Window leaves before any hook: an exact `$1/@4` Window binding, one
// shell Pane whose supervisor `killed` receipt is already absorbed, and no
// stored pane-exit teardown evidence. tmux fires only window-unlinked for it.
func killWindowShapeFixture(t *testing.T, ambiguous bool) (*fakeResourceStore, *exactPaneExitInventory, string, string) {
	t.Helper()
	store := newFakeResourceStore(t)
	mutator := store.mutator()
	mutator.NewUID = func(kind coremetadata.Kind) (string, error) {
		switch kind {
		case coremetadata.KindWindow:
			return "win-killshape", nil
		case coremetadata.KindPane:
			return "pane-killshape", nil
		}
		return "", errors.New("kill-window fixture mints only one Window and one Pane")
	}
	window, panes, err := mutator.AddWindow(&store.registry, "prj-alpha", coremetadata.BootstrapWindow{Name: "disposable"}, "", "op-kill-shape")
	if err != nil || len(panes) != 1 {
		t.Fatalf("add disposable shell Window = %+v, %+v, %v", window, panes, err)
	}
	paneUID := panes[0].Metadata.UID
	activateExactPane(t, store, paneUID, "", "gen-kill-shape", "%21")
	killed := phase2NormalReceipt(paneUID, "", "gen-kill-shape")
	killed.Classification, killed.ExitCode, killed.Signal = coremetadata.TerminationKilled, nil, "HUP"
	if _, err := absorbTerminationReceipts(&store.registry, store.mutator(), []coremetadata.TerminationEvidence{killed}); err != nil {
		t.Fatalf("absorb killed receipt: %v", err)
	}
	if pane, ok := store.registry.Pane(paneUID); !ok || pane.Status.LastTermination == nil ||
		pane.Status.LastTermination.Classification != coremetadata.TerminationKilled || pane.Status.Teardown != nil {
		t.Fatalf("kill-window shape Pane = %+v", pane)
	}
	if ambiguous {
		for i := range store.registry.Windows {
			if store.registry.Windows[i].Metadata.UID == "win-alpha-review" {
				store.registry.Windows[i].Status.RuntimeSessionID = "$1"
				store.registry.Windows[i].Status.RuntimeID = "@4"
			}
		}
	}
	live := map[string]bool{}
	for _, pane := range store.registry.Panes {
		if pane.Metadata.UID != paneUID {
			live[pane.Metadata.UID] = true
		}
	}
	liveWindows := map[string]bool{}
	for _, candidate := range store.registry.Windows {
		if candidate.Metadata.UID != window.Metadata.UID {
			liveWindows[candidate.Metadata.UID] = true
		}
	}
	inventory := &exactPaneExitInventory{uids: live, windows: liveWindows, windowSessions: map[string]int{"$1": 1}}
	return store, inventory, window.Metadata.UID, paneUID
}

// runKillWindowShapeUnlink drives the real controller convergence body for the
// hook's own window-unlinked event against the fixture.
func runKillWindowShapeUnlink(t *testing.T, tmux *fakeTmux, store *fakeResourceStore, inventory livePaneInventory,
	recorder *diagnostics.TeardownRecorder) controllerTriggerOutcome {
	t.Helper()
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
		reason: controllerTriggerWindowUnlinked, target: target, session: "$1", hookWindow: "@4",
	})
	if err != nil {
		t.Fatalf("kill-window shape unlink: %v", err)
	}
	return outcome
}

// Acceptance 4 and 5 shape: the most common retained-Window close journals its
// first awaiting decision with the exact Window UID, Pane UID, and killed
// classification, with outcome and Registry bytes equal to an unjournaled twin.
func TestTeardownJournalKillWindowShapeFirstWaitCarriesWindowPaneAndKilled(t *testing.T) {
	t.Parallel()
	journal := newTeardownJournal(t)
	store, inventory, windowUID, paneUID := killWindowShapeFixture(t, false)
	twinStore, twinInventory, _, _ := killWindowShapeFixture(t, false)
	before := store.snapshot()
	tmux := newFakeTmux()

	outcome := runKillWindowShapeUnlink(t, tmux, store, inventory, journal.recorder)
	twinOutcome := runKillWindowShapeUnlink(t, tmux, twinStore, twinInventory, nil)
	if !reflect.DeepEqual(outcome, twinOutcome) {
		t.Fatalf("journaled outcome %s, twin %s", outcome.describe(), twinOutcome.describe())
	}
	if !strings.Contains(outcome.deferred, "window-unlinked is awaiting its causal pane-exited event") {
		t.Fatalf("kill-window shape outcome = %s, want the causal wait", outcome.describe())
	}
	if store.snapshot() != twinStore.snapshot() || store.snapshot() != before {
		t.Fatal("kill-window shape unlink changed Registry bytes")
	}
	if _, ok := store.registry.Window(windowUID); !ok {
		t.Fatal("kill-window shape unlink deleted the retained Window")
	}
	recorded := journal.decisions(t)
	if len(recorded) != 1 {
		t.Fatalf("kill-window shape journaled %d decisions, want 1: %+v", len(recorded), recorded)
	}
	requireTeardownDecision(t, recorded[0], "retain", "topology.teardown.awaiting-pane-exit", "killed", windowUID, paneUID)
}

// Two Registry Windows carrying the same exact handles are ambiguous: the
// awaiting decision is still journaled, without guessing either UID.
func TestTeardownJournalAmbiguousUnlinkSubjectOmitsUIDs(t *testing.T) {
	t.Parallel()
	journal := newTeardownJournal(t)
	store, inventory, windowUID, _ := killWindowShapeFixture(t, true)
	twinStore, twinInventory, _, _ := killWindowShapeFixture(t, true)
	tmux := newFakeTmux()

	outcome := runKillWindowShapeUnlink(t, tmux, store, inventory, journal.recorder)
	twinOutcome := runKillWindowShapeUnlink(t, tmux, twinStore, twinInventory, nil)
	if !reflect.DeepEqual(outcome, twinOutcome) || store.snapshot() != twinStore.snapshot() {
		t.Fatalf("ambiguous journaled outcome %s, twin %s", outcome.describe(), twinOutcome.describe())
	}
	if _, ok := store.registry.Window(windowUID); !ok {
		t.Fatal("ambiguous unlink deleted a Window")
	}
	recorded := journal.decisions(t)
	if len(recorded) != 1 {
		t.Fatalf("ambiguous unlink journaled %d decisions, want 1: %+v", len(recorded), recorded)
	}
	requireTeardownDecision(t, recorded[0], "retain", "topology.teardown.awaiting-pane-exit", "", "", "")
}

// The subject resolver is a pure read: one Window with several Panes keeps its
// Window UID but omits the Pane UID and classification, and missing handles
// resolve nothing.
func TestLifecycleWindowUnlinkAwaitingSubjectOmitsAmbiguousPane(t *testing.T) {
	t.Parallel()
	store, _, windowUID, paneUID := killWindowShapeFixture(t, false)
	event := lifecycleDirtyEvent{runtimeSessionID: "$1", runtimeWindowID: "@4"}
	if got := lifecycleWindowUnlinkAwaitingSubject(store.registry, event); got.windowUID != windowUID || got.paneUID != paneUID ||
		got.classification != coremetadata.TerminationKilled {
		t.Fatalf("single-Pane subject = %+v", got)
	}
	if _, err := store.mutator().AddPane(&store.registry, windowUID, coremetadata.BootstrapPane{CWD: "/srv/alpha"}, "", "op-second-pane"); err != nil {
		t.Fatalf("add second Pane: %v", err)
	}
	before := store.snapshot()
	if got := lifecycleWindowUnlinkAwaitingSubject(store.registry, event); got.windowUID != windowUID || got.paneUID != "" || got.classification != "" {
		t.Fatalf("multi-Pane subject = %+v", got)
	}
	if store.snapshot() != before {
		t.Fatal("subject resolution changed the Registry")
	}
	for _, missing := range []lifecycleDirtyEvent{{runtimeWindowID: "@4"}, {runtimeSessionID: "$1"}, {runtimeSessionID: "$1", runtimeWindowID: "@999999"}} {
		if got := lifecycleWindowUnlinkAwaitingSubject(store.registry, missing); got != (lifecycleTeardownSubject{}) {
			t.Fatalf("subject for %+v = %+v, want none", missing, got)
		}
	}
}
