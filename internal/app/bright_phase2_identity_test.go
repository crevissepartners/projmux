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
		"standalone": "f8a27fb181aab9c1b723924e8ea64c316c5fcb1b2205624d960dec3b24118ef3",
		"app":        "58e4e1c02d2daa4f5783ce6716d96055746fb7ee441bd5223faef62f39086985",
	},
	"projmux": {
		"standalone": "340895fa4e2c5b31ca5d8384198c1b6420dec71ffe561f8e94dffe971ffe8c5d",
		"app":        "6b888c0aeb1616d607dcdd213bdb9ae98bc470334d2ecee2ea735d3de6f9f59d",
	},
	"blue-hour": {
		"standalone": "3193b5037f01af8dbf3c7afa7ad30c4de0f25a7b45363b460b6eb84549ba4be3",
		"app":        "2bd1ee0e32d17d63b30f8692248022da3a41d33f4ce94028e3aa40f948ed70be",
	},
	"carbon-violet": {
		"standalone": "2c53b05cae8dc24aa4bee8e39790e2b6e04733781328564fa2f5c10be49be8c3",
		"app":        "77e6dece8d6d0ac7cbf0c14ac84d62602273603296e0b27be8b1f8b260f9411f",
	},
	"ember": {
		"standalone": "0b5e8936eb9d4a4ccbf2c8ee54964a8f0b7f1f81bc5746d641ed65fb17c99034",
		"app":        "92f546f7b0cb0a62453c1ff45f018cfd6b48c7fdea885135663bb0805ef875e2",
	},
	"forest": {
		"standalone": "ee25a255ba135fa41f0c262c684c9f9cba7e345e8494e65441ad559ed8ea1b61",
		"app":        "4bb98b954b3dd3de43ce15718c8f73611815493d81714d0e8a6b6d1aceaa6202",
	},
	"rose": {
		"standalone": "6eb3c4c01cafbbd09facb7d980998395c1e931b1199006f72022af0905ff10bf",
		"app":        "7ca31fdb14c55ef76aa5f0c85f89fd4172a6c875293a1c7991a4cd4e603f44a9",
	},
	"high-contrast": {
		"standalone": "d57bdf5c679bf2b01fbb61e83f3406f4a99828003c0deca411ac640f5d6b0216",
		"app":        "5fcab3a95eaad8b92c42ba630d42541aacb98b640be459d3ceabc39426c0ba75",
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
