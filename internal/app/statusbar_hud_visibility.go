package app

import (
	"fmt"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
)

type statusbarHUDComponent string

const (
	statusbarHUDNotifications statusbarHUDComponent = "notifications-hud"
	statusbarHUDAgentUsage    statusbarHUDComponent = "agent-usage-hud"
)

type statusbarHUDVisibilitySet struct {
	Notifications config.StatusbarVisibility
	AgentUsage    config.StatusbarVisibility
}

func defaultStatusbarHUDVisibilitySet() statusbarHUDVisibilitySet {
	return statusbarHUDVisibilitySet{
		Notifications: config.StatusbarVisibilityOn,
		AgentUsage:    config.StatusbarVisibilityOn,
	}
}

func (s statusbarHUDVisibilitySet) isDefault() bool {
	return s.mode(statusbarHUDNotifications) == config.StatusbarVisibilityOn &&
		s.mode(statusbarHUDAgentUsage) == config.StatusbarVisibilityOn
}

func (s statusbarHUDVisibilitySet) mode(component statusbarHUDComponent) config.StatusbarVisibility {
	switch component {
	case statusbarHUDNotifications:
		return config.NormalizeStatusbarVisibility(string(s.Notifications))
	case statusbarHUDAgentUsage:
		return config.NormalizeStatusbarVisibility(string(s.AgentUsage))
	default:
		return config.StatusbarVisibilityOn
	}
}

func (s statusbarHUDVisibilitySet) visible(component statusbarHUDComponent) bool {
	return s.mode(component) == config.StatusbarVisibilityOn
}

func (s statusbarHUDVisibilitySet) anyVisible() bool {
	return s.visible(statusbarHUDNotifications) || s.visible(statusbarHUDAgentUsage)
}

func statusbarHUDVisibilityPath(paths config.Paths, component statusbarHUDComponent) (string, bool) {
	switch component {
	case statusbarHUDNotifications:
		return paths.StatusbarNotificationsHUDVisibilityFile(), true
	case statusbarHUDAgentUsage:
		return paths.StatusbarAgentUsageHUDVisibilityFile(), true
	default:
		return "", false
	}
}

func loadStatusbarHUDVisibilityState(homeDir func() (string, error), lookupEnv func(string) string, component statusbarHUDComponent) config.StatusbarVisibilityState {
	paths, err := configPaths(homeDir, lookupEnv)
	if err != nil {
		return config.DefaultStatusbarVisibilityState()
	}
	path, ok := statusbarHUDVisibilityPath(paths, component)
	if !ok {
		return config.DefaultStatusbarVisibilityState()
	}
	state, err := config.LoadStatusbarVisibilityFile(path)
	if err != nil {
		return config.DefaultStatusbarVisibilityState()
	}
	return state
}

func loadStatusbarHUDVisibilitySet(homeDir func() (string, error), lookupEnv func(string) string) statusbarHUDVisibilitySet {
	return statusbarHUDVisibilitySet{
		Notifications: loadStatusbarHUDVisibilityState(homeDir, lookupEnv, statusbarHUDNotifications).Effective,
		AgentUsage:    loadStatusbarHUDVisibilityState(homeDir, lookupEnv, statusbarHUDAgentUsage).Effective,
	}
}

func statusbarHUDComponentName(component statusbarHUDComponent) string {
	switch component {
	case statusbarHUDNotifications:
		return "Notifications HUD"
	case statusbarHUDAgentUsage:
		return "Agent Usage HUD"
	default:
		return "Status Bar HUD"
	}
}

func parseStatusbarHUDVisibilityAction(value string) (statusbarHUDComponent, config.StatusbarVisibility, bool) {
	componentText, modeText, ok := strings.Cut(strings.TrimSpace(value), ":")
	if !ok {
		return "", "", false
	}
	component := statusbarHUDComponent(strings.TrimSpace(componentText))
	if component != statusbarHUDNotifications && component != statusbarHUDAgentUsage {
		return "", "", false
	}
	modeText = strings.ToLower(strings.TrimSpace(modeText))
	if modeText != string(config.StatusbarVisibilityOn) && modeText != string(config.StatusbarVisibilityOff) {
		return "", "", false
	}
	return component, config.StatusbarVisibility(modeText), true
}

func (c *settingsCommand) setStatusbarHUDVisibility(component statusbarHUDComponent, mode config.StatusbarVisibility) error {
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return err
	}
	path, ok := statusbarHUDVisibilityPath(paths, component)
	if !ok {
		return fmt.Errorf("unknown status bar HUD component: %s", component)
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
