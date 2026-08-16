package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	LiveResourcesFileName       = "live-resources"
	LiveResourcesSampleFileName = "live-resources-sample.json"

	LiveResourcesOff LiveResourcesMode = "off"
	LiveResourcesOn  LiveResourcesMode = "on"

	LiveResourcesSourceDefault LiveResourcesSource = "default"
	LiveResourcesSourceSaved   LiveResourcesSource = "saved"
)

type LiveResourcesMode string
type LiveResourcesSource string

type LiveResourcesState struct {
	Effective LiveResourcesMode
	Saved     string
	Source    LiveResourcesSource
	Invalid   string
}

func DefaultLiveResourcesState() LiveResourcesState {
	return LiveResourcesState{
		Effective: LiveResourcesOff,
		Source:    LiveResourcesSourceDefault,
	}
}

func NormalizeLiveResourcesMode(value string) LiveResourcesMode {
	if strings.EqualFold(strings.TrimSpace(value), string(LiveResourcesOn)) {
		return LiveResourcesOn
	}
	return LiveResourcesOff
}

func (p Paths) LiveResourcesFile() string {
	return filepath.Join(p.ConfigDir, LiveResourcesFileName)
}

func (p Paths) LiveResourcesSampleFile() string {
	return filepath.Join(p.StateDir, LiveResourcesSampleFileName)
}

func LoadLiveResourcesFile(path string) (LiveResourcesMode, error) {
	state, err := LoadLiveResourcesStateFile(path)
	return state.Effective, err
}

// LoadLiveResourcesStateFile keeps the existing live-resources file as the
// component's single source while projecting saved/effective/source separately.
// Missing and invalid values retain the compatibility default off.
func LoadLiveResourcesStateFile(path string) (LiveResourcesState, error) {
	state := DefaultLiveResourcesState()
	if strings.TrimSpace(path) == "" {
		return state, nil
	}
	// #nosec G304 -- path is the resolved projmux configuration file supplied by the caller.
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return state, nil
		}
		return state, fmt.Errorf("read live resources file: %w", err)
	}
	raw := strings.TrimSpace(string(content))
	switch strings.ToLower(raw) {
	case string(LiveResourcesOn), string(LiveResourcesOff):
		state.Effective = LiveResourcesMode(strings.ToLower(raw))
		state.Saved = string(state.Effective)
		state.Source = LiveResourcesSourceSaved
	default:
		state.Invalid = raw
	}
	return state, nil
}

func SaveLiveResourcesFile(path string, value LiveResourcesMode) error {
	if strings.TrimSpace(path) == "" {
		return ErrHomeDirRequired
	}

	value = NormalizeLiveResourcesMode(string(value))
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create live resources directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, LiveResourcesFileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("create live resources temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.WriteString(string(value) + "\n"); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write live resources temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close live resources temp file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("chmod live resources temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename live resources temp file: %w", err)
	}
	return nil
}
