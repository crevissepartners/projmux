package usage

// HUD bar rendering primitives shared by the `projmux status usage` HUD
// segment. Kept in the core package (rather than internal/app) so unit tests
// can exercise the math without importing the CLI plumbing.

import (
	"strings"
)

// BarCells is the fixed width of the HUD bar widget in cells. The bar is
// rendered with `█` filled cells and `░` empty cells, surrounded by `[` `]`.
const BarCells = 10

// BarFilledRune is the rune used for the filled portion of the HUD bar.
const BarFilledRune = '█'

// BarEmptyRune is the rune used for the empty portion of the HUD bar.
const BarEmptyRune = '░'

// BarFillCount returns the number of filled cells for the supplied
// percentage. The result is clamped to [0, BarCells]. For percentages > 100
// the bar saturates at full — callers should still surface the over-limit
// number as text so the user sees the actual overshoot.
func BarFillCount(pct float64) int {
	if pct <= 0 {
		return 0
	}
	if pct >= 100 {
		return BarCells
	}
	cells := min(max(int(pct/100.0*float64(BarCells)+0.5), 0), BarCells)
	return cells
}

// RenderBar returns a fixed-width HUD bar string. The output includes
// surrounding brackets but no color escapes — callers that want colorisation
// should use RenderColoredBar.
func RenderBar(pct float64) string {
	filled := BarFillCount(pct)
	var b strings.Builder
	b.Grow(BarCells + 2)
	b.WriteByte('[')
	for i := range BarCells {
		if i < filled {
			b.WriteRune(BarFilledRune)
		} else {
			b.WriteRune(BarEmptyRune)
		}
	}
	b.WriteByte(']')
	return b.String()
}

// RenderColoredBar wraps the filled and empty halves of the bar in tmux
// color escapes. fillColor is applied to the `█` runs, emptyColor to the
// `░` runs. Callers append `#[default]` or another color reset themselves.
func RenderColoredBar(pct float64, fillColor, emptyColor string) string {
	filled := BarFillCount(pct)
	var b strings.Builder
	b.Grow(BarCells + 32)
	b.WriteByte('[')
	if filled > 0 {
		if fillColor != "" {
			b.WriteString("#[fg=")
			b.WriteString(fillColor)
			b.WriteByte(']')
		}
		for range filled {
			b.WriteRune(BarFilledRune)
		}
	}
	if filled < BarCells {
		if emptyColor != "" {
			b.WriteString("#[fg=")
			b.WriteString(emptyColor)
			b.WriteByte(']')
		}
		for i := filled; i < BarCells; i++ {
			b.WriteRune(BarEmptyRune)
		}
	}
	b.WriteByte(']')
	return b.String()
}

// BarColorForPct returns the tmux color spec for the bar fill at the
// supplied percentage. Ramps:
//
//	<80%   → muted teal
//	80-95% → amber warning
//	95%+   → red critical
//	>100%  → red,bold (over-limit)
func BarColorForPct(pct float64) string {
	switch {
	case pct > 100:
		return "colour160,bold"
	case pct >= 95:
		return "colour160"
	case pct >= 80:
		return "colour214"
	default:
		return "colour72"
	}
}

// BarEmptyColor is the tmux color used for empty bar cells across the HUD.
const BarEmptyColor = "colour238"
