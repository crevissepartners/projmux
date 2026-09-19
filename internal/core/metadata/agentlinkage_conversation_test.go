package metadata

import (
	"slices"
	"testing"
)

// conversationHolder is one Agent recording ref, owned by windowUID.
func conversationHolder(uid, windowUID, paneUID string, ref *AgentSessionRef) Agent {
	return Agent{
		APIVersion: APIVersion, Kind: KindAgent,
		Metadata: ObjectMeta{UID: uid, Name: uid, OwnerRef: &OwnerRef{Kind: KindWindow, UID: windowUID}},
		Status:   AgentStatus{PaneRef: paneUID, SessionRef: ref},
	}
}

func agentUIDsOf(agents []Agent) []string {
	out := make([]string, 0, len(agents))
	for _, agent := range agents {
		out = append(out, agent.Metadata.UID)
	}
	return out
}

// TestAgentsRecordingConversationMatchesProviderAndIdAcrossTheWholeRegistry
// pins the lookup a resume-picker create reads: every Agent in any Window or
// Project recording the same provider conversation, Pane owners included, in
// uid order; never an Agent of another provider carrying the same id string,
// nor one of another conversation.
func TestAgentsRecordingConversationMatchesProviderAndIdAcrossTheWholeRegistry(t *testing.T) {
	t.Parallel()
	const id = "7c1d2e3f-0000-4000-8000-000000000001"
	claude := &AgentSessionRef{Provider: "claude", Claude: &ClaudeSessionRef{SessionID: id, TranscriptPath: "/t/a.jsonl"}}
	registry := NewRegistry()
	registry.Agents = []Agent{
		conversationHolder("agt-z-other-project", "win-beta", "", claude),
		conversationHolder("agt-a-live", "win-alpha", "pan-live", &AgentSessionRef{Provider: "claude", Claude: &ClaudeSessionRef{SessionID: id}}),
		conversationHolder("agt-codex-same-id", "win-alpha", "", &AgentSessionRef{Provider: "codex", Codex: &CodexSessionRef{ThreadID: id}}),
		conversationHolder("agt-antigravity-same-id", "win-alpha", "", &AgentSessionRef{Provider: "antigravity", Antigravity: &AntigravitySessionRef{ConversationID: id}}),
		conversationHolder("agt-other-conversation", "win-alpha", "", &AgentSessionRef{Provider: "claude", Claude: &ClaudeSessionRef{SessionID: "other"}}),
		conversationHolder("agt-no-ref", "win-alpha", "", nil),
	}

	for _, test := range []struct {
		name     string
		observed AgentSessionObservation
		want     []string
	}{
		{"claude matches every claude holder", AgentSessionObservation{Provider: "claude", SessionID: id}, []string{"agt-a-live", "agt-z-other-project"}},
		{"claude id is trimmed like a hook observation", AgentSessionObservation{Provider: "claude", SessionID: " " + id + " "}, []string{"agt-a-live", "agt-z-other-project"}},
		{"codex never equates a claude session id", AgentSessionObservation{Provider: "codex", ThreadID: id}, []string{"agt-codex-same-id"}},
		{"antigravity never equates a claude session id", AgentSessionObservation{Provider: "antigravity", ThreadID: id}, []string{"agt-antigravity-same-id"}},
		{"another conversation matches only its holder", AgentSessionObservation{Provider: "claude", SessionID: "other"}, []string{"agt-other-conversation"}},
		{"an unrecorded conversation matches nothing", AgentSessionObservation{Provider: "claude", SessionID: "nobody"}, nil},
		{"an empty conversation matches nothing", AgentSessionObservation{Provider: "claude"}, nil},
		{"an unknown provider matches nothing", AgentSessionObservation{Provider: "gemini", SessionID: id}, nil},
	} {
		if got := agentUIDsOf(registry.AgentsRecordingConversation(test.observed)); !slices.Equal(got, test.want) {
			t.Errorf("%s: holders = %v, want %v", test.name, got, test.want)
		}
	}
}
