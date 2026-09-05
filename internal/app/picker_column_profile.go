package app

import (
	"slices"
	"strconv"

	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

const (
	registryColumnProfileAction = "Resources:ToggleColumnProfile"
	runtimeColumnProfileAction  = "RuntimeDiagnostics:ToggleColumnProfile"
)

// columnPickerState belongs to one open list, including returns from its action
// menu. Reopening the command starts with its zero value: compact, no filter.
// A toggle only renders the existing snapshot again; it performs no read/write.
type columnPickerState struct {
	profile  columnProfile
	query    string
	selected string
}

func pickerColumnProfile(profile columnProfile) columnProfile {
	if profile == columnWide {
		return columnWide
	}
	return columnCompact
}

func (s *columnPickerState) run(homeDir func() (string, error), lookupEnv func(string) string, native intpicker.Runner, action string, options intpickercompat.Options, entries func(columnProfile) []intpickercompat.Entry) (intpickercompat.Result, error) {
	keys := effectivePickerKeysForActions(homeDir, lookupEnv, []string{action}, nil)
	for {
		current := options
		current.Entries = entries(pickerColumnProfile(s.profile))
		current.InitialQuery = s.query
		current.ExpectKeys = append(append([]string(nil), options.ExpectKeys...), keys...)
		current.Bindings = append([]string(nil), options.Bindings...)
		items := intpickercompat.PickerOptions(current).Items
		for i, item := range intpicker.FilterItems(items, s.query) {
			if s.selected != "" && item.Value == s.selected {
				current.Bindings = append(current.Bindings, "start:pos("+strconv.Itoa(i+1)+")")
				break
			}
		}
		label := "wide columns"
		if pickerColumnProfile(s.profile) == columnWide {
			label = "compact columns"
		}
		current.Footer = pickerActionKeyGuide(homeDir, lookupEnv, []pickerActionKeyGuideItem{{ActionID: action, Label: label}}) + " | " + options.Footer
		result, err := runNativePickerOption(homeDir, lookupEnv, native, current)
		if err != nil {
			return result, err
		}
		s.query, s.selected = result.Query, result.Value
		if !slices.Contains(keys, result.Key) {
			return result, nil
		}
		if pickerColumnProfile(s.profile) == columnWide {
			s.profile = columnCompact
		} else {
			s.profile = columnWide
		}
	}
}
