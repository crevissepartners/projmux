package app

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/theme"
)

// bright_phase2_identity_test.go pins the generated tmux config byte-identity
// for the fallback theme and every built-in (dark) preset, and covers the B1
// theme-threading corrections (bright preset roadmap Phase 2).

// appLightThemeConfig mirrors the light test palette used by the theme package
// tests. A real light preset is Phase 1 scope and intentionally NOT added to
// builtinPresets here.
func appLightThemeConfig() theme.ThemeConfig {
	return theme.ThemeConfig{
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

// brightPhase2ConfigGoldens are SHA-256 digests of the generated tmux configs
// for a fixed binary path, shell, symbol decorations, and default-off optional
// features. Theme rendering must not drift between presets; intentional shared
// config contract additions update every digest together.
var brightPhase2ConfigGoldens = map[string]map[string]string{
	"fallback": {
		"standalone": "f989fb42d09c96006e5551f3f1114ca8dc39517a142c5af34ad66730100d5f3f",
		"app":        "22741a111de247cd4757c4712c29f03b23e06502045434d5d99fc17c2bbc6366",
	},
	"projmux": {
		"standalone": "174cf8ab7c37dc00d8dbfc18b2873d384d0b18307623254d984b1cdaf760f305",
		"app":        "885a3c95b8e02591dfc089c722b8cd1d647d7e462a0749eaab58ac7f8632b6c5",
	},
	"blue-hour": {
		"standalone": "f3cf632abdd9c6c1de71ef0d09d64e61db46dbf4fec6165dea1079989f17fd65",
		"app":        "06766bcef770e2862f62f019a3ccbcb907da73a1cc6ef1c0a4f471310199d993",
	},
	"carbon-violet": {
		"standalone": "c85a52ff8c6b4853451823b343ca56f7e6396ad14dc00b9775a8546ad7697441",
		"app":        "7b24d737d92766ae504a9e99b0561828875308d9cf136149f7c9f790a081a527",
	},
	"ember": {
		"standalone": "2e159ff8f9be7d923ccd5c69dd59de7ad9db64ef41b55f7a5e9ab76b4cdbe8b4",
		"app":        "72ae4943b6af6509a03a0057bd618e3acbf1941a3ebac828d039e1a2ceb53f56",
	},
	"forest": {
		"standalone": "33f428a9f227a4027383e8f21b8a003a115cb023dffc8b2b294b53ae7c2319e9",
		"app":        "6976485c34ffe4e7341e5fbfd0abee094aa62e6711a2565a9be7ce1db6fe9085",
	},
	"rose": {
		"standalone": "0609acc4e67f85f1dae815fb1ad383c7fda31d7ceb19f1e456ebc03f95d57a6e",
		"app":        "cdfcddff4d797146f83532eaada48bd68ed6c9e72c9102def88510a893da92ed",
	},
	"high-contrast": {
		"standalone": "948be366ca9b845c68f4984681c10455c14c6cebdd77da4da93a25451daae02f",
		"app":        "cfd4126b46beeb2611cc3b017710578340fdf9d1262cc606ac295ecfb6412f20",
	},
}

// TestBrightPhase2GeneratedConfigByteIdentity regenerates the standalone and
// app tmux configs for the fallback theme and every built-in preset and
// compares them against the pre-Phase 2 golden digests.
func TestBrightPhase2GeneratedConfigByteIdentity(t *testing.T) {
	t.Parallel()

	decorations := statusbarDecorationSet{
		Cwd:    config.StatusbarDecorationSymbol,
		Git:    config.StatusbarDecorationSymbol,
		Notify: config.StatusbarDecorationSymbol,
	}
	for label, goldens := range brightPhase2ConfigGoldens {
		cfg := theme.ThemeConfig{}
		if label != "fallback" {
			cfg.Preset = label
		}
		source := newRenderThemeSource(theme.ResolveTheme(cfg))
		outputs := map[string]string{
			"standalone": source.tmuxStandaloneConfig("/usr/bin/projmux", decorations, defaultKeyBindingCatalog(), false),
			"app":        source.tmuxAppConfig("/usr/bin/projmux", "/bin/zsh", decorations, defaultKeyBindingCatalog(), false),
		}
		for kind, body := range outputs {
			sum := sha256.Sum256([]byte(body))
			if got := hex.EncodeToString(sum[:]); got != goldens[kind] {
				t.Errorf("theme %s %s config drifted from the golden: sha256 %s, want %s", label, kind, got, goldens[kind])
			}
		}
	}
}

// TestBrightPhase2StatusSegmentThemeInjection proves the B1 threading: applying
// a light effective theme repaints the statusbar segment roles (dark-contrast
// derivations), and restoring the fallback returns the historical literals.
//
// NOTE: intentionally NOT t.Parallel() — it mutates the shared status segment
// role vars under nativeUIThemeMu (see theme_render_native_test.go).
func TestBrightPhase2StatusSegmentThemeInjection(t *testing.T) {
	light := theme.ResolveTheme(appLightThemeConfig())

	withNativeUITheme(light, func() {
		// decoration.cwd is luma-gated: light status background derives hex.
		if statusSegmentRoles.DecorationCwd == theme.TmuxDecorationCwdFg {
			t.Fatalf("decoration.cwd still %q under a light theme, want darkened hex", statusSegmentRoles.DecorationCwd)
		}
		if !strings.HasPrefix(statusSegmentRoles.DecorationCwd, "#") {
			t.Fatalf("decoration.cwd = %q, want #rrggbb derivation", statusSegmentRoles.DecorationCwd)
		}
		format := statusbarCwdDecoratorFormat(statusSegmentRoles)
		if strings.Contains(format, theme.TmuxDecorationCwdFg) {
			t.Fatalf("cwd decorator format still carries %s under a light theme: %q", theme.TmuxDecorationCwdFg, format)
		}
		// The notify HUD severity tokens follow the explicit state tokens.
		if !strings.Contains(notifySeverityWarn, "#9a6700") {
			t.Fatalf("notify severity warn = %q, want explicit light warning #9a6700", notifySeverityWarn)
		}
		if strings.Contains(notifyLineOpen, theme.TmuxStateProgressFg) {
			t.Fatalf("notify line open still carries fallback progress literal: %q", notifyLineOpen)
		}
		// The attention window badge fg follows the explicit AI tokens.
		if got := tmuxAIBadgeKindFg(aiBadgeKindApprovalRequired, statusSegmentRoles); got != "#b35900" {
			t.Fatalf("attention badge fg = %q, want explicit action_required #b35900", got)
		}
		// statusbar popup severity text adapts the explicit state roles.
		if !strings.Contains(amberANSI("85%"), "\x1b[38;2;154;103;0m") {
			t.Fatalf("amberANSI = %q, want truecolor of #9a6700", amberANSI("85%"))
		}
		// hook-trust popup muted text follows the muted token.
		if hookTrustMuted("x") == theme.ANSITextMutedStart+"x"+theme.ANSIReset {
			t.Fatalf("hookTrustMuted still renders the fallback muted literal under a light theme")
		}
	})

	// Everything restored to the historical literals after the scope.
	if statusSegmentRoles.DecorationCwd != theme.TmuxDecorationCwdFg {
		t.Fatalf("decoration.cwd not restored: %q", statusSegmentRoles.DecorationCwd)
	}
	if want := "#[bg=" + tmuxAccentAttentionBg + ",fg=" + theme.TmuxStateWarningFg + "]"; notifySeverityWarn != want {
		t.Fatalf("notify severity warn not restored: %q, want %q", notifySeverityWarn, want)
	}
	if want := theme.ANSI256FgStart(theme.TmuxStateWarningFg) + "85%" + theme.ANSIReset; amberANSI("85%") != want {
		t.Fatalf("amberANSI not restored: %q, want %q", amberANSI("85%"), want)
	}
	if want := theme.ANSITextMutedStart + "x" + theme.ANSIReset; hookTrustMuted("x") != want {
		t.Fatalf("hookTrustMuted not restored: %q, want %q", hookTrustMuted("x"), want)
	}
	if got := tmuxAIBadgeKindFg(aiBadgeKindApprovalRequired, statusSegmentRoles); got != theme.TmuxAIBadgeActionRequiredFg {
		t.Fatalf("attention badge fg not restored: %q, want %q", got, theme.TmuxAIBadgeActionRequiredFg)
	}
}

// TestBrightPhase2StatusNotifySegmentFallbackByteIdentity locks the notify
// status segment output under the fallback role state to the exact bytes
// captured at main fa3da53 (pre-Phase 2).
func TestBrightPhase2StatusNotifySegmentFallbackByteIdentity(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	entries := []notify.Notification{
		{ID: "1", Session: "repos-projmux", Text: "claude: reply ready", Source: notify.SourceAI, Severity: notify.SeverityInfo, CreatedAt: now.Add(-3 * time.Minute), Metadata: map[string]string{"state": "need"}},
		{ID: "2", Session: "repos-x", Text: "warn body", Severity: notify.SeverityWarn, CreatedAt: now.Add(-10 * time.Minute)},
		{ID: "3", Session: "repos-y", Text: "crit body", Severity: notify.SeverityCritical, CreatedAt: now.Add(-20 * time.Minute)},
	}
	want := "#[bg=colour53,fg=colour220]#[bg=colour90,fg=colour231,bold] projmux #[bg=colour53,fg=colour220] claude: reply ready #[bg=colour53,fg=colour153]· 3m ago#[bg=colour53,fg=colour220]   #[bg=colour53,fg=colour220,bold]+2#[bg=colour53,fg=colour220]#[default]"
	if got := formatStatusNotify(entries, 0, now); got != want {
		t.Fatalf("fallback notify segment drifted:\n got %q\nwant %q", got, want)
	}
}
