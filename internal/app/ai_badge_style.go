package app

import (
"github.com/crevissepartners/projmux/internal/services/tmuxconfig"
	"fmt"
	"os"

	"github.com/crevissepartners/projmux/internal/config"
)

const aiBadgeStyleTmuxOption = tmuxconfig.AiBadgeStyleTmuxOption

func loadAIBadgeStyle(homeDir func() (string, error), lookupEnv func(string) string) config.AIBadgeStyle {
return tmuxconfig.LoadAIBadgeStyle(homeDir, lookupEnv, configPaths)
}

func aiBadgeConfigPaths(homeDir func() (string, error), lookupEnv func(string) string) (config.Paths, error) {
	if homeDir == nil {
		homeDir = os.UserHomeDir
	}
	return configPaths(homeDir, lookupEnv)
}

func saveAIBadgeStyle(homeDir func() (string, error), lookupEnv func(string) string, value config.AIBadgeStyle) error {
	paths, err := aiBadgeConfigPaths(homeDir, lookupEnv)
	if err != nil {
		return fmt.Errorf("resolve AI badge style path: %w", err)
	}
	return config.SaveAIBadgeStyleFile(paths.AIBadgeStyleFile(), value)
}
