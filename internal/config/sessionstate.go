package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	SessionStateAutosaveFileName    = "sessionstate-autosave"
	SessionStateAutorestoreFileName = "sessionstate-autorestore"

	SessionStateToggleOn  SessionStateToggle = "on"
	SessionStateToggleOff SessionStateToggle = "off"
)

type SessionStateToggle string

func NormalizeSessionStateToggle(value string) SessionStateToggle {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "0", "false", "no", "off", "disabled", "disable":
		return SessionStateToggleOff
	default:
		return SessionStateToggleOn
	}
}

func (t SessionStateToggle) Enabled() bool {
	return NormalizeSessionStateToggle(string(t)) == SessionStateToggleOn
}

func (p Paths) SessionStateAutosaveFile() string {
	return filepath.Join(p.ConfigDir, SessionStateAutosaveFileName)
}

func (p Paths) SessionStateAutorestoreFile() string {
	return filepath.Join(p.ConfigDir, SessionStateAutorestoreFileName)
}

func (p Paths) SessionStateDir() string {
	return filepath.Join(p.StateDir, "sessions")
}

func LoadSessionStateToggleFile(path string) (SessionStateToggle, error) {
	if strings.TrimSpace(path) == "" {
		return SessionStateToggleOn, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return SessionStateToggleOn, nil
		}
		return SessionStateToggleOn, fmt.Errorf("read sessionstate toggle file: %w", err)
	}
	return NormalizeSessionStateToggle(string(content)), nil
}

func SaveSessionStateToggleFile(path string, value SessionStateToggle) error {
	if strings.TrimSpace(path) == "" {
		return ErrHomeDirRequired
	}

	value = NormalizeSessionStateToggle(string(value))
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create sessionstate toggle directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create sessionstate toggle temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.WriteString(string(value) + "\n"); err != nil {
		tmp.Close()
		return fmt.Errorf("write sessionstate toggle temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close sessionstate toggle temp file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod sessionstate toggle temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename sessionstate toggle temp file: %w", err)
	}
	return nil
}
