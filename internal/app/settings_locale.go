package app

import (
	"fmt"
	"io"
	"strings"

	"github.com/crevissepartners/projmux/internal/i18n"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

func (c *settingsCommand) localeSettingsEntry() intpickercompat.Entry {
	resolution := appLocaleResolution(c.homeDir, c.lookupEnv)
	locale := resolution.Locale
	setting, source, err := c.currentGlobalLocaleSetting()
	if err != nil {
		return intpickercompat.Entry{
			Label:     settingsNodeRowLabelDimLocale(locale, settingsNavAppearance+".locale", settingsCatalogTextLocale(locale, "unreadable")+" - "+err.Error()),
			Value:     settingsNoopValue,
			SearchKey: "appearance language locale unreadable PROJMUX_LOCALE ui.locale",
		}
	}
	desc := fmt.Sprintf("%s - %s", setting, localeResolutionSummary(locale, resolution, c.globalConfigDisplayPath()))
	if resolution.HasUnsupportedLocale() {
		desc = settingsCatalogTextLocale(locale, "warning") + " - " + desc
	}
	if source != "" && setting != i18n.LocaleSettingAuto {
		desc += " - " + c.globalConfigDisplayPath()
	}
	return intpickercompat.Entry{
		Label:     settingsNodeRowLabelLocale(locale, settingsNavAppearance+".locale", settingsGlyphOpen, settingsColorType, desc),
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
			if err := c.runSettingsMutation("Language / Locale", stdout, stderr, func(io.Writer, io.Writer) error {
				return c.setGlobalLocale(strings.TrimPrefix(action, settingsActionPrefixLocale))
			}); err != nil {
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
	settingPath := ""
	if source != "" {
		settingPath = c.globalConfigDisplayPath()
	}
	entries = append(entries,
		intpickercompat.Entry{
			Label:     settingsLabelInfoLocale(locale, "Current", string(resolution.Locale), localeResolutionSourceLabel(resolution, c.globalConfigDisplayPath())),
			Value:     settingsNoopValue,
			SearchKey: "current locale " + string(resolution.Locale) + " " + string(resolution.Source),
		},
		intpickercompat.Entry{
			Label:     settingsLabelInfoLocale(locale, "[ui].locale", setting, settingPath),
			Value:     settingsNoopValue,
			SearchKey: "ui.locale config " + setting,
		},
	)
	if resolution.HasUnsupportedLocale() {
		entries = append(entries, intpickercompat.Entry{
			Label:     settingsLabelDimLocale(locale, "Warning", localeUnsupportedWarning(locale, resolution, c.globalConfigDisplayPath())),
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
	return loadCentralLocaleSetting(c.homeDir, c.lookupEnv)
}

func (c *settingsCommand) setGlobalLocale(value string) error {
	value, err := saveCentralLocale(c.homeDir, c.lookupEnv, value)
	if err != nil {
		return err
	}
	if c.lookupEnv != nil && strings.TrimSpace(c.lookupEnv("TMUX")) != "" && c.runCommand != nil {
		_ = c.runCommand("tmux", "display-message", "[ui].locale: "+value)
	}
	return nil
}

// globalConfigDisplayPath is the config.toml the locale resolution read, as
// Settings shows it.
func (c *settingsCommand) globalConfigDisplayPath() string {
	return configFileDisplayPath(c.homeDir, c.lookupEnv, "config.toml")
}

func localeResolutionSummary(locale i18n.Locale, resolution i18n.LocaleResolution, configPath string) string {
	if locale == i18n.Locale("ko-KR") {
		return string(resolution.Locale) + " · " + localeResolutionSourceLabel(resolution, configPath)
	}
	return string(resolution.Locale) + " from " + localeResolutionSourceLabel(resolution, configPath)
}

// localeResolutionSourceLabel names where the locale came from. configPath is
// the global config.toml as Settings shows it, used when that file set it.
func localeResolutionSourceLabel(resolution i18n.LocaleResolution, configPath string) string {
	switch resolution.Source {
	case i18n.LocaleSourceEnv:
		return i18n.LocaleEnvName + " env"
	case i18n.LocaleSourceConfig:
		return configPath
	case i18n.LocaleSourceLCAll, i18n.LocaleSourceLCMessages, i18n.LocaleSourceLANG:
		return string(resolution.Source) + " env"
	case i18n.LocaleSourceOverride:
		return "explicit override"
	default:
		return "built-in fallback"
	}
}

func localeUnsupportedWarning(locale i18n.Locale, resolution i18n.LocaleResolution, configPath string) string {
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
		"{source}", localeResolutionSourceLabel(resolution, configPath),
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
