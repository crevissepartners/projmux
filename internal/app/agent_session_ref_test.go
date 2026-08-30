package app

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// sessionRefObservedAt is the deterministic ingest clock of these tests.
var sessionRefObservedAt = time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)

// sessionRefHarness wires an aiCommand against an in-memory registry and a
// recorded tmux transport, so an ingest run can be judged on both the pane
// options it wrote and the Agent resource it updated.
type sessionRefHarness struct {
	cmd      *aiCommand
	registry *coremetadata.Registry
	agentUID string
	paneUID  string

	tmuxCalls     []string
	updates       int
	loads         int
	envPaneUID    string
	envGeneration string
	envTMUXPane   string
}

func newSessionRefHarness(t *testing.T, provider string) *sessionRefHarness {
	t.Helper()

	mutator := coremetadata.Mutator{
		Now:       func() time.Time { return sessionRefObservedAt },
		NewUID:    sequentialTestUID(),
		DirExists: func(string) (bool, error) { return true, nil },
	}
	registry := &coremetadata.Registry{APIVersion: coremetadata.APIVersion, SchemaVersion: coremetadata.SchemaVersion}
	project, err := mutator.RegisterProject(registry, coremetadata.RegisterProjectOptions{
		Root:         "/src/app",
		DefaultShell: "/bin/zsh",
		OperationID:  "op-1",
	})
	if err != nil {
		t.Fatalf("register project: %v", err)
	}
	agent, err := mutator.CreateAgent(registry, project.Windows[0].Metadata.UID, coremetadata.CreateAgentOptions{
		Provider:    provider,
		OperationID: "op-2",
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	pane, err := mutator.AttachAgentPane(registry, agent.Metadata.UID, coremetadata.BootstrapPane{
		Command: provider,
		CWD:     "/src/app",
	}, "op-3")
	if err != nil {
		t.Fatalf("attach agent pane: %v", err)
	}
	if _, err := mutator.RecordPaneActivation(registry, pane.Metadata.UID, coremetadata.PaneActivationOptions{
		Generation: "gen-session-ref", AgentUID: agent.Metadata.UID, OperationID: "op-3",
	}); err != nil {
		t.Fatalf("record pane activation: %v", err)
	}
	if _, err := mutator.ObservePaneActivationRuntime(registry, pane.Metadata.UID, "gen-session-ref", "%7"); err != nil {
		t.Fatalf("observe pane activation runtime: %v", err)
	}

	h := &sessionRefHarness{
		registry: registry, agentUID: agent.Metadata.UID, paneUID: pane.Metadata.UID,
		envPaneUID: pane.Metadata.UID, envGeneration: "gen-session-ref", envTMUXPane: "%7",
	}
	home := t.TempDir()
	h.cmd = &aiCommand{
		lookupEnv: func(name string) string {
			switch name {
			case "HOME":
				return home
			case "TMUX_PANE":
				// Ingest attributes the event to the inherited pane, which is the
				// production path a provider hook runs through.
				return h.envTMUXPane
			case internalActivationPaneUIDEnv:
				return h.envPaneUID
			case internalActivationGenerationEnv:
				return h.envGeneration
			default:
				return ""
			}
		},
		homeDir:   func() (string, error) { return home, nil },
		readFile:  func(string) ([]byte, error) { return nil, os.ErrNotExist },
		writeFile: os.WriteFile,
		mkdirAll:  os.MkdirAll,
		now:       func() time.Time { return sessionRefObservedAt },
		sleep:     func(time.Duration) {},
		runCommand: func(_ context.Context, name string, args ...string) error {
			h.tmuxCalls = append(h.tmuxCalls, name+" "+strings.Join(args, " "))
			return nil
		},
		readCommand: func(_ context.Context, name string, args ...string) ([]byte, error) {
			h.tmuxCalls = append(h.tmuxCalls, name+" "+strings.Join(args, " "))
			if len(args) >= 5 && args[0] == "display-message" && args[4] == "#{"+tmuxopts.PaneUID+"}" {
				return []byte(h.paneUID + "\n"), nil
			}
			return nil, os.ErrNotExist
		},
		loadRegistry: func() (coremetadata.Registry, error) {
			h.loads++
			return h.registry.Clone(), nil
		},
	}
	h.cmd.updateRegistry = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
		h.updates++
		working := h.registry.Clone()
		if err := fn(&working); err != nil {
			// The real store performs no write at all when the operation fails.
			return coremetadata.Registry{}, err
		}
		if err := working.Validate(); err != nil {
			return coremetadata.Registry{}, err
		}
		*h.registry = working
		return working.Clone(), nil
	}
	return h
}

func (h *sessionRefHarness) agent(t *testing.T) coremetadata.Agent {
	t.Helper()
	agent, ok := h.registry.Agent(h.agentUID)
	if !ok {
		t.Fatalf("agent %s disappeared", h.agentUID)
	}
	return agent.Clone()
}

func TestCreateTimeSessionRefMakesSameAndCrossProviderHooksWriteFree(t *testing.T) {
	h := newSessionRefHarness(t, aiModeCodex)
	agent, _ := h.registry.Agent(h.agentUID)
	agent.Status.SessionRef = codexConversationRef("thread-picker")
	before := h.registry.Clone()

	h.cmd.persistAgentSessionRef("%7", coremetadata.AgentSessionObservation{Provider: aiModeCodex, ThreadID: "thread-picker"})
	h.cmd.persistAgentSessionRef("%7", coremetadata.AgentSessionObservation{Provider: aiModeClaude, SessionID: "thread-picker"})

	if h.updates != 0 {
		t.Fatalf("same/cross-provider hooks opened %d Registry write transactions, want zero", h.updates)
	}
	if !reflect.DeepEqual(before, *h.registry) {
		t.Fatal("same/cross-provider hook changed create-time sessionRef")
	}
}

func (h *sessionRefHarness) ingest(t *testing.T, args []string, payload string) {
	t.Helper()
	h.cmd.stdin = strings.NewReader(payload)
	var stdout, stderr bytes.Buffer
	if err := h.cmd.runIngest(args, &stdout, &stderr); err != nil {
		t.Fatalf("runIngest %v: %v (stderr=%s)", args, err, stderr.String())
	}
}

func sequentialTestUID() func(coremetadata.Kind) (string, error) {
	counts := map[coremetadata.Kind]int{}
	return func(kind coremetadata.Kind) (string, error) {
		counts[kind]++
		return strings.ToLower(string(kind)) + "-0" + string(rune('0'+counts[kind])), nil
	}
}

func seedNativeCodexBinding(t *testing.T, h *sessionRefHarness, threadID, turnID string) {
	t.Helper()
	agent, _ := h.registry.Agent(h.agentUID)
	pane, _ := h.registry.Pane(h.paneUID)
	agent.Status.SessionRef = &coremetadata.AgentSessionRef{
		Provider: aiModeCodex, ObservedAt: sessionRefObservedAt,
		Codex: &coremetadata.CodexSessionRef{ThreadID: threadID},
	}
	pane.Status.Activation.Codex = &coremetadata.CodexActivationBinding{ThreadID: threadID, TurnID: turnID}
}

func TestNativeCodexHookRejectsForeignIdentityBeforeRemapOrWrite(t *testing.T) {
	tests := []struct {
		name          string
		threadID      string
		envPaneUID    string
		envGeneration string
	}{
		{name: "other thread", threadID: "thread-other"},
		{name: "other Pane", threadID: "thread-native", envPaneUID: "pane-other"},
		{name: "previous generation", threadID: "thread-native", envGeneration: "gen-previous"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newSessionRefHarness(t, aiModeCodex)
			seedNativeCodexBinding(t, h, "thread-native", "turn-initial")
			if tc.envPaneUID != "" {
				h.envPaneUID = tc.envPaneUID
			}
			if tc.envGeneration != "" {
				h.envGeneration = tc.envGeneration
			}
			before := h.registry.Clone()
			h.ingest(t, []string{"codex-hook"}, `{"hook_event_name":"UserPromptSubmit","thread_id":"`+tc.threadID+`","turn_id":"turn-foreign","cwd":"/src/app"}`)
			if h.updates != 0 {
				t.Fatalf("Registry update transactions = %d, want zero", h.updates)
			}
			if !reflect.DeepEqual(before, *h.registry) {
				t.Fatal("foreign hook mutated native Registry binding")
			}
			if slices.ContainsFunc(h.tmuxCalls, func(call string) bool { return strings.Contains(call, " set-option ") }) {
				t.Fatalf("foreign hook remapped tmux state: %v", h.tmuxCalls)
			}
		})
	}
}

func TestNativeCodexHookRefinesOnlyTheExactThreadTurn(t *testing.T) {
	h := newSessionRefHarness(t, aiModeCodex)
	seedNativeCodexBinding(t, h, "thread-native", "turn-initial")
	// Native routing ignores inherited TMUX_PANE and uses the exact activation
	// runtime binding recorded for this Pane generation.
	h.envTMUXPane = "%foreign"
	h.ingest(t, []string{"codex-hook"}, `{"hook_event_name":"UserPromptSubmit","thread_id":"thread-native","turn_id":"turn-next","cwd":"/src/app"}`)
	pane, _ := h.registry.Pane(h.paneUID)
	if pane.Status.Activation.Codex == nil || pane.Status.Activation.Codex.TurnID != "turn-next" {
		t.Fatalf("exact hook turn refinement = %#v", pane.Status.Activation.Codex)
	}
	if h.updates != 1 {
		t.Fatalf("Registry update transactions = %d, want one exact refinement", h.updates)
	}
}

func TestHookRoutingWaitsOnlyForItsExactPublishedPaneUID(t *testing.T) {
	h := newSessionRefHarness(t, aiModeCodex)
	seedNativeCodexBinding(t, h, "thread-native", "turn-initial")
	ready := h.registry.Clone()
	h.cmd.loadRegistry = func() (coremetadata.Registry, error) {
		h.loads++
		if h.loads < 3 {
			withoutPane := ready.Clone()
			withoutPane.Panes = nil
			return withoutPane, nil
		}
		return ready.Clone(), nil
	}
	paneID, handled, allowed, reason := h.cmd.routeNativeCodexHook("thread-native")
	if paneID != "%7" || !handled || !allowed || reason != "" || h.loads != 3 {
		t.Fatalf("route = (%q,%t,%t,%q) loads=%d", paneID, handled, allowed, reason, h.loads)
	}
}

func TestCompatibilityAITopicAndStatusForwardThroughAgentAuthority(t *testing.T) {
	t.Parallel()
	h := newSessionRefHarness(t, aiModeCodex)

	var stdout, stderr bytes.Buffer
	if err := h.cmd.Run([]string{"topic", "set", "--pane", "%7", "review"}, &stdout, &stderr); err != nil {
		t.Fatalf("ai topic set: %v (stderr=%q)", err, stderr.String())
	}
	if stdout.String() != "" || stderr.String() != "" {
		t.Fatalf("ai topic set bytes stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if got := h.agent(t).Metadata.Annotations[coremetadata.AnnotationAgentTopic]; got != "review" {
		t.Fatalf("compatibility topic bypassed Registry: %q", got)
	}

	stdout.Reset()
	stderr.Reset()
	if err := h.cmd.Run([]string{"topic", "get", "--pane", "%7"}, &stdout, &stderr); err != nil {
		t.Fatalf("ai topic get: %v", err)
	}
	if stdout.String() != "review\n" || stderr.String() != "" {
		t.Fatalf("ai topic get bytes stdout=%q stderr=%q", stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if err := h.cmd.Run([]string{"status", "set", "waiting", "%7"}, &stdout, &stderr); err != nil {
		t.Fatalf("ai status set: %v (stderr=%q)", err, stderr.String())
	}
	interaction := h.agent(t).Status.Interaction
	if interaction.Kind != coremetadata.InteractionResponseComplete || interaction.Source != "compatibility-ai" {
		t.Fatalf("compatibility status bypassed Registry: %+v", interaction)
	}
	if stdout.String() != "" || stderr.String() != "" {
		t.Fatalf("ai status set bytes stdout=%q stderr=%q", stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if err := h.cmd.Run([]string{"topic", "clear", "--pane", "%7"}, &stdout, &stderr); err != nil {
		t.Fatalf("ai topic clear: %v", err)
	}
	if _, ok := h.agent(t).Metadata.Annotations[coremetadata.AnnotationAgentTopic]; ok {
		t.Fatal("compatibility topic clear did not clear Registry authority")
	}
	if stdout.String() != "" || stderr.String() != "" {
		t.Fatalf("ai topic clear bytes stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

// TestOneProviderHookRecordsItsOwnConversationShapeOnTheAgent is the
// per-provider ingest table. Each provider goes through the canonical hook
// ingest handler with a real payload, and each one is judged on the union
// member it populated -- Claude's session id plus transcript path, Codex's
// thread and session ids, Antigravity's single conversation id.
func TestOneProviderHookRecordsItsOwnConversationShapeOnTheAgent(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		args     []string
		payload  string
		want     *coremetadata.AgentSessionRef
	}{
		{
			name:     "claude reports a session id and a transcript path",
			provider: "claude",
			args:     []string{"claude-hook"},
			payload: `{"hook_event_name":"UserPromptSubmit","session_id":"claude-session-1",` +
				`"transcript_path":"/home/u/.claude/projects/app/claude-session-1.jsonl","cwd":"/src/app"}`,
			want: &coremetadata.AgentSessionRef{
				Provider:   "claude",
				ObservedAt: sessionRefObservedAt,
				Claude: &coremetadata.ClaudeSessionRef{
					SessionID:      "claude-session-1",
					TranscriptPath: "/home/u/.claude/projects/app/claude-session-1.jsonl",
				},
			},
		},
		{
			name:     "codex reports a thread id beside its session id",
			provider: "codex",
			args:     []string{"codex-hook"},
			payload: `{"hook_event_name":"UserPromptSubmit","thread_id":"codex-thread-1",` +
				`"session_id":"codex-session-1","turn_id":"codex-turn-1","cwd":"/src/app"}`,
			want: &coremetadata.AgentSessionRef{
				Provider:   "codex",
				ObservedAt: sessionRefObservedAt,
				Codex: &coremetadata.CodexSessionRef{
					ThreadID:  "codex-thread-1",
					SessionID: "codex-session-1",
				},
			},
		},
		{
			name:     "antigravity reports one conversation id",
			provider: "antigravity",
			args:     []string{"antigravity-hook", "--event", "PreInvocation"},
			payload:  `{"conversationId":"antigravity-conversation-1","workspacePaths":["/src/app"]}`,
			want: &coremetadata.AgentSessionRef{
				Provider:    "antigravity",
				ObservedAt:  sessionRefObservedAt,
				Antigravity: &coremetadata.AntigravitySessionRef{ConversationID: "antigravity-conversation-1"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newSessionRefHarness(t, tc.provider)
			if before := h.agent(t); before.Status.SessionRef != nil {
				t.Fatal("the fixture Agent already carries a session ref")
			}

			h.ingest(t, tc.args, tc.payload)

			got := h.agent(t).Status.SessionRef
			if got == nil {
				t.Fatalf("no session ref recorded; tmux calls = %v", h.tmuxCalls)
			}
			if got.Provider != tc.want.Provider || !got.ObservedAt.Equal(tc.want.ObservedAt) {
				t.Fatalf("ref header = %s/%s, want %s/%s", got.Provider, got.ObservedAt, tc.want.Provider, tc.want.ObservedAt)
			}
			if !got.SameConversation(tc.want) {
				t.Fatalf("ref = %#v, want %#v", got, tc.want)
			}
			// A per-provider union means the other members stay nil.
			switch tc.provider {
			case "claude":
				if got.Codex != nil || got.Antigravity != nil {
					t.Fatalf("claude ref populated another provider member: %#v", got)
				}
			case "codex":
				if got.Claude != nil || got.Antigravity != nil {
					t.Fatalf("codex ref populated another provider member: %#v", got)
				}
			case "antigravity":
				if got.Claude != nil || got.Codex != nil {
					t.Fatalf("antigravity ref populated another provider member: %#v", got)
				}
			}
			if err := h.registry.Validate(); err != nil {
				t.Fatalf("registry invalid after ingest: %v", err)
			}
		})
	}
}

// TestIngestStillWritesThePaneSessionOptionUnchanged is acceptance criterion 5:
// the Agent field is a second, additive home and the live routing index the
// ingest matcher depends on is written exactly as before.
func TestIngestStillWritesThePaneSessionOptionUnchanged(t *testing.T) {
	h := newSessionRefHarness(t, "codex")
	h.ingest(t, []string{"codex-hook"},
		`{"hook_event_name":"UserPromptSubmit","thread_id":"codex-thread-1","session_id":"codex-session-1","cwd":"/src/app"}`)

	wantOptions := []string{
		"tmux set-option -p -t %7 " + aiPaneSessionIDOption + " codex-session-1",
		"tmux set-option -p -t %7 " + aiPaneThreadIDOption + " codex-thread-1",
		"tmux set-option -p -t %7 " + aiPaneAgentOption + " codex",
		"tmux set-option -p -t %7 " + aiPaneResumeIDOption + " codex-session-1",
	}
	for _, want := range wantOptions {
		if !slices.Contains(h.tmuxCalls, want) {
			t.Fatalf("missing pane option write %q in %v", want, h.tmuxCalls)
		}
	}
	// Nothing removes or unsets the routing index.
	for _, call := range h.tmuxCalls {
		if strings.Contains(call, "set-option") && strings.Contains(call, "-u") && strings.Contains(call, aiPaneSessionIDOption) {
			t.Fatalf("ingest unset the live routing index: %q", call)
		}
	}
}

// TestAnUnwiredRegistrySeamLeavesIngestExactlyAsItWas keeps the many aiCommand
// fixtures -- and any caller that never wires the seam -- from reaching the real
// state directory just because a hook was ingested.
func TestAnUnwiredRegistrySeamLeavesIngestExactlyAsItWas(t *testing.T) {
	h := newSessionRefHarness(t, "codex")
	h.cmd.loadRegistry = nil
	h.cmd.updateRegistry = nil

	h.ingest(t, []string{"codex-hook"},
		`{"hook_event_name":"UserPromptSubmit","thread_id":"codex-thread-1","cwd":"/src/app"}`)

	if h.updates != 0 || h.loads != 0 {
		t.Fatalf("registry touched with an unwired seam: loads=%d updates=%d", h.loads, h.updates)
	}
	if agent := h.agent(t); agent.Status.SessionRef != nil {
		t.Fatalf("session ref recorded through an unwired seam: %#v", agent.Status.SessionRef)
	}
	for _, call := range h.tmuxCalls {
		if strings.Contains(call, tmuxopts.PaneUID) {
			t.Fatalf("an unwired seam still queried the pane uid: %q", call)
		}
	}
}

// TestIngestNegativesDoNotInventAConversation covers every reason the session
// ref write is skipped. An attributable hook without a conversation id still
// records its semantic interaction, in one transaction, but never invents a
// provider conversation pointer.
func TestIngestNegativesOpenNoRegistryTransaction(t *testing.T) {
	tests := []struct {
		name        string
		arrange     func(t *testing.T, h *sessionRefHarness)
		payload     string
		wantUpdates int
	}{
		{
			name: "the registry holds no Agent at all",
			arrange: func(_ *testing.T, h *sessionRefHarness) {
				h.registry.Agents = nil
			},
			payload: `{"hook_event_name":"UserPromptSubmit","thread_id":"codex-thread-1","cwd":"/src/app"}`,
		},
		{
			name: "the pane mirrors no Projmux uid",
			arrange: func(_ *testing.T, h *sessionRefHarness) {
				h.paneUID = ""
			},
			payload: `{"hook_event_name":"UserPromptSubmit","thread_id":"codex-thread-1","cwd":"/src/app"}`,
		},
		{
			name: "the pane is a Window-owned shell pane, not a managed Agent pane",
			arrange: func(t *testing.T, h *sessionRefHarness) {
				pane, ok := h.registry.Pane(h.paneUID)
				if !ok {
					t.Fatalf("pane %s missing", h.paneUID)
				}
				h.paneUID = h.registry.Windows[0].Spec.AnchorPaneRef
				_ = pane
			},
			payload: `{"hook_event_name":"UserPromptSubmit","thread_id":"codex-thread-1","cwd":"/src/app"}`,
		},
		{
			name:        "the hook carries no conversation id yet",
			arrange:     func(*testing.T, *sessionRefHarness) {},
			payload:     `{"hook_event_name":"UserPromptSubmit","cwd":"/src/app"}`,
			wantUpdates: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newSessionRefHarness(t, "codex")
			tc.arrange(t, h)
			h.ingest(t, []string{"codex-hook"}, tc.payload)

			if h.updates != tc.wantUpdates {
				t.Fatalf("opened %d registry write transactions, want %d", h.updates, tc.wantUpdates)
			}
			if agent, ok := h.registry.Agent(h.agentUID); ok && agent.Status.SessionRef != nil {
				t.Fatalf("recorded %#v, want nothing", agent.Status.SessionRef)
			}
		})
	}
}

// TestARepeatedHookDoesNotRewriteTheRegistry keeps a hook that fires on every
// turn from opening a write transaction each time.
func TestARepeatedHookDoesNotRewriteTheRegistry(t *testing.T) {
	h := newSessionRefHarness(t, "claude")
	payload := `{"hook_event_name":"UserPromptSubmit","session_id":"claude-session-1","cwd":"/src/app"}`

	h.ingest(t, []string{"claude-hook"}, payload)
	if h.updates != 1 {
		t.Fatalf("first ingest opened %d transactions, want 1", h.updates)
	}
	h.ingest(t, []string{"claude-hook"}, payload)
	h.ingest(t, []string{"claude-hook"}, payload)
	if h.updates != 1 {
		t.Fatalf("re-observing the same conversation opened %d transactions, want 1", h.updates)
	}

	// A different conversation is a real change and is recorded.
	h.ingest(t, []string{"claude-hook"},
		`{"hook_event_name":"UserPromptSubmit","session_id":"claude-session-2","cwd":"/src/app"}`)
	if h.updates != 2 {
		t.Fatalf("a new conversation opened %d transactions, want 2", h.updates)
	}
	if got := h.agent(t).Status.SessionRef.Summary(); got != "claude:claude-session-2" {
		t.Fatalf("session ref = %q, want the newest conversation", got)
	}
}

// TestACrossProviderHookNeverStampsAnAgent proves the provider guard survives
// the whole ingest path: a Claude hook attributed to a Codex Agent's pane
// records nothing and leaves the registry byte-identical.
func TestACrossProviderHookNeverStampsAnAgent(t *testing.T) {
	h := newSessionRefHarness(t, "codex")
	before := h.agent(t)

	h.ingest(t, []string{"claude-hook"},
		`{"hook_event_name":"UserPromptSubmit","session_id":"claude-session-1","cwd":"/src/app"}`)

	after := h.agent(t)
	if after.Status.SessionRef != nil {
		t.Fatalf("a claude hook stamped a codex Agent: %#v", after.Status.SessionRef)
	}
	if after.Status.Phase != before.Status.Phase || after.Status.PaneRef != before.Status.PaneRef {
		t.Fatalf("the refused write disturbed the Agent: %+v", after.Status)
	}
	if err := h.registry.Validate(); err != nil {
		t.Fatalf("registry invalid: %v", err)
	}
}

// TestARecordedSessionRefSurvivesTheAgentGoingOffline is acceptance criterion 2
// through the production release path: the managed Pane is deleted, the Agent
// falls back to Offline with a cleared paneRef, and the conversation pointer is
// still there.
func TestARecordedSessionRefSurvivesTheAgentGoingOffline(t *testing.T) {
	h := newSessionRefHarness(t, "claude")
	h.ingest(t, []string{"claude-hook"},
		`{"hook_event_name":"UserPromptSubmit","session_id":"claude-session-1","cwd":"/src/app"}`)

	mutator := intmetadata.DefaultMutator()
	if _, err := mutator.ReleaseAgentPane(h.registry, h.agentUID, coremetadata.AgentExitNormal, "exited"); err != nil {
		t.Fatalf("release agent pane: %v", err)
	}

	agent := h.agent(t)
	if agent.Status.Phase != coremetadata.PhaseOffline || agent.Status.PaneRef != "" {
		t.Fatalf("agent = %s/%q, want Offline with no paneRef", agent.Status.Phase, agent.Status.PaneRef)
	}
	if got := agent.Status.SessionRef.Summary(); got != "claude:claude-session-1" {
		t.Fatalf("session ref after the Pane died = %q, want it preserved", got)
	}
	if err := h.registry.Validate(); err != nil {
		t.Fatalf("registry invalid: %v", err)
	}
}

// setFixtureSessionRef stamps one conversation onto the fixture's Agent so the
// read routes have something to surface.
func setFixtureSessionRef(t *testing.T, store *fakeResourceStore, uid string, ref *coremetadata.AgentSessionRef) {
	t.Helper()
	agent, ok := store.registry.Agent(uid)
	if !ok {
		t.Fatalf("fixture agent %q missing", uid)
	}
	agent.Status.SessionRef = ref
	if err := store.registry.Validate(); err != nil {
		t.Fatalf("fixture registry invalid: %v", err)
	}
}

// TestDescribeAgentRendersThePerProviderSessionRef is acceptance criterion 1 at
// the read surface. The row keys follow the populated provider member, which is
// the observable difference a flattened single string could not produce.
func TestDescribeAgentRendersThePerProviderSessionRef(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		ref  *coremetadata.AgentSessionRef
		want map[string]string
		gone []string
	}{
		{
			name: "codex renders a thread id and a session id",
			ref: &coremetadata.AgentSessionRef{
				Provider:   "codex",
				ObservedAt: sessionRefObservedAt,
				Codex:      &coremetadata.CodexSessionRef{ThreadID: "codex-thread-1", SessionID: "codex-session-1"},
			},
			want: map[string]string{
				"SessionProvider":   "codex",
				"ThreadID":          "codex-thread-1",
				"SessionID":         "codex-session-1",
				"SessionObservedAt": "2026-08-15T12:00:00Z",
			},
			gone: []string{"TranscriptPath", "ConversationID"},
		},
		{
			name: "an Agent with no observed conversation renders no session rows",
			ref:  nil,
			gone: []string{"SessionProvider", "SessionID", "ThreadID", "ConversationID", "SessionObservedAt"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			setFixtureSessionRef(t, store, "agt-alpha-codex", test.ref)

			stdout, stderr, err := runRoute(t, newTestDescribeCommand(t, store), "agent", "codex", "--project", "alpha")
			if err != nil {
				t.Fatalf("describe agent: %v (stderr=%s)", err, stderr)
			}
			rows := describeRows(t, stdout)
			// The pre-existing rows are untouched.
			for key, want := range map[string]string{"Provider": "codex", "Phase": "Running", "PaneRef": "pan-alpha-codex"} {
				if got := rows[key]; len(got) != 1 || got[0] != want {
					t.Fatalf("row %q = %v, want [%q]\n%s", key, got, want, stdout)
				}
			}
			for key, want := range test.want {
				if got := rows[key]; len(got) != 1 || got[0] != want {
					t.Fatalf("row %q = %v, want [%q]\n%s", key, got, want, stdout)
				}
			}
			for _, key := range test.gone {
				if got := rows[key]; len(got) != 0 {
					t.Fatalf("row %q = %v, want none\n%s", key, got, stdout)
				}
			}
		})
	}
}

// TestGetAgentsSurfacesTheConversationPointer proves the plural read exposes the
// value too, without disturbing the other kinds or the structured modes.
func TestGetAgentsSurfacesTheConversationPointer(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	setFixtureSessionRef(t, store, "agt-alpha-codex", &coremetadata.AgentSessionRef{
		Provider:   "codex",
		ObservedAt: sessionRefObservedAt,
		Codex:      &coremetadata.CodexSessionRef{ThreadID: "codex-thread-1", SessionID: "codex-session-1"},
	})

	stdout, stderr, err := runRoute(t, newTestListGetCommand(t, store), "agents", "--project", "alpha")
	if err != nil {
		t.Fatalf("get agents: %v (stderr=%s)", err, stderr)
	}
	// The conversation pointer is the SESSION column of the columnar read.
	const want = "DISPLAY NAME  NAME   STATUS  INTERACTION  PROJECT  WINDOW  SESSION               TERMINATION  AGE\n" +
		"codex         codex  live    unknown      alpha    main    codex:codex-thread-1               2d\n"
	if stdout != want {
		t.Fatalf("get agents = %q, want %q", stdout, want)
	}

	// An Agent with no observed conversation leaves the cell empty without
	// disturbing the columns around it -- SESSION is an interior column now that
	// AGE follows it, so an empty cell has to hold its width rather than end the
	// line.
	beta, _, err := runRoute(t, newTestListGetCommand(t, store), "agents", "--project", "beta")
	if err != nil {
		t.Fatalf("get agents --project beta: %v", err)
	}
	if beta != "DISPLAY NAME  NAME   STATUS   INTERACTION  PROJECT  WINDOW  SESSION  TERMINATION  AGE\ncodex         codex  offline  unknown      beta     main                          2d\n" {
		t.Fatalf("an Agent with no session ref rendered %q", beta)
	}

	// Other kinds carry no SESSION column at all.
	windows, _, err := runRoute(t, newTestListGetCommand(t, store), "windows", "--project", "beta")
	if err != nil {
		t.Fatalf("get windows: %v", err)
	}
	if windows != "DISPLAY NAME  NAME  STATUS   PROJECT  AGE\nmain          main  offline  beta     2d\n" {
		t.Fatalf("get windows = %q, want the Window column contract", windows)
	}

	// The structured mode carries the whole per-provider union.
	structured, _, err := runRoute(t, newTestListGetCommand(t, store), "agents", "--project", "alpha", "-o", "json")
	if err != nil {
		t.Fatalf("get agents -o json: %v", err)
	}
	for _, needle := range []string{`"sessionRef"`, `"provider": "codex"`, `"threadId": "codex-thread-1"`, `"sessionId": "codex-session-1"`} {
		if !strings.Contains(structured, needle) {
			t.Fatalf("get agents -o json is missing %s:\n%s", needle, structured)
		}
	}
}

// TestAgentResumeConsumesTheStoredSessionRefAndStillRefusesRunning is the
// Phase 1 successor of Phase 0's "resume is unchanged by a stored ref" test.
//
// Phase 0 required the two invocations to be byte-identical, because persisting
// the pointer was not allowed to start consuming it. This Phase is the one that
// consumes it, so the requirement inverts for the resumable half and is kept for
// the Running half:
//
//   - An Offline Agent *diverges* on the ref. Without one it refuses with the
//     missing-ref message and starts nothing; with one it rebinds. The
//     divergence is the feature.
//   - A Running Agent is byte-identical with and without a ref. The phase gate
//     runs before the ref is ever read, so knowing which conversation an Agent
//     belongs to never makes a live Agent rebindable.
func TestAgentResumeConsumesTheStoredSessionRefAndStillRefusesRunning(t *testing.T) {
	t.Parallel()

	ref := func() *coremetadata.AgentSessionRef {
		return &coremetadata.AgentSessionRef{
			Provider:   "codex",
			ObservedAt: sessionRefObservedAt,
			Codex:      &coremetadata.CodexSessionRef{ThreadID: "codex-thread-1", SessionID: "codex-session-1"},
		}
	}

	t.Run("an Offline agent diverges on the stored ref", func(t *testing.T) {
		t.Parallel()

		without := newFakeResourceStore(t)
		cmdWithout, _, _ := newTestAgentCommand(t, without)
		outWithout, _, errWithout := runRoute(t, cmdWithout, "resume", "codex", "--project", "beta")
		if errWithout == nil {
			t.Fatal("an Agent with no conversation resumed anyway")
		}
		if !strings.Contains(errWithout.Error(), "has no provider session ref") {
			t.Fatalf("error = %q, want the missing-ref refusal", errWithout)
		}
		if outWithout != "" || without.transactions != 0 {
			t.Fatalf("stdout = %q, transactions = %d, want 0 bytes and 0", outWithout, without.transactions)
		}

		with := newFakeResourceStore(t)
		setFixtureSessionRef(t, with, "agt-beta-codex", ref())
		tmux := newFakeTmux()
		cmdWith, launcher, _, _ := newTestAgentResumeCommand(t, with, tmux)
		outWith, _, errWith := runRoute(t, cmdWith, "resume", "codex", "--project", "beta")
		if errWith != nil {
			t.Fatalf("an Agent with a stored conversation failed to resume: %v", errWith)
		}
		if outWith != "agent/codex resumed\n" {
			t.Fatalf("stdout = %q, want the resumed result line", outWith)
		}
		agent, _ := with.registry.Agent("agt-beta-codex")
		if agent.Status.Phase != coremetadata.PhaseRunning {
			t.Fatalf("phase = %q, want Running", agent.Status.Phase)
		}
		// The stored pointer is what the launch addressed, and it survives the
		// rebind unchanged.
		if len(launcher.plans) != 1 || launcher.plans[0].conversationID != "codex-thread-1" {
			t.Fatalf("resume launches = %+v, want one addressing codex-thread-1", launcher.plans)
		}
		if !agent.Status.SessionRef.SameConversation(ref()) {
			t.Fatalf("resume rewrote the stored conversation: %#v", agent.Status.SessionRef)
		}
	})

	t.Run("a Running agent is refused identically with and without a ref", func(t *testing.T) {
		t.Parallel()

		without := newFakeResourceStore(t)
		cmdWithout, aiWithout, usageWithout := newTestAgentCommand(t, without)
		outWithout, errStreamWithout, runErrWithout := runRoute(t, cmdWithout, "resume", "codex", "--project", "alpha")

		with := newFakeResourceStore(t)
		setFixtureSessionRef(t, with, "agt-alpha-codex", ref())
		cmdWith, aiWith, usageWith := newTestAgentCommand(t, with)
		outWith, errStreamWith, runErrWith := runRoute(t, cmdWith, "resume", "codex", "--project", "alpha")

		if outWithout != outWith || errStreamWithout != errStreamWith {
			t.Fatalf("streams diverged:\nwithout stdout=%q stderr=%q\nwith stdout=%q stderr=%q",
				outWithout, errStreamWithout, outWith, errStreamWith)
		}
		if runErrWithout == nil || runErrWith == nil {
			t.Fatalf("a Running Agent resumed: without=%v with=%v", runErrWithout, runErrWith)
		}
		if runErrWithout.Error() != runErrWith.Error() {
			t.Fatalf("error text diverged:\nwithout=%q\nwith=%q", runErrWithout, runErrWith)
		}
		if !IsUsageError(runErrWith) {
			t.Fatalf("the Running refusal is no longer a usage error: %v", runErrWith)
		}
		for name, recorder := range map[string]*recordingArgv{
			"ai without": aiWithout, "usage without": usageWithout,
			"ai with": aiWith, "usage with": usageWith,
		} {
			if len(recorder.calls) != 0 {
				t.Fatalf("%s handler was reached: %#v", name, recorder.calls)
			}
		}
		if agent, ok := with.registry.Agent("agt-alpha-codex"); !ok || !agent.Status.SessionRef.SameConversation(ref()) {
			t.Fatal("the refused resume disturbed the stored session ref")
		}
		if with.transactions != 0 {
			t.Fatalf("a refused resume opened %d registry transactions, want 0", with.transactions)
		}
	})
}
