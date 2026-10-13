package projmux

import (
	"time"

	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

// CreateKind names the closed create.outcome operation vocabulary.
type CreateKind string

const (
	CreateKindWindow CreateKind = "window"
	CreateKindPane   CreateKind = "pane"
	CreateKindAgent  CreateKind = "agent"
	CreateKindResume CreateKind = "resume"
)

// CreateResult names the result of the transaction, before projection or Agent
// activation acknowledgement. A committed transaction has result success even
// if later projection or acknowledgement returns an error to the caller.
type CreateResult string

const (
	CreateSuccess CreateResult = "success"
	CreateError   CreateResult = "error"
)

// CreatePhase names the stages that tile the measured Registry lock hold.
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

// CreatePhases holds the duration of each stage. Nil means never entered.
// StoreWrite includes normalization, validation, durable write and unlock, up
// to the Store update returning. Failed stages can leave a partial breakdown.
type CreatePhases [createPhaseCount]*time.Duration

// CreateOutcome is a content-free measurement of one finished transaction,
// with the same timing semantics as the CLI create.outcome event. Duration
// covers execution and cleanup, excluding the recorder. LockHeld starts at
// mutation entry and ends when Store update returns (excluding lock wait).
// LockHeld is nil if no mutation was entered. Phases and SpawnToRelease exist
// only with LockHeld; SpawnToRelease measures the first supervised child spawn
// through Store update return, for Agent/Resume only. Values are raw durations;
// journal writers apply their own CLI-compatible rounding and clamping.
// The outcome and all duration pointers belong to the recipient.
type CreateOutcome struct {
	Kind           CreateKind
	Result         CreateResult
	Duration       time.Duration
	LockHeld       *time.Duration
	Phases         CreatePhases
	SpawnToRelease *time.Duration
}

// CreateRecorder receives one outcome per admitted transaction, after the
// Registry lock is released and rollback/lease cleanup finishes. Validation or
// cancellation before transaction admission records nothing. It runs
// synchronously, without the caller's canceled context, and must return
// promptly. Concurrent Client calls can invoke it concurrently; the caller
// must synchronize its sink. Returned errors and panics are ignored so
// recording cannot change the creation result. Nil is a no-op.
type CreateRecorder func(CreateOutcome) error

func (c *Client) createOutcomes() mutationruntime.CreateRecorder {
	if c.createRecorder == nil {
		return nil
	}
	return publicCreateRecorder{c.createRecorder}
}

type publicCreateRecorder struct{ record CreateRecorder }

func (r publicCreateRecorder) Record(o mutationruntime.CreateOutcome) {
	if r.record == nil {
		return
	}
	// A callback is caller code. Preserve the transaction's result even if its
	// optional measurement sink panics, just as for a failed append.
	defer func() { _ = recover() }()
	out := CreateOutcome{
		Kind: CreateKind(o.Kind), Result: CreateResult(o.Result), Duration: o.Duration,
		LockHeld: copyCreateDuration(o.LockHeld), SpawnToRelease: copyCreateDuration(o.SpawnToRelease),
	}
	for phase, duration := range o.Phases {
		out.Phases[phase] = copyCreateDuration(duration)
	}
	_ = r.record(out)
}

func copyCreateDuration(value *time.Duration) *time.Duration {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
