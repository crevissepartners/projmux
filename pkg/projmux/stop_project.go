package projmux

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/receipt"
	"github.com/crevissepartners/projmux/internal/core/selector"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/services/mutations"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/internal/services/resources"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

const (
	CodeProjectNotLive             Code = "project-not-live"
	CodeProjectStopRouteRefused    Code = "project-stop-route-refused"
	CodeProjectStopExecutionFailed Code = "project-stop-execution-failed"
	// CodeProjectHasRunningProcessAgents remains exported for source compatibility.
	CodeProjectHasRunningProcessAgents Code = "project-has-running-process-agents"
)

// StopProject stops Running process Agents and the exact persistent tmux
// session through the CLI's shared executor, retaining Project identity,
// desired topology, Agent/Pane identities and conversations. Partial failures
// include confirmed Agent stops in Receipt. Child-free claims remain refused.
// Cancellation prevents admission; admitted Stop and compensation finish
// independently, with process retirement bounded by the earlier caller deadline
// or ten seconds. A process-only Project needs no live tmux server.
func (c *Client) StopProject(ctx context.Context, req core.StopProjectRequest) (core.StopProjectResult, error) {
	return c.stopProjectWithRunner(ctx, req, tmux.ExecRunner{})
}
func (c *Client) stopProjectWithRunner(ctx context.Context, req core.StopProjectRequest, runner mutationruntime.TmuxCommandRunner) (core.StopProjectResult, error) {
	result := core.StopProjectResult{}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if strings.TrimSpace(req.Ref) == "" {
		return result, &Refusal{Code: CodeSelectorInvalid, message: "StopProjectRequest.Ref must name one Project"}
	}
	// Input shape uses the same parser and named-field rendering as mutationQuery;
	// the shared resolver still owns selection and its CLI-equivalent outcomes.
	if ref := strings.TrimSpace(req.Ref); !filepath.IsAbs(ref) {
		if _, err := selector.ParseRef(metadata.KindProject, ref); err != nil {
			return result, resourceRefusal(selectorInputError(err, "StopProjectRequest.Ref"))
		}
	}

	if req.SocketPath != "" && !filepath.IsAbs(req.SocketPath) {
		return result, &Refusal{Code: CodeRuntimeRequestInvalid, message: "StopProjectRequest.SocketPath must be absolute"}
	}

	if c.pathsErr != nil {
		return result, resourceRefusal(c.pathsErr)
	}
	reg, err := resources.Load(c.paths)
	if err != nil {
		return result, resourceRefusal(err)
	}
	project, err := mutations.ResolveProjectRef(func() (metadata.Registry, error) { return reg, nil }, "stop project", req.Ref, func(message string) error { return &Refusal{Code: CodeResourceNotFound, message: message} }, func(err error) error { return err })
	if err != nil {
		return result, resourceRefusal(err)
	}
	hasProcess := len(mutationruntime.RunningProjectProcessAgents(reg, project.Metadata.UID)) != 0
	root := strings.TrimSpace(project.Spec.Root)
	if root == "" {
		return result, &Refusal{Code: CodeProjectStopRouteRefused, message: fmt.Sprintf("stop project: project/%s has no spec.root to route a runtime through", project.Metadata.Name)}
	}
	socket := req.SocketPath
	if socket == "" {
		socket = c.runtimeSocketPath
	}
	var target mutationruntime.ManagedRuntimeStopTarget
	var live bool
	route, err := mutationruntime.ResolveProjectStopRoute(ctx, runner, socket)
	if err != nil {
		// Process-only Project stop does not depend on tmux being installed or
		// running. Other route failures still refuse mixed runtime authority.
		if !tmux.IsNoServerFailure(err) && !errors.Is(err, exec.ErrNotFound) {
			return result, deleteRefusal(CodeProjectStopRouteRefused, err)
		}
		if !hasProcess {
			return result, &Refusal{Code: CodeProjectNotLive, message: mutationruntime.ProjectNotLive(project).Error(), cause: err}
		}
	} else {
		target, live, err = mutationruntime.FindProjectStopTarget(ctx, runner, route, project, c.projectSessionName(root))
		if err != nil {
			return result, deleteRefusal(CodeProjectStopRouteRefused, err)
		}
	}
	if !live && !hasProcess {
		return result, &Refusal{Code: CodeProjectNotLive, message: mutationruntime.ProjectNotLive(project).Error()}
	}
	storage := store.NewStore(store.PathFor(c.paths.StateDir))
	s := &mutationruntime.StopStore{Load: storage.LoadReadOnly, Update: storage.Update, Mutator: store.DefaultMutator}
	internal := mutationruntime.StopProjectResult{}
	var stopTmux func(context.Context) error
	if live {
		stopTmux = func(execution context.Context) error {
			var stopErr error
			internal, stopErr = mutationruntime.StopProject(execution, runner, target, s, project)
			return stopErr
		}
	}
	executor := mutationruntime.ProjectProcessStopExecutor{RegistryPath: store.PathFor(c.paths.StateDir), Load: storage.LoadReadOnly, Update: storage.UpdateConvergentContext}
	stopped, tmuxStopped, err := mutationruntime.ExecuteProjectStop(ctx, func(execution context.Context) ([]receipt.ReceiptResource, error) {
		return executor.Stop(execution, project.Metadata.UID)
	}, stopTmux)
	internal.Stopped = err == nil && (tmuxStopped || len(stopped) != 0)
	if tmuxStopped || len(stopped) != 0 {
		runtime := receipt.RuntimeStopped
		if err != nil {
			runtime = receipt.RuntimePartiallyStopped
		} else if !live {
			runtime = receipt.RuntimeAgentsStopped
		}
		internal.Receipt = receipt.NewReceipt(receipt.OperationStopProject, receipt.ReceiptTarget{Kind: "Project", UID: project.Metadata.UID, Name: project.Metadata.Name}, receipt.ReceiptEffects{Identity: receipt.IdentityUnchanged, Address: receipt.AddressUnchanged, Topology: receipt.TopologyUnchanged, DesiredState: receipt.DesiredStateUnchanged, Runtime: runtime, Focus: receipt.FocusUnchanged})
		action := receipt.ActionStopped
		if !tmuxStopped {
			action = receipt.ActionUnchanged
		}
		internal.Receipt.Add("Project", project.Metadata.UID, project.Metadata.Name, action)
		for _, agent := range stopped {
			internal.Receipt.Add(agent.Kind, agent.UID, agent.Name, agent.Action)
		}
	}
	if copyErr := unregisterCopy(internal, &result); copyErr != nil {
		return result, errors.Join(err, copyErr)
	}
	return result, deleteRefusal(CodeProjectStopExecutionFailed, err)
}
