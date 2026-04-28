package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// WorkdirsFileName is the basename of the persisted managed-roots list. Each
// non-empty, non-comment line stores one absolute directory path.
const WorkdirsFileName = "workdirs"

// WorkdirsFile returns the default file used for the persisted workdirs list.
func (p Paths) WorkdirsFile() string {
	return filepath.Join(p.ConfigDir, WorkdirsFileName)
}

// WorkdirsFile returns the path to the persisted workdirs file rooted at the
// supplied home directory. An empty homeDir yields an empty string.
func WorkdirsFile(homeDir string) string {
	if homeDir == "" {
		return ""
	}
	return filepath.Join(homeDir, ".config", AppName, WorkdirsFileName)
}

// LoadWorkdirs reads the saved workdirs file rooted at homeDir and returns its
// directory list. A missing file yields (nil, nil). Empty lines and lines that
// start with '#' are skipped. Whitespace is trimmed and duplicate paths are
// removed while preserving order.
func LoadWorkdirs(homeDir string) ([]string, error) {
	path := WorkdirsFile(homeDir)
	if path == "" {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open workdirs file: %w", err)
	}
	defer file.Close()

	seen := make(map[string]struct{})
	var dirs []string

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		if _, ok := seen[line]; ok {
			continue
		}
		seen[line] = struct{}{}
		dirs = append(dirs, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read workdirs file: %w", err)
	}
	return dirs, nil
}

// SaveWorkdirs persists dirs to the workdirs file rooted at homeDir using an
// atomic rename. A nil or empty list removes the file. The parent directory is
// created with 0o755 if missing. Each entry is written on its own line; entries
// are trimmed and deduplicated before writing.
func SaveWorkdirs(homeDir string, dirs []string) error {
	path := WorkdirsFile(homeDir)
	if path == "" {
		return ErrHomeDirRequired
	}

	cleaned := normalizeWorkdirs(dirs)

	if len(cleaned) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove workdirs file: %w", err)
		}
		return nil
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create workdirs directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, WorkdirsFileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("create workdirs temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	writer := bufio.NewWriter(tmp)
	for _, d := range cleaned {
		if _, err := writer.WriteString(d + "\n"); err != nil {
			tmp.Close()
			return fmt.Errorf("write workdirs temp file: %w", err)
		}
	}
	if err := writer.Flush(); err != nil {
		tmp.Close()
		return fmt.Errorf("flush workdirs temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close workdirs temp file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod workdirs temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename workdirs temp file: %w", err)
	}
	return nil
}

// AddWorkdir appends dir to the persisted workdirs list when it is not already
// present. The returned bool reports whether a write occurred.
func AddWorkdir(homeDir, dir string) (bool, error) {
	trimmed := strings.TrimSpace(dir)
	if trimmed == "" {
		return false, nil
	}

	current, err := LoadWorkdirs(homeDir)
	if err != nil {
		return false, err
	}
	if slices.Contains(current, trimmed) {
		return false, nil
	}

	updated := append(current, trimmed)
	if err := SaveWorkdirs(homeDir, updated); err != nil {
		return false, err
	}
	return true, nil
}

// RemoveWorkdir removes dir from the persisted workdirs list when present. The
// returned bool reports whether a write occurred.
func RemoveWorkdir(homeDir, dir string) (bool, error) {
	trimmed := strings.TrimSpace(dir)
	if trimmed == "" {
		return false, nil
	}

	current, err := LoadWorkdirs(homeDir)
	if err != nil {
		return false, err
	}

	updated := make([]string, 0, len(current))
	removed := false
	for _, existing := range current {
		if existing == trimmed {
			removed = true
			continue
		}
		updated = append(updated, existing)
	}
	if !removed {
		return false, nil
	}

	if err := SaveWorkdirs(homeDir, updated); err != nil {
		return false, err
	}
	return true, nil
}

func normalizeWorkdirs(dirs []string) []string {
	seen := make(map[string]struct{}, len(dirs))
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	return out
}
