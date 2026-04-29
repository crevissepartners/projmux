package app

import "strings"

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
	settingsColorAdd    = "\x1b[32m" // green: positive / additive action
	settingsColorType   = "\x1b[36m" // cyan: typed / edit / navigate
	settingsColorRemove = "\x1b[31m" // red: destructive
	settingsColorBack   = "\x1b[90m" // dim: back / cancel
	settingsColorActive = "\x1b[1m"  // bold: active / current value
	settingsColorDim    = "\x1b[90m" // dim: descriptions, secondary text
	settingsColorInfo   = "\x1b[37m" // muted white: info / read-only label
	settingsColorReset  = "\x1b[0m"
)

// settingsLabelNameWidth is the byte width the name column is padded to.
// Names in this codebase are ASCII so byte-len padding is good enough.
const settingsLabelNameWidth = 24

// settingsLabel formats a single picker row with a glyph + colored name +
// dim description. An empty glyph falls back to a single space so that rows
// without a glyph align with rows that use a single-cell glyph (followed by
// the standard two-space gap before the name column).
func settingsLabel(glyph, color, name, description string) string {
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

// padRight right-pads s with spaces so its byte length is at least width.
// settings labels are ASCII, so byte-len matches display columns closely
// enough for our purposes.
func padRight(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}
