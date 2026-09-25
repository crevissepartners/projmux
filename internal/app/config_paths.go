package app

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/crevissepartners/projmux/internal/config"
	intrender "github.com/crevissepartners/projmux/internal/ui/render"
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

// configFileDisplayPath is where Settings says a central config file lives:
// the file under the resolved config home (XDG_CONFIG_HOME, else ~/.config),
// shortened to ~/... when it sits under $HOME and absolute otherwise. Without a
// config home to resolve it keeps the default ~/.config/projmux spelling.
func configFileDisplayPath(homeDir func() (string, error), lookupEnv func(string) string, name string) string {
	if homeDir == nil {
		homeDir = os.UserHomeDir
	}
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	home, err := homeDir()
	if err != nil {
		home = ""
	}
	configHome, err := config.ResolveConfigHome(home, lookupEnv("XDG_CONFIG_HOME"))
	if err != nil {
		return "~/.config/" + config.AppName + "/" + name
	}
	return intrender.PrettyPath(filepath.Join(configHome, config.AppName, name), home, "")
}
