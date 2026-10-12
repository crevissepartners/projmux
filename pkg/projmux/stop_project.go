package projmux

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
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
	// CodeProjectHasRunningProcessAgents refuses StopProject before any writes
	// when its Registry snapshot contains Running process Agents in the Project.
	// The Refusal message identifies each Agent by name and UID.
	CodeProjectHasRunningProcessAgents Code = "project-has-running-process-agents"
)

// StopProject ends one Project's persistent tmux session while retaining its
// Registry identity, desired topology, root and external assets. If its Registry
// snapshot contains Running process Agents in the Project, it returns a Refusal
// with CodeProjectHasRunningProcessAgents and their names and UIDs before any
// Registry or tmux writes. Use CLI stop project to stop those Agents. Otherwise
// it ends only tmux and does not move clients. Cancellation is effective before
// admission only; admitted execution and compensation finish independently.
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
	windowUIDs := make(map[string]bool)
	for _, window := range reg.WindowsOf(project.Metadata.UID) {
		windowUIDs[window.Metadata.UID] = true
	}
	var runningProcessAgents []string
	for _, agent := range reg.Agents {
		if agent.Status.Phase != metadata.PhaseRunning || agent.Metadata.OwnerRef == nil ||
			agent.Metadata.OwnerRef.Kind != metadata.KindWindow || !windowUIDs[agent.Metadata.OwnerRef.UID] {
			continue
		}
		pane, ok := reg.Pane(agent.Status.PaneRef)
		if ok && mutationruntime.IsNonTmuxRuntimePane(*pane) {
			runningProcessAgents = append(runningProcessAgents, fmt.Sprintf("%s (uid:%s)", agent.Metadata.Name, agent.Metadata.UID))
		}
	}
	if len(runningProcessAgents) != 0 {
		sort.Strings(runningProcessAgents)
		return result, &Refusal{Code: CodeProjectHasRunningProcessAgents, message: fmt.Sprintf(
			"stop project: project/%s has Running process Agents: %s; use CLI stop project to stop them",
			project.Metadata.Name, strings.Join(runningProcessAgents, ", "))}
	}
	root := strings.TrimSpace(project.Spec.Root)
	if root == "" {
		return result, &Refusal{Code: CodeProjectStopRouteRefused, message: fmt.Sprintf("stop project: project/%s has no spec.root to route a runtime through", project.Metadata.Name)}
	}
	socket := req.SocketPath
	if socket == "" {
		socket = c.runtimeSocketPath
	}
	route, err := mutationruntime.ResolveProjectStopRoute(ctx, runner, socket)
	if err != nil {
		if tmux.IsNoServerFailure(err) {
			return result, &Refusal{Code: CodeProjectNotLive, message: mutationruntime.ProjectNotLive(project).Error(), cause: err}
		}
		code := CodeProjectStopRouteRefused
		if socket != "" && !filepath.IsAbs(socket) {
			return result, &Refusal{Code: CodeRuntimeRequestInvalid, message: "StopProjectRequest.SocketPath must be absolute", cause: err}
		}
		return result, deleteRefusal(code, err)
	}
	target, live, err := mutationruntime.FindProjectStopTarget(ctx, runner, route, project, c.projectSessionName(root))
	if err != nil {
		return result, deleteRefusal(CodeProjectStopRouteRefused, err)
	}
	if !live {
		return result, &Refusal{Code: CodeProjectNotLive, message: mutationruntime.ProjectNotLive(project).Error()}
	}
	storage := store.NewStore(store.PathFor(c.paths.StateDir))
	s := &mutationruntime.StopStore{Load: storage.LoadReadOnly, Update: storage.Update, Mutator: store.DefaultMutator}
	internal, err := mutationruntime.StopProject(ctx, runner, target, s, project)
	if copyErr := unregisterCopy(internal, &result); copyErr != nil {
		return result, errors.Join(err, copyErr)
	}
	return result, deleteRefusal(CodeProjectStopExecutionFailed, err)
}
