package tmuxconfig
import (
"github.com/crevissepartners/projmux/internal/config"

)

func DefaultStatusbarHUDVisibilitySet() StatusbarHUDVisibilitySet {
	return StatusbarHUDVisibilitySet{
		Notifications: config.StatusbarVisibilityOn,
		AgentUsage:    config.StatusbarVisibilityOn,
	}
}

func (s StatusbarHUDVisibilitySet) IsDefault() bool {
	return s.Mode(StatusbarHUDNotifications) == config.StatusbarVisibilityOn &&
		s.Mode(StatusbarHUDAgentUsage) == config.StatusbarVisibilityOn
}

func (s StatusbarHUDVisibilitySet) Mode(component StatusbarHUDComponent) config.StatusbarVisibility {
	switch component {
	case StatusbarHUDNotifications:
		return config.NormalizeStatusbarVisibility(string(s.Notifications))
	case StatusbarHUDAgentUsage:
		return config.NormalizeStatusbarVisibility(string(s.AgentUsage))
	default:
		return config.StatusbarVisibilityOn
	}
}

func (s StatusbarHUDVisibilitySet) Visible(component StatusbarHUDComponent) bool {
	return s.Mode(component) == config.StatusbarVisibilityOn
}

func (s StatusbarHUDVisibilitySet) AnyVisible() bool {
	return s.Visible(StatusbarHUDNotifications) || s.Visible(StatusbarHUDAgentUsage)
}

func StatusbarHUDVisibilityPath(paths config.Paths, component StatusbarHUDComponent) (string, bool) {
	switch component {
	case StatusbarHUDNotifications:
		return paths.StatusbarNotificationsHUDVisibilityFile(), true
	case StatusbarHUDAgentUsage:
		return paths.StatusbarAgentUsageHUDVisibilityFile(), true
	default:
		return "", false
	}
}

func LoadStatusbarHUDVisibilityState(homeDir func() (string, error), lookupEnv func(string) string, component StatusbarHUDComponent, resolvePaths PathsResolver) config.StatusbarVisibilityState {
	paths, err := resolvePaths(homeDir, lookupEnv)
	if err != nil {
		return config.DefaultStatusbarVisibilityState()
	}
	path, ok := StatusbarHUDVisibilityPath(paths, component)
	if !ok {
		return config.DefaultStatusbarVisibilityState()
	}
	state, err := config.LoadLayeredStatusbarVisibility(path, paths.StatusbarDefaultsFile(), config.StatusbarVisibilityOn)
	if err != nil {
		return config.DefaultStatusbarVisibilityState()
	}
	return state
}

func LoadStatusbarHUDVisibilitySet(homeDir func() (string, error), lookupEnv func(string) string, resolvePaths PathsResolver) StatusbarHUDVisibilitySet {
	return StatusbarHUDVisibilitySet{
		Notifications: LoadStatusbarHUDVisibilityState(homeDir, lookupEnv, StatusbarHUDNotifications, resolvePaths).Effective,
		AgentUsage:    LoadStatusbarHUDVisibilityState(homeDir, lookupEnv, StatusbarHUDAgentUsage, resolvePaths).Effective,
	}
}
type StatusbarHUDComponent string

const (
	StatusbarHUDNotifications StatusbarHUDComponent = "notifications-hud"
	StatusbarHUDAgentUsage    StatusbarHUDComponent = "agent-usage-hud"
)

type StatusbarHUDVisibilitySet struct {
	Notifications config.StatusbarVisibility
	AgentUsage    config.StatusbarVisibility
}

