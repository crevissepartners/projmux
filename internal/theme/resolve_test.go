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

	if !IsThemeDefaultSpec(got.Background.Value) || got.Background.Source != SourceFallback {
		t.Fatalf("background = %#v, want fallback terminal-default background", got.Background)
	}
	if got.Preset.Value != "projmux" || got.Preset.Source != SourceFallback {
		t.Fatalf("preset = %#v, want fallback projmux", got.Preset)
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

func TestPresetNamesPutPrimaryChoicesFirst(t *testing.T) {
	t.Parallel()

	got := PresetNames()
	wantPrefix := []string{"projmux", "high-contrast", "blue-hour", "carbon-violet"}
	if len(got) < len(wantPrefix) {
		t.Fatalf("PresetNames() = %#v, want prefix %#v", got, wantPrefix)
	}
	for i, want := range wantPrefix {
		if got[i] != want {
			t.Fatalf("PresetNames()[%d] = %q, want %q in %#v", i, got[i], want, got)
		}
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

func TestResolveThemeLegacyForegroundFillsSplitForegroundTokens(t *testing.T) {
	t.Parallel()

	got := ResolveTheme(ThemeConfig{Foreground: "#eeeeee"})

	for name, field := range map[string]ColorField{
		"foreground":        got.Foreground,
		"chrome_foreground": got.ChromeForeground,
		"text_primary":      got.TextPrimary,
	} {
		if field.Value.Hex != "#eeeeee" || field.Source != SourceGlobal {
			t.Fatalf("%s = %#v, want global legacy foreground fill #eeeeee", name, field)
		}
	}
}

func TestResolveThemeSplitForegroundTokensOverrideLegacyFill(t *testing.T) {
	t.Parallel()

	got := ResolveTheme(ThemeConfig{
		Foreground:       "#eeeeee",
		ChromeForeground: "#aabbcc",
		TextPrimary:      "#112233",
	})

	if got.Foreground.Value.Hex != "#eeeeee" {
		t.Fatalf("foreground = %#v, want legacy value readable", got.Foreground)
	}
	if got.ChromeForeground.Value.Hex != "#aabbcc" {
		t.Fatalf("chrome_foreground = %#v, want explicit split value", got.ChromeForeground)
	}
	if got.TextPrimary.Value.Hex != "#112233" {
		t.Fatalf("text_primary = %#v, want explicit split value", got.TextPrimary)
	}
}

func TestResolveThemeDefaultSentinelOverridesPresetForBackground(t *testing.T) {
	t.Parallel()

	got := ResolveTheme(ThemeConfig{Preset: "forest", Background: "default", Surface: "default"})

	if !IsThemeDefaultSpec(got.Background.Value) || got.Background.Source != SourceGlobal {
		t.Fatalf("background = %#v, want global terminal-default sentinel", got.Background)
	}
	if !IsThemeDefaultSpec(got.Surface.Value) || got.Surface.Source != SourceGlobal {
		t.Fatalf("surface = %#v, want global terminal-default sentinel", got.Surface)
	}
	// SurfaceActive was not set to default, so it keeps the preset fill.
	if got.SurfaceActive.Value.Hex == "" || got.SurfaceActive.Source != SourceGlobal {
		t.Fatalf("surface_active = %#v, want preset-filled value, not default", got.SurfaceActive)
	}
}

func TestResolveThemeDefaultSentinelEmitsTmuxDefaultRole(t *testing.T) {
	t.Parallel()

	roles := RenderRolesFromEffective(ResolveTheme(ThemeConfig{Preset: "forest", Background: "default", Surface: "default", StatusBackground: "default"}))
	if roles.PaneInactiveBg != "default" {
		t.Fatalf("PaneInactiveBg = %q, want default (sentinel beats preset)", roles.PaneInactiveBg)
	}
	if roles.StatusBg != "default" {
		t.Fatalf("StatusBg = %q, want default (status_background sentinel)", roles.StatusBg)
	}
}

func TestResolveThemeDefaultSentinelOnlyValidForBackgroundSurface(t *testing.T) {
	t.Parallel()

	// "default" on a non-surface token (foreground) is treated as invalid hex and
	// drops that global layer, so resolution falls back to the built-in preset.
	got := ResolveTheme(ThemeConfig{Foreground: "default"})
	if got.Foreground.Source != SourceFallback {
		t.Fatalf("foreground = %#v, want fallback (default invalid for non-surface tokens)", got.Foreground)
	}
	if len(got.Warnings) == 0 {
		t.Fatalf("warnings = %#v, want a warning for default on a non-surface token", got.Warnings)
	}
}

func TestResolveThemeDefaultSentinelANSIResetsSurface(t *testing.T) {
	t.Parallel()

	ansi := ANSIRolesFromEffective(ResolveTheme(ThemeConfig{Preset: "forest", Surface: "default", Foreground: "#ffffff"}))
	// SurfaceRaised must not contain a background sequence (48;2) when surface is
	// the terminal-default sentinel; the foreground escape is still present.
	if containsBGSequence(ansi.SurfaceRaised) {
		t.Fatalf("SurfaceRaised = %q, want no background sequence for terminal-default surface", ansi.SurfaceRaised)
	}
}

func containsBGSequence(s string) bool {
	for _, marker := range []string{"48;2;", "48;5;"} {
		if indexOf(s, marker) >= 0 {
			return true
		}
	}
	return false
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestResolveThemeUnsetByteIdenticalWithoutSentinel(t *testing.T) {
	t.Parallel()

	// Sentinel handling must not perturb the unset/default resolution.
	got := ResolveTheme(ThemeConfig{})
	want := RenderRolesFromEffective(got)
	if want.PaneInactiveBg != "default" {
		t.Fatalf("unset PaneInactiveBg = %q, want historical default literal", want.PaneInactiveBg)
	}
	if got.Background.Source != SourceFallback {
		t.Fatalf("unset background source = %q, want fallback", got.Background.Source)
	}
}

func TestTmuxRenderTokensFallbackUsesTerminalDefaultPaneBackground(t *testing.T) {
	t.Parallel()

	got := TmuxRenderTokensFromEffective(ResolveTheme(ThemeConfig{}))
	want := TmuxRenderTokens{
		WindowInactiveBg: ThemeDefaultSentinel,
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

func TestRenderRolesFallbackUsesTerminalDefaultPaneAndPopupBackgrounds(t *testing.T) {
	t.Parallel()

	got := RenderRolesFromEffective(ResolveTheme(ThemeConfig{}))
	want := RenderRoles{
		WindowInactiveBg:  ThemeDefaultSentinel,
		WindowInactiveFg:  TmuxWindowInactiveFg,
		WindowActiveBg:    TmuxWindowActiveBg,
		WindowActiveFg:    TmuxWindowActiveFg,
		StatusBg:          TmuxWindowInactiveBg,
		StatusFg:          TmuxWindowInactiveFg,
		PaneBorder:        TmuxPaneBorderFg,
		FocusBorder:       TmuxPaneActiveBorderFg,
		PaneTopicChipBg:   TmuxPaneActiveBg,
		PaneTopicChipFg:   TmuxPaneActiveFg,
		FocusPaneActiveBg: ThemeDefaultSentinel, // default active pane rides terminal bg
		PaneInactiveBg:    "default",            // Phase 6b: terminal default literal so window-style stays bg=default when unset
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

func TestTextPrimaryDoesNotRepaintTmuxChromeRoles(t *testing.T) {
	t.Parallel()

	got := RenderRolesFromEffective(ResolveTheme(ThemeConfig{TextPrimary: "#ff0000"}))
	fallback := RenderRolesFromEffective(ResolveTheme(ThemeConfig{}))

	if got.WindowInactiveFg != fallback.WindowInactiveFg ||
		got.WindowActiveFg != fallback.WindowActiveFg ||
		got.StatusFg != fallback.StatusFg ||
		got.GitSegmentFg != fallback.GitSegmentFg {
		t.Fatalf("text_primary repainted chrome roles: got %#v fallback %#v", got, fallback)
	}
}

func TestChromeForegroundRepaintsTmuxChromeRoles(t *testing.T) {
	t.Parallel()

	got := RenderRolesFromEffective(ResolveTheme(ThemeConfig{ChromeForeground: "#ff0000"}))
	fallback := RenderRolesFromEffective(ResolveTheme(ThemeConfig{}))

	if got.WindowInactiveFg == fallback.WindowInactiveFg ||
		got.WindowActiveFg == fallback.WindowActiveFg ||
		got.StatusFg == fallback.StatusFg ||
		got.GitSegmentFg == fallback.GitSegmentFg {
		t.Fatalf("chrome_foreground did not repaint chrome roles: got %#v fallback %#v", got, fallback)
	}
}

func TestTmuxRenderTokensUseGlobalTruecolorWithoutFallbackLeak(t *testing.T) {
	t.Parallel()

	got := TmuxRenderTokensFromEffective(ResolveTheme(ThemeConfig{
		Background:       "#ff0000",
		Surface:          "#ff00ff",
		StatusBackground: "#112233",
		SurfaceActive:    "#0000ff",
		Foreground:       "#00ff00",
	}))
	fallback := TmuxRenderTokensFromEffective(ResolveTheme(ThemeConfig{}))

	if got.WindowInactiveBg == "" || got.WindowInactiveFg == "" || got.WindowActiveBg == "" || got.WindowActiveFg == "" {
		t.Fatalf("global tmux render tokens = %#v, want populated truecolor tokens", got)
	}
	if got.WindowInactiveBg == fallback.WindowInactiveBg || got.WindowInactiveFg == fallback.WindowInactiveFg || got.WindowActiveBg == fallback.WindowActiveBg || got.WindowActiveFg == fallback.WindowActiveFg {
		t.Fatalf("global tmux render tokens = %#v, must not reuse fallback tokens %#v", got, fallback)
	}
	if got.WindowInactiveBg != "#ff0000" || got.StatusBg != "#112233" || got.WindowActiveBg != "#0000ff" || got.WindowInactiveFg != "#00ff00" {
		t.Fatalf("global tmux render tokens = %#v, want exact hex truecolor tokens", got)
	}
	// StatusBg follows `status_background`, not `surface` or `background`.
	if got.StatusBg == "" {
		t.Fatalf("status tokens = %#v, want populated StatusBg", got)
	}
	if got.StatusBg == fallback.StatusBg {
		t.Fatalf("status bg = %q, want derived from explicit status_background, not fallback literal %q", got.StatusBg, fallback.StatusBg)
	}
	if got.StatusBg == got.WindowInactiveBg || got.StatusBg == "#ff00ff" {
		t.Fatalf("status bg = %q must not equal background-driven pane bg %q or surface #ff00ff", got.StatusBg, got.WindowInactiveBg)
	}
	if got.StatusFg != got.WindowInactiveFg {
		t.Fatalf("status fg = %q, want inactive window fg %q", got.StatusFg, got.WindowInactiveFg)
	}
}

// Phase 6b: an explicit `background` repaints the general pane body
// (PaneInactiveBg → tmux window-style) but must NOT drag the bottom status bg
// with it.
func TestRenderRolesExplicitBackgroundRepaintsPaneBodyNotPopup(t *testing.T) {
	t.Parallel()

	got := RenderRolesFromEffective(ResolveTheme(ThemeConfig{Background: "#1e1e2e"}))
	fallback := RenderRolesFromEffective(ResolveTheme(ThemeConfig{}))

	// Pane body repaints: PaneInactiveBg follows the explicit background and is
	// no longer the terminal default literal.
	if got.PaneInactiveBg == "default" {
		t.Fatalf("pane.inactive_bg = %q, want repainted from explicit background, not \"default\"", got.PaneInactiveBg)
	}
	if got.PaneInactiveBg != "#1e1e2e" {
		t.Fatalf("pane.inactive_bg = %q, want exact hex truecolor #1e1e2e", got.PaneInactiveBg)
	}
	if got.PaneInactiveBg != got.WindowInactiveBg {
		t.Fatalf("pane.inactive_bg = %q, want background-derived (= WindowInactiveBg %q)", got.PaneInactiveBg, got.WindowInactiveBg)
	}
	// Status does NOT follow background: StatusBg stays at the fallback.
	if got.StatusBg != fallback.StatusBg {
		t.Fatalf("status bg = %q, want fallback %q (status must not follow background)", got.StatusBg, fallback.StatusBg)
	}
}

// An explicit `surface` repaints popup/native surfaces but not the bottom status
// or the general pane body.
func TestRenderRolesExplicitSurfaceDoesNotRepaintStatusOrPaneBody(t *testing.T) {
	t.Parallel()

	got := RenderRolesFromEffective(ResolveTheme(ThemeConfig{Surface: "#ff00ff"}))
	fallback := RenderRolesFromEffective(ResolveTheme(ThemeConfig{}))

	if got.StatusBg != fallback.StatusBg {
		t.Fatalf("status bg = %q, want fallback %q when only surface changes", got.StatusBg, fallback.StatusBg)
	}
	// Pane body untouched: PaneInactiveBg stays "default" (unset background).
	if got.PaneInactiveBg != "default" {
		t.Fatalf("pane.inactive_bg = %q, want \"default\" when background unset (pane body must not follow surface)", got.PaneInactiveBg)
	}
}

func TestRenderRolesExplicitStatusBackgroundRepaintsOnlyStatus(t *testing.T) {
	t.Parallel()

	got := RenderRolesFromEffective(ResolveTheme(ThemeConfig{StatusBackground: "#334455"}))
	fallback := RenderRolesFromEffective(ResolveTheme(ThemeConfig{}))

	if got.StatusBg != "#334455" {
		t.Fatalf("status bg = %q, want explicit status_background", got.StatusBg)
	}
	if got.PaneInactiveBg != fallback.PaneInactiveBg {
		t.Fatalf("pane.inactive_bg = %q, want unchanged fallback %q", got.PaneInactiveBg, fallback.PaneInactiveBg)
	}
	if got.WindowInactiveBg != fallback.WindowInactiveBg {
		t.Fatalf("window inactive bg = %q, want unchanged fallback %q", got.WindowInactiveBg, fallback.WindowInactiveBg)
	}
}

func TestResolveThemeUnknownPresetIgnoresGlobalAndWarns(t *testing.T) {
	t.Parallel()

	got := ResolveTheme(ThemeConfig{Preset: "does-not-exist", Background: "#111111"})

	if !IsThemeDefaultSpec(got.Background.Value) || got.Background.Source != SourceFallback {
		t.Fatalf("background = %#v, want fallback terminal-default after invalid global preset", got.Background)
	}
	requireThemeWarning(t, got, SourceGlobal, "preset")
}

func TestResolveThemeInvalidColorIgnoresGlobalAndWarns(t *testing.T) {
	t.Parallel()

	got := ResolveTheme(ThemeConfig{Background: "blue"})

	if !IsThemeDefaultSpec(got.Background.Value) || got.Background.Source != SourceFallback {
		t.Fatalf("background = %#v, want fallback terminal-default after invalid global color", got.Background)
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
		t.Fatalf("accent 256-color approximation empty, want nearest colourN mapping")
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
	for _, token := range []ColorToken{TokenChromeForeground, TokenTextPrimary, TokenProgress, TokenSuccess, TokenActionRequired, TokenPaneActiveBg, TokenFocus} {
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
