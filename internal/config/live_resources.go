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
)

type LiveResourcesMode string

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
	if strings.TrimSpace(path) == "" {
		return LiveResourcesOff, nil
	}
	// #nosec G304 -- path is the resolved projmux configuration file supplied by the caller.
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return LiveResourcesOff, nil
		}
		return LiveResourcesOff, fmt.Errorf("read live resources file: %w", err)
	}
	return NormalizeLiveResourcesMode(string(content)), nil
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
