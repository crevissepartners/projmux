package app

import (
	"fmt"

	"github.com/crevissepartners/projmux/internal/config"
)

// aiConfigHome is the config home the AI mode files live under, as
// config.ResolveConfigHome resolves it: XDG_CONFIG_HOME, else the home
// directory's .config. With neither it returns a *config.MissingHomeError,
// never a path relative to the working directory. A nil resolver and a blank
// value count as unset.
func aiConfigHome(homeDir func() (string, error), lookupEnv func(string) string) (string, error) {
	if lookupEnv == nil {
		lookupEnv = func(string) string { return "" }
	}
	if configHome, err := config.ResolveConfigHome("", lookupEnv("XDG_CONFIG_HOME")); err == nil {
		return configHome, nil
	}
	return config.ResolveConfigHome(resolvedHome(homeDir), lookupEnv("XDG_CONFIG_HOME"))
}

// loadCentralAINewWindowMode reads the central new AI window mode. It is a
// central read, so it reports no front read.
// Without a config home there is nothing saved to read, so it reports the
// mode unset and touches no file.
func loadCentralAINewWindowMode(homeDir func() (string, error), lookupEnv func(string) string) (mode string, saved bool, err error) {
	configHome, err := aiConfigHome(homeDir, lookupEnv)
	if err != nil {
		return "", false, nil
	}
	return config.LoadAINewWindowModeFile(config.DefaultPaths(configHome, "").AINewWindowModeFile())
}

// saveCentralAINewWindowMode writes the central new AI window mode, refusing
// a value that is not one of config.AINewWindowModes, and without a config
// home it writes nothing and returns the missing-HOME reason.
func saveCentralAINewWindowMode(homeDir func() (string, error), lookupEnv func(string) string, mode string) error {
	configHome, err := aiConfigHome(homeDir, lookupEnv)
	if err != nil {
		return fmt.Errorf("save new AI window mode: %w", err)
	}
	return config.SaveAINewWindowModeFile(config.DefaultPaths(configHome, "").AINewWindowModeFile(), mode)
}
