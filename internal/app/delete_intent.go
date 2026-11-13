package app

import (
	"context"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

func (c *deleteCommand) deleteExecutor(target tmuxTransport) *mutationruntime.DeleteExecutor {
	e := &mutationruntime.DeleteExecutor{Windows: c.windows, NewOperationID: c.newOperationID, MapError: MapMetadataError, Via: c.deletionVia(), Actor: func(r *coremetadata.Registry) DeletionActor { return c.observeDeletionActor(target, r) }}
	if c.store != nil {
		e.Process = c.windowProcessDeleter().shared()
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

// windowProcessDeleter is the process Agent deleter a Window delete uses to
// stop the running process providers its plan removes.
func (c *deleteCommand) windowProcessDeleter() *processAgentDeleter {
	d := c.processDeleter
	if d == nil {
		d = newProcessAgentDeleter()
	}
	d.store, d.via = c.store, c.deletionVia()
	return d
}

// windowProcessTargets reads the process Agents a Window plan holds, for the
// dry run and the confirmation text. It is best effort: a classification that
// cannot be read is refused later by the executor, never here.
func (c *deleteCommand) windowProcessTargets(registry coremetadata.Registry, plan deletePlan) []processDeleteTarget {
	if c.store == nil {
		return nil
	}
	d := c.windowProcessDeleter()
	var out []processDeleteTarget
	for _, uid := range mutationruntime.ProcessDeleteAgentUIDs(registry, mutationruntime.DeletePlan(plan)) {
		if t, err := d.classify(registry, uid); err == nil {
			out = append(out, t)
		}
	}
	return out
}
