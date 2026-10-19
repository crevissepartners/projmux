package tmuxconfig
import (
"github.com/crevissepartners/projmux/internal/config"
"strings"
"github.com/crevissepartners/projmux/internal/theme"
)

func DefaultStatusbarRowOneVisibilitySet() StatusbarRowOneVisibilitySet {
	return StatusbarRowOneVisibilitySet{
		Project:          config.StatusbarVisibilityOn,
		WorkingDirectory: config.StatusbarVisibilityOn,
		Git:              config.StatusbarVisibilityOn,
		Clock:            config.StatusbarVisibilityOn,
		SettingsLauncher: config.StatusbarVisibilityOn,
	}
}

func (s StatusbarRowOneVisibilitySet) IsDefault() bool {
	for _, component := range statusbarRowOneComponents {
		if !s.Visible(component) {
			return false
		}
	}
	return true
}

func (s StatusbarRowOneVisibilitySet) Mode(component StatusbarRowOneComponent) config.StatusbarVisibility {
	var mode config.StatusbarVisibility
	switch component {
	case StatusbarRowOneProject:
		mode = s.Project
	case StatusbarRowOneWorkingDirectory:
		mode = s.WorkingDirectory
	case StatusbarRowOneGit:
		mode = s.Git
	case StatusbarRowOneClock:
		mode = s.Clock
	case StatusbarRowOneSettingsLauncher:
		mode = s.SettingsLauncher
	default:
		return config.StatusbarVisibilityOn
	}
	return config.NormalizeStatusbarVisibility(string(mode))
}

func (s StatusbarRowOneVisibilitySet) Visible(component StatusbarRowOneComponent) bool {
	return s.Mode(component) == config.StatusbarVisibilityOn
}

func StatusbarRowOneVisibilityPath(paths config.Paths, component StatusbarRowOneComponent) (string, bool) {
	switch component {
	case StatusbarRowOneProject:
		return paths.StatusbarProjectVisibilityFile(), true
	case StatusbarRowOneWorkingDirectory:
		return paths.StatusbarWorkingDirectoryVisibilityFile(), true
	case StatusbarRowOneGit:
		return paths.StatusbarGitVisibilityFile(), true
	case StatusbarRowOneClock:
		return paths.StatusbarClockVisibilityFile(), true
	case StatusbarRowOneSettingsLauncher:
		return paths.StatusbarSettingsLauncherVisibilityFile(), true
	default:
		return "", false
	}
}

func LoadStatusbarRowOneVisibilityState(homeDir func() (string, error), lookupEnv func(string) string, component StatusbarRowOneComponent, resolvePaths PathsResolver) config.StatusbarVisibilityState {
	paths, err := resolvePaths(homeDir, lookupEnv)
	if err != nil {
		return config.DefaultStatusbarVisibilityState()
	}
	path, ok := StatusbarRowOneVisibilityPath(paths, component)
	if !ok {
		return config.DefaultStatusbarVisibilityState()
	}
	state, err := config.LoadLayeredStatusbarVisibility(path, paths.StatusbarDefaultsFile(), config.StatusbarVisibilityOn)
	if err != nil {
		return config.DefaultStatusbarVisibilityState()
	}
	return state
}

func LoadStatusbarRowOneVisibilitySet(homeDir func() (string, error), lookupEnv func(string) string, resolvePaths PathsResolver) StatusbarRowOneVisibilitySet {
	return StatusbarRowOneVisibilitySet{
		Project:          LoadStatusbarRowOneVisibilityState(homeDir, lookupEnv, StatusbarRowOneProject, resolvePaths).Effective,
		WorkingDirectory: LoadStatusbarRowOneVisibilityState(homeDir, lookupEnv, StatusbarRowOneWorkingDirectory, resolvePaths).Effective,
		Git:              LoadStatusbarRowOneVisibilityState(homeDir, lookupEnv, StatusbarRowOneGit, resolvePaths).Effective,
		Clock:            LoadStatusbarRowOneVisibilityState(homeDir, lookupEnv, StatusbarRowOneClock, resolvePaths).Effective,
		SettingsLauncher: LoadStatusbarRowOneVisibilityState(homeDir, lookupEnv, StatusbarRowOneSettingsLauncher, resolvePaths).Effective,
	}
}

func StatusbarRowOneProjectFormat(bin string, roles theme.RenderRoles, visibility StatusbarRowOneVisibilitySet) string {
	if !visibility.Visible(StatusbarRowOneProject) {
		return ""
	}
	return StatusbarSessionLeftFormat(bin, roles)
}

func StatusbarRowOneRightFormat(bin string, roles theme.RenderRoles, liveResourcesMode config.LiveResourcesMode, visibility StatusbarRowOneVisibilitySet, settingsLabel string) string {
	var b strings.Builder
	if visibility.Visible(StatusbarRowOneWorkingDirectory) {
		b.WriteString(StatusbarCwdSegmentFormat(roles))
		b.WriteString("#[fg=" + roles.DividerFg + "]  ")
	}
	if visibility.Visible(StatusbarRowOneGit) {
		b.WriteString("#[range=user|git]#(" + bin + " internal status git)#[norange]")
	}
	if config.NormalizeLiveResourcesMode(string(liveResourcesMode)) == config.LiveResourcesOn {
		b.WriteString("#[range=user|resources]#(" + bin + " internal status resources)#[norange]")
	}
	if visibility.Visible(StatusbarRowOneClock) {
		b.WriteString("#[fg=" + roles.StatusTextSecondary + "]   %Y-%m-%d %H:%M ")
	}
	if visibility.Visible(StatusbarRowOneSettingsLauncher) {
		b.WriteString(StatusbarSettingsButton(settingsLabel, roles))
	}
	return b.String()
}
type StatusbarRowOneComponent string

const (
	StatusbarRowOneProject          StatusbarRowOneComponent = "project"
	StatusbarRowOneWorkingDirectory StatusbarRowOneComponent = "working-directory"
	StatusbarRowOneGit              StatusbarRowOneComponent = "git"
	StatusbarRowOneClock            StatusbarRowOneComponent = "clock"
	StatusbarRowOneSettingsLauncher StatusbarRowOneComponent = "settings-launcher"
)

var statusbarRowOneComponents = []StatusbarRowOneComponent{
	StatusbarRowOneProject,
	StatusbarRowOneWorkingDirectory,
	StatusbarRowOneGit,
	StatusbarRowOneClock,
	StatusbarRowOneSettingsLauncher,
}

type StatusbarRowOneVisibilitySet struct {
	Project          config.StatusbarVisibility
	WorkingDirectory config.StatusbarVisibility
	Git              config.StatusbarVisibility
	Clock            config.StatusbarVisibility
	SettingsLauncher config.StatusbarVisibility
}

