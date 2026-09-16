package transcript

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Chat text names things that live in a repository — `internal/x.go:42`, a
// `#1018`, a commit — and none of them is a link on its own. What turns them
// into one is knowing which repository the agent is working in, and only the
// agent's workspace says that. So the server reads it once from git and hands
// the browser just enough to build URLs: the web base, the checkout root that
// absolute paths are relative to, and a revision to pin file links at.
//
// Only GitHub is recognized. Another host's URL shapes differ, and a link that
// guesses them is worse than text.

// Repo is what the browser needs to link repository references.
type Repo struct {
	// Web is the repository's page, e.g. https://github.com/owner/name.
	Web string `json:"web"`
	// Root is the checkout's top level, which absolute paths are cut against.
	Root string `json:"root"`
	// Rev is the commit file links are pinned to. It is HEAD when HEAD has been
	// pushed; otherwise the newest pushed ancestor, because a link to a commit
	// GitHub has never seen is a 404 on every file.
	Rev string `json:"rev"`
}

// githubRemote matches the spellings git accepts for a GitHub origin:
// scp-like `git@github.com:o/r.git`, and ssh:// or https:// URLs, with or
// without a userinfo part.
var githubRemote = regexp.MustCompile(`^(?:git@github\.com:|(?:ssh|https?)://(?:[^@/]+@)?github\.com/)([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(?:\.git)?/?$`)

// GitHubWeb turns an origin URL into the repository's web page, or "" when the
// origin is not GitHub.
func GitHubWeb(remote string) string {
	m := githubRemote.FindStringSubmatch(strings.TrimSpace(remote))
	if m == nil || m[1] == "." || m[1] == ".." || m[2] == "." || m[2] == ".." {
		return ""
	}
	return "https://github.com/" + m[1] + "/" + m[2]
}

// repoTTL bounds how stale a pinned revision can get. HEAD moves with every
// commit the agent makes, but a link pinned a few seconds back still opens.
const repoTTL = 30 * time.Second

type repoEntry struct {
	repo *Repo
	at   time.Time
}

var (
	repoMu    sync.Mutex
	repoCache = map[string]repoEntry{}
)

// RepoFor reports the GitHub repository dir is checked out from, or nil when
// it is not one. Failures are cached like successes: a workspace that is not
// a GitHub checkout stays that way, and each lookup spawns several processes.
func RepoFor(ctx context.Context, dir string) *Repo {
	if dir == "" || !filepath.IsAbs(dir) {
		return nil
	}
	repoMu.Lock()
	entry, ok := repoCache[dir]
	repoMu.Unlock()
	if ok && time.Since(entry.at) < repoTTL {
		return entry.repo
	}

	repo := readRepo(ctx, dir)
	repoMu.Lock()
	repoCache[dir] = repoEntry{repo: repo, at: time.Now()}
	repoMu.Unlock()
	return repo
}

func readRepo(ctx context.Context, dir string) *Repo {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	// The configured URL, not `remote get-url`: that applies insteadOf
	// rewrites, which point a GitHub origin at a mirror or a local path.
	remote, err := git(ctx, dir, "config", "--get", "remote.origin.url")
	if err != nil {
		return nil
	}
	web := GitHubWeb(remote)
	if web == "" {
		return nil
	}
	top, err := git(ctx, dir, "rev-parse", "--show-toplevel", "HEAD")
	if err != nil {
		return nil
	}
	fields := strings.Fields(top)
	if len(fields) != 2 {
		return nil
	}
	return &Repo{Web: web, Root: fields[0], Rev: pushedRev(ctx, dir, fields[1])}
}

// pushedRev is head when origin has it, else the newest ancestor origin has.
func pushedRev(ctx context.Context, dir, head string) string {
	if out, err := git(ctx, dir, "for-each-ref", "--count=1", "--contains", head, "--format=%(refname)", "refs/remotes/origin"); err == nil && out != "" {
		return head
	}
	for _, base := range []string{"refs/remotes/origin/HEAD", "refs/remotes/origin/main", "refs/remotes/origin/master"} {
		if out, err := git(ctx, dir, "merge-base", head, base); err == nil && out != "" {
			return out
		}
	}
	return head
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...) // #nosec G204 -- fixed git binary, argv form, package-constant subcommands.
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", errors.New(strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
