package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const AINotifyDedupeSecondsFileName = "ai-notify-dedupe-seconds"
const AIHookActionsFileName = "ai-hook-actions.json"

type AIHookActionsFile struct {
	Version   int                              `json:"version,omitempty"`
	Providers map[string]AIHookProviderActions `json:"providers,omitempty"`
}

type AIHookProviderActions struct {
	Events map[string]string `json:"events,omitempty"`
}

func (p Paths) AINotifyDedupeSecondsFile() string {
	return filepath.Join(p.ConfigDir, AINotifyDedupeSecondsFileName)
}

func (p Paths) AIHookActionsFile() string {
	return filepath.Join(p.ConfigDir, AIHookActionsFileName)
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

func LoadAIHookActionsFile(path string) (AIHookActionsFile, error) {
	if strings.TrimSpace(path) == "" {
		return AIHookActionsFile{}, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return AIHookActionsFile{}, nil
		}
		return AIHookActionsFile{}, fmt.Errorf("read AI hook actions file: %w", err)
	}
	if len(strings.TrimSpace(string(content))) == 0 {
		return AIHookActionsFile{}, nil
	}
	var file AIHookActionsFile
	if err := json.Unmarshal(content, &file); err != nil {
		return AIHookActionsFile{}, fmt.Errorf("parse AI hook actions file: %w", err)
	}
	if file.Providers == nil {
		file.Providers = map[string]AIHookProviderActions{}
	}
	for provider, actions := range file.Providers {
		if actions.Events == nil {
			actions.Events = map[string]string{}
			file.Providers[provider] = actions
		}
	}
	return file, nil
}

func SaveAIHookActionsFile(path string, file AIHookActionsFile) error {
	if strings.TrimSpace(path) == "" {
		return ErrHomeDirRequired
	}
	if file.Version == 0 {
		file.Version = 1
	}
	if file.Providers == nil {
		file.Providers = map[string]AIHookProviderActions{}
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("encode AI hook actions file: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create AI hook actions directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create AI hook actions temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write AI hook actions temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close AI hook actions temp file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod AI hook actions temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename AI hook actions temp file: %w", err)
	}
	return nil
}
