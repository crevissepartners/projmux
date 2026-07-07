// Package projectidentity resolves the display project name for a tmux window
// from a fixed set of identity signals, independent of any tmux I/O.
//
// The resolver is pure: callers (app-side thin adapters) read the anchor,
// session name, and pane cwd from tmux and pass them in; the filesystem reads
// the worktree/marker resolution needs are injected via FS so they can be
// stubbed in tests. No git subprocess is ever spawned and a corrupt `.git`
// never panics.
//
// It is the single source of truth for the priority that used to live inline in
// the recent-windows snapshot chain and the notify sidebar de-slug, so every
// surface that draws a project badge resolves the same way.
package projectidentity

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Source identifies which input yielded the resolved project name. The zero
// value is None so an unresolved Result{} reports None.
type Source string

const (
	// None means no input yielded a name (empty Result).
	None Source = ""
	// Anchor is the session anchor path basename (@projmux_project_path).
	Anchor Source = "anchor"
	// WorktreeMain is the main-repo basename resolved from a worktree gitlink.
	WorktreeMain Source = "worktree-main"
	// SessionName is the tmux session name, returned verbatim (not de-slugged).
	SessionName Source = "session-name"
	// CWD is the pane cwd project-marker basename (last resort).
	CWD Source = "cwd"
)

// Inputs are the raw identity signals captured for a window, in either a
// recorded (recent-windows snapshot) or live (app adapter reads tmux) context.
// Any field may be empty; Resolve falls through the priority accordingly.
type Inputs struct {
	// AnchorPath is the session anchor project root (@projmux_project_path).
	AnchorPath string
	// PaneCWD is the pane's current working directory.
	PaneCWD string
	// SessionName is the tmux session name (a `<parent>-<base>` slug).
	SessionName string
}

// Result is the resolved project name and the source it came from.
type Result struct {
	Name   string
	Source Source
}

// FS abstracts the filesystem reads the resolver performs so tests can inject a
// fake. OSFS backs it with the real operating system.
type FS interface {
	Stat(name string) (fs.FileInfo, error)
	ReadFile(name string) ([]byte, error)
}

type osFS struct{}

func (osFS) Stat(name string) (fs.FileInfo, error) { return os.Stat(name) }
func (osFS) ReadFile(name string) ([]byte, error)  { return os.ReadFile(name) }

// OSFS is the default FS backed by the real operating system.
var OSFS FS = osFS{}

// Resolve returns the display project name in fixed, drift-safe priority:
//
//  1. Session anchor basename (@projmux_project_path) — fixed at session
//     creation (roadmap #488) so it survives any pane cwd drift (#489).
//  2. Worktree main-repo resolution — when the pane cwd sits inside a git
//     worktree, its MAIN repo is unambiguously the session's project (a worktree
//     of X always belongs to X), so it is resolved even without an anchor
//     (#491). This is a resolved project identity, not the cwd's own basename.
//  3. Session name (verbatim) — an anchor-less (pre-#488) session's pane cwd may
//     have drifted into an entirely different sibling repo, so a plain
//     regular-repo cwd basename is NOT trusted; the session name always reflects
//     the window's real session (#493). The name is returned as-is (not
//     de-slugged) to preserve the recent-windows badge; callers wanting a
//     de-slugged label apply DeSlug.
//  4. cwd project-marker basename — true last resort, reached only when there is
//     no session name at all.
//
// A nil FS defaults to OSFS. No git subprocess is spawned and a corrupt `.git`
// never panics.
func Resolve(in Inputs, f FS) Result {
	if f == nil {
		f = OSFS
	}
	if name := anchorProjectName(in.AnchorPath); name != "" {
		return Result{Name: name, Source: Anchor}
	}
	if name := worktreeProjectName(in.PaneCWD, f); name != "" {
		return Result{Name: name, Source: WorktreeMain}
	}
	if s := strings.TrimSpace(in.SessionName); s != "" {
		return Result{Name: s, Source: SessionName}
	}
	if name := projectName(in.PaneCWD, f); name != "" {
		return Result{Name: name, Source: CWD}
	}
	return Result{Source: None}
}

// DeSlug reduces a `<parent>-<base>` session slug (e.g. "repos-projmux") to its
// base segment ("projmux") by cutting at the first "-". A session name without a
// usable base segment is returned trimmed and unchanged. This mirrors the notify
// sidebar's historical de-slug exactly; it is deliberately lossy for bases that
// themselves contain "-" (a Phase 2/3 concern), and is kept out of the Resolve
// priority chain so recent-windows badges stay verbatim.
func DeSlug(session string) string {
	session = strings.TrimSpace(session)
	if session == "" {
		return ""
	}
	if _, after, ok := strings.Cut(session, "-"); ok && strings.TrimSpace(after) != "" {
		return strings.TrimSpace(after)
	}
	return session
}

// anchorProjectName returns the basename of the session anchor path. The anchor
// is already the project root, so (unlike projectName) it does not walk for a
// project marker — its last segment is the project basename.
func anchorProjectName(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	name := filepath.Base(filepath.Clean(path))
	if name == "." || name == string(filepath.Separator) {
		return ""
	}
	return name
}

// worktreeProjectName resolves the MAIN repo project basename when the pane cwd
// sits inside (or under) a git worktree, else "". It mirrors projectName's
// marker walk to find the enclosing project root, then reuses
// worktreeMainProjectName's pure `.git`-file parse (no git subprocess) to turn a
// worktree gitlink into its main-repo basename. A regular repo (.git dir), a
// missing marker, or a non-worktree `.git` file yields "" so the caller prefers
// the session identity over an untrusted regular-repo cwd basename.
func worktreeProjectName(path string, f FS) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	root := nearestMarker(path, f, os.TempDir())
	if root == "" {
		root = path
	}
	return worktreeMainProjectName(root, f)
}

// projectName resolves the enclosing project root of path and returns its
// basename, resolving a worktree gitlink to its main-repo basename when present.
func projectName(path string, f FS) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	root := nearestMarker(path, f, os.TempDir())
	if root == "" {
		root = path
	}
	// A git worktree carries its own `.git` marker, so nearestMarker stops at the
	// worktree dir whose basename is the branch/worktree name, not the project.
	// When <root>/.git is a FILE (the worktree case) resolve the main repo root by
	// parsing it — a pure file read, no git subprocess — and use the main project
	// basename. A regular repo (.git dir), a missing .git, or a corrupt/unexpected
	// .git file falls through to the marker-basename behavior and never panics.
	if main := worktreeMainProjectName(root, f); main != "" {
		return main
	}
	name := filepath.Base(filepath.Clean(root))
	if name == "." || name == string(filepath.Separator) {
		return ""
	}
	return name
}

// worktreeMainProjectName returns the main repository's project basename when
// root is a git worktree, else "". A worktree's <root>/.git is a file whose
// content is `gitdir: <main>/.git/worktrees/<name>`; the main repo root is the
// path segment before `/.git/worktrees/`. Anything else — a `.git` directory
// (regular repo), a missing/unreadable file, or content without the expected
// `gitdir:` worktree pointer — yields "" so the caller keeps its prior behavior.
// No git subprocess is spawned; only the marker file is read.
func worktreeMainProjectName(root string, f FS) string {
	gitPath := filepath.Join(root, ".git")
	info, err := f.Stat(gitPath)
	if err != nil || info.IsDir() {
		return ""
	}
	data, err := f.ReadFile(gitPath)
	if err != nil {
		return ""
	}
	gitdir := ""
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "gitdir:"); ok {
			gitdir = strings.TrimSpace(rest)
			break
		}
	}
	if gitdir == "" {
		return ""
	}
	gitdir = filepath.ToSlash(gitdir)
	idx := strings.Index(gitdir, "/.git/worktrees/")
	if idx <= 0 {
		return ""
	}
	mainRoot := filepath.Clean(filepath.FromSlash(gitdir[:idx]))
	name := filepath.Base(mainRoot)
	if name == "." || name == string(filepath.Separator) {
		return ""
	}
	return name
}

// nearestMarker walks parent directories looking for a `.projmux` or `.git`
// marker. Boundary paths are not considered candidates. Returns "" when the walk
// reaches a boundary or the filesystem root with nothing found.
func nearestMarker(path string, f FS, boundaries ...string) string {
	path = filepath.Clean(path)
	for {
		for _, boundary := range boundaries {
			boundary = filepath.Clean(strings.TrimSpace(boundary))
			if boundary != "" && boundary != "." && path == boundary {
				return ""
			}
		}
		if markerExists(f, filepath.Join(path, ".projmux")) || markerExists(f, filepath.Join(path, ".git")) {
			return path
		}
		parent := filepath.Dir(path)
		if parent == path {
			return ""
		}
		path = parent
	}
}

func markerExists(f FS, path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	_, err := f.Stat(path)
	return err == nil
}
