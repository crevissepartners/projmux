package theme

import "testing"

// daylight_preset_test.go pins the Phase 1 fully-light built-in preset: every
// backing surface is a concrete light color (option 2 — no dark status bar, no
// terminal-default sentinel), foreground text is dark, and the Phase 2
// light-background derivations actually fire for it.

func TestDaylightPresetIsFullyLight(t *testing.T) {
	t.Parallel()

	effective := ResolveTheme(ThemeConfig{Preset: "daylight"})
	for name, field := range map[string]ColorField{
		"background":        effective.Background,
		"surface":           effective.Surface,
		"status_background": effective.StatusBackground,
		"pane_active_bg":    effective.PaneActiveBg,
	} {
		if IsThemeDefaultSpec(field.Value) {
			t.Fatalf("daylight %s rides the terminal default; a fully-light preset must pin every surface", name)
		}
		if !colorFieldIsLight(field) {
			t.Fatalf("daylight %s = %q is not a light color", name, field.Value.Hex)
		}
	}
	if got, want := effective.Background.Value.Hex, "#f2efe9"; got != want {
		t.Fatalf("daylight background = %q, want warm paper %q", got, want)
	}
	if got, want := effective.StatusBackground.Value.Hex, "#dfdad0"; got != want {
		t.Fatalf("daylight status_background = %q, want explicit light status surface %q", got, want)
	}
	if effective.StatusBackground.Value.Hex == effective.Surface.Value.Hex {
		t.Fatalf("daylight status_background equals surface %q; it must be defined explicitly, not surface auto-fill", effective.Surface.Value.Hex)
	}

	// Text tokens must be dark on the light surfaces.
	for name, field := range map[string]ColorField{
		"chrome_foreground": effective.ChromeForeground,
		"text_primary":      effective.TextPrimary,
		"foreground":        effective.Foreground,
	} {
		r, g, b, ok := parseHexRGB(field.Value.Hex)
		if !ok {
			t.Fatalf("daylight %s = %q is not a hex color", name, field.Value.Hex)
		}
		if luma := rec601Luma(r, g, b); luma >= lightBackgroundLumaThreshold {
			t.Fatalf("daylight %s = %q luma %.1f; text must be dark on a light background", name, field.Value.Hex, luma)
		}
	}

	// pane_active_bg sinks: one tone darker than the pane body background
	// (inverse of the dark presets' relationship).
	br, bg, bb, _ := parseHexRGB(effective.Background.Value.Hex)
	ar, ag, ab, _ := parseHexRGB(effective.PaneActiveBg.Value.Hex)
	if rec601Luma(ar, ag, ab) >= rec601Luma(br, bg, bb) {
		t.Fatalf("daylight pane_active_bg %q is not darker than background %q", effective.PaneActiveBg.Value.Hex, effective.Background.Value.Hex)
	}
}

func TestDaylightPresetTriggersLightDerivations(t *testing.T) {
	t.Parallel()

	effective := ResolveTheme(ThemeConfig{Preset: "daylight"})

	render := RenderRolesFromEffective(effective)
	for role, got := range map[string]string{
		"decoration.cwd":        render.DecorationCwd,
		"status.text_secondary": render.StatusTextSecondary,
		"status.text_muted":     render.StatusTextMuted,
		"accent.ai_fg":          render.AccentAIFg,
		"usage.bar_empty":       render.UsageBarEmpty,
		"pane.border_muted_fg":  render.PaneBorderMutedFg,
	} {
		r, g, b, ok := parseHexRGB(got)
		if !ok {
			t.Fatalf("daylight %s = %q, want darkened #rrggbb derivation on light chrome", role, got)
		}
		if luma := rec601Luma(r, g, b); luma > contrastDarkenTargetLuma {
			t.Fatalf("daylight %s = %q luma %.1f, want <= %.1f", role, got, luma, contrastDarkenTargetLuma)
		}
	}

	ansi := ANSIRolesFromEffective(effective)
	if ansi.NotifyTitle == ANSINotifyTitleStart {
		t.Fatalf("daylight notify.title still renders the dark-chrome literal %q", ansi.NotifyTitle)
	}
	if ansi.TrustTrusted == ANSITrustTrustedStart {
		t.Fatalf("daylight trust.trusted still renders the dark-chrome literal %q", ansi.TrustTrusted)
	}
}
