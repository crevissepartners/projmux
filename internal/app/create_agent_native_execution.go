package app

import (
	"context"
	"fmt"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/sessionhistory"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"strings"
)

type nativeCreationEngine struct {
	Controller codexNativeThreadController
	Spelling   string
	Launcher   codexNativeAgentLauncher
	Route      codexNativeEndpointRoute
	Prompt     string
	Runner     func() tmuxCommandRunner
	History    func(*coremetadata.Registry, string) []sessionhistory.Record
}

func (n *nativeCreationEngine) Admit(ctx context.Context, count int) error {
	if count > 1 {
		return nativeFanOutRefusal(n.Spelling, count)
	}
	admissionCtx, cancel := prepareNativeContext(ctx)
	defer cancel()
	admitted, err := n.Controller.Resolve(admissionCtx, n.Route.Endpoint)
	if err != nil || !admitted.valid() || !admitted.Endpoint.Same(n.Route.Endpoint) || admitted.State != coremetadata.CodexGenerationCurrent {
		if err == nil {
			err = &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable}
		}
		return nativeCreatePreparationRefusal(n.Spelling, err)
	}
	n.Route = admitted
	return nil
}
func (n *nativeCreationEngine) Create(ctx context.Context, working *coremetadata.Registry, mutator coremetadata.Mutator, work mutationruntime.AgentWork, workspace coremetadata.AgentWorkspace, preparation mutationruntime.AgentPreparation) (mutationruntime.NativeAgentBinding, error) {
	var out mutationruntime.NativeAgentBinding
	if err := mutator.StageCodexEndpoint(working, work.Agent.Metadata.UID, n.Route.Endpoint); err != nil {
		return out, MapMetadataError(err)
	}

	nativeCtx, cancel := prepareNativeContext(ctx)
	prepared, nativeErr := n.Controller.Create(nativeCtx, n.Route, codexNativeCreateInput{Workspace: workspace, DeveloperInstructions: preparation.DeveloperInstructions(work.Agent.Metadata.UID), Policy: preparation.Policy, Model: preparation.Model, Effort: preparation.Effort, Prompt: n.Prompt, RequestKey: work.Activation.Generation})
	cancel()
	switch {
	case nativeErr == nil && strings.TrimSpace(prepared.ThreadID) != "":
		title, argv, err := planNativeCodexResumeOptions(n.Launcher, n.Route, workspace, prepared.ThreadID, preparation.Model, preparation.Effort)
		if err != nil {
			return out, nativeLaunchError(n.Spelling, err)
		}
		if _, err := mutator.BindCodexActivation(working, coremetadata.CodexActivationObservation{AgentUID: work.Agent.Metadata.UID, PaneUID: work.Pane.Metadata.UID, Generation: work.Activation.Generation, ThreadID: prepared.ThreadID, TurnID: prepared.TurnID, Endpoint: n.Route.Endpoint}); err != nil {
			return out, MapMetadataError(err)
		}
		out = mutationruntime.NativeAgentBinding{Title: title, Argv: argv, ThreadID: prepared.ThreadID, TurnID: prepared.TurnID}
		out.History = n.History(working, work.Agent.Metadata.UID)
		return out, nil
	case nativeErr == nil:
		return out, nativeLaunchError(n.Spelling, fmt.Errorf("%w: native create returned an empty thread", codexappserver.ErrProtocol))
	case nativeFallbackAllowed(n.Controller, nativeErr), nativeRootsUnsupported(nativeErr):
		return out, nativeCreatePreparationRefusal(n.Spelling, nativeErr)
	default:
		return out, nativeLaunchError(n.Spelling, nativeErr)
	}
}
func (n *nativeCreationEngine) Bind(ctx context.Context, _ mutationruntime.AgentWork, workspace coremetadata.AgentWorkspace, paneID string, binding mutationruntime.NativeAgentBinding) error {
	if err := bindNativeCodexPaneOnRoute(ctx, n.Launcher, n.Runner(), paneID, workspace.CWD, binding.Title, "", binding.ThreadID); err != nil {
		return fmt.Errorf("%s: bind native Codex Pane %s presentation metadata: %w", n.Spelling, paneID, err)
	}
	return nil
}
