package aisessions

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

func TestDiscoverDedupesResumeIDKeepingNewest(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionsDir := filepath.Join(root, "codex", "sessions", "2026", "06", "25")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	olderPath := filepath.Join(sessionsDir, "rollout-older.jsonl")
	newerPath := filepath.Join(sessionsDir, "rollout-newer.jsonl")
	sharedID := "019f0000-0000-7000-8000-000000000099"
	writeFile(t, olderPath, `{"type":"session_meta","payload":{"id":"`+sharedID+`","cwd":"/workspace/app","git_branch":"feat/older"}}
{"type":"event_msg","payload":{"message":"Older duplicate"}}
`)
	writeFile(t, newerPath, `{"type":"session_meta","payload":{"id":"`+sharedID+`","cwd":"/workspace/app","git_branch":"feat/newer"}}
{"type":"event_msg","payload":{"message":"Newer duplicate"}}
`)
	setModTime(t, olderPath, time.Date(2026, 6, 25, 8, 0, 0, 0, time.UTC))
	setModTime(t, newerPath, time.Date(2026, 6, 25, 9, 0, 0, 0, time.UTC))

	got, err := Discover("/workspace/app", DiscoverOptions{
		CodexSessionsDir: filepath.Join(root, "codex", "sessions"),
	})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Discover() len = %d, want 1: %#v", len(got), got)
	}
	assertSession(t, got[0], SessionMeta{
		Agent:        AgentCodex,
		ResumeID:     sharedID,
		Title:        "Newer duplicate",
		LastModified: time.Date(2026, 6, 25, 9, 0, 0, 0, time.UTC),
		Context:      SessionContext{CWD: "/workspace/app", Branch: "feat/newer"},
		Source:       SourceCodexRollout,
	})
}

func TestDiscoverCodexScansNewestFilesFirstByModTime(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionsDir := filepath.Join(root, "codex", "sessions", "2026", "06", "25")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	olderPath := filepath.Join(sessionsDir, "rollout-000-older.jsonl")
	newerPath := filepath.Join(sessionsDir, "rollout-999-newer.jsonl")
	writeFile(t, olderPath, `{"type":"session_meta","payload":{"id":"019f0000-0000-7000-8000-000000000201","cwd":"/workspace/app","git_branch":"feat/older"}}
{"type":"event_msg","payload":{"message":"Older session"}}
`)
	writeFile(t, newerPath, `{"type":"session_meta","payload":{"id":"019f0000-0000-7000-8000-000000000202","cwd":"/workspace/app","git_branch":"feat/newer"}}
{"type":"event_msg","payload":{"message":"Newer session"}}
`)
	setModTime(t, olderPath, time.Date(2026, 6, 25, 8, 0, 0, 0, time.UTC))
	setModTime(t, newerPath, time.Date(2026, 6, 25, 9, 0, 0, 0, time.UTC))

	got := discoverCodex("/workspace/app", filepath.Join(root, "codex", "sessions"), 0)
	if len(got) != 2 {
		t.Fatalf("discoverCodex() len = %d, want 2: %#v", len(got), got)
	}
	if got[0].Title != "Newer session" || got[1].Title != "Older session" {
		t.Fatalf("discoverCodex() titles = [%q, %q], want newest first", got[0].Title, got[1].Title)
	}
}

func TestDiscoverCodexLimitsScanToMostRecentFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionsDir := filepath.Join(root, "codex", "sessions")
	base := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	for i := 0; i <= codexScanFileLimit; i++ {
		writeNumberedCodexSession(t, sessionsDir, i, base.Add(-time.Duration(i)*time.Minute), "/workspace/app")
	}

	got := discoverCodex("/workspace/app", sessionsDir, 0)
	if len(got) != codexScanFileLimit {
		t.Fatalf("discoverCodex() len = %d, want %d", len(got), codexScanFileLimit)
	}
	oldestID := numberedCodexSessionID(codexScanFileLimit)
	for _, session := range got {
		if session.ResumeID == oldestID {
			t.Fatalf("discoverCodex() included oldest session beyond scan limit: %#v", session)
		}
	}
}

func TestDiscoverCodexLimitedScanPreservesRecentPickerResults(t *testing.T) {
	t.Parallel()

	const pickerVisibleLimit = 30

	root := t.TempDir()
	sessionsDir := filepath.Join(root, "codex", "sessions")
	base := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	for i := range codexScanFileLimit + 40 {
		writeNumberedCodexSession(t, sessionsDir, i, base.Add(-time.Duration(i)*time.Minute), "/workspace/app")
	}

	limited := discoverCodexWithFileLimit("/workspace/app", sessionsDir, codexScanFileLimit, 0)
	unbounded := discoverCodexWithFileLimit("/workspace/app", sessionsDir, 0, 0)
	if len(limited) < pickerVisibleLimit || len(unbounded) < pickerVisibleLimit {
		t.Fatalf("not enough sessions to compare picker rows: limited=%d unbounded=%d", len(limited), len(unbounded))
	}
	for i := range pickerVisibleLimit {
		if limited[i].ResumeID != unbounded[i].ResumeID ||
			limited[i].Title != unbounded[i].Title ||
			!limited[i].LastModified.Equal(unbounded[i].LastModified) {
			t.Fatalf("row %d differs after limit: limited=%#v unbounded=%#v", i, limited[i], unbounded[i])
		}
	}
}

func TestDiscoverSkipsNoisyTitleCandidates(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionsDir := filepath.Join(root, "codex", "sessions", "2026", "06", "25")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessionsDir, "rollout-noisy-title.jsonl")
	writeFile(t, path, `{"type":"session_meta","payload":{"id":"019f0000-0000-7000-8000-000000000088","cwd":"/workspace/app","git_branch":"feat/title"}}
{"type":"event_msg","payload":{"message":"# AGENTS.md instructions for /workspace/app"}}
{"type":"event_msg","payload":{"message":"<command-name>/goal"}}
{"type":"event_msg","payload":{"message":"Implement resume picker"}}
`)
	setModTime(t, path, time.Date(2026, 6, 25, 9, 0, 0, 0, time.UTC))

	got, err := Discover("/workspace/app", DiscoverOptions{
		CodexSessionsDir: filepath.Join(root, "codex", "sessions"),
	})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Discover() len = %d, want 1: %#v", len(got), got)
	}
	if got[0].Title != "Implement resume picker" {
		t.Fatalf("Title = %q, want cleaned prose title", got[0].Title)
	}
}

func TestScanSessionJSONLStopsAfterCwdMismatch(t *testing.T) {
	t.Parallel()

	firstLine := `{"type":"session_meta","payload":{"id":"019f0000-0000-7000-8000-000000000077","cwd":"/workspace/other","git_branch":"feat/other"}}` + "\n"
	reader := newFailAfterReader(firstLine+`{"type":"event_msg","payload":{"message":"must not be read"}}`+"\n", len(firstLine))

	details, ok := scanSessionJSONLReader(reader, sessionScanOptions{
		targetCWD:  "/workspace/app",
		requireCWD: true,
	})

	if ok {
		t.Fatalf("scanSessionJSONLReader() ok = true, details = %#v", details)
	}
	if reader.failed {
		t.Fatal("scanSessionJSONLReader() read past mismatched cwd metadata")
	}
}

func TestCountUserTurnsExcludesAgentMessages(t *testing.T) {
	t.Parallel()

	// The deferred turn count reads the whole log counting only user turns; the
	// session_meta and agent_message records are not user turns.
	log := `{"type":"session_meta","payload":{"id":"019f0000-0000-7000-8000-000000000066","cwd":"/workspace/app","git_branch":"feat/perf"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"user_message","message":"Speed up resume picker"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"agent_message","message":"On it"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"user_message","message":"Also add turn counts"}}` + "\n"

	turns, ok := countUserTurnsReader(strings.NewReader(log))
	if !ok {
		t.Fatal("countUserTurnsReader() ok = false")
	}
	if turns != 2 {
		t.Fatalf("turns = %d, want 2 (two user_message records, agent_message excluded)", turns)
	}
}

func TestScanSessionJSONLCandidatePassEarlyExitsBeforeCountingTurns(t *testing.T) {
	t.Parallel()

	// Candidate discovery must stop the moment the cheap id/cwd/title/branch
	// fields are captured and must not read the rest of the window; the tail is
	// never touched. This is the #477 early-exit. The turn count is a separate
	// deferred full-file pass (countUserTurns), so it never runs here.
	metaAndFirstTurn := `{"type":"session_meta","payload":{"id":"019f0000-0000-7000-8000-000000000066","cwd":"/workspace/app","git_branch":"feat/perf"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"user_message","message":"Speed up resume picker"}}` + "\n"
	tail := `{"type":"event_msg","payload":{"type":"user_message","message":"must not be read"}}` + "\n"
	reader := newFailAfterReader(metaAndFirstTurn+tail, len(metaAndFirstTurn))

	details, ok := scanSessionJSONLReader(reader, sessionScanOptions{
		targetCWD:  "/workspace/app",
		requireCWD: true,
	})

	if !ok {
		t.Fatal("scanSessionJSONLReader() ok = false")
	}
	if reader.failed {
		t.Fatal("scanSessionJSONLReader() read past the ready candidate fields")
	}
	if details.title != "Speed up resume picker" {
		t.Fatalf("title = %q, want first user prompt", details.title)
	}
}

func TestScanSessionJSONLCountsClaudeUserTurnsExcludingToolResults(t *testing.T) {
	t.Parallel()

	// Claude replays tool output as role "user"; those carrier records must not
	// inflate the turn count. Only real user prompts (string / text content)
	// count.
	log := `{"type":"user","message":{"role":"user","content":"first prompt"},"cwd":"/workspace/app","sessionId":"11111111-2222-4333-8444-555555555555","gitBranch":"main"}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash"}]}}` + "\n" +
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"output"}]}}` + "\n" +
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"second prompt"}]}}` + "\n"

	turns, ok := countUserTurnsReader(strings.NewReader(log))
	if !ok {
		t.Fatal("countUserTurnsReader() ok = false")
	}
	if turns != 2 {
		t.Fatalf("turns = %d, want 2 (tool_result carrier excluded)", turns)
	}
}

func TestDiscoverPopulatesTurnCount(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionsDir := filepath.Join(root, "codex", "sessions", "2026", "06", "25")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessionsDir, "rollout-turns.jsonl")
	writeFile(t, path, `{"type":"session_meta","payload":{"id":"019f0000-0000-7000-8000-000000000099","cwd":"/workspace/app","git_branch":"feat/turns"}}
{"type":"event_msg","payload":{"type":"user_message","message":"First"}}
{"type":"event_msg","payload":{"type":"agent_message","message":"Reply"}}
{"type":"event_msg","payload":{"type":"user_message","message":"Second"}}
{"type":"event_msg","payload":{"type":"user_message","message":"Third"}}
`)
	setModTime(t, path, time.Date(2026, 6, 25, 9, 0, 0, 0, time.UTC))

	got, err := Discover("/workspace/app", DiscoverOptions{
		CodexSessionsDir: filepath.Join(root, "codex", "sessions"),
	})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Discover() len = %d, want 1: %#v", len(got), got)
	}
	if got[0].Turns != 3 {
		t.Fatalf("Turns = %d, want 3", got[0].Turns)
	}
}

func TestCountUserTurnsCountsBeyondCandidateScanLimit(t *testing.T) {
	t.Parallel()

	// The candidate scan stops at sessionScanLineLimit (100). The deferred turn
	// count must not: a long session with user turns well past line 100 must
	// count all of them, otherwise long sessions are badly under-reported — the
	// #490 follow-up bug (real ~505 user turns rendered as ~17).
	var b strings.Builder
	b.WriteString(`{"type":"session_meta","payload":{"id":"019f0000-0000-7000-8000-0000000000aa","cwd":"/workspace/app","git_branch":"feat/long"}}` + "\n")
	const userTurns = 505
	for i := 0; i < userTurns; i++ {
		b.WriteString(`{"type":"event_msg","payload":{"type":"user_message","message":"prompt"}}` + "\n")
		b.WriteString(`{"type":"event_msg","payload":{"type":"agent_message","message":"reply"}}` + "\n")
	}

	turns, ok := countUserTurnsReader(strings.NewReader(b.String()))
	if !ok {
		t.Fatal("countUserTurnsReader() ok = false")
	}
	if turns != userTurns {
		t.Fatalf("turns = %d, want %d (must count past the %d-line candidate limit)", turns, userTurns, sessionScanLineLimit)
	}
}

func TestEnrichTurnsCountsBeyondCandidateScanLimit(t *testing.T) {
	t.Parallel()

	// End-to-end: Discover + enrich on a >100-line log must report the full user
	// turn count, proving enrichTurns wires through the full-file countUserTurns
	// path rather than the bounded candidate window.
	root := t.TempDir()
	sessionsDir := filepath.Join(root, "codex", "sessions", "2026", "06", "25")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessionsDir, "rollout-long.jsonl")

	var b strings.Builder
	b.WriteString(`{"type":"session_meta","payload":{"id":"019f0000-0000-7000-8000-0000000000ab","cwd":"/workspace/app","git_branch":"feat/long"}}` + "\n")
	const userTurns = 250
	for i := 0; i < userTurns; i++ {
		b.WriteString(`{"type":"event_msg","payload":{"type":"user_message","message":"prompt"}}` + "\n")
		b.WriteString(`{"type":"event_msg","payload":{"type":"agent_message","message":"reply"}}` + "\n")
	}
	writeFile(t, path, b.String())
	setModTime(t, path, time.Date(2026, 6, 25, 9, 0, 0, 0, time.UTC))

	got, err := Discover("/workspace/app", DiscoverOptions{
		CodexSessionsDir: filepath.Join(root, "codex", "sessions"),
	})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Discover() len = %d, want 1: %#v", len(got), got)
	}
	if got[0].Turns != userTurns {
		t.Fatalf("Turns = %d, want %d (full-file enrich must count past the %d-line candidate limit)", got[0].Turns, userTurns, sessionScanLineLimit)
	}
}

func TestDiscoverDeferTurnsLeavesCountZeroUntilEnriched(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionsDir := filepath.Join(root, "codex", "sessions", "2026", "06", "25")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessionsDir, "rollout-defer.jsonl")
	writeFile(t, path, `{"type":"session_meta","payload":{"id":"019f0000-0000-7000-8000-000000000099","cwd":"/workspace/app","git_branch":"feat/turns"}}
{"type":"event_msg","payload":{"type":"user_message","message":"First"}}
{"type":"event_msg","payload":{"type":"agent_message","message":"Reply"}}
{"type":"event_msg","payload":{"type":"user_message","message":"Second"}}
{"type":"event_msg","payload":{"type":"user_message","message":"Third"}}
`)
	setModTime(t, path, time.Date(2026, 6, 25, 9, 0, 0, 0, time.UTC))

	opts := DiscoverOptions{
		CodexSessionsDir: filepath.Join(root, "codex", "sessions"),
		DeferTurns:       true,
	}
	got, err := Discover("/workspace/app", opts)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Discover() len = %d, want 1: %#v", len(got), got)
	}
	// Deferred: the identity fields are present but the turn count is not paid.
	if got[0].ResumeID == "" || got[0].Title == "" {
		t.Fatalf("deferred candidate missing identity fields: %#v", got[0])
	}
	if got[0].Turns != 0 {
		t.Fatalf("Turns = %d, want 0 before enrichment (DeferTurns)", got[0].Turns)
	}

	// The background enrich pass fills the same count the blocking path produces.
	enriched := EnrichTurns(got)
	if enriched[0].Turns != 3 {
		t.Fatalf("Turns after EnrichTurns = %d, want 3", enriched[0].Turns)
	}
	if got[0].Turns != 3 {
		t.Fatalf("EnrichTurns did not fill the passed slice: Turns = %d, want 3", got[0].Turns)
	}
}

func TestEnrichTurnsIgnoresSessionsWithoutLog(t *testing.T) {
	t.Parallel()

	// Antigravity rows carry no per-turn log (empty sourcePath); enrichment must
	// leave them at 0 rather than panic or read a bogus path.
	sessions := []SessionMeta{{Agent: AgentAntigravity, ResumeID: "abc", Turns: 0}}
	got := EnrichTurns(sessions)
	if got[0].Turns != 0 {
		t.Fatalf("Turns = %d, want 0 (no per-turn log)", got[0].Turns)
	}
}

func TestDiscoverUsesShortIDWhenTitleIsOutsideBoundedPrefix(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionsDir := filepath.Join(root, "codex", "sessions", "2026", "06", "25")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	id := "019f0000-0000-7000-8000-000000000055"
	var lines strings.Builder
	lines.WriteString(`{"type":"session_meta","payload":{"id":"` + id + `","cwd":"/workspace/app","git_branch":"feat/title-bound"}}` + "\n")
	for i := 1; i < sessionTitleLineLimit; i++ {
		lines.WriteString(`{"type":"event_msg","payload":{"message":"# AGENTS.md instructions for /workspace/app"}}` + "\n")
	}
	lines.WriteString(`{"type":"event_msg","payload":{"message":"Title after bounded prefix"}}` + "\n")
	path := filepath.Join(sessionsDir, "rollout-late-title.jsonl")
	writeFile(t, path, lines.String())

	got, err := Discover("/workspace/app", DiscoverOptions{
		CodexSessionsDir: filepath.Join(root, "codex", "sessions"),
	})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Discover() len = %d, want 1: %#v", len(got), got)
	}
	if got[0].Title != shortResumeID(id) {
		t.Fatalf("Title = %q, want short id fallback %q", got[0].Title, shortResumeID(id))
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

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
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

func writeNumberedCodexSession(t *testing.T, sessionsDir string, index int, modTime time.Time, cwd string) string {
	t.Helper()

	dir := filepath.Join(sessionsDir, "2026", "06", "25")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, fmt.Sprintf("rollout-%03d.jsonl", index))
	id := numberedCodexSessionID(index)
	writeFile(t, path, `{"type":"session_meta","payload":{"id":"`+id+`","cwd":"`+cwd+`","git_branch":"feat/perf"}}
{"type":"event_msg","payload":{"message":"Session `+fmt.Sprintf("%03d", index)+`"}}
`)
	setModTime(t, path, modTime)
	return path
}

func numberedCodexSessionID(index int) string {
	return fmt.Sprintf("019f0000-0000-7000-8000-%012d", index)
}

type failAfterReader struct {
	data   string
	limit  int
	offset int
	failed bool
}

func newFailAfterReader(data string, limit int) *failAfterReader {
	return &failAfterReader{data: data, limit: limit}
}

func (r *failAfterReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	if r.offset >= r.limit {
		r.failed = true
		return 0, errors.New("read past limit")
	}
	p[0] = r.data[r.offset]
	r.offset++
	return 1, nil
}
