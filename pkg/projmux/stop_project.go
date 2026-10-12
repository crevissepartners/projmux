package projmux

import (
	"context"
	"errors"
	"fmt"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"path/filepath"
	"strings"

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
)

// StopProject ends one Project's persistent tmux session while retaining its
// Registry identity, desired topology, root and external assets. It does not
// stop process Agents or move clients. Cancellation is effective before
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
