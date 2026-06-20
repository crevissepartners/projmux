package theme

import "testing"

func TestResolveThemeGlobalValuesResolveWithGlobalSource(t *testing.T) {
	t.Parallel()

	got := ResolveTheme(ThemeConfig{Muted: "#888888", Accent: "#123456"})

	if got.Muted.Value.Hex != "#888888" || got.Muted.Source != SourceGlobal {
		t.Fatalf("muted = %#v, want global #888888", got.Muted)
	}
	if got.Accent.Value.Hex != "#123456" || got.Accent.Source != SourceGlobal {
		t.Fatalf("accent = %#v, want global #123456", got.Accent)
	}
}

func TestResolveThemeGlobalMissingFallsBackToBuiltInFallback(t *testing.T) {
	t.Parallel()

	got := ResolveTheme(ThemeConfig{})

	if got.Background.Value.Hex != "#182226" || got.Background.Source != SourceFallback {
		t.Fatalf("background = %#v, want fallback projmux-dark background", got.Background)
	}
	if got.Preset.Value != "projmux-dark" || got.Preset.Source != SourceFallback {
		t.Fatalf("preset = %#v, want fallback projmux-dark", got.Preset)
	}
}

func TestResolveThemePresetFillsMissingColorTokens(t *testing.T) {
	t.Parallel()

	got := ResolveTheme(ThemeConfig{Preset: "forest"})

	if got.Background.Value.Hex != "#14201a" || got.Background.Source != SourceGlobal {
		t.Fatalf("background = %#v, want forest/global", got.Background)
	}
	if got.Warning.Value.Hex != "#e5c45f" || got.Warning.Source != SourceGlobal {
		t.Fatalf("warning = %#v, want forest/global", got.Warning)
	}
}

func TestResolveThemeExplicitColorOverridesPreset(t *testing.T) {
	t.Parallel()

	got := ResolveTheme(ThemeConfig{Preset: "forest", Background: "#123456"})

	if got.Background.Value.Hex != "#123456" || got.Background.Source != SourceGlobal {
		t.Fatalf("background = %#v, want explicit global #123456", got.Background)
	}
	if got.Surface.Value.Hex != "#1b2b22" || got.Surface.Source != SourceGlobal {
		t.Fatalf("surface = %#v, want preset-filled forest surface", got.Surface)
	}
}

func TestResolveThemeFontDesiredValuesUseGlobalThenFallback(t *testing.T) {
	t.Parallel()

	got := ResolveTheme(ThemeConfig{FontFamily: "JetBrains Mono", FontSize: "12"})

	if got.FontFamily.Value != "JetBrains Mono" || got.FontFamily.Source != SourceGlobal {
		t.Fatalf("font family = %#v, want global JetBrains Mono", got.FontFamily)
	}
	if got.FontSize.Value != 12 || got.FontSize.Source != SourceGlobal {
		t.Fatalf("font size = %#v, want global 12", got.FontSize)
	}

	unset := ResolveTheme(ThemeConfig{})
	if unset.FontFamily.Value != "" || unset.FontFamily.Source != SourceFallback {
		t.Fatalf("unset font family = %#v, want fallback empty", unset.FontFamily)
	}
}

func TestTmuxRenderTokensFallbackPreservesBuiltInPalette(t *testing.T) {
	t.Parallel()

	got := TmuxRenderTokensFromEffective(ResolveTheme(ThemeConfig{}))
	want := TmuxRenderTokens{
		WindowInactiveBg: TmuxWindowInactiveBg,
		WindowInactiveFg: TmuxWindowInactiveFg,
		WindowActiveBg:   TmuxWindowActiveBg,
		WindowActiveFg:   TmuxWindowActiveFg,
		StatusBg:         TmuxWindowInactiveBg,
		StatusFg:         TmuxWindowInactiveFg,
	}
	if got != want {
		t.Fatalf("fallback tmux render tokens = %#v, want %#v", got, want)
	}
}

func TestTmuxRenderTokensUseGlobalNearest256ColorsWithoutFallbackLeak(t *testing.T) {
	t.Parallel()

	got := TmuxRenderTokensFromEffective(ResolveTheme(ThemeConfig{
		Background:    "#ff0000",
		SurfaceActive: "#0000ff",
		Foreground:    "#00ff00",
	}))
	fallback := TmuxRenderTokensFromEffective(ResolveTheme(ThemeConfig{}))

	if got.WindowInactiveBg == "" || got.WindowInactiveFg == "" || got.WindowActiveBg == "" || got.WindowActiveFg == "" {
		t.Fatalf("global tmux render tokens = %#v, want populated colourN tokens", got)
	}
	if got.WindowInactiveBg == fallback.WindowInactiveBg || got.WindowInactiveFg == fallback.WindowInactiveFg || got.WindowActiveBg == fallback.WindowActiveBg || got.WindowActiveFg == fallback.WindowActiveFg {
		t.Fatalf("global tmux render tokens = %#v, must not reuse fallback tokens %#v", got, fallback)
	}
	if got.StatusBg != got.WindowInactiveBg || got.StatusFg != got.WindowInactiveFg {
		t.Fatalf("status tokens = bg %q fg %q, want inactive window bg/fg %#v", got.StatusBg, got.StatusFg, got)
	}
}

func TestResolveThemeUnknownPresetIgnoresGlobalAndWarns(t *testing.T) {
	t.Parallel()

	got := ResolveTheme(ThemeConfig{Preset: "does-not-exist", Background: "#111111"})

	if got.Background.Value.Hex != "#182226" || got.Background.Source != SourceFallback {
		t.Fatalf("background = %#v, want fallback after invalid global preset", got.Background)
	}
	requireThemeWarning(t, got, SourceGlobal, "preset")
}

func TestResolveThemeInvalidColorIgnoresGlobalAndWarns(t *testing.T) {
	t.Parallel()

	got := ResolveTheme(ThemeConfig{Background: "blue"})

	if got.Background.Value.Hex != "#182226" || got.Background.Source != SourceFallback {
		t.Fatalf("background = %#v, want fallback after invalid global color", got.Background)
	}
	requireThemeWarning(t, got, SourceGlobal, "background")
}

func TestResolveThemeInvalidFontIgnoresGlobalAndWarns(t *testing.T) {
	t.Parallel()

	got := ResolveTheme(ThemeConfig{FontFamily: "JetBrains Mono", FontSize: "0"})

	if got.FontFamily.Value != "" || got.FontFamily.Source != SourceFallback {
		t.Fatalf("font family = %#v, want fallback after invalid global font size", got.FontFamily)
	}
	if got.FontSize.Value != 0 || got.FontSize.Source != SourceFallback {
		t.Fatalf("font size = %#v, want fallback after invalid global font size", got.FontSize)
	}
	requireThemeWarning(t, got, SourceGlobal, "font_size")
}

func TestResolveThemeInvalidFontFamilyIgnoresGlobalAndWarns(t *testing.T) {
	t.Parallel()

	got := ResolveTheme(ThemeConfig{FontFamily: "Bad\x7fFont"})

	if got.FontFamily.Value != "" || got.FontFamily.Source != SourceFallback {
		t.Fatalf("font family = %#v, want fallback after invalid global font family", got.FontFamily)
	}
	requireThemeWarning(t, got, SourceGlobal, "font_family")
}

func TestResolveThemeEveryEffectiveFieldHasGlobalOrFallbackSource(t *testing.T) {
	t.Parallel()

	got := ResolveTheme(ThemeConfig{})
	for _, field := range got.Fields() {
		switch field.Source {
		case SourceGlobal, SourceFallback:
		default:
			t.Fatalf("field %q source = %q, want global/fallback", field.Name, field.Source)
		}
	}
}

func TestColorSpecEncodesTruecolorAndTmuxMapping(t *testing.T) {
	t.Parallel()

	got := ResolveTheme(ThemeConfig{Accent: "#123456"})
	if got.Accent.Value.TruecolorFG() != "38;2;18;52;86" {
		t.Fatalf("accent truecolor fg = %q, want exact RGB SGR token", got.Accent.Value.TruecolorFG())
	}
	if got.Accent.Value.Tmux == "" {
		t.Fatalf("accent tmux token empty, want nearest colourN mapping")
	}
}

func requireThemeWarning(t *testing.T, got EffectiveTheme, source Source, field string) {
	t.Helper()
	for _, warning := range got.Warnings {
		if warning.Source == source && warning.Field == field {
			return
		}
	}
	t.Fatalf("warnings = %#v, want %s.%s warning", got.Warnings, source, field)
}
