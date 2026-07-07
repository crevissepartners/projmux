package projectidentity

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolvePriority pins the fixed drift-safe priority of the unified
// resolver, absorbing the recent-windows badge cases from roadmap #489/#491/#493
// (formerly recentWindowSnapshotProject): anchor beats a drifted cwd, a worktree
// resolves to its main repo even without an anchor, an anchor-less regular-repo
// cwd is distrusted in favor of the session name, and the cwd marker is only a
// last resort. The session name is returned verbatim (no de-slug); see
// TestDeSlug for the notify-side label reduction.
func TestResolvePriority(t *testing.T) {
	tests := []struct {
		name       string
		in         Inputs
		want       string
		wantSource Source
	}{
		{
			name:       "anchor basename wins over live cwd",
			in:         Inputs{AnchorPath: "/home/es5h/source/repos/projmux", PaneCWD: "/home/es5h/source/repos/projmux/internal/app", SessionName: "repos-projmux"},
			want:       "projmux",
			wantSource: Anchor,
		},
		{
			name:       "anchor stable when cwd drifts into subdir",
			in:         Inputs{AnchorPath: "/home/es5h/source/repos/projmux", PaneCWD: "/home/es5h/source/repos/projmux/internal/core/recentwindows", SessionName: "repos-projmux"},
			want:       "projmux",
			wantSource: Anchor,
		},
		{
			name:       "anchor trailing slash still yields basename",
			in:         Inputs{AnchorPath: "/home/es5h/source/repos/projmux/", PaneCWD: "/tmp/elsewhere", SessionName: "repos-projmux"},
			want:       "projmux",
			wantSource: Anchor,
		},
		{
			// #491 preserved even without an anchor: a worktree's main-repo
			// resolution is unambiguous, so it beats the session identity.
			name:       "no anchor + worktree cwd resolves to main repo (#491)",
			in:         Inputs{PaneCWD: worktreeMarkerDir(t), SessionName: "fix-recent-window-session-badge"},
			want:       "projmux",
			wantSource: WorktreeMain,
		},
		{
			// Anchor-less session whose pane cwd drifted into a *different* sibling
			// repo (its own .git dir). The regular-repo basename is NOT trusted —
			// the session identity must win (#493). Returned verbatim (slug intact).
			name:       "no anchor + regular-repo cwd falls back to session identity",
			in:         Inputs{PaneCWD: projectMarkerDir(t), SessionName: "repos-projmux"},
			want:       "repos-projmux",
			wantSource: SessionName,
		},
		{
			name:       "no anchor and no marker falls back to session",
			in:         Inputs{SessionName: "repos-projmux"},
			want:       "repos-projmux",
			wantSource: SessionName,
		},
		{
			// Session base containing a dash is preserved verbatim by Resolve; only
			// DeSlug reduces it (see TestDeSlug).
			name:       "session slug with dashed base returned verbatim",
			in:         Inputs{SessionName: "repos-my-app"},
			want:       "repos-my-app",
			wantSource: SessionName,
		},
		{
			// No session at all: the cwd marker basename is the last resort.
			name:       "no anchor, no session, regular-repo cwd uses marker basename",
			in:         Inputs{PaneCWD: projectMarkerDir(t)},
			want:       "projmux-fixture",
			wantSource: CWD,
		},
		{
			name:       "empty inputs resolve to none",
			in:         Inputs{},
			want:       "",
			wantSource: None,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Resolve(tt.in, OSFS)
			if got.Name != tt.want {
				t.Fatalf("Resolve(%+v).Name = %q, want %q", tt.in, got.Name, tt.want)
			}
			if got.Source != tt.wantSource {
				t.Fatalf("Resolve(%+v).Source = %q, want %q", tt.in, got.Source, tt.wantSource)
			}
		})
	}
}

// TestResolveNilFSDefaultsToOS ensures a nil FS is safe and behaves like OSFS.
func TestResolveNilFSDefaultsToOS(t *testing.T) {
	if got := Resolve(Inputs{SessionName: "repos-projmux"}, nil); got.Name != "repos-projmux" || got.Source != SessionName {
		t.Fatalf("Resolve with nil FS = %+v, want {repos-projmux session-name}", got)
	}
}

// TestResolveHomeDirectory ensures a home-like cwd with no marker and no session
// does not resolve a spurious project name.
func TestResolveHomeDirectory(t *testing.T) {
	home := t.TempDir() // bare dir, no .git/.projmux marker
	got := Resolve(Inputs{PaneCWD: home}, OSFS)
	// No marker: nearestMarker returns "" so projectName uses the path basename.
	// It is a real last-resort basename but never a session/anchor identity.
	if got.Source != CWD {
		t.Fatalf("Resolve(home).Source = %q, want %q", got.Source, CWD)
	}
	if got.Name != filepath.Base(home) {
		t.Fatalf("Resolve(home).Name = %q, want %q", got.Name, filepath.Base(home))
	}
}

// TestResolveWorktreeMatrix pins the #491 worktree/main-repo resolution and its
// graceful fallbacks (formerly recentWindowProjectName), exercised through the
// resolver's CWD/WorktreeMain paths with no anchor and no session.
func TestResolveWorktreeMatrix(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T) string
		want       string
		wantSource Source
	}{
		{
			name: "worktree .git file resolves to main repo basename",
			setup: func(t *testing.T) string {
				main := filepath.Join(t.TempDir(), "projmux")
				worktree := filepath.Join(main, ".wt", "fix", "recent-window-worktree-project")
				mustMkdirAll(t, worktree)
				gitdir := filepath.Join(main, ".git", "worktrees", "recent-window-worktree-project")
				writeWorktreeGitFile(t, worktree, "gitdir: "+gitdir+"\n")
				return worktree
			},
			want:       "projmux",
			wantSource: WorktreeMain,
		},
		{
			name: "worktree cwd drifted into a subdir still resolves to main repo",
			setup: func(t *testing.T) string {
				main := filepath.Join(t.TempDir(), "projmux")
				worktree := filepath.Join(main, ".wt", "feature", "some-branch")
				sub := filepath.Join(worktree, "internal", "app")
				mustMkdirAll(t, sub)
				gitdir := filepath.Join(main, ".git", "worktrees", "some-branch")
				writeWorktreeGitFile(t, worktree, "gitdir: "+gitdir+"\n")
				return sub
			},
			want:       "projmux",
			wantSource: WorktreeMain,
		},
		{
			name: "regular repo .git dir keeps marker basename",
			setup: func(t *testing.T) string {
				root := filepath.Join(t.TempDir(), "projmux-fixture")
				mustMkdirAll(t, filepath.Join(root, ".git"))
				return filepath.Join(root, "internal", "app")
			},
			want:       "projmux-fixture",
			wantSource: CWD,
		},
		{
			name: "corrupt .git file falls back to worktree basename",
			setup: func(t *testing.T) string {
				worktree := filepath.Join(t.TempDir(), "branch-dir")
				mustMkdirAll(t, worktree)
				writeWorktreeGitFile(t, worktree, "this is not a gitdir pointer\n")
				return worktree
			},
			want:       "branch-dir",
			wantSource: CWD,
		},
		{
			name: "gitdir without worktrees segment falls back to worktree basename",
			setup: func(t *testing.T) string {
				worktree := filepath.Join(t.TempDir(), "linked-dir")
				mustMkdirAll(t, worktree)
				writeWorktreeGitFile(t, worktree, "gitdir: /some/other/place/.git\n")
				return worktree
			},
			want:       "linked-dir",
			wantSource: CWD,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.setup(t)
			got := Resolve(Inputs{PaneCWD: path}, OSFS)
			if got.Name != tt.want {
				t.Fatalf("Resolve(cwd=%q).Name = %q, want %q", path, got.Name, tt.want)
			}
			if got.Source != tt.wantSource {
				t.Fatalf("Resolve(cwd=%q).Source = %q, want %q", path, got.Source, tt.wantSource)
			}
		})
	}
}

// TestResolveCorruptGitNoPanic guards that an unreadable/odd `.git` file never
// panics and degrades to the marker basename.
func TestResolveCorruptGitNoPanic(t *testing.T) {
	worktree := filepath.Join(t.TempDir(), "weird-dir")
	mustMkdirAll(t, worktree)
	// Empty .git file (no gitdir pointer).
	writeWorktreeGitFile(t, worktree, "")
	got := Resolve(Inputs{PaneCWD: worktree}, OSFS)
	if got.Name != "weird-dir" || got.Source != CWD {
		t.Fatalf("Resolve(empty .git) = %+v, want {weird-dir cwd}", got)
	}
}

func TestDeSlug(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"repos-projmux", "projmux"},
		{"projmux", "projmux"},
		{"", ""},
		{"   ", ""},
		{"  repos-projmux  ", "projmux"},
		{"repos-my-app", "my-app"}, // base retains its own dashes
		{"x-", "x-"},               // no usable base segment -> verbatim
		{"-x", "x"},                // empty parent, real base
	}
	for _, tt := range tests {
		if got := DeSlug(tt.in); got != tt.want {
			t.Errorf("DeSlug(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// projectMarkerDir creates a temp project directory named "projmux-fixture" with
// a .git marker so the resolver's marker walk resolves it as the nearest project
// root basename.
func projectMarkerDir(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "projmux-fixture")
	mustMkdirAll(t, filepath.Join(root, ".git"))
	return filepath.Join(root, "internal", "app")
}

// worktreeMarkerDir creates a git worktree layout (main repo "projmux" with a
// worktree whose `.git` is a gitlink file) and returns a subdir inside the
// worktree, so the resolver resolves it to the main repo basename "projmux"
// (#491), independent of any session anchor.
func worktreeMarkerDir(t *testing.T) string {
	t.Helper()
	main := filepath.Join(t.TempDir(), "projmux")
	worktree := filepath.Join(main, ".wt", "fix", "recent-window-session-badge")
	sub := filepath.Join(worktree, "internal", "app")
	mustMkdirAll(t, sub)
	gitdir := filepath.Join(main, ".git", "worktrees", "recent-window-session-badge")
	writeWorktreeGitFile(t, worktree, "gitdir: "+gitdir+"\n")
	return sub
}

// writeWorktreeGitFile writes a `.git` FILE (not directory) inside root with the
// given content, mirroring how git materializes a worktree's gitlink.
func writeWorktreeGitFile(t *testing.T, root, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte(content), 0o644); err != nil {
		t.Fatalf("write worktree .git file: %v", err)
	}
}

func mustMkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %q: %v", dir, err)
	}
}
