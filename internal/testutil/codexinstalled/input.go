package codexinstalled

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

const maxExplicitInputBytes = 64 << 10

// ReadExplicitInput reads a bounded regular JSON input through a directory
// capability. Relative/traversing paths, final symlinks and special files refuse
// before reading. OpenRoot keeps a replacement symlink inside the chosen parent.
func ReadExplicitInput(path string) ([]byte, error) {
	refuse := func() ([]byte, error) {
		return nil, errors.New("explicit input must be an absolute clean regular JSON file, at most 64 KiB")
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Ext(path) != ".json" {
		return refuse()
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return refuse()
	}
	defer root.Close()
	name := filepath.Base(path)
	before, err := root.Lstat(name)
	if err != nil || !before.Mode().IsRegular() || before.Size() > maxExplicitInputBytes {
		return refuse()
	}
	file, err := root.Open(name)
	if err != nil {
		return refuse()
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return refuse()
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxExplicitInputBytes+1))
	if err != nil || len(raw) > maxExplicitInputBytes {
		return refuse()
	}
	return raw, nil
}
