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

// CreateOutcome is one finished create transaction as its caller measured it.
// Duration covers the whole transaction. LockHeld is nil when the transaction
// ended before it entered the Registry mutation, and otherwise covers the span
// from entering that mutation to the Registry update returning.
type CreateOutcome struct {
	Kind     CreateKind
	Result   LifecycleResult
	Duration time.Duration
	LockHeld *time.Duration
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

func validateCreateOutcomeEvent(event Event) error {
	if event.Component != "create" || event.Command != "" || event.Subcommand != "" || event.Message != "" ||
		event.Code != "" || event.Source != "" || event.hasCounts() || event.hasNotifyFocusFields() ||
		event.hasAIFields() || event.hasResourceFields() || event.hasTeardownFields() {
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
	return nil
}
