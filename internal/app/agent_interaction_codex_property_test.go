package app_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	app "github.com/crevissepartners/projmux/internal/app"
)

type lifecyclePropertyRunner struct {
	sut       app.CodexLifecycleTestHarness
	model     lifecycleModelState
	history   []lifecycleConcreteOperation
	lastEvent *lifecycleConcreteOperation
}

func TestCodexLifecycleReferenceModelSeedCorpus(t *testing.T) {
	// This corpus and the generated property own pure invalidation, not-loaded
	// snapshot, and epoch fencing semantics. Their former example tests were
	// removed; the remaining examples own notice side effects the model omits.
	traces := [][]lifecycleOperation{
		{
			testSnapshot("active", "turn-1", "in-progress"),
			testEvent(lifecycleCurrent, lifecycleModelEvent{kind: "thread-status", threadState: "waiting-on-approval"}),
			testEvent(lifecycleCurrent, testPendingEvent("turn-1", "item-1", "request-1")),
			{kind: lifecycleDuplicate},
			{kind: lifecycleAllowedReorder, reorderKey: 7},
			testEvent(lifecycleCurrent, lifecycleModelEvent{kind: "request-resolved", requestID: "request-1"}),
			{kind: lifecycleInvalidation, authority: lifecycleCurrent},
			testEvent(lifecycleCurrent, lifecycleModelEvent{kind: "turn-started", turnID: "turn-late"}),
			{kind: lifecycleEpochReplace, snapshot: lifecycleModelSnapshot{threadID: "thread-1", threadState: "idle"}},
		},
		{
			testSnapshot("waiting-on-user-input", "turn-1", "in-progress"),
			testEvent(lifecycleForeign, lifecycleModelEvent{kind: "thread-status", threadState: "idle"}),
			testEvent(lifecycleStale, lifecycleModelEvent{kind: "turn-started", turnID: "turn-stale"}),
			testEvent(lifecycleFuture, lifecycleModelEvent{kind: "turn-started", turnID: "turn-future"}),
			{kind: lifecycleAllowedReorder, reorderKey: 13},
		},
		{
			testSnapshot("not-loaded", "turn-1", "completed"),
			testEvent(lifecycleCurrent, lifecycleModelEvent{kind: "turn-started", turnID: "turn-late"}),
			{kind: lifecycleEpochReplace, snapshot: lifecycleModelSnapshot{threadID: "thread-1", threadState: "active", turnID: "turn-2", turnState: "in-progress"}},
		},
	}
	for index, trace := range traces {
		t.Run(fmt.Sprintf("trace-%d", index), func(t *testing.T) {
			runLifecyclePropertyTrace(t, trace)
		})
	}
}

func TestCodexLifecycleDuplicateStaleAndForeignOperationsAreNoOps(t *testing.T) {
	runLifecyclePropertyTrace(t, []lifecycleOperation{
		testSnapshot("idle", "turn-1", "in-progress"),
		testEvent(lifecycleCurrent, lifecycleModelEvent{kind: "turn-started", turnID: "turn-2"}),
		{kind: lifecycleDuplicate},
		{kind: lifecycleEpochReplace, snapshot: lifecycleModelSnapshot{threadID: "thread-1", threadState: "active", turnID: "turn-2", turnState: "in-progress"}},
		testEvent(lifecycleStale, testPendingEvent("turn-2", "item-stale", "request-stale")),
		testEvent(lifecycleFuture, testPendingEvent("turn-2", "item-future", "request-future")),
		testEvent(lifecycleForeign, testPendingEvent("turn-2", "item-foreign", "request-foreign")),
		{kind: lifecycleInvalidation, authority: lifecycleStale},
	})
}

func TestCodexLifecycleAllowedReorderReachesSameFixedPoint(t *testing.T) {
	runLifecyclePropertyTrace(t, []lifecycleOperation{
		testSnapshot("waiting-on-approval", "turn-1", "in-progress"),
		{kind: lifecycleAllowedReorder, reorderKey: 42},
	})
}

func TestCodexLifecycleMutationDiagnosticsIncludeMinimalOperationTrace(t *testing.T) {
	t.Run("stale-acceptance", func(t *testing.T) {
		trace := []lifecycleOperation{
			testSnapshot("idle", "turn-1", "in-progress"),
			testEvent(lifecycleStale, lifecycleModelEvent{kind: "turn-started", turnID: "turn-stale"}),
		}
		runner := runLifecyclePropertyTrace(t, trace)
		mutant := runner.sut.State()
		mutant.Interaction = "in_progress"
		diagnostic := lifecycleStateDiff(1, trace, runner.model, mutant)
		assertMinimalTraceDiagnostic(t, diagnostic, "field=interaction", "event[stale]")
	})

	t.Run("wrong-request-ownership", func(t *testing.T) {
		trace := []lifecycleOperation{
			testSnapshot("waiting-on-approval", "turn-1", "in-progress"),
			testEvent(lifecycleCurrent, testPendingEvent("turn-1", "item-1", "request-1")),
		}
		runner := runLifecyclePropertyTrace(t, trace)
		mutant := runner.sut.State()
		mutant.Pending[0].TurnID = "turn-foreign"
		diagnostic := lifecycleStateDiff(1, trace, runner.model, mutant)
		assertMinimalTraceDiagnostic(t, diagnostic, "field=pending", "request=request-1")
	})
}

func FuzzCodexLifecycleReferenceModel(f *testing.F) {
	f.Add([]byte{0, 3, 0, 1, 21, 0, 1, 2, 0, 2, 0, 0, 3, 9, 0, 1, 3, 0, 4, 0, 0, 1, 5, 0})
	f.Add([]byte{0, 25, 0, 1, 2, 3, 1, 2, 1, 1, 2, 2, 3, 17, 0, 5, 3, 0})
	f.Add([]byte{0, 1, 0, 1, 64, 0, 2, 0, 0, 4, 0, 0, 5, 3, 0})
	f.Fuzz(func(t *testing.T, encoded []byte) {
		if len(encoded) > 384 {
			encoded = encoded[:384]
		}
		runLifecyclePropertyTrace(t, decodeLifecycleOperations(encoded))
	})
}

func runLifecyclePropertyTrace(t *testing.T, trace []lifecycleOperation) *lifecyclePropertyRunner {
	t.Helper()
	runner := &lifecyclePropertyRunner{}
	for index, operation := range trace {
		runner.apply(t, index, trace[:index+1], operation)
	}
	return runner
}

func (r *lifecyclePropertyRunner) apply(t *testing.T, index int, trace []lifecycleOperation, operation lifecycleOperation) {
	t.Helper()
	beforeModel := lifecycleContractStateFromModel(r.model)
	beforeSUT := r.sut.State()
	shouldBeNoOp := false

	switch operation.kind {
	case lifecycleSnapshot:
		epoch := currentLifecycleEpoch(r.model.epoch)
		concrete := lifecycleConcreteOperation{kind: lifecycleConcreteSnapshot, epoch: epoch, snapshot: operation.snapshot}
		r.applyConcrete(t, index, 0, trace, concrete)
		r.history = append(r.history, concrete)
		r.lastEvent = nil
		shouldBeNoOp = operation.snapshot.threadID != "thread-1"
	case lifecycleProviderEvent:
		concrete := lifecycleConcreteOperation{
			kind: lifecycleConcreteEvent, epoch: lifecycleEpochFor(operation.authority, r.model.epoch), event: operation.event,
		}
		if operation.authority == lifecycleForeign {
			concrete.event.threadID = "thread-foreign"
		} else {
			concrete.event.threadID = "thread-1"
		}
		r.applyConcrete(t, index, 0, trace, concrete)
		r.history = append(r.history, concrete)
		r.lastEvent = &r.history[len(r.history)-1]
		shouldBeNoOp = operation.authority != lifecycleCurrent
	case lifecycleDuplicate:
		shouldBeNoOp = true
		if r.lastEvent != nil {
			concrete := *r.lastEvent
			r.applyConcrete(t, index, 0, trace, concrete)
			r.history = append(r.history, concrete)
		}
	case lifecycleAllowedReorder:
		r.applyAllowedReorder(t, index, trace, operation)
	case lifecycleInvalidation:
		concrete := lifecycleConcreteOperation{
			kind: lifecycleConcreteInvalidation, epoch: lifecycleInvalidationEpochFor(operation.authority, r.model.epoch),
		}
		r.applyConcrete(t, index, 0, trace, concrete)
		r.history = append(r.history, concrete)
		r.lastEvent = nil
		shouldBeNoOp = operation.authority != lifecycleCurrent
	case lifecycleEpochReplace:
		concrete := lifecycleConcreteOperation{
			kind: lifecycleConcreteSnapshot, epoch: nextLifecycleEpoch(r.model.epoch), snapshot: operation.snapshot,
		}
		r.applyConcrete(t, index, 0, trace, concrete)
		r.history = append(r.history, concrete)
		r.lastEvent = nil
	default:
		t.Fatalf("closed operation vocabulary escaped: operation=%d trace=%s", operation.kind, formatLifecycleTrace(trace))
	}

	if diagnostic := lifecycleStateDiff(index, trace, r.model, r.sut.State()); diagnostic != "" {
		t.Fatal(diagnostic)
	}
	if shouldBeNoOp {
		if diagnostic := lifecycleNoOpDiff(index, trace, beforeModel, lifecycleContractStateFromModel(r.model), beforeSUT, r.sut.State()); diagnostic != "" {
			t.Fatal(diagnostic)
		}
	}
}

func (r *lifecyclePropertyRunner) applyAllowedReorder(t *testing.T, index int, trace []lifecycleOperation, operation lifecycleOperation) {
	t.Helper()
	prefix := append([]lifecycleConcreteOperation(nil), r.history...)
	turnID := r.model.currentTurnID
	requestPrefix := fmt.Sprintf("reorder-%d-%d", operation.reorderKey, index)
	first := lifecycleConcreteOperation{kind: lifecycleConcreteEvent, epoch: currentLifecycleEpoch(r.model.epoch), event: lifecycleModelEvent{
		kind: "approval-pending", threadID: "thread-1", turnID: turnID,
		itemID: requestPrefix + "-item-a", requestID: requestPrefix + "-request-a", approvalKind: "command",
	}}
	second := lifecycleConcreteOperation{kind: lifecycleConcreteEvent, epoch: currentLifecycleEpoch(r.model.epoch), event: lifecycleModelEvent{
		kind: "approval-pending", threadID: "thread-1", turnID: turnID,
		itemID: requestPrefix + "-item-b", requestID: requestPrefix + "-request-b", approvalKind: "file-change",
	}}

	r.applyConcrete(t, index, 0, trace, first)
	r.applyConcrete(t, index, 1, trace, second)
	canonical := r.sut.State()
	r.history = append(r.history, first, second)
	r.lastEvent = &r.history[len(r.history)-1]

	var alternate lifecyclePropertyRunner
	for replayIndex, concrete := range prefix {
		alternate.applyConcrete(t, index, replayIndex, trace, concrete)
		alternate.history = append(alternate.history, concrete)
	}
	alternate.applyConcrete(t, index, len(prefix), trace, second)
	alternate.applyConcrete(t, index, len(prefix)+1, trace, first)
	if diagnostic := lifecycleFixedPointDiff(index, trace, canonical, alternate.sut.State()); diagnostic != "" {
		t.Fatal(diagnostic)
	}
}

func (r *lifecyclePropertyRunner) applyConcrete(t *testing.T, operationIndex, substep int, trace []lifecycleOperation, operation lifecycleConcreteOperation) {
	t.Helper()
	want := r.model.apply(operation)
	got := applyLifecycleSUTOperation(&r.sut, operation)
	if want.accepted != got.Accepted {
		t.Fatalf("step=%d.%d field=accepted want=%t got=%t trace=%s", operationIndex, substep, want.accepted, got.Accepted, formatLifecycleTrace(trace))
	}
	if want.invalidated != got.Invalidated {
		t.Fatalf("step=%d.%d field=invalidated want=%t got=%t trace=%s", operationIndex, substep, want.invalidated, got.Invalidated, formatLifecycleTrace(trace))
	}
	if diagnostic := lifecycleStateDiff(operationIndex, trace, r.model, got.State); diagnostic != "" {
		t.Fatal(diagnostic)
	}
}

func applyLifecycleSUTOperation(harness *app.CodexLifecycleTestHarness, operation lifecycleConcreteOperation) app.CodexLifecycleTestResult {
	switch operation.kind {
	case lifecycleConcreteSnapshot:
		return harness.Snapshot(app.CodexLifecycleTestSnapshot{
			Epoch: operation.epoch, ThreadID: operation.snapshot.threadID,
			ThreadState: operation.snapshot.threadState, TurnID: operation.snapshot.turnID, TurnState: operation.snapshot.turnState,
		})
	case lifecycleConcreteEvent:
		return harness.Event(app.CodexLifecycleTestEvent{
			Epoch: operation.epoch, Kind: operation.event.kind, ThreadID: operation.event.threadID,
			TurnID: operation.event.turnID, ItemID: operation.event.itemID, RequestID: operation.event.requestID,
			ThreadState: operation.event.threadState, TurnState: operation.event.turnState, ApprovalKind: operation.event.approvalKind,
		})
	case lifecycleConcreteInvalidation:
		return harness.Invalidate(operation.epoch)
	default:
		return app.CodexLifecycleTestResult{State: harness.State()}
	}
}

func lifecycleStateDiff(step int, trace []lifecycleOperation, want lifecycleModelState, got app.CodexLifecycleTestState) string {
	if want.epoch != got.Epoch {
		return lifecycleDiagnostic(step, "epoch", want.epoch, got.Epoch, trace)
	}
	if want.active != got.Active {
		return lifecycleDiagnostic(step, "active", want.active, got.Active, trace)
	}
	if want.interaction != got.Interaction {
		return lifecycleDiagnostic(step, "interaction", want.interaction, got.Interaction, trace)
	}
	wantPending := want.sortedPending()
	gotPending := make([]lifecycleModelPending, len(got.Pending))
	for index, pending := range got.Pending {
		gotPending[index] = lifecycleModelPending{
			turnID: pending.TurnID, itemID: pending.ItemID, requestID: pending.RequestID,
			approvalKind: pending.ApprovalKind, notified: pending.Notified,
		}
	}
	if !reflect.DeepEqual(wantPending, gotPending) {
		return lifecycleDiagnostic(step, "pending", wantPending, gotPending, trace)
	}
	return ""
}

type lifecycleContractState struct {
	epoch       uint64
	active      bool
	interaction string
	pending     []lifecycleModelPending
}

func lifecycleContractStateFromModel(state lifecycleModelState) lifecycleContractState {
	return lifecycleContractState{epoch: state.epoch, active: state.active, interaction: state.interaction, pending: state.sortedPending()}
}

func lifecycleContractStateFromSUT(state app.CodexLifecycleTestState) lifecycleContractState {
	pending := make([]lifecycleModelPending, len(state.Pending))
	for index, request := range state.Pending {
		pending[index] = lifecycleModelPending{
			turnID: request.TurnID, itemID: request.ItemID, requestID: request.RequestID,
			approvalKind: request.ApprovalKind, notified: request.Notified,
		}
	}
	return lifecycleContractState{epoch: state.Epoch, active: state.Active, interaction: state.Interaction, pending: pending}
}

func lifecycleNoOpDiff(step int, trace []lifecycleOperation, beforeModel, afterModel lifecycleContractState, beforeSUT, afterSUT app.CodexLifecycleTestState) string {
	if !reflect.DeepEqual(beforeModel, afterModel) {
		return lifecycleDiagnostic(step, "model-no-op", beforeModel, afterModel, trace)
	}
	beforeActual := lifecycleContractStateFromSUT(beforeSUT)
	afterActual := lifecycleContractStateFromSUT(afterSUT)
	if !reflect.DeepEqual(beforeActual, afterActual) {
		return lifecycleDiagnostic(step, "sut-no-op", beforeActual, afterActual, trace)
	}
	return ""
}

func lifecycleFixedPointDiff(step int, trace []lifecycleOperation, canonical, alternate app.CodexLifecycleTestState) string {
	canonicalState := lifecycleContractStateFromSUT(canonical)
	alternateState := lifecycleContractStateFromSUT(alternate)
	if !reflect.DeepEqual(canonicalState, alternateState) {
		return lifecycleDiagnostic(step, "reorder-fixed-point", canonicalState, alternateState, trace)
	}
	return ""
}

func lifecycleDiagnostic(step int, field string, want, got any, trace []lifecycleOperation) string {
	return fmt.Sprintf("step=%d field=%s want=%#v got=%#v trace=%s", step, field, want, got, formatLifecycleTrace(trace))
}

func assertMinimalTraceDiagnostic(t *testing.T, diagnostic string, fragments ...string) {
	t.Helper()
	if diagnostic == "" {
		t.Fatal("injected lifecycle mutant escaped the reference property")
	}
	for _, fragment := range append([]string{"step=1", "trace=["}, fragments...) {
		if !strings.Contains(diagnostic, fragment) {
			t.Fatalf("diagnostic %q does not contain %q", diagnostic, fragment)
		}
	}
}

func currentLifecycleEpoch(epoch uint64) uint64 {
	if epoch == 0 {
		return 1
	}
	return epoch
}

func nextLifecycleEpoch(epoch uint64) uint64 {
	if epoch == 0 {
		return 1
	}
	return epoch + 1
}

func lifecycleEpochFor(authority lifecycleAuthority, epoch uint64) uint64 {
	current := currentLifecycleEpoch(epoch)
	switch authority {
	case lifecycleCurrent, lifecycleForeign:
		return current
	case lifecycleStale:
		if current > 1 {
			return current - 1
		}
		return current + 97
	case lifecycleFuture:
		return current + 1
	default:
		return current + 101
	}
}

func lifecycleInvalidationEpochFor(authority lifecycleAuthority, epoch uint64) uint64 {
	if authority == lifecycleForeign {
		return currentLifecycleEpoch(epoch) + 1
	}
	return lifecycleEpochFor(authority, epoch)
}

func testSnapshot(threadState, turnID, turnState string) lifecycleOperation {
	return lifecycleOperation{
		kind: lifecycleSnapshot, authority: lifecycleCurrent,
		snapshot: lifecycleModelSnapshot{threadID: "thread-1", threadState: threadState, turnID: turnID, turnState: turnState},
	}
}

func testEvent(authority lifecycleAuthority, event lifecycleModelEvent) lifecycleOperation {
	if authority == lifecycleForeign {
		event.threadID = "thread-foreign"
	} else {
		event.threadID = "thread-1"
	}
	return lifecycleOperation{kind: lifecycleProviderEvent, authority: authority, event: event}
}

func testPendingEvent(turnID, itemID, requestID string) lifecycleModelEvent {
	return lifecycleModelEvent{
		kind: "approval-pending", turnID: turnID, itemID: itemID, requestID: requestID, approvalKind: "command",
	}
}
