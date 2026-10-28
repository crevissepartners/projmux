package runtime

import (
	"context"
	"fmt"
	"strings"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// NativeResumeEngine is the native half of a resume-picker create of one Codex
// app-server conversation, the `openIntentAgent` native catalog branch: the
// thread is resumed on the route resolved before the transaction with the
// policy of the inherited profile, the launch attaches to it, and the
// activation binds the new Agent and Pane to it. It never retries an
// indeterminate RPC and never falls back onto the rollout CLI lane.
type NativeResumeEngine struct {
	Spelling     string
	Route        NativeCodexRoute
	Conversation string
	Resume       func(context.Context, NativeCodexRoute, metadata.AgentWorkspace, string, codexappserver.ThreadSettings) (codexappserver.ThreadBinding, error)
	Plan         func(NativeCodexRoute, metadata.AgentWorkspace, string, string, string) (string, []string, error)
	BindPane     func(context.Context, string, metadata.AgentWorkspace, NativeAgentBinding) error
	SafeFallback func(error) bool
}

// Admit refuses a create that would resume the conversation in more than one
// Agent; the route was resolved before the transaction.
func (n *NativeResumeEngine) Admit(_ context.Context, count int) error {
	if count > 1 {
		return NativeFanOutRefusal(n.Spelling, count)
	}
	return nil
}

func (n *NativeResumeEngine) Create(ctx context.Context, working *metadata.Registry, m metadata.Mutator, work AgentWork, w metadata.AgentWorkspace, p AgentPreparation) (NativeAgentBinding, error) {
	var out NativeAgentBinding
	resumeCtx, cancel := context.WithTimeout(ctx, nativeCodexTimeout)
	binding, err := n.Resume(resumeCtx, n.Route, w, n.Conversation, codexappserver.ThreadSettings{Policy: p.Policy})
	cancel()
	switch {
	case err == nil:
	case n.SafeFallback(err):
		// The conversation is a thread the app-server owns; rebinding it onto
		// the rollout CLI lane would carry no native turn control.
		return out, NativeResumePreparationRefusal(n.Spelling, err)
	default:
		return out, NativeLaunchError(n.Spelling, err)
	}
	if strings.TrimSpace(binding.ThreadID) != strings.TrimSpace(n.Conversation) {
		return out, NativeLaunchError(n.Spelling, fmt.Errorf("%w: native resume returned a different thread", codexappserver.ErrProtocol))
	}
	title, argv, err := n.Plan(n.Route, w, binding.ThreadID, "", "")
	if err != nil {
		return out, NativeLaunchError(n.Spelling, err)
	}
	if _, err := m.BindCodexActivation(working, metadata.CodexActivationObservation{AgentUID: work.Agent.Metadata.UID, PaneUID: work.Pane.Metadata.UID, Generation: work.Activation.Generation, ThreadID: binding.ThreadID, TurnID: binding.TurnID, Endpoint: n.Route.Endpoint}); err != nil {
		return out, MapMetadataError(err)
	}
	return NativeAgentBinding{Title: title, Argv: argv, ThreadID: binding.ThreadID, TurnID: binding.TurnID}, nil
}

func (n *NativeResumeEngine) Bind(ctx context.Context, _ AgentWork, w metadata.AgentWorkspace, pane string, b NativeAgentBinding) error {
	if err := n.BindPane(ctx, pane, w, b); err != nil {
		return fmt.Errorf("%s: bind native Codex Pane %s presentation metadata: %w", n.Spelling, pane, err)
	}
	return nil
}
