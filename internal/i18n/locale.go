package i18n

import (
	"os"
	"strings"
)

const (
	// FallbackLocale is the catalog locale every translated key must define.
	FallbackLocale Locale = "en-US"
)

// Locale is a normalized BCP-47-ish locale tag used by the message catalog.
type Locale string

// LocaleSource records where the selected locale came from.
type LocaleSource string

const (
	LocaleSourceOverride   LocaleSource = "override"
	LocaleSourceLCAll      LocaleSource = "LC_ALL"
	LocaleSourceLCMessages LocaleSource = "LC_MESSAGES"
	LocaleSourceLANG       LocaleSource = "LANG"
	LocaleSourceFallback   LocaleSource = "fallback"
)

// LocaleResolution is the result of applying projmux locale precedence.
type LocaleResolution struct {
	Locale Locale
	Source LocaleSource
}

// LocaleOptions controls locale resolution for tests and future explicit APIs.
type LocaleOptions struct {
	Override  string
	LookupEnv func(string) (string, bool)
}

// ResolveLocale applies projmux locale precedence:
// explicit override/API > LC_ALL > LC_MESSAGES > LANG > en-US.
func ResolveLocale(opts LocaleOptions) LocaleResolution {
	if locale, ok := NormalizeLocale(opts.Override); ok {
		return LocaleResolution{Locale: locale, Source: LocaleSourceOverride}
	}
	lookupEnv := opts.LookupEnv
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	for _, candidate := range []struct {
		name   string
		source LocaleSource
	}{
		{name: "LC_ALL", source: LocaleSourceLCAll},
		{name: "LC_MESSAGES", source: LocaleSourceLCMessages},
		{name: "LANG", source: LocaleSourceLANG},
	} {
		raw, ok := lookupEnv(candidate.name)
		if !ok {
			continue
		}
		if locale, ok := NormalizeLocale(raw); ok {
			return LocaleResolution{Locale: locale, Source: candidate.source}
		}
	}
	return LocaleResolution{Locale: FallbackLocale, Source: LocaleSourceFallback}
}

// NormalizeLocale converts common POSIX and underscore forms into catalog tags.
func NormalizeLocale(raw string) (Locale, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", false
	}
	if dot := strings.IndexByte(value, '.'); dot >= 0 {
		value = value[:dot]
	}
	if at := strings.IndexByte(value, '@'); at >= 0 {
		value = value[:at]
	}
	value = strings.Trim(value, " \t\r\n-_")
	if value == "" {
		return "", false
	}
	upper := strings.ToUpper(value)
	if upper == "C" || upper == "POSIX" {
		return "", false
	}

	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == '-' || r == '_'
	})
	if len(parts) == 0 {
		return "", false
	}
	language := strings.ToLower(parts[0])
	if language == "" || language == "c" || language == "posix" {
		return "", false
	}
	if len(parts) == 1 {
		switch language {
		case "en":
			return FallbackLocale, true
		case "ko":
			return Locale("ko-KR"), true
		default:
			return Locale(language), true
		}
	}

	region := strings.ToUpper(parts[1])
	if region == "" {
		return Locale(language), true
	}
	return Locale(language + "-" + region), true
}
