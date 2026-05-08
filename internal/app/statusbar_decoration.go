package app

import (
	"fmt"
	"os"

	"github.com/crevissepartners/projmux/internal/config"
)

const statusbarDecorationTmuxOption = "@projmux_statusbar_decoration"

func statusbarConfigPaths(homeDir func() (string, error), lookupEnv func(string) string) (config.Paths, error) {
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

func loadStatusbarDecoration(homeDir func() (string, error), lookupEnv func(string) string) config.StatusbarDecoration {
	if homeDir == nil {
		return config.StatusbarDecorationOff
	}
	paths, err := statusbarConfigPaths(homeDir, lookupEnv)
	if err != nil {
		return config.StatusbarDecorationOff
	}
	mode, err := config.LoadStatusbarDecorationFile(paths.StatusbarDecorationFile())
	if err != nil {
		return config.StatusbarDecorationOff
	}
	return mode
}

func statusbarCwdSegmentFormat() string {
	return "#[range=user|pwd]" + statusbarCwdDecoratorFormat() + "#[fg=colour250]#{=-28/...:pane_current_path}#[norange]"
}

func statusbarCwdDecoratorFormat() string {
	return "#{?#{==:#{@projmux_statusbar_decoration},symbol},#[fg=colour244] ,#{?#{==:#{@projmux_statusbar_decoration},emoji},#[fg=colour244]📁 ,}}"
}

func statusbarGitDecorator(mode config.StatusbarDecoration) string {
	switch mode {
	case config.StatusbarDecorationSymbol:
		return "#[fg=colour17] #[fg=colour16]"
	case config.StatusbarDecorationEmoji:
		return "#[fg=colour17]🌿 #[fg=colour16]"
	default:
		return ""
	}
}
