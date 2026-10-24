package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

const claudeSubagentApprovalAgentID = "a3b0-workflow-child"

// claudeSubagentApprovalMarkerReads counts each fixture's hook marker reads.
// The tests using it do not run in parallel.
var claudeSubagentApprovalMarkerReads = map[*claudeQuietHookFixture]int{}

// claudeSubagentApprovalFixture builds the Pane a background Workflow runs in.
// "owned" keeps the exact Agent->Pane binding; "binding_miss" is the same
// managed Agent whose hook cannot read the Pane's uid option, so every hook
// projects onto the Pane alone and the Registry is never written; "unbound" is
// a Pane with no Agent at all. The hook marker read answers from the Pane
// options the fixture recorded so far, as tmux would.
func claudeSubagentApprovalFixture(t *testing.T, mode string) *claudeQuietHookFixture {
	t.Helper()
	f := newClaudeQuietHookFixture(t, mode != "unbound")
	read := f.cmd.readCommand
	lookup := claudeIngestReadCommand(claudeQuietHookPane)
	f.cmd.readCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && len(args) == 5 && args[0] == "display-message" && args[3] == claudeQuietHookPane && args[4] == aiHookPaneMarkerFormat {
			claudeSubagentApprovalMarkerReads[f]++
			state := claudeQuietHookPaneState(t, cmdRecorder(f.cmd).commands)
			fields := []string{claudeQuietHookPane}
			for _, option := range aiHookPaneMarkerOptions {
				fields = append(fields, state[option])
			}
			return []byte(strings.Join(fields, aiHookPaneMarkerSeparator) + "\n"), nil
		}
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

func claudeSubagentApprovalRequest(t *testing.T, f *claudeQuietHookFixture, agentID string) aiIngestLogEntry {
	t.Helper()
	extra := map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "go test ./..."}}
	if agentID != "" {
		extra["agent_id"] = agentID
	}
	return claudeBackgroundIngest(t, f, "PermissionRequest", extra)
}

func claudeSubagentApprovalToolRan(t *testing.T, f *claudeQuietHookFixture, event, agentID string) aiIngestLogEntry {
	t.Helper()
	extra := map[string]any{"tool_name": "Bash"}
	if agentID != "" {
		extra["agent_id"] = agentID
	}
	return claudeBackgroundIngest(t, f, event, extra)
}

// Several subagents can wait at once: the badge returns to busy only when the
// last pending request is answered, and each answer is a distinct ingest
// record.
func TestClaudeSubagentPermissionAnswersClearTheBadgeOnlyWhenNoneWait(t *testing.T) {
	for _, mode := range []string{"owned", "binding_miss", "unbound"} {
		for _, event := range []string{"PostToolUse", "PostToolUseFailure"} {
			t.Run(mode+"/"+event, func(t *testing.T) {
				f := claudeSubagentApprovalFixture(t, mode)
				claudeSubagentApprovalRequest(t, f, "child-a")
				claudeSubagentApprovalRequest(t, f, "child-b")
				requested := len(f.cmd.notifyStore.(*stubNotifyStore).pushed)
				reads := claudeSubagentApprovalMarkerReads[f]
				entry := claudeSubagentApprovalToolRan(t, f, event, "child-a")
				if got := claudeSubagentApprovalBadge(t, f); got != aiBadgeKindApprovalRequired {
					t.Fatalf("badge with child-b still waiting = %q", got)
				}
				if entry.Result != "quiet" || entry.Reason != aiIngestReasonSubagentApprovalPending || entry.AgentID != "child-a" {
					t.Fatalf("first answer record = %+v", entry)
				}
				if mode == "owned" {
					agent, _ := f.registry.Agent(f.agentUID)
					if agent.Status.Interaction.Kind != coremetadata.InteractionApprovalRequired {
						t.Fatalf("interaction with child-b still waiting = %+v", agent.Status.Interaction)
					}
				}
				entry = claudeSubagentApprovalToolRan(t, f, event, "child-b")
				if got := claudeSubagentApprovalMarkerReads[f] - reads; got != 2 {
					t.Fatalf("marker reads for two answers = %d, want one per hook", got)
				}
				if got := claudeSubagentApprovalBadge(t, f); got != aiBadgeKindInProgress {
					t.Fatalf("badge after the last answer = %q", got)
				}
				if entry.Result != "state" || entry.Reason != aiIngestReasonSubagentApprovalAnswered || entry.AgentID != "child-b" {
					t.Fatalf("last answer record = %+v", entry)
				}
				if state := claudeQuietHookPaneState(t, cmdRecorder(f.cmd).commands); state[aiPaneApprovalAgentIDsOption] != "" {
					t.Fatalf("pending set after the last answer = %q", state[aiPaneApprovalAgentIDsOption])
				}
				if got := len(f.cmd.notifyStore.(*stubNotifyStore).pushed); got != requested {
					t.Fatalf("answers pushed %d notifications, want none", got-requested)
				}
			})
		}
	}
}

// Requests that are not answered, tools that are not the waiting subagent's,
// main-thread requests and overflowing sets keep the earlier behavior: the
// approval badge stays.
func TestClaudeSubagentPermissionBadgeStaysWithoutItsAnswer(t *testing.T) {
	cases := []struct {
		name  string
		steps func(t *testing.T, f *claudeQuietHookFixture)
	}{
		{"unanswered", func(t *testing.T, f *claudeQuietHookFixture) {
			claudeSubagentApprovalRequest(t, f, "child-a")
			claudeBackgroundIngest(t, f, "PreToolUse", map[string]any{"agent_id": "child-a"})
			claudeBackgroundIngest(t, f, "Notification", map[string]any{"notification_type": "permission_prompt"})
		}},
		{"other_subagent_tool", func(t *testing.T, f *claudeQuietHookFixture) {
			claudeSubagentApprovalRequest(t, f, "child-a")
			claudeSubagentApprovalToolRan(t, f, "PostToolUse", "child-z")
		}},
		{"main_thread_request_empties_the_set", func(t *testing.T, f *claudeQuietHookFixture) {
			claudeSubagentApprovalRequest(t, f, "child-a")
			claudeSubagentApprovalRequest(t, f, "")
			if state := claudeQuietHookPaneState(t, cmdRecorder(f.cmd).commands); state[aiPaneApprovalAgentIDsOption] != "" {
				t.Fatalf("pending set after a main-thread request = %q", state[aiPaneApprovalAgentIDsOption])
			}
			claudeSubagentApprovalToolRan(t, f, "PostToolUse", "child-a")
		}},
		{"subagent_joins_a_main_thread_wait", func(t *testing.T, f *claudeQuietHookFixture) {
			claudeSubagentApprovalRequest(t, f, "")
			claudeSubagentApprovalRequest(t, f, "child-b")
			claudeSubagentApprovalToolRan(t, f, "PostToolUse", "child-b")
		}},
		{"overflow", func(t *testing.T, f *claudeQuietHookFixture) {
			for i := range claudeApprovalAgentIDsLimit + 1 {
				claudeSubagentApprovalRequest(t, f, fmt.Sprintf("child-%d", i))
			}
			for i := range claudeApprovalAgentIDsLimit + 1 {
				claudeSubagentApprovalToolRan(t, f, "PostToolUse", fmt.Sprintf("child-%d", i))
			}
		}},
		{"untrackable_id", func(t *testing.T, f *claudeQuietHookFixture) {
			claudeSubagentApprovalRequest(t, f, "child,a")
			claudeSubagentApprovalToolRan(t, f, "PostToolUse", "child,a")
		}},
	}
	for _, mode := range []string{"binding_miss", "unbound"} {
		for _, tc := range cases {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				f := claudeSubagentApprovalFixture(t, mode)
				tc.steps(t, f)
				if got := claudeSubagentApprovalBadge(t, f); got != aiBadgeKindApprovalRequired {
					t.Fatalf("badge = %q, want %q", got, aiBadgeKindApprovalRequired)
				}
			})
		}
	}
}

// The main-thread approval path is unchanged: an owned main-thread request
// records approval_required, and the main-thread tool that follows its answer
// closes it through the existing operator-answer handler.
func TestClaudeMainThreadPermissionAnswerKeepsTheOperatorAnswerPath(t *testing.T) {
	f := claudeSubagentApprovalFixture(t, "owned")
	claudeSubagentApprovalRequest(t, f, "")
	agent, _ := f.registry.Agent(f.agentUID)
	if agent.Status.Interaction.Kind != coremetadata.InteractionApprovalRequired {
		t.Fatalf("interaction after the request = %+v", agent.Status.Interaction)
	}
	entry := claudeSubagentApprovalToolRan(t, f, "PostToolUse", "")
	agent, _ = f.registry.Agent(f.agentUID)
	if agent.Status.Interaction.Kind != coremetadata.InteractionInProgress || entry.Result != "state" || entry.Reason != "" {
		t.Fatalf("interaction = %+v record = %+v", agent.Status.Interaction, entry)
	}
	if got := claudeSubagentApprovalBadge(t, f); got != aiBadgeKindInProgress {
		t.Fatalf("badge = %q", got)
	}
}
