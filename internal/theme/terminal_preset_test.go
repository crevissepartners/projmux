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

func TestBlueHourTargetsInactivePaneBlueTint(t *testing.T) {
	t.Parallel()

	fixed := ResolveTheme(ThemeConfig{Preset: "blue-hour"})
	if got, want := fixed.Background.Value.Hex, "#1e2a44"; got != want {
		t.Fatalf("blue-hour background = %q, want inactive pane blue tint %q", got, want)
	}
	if got, want := fixed.Accent.Value.Hex, "#89b4fa"; got != want {
		t.Fatalf("blue-hour accent = %q, want terminal blue %q", got, want)
	}
	if got, want := fixed.PaneActiveBg.Value.Hex, "#000000"; got != want {
		t.Fatalf("blue-hour pane_active_bg = %q, want black %q", got, want)
	}

	terminal := ResolveTheme(ThemeConfig{Preset: "blue-hour-terminal"})
	if !IsThemeDefaultSpec(terminal.Background.Value) || !IsThemeDefaultSpec(terminal.Surface.Value) {
		t.Fatalf("blue-hour-terminal background/surface = %#v/%#v, want terminal default", terminal.Background, terminal.Surface)
	}
	if got, want := terminal.Accent.Value.Hex, "#89b4fa"; got != want {
		t.Fatalf("blue-hour-terminal accent = %q, want terminal blue %q", got, want)
	}
}

func TestInspiredPresetPairsUseBlackActivePaneTint(t *testing.T) {
	t.Parallel()

	for _, preset := range []string{"blue-hour", "blue-hour-terminal", "carbon-violet", "carbon-violet-terminal"} {
		t.Run(preset, func(t *testing.T) {
			t.Parallel()

			effective := ResolveTheme(ThemeConfig{Preset: preset})
			if got, want := effective.PaneActiveBg.Value.Hex, "#000000"; got != want {
				t.Fatalf("%s pane_active_bg = %q, want black %q", preset, got, want)
			}
		})
	}
}
