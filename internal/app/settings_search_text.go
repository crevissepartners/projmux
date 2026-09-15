package app

import (
	"strings"

	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// withSettingsRenderedLabelSearchText joins each keyed row's rendered label
// onto its SearchKey. The native picker matches a row's SearchText alone
// whenever it is set, so a hand-written (mostly English) key would otherwise
// hide the localized label the user actually sees. The existing key is kept
// verbatim, so every earlier match still matches. Rows without a SearchKey stay
// untouched, which keeps SearchKey-free Views in the picker's scored mode.
func withSettingsRenderedLabelSearchText(options intpickercompat.Options) intpickercompat.Options {
	entries := options.Entries
	copied := false
	for i, entry := range entries {
		if strings.TrimSpace(entry.SearchKey) == "" {
			continue
		}
		label := strings.TrimSpace(stripSettingsLabelANSI(entry.Label))
		if label == "" || strings.Contains(entry.SearchKey, label) {
			continue
		}
		if !copied {
			entries = append([]intpickercompat.Entry(nil), entries...)
			copied = true
		}
		entries[i].SearchKey = entry.SearchKey + " " + label
	}
	options.Entries = entries
	return options
}

// stripSettingsLabelANSI removes the terminal escape sequences Settings row
// builders style labels with, leaving the text the picker displays.
func stripSettingsLabelANSI(value string) string {
	if !strings.Contains(value, "\x1b") {
		return value
	}
	var out strings.Builder
	for i := 0; i < len(value); {
		if value[i] != '\x1b' {
			out.WriteByte(value[i])
			i++
			continue
		}
		if i+1 < len(value) && value[i+1] == '[' {
			i += 2
			for i < len(value) {
				b := value[i]
				i++
				if b >= 0x40 && b <= 0x7e {
					break
				}
			}
			continue
		}
		i += 2
	}
	return out.String()
}
