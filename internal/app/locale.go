package app

import (
	"strings"

	"github.com/crevissepartners/projmux/internal/i18n"
)

func appLocale(lookupEnv func(string) string) i18n.Locale {
	return i18n.ResolveLocale(i18n.LocaleOptions{LookupEnv: appLocaleLookup(lookupEnv)}).Locale
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
