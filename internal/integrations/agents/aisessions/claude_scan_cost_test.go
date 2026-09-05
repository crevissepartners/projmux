package aisessions

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// claudeFixtureSpec declares one deterministic Claude project directory: how
// many transcripts record the search cwd, how many record a different one, and
// which cwds those are. Nothing here is sampled, timed, or derived from the
// host: file count, mtime order, and cwd match are all named by the caller.
type claudeFixtureSpec struct {
	matching    int
	nonMatching int
	cwd         string
	otherCWD    string
}

// claudeFixture is the built directory plus the exact file set the scan is
// allowed to see, newest mtime first.
type claudeFixture struct {
	projectsDir string
	cwd         string
	files       []string
	matching    []string
}

// buildClaudeFixture writes spec.matching+spec.nonMatching transcripts into the
// encoded project directory for spec.cwd. Every file gets its own mtime, one
// second apart and descending in write order, so LastModified ordering is
// total and reproducible without reading a clock.
func buildClaudeFixture(t *testing.T, spec claudeFixtureSpec) claudeFixture {
	t.Helper()
	projectsDir := filepath.Join(t.TempDir(), "claude", "projects")
	encoded := EncodeClaudeProjectPath(spec.cwd)
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

	fixture := claudeFixture{projectsDir: projectsDir, cwd: spec.cwd}
	write := func(index int, cwd string, matching bool) {
		id := fmt.Sprintf("11111111-2222-4333-8444-%012d", index)
		path := writeClaudeSession(t, projectsDir, encoded, fmt.Sprintf("session-%04d.jsonl", index),
			id, cwd, "main", fmt.Sprintf("Session %04d", index))
		setModTime(t, path, base.Add(-time.Duration(index)*time.Second))
		fixture.files = append(fixture.files, path)
		if matching {
			fixture.matching = append(fixture.matching, path)
		}
	}
	index := 0
	for range spec.matching {
		write(index, spec.cwd, true)
		index++
	}
	for range spec.nonMatching {
		write(index, spec.otherCWD, false)
		index++
	}
	return fixture
}

// observeClaudeScannedFiles installs the test-only scan observer and returns a
// snapshot accessor. The observer is removed when the test ends.
func observeClaudeScannedFiles(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var opened []string
	observer := func(path string) {
		mu.Lock()
		opened = append(opened, path)
		mu.Unlock()
	}
	claudeScanObserver.Store(&observer)
	t.Cleanup(func() { claudeScanObserver.Store(nil) })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), opened...)
	}
}

// TestClaudeDiscoveryOpensEveryTranscriptRegardlessOfLimit pins the defect, it
// does not justify it.
//
// Contract violated: C-2 ("cost is proportional to what is displayed"). The
// Claude lane opens every transcript in the project directory and only then
// applies the cap, so its cost is O(total history) instead of O(limit). The
// final sort key is LastModified, which comes from the dirent (entry.Info()),
// so the rows that survive the cap are knowable before a single file is
// opened - every file opened beyond the limit is work that is thrown away.
//
// Phase 1 (`fix(resume): bound the claude resume scan to the displayed limit`)
// inverts these assertions: after mtime pre-sorting and a matching-bounded
// scan, the opened count must be derived from the limit, not from M.
func TestClaudeDiscoveryOpensEveryTranscriptRegardlessOfLimit(t *testing.T) {
	const (
		files = 120
		limit = 10
	)
	fixture := buildClaudeFixture(t, claudeFixtureSpec{
		matching: files,
		cwd:      "/workspace/app",
		otherCWD: "/workspace/app-other",
	})
	opts := DiscoverOptions{ClaudeProjectsDir: fixture.projectsDir, DeferTurns: true}

	// The interactive provider lane receives the limit and still opens M files.
	opened := observeClaudeScannedFiles(t)
	discovery, err := DiscoverProviderContext(context.Background(), AgentClaude, fixture.cwd, opts, limit)
	if err != nil {
		t.Fatalf("DiscoverProviderContext error = %v", err)
	}
	if got := len(opened()); got != files {
		t.Fatalf("claude discovery opened %d files, want %d (every transcript in the directory)", got, files)
	}
	if got := len(discovery.Sessions); got != limit {
		t.Fatalf("claude discovery published %d rows, want %d", got, limit)
	}

	// The picker's own lane is worse: summary.go hands the claude arm a limit
	// of 0, so the provider-local cap is off as well and all M rows travel to
	// the controller before settleAIResumeSummaries caps them.
	opened = observeClaudeScannedFiles(t)
	summaries, err := DiscoverResumeSummariesContext(context.Background(), AgentClaude, fixture.cwd,
		ResumeSummaryOptions{DiscoverOptions: opts}, limit)
	if err != nil {
		t.Fatalf("DiscoverResumeSummariesContext error = %v", err)
	}
	if got := len(opened()); got != files {
		t.Fatalf("claude summary discovery opened %d files, want %d", got, files)
	}
	if got := len(summaries.Summaries); got != files {
		t.Fatalf("claude summary discovery returned %d rows, want %d (the limit is not applied on this lane)", got, files)
	}
}

// TestClaudeDiscoveryOpensNonMatchingTranscripts pins the second half of the
// same C-2 violation: files whose recorded cwd can never match are opened and
// parsed before they are discarded. A limit-bounded scan has to keep counting
// matches rather than files, which is why this count is stated separately.
//
// Phase 1 inverts this too: the opened count must stay bounded even when
// non-matching files sit at the top of the mtime order.
func TestClaudeDiscoveryOpensNonMatchingTranscripts(t *testing.T) {
	fixture := buildClaudeFixture(t, claudeFixtureSpec{
		matching:    5,
		nonMatching: 40,
		cwd:         "/workspace/app",
		otherCWD:    "/workspace/app-other",
	})
	opened := observeClaudeScannedFiles(t)
	discovery, err := DiscoverProviderContext(context.Background(), AgentClaude, fixture.cwd,
		DiscoverOptions{ClaudeProjectsDir: fixture.projectsDir, DeferTurns: true}, 5)
	if err != nil {
		t.Fatalf("DiscoverProviderContext error = %v", err)
	}
	if got, want := len(opened()), len(fixture.files); got != want {
		t.Fatalf("claude discovery opened %d files, want %d (matching and non-matching alike)", got, want)
	}
	if got := len(discovery.Sessions); got != len(fixture.matching) {
		t.Fatalf("claude discovery published %d rows, want %d", got, len(fixture.matching))
	}
}
