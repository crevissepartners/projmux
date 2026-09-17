package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// SidebarStartupPickerFileName stores the closed-Project startup
	// preference of the Project Sidebar. The file name is unchanged from the
	// release that introduced the setting, so saved values keep working.
	SidebarStartupPickerFileName = "sidebar-startup-picker"

	SidebarStartupPickerOn  SidebarStartupPicker = "on"
	SidebarStartupPickerOff SidebarStartupPicker = "off"
)

// SidebarStartupPicker is the saved on/off closed-Project startup preference.
type SidebarStartupPicker string

// NormalizeSidebarStartupPicker maps any value other than "off" to "on".
func NormalizeSidebarStartupPicker(value string) SidebarStartupPicker {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(SidebarStartupPickerOff):
		return SidebarStartupPickerOff
	default:
		return SidebarStartupPickerOn
	}
}

func (t SidebarStartupPicker) Enabled() bool {
	return NormalizeSidebarStartupPicker(string(t)) == SidebarStartupPickerOn
}

func (p Paths) SidebarStartupPickerFile() string {
	return filepath.Join(p.ConfigDir, SidebarStartupPickerFileName)
}

// LoadSidebarStartupPickerFile reads the saved preference. A missing file or
// an empty path resolves to "on".
func LoadSidebarStartupPickerFile(path string) (SidebarStartupPicker, error) {
	if strings.TrimSpace(path) == "" {
		return SidebarStartupPickerOn, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return SidebarStartupPickerOn, nil
		}
		return SidebarStartupPickerOn, fmt.Errorf("read sidebar startup picker file: %w", err)
	}
	return NormalizeSidebarStartupPicker(string(content)), nil
}

// SaveSidebarStartupPickerFile atomically replaces the saved preference.
func SaveSidebarStartupPickerFile(path string, value SidebarStartupPicker) error {
	if strings.TrimSpace(path) == "" {
		return ErrHomeDirRequired
	}

	value = NormalizeSidebarStartupPicker(string(value))
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create sidebar startup picker directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create sidebar startup picker temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.WriteString(string(value) + "\n"); err != nil {
		tmp.Close()
		return fmt.Errorf("write sidebar startup picker temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close sidebar startup picker temp file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod sidebar startup picker temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename sidebar startup picker temp file: %w", err)
	}
	return nil
}
