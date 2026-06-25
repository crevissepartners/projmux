package aisessions

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDiscoverMergesClaudeAndCodexSessionsForCWD(t *testing.T) {
	t.Parallel()

	root := copyFixture(t, "discover")
	claudePath := filepath.Join(root, "claude", "projects", "-workspace-app", "claude-session.jsonl")
	codexPath := filepath.Join(root, "codex", "sessions", "2026", "06", "25", "rollout-codex-session.jsonl")
	otherCodexPath := filepath.Join(root, "codex", "sessions", "2026", "06", "24", "rollout-other-cwd.jsonl")
	setModTime(t, claudePath, time.Date(2026, 6, 25, 8, 0, 0, 0, time.UTC))
	setModTime(t, codexPath, time.Date(2026, 6, 25, 9, 0, 0, 0, time.UTC))
	setModTime(t, otherCodexPath, time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC))

	got, err := Discover("/workspace/app", DiscoverOptions{
		ClaudeProjectsDir: filepath.Join(root, "claude", "projects"),
		CodexSessionsDir:  filepath.Join(root, "codex", "sessions"),
	})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("Discover() len = %d, want 2: %#v", len(got), got)
	}
	assertSession(t, got[0], SessionMeta{
		Agent:        AgentCodex,
		ResumeID:     "019f0000-0000-7000-8000-000000000001",
		Title:        "Add resume picker",
		LastModified: time.Date(2026, 6, 25, 9, 0, 0, 0, time.UTC),
		Context:      SessionContext{CWD: "/workspace/app", Branch: "feat/picker"},
		Source:       SourceCodexRollout,
	})
	assertSession(t, got[1], SessionMeta{
		Agent:        AgentClaude,
		ResumeID:     "11111111-2222-4333-8444-555555555555",
		Title:        "Investigate session discovery",
		LastModified: time.Date(2026, 6, 25, 8, 0, 0, 0, time.UTC),
		Context:      SessionContext{CWD: "/workspace/app", Branch: "feat/discovery"},
		Source:       SourceClaudeTranscript,
	})
}

func TestDiscoverReturnsEmptyForMissingOrInvalidSessions(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	got, err := Discover("/workspace/app", DiscoverOptions{
		ClaudeProjectsDir: filepath.Join(root, "missing-claude"),
		CodexSessionsDir:  filepath.Join(root, "missing-codex"),
	})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Discover() len = %d, want 0", len(got))
	}
}

func TestEncodeClaudeProjectPath(t *testing.T) {
	t.Parallel()

	got := EncodeClaudeProjectPath("/workspace/app")
	if got != "-workspace-app" {
		t.Fatalf("EncodeClaudeProjectPath() = %q, want %q", got, "-workspace-app")
	}
}

func copyFixture(t *testing.T, name string) string {
	t.Helper()

	dst := t.TempDir()
	src := filepath.Join("testdata", name)
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatalf("copy fixture %s: %v", name, err)
	}
	return dst
}

func setModTime(t *testing.T, path string, at time.Time) {
	t.Helper()

	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

func assertSession(t *testing.T, got, want SessionMeta) {
	t.Helper()

	if got.Agent != want.Agent ||
		got.ResumeID != want.ResumeID ||
		got.Title != want.Title ||
		got.Context != want.Context ||
		got.Source != want.Source ||
		!got.LastModified.Equal(want.LastModified) {
		t.Fatalf("session = %#v, want %#v", got, want)
	}
}
