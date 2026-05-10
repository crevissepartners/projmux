package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	PickerBackendFileName = "picker-backend"

	PickerBackendNative  PickerBackend = "native"
	DefaultPickerBackend               = PickerBackendNative
)

type PickerBackend string

func NormalizePickerBackend(value string) PickerBackend {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(PickerBackendNative):
		return PickerBackendNative
	default:
		return DefaultPickerBackend
	}
}

func (p Paths) PickerBackendFile() string {
	return filepath.Join(p.ConfigDir, PickerBackendFileName)
}

func LoadPickerBackendFile(path string) (PickerBackend, error) {
	if strings.TrimSpace(path) == "" {
		return DefaultPickerBackend, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DefaultPickerBackend, nil
		}
		return DefaultPickerBackend, fmt.Errorf("read picker backend file: %w", err)
	}
	return NormalizePickerBackend(string(content)), nil
}

func SavePickerBackendFile(path string, value PickerBackend) error {
	if strings.TrimSpace(path) == "" {
		return ErrHomeDirRequired
	}

	value = NormalizePickerBackend(string(value))
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create picker backend directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, PickerBackendFileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("create picker backend temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.WriteString(string(value) + "\n"); err != nil {
		tmp.Close()
		return fmt.Errorf("write picker backend temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close picker backend temp file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod picker backend temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename picker backend temp file: %w", err)
	}
	return nil
}
