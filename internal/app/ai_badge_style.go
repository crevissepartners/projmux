package app

import (
	"fmt"
	"os"

	"github.com/crevissepartners/projmux/internal/config"
)

const aiBadgeStyleTmuxOption = "@projmux_ai_badge_style"

func loadAIBadgeStyle(homeDir func() (string, error), lookupEnv func(string) string) config.AIBadgeStyle {
	if homeDir == nil {
		return config.AIBadgeStyleDot
	}
	paths, err := statusbarConfigPaths(homeDir, lookupEnv)
	if err != nil {
		return config.AIBadgeStyleDot
	}
	mode, err := config.LoadAIBadgeStyleFile(paths.AIBadgeStyleFile())
	if err != nil {
		return config.AIBadgeStyleDot
	}
	return mode
}

func aiBadgeConfigPaths(homeDir func() (string, error), lookupEnv func(string) string) (config.Paths, error) {
	if homeDir == nil {
		homeDir = os.UserHomeDir
	}
	return statusbarConfigPaths(homeDir, lookupEnv)
}

func saveAIBadgeStyle(homeDir func() (string, error), lookupEnv func(string) string, value config.AIBadgeStyle) error {
	paths, err := aiBadgeConfigPaths(homeDir, lookupEnv)
	if err != nil {
		return fmt.Errorf("resolve AI badge style path: %w", err)
	}
	return config.SaveAIBadgeStyleFile(paths.AIBadgeStyleFile(), value)
}
