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
