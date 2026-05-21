package app

import (
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
	return renderThemeSource{effective: theme.ResolveTheme(theme.ThemeConfig{}, theme.ThemeConfig{})}
}

func newRenderThemeSource(effective theme.EffectiveTheme) renderThemeSource {
	return renderThemeSource{effective: effective}
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

func (s renderThemeSource) tmuxAppConfig(binaryPath, defaultShell string, decorations statusbarDecorationSet, catalog []keyBindingAction, keymapPresent bool) string {
	return tmuxAppConfigWithKeymapTheme(binaryPath, defaultShell, decorations, catalog, keymapPresent, s.effective)
}
