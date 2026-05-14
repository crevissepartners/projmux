package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const AINotifyDedupeSecondsFileName = "ai-notify-dedupe-seconds"

func (p Paths) AINotifyDedupeSecondsFile() string {
	return filepath.Join(p.ConfigDir, AINotifyDedupeSecondsFileName)
}

func LoadAINotifyDedupeSecondsFileDefault(path string, fallback int) (int, error) {
	if fallback <= 0 {
		fallback = 120
	}
	if strings.TrimSpace(path) == "" {
		return fallback, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fallback, nil
		}
		return fallback, fmt.Errorf("read AI notify dedupe seconds file: %w", err)
	}
	seconds, err := strconv.Atoi(strings.TrimSpace(string(content)))
	if err != nil || seconds <= 0 {
		return fallback, nil
	}
	return seconds, nil
}

func SaveAINotifyDedupeSecondsFile(path string, seconds int) error {
	if strings.TrimSpace(path) == "" {
		return ErrHomeDirRequired
	}
	if seconds <= 0 {
		return fmt.Errorf("AI notify dedupe seconds must be positive")
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create AI notify dedupe directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create AI notify dedupe temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.WriteString(strconv.Itoa(seconds) + "\n"); err != nil {
		tmp.Close()
		return fmt.Errorf("write AI notify dedupe temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close AI notify dedupe temp file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod AI notify dedupe temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename AI notify dedupe temp file: %w", err)
	}
	return nil
}
