package pins

import (
	"errors"
	"slices"
	"strings"

	"github.com/es5h/projmux/internal/config"
	"github.com/es5h/projmux/internal/state"
)

var ErrInvalidPin = errors.New("invalid pin path")

// Store manages a file-backed set of pinned project directories.
type Store struct {
	file state.LinesFile
}

// NewStore builds a pin store for the provided file path.
func NewStore(path string) Store {
	return Store{file: state.NewLinesFile(path)}
}

// NewDefaultStore builds a pin store from resolved projmux paths.
func NewDefaultStore(paths config.Paths) Store {
	return NewStore(paths.PinFile())
}

// Path returns the file path used by this store.
func (s Store) Path() string {
	return s.file.Path()
}

// List returns the current ordered pin set.
func (s Store) List() ([]string, error) {
	return s.load()
}

// Add records a new pin unless it already exists.
func (s Store) Add(pin string) error {
	pin, err := validate(pin)
	if err != nil {
		return err
	}

	lines, err := s.load()
	if err != nil {
		return err
	}
	if contains(lines, pin) {
		return nil
	}

	lines = append(lines, pin)
	return s.file.Write(lines)
}

// Remove drops a pin if present.
func (s Store) Remove(pin string) error {
	pin, err := validate(pin)
	if err != nil {
		return err
	}

	lines, err := s.load()
	if err != nil {
		return err
	}

	filtered := lines[:0]
	for _, line := range lines {
		if line == pin {
			continue
		}
		filtered = append(filtered, line)
	}

	return s.file.Write(filtered)
}

// Toggle flips the pin state and returns whether the pin is now present.
func (s Store) Toggle(pin string) (bool, error) {
	pin, err := validate(pin)
	if err != nil {
		return false, err
	}

	lines, err := s.load()
	if err != nil {
		return false, err
	}

	if contains(lines, pin) {
		filtered := lines[:0]
		for _, line := range lines {
			if line == pin {
				continue
			}
			filtered = append(filtered, line)
		}
		if err := s.file.Write(filtered); err != nil {
			return false, err
		}
		return false, nil
	}

	lines = append(lines, pin)
	if err := s.file.Write(lines); err != nil {
		return false, err
	}
	return true, nil
}

// Clear truncates the underlying file to an empty set.
func (s Store) Clear() error {
	return s.file.Write(nil)
}

func (s Store) load() ([]string, error) {
	lines, err := s.file.Read()
	if err != nil {
		return nil, err
	}
	return unique(lines), nil
}

func validate(pin string) (string, error) {
	if strings.TrimSpace(pin) == "" {
		return "", ErrInvalidPin
	}
	if strings.ContainsAny(pin, "\r\n") {
		return "", ErrInvalidPin
	}
	return pin, nil
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
