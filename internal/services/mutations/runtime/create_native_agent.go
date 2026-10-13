package runtime

import (
	"context"
	"fmt"
	"strings"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/sessionhistory"
)

// NativeCreationEngine is the single native create transaction adapter. Its
// backend opens the existing endpoint; it never retries an indeterminate RPC.
type NativeCreationEngine struct {
	Spelling           string
	Endpoint           metadata.CodexEndpointRef
	Prompt             string
	AdmitEndpoint      func(context.Context) (metadata.CodexEndpointRef, error)
	CreateThread       func(context.Context, NativeCodexCreateInput) (codexappserver.ThreadBinding, error)
	Plan               func(metadata.AgentWorkspace, string, string, string) (string, []string, error)
	BindPane           func(context.Context, string, metadata.AgentWorkspace, NativeAgentBinding) error
	PreparationRefusal func(error) error
	LaunchError        func(error) error
	FanOutRefusal      func(int) error
	SafeFallback       func(error) bool
}

func (n *NativeCreationEngine) Admit(ctx context.Context, count int) error {
	if count > 1 {
		return n.FanOutRefusal(count)
	}
	endpoint, err := n.AdmitEndpoint(ctx)
	if err == nil && !endpoint.Same(n.Endpoint) {
		err = codexappserver.ErrEndpointChanged
	}
	if err != nil {
		return n.PreparationRefusal(err)
	}
	n.Endpoint = endpoint
	return nil
}
func (n *NativeCreationEngine) Create(ctx context.Context, working *metadata.Registry, m metadata.Mutator, work AgentWork, w metadata.AgentWorkspace, p AgentPreparation) (NativeAgentBinding, error) {
	var out NativeAgentBinding
	if err := m.StageCodexEndpoint(working, work.Agent.Metadata.UID, n.Endpoint); err != nil {
		return out, MapMetadataError(err)
	}
	instructions := ""
	if p.DeveloperInstructions != nil {
		instructions = p.DeveloperInstructions(work.Agent.Metadata.UID)
	}
	binding, err := n.CreateThread(ctx, NativeCodexCreateInput{Workspace: w, DeveloperInstructions: instructions, Policy: p.Policy, Model: p.Model, Effort: p.Effort, Prompt: n.Prompt, RequestKey: work.Activation.Generation})
	if err != nil {
		if n.SafeFallback(err) || NativeRootsUnsupported(err) {
			return out, n.PreparationRefusal(err)
		}
		return out, n.LaunchError(err)
	}
	if strings.TrimSpace(binding.ThreadID) == "" {
		return out, n.LaunchError(fmt.Errorf("%w: native create returned an empty thread", codexappserver.ErrProtocol))
	}
	title, argv, err := n.Plan(w, binding.ThreadID, p.Model, p.Effort)
	if err != nil {
		return out, n.LaunchError(err)
	}
	if _, err := m.BindCodexActivation(working, metadata.CodexActivationObservation{AgentUID: work.Agent.Metadata.UID, PaneUID: work.Pane.Metadata.UID, Generation: work.Activation.Generation, ThreadID: binding.ThreadID, TurnID: binding.TurnID, Endpoint: n.Endpoint}); err != nil {
		return out, MapMetadataError(err)
	}
	out = NativeAgentBinding{Title: title, Argv: argv, ThreadID: binding.ThreadID, TurnID: binding.TurnID}
	if bound, ok := working.Agent(work.Agent.Metadata.UID); ok {
		if row, valid := sessionhistory.ObservedRecordFor(working, bound.Metadata.UID, bound.Status.SessionRef); valid {
			out.History = []sessionhistory.Record{row}
		}
	}
	return out, nil
}
func (n *NativeCreationEngine) Bind(ctx context.Context, _ AgentWork, w metadata.AgentWorkspace, pane string, b NativeAgentBinding) error {
	if err := n.BindPane(ctx, pane, w, b); err != nil {
		return fmt.Errorf("%s: bind native Codex Pane %s presentation metadata: %w", n.Spelling, pane, err)
	}
	return nil
}
