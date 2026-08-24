package app

import (
	"fmt"

	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

func runNativePickerOption(homeDir func() (string, error), lookupEnv func(string) string, native intpicker.Runner, options intpickercompat.Options) (intpickercompat.Result, error) {
	if native == nil {
		return intpickercompat.Result{}, fmt.Errorf("native picker is not configured")
	}
	options = localizePickerOptions(homeDir, lookupEnv, options)
	if options.Theme == nil {
		// Theme-by-default: a picker that does not pre-resolve its own theme
		// still gets the global `[theme]` so explicit background/surface reaches
		// every popup, not just the surfaces (switch/notify/ai/settings/recent)
		// that inject configRenderThemeSource themselves. Degrade to the built-in
		// fallback only when the global config cannot be read. Theme is
		// global-only, so no project path participates.
		if source, err := configRenderThemeSource(homeDir, lookupEnv, ""); err == nil {
			options = source.pickerCompatOptions(options)
		} else {
			options = fallbackRenderThemeSource().pickerCompatOptions(options)
		}
	}
	result, err := native.Run(intpickercompat.PickerOptions(options))
	return intpickercompat.ResultFromPicker(result), err
}

// localizePickerOptions is the shared choke point that localizes every
// user-facing chrome field on a picker's Options before it is rendered. It is
// the single authority for picker localization across all non-Settings pickers
// (notify routes its own intpicker.Options separately).
//
// The active locale is taken from options.Locale when a caller already resolved
// it (Settings sets this); otherwise it is resolved the same way the rest of
// the app does via appLocale, which MUST receive the command's homeDir so the
// global config `[ui] locale` override is honored. Passing a nil homeDir here
// silently skips that override (appGlobalLocaleOverride returns "" for nil) and
// falls through to the ambient LANG — which would render Korean chrome for a
// user who pinned `[ui] locale = "en-US"` under a ko_KR terminal. Translation
// is idempotent only when both passes resolve the SAME locale, so resolving the
// real locale here is also what keeps Settings' own pre-localization a no-op.
func localizePickerOptions(homeDir func() (string, error), lookupEnv func(string) string, options intpickercompat.Options) intpickercompat.Options {
	locale := options.Locale
	if locale == "" {
		locale = appLocale(homeDir, lookupEnv)
	}
	options.Locale = locale
	options.Title = localizeUIText(locale, options.Title)
	options.Prompt = localizeUIText(locale, options.Prompt)
	options.Header = localizeUIText(locale, options.Header)
	options.Footer = localizeUIText(locale, options.Footer)
	for i := range options.TitleChips {
		options.TitleChips[i].Label = localizeUIText(locale, options.TitleChips[i].Label)
	}
	return options
}

func pickerOptionsFromCompatPicker(options intpickercompat.Options) intpicker.Options {
	return intpickercompat.PickerOptions(options)
}
