package app

import (
	"os"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
)

func desktopNotifyModeFromConfig(mode config.DesktopNotifyMode) desktopNotifyMode {
	switch config.NormalizeDesktopNotifyMode(string(mode)) {
	case config.DesktopNotifyModeOff:
		return desktopNotifyModeNone
	case config.DesktopNotifyModeRaise:
		return desktopNotifyModeRaise
	default:
		return desktopNotifyModeNotify
	}
}

func desktopNotifyConfigValue(mode desktopNotifyMode) config.DesktopNotifyMode {
	switch mode {
	case desktopNotifyModeNone:
		return config.DesktopNotifyModeOff
	case desktopNotifyModeRaise:
		return config.DesktopNotifyModeRaise
	default:
		return config.DesktopNotifyModeNotify
	}
}

func defaultDesktopNotifyConfigValue(lookupEnv func(string) string) config.DesktopNotifyMode {
	if lookupEnv != nil &&
		strings.TrimSpace(lookupEnv("WSL_DISTRO_NAME")) != "" &&
		strings.TrimSpace(lookupEnv("WT_SESSION")) != "" {
		return config.DesktopNotifyModeRaise
	}
	return config.DefaultDesktopNotifyMode
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

func loadDesktopNotifyModeForTmuxConfig(homeDir func() (string, error), lookupEnv func(string) string) config.DesktopNotifyMode {
	if mode, ok := loadSavedDesktopNotifyMode(homeDir, lookupEnv); ok {
		return desktopNotifyConfigValue(mode)
	}
	return defaultDesktopNotifyConfigValue(lookupEnv)
}
