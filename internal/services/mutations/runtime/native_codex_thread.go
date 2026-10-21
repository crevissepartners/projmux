package runtime

import (
	"context"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

type NativeCodexCreateInput struct {
	Workspace metadata.AgentWorkspace
	// DeveloperInstructions retains the internal create vocabulary; the
	// managed composition is sent as baseInstructions, never a permanent developer message.
	DeveloperInstructions string
	ManagedInstructions   *codexappserver.ManagedInstructions
	Policy                codexappserver.ThreadPolicy
	Model                 string
	Effort                string
	Prompt                string
	RequestKey            string
}
type NativeCodexThreadClient interface {
	StartThreadWithSettings(context.Context, string, []string, string, codexappserver.ThreadSettings) (codexappserver.ThreadBinding, error)
	StartTurnWithOptions(context.Context, string, string, string, string, string) (string, error)
	Close() error
}

// CreateNativeCodexThread writes one thread and at most one initial turn.
// An unknown RPC outcome is returned unchanged, without retry or cleanup claims.
func CreateNativeCodexThread(ctx context.Context, input NativeCodexCreateInput, open func(context.Context, bool) (NativeCodexThreadClient, error), durable func(context.Context, metadata.AgentWorkspace, string) error) (codexappserver.ThreadBinding, error) {
	workspace := input.Workspace
	if input.ManagedInstructions == nil {
		input.ManagedInstructions = &codexappserver.ManagedInstructions{Base: input.DeveloperInstructions}
	}
	client, err := open(ctx, len(workspace.AdditionalWritableRoots) > 0)
	if err != nil {
		return codexappserver.ThreadBinding{}, err
	}
	closed := false
	defer func() {
		if !closed {
			_ = client.Close()
		}
	}()
	binding, err := client.StartThreadWithSettings(ctx, workspace.CWD, workspace.AdditionalWritableRoots, "", codexappserver.ThreadSettings{Model: input.Model, Effort: input.Effort, Policy: input.Policy, ManagedInstructions: input.ManagedInstructions})
	if err != nil {
		return binding, err
	}
	if input.Prompt == "" {
		// Closing a default-route client also reaps its owned stdio proxy and may
		// report that local child exit. That cleanup result says nothing about
		// whether the shared endpoint persisted the exact thread. The fresh
		// independent resume/read below is the only semantic readiness verdict.
		_ = client.Close()
		closed = true
		return binding, durable(ctx, workspace, binding.ThreadID)
	}
	binding.TurnID, err = client.StartTurnWithOptions(ctx, binding.ThreadID, input.Prompt, input.RequestKey, input.Model, input.Effort)
	return binding, err
}
