package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func agentWith(provider string, ref *coremetadata.AgentSessionRef) coremetadata.Agent {
	var agent coremetadata.Agent
	agent.Spec.Provider = provider
	agent.Status.SessionRef = ref
	return agent
}

func TestPathClaudeAndAntigravity(t *testing.T) {
	claude := agentWith("claude", &coremetadata.AgentSessionRef{
		Provider: "claude",
		Claude:   &coremetadata.ClaudeSessionRef{SessionID: "s", TranscriptPath: "/x/claude.jsonl"},
	})
	if got, err := Path(claude, ""); err != nil || got != "/x/claude.jsonl" {
		t.Fatalf("claude = %q %v", got, err)
	}
	antigravity := agentWith("Antigravity", &coremetadata.AgentSessionRef{
		Provider:    "antigravity",
		Antigravity: &coremetadata.AntigravitySessionRef{ConversationID: "c", TranscriptPath: "/x/ag.jsonl"},
	})
	if got, err := Path(antigravity, ""); err != nil || got != "/x/ag.jsonl" {
		t.Fatalf("antigravity = %q %v", got, err)
	}
	// The discriminator is the fallback when spec is empty.
	legacy := agentWith("", &coremetadata.AgentSessionRef{
		Provider: "claude",
		Claude:   &coremetadata.ClaudeSessionRef{TranscriptPath: "/x/legacy.jsonl"},
	})
	if got, err := Path(legacy, ""); err != nil || got != "/x/legacy.jsonl" {
		t.Fatalf("legacy = %q %v", got, err)
	}
}

func TestPathMissingReferences(t *testing.T) {
	cases := map[string]coremetadata.Agent{
		"claude nil ref":         agentWith("claude", nil),
		"claude nil member":      agentWith("claude", &coremetadata.AgentSessionRef{Provider: "claude"}),
		"claude empty path":      agentWith("claude", &coremetadata.AgentSessionRef{Claude: &coremetadata.ClaudeSessionRef{SessionID: "s"}}),
		"antigravity nil member": agentWith("antigravity", &coremetadata.AgentSessionRef{}),
		"codex nil ref":          agentWith("codex", nil),
		"codex nil member":       agentWith("codex", &coremetadata.AgentSessionRef{}),
		"codex empty ids":        agentWith("codex", &coremetadata.AgentSessionRef{Codex: &coremetadata.CodexSessionRef{}}),
		"no provider":            agentWith("", nil),
		"unknown provider":       agentWith("gemini", nil),
	}
	for name, agent := range cases {
		if got, err := Path(agent, t.TempDir()); err == nil {
			t.Errorf("%s: got %q, want error", name, got)
		}
	}
}

func TestPathCodexFindsNewestRollout(t *testing.T) {
	home := t.TempDir()
	const id = "0199aaaa-bbbb-cccc-dddd-eeeeffff0000"
	older := filepath.Join(home, ".codex", "sessions", "2026", "01", "01", "rollout-2026-01-01T00-00-00-"+id+".jsonl")
	newer := filepath.Join(home, ".codex", "sessions", "2026", "01", "02", "rollout-2026-01-02T00-00-00-"+id+".jsonl")
	other := filepath.Join(home, ".codex", "sessions", "2026", "01", "03", "rollout-2026-01-03T00-00-00-other.jsonl")
	for _, path := range []string{older, newer, other} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	bySession := agentWith("codex", &coremetadata.AgentSessionRef{
		Codex: &coremetadata.CodexSessionRef{SessionID: id, ThreadID: "unrelated"},
	})
	if got, err := Path(bySession, home); err != nil || got != newer {
		t.Fatalf("session id = %q %v", got, err)
	}
	// Codex reports the same id in both slots; a thread id alone is enough.
	byThread := agentWith("codex", &coremetadata.AgentSessionRef{
		Codex: &coremetadata.CodexSessionRef{ThreadID: id},
	})
	if got, err := Path(byThread, home); err != nil || got != newer {
		t.Fatalf("thread id = %q %v", got, err)
	}

	missing := agentWith("codex", &coremetadata.AgentSessionRef{Codex: &coremetadata.CodexSessionRef{SessionID: "nope"}})
	if _, err := Path(missing, home); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("missing rollout err = %v", err)
	}
	if _, err := Path(bySession, ""); err == nil {
		t.Fatal("empty home must fail")
	}
	traversal := agentWith("codex", &coremetadata.AgentSessionRef{Codex: &coremetadata.CodexSessionRef{SessionID: "../" + id}})
	if _, err := Path(traversal, home); err == nil {
		t.Fatal("a separator in the id must be refused")
	}
}
