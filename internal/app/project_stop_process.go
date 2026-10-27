package app

import (
	"context"
	"errors"
	"time"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	storemetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

const projectStopAgentsStillRunning = mutationruntime.ProjectStopAgentsStillRunning

type projectProcessStopper func(context.Context, *resourceStore, string) ([]cli.ReceiptResource, error)
type projectProcessStopTarget struct {
	agent coremetadata.Agent
	pane  coremetadata.Pane
}

func runningProjectProcessAgents(reg coremetadata.Registry, projectUID string) []projectProcessStopTarget {
	var targets []projectProcessStopTarget
	for _, target := range mutationruntime.RunningProjectProcessAgents(reg, projectUID) {
		targets = append(targets, projectProcessStopTarget{target.Agent, target.Pane})
	}
	return targets
}

func stopProjectProcessAgents(ctx context.Context, store *resourceStore, projectUID string) ([]cli.ReceiptResource, error) {
	return stopProjectProcessAgentsWith(ctx, store, projectUID, nil, nil, processRelaunchRetirementTimeout)
}

func stopProjectProcessAgentsWith(ctx context.Context, store *resourceStore, projectUID string,
	stop func(context.Context, coremetadata.Registry, coremetadata.Agent, string, string, string) (string, error),
	identity processIdentityReader, timeout time.Duration) ([]cli.ReceiptResource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if store == nil {
		return nil, errors.New("project stop Registry store is not configured")
	}
	// Final observation must use the same Registry authority as Load, including
	// callers that supply an in-memory store rather than a default file path.
	update := store.updateConvergent
	if update == nil {
		update = func(apply func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error) {
			if store.update == nil {
				return coremetadata.Registry{}, false, errors.New("project stop Registry write store is not configured")
			}
			reg, err := store.update(apply)
			return reg, err == nil, err
		}
	}
	executor := mutationruntime.ProjectProcessStopExecutor{Update: update, Load: store.load, ReadIdentity: mutationruntime.ProcessIdentityReader(identity), Timeout: timeout}
	if store.stateDir != nil {
		state, err := store.stateDir()
		if err != nil {
			return nil, err
		}
		executor.RegistryPath = storemetadata.PathFor(state)
	}
	if stop != nil {
		executor.CallStop = func(ctx context.Context, reg coremetadata.Registry, agent coremetadata.Agent) error {
			pane, ok := reg.Pane(agent.Status.PaneRef)
			if !ok || pane.Status.ProcessSession == nil {
				return errors.New("stale process Pane")
			}
			_, err := stop(ctx, reg, agent, pane.Status.ProcessSession.Provider, "stop", "")
			return err
		}
	}
	return executor.Stop(ctx, projectUID)
}
