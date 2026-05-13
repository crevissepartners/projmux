package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	SessionStateAutosaveFileName         = "sessionstate-autosave"
	SessionStateAutosaveIntervalFileName = "sessionstate-autosave-interval"
	SessionStateProjectsDirName          = "sessionstate-projects"
	SidebarStartupPickerFileName         = "sidebar-startup-picker"

	SessionStateToggleOn  SessionStateToggle = "on"
	SessionStateToggleOff SessionStateToggle = "off"

	SessionStateProjectInherit SessionStateProjectToggle = "inherit"
	SessionStateProjectOn      SessionStateProjectToggle = "on"
	SessionStateProjectOff     SessionStateProjectToggle = "off"
)

type SessionStateToggle string
type SessionStateProjectToggle string

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

func NormalizeSessionStateProjectToggle(value string) SessionStateProjectToggle {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on", "enabled", "enable":
		return SessionStateProjectOn
	case "0", "false", "no", "off", "disabled", "disable":
		return SessionStateProjectOff
	default:
		return SessionStateProjectInherit
	}
}

func (p Paths) SessionStateAutosaveFile() string {
	return filepath.Join(p.ConfigDir, SessionStateAutosaveFileName)
}

func (p Paths) SessionStateAutosaveIntervalFile() string {
	return filepath.Join(p.ConfigDir, SessionStateAutosaveIntervalFileName)
}

func (p Paths) SidebarStartupPickerFile() string {
	return filepath.Join(p.ConfigDir, SidebarStartupPickerFileName)
}

func (p Paths) ProjectSessionStateAutosaveFile(sessionName string) string {
	return filepath.Join(p.ConfigDir, SessionStateProjectsDirName, safeSessionStateProjectName(sessionName), "autosave")
}

func (p Paths) SessionStateDir() string {
	return filepath.Join(p.StateDir, "sessions")
}

func LoadSessionStateToggleFile(path string) (SessionStateToggle, error) {
	return LoadSessionStateToggleFileDefault(path, SessionStateToggleOn)
}

func LoadSessionStateToggleFileDefault(path string, fallback SessionStateToggle) (SessionStateToggle, error) {
	fallback = NormalizeSessionStateToggle(string(fallback))
	if strings.TrimSpace(path) == "" {
		return fallback, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fallback, nil
		}
		return fallback, fmt.Errorf("read sessionstate toggle file: %w", err)
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

func LoadSessionStateDurationFileDefault(path string, fallback time.Duration) (time.Duration, error) {
	if fallback <= 0 {
		fallback = time.Minute
	}
	if strings.TrimSpace(path) == "" {
		return fallback, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fallback, nil
		}
		return fallback, fmt.Errorf("read sessionstate duration file: %w", err)
	}
	value, err := time.ParseDuration(strings.TrimSpace(string(content)))
	if err != nil || value <= 0 {
		return fallback, nil
	}
	return value, nil
}

func SaveSessionStateDurationFile(path string, value time.Duration) error {
	if strings.TrimSpace(path) == "" {
		return ErrHomeDirRequired
	}
	if value <= 0 {
		return fmt.Errorf("sessionstate duration must be positive")
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create sessionstate duration directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create sessionstate duration temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.WriteString(value.String() + "\n"); err != nil {
		tmp.Close()
		return fmt.Errorf("write sessionstate duration temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close sessionstate duration temp file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod sessionstate duration temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename sessionstate duration temp file: %w", err)
	}
	return nil
}

func LoadSessionStateProjectToggleFile(path string) (SessionStateProjectToggle, error) {
	if strings.TrimSpace(path) == "" {
		return SessionStateProjectInherit, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return SessionStateProjectInherit, nil
		}
		return SessionStateProjectInherit, fmt.Errorf("read project sessionstate toggle file: %w", err)
	}
	return NormalizeSessionStateProjectToggle(string(content)), nil
}

func SaveSessionStateProjectToggleFile(path string, value SessionStateProjectToggle) error {
	if strings.TrimSpace(path) == "" {
		return ErrHomeDirRequired
	}

	value = NormalizeSessionStateProjectToggle(string(value))
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create project sessionstate toggle directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create project sessionstate toggle temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.WriteString(string(value) + "\n"); err != nil {
		tmp.Close()
		return fmt.Errorf("write project sessionstate toggle temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close project sessionstate toggle temp file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod project sessionstate toggle temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename project sessionstate toggle temp file: %w", err)
	}
	return nil
}

func safeSessionStateProjectName(sessionName string) string {
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		return "_"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", string(filepath.Separator), "_")
	return replacer.Replace(sessionName)
}
