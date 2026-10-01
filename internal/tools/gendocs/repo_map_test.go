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

// extraMapPath is the optional map a derived tree keeps for top-level
// directories it adds, so it never has to edit the two shared maps. Neither
// map lists what it lists; the guard counts it toward both.
const extraMapPath = "docs/repo-layout.local.md"

// extraMapPaths returns the backticked paths in the first column of every
// table row in the optional extra map, or nil when the file does not exist.
func extraMapPaths(t *testing.T, root string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(extraMapPath))) // #nosec G304 -- repository-relative optional doc
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read %s: %v", extraMapPath, err)
	}
	var paths []string
	for line := range strings.SplitSeq(string(data), "\n") {
		cells := strings.Split(line, "|")
		if len(cells) < 3 {
			continue
		}
		for _, match := range repoMapPathPattern.FindAllStringSubmatch(cells[1], -1) {
			paths = append(paths, strings.TrimSuffix(match[1], "/"))
		}
	}
	return paths
}

// repoMapProblems returns one message per map that misses a top-level
// internal/ directory or lists a path that does not exist under root.
func repoMapProblems(t *testing.T, root string) []string {
	t.Helper()
	dirs := topLevelInternalDirs(t, root)
	extra := extraMapPaths(t, root)
	maps := []struct {
		file  string
		paths []string
	}{
		{"AGENTS.md", repoMapPaths(t, readRepoFile(t, root, "AGENTS.md"))},
		{"docs/repo-layout.md", layoutTreePaths(t, readRepoFile(t, root, "docs/repo-layout.md"))},
	}
	if extra != nil {
		maps = append(maps, struct {
			file  string
			paths []string
		}{extraMapPath, extra})
	}
	var problems []string
	for _, m := range maps {
		var missing, absent []string
		if m.file != extraMapPath {
			for _, dir := range dirs {
				if !covers(m.paths, dir) && !covers(extra, dir) {
					missing = append(missing, dir)
				}
			}
		}
		for _, path := range m.paths {
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); err != nil {
				absent = append(absent, path)
			}
		}
		if len(missing) > 0 {
			problems = append(problems, m.file+" does not list these top-level internal/ directories; add each one: "+strings.Join(missing, ", "))
		}
		if len(absent) > 0 {
			problems = append(problems, m.file+" lists paths that do not exist; remove or correct them: "+strings.Join(absent, ", "))
		}
	}
	return problems
}

// TestRepoMapsListEveryTopLevelInternalDirectory holds the AGENTS.md repo map
// and the docs/repo-layout.md tree to the tree on disk: each must name every
// top-level internal/ directory, directly or through the optional extra map,
// and every path any of them lists must exist.
func TestRepoMapsListEveryTopLevelInternalDirectory(t *testing.T) {
	t.Parallel()
	for _, problem := range repoMapProblems(t, repoRoot(t)) {
		t.Error(problem)
	}
}

// TestRepoMapsCountTheOptionalExtraMap runs the guard over a small fixture
// tree: without the extra map nothing changes, a directory the extra map lists
// needs no row in the shared maps, and a path it lists must exist.
func TestRepoMapsCountTheOptionalExtraMap(t *testing.T) {
	t.Parallel()
	const (
		agents = "# Agent Guide\n\n## Repo map\n| Path | Purpose |\n| --- | --- |\n| `internal/app` | App. |\n\n## Workflow\n"
		layout = "# Repository Layout\n\n## Current layout\n\n```text\nprojmux/\n  internal/\n    app/\n```\n"
	)
	fixture := func(t *testing.T, extraDir, extraMap string) string {
		t.Helper()
		root := t.TempDir()
		for _, dir := range []string{"docs", "internal/app", extraDir} {
			if dir == "" {
				continue
			}
			if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(dir)), 0o750); err != nil {
				t.Fatal(err)
			}
		}
		files := map[string]string{"AGENTS.md": agents, "docs/repo-layout.md": layout}
		if extraMap != "" {
			files[extraMapPath] = extraMap
		}
		for rel, body := range files {
			if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return root
	}
	cases := []struct {
		name     string
		extraDir string
		extraMap string
		want     []string
	}{
		{name: "no extra map, shared maps complete"},
		{
			name:     "no extra map, unlisted directory",
			extraDir: "internal/extra",
			want: []string{
				"AGENTS.md does not list these top-level internal/ directories; add each one: internal/extra",
				"docs/repo-layout.md does not list these top-level internal/ directories; add each one: internal/extra",
			},
		},
		{
			name:     "extra map lists the directory",
			extraDir: "internal/extra",
			extraMap: "| Path | Purpose |\n| --- | --- |\n| `internal/extra` | Extra. |\n",
		},
		{
			name:     "extra map lists a missing path",
			extraMap: "| Path | Purpose |\n| --- | --- |\n| `internal/gone` | Gone. |\n",
			want:     []string{extraMapPath + " lists paths that do not exist; remove or correct them: internal/gone"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := repoMapProblems(t, fixture(t, tc.extraDir, tc.extraMap))
			if !slices.Equal(got, tc.want) {
				t.Errorf("problems = %q, want %q", got, tc.want)
			}
		})
	}
}
