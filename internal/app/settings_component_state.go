package app

import (
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/services/settings"
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
	state, err := config.LoadLiveResourcesStateFile(paths.LiveResourcesFile())
	if err != nil {
		return config.LiveResourcesOff, "default", true
	}
	source := string(state.Source)
	if state.Invalid != "" {
		source += " (invalid saved value ignored)"
	}
	return state.Effective, source, true
}

func (c *settingsCommand) setLiveResourcesMode(value string) error {
	_, err := (settings.StatusbarResources{
		Supported: systemstatus.Supported,
		Paths:     func() (config.Paths, error) { return configPaths(c.homeDir, c.lookupEnv) },
		Apply:     c.regenerateAndReloadTmuxConfig,
	}).Set(value)
	return err
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
