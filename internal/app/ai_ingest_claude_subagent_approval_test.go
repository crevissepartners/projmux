package app

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

const claudeSubagentApprovalAgentID = "a3b0-workflow-child"

// claudeSubagentApprovalPane is the emulated tmux Pane of one fixture and the
// number of hook marker reads it answered. The tests using it do not run in
// parallel.
type claudeSubagentApprovalPane struct {
	pane        *hookMarkerPane
	markerReads int
}

var claudeSubagentApprovalPanes = map[*claudeQuietHookFixture]*claudeSubagentApprovalPane{}

// claudeSubagentApprovalFixture builds the Pane a background Workflow runs in.
// "owned" keeps the exact Agent->Pane binding; "binding_miss" is the same
// managed Agent whose hook cannot read the Pane's uid option, so every hook
// projects onto the Pane alone and the Registry is never written; "unbound" is
// a Pane with no Agent at all. installHookMarkerPane answers the hook marker
// read from the options earlier hooks wrote, as tmux would.
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
	state := &claudeSubagentApprovalPane{pane: installHookMarkerPane(f.cmd, claudeQuietHookPane)}
	marked := f.cmd.readCommand
	f.cmd.readCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		out, err := marked(ctx, name, args...)
		if marker := stripRecordedTmuxRoute(args); err == nil && name == "tmux" && len(marker) == 5 &&
			marker[0] == "display-message" && marker[3] == claudeQuietHookPane && marker[4] == aiHookPaneMarkerFormat {
			state.markerReads++
		}
		return out, err
	}
	claudeSubagentApprovalPanes[f] = state
	if mode != "unbound" {
		f.ingest(t, "PreToolUse")
		setClaudeQuietInteraction(t, f, coremetadata.InteractionInProgress)
	}
	return f
}

// claudeSubagentApprovalIngest is claudeBackgroundIngest that fails unless the
// hook's one marker read was answered: a fixture that misses that read would
// judge every Pane unknown and prove nothing.
func claudeSubagentApprovalIngest(t *testing.T, f *claudeQuietHookFixture, event string, extra map[string]any) aiIngestLogEntry {
	t.Helper()
	state := claudeSubagentApprovalPanes[f]
	before := state.markerReads
	entry := claudeBackgroundIngest(t, f, event, extra)
	if got := state.markerReads - before; got != 1 {
		t.Fatalf("%s answered %d hook marker reads, want exactly 1", event, got)
	}
	return entry
}

func claudeSubagentApprovalBadge(t *testing.T, f *claudeQuietHookFixture) string {
	t.Helper()
	return claudeSubagentApprovalPanes[f].pane.options[aiPaneBadgeKindOption]
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
			claudeSubagentApprovalIngest(t, f, "Stop", map[string]any{"background_tasks": []any{map[string]any{"type": "workflow", "status": "running"}}})
			claudeSubagentApprovalIngest(t, f, "PreToolUse", child)
			claudeSubagentApprovalIngest(t, f, "PermissionRequest", map[string]any{"agent_id": claudeSubagentApprovalAgentID, "agent_type": "workflow-subagent",
				"tool_name": "Bash", "tool_input": map[string]any{"command": "go test ./..."}})
			if got := claudeSubagentApprovalBadge(t, f); got != aiBadgeKindApprovalRequired {
				t.Fatalf("badge after the request = %q, want %q", got, aiBadgeKindApprovalRequired)
			}
			claudeSubagentApprovalIngest(t, f, "Notification", map[string]any{"notification_type": "permission_prompt", "message": "Claude needs your permission to use Bash"})
			entry := claudeSubagentApprovalIngest(t, f, "PostToolUse", map[string]any{"agent_id": claudeSubagentApprovalAgentID, "agent_type": "workflow-subagent", "tool_name": "Bash"})
			if entry.Result != "state" || entry.Reason != aiIngestReasonSubagentApprovalAnswered {
				t.Fatalf("answer record = %+v, want the subagent approval answer", entry)
			}

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
	claudeSubagentApprovalIngest(t, f, "PermissionRequest", map[string]any{"agent_id": claudeSubagentApprovalAgentID, "tool_name": "Bash"})
	now = now.Add(coremetadata.AgentInteractionFreshFor + time.Minute)
	entry := claudeSubagentApprovalIngest(t, f, "PostToolUse", map[string]any{"agent_id": claudeSubagentApprovalAgentID, "tool_name": "Bash"})
	if got := claudeSubagentApprovalBadge(t, f); got != aiBadgeKindInProgress {
		t.Fatalf("badge = %q, want %q", got, aiBadgeKindInProgress)
	}
	if entry.Result != "state" || entry.Reason != aiIngestReasonSubagentApprovalAnswered {
		t.Fatalf("answer record = %+v, want the subagent approval answer", entry)
	}
	agent, _ := f.registry.Agent(f.agentUID)
	if agent.Status.Interaction.Kind != coremetadata.InteractionInProgress {
		t.Fatalf("interaction = %+v, want in_progress", agent.Status.Interaction)
	}
}

func claudeSubagentApprovalRequest(t *testing.T, f *claudeQuietHookFixture, agentID string) aiIngestLogEntry {
	t.Helper()
	extra := map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "go test ./..."}}
	if agentID != "" {
		extra["agent_id"] = agentID
	}
	return claudeSubagentApprovalIngest(t, f, "PermissionRequest", extra)
}

func claudeSubagentApprovalToolRan(t *testing.T, f *claudeQuietHookFixture, event, agentID string) aiIngestLogEntry {
	t.Helper()
	extra := map[string]any{"tool_name": "Bash"}
	if agentID != "" {
		extra["agent_id"] = agentID
	}
	return claudeSubagentApprovalIngest(t, f, event, extra)
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
				if got := claudeSubagentApprovalBadge(t, f); got != aiBadgeKindInProgress {
					t.Fatalf("badge after the last answer = %q", got)
				}
				if entry.Result != "state" || entry.Reason != aiIngestReasonSubagentApprovalAnswered || entry.AgentID != "child-b" {
					t.Fatalf("last answer record = %+v", entry)
				}
				if pending, ok := claudeSubagentApprovalPanes[f].pane.options[aiPaneApprovalAgentIDsOption]; ok {
					t.Fatalf("pending set after the last answer = %q", pending)
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
			claudeSubagentApprovalIngest(t, f, "PreToolUse", map[string]any{"agent_id": "child-a"})
			claudeSubagentApprovalIngest(t, f, "Notification", map[string]any{"notification_type": "permission_prompt"})
		}},
		{"other_subagent_tool", func(t *testing.T, f *claudeQuietHookFixture) {
			claudeSubagentApprovalRequest(t, f, "child-a")
			claudeSubagentApprovalToolRan(t, f, "PostToolUse", "child-z")
		}},
		{"main_thread_request_empties_the_set", func(t *testing.T, f *claudeQuietHookFixture) {
			claudeSubagentApprovalRequest(t, f, "child-a")
			claudeSubagentApprovalRequest(t, f, "")
			if pending, ok := claudeSubagentApprovalPanes[f].pane.options[aiPaneApprovalAgentIDsOption]; ok {
				t.Fatalf("pending set after a main-thread request = %q", pending)
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
