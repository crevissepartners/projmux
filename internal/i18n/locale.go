package i18n

import (
	"os"
	"strings"
)

const (
	// FallbackLocale is the catalog locale every translated key must define.
	FallbackLocale Locale = "en-US"
	// LocaleSettingAuto means "detect from LC_ALL, LC_MESSAGES, LANG, then fallback".
	LocaleSettingAuto = "auto"
	// LocaleEnvName is the process-level locale override.
	LocaleEnvName = "PROJMUX_LOCALE"
)

// Locale is a normalized BCP-47-ish locale tag used by the message catalog.
type Locale string

// LocaleSource records where the selected locale came from.
type LocaleSource string

const (
	LocaleSourceOverride   LocaleSource = "override"
	LocaleSourceEnv        LocaleSource = "PROJMUX_LOCALE"
	LocaleSourceConfig     LocaleSource = "global config"
	LocaleSourceLCAll      LocaleSource = "LC_ALL"
	LocaleSourceLCMessages LocaleSource = "LC_MESSAGES"
	LocaleSourceLANG       LocaleSource = "LANG"
	LocaleSourceFallback   LocaleSource = "fallback"
)

// LocaleResolution is the result of applying projmux locale precedence.
type LocaleResolution struct {
	Locale            Locale
	Source            LocaleSource
	Raw               string
	UnsupportedLocale Locale
	UnsupportedRaw    string
}

func (r LocaleResolution) HasUnsupportedLocale() bool {
	return r.UnsupportedLocale != "" || strings.TrimSpace(r.UnsupportedRaw) != ""
}

// LocaleOptions controls locale resolution for tests and explicit APIs.
type LocaleOptions struct {
	Override       string
	EnvOverride    string
	ConfigOverride string
	LookupEnv      func(string) (string, bool)
}

// ResolveLocale applies projmux locale precedence:
// explicit override/API > PROJMUX_LOCALE > global config > LC_ALL >
// LC_MESSAGES > LANG > en-US.
func ResolveLocale(opts LocaleOptions) LocaleResolution {
	if resolution, ok := resolveLocaleCandidate(opts.Override, LocaleSourceOverride, true); ok {
		return resolution
	}
	lookupEnv := opts.LookupEnv
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	envOverride := opts.EnvOverride
	envOverrideSet := strings.TrimSpace(envOverride) != ""
	if strings.TrimSpace(envOverride) == "" {
		if raw, ok := lookupEnv(LocaleEnvName); ok {
			envOverride = raw
			envOverrideSet = true
		}
	}
	envAuto := envOverrideSet && isAutoLocaleSetting(envOverride)
	if !envAuto {
		if resolution, ok := resolveLocaleCandidate(envOverride, LocaleSourceEnv, true); ok {
			return resolution
		}
	}
	if !envAuto && !isAutoLocaleSetting(opts.ConfigOverride) {
		if resolution, ok := resolveLocaleCandidate(opts.ConfigOverride, LocaleSourceConfig, true); ok {
			return resolution
		}
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
		if resolution, ok := resolveLocaleCandidate(raw, candidate.source, false); ok {
			return resolution
		}
	}
	return LocaleResolution{Locale: FallbackLocale, Source: LocaleSourceFallback}
}

func resolveLocaleCandidate(raw string, source LocaleSource, explicit bool) (LocaleResolution, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || isAutoLocaleSetting(raw) {
		return LocaleResolution{}, false
	}
	locale, ok := NormalizeLocale(raw)
	if !ok {
		if explicit {
			return LocaleResolution{
				Locale:         FallbackLocale,
				Source:         source,
				Raw:            raw,
				UnsupportedRaw: raw,
			}, true
		}
		return LocaleResolution{}, false
	}
	if IsSupportedLocale(locale) {
		return LocaleResolution{Locale: locale, Source: source, Raw: raw}, true
	}
	return LocaleResolution{
		Locale:            FallbackLocale,
		Source:            source,
		Raw:               raw,
		UnsupportedLocale: locale,
		UnsupportedRaw:    raw,
	}, true
}

func isAutoLocaleSetting(raw string) bool {
	return strings.EqualFold(strings.TrimSpace(raw), LocaleSettingAuto)
}

func IsSupportedLocale(locale Locale) bool {
	switch locale {
	case FallbackLocale, Locale("ko-KR"):
		return true
	default:
		return false
	}
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
