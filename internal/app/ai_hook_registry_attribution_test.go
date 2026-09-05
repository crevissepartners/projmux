package app

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// sharedHostCommand is the exact shape of the process this step exists for: a
// provider host several Panes share. It inherited no activation envelope, so
// nothing was handed to `--pane`, and no tmux environment, so the Pane
// inventory the established ladder reads is unreachable.
func sharedHostCommand(t *testing.T, registry coremetadata.Registry) *aiCommand {
	t.Helper()
	cmd := testAICommand(t.TempDir())
	cmd.lookupEnv = func(string) string { return "" }
	cmd.readCommand = func(context.Context, string, ...string) ([]byte, error) { return nil, os.ErrNotExist }
	cmd.loadRegistry = func() (coremetadata.Registry, error) { return registry, nil }
	cmd.updateRegistry = func(func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
		t.Fatal("resolving a conversation wrote to the registry; this step is a lookup only")
		return coremetadata.Registry{}, nil
	}
	return cmd
}

// conversationRegistry builds one intact Agent->Pane binding carrying both
// conversation records the Registry keeps: the activation refinement pinned to
// this materialization and the Agent's durable session pointer.
func conversationRegistry(paneUID, runtimeID string, activation *coremetadata.CodexActivationBinding, session *coremetadata.AgentSessionRef) coremetadata.Registry {
	agentUID := "agent-for-" + paneUID
	return coremetadata.Registry{
		Agents: []coremetadata.Agent{{
			Metadata: coremetadata.ObjectMeta{UID: agentUID, Name: "conversation-agent"},
			Status:   coremetadata.AgentStatus{PaneRef: paneUID, SessionRef: session},
		}},
		Panes: []coremetadata.Pane{{
			Metadata: coremetadata.ObjectMeta{UID: paneUID, Name: "conversation-pane"},
			Status: coremetadata.PaneStatus{Activation: coremetadata.PaneActivation{
				RuntimeID: runtimeID,
				AgentUID:  agentUID,
				Codex:     activation,
			}},
		}},
	}
}

// Acceptance 1: the hook a shared provider host fired reaches its own Pane. The
// conversation identifier the payload carries is the only thing it has, and the
// Registry already records which Pane owns that conversation.
func TestMatchAIPaneAttributesASharedHostHookThroughTheRegistry(t *testing.T) {
	t.Parallel()

	registry := conversationRegistry("pane-native", "%78",
		&coremetadata.CodexActivationBinding{ThreadID: "thread-native"}, nil)

	for _, tc := range []struct {
		name string
		in   aiPaneMatchInput
	}{
		{name: "thread", in: aiPaneMatchInput{ThreadID: "thread-native"}},
		{name: "session", in: aiPaneMatchInput{SessionID: "thread-native"}},
		{
			name: "cwd that matches nothing",
			in:   aiPaneMatchInput{CWD: "/repo/projmux", ThreadID: "thread-native"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cmd := sharedHostCommand(t, registry)
			paneID, reason := cmd.matchAIPane(tc.in)
			if paneID != "%78" || reason != "" {
				t.Fatalf("matchAIPane() = %q, reason %q, want %%78", paneID, reason)
			}
		})
	}
}

// Acceptance 2: a conversation no Pane holds fails by name. The neighbouring
// Pane below shares the working directory and is live, which is exactly the
// Pane a guess would land on.
func TestMatchAIPaneRefusesAConversationNoPaneHolds(t *testing.T) {
	t.Parallel()

	registry := conversationRegistry("pane-native", "%78",
		&coremetadata.CodexActivationBinding{ThreadID: "thread-native"}, nil)
	cmd := sharedHostCommand(t, registry)

	paneID, reason := cmd.matchAIPane(aiPaneMatchInput{
		CWD:      "/repo/projmux",
		ThreadID: "thread-somebody-else",
	})
	if paneID != "" {
		t.Fatalf("matchAIPane() attributed an unregistered conversation to %q", paneID)
	}
	if reason != aiPaneMatchReasonConversationUnknown {
		t.Fatalf("reason = %q, want %q", reason, aiPaneMatchReasonConversationUnknown)
	}
}

// Two Panes recording the same conversation is a Registry the matcher cannot
// disambiguate. Picking either one is a coin flip that silently lands half its
// events on the wrong Pane, so both are refused.
func TestMatchAIPaneRefusesAConversationTwoPanesHold(t *testing.T) {
	t.Parallel()

	shared := conversationRegistry("pane-one", "%1",
		&coremetadata.CodexActivationBinding{ThreadID: "thread-shared"}, nil)
	second := conversationRegistry("pane-two", "%2",
		&coremetadata.CodexActivationBinding{ThreadID: "thread-shared"}, nil)
	shared.Agents = append(shared.Agents, second.Agents...)
	shared.Panes = append(shared.Panes, second.Panes...)

	cmd := sharedHostCommand(t, shared)
	paneID, reason := cmd.matchAIPane(aiPaneMatchInput{ThreadID: "thread-shared"})
	if paneID != "" {
		t.Fatalf("matchAIPane() picked %q out of an ambiguous conversation", paneID)
	}
	if reason != aiPaneMatchReasonConversationShared {
		t.Fatalf("reason = %q, want %q", reason, aiPaneMatchReasonConversationShared)
	}
}

// The lookup demands the same round trip the explicit path demands. A Pane that
// records a conversation but is no longer materialized, or whose Agent no
// longer points back at it, is torn state and never an attribution target.
func TestRegistryAttributionRequiresAnIntactBinding(t *testing.T) {
	t.Parallel()

	activation := &coremetadata.CodexActivationBinding{ThreadID: "thread-native"}

	noRuntime := conversationRegistry("pane-native", "", activation, nil)
	torn := conversationRegistry("pane-native", "%78", activation, nil)
	torn.Agents[0].Status.PaneRef = "pane-somewhere-else"
	orphan := conversationRegistry("pane-native", "%78", activation, nil)
	orphan.Agents = nil

	for _, tc := range []struct {
		name     string
		registry coremetadata.Registry
	}{
		{name: "pane is not materialized", registry: noRuntime},
		{name: "agent no longer points back", registry: torn},
		{name: "owning agent is gone", registry: orphan},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cmd := sharedHostCommand(t, tc.registry)
			paneID, reason := cmd.matchAIPane(aiPaneMatchInput{ThreadID: "thread-native"})
			if paneID != "" {
				t.Fatalf("matchAIPane() attributed a torn binding to %q", paneID)
			}
			if reason != aiPaneMatchReasonConversationUnknown {
				t.Fatalf("reason = %q, want %q", reason, aiPaneMatchReasonConversationUnknown)
			}
		})
	}
}

// The step reads the resource model, not a provider name. Every provider whose
// conversation the Registry records resolves through the same code, and a
// provider that records none simply contributes nothing.
func TestRegistryAttributionIsProviderNeutral(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		session *coremetadata.AgentSessionRef
		in      aiPaneMatchInput
	}{
		{
			name:    "claude",
			session: &coremetadata.AgentSessionRef{Provider: "claude", Claude: &coremetadata.ClaudeSessionRef{SessionID: "session-claude"}},
			in:      aiPaneMatchInput{SessionID: "session-claude"},
		},
		{
			name:    "codex",
			session: &coremetadata.AgentSessionRef{Provider: "codex", Codex: &coremetadata.CodexSessionRef{ThreadID: "thread-codex"}},
			in:      aiPaneMatchInput{ThreadID: "thread-codex"},
		},
		{
			name:    "antigravity",
			session: &coremetadata.AgentSessionRef{Provider: "antigravity", Antigravity: &coremetadata.AntigravitySessionRef{ConversationID: "conversation-antigravity"}},
			in:      aiPaneMatchInput{ThreadID: "conversation-antigravity"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cmd := sharedHostCommand(t, conversationRegistry("pane-native", "%78", nil, tc.session))
			paneID, reason := cmd.matchAIPane(tc.in)
			if paneID != "%78" || reason != "" {
				t.Fatalf("matchAIPane() = %q, reason %q, want %%78", paneID, reason)
			}
		})
	}
}

// The order is the contract. The Registry step is the last resort, so anything
// the established path can answer still answers it and this change cannot move
// an event that already had a home.
func TestMatchAIPaneKeepsTheRegistryStepBehindTheEstablishedLadder(t *testing.T) {
	t.Parallel()

	registry := conversationRegistry("pane-native", "%registry",
		&coremetadata.CodexActivationBinding{ThreadID: "thread-native"}, nil)
	in := aiPaneMatchInput{CWD: "/repo/projmux", ThreadID: "thread-native"}

	withLadder := func(t *testing.T, env string) *aiCommand {
		t.Helper()
		cmd := sharedHostCommand(t, registry)
		cmd.lookupEnv = func(name string) string {
			if name == "TMUX_PANE" {
				return env
			}
			return ""
		}
		cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name == "tmux" && len(args) > 0 && args[0] == "list-panes" {
				return []byte("%cwd\x1f/repo/projmux\x1fthread-native\x1f\n"), nil
			}
			return nil, os.ErrNotExist
		}
		return cmd
	}

	explicit := withLadder(t, "%inherited")
	explicit.loadRegistry = func() (coremetadata.Registry, error) {
		return hookIdentityRegistry("pane-handed", "%handed"), nil
	}
	handed := in
	handed.ExplicitPane = "pane-handed"
	if paneID, _ := explicit.matchAIPane(handed); paneID != "%handed" {
		t.Fatalf("handed identity = %q, want %%handed", paneID)
	}
	if paneID, _ := withLadder(t, "%inherited").matchAIPane(in); paneID != "%inherited" {
		t.Fatalf("inherited environment = %q, want %%inherited", paneID)
	}
	if paneID, _ := withLadder(t, "").matchAIPane(in); paneID != "%cwd" {
		t.Fatalf("pane inventory = %q, want %%cwd", paneID)
	}
	if paneID, _ := sharedHostCommand(t, registry).matchAIPane(in); paneID != "%registry" {
		t.Fatalf("registry step = %q, want %%registry", paneID)
	}
}

// A conversation the payload never carried leaves the established ladder's own
// answer in place. The Registry cannot be asked a question with no subject, and
// inventing one would relabel two failures that mean different things.
func TestMatchAIPaneLeavesTheLadderReasonWhenThereIsNoConversation(t *testing.T) {
	t.Parallel()

	registry := conversationRegistry("pane-native", "%78",
		&coremetadata.CodexActivationBinding{ThreadID: "thread-native"}, nil)

	unreachable := sharedHostCommand(t, registry)
	if _, reason := unreachable.matchAIPane(aiPaneMatchInput{CWD: "/repo/projmux"}); reason != aiPaneMatchReasonNoInventory {
		t.Fatalf("reason = %q, want %q", reason, aiPaneMatchReasonNoInventory)
	}

	readable := sharedHostCommand(t, registry)
	readable.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && len(args) > 0 && args[0] == "list-panes" {
			return []byte("%other\x1f/repo/other\x1f\x1f\n"), nil
		}
		return nil, os.ErrNotExist
	}
	if _, reason := readable.matchAIPane(aiPaneMatchInput{CWD: "/repo/projmux"}); reason != aiPaneMatchReasonNoMatch {
		t.Fatalf("reason = %q, want %q", reason, aiPaneMatchReasonNoMatch)
	}
}

// The registry itself being unreachable is its own answer. It is not the same
// failure as a conversation nobody claims, and an operator reading the log has
// to be able to tell them apart.
func TestRegistryAttributionReportsAnUnreachableRegistry(t *testing.T) {
	t.Parallel()

	cmd := testAICommand(t.TempDir())
	cmd.lookupEnv = func(string) string { return "" }
	cmd.readCommand = func(context.Context, string, ...string) ([]byte, error) { return nil, os.ErrNotExist }
	cmd.loadRegistry = func() (coremetadata.Registry, error) { return coremetadata.Registry{}, os.ErrPermission }

	paneID, reason := cmd.matchAIPane(aiPaneMatchInput{ThreadID: "thread-native"})
	if paneID != "" {
		t.Fatalf("matchAIPane() = %q, want no attribution", paneID)
	}
	if reason != aiPaneMatchReasonRegistryUnavailable {
		t.Fatalf("reason = %q, want %q", reason, aiPaneMatchReasonRegistryUnavailable)
	}
}

// Negative audit: this step defines no binding. It reads the Registry, writes
// nothing, and touches no conversation authority — that authority is owned
// elsewhere and stays there.
func TestRegistryAttributionDefinesNoBinding(t *testing.T) {
	t.Parallel()

	registry := conversationRegistry("pane-native", "%78",
		&coremetadata.CodexActivationBinding{ThreadID: "thread-native"}, nil)
	loads := 0
	cmd := sharedHostCommand(t, registry)
	cmd.loadRegistry = func() (coremetadata.Registry, error) {
		loads++
		return registry, nil
	}
	cmd.runCommand = func(_ context.Context, name string, args ...string) error {
		t.Fatalf("the lookup issued a command: %s %s", name, strings.Join(args, " "))
		return nil
	}

	if paneID, _ := cmd.matchAIPane(aiPaneMatchInput{ThreadID: "thread-native"}); paneID != "%78" {
		t.Fatalf("matchAIPane() = %q, want %%78", paneID)
	}
	if loads != 1 {
		t.Fatalf("registry reads = %d, want exactly one", loads)
	}
	if len(registry.Panes) != 1 || registry.Panes[0].Status.Activation.Codex.ThreadID != "thread-native" ||
		registry.Agents[0].Status.PaneRef != "pane-native" {
		t.Fatal("the lookup mutated the registry snapshot it was handed")
	}
}

// The whole route, not just the matcher: a codex hook payload arriving from a
// shared provider host with an empty `--pane` lands on its own Pane and says so
// in the record an operator reads.
func TestCodexHookIngestAttributesASharedHostEventThroughTheRegistry(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	registry := conversationRegistry("pane-native", "%78",
		&coremetadata.CodexActivationBinding{ThreadID: "thread-native"}, nil)
	cmd := sharedHostCommand(t, registry)
	cmd.homeDir = func() (string, error) { return home, nil }
	cmd.stdin = strings.NewReader(`{"hook_event_name":"PreToolUse","thread_id":"thread-native","turn_id":"turn-1","cwd":"/repo/projmux"}`)

	if err := cmd.runIngest([]string{"codex-hook", "--pane="}, io.Discard, io.Discard); err != nil {
		t.Fatalf("runIngest() error = %v", err)
	}

	entry := lastAIIngestLogEntry(t, cmd)
	if entry.Pane != "%78" {
		t.Fatalf("record pane = %q, want %%78 (record: %+v)", entry.Pane, entry)
	}
	if entry.Result == "ignored" || entry.Reason == aiPaneMatchReasonNoInventory {
		t.Fatalf("the event was still dropped: %+v", entry)
	}

	// The same route, a conversation nothing holds: no Pane, and the reason
	// names the step that could not answer.
	unclaimedHome := t.TempDir()
	unclaimed := sharedHostCommand(t, registry)
	unclaimed.homeDir = func() (string, error) { return unclaimedHome, nil }
	unclaimed.stdin = strings.NewReader(`{"hook_event_name":"PreToolUse","thread_id":"thread-nobody-holds","cwd":"/repo/projmux"}`)
	if err := unclaimed.runIngest([]string{"codex-hook", "--pane="}, io.Discard, io.Discard); err != nil {
		t.Fatalf("runIngest() error = %v", err)
	}
	entry = lastAIIngestLogEntry(t, unclaimed)
	if entry.Pane != "" || entry.Result != "ignored" || entry.Reason != aiPaneMatchReasonConversationUnknown {
		t.Fatalf("unclaimed conversation record = %+v", entry)
	}
}

func lastAIIngestLogEntry(t *testing.T, cmd *aiCommand) aiIngestLogEntry {
	t.Helper()
	path, err := cmd.aiIngestLogPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := nonEmptyLines(string(data))
	if len(lines) == 0 {
		t.Fatal("the route wrote no record at all")
	}
	var entry aiIngestLogEntry
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &entry); err != nil {
		t.Fatal(err)
	}
	return entry
}
