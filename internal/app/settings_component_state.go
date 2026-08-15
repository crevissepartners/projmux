package app

import (
	"fmt"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/systemstatus"
)

// The Labs container is retired. Its two members were promoted to stable
// destinations: `Live system resources` is now `Appearance > Status Bar >
// Resources`, and the project hook execution gate is now `Automation > Project
// automation policy`. The state helpers below are the ones those destinations
// reuse; the saved files and their spellings are unchanged.

func (c *settingsCommand) currentLiveResourcesMode() (config.LiveResourcesMode, string, bool) {
	if !systemstatus.Supported() {
		return config.LiveResourcesOff, "unsupported platform", false
	}
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return config.LiveResourcesOff, "default", true
	}
	mode, err := config.LoadLiveResourcesFile(paths.LiveResourcesFile())
	if err != nil {
		return config.LiveResourcesOff, "default", true
	}
	if _, err := c.statFile(paths.LiveResourcesFile()); err == nil {
		return mode, "saved", true
	}
	return mode, "default", true
}

func (c *settingsCommand) setLiveResourcesMode(value string) error {
	if !systemstatus.Supported() {
		return fmt.Errorf("live system resources are unavailable on this platform")
	}
	mode := config.NormalizeLiveResourcesMode(value)
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return err
	}
	if err := config.SaveLiveResourcesFile(paths.LiveResourcesFile(), mode); err != nil {
		return err
	}
	if c.lookupEnv != nil && strings.TrimSpace(c.lookupEnv("TMUX")) != "" && c.runCommand != nil {
		if err := c.runCommand("tmux", "set-option", "-g", liveResourcesTmuxOption, string(mode)); err != nil {
			return fmt.Errorf("set live tmux resource status: %w", err)
		}
	}
	return nil
}

func (c *settingsCommand) currentProjectHooksMode() (config.ProjectHooksMode, string) {
	if c.lookupEnv != nil && strings.EqualFold(strings.TrimSpace(c.lookupEnv("PROJMUX_PROJECT_HOOKS")), string(config.ProjectHooksOff)) {
		return config.ProjectHooksOff, "PROJMUX_PROJECT_HOOKS env"
	}
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return config.ProjectHooksOn, "default"
	}
	mode, err := config.LoadProjectHooksFile(paths.ProjectHooksFile())
	if err != nil {
		return config.ProjectHooksOn, "default"
	}
	if _, err := c.statFile(paths.ProjectHooksFile()); err == nil {
		return mode, "saved"
	}
	return mode, "default"
}

func (c *settingsCommand) setProjectHooksMode(value string) error {
	mode := config.NormalizeProjectHooksMode(value)
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return err
	}
	if err := config.SaveProjectHooksFile(paths.ProjectHooksFile(), mode); err != nil {
		return err
	}
	if c.lookupEnv != nil && strings.TrimSpace(c.lookupEnv("TMUX")) != "" && c.runCommand != nil {
		_ = c.runCommand("tmux", "display-message", "project hooks: "+string(mode))
	}
	return nil
}
