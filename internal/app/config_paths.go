package app

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/crevissepartners/projmux/internal/config"
)

func configPaths(homeDir func() (string, error), lookupEnv func(string) string) (config.Paths, error) {
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

// generatedAppConfigDefaultPath is the one default location of the generated
// app tmux config. Apply writes it and create hands it to `tmux -f`, so both
// resolve it here and XDG_CONFIG_HOME moves them together.
func generatedAppConfigDefaultPath(homeDir func() (string, error), lookupEnv func(string) string) (string, error) {
	paths, err := configPaths(homeDir, lookupEnv)
	if err != nil {
		return "", err
	}
	return filepath.Join(paths.ConfigDir, "tmux.conf"), nil
}
