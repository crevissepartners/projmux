package aisessions

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// mustSymlink creates oldname<-newname, skipping the test when the platform or
// filesystem does not support symlinks rather than failing spuriously.
func mustSymlink(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}
}

// discoverWithin runs Discover in a goroutine and fails if it does not return
// within the deadline, turning an unbounded symlink walk into a test failure
// instead of a hang until the go-test timeout.
func discoverWithin(t *testing.T, cwd string, opts DiscoverOptions, deadline time.Duration) []SessionMeta {
	t.Helper()
	type result struct {
		sessions []SessionMeta
		err      error
	}
	done := make(chan result, 1)
	go func() {
		got, err := Discover(cwd, opts)
		done <- result{got, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("Discover() error = %v", r.err)
		}
		return r.sessions
	case <-time.After(deadline):
		t.Fatal("Discover() did not terminate: symlink cycle guard failed")
		return nil
	}
}

// A directory that symlinks back to one of its ancestors is a cycle. A walk
// that follows symlinks without a guard would recurse forever; the pathGuard
// must terminate it and still return the real session once.
func TestDiscoverCodexSymlinkCycleTerminatesBounded(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionsDir := filepath.Join(root, "codex", "sessions")
	claudeDir := filepath.Join(root, "claude", "projects") // isolate from real home
	base := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)

	writeNumberedCodexSession(t, sessionsDir, 1, base, "/workspace/app")
	writeNumberedCodexSession(t, sessionsDir, 2, base.Add(-time.Minute), "/workspace/app/web")

	// .../2026/loop -> sessionsDir (its grandparent): a self-referential cycle.
	mustSymlink(t, sessionsDir, filepath.Join(sessionsDir, "2026", "loop"))

	got := discoverWithin(t, "/workspace/app", DiscoverOptions{
		ClaudeProjectsDir: claudeDir,
		CodexSessionsDir:  sessionsDir,
		Depth:             2,
	}, 10*time.Second)

	if len(got) != 2 {
		t.Fatalf("Discover() len = %d, want 2 (exact + child, no cycle duplication): %#v", len(got), got)
	}
}

// Two symlink paths that alias the same real session directory must yield the
// session once, not once per path.
func TestDiscoverCodexSymlinkAliasDedupes(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionsDir := filepath.Join(root, "codex", "sessions")
	claudeDir := filepath.Join(root, "claude", "projects")
	base := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)

	writeNumberedCodexSession(t, sessionsDir, 1, base, "/workspace/app")

	// A sibling directory that symlinks to the whole real sessions tree, so the
	// single rollout file is reachable through two distinct paths.
	aliasParent := filepath.Join(root, "codex")
	mustSymlink(t, sessionsDir, filepath.Join(aliasParent, "sessions-alias"))
	// Walk from the parent so both "sessions" and "sessions-alias" are seen.
	got := discoverWithin(t, "/workspace/app", DiscoverOptions{
		ClaudeProjectsDir: claudeDir,
		CodexSessionsDir:  aliasParent,
		Depth:             0,
	}, 10*time.Second)

	if len(got) != 1 {
		t.Fatalf("Discover() len = %d, want 1 (aliased session deduped): %#v", len(got), got)
	}
	if got[0].Context.CWD != "/workspace/app" {
		t.Fatalf("Context.CWD = %q, want /workspace/app", got[0].Context.CWD)
	}
}

// A symlinked codex sessions directory (the whole store behind a link) must
// still be discoverable — the follow-symlinks policy in effect.
func TestDiscoverCodexFollowsSymlinkedStore(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	realStore := filepath.Join(root, "real", "sessions")
	claudeDir := filepath.Join(root, "claude", "projects")
	base := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	writeNumberedCodexSession(t, realStore, 1, base, "/workspace/app")

	linkedStore := filepath.Join(root, "linked-sessions")
	mustSymlink(t, realStore, linkedStore)

	got := discoverWithin(t, "/workspace/app", DiscoverOptions{
		ClaudeProjectsDir: claudeDir,
		CodexSessionsDir:  linkedStore,
		Depth:             0,
	}, 10*time.Second)

	if len(got) != 1 {
		t.Fatalf("Discover() len = %d, want 1 (session behind symlinked store): %#v", len(got), got)
	}
}

// Two Claude project-dir names that alias the same real directory (one a
// symlink whose name shares the encoded-cwd prefix) must not surface the
// session twice at depth>0.
func TestDiscoverClaudeSymlinkAliasDedupes(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	projectsDir := filepath.Join(root, "claude", "projects")
	codexDir := filepath.Join(root, "codex", "sessions")

	realPath := writeClaudeSession(t, projectsDir, "-workspace-app", "root.jsonl",
		"11111111-2222-4333-8444-555555550001", "/workspace/app", "feat/root", "Root session")
	setModTime(t, realPath, time.Date(2026, 6, 25, 9, 0, 0, 0, time.UTC))

	// A second project-dir name that resolves to the same real directory. Its
	// name shares the "-workspace-app-" prefix, so the pre-filter accepts it and
	// only the real-path guard prevents a duplicate scan.
	mustSymlink(t, filepath.Join(projectsDir, "-workspace-app"),
		filepath.Join(projectsDir, "-workspace-app-dup"))

	got := discoverWithin(t, "/workspace/app", DiscoverOptions{
		ClaudeProjectsDir: projectsDir,
		CodexSessionsDir:  codexDir,
		Depth:             1,
	}, 10*time.Second)

	if len(got) != 1 {
		t.Fatalf("Discover() len = %d, want 1 (aliased Claude project deduped): %#v", len(got), got)
	}
	if got[0].Title != "Root session" {
		t.Fatalf("Title = %q, want Root session", got[0].Title)
	}
}
