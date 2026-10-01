package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The repository maps are hand-written docs, so nothing regenerates them when
// a top-level internal/ directory appears or a listed path goes away. This
// guard lives beside the CLI reference generator, the repository's docs tool,
// rather than in internal/app: it reads only checked-in files and needs none of
// the application to compile.

// repoMapPathPattern matches one backticked path in a repo map table row.
var repoMapPathPattern = regexp.MustCompile("`([^`]+)`")

// repoRoot is the repository the test runs in.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// readRepoFile returns one repository-relative file.
func readRepoFile(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel))) // #nosec G304 -- repository-relative checked-in doc
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

// topLevelInternalDirs returns every directory directly under internal/, as
// "internal/<name>".
func topLevelInternalDirs(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "internal"))
	if err != nil {
		t.Fatalf("read internal/: %v", err)
	}
	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, "internal/"+entry.Name())
		}
	}
	return dirs
}

// repoMapPaths returns the backticked paths in the first column of the
// AGENTS.md "## Repo map" table, with any trailing slash removed.
func repoMapPaths(t *testing.T, agents string) []string {
	t.Helper()
	_, section, found := strings.Cut(agents, "\n## Repo map\n")
	if !found {
		t.Fatal(`AGENTS.md has no "## Repo map" section`)
	}
	if next := strings.Index(section, "\n## "); next >= 0 {
		section = section[:next]
	}
	var paths []string
	for line := range strings.SplitSeq(section, "\n") {
		cells := strings.Split(line, "|")
		if len(cells) < 3 {
			continue
		}
		for _, match := range repoMapPathPattern.FindAllStringSubmatch(cells[1], -1) {
			paths = append(paths, strings.TrimSuffix(match[1], "/"))
		}
	}
	if len(paths) == 0 {
		t.Fatal("AGENTS.md repo map table lists no paths")
	}
	return paths
}

// layoutTreePaths returns every path in the first text block of the
// docs/repo-layout.md "## Current layout" section, relative to the repository
// root. The tree indents two spaces per level under a single root line.
func layoutTreePaths(t *testing.T, layout string) []string {
	t.Helper()
	_, section, found := strings.Cut(layout, "\n## Current layout\n")
	if !found {
		t.Fatal(`docs/repo-layout.md has no "## Current layout" section`)
	}
	_, block, found := strings.Cut(section, "```text\n")
	if !found {
		t.Fatal("docs/repo-layout.md current layout has no text block")
	}
	block, _, found = strings.Cut(block, "```")
	if !found {
		t.Fatal("docs/repo-layout.md current layout text block is not closed")
	}
	var (
		paths []string
		stack []string
	)
	for i, line := range strings.Split(strings.TrimRight(block, "\n"), "\n") {
		if i == 0 {
			continue // the repository root line
		}
		name := strings.TrimLeft(line, " ")
		indent := len(line) - len(name)
		if indent%2 != 0 || indent == 0 || indent/2 > len(stack)+1 {
			t.Fatalf("docs/repo-layout.md tree line %q is not indented by two spaces per level", line)
		}
		stack = append(stack[:indent/2-1], strings.TrimSuffix(name, "/"))
		paths = append(paths, strings.Join(stack, "/"))
	}
	if len(paths) == 0 {
		t.Fatal("docs/repo-layout.md current layout tree lists no paths")
	}
	return paths
}

// covers reports whether a listed path is dir itself or a path inside it.
func covers(listed []string, dir string) bool {
	return slices.ContainsFunc(listed, func(path string) bool {
		return path == dir || strings.HasPrefix(path, dir+"/")
	})
}

// TestRepoMapsListEveryTopLevelInternalDirectory holds the AGENTS.md repo map
// and the docs/repo-layout.md tree to the tree on disk: each must name every
// top-level internal/ directory, and every path either lists must exist.
func TestRepoMapsListEveryTopLevelInternalDirectory(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dirs := topLevelInternalDirs(t, root)
	maps := []struct {
		file  string
		paths []string
	}{
		{"AGENTS.md", repoMapPaths(t, readRepoFile(t, root, "AGENTS.md"))},
		{"docs/repo-layout.md", layoutTreePaths(t, readRepoFile(t, root, "docs/repo-layout.md"))},
	}
	for _, m := range maps {
		var missing, absent []string
		for _, dir := range dirs {
			if !covers(m.paths, dir) {
				missing = append(missing, dir)
			}
		}
		for _, path := range m.paths {
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); err != nil {
				absent = append(absent, path)
			}
		}
		if len(missing) > 0 {
			t.Errorf("%s does not list these top-level internal/ directories; add each one: %s", m.file, strings.Join(missing, ", "))
		}
		if len(absent) > 0 {
			t.Errorf("%s lists paths that do not exist; remove or correct them: %s", m.file, strings.Join(absent, ", "))
		}
	}
}
