package app

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
)

const (
	defaultAINotifyDedupeSeconds = 120
	aiNotifyDedupeSecondsEnv     = "PROJMUX_TMUX_NOTIFY_DEDUPE_SECONDS"
)

type aiNotifyDedupeSource string

const (
	aiNotifyDedupeSourceEnv     aiNotifyDedupeSource = "env"
	aiNotifyDedupeSourceSetting aiNotifyDedupeSource = "setting"
	aiNotifyDedupeSourceDefault aiNotifyDedupeSource = "default"
)

type aiNotifyDedupeResolution struct {
	Seconds int
	Source  aiNotifyDedupeSource
}

func resolveAINotifyDedupeSeconds(homeDir func() (string, error), lookupEnv func(string) string) aiNotifyDedupeResolution {
	if lookupEnv != nil {
		if seconds := parsePositiveInt(lookupEnv(aiNotifyDedupeSecondsEnv)); seconds > 0 {
			return aiNotifyDedupeResolution{Seconds: seconds, Source: aiNotifyDedupeSourceEnv}
		}
	}
	paths, err := pickerBackendConfigPaths(homeDir, lookupEnv)
	if err == nil {
		seconds, err := config.LoadAINotifyDedupeSecondsFileDefault(paths.AINotifyDedupeSecondsFile(), defaultAINotifyDedupeSeconds)
		if err == nil {
			if _, statErr := osStat(paths.AINotifyDedupeSecondsFile()); statErr == nil {
				return aiNotifyDedupeResolution{Seconds: seconds, Source: aiNotifyDedupeSourceSetting}
			}
			return aiNotifyDedupeResolution{Seconds: seconds, Source: aiNotifyDedupeSourceDefault}
		}
	}
	return aiNotifyDedupeResolution{Seconds: defaultAINotifyDedupeSeconds, Source: aiNotifyDedupeSourceDefault}
}

func (c *aiCommand) aiNotifyDedupeSeconds() int {
	return resolveAINotifyDedupeSeconds(c.homeDir, c.lookupEnv).Seconds
}

func (c *settingsCommand) currentAINotifyDedupeSeconds() aiNotifyDedupeResolution {
	return resolveAINotifyDedupeSeconds(c.homeDir, c.lookupEnv)
}

func (c *settingsCommand) setAINotifyDedupeSeconds(seconds int, stdout io.Writer) error {
	if seconds <= 0 {
		return fmt.Errorf("AI notify dedupe seconds must be positive")
	}
	paths, err := pickerBackendConfigPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return err
	}
	if err := config.SaveAINotifyDedupeSecondsFile(paths.AINotifyDedupeSecondsFile(), seconds); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "AI notification dedupe: %ds\n", seconds); err != nil {
		return err
	}
	if c.lookupEnv != nil && strings.TrimSpace(c.lookupEnv("TMUX")) != "" && c.runCommand != nil {
		_ = c.runCommand("tmux", "display-message", "AI notification dedupe: "+strconv.Itoa(seconds)+"s")
	}
	return nil
}
