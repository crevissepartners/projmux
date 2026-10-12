package app

import (
	"context"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

func (c *deleteCommand) deleteExecutor(target tmuxTransport) *mutationruntime.DeleteExecutor {
	e := &mutationruntime.DeleteExecutor{Windows: c.windows, NewOperationID: c.newOperationID, MapError: MapMetadataError, Via: c.deletionVia(), Actor: func(r *coremetadata.Registry) DeletionActor { return c.observeDeletionActor(target, r) }}
	if c.store != nil {
		e.Mutate = c.store.mutate
		e.Update = c.store.update
		e.Mutator = c.store.mutator
		e.StateDir = c.store.stateDir
	}
	if c.panes != nil {
		e.Panes = mutationruntime.PaneDeleteRuntime{Preflight: c.panes.preflight, PrepareReplacements: c.panes.prepareReplacements, RollbackReplacements: c.panes.rollbackReplacements, Kill: c.panes.kill, TombstoneSelfKill: c.panes.tombstoneSelfKill, RestoreSelfKill: c.panes.restoreSelfKill}
		if batch, ok := c.panes.(interface {
			KillAll(context.Context, []paneLiveDeleteTarget) (int, error)
		}); ok {
			e.Panes.KillAll = batch.KillAll
		}
	}
	return e
}
