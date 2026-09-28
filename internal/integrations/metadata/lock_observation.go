package metadata

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

// LockOperation is the closed name of the Store entry point that took the
// Registry mutation lock. It names the entry point, never the caller's intent:
// an Update that creates an Agent and one that renames a Window are both
// "update" here, and the caller that needs more says so in its own record.
type LockOperation string

const (
	LockOperationUpdate           LockOperation = "update"
	LockOperationUpdateConvergent LockOperation = "update-convergent"
	LockOperationLoad             LockOperation = "load"
	LockOperationMigrate          LockOperation = "migrate"
	LockOperationAdmissionBarrier LockOperation = "admission-barrier"
)

// LockOutcome is how one acquisition ended.
//
//   - LockOutcomeReleased: the lease was granted, the locked work ran, and the
//     lease was released.
//   - LockOutcomeTimeout: the acquisition gave up on its deadline, so the error
//     the caller receives matches ErrLockTimeout.
//   - LockOutcomeAcquireFailed: any other acquisition error -- an unopenable
//     lock file, a refused flock, a cancelled context.
type LockOutcome string

const (
	LockOutcomeReleased      LockOutcome = "released"
	LockOutcomeTimeout       LockOutcome = "timeout"
	LockOutcomeAcquireFailed LockOutcome = "acquire-failed"
)

// LockObservation is one Registry lock acquisition as the Store measured it on
// its own injectable clock, the same one the lock deadline is read through.
//
// Wait runs from just before the acquisition to the grant, or to giving up.
// Held runs from the grant to just after the lease was released, so it covers
// the locked read, the caller's callback, the durable write, and the unlock
// itself; it is zero unless Outcome is LockOutcomeReleased. Failed reports that
// the locked work returned an error -- the callback refused, validation failed,
// the write failed -- and is meaningful only for a released lease.
type LockObservation struct {
	Operation LockOperation
	Outcome   LockOutcome
	Wait      time.Duration
	Held      time.Duration
	Failed    bool
}

// LockObserver receives one observation per Registry lock acquisition. It is
// called after the lease is released (or after the acquisition failed), never
// while the lock is held, so whatever it does -- a journal append, a slow
// disk -- cannot lengthen another writer's wait. It runs on the goroutine that
// took the lock and must not call back into the same Store.
type LockObserver func(LockObservation)

// defaultLockObserver is the process-wide observer every Store without its own
// uses. The CLI entry point installs it once, after it knows which command it
// is running; nothing else in production sets it.
var defaultLockObserver atomic.Pointer[LockObserver]

// SetLockObserver installs the process-wide Registry lock observer and returns
// a function that restores the previous one. A nil observer clears it. With no
// observer installed the lock path reads the clock exactly as it did before
// the seam existed.
func SetLockObserver(observer LockObserver) (restore func()) {
	var next *LockObserver
	if observer != nil {
		next = &observer
	}
	previous := defaultLockObserver.Swap(next)
	return func() { defaultLockObserver.Store(previous) }
}

// SetLockObserver overrides the process-wide observer for this Store alone.
// Tests use it so a parallel test's observer never sees another test's
// acquisitions. A nil observer returns the Store to the process-wide one.
func (s *Store) SetLockObserver(observer LockObserver) {
	if s != nil {
		s.lockObserver = observer
	}
}

func (s *Store) currentLockObserver() LockObserver {
	if s.lockObserver != nil {
		return s.lockObserver
	}
	if observer := defaultLockObserver.Load(); observer != nil {
		return *observer
	}
	return nil
}

// withObservedLock is withLock with the three clock readings an observation
// needs: before the acquisition, at the grant (or the failure), and after the
// release. The lock order, deadline, and transaction are exactly withLock's;
// the observer runs only once the lease is gone, and its panic is swallowed so
// an observer can never change what the mutation returned.
func (s *Store) withObservedLock(operation LockOperation, observer LockObserver, fn func() error) error {
	started := s.clock()
	lease, err := s.acquireLock(context.Background())
	acquired := s.clock()
	if err != nil {
		outcome := LockOutcomeAcquireFailed
		if errors.Is(err, ErrLockTimeout) {
			outcome = LockOutcomeTimeout
		}
		notifyLockObserver(observer, LockObservation{Operation: operation, Outcome: outcome, Wait: acquired.Sub(started)})
		return err
	}
	fnErr := runLeased(lease, fn)
	released := s.clock()
	notifyLockObserver(observer, LockObservation{
		Operation: operation, Outcome: LockOutcomeReleased,
		Wait: acquired.Sub(started), Held: released.Sub(acquired), Failed: fnErr != nil,
	})
	return fnErr
}

// runLeased runs fn and releases lease on every exit, a panic included, the way
// withLock's deferred release does.
func runLeased(lease *registryLease, fn func() error) error {
	defer lease.release()
	return fn()
}

func notifyLockObserver(observer LockObserver, observation LockObservation) {
	defer func() { _ = recover() }()
	observer(observation)
}
