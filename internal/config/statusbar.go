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
	AIBadgeStyleFileName        = "ai-badge-style"

	StatusbarDecorationOff    StatusbarDecoration = "off"
	StatusbarDecorationSymbol StatusbarDecoration = "symbol"
	StatusbarDecorationEmoji  StatusbarDecoration = "emoji"

	AIBadgeStyleDot   AIBadgeStyle = "dot"
	AIBadgeStyleEmoji AIBadgeStyle = "emoji"
	AIBadgeStyleOff   AIBadgeStyle = "off"
)

type StatusbarDecoration string
type AIBadgeStyle string

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

func NormalizeAIBadgeStyle(value string) AIBadgeStyle {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(AIBadgeStyleEmoji):
		return AIBadgeStyleEmoji
	case string(AIBadgeStyleOff), "minimal":
		return AIBadgeStyleOff
	default:
		return AIBadgeStyleDot
	}
}

// StatusbarDecorationFile returns the default file used for the persisted
// statusbar cwd/git leading decoration mode.
func (p Paths) StatusbarDecorationFile() string {
	return filepath.Join(p.ConfigDir, StatusbarDecorationFileName)
}

// AIBadgeStyleFile returns the default file used for the persisted live AI
// status badge display style.
func (p Paths) AIBadgeStyleFile() string {
	return filepath.Join(p.ConfigDir, AIBadgeStyleFileName)
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

// LoadAIBadgeStyleFile reads a persisted live AI status badge display style.
// Missing, empty, and unrecognized values all resolve to dot so the compact
// legacy badge remains the first-class fallback.
func LoadAIBadgeStyleFile(path string) (AIBadgeStyle, error) {
	if strings.TrimSpace(path) == "" {
		return AIBadgeStyleDot, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return AIBadgeStyleDot, nil
		}
		return AIBadgeStyleDot, fmt.Errorf("read AI badge style file: %w", err)
	}
	return NormalizeAIBadgeStyle(string(content)), nil
}

// SaveAIBadgeStyleFile persists the normalized live AI status badge display
// style using an atomic rename. The parent directory is created if missing.
func SaveAIBadgeStyleFile(path string, value AIBadgeStyle) error {
	if strings.TrimSpace(path) == "" {
		return ErrHomeDirRequired
	}

	value = NormalizeAIBadgeStyle(string(value))
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create AI badge style directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, AIBadgeStyleFileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("create AI badge style temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.WriteString(string(value) + "\n"); err != nil {
		tmp.Close()
		return fmt.Errorf("write AI badge style temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close AI badge style temp file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod AI badge style temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename AI badge style temp file: %w", err)
	}
	return nil
}
