package app

import (
	"context"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

const createKindUnrecorded diagnostics.CreateKind = ""

type createOutcomeRecorder struct{ recorder *diagnostics.CreateRecorder }

func (r createOutcomeRecorder) Record(o mutationruntime.CreateOutcome) {
	r.recorder.Record(diagnostics.CreateOutcome{Kind: diagnostics.CreateKind(o.Kind), Result: diagnostics.LifecycleResult(o.Result), Duration: o.Duration, LockHeld: o.LockHeld, Phases: diagnostics.CreatePhases(o.Phases), SpawnToRelease: o.SpawnToRelease})
}
func (c *createCommand) createTransaction() *mutationruntime.CreateTransaction {
	if c == nil {
		return nil
	}
	t := &mutationruntime.CreateTransaction{Runtime: c.runtime, Reconciler: c.reconciler, NewOperationID: c.newOperationID, Now: c.now, OutcomeClock: c.outcomeClock}
	t.EnsureRuntimeRoute = func(ctx context.Context) error {
		err := c.ensureRuntimeRoute(ctx)
		t.Runtime = c.runtime
		t.Reconciler = c.reconciler
		return err
	}
	if c.outcomes != nil {
		t.Outcomes = createOutcomeRecorder{c.outcomes}
	}
	if c.store != nil {
		t.Store = &mutationruntime.CreateStore{Load: c.store.load, Snapshot: c.store.snapshot, Update: c.store.update, Mutator: c.store.mutator}
	}
	return t
}
func (c *createCommand) transact(kind diagnostics.CreateKind, op createOperation, guards ...createPreReconcile) error {
	return MapMetadataError(c.createTransaction().Transact(mutationruntime.CreateKind(kind), op, guards...))
}
func (c *createCommand) transactAdmitted(kind diagnostics.CreateKind, admit func(coremetadata.Registry) error, op createOperation, guards ...createPreReconcile) error {
	return MapMetadataError(c.createTransaction().TransactAdmitted(mutationruntime.CreateKind(kind), admit, op, guards...))
}
