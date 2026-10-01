package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
)

type recordedCodexBrokerRefusal struct {
	operation diagnostics.CodexBrokerOperation
	reason    codexbroker.Refusal
}

// codexBrokerRefusalTally stands in for the journal on one control epoch.
type codexBrokerRefusalTally struct {
	mu      sync.Mutex
	records []recordedCodexBrokerRefusal
}

func (t *codexBrokerRefusalTally) record(operation diagnostics.CodexBrokerOperation, err error) {
	var refusal *codexbroker.BrokerError
	if !errors.As(err, &refusal) {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.records = append(t.records, recordedCodexBrokerRefusal{operation, refusal.Refusal})
}

func (t *codexBrokerRefusalTally) snapshot() []recordedCodexBrokerRefusal {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.records)
}

// failingEventWriter is a journal that refuses every append.
type failingEventWriter struct {
	mu      sync.Mutex
	appends int
}

func (w *failingEventWriter) Append(diagnostics.Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.appends++
	return errors.New("fixture journal unavailable")
}

func (w *failingEventWriter) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.appends
}

// A refusal the delivery absorbs by waiting out the one-second window is still
// a refusal the observer received, so it is journaled once per receipt.
func TestCodexControlEpochJournalsTheLifecycleRetryItAbsorbs(t *testing.T) {
	t.Parallel()
	identity := phase6Identity()
	active := activeWithThreadState(codexappserver.ThreadStateActive)
	retry := &codexbroker.BrokerError{Refusal: codexbroker.RefusalLifecycleRetry}
	wire := &scriptedLifecycleWire{
		fakeExactControlWire: &fakeExactControlWire{},
		snapshots:            []codexappserver.LifecycleSnapshot{{}, {}, active},
		errors:               []error{retry, retry, nil},
	}
	epoch := newCodexControlEpoch(wire, identity, "epoch-1", active, func(codexLifecycleIdentity) bool { return true })
	tally := &codexBrokerRefusalTally{}
	epoch.recordRefusal = tally.record
	epoch.retryWait = func(context.Context) error { return nil }
	request := agentControlRequest{Operation: agentControlOpDeliver, Identity: identity, Epoch: "epoch-1", Text: "input"}
	first := epoch.Handle(t.Context(), request)
	second := epoch.Handle(t.Context(), request)
	if first.OK || first.Code != string(codexbroker.RefusalLifecycleRetry) || !second.OK {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	want := []recordedCodexBrokerRefusal{
		{diagnostics.CodexBrokerOperationLifecycleRead, codexbroker.RefusalLifecycleRetry},
		{diagnostics.CodexBrokerOperationLifecycleRead, codexbroker.RefusalLifecycleRetry},
	}
	if got := tally.snapshot(); !slices.Equal(got, want) {
		t.Fatalf("refusals = %+v, want %+v", got, want)
	}
}

func TestCodexControlEpochJournalsEachWireRefusalUnderItsOperation(t *testing.T) {
	t.Parallel()
	identity := phase6Identity()
	idle := codexappserver.LifecycleSnapshot{ThreadID: identity.ThreadID, ThreadState: codexappserver.ThreadStateIdle}
	active := activeWithThreadState(codexappserver.ThreadStateActive)
	drain := &codexbroker.BrokerError{Refusal: codexbroker.RefusalDrainRequired}
	stale := &codexbroker.BrokerError{Refusal: codexbroker.RefusalStaleBindingEpoch}
	tests := []struct {
		name     string
		wire     *fakeExactControlWire
		snapshot codexappserver.LifecycleSnapshot
		op       string
		want     []recordedCodexBrokerRefusal
	}{
		{"start read drained", &fakeExactControlWire{snapshot: idle, snapshotErr: drain}, idle, agentControlOpStart,
			[]recordedCodexBrokerRefusal{{diagnostics.CodexBrokerOperationLifecycleRead, codexbroker.RefusalDrainRequired}}},
		{"start write refused", &fakeExactControlWire{snapshot: idle, err: stale}, idle, agentControlOpStart,
			[]recordedCodexBrokerRefusal{{diagnostics.CodexBrokerOperationTurnStart, codexbroker.RefusalStaleBindingEpoch}}},
		{"steer write refused", &fakeExactControlWire{snapshot: active, err: stale}, active, agentControlOpSteer,
			[]recordedCodexBrokerRefusal{{diagnostics.CodexBrokerOperationTurnSteer, codexbroker.RefusalStaleBindingEpoch}}},
		{"deliver steer refused", &fakeExactControlWire{snapshot: active, err: stale}, active, agentControlOpDeliver,
			[]recordedCodexBrokerRefusal{{diagnostics.CodexBrokerOperationTurnSteer, codexbroker.RefusalStaleBindingEpoch}}},
		{"interrupt refused", &fakeExactControlWire{err: stale}, active, agentControlOpInterrupt,
			[]recordedCodexBrokerRefusal{{diagnostics.CodexBrokerOperationTurnInterrupt, codexbroker.RefusalStaleBindingEpoch}}},
		// An untyped wire error carries no closed reason and records nothing.
		{"untyped error", &fakeExactControlWire{snapshot: active, err: errors.New("provider said no")}, active, agentControlOpSteer, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			epoch := newCodexControlEpoch(tt.wire, identity, "epoch-1", tt.snapshot, func(codexLifecycleIdentity) bool { return true })
			tally := &codexBrokerRefusalTally{}
			epoch.recordRefusal = tally.record
			response := epoch.Handle(t.Context(), agentControlRequest{Operation: tt.op, Identity: identity, Epoch: "epoch-1", Text: "input"})
			// A drained read falls back to the tracked idle state and the start
			// still goes ahead; every other case is refused.
			if response.OK != (tt.name == "start read drained") {
				t.Fatalf("response = %+v", response)
			}
			if got := tally.snapshot(); !slices.Equal(got, tt.want) {
				t.Fatalf("refusals = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestCodexControlEpochJournalsARefusedApprovalAnswer(t *testing.T) {
	t.Parallel()
	identity := phase6Identity()
	stale := &codexbroker.BrokerError{Refusal: codexbroker.RefusalLeaseIdentityMismatch}
	wire := &fakeExactControlWire{err: stale}
	epoch := newCodexControlEpoch(wire, identity, "epoch-1", codexappserver.LifecycleSnapshot{ThreadID: "thread-1",
		ThreadState: codexappserver.ThreadStateWaitingOnApproval, TurnID: "turn-1", TurnState: codexappserver.TurnStateInProgress},
		func(codexLifecycleIdentity) bool { return true })
	tally := &codexBrokerRefusalTally{}
	epoch.recordRefusal = tally.record
	approval := codexappserver.Notification{
		Method: "item/commandExecution/requestApproval", RequestID: "7", RawRequestID: json.RawMessage(`7`),
		Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","startedAtMs":1,"command":"make test","cwd":"/work","availableDecisions":["accept"]}`),
	}
	if err := epoch.ApplyNotification(approval); err != nil {
		t.Fatal(err)
	}
	response := epoch.Handle(t.Context(), agentControlRequest{Operation: agentControlOpReview, Identity: identity, Epoch: "epoch-1",
		RequestKey: "7", Decision: string(codexappserver.DecisionAccept)})
	want := []recordedCodexBrokerRefusal{{diagnostics.CodexBrokerOperationApprovalAnswer, codexbroker.RefusalLeaseIdentityMismatch}}
	if response.OK || !slices.Equal(tally.snapshot(), want) {
		t.Fatalf("response=%+v refusals=%+v", response, tally.snapshot())
	}
}

// A journal that refuses every append changes nothing about the control
// response: the same refusal comes back with or without it.
func TestCodexControlEpochResponseIsTheSameWhenTheJournalFails(t *testing.T) {
	t.Parallel()
	identity := phase6Identity()
	active := activeWithThreadState(codexappserver.ThreadStateActive)
	drain := &codexbroker.BrokerError{Refusal: codexbroker.RefusalDrainRequired}
	handle := func(record func(diagnostics.CodexBrokerOperation, error)) agentControlResponse {
		wire := &fakeExactControlWire{snapshot: active, snapshotErr: drain, err: &codexbroker.BrokerError{Refusal: codexbroker.RefusalStaleBindingEpoch}}
		epoch := newCodexControlEpoch(wire, identity, "epoch-1", active, func(codexLifecycleIdentity) bool { return true })
		epoch.recordRefusal = record
		return epoch.Handle(t.Context(), agentControlRequest{Operation: agentControlOpSteer, Identity: identity, Epoch: "epoch-1", Text: "input"})
	}
	writer := &failingEventWriter{}
	recorder := diagnostics.NewLifecycleRecorder(writer, "run", "1.0.0", "tmux").CodexBroker()
	failing := handle(func(operation diagnostics.CodexBrokerOperation, err error) {
		var refusal *codexbroker.BrokerError
		if errors.As(err, &refusal) {
			recorder.RecordRefusal(diagnostics.CodexBrokerRefusal{Role: diagnostics.CodexBrokerRoleObserver, Operation: operation, Reason: string(refusal.Refusal)})
		}
	})
	without := handle(nil)
	if !reflect.DeepEqual(failing, without) || failing.OK || writer.count() != 2 {
		t.Fatalf("with failing journal=%+v without=%+v appends=%d", failing, without, writer.count())
	}
}

// The probe end to end: an empty state domain with --no-start writes exactly
// one host-unavailable record, and nothing about it names the domain. Ensure
// refuses before any dial here, so the record carries no dial stage.
// Not parallel: it installs the process-wide recorder.
func TestCodexBrokerProbeNoStartJournalsHostUnavailable(t *testing.T) {
	domain := newBrokerStateDomain(t)
	store := diagnostics.NewStore(filepath.Join(t.TempDir(), "operations.jsonl"))
	recorder := diagnostics.NewLifecycleRecorder(store, "probe-run", "1.0.0", "tmux")
	args := []string{"internal", "codex-broker", "probe", "--no-start", "--state-domain", domain}
	var stdout, stderr bytes.Buffer
	err := RunWithLifecycleDiagnostics(args, &stdout, &stderr, recorder)
	if err == nil || err.Error() != "reach codex broker runtime: host-unavailable" {
		t.Fatalf("probe err = %v", err)
	}
	events, readErr := store.Read()
	if readErr != nil {
		t.Fatal(readErr)
	}
	var refusals []diagnostics.Event
	for _, event := range events {
		if event.Event == "codex.broker.refusal" {
			refusals = append(refusals, event)
		}
	}
	if len(refusals) != 1 {
		t.Fatalf("refusal records = %+v", events)
	}
	got := refusals[0]
	if got.Component != "codex-broker" || got.Source != "probe" || got.Operation != "ensure" || got.Code != "host-unavailable" ||
		got.RunID != "probe-run" || got.Message != "" {
		t.Fatalf("refusal = %+v", got)
	}
	if codexBrokerRefusalJournal.Load() != nil {
		t.Fatal("the invocation left its recorder installed")
	}
}

// A failing journal leaves the probe's error, and so its exit code, as they
// are without one. Not parallel: it installs the process-wide recorder.
func TestCodexBrokerProbeResultIsTheSameWhenTheJournalFails(t *testing.T) {
	domain := newBrokerStateDomain(t)
	args := []string{"internal", "codex-broker", "probe", "--no-start", "--state-domain", domain}
	writer := &failingEventWriter{}
	failingErr := RunWithLifecycleDiagnostics(args, &bytes.Buffer{}, &bytes.Buffer{}, diagnostics.NewLifecycleRecorder(writer, "run", "1.0.0", "tmux"))
	withoutErr := RunWithLifecycleDiagnostics(args, &bytes.Buffer{}, &bytes.Buffer{}, nil)
	if failingErr == nil || withoutErr == nil || failingErr.Error() != withoutErr.Error() {
		t.Fatalf("with failing journal=%v without=%v", failingErr, withoutErr)
	}
	failing, without := cli.ClassifyFailure(failingErr, IsUsageError(failingErr)), cli.ClassifyFailure(withoutErr, IsUsageError(withoutErr))
	if failing != without || writer.count() == 0 {
		t.Fatalf("exit with failing journal=%+v without=%+v appends=%d", failing, without, writer.count())
	}
}

// Doctor and the support report never install the recorder, so a refusal
// received while they run writes nothing. Not parallel: it reads the
// process-wide recorder.
func TestCodexBrokerRefusalJournalIsNeverInstalledForNoWriteInvocations(t *testing.T) {
	writer := &failingEventWriter{}
	recorder := diagnostics.NewLifecycleRecorder(writer, "run", "1.0.0", "tmux")
	refusal := fmt.Errorf("wrapped: %w", &codexbroker.BrokerError{Refusal: codexbroker.RefusalHostUnavailable})
	for _, args := range [][]string{{"doctor"}, {"doctor", "--json"}, {"diagnostics", "report"}} {
		restore := observeCodexBrokerRefusals(recorder, args)
		recordCodexBrokerRefusal(diagnostics.CodexBrokerRoleProbe, diagnostics.CodexBrokerOperationEnsure, refusal)
		restore()
	}
	if writer.count() != 0 {
		t.Fatalf("no-write invocations appended %d records", writer.count())
	}
	restore := observeCodexBrokerRefusals(recorder, []string{"internal", "codex-broker", "probe"})
	recordCodexBrokerRefusal(diagnostics.CodexBrokerRoleProbe, diagnostics.CodexBrokerOperationEnsure, refusal)
	restore()
	if writer.count() != 1 || codexBrokerRefusalJournal.Load() != nil {
		t.Fatalf("probe appends = %d, installed after restore = %v", writer.count(), codexBrokerRefusalJournal.Load() != nil)
	}
}
