package usagecmd

import (
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/theme"
)

// bright_phase2_test.go covers the B1 theme threading for the usage HUD
// subprocess (bright preset roadmap Phase 2): ApplyStatusTheme repoints the
// role map, a light status background derives dark-contrast fg colors, and
// restoring the fallback theme is byte-identical to the historical literals.

func usageLightThemeConfig() theme.ThemeConfig {
	return theme.ThemeConfig{
		Background:       "#f5f5f0",
		Surface:          "#f2f2ee",
		StatusBackground: "#e8e8e2",
		SurfaceActive:    "#d8d8d0",
		ChromeForeground: "#2a2e32",
		TextPrimary:      "#1f2428",
		Foreground:       "#1f2428",
		Muted:            "#6a7076",
		Accent:           "#0f7b6c",
		Critical:         "#c62828",
		Warning:          "#9a6700",
		Progress:         "#0757ba",
		Success:          "#1a7f37",
		ActionRequired:   "#b35900",
		PaneActiveBg:     "#e2e2da",
		Focus:            "#0757ba",
	}
}

// withUsageStatusTheme applies a theme, runs fn, and restores the fallback.
// NOT parallel-safe by design; callers stay serial like the app package's
// native-UI repaint tests.
func withUsageStatusTheme(effective theme.EffectiveTheme, fn func()) {
	ApplyStatusTheme(effective)
	defer ApplyStatusTheme(theme.ResolveTheme(theme.ThemeConfig{}))
	fn()
}

// TestUsageHUDLightThemeDerivesDarkFg: under a light status background the HUD
// model label, age indicator, and bar colors leave the dark-tuned literals.
func TestUsageHUDLightThemeDerivesDarkFg(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	models := []modelDisplay{{label: "Claude", shortLabel: "C", hasFive: true, fivePct: 42, showAge: true, lastSync: now.Add(-2 * time.Hour)}}

	withUsageStatusTheme(theme.ResolveTheme(usageLightThemeConfig()), func() {
		out := renderTierLongHUDWithAge(models, now)
		if strings.Contains(out, "#[fg="+theme.TmuxAccentAIFg) {
			t.Fatalf("HUD label still carries %s under a light theme: %q", theme.TmuxAccentAIFg, out)
		}
		if strings.Contains(out, "#[fg="+theme.TmuxMutedFg+"]") {
			t.Fatalf("HUD age indicator still carries %s under a light theme: %q", theme.TmuxMutedFg, out)
		}
		if !strings.Contains(out, "#[fg=#") {
			t.Fatalf("HUD carries no hex-derived fg under a light theme: %q", out)
		}
		// Bar fill follows the explicit success token; empty cells leave the
		// dark colour238 literal.
		pair := renderHUDPair("5h", 42)
		if !strings.Contains(pair, "#1a7f37") {
			t.Fatalf("HUD bar fill = %q, want explicit light success #1a7f37", pair)
		}
		if strings.Contains(pair, theme.TmuxUsageEmptyFg) {
			t.Fatalf("HUD bar empty cells still carry %s under a light theme: %q", theme.TmuxUsageEmptyFg, pair)
		}
	})
}

// TestUsageHUDFallbackByteIdentity locks the HUD output under the fallback
// role state to the exact bytes captured at main fa3da53 (pre-Phase 2).
//
// The only intentional drift since that capture is the `~~` suffix inside the
// age indicator: the 2h fixture is past veryStaleAfter, and the age indicator
// now restores the legacy stale marker. Every colour role in the expectation
// is unchanged, which is what this test exists to lock — including the muted
// colour244 the indicator keeps at that tier.
func TestUsageHUDFallbackByteIdentity(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	models := []modelDisplay{
		{label: "Claude", shortLabel: "C", hasFive: true, fivePct: 42, hasWeek: true, weekPct: 88, showAge: true, lastSync: now.Add(-2 * time.Hour)},
		{label: "Codex", shortLabel: "X", hasFive: true, fivePct: 97},
	}
	want := "#[fg=colour121,bold]Claude#[default] #[fg=colour244](2h~~)#[default] 5h [#[fg=colour72]████#[fg=colour238]░░░░░░] #[fg=colour72]42%#[default] · weekly [#[fg=colour214]█████████#[fg=colour238]░] #[fg=colour214]88%#[default]#[default]   #[fg=colour121,bold]Codex#[default] 5h [#[fg=colour160]██████████] #[fg=colour160]97%#[default]#[default]#[default]"
	if got := renderTierLongHUDWithAge(models, now); got != want {
		t.Fatalf("fallback usage HUD drifted:\n got %q\nwant %q", got, want)
	}
	if got, want := renderAgeIndicator(modelDisplay{showAge: true, lastSync: now.Add(-5 * time.Minute)}, now), "#[fg=colour245](5m)#[default]"; got != want {
		t.Fatalf("fallback age indicator drifted: %q, want %q", got, want)
	}
}
