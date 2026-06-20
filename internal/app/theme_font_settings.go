package app

import (
	"strings"

	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	"github.com/crevissepartners/projmux/internal/theme"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

func (c *settingsCommand) themeFontStatusEntry() intpickercompat.Entry {
	application, source, err := c.currentThemeFontApplication()
	if err != nil {
		return intpickercompat.Entry{
			Label:     c.rowLabelDim("Theme font", "not applied - "+err.Error()),
			Value:     settingsNoopValue,
			SearchKey: "appearance theme font not applied error",
		}
	}
	return intpickercompat.Entry{
		Label:     c.rowLabelInfo("Theme font", application.Desired(), source+"; "+application.Summary()),
		Value:     settingsNoopValue,
		SearchKey: "appearance theme font family size desired " + string(application.Status),
	}
}

func (c *settingsCommand) currentThemeFontApplication() (theme.FontApplication, string, error) {
	globalCfg, err := c.currentGlobalProjectConfig()
	if err != nil {
		return theme.FontApplication{}, "", err
	}
	effective := theme.ResolveTheme(globalCfg.Theme)
	source := themeFontSourceSummary(effective)
	return theme.EvaluateFontApplication(effective, theme.NoFontCapability()), source, nil
}

func (c *settingsCommand) currentGlobalProjectConfig() (hooks.ProjectConfig, error) {
	path, err := c.globalConfigPath()
	if err != nil {
		return hooks.ProjectConfig{}, err
	}
	return hooks.LoadGlobalConfig(path)
}

func themeFontSourceSummary(effective theme.EffectiveTheme) string {
	var parts []string
	if strings.TrimSpace(effective.FontFamily.Value) != "" {
		parts = append(parts, "font_family "+string(effective.FontFamily.Source))
	}
	if effective.FontSize.Value != 0 {
		parts = append(parts, "font_size "+string(effective.FontSize.Source))
	}
	if len(parts) == 0 {
		return "fallback"
	}
	return strings.Join(parts, ", ")
}
