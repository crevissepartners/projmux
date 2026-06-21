package theme

import "testing"

func TestBlueHourTargetsInactivePaneBlueTint(t *testing.T) {
	t.Parallel()

	fixed := ResolveTheme(ThemeConfig{Preset: "blue-hour"})
	if got, want := fixed.Background.Value.Hex, "#1e1e2e"; got != want {
		t.Fatalf("blue-hour background = %q, want terminal palette background %q", got, want)
	}
	if got, want := fixed.Surface.Value.Hex, "#16242d"; got != want {
		t.Fatalf("blue-hour surface = %q, want midnight popup surface %q", got, want)
	}
	if got, want := fixed.StatusBackground.Value.Hex, "#132b38"; got != want {
		t.Fatalf("blue-hour status_background = %q, want ocean status surface %q", got, want)
	}
	if got, want := fixed.Accent.Value.Hex, "#3d8fd1"; got != want {
		t.Fatalf("blue-hour accent = %q, want terminal blue %q", got, want)
	}
	if got, want := fixed.PaneActiveBg.Value.Hex, "#000000"; got != want {
		t.Fatalf("blue-hour pane_active_bg = %q, want true black %q", got, want)
	}

}

func TestCarbonVioletUsesReadablePopupAndStatusSurfaces(t *testing.T) {
	t.Parallel()

	effective := ResolveTheme(ThemeConfig{Preset: "carbon-violet"})
	if got, want := effective.Surface.Value.Hex, "#23212b"; got != want {
		t.Fatalf("carbon-violet surface = %q, want darker readable popup surface %q", got, want)
	}
	if got, want := effective.StatusBackground.Value.Hex, "#1a1822"; got != want {
		t.Fatalf("carbon-violet status_background = %q, want darker status surface %q", got, want)
	}
	if got, want := effective.PaneActiveBg.Value.Hex, "#000000"; got != want {
		t.Fatalf("carbon-violet pane_active_bg = %q, want true black %q", got, want)
	}
}

func TestInspiredPresetPairsUseBlackActivePaneTint(t *testing.T) {
	t.Parallel()

	for preset, want := range map[string]string{
		"blue-hour":     "#000000",
		"carbon-violet": "#000000",
	} {
		t.Run(preset, func(t *testing.T) {
			t.Parallel()

			effective := ResolveTheme(ThemeConfig{Preset: preset})
			if got := effective.PaneActiveBg.Value.Hex; got != want {
				t.Fatalf("%s pane_active_bg = %q, want palette black %q", preset, got, want)
			}
		})
	}
}

func TestHighContrastPresetUsesStrongContrastPalette(t *testing.T) {
	t.Parallel()

	effective := ResolveTheme(ThemeConfig{Preset: "high-contrast"})
	for name, field := range map[string]ColorField{
		"background":        effective.Background,
		"surface":           effective.Surface,
		"foreground":        effective.Foreground,
		"chrome_foreground": effective.ChromeForeground,
		"text_primary":      effective.TextPrimary,
	} {
		want := "#000000"
		if name == "foreground" || name == "chrome_foreground" || name == "text_primary" {
			want = "#ffffff"
		}
		if got := field.Value.Hex; got != want {
			t.Fatalf("high-contrast %s = %q, want %q", name, got, want)
		}
	}
	if got, want := effective.SurfaceActive.Value.Hex, "#005fff"; got != want {
		t.Fatalf("high-contrast surface_active = %q, want vivid active surface %q", got, want)
	}
	if got, want := effective.PaneActiveBg.Value.Hex, "#080808"; got != want {
		t.Fatalf("high-contrast pane_active_bg = %q, want near-black active pane tint %q", got, want)
	}
	if got, want := effective.Focus.Value.Hex, "#00ffff"; got != want {
		t.Fatalf("high-contrast focus = %q, want vivid cyan focus %q", got, want)
	}
}
