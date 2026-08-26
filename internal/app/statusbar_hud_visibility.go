package app

import (
	"fmt"
	"strings"

	"github.com/crevissepartners/projmux/internal/app/usagecmd"
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

type agentUsageVisibilityLeaf struct {
	provider string
	window   string
}

const (
	agentUsageProviderVisibilityAction = "agent-usage-provider"
	agentUsageWindowVisibilityAction   = "agent-usage-window"
)

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

func agentUsageProviderCapability(provider string) (usagecmd.HUDProviderCapability, bool) {
	for _, capability := range usagecmd.HUDProviderCapabilities() {
		if strings.EqualFold(string(capability.ID), strings.TrimSpace(provider)) {
			return capability, true
		}
	}
	return usagecmd.HUDProviderCapability{}, false
}

func agentUsageWindowCapability(provider, window string) (usagecmd.HUDWindowCapability, bool) {
	capability, ok := agentUsageProviderCapability(provider)
	if !ok {
		return usagecmd.HUDWindowCapability{}, false
	}
	for _, candidate := range capability.Windows {
		if strings.EqualFold(candidate.Key, strings.TrimSpace(window)) {
			return candidate, true
		}
	}
	return usagecmd.HUDWindowCapability{}, false
}

func agentUsageVisibilityPath(paths config.Paths, leaf agentUsageVisibilityLeaf) (string, bool) {
	provider, ok := agentUsageProviderCapability(leaf.provider)
	if !ok {
		return "", false
	}
	if strings.TrimSpace(leaf.window) == "" {
		return paths.StatusbarAgentUsageProviderVisibilityFile(string(provider.ID)), true
	}
	window, ok := agentUsageWindowCapability(string(provider.ID), leaf.window)
	if !ok {
		return "", false
	}
	return paths.StatusbarAgentUsageWindowVisibilityFile(string(provider.ID), window.Key), true
}

func loadAgentUsageVisibilityState(homeDir func() (string, error), lookupEnv func(string) string, leaf agentUsageVisibilityLeaf) config.StatusbarVisibilityState {
	defaultState := config.DefaultStatusbarVisibilityState()
	window, hasWindow := agentUsageWindowCapability(leaf.provider, leaf.window)
	if strings.TrimSpace(leaf.window) != "" && hasWindow {
		defaultState.Effective = config.NormalizeStatusbarVisibility(string(window.DefaultVisibility))
	}
	paths, err := configPaths(homeDir, lookupEnv)
	if err != nil {
		return defaultState
	}
	path, ok := agentUsageVisibilityPath(paths, leaf)
	if !ok {
		return defaultState
	}
	var state config.StatusbarVisibilityState
	if hasWindow {
		state, err = config.LoadStatusbarVisibilityFileWithDefault(path, window.DefaultVisibility)
	} else {
		state, err = config.LoadStatusbarVisibilityFile(path)
	}
	if err != nil {
		return defaultState
	}
	return state
}

func gatedStatusbarVisibility(state config.StatusbarVisibilityState, parents ...config.StatusbarVisibilityState) config.StatusbarVisibilityState {
	effective := state
	for _, parent := range parents {
		if parent.Effective == config.StatusbarVisibilityOff {
			effective.Effective = config.StatusbarVisibilityOff
			break
		}
	}
	return effective
}

func parseAgentUsageVisibilityAction(value string) (agentUsageVisibilityLeaf, config.StatusbarVisibility, bool) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) == 3 && parts[0] == agentUsageProviderVisibilityAction {
		if _, ok := agentUsageProviderCapability(parts[1]); !ok {
			return agentUsageVisibilityLeaf{}, "", false
		}
		mode := config.StatusbarVisibility(strings.ToLower(strings.TrimSpace(parts[2])))
		if mode != config.StatusbarVisibilityOn && mode != config.StatusbarVisibilityOff {
			return agentUsageVisibilityLeaf{}, "", false
		}
		return agentUsageVisibilityLeaf{provider: strings.ToLower(strings.TrimSpace(parts[1]))}, mode, true
	}
	if len(parts) == 4 && parts[0] == agentUsageWindowVisibilityAction {
		if _, ok := agentUsageWindowCapability(parts[1], parts[2]); !ok {
			return agentUsageVisibilityLeaf{}, "", false
		}
		mode := config.StatusbarVisibility(strings.ToLower(strings.TrimSpace(parts[3])))
		if mode != config.StatusbarVisibilityOn && mode != config.StatusbarVisibilityOff {
			return agentUsageVisibilityLeaf{}, "", false
		}
		return agentUsageVisibilityLeaf{provider: strings.ToLower(strings.TrimSpace(parts[1])), window: strings.ToLower(strings.TrimSpace(parts[2]))}, mode, true
	}
	return agentUsageVisibilityLeaf{}, "", false
}

func (c *settingsCommand) setAgentUsageVisibility(leaf agentUsageVisibilityLeaf, mode config.StatusbarVisibility) error {
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return err
	}
	path, ok := agentUsageVisibilityPath(paths, leaf)
	if !ok {
		return fmt.Errorf("unknown Agent Usage HUD visibility leaf: %s/%s", leaf.provider, leaf.window)
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
