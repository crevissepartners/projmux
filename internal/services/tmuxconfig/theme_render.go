package tmuxconfig

import (
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	"github.com/crevissepartners/projmux/internal/services/keybinding"
	"github.com/crevissepartners/projmux/internal/theme"
)

func FallbackRenderThemeSource() RenderThemeSource {
	return RenderThemeSource{effective: theme.ResolveTheme(theme.ThemeConfig{})}
}

func NewRenderThemeSource(effective theme.EffectiveTheme) RenderThemeSource {
	return RenderThemeSource{effective: effective}
}

func ConfigRenderThemeSource(homeDir func() (string, error), lookupEnv func(string) string, projectPath string, resolvePaths PathsResolver) (RenderThemeSource, error) {
	effective, err := EffectiveThemeFromConfigNoting(config.NoteFrontRead, homeDir, lookupEnv, resolvePaths)
	if err != nil {
		return RenderThemeSource{}, err
	}
	return NewRenderThemeSource(effective), nil
}

func EffectiveThemeFromConfig(homeDir func() (string, error), lookupEnv func(string) string, projectPath string, resolvePaths PathsResolver) (theme.EffectiveTheme, error) {
	_ = projectPath
	return EffectiveThemeFromConfigNoting(config.NoteFrontRead, homeDir, lookupEnv, resolvePaths)
}

func EffectiveThemeFromConfigNoting(note func(name, path string), homeDir func() (string, error), lookupEnv func(string) string, resolvePaths PathsResolver) (theme.EffectiveTheme, error) {
	paths, err := resolvePaths(homeDir, lookupEnv)
	if err != nil {
		return theme.EffectiveTheme{}, err
	}
	note(config.SettingConfigTheme, paths.GlobalConfigFile())
	globalCfg, err := hooks.LoadProjectConfigFile(paths.GlobalConfigFile())
	if err != nil {
		return theme.EffectiveTheme{}, err
	}
	return theme.ResolveTheme(globalCfg.Theme), nil
}

func (s RenderThemeSource) TmuxStandaloneConfig(binaryPath string, decorations StatusbarDecorationSet, catalog []keybinding.KeyBindingAction, keymapPresent bool) string {
	return TmuxStandaloneConfigWithKeymapTheme(binaryPath, decorations, catalog, keymapPresent, s.effective)
}

func (s RenderThemeSource) TmuxStandaloneConfigWithAIBadgeStyleDesktopNotifyModeAndLiveResources(binaryPath string, decorations StatusbarDecorationSet, badgeStyle config.AIBadgeStyle, desktopNotifyMode config.DesktopNotifyMode, liveResourcesMode config.LiveResourcesMode, catalog []keybinding.KeyBindingAction, keymapPresent bool) string {
	return TmuxStandaloneConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeAndLiveResources(binaryPath, decorations, badgeStyle, desktopNotifyMode, liveResourcesMode, catalog, keymapPresent, s.effective)
}

func (s RenderThemeSource) TmuxStandaloneConfigWithAIBadgeStyleDesktopNotifyModeLiveResourcesAndHUDVisibility(binaryPath string, decorations StatusbarDecorationSet, badgeStyle config.AIBadgeStyle, desktopNotifyMode config.DesktopNotifyMode, liveResourcesMode config.LiveResourcesMode, hudVisibility StatusbarHUDVisibilitySet, catalog []keybinding.KeyBindingAction, keymapPresent bool) string {
	if hudVisibility.IsDefault() {
		return s.TmuxStandaloneConfigWithAIBadgeStyleDesktopNotifyModeAndLiveResources(binaryPath, decorations, badgeStyle, desktopNotifyMode, liveResourcesMode, catalog, keymapPresent)
	}
	return TmuxStandaloneConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeLiveResourcesAndHUDVisibility(binaryPath, decorations, badgeStyle, desktopNotifyMode, liveResourcesMode, hudVisibility, catalog, keymapPresent, s.effective)
}

func (s RenderThemeSource) TmuxStandaloneConfigWithAIBadgeStyleDesktopNotifyModeLiveResourcesAndVisibility(binaryPath string, decorations StatusbarDecorationSet, badgeStyle config.AIBadgeStyle, desktopNotifyMode config.DesktopNotifyMode, liveResourcesMode config.LiveResourcesMode, hudVisibility StatusbarHUDVisibilitySet, rowOneVisibility StatusbarRowOneVisibilitySet, catalog []keybinding.KeyBindingAction, keymapPresent bool) string {
	if rowOneVisibility.IsDefault() {
		return s.TmuxStandaloneConfigWithAIBadgeStyleDesktopNotifyModeLiveResourcesAndHUDVisibility(binaryPath, decorations, badgeStyle, desktopNotifyMode, liveResourcesMode, hudVisibility, catalog, keymapPresent)
	}
	return TmuxStandaloneConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeLiveResourcesAndVisibility(binaryPath, decorations, badgeStyle, desktopNotifyMode, liveResourcesMode, hudVisibility, rowOneVisibility, catalog, keymapPresent, s.effective)
}

func (s RenderThemeSource) TmuxAppConfig(binaryPath, defaultShell string, decorations StatusbarDecorationSet, catalog []keybinding.KeyBindingAction, keymapPresent bool) string {
	return TmuxAppConfigWithKeymapTheme(binaryPath, defaultShell, decorations, catalog, keymapPresent, s.effective)
}

func (s RenderThemeSource) TmuxAppConfigWithAIBadgeStyleDesktopNotifyModeAndLiveResources(binaryPath, defaultShell string, decorations StatusbarDecorationSet, badgeStyle config.AIBadgeStyle, desktopNotifyMode config.DesktopNotifyMode, liveResourcesMode config.LiveResourcesMode, catalog []keybinding.KeyBindingAction, keymapPresent bool) string {
	return TmuxAppConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeAndLiveResources(binaryPath, defaultShell, decorations, badgeStyle, desktopNotifyMode, liveResourcesMode, catalog, keymapPresent, s.effective)
}

func (s RenderThemeSource) TmuxAppConfigWithAIBadgeStyleDesktopNotifyModeLiveResourcesAndHUDVisibility(binaryPath, defaultShell string, decorations StatusbarDecorationSet, badgeStyle config.AIBadgeStyle, desktopNotifyMode config.DesktopNotifyMode, liveResourcesMode config.LiveResourcesMode, hudVisibility StatusbarHUDVisibilitySet, catalog []keybinding.KeyBindingAction, keymapPresent bool) string {
	if hudVisibility.IsDefault() {
		return s.TmuxAppConfigWithAIBadgeStyleDesktopNotifyModeAndLiveResources(binaryPath, defaultShell, decorations, badgeStyle, desktopNotifyMode, liveResourcesMode, catalog, keymapPresent)
	}
	return TmuxAppConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeLiveResourcesAndHUDVisibility(binaryPath, defaultShell, decorations, badgeStyle, desktopNotifyMode, liveResourcesMode, hudVisibility, catalog, keymapPresent, s.effective)
}

func (s RenderThemeSource) TmuxAppConfigWithAIBadgeStyleDesktopNotifyModeLiveResourcesAndVisibility(binaryPath, defaultShell string, decorations StatusbarDecorationSet, badgeStyle config.AIBadgeStyle, desktopNotifyMode config.DesktopNotifyMode, liveResourcesMode config.LiveResourcesMode, hudVisibility StatusbarHUDVisibilitySet, rowOneVisibility StatusbarRowOneVisibilitySet, catalog []keybinding.KeyBindingAction, keymapPresent bool) string {
	if rowOneVisibility.IsDefault() {
		return s.TmuxAppConfigWithAIBadgeStyleDesktopNotifyModeLiveResourcesAndHUDVisibility(binaryPath, defaultShell, decorations, badgeStyle, desktopNotifyMode, liveResourcesMode, hudVisibility, catalog, keymapPresent)
	}
	return TmuxAppConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeLiveResourcesAndVisibility(binaryPath, defaultShell, decorations, badgeStyle, desktopNotifyMode, liveResourcesMode, hudVisibility, rowOneVisibility, catalog, keymapPresent, s.effective)
}

type RenderThemeSource struct{ effective theme.EffectiveTheme }
