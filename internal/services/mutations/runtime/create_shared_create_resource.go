package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// CreateOperation is one create body, run inside the registry transaction.
type CreateOperation func(
	ctx context.Context,
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	operationID string,
	ledger *RuntimeLedger,
) error

type CreatePreReconcile func(
	ctx context.Context,
	working coremetadata.Registry,
	mutator coremetadata.Mutator,
	operationID string,
) (LiveSessionIdentity, error)

// OperationClock is the Clock the transaction ledger's create-operation marker
// is stamped from. Production wires time.Now; a nil seam falls back to it.
func (c *CreateTransaction) OperationClock() func() time.Time {
	if c == nil || c.Now == nil {
		return time.Now
	}
	return c.Now
}

// CreateKindUnrecorded is the kind of a route that runs through Transact but
// is not a create -- the generated Window rename. It writes no create.outcome.
const CreateKindUnrecorded CreateKind = ""

// CreateOutcomeClock is the Clock create.outcome timings are measured on.
func (c *CreateTransaction) CreateOutcomeClock() func() time.Time {
	if c == nil || c.OutcomeClock == nil {
		return time.Now
	}
	return c.OutcomeClock
}

// CreateLockSpan measures how long one transaction Held the Registry lock:
// from entering the mutation closure to the Registry update returning, so it
// covers the closure, the store's own normalize/validate/write, and the unlock,
// and never the wait for the lock or the Registry read before the closure.
//
// The span is also split into the closed create Phases. Enter opens the guard
// Phase, each Mark closes the open Phase and opens the next on one Clock
// reading, and Leave closes whichever Phase is open, so the Phases tile the
// hold exactly. A Phase the transaction never reached stays nil. The first
// supervised child spawn inside the span is stamped too, so Leave can report
// how much of the hold came after it.
//
// One span belongs to one transaction and is only touched from the goroutine
// running it; it travels to the operation through the context, never through
// the CreateTransaction, which concurrent transactions may share.
type CreateLockSpan struct {
	Clock     func() time.Time
	Entered   bool
	EnteredAt time.Time
	Held      *time.Duration

	Phase          CreatePhase
	PhaseStartedAt time.Time
	Phases         CreatePhases

	Spawned        bool
	SpawnedAt      time.Time
	SpawnToRelease *time.Duration
}

func (s *CreateLockSpan) Enter() {
	if !s.Entered {
		s.Entered, s.EnteredAt = true, s.Clock()
		s.Phase, s.PhaseStartedAt = CreatePhaseGuard, s.EnteredAt
	}
}

// Mark closes the open Phase and opens next. It is a no-op outside the span.
func (s *CreateLockSpan) Mark(next CreatePhase) {
	if s == nil || !s.Entered || s.Held != nil {
		return
	}
	now := s.Clock()
	s.ClosePhase(now)
	s.Phase, s.PhaseStartedAt = next, now
}

func (s *CreateLockSpan) ClosePhase(now time.Time) {
	elapsed := now.Sub(s.PhaseStartedAt)
	if previous := s.Phases[s.Phase]; previous != nil {
		elapsed += *previous
	}
	s.Phases[s.Phase] = &elapsed
}

// MarkSpawn stamps the first supervised child spawn inside the span.
func (s *CreateLockSpan) MarkSpawn() {
	if s == nil || !s.Entered || s.Held != nil || s.Spawned {
		return
	}
	s.Spawned, s.SpawnedAt = true, s.Clock()
}

func (s *CreateLockSpan) Leave() {
	if s.Entered {
		now := s.Clock()
		Held := now.Sub(s.EnteredAt)
		s.Held = &Held
		s.ClosePhase(now)
		if s.Spawned {
			SpawnToRelease := now.Sub(s.SpawnedAt)
			s.SpawnToRelease = &SpawnToRelease
		}
	}
}

type CreateLockSpanKey struct{}

// WithCreateLockSpan hands the transaction's span to the operation it runs.
func WithCreateLockSpan(ctx context.Context, span *CreateLockSpan) context.Context {
	if span == nil {
		return ctx
	}
	return context.WithValue(ctx, CreateLockSpanKey{}, span)
}

// MarkSupervisedSpawn records, once per transaction, the moment the split
// that starts a managed Pane's supervisor returned a Pane: from then on the
// supervised child is running and will need the Registry lock this
// transaction still holds. Outside a create transaction it does nothing.
func MarkSupervisedSpawn(ctx context.Context) {
	if span, ok := ctx.Value(CreateLockSpanKey{}).(*CreateLockSpan); ok {
		span.MarkSpawn()
	}
}

// Transact runs one create operation and records its create.outcome.
//
// The record is measurement only. It is appended after the whole transaction
// returned -- the Registry lock released, rollback and lease clear done -- and a
// journal failure never reaches the create's result. kind names the create;
// CreateKindUnrecorded records nothing.
func (c *CreateTransaction) Transact(kind CreateKind, op CreateOperation, guards ...CreatePreReconcile) error {
	return c.TransactAdmitted(kind, nil, op, guards...)
}

func (c *CreateTransaction) TransactAdmitted(kind CreateKind, admission func(coremetadata.Registry) error, op CreateOperation, guards ...CreatePreReconcile) error {
	if c == nil {
		return ErrCreateRoutesNotConfigured
	}
	Clock := c.CreateOutcomeClock()
	started := Clock()
	lock := CreateLockSpan{Clock: Clock}
	err := c.RunTransaction(&lock, admission, op, guards...)
	if kind != CreateKindUnrecorded && c.Outcomes != nil {
		result := "success"
		if err != nil {
			result = "error"
		}
		c.Outcomes.Record(CreateOutcome{
			Kind: kind, Result: result, Duration: Clock().Sub(started), LockHeld: lock.Held,
			Phases: lock.Phases, SpawnToRelease: lock.SpawnToRelease,
		})
	}
	return err
}

var ErrCreateRoutesNotConfigured = errors.New("create: the resource-backed create routes are not configured")

// RunTransaction runs one create operation under the declared transaction
// order: full preflight -> operation id -> created-resource ledger -> metadata
// mutation -> runtime mutation -> commit.
//
// Everything happens inside a single registry transaction, including the
// mutation-route registry reconciliation. The store applies the operation to a
// private clone and only writes when the whole body succeeds, so any failure --
// a pre-create hook refusal, a stale anchor, a tmux error -- leaves the registry
// file byte-identical. The tmux objects the body created are undone from the
// runtime ledger, which is the half no transaction can roll back for us.
func (c *CreateTransaction) RunTransaction(lock *CreateLockSpan, admission func(coremetadata.Registry) error, op CreateOperation, guards ...CreatePreReconcile) error {
	if c == nil || c.Store == nil || c.Store.Update == nil || c.Runtime == nil || c.Reconciler == nil {
		return ErrCreateRoutesNotConfigured
	}
	// The span rides the context so the operation can stamp its supervised
	// spawn without the shared CreateTransaction carrying per-transaction state.
	ctx := WithCreateLockSpan(context.Background(), lock)
	// Parsing and scope resolution have already succeeded before a resource
	// handler reaches the transaction. Bind the exact app-owned route here so
	// malformed argv keeps its stable usage failure and no environment probe can
	// preempt it, while reconciliation and every later write share one route.
	if admission != nil {
		read := c.Store.Snapshot
		if read == nil {
			read = c.Store.Load
		}
		if read == nil {
			return ErrCreateRoutesNotConfigured
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

	operationID, err := c.NewOperationID()
	if err != nil {
		return err
	}
	ledger := NewRuntimeLedgerAt(operationID, c.OperationClock()())
	// One transaction is one identity-reuse scope. The first route guard still
	// proves the exact server against tmux; later guards for the same tuple
	// reuse that proof instead of re-reading it under the registry lock. The
	// scope closes before rollback, so unwinding re-proves identity in full.
	c.Runtime.OpenRouteIdentityCache(operationID)
	defer c.Runtime.CloseRouteIdentityCache()
	// The read-only admission above refuses process anchors before transport.
	// After it passes, preserve the tmux default-read order outside the lock.
	// Admission is repeated against locked bytes before reconciliation or writes.
	clearDefaults := c.Runtime.PrefetchPaneDefaults(ctx)
	defer clearDefaults()

	guard := CombineCreateGuards(guards)

	_, err = c.Store.Update(func(working *coremetadata.Registry) error {
		lock.Enter()
		// Whatever the closure returns, what follows it -- normalize, validate,
		// the durable write, the unlock -- is the store-write Phase.
		defer lock.Mark(CreatePhaseStoreWrite)
		if admission != nil {
			if err := admission(working.Clone()); err != nil {
				return err
			}
		}
		return c.ReconcileCreateOperation(ctx, working, lock, operationID, ledger, guard, op)
	})
	lock.Leave()
	if err != nil {
		// Rollback runs after the scope is closed, so every guard of the
		// unwind -- and of the lease clear after it -- proves identity in full.
		c.Runtime.CloseRouteIdentityCache()
		c.Runtime.Rollback(ctx, ledger)
		c.Runtime.ClearCreateOperations(ctx, ledger)
		return MapMetadataError(err)
	}
	// On success the lease clear is the transaction's last guarded write and
	// runs inside the scope: its guards may reuse the commit re-proof (or the
	// post-effect proof of the previous guarded write), which no write of ours
	// has followed. Its own guarded-write seam drops that proof after the
	// write, so the post-effect observation proves identity again.
	c.Runtime.ClearCreateOperations(ctx, ledger)
	c.Runtime.CloseRouteIdentityCache()
	return nil
}

// ReconcileCreateOperation retains the preflight proof only until a guarded
// write occurs, then re-proves both reconciliation and commit authority.
func (c *CreateTransaction) ReconcileCreateOperation(ctx context.Context, working *coremetadata.Registry, lock *CreateLockSpan, operationID string, ledger *RuntimeLedger, guard CreatePreReconcile, op CreateOperation) error {
	preflight, err := guard(ctx, working.Clone(), c.Store.Mutator(), operationID)
	if err != nil {
		return err
	}
	// The first reconcile pass asks the same guards the same question about
	// the same Registry before any write of ours: it reuses the preflight
	// answer while no guarded write has run since. The second pass follows
	// the create's own writes and always asks tmux again.
	preflightWrites := c.Runtime.GuardedWrites
	firstPass := func(ctx context.Context, working coremetadata.Registry, mutator coremetadata.Mutator, operationID string) (LiveSessionIdentity, error) {
		if c.Runtime.GuardedWrites == preflightWrites {
			return preflight, nil
		}
		return guard(ctx, working, mutator, operationID)
	}
	lock.Mark(CreatePhaseFirstReconcile)
	if err := c.Reconciler.ReconcileGuarded(ctx, working, c.Store.Mutator(), operationID, firstPass); err != nil {
		return err
	}
	lock.Mark(CreatePhaseOperation)
	if err := op(ctx, working, c.Store.Mutator(), operationID, ledger); err != nil {
		return err
	}
	lock.Mark(CreatePhaseSecondReconcile)
	// The lifecycle hook caused by our own tmux mutation deliberately
	// defers while this transaction owns the registry lock. Re-run the same
	// reconciler after all explicit mirrors are in place so the committed
	// status and the live tmux projection agree before create returns.
	if err := c.Reconciler.ReconcileGuarded(ctx, working, c.Store.Mutator(), operationID, guard); err != nil {
		return err
	}
	lock.Mark(CreatePhaseReprove)
	// Any identity reused inside this transaction is proved once more
	// before commit, so a server that drifted after the first proof rolls
	// the whole operation back instead of committing on stale evidence.
	return c.Runtime.ReproveReusedRouteIdentity(ctx)
}

// CombineCreateGuards preserves each guard's private Registry snapshot and
// refuses conflicting exact session selections before reconciliation.
func CombineCreateGuards(guards []CreatePreReconcile) CreatePreReconcile {
	return func(ctx context.Context, working coremetadata.Registry, mutator coremetadata.Mutator, operationID string) (LiveSessionIdentity, error) {
		var selected LiveSessionIdentity
		for _, candidate := range guards {
			if candidate != nil {
				identity, err := candidate(ctx, working.Clone(), mutator, operationID)
				if err != nil {
					return LiveSessionIdentity{}, err
				}
				if selected.Name != "" && identity.Name != "" && selected.Name != identity.Name {
					return LiveSessionIdentity{}, fmt.Errorf("create: ownership guards selected different tmux sessions %q and %q", selected.Name, identity.Name)
				}
				if selected.ID != "" && identity.ID != "" && selected.ID != identity.ID {
					return LiveSessionIdentity{}, fmt.Errorf("create: ownership guards selected different tmux session ids %q and %q", selected.ID, identity.ID)
				}
				if identity.Name != "" {
					selected = identity
				}
			}
		}
		return selected, nil

	}
}

func (c *CreateTransaction) ensureRuntimeRoute(ctx context.Context) error {
	if c.EnsureRuntimeRoute == nil {
		return nil
	}
	return c.EnsureRuntimeRoute(ctx)
}
