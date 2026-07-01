package app

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRecentWindowProjectNameResolvesWorktreeToMainRepo covers the #489
// follow-up: an anchor-less session whose pane cwd sits inside a git worktree
// must resolve the recent-window project badge to the MAIN repo basename
// (projmux), not the worktree/branch directory name. A regular repo and a
// corrupt/unexpected .git file keep the prior nearest-marker-basename behavior.
func TestRecentWindowProjectNameResolvesWorktreeToMainRepo(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T) string
		want  string
	}{
		{
			name: "worktree .git file resolves to main repo basename",
			setup: func(t *testing.T) string {
				main := filepath.Join(t.TempDir(), "projmux")
				worktree := filepath.Join(main, ".wt", "fix", "recent-window-worktree-project")
				if err := os.MkdirAll(worktree, 0o755); err != nil {
					t.Fatalf("create worktree dir: %v", err)
				}
				gitdir := filepath.Join(main, ".git", "worktrees", "recent-window-worktree-project")
				writeWorktreeGitFile(t, worktree, "gitdir: "+gitdir+"\n")
				return worktree
			},
			want: "projmux",
		},
		{
			name: "worktree cwd drifted into a subdir still resolves to main repo",
			setup: func(t *testing.T) string {
				main := filepath.Join(t.TempDir(), "projmux")
				worktree := filepath.Join(main, ".wt", "feature", "some-branch")
				sub := filepath.Join(worktree, "internal", "app")
				if err := os.MkdirAll(sub, 0o755); err != nil {
					t.Fatalf("create worktree subdir: %v", err)
				}
				gitdir := filepath.Join(main, ".git", "worktrees", "some-branch")
				writeWorktreeGitFile(t, worktree, "gitdir: "+gitdir+"\n")
				return sub
			},
			want: "projmux",
		},
		{
			name: "regular repo .git dir keeps marker basename",
			setup: func(t *testing.T) string {
				root := filepath.Join(t.TempDir(), "projmux-fixture")
				if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
					t.Fatalf("create marker: %v", err)
				}
				return filepath.Join(root, "internal", "app")
			},
			want: "projmux-fixture",
		},
		{
			name: "corrupt .git file falls back to worktree basename",
			setup: func(t *testing.T) string {
				worktree := filepath.Join(t.TempDir(), "branch-dir")
				if err := os.MkdirAll(worktree, 0o755); err != nil {
					t.Fatalf("create worktree dir: %v", err)
				}
				writeWorktreeGitFile(t, worktree, "this is not a gitdir pointer\n")
				return worktree
			},
			want: "branch-dir",
		},
		{
			name: "gitdir without worktrees segment falls back to worktree basename",
			setup: func(t *testing.T) string {
				worktree := filepath.Join(t.TempDir(), "linked-dir")
				if err := os.MkdirAll(worktree, 0o755); err != nil {
					t.Fatalf("create worktree dir: %v", err)
				}
				writeWorktreeGitFile(t, worktree, "gitdir: /some/other/place/.git\n")
				return worktree
			},
			want: "linked-dir",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.setup(t)
			if got := recentWindowProjectName(path); got != tt.want {
				t.Fatalf("recentWindowProjectName(%q) = %q, want %q", path, got, tt.want)
			}
		})
	}
}

// writeWorktreeGitFile writes a `.git` FILE (not directory) inside root with the
// given content, mirroring how git materializes a worktree's gitlink.
func writeWorktreeGitFile(t *testing.T, root, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte(content), 0o644); err != nil {
		t.Fatalf("write worktree .git file: %v", err)
	}
}
