package app

import (
	"fmt"

	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

func runPickerOptionBackend(lookupEnv func(string) string, native intpicker.Runner, compat intpickercompat.Runner, options intpickercompat.Options) (intpickercompat.Result, error) {
	_ = compat
	if native == nil {
		return intpickercompat.Result{}, fmt.Errorf("native picker is not configured")
	}
	options = localizePickerOptions(lookupEnv, options)
	if options.Theme == nil {
		options = fallbackRenderThemeSource().pickerCompatOptions(options)
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
// it (Settings sets this); otherwise it is resolved from the environment the
// same way the rest of the app does via appLocale. Translation is idempotent,
// so Settings localizing first and this layer re-localizing is a safe no-op.
func localizePickerOptions(lookupEnv func(string) string, options intpickercompat.Options) intpickercompat.Options {
	locale := options.Locale
	if locale == "" {
		locale = appLocale(nil, lookupEnv)
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

func pickerCommandFromCompatBinding(action string) string {
	return intpickercompat.PickerCommandFromBinding(action)
}
