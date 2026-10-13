package app

import (
	"context"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

type nativeCreationEngine struct {
	Controller codexNativeThreadController
	Spelling   string
	Launcher   codexNativeAgentLauncher
	Route      codexNativeEndpointRoute
	Prompt     string
	Runner     func() tmuxCommandRunner
}

func (n *nativeCreationEngine) engine() *mutationruntime.NativeCreationEngine {
	return &mutationruntime.NativeCreationEngine{
		Spelling: n.Spelling, Endpoint: n.Route.Endpoint, Prompt: n.Prompt,
		AdmitEndpoint: func(ctx context.Context) (coremetadata.CodexEndpointRef, error) {
			ctx, cancel := prepareNativeContext(ctx)
			defer cancel()
			route, err := n.Controller.Resolve(ctx, n.Route.Endpoint)
			if err == nil && (!route.valid() || !route.Endpoint.Same(n.Route.Endpoint) || route.State != coremetadata.CodexGenerationCurrent) {
				err = &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable}
			}
			if err == nil {
				n.Route = route
			}
			return route.Endpoint, err
		},
		CreateThread: func(ctx context.Context, input mutationruntime.NativeCodexCreateInput) (codexappserver.ThreadBinding, error) {
			ctx, cancel := prepareNativeContext(ctx)
			defer cancel()
			return n.Controller.Create(ctx, n.Route, input)
		},
		Plan: func(w coremetadata.AgentWorkspace, id, model, effort string) (string, []string, error) {
			return planNativeCodexResumeOptions(n.Launcher, n.Route, w, id, model, effort)
		},
		BindPane: func(ctx context.Context, pane string, w coremetadata.AgentWorkspace, b mutationruntime.NativeAgentBinding) error {
			return bindNativeCodexPaneOnRoute(ctx, n.Launcher, n.Runner(), pane, w.CWD, b.Title, "", b.ThreadID)
		},
		PreparationRefusal: func(err error) error { return nativeCreatePreparationRefusal(n.Spelling, err) },
		LaunchError:        func(err error) error { return nativeLaunchError(n.Spelling, err) },
		FanOutRefusal:      func(count int) error { return nativeFanOutRefusal(n.Spelling, count) },
		SafeFallback:       func(err error) bool { return nativeFallbackAllowed(n.Controller, err) },
	}
}
func (n *nativeCreationEngine) Admit(ctx context.Context, count int) error {
	return n.engine().Admit(ctx, count)
}
func (n *nativeCreationEngine) Create(ctx context.Context, r *coremetadata.Registry, m coremetadata.Mutator, w mutationruntime.AgentWork, workspace coremetadata.AgentWorkspace, p mutationruntime.AgentPreparation) (mutationruntime.NativeAgentBinding, error) {
	out, err := n.engine().Create(ctx, r, m, w, workspace, p)
	return out, MapMetadataError(err)
}
func (n *nativeCreationEngine) Bind(ctx context.Context, w mutationruntime.AgentWork, workspace coremetadata.AgentWorkspace, pane string, b mutationruntime.NativeAgentBinding) error {
	return n.engine().Bind(ctx, w, workspace, pane, b)
}
