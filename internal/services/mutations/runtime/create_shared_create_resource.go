package runtime

import mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/cli"
	"github.com/crevissepartners/projmux/internal/core/candidates"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	intmux "github.com/crevissepartners/projmux/internal/integrations/mux"
)

// createOperation is one create body, run inside the registry transaction.
type createOperation func(
	ctx context.Context,
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	operationID string,
	ledger *RuntimeLedger,
) error

type createPreReconcile func(
	ctx context.Context,
	working coremetadata.Registry,
	mutator coremetadata.Mutator,
	operationID string,
) (liveSessionIdentity, error)

// operationClock is the clock the transaction ledger's create-operation marker
// is stamped from. Production wires time.Now; a nil seam falls back to it.
func (c *createCommand) operationClock() func() time.Time {
	if c == nil || c.now == nil {
		return time.Now
	}
	return c.now
}

// createKindUnrecorded is the kind of a route that runs through transact but
// is not a create -- the generated Window rename. It writes no create.outcome.
const createKindUnrecorded diagnostics.CreateKind = ""

// createOutcomeClock is the clock create.outcome timings are measured on.
func (c *createCommand) createOutcomeClock() func() time.Time {
	if c == nil || c.outcomeClock == nil {
		return time.Now
	}
	return c.outcomeClock
}

// createLockSpan measures how long one transaction held the Registry lock:
// from entering the mutation closure to the Registry update returning, so it
// covers the closure, the store's own normalize/validate/write, and the unlock,
// and never the wait for the lock or the Registry read before the closure.
//
// The span is also split into the closed create phases. enter opens the guard
// phase, each mark closes the open phase and opens the next on one clock
// reading, and leave closes whichever phase is open, so the phases tile the
// hold exactly. A phase the transaction never reached stays nil. The first
// supervised child spawn inside the span is stamped too, so leave can report
// how much of the hold came after it.
//
// One span belongs to one transaction and is only touched from the goroutine
// running it; it travels to the operation through the context, never through
// the createCommand, which concurrent transactions may share.
type createLockSpan struct {
	clock     func() time.Time
	entered   bool
	enteredAt time.Time
	held      *time.Duration

	phase          diagnostics.CreatePhase
	phaseStartedAt time.Time
	phases         diagnostics.CreatePhases

	spawned        bool
	spawnedAt      time.Time
	spawnToRelease *time.Duration
}

func (s *createLockSpan) enter() {
	if !s.entered {
		s.entered, s.enteredAt = true, s.clock()
		s.phase, s.phaseStartedAt = diagnostics.CreatePhaseGuard, s.enteredAt
	}
}

// mark closes the open phase and opens next. It is a no-op outside the span.
func (s *createLockSpan) mark(next diagnostics.CreatePhase) {
	if s == nil || !s.entered || s.held != nil {
		return
	}
	now := s.clock()
	s.closePhase(now)
	s.phase, s.phaseStartedAt = next, now
}

func (s *createLockSpan) closePhase(now time.Time) {
	elapsed := now.Sub(s.phaseStartedAt)
	if previous := s.phases[s.phase]; previous != nil {
		elapsed += *previous
	}
	s.phases[s.phase] = &elapsed
}

// markSpawn stamps the first supervised child spawn inside the span.
func (s *createLockSpan) markSpawn() {
	if s == nil || !s.entered || s.held != nil || s.spawned {
		return
	}
	s.spawned, s.spawnedAt = true, s.clock()
}

func (s *createLockSpan) leave() {
	if s.entered {
		now := s.clock()
		held := now.Sub(s.enteredAt)
		s.held = &held
		s.closePhase(now)
		if s.spawned {
			spawnToRelease := now.Sub(s.spawnedAt)
			s.spawnToRelease = &spawnToRelease
		}
	}
}

type createLockSpanKey struct{}

// withCreateLockSpan hands the transaction's span to the operation it runs.
func withCreateLockSpan(ctx context.Context, span *createLockSpan) context.Context {
	if span == nil {
		return ctx
	}
	return context.WithValue(ctx, createLockSpanKey{}, span)
}

// markSupervisedSpawn records, once per transaction, the moment the split
// that starts a managed Pane's supervisor returned a Pane: from then on the
// supervised child is running and will need the Registry lock this
// transaction still holds. Outside a create transaction it does nothing.
func markSupervisedSpawn(ctx context.Context) {
	if span, ok := ctx.Value(createLockSpanKey{}).(*createLockSpan); ok {
		span.markSpawn()
	}
}

// transact runs one create operation and records its create.outcome.
//
// The record is measurement only. It is appended after the whole transaction
// returned -- the Registry lock released, rollback and lease clear done -- and a
// journal failure never reaches the create's result. kind names the create;
// createKindUnrecorded records nothing.
func (c *createCommand) transact(kind diagnostics.CreateKind, op createOperation, guards ...createPreReconcile) error {
	return c.transactAdmitted(kind, nil, op, guards...)
}

func (c *createCommand) transactAdmitted(kind diagnostics.CreateKind, admission func(coremetadata.Registry) error, op createOperation, guards ...createPreReconcile) error {
	if c == nil {
		return errCreateRoutesNotConfigured
	}
	clock := c.createOutcomeClock()
	started := clock()
	lock := createLockSpan{clock: clock}
	err := c.runTransaction(&lock, admission, op, guards...)
	if kind != createKindUnrecorded {
		result := diagnostics.LifecycleSuccess
		if err != nil {
			result = diagnostics.LifecycleError
		}
		c.outcomes.Record(diagnostics.CreateOutcome{
			Kind: kind, Result: result, Duration: clock().Sub(started), LockHeld: lock.held,
			Phases: lock.phases, SpawnToRelease: lock.spawnToRelease,
		})
	}
	return err
}

var errCreateRoutesNotConfigured = errors.New("create: the resource-backed create routes are not configured")

// runTransaction runs one create operation under the declared transaction
// order: full preflight -> operation id -> created-resource ledger -> metadata
// mutation -> runtime mutation -> commit.
//
// Everything happens inside a single registry transaction, including the
// mutation-route registry reconciliation. The store applies the operation to a
// private clone and only writes when the whole body succeeds, so any failure --
// a pre-create hook refusal, a stale anchor, a tmux error -- leaves the registry
// file byte-identical. The tmux objects the body created are undone from the
// runtime ledger, which is the half no transaction can roll back for us.
func (c *createCommand) runTransaction(lock *createLockSpan, admission func(coremetadata.Registry) error, op createOperation, guards ...createPreReconcile) error {
	if c == nil || c.store == nil || c.store.update == nil || c.runtime == nil || c.reconciler == nil {
		return errCreateRoutesNotConfigured
	}
	// The span rides the context so the operation can stamp its supervised
	// spawn without the shared createCommand carrying per-transaction state.
	ctx := withCreateLockSpan(context.Background(), lock)
	// Parsing and scope resolution have already succeeded before a resource
	// handler reaches the transaction. Bind the exact app-owned route here so
	// malformed argv keeps its stable usage failure and no environment probe can
	// preempt it, while reconciliation and every later write share one route.
	if admission != nil {
		read := c.store.snapshot
		if read == nil {
			read = c.store.load
		}
		if read == nil {
			return errCreateRoutesNotConfigured
		}
		registry, err := read()
		if err != nil {
			return MapMetadataError(err)
		}
		if err := admission(registry); err != nil {
			return err
		}
	}
	if err := c.ensureRuntimeRoute(ctx); err != nil {
		return err
	}

	operationID, err := c.newOperationID()
	if err != nil {
		return err
	}
	ledger := NewRuntimeLedgerAt(operationID, c.operationClock()())
	// One transaction is one identity-reuse scope. The first route guard still
	// proves the exact server against tmux; later guards for the same tuple
	// reuse that proof instead of re-reading it under the registry lock. The
	// scope closes before rollback, so unwinding re-proves identity in full.
	c.runtime.openRouteIdentityCache(operationID)
	defer c.runtime.closeRouteIdentityCache()
	// The read-only admission above refuses process anchors before transport.
	// After it passes, preserve the tmux default-read order outside the lock.
	// Admission is repeated against locked bytes before reconciliation or writes.
	clearDefaults := c.runtime.prefetchPaneDefaults(ctx)
	defer clearDefaults()

	guard := combineCreateGuards(guards)

	_, err = c.store.update(func(working *coremetadata.Registry) error {
		lock.enter()
		// Whatever the closure returns, what follows it -- normalize, validate,
		// the durable write, the unlock -- is the store-write phase.
		defer lock.mark(diagnostics.CreatePhaseStoreWrite)
		if admission != nil {
			if err := admission(working.Clone()); err != nil {
				return err
			}
		}
		return c.reconcileCreateOperation(ctx, working, lock, operationID, ledger, guard, op)
	})
	lock.leave()
	if err != nil {
		// Rollback runs after the scope is closed, so every guard of the
		// unwind -- and of the lease clear after it -- proves identity in full.
		c.runtime.closeRouteIdentityCache()
		c.runtime.Rollback(ctx, ledger)
		c.runtime.ClearCreateOperations(ctx, ledger)
		return MapMetadataError(err)
	}
	// On success the lease clear is the transaction's last guarded write and
	// runs inside the scope: its guards may reuse the commit re-proof (or the
	// post-effect proof of the previous guarded write), which no write of ours
	// has followed. Its own guarded-write seam drops that proof after the
	// write, so the post-effect observation proves identity again.
	c.runtime.ClearCreateOperations(ctx, ledger)
	c.runtime.closeRouteIdentityCache()
	return nil
}

// reconcileCreateOperation retains the preflight proof only until a guarded
// write occurs, then re-proves both reconciliation and commit authority.
func (c *createCommand) reconcileCreateOperation(ctx context.Context, working *coremetadata.Registry, lock *createLockSpan, operationID string, ledger *RuntimeLedger, guard createPreReconcile, op createOperation) error {
	preflight, err := guard(ctx, working.Clone(), c.store.mutator(), operationID)
	if err != nil {
		return err
	}
	// The first reconcile pass asks the same guards the same question about
	// the same Registry before any write of ours: it reuses the preflight
	// answer while no guarded write has run since. The second pass follows
	// the create's own writes and always asks tmux again.
	preflightWrites := c.runtime.GuardedWrites
	firstPass := func(ctx context.Context, working coremetadata.Registry, mutator coremetadata.Mutator, operationID string) (liveSessionIdentity, error) {
		if c.runtime.GuardedWrites == preflightWrites {
			return preflight, nil
		}
		return guard(ctx, working, mutator, operationID)
	}
	lock.mark(diagnostics.CreatePhaseFirstReconcile)
	if err := c.reconciler.reconcileGuarded(ctx, working, c.store.mutator(), operationID, firstPass); err != nil {
		return err
	}
	lock.mark(diagnostics.CreatePhaseOperation)
	if err := op(ctx, working, c.store.mutator(), operationID, ledger); err != nil {
		return err
	}
	lock.mark(diagnostics.CreatePhaseSecondReconcile)
	// The lifecycle hook caused by our own tmux mutation deliberately
	// defers while this transaction owns the registry lock. Re-run the same
	// reconciler after all explicit mirrors are in place so the committed
	// status and the live tmux projection agree before create returns.
	if err := c.reconciler.reconcileGuarded(ctx, working, c.store.mutator(), operationID, guard); err != nil {
		return err
	}
	lock.mark(diagnostics.CreatePhaseReprove)
	// Any identity reused inside this transaction is proved once more
	// before commit, so a server that drifted after the first proof rolls
	// the whole operation back instead of committing on stale evidence.
	return c.runtime.reproveReusedRouteIdentity(ctx)
}

// combineCreateGuards preserves each guard's private Registry snapshot and
// refuses conflicting exact session selections before reconciliation.
func combineCreateGuards(guards []createPreReconcile) createPreReconcile {
	return func(ctx context.Context, working coremetadata.Registry, mutator coremetadata.Mutator, operationID string) (liveSessionIdentity, error) {
		var selected liveSessionIdentity
		for _, candidate := range guards {
			if candidate != nil {
				identity, err := candidate(ctx, working.Clone(), mutator, operationID)
				if err != nil {
					return liveSessionIdentity{}, err
				}
				if selected.Name != "" && identity.Name != "" && selected.Name != identity.Name {
					return liveSessionIdentity{}, fmt.Errorf("create: ownership guards selected different tmux sessions %q and %q", selected.Name, identity.Name)
				}
				if selected.ID != "" && identity.ID != "" && selected.ID != identity.ID {
					return liveSessionIdentity{}, fmt.Errorf("create: ownership guards selected different tmux session ids %q and %q", selected.ID, identity.ID)
				}
				if identity.Name != "" {
					selected = identity
				}
			}
		}
		return selected, nil

	}
}
