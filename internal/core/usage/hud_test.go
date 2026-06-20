package usage

import (
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/theme"
)

func TestBarFillCountBoundaryCases(t *testing.T) {
	t.Parallel()

	// Each row asserts (input pct, expected number of filled cells).
	cases := []struct {
		name string
		pct  float64
		want int
	}{
		{"zero", 0, 0},
		{"one", 1, 0},  // 1% rounds to 0 cells (0.1 → 0).
		{"five", 5, 1}, // boundary: 0.5 → 1 cell with round-half-up.
		{"forty-nine", 49, 5},
		{"fifty", 50, 5},
		{"seventy-nine", 79, 8},
		{"eighty", 80, 8},
		{"ninety-nine", 99, 10},
		{"hundred", 100, 10},
		{"one-fifty", 150, 10}, // saturates at full.
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := BarFillCount(tc.pct); got != tc.want {
				t.Fatalf("BarFillCount(%v) = %d, want %d", tc.pct, got, tc.want)
			}
		})
	}
}

func TestRenderBarFixedWidth(t *testing.T) {
	t.Parallel()

	// Bar is always BarCells+2 runes (brackets included).
	cases := []float64{0, 1, 49, 50, 79, 80, 99, 100, 150}
	for _, pct := range cases {
		bar := RenderBar(pct)
		runes := []rune(bar)
		if len(runes) != BarCells+2 {
			t.Fatalf("RenderBar(%v) rune len = %d, want %d (%q)", pct, len(runes), BarCells+2, bar)
		}
		if runes[0] != '[' || runes[len(runes)-1] != ']' {
			t.Fatalf("RenderBar(%v) brackets missing: %q", pct, bar)
		}
	}
}

func TestRenderBarFillRatios(t *testing.T) {
	t.Parallel()

	cases := []struct {
		pct   float64
		fills int
	}{
		{0, 0},
		{50, 5},
		{100, 10},
		{150, 10},
	}
	for _, tc := range cases {
		bar := RenderBar(tc.pct)
		filled := strings.Count(bar, string(BarFilledRune))
		empty := strings.Count(bar, string(BarEmptyRune))
		if filled != tc.fills {
			t.Fatalf("RenderBar(%v) filled=%d, want %d (%q)", tc.pct, filled, tc.fills, bar)
		}
		if filled+empty != BarCells {
			t.Fatalf("RenderBar(%v) filled+empty = %d, want %d (%q)", tc.pct, filled+empty, BarCells, bar)
		}
	}
}

func TestBarColorForPctRamp(t *testing.T) {
	t.Parallel()

	cases := []struct {
		pct  float64
		want string
	}{
		{0, "colour72"},
		{79, "colour72"},
		{80, "colour214"},
		{94.9, "colour214"},
		{95, "colour160"},
		{100, "colour160"},
		{101, "colour160,bold"},
		{300, "colour160,bold"},
	}
	roles := theme.RenderRolesFromEffective(theme.EffectiveTheme{})
	for _, tc := range cases {
		if got := BarColorForPct(tc.pct, roles); got != tc.want {
			t.Fatalf("BarColorForPct(%v) = %q, want %q", tc.pct, got, tc.want)
		}
	}
}

func TestBarColorForPctRepaintsFromExplicitThemeWarningCritical(t *testing.T) {
	t.Parallel()

	fallback := theme.RenderRolesFromEffective(theme.EffectiveTheme{})
	explicit := theme.RenderRolesFromEffective(theme.ResolveTheme(theme.ThemeConfig{
		Warning:  "#00ff00",
		Critical: "#0000ff",
	}))

	// Warning ramp (80-95%) follows the explicit warning token.
	if BarColorForPct(85, explicit) == BarColorForPct(85, fallback) {
		t.Fatalf("usage warning color did not repaint from explicit theme")
	}
	if BarColorForPct(85, explicit) != explicit.StateWarning {
		t.Fatalf("usage warning color = %q, want explicit state.warning %q", BarColorForPct(85, explicit), explicit.StateWarning)
	}
	// Critical ramp (95%+) follows the explicit critical token.
	if BarColorForPct(96, explicit) == BarColorForPct(96, fallback) {
		t.Fatalf("usage critical color did not repaint from explicit theme")
	}
	if BarColorForPct(96, explicit) != explicit.StateCritical {
		t.Fatalf("usage critical color = %q, want explicit state.critical %q", BarColorForPct(96, explicit), explicit.StateCritical)
	}
	// Over-limit keeps the ,bold suffix on the repainted critical.
	if BarColorForPct(150, explicit) != explicit.StateCritical+",bold" {
		t.Fatalf("usage over-limit color = %q, want explicit critical,bold", BarColorForPct(150, explicit))
	}
}

func TestRenderColoredBarHasEscapes(t *testing.T) {
	t.Parallel()

	// 50% → 5 filled + 5 empty + brackets + 2 color escapes.
	out := RenderColoredBar(50, "colour72", BarEmptyColor)
	if !strings.HasPrefix(out, "[#[fg=colour72]") {
		t.Fatalf("missing fill color prefix: %q", out)
	}
	if !strings.Contains(out, "#[fg="+BarEmptyColor+"]") {
		t.Fatalf("missing empty color escape: %q", out)
	}
	if !strings.HasSuffix(out, "]") {
		t.Fatalf("missing closing bracket: %q", out)
	}
}

func TestRenderColoredBarFullSuppressesEmptyEscape(t *testing.T) {
	t.Parallel()

	// 100% has no empty cells — empty color escape must be omitted.
	out := RenderColoredBar(100, "red", BarEmptyColor)
	if strings.Contains(out, BarEmptyColor) {
		t.Fatalf("full bar must not emit empty color escape: %q", out)
	}
}

func TestRenderColoredBarEmptySuppressesFillEscape(t *testing.T) {
	t.Parallel()

	// 0% has no filled cells — fill color escape must be omitted.
	out := RenderColoredBar(0, "green", BarEmptyColor)
	if strings.Contains(out, "fg=green") {
		t.Fatalf("empty bar must not emit fill color escape: %q", out)
	}
}
