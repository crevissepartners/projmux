package transcript

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitHubWeb(t *testing.T) {
	cases := map[string]string{
		"git@github.com:es5h/projmux-web-mvp.git":         "https://github.com/es5h/projmux-web-mvp",
		"https://github.com/crevissepartners/projmux.git": "https://github.com/crevissepartners/projmux",
		"https://github.com/crevissepartners/projmux":     "https://github.com/crevissepartners/projmux",
		"https://x-access-token@github.com/o/r.git":       "https://github.com/o/r",
		"ssh://git@github.com/o/r.git\n":                  "https://github.com/o/r",
		"https://gitlab.g-dev.brictoworks.com/g/r.git":    "",
		"https://github.com.evil.example/o/r.git":         "",
		"https://evil.example/github.com/o/r.git":         "",
		"git@github.com:o/r/extra.git":                    "",
		"https://github.com/../r":                         "",
		"https://github.com/o/r?x=1":                      "",
		"":                                                "",
	}
	for in, want := range cases {
		if got := GitHubWeb(in); got != want {
			t.Errorf("GitHubWeb(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRepoForPinsPushedRevision builds a checkout whose origin is a local
// bare repo with a GitHub-shaped URL rewritten onto it, so the push check runs
// against real refs without a network.
func TestRepoForPinsPushedRevision(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	base := t.TempDir()
	bare := filepath.Join(base, "bare.git")
	work := filepath.Join(base, "work")
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	run(base, "init", "-q", "--bare", "-b", "main", bare)
	run(base, "init", "-q", "-b", "main", work)
	run(work, "remote", "add", "origin", "git@github.com:o/r.git")
	// The URL is GitHub's; the transport goes to the bare repo.
	run(work, "config", "url."+bare+".insteadOf", "git@github.com:o/r.git")
	run(work, "commit", "-q", "--allow-empty", "-m", "one")
	run(work, "push", "-q", "origin", "main")
	pushed := strings.TrimSpace(run(work, "rev-parse", "HEAD"))

	ctx := context.Background()
	repo := readRepo(ctx, work)
	if repo == nil {
		t.Fatal("readRepo = nil for a GitHub checkout")
	}
	if repo.Web != "https://github.com/o/r" || repo.Root != work || repo.Rev != pushed {
		t.Fatalf("pushed HEAD: got %+v", repo)
	}

	// An unpushed commit is not linkable; the pushed parent is.
	run(work, "commit", "-q", "--allow-empty", "-m", "two")
	if repo := readRepo(ctx, work); repo == nil || repo.Rev != pushed {
		t.Fatalf("unpushed HEAD: got %+v, want rev %s", repo, pushed)
	}

	// A subdirectory resolves to the same checkout root.
	sub := filepath.Join(work, "internal")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if repo := readRepo(ctx, sub); repo == nil || repo.Root != work {
		t.Fatalf("subdirectory: got %+v", repo)
	}

	if RepoFor(ctx, "relative/dir") != nil {
		t.Fatal("a relative workspace must not be resolved")
	}
	if RepoFor(ctx, base) != nil {
		t.Fatal("a directory outside any checkout must be nil")
	}
}
