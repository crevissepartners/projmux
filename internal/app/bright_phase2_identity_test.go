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
		"standalone": "ff7009d4107f7686bdc9067d6fbdabc6b8bee7ea956c1779a5d677fd0cec2ad9",
		"app":        "11e644f8b1cd32ed089e7917a5dc8fcd73b28fc8c0ca13fee35865be1f7e8d65",
	},
	"projmux": {
		"standalone": "504568fc777de6cc4eee3703a20064242e01a0772088cb86827169cf8866da1b",
		"app":        "b187a6fb280e3be6b8a25c3d375180c9a1ac3c02e103d85a4cfc71019c6638ff",
	},
	"blue-hour": {
		"standalone": "efbe2fa39637a76da9b9f3c42e0b97530f65f1d9f062677f807b5cdb48ffa063",
		"app":        "9a0b803c2a6027c543745e97ae761b071fa7d13c2979911d9f3d4e0ea5928d81",
	},
	"carbon-violet": {
		"standalone": "90f62f8bbfe5e9ea92b013ba8aa465026a276f37fa9dd4b8abb5b26bac5d576b",
		"app":        "c2c4d29d973749846c3f29a58a52c0d0a92652d157eaa32aab6db64625283fb6",
	},
	"ember": {
		"standalone": "da136b8b8e5fd6b77b4ded92414c2a6463845dbaad31b9515a85cd48ba094852",
		"app":        "c1ff714ae903561c5e938404d4b23894f79175c4dddfc317ef6133123dd17813",
	},
	"forest": {
		"standalone": "15483a62e893ee98c7453a1d1fe217b48ff11537b35d18edd8bb01d371fd0b19",
		"app":        "bbd3cd25aa38b4e94dd7cdf4556ca6218550ebf7eaa69b1c149802ba28c096a5",
	},
	"rose": {
		"standalone": "7b2841d83314512ae985b1c779f5bac669a93a2eedce1612940a3bb25bfb2236",
		"app":        "a570650fa157093a46754261842acf1f96304bd84b92ceefd306513f0cee114b",
	},
	"high-contrast": {
		"standalone": "3cf7c72c1fd1969b72edb5190b083c9a3040dd1611e912a893fc34334902b5a9",
		"app":        "d85350032de518dbf1b7d390f670b3171c8c7d30f1bbe3401c44f3392d062218",
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
