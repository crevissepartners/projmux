package aisessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestDiscoverAntigravityLastConversationUsesValidatedDBBoundary(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	workspace := filepath.Join(root, "workspace", "app")
	cacheDir := filepath.Join(root, "cache")
	dbDir := filepath.Join(root, "conversations")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	id := "11111111-2222-4333-8444-555555555555"
	dbPath := filepath.Join(dbDir, id+".db")
	writeFile(t, dbPath, "synthetic opaque db placeholder")
	dbTime := time.Date(2026, 8, 12, 3, 4, 5, 0, time.UTC)
	setModTime(t, dbPath, dbTime)
	writeJSONFile(t, filepath.Join(cacheDir, "last_conversations.json"), map[string]any{
		workspace: id,
	})
	// A newer legacy row for the same UUID must not overwrite the stronger
	// DB-validated last-conversation source.
	historyPath := filepath.Join(root, "history.jsonl")
	writeFile(t, historyPath, `{"conversationId":"`+id+`","workspace":"`+workspace+`","timestamp":1999999999999,"display":"legacy title"}`+"\n")

	got, err := Discover(workspace, DiscoverOptions{
		AntigravityCacheDir:         cacheDir,
		AntigravityConversationsDir: dbDir,
		AntigravityHistoryPath:      historyPath,
	})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Discover() len = %d, want 1: %#v", len(got), got)
	}
	assertSession(t, got[0], SessionMeta{
		Agent:        AgentAntigravity,
		ResumeID:     id,
		Title:        shortResumeID(id),
		LastModified: dbTime,
		Context:      SessionContext{CWD: workspace},
		Source:       SourceAntigravityLastConversation,
	})
	if got[0].Turns != 0 {
		t.Fatalf("Turns = %d, want blank/unknown", got[0].Turns)
	}
}

func TestDiscoverAntigravityMetadataWorkspaceVariantsAndSafeTitle(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	workspace := filepath.Join(root, "workspace", "app")
	child := filepath.Join(workspace, "child dir")
	cacheDir := filepath.Join(root, "cache")
	dbDir := filepath.Join(root, "conversations")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ids := []string{
		"11111111-2222-4333-8444-555555555561",
		"11111111-2222-4333-8444-555555555562",
		"11111111-2222-4333-8444-555555555563",
	}
	for i, id := range ids {
		path := filepath.Join(dbDir, id+".db")
		writeFile(t, path, "opaque")
		setModTime(t, path, time.Date(2026, 8, 12, 4, i, 0, 0, time.UTC))
	}
	writeJSONFile(t, filepath.Join(cacheDir, "conversation_metadata.json"), map[string]any{
		"conversations": map[string]any{
			ids[0]: map[string]any{"workspaceUri": pathToFileURI(workspace), "summary": "  Safe synthetic summary  ", "unknown": true},
			ids[1]: map[string]any{"WorkspaceURIs": []any{"https://invalid.example/work", pathToFileURI(child)}, "summary": "<command-name>/goal"},
			ids[2]: map[string]any{"summary": "must not imply a workspace"},
		},
		"future_field": map[string]any{"ignored": true},
	})

	got, err := Discover(workspace, DiscoverOptions{
		AntigravityCacheDir:         cacheDir,
		AntigravityConversationsDir: dbDir,
		AntigravityHistoryPath:      filepath.Join(root, "missing-history.jsonl"),
		Depth:                       1,
	})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Discover() len = %d, want 2 workspace-bearing rows: %#v", len(got), got)
	}
	if got[0].ResumeID != ids[1] || got[0].Title != shortResumeID(ids[1]) || got[0].Context.CWD != child {
		t.Fatalf("workspace-list row = %#v, want child cwd and short UUID title", got[0])
	}
	if got[1].ResumeID != ids[0] || got[1].Title != "Safe synthetic summary" || got[1].Context.CWD != workspace {
		t.Fatalf("workspace URI row = %#v, want safe summary/exact cwd", got[1])
	}
	for _, session := range got {
		if session.Source != SourceAntigravityMetadata || session.Turns != 0 {
			t.Fatalf("metadata row source/turns = %q/%d", session.Source, session.Turns)
		}
	}
}

func TestDiscoverAntigravityCurrentStorageCleanDegradesMalformedAndStaleRows(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	workspace := filepath.Join(root, "workspace", "app")
	cacheDir := filepath.Join(root, "cache")
	dbDir := filepath.Join(root, "conversations")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	validID := "11111111-2222-4333-8444-555555555571"
	invalidID := "../../escape"
	missingID := "11111111-2222-4333-8444-555555555572"
	writeFile(t, filepath.Join(dbDir, validID+".db-wal"), "sidecar only")
	writeFile(t, filepath.Join(dbDir, validID+".db-shm"), "sidecar only")
	writeJSONFile(t, filepath.Join(cacheDir, "last_conversations.json"), map[string]any{
		workspace:                         validID,
		filepath.Join(workspace, "child"): 42,
	})
	writeJSONFile(t, filepath.Join(cacheDir, "conversation_metadata.json"), map[string]any{
		"conversations": map[string]any{
			invalidID: map[string]any{"workspace": workspace, "summary": "invalid id"},
			missingID: map[string]any{"workspace": workspace, "summary": "missing db"},
		},
	})

	got, err := Discover(workspace, DiscoverOptions{
		AntigravityCacheDir:         cacheDir,
		AntigravityConversationsDir: dbDir,
		AntigravityHistoryPath:      filepath.Join(root, "missing-history.jsonl"),
	})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Discover() = %#v, want sidecar/traversal/missing DB rows skipped", got)
	}

	writeFile(t, filepath.Join(cacheDir, "last_conversations.json"), "{ malformed")
	writeFile(t, filepath.Join(cacheDir, "conversation_metadata.json"), `{"conversations":[]}`)
	got, err = Discover(workspace, DiscoverOptions{
		AntigravityCacheDir:         cacheDir,
		AntigravityConversationsDir: dbDir,
		AntigravityHistoryPath:      filepath.Join(root, "missing-history.jsonl"),
	})
	if err != nil || len(got) != 0 {
		t.Fatalf("malformed cache Discover() = %#v, %v; want clean empty degrade", got, err)
	}
}

func TestAntigravityConversationDBRejectsSymlinkAndNonExactNames(t *testing.T) {
	t.Parallel()

	dbDir := t.TempDir()
	id := "11111111-2222-4333-8444-555555555581"
	outside := filepath.Join(t.TempDir(), "outside.db")
	writeFile(t, outside, "opaque")
	mustSymlink(t, outside, filepath.Join(dbDir, id+".db"))
	for _, candidate := range []string{id, id + ".db-wal", id + ".db-shm", "../" + id, "not-a-uuid"} {
		if got, _, ok := antigravityConversationDB(dbDir, candidate); ok {
			t.Fatalf("antigravityConversationDB(%q) = %q, want rejected", candidate, got)
		}
	}
}

func TestAntigravitySourcePriorityDedupe(t *testing.T) {
	t.Parallel()

	id := "11111111-2222-4333-8444-555555555591"
	newer := time.Date(2026, 8, 12, 5, 0, 0, 0, time.UTC)
	older := newer.Add(-time.Hour)
	sessions := dedupeByResumeID([]SessionMeta{
		{Agent: AgentAntigravity, ResumeID: id, Source: SourceAntigravityHistory, LastModified: newer, Title: "history"},
		{Agent: AgentAntigravity, ResumeID: id, Source: SourceAntigravityMetadata, LastModified: newer, Title: "metadata"},
		{Agent: AgentAntigravity, ResumeID: id, Source: SourceAntigravityLastConversation, LastModified: newer, Title: "last"},
		{Agent: AgentAntigravity, ResumeID: id, Source: "hook", LastModified: older, Title: "live"},
	})
	if len(sessions) != 1 || sessions[0].Source != "hook" || sessions[0].Title != "live" {
		t.Fatalf("dedupeByResumeID() = %#v, want live hook despite older timestamp", sessions)
	}
}

func TestAntigravitySourcePriorityDoesNotChangeCrossProviderDedupe(t *testing.T) {
	t.Parallel()

	id := "11111111-2222-4333-8444-555555555592"
	newer := time.Date(2026, 8, 12, 6, 0, 0, 0, time.UTC)
	sessions := dedupeByResumeID([]SessionMeta{
		{Agent: AgentCodex, ResumeID: id, Source: SourceCodexRollout, LastModified: newer, Title: "newer codex"},
		{Agent: AgentAntigravity, ResumeID: id, Source: SourceAntigravityLastConversation, LastModified: newer.Add(-time.Hour), Title: "older cache"},
	})
	if len(sessions) != 1 || sessions[0].Agent != AgentCodex || sessions[0].Title != "newer codex" {
		t.Fatalf("dedupeByResumeID() = %#v, want historical newest cross-provider candidate", sessions)
	}
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(data))
}

func pathToFileURI(path string) string {
	return "file://" + strings.ReplaceAll(path, " ", "%20")
}
