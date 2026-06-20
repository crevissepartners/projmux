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

func TestRenderRolesFallbackPreservesBuiltInPalette(t *testing.T) {
	t.Parallel()

	got := RenderRolesFromEffective(ResolveTheme(ThemeConfig{}))
	want := RenderRoles{
		WindowInactiveBg:  TmuxWindowInactiveBg,
		WindowInactiveFg:  TmuxWindowInactiveFg,
		WindowActiveBg:    TmuxWindowActiveBg,
		WindowActiveFg:    TmuxWindowActiveFg,
		StatusBg:          TmuxWindowInactiveBg,
		StatusFg:          TmuxWindowInactiveFg,
		PaneBorder:        TmuxPaneBorderFg,
		FocusBorder:       TmuxPaneActiveBorderFg,
		PaneTopicChipBg:   TmuxPaneActiveBg,
		PaneTopicChipFg:   TmuxPaneActiveFg,
		FocusPaneActiveBg: TmuxPaneActiveTintBg, // dedicated dark tint colour234
		StateWarning:      TmuxStateWarningFg,
		StateCritical:     TmuxStateCriticalFg,
		StateProgress:     TmuxStateProgressFg,
		StateSuccess:      TmuxStateSuccessFg,
		AIProgress:        TmuxStateProgressFg,
		AISuccess:         TmuxStateSuccessFg,
		AIActionRequired:  TmuxAIBadgeActionRequiredFg,
		GitSegmentFg:      TmuxGitSegmentFg,
		GitSegmentBg:      TmuxGitSegmentBg,
		GitStaged:         TmuxStateStagedFg,
		GitDirty:          TmuxStateDirtyFg,
		GitAhead:          TmuxStateAheadFg,
		GitBehind:         TmuxStateBehindFg,
		DecorationCwd:     TmuxDecorationCwdFg,
		DecorationGitLab:  TmuxDecorationGitLabFg,
		KubeContext:       TmuxKubeContextFg,
		KubeNamespace:     TmuxKubeNamespaceFg,
		IdentityBg:        TmuxIdentityBg,
		IdentityFg:        TmuxIdentityFg,
		ActionBg:          TmuxActionBg,
		ActionFg:          TmuxActionFg,
		DividerFg:         TmuxDividerFg,
	}
	if got != want {
		t.Fatalf("fallback render roles = %#v, want %#v", got, want)
	}
}

func TestRenderRolesStateAIFallbackEqualHistoricalLiterals(t *testing.T) {
	t.Parallel()

	got := RenderRolesFromEffective(ResolveTheme(ThemeConfig{}))

	// State/severity cluster fallback must equal the historical palette
	// literals so generated fallback output stays byte-identical.
	if got.StateWarning != "colour214" {
		t.Fatalf("state.warning = %q, want colour214", got.StateWarning)
	}
	if got.StateCritical != "colour160" {
		t.Fatalf("state.critical = %q, want colour160", got.StateCritical)
	}
	if got.StateProgress != "colour220" {
		t.Fatalf("state.progress = %q, want colour220", got.StateProgress)
	}
	if got.StateSuccess != "colour72" {
		t.Fatalf("state.success = %q, want colour72", got.StateSuccess)
	}

	// AI cluster: progress/success reuse the state colors; action_required is
	// its OWN role and must remain colour214 (NOT merged into critical).
	if got.AIProgress != got.StateProgress {
		t.Fatalf("ai.progress = %q, want = state.progress %q", got.AIProgress, got.StateProgress)
	}
	if got.AISuccess != got.StateSuccess {
		t.Fatalf("ai.success = %q, want = state.success %q", got.AISuccess, got.StateSuccess)
	}
	if got.AIActionRequired != "colour214" {
		t.Fatalf("ai.action_required = %q, want colour214", got.AIActionRequired)
	}
	if got.AIActionRequired == got.StateCritical {
		t.Fatalf("ai.action_required = %q must never equal state.critical %q", got.AIActionRequired, got.StateCritical)
	}
}

func TestRenderRolesExplicitThemeRepaintsStateTierAButNotTierC(t *testing.T) {
	t.Parallel()

	got := RenderRolesFromEffective(ResolveTheme(ThemeConfig{
		Warning:  "#00ff00",
		Critical: "#0000ff",
		Accent:   "#ffff00",
	}))
	fallback := RenderRolesFromEffective(ResolveTheme(ThemeConfig{}))

	// Tier A: warning/critical repaint from the explicit public tokens.
	if got.StateWarning == fallback.StateWarning {
		t.Fatalf("state.warning = %q, want repainted from explicit warning, not literal", got.StateWarning)
	}
	if got.StateCritical == fallback.StateCritical {
		t.Fatalf("state.critical = %q, want repainted from explicit critical, not literal", got.StateCritical)
	}

	// progress/success/action_required are now Tier A public tokens, but they
	// are UNSET here, so they must keep their fallback literals. This also proves
	// action_required is independent of critical — an explicit critical never
	// bleeds into action_required.
	if got.StateProgress != fallback.StateProgress {
		t.Fatalf("state.progress = %q, want unchanged literal %q (Tier C)", got.StateProgress, fallback.StateProgress)
	}
	if got.StateSuccess != fallback.StateSuccess {
		t.Fatalf("state.success = %q, want unchanged literal %q (Tier C)", got.StateSuccess, fallback.StateSuccess)
	}
	if got.AIActionRequired != fallback.AIActionRequired {
		t.Fatalf("ai.action_required = %q, want unchanged literal %q (independent of critical)", got.AIActionRequired, fallback.AIActionRequired)
	}
	if got.AIActionRequired == got.StateCritical {
		t.Fatalf("ai.action_required = %q must never equal repainted state.critical %q", got.AIActionRequired, got.StateCritical)
	}
}

func TestRenderRolesExplicitThemeRepaintsTierABChromeRoles(t *testing.T) {
	t.Parallel()

	got := RenderRolesFromEffective(ResolveTheme(ThemeConfig{
		Background:    "#ff0000",
		SurfaceActive: "#0000ff",
		Muted:         "#00ff00",
		Accent:        "#ffff00",
	}))
	fallback := RenderRolesFromEffective(ResolveTheme(ThemeConfig{}))

	// Tier A/B chrome roles must follow the explicit theme, not the literal.
	// (focus.border and focus.pane_active_bg are now Tier A roles driven by the
	// `focus`/`pane_active_bg` tokens; they are UNSET here so they keep their
	// literals and are intentionally not asserted in this test.)
	if got.PaneBorder == fallback.PaneBorder {
		t.Fatalf("pane.border = %q, want derived from muted, not literal", got.PaneBorder)
	}
	if got.PaneTopicChipBg == fallback.PaneTopicChipBg {
		t.Fatalf("pane.topic_chip_bg = %q, want derived from accent", got.PaneTopicChipBg)
	}
	// contrastFg: yellow accent is light -> dark fg.
	if got.PaneTopicChipFg != "colour16" {
		t.Fatalf("pane.topic_chip_fg = %q, want dark contrast fg colour16 on light accent", got.PaneTopicChipFg)
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

func TestPhase6NewPublicTokensRepaintTmuxRoles(t *testing.T) {
	t.Parallel()

	fallback := RenderRolesFromEffective(ResolveTheme(ThemeConfig{}))

	// progress repaints state.progress + ai.progress.
	progress := RenderRolesFromEffective(ResolveTheme(ThemeConfig{Progress: "#112233"}))
	if progress.StateProgress == fallback.StateProgress || progress.AIProgress == fallback.AIProgress {
		t.Fatalf("progress token did not repaint state/ai progress: %q / %q", progress.StateProgress, progress.AIProgress)
	}
	if progress.StateProgress != progress.AIProgress {
		t.Fatalf("state.progress %q must equal ai.progress %q", progress.StateProgress, progress.AIProgress)
	}

	// success repaints state.success + ai.success.
	success := RenderRolesFromEffective(ResolveTheme(ThemeConfig{Success: "#445566"}))
	if success.StateSuccess == fallback.StateSuccess || success.AISuccess == fallback.AISuccess {
		t.Fatalf("success token did not repaint state/ai success: %q / %q", success.StateSuccess, success.AISuccess)
	}

	// action_required repaints ai.action_required, but never equals state.critical.
	ar := RenderRolesFromEffective(ResolveTheme(ThemeConfig{ActionRequired: "#778899"}))
	if ar.AIActionRequired == fallback.AIActionRequired {
		t.Fatalf("action_required token did not repaint ai.action_required: %q", ar.AIActionRequired)
	}
	if ar.AIActionRequired == ar.StateCritical {
		t.Fatalf("ai.action_required %q must never equal state.critical %q", ar.AIActionRequired, ar.StateCritical)
	}

	// action_required set to the same color as critical still resolves to its
	// own role; setting critical does not change action_required either.
	both := RenderRolesFromEffective(ResolveTheme(ThemeConfig{Critical: "#abcdef"}))
	if both.AIActionRequired != fallback.AIActionRequired {
		t.Fatalf("ai.action_required = %q, want unchanged when only critical is set", both.AIActionRequired)
	}

	// pane_active_bg repaints FocusPaneActiveBg.
	pab := RenderRolesFromEffective(ResolveTheme(ThemeConfig{PaneActiveBg: "#010203"}))
	if pab.FocusPaneActiveBg == fallback.FocusPaneActiveBg {
		t.Fatalf("pane_active_bg token did not repaint FocusPaneActiveBg: %q", pab.FocusPaneActiveBg)
	}

	// focus repaints FocusBorder.
	focus := RenderRolesFromEffective(ResolveTheme(ThemeConfig{Focus: "#0a0b0c"}))
	if focus.FocusBorder == fallback.FocusBorder {
		t.Fatalf("focus token did not repaint FocusBorder: %q", focus.FocusBorder)
	}
}

func TestPhase6NewPublicTokensRepaintANSIRoles(t *testing.T) {
	t.Parallel()

	fallback := ANSIRolesFromEffective(ResolveTheme(ThemeConfig{}))

	progress := ANSIRolesFromEffective(ResolveTheme(ThemeConfig{Progress: "#112233"}))
	if progress.StateProgress == fallback.StateProgress || progress.AIBadgeProgress == fallback.AIBadgeProgress {
		t.Fatalf("progress did not repaint ANSI state/ai progress: %q / %q", progress.StateProgress, progress.AIBadgeProgress)
	}

	success := ANSIRolesFromEffective(ResolveTheme(ThemeConfig{Success: "#445566"}))
	if success.AIBadgeSuccess == fallback.AIBadgeSuccess {
		t.Fatalf("success did not repaint ANSI ai.success: %q", success.AIBadgeSuccess)
	}

	ar := ANSIRolesFromEffective(ResolveTheme(ThemeConfig{ActionRequired: "#778899"}))
	if ar.AIBadgeActionRequired == fallback.AIBadgeActionRequired {
		t.Fatalf("action_required did not repaint ANSI ai.action_required: %q", ar.AIBadgeActionRequired)
	}
	if ar.AIBadgeActionRequired == ar.StateCritical {
		t.Fatalf("ANSI ai.action_required %q must never equal state.critical %q", ar.AIBadgeActionRequired, ar.StateCritical)
	}
}

func TestPhase6FieldsIncludeNewTokens(t *testing.T) {
	t.Parallel()

	got := ResolveTheme(ThemeConfig{})
	names := map[string]bool{}
	for _, field := range got.Fields() {
		names[field.Name] = true
	}
	for _, token := range []ColorToken{TokenProgress, TokenSuccess, TokenActionRequired, TokenPaneActiveBg, TokenFocus} {
		if !names[string(token)] {
			t.Fatalf("Fields() missing new token %q", token)
		}
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
