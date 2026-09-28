package diagnostics

import (
	"fmt"
	"time"
)

// createOutcomeEvent is the one record a create transaction writes: how long
// the whole transaction took and how long it held the Registry lock.
const createOutcomeEvent = "create.outcome"

// CreateKind is the closed, content-free kind of one create transaction. It is
// carried in the `operation` field, which every other family also uses for
// "which state change this record describes".
type CreateKind string

const (
	CreateKindWindow CreateKind = "window"
	CreateKindPane   CreateKind = "pane"
	CreateKindAgent  CreateKind = "agent"
	CreateKindResume CreateKind = "resume"
)

var createKinds = [...]CreateKind{CreateKindWindow, CreateKindPane, CreateKindAgent, CreateKindResume}

func validCreateKind(kind CreateKind) bool {
	for _, candidate := range createKinds {
		if kind == candidate {
			return true
		}
	}
	return false
}

// CreatePhase is the closed name of one stage of a create's Registry
// mutation. The stages run in this order and tile the lock hold: each starts
// where the previous one ends, the first at the mutation's entry and the last
// at the Registry update returning.
//
//   - guard: the ownership guards' preflight against tmux.
//   - first-reconcile: the reconcile pass before any write of the create.
//   - operation: the create's own work -- tmux splits, mirrors, the Registry
//     edit, and for an Agent the supervised child spawn.
//   - second-reconcile: the reconcile pass after the create's writes.
//   - reprove: the re-proof of any route identity reused in the transaction.
//   - store-write: from the mutation callback returning to the Registry
//     update returning -- normalize, validate, the durable write, the unlock,
//     and anything the Store does after its unlock before it returns.
type CreatePhase int

const (
	CreatePhaseGuard CreatePhase = iota
	CreatePhaseFirstReconcile
	CreatePhaseOperation
	CreatePhaseSecondReconcile
	CreatePhaseReprove
	CreatePhaseStoreWrite
	createPhaseCount
)

// CreatePhases holds one duration per CreatePhase. A nil entry is a phase the
// transaction never entered, because an earlier stage returned an error.
type CreatePhases [createPhaseCount]*time.Duration

// CreateOutcome is one finished create transaction as its caller measured it.
// Duration covers the whole transaction. LockHeld is nil when the transaction
// ended before it entered the Registry mutation, and otherwise covers the span
// from entering that mutation to the Registry update returning. Phases splits
// LockHeld into its stages, and SpawnToRelease is the part of it from the
// first supervised child spawn to the update returning: the share of that
// child's own lock budget the creator consumed before the child could take the
// lock. Both are recorded only with LockHeld.
type CreateOutcome struct {
	Kind           CreateKind
	Result         LifecycleResult
	Duration       time.Duration
	LockHeld       *time.Duration
	Phases         CreatePhases
	SpawnToRelease *time.Duration
}

// CreateRecorder appends one `create.outcome` per create transaction under the
// invocation run ID. It deliberately owns no top-level outcome and no once: a
// process may run several transactions, each records itself, and the generic
// `command.outcome` of the invocation is left exactly as it was. Appends are
// best-effort and never flow back into the create.
type CreateRecorder struct {
	lifecycle *LifecycleRecorder
}

func (r *LifecycleRecorder) Create() *CreateRecorder {
	if r == nil {
		return nil
	}
	return &CreateRecorder{lifecycle: r}
}

// Record appends one outcome. An unknown kind or result drops the record,
// because the vocabulary is closed and callers name it statically. Timings are
// clamped so the record always satisfies 0 <= lock_held_ms <= duration_ms even
// when the caller's clock is not monotonic.
func (r *CreateRecorder) Record(outcome CreateOutcome) {
	if r == nil || r.lifecycle == nil {
		return
	}
	owner := r.lifecycle
	duration := max(outcome.Duration, 0)
	event := Event{
		At: owner.now().UTC().Format(time.RFC3339Nano), Level: "info", Component: "create",
		Event: createOutcomeEvent, Result: string(outcome.Result), DurationMS: duration.Milliseconds(),
		RunID: owner.runID, Version: owner.version, MuxBackend: owner.muxBackend,
		Operation: string(outcome.Kind),
	}
	if outcome.LockHeld != nil {
		held := min(max(*outcome.LockHeld, 0), duration).Milliseconds()
		event.LockHeldMS = &held
		event.setCreatePhases(outcome.Phases, held)
		if spawn := outcome.SpawnToRelease; spawn != nil && (outcome.Kind == CreateKindAgent || outcome.Kind == CreateKindResume) {
			ms := min(max(*spawn, 0).Milliseconds(), held)
			event.SpawnToReleaseMS = &ms
		}
	}
	if outcome.Result == LifecycleError {
		event.Level, event.Kind = "error", "runtime"
	}
	if validateCreateOutcomeEvent(event) != nil {
		return
	}
	owner.writeMu.Lock()
	defer owner.writeMu.Unlock()
	owner.append(event)
}

// setCreatePhases writes the phase fields when their millisecond sum stays
// within held, and none of them otherwise. Rounding each phase down can only
// shrink the sum, so this drops the breakdown only when the caller's clock
// went backwards between readings or the phases were not taken on the lock
// hold's own clock; a partial breakdown would misattribute the difference.
func (event *Event) setCreatePhases(phases CreatePhases, held int64) {
	var values [createPhaseCount]*int64
	var sum int64
	for phase, duration := range phases {
		if duration == nil {
			continue
		}
		ms := max(*duration, 0).Milliseconds()
		values[phase] = &ms
		sum += ms
	}
	if sum > held {
		return
	}
	event.PhaseGuardMS, event.PhaseFirstReconcileMS, event.PhaseOperationMS = values[CreatePhaseGuard], values[CreatePhaseFirstReconcile], values[CreatePhaseOperation]
	event.PhaseSecondReconcileMS, event.PhaseReproveMS, event.PhaseStoreWriteMS = values[CreatePhaseSecondReconcile], values[CreatePhaseReprove], values[CreatePhaseStoreWrite]
}

func validateCreateOutcomeEvent(event Event) error {
	if event.Component != "create" || event.Command != "" || event.Subcommand != "" || event.Message != "" ||
		event.Code != "" || event.Source != "" || event.hasCounts() || event.hasNotifyFocusFields() ||
		event.hasAIFields() || event.hasResourceFields() || event.hasTeardownFields() || event.WaitMS != nil {
		return fmt.Errorf("invalid create outcome shape")
	}
	if !validCreateKind(CreateKind(event.Operation)) {
		return fmt.Errorf("invalid create kind")
	}
	switch event.Result {
	case "success":
		if event.Level != "info" || event.Kind != "" {
			return fmt.Errorf("invalid create success shape")
		}
	case "error":
		if event.Level != "error" || event.Kind != "runtime" {
			return fmt.Errorf("invalid create error shape")
		}
	default:
		return fmt.Errorf("invalid create result")
	}
	if held := event.LockHeldMS; held != nil && (*held < 0 || *held > event.DurationMS) {
		return fmt.Errorf("invalid create lock hold")
	}
	return validateCreateLockBreakdown(event)
}

// validateCreateLockBreakdown enforces the phase and spawn invariants: both
// exist only inside a recorded lock hold, every value is non-negative, the
// phases sum to at most the hold, and the spawn share belongs to the kinds
// that spawn a supervised child and never exceeds the hold.
func validateCreateLockBreakdown(event Event) error {
	if !event.hasCreatePhaseFields() {
		return nil
	}
	if event.LockHeldMS == nil {
		return fmt.Errorf("create lock breakdown without a lock hold")
	}
	held := *event.LockHeldMS
	var sum int64
	for _, value := range event.createPhaseFields() {
		if value == nil {
			continue
		}
		if *value < 0 {
			return fmt.Errorf("invalid create phase")
		}
		sum += *value
	}
	if sum > held {
		return fmt.Errorf("create phases exceed the lock hold")
	}
	if spawn := event.SpawnToReleaseMS; spawn != nil {
		kind := CreateKind(event.Operation)
		if (kind != CreateKindAgent && kind != CreateKindResume) || *spawn < 0 || *spawn > held {
			return fmt.Errorf("invalid create spawn to release")
		}
	}
	return nil
}
