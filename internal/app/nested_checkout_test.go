package app

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
// walk root is exempt: it is the checkout being audited.
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

// TestDeclaredTestNamesIgnoreNestedCheckouts pins the walk the contract gate
// counts live tests with: names declared only in a nested checkout or inside
// VCS data are not live, while the root and its packages are walked even
// though the root carries a .git of its own.
func TestDeclaredTestNamesIgnoreNestedCheckouts(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	files := map[string]string{
		"go.mod":                  "module example.com/fixture\n",
		".git":                    "gitdir: /elsewhere/root\n",
		"root_test.go":            "package fixture\n\nfunc TestRootOnly(t *testing.T) {}\n",
		"pkg/pkg_test.go":         "package pkg\n\nfunc TestInPackage(t *testing.T) {}\n",
		"wt/other/.git":           "gitdir: /elsewhere\n",
		"wt/other/nested_test.go": "package other\n\nfunc TestNestedOnly(t *testing.T) {}\n",
		"sub/.git/x_test.go":      "package x\n\nfunc TestInsideGitDir(t *testing.T) {}\n",
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	declared, err := declaredTestNames(root)
	if err != nil {
		t.Fatalf("declaredTestNames: %v", err)
	}
	for _, name := range []string{"TestRootOnly", "TestInPackage"} {
		if !declared[name] {
			t.Errorf("%s missing from %v", name, declared)
		}
	}
	for _, name := range []string{"TestNestedOnly", "TestInsideGitDir"} {
		if declared[name] {
			t.Errorf("%s counted as live, but it is declared only in a nested checkout or VCS data", name)
		}
	}
}
