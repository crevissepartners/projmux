package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	localstate "github.com/crevissepartners/projmux/internal/state"
	"golang.org/x/sys/unix"
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
// hook's marker reads were answered: a fixture that misses them would judge
// every Pane unknown and prove nothing. Every hook reads the markers once; an
// approval-related hook reads them once more under the approval lock: a
// PermissionRequest, or a subagent's tool on a Pane awaiting approval. An
// answer that returns the badge to busy reads them a third time for the fence.
func claudeSubagentApprovalIngest(t *testing.T, f *claudeQuietHookFixture, event string, extra map[string]any) aiIngestLogEntry {
	t.Helper()
	state := claudeSubagentApprovalPanes[f]
	agentID, _ := extra["agent_id"].(string)
	answer := agentID != "" && (event == "PostToolUse" || event == "PostToolUseFailure") &&
		state.pane.options[aiPaneBadgeKindOption] == aiBadgeKindApprovalRequired
	want := 1
	if event == "PermissionRequest" || answer {
		want = 2
	}
	before := state.markerReads
	entry := claudeBackgroundIngest(t, f, event, extra)
	if answer && state.pane.options[aiPaneBadgeKindOption] == aiBadgeKindInProgress {
		want = 3
	}
	if got := state.markerReads - before; got != want {
		t.Fatalf("%s answered %d hook marker reads, want %d", event, got, want)
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

// PermissionDenied and ElicitationResult keep their existing handling: under a
// waiting subagent's agent_id they neither leave the set nor clear the badge.
func TestClaudeSubagentPermissionDeniedAndElicitationResultKeepTheBadge(t *testing.T) {
	for _, mode := range []string{"binding_miss", "unbound"} {
		for _, event := range []string{"PermissionDenied", "ElicitationResult"} {
			t.Run(mode+"/"+event, func(t *testing.T) {
				f := claudeSubagentApprovalFixture(t, mode)
				claudeSubagentApprovalRequest(t, f, "child-a")
				entry := claudeSubagentApprovalToolRan(t, f, event, "child-a")
				if got := claudeSubagentApprovalBadge(t, f); got != aiBadgeKindApprovalRequired {
					t.Fatalf("badge = %q, want %q", got, aiBadgeKindApprovalRequired)
				}
				if pending := claudeSubagentApprovalPanes[f].pane.options[aiPaneApprovalAgentIDsOption]; pending != "child-a" {
					t.Fatalf("pending set = %q, want child-a", pending)
				}
				if entry.Reason == aiIngestReasonSubagentApprovalAnswered || entry.Reason == aiIngestReasonSubagentApprovalPending {
					t.Fatalf("record = %+v, want the existing handling", entry)
				}
			})
		}
	}
}

// A main-thread request empties the set even when its marker reads fail, so a
// waiting subagent's later answer cannot clear the main-thread wait.
func TestClaudeMainThreadPermissionEmptiesTheSetWhenTheMarkerReadFails(t *testing.T) {
	for _, mode := range []string{"binding_miss", "unbound"} {
		t.Run(mode, func(t *testing.T) {
			f := claudeSubagentApprovalFixture(t, mode)
			state := claudeSubagentApprovalPanes[f]
			claudeSubagentApprovalRequest(t, f, "child-a")
			state.pane.readFails = true
			before := state.markerReads
			claudeBackgroundIngest(t, f, "PermissionRequest", map[string]any{"tool_name": "Bash"})
			if got := state.markerReads - before; got != 0 {
				t.Fatalf("marker reads answered while failing = %d", got)
			}
			state.pane.readFails = false
			if pending, ok := state.pane.options[aiPaneApprovalAgentIDsOption]; ok {
				t.Fatalf("pending set after a main-thread request with an unread Pane = %q", pending)
			}
			claudeSubagentApprovalToolRan(t, f, "PostToolUse", "child-a")
			if got := claudeSubagentApprovalBadge(t, f); got != aiBadgeKindApprovalRequired {
				t.Fatalf("badge = %q, want the main-thread wait kept", got)
			}
		})
	}
}

// claudeSharedApprovalPane is one tmux Pane two hook processes write: each
// aiCommand records its own commands, while the Pane options they read and
// write are shared and guarded like a tmux server.
type claudeSharedApprovalPane struct {
	mu      sync.Mutex
	options map[string]string
}

func (p *claudeSharedApprovalPane) install(cmd *aiCommand) {
	run, read := cmd.runCommand, cmd.readCommand
	lookup := claudeIngestReadCommand(claudeQuietHookPane)
	cmd.runCommand = func(ctx context.Context, name string, args ...string) error {
		if err := run(ctx, name, args...); err != nil {
			return err
		}
		a := stripRecordedTmuxRoute(args)
		p.mu.Lock()
		defer p.mu.Unlock()
		switch {
		case name == "tmux" && len(a) == 6 && a[0] == "set-option" && a[1] == "-p" && a[2] == "-t" && a[3] == claudeQuietHookPane:
			p.options[a[4]] = a[5]
		case name == "tmux" && len(a) == 6 && a[0] == "set-option" && a[1] == "-p" && a[2] == "-u" && a[3] == "-t" && a[4] == claudeQuietHookPane:
			delete(p.options, a[5])
		}
		return nil
	}
	cmd.readCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if a := stripRecordedTmuxRoute(args); name == "tmux" && len(a) == 5 && a[0] == "display-message" && a[3] == claudeQuietHookPane && a[4] == aiHookPaneMarkerFormat {
			p.mu.Lock()
			defer p.mu.Unlock()
			fields := []string{claudeQuietHookPane}
			for _, option := range aiHookPaneMarkerOptions {
				fields = append(fields, p.options[option])
			}
			return []byte(strings.Join(fields, aiHookPaneMarkerSeparator) + "\n"), nil
		}
		if data, err := read(ctx, name, args...); err == nil {
			return data, nil
		}
		return lookup(ctx, name, args...)
	}
}

func (p *claudeSharedApprovalPane) option(name string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	value, ok := p.options[name]
	return value, ok
}

// claudeApprovalHookProcesses returns two unbound hook processes of one Pane
// that share its tmux options and the approval lock directory.
func claudeApprovalHookProcesses(t *testing.T) (*claudeQuietHookFixture, *claudeQuietHookFixture, *claudeSharedApprovalPane) {
	t.Helper()
	pane := &claudeSharedApprovalPane{options: map[string]string{}}
	first := newClaudeQuietHookFixture(t, false)
	second := newClaudeQuietHookFixture(t, false)
	second.cmd.homeDir, second.cmd.lookupEnv = first.cmd.homeDir, first.cmd.lookupEnv
	pane.install(first.cmd)
	pane.install(second.cmd)
	return first, second, pane
}

func claudeApprovalHookPayload(t *testing.T, event, agentID string) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{"hook_event_name": event, "session_id": claudeQuietHookSession,
		"cwd": claudeQuietHookCWD, "agent_id": agentID, "tool_name": "Bash"})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// overlapClaudeApprovalHooks runs first's hook until it holds the approval
// lock with the markers read, starts second's hook against the same Pane, and
// releases first only after second found the lock taken. Without the lock both
// would judge the same markers.
func overlapClaudeApprovalHooks(t *testing.T, first, second *claudeQuietHookFixture, firstData, secondData []byte) {
	t.Helper()
	holding, release, contended := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	first.cmd.claudeApprovalLockHooks = &claudeApprovalLockHooks{afterRead: func() {
		once.Do(func() {
			close(holding)
			<-release
		})
	}}
	second.cmd.claudeApprovalLockHooks = &claudeApprovalLockHooks{contended: func() { close(contended) }}
	defer func() { first.cmd.claudeApprovalLockHooks, second.cmd.claudeApprovalLockHooks = nil, nil }()
	errs := make(chan error, 2)
	go func() { errs <- first.cmd.ingestClaudeHook(firstData, first.explicitPane) }()
	select {
	case <-holding:
	case <-time.After(5 * time.Second):
		t.Fatal("first hook never held the approval lock")
	}
	go func() { errs <- second.cmd.ingestClaudeHook(secondData, second.explicitPane) }()
	select {
	case <-contended:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("second hook never waited for the approval lock")
	}
	close(release)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

// Two subagents ask at once on a busy Pane: both requests stay pending, so the
// first answer keeps the badge.
func TestClaudeSubagentPermissionRequestsAtOnceBothStayPending(t *testing.T) {
	first, second, pane := claudeApprovalHookProcesses(t)
	overlapClaudeApprovalHooks(t, first, second,
		claudeApprovalHookPayload(t, "PermissionRequest", "child-a"), claudeApprovalHookPayload(t, "PermissionRequest", "child-b"))
	if pending, _ := pane.option(aiPaneApprovalAgentIDsOption); pending != "child-a,child-b" {
		t.Fatalf("pending set = %q, want both requests", pending)
	}
	if err := second.cmd.ingestClaudeHook(claudeApprovalHookPayload(t, "PostToolUse", "child-b"), second.explicitPane); err != nil {
		t.Fatal(err)
	}
	if badge, _ := pane.option(aiPaneBadgeKindOption); badge != aiBadgeKindApprovalRequired {
		t.Fatalf("badge with child-a still waiting = %q", badge)
	}
}

// Two answers at once on {child-a, child-b}: neither restores the other's id,
// so the set ends empty and the badge returns to busy.
func TestClaudeSubagentPermissionAnswersAtOnceEmptyTheSet(t *testing.T) {
	first, second, pane := claudeApprovalHookProcesses(t)
	for _, id := range []string{"child-a", "child-b"} {
		if err := first.cmd.ingestClaudeHook(claudeApprovalHookPayload(t, "PermissionRequest", id), first.explicitPane); err != nil {
			t.Fatal(err)
		}
	}
	overlapClaudeApprovalHooks(t, first, second,
		claudeApprovalHookPayload(t, "PostToolUse", "child-a"), claudeApprovalHookPayload(t, "PostToolUse", "child-b"))
	if pending, ok := pane.option(aiPaneApprovalAgentIDsOption); ok {
		t.Fatalf("pending set after both answers = %q", pending)
	}
	if badge, _ := pane.option(aiPaneBadgeKindOption); badge != aiBadgeKindInProgress {
		t.Fatalf("badge after both answers = %q", badge)
	}
}

// A lock that cannot be taken never clears the badge, and the record says why.
func TestClaudeSubagentPermissionAnswerWithoutTheLockKeepsTheBadge(t *testing.T) {
	f := claudeSubagentApprovalFixture(t, "unbound")
	claudeSubagentApprovalRequest(t, f, "child-a")
	path, err := f.cmd.claudeApprovalLockPath(claudeQuietHookPane)
	if err != nil {
		t.Fatal(err)
	}
	held, err := localstate.AcquireFileLock(path, time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = unix.Flock(int(held.Fd()), unix.LOCK_UN)
		_ = held.Close()
	}()
	entry := claudeBackgroundIngest(t, f, "PostToolUse", map[string]any{"agent_id": "child-a", "tool_name": "Bash"})
	if entry.Result != "quiet" || entry.Reason != aiIngestReasonSubagentApprovalLocked {
		t.Fatalf("record = %+v, want the lock failure", entry)
	}
	if got := claudeSubagentApprovalBadge(t, f); got != aiBadgeKindApprovalRequired {
		t.Fatalf("badge = %q, want %q", got, aiBadgeKindApprovalRequired)
	}
}

// holdClaudeApprovalLock runs holder's hook until it holds the approval lock
// with the markers read and keeps it there. The returned func releases the
// holder and waits for its hook to finish.
func holdClaudeApprovalLock(t *testing.T, holder *claudeQuietHookFixture, data []byte) func() {
	t.Helper()
	holding, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	holder.cmd.claudeApprovalLockHooks = &claudeApprovalLockHooks{afterRead: func() {
		once.Do(func() {
			close(holding)
			<-release
		})
	}}
	done := make(chan error, 1)
	go func() { done <- holder.cmd.ingestClaudeHook(data, holder.explicitPane) }()
	select {
	case <-holding:
	case <-time.After(5 * time.Second):
		t.Fatal("holder never held the approval lock")
	}
	var released sync.Once
	finish := func() {
		released.Do(func() {
			close(release)
			if err := <-done; err != nil {
				t.Error(err)
			}
			holder.cmd.claudeApprovalLockHooks = nil
		})
	}
	t.Cleanup(finish)
	return finish
}

// claudeApprovalRequestTimesOut runs a request while another hook holds the
// Pane's approval lock: it gives up after the wait limit, sets the fence, and
// projects its approval outside the lock.
func claudeApprovalRequestTimesOut(t *testing.T, f *claudeQuietHookFixture, pane *claudeSharedApprovalPane, agentID string) {
	t.Helper()
	if err := f.cmd.ingestClaudeHook(claudeApprovalHookPayload(t, "PermissionRequest", agentID), f.explicitPane); err != nil {
		t.Fatal(err)
	}
	if fence, _ := pane.option(aiPaneApprovalFenceOption); fence != claudeApprovalFenceSet {
		t.Fatalf("fence after the timed-out request = %q", fence)
	}
	if badge, _ := pane.option(aiPaneBadgeKindOption); badge != aiBadgeKindApprovalRequired {
		t.Fatalf("badge after the timed-out request = %q", badge)
	}
}

// An answer holds the lock with {child-a} read while child-b's request times
// out and projects outside it. The holder then empties the set and writes
// busy from its older read, but the fence it reads afterwards restores
// child-b's approval, and child-b's own answer later keeps it too.
func TestClaudeSubagentPermissionTimedOutRequestSurvivesAnAnswerHolder(t *testing.T) {
	first, second, pane := claudeApprovalHookProcesses(t)
	if err := first.cmd.ingestClaudeHook(claudeApprovalHookPayload(t, "PermissionRequest", "child-a"), first.explicitPane); err != nil {
		t.Fatal(err)
	}
	finish := holdClaudeApprovalLock(t, first, claudeApprovalHookPayload(t, "PostToolUse", "child-a"))
	claudeApprovalRequestTimesOut(t, second, pane, "child-b")
	finish()
	if badge, _ := pane.option(aiPaneBadgeKindOption); badge != aiBadgeKindApprovalRequired {
		t.Fatalf("badge after the holder answered child-a = %q, want child-b's approval kept", badge)
	}
	if err := second.cmd.ingestClaudeHook(claudeApprovalHookPayload(t, "PostToolUse", "child-b"), second.explicitPane); err != nil {
		t.Fatal(err)
	}
	if badge, _ := pane.option(aiPaneBadgeKindOption); badge != aiBadgeKindApprovalRequired {
		t.Fatalf("badge on a fenced Pane after child-b's tool = %q", badge)
	}
	if err := second.cmd.ingestClaudeHook(claudeApprovalHookPayload(t, "PermissionRequest", ""), second.explicitPane); err != nil {
		t.Fatal(err)
	}
	if fence, ok := pane.option(aiPaneApprovalFenceOption); ok {
		t.Fatalf("fence after a main-thread request = %q", fence)
	}
}

// A request holds the lock with a busy Pane read while child-b's request
// times out and projects outside it. The holder then starts {child-a} from its
// older read, but child-a's answer cannot clear the badge child-b still needs.
func TestClaudeSubagentPermissionTimedOutRequestSurvivesARequestHolder(t *testing.T) {
	first, second, pane := claudeApprovalHookProcesses(t)
	finish := holdClaudeApprovalLock(t, first, claudeApprovalHookPayload(t, "PermissionRequest", "child-a"))
	claudeApprovalRequestTimesOut(t, second, pane, "child-b")
	finish()
	if pending, _ := pane.option(aiPaneApprovalAgentIDsOption); pending != "child-a" {
		t.Fatalf("pending set the holder wrote = %q, want its older read's set", pending)
	}
	if err := first.cmd.ingestClaudeHook(claudeApprovalHookPayload(t, "PostToolUse", "child-a"), first.explicitPane); err != nil {
		t.Fatal(err)
	}
	if badge, _ := pane.option(aiPaneBadgeKindOption); badge != aiBadgeKindApprovalRequired {
		t.Fatalf("badge after child-a's answer = %q, want child-b's approval kept", badge)
	}
	if fence, _ := pane.option(aiPaneApprovalFenceOption); fence != claudeApprovalFenceSet {
		t.Fatalf("fence after child-a's answer = %q", fence)
	}
}
