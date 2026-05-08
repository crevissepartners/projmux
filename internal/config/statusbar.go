package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	StatusbarDecorationFileName = "statusbar-decoration"

	StatusbarDecorationOff    StatusbarDecoration = "off"
	StatusbarDecorationSymbol StatusbarDecoration = "symbol"
	StatusbarDecorationEmoji  StatusbarDecoration = "emoji"
)

type StatusbarDecoration string

func NormalizeStatusbarDecoration(value string) StatusbarDecoration {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(StatusbarDecorationSymbol):
		return StatusbarDecorationSymbol
	case string(StatusbarDecorationEmoji):
		return StatusbarDecorationEmoji
	default:
		return StatusbarDecorationOff
	}
}

// StatusbarDecorationFile returns the default file used for the persisted
// statusbar cwd/git leading decoration mode.
func (p Paths) StatusbarDecorationFile() string {
	return filepath.Join(p.ConfigDir, StatusbarDecorationFileName)
}

// LoadStatusbarDecorationFile reads a persisted statusbar decoration enum.
// Missing, empty, and unrecognized values all resolve to the conservative
// default: off.
func LoadStatusbarDecorationFile(path string) (StatusbarDecoration, error) {
	if strings.TrimSpace(path) == "" {
		return StatusbarDecorationOff, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return StatusbarDecorationOff, nil
		}
		return StatusbarDecorationOff, fmt.Errorf("read statusbar decoration file: %w", err)
	}
	return NormalizeStatusbarDecoration(string(content)), nil
}

// SaveStatusbarDecorationFile persists the normalized statusbar decoration
// enum using an atomic rename. The parent directory is created if missing.
func SaveStatusbarDecorationFile(path string, value StatusbarDecoration) error {
	if strings.TrimSpace(path) == "" {
		return ErrHomeDirRequired
	}

	value = NormalizeStatusbarDecoration(string(value))
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create statusbar decoration directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, StatusbarDecorationFileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("create statusbar decoration temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.WriteString(string(value) + "\n"); err != nil {
		tmp.Close()
		return fmt.Errorf("write statusbar decoration temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close statusbar decoration temp file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod statusbar decoration temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename statusbar decoration temp file: %w", err)
	}
	return nil
}
