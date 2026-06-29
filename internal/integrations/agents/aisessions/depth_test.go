package aisessions

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func TestWithinTree(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		recorded string
		cwd      string
		depth    int
		want     bool
	}{
		{name: "exact match depth 0", recorded: "/workspace/app", cwd: "/workspace/app", depth: 0, want: true},
		{name: "child excluded at depth 0", recorded: "/workspace/app/web", cwd: "/workspace/app", depth: 0, want: false},
		{name: "child included at depth 1", recorded: "/workspace/app/web", cwd: "/workspace/app", depth: 1, want: true},
		{name: "grandchild excluded at depth 1", recorded: "/workspace/app/web/api", cwd: "/workspace/app", depth: 1, want: false},
		{name: "grandchild included at depth 2", recorded: "/workspace/app/web/api", cwd: "/workspace/app", depth: 2, want: true},
		{name: "parent excluded", recorded: "/workspace", cwd: "/workspace/app", depth: 5, want: false},
		{name: "sibling excluded", recorded: "/workspace/other", cwd: "/workspace/app", depth: 5, want: false},
		{name: "lossy repo-other mismatch excluded", recorded: "/workspace/app-other", cwd: "/workspace/app", depth: 5, want: false},
		{name: "trailing slash normalized", recorded: "/workspace/app/web/", cwd: "/workspace/app/", depth: 1, want: true},
		{name: "dot segments normalized", recorded: "/workspace/app/web/../web/api", cwd: "/workspace/app", depth: 2, want: true},
		{name: "empty recorded", recorded: "", cwd: "/workspace/app", depth: 1, want: false},
		{name: "empty cwd", recorded: "/workspace/app", cwd: "", depth: 1, want: false},
		{name: "negative depth treated as zero child", recorded: "/workspace/app/web", cwd: "/workspace/app", depth: -1, want: false},
		{name: "negative depth exact still matches", recorded: "/workspace/app", cwd: "/workspace/app", depth: -1, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := withinTree(tc.recorded, tc.cwd, tc.depth); got != tc.want {
				t.Fatalf("withinTree(%q, %q, %d) = %v, want %v", tc.recorded, tc.cwd, tc.depth, got, tc.want)
			}
		})
	}
}

func TestCodexScanBudget(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		depth int
		want  int
	}{
		{depth: 0, want: codexScanFileLimit},
		{depth: -2, want: codexScanFileLimit},
		{depth: 1, want: codexScanFileLimit * 2},
		{depth: 2, want: codexScanFileLimit * 3},
		{depth: 4, want: codexScanFileLimit * 5}, // 400, exactly the cap
		{depth: 5, want: codexScanBudgetMax},     // 480 -> capped
		{depth: 8, want: codexScanBudgetMax},
	} {
		if got := codexScanBudget(tc.depth); got != tc.want {
			t.Fatalf("codexScanBudget(%d) = %d, want %d", tc.depth, got, tc.want)
		}
	}
}

// writeClaudeSession writes a minimal Claude transcript with all the fields the
// scanner needs (id, cwd, branch, title) on a single user record.
func writeClaudeSession(t *testing.T, projectsDir, encodedDir, file, id, cwd, branch, title string) string {
	t.Helper()
	dir := filepath.Join(projectsDir, encodedDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, file)
	writeFile(t, path, `{"type":"user","message":{"role":"user","content":"`+title+`"},"cwd":"`+cwd+`","sessionId":"`+id+`","gitBranch":"`+branch+`"}`+"\n")
	return path
}

func TestDiscoverClaudeDepthIncludesChildrenWithinDepth(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	projectsDir := filepath.Join(root, "claude", "projects")
	base := time.Date(2026, 6, 25, 9, 0, 0, 0, time.UTC)

	rootPath := writeClaudeSession(t, projectsDir, "-workspace-app", "root.jsonl",
		"11111111-2222-4333-8444-555555550001", "/workspace/app", "feat/root", "Root session")
	webPath := writeClaudeSession(t, projectsDir, "-workspace-app-web", "web.jsonl",
		"11111111-2222-4333-8444-555555550002", "/workspace/app/web", "feat/web", "Web session")
	apiPath := writeClaudeSession(t, projectsDir, "-workspace-app-web-api", "api.jsonl",
		"11111111-2222-4333-8444-555555550003", "/workspace/app/web/api", "feat/api", "Api session")
	// Lossy encoding collision: "/workspace/app-other" encodes to a dir whose
	// name shares the "-workspace-app-" prefix, so the dir pre-filter accepts it
	// but the recorded cwd must reject it as a sibling.
	siblingPath := writeClaudeSession(t, projectsDir, "-workspace-app-other", "other.jsonl",
		"11111111-2222-4333-8444-555555550004", "/workspace/app-other", "feat/other", "Sibling session")
	for _, p := range []string{rootPath, webPath, apiPath, siblingPath} {
		setModTime(t, p, base)
	}

	collectTitles := func(depth int) []string {
		got, err := Discover("/workspace/app", DiscoverOptions{
			ClaudeProjectsDir: projectsDir,
			Depth:             depth,
		})
		if err != nil {
			t.Fatalf("Discover(depth=%d) error = %v", depth, err)
		}
		titles := make([]string, 0, len(got))
		for _, s := range got {
			titles = append(titles, s.Title)
		}
		sort.Strings(titles)
		return titles
	}

	if got := collectTitles(0); len(got) != 1 || got[0] != "Root session" {
		t.Fatalf("depth 0 titles = %v, want [Root session]", got)
	}
	if got := collectTitles(1); len(got) != 2 || got[0] != "Root session" || got[1] != "Web session" {
		t.Fatalf("depth 1 titles = %v, want [Root session Web session]", got)
	}
	if got := collectTitles(2); len(got) != 3 || got[0] != "Api session" || got[1] != "Root session" || got[2] != "Web session" {
		t.Fatalf("depth 2 titles = %v, want root+web+api (no sibling)", got)
	}
}

func TestDiscoverClaudeDepthRecordsRelativeCWD(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	projectsDir := filepath.Join(root, "claude", "projects")
	webPath := writeClaudeSession(t, projectsDir, "-workspace-app-web", "web.jsonl",
		"11111111-2222-4333-8444-555555550012", "/workspace/app/web", "feat/web", "Web session")
	setModTime(t, webPath, time.Date(2026, 6, 25, 9, 0, 0, 0, time.UTC))

	got, err := Discover("/workspace/app", DiscoverOptions{ClaudeProjectsDir: projectsDir, Depth: 1})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Discover() len = %d, want 1: %#v", len(got), got)
	}
	if got[0].Context.CWD != "/workspace/app/web" {
		t.Fatalf("Context.CWD = %q, want recorded child cwd", got[0].Context.CWD)
	}
}

func TestDiscoverCodexDepthFiltersByTreeAndWidensBudget(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionsDir := filepath.Join(root, "codex", "sessions")
	base := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	// codexScanFileLimit exact-cwd files plus extra ones inside a child cwd that
	// only become reachable once depth widens both the tree filter and budget.
	for i := range codexScanFileLimit {
		writeNumberedCodexSession(t, sessionsDir, i, base.Add(-time.Duration(i)*time.Minute), "/workspace/app")
	}
	for i := codexScanFileLimit; i < codexScanFileLimit+20; i++ {
		writeNumberedCodexSession(t, sessionsDir, i, base.Add(-time.Duration(i)*time.Minute), "/workspace/app/web")
	}
	// A sibling cwd that must never appear regardless of depth. Kept oldest so
	// it never steals a budget slot from the exact-cwd files at depth 0.
	writeNumberedCodexSession(t, sessionsDir, 999, base.Add(-1000*time.Minute), "/workspace/app-other")

	depth0 := discoverCodex("/workspace/app", sessionsDir, 0)
	if len(depth0) != codexScanFileLimit {
		t.Fatalf("depth 0 len = %d, want %d (exact cwd only)", len(depth0), codexScanFileLimit)
	}
	for _, s := range depth0 {
		if s.Context.CWD != "/workspace/app" {
			t.Fatalf("depth 0 session cwd = %q, want exact cwd", s.Context.CWD)
		}
	}

	depth1 := discoverCodex("/workspace/app", sessionsDir, 1)
	if len(depth1) != codexScanFileLimit+20 {
		t.Fatalf("depth 1 len = %d, want %d (exact + child, budget widened)", len(depth1), codexScanFileLimit+20)
	}
	for _, s := range depth1 {
		if s.Context.CWD == "/workspace/app-other" {
			t.Fatalf("depth 1 included sibling cwd: %#v", s)
		}
	}
}
