package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func TestLifecycleHookRoutesCarryExactPaneAndWindowEvidence(t *testing.T) {
	t.Parallel()

	socket := filepath.Join(t.TempDir(), "server.sock")
	recorder := &recordingTriggering{}
	cmd := &tmuxCommand{runner: newFakeTmux(), triggerRunner: recorder}
	for _, args := range [][]string{
		{"--socket-path", socket, "--reason", "pane-exited", "--hook-pane", "%9"},
		{"--socket-path", socket, "--session", "$1", "--reason", "pane-killed"},
		{"--socket-path", socket, "--session", "$1", "--reason", "window-unlinked", "--hook-window", "@7"},
	} {
		if err := cmd.runConverge(args, &bytes.Buffer{}); err != nil {
			t.Fatalf("runConverge(%v): %v", args, err)
		}
	}
	if len(recorder.triggers) != 3 || recorder.triggers[0].session != "" || recorder.triggers[0].hookPane != "%9" || recorder.triggers[0].hookWindow != "" ||
		recorder.triggers[1].hookPane != "" || recorder.triggers[1].hookWindow != "" ||
		recorder.triggers[2].hookWindow != "@7" {
		t.Fatalf("triggers = %+v", recorder.triggers)
	}
}

// TestHookWaitStatusReceiptDecisionTableIsClosed is the Phase 6
// hook x wait-status x control-receipt contract. Every row exercises the
// production source/classification guard and final projection rather than a
// duplicate test-only classifier.
func TestHookWaitStatusReceiptDecisionTableIsClosed(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name         string
		hook         controllerTriggerReason
		wait         *processOutcome
		control      bool
		want         coremetadata.TerminationClassification
		wantRetained bool
	}{
		{"pane-exited clean is normal", controllerTriggerPaneExited, &processOutcome{ExitCode: 0}, false, coremetadata.TerminationNormal, true},
		{"pane-exited nonzero is abnormal", controllerTriggerPaneExited, &processOutcome{ExitCode: 7}, false, coremetadata.TerminationAbnormal, true},
		{"after-kill-pane HUP is killed", controllerTriggerPaneKilled, &processOutcome{Signal: "HUP", SignalNumber: 1}, false, coremetadata.TerminationKilled, true},
		{"after-kill-pane paired control receipt is intentional", controllerTriggerPaneKilled, &processOutcome{Signal: "HUP", SignalNumber: 1}, true, coremetadata.TerminationIntentional, true},
		{"window-unlinked standalone HUP is killed", controllerTriggerWindowUnlinked, &processOutcome{Signal: "HUP", SignalNumber: 1}, false, coremetadata.TerminationKilled, true},
		{"window-unlinked after clean last pane stays normal", controllerTriggerWindowUnlinked, &processOutcome{ExitCode: 0}, false, coremetadata.TerminationNormal, true},
		{"window-unlinked paired control receipt is intentional", controllerTriggerWindowUnlinked, &processOutcome{Signal: "HUP", SignalNumber: 1}, true, coremetadata.TerminationIntentional, true},
		{"absence without wait status or receipt is unknown", controllerTriggerPaneKilled, nil, false, coremetadata.TerminationUnknown, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newFakeResourceStore(t)
			activatePaneFixture(t, store, "pan-alpha-codex", "agt-alpha-codex", "gen-table")
			mutator := store.mutator()
			if test.control {
				if _, err := mutator.RecordTermination(&store.registry, coremetadata.TerminationEvidence{
					Source: coremetadata.TerminationSourceControlAction, Classification: coremetadata.TerminationIntentional,
					PaneUID: "pan-alpha-codex", AgentUID: "agt-alpha-codex", Generation: "gen-table", OperationID: "op-delete",
				}); err != nil {
					t.Fatalf("control receipt: %v", err)
				}
			}
			if test.wait != nil {
				receipt := coremetadata.TerminationEvidence{
					Source:         coremetadata.TerminationSourceSupervisor,
					Classification: coremetadata.ClassifyProcessExit(test.wait.ExitCode, test.wait.Signal),
					PaneUID:        "pan-alpha-codex", AgentUID: "agt-alpha-codex", Generation: "gen-table",
				}
				if test.wait.Signal != "" {
					receipt.Signal = test.wait.Signal
				} else {
					code := test.wait.ExitCode
					receipt.ExitCode = &code
				}
				if _, err := mutator.RecordTermination(&store.registry, receipt); err != nil {
					t.Fatalf("supervisor receipt: %v", err)
				}
			}
			projection, err := mutator.ProjectTermination(&store.registry, coremetadata.TerminationProjectionInput{PaneUID: "pan-alpha-codex"})
			if err != nil {
				t.Fatalf("ProjectTermination: %v", err)
			}
			if projection.Classification != test.want || projection.PaneRetained != test.wantRetained {
				t.Fatalf("hook=%s projection=%+v, want classification=%s retained=%t",
					test.hook, projection, test.want, test.wantRetained)
			}
			if _, ok := store.registry.Pane("pan-alpha-codex"); !ok {
				t.Fatal("hook-driven termination deleted the Pane row")
			}
		})
	}
}

func TestWindowUnlinkedRecordsKillEvidenceWithoutPersistedRuntimeState(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	activatePaneFixture(t, store, "pan-alpha-log", "", "gen-window")
	activatePaneFixture(t, store, "pan-alpha-review", "", "gen-unrelated")
	journal := terminationJournal{path: filepath.Join(t.TempDir(), terminationJournalFile)}
	if err := journal.append(coremetadata.TerminationEvidence{
		Source: coremetadata.TerminationSourceSupervisor, Classification: coremetadata.TerminationKilled,
		PaneUID: "pan-alpha-log", Generation: "gen-window", Signal: "HUP",
	}); err != nil {
		t.Fatalf("append receipt: %v", err)
	}
	receipts, err := journal.read()
	if err != nil {
		t.Fatalf("read receipts: %v", err)
	}
	result, err := reconcileLifecycle(context.Background(), lifecycleDirtyEvent{
		target:          tmuxTransport{Kind: tmuxSocketPath, Value: "/tmp/phase6.sock", Source: tmuxSocketPathSource},
		runtimeWindowID: "@12", receipts: receipts,
	}, &stubPaneInventory{uids: map[string]bool{"pan-alpha-review": true}}, store.store())
	if err != nil {
		t.Fatalf("reconcileLifecycle: %v", err)
	}
	if got := (lifecycleDirtyEvent{runtimeWindowID: "@12"}).describe(); !strings.Contains(got, "hook-window=@12") {
		t.Fatalf("event description = %q, want exact hook Window evidence", got)
	}
	pane, _ := store.registry.Pane("pan-alpha-log")
	if pane.Status.LastTermination == nil || pane.Status.LastTermination.Classification != coremetadata.TerminationKilled {
		t.Fatalf("kill-window receipt = %+v, want killed", pane.Status.LastTermination)
	}
	unrelated, _ := store.registry.Pane("pan-alpha-review")
	if unrelated.Status.LastTermination != nil {
		t.Fatalf("unrelated Window Pane gained evidence: %+v", unrelated.Status.LastTermination)
	}
	if _, ok := store.registry.Pane("pan-alpha-log"); !ok {
		t.Fatal("window-unlinked deleted a Pane row")
	}
	found := false
	for _, projection := range result.projected {
		found = found || projection.PaneUID == "pan-alpha-log"
	}
	if !found {
		t.Fatalf("whole-host fresh projection = %+v, want killed Pane", result.projected)
	}
}

func TestSupervisorReceiptPrewriteDoesNotWaitForRegistryLock(t *testing.T) {
	t.Parallel()

	registryEntered := make(chan struct{}, 1)
	cmd := &superviseCommand{
		store: &resourceStore{update: func(func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
			registryEntered <- struct{}{}
			select {}
		}},
		journal: terminationJournal{path: filepath.Join(t.TempDir(), terminationJournalFile)},
		now:     func() time.Time { return resourceFixtureClock },
	}
	done := make(chan struct{})
	go func() {
		cmd.recordOutcome(superviseSpec{PaneUID: "pan-alpha-log", Generation: "gen-lock-free"}, processOutcome{Signal: "HUP"}, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("supervisor prewrite waited for the injected Registry lock path")
	}
	select {
	case <-registryEntered:
		t.Fatal("supervisor prewrite entered the Registry update path")
	default:
	}
	if receipts := readTestTerminationJournal(t, cmd); len(receipts) != 1 || receipts[0].Classification != coremetadata.TerminationKilled {
		t.Fatalf("journal receipts = %#v, want one killed receipt", receipts)
	}
}

func TestMalformedTerminationJournalRowCannotDisableConvergence(t *testing.T) {
	t.Parallel()

	journal := terminationJournal{path: filepath.Join(t.TempDir(), terminationJournalFile)}
	if err := os.WriteFile(journal.path, []byte("{partial"), 0o600); err != nil {
		t.Fatalf("write partial row: %v", err)
	}
	if err := journal.append(coremetadata.TerminationEvidence{
		Source: coremetadata.TerminationSourceSupervisor, Classification: coremetadata.TerminationKilled,
		PaneUID: "pan-alpha-log", Generation: "gen-current", Signal: "HUP",
	}); err != nil {
		t.Fatalf("append valid row: %v", err)
	}
	receipts, err := journal.read()
	if err != nil || len(receipts) != 1 || receipts[0].PaneUID != "pan-alpha-log" {
		t.Fatalf("read after malformed row = %#v, %v", receipts, err)
	}
}

func TestInvalidTerminationJournalRowCannotDisableLaterValidReceipt(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	activatePaneFixture(t, store, "pan-alpha-log", "", "gen-current")
	journal := terminationJournal{path: filepath.Join(t.TempDir(), terminationJournalFile)}
	if err := os.WriteFile(journal.path, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write well-formed invalid row: %v", err)
	}
	if err := journal.append(coremetadata.TerminationEvidence{
		Source: coremetadata.TerminationSourceSupervisor, Classification: coremetadata.TerminationKilled,
		PaneUID: "pan-alpha-log", Generation: "gen-current", Signal: "HUP",
	}); err != nil {
		t.Fatalf("append valid row: %v", err)
	}
	receipts, err := journal.read()
	if err != nil || len(receipts) != 2 {
		t.Fatalf("read receipts = %#v, %v; want invalid and valid rows", receipts, err)
	}
	if _, err := absorbTerminationReceipts(&store.registry, store.mutator(), receipts); err != nil {
		t.Fatalf("absorb after invalid row: %v", err)
	}
	pane, _ := store.registry.Pane("pan-alpha-log")
	if pane.Status.LastTermination == nil || pane.Status.LastTermination.Classification != coremetadata.TerminationKilled {
		t.Fatalf("later valid receipt = %+v, want killed", pane.Status.LastTermination)
	}
}

func TestPaneExitedAbsorbsLateSupervisorReceiptAfterUnknownProjection(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name           string
		classification coremetadata.TerminationClassification
		exitCode       *int
		signal         string
		wantPhase      coremetadata.AgentPhase
		wantReason     string
	}{
		{name: "normal", classification: coremetadata.TerminationNormal, exitCode: func() *int { code := 0; return &code }(), wantPhase: coremetadata.PhaseOffline, wantReason: coremetadata.TerminationReasonNormal},
		{name: "abnormal", classification: coremetadata.TerminationAbnormal, exitCode: func() *int { code := 7; return &code }(), wantPhase: coremetadata.PhaseFailed, wantReason: coremetadata.TerminationReasonAbnormal},
		{name: "killed", classification: coremetadata.TerminationKilled, signal: "HUP", wantPhase: coremetadata.PhaseOffline, wantReason: coremetadata.TerminationReasonKilled},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newFakeResourceStore(t)
			activatePaneFixture(t, store, "pan-alpha-codex", "agt-alpha-codex", "gen-fast-exit")
			mutator := store.mutator()
			if _, err := mutator.ObservePaneActivationRuntime(&store.registry, "pan-alpha-codex", "gen-fast-exit", "%9"); err != nil {
				t.Fatalf("observe runtime handle: %v", err)
			}
			if projection, err := mutator.ProjectTermination(&store.registry, coremetadata.TerminationProjectionInput{PaneUID: "pan-alpha-codex"}); err != nil || projection.Classification != coremetadata.TerminationUnknown {
				t.Fatalf("early runtime-created projection = %+v, %v; want unknown", projection, err)
			}
			agent, _ := store.registry.Agent("agt-alpha-codex")
			if agent.Status.PaneRef != "" {
				t.Fatalf("early unknown left paneRef %q, want released binding", agent.Status.PaneRef)
			}
			receipt := coremetadata.TerminationEvidence{
				Source: coremetadata.TerminationSourceSupervisor, Classification: test.classification,
				ObservedAt: resourceFixtureClock, PaneUID: "pan-alpha-codex", AgentUID: "agt-alpha-codex",
				Generation: "gen-fast-exit", ExitCode: test.exitCode, Signal: test.signal,
			}
			event := lifecycleDirtyEvent{runtimePaneID: "%9", receipts: []coremetadata.TerminationEvidence{receipt}}
			inventory := &stubPaneInventory{uids: map[string]bool{}}
			first, err := reconcileLifecycle(context.Background(), event, inventory, store.store())
			if err != nil {
				t.Fatalf("late pane-exited receipt: %v", err)
			}
			pane, _ := store.registry.Pane("pan-alpha-codex")
			agent, _ = store.registry.Agent("agt-alpha-codex")
			if first.transactions != 1 || pane.Status.LastTermination == nil || agent.Status.LastTermination == nil ||
				pane.Status.LastTermination.Classification != test.classification || agent.Status.LastTermination.Classification != test.classification ||
				pane.Status.LastTermination.Source != coremetadata.TerminationSourceSupervisor || agent.Status.PaneRef != "" ||
				agent.Status.Phase != test.wantPhase || agent.Status.Reason != test.wantReason {
				t.Fatalf("late receipt result=%+v pane=%+v agent=%+v", first, pane.Status.LastTermination, agent.Status)
			}
			second, err := reconcileLifecycle(context.Background(), event, inventory, store.store())
			if err != nil {
				t.Fatalf("repeat late receipt: %v", err)
			}
			if second.transactions != 0 {
				t.Fatalf("repeat transactions = %d, want zero", second.transactions)
			}
		})
	}
}

func TestPaneKilledWaitsPastHookDelayForSupervisorReceipt(t *testing.T) {
	store := newFakeResourceStore(t)
	activatePaneFixture(t, store, "pan-alpha-codex", "agt-alpha-codex", "gen-delayed-kill")
	journal := terminationJournal{path: filepath.Join(t.TempDir(), terminationJournalFile)}
	waiting := make(chan struct{})
	appendDone := make(chan error, 1)
	runner := &controllerTriggerRunner{
		store: store.store(), receipts: journal,
		observe: func(tmuxTransport) livePaneInventory {
			return &stubPaneInventory{uids: map[string]bool{}}
		},
		receiptWaitTimeout: 300 * time.Millisecond,
		receiptPoll:        10 * time.Millisecond,
		beforeReceiptWait:  func() { close(waiting) },
	}
	go func() {
		<-waiting
		// Deliberately later than the generated hook's 50ms delay.
		time.Sleep(75 * time.Millisecond)
		appendDone <- journal.append(coremetadata.TerminationEvidence{
			Source: coremetadata.TerminationSourceSupervisor, Classification: coremetadata.TerminationKilled,
			ObservedAt: resourceFixtureClock, PaneUID: "pan-alpha-codex", AgentUID: "agt-alpha-codex",
			Generation: "gen-delayed-kill", Signal: "HUP",
		})
	}()
	trigger := controllerTrigger{
		reason: controllerTriggerPaneKilled,
		target: tmuxTransport{Kind: tmuxSocketPath, Value: "/tmp/phase6-delayed-kill.sock", Source: tmuxSocketPathSource},
	}
	started := time.Now()
	receipts, err := runner.awaitRuntimeExitTerminationReceipts(context.Background(), trigger, nil)
	if err != nil {
		t.Fatalf("await delayed receipt: %v", err)
	}
	if err := <-appendDone; err != nil {
		t.Fatalf("append delayed receipt: %v", err)
	}
	if elapsed := time.Since(started); elapsed < 50*time.Millisecond {
		t.Fatalf("wait returned after %s, before the delayed supervisor append", elapsed)
	}
	if len(receipts) != 1 || receipts[0].Classification != coremetadata.TerminationKilled {
		t.Fatalf("receipts = %+v, want delayed killed receipt", receipts)
	}
	if _, err := reconcileLifecycle(context.Background(), lifecycleDirtyEvent{receipts: receipts},
		&stubPaneInventory{uids: map[string]bool{}}, store.store()); err != nil {
		t.Fatalf("project delayed kill: %v", err)
	}
	agent, _ := store.registry.Agent("agt-alpha-codex")
	pane, _ := store.registry.Pane("pan-alpha-codex")
	if agent.Status.Phase != coremetadata.PhaseOffline || agent.Status.PaneRef != "" ||
		agent.Status.LastTermination == nil || agent.Status.LastTermination.Classification != coremetadata.TerminationKilled ||
		pane.Status.LastTermination == nil || pane.Status.LastTermination.Classification != coremetadata.TerminationKilled {
		t.Fatalf("delayed kill Agent=%+v Pane=%+v", agent.Status, pane.Status)
	}
}

func TestPaneKilledReceiptWaitIsBoundedBeforeUnknownProjection(t *testing.T) {
	store := newFakeResourceStore(t)
	activatePaneFixture(t, store, "pan-alpha-codex", "agt-alpha-codex", "gen-no-receipt")
	runner := &controllerTriggerRunner{
		store: store.store(), receipts: terminationJournal{path: filepath.Join(t.TempDir(), terminationJournalFile)},
		observe: func(tmuxTransport) livePaneInventory {
			return &stubPaneInventory{uids: map[string]bool{}}
		},
		receiptWaitTimeout: 70 * time.Millisecond,
		receiptPoll:        10 * time.Millisecond,
	}
	trigger := controllerTrigger{
		reason: controllerTriggerPaneKilled,
		target: tmuxTransport{Kind: tmuxSocketPath, Value: "/tmp/phase6-no-receipt.sock", Source: tmuxSocketPathSource},
	}
	started := time.Now()
	receipts, err := runner.awaitRuntimeExitTerminationReceipts(context.Background(), trigger, nil)
	elapsed := time.Since(started)
	if err != nil || len(receipts) != 0 {
		t.Fatalf("bounded wait receipts = %+v, %v", receipts, err)
	}
	if elapsed < 50*time.Millisecond || elapsed > 500*time.Millisecond {
		t.Fatalf("bounded wait elapsed = %s, want 50ms..500ms", elapsed)
	}
	result, err := reconcileLifecycle(context.Background(), lifecycleDirtyEvent{receipts: receipts},
		&stubPaneInventory{uids: map[string]bool{}}, store.store())
	if err != nil {
		t.Fatalf("project receipt-free kill: %v", err)
	}
	agent, _ := store.registry.Agent("agt-alpha-codex")
	pane, _ := store.registry.Pane("pan-alpha-codex")
	if result.transactions != 1 || agent.Status.LastTermination == nil ||
		agent.Status.LastTermination.Classification != coremetadata.TerminationUnknown ||
		pane.Status.LastTermination == nil || pane.Status.LastTermination.Classification != coremetadata.TerminationUnknown {
		t.Fatalf("receipt-free result=%+v Agent=%+v Pane=%+v", result, agent.Status, pane.Status)
	}
}

func TestPaneKilledWaitRefinesRacingUnknownWithDelayedSupervisorReceipt(t *testing.T) {
	store := newFakeResourceStore(t)
	activatePaneFixture(t, store, "pan-alpha-codex", "agt-alpha-codex", "gen-racing-unknown")
	if projection, err := store.mutator().ProjectTermination(&store.registry,
		coremetadata.TerminationProjectionInput{PaneUID: "pan-alpha-codex"}); err != nil ||
		projection.Classification != coremetadata.TerminationUnknown {
		t.Fatalf("preseed unknown = %+v, %v", projection, err)
	}
	journal := terminationJournal{path: filepath.Join(t.TempDir(), terminationJournalFile)}
	waiting := make(chan struct{})
	appendDone := make(chan error, 1)
	runner := &controllerTriggerRunner{
		store: store.store(), receipts: journal,
		observe: func(tmuxTransport) livePaneInventory {
			return &stubPaneInventory{uids: map[string]bool{}}
		},
		receiptWaitTimeout: 300 * time.Millisecond,
		receiptPoll:        10 * time.Millisecond,
		beforeReceiptWait:  func() { close(waiting) },
	}
	go func() {
		<-waiting
		time.Sleep(75 * time.Millisecond)
		appendDone <- journal.append(coremetadata.TerminationEvidence{
			Source: coremetadata.TerminationSourceSupervisor, Classification: coremetadata.TerminationKilled,
			ObservedAt: resourceFixtureClock, PaneUID: "pan-alpha-codex", AgentUID: "agt-alpha-codex",
			Generation: "gen-racing-unknown", Signal: "HUP",
		})
	}()
	trigger := controllerTrigger{
		reason: controllerTriggerPaneKilled,
		target: tmuxTransport{Kind: tmuxSocketPath, Value: "/tmp/phase6-racing-unknown.sock", Source: tmuxSocketPathSource},
	}
	receipts, err := runner.awaitRuntimeExitTerminationReceipts(context.Background(), trigger, nil)
	if err != nil {
		t.Fatalf("await racing unknown refinement: %v", err)
	}
	if err := <-appendDone; err != nil {
		t.Fatalf("append racing unknown refinement: %v", err)
	}
	if _, err := reconcileLifecycle(context.Background(), lifecycleDirtyEvent{receipts: receipts},
		&stubPaneInventory{uids: map[string]bool{}}, store.store()); err != nil {
		t.Fatalf("refine racing unknown: %v", err)
	}
	agent, _ := store.registry.Agent("agt-alpha-codex")
	if agent.Status.LastTermination == nil || agent.Status.LastTermination.Classification != coremetadata.TerminationKilled ||
		agent.Status.Reason != coremetadata.TerminationReasonKilled {
		t.Fatalf("racing unknown Agent = %+v, want killed refinement", agent.Status)
	}
}

func TestExactPaneExitedReceiptWaitIsBoundedBeforeUnknownProjection(t *testing.T) {
	store := newFakeResourceStore(t)
	activatePaneFixture(t, store, "pan-alpha-codex", "agt-alpha-codex", "gen-voluntary")
	if _, err := store.mutator().ObservePaneActivationRuntime(&store.registry,
		"pan-alpha-codex", "gen-voluntary", "%9"); err != nil {
		t.Fatalf("observe exact Pane: %v", err)
	}
	runner := &controllerTriggerRunner{
		store: store.store(), receipts: terminationJournal{path: filepath.Join(t.TempDir(), terminationJournalFile)},
		observe: func(tmuxTransport) livePaneInventory {
			return &stubPaneInventory{uids: map[string]bool{}}
		},
		beforeReceiptWait: func() {}, receiptWaitTimeout: time.Millisecond, receiptPoll: 100 * time.Microsecond,
	}
	pass, err := runner.converge(context.Background(), controllerTrigger{
		reason: controllerTriggerPaneExited, hookPane: "%9", hookWindow: "@4",
		target: tmuxTransport{Kind: tmuxSocketPath, Value: "/tmp/phase6-voluntary.sock", Source: tmuxSocketPathSource},
	})
	if err != nil {
		t.Fatalf("exact pane-exited: %v", err)
	}
	if pass.residualExits != 1 {
		t.Fatalf("exact pane-exited pass = %+v, want bounded exact unknown projection", pass)
	}
}

func TestExactDeadPaneReceiptWaitUsesLiveMinusDeadObservation(t *testing.T) {
	store := newFakeResourceStore(t)
	activateExactPane(t, store, "pan-alpha-codex", "agt-alpha-codex", "gen-dead-wait", "%9")
	journal := terminationJournal{path: filepath.Join(t.TempDir(), terminationJournalFile)}
	waiting := make(chan struct{})
	appendDone := make(chan error, 1)
	runner := &controllerTriggerRunner{
		store: store.store(), receipts: journal,
		observe: func(tmuxTransport) livePaneInventory {
			return &exactPaneExitInventory{
				uids: map[string]bool{"pan-alpha-codex": true},
				dead: map[string]bool{"pan-alpha-codex": true},
			}
		},
		receiptWaitTimeout: 300 * time.Millisecond,
		receiptPoll:        10 * time.Millisecond,
		beforeReceiptWait:  func() { close(waiting) },
	}
	go func() {
		<-waiting
		time.Sleep(40 * time.Millisecond)
		appendDone <- journal.append(phase2NormalReceipt("pan-alpha-codex", "agt-alpha-codex", "gen-dead-wait"))
	}()
	receipts, err := runner.awaitRuntimeExitTerminationReceipts(context.Background(), controllerTrigger{
		reason: controllerTriggerPaneExited, hookPane: "%9",
		target: tmuxTransport{Kind: tmuxSocketPath, Value: "/tmp/phase0-dead-wait.sock", Source: tmuxSocketPathSource},
	}, nil)
	if err != nil {
		t.Fatalf("wait for dead mirrored Pane receipt: %v", err)
	}
	if err := <-appendDone; err != nil {
		t.Fatalf("append dead mirrored Pane receipt: %v", err)
	}
	if len(receipts) != 1 || receipts[0].Classification != coremetadata.TerminationNormal ||
		receipts[0].Source != coremetadata.TerminationSourceSupervisor {
		t.Fatalf("dead mirrored Pane receipts = %+v", receipts)
	}
}
