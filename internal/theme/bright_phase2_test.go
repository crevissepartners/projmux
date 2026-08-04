package theme

import (
	"strconv"
	"strings"
	"testing"
)

// bright_phase2_test.go covers the light-background contrast corrections
// (bright preset roadmap Phase 2, clusters B1/B2) and pins dark/fallback
// byte-identity for every affected role.

// lightThemeConfig is the test-only light palette used to exercise the
// derivations. A real light preset is Phase 1 scope and intentionally NOT
// added to builtinPresets here.
func lightThemeConfig() ThemeConfig {
	return ThemeConfig{
		Background:       "#f5f5f0",
		Surface:          "#f2f2ee",
		StatusBackground: "#e8e8e2",
		SurfaceActive:    "#d8d8d0",
		ChromeForeground: "#2a2e32",
		TextPrimary:      "#1f2428",
		Foreground:       "#1f2428",
		Muted:            "#6a7076",
		Accent:           "#0f7b6c",
		Critical:         "#c62828",
		Warning:          "#9a6700",
		Progress:         "#0757ba",
		Success:          "#1a7f37",
		ActionRequired:   "#b35900",
		PaneActiveBg:     "#e2e2da",
		Focus:            "#0757ba",
	}
}

// parseANSITruecolorFG extracts (attrs, r, g, b) from an escape of the form
// \x1b[<attrs>38;2;R;G;Bm.
func parseANSITruecolorFG(t *testing.T, escape string) (string, int, int, int) {
	t.Helper()
	if !strings.HasPrefix(escape, "\x1b[") || !strings.HasSuffix(escape, "m") {
		t.Fatalf("escape %q is not an SGR sequence", escape)
	}
	body := strings.TrimSuffix(strings.TrimPrefix(escape, "\x1b["), "m")
	before, after, ok := strings.Cut(body, "38;2;")
	if !ok {
		t.Fatalf("escape %q is not a truecolor fg sequence", escape)
	}
	attrs := before
	parts := strings.Split(after, ";")
	if len(parts) != 3 {
		t.Fatalf("escape %q carries %d channel tokens, want 3", escape, len(parts))
	}
	vals := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			t.Fatalf("escape %q channel %q: %v", escape, p, err)
		}
		vals[i] = n
	}
	return attrs, vals[0], vals[1], vals[2]
}

func parseTestHexRGB(t *testing.T, hex string) (int, int, int) {
	t.Helper()
	r, g, b, ok := parseHexRGB(hex)
	if !ok {
		t.Fatalf("value %q is not a #rrggbb hex color", hex)
	}
	return r, g, b
}

// TestBrightPhase2ANSIRolesDarkByteIdentity pins the B2 cluster: for the
// fallback theme and EVERY current built-in (dark) preset, the fg-only Tier C
// literals must be emitted byte-identically.
func TestBrightPhase2ANSIRolesDarkByteIdentity(t *testing.T) {
	t.Parallel()

	configs := map[string]ThemeConfig{"fallback": {}}
	for _, name := range PresetNames() {
		configs["preset:"+name] = ThemeConfig{Preset: name}
	}
	for label, cfg := range configs {
		roles := ANSIRolesFromEffective(ResolveTheme(cfg))
		checks := []struct {
			role string
			got  string
			want string
		}{
			{"trust.trusted", roles.TrustTrusted, ANSITrustTrustedStart},
			{"trust.stale", roles.TrustStale, ANSITrustStaleStart},
			{"trust.untrusted", roles.TrustUntrusted, ANSITrustUntrustedStart},
			{"notify.title", roles.NotifyTitle, ANSINotifyTitleStart},
			{"notify.dim", roles.NotifyDim, ANSINotifyDimStart},
			{"notify.age", roles.NotifyAge, ANSINotifyAgeStart},
			{"switch.attention_needs", roles.SwitchAttentionNeeds, ANSISwitchAttentionNeedsStart},
			{"switch.attention_ready", roles.SwitchAttentionReady, ANSISwitchAttentionReadyStart},
		}
		for _, c := range checks {
			if c.got != c.want {
				t.Fatalf("%s %s = %q, want historical literal %q", label, c.role, c.got, c.want)
			}
		}
	}
}

// TestBrightPhase2RenderRolesDarkByteIdentity pins the B1 cluster on the tmux
// side: fallback and every dark preset keep the historical literals for the
// luma-gated statusbar roles.
func TestBrightPhase2RenderRolesDarkByteIdentity(t *testing.T) {
	t.Parallel()

	configs := map[string]ThemeConfig{"fallback": {}}
	for _, name := range PresetNames() {
		configs["preset:"+name] = ThemeConfig{Preset: name}
	}
	for label, cfg := range configs {
		roles := RenderRolesFromEffective(ResolveTheme(cfg))
		checks := []struct {
			role string
			got  string
			want string
		}{
			{"decoration.cwd", roles.DecorationCwd, TmuxDecorationCwdFg},
			{"decoration.gitlab", roles.DecorationGitLab, TmuxDecorationGitLabFg},
			{"git.staged", roles.GitStaged, TmuxStateStagedFg},
			{"git.dirty", roles.GitDirty, TmuxStateDirtyFg},
			{"git.ahead", roles.GitAhead, TmuxStateAheadFg},
			{"git.behind", roles.GitBehind, TmuxStateBehindFg},
			{"status.text_primary", roles.StatusTextPrimary, TmuxPrimaryFg},
			{"status.text_secondary", roles.StatusTextSecondary, TmuxSecondaryFg},
			{"status.text_muted", roles.StatusTextMuted, TmuxMutedFg},
			{"accent.ai_fg", roles.AccentAIFg, TmuxAccentAIFg},
			{"usage.bar_empty", roles.UsageBarEmpty, TmuxUsageEmptyFg},
			{"pane.border_muted_fg", roles.PaneBorderMutedFg, TmuxMutedFg},
		}
		for _, c := range checks {
			if c.got != c.want {
				t.Fatalf("%s %s = %q, want historical literal %q", label, c.role, c.got, c.want)
			}
		}
	}
}

// TestBrightPhase2ANSIRolesLightSurfaceDerivesDarkFg proves the B2 correction:
// on an explicit light surface every fg-only literal is replaced by a darkened
// truecolor variant (luma at most the darken target), with leading attributes
// preserved.
func TestBrightPhase2ANSIRolesLightSurfaceDerivesDarkFg(t *testing.T) {
	t.Parallel()

	roles := ANSIRolesFromEffective(ResolveTheme(lightThemeConfig()))
	checks := []struct {
		role      string
		got       string
		literal   string
		wantAttrs string
	}{
		{"trust.trusted", roles.TrustTrusted, ANSITrustTrustedStart, ""},
		{"trust.stale", roles.TrustStale, ANSITrustStaleStart, ""},
		{"trust.untrusted", roles.TrustUntrusted, ANSITrustUntrustedStart, ""},
		{"notify.title", roles.NotifyTitle, ANSINotifyTitleStart, "1;"},
		{"notify.dim", roles.NotifyDim, ANSINotifyDimStart, ""},
		{"notify.age", roles.NotifyAge, ANSINotifyAgeStart, ""},
		{"switch.attention_needs", roles.SwitchAttentionNeeds, ANSISwitchAttentionNeedsStart, ""},
		{"switch.attention_ready", roles.SwitchAttentionReady, ANSISwitchAttentionReadyStart, ""},
	}
	for _, c := range checks {
		if c.got == c.literal {
			t.Fatalf("%s = dark literal %q on light surface, want darkened derivation", c.role, c.got)
		}
		attrs, r, g, b := parseANSITruecolorFG(t, c.got)
		if attrs != c.wantAttrs {
			t.Fatalf("%s attrs = %q, want %q (escape %q)", c.role, attrs, c.wantAttrs, c.got)
		}
		if luma := rec601Luma(r, g, b); luma > contrastDarkenTargetLuma {
			t.Fatalf("%s = %q with luma %.1f, want <= %.1f on a light surface", c.role, c.got, luma, contrastDarkenTargetLuma)
		}
	}
}

// TestBrightPhase2RenderRolesLightStatusBackgroundDerivesDarkFg proves the B1
// correction on the tmux side: an explicit light status_background darkens the
// statusbar text/decoration literals to hex values readable on light chrome.
func TestBrightPhase2RenderRolesLightStatusBackgroundDerivesDarkFg(t *testing.T) {
	t.Parallel()

	roles := RenderRolesFromEffective(ResolveTheme(lightThemeConfig()))
	darkened := []struct {
		role string
		got  string
	}{
		{"decoration.cwd", roles.DecorationCwd},
		{"status.text_secondary", roles.StatusTextSecondary},
		{"status.text_muted", roles.StatusTextMuted},
		{"accent.ai_fg", roles.AccentAIFg},
		{"usage.bar_empty", roles.UsageBarEmpty},
		{"pane.border_muted_fg", roles.PaneBorderMutedFg},
	}
	for _, c := range darkened {
		r, g, b := parseTestHexRGB(t, c.got)
		if luma := rec601Luma(r, g, b); luma > contrastDarkenTargetLuma {
			t.Fatalf("%s = %q with luma %.1f, want <= %.1f on a light status background", c.role, c.got, luma, contrastDarkenTargetLuma)
		}
	}
	// Roles rendered inside the fixed dark git segment (bg colour30) must NOT
	// be darkened even on a light status bar.
	verbatim := []struct {
		role string
		got  string
		want string
	}{
		{"decoration.gitlab", roles.DecorationGitLab, TmuxDecorationGitLabFg},
		{"git.staged", roles.GitStaged, TmuxStateStagedFg},
		{"git.dirty", roles.GitDirty, TmuxStateDirtyFg},
		{"git.ahead", roles.GitAhead, TmuxStateAheadFg},
		{"git.behind", roles.GitBehind, TmuxStateBehindFg},
		{"status.text_primary", roles.StatusTextPrimary, TmuxPrimaryFg},
	}
	for _, c := range verbatim {
		if c.got != c.want {
			t.Fatalf("%s = %q, want verbatim literal %q (renders on a fixed dark badge/segment bg)", c.role, c.got, c.want)
		}
	}
}

// TestBrightPhase2SentinelSurfaceKeepsLiterals: the terminal-default sentinel
// surface never counts as light — the correction must not fire when the
// terminal background is unknowable.
func TestBrightPhase2SentinelSurfaceKeepsLiterals(t *testing.T) {
	t.Parallel()

	cfg := lightThemeConfig()
	cfg.Surface = ThemeDefaultSentinel
	cfg.StatusBackground = ThemeDefaultSentinel
	cfg.Background = ThemeDefaultSentinel
	effective := ResolveTheme(cfg)

	ansi := ANSIRolesFromEffective(effective)
	if ansi.NotifyTitle != ANSINotifyTitleStart {
		t.Fatalf("notify.title = %q on sentinel surface, want literal %q", ansi.NotifyTitle, ANSINotifyTitleStart)
	}
	if ansi.TrustTrusted != ANSITrustTrustedStart {
		t.Fatalf("trust.trusted = %q on sentinel surface, want literal %q", ansi.TrustTrusted, ANSITrustTrustedStart)
	}
	render := RenderRolesFromEffective(effective)
	if render.DecorationCwd != TmuxDecorationCwdFg {
		t.Fatalf("decoration.cwd = %q on sentinel status background, want literal %q", render.DecorationCwd, TmuxDecorationCwdFg)
	}
	if render.StatusTextSecondary != TmuxSecondaryFg {
		t.Fatalf("status.text_secondary = %q on sentinel status background, want literal %q", render.StatusTextSecondary, TmuxSecondaryFg)
	}
}

// TestBrightPhase2LumaGateBaseColorsMatchLiterals is a self-check: the base
// RGB values hardcoded into the ANSIRolesFromEffective derivations must match
// the colors actually baked into the historical literals.
func TestBrightPhase2LumaGateBaseColorsMatchLiterals(t *testing.T) {
	t.Parallel()

	cases := []struct {
		role    string
		literal string
		r, g, b int
	}{
		{"trust.trusted", ANSITrustTrustedStart, 154, 191, 136},
		{"trust.stale", ANSITrustStaleStart, 177, 139, 212},
		{"trust.untrusted", ANSITrustUntrustedStart, 210, 139, 88},
		{"notify.title (colour220)", ANSINotifyTitleStart, 255, 215, 0},
		{"notify.dim (colour245)", ANSINotifyDimStart, 138, 138, 138},
		{"notify.age (colour153)", ANSINotifyAgeStart, 175, 215, 255},
		{"switch.attention_needs (colour220)", ANSISwitchAttentionNeedsStart, 255, 215, 0},
		{"switch.attention_ready (colour82)", ANSISwitchAttentionReadyStart, 95, 255, 0},
	}
	for _, c := range cases {
		var r, g, b int
		if strings.Contains(c.literal, "38;2;") {
			_, r, g, b = parseANSITruecolorFG(t, c.literal)
		} else {
			body := strings.TrimSuffix(strings.TrimPrefix(c.literal, "\x1b["), "m")
			_, after, ok := strings.Cut(body, "38;5;")
			if !ok {
				t.Fatalf("%s literal %q is neither truecolor nor 256-color", c.role, c.literal)
			}
			n, err := strconv.Atoi(after)
			if err != nil {
				t.Fatalf("%s literal %q: %v", c.role, c.literal, err)
			}
			r, g, b = xterm256RGB(n)
		}
		if r != c.r || g != c.g || b != c.b {
			t.Fatalf("%s base RGB in derivation = (%d,%d,%d), literal carries (%d,%d,%d)", c.role, c.r, c.g, c.b, r, g, b)
		}
	}
}

// TestContrastDarkenRGBPreservesDarkColors: colors at or below the target luma
// pass through unchanged; brighter colors are scaled onto the target.
func TestContrastDarkenRGBPreservesDarkColors(t *testing.T) {
	t.Parallel()

	if r, g, b := contrastDarkenRGB(40, 60, 50); r != 40 || g != 60 || b != 50 {
		t.Fatalf("contrastDarkenRGB(dark) = (%d,%d,%d), want unchanged", r, g, b)
	}
	r, g, b := contrastDarkenRGB(255, 215, 0)
	if luma := rec601Luma(r, g, b); luma > contrastDarkenTargetLuma {
		t.Fatalf("contrastDarkenRGB(gold) luma = %.1f, want <= %.1f", luma, contrastDarkenTargetLuma)
	}
	if b != 0 || r <= 0 || g <= 0 || float64(r)/float64(g) < 255.0/215.0-0.05 || float64(r)/float64(g) > 255.0/215.0+0.05 {
		t.Fatalf("contrastDarkenRGB(gold) = (%d,%d,%d), want hue-preserving scale of (255,215,0)", r, g, b)
	}
}

// TestANSIFgFromTmuxColor covers the RenderRoles->ANSI adapter used by the
// statusbar usage popup severity text.
func TestANSIFgFromTmuxColor(t *testing.T) {
	t.Parallel()

	if got, want := ANSIFgFromTmuxColor("colour214"), "\x1b[38;5;214m"; got != want {
		t.Fatalf("ANSIFgFromTmuxColor(colour214) = %q, want %q", got, want)
	}
	if got, want := ANSIFgFromTmuxColor(TmuxStateCriticalFg+",bold"), "\x1b[38;5;160m"; got != want {
		t.Fatalf("ANSIFgFromTmuxColor(colour160,bold) = %q, want %q", got, want)
	}
	if got, want := ANSIFgFromTmuxColor("#ffcc66"), "\x1b[38;2;255;204;102m"; got != want {
		t.Fatalf("ANSIFgFromTmuxColor(#ffcc66) = %q, want %q", got, want)
	}
	if got := ANSIFgFromTmuxColor("red"); got != "" {
		t.Fatalf("ANSIFgFromTmuxColor(red) = %q, want empty for unknown forms", got)
	}
	// Byte-identity bridge: for every fallback state role the adapter emits the
	// same escape the statusbar popup historically hardcoded.
	fallback := RenderRolesFromEffective(ResolveTheme(ThemeConfig{}))
	if got, want := ANSIFgFromTmuxColor(fallback.StateWarning), ANSI256FgStart(TmuxStateWarningFg); got != want {
		t.Fatalf("fallback state.warning adapter = %q, want %q", got, want)
	}
	if got, want := ANSIFgFromTmuxColor(fallback.StateCritical), ANSI256FgStart(TmuxStateCriticalFg); got != want {
		t.Fatalf("fallback state.critical adapter = %q, want %q", got, want)
	}
	if got, want := ANSIFgFromTmuxColor(fallback.StateSuccess), ANSI256FgStart(TmuxStateSuccessFg); got != want {
		t.Fatalf("fallback state.success adapter = %q, want %q", got, want)
	}
}

// TestColorFieldIsLight pins the gate: fallback, sentinel, dark hex -> false;
// light hex -> true.
func TestColorFieldIsLight(t *testing.T) {
	t.Parallel()

	if colorFieldIsLight(ColorField{}) {
		t.Fatal("zero ColorField must not be light")
	}
	if colorFieldIsLight(ColorField{Value: ColorSpec{Hex: "#ffffff"}, Source: SourceFallback}) {
		t.Fatal("fallback-sourced field must never be light")
	}
	if colorFieldIsLight(ColorField{Value: ColorSpec{Tmux: ThemeDefaultSentinel}, Source: SourceGlobal}) {
		t.Fatal("terminal-default sentinel must never be light")
	}
	if colorFieldIsLight(ColorField{Value: ColorSpec{Hex: "#182226"}, Source: SourceGlobal}) {
		t.Fatal("dark explicit surface must not be light")
	}
	if !colorFieldIsLight(ColorField{Value: ColorSpec{Hex: "#f2f2ee"}, Source: SourceGlobal}) {
		t.Fatal("light explicit surface must be light")
	}
}

// TestBrightPhase2LightThemePassesDesignSanity keeps the light test palette
// honest: all five state colors must stay mutually distinguishable after tmux
// 256 quantization, mirroring the preset design rubric that a future Phase 1
// bright preset must pass.
func TestBrightPhase2LightThemePassesDesignSanity(t *testing.T) {
	t.Parallel()

	effective := ResolveTheme(lightThemeConfig())
	seen := map[string]string{}
	for name, field := range map[string]ColorField{
		"progress":        effective.Progress,
		"warning":         effective.Warning,
		"critical":        effective.Critical,
		"success":         effective.Success,
		"action_required": effective.ActionRequired,
	} {
		key := field.Value.Tmux
		if other, dup := seen[key]; dup {
			t.Fatalf("light test palette: %s and %s collide on %s after quantization", name, other, key)
		}
		seen[key] = name
	}
}
