package theme

import "testing"

// TestTerminalPresetRidesTerminalBackground verifies the terminal-native preset
// pins background/surface to the terminal-default sentinel (so the terminal's
// own background shows through) while still painting text/chrome foreground,
// accent, and state
// chrome. Selecting the preset by name must behave like the user typing
// `background = "default"`, on both the tmux and ANSI render paths.
func TestTerminalPresetRidesTerminalBackground(t *testing.T) {
	t.Parallel()

	for _, preset := range []string{"terminal", "terminal-cool", "terminal-warm", "blue-hour-terminal", "carbon-violet-terminal"} {
		t.Run(preset, func(t *testing.T) {
			t.Parallel()

			effective := ResolveTheme(ThemeConfig{Preset: preset})

			// background/surface ride the terminal default; text/chrome foreground and accent are real.
			if !IsThemeDefaultSpec(effective.Background.Value) {
				t.Fatalf("%s background = %#v, want terminal-default sentinel", preset, effective.Background)
			}
			if !IsThemeDefaultSpec(effective.Surface.Value) {
				t.Fatalf("%s surface = %#v, want terminal-default sentinel", preset, effective.Surface)
			}
			if effective.Foreground.Value.Hex == "" {
				t.Fatalf("%s foreground = %#v, want a concrete color (fg has no sentinel)", preset, effective.Foreground)
			}

			// tmux: pane/popup backgrounds emit bg=default.
			roles := RenderRolesFromEffective(effective)
			if roles.PaneInactiveBg != "default" {
				t.Fatalf("%s PaneInactiveBg = %q, want default", preset, roles.PaneInactiveBg)
			}
			if roles.StatusBg != "default" {
				t.Fatalf("%s StatusBg = %q, want default (surface rides terminal)", preset, roles.StatusBg)
			}

			// ANSI: the preset must still satisfy the design rubric — selecting it must
			// not collapse state colors (guarded broadly by the rubric test; here we just
			// confirm the preset resolves without dropping the layer).
			if effective.Accent.Value.Hex == "" {
				t.Fatalf("%s accent = %#v, want a concrete accent", preset, effective.Accent)
			}
		})
	}
}

func TestTerminalPresetPairsOnlyDefaultBackgroundAndSurface(t *testing.T) {
	t.Parallel()

	for base, terminal := range map[string]string{
		"blue-hour":     "blue-hour-terminal",
		"carbon-violet": "carbon-violet-terminal",
	} {
		t.Run(terminal, func(t *testing.T) {
			t.Parallel()

			baseTheme := ResolveTheme(ThemeConfig{Preset: base})
			terminalTheme := ResolveTheme(ThemeConfig{Preset: terminal})
			if !IsThemeDefaultSpec(terminalTheme.Background.Value) || !IsThemeDefaultSpec(terminalTheme.Surface.Value) {
				t.Fatalf("%s background/surface = %#v/%#v, want terminal default", terminal, terminalTheme.Background, terminalTheme.Surface)
			}
			if got := terminalTheme.SurfaceActive.Value.Hex; got != baseTheme.SurfaceActive.Value.Hex {
				t.Fatalf("%s surface_active = %q, want %s value %q", terminal, got, base, baseTheme.SurfaceActive.Value.Hex)
			}
			if got := terminalTheme.ChromeForeground.Value.Hex; got != baseTheme.ChromeForeground.Value.Hex {
				t.Fatalf("%s chrome_foreground = %q, want %s value %q", terminal, got, base, baseTheme.ChromeForeground.Value.Hex)
			}
			if got := terminalTheme.TextPrimary.Value.Hex; got != baseTheme.TextPrimary.Value.Hex {
				t.Fatalf("%s text_primary = %q, want %s value %q", terminal, got, base, baseTheme.TextPrimary.Value.Hex)
			}
			if got := terminalTheme.Foreground.Value.Hex; got != baseTheme.Foreground.Value.Hex {
				t.Fatalf("%s foreground = %q, want %s value %q", terminal, got, base, baseTheme.Foreground.Value.Hex)
			}
			if got := terminalTheme.Muted.Value.Hex; got != baseTheme.Muted.Value.Hex {
				t.Fatalf("%s muted = %q, want %s value %q", terminal, got, base, baseTheme.Muted.Value.Hex)
			}
			if got := terminalTheme.Accent.Value.Hex; got != baseTheme.Accent.Value.Hex {
				t.Fatalf("%s accent = %q, want %s value %q", terminal, got, base, baseTheme.Accent.Value.Hex)
			}
			if got := terminalTheme.Critical.Value.Hex; got != baseTheme.Critical.Value.Hex {
				t.Fatalf("%s critical = %q, want %s value %q", terminal, got, base, baseTheme.Critical.Value.Hex)
			}
			if got := terminalTheme.Warning.Value.Hex; got != baseTheme.Warning.Value.Hex {
				t.Fatalf("%s warning = %q, want %s value %q", terminal, got, base, baseTheme.Warning.Value.Hex)
			}
			if got := terminalTheme.Progress.Value.Hex; got != baseTheme.Progress.Value.Hex {
				t.Fatalf("%s progress = %q, want %s value %q", terminal, got, base, baseTheme.Progress.Value.Hex)
			}
			if got := terminalTheme.Success.Value.Hex; got != baseTheme.Success.Value.Hex {
				t.Fatalf("%s success = %q, want %s value %q", terminal, got, base, baseTheme.Success.Value.Hex)
			}
			if got := terminalTheme.ActionRequired.Value.Hex; got != baseTheme.ActionRequired.Value.Hex {
				t.Fatalf("%s action_required = %q, want %s value %q", terminal, got, base, baseTheme.ActionRequired.Value.Hex)
			}
			if got := terminalTheme.PaneActiveBg.Value.Hex; got != baseTheme.PaneActiveBg.Value.Hex {
				t.Fatalf("%s pane_active_bg = %q, want %s value %q", terminal, got, base, baseTheme.PaneActiveBg.Value.Hex)
			}
			if got := terminalTheme.Focus.Value.Hex; got != baseTheme.Focus.Value.Hex {
				t.Fatalf("%s focus = %q, want %s value %q", terminal, got, base, baseTheme.Focus.Value.Hex)
			}
		})
	}
}

func TestBlueHourTargetsInactivePaneBlueTint(t *testing.T) {
	t.Parallel()

	fixed := ResolveTheme(ThemeConfig{Preset: "blue-hour"})
	if got, want := fixed.Background.Value.Hex, "#1e1e2e"; got != want {
		t.Fatalf("blue-hour background = %q, want terminal palette background %q", got, want)
	}
	if got, want := fixed.Surface.Value.Hex, "#2d4a6e"; got != want {
		t.Fatalf("blue-hour surface = %q, want terminal palette selection background %q", got, want)
	}
	if got, want := fixed.Accent.Value.Hex, "#3d8fd1"; got != want {
		t.Fatalf("blue-hour accent = %q, want terminal blue %q", got, want)
	}
	if got, want := fixed.PaneActiveBg.Value.Hex, "#000000"; got != want {
		t.Fatalf("blue-hour pane_active_bg = %q, want true black %q", got, want)
	}

	terminal := ResolveTheme(ThemeConfig{Preset: "blue-hour-terminal"})
	if !IsThemeDefaultSpec(terminal.Background.Value) || !IsThemeDefaultSpec(terminal.Surface.Value) {
		t.Fatalf("blue-hour-terminal background/surface = %#v/%#v, want terminal default", terminal.Background, terminal.Surface)
	}
	if got, want := terminal.Accent.Value.Hex, "#3d8fd1"; got != want {
		t.Fatalf("blue-hour-terminal accent = %q, want terminal blue %q", got, want)
	}
}

func TestInspiredPresetPairsUseBlackActivePaneTint(t *testing.T) {
	t.Parallel()

	for preset, want := range map[string]string{
		"blue-hour":              "#000000",
		"blue-hour-terminal":     "#000000",
		"carbon-violet":          "#000000",
		"carbon-violet-terminal": "#000000",
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
