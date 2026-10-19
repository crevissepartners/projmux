package app

import (
	"fmt"
	"github.com/crevissepartners/projmux/internal/services/tmuxconfig"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/theme"
)

type statusbarRowOneComponent tmuxconfig.StatusbarRowOneComponent

const (
	statusbarRowOneProject          statusbarRowOneComponent = statusbarRowOneComponent(tmuxconfig.StatusbarRowOneProject)
	statusbarRowOneWorkingDirectory statusbarRowOneComponent = statusbarRowOneComponent(tmuxconfig.StatusbarRowOneWorkingDirectory)
	statusbarRowOneGit              statusbarRowOneComponent = statusbarRowOneComponent(tmuxconfig.StatusbarRowOneGit)
	statusbarRowOneClock            statusbarRowOneComponent = statusbarRowOneComponent(tmuxconfig.StatusbarRowOneClock)
	statusbarRowOneSettingsLauncher statusbarRowOneComponent = statusbarRowOneComponent(tmuxconfig.StatusbarRowOneSettingsLauncher)
)

var statusbarRowOneComponents = []statusbarRowOneComponent{
	statusbarRowOneProject,
	statusbarRowOneWorkingDirectory,
	statusbarRowOneGit,
	statusbarRowOneClock,
	statusbarRowOneSettingsLauncher,
}

type statusbarRowOneVisibilitySet tmuxconfig.StatusbarRowOneVisibilitySet

func defaultStatusbarRowOneVisibilitySet() statusbarRowOneVisibilitySet {
	return statusbarRowOneVisibilitySet(tmuxconfig.DefaultStatusbarRowOneVisibilitySet())
}

func (s statusbarRowOneVisibilitySet) isDefault() bool {
	return tmuxconfig.StatusbarRowOneVisibilitySet(s).IsDefault()
}

func (s statusbarRowOneVisibilitySet) visible(component statusbarRowOneComponent) bool {
	return tmuxconfig.StatusbarRowOneVisibilitySet(s).Visible(tmuxconfig.StatusbarRowOneComponent(component))
}

func statusbarRowOneVisibilityPath(paths config.Paths, component statusbarRowOneComponent) (string, bool) {
	return tmuxconfig.StatusbarRowOneVisibilityPath(paths, tmuxconfig.StatusbarRowOneComponent(component))
}

func loadStatusbarRowOneVisibilityState(homeDir func() (string, error), lookupEnv func(string) string, component statusbarRowOneComponent) config.StatusbarVisibilityState {
	return tmuxconfig.LoadStatusbarRowOneVisibilityState(homeDir, lookupEnv, tmuxconfig.StatusbarRowOneComponent(component), configPaths)
}

func loadStatusbarRowOneVisibilitySet(homeDir func() (string, error), lookupEnv func(string) string) statusbarRowOneVisibilitySet {
	return statusbarRowOneVisibilitySet(tmuxconfig.LoadStatusbarRowOneVisibilitySet(homeDir, lookupEnv, configPaths))
}

func statusbarRowOneComponentName(component statusbarRowOneComponent) string {
	switch component {
	case statusbarRowOneProject:
		return "Project"
	case statusbarRowOneWorkingDirectory:
		return "Working directory"
	case statusbarRowOneGit:
		return "Git"
	case statusbarRowOneClock:
		return "Clock"
	case statusbarRowOneSettingsLauncher:
		return "Settings launcher"
	default:
		return "Status Bar component"
	}
}

func parseStatusbarRowOneVisibilityAction(value string) (statusbarRowOneComponent, config.StatusbarVisibility, bool) {
	componentText, modeText, ok := strings.Cut(strings.TrimSpace(value), ":")
	if !ok {
		return "", "", false
	}
	component := statusbarRowOneComponent(strings.TrimSpace(componentText))
	if _, ok := statusbarRowOneVisibilityPath(config.Paths{}, component); !ok {
		return "", "", false
	}
	modeText = strings.ToLower(strings.TrimSpace(modeText))
	if modeText != string(config.StatusbarVisibilityOn) && modeText != string(config.StatusbarVisibilityOff) {
		return "", "", false
	}
	return component, config.StatusbarVisibility(modeText), true
}

func (c *settingsCommand) setStatusbarRowOneVisibility(component statusbarRowOneComponent, mode config.StatusbarVisibility) error {
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return err
	}
	path, ok := statusbarRowOneVisibilityPath(paths, component)
	if !ok {
		return fmt.Errorf("unknown status bar row one component: %s", component)
	}
	if err := config.SaveStatusbarVisibilityFile(path, mode); err != nil {
		return err
	}
	prepared, live, err := c.regenerateAndReloadTmuxConfig()
	if err != nil {
		if prepared.Status == keymapApplyFailed {
			return fmt.Errorf("update status bar runtime config: %w", err)
		}
		if live.Status == keymapApplyFailed {
			return fmt.Errorf("reload active status bar: %w", err)
		}
		return err
	}
	return nil
}

func statusbarRowOneProjectFormat(bin string, roles theme.RenderRoles, visibility statusbarRowOneVisibilitySet) string {
	return tmuxconfig.StatusbarRowOneProjectFormat(bin, roles, tmuxconfig.StatusbarRowOneVisibilitySet(visibility))
}

// statusbarRowOneRightFormat assembles only enabled Projmux-owned segments.
// Each optional segment owns its range and padding, so removing it cannot leave
// an empty click range or a separator that belonged to the hidden component.
func statusbarRowOneRightFormat(bin string, roles theme.RenderRoles, liveResourcesMode config.LiveResourcesMode, visibility statusbarRowOneVisibilitySet, settingsLabel string) string {
	return tmuxconfig.StatusbarRowOneRightFormat(bin, roles, liveResourcesMode, tmuxconfig.StatusbarRowOneVisibilitySet(visibility), settingsLabel)
}
