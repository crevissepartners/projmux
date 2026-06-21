package theme

import "testing"

// TestTerminalPresetRidesTerminalBackground verifies the terminal-native preset
// pins background/surface to the terminal-default sentinel (so the terminal's
// own background shows through) while still painting foreground/accent/state
// chrome. Selecting the preset by name must behave like the user typing
// `background = "default"`, on both the tmux and ANSI render paths.
func TestTerminalPresetRidesTerminalBackground(t *testing.T) {
	t.Parallel()

	effective := ResolveTheme(ThemeConfig{Preset: "terminal"})

	// background/surface ride the terminal default; foreground/accent are real.
	if !IsThemeDefaultSpec(effective.Background.Value) {
		t.Fatalf("terminal background = %#v, want terminal-default sentinel", effective.Background)
	}
	if !IsThemeDefaultSpec(effective.Surface.Value) {
		t.Fatalf("terminal surface = %#v, want terminal-default sentinel", effective.Surface)
	}
	if effective.Foreground.Value.Hex == "" {
		t.Fatalf("terminal foreground = %#v, want a concrete color (fg has no sentinel)", effective.Foreground)
	}

	// tmux: pane/popup backgrounds emit bg=default.
	roles := RenderRolesFromEffective(effective)
	if roles.PaneInactiveBg != "default" {
		t.Fatalf("PaneInactiveBg = %q, want default", roles.PaneInactiveBg)
	}
	if roles.StatusBg != "default" {
		t.Fatalf("StatusBg = %q, want default (surface rides terminal)", roles.StatusBg)
	}

	// ANSI: the preset must still satisfy the design rubric — selecting it must
	// not collapse state colors (guarded broadly by the rubric test; here we just
	// confirm the preset resolves without dropping the layer).
	if effective.Accent.Value.Hex == "" {
		t.Fatalf("terminal accent = %#v, want a concrete accent", effective.Accent)
	}
}
