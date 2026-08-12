package app

import (
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	"github.com/crevissepartners/projmux/internal/theme"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// renderThemeSource is the app-layer handoff from resolved theme data to the
// renderers that already know how to adapt it for ANSI or tmux.
type renderThemeSource struct {
	effective theme.EffectiveTheme
}

func fallbackRenderThemeSource() renderThemeSource {
	return renderThemeSource{effective: theme.ResolveTheme(theme.ThemeConfig{})}
}

func newRenderThemeSource(effective theme.EffectiveTheme) renderThemeSource {
	return renderThemeSource{effective: effective}
}

func configRenderThemeSource(homeDir func() (string, error), lookupEnv func(string) string, projectPath string) (renderThemeSource, error) {
	effective, err := effectiveThemeFromConfig(homeDir, lookupEnv, projectPath)
	if err != nil {
		return renderThemeSource{}, err
	}
	return newRenderThemeSource(effective), nil
}

// effectiveThemeFromConfig resolves the effective theme from the global user
// config alone. Theme is a global preference: a project's
// .projmux/config.toml [theme] is deprecated migration data and is no longer
// read here. projectPath is retained for caller-signature stability but does
// not participate in theme resolution.
func effectiveThemeFromConfig(homeDir func() (string, error), lookupEnv func(string) string, projectPath string) (theme.EffectiveTheme, error) {
	_ = projectPath
	paths, err := configPaths(homeDir, lookupEnv)
	if err != nil {
		return theme.EffectiveTheme{}, err
	}
	globalCfg, err := hooks.LoadProjectConfigFile(paths.GlobalConfigFile())
	if err != nil {
		return theme.EffectiveTheme{}, err
	}
	return theme.ResolveTheme(globalCfg.Theme), nil
}

func (s renderThemeSource) pickerOptions(options intpicker.Options) intpicker.Options {
	effective := s.effective
	options.Theme = &effective
	return options
}

func (s renderThemeSource) pickerCompatOptions(options intpickercompat.Options) intpickercompat.Options {
	effective := s.effective
	options.Theme = &effective
	return options
}

func (s renderThemeSource) tmuxStandaloneConfig(binaryPath string, decorations statusbarDecorationSet, catalog []keyBindingAction, keymapPresent bool) string {
	return tmuxStandaloneConfigWithKeymapTheme(binaryPath, decorations, catalog, keymapPresent, s.effective)
}

func (s renderThemeSource) tmuxStandaloneConfigWithAIBadgeStyle(binaryPath string, decorations statusbarDecorationSet, badgeStyle config.AIBadgeStyle, catalog []keyBindingAction, keymapPresent bool) string {
	return tmuxStandaloneConfigWithKeymapThemeAndAIBadgeStyle(binaryPath, decorations, badgeStyle, catalog, keymapPresent, s.effective)
}

func (s renderThemeSource) tmuxStandaloneConfigWithAIBadgeStyleDesktopNotifyModeAndLiveResources(binaryPath string, decorations statusbarDecorationSet, badgeStyle config.AIBadgeStyle, desktopNotifyMode config.DesktopNotifyMode, liveResourcesMode config.LiveResourcesMode, catalog []keyBindingAction, keymapPresent bool) string {
	return tmuxStandaloneConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeAndLiveResources(binaryPath, decorations, badgeStyle, desktopNotifyMode, liveResourcesMode, catalog, keymapPresent, s.effective)
}

func (s renderThemeSource) tmuxAppConfig(binaryPath, defaultShell string, decorations statusbarDecorationSet, catalog []keyBindingAction, keymapPresent bool) string {
	return tmuxAppConfigWithKeymapTheme(binaryPath, defaultShell, decorations, catalog, keymapPresent, s.effective)
}

func (s renderThemeSource) tmuxAppConfigWithAIBadgeStyle(binaryPath, defaultShell string, decorations statusbarDecorationSet, badgeStyle config.AIBadgeStyle, catalog []keyBindingAction, keymapPresent bool) string {
	return tmuxAppConfigWithKeymapThemeAndAIBadgeStyle(binaryPath, defaultShell, decorations, badgeStyle, catalog, keymapPresent, s.effective)
}

func (s renderThemeSource) tmuxAppConfigWithAIBadgeStyleDesktopNotifyModeAndLiveResources(binaryPath, defaultShell string, decorations statusbarDecorationSet, badgeStyle config.AIBadgeStyle, desktopNotifyMode config.DesktopNotifyMode, liveResourcesMode config.LiveResourcesMode, catalog []keyBindingAction, keymapPresent bool) string {
	return tmuxAppConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeAndLiveResources(binaryPath, defaultShell, decorations, badgeStyle, desktopNotifyMode, liveResourcesMode, catalog, keymapPresent, s.effective)
}
