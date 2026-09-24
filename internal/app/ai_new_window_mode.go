package app

import (
	"path/filepath"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
)

// aiConfigHome is the config home the AI mode files live under:
// XDG_CONFIG_HOME, else the home directory's .config. With neither it stays
// relative to the working directory, as the TUI split default always has. A
// nil resolver counts as unset.
func aiConfigHome(homeDir func() (string, error), lookupEnv func(string) string) string {
	if lookupEnv != nil {
		if configHome := strings.TrimSpace(lookupEnv("XDG_CONFIG_HOME")); configHome != "" {
			return configHome
		}
	}
	if homeDir == nil {
		return ".config"
	}
	home, err := homeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ".config"
	}
	return filepath.Join(home, ".config")
}

// loadCentralAINewWindowMode reads the central new AI window mode. It is a
// central read, so it reports no front read.
func loadCentralAINewWindowMode(homeDir func() (string, error), lookupEnv func(string) string) (mode string, saved bool, err error) {
	return config.LoadAINewWindowModeFile(config.DefaultPaths(aiConfigHome(homeDir, lookupEnv), "").AINewWindowModeFile())
}

// saveCentralAINewWindowMode writes the central new AI window mode, refusing
// a value that is not one of config.AINewWindowModes.
func saveCentralAINewWindowMode(homeDir func() (string, error), lookupEnv func(string) string, mode string) error {
	return config.SaveAINewWindowModeFile(config.DefaultPaths(aiConfigHome(homeDir, lookupEnv), "").AINewWindowModeFile(), mode)
}
