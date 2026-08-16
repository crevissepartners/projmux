package app

import (
	"fmt"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/theme"
)

type statusbarRowOneComponent string

const (
	statusbarRowOneProject          statusbarRowOneComponent = "project"
	statusbarRowOneWorkingDirectory statusbarRowOneComponent = "working-directory"
	statusbarRowOneGit              statusbarRowOneComponent = "git"
	statusbarRowOneClock            statusbarRowOneComponent = "clock"
	statusbarRowOneSettingsLauncher statusbarRowOneComponent = "settings-launcher"
)

var statusbarRowOneComponents = []statusbarRowOneComponent{
	statusbarRowOneProject,
	statusbarRowOneWorkingDirectory,
	statusbarRowOneGit,
	statusbarRowOneClock,
	statusbarRowOneSettingsLauncher,
}

type statusbarRowOneVisibilitySet struct {
	Project          config.StatusbarVisibility
	WorkingDirectory config.StatusbarVisibility
	Git              config.StatusbarVisibility
	Clock            config.StatusbarVisibility
	SettingsLauncher config.StatusbarVisibility
}

func defaultStatusbarRowOneVisibilitySet() statusbarRowOneVisibilitySet {
	return statusbarRowOneVisibilitySet{
		Project:          config.StatusbarVisibilityOn,
		WorkingDirectory: config.StatusbarVisibilityOn,
		Git:              config.StatusbarVisibilityOn,
		Clock:            config.StatusbarVisibilityOn,
		SettingsLauncher: config.StatusbarVisibilityOn,
	}
}

func (s statusbarRowOneVisibilitySet) isDefault() bool {
	for _, component := range statusbarRowOneComponents {
		if !s.visible(component) {
			return false
		}
	}
	return true
}

func (s statusbarRowOneVisibilitySet) mode(component statusbarRowOneComponent) config.StatusbarVisibility {
	var mode config.StatusbarVisibility
	switch component {
	case statusbarRowOneProject:
		mode = s.Project
	case statusbarRowOneWorkingDirectory:
		mode = s.WorkingDirectory
	case statusbarRowOneGit:
		mode = s.Git
	case statusbarRowOneClock:
		mode = s.Clock
	case statusbarRowOneSettingsLauncher:
		mode = s.SettingsLauncher
	default:
		return config.StatusbarVisibilityOn
	}
	return config.NormalizeStatusbarVisibility(string(mode))
}

func (s statusbarRowOneVisibilitySet) visible(component statusbarRowOneComponent) bool {
	return s.mode(component) == config.StatusbarVisibilityOn
}

func statusbarRowOneVisibilityPath(paths config.Paths, component statusbarRowOneComponent) (string, bool) {
	switch component {
	case statusbarRowOneProject:
		return paths.StatusbarProjectVisibilityFile(), true
	case statusbarRowOneWorkingDirectory:
		return paths.StatusbarWorkingDirectoryVisibilityFile(), true
	case statusbarRowOneGit:
		return paths.StatusbarGitVisibilityFile(), true
	case statusbarRowOneClock:
		return paths.StatusbarClockVisibilityFile(), true
	case statusbarRowOneSettingsLauncher:
		return paths.StatusbarSettingsLauncherVisibilityFile(), true
	default:
		return "", false
	}
}

func loadStatusbarRowOneVisibilityState(homeDir func() (string, error), lookupEnv func(string) string, component statusbarRowOneComponent) config.StatusbarVisibilityState {
	paths, err := configPaths(homeDir, lookupEnv)
	if err != nil {
		return config.DefaultStatusbarVisibilityState()
	}
	path, ok := statusbarRowOneVisibilityPath(paths, component)
	if !ok {
		return config.DefaultStatusbarVisibilityState()
	}
	state, err := config.LoadStatusbarVisibilityFile(path)
	if err != nil {
		return config.DefaultStatusbarVisibilityState()
	}
	return state
}

func loadStatusbarRowOneVisibilitySet(homeDir func() (string, error), lookupEnv func(string) string) statusbarRowOneVisibilitySet {
	return statusbarRowOneVisibilitySet{
		Project:          loadStatusbarRowOneVisibilityState(homeDir, lookupEnv, statusbarRowOneProject).Effective,
		WorkingDirectory: loadStatusbarRowOneVisibilityState(homeDir, lookupEnv, statusbarRowOneWorkingDirectory).Effective,
		Git:              loadStatusbarRowOneVisibilityState(homeDir, lookupEnv, statusbarRowOneGit).Effective,
		Clock:            loadStatusbarRowOneVisibilityState(homeDir, lookupEnv, statusbarRowOneClock).Effective,
		SettingsLauncher: loadStatusbarRowOneVisibilityState(homeDir, lookupEnv, statusbarRowOneSettingsLauncher).Effective,
	}
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
	if !visibility.visible(statusbarRowOneProject) {
		return ""
	}
	return statusbarSessionLeftFormat(bin, roles)
}

// statusbarRowOneRightFormat assembles only enabled Projmux-owned segments.
// Each optional segment owns its range and padding, so removing it cannot leave
// an empty click range or a separator that belonged to the hidden component.
func statusbarRowOneRightFormat(bin string, roles theme.RenderRoles, liveResourcesMode config.LiveResourcesMode, visibility statusbarRowOneVisibilitySet, settingsLabel string) string {
	var b strings.Builder
	if visibility.visible(statusbarRowOneWorkingDirectory) {
		b.WriteString(statusbarCwdSegmentFormat(roles))
		b.WriteString("#[fg=" + roles.DividerFg + "]  ")
	}
	if visibility.visible(statusbarRowOneGit) {
		b.WriteString("#[range=user|git]#(" + bin + " internal status git)#[norange]")
	}
	if config.NormalizeLiveResourcesMode(string(liveResourcesMode)) == config.LiveResourcesOn {
		b.WriteString("#[range=user|resources]#(" + bin + " internal status resources)#[norange]")
	}
	if visibility.visible(statusbarRowOneClock) {
		b.WriteString("#[fg=" + roles.StatusTextSecondary + "]   %Y-%m-%d %H:%M ")
	}
	if visibility.visible(statusbarRowOneSettingsLauncher) {
		b.WriteString(statusbarSettingsButton(settingsLabel, roles))
	}
	return b.String()
}
