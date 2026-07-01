package aisessions

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// antigravityHistoryFixture is a history.jsonl with a mix of valid records, a
// malformed line, a blank line, a record for a different cwd, a record whose
// conversationId is not a valid UUID, and two valid records for the target cwd
// (one older, one newer). Only the two target-cwd valid records should surface.
const antigravityHistoryFixture = `{"conversationId":"aaaaaaaa-bbbb-4ccc-8ddd-000000000001","workspace":"/workspace/app","timestamp":1779433568000,"display":"First antigravity session"}
{"conversationId":"aaaaaaaa-bbbb-4ccc-8ddd-000000000002","workspace":"/workspace/app","timestamp":1779433570000,"display":"Second antigravity session"}
{"conversationId":"aaaaaaaa-bbbb-4ccc-8ddd-000000000003","workspace":"/workspace/other","timestamp":1779433571000,"display":"Different cwd session"}
{"conversationId":"not-a-uuid","workspace":"/workspace/app","timestamp":1779433572000,"display":"Invalid id session"}
{"conversationId":"aaaaaaaa-bbbb-4ccc-8ddd-000000000004","workspace":"/workspace/app"  BROKEN JSON
{"conversationId":"aaaaaaaa-bbbb-4ccc-8ddd-000000000005","workspace":"/workspace/app","timestamp":1779433569000,"display":"<command-name>/goal"}
`

func writeAntigravityHistory(t *testing.T, content string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "history.jsonl")
	writeFile(t, path, content)
	return path
}

func TestDiscoverAntigravityFiltersSortsAndSkips(t *testing.T) {
	t.Parallel()

	path := writeAntigravityHistory(t, antigravityHistoryFixture)

	got, err := Discover("/workspace/app", DiscoverOptions{
		AntigravityHistoryPath: path,
	})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	// Three valid target-cwd records survive: id 0002 (newest), 0005 (noisy
	// title falls back to short id), 0001 (oldest). The other-cwd record, the
	// invalid-id record, and the malformed line are all skipped.
	if len(got) != 3 {
		t.Fatalf("Discover() len = %d, want 3: %#v", len(got), got)
	}

	assertSession(t, got[0], SessionMeta{
		Agent:        AgentAntigravity,
		ResumeID:     "aaaaaaaa-bbbb-4ccc-8ddd-000000000002",
		Title:        "Second antigravity session",
		LastModified: time.UnixMilli(1779433570000),
		Context:      SessionContext{CWD: "/workspace/app"},
		Source:       SourceAntigravityHistory,
	})
	// The noisy "<command-name>/goal" display is rejected by the shared title
	// cleaner, so this row falls back to the short resume id.
	assertSession(t, got[1], SessionMeta{
		Agent:        AgentAntigravity,
		ResumeID:     "aaaaaaaa-bbbb-4ccc-8ddd-000000000005",
		Title:        shortResumeID("aaaaaaaa-bbbb-4ccc-8ddd-000000000005"),
		LastModified: time.UnixMilli(1779433569000),
		Context:      SessionContext{CWD: "/workspace/app"},
		Source:       SourceAntigravityHistory,
	})
	assertSession(t, got[2], SessionMeta{
		Agent:        AgentAntigravity,
		ResumeID:     "aaaaaaaa-bbbb-4ccc-8ddd-000000000001",
		Title:        "First antigravity session",
		LastModified: time.UnixMilli(1779433568000),
		Context:      SessionContext{CWD: "/workspace/app"},
		Source:       SourceAntigravityHistory,
	})
	for _, session := range got {
		if session.Turns != 0 {
			t.Fatalf("Turns = %d, want 0 (history.jsonl has no per-turn data)", session.Turns)
		}
	}
}

func TestDiscoverAntigravityMissingFileYieldsNoRows(t *testing.T) {
	t.Parallel()

	got, err := Discover("/workspace/app", DiscoverOptions{
		AntigravityHistoryPath: filepath.Join(t.TempDir(), "missing", "history.jsonl"),
	})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Discover() len = %d, want 0: %#v", len(got), got)
	}
}

func TestDiscoverAntigravityDepthWidensToChildWorkspace(t *testing.T) {
	t.Parallel()

	content := `{"conversationId":"aaaaaaaa-bbbb-4ccc-8ddd-000000000010","workspace":"/workspace/app/child","timestamp":1779433580000,"display":"Child workspace session"}
{"conversationId":"aaaaaaaa-bbbb-4ccc-8ddd-000000000011","workspace":"/workspace/app/child/grandchild","timestamp":1779433581000,"display":"Too deep session"}
`
	path := writeAntigravityHistory(t, content)

	// depth 0: the exact-cwd filter rejects the child workspace.
	exact, err := Discover("/workspace/app", DiscoverOptions{AntigravityHistoryPath: path})
	if err != nil {
		t.Fatalf("Discover(depth=0) error = %v", err)
	}
	if len(exact) != 0 {
		t.Fatalf("Discover(depth=0) len = %d, want 0: %#v", len(exact), exact)
	}

	// depth 1: the direct child matches and records its own workspace; the
	// grandchild is one level too deep and is excluded.
	widened, err := Discover("/workspace/app", DiscoverOptions{AntigravityHistoryPath: path, Depth: 1})
	if err != nil {
		t.Fatalf("Discover(depth=1) error = %v", err)
	}
	if len(widened) != 1 {
		t.Fatalf("Discover(depth=1) len = %d, want 1: %#v", len(widened), widened)
	}
	assertSession(t, widened[0], SessionMeta{
		Agent:        AgentAntigravity,
		ResumeID:     "aaaaaaaa-bbbb-4ccc-8ddd-000000000010",
		Title:        "Child workspace session",
		LastModified: time.UnixMilli(1779433580000),
		Context:      SessionContext{CWD: "/workspace/app/child"},
		Source:       SourceAntigravityHistory,
	})
}

func TestDiscoverMergesAntigravityWithOtherAgents(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	codexDir := filepath.Join(root, "codex", "sessions", "2026", "06", "25")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	codexPath := filepath.Join(codexDir, "rollout-codex.jsonl")
	writeFile(t, codexPath, `{"type":"session_meta","payload":{"id":"019f0000-0000-7000-8000-000000000301","cwd":"/workspace/app","git_branch":"feat/codex"}}
{"type":"event_msg","payload":{"type":"user_message","message":"Codex session"}}
`)
	setModTime(t, codexPath, time.UnixMilli(1779433569000))

	agyPath := writeAntigravityHistory(t, `{"conversationId":"aaaaaaaa-bbbb-4ccc-8ddd-000000000020","workspace":"/workspace/app","timestamp":1779433570000,"display":"Newest antigravity session"}
`)

	got, err := Discover("/workspace/app", DiscoverOptions{
		CodexSessionsDir:       filepath.Join(root, "codex", "sessions"),
		AntigravityHistoryPath: agyPath,
	})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Discover() len = %d, want 2: %#v", len(got), got)
	}
	// The antigravity record is newer, so it sorts first ahead of the codex row.
	if got[0].Agent != AgentAntigravity || got[1].Agent != AgentCodex {
		t.Fatalf("agents = [%q, %q], want [antigravity, codex] newest first", got[0].Agent, got[1].Agent)
	}
}
