package app

import (
	"github.com/crevissepartners/projmux/internal/services/tmuxconfig"

	"github.com/crevissepartners/projmux/internal/config"
)

func desktopNotifyModeFromConfig(mode config.DesktopNotifyMode) desktopNotifyMode {
	return desktopNotifyMode(tmuxconfig.DesktopNotifyModeFromConfig(mode))
}

func desktopNotifyConfigValue(mode desktopNotifyMode) config.DesktopNotifyMode {
	return tmuxconfig.DesktopNotifyConfigValue(tmuxconfig.DesktopNotifyMode(mode))
}

func loadSavedDesktopNotifyMode(homeDir func() (string, error), lookupEnv func(string) string) (desktopNotifyMode, bool) {
	mode, ok := tmuxconfig.LoadSavedDesktopNotifyMode(homeDir, lookupEnv, configPaths)
	return desktopNotifyMode(mode), ok
}

// loadDesktopNotifyModeForTmuxConfig picks the value written into the
// generated tmux config. It only ever emits `off` or `notify`: the saved value
// is normalized through the two-state model, and an absent saved value takes
// the platform-neutral `notify` default (WSL + Windows Terminal included).
func loadDesktopNotifyModeForTmuxConfig(homeDir func() (string, error), lookupEnv func(string) string) config.DesktopNotifyMode {
	return tmuxconfig.LoadDesktopNotifyModeForTmuxConfig(homeDir, lookupEnv, configPaths)
}
