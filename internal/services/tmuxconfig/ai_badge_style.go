package tmuxconfig

import (
	"github.com/crevissepartners/projmux/internal/config"
)

func LoadAIBadgeStyle(homeDir func() (string, error), lookupEnv func(string) string, resolvePaths PathsResolver) config.AIBadgeStyle {
	if homeDir == nil {
		return config.AIBadgeStyleDot
	}
	paths, err := resolvePaths(homeDir, lookupEnv)
	if err != nil {
		return config.AIBadgeStyleDot
	}
	mode, err := config.LoadAIBadgeStyleFile(paths.AIBadgeStyleFile())
	if err != nil {
		return config.AIBadgeStyleDot
	}
	return mode
}
