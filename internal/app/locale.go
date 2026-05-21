package app

import (
	"strings"

	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
)

func appLocale(homeDir func() (string, error), lookupEnv func(string) string) i18n.Locale {
	return appLocaleResolution(homeDir, lookupEnv).Locale
}

func appLocaleResolution(homeDir func() (string, error), lookupEnv func(string) string) i18n.LocaleResolution {
	return i18n.ResolveLocale(i18n.LocaleOptions{
		ConfigOverride: appGlobalLocaleOverride(homeDir, lookupEnv),
		LookupEnv:      appLocaleLookup(lookupEnv),
	})
}

func appGlobalLocaleOverride(homeDir func() (string, error), lookupEnv func(string) string) string {
	if homeDir == nil {
		return ""
	}
	path, err := hooks.GlobalConfigPath(lookupEnv, homeDir)
	if err != nil {
		return ""
	}
	cfg, err := hooks.LoadGlobalConfig(path)
	if err != nil {
		return ""
	}
	return cfg.UI.Locale
}

func appLocaleLookup(lookupEnv func(string) string) func(string) (string, bool) {
	if lookupEnv == nil {
		return nil
	}
	return func(name string) (string, bool) {
		value := lookupEnv(name)
		return value, strings.TrimSpace(value) != ""
	}
}

func localizeText(locale i18n.Locale, key i18n.Key, fallback string) string {
	text, err := i18n.NewLocalizer(locale).Text(key)
	if err != nil {
		return fallback
	}
	return text.String()
}
