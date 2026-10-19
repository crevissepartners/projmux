package tmuxconfig

import (
	"os"

	"github.com/crevissepartners/projmux/internal/config"
)

func DesktopNotifyModeFromConfig(mode config.DesktopNotifyMode) DesktopNotifyMode {
	if config.NormalizeDesktopNotifyMode(string(mode)) == config.DesktopNotifyModeOff {
		return DesktopNotifyModeNone
	}
	return DesktopNotifyModeNotify
}

func DesktopNotifyConfigValue(mode DesktopNotifyMode) config.DesktopNotifyMode {
	if mode == DesktopNotifyModeNone {
		return config.DesktopNotifyModeOff
	}
	return config.DesktopNotifyModeNotify
}

func LoadSavedDesktopNotifyMode(homeDir func() (string, error), lookupEnv func(string) string, resolvePaths PathsResolver) (DesktopNotifyMode, bool) {
	if homeDir == nil {
		return DesktopNotifyModeNotify, false
	}
	paths, err := resolvePaths(homeDir, lookupEnv)
	if err != nil {
		return DesktopNotifyModeNotify, false
	}
	path := paths.DesktopNotifyModeFile()
	if _, err := os.Stat(path); err != nil {
		return DesktopNotifyModeNotify, false
	}
	mode, err := config.LoadDesktopNotifyModeFile(path)
	if err != nil {
		return DesktopNotifyModeNotify, false
	}
	return DesktopNotifyModeFromConfig(mode), true
}

// loadDesktopNotifyModeForTmuxConfig picks the value written into the
// generated tmux config. It only ever emits `off` or `notify`: the saved value
// is normalized through the two-state model, and an absent saved value takes
// the platform-neutral `notify` default (WSL + Windows Terminal included).
func LoadDesktopNotifyModeForTmuxConfig(homeDir func() (string, error), lookupEnv func(string) string, resolvePaths PathsResolver) config.DesktopNotifyMode {
	if mode, ok := LoadSavedDesktopNotifyMode(homeDir, lookupEnv, resolvePaths); ok {
		return DesktopNotifyConfigValue(mode)
	}
	return config.DefaultDesktopNotifyMode
}

type DesktopNotifyMode string

const (
	DesktopNotifyModeNone   DesktopNotifyMode = "none"
	DesktopNotifyModeNotify DesktopNotifyMode = "notify"
)
