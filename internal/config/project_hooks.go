package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	ProjectHooksOn  ProjectHooksMode = "on"
	ProjectHooksOff ProjectHooksMode = "off"
)

type ProjectHooksMode string

func NormalizeProjectHooksMode(value string) ProjectHooksMode {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(ProjectHooksOff):
		return ProjectHooksOff
	default:
		return ProjectHooksOn
	}
}

func (p Paths) ProjectHooksFile() string {
	return filepath.Join(p.ConfigDir, ProjectHooksFileName)
}

func LoadProjectHooksFile(path string) (ProjectHooksMode, error) {
	if strings.TrimSpace(path) == "" {
		return ProjectHooksOn, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ProjectHooksOn, nil
		}
		return ProjectHooksOn, fmt.Errorf("read project hooks file: %w", err)
	}
	return NormalizeProjectHooksMode(string(content)), nil
}

func SaveProjectHooksFile(path string, value ProjectHooksMode) error {
	if strings.TrimSpace(path) == "" {
		return ErrHomeDirRequired
	}

	value = NormalizeProjectHooksMode(string(value))
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create project hooks directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ProjectHooksFileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("create project hooks temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.WriteString(string(value) + "\n"); err != nil {
		tmp.Close()
		return fmt.Errorf("write project hooks temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close project hooks temp file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod project hooks temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename project hooks temp file: %w", err)
	}
	return nil
}
