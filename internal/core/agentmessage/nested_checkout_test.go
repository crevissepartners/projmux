package agentmessage

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// skipNestedCheckout reports whether a source walk from root should skip the
// directory at path.
//
// A checkout keeps its linked worktrees under a gitignored .wt/, and each one
// is another branch's source tree, not this checkout's. Each has a .git entry
// at its top (a file for a linked worktree, a directory for a clone), so any
// directory below root that carries one is skipped, as is .git itself. The
// walk root is exempt: it is the checkout being audited. internal/app keeps
// its own copy, since test helpers cannot cross packages.
func skipNestedCheckout(root, path string, entry fs.DirEntry) bool {
	if !entry.IsDir() {
		return false
	}
	if entry.Name() == ".git" {
		return true
	}
	if path == root {
		return false
	}
	_, err := os.Lstat(filepath.Join(path, ".git"))
	return err == nil
}

func TestSkipNestedCheckout(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, dir := range []string{"plain", "worktree", "clone/.git"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{".git", filepath.Join("worktree", ".git")} {
		if err := os.WriteFile(filepath.Join(root, file), []byte("gitdir: /elsewhere\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		path string
		want bool
	}{
		{root, false},
		{filepath.Join(root, "plain"), false},
		{filepath.Join(root, "worktree"), true},
		{filepath.Join(root, "clone"), true},
		{filepath.Join(root, "clone", ".git"), true},
	}
	for _, tc := range cases {
		info, err := os.Lstat(tc.path)
		if err != nil {
			t.Fatal(err)
		}
		if got := skipNestedCheckout(root, tc.path, fs.FileInfoToDirEntry(info)); got != tc.want {
			t.Errorf("skipNestedCheckout(%s) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
