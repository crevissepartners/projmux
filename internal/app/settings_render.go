package app

import (
	"strings"

	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/theme"
)

// settings_render.go centralizes the picker-row formatting used by every
// settings entry builder. Keeping the glyph + color + padding in one place
// lets us tune the design system without touching every call site.

// Glyph runes used by the settings picker rows. Single code point each so
// padding works with simple byte/rune len. The fullwidth plus is two display
// cells, intentional so Add actions stand out visually.
const (
	settingsGlyphBack     = "↩" // ↩ navigate back / cancel
	settingsGlyphAdd      = "＋" // ＋ add (fullwidth plus)
	settingsGlyphType     = "✎" // ✎ direct typed entry
	settingsGlyphRemove   = "✕" // ✕ remove / clear
	settingsGlyphToggle   = "◉" // ◉ toggle on
	settingsGlyphInactive = "○" // ○ toggle off
	settingsGlyphInfo     = "·" // · info / read-only / disabled
	settingsGlyphOpen     = "▸" // ▸ open / navigate
)

// ANSI color sequences mapped per the design system.
const (
	settingsColorAdd    = theme.ANSIAccentActionStart  // additive / primary action
	settingsColorType   = theme.ANSIAccentActionStart  // edit / navigate action
	settingsColorRemove = theme.ANSIStateDangerStart   // destructive
	settingsColorBack   = theme.ANSITextSecondaryStart // back / cancel
	settingsColorActive = theme.ANSIBold               // active / current value
	settingsColorDim    = theme.ANSITextDimStart       // descriptions, secondary text
	settingsColorInfo   = theme.ANSITextPrimaryStart   // info / read-only label
	settingsColorReset  = theme.ANSIReset
)

// settingsLabelNameWidth is the byte width the name column is padded to.
// Names in this codebase are ASCII so byte-len padding is good enough.
const settingsLabelNameWidth = 24

// settingsLabel formats a single picker row with a glyph + colored name +
// dim description. An empty glyph falls back to a single space so that rows
// without a glyph align with rows that use a single-cell glyph (followed by
// the standard two-space gap before the name column).
func settingsLabel(glyph, color, name, description string) string {
	return settingsLabelLocale(settingsLocaleFromEnv(), glyph, color, name, description)
}

func settingsLabelLocale(locale i18n.Locale, glyph, color, name, description string) string {
	name = settingsCatalogTextLocale(locale, name)
	description = settingsCatalogTextLocale(locale, description)
	var b strings.Builder

	if glyph == "" {
		b.WriteString(" ")
	} else {
		b.WriteString(glyph)
	}
	b.WriteString("  ")

	padded := padRight(name, settingsLabelNameWidth)
	if color == "" {
		b.WriteString(padded)
	} else {
		b.WriteString(color)
		b.WriteString(padded)
		b.WriteString(settingsColorReset)
	}

	if description != "" {
		b.WriteString("  ")
		b.WriteString(settingsColorDim)
		b.WriteString(description)
		b.WriteString(settingsColorReset)
	}
	return b.String()
}

// settingsLabelDim formats a read-only / disabled-style row. The whole row
// is wrapped in the dim color so it visually recedes, and no action color
// is applied to the name column.
func settingsLabelDim(name, description string) string {
	return settingsLabelDimLocale(settingsLocaleFromEnv(), name, description)
}

func settingsLabelDimLocale(locale i18n.Locale, name, description string) string {
	name = settingsCatalogTextLocale(locale, name)
	description = settingsCatalogTextLocale(locale, description)
	var b strings.Builder
	b.WriteString(settingsGlyphInfo)
	b.WriteString("  ")
	b.WriteString(settingsColorDim)
	b.WriteString(padRight(name, settingsLabelNameWidth))
	b.WriteString(settingsColorReset)
	if description != "" {
		b.WriteString("  ")
		b.WriteString(settingsColorDim)
		b.WriteString(description)
		b.WriteString(settingsColorReset)
	}
	return b.String()
}

// settingsLabelInfo formats an info row of the shape:
//
//	·  Name (muted, padded)  Value (bold)  (source) (dim)
//
// Used for things like "Project Root  /home/...  (PROJMUX_PROJDIR env)" where the
// name is a static label, the value is the resolved data, and the source
// annotation explains where the value came from.
func settingsLabelInfo(name, value, source string) string {
	return settingsLabelInfoLocale(settingsLocaleFromEnv(), name, value, source)
}

func settingsLabelInfoLocale(locale i18n.Locale, name, value, source string) string {
	name = settingsCatalogTextLocale(locale, name)
	value = settingsCatalogTextLocale(locale, value)
	source = settingsCatalogTextLocale(locale, source)
	var b strings.Builder
	b.WriteString(settingsGlyphInfo)
	b.WriteString("  ")
	b.WriteString(settingsColorInfo)
	b.WriteString(padRight(name, settingsLabelNameWidth))
	b.WriteString(settingsColorReset)
	if value != "" {
		b.WriteString("  ")
		b.WriteString(settingsColorActive)
		b.WriteString(value)
		b.WriteString(settingsColorReset)
	}
	if source != "" {
		b.WriteString("  ")
		b.WriteString(settingsColorDim)
		b.WriteString("(" + source + ")")
		b.WriteString(settingsColorReset)
	}
	return b.String()
}

// padRight right-pads s with spaces so its visible terminal width is at least width.
func padRight(s string, width int) string {
	if i18n.TerminalCellWidth(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-i18n.TerminalCellWidth(s))
}
