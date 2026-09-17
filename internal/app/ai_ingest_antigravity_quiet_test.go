package app

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// antigravityQuietHookClock is the fixed ingest clock of these tests, so the
// resume timestamp, the session ref and every status write are deterministic.
var antigravityQuietHookClock = time.Date(2026, 9, 15, 9, 30, 0, 0, time.UTC)

const (
	antigravityQuietHookPane         = "%7"
	antigravityQuietHookCWD          = "/src/app"
	antigravityQuietHookConversation = "antigravity-quiet-conversation"
	antigravityQuietHookTranscript   = "/home/u/.gemini/antigravity/brain/antigravity-quiet-conversation/transcript.jsonl"
	antigravityQuietHookGeneration   = "gen-antigravity-quiet"
)

// antigravityQuietHookFixture drives ingestAntigravityHook against an in-memory
// Registry and the recorded tmux transport of testAICommand. An owned fixture
// holds a running Antigravity Agent bound to the Pane and is attributed through
// the explicit `--pane` route a projmux-installed hook uses; an unbound fixture
// has no Agent and is attributed through the inherited TMUX_PANE.
type antigravityQuietHookFixture struct {
	cmd          *aiCommand
	registry     *coremetadata.Registry
	agentUID     string
	explicitPane string
	loads        int
}

func newAntigravityQuietHookFixture(t *testing.T, owned bool) *antigravityQuietHookFixture {
	t.Helper()

	home := t.TempDir()
	f := &antigravityQuietHookFixture{
		registry: &coremetadata.Registry{APIVersion: coremetadata.APIVersion, SchemaVersion: coremetadata.SchemaVersion},
	}
	paneUID := ""
	if owned {
		mutator := coremetadata.Mutator{
			Now:       func() time.Time { return antigravityQuietHookClock },
			NewUID:    sequentialTestUID(),
			DirExists: func(string) (bool, error) { return true, nil },
		}
		project, err := mutator.RegisterProject(f.registry, coremetadata.RegisterProjectOptions{
			Root: antigravityQuietHookCWD, DefaultShell: "/bin/zsh", OperationID: "op-1",
		})
		if err != nil {
			t.Fatalf("register project: %v", err)
		}
		agent, err := mutator.CreateAgent(f.registry, project.Windows[0].Metadata.UID, coremetadata.CreateAgentOptions{
			Provider: aiModeAntigravity, OperationID: "op-2",
		})
		if err != nil {
			t.Fatalf("create agent: %v", err)
		}
		pane, err := mutator.AttachAgentPane(f.registry, agent.Metadata.UID, coremetadata.BootstrapPane{
			Command: aiModeAntigravity, CWD: antigravityQuietHookCWD,
		}, "op-3")
		if err != nil {
			t.Fatalf("attach agent pane: %v", err)
		}
		if _, err := mutator.RecordPaneActivation(f.registry, pane.Metadata.UID, coremetadata.PaneActivationOptions{
			Generation: antigravityQuietHookGeneration, AgentUID: agent.Metadata.UID, OperationID: "op-3",
		}); err != nil {
			t.Fatalf("record pane activation: %v", err)
		}
		if _, err := mutator.ObservePaneActivationRuntime(f.registry, pane.Metadata.UID, antigravityQuietHookGeneration, antigravityQuietHookPane); err != nil {
			t.Fatalf("observe pane activation runtime: %v", err)
		}
		// A launch awaiting its provider, so a state event's activation commit
		// really runs between the hook marking and the event's record.
		bound, _ := f.registry.Agent(agent.Metadata.UID)
		bound.Status.Activation = coremetadata.AgentActivation{State: coremetadata.ActivationPending}
		f.agentUID = agent.Metadata.UID
		f.explicitPane = antigravityQuietHookPane
		paneUID = pane.Metadata.UID
	}

	store := &stubNotifyStore{}
	cmd := testAICommand(home)
	cmd.now = func() time.Time { return antigravityQuietHookClock }
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
				return antigravityQuietHookPane
			}
		case internalActivationPaneUIDEnv:
			return paneUID
		case internalActivationGenerationEnv:
			if owned {
				return antigravityQuietHookGeneration
			}
		}
		return ""
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if row, ok := testAIPaneRouteProbe(name, args); ok {
			return row, nil
		}
		if name == "tmux" && len(args) >= 5 && args[0] == "display-message" {
			switch args[4] {
			case "#{" + tmuxopts.PaneUID + "}":
				if paneUID != "" {
					return []byte(paneUID + "\n"), nil
				}
			}
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

// runtimeAction installs a runtime hook action for one Antigravity event, the
// override an operator writes with `projmux settings`.
func (f *antigravityQuietHookFixture) runtimeAction(t *testing.T, event, action string) {
	t.Helper()
	home, err := f.cmd.homeDir()
	if err != nil {
		t.Fatal(err)
	}
	paths := config.DefaultPaths(filepath.Join(home, ".config"), filepath.Join(home, ".local", "state"))
	if err := config.SaveAIHookActionsFile(paths.AIHookActionsFile(), config.AIHookActionsFile{
		Version: 1,
		Providers: map[string]config.AIHookProviderActions{
			aiHookProviderAntigravity: {Events: map[string]string{event: action}},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

// ingest runs one Antigravity hook event through the canonical handler, the way
// the installed hook passes it (`--event`), including its deferred session-ref
// flush, and returns only what that one call did.
func (f *antigravityQuietHookFixture) ingest(t *testing.T, event string, extra map[string]any) ([]recordedAICommand, int) {
	t.Helper()
	fields := map[string]any{
		"conversation_id": antigravityQuietHookConversation,
		"cwd":             antigravityQuietHookCWD,
		"transcript_path": antigravityQuietHookTranscript,
	}
	maps.Copy(fields, extra)
	payload, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	commandsBefore, loadsBefore := len(cmdRecorder(f.cmd).commands), f.loads
	if err := f.cmd.ingestAntigravityHook(payload, event, f.explicitPane); err != nil {
		t.Fatalf("ingest antigravity %s: %v", event, err)
	}
	return cmdRecorder(f.cmd).commands[commandsBefore:], f.loads - loadsBefore
}

// TestAntigravityQuietHookMarksThePaneOnce owns the cost of a quiet Antigravity
// event. ingestAntigravityHook marks the attributed Pane before it dispatches,
// and a quiet branch only appends its record, so one event is one hook marking:
// one write per marker option and one Registry binding read. The pane option
// counting is provider-agnostic and shared with the Claude quiet tests.
func TestAntigravityQuietHookMarksThePaneOnce(t *testing.T) {
	t.Parallel()

	// Antigravity hands its conversation id over as both thread and session, so
	// the marking also writes the thread option.
	ownedOptions := []string{
		aiPaneHookActiveOption, aiPaneManagedOption, aiPaneAgentOption, aiPaneContextOption,
		aiPaneThreadIDOption, aiPaneSessionIDOption, aiPaneResumeIDOption, aiPaneResumeSourceOption,
		aiPaneResumeUpdatedAtOption, aiPaneTranscriptPathOption,
	}
	unboundOptions := []string{
		aiPaneHookActiveOption, aiPaneContextOption, aiPaneThreadIDOption, aiPaneSessionIDOption,
		aiPaneResumeIDOption, aiPaneResumeSourceOption, aiPaneResumeUpdatedAtOption, aiPaneTranscriptPathOption,
	}
	tests := []struct {
		name      string
		owned     bool
		event     string
		payload   map[string]any
		action    string
		options   []string
		forbidden []string
		// wantTmux is every recorded tmux command of the one ingest call: the
		// marker set-options and nothing else, because a quiet event writes no
		// status. The route probe and the Pane uid read are reads, not
		// commands.
		//   owned:   10 marker options = 10.
		//   unbound: 10 minus managed and agent = 8.
		wantTmux int
		// wantLoads is every Registry read of the one ingest call.
		//   owned:   explicit --pane resolution (1) + the mark's binding read (1)
		//            + the deferred session-ref flush (1) = 3.
		//   unbound: the mark's binding read only (1); TMUX_PANE attribution
		//            reads no Registry, and an Agent-less Registry stages no ref.
		wantLoads int
	}{
		{name: "owned PostToolUse", owned: true, event: "PostToolUse", options: ownedOptions, wantTmux: 10, wantLoads: 3},
		{
			name: "owned PostToolUse with error", owned: true, event: "PostToolUse",
			payload: map[string]any{"error": "exit status 1"}, options: ownedOptions, wantTmux: 10, wantLoads: 3,
		},
		{name: "owned PostInvocation", owned: true, event: "PostInvocation", options: ownedOptions, wantTmux: 10, wantLoads: 3},
		{name: "owned unknown event", owned: true, event: "ExperimentalEvent", options: ownedOptions, wantTmux: 10, wantLoads: 3},
		{
			name: "owned Stop runtime quiet", owned: true, event: "Stop",
			payload: map[string]any{"terminationReason": "completed"}, action: aiHookActionQuiet,
			options: ownedOptions, wantTmux: 10, wantLoads: 3,
		},
		{
			name: "owned PreInvocation runtime quiet", owned: true, event: "PreInvocation",
			action: aiHookActionQuiet, options: ownedOptions, wantTmux: 10, wantLoads: 3,
		},
		{
			name: "unbound PostToolUse", event: "PostToolUse", options: unboundOptions,
			forbidden: []string{aiPaneManagedOption, aiPaneAgentOption}, wantTmux: 8, wantLoads: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newAntigravityQuietHookFixture(t, tc.owned)
			if tc.action != "" {
				f.runtimeAction(t, tc.event, tc.action)
			}
			commands, loads := f.ingest(t, tc.event, tc.payload)

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
			records := claudeQuietHookLogRecords(t, f.cmd)
			if len(records) != 1 || records[0].Result != "quiet" {
				t.Errorf("records = %#v, want one quiet record", records)
			}
		})
	}
}

// antigravityQuietHookOutcome is what an Antigravity hook event leaves behind:
// the final Pane option state, the Agent's durable conversation pointer and
// activation boundary, and the ingest records.
type antigravityQuietHookOutcome struct {
	PaneOptions      map[string]string
	SessionRef       *coremetadata.AgentSessionRef
	ActivationState  string
	ActivationSource string
	Records          []aiIngestLogEntry
}

// TestAntigravityHookOutcomeIsUnchangedByMarkingOnce characterizes every
// Antigravity dispatch family on an owned
// Pane with golden values. It was green before quietAntigravityHook stopped
// re-marking the Pane and must stay green after: the second mark repeated
// identical writes, so collapsing it changes no state.
func TestAntigravityHookOutcomeIsUnchangedByMarkingOnce(t *testing.T) {
	t.Parallel()

	ref := &coremetadata.AgentSessionRef{
		Provider:   aiModeAntigravity,
		ObservedAt: antigravityQuietHookClock,
		Antigravity: &coremetadata.AntigravitySessionRef{
			ConversationID: antigravityQuietHookConversation,
			TranscriptPath: antigravityQuietHookTranscript,
		},
	}
	record := func(event, result string, reason aiIngestReason) []aiIngestLogEntry {
		return []aiIngestLogEntry{{
			Source: "antigravity-hook", Event: event, Result: result, Reason: reason,
			Pane: antigravityQuietHookPane, CWD: antigravityQuietHookCWD, ThreadID: antigravityQuietHookConversation,
		}}
	}
	// markers is the hook marking of an owned Pane; every event leaves it.
	markers := func(extra map[string]string) map[string]string {
		options := map[string]string{
			"@projmux_ai_hook_active":       "1",
			"@projmux_ai_managed":           "1",
			"@projmux_ai_agent":             "antigravity",
			"@projmux_ai_context":           "/src/app",
			"@projmux_ai_thread_id":         "antigravity-quiet-conversation",
			"@projmux_ai_session_id":        "antigravity-quiet-conversation",
			"@projmux_ai_resume_id":         "antigravity-quiet-conversation",
			"@projmux_ai_resume_source":     "hook",
			"@projmux_ai_resume_updated_at": "2026-09-15T09:30:00Z",
			"@projmux_ai_transcript_path":   "/home/u/.gemini/antigravity/brain/antigravity-quiet-conversation/transcript.jsonl",
		}
		maps.Copy(options, extra)
		return options
	}
	stopPayload := map[string]any{"terminationReason": "completed"}
	tests := []struct {
		name    string
		event   string
		payload map[string]any
		action  string
		want    antigravityQuietHookOutcome
	}{
		{name: "Stop notify", event: "Stop", payload: stopPayload, want: antigravityQuietHookOutcome{
			PaneOptions: markers(map[string]string{
				"@projmux_ai_state":                 "waiting",
				"@projmux_ai_badge_kind":            "response_complete",
				"@projmux_attention_state":          "reply",
				"@projmux_attention_focus_armed":    "1",
				"@projmux_desktop_notified":         "1",
				"@projmux_desktop_notification_key": "hook|ready",
				"@projmux_desktop_notification_at":  "1789464600",
			}),
			SessionRef:      ref,
			ActivationState: "acknowledged", ActivationSource: "provider-hook",
			Records: record("Stop", "notify", ""),
		}},
		{name: "Stop runtime state", event: "Stop", payload: stopPayload, action: aiHookActionState, want: antigravityQuietHookOutcome{
			PaneOptions: markers(map[string]string{
				"@projmux_ai_state":              "waiting",
				"@projmux_ai_badge_kind":         "response_complete",
				"@projmux_attention_state":       "reply",
				"@projmux_attention_focus_armed": "1",
			}),
			SessionRef:      ref,
			ActivationState: "acknowledged", ActivationSource: "provider-hook",
			Records: record("Stop", "state", "runtime state event"),
		}},
		{name: "Stop runtime quiet", event: "Stop", payload: stopPayload, action: aiHookActionQuiet, want: antigravityQuietHookOutcome{
			PaneOptions: markers(nil), SessionRef: ref,
			ActivationState: "pending",
			Records:         record("Stop", "quiet", "runtime quiet event"),
		}},
		{name: "PreInvocation state", event: "PreInvocation", want: antigravityQuietHookOutcome{
			PaneOptions: markers(map[string]string{
				"@projmux_ai_state":        "thinking",
				"@projmux_ai_badge_kind":   "in_progress",
				"@projmux_attention_state": "busy",
			}),
			SessionRef:      ref,
			ActivationState: "acknowledged", ActivationSource: "provider-hook",
			Records: record("PreInvocation", "state", "invocation started"),
		}},
		{name: "PreInvocation runtime quiet", event: "PreInvocation", action: aiHookActionQuiet, want: antigravityQuietHookOutcome{
			PaneOptions: markers(nil), SessionRef: ref,
			ActivationState: "pending",
			Records:         record("PreInvocation", "quiet", "runtime quiet event"),
		}},
		{name: "PostInvocation", event: "PostInvocation", want: antigravityQuietHookOutcome{
			PaneOptions: markers(nil), SessionRef: ref,
			ActivationState: "pending",
			Records:         record("PostInvocation", "quiet", "catalog quiet event"),
		}},
		{name: "PostToolUse", event: "PostToolUse", want: antigravityQuietHookOutcome{
			PaneOptions: markers(nil), SessionRef: ref,
			ActivationState: "pending",
			Records:         record("PostToolUse", "quiet", "catalog quiet event"),
		}},
		{name: "PostToolUse with error", event: "PostToolUse", payload: map[string]any{"error": "exit status 1"}, want: antigravityQuietHookOutcome{
			PaneOptions: markers(nil), SessionRef: ref,
			ActivationState: "pending",
			Records:         record("PostToolUse", "quiet", "tool reported an error"),
		}},
		{name: "unknown event", event: "ExperimentalEvent", want: antigravityQuietHookOutcome{
			PaneOptions: markers(nil), SessionRef: ref,
			ActivationState: "pending",
			Records:         record("ExperimentalEvent", "quiet", "unknown event"),
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newAntigravityQuietHookFixture(t, true)
			if tc.action != "" {
				f.runtimeAction(t, tc.event, tc.action)
			}
			commands, _ := f.ingest(t, tc.event, tc.payload)

			agent, ok := f.registry.Agent(f.agentUID)
			if !ok {
				t.Fatalf("Agent %s disappeared", f.agentUID)
			}
			got := antigravityQuietHookOutcome{
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
