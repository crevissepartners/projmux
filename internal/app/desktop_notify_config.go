package app

import (
	"os"

	"github.com/crevissepartners/projmux/internal/config"
)

func desktopNotifyModeFromConfig(mode config.DesktopNotifyMode) desktopNotifyMode {
	if config.NormalizeDesktopNotifyMode(string(mode)) == config.DesktopNotifyModeOff {
		return desktopNotifyModeNone
	}
	return desktopNotifyModeNotify
}

func desktopNotifyConfigValue(mode desktopNotifyMode) config.DesktopNotifyMode {
	if mode == desktopNotifyModeNone {
		return config.DesktopNotifyModeOff
	}
	return config.DesktopNotifyModeNotify
}

func loadSavedDesktopNotifyMode(homeDir func() (string, error), lookupEnv func(string) string) (desktopNotifyMode, bool) {
	if homeDir == nil {
		return desktopNotifyModeNotify, false
	}
	paths, err := configPaths(homeDir, lookupEnv)
	if err != nil {
		return desktopNotifyModeNotify, false
	}
	path := paths.DesktopNotifyModeFile()
	if _, err := os.Stat(path); err != nil {
		return desktopNotifyModeNotify, false
	}
	mode, err := config.LoadDesktopNotifyModeFile(path)
	if err != nil {
		return desktopNotifyModeNotify, false
	}
	return desktopNotifyModeFromConfig(mode), true
}

// loadDesktopNotifyModeForTmuxConfig picks the value written into the
// generated tmux config. It only ever emits `off` or `notify`: the saved value
// is normalized through the two-state model, and an absent saved value takes
// the platform-neutral `notify` default (WSL + Windows Terminal included).
func loadDesktopNotifyModeForTmuxConfig(homeDir func() (string, error), lookupEnv func(string) string) config.DesktopNotifyMode {
	if mode, ok := loadSavedDesktopNotifyMode(homeDir, lookupEnv); ok {
		return desktopNotifyConfigValue(mode)
	}
	return config.DefaultDesktopNotifyMode
}
