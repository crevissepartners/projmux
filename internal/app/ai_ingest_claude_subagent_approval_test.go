package app

import (
	"context"
	"os"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

const claudeSubagentApprovalAgentID = "a3b0-workflow-child"

// claudeSubagentApprovalFixture builds the Pane a background Workflow runs in.
// "owned" keeps the exact Agent->Pane binding; "binding_miss" is the same
// managed Agent whose hook cannot read the Pane's uid option, so every hook
// projects onto the Pane alone and the Registry is never written; "unbound" is
// a Pane with no Agent at all.
func claudeSubagentApprovalFixture(t *testing.T, mode string) *claudeQuietHookFixture {
	t.Helper()
	f := newClaudeQuietHookFixture(t, mode != "unbound")
	read := f.cmd.readCommand
	lookup := claudeIngestReadCommand(claudeQuietHookPane)
	f.cmd.readCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if mode == "binding_miss" && name == "tmux" && len(args) >= 5 && args[0] == "display-message" && args[4] == "#{"+tmuxopts.PaneUID+"}" {
			return nil, os.ErrNotExist
		}
		if data, err := read(ctx, name, args...); err == nil {
			return data, nil
		}
		return lookup(ctx, name, args...)
	}
	if mode != "unbound" {
		f.ingest(t, "PreToolUse")
		setClaudeQuietInteraction(t, f, coremetadata.InteractionInProgress)
	}
	return f
}

func claudeSubagentApprovalBadge(t *testing.T, f *claudeQuietHookFixture) string {
	t.Helper()
	return claudeQuietHookPaneState(t, cmdRecorder(f.cmd).commands)[aiPaneBadgeKindOption]
}

// The issue #1644 order: a main-thread Stop leaves a Workflow in flight, a
// Workflow subagent asks for a permission, the operator answers, and the
// approved tool runs under the same agent_id while the main thread stays
// silent.
func TestClaudeSubagentPermissionAnswerReturnsTheBadgeToBusy(t *testing.T) {
	for _, mode := range []string{"owned", "binding_miss", "unbound"} {
		t.Run(mode, func(t *testing.T) {
			f := claudeSubagentApprovalFixture(t, mode)
			child := map[string]any{"agent_id": claudeSubagentApprovalAgentID, "agent_type": "workflow-subagent"}
			claudeBackgroundIngest(t, f, "Stop", map[string]any{"background_tasks": []any{map[string]any{"type": "workflow", "status": "running"}}})
			claudeBackgroundIngest(t, f, "PreToolUse", child)
			claudeBackgroundIngest(t, f, "PermissionRequest", map[string]any{"agent_id": claudeSubagentApprovalAgentID, "agent_type": "workflow-subagent",
				"tool_name": "Bash", "tool_input": map[string]any{"command": "go test ./..."}})
			if got := claudeSubagentApprovalBadge(t, f); got != aiBadgeKindApprovalRequired {
				t.Fatalf("badge after the request = %q, want %q", got, aiBadgeKindApprovalRequired)
			}
			claudeBackgroundIngest(t, f, "Notification", map[string]any{"notification_type": "permission_prompt", "message": "Claude needs your permission to use Bash"})
			claudeBackgroundIngest(t, f, "PostToolUse", map[string]any{"agent_id": claudeSubagentApprovalAgentID, "agent_type": "workflow-subagent", "tool_name": "Bash"})

			if got := claudeSubagentApprovalBadge(t, f); got != aiBadgeKindInProgress {
				t.Fatalf("badge after the answered tool ran = %q, want %q", got, aiBadgeKindInProgress)
			}
			if mode == "owned" {
				agent, _ := f.registry.Agent(f.agentUID)
				if agent.Status.Interaction.Kind != coremetadata.InteractionInProgress {
					t.Fatalf("interaction = %+v, want in_progress", agent.Status.Interaction)
				}
			}
			if mode == "binding_miss" {
				agent, _ := f.registry.Agent(f.agentUID)
				if !agent.Status.Interaction.ObservedAt.Equal(claudeQuietHookClock) || agent.Status.Interaction.Kind != coremetadata.InteractionInProgress {
					t.Fatalf("a binding miss wrote the Registry: %+v", agent.Status.Interaction)
				}
			}
		})
	}
}

// An owned request answered after the freshness window: the recorded
// approval_required has aged into unknown when the approved tool runs.
func TestClaudeSubagentPermissionAnsweredAfterTheFreshnessWindowReturnsTheBadgeToBusy(t *testing.T) {
	f := claudeSubagentApprovalFixture(t, "owned")
	now := claudeQuietHookClock
	f.cmd.now = func() time.Time { return now }
	claudeBackgroundIngest(t, f, "PermissionRequest", map[string]any{"agent_id": claudeSubagentApprovalAgentID, "tool_name": "Bash"})
	now = now.Add(coremetadata.AgentInteractionFreshFor + time.Minute)
	claudeBackgroundIngest(t, f, "PostToolUse", map[string]any{"agent_id": claudeSubagentApprovalAgentID, "tool_name": "Bash"})
	if got := claudeSubagentApprovalBadge(t, f); got != aiBadgeKindInProgress {
		t.Fatalf("badge = %q, want %q", got, aiBadgeKindInProgress)
	}
}
