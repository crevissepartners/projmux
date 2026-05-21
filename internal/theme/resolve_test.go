package theme

import "testing"

func TestResolveThemeProjectValuesOverrideGlobalValues(t *testing.T) {
	t.Parallel()

	got := ResolveTheme(
		ThemeConfig{Background: "#222222"},
		ThemeConfig{Background: "#111111"},
	)

	if got.Background.Value.Hex != "#111111" || got.Background.Source != SourceProject {
		t.Fatalf("background = %#v, want project #111111", got.Background)
	}
}

func TestResolveThemeProjectMissingFallsBackToGlobal(t *testing.T) {
	t.Parallel()

	got := ResolveTheme(
		ThemeConfig{Muted: "#888888"},
		ThemeConfig{Accent: "#123456"},
	)

	if got.Muted.Value.Hex != "#888888" || got.Muted.Source != SourceGlobal {
		t.Fatalf("muted = %#v, want global #888888", got.Muted)
	}
	if got.Accent.Value.Hex != "#123456" || got.Accent.Source != SourceProject {
		t.Fatalf("accent = %#v, want project #123456", got.Accent)
	}
}

func TestResolveThemeGlobalMissingFallsBackToBuiltInFallback(t *testing.T) {
	t.Parallel()

	got := ResolveTheme(ThemeConfig{}, ThemeConfig{})

	if got.Background.Value.Hex != "#182226" || got.Background.Source != SourceFallback {
		t.Fatalf("background = %#v, want fallback projmux-dark background", got.Background)
	}
	if got.Preset.Value != "projmux-dark" || got.Preset.Source != SourceFallback {
		t.Fatalf("preset = %#v, want fallback projmux-dark", got.Preset)
	}
}

func TestResolveThemePresetFillsMissingColorTokens(t *testing.T) {
	t.Parallel()

	got := ResolveTheme(ThemeConfig{Preset: "forest"}, ThemeConfig{})

	if got.Background.Value.Hex != "#14201a" || got.Background.Source != SourceGlobal {
		t.Fatalf("background = %#v, want forest/global", got.Background)
	}
	if got.Warning.Value.Hex != "#e5c45f" || got.Warning.Source != SourceGlobal {
		t.Fatalf("warning = %#v, want forest/global", got.Warning)
	}
}

func TestResolveThemeExplicitColorOverridesPreset(t *testing.T) {
	t.Parallel()

	got := ResolveTheme(ThemeConfig{Preset: "forest", Background: "#123456"}, ThemeConfig{})

	if got.Background.Value.Hex != "#123456" || got.Background.Source != SourceGlobal {
		t.Fatalf("background = %#v, want explicit global #123456", got.Background)
	}
	if got.Surface.Value.Hex != "#1b2b22" || got.Surface.Source != SourceGlobal {
		t.Fatalf("surface = %#v, want preset-filled forest surface", got.Surface)
	}
}

func TestResolveThemeFontDesiredValuesUseProjectGlobalFallback(t *testing.T) {
	t.Parallel()

	got := ResolveTheme(
		ThemeConfig{FontFamily: "Cascadia Mono", FontSize: "12"},
		ThemeConfig{FontFamily: "JetBrains Mono"},
	)

	if got.FontFamily.Value != "JetBrains Mono" || got.FontFamily.Source != SourceProject {
		t.Fatalf("font family = %#v, want project JetBrains Mono", got.FontFamily)
	}
	if got.FontSize.Value != 12 || got.FontSize.Source != SourceGlobal {
		t.Fatalf("font size = %#v, want global 12", got.FontSize)
	}
}

func TestTmuxRenderTokensFallbackPreservesBuiltInPalette(t *testing.T) {
	t.Parallel()

	got := TmuxRenderTokensFromEffective(ResolveTheme(ThemeConfig{}, ThemeConfig{}))
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

func TestTmuxRenderTokensUseProjectNearest256ColorsWithoutGlobalLeak(t *testing.T) {
	t.Parallel()

	got := TmuxRenderTokensFromEffective(ResolveTheme(
		ThemeConfig{
			Background:    "#ff0000",
			SurfaceActive: "#0000ff",
			Foreground:    "#00ff00",
		},
		ThemeConfig{
			Background:    "#010203",
			SurfaceActive: "#040506",
			Foreground:    "#aabbcc",
		},
	))
	global := TmuxRenderTokensFromEffective(ResolveTheme(
		ThemeConfig{},
		ThemeConfig{
			Background:    "#ff0000",
			SurfaceActive: "#0000ff",
			Foreground:    "#00ff00",
		},
	))

	if got.WindowInactiveBg == "" || got.WindowInactiveFg == "" || got.WindowActiveBg == "" || got.WindowActiveFg == "" {
		t.Fatalf("project tmux render tokens = %#v, want populated colourN tokens", got)
	}
	if got.WindowInactiveBg == global.WindowInactiveBg || got.WindowInactiveFg == global.WindowInactiveFg || got.WindowActiveBg == global.WindowActiveBg || got.WindowActiveFg == global.WindowActiveFg {
		t.Fatalf("project tmux render tokens = %#v, must not reuse global tokens %#v", got, global)
	}
	if got.StatusBg != got.WindowInactiveBg || got.StatusFg != got.WindowInactiveFg {
		t.Fatalf("status tokens = bg %q fg %q, want inactive window bg/fg %#v", got.StatusBg, got.StatusFg, got)
	}
}

func TestResolveThemeUnknownPresetIgnoresLayerAndWarns(t *testing.T) {
	t.Parallel()

	got := ResolveTheme(
		ThemeConfig{Background: "#222222"},
		ThemeConfig{Preset: "does-not-exist", Background: "#111111"},
	)

	if got.Background.Value.Hex != "#222222" || got.Background.Source != SourceGlobal {
		t.Fatalf("background = %#v, want global after invalid project preset", got.Background)
	}
	requireThemeWarning(t, got, SourceProject, "preset")
}

func TestResolveThemeInvalidColorIgnoresLayerAndWarns(t *testing.T) {
	t.Parallel()

	got := ResolveTheme(
		ThemeConfig{Background: "#222222"},
		ThemeConfig{Background: "blue"},
	)

	if got.Background.Value.Hex != "#222222" || got.Background.Source != SourceGlobal {
		t.Fatalf("background = %#v, want global after invalid project color", got.Background)
	}
	requireThemeWarning(t, got, SourceProject, "background")
}

func TestResolveThemeInvalidFontIgnoresLayerAndWarns(t *testing.T) {
	t.Parallel()

	got := ResolveTheme(
		ThemeConfig{FontFamily: "Cascadia Mono", FontSize: "13"},
		ThemeConfig{FontFamily: "JetBrains Mono", FontSize: "0"},
	)

	if got.FontFamily.Value != "Cascadia Mono" || got.FontFamily.Source != SourceGlobal {
		t.Fatalf("font family = %#v, want global after invalid project font size", got.FontFamily)
	}
	if got.FontSize.Value != 13 || got.FontSize.Source != SourceGlobal {
		t.Fatalf("font size = %#v, want global 13 after invalid project font size", got.FontSize)
	}
	requireThemeWarning(t, got, SourceProject, "font_size")
}

func TestResolveThemeInvalidFontFamilyIgnoresLayerAndWarns(t *testing.T) {
	t.Parallel()

	got := ResolveTheme(
		ThemeConfig{FontFamily: "Cascadia Mono"},
		ThemeConfig{FontFamily: "Bad\x7fFont"},
	)

	if got.FontFamily.Value != "Cascadia Mono" || got.FontFamily.Source != SourceGlobal {
		t.Fatalf("font family = %#v, want global after invalid project font family", got.FontFamily)
	}
	requireThemeWarning(t, got, SourceProject, "font_family")
}

func TestResolveThemeEveryEffectiveFieldHasSourceLabel(t *testing.T) {
	t.Parallel()

	got := ResolveTheme(ThemeConfig{}, ThemeConfig{})
	for _, field := range got.Fields() {
		switch field.Source {
		case SourceProject, SourceGlobal, SourceFallback:
		default:
			t.Fatalf("field %q source = %q, want project/global/fallback", field.Name, field.Source)
		}
	}
}

func TestColorSpecEncodesTruecolorAndTmuxMapping(t *testing.T) {
	t.Parallel()

	got := ResolveTheme(ThemeConfig{}, ThemeConfig{Accent: "#123456"})
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
