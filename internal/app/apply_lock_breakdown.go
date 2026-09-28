package app

import (
	"context"

	"github.com/crevissepartners/projmux/internal/diagnostics"
)

// The tmux.apply breakdown travels through the context, the way a create's
// lock span does: runApply puts its apply recorder on the context it hands to
// the controller, and each Registry transaction site on that path names its
// kind just before its Store call and marks its phases inside the callback.
// The Store's lock observer, which cannot see the context, reaches the same
// recorder through the lifecycle recorder's active command scope, and the
// pending transaction named here is what it attributes the wait and hold to.
//
// With no apply recorder on the context -- hooks, every other command, and an
// apply without a journal -- every helper is a no-op that reads no clock.
//
// Registry transactions reachable from runApply, all update-convergent and all
// in the exhausted-replay or converge step, and the kind each is named:
//
//   - runLockedAutomaticMirrorRecovery: mirror-recovery.
//   - (*controllerTriggerRunner).converge's binding reconcile: binding-converge.
//   - (*controllerTriggerRunner).lowerProjectSessionsEndedOnHookServer:
//     session-lower.
//   - (*controlSessionConverger).convergeTargetWithEvidence: control-targets.
//   - reconcileLifecycle: lifecycle-reconcile, or preexisting-dead-agent when
//     (*controllerTriggerRunner).reconcileOnePreexistingDeadAgentPane runs it.
//
// The Registry reads on the same path (resourceStore.load) take no lock.
type applyRecorderKey struct{}

type applyLockKindKey struct{}

// withApplyRecorder hands the apply recorder to the Registry transactions the
// apply runs.
func withApplyRecorder(ctx context.Context, apply *diagnostics.ApplyRecorder) context.Context {
	if apply == nil {
		return ctx
	}
	return context.WithValue(ctx, applyRecorderKey{}, apply)
}

func applyRecorderFrom(ctx context.Context) *diagnostics.ApplyRecorder {
	if ctx == nil {
		return nil
	}
	apply, _ := ctx.Value(applyRecorderKey{}).(*diagnostics.ApplyRecorder)
	return apply
}

// withApplyLockKind names every Registry transaction run under ctx as kind,
// over the kind the transaction's own site names. A caller uses it when it runs
// a shared transaction for a purpose of its own.
func withApplyLockKind(ctx context.Context, kind diagnostics.ApplyLockKind) context.Context {
	if applyRecorderFrom(ctx) == nil {
		return ctx
	}
	return context.WithValue(ctx, applyLockKindKey{}, kind)
}

// beginApplyLock names the Registry transaction the caller is about to run.
// The caller ends it once the Store call returned, and calls Returned when its
// callback returns; both are nil-safe.
func beginApplyLock(ctx context.Context, kind diagnostics.ApplyLockKind) *diagnostics.ApplyLockTransaction {
	apply := applyRecorderFrom(ctx)
	if apply == nil {
		return nil
	}
	if outer, ok := ctx.Value(applyLockKindKey{}).(diagnostics.ApplyLockKind); ok {
		kind = outer
	}
	return apply.BeginLock(kind)
}

// markApplyLockPhase marks phase on the pending apply transaction, for code
// inside a transaction callback that does not hold the transaction itself.
func markApplyLockPhase(ctx context.Context, phase diagnostics.ApplyLockPhase) {
	applyRecorderFrom(ctx).MarkLockPhase(phase)
}
