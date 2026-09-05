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

// Phase 1 reverses Phase 0's C-2 defect assertion: typical newest-match
// scans open the limit plus the terminal tie group, independent of old history.
// C-3 permits no fixed cap on ties, nonmatches, or duplicate identities.
func TestClaudeDiscoveryScanCostTracksLimitAndTerminalTie(t *testing.T) {
	const limit = 10
	for _, files := range []int{120, 480} {
		for _, tieExtra := range []int{0, 4} {
			t.Run(fmt.Sprintf("files_%d_tie_extra_%d", files, tieExtra), func(t *testing.T) {
				fixture := buildClaudeFixture(t, claudeFixtureSpec{matching: files, cwd: "/workspace/app"})
				boundary := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC).Add(-(limit - 1) * time.Second)
				for i := limit; i < limit+tieExtra; i++ {
					setModTime(t, fixture.files[i], boundary)
				}
				opts := DiscoverOptions{ClaudeProjectsDir: fixture.projectsDir, DeferTurns: true}
				opened := observeClaudeScannedFiles(t)
				discovery, err := DiscoverProviderContext(context.Background(), AgentClaude, fixture.cwd, opts, limit)
				if err != nil {
					t.Fatal(err)
				}
				if got := len(opened()); got != limit+tieExtra {
					t.Fatalf("opened %d files, want limit + tie extra = %d", got, limit+tieExtra)
				}
				if len(discovery.Sessions) != limit || !discovery.MoreNotLoaded {
					t.Fatalf("interactive rows=%d more=%t, want %d/true", len(discovery.Sessions), discovery.MoreNotLoaded, limit)
				}
				opened = observeClaudeScannedFiles(t)
				summary, err := DiscoverResumeSummariesContext(context.Background(), AgentClaude, fixture.cwd, ResumeSummaryOptions{DiscoverOptions: opts}, limit)
				if err != nil {
					t.Fatal(err)
				}
				if got := len(opened()); got != limit+tieExtra {
					t.Fatalf("summary opened %d files, want %d", got, limit+tieExtra)
				}
				if len(summary.Summaries) != limit+tieExtra || !summary.MoreNotLoaded {
					t.Fatalf("summary rows=%d more=%t, want %d/true", len(summary.Summaries), summary.MoreNotLoaded, limit+tieExtra)
				}
			})
		}
	}
}

// Phase 1 reverses the second Phase 0 assertion: older nonmatching files need
// not be opened once the newest unique matches and their tie group are known.
func TestClaudeDiscoverySkipsOlderNonMatchingTranscripts(t *testing.T) {
	fixture := buildClaudeFixture(t, claudeFixtureSpec{matching: 5, nonMatching: 40, cwd: "/workspace/app", otherCWD: "/workspace/app-other"})
	opened := observeClaudeScannedFiles(t)
	discovery, err := DiscoverProviderContext(context.Background(), AgentClaude, fixture.cwd,
		DiscoverOptions{ClaudeProjectsDir: fixture.projectsDir, DeferTurns: true}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(opened()); got != 5 {
		t.Fatalf("opened %d files, want 5 newest matching files", got)
	}
	if len(discovery.Sessions) != 5 || !discovery.MoreNotLoaded {
		t.Fatalf("rows=%d more=%t, want 5/true", len(discovery.Sessions), discovery.MoreNotLoaded)
	}
}
