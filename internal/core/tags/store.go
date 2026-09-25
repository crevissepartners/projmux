package tags

import (
	"errors"
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/state"
)

var ErrInvalidTag = errors.New("invalid tag")

// Store manages a file-backed ordered set of tagged entries.
type Store struct {
	file state.LinesFile
	// afterRead is a test seam run inside a locked update after the stored
	// tags were read.
	afterRead func()
}

// NewStore builds a tag store for the provided file path.
func NewStore(path string) Store {
	return Store{file: state.NewLinesFile(path)}
}

// NewDefaultStore builds a tag store from resolved projmux paths.
func NewDefaultStore(paths config.Paths) Store {
	return NewStore(paths.TagFile())
}

// Path returns the file path used by this store.
func (s Store) Path() string {
	return s.file.Path()
}

// List returns the current ordered tag set.
func (s Store) List() ([]string, error) {
	return s.load()
}

// Toggle flips the tag state and reports whether the tag is now present. The
// read-modify-write runs under the tag file's lock, so an overlapping Toggle or
// Clear cannot drop this change or have its own change dropped.
func (s Store) Toggle(name string) (bool, error) {
	name, err := validate(name)
	if err != nil {
		return false, err
	}

	present := false
	err = s.update(func(lines []string) ([]string, bool, error) {
		if contains(lines, name) {
			filtered := lines[:0]
			for _, line := range lines {
				if line == name {
					continue
				}
				filtered = append(filtered, line)
			}
			present = false
			return filtered, true, nil
		}
		present = true
		return append(lines, name), true, nil
	})
	if err != nil {
		return false, err
	}
	return present, nil
}

// Clear truncates the underlying file to an empty set. It takes the same lock
// as Toggle, so it neither erases nor is erased by an overlapping Toggle.
func (s Store) Clear() error {
	return s.update(func([]string) ([]string, bool, error) {
		return nil, true, nil
	})
}

// update runs one locked read-modify-write over the deduplicated tag set.
func (s Store) update(fn func(lines []string) ([]string, bool, error)) error {
	return s.file.Update(func(lines []string) ([]string, bool, error) {
		if s.afterRead != nil {
			s.afterRead()
		}
		return fn(unique(lines))
	})
}

func (s Store) load() ([]string, error) {
	lines, err := s.file.Read()
	if err != nil {
		return nil, err
	}
	return unique(lines), nil
}

func validate(name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", ErrInvalidTag
	}
	if strings.ContainsAny(name, "\r\n") {
		return "", ErrInvalidTag
	}
	return name, nil
}

func unique(lines []string) []string {
	if len(lines) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(lines))
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if _, ok := seen[line]; ok {
			continue
		}
		seen[line] = struct{}{}
		out = append(out, line)
	}
	return out
}

func contains(lines []string, target string) bool {
	return slices.Contains(lines, target)
}
