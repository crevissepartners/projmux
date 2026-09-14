package app

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// claudeQuietHookClock is the fixed ingest clock of these tests, so the resume
// timestamp, the session ref and every status write are deterministic.
var claudeQuietHookClock = time.Date(2026, 9, 14, 9, 30, 0, 0, time.UTC)

const (
	claudeQuietHookPane       = "%7"
	claudeQuietHookCWD        = "/src/app"
	claudeQuietHookSession    = "claude-quiet-session"
	claudeQuietHookTranscript = "/home/u/.claude/projects/app/claude-quiet-session.jsonl"
	claudeQuietHookGeneration = "gen-claude-quiet"
)

// claudeQuietHookFixture drives ingestClaudeHook against an in-memory Registry
// and the recorded tmux transport of testAICommand. An owned fixture holds a
// running Claude Agent bound to the Pane and is attributed through the explicit
// `--pane` route a projmux-installed hook uses; an unbound fixture has no Agent
// and is attributed through the inherited TMUX_PANE.
type claudeQuietHookFixture struct {
	cmd          *aiCommand
	registry     *coremetadata.Registry
	agentUID     string
	explicitPane string
	loads        int
}

func newClaudeQuietHookFixture(t *testing.T, owned bool) *claudeQuietHookFixture {
	t.Helper()

	home := t.TempDir()
	f := &claudeQuietHookFixture{
		registry: &coremetadata.Registry{APIVersion: coremetadata.APIVersion, SchemaVersion: coremetadata.SchemaVersion},
	}
	paneUID := ""
	if owned {
		mutator := coremetadata.Mutator{
			Now:       func() time.Time { return claudeQuietHookClock },
			NewUID:    sequentialTestUID(),
			DirExists: func(string) (bool, error) { return true, nil },
		}
		project, err := mutator.RegisterProject(f.registry, coremetadata.RegisterProjectOptions{
			Root: claudeQuietHookCWD, DefaultShell: "/bin/zsh", OperationID: "op-1",
		})
		if err != nil {
			t.Fatalf("register project: %v", err)
		}
		agent, err := mutator.CreateAgent(f.registry, project.Windows[0].Metadata.UID, coremetadata.CreateAgentOptions{
			Provider: aiModeClaude, OperationID: "op-2",
		})
		if err != nil {
			t.Fatalf("create agent: %v", err)
		}
		pane, err := mutator.AttachAgentPane(f.registry, agent.Metadata.UID, coremetadata.BootstrapPane{
			Command: aiModeClaude, CWD: claudeQuietHookCWD,
		}, "op-3")
		if err != nil {
			t.Fatalf("attach agent pane: %v", err)
		}
		if _, err := mutator.RecordPaneActivation(f.registry, pane.Metadata.UID, coremetadata.PaneActivationOptions{
			Generation: claudeQuietHookGeneration, AgentUID: agent.Metadata.UID, OperationID: "op-3",
		}); err != nil {
			t.Fatalf("record pane activation: %v", err)
		}
		if _, err := mutator.ObservePaneActivationRuntime(f.registry, pane.Metadata.UID, claudeQuietHookGeneration, claudeQuietHookPane); err != nil {
			t.Fatalf("observe pane activation runtime: %v", err)
		}
		// A launch awaiting its provider, so SessionStart's readiness commit
		// really runs between the hook marking and the quiet record.
		bound, _ := f.registry.Agent(agent.Metadata.UID)
		bound.Status.Activation = coremetadata.AgentActivation{State: coremetadata.ActivationPending}
		f.agentUID = agent.Metadata.UID
		f.explicitPane = claudeQuietHookPane
		paneUID = pane.Metadata.UID
	}

	store := &stubNotifyStore{}
	cmd := testAICommand(home)
	cmd.now = func() time.Time { return claudeQuietHookClock }
	cmd.notifyStore = store
	cmd.producer = &storeAttentionNotifyProducer{store: store, ttl: time.Minute}
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "PROJMUX_NOTIFY_HOOK":
			// Keeps a notify event's desktop dispatch on the recorded transport.
			return "/nonexistent/projmux-notify-hook"
		case "TMUX_PANE":
			if !owned {
				return claudeQuietHookPane
			}
		case internalActivationPaneUIDEnv:
			return paneUID
		case internalActivationGenerationEnv:
			if owned {
				return claudeQuietHookGeneration
			}
		}
		return ""
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if row, ok := testAIPaneRouteProbe(name, args); ok {
			return row, nil
		}
		if paneUID != "" && name == "tmux" && len(args) >= 5 && args[0] == "display-message" && args[4] == "#{"+tmuxopts.PaneUID+"}" {
			return []byte(paneUID + "\n"), nil
		}
		return nil, os.ErrNotExist
	}
	cmd.loadRegistry = func() (coremetadata.Registry, error) {
		f.loads++
		return f.registry.Clone(), nil
	}
	cmd.updateRegistry = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
		working := f.registry.Clone()
		if err := fn(&working); err != nil {
			return coremetadata.Registry{}, err
		}
		if err := working.Validate(); err != nil {
			return coremetadata.Registry{}, err
		}
		*f.registry = working
		return working.Clone(), nil
	}
	f.cmd = cmd
	return f
}

// ingest runs one Claude hook event through the canonical handler, including
// its deferred session-ref flush, and returns only what that one call did.
func (f *claudeQuietHookFixture) ingest(t *testing.T, event string) ([]recordedAICommand, int) {
	t.Helper()
	payload, err := json.Marshal(map[string]string{
		"hook_event_name": event,
		"session_id":      claudeQuietHookSession,
		"cwd":             claudeQuietHookCWD,
		"transcript_path": claudeQuietHookTranscript,
	})
	if err != nil {
		t.Fatal(err)
	}
	commandsBefore, loadsBefore := len(cmdRecorder(f.cmd).commands), f.loads
	if err := f.cmd.ingestClaudeHook(payload, f.explicitPane); err != nil {
		t.Fatalf("ingest claude %s: %v", event, err)
	}
	return cmdRecorder(f.cmd).commands[commandsBefore:], f.loads - loadsBefore
}

// claudeQuietHookSetOptionCounts counts pane set-option writes per option.
func claudeQuietHookSetOptionCounts(commands []recordedAICommand) map[string]int {
	counts := map[string]int{}
	for _, command := range commands {
		args := stripRecordedTmuxRoute(command.args)
		if command.name == "tmux" && len(args) >= 6 && args[0] == "set-option" && args[1] == "-p" && args[2] == "-t" {
			counts[args[4]]++
		}
	}
	return counts
}

// claudeQuietHookPaneState replays recorded pane option writes in order, so a
// repeated write of the same value collapses and only the final state remains.
func claudeQuietHookPaneState(t *testing.T, commands []recordedAICommand) map[string]string {
	t.Helper()
	state := map[string]string{}
	for _, command := range commands {
		if command.name != "tmux" {
			continue
		}
		args := stripRecordedTmuxRoute(command.args)
		switch {
		case len(args) == 6 && args[0] == "set-option" && args[1] == "-p" && args[2] == "-t":
			state[args[4]] = args[5]
		case len(args) == 6 && args[0] == "set-option" && args[1] == "-p" && args[2] == "-u" && args[3] == "-t":
			delete(state, args[5])
		case len(args) > 0 && args[0] == "set-option":
			t.Fatalf("unrecognized pane option write %q", args)
		}
	}
	return state
}

func claudeQuietHookLogRecords(t *testing.T, cmd *aiCommand) []aiIngestLogEntry {
	t.Helper()
	path, err := cmd.aiIngestLogPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ingest log: %v", err)
	}
	var records []aiIngestLogEntry
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		var record aiIngestLogEntry
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode ingest log line %q: %v", line, err)
		}
		record.At = ""
		records = append(records, record)
	}
	return records
}

// TestClaudeQuietHookMarksThePaneOnce owns the cost of a quiet Claude event.
// ingestClaudeHook marks the attributed Pane before it dispatches, and a quiet
// branch only appends its record, so one event is one hook marking: one write
// per marker option and one Registry binding read.
func TestClaudeQuietHookMarksThePaneOnce(t *testing.T) {
	t.Parallel()

	ownedOptions := []string{
		aiPaneHookActiveOption, aiPaneManagedOption, aiPaneAgentOption, aiPaneContextOption,
		aiPaneSessionIDOption, aiPaneResumeIDOption, aiPaneResumeSourceOption, aiPaneResumeUpdatedAtOption,
		aiPaneTranscriptPathOption,
	}
	unboundOptions := []string{
		aiPaneHookActiveOption, aiPaneContextOption, aiPaneSessionIDOption, aiPaneResumeIDOption,
		aiPaneResumeSourceOption, aiPaneResumeUpdatedAtOption, aiPaneTranscriptPathOption,
	}
	tests := []struct {
		name      string
		owned     bool
		event     string
		options   []string
		forbidden []string
		// wantTmux is every recorded tmux command of the one ingest call: the
		// marker set-options and nothing else, because a quiet event writes no
		// status. The route probe and the Pane uid read are reads, not commands.
		wantTmux int
		// wantLoads is every Registry read of the one ingest call.
		//   owned:   explicit --pane resolution (1) + the mark's binding read (1)
		//            + the deferred session-ref flush (1) = 3.
		//   unbound: the mark's binding read only (1); TMUX_PANE attribution
		//            reads no Registry, and an Agent-less Registry stages no ref.
		wantLoads int
	}{
		{name: "owned PreToolUse", owned: true, event: "PreToolUse", options: ownedOptions, wantTmux: 9, wantLoads: 3},
		{name: "owned PostToolUse", owned: true, event: "PostToolUse", options: ownedOptions, wantTmux: 9, wantLoads: 3},
		{name: "owned PostToolBatch", owned: true, event: "PostToolBatch", options: ownedOptions, wantTmux: 9, wantLoads: 3},
		{name: "owned unknown event", owned: true, event: "ExperimentalEvent", options: ownedOptions, wantTmux: 9, wantLoads: 3},
		{
			name: "unbound PreToolUse", event: "PreToolUse", options: unboundOptions,
			forbidden: []string{aiPaneManagedOption, aiPaneAgentOption}, wantTmux: 7, wantLoads: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newClaudeQuietHookFixture(t, tc.owned)
			commands, loads := f.ingest(t, tc.event)

			counts := claudeQuietHookSetOptionCounts(commands)
			for _, option := range tc.options {
				if counts[option] != 1 {
					t.Errorf("%s set-option count = %d, want 1", option, counts[option])
				}
			}
			for _, option := range tc.forbidden {
				if counts[option] != 0 {
					t.Errorf("%s set-option count = %d, want 0 on an unbound Pane", option, counts[option])
				}
			}
			tmux := 0
			for _, command := range commands {
				if command.name == "tmux" {
					tmux++
				}
			}
			if tmux != tc.wantTmux || len(commands) != tc.wantTmux {
				t.Errorf("tmux commands = %d (all commands %d), want %d: %q", tmux, len(commands), tc.wantTmux, commands)
			}
			if loads != tc.wantLoads {
				t.Errorf("Registry loads = %d, want %d", loads, tc.wantLoads)
			}
		})
	}
}

// claudeQuietHookOutcome is what a Claude hook event leaves behind: the final
// Pane option state, the Agent's durable conversation pointer and activation
// boundary, and the ingest records.
type claudeQuietHookOutcome struct {
	PaneOptions      map[string]string
	SessionRef       *coremetadata.AgentSessionRef
	ActivationState  string
	ActivationSource string
	Records          []aiIngestLogEntry
}

// TestClaudeHookOutcomeIsUnchangedByMarkingOnce characterizes every Claude
// dispatch family on an owned Pane with golden values. It was green before
// quietClaudeHook stopped re-marking the Pane and must stay green after: the
// second mark repeated identical writes, so collapsing it changes no state.
func TestClaudeHookOutcomeIsUnchangedByMarkingOnce(t *testing.T) {
	t.Parallel()

	ref := &coremetadata.AgentSessionRef{
		Provider:   aiModeClaude,
		ObservedAt: claudeQuietHookClock,
		Claude:     &coremetadata.ClaudeSessionRef{SessionID: claudeQuietHookSession, TranscriptPath: claudeQuietHookTranscript},
	}
	record := func(event, result string, reason aiIngestReason) []aiIngestLogEntry {
		return []aiIngestLogEntry{{
			Source: "claude-hook", Event: event, Result: result, Reason: reason,
			Pane: claudeQuietHookPane, CWD: claudeQuietHookCWD, SessionID: claudeQuietHookSession,
		}}
	}
	// markers is the hook marking of an owned Pane; every event leaves it.
	markers := func(extra map[string]string) map[string]string {
		options := map[string]string{
			"@projmux_ai_hook_active":       "1",
			"@projmux_ai_managed":           "1",
			"@projmux_ai_agent":             "claude",
			"@projmux_ai_context":           "/src/app",
			"@projmux_ai_session_id":        "claude-quiet-session",
			"@projmux_ai_resume_id":         "claude-quiet-session",
			"@projmux_ai_resume_source":     "hook",
			"@projmux_ai_resume_updated_at": "2026-09-14T09:30:00Z",
			"@projmux_ai_transcript_path":   "/home/u/.claude/projects/app/claude-quiet-session.jsonl",
		}
		maps.Copy(options, extra)
		return options
	}
	tests := []struct {
		name        string
		event       string
		quietAction bool
		want        claudeQuietHookOutcome
	}{
		{name: "SessionStart", event: "SessionStart", want: claudeQuietHookOutcome{
			PaneOptions: markers(nil), SessionRef: ref,
			ActivationState: "pending", ActivationSource: "provider-hook",
			Records: record("SessionStart", "quiet", "catalog quiet event"),
		}},
		{name: "UserPromptSubmit state", event: "UserPromptSubmit", want: claudeQuietHookOutcome{
			PaneOptions: markers(map[string]string{
				"@projmux_ai_state":        "thinking",
				"@projmux_ai_badge_kind":   "in_progress",
				"@projmux_attention_state": "busy",
			}),
			SessionRef:      ref,
			ActivationState: "acknowledged", ActivationSource: "provider-hook",
			Records: record("UserPromptSubmit", "state", ""),
		}},
		{name: "UserPromptSubmit runtime quiet", event: "UserPromptSubmit", quietAction: true, want: claudeQuietHookOutcome{
			PaneOptions: markers(nil), SessionRef: ref,
			ActivationState: "pending",
			Records:         record("UserPromptSubmit", "quiet", "runtime quiet event"),
		}},
		{name: "PreToolUse", event: "PreToolUse", want: claudeQuietHookOutcome{
			PaneOptions: markers(nil), SessionRef: ref,
			ActivationState: "pending",
			Records:         record("PreToolUse", "quiet", "catalog quiet event"),
		}},
		{name: "PostToolBatch", event: "PostToolBatch", want: claudeQuietHookOutcome{
			PaneOptions: markers(nil), SessionRef: ref,
			ActivationState: "pending",
			Records:         record("PostToolBatch", "quiet", "catalog quiet event"),
		}},
		{name: "Stop", event: "Stop", want: claudeQuietHookOutcome{
			PaneOptions: markers(map[string]string{
				"@projmux_ai_state":                 "waiting",
				"@projmux_ai_badge_kind":            "response_complete",
				"@projmux_attention_state":          "reply",
				"@projmux_attention_focus_armed":    "1",
				"@projmux_desktop_notified":         "1",
				"@projmux_desktop_notification_key": "hook|ready",
				"@projmux_desktop_notification_at":  "1789378200",
			}),
			SessionRef:      ref,
			ActivationState: "acknowledged", ActivationSource: "provider-hook",
			Records: record("Stop", "notify", ""),
		}},
		{name: "unknown event", event: "ExperimentalEvent", want: claudeQuietHookOutcome{
			PaneOptions: markers(nil), SessionRef: ref,
			ActivationState: "pending",
			Records:         record("ExperimentalEvent", "quiet", "unknown event"),
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newClaudeQuietHookFixture(t, true)
			if tc.quietAction {
				home, err := f.cmd.homeDir()
				if err != nil {
					t.Fatal(err)
				}
				paths := config.DefaultPaths(filepath.Join(home, ".config"), filepath.Join(home, ".local", "state"))
				if err := config.SaveAIHookActionsFile(paths.AIHookActionsFile(), config.AIHookActionsFile{
					Version: 1,
					Providers: map[string]config.AIHookProviderActions{
						aiHookProviderClaude: {Events: map[string]string{tc.event: aiHookActionQuiet}},
					},
				}); err != nil {
					t.Fatal(err)
				}
			}
			commands, _ := f.ingest(t, tc.event)

			agent, ok := f.registry.Agent(f.agentUID)
			if !ok {
				t.Fatalf("Agent %s disappeared", f.agentUID)
			}
			got := claudeQuietHookOutcome{
				PaneOptions:      claudeQuietHookPaneState(t, commands),
				SessionRef:       agent.Status.SessionRef,
				ActivationState:  string(agent.Status.Activation.State),
				ActivationSource: agent.Status.Activation.Source,
				Records:          claudeQuietHookLogRecords(t, f.cmd),
			}
			if !reflect.DeepEqual(got.PaneOptions, tc.want.PaneOptions) {
				t.Errorf("pane options =\n%#v\nwant\n%#v", got.PaneOptions, tc.want.PaneOptions)
			}
			if !reflect.DeepEqual(got.SessionRef, tc.want.SessionRef) {
				t.Errorf("session ref = %#v, want %#v", got.SessionRef, tc.want.SessionRef)
			}
			if got.ActivationState != tc.want.ActivationState || got.ActivationSource != tc.want.ActivationSource {
				t.Errorf("activation = %q/%q, want %q/%q", got.ActivationState, got.ActivationSource, tc.want.ActivationState, tc.want.ActivationSource)
			}
			if !reflect.DeepEqual(got.Records, tc.want.Records) {
				t.Errorf("records = %#v, want %#v", got.Records, tc.want.Records)
			}
		})
	}
}
