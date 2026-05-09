package app

import (
	"fmt"

	intfzf "github.com/crevissepartners/projmux/internal/ui/fzf"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
)

func runPickerOptionBackend(lookupEnv func(string) string, native intpicker.Runner, fzf intfzf.Runner, options intfzf.Options) (intfzf.Result, error) {
	if resolvePickerBackend(lookupEnv) == intpicker.BackendNative {
		if native == nil {
			return intfzf.Result{}, fmt.Errorf("native picker is not configured")
		}
		result, err := native.Run(intfzf.PickerOptions(options))
		return intfzf.ResultFromPicker(result), err
	}
	if fzf == nil {
		return intfzf.Result{}, fmt.Errorf("fzf picker is not configured")
	}
	return fzf.Run(options)
}

func pickerOptionsFromFZF(options intfzf.Options) intpicker.Options {
	return intfzf.PickerOptions(options)
}

func pickerCommandFromFZFBinding(action string) string {
	return intfzf.PickerCommandFromBinding(action)
}
