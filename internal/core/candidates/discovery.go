package candidates

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Inputs captures the filesystem-backed sources used to build the first
// sessionizer candidate set.
type Inputs struct {
	HomeDir      string
	RepoRoot     string
	ManagedRoots []string
	Pins         []string
	CurrentPath  string
}

// Discover returns the ordered sessionizer directory candidates for the
// provided inputs.
func Discover(inputs Inputs) ([]string, error) {
	builder := orderedSet{}

	builder.appendDir(inputs.HomeDir)
	builder.appendDir(filepath.Join(cleanPath(inputs.HomeDir), "dotfiles"))

	for _, pin := range inputs.Pins {
		builder.appendDir(pin)
	}

	if current := snappedCurrentPath(inputs.CurrentPath, inputs.snapRoots()); current != "" {
		builder.appendDir(current)
	}

	if err := builder.appendRootChildren(inputs.RepoRoot); err != nil {
		return nil, err
	}

	for _, root := range inputs.ManagedRoots {
		if err := builder.appendRootChildren(root); err != nil {
			return nil, err
		}
	}

	return builder.values, nil
}

func (i Inputs) snapRoots() []string {
	roots := make([]string, 0, len(i.ManagedRoots)+1)
	if i.RepoRoot != "" {
		roots = append(roots, i.RepoRoot)
	}
	roots = append(roots, i.ManagedRoots...)
	return roots
}

func snappedCurrentPath(path string, managedRoots []string) string {
	if !dirExists(path) {
		return ""
	}

	current := cleanPath(path)
	for _, root := range managedRoots {
		if !dirExists(root) {
			continue
		}

		cleanRoot := cleanPath(root)
		prefix := cleanRoot + string(filepath.Separator)
		if !strings.HasPrefix(current, prefix) {
			continue
		}

		rel := strings.TrimPrefix(current, prefix)
		project := strings.SplitN(rel, string(filepath.Separator), 2)[0]
		if project == "" {
			continue
		}

		return filepath.Join(cleanRoot, project)
	}

	return current
}

type orderedSet struct {
	values []string
	seen   map[string]struct{}
}

func (s *orderedSet) appendDir(path string) {
	if !dirExists(path) {
		return
	}

	s.append(cleanPath(path))
}

func (s *orderedSet) appendRootChildren(root string) error {
	if !dirExists(root) {
		return nil
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("read root %q: %w", root, err)
	}
	slices.SortFunc(entries, func(a, b os.DirEntry) int {
		return strings.Compare(a.Name(), b.Name())
	})

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		s.append(filepath.Join(cleanPath(root), entry.Name()))
	}

	return nil
}

func (s *orderedSet) append(path string) {
	if s.seen == nil {
		s.seen = make(map[string]struct{})
	}

	if _, ok := s.seen[path]; ok {
		return
	}

	s.seen[path] = struct{}{}
	s.values = append(s.values, path)
}

func dirExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}

	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func cleanPath(path string) string {
	if path == "" {
		return ""
	}

	return filepath.Clean(path)
}
