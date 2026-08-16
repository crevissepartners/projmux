package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	StatusbarNotificationsHUDVisibilityFileName = "statusbar-visibility-notifications-hud"
	StatusbarAgentUsageHUDVisibilityFileName    = "statusbar-visibility-agent-usage-hud"

	StatusbarVisibilityOn  StatusbarVisibility = "on"
	StatusbarVisibilityOff StatusbarVisibility = "off"

	StatusbarVisibilitySourceDefault StatusbarVisibilitySource = "default"
	StatusbarVisibilitySourceSaved   StatusbarVisibilitySource = "saved"
)

type StatusbarVisibility string
type StatusbarVisibilitySource string

// StatusbarVisibilityState projects the persisted value separately from the
// effective value and its source. Invalid saved text is retained for Settings
// diagnostics while effective behavior falls back to the compatibility-safe
// default (on).
type StatusbarVisibilityState struct {
	Effective StatusbarVisibility
	Saved     string
	Source    StatusbarVisibilitySource
	Invalid   string
}

func DefaultStatusbarVisibilityState() StatusbarVisibilityState {
	return StatusbarVisibilityState{
		Effective: StatusbarVisibilityOn,
		Source:    StatusbarVisibilitySourceDefault,
	}
}

func NormalizeStatusbarVisibility(value string) StatusbarVisibility {
	if strings.EqualFold(strings.TrimSpace(value), string(StatusbarVisibilityOff)) {
		return StatusbarVisibilityOff
	}
	return StatusbarVisibilityOn
}

func (p Paths) StatusbarNotificationsHUDVisibilityFile() string {
	return filepath.Join(p.ConfigDir, StatusbarNotificationsHUDVisibilityFileName)
}

func (p Paths) StatusbarAgentUsageHUDVisibilityFile() string {
	return filepath.Join(p.ConfigDir, StatusbarAgentUsageHUDVisibilityFileName)
}

func LoadStatusbarVisibilityFile(path string) (StatusbarVisibilityState, error) {
	state := DefaultStatusbarVisibilityState()
	if strings.TrimSpace(path) == "" {
		return state, nil
	}
	// #nosec G304 -- path is the resolved projmux configuration file supplied by the caller.
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return state, nil
		}
		return state, fmt.Errorf("read statusbar visibility file: %w", err)
	}
	raw := strings.TrimSpace(string(content))
	switch strings.ToLower(raw) {
	case string(StatusbarVisibilityOn), string(StatusbarVisibilityOff):
		state.Effective = StatusbarVisibility(strings.ToLower(raw))
		state.Saved = string(state.Effective)
		state.Source = StatusbarVisibilitySourceSaved
	default:
		state.Invalid = raw
	}
	return state, nil
}

func SaveStatusbarVisibilityFile(path string, value StatusbarVisibility) error {
	if strings.TrimSpace(path) == "" {
		return ErrHomeDirRequired
	}
	value = NormalizeStatusbarVisibility(string(value))
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create statusbar visibility directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create statusbar visibility temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.WriteString(string(value) + "\n"); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write statusbar visibility temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close statusbar visibility temp file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("chmod statusbar visibility temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename statusbar visibility temp file: %w", err)
	}
	return nil
}
