package app

import (
	"fmt"

	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

func runPickerOptionBackend(lookupEnv func(string) string, native intpicker.Runner, compat intpickercompat.Runner, options intpickercompat.Options) (intpickercompat.Result, error) {
	_ = lookupEnv
	_ = compat
	if native == nil {
		return intpickercompat.Result{}, fmt.Errorf("native picker is not configured")
	}
	result, err := native.Run(intpickercompat.PickerOptions(options))
	return intpickercompat.ResultFromPicker(result), err
}

func pickerOptionsFromCompatPicker(options intpickercompat.Options) intpicker.Options {
	return intpickercompat.PickerOptions(options)
}

func pickerCommandFromCompatBinding(action string) string {
	return intpickercompat.PickerCommandFromBinding(action)
}
