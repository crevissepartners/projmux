package app

import (
	"fmt"
	"os"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
)

const pickerBackendTmuxEnv = intpicker.BackendEnv

func resolvePickerBackend(lookupEnv func(string) string) intpicker.Backend {
	return resolvePickerBackendWithConfig(os.UserHomeDir, lookupEnv)
}

func resolvePickerBackendWithConfig(homeDir func() (string, error), lookupEnv func(string) string) intpicker.Backend {
	if backend, ok := pickerBackendFromEnv(lookupEnv); ok {
		return backend
	}

	paths, err := pickerBackendConfigPaths(homeDir, lookupEnv)
	if err != nil {
		return intpicker.BackendNative
	}
	mode, err := config.LoadPickerBackendFile(paths.PickerBackendFile())
	if err != nil {
		return intpicker.BackendNative
	}
	return pickerBackendFromConfig(mode)
}

func pickerBackendFromEnv(lookupEnv func(string) string) (intpicker.Backend, bool) {
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	raw := strings.TrimSpace(lookupEnv(intpicker.BackendEnv))
	if raw == "" {
		return "", false
	}
	switch strings.ToLower(raw) {
	case string(intpicker.BackendNative):
		return intpicker.BackendNative, true
	case string(intpicker.BackendFZF):
		return intpicker.BackendFZF, true
	default:
		return intpicker.BackendNative, true
	}
}

func pickerBackendFromConfig(mode config.PickerBackend) intpicker.Backend {
	switch config.NormalizePickerBackend(string(mode)) {
	case config.PickerBackendFZF:
		return intpicker.BackendFZF
	default:
		return intpicker.BackendNative
	}
}

func pickerBackendConfigPaths(homeDir func() (string, error), lookupEnv func(string) string) (config.Paths, error) {
	if homeDir == nil {
		homeDir = os.UserHomeDir
	}
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	home, err := homeDir()
	if err != nil {
		return config.Paths{}, fmt.Errorf("resolve home directory: %w", err)
	}
	return config.Homes{
		HomeDir:    home,
		ConfigHome: lookupEnv("XDG_CONFIG_HOME"),
		StateHome:  lookupEnv("XDG_STATE_HOME"),
	}.Paths()
}
