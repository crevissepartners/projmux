package app

import (
	"fmt"
	"io"
	"strings"

	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

func (c *settingsCommand) localeSettingsEntry() intpickercompat.Entry {
	resolution := appLocaleResolution(c.homeDir, c.lookupEnv)
	locale := resolution.Locale
	setting, source, err := c.currentGlobalLocaleSetting()
	if err != nil {
		return intpickercompat.Entry{
			Label:     settingsLabelDimLocale(locale, "Language / Locale", settingsCatalogTextLocale(locale, "unreadable")+" - "+err.Error()),
			Value:     settingsNoopValue,
			SearchKey: "appearance language locale unreadable PROJMUX_LOCALE ui.locale",
		}
	}
	desc := fmt.Sprintf("%s - %s", setting, localeResolutionSummary(locale, resolution))
	if resolution.HasUnsupportedLocale() {
		desc = settingsCatalogTextLocale(locale, "warning") + " - " + desc
	}
	if source != "" && setting != i18n.LocaleSettingAuto {
		desc += " - " + source
	}
	return intpickercompat.Entry{
		Label:     settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, "Language / Locale", desc),
		Value:     settingsAppearanceLanguage,
		SearchKey: "appearance language locale ui.locale PROJMUX_LOCALE auto en-US ko-KR",
	}
}

func (c *settingsCommand) runLocaleSection(stdout, stderr io.Writer) error {
	for {
		options := c.localeOptions()
		result, err := c.runPicker(options)
		if err != nil {
			return err
		}
		action := strings.TrimSpace(result.Value)
		if result.Key != "enter" || action == "" {
			return errSettingsClosed
		}
		switch {
		case action == settingsBackValue:
			return nil
		case action == settingsNoopValue:
			continue
		case strings.HasPrefix(action, settingsActionPrefixLocale):
			if err := c.setGlobalLocale(strings.TrimPrefix(action, settingsActionPrefixLocale)); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown locale settings action: %s", action)
		}
	}
}

func (c *settingsCommand) localeOptions() intpickercompat.Options {
	locale := appLocale(c.homeDir, c.lookupEnv)
	return intpickercompat.Options{
		UI:         "settings-locale-detail",
		Entries:    c.localeEntries(),
		Title:      settingsCatalogTextLocale(locale, "Appearance - Language / Locale"),
		TitleChips: settingsPassiveRootTabChipsLocale(settingsRootTabGlobal, c.resolveSettingsProjectContext().hasProject(), locale),
		Prompt:     settingsCatalogTextLocale(locale, "Settings > Appearance > Language / Locale > "),
		Footer:     strings.TrimSpace(settingsCatalogTextLocale(locale, "Enter: apply  |  Back row: parent ")),
		ExpectKeys: []string{"enter"},
		Bindings:   settingsCloseBindings(),
	}
}

func (c *settingsCommand) localeEntries() []intpickercompat.Entry {
	resolution := appLocaleResolution(c.homeDir, c.lookupEnv)
	locale := resolution.Locale
	setting, source, err := c.currentGlobalLocaleSetting()
	entries := []intpickercompat.Entry{settingsBackEntryLocale(locale)}
	if err != nil {
		return append(entries, intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, "Warning", settingsCatalogTextLocale(locale, "global config unreadable")+" - "+err.Error()),
			Value: settingsNoopValue,
		})
	}
	entries = append(entries,
		intpickercompat.Entry{
			Label:     settingsLabelInfoLocale(locale, "Current", string(resolution.Locale), localeResolutionSourceLabel(resolution)),
			Value:     settingsNoopValue,
			SearchKey: "current locale " + string(resolution.Locale) + " " + string(resolution.Source),
		},
		intpickercompat.Entry{
			Label:     settingsLabelInfoLocale(locale, "[ui].locale", setting, source),
			Value:     settingsNoopValue,
			SearchKey: "ui.locale config " + setting,
		},
	)
	if resolution.HasUnsupportedLocale() {
		entries = append(entries, intpickercompat.Entry{
			Label:     settingsLabelDimLocale(locale, "Warning", localeUnsupportedWarning(locale, resolution)),
			Value:     settingsNoopValue,
			SearchKey: "warning unsupported locale fallback en-US",
		})
	}
	envValue := ""
	if c.lookupEnv != nil {
		envValue = strings.TrimSpace(c.lookupEnv(i18n.LocaleEnvName))
	}
	if envValue != "" {
		entries = append(entries, intpickercompat.Entry{
			Label:     settingsLabelInfoLocale(locale, i18n.LocaleEnvName, envValue, "env override"),
			Value:     settingsNoopValue,
			SearchKey: "PROJMUX_LOCALE env override " + envValue,
		})
	}
	for _, choice := range []string{i18n.LocaleSettingAuto, string(i18n.FallbackLocale), "ko-KR"} {
		glyph := settingsGlyphInactive
		color := settingsColorDim
		desc := settingsCatalogTextLocale(locale, localeChoiceDescription(choice))
		if choice == setting {
			glyph = settingsGlyphToggle
			color = settingsColorAdd
			desc += " - " + settingsCatalogTextLocale(locale, "current")
		}
		entries = append(entries, intpickercompat.Entry{
			Label:     settingsLabelLocale(locale, glyph, color, choice, desc),
			Value:     settingsActionPrefixLocale + choice,
			SearchKey: "locale " + choice + " language",
		})
	}
	return entries
}

func (c *settingsCommand) currentGlobalLocaleSetting() (setting string, source string, err error) {
	path, err := c.globalConfigPath()
	if err != nil {
		return i18n.LocaleSettingAuto, "", err
	}
	cfg, err := hooks.LoadGlobalConfig(path)
	if err != nil {
		return i18n.LocaleSettingAuto, path, err
	}
	setting = strings.TrimSpace(cfg.UI.Locale)
	if setting == "" {
		setting = i18n.LocaleSettingAuto
	}
	return setting, path, nil
}

func (c *settingsCommand) setGlobalLocale(value string) error {
	value = strings.TrimSpace(value)
	switch value {
	case i18n.LocaleSettingAuto, string(i18n.FallbackLocale), "ko-KR":
	default:
		return fmt.Errorf("unsupported locale setting: %s", value)
	}
	path, err := c.globalConfigPath()
	if err != nil {
		return err
	}
	if _, err := hooks.UpdateGlobalConfig(path, func(cfg *hooks.ProjectConfig) error {
		cfg.UI.Locale = value
		return nil
	}); err != nil {
		return err
	}
	if c.lookupEnv != nil && strings.TrimSpace(c.lookupEnv("TMUX")) != "" && c.runCommand != nil {
		_ = c.runCommand("tmux", "display-message", "[ui].locale: "+value)
	}
	return nil
}

func localeResolutionSummary(locale i18n.Locale, resolution i18n.LocaleResolution) string {
	if locale == i18n.Locale("ko-KR") {
		return string(resolution.Locale) + " · " + localeResolutionSourceLabel(resolution)
	}
	return string(resolution.Locale) + " from " + localeResolutionSourceLabel(resolution)
}

func localeResolutionSourceLabel(resolution i18n.LocaleResolution) string {
	switch resolution.Source {
	case i18n.LocaleSourceEnv:
		return i18n.LocaleEnvName + " env"
	case i18n.LocaleSourceConfig:
		return "~/.config/projmux/config.toml"
	case i18n.LocaleSourceLCAll, i18n.LocaleSourceLCMessages, i18n.LocaleSourceLANG:
		return string(resolution.Source) + " env"
	case i18n.LocaleSourceOverride:
		return "explicit override"
	default:
		return "built-in fallback"
	}
}

func localeUnsupportedWarning(locale i18n.Locale, resolution i18n.LocaleResolution) string {
	raw := strings.TrimSpace(string(resolution.UnsupportedLocale))
	if raw == "" {
		raw = strings.TrimSpace(resolution.UnsupportedRaw)
	}
	if raw == "" {
		raw = strings.TrimSpace(resolution.Raw)
	}
	template := settingsCatalogTextLocale(locale, "Unsupported locale {locale} from {source}; using {fallback}.")
	replacer := strings.NewReplacer(
		"{locale}", raw,
		"{source}", localeResolutionSourceLabel(resolution),
		"{fallback}", string(i18n.FallbackLocale),
	)
	return replacer.Replace(template)
}

func localeChoiceDescription(choice string) string {
	switch choice {
	case i18n.LocaleSettingAuto:
		return "detect from LC_ALL, LC_MESSAGES, LANG"
	case string(i18n.FallbackLocale):
		return "English UI"
	case "ko-KR":
		return "Korean UI"
	default:
		return "unsupported"
	}
}
