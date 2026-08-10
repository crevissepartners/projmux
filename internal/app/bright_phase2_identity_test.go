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
		"standalone": "b35278748392810db4348082771550bb323624463d3eb36397d883590d7483cb",
		"app":        "e2e249c10e3b7faaeddb3d539637747624aba80a1db142b21054aade9ecac40a",
	},
	"projmux": {
		"standalone": "f7077359b11cea5ac8c67c613b7ad8424b482d84a7229ba9236c0718baf9b031",
		"app":        "f55c2d71093904f4355038cd3b28c0fdf8b4f2f5900d5b591c9b5d4d049d295e",
	},
	"blue-hour": {
		"standalone": "bfd5f093394723c89162b0c7ef61a67759bff7aa43a125c82a3608e456c5b4c2",
		"app":        "c726b9524496c2de150332b47a220a205ae2840f2f68e91343cd5b0fea7662d4",
	},
	"carbon-violet": {
		"standalone": "d00ccae1494b19d25463604f1c5124b5c23d5d4208f1fae2755abe232887025c",
		"app":        "0ea164fd47b9fbc704457b4e6e441f82327a9c569226f1efc422b8b9f6adc2bf",
	},
	"ember": {
		"standalone": "b3b17db9979524ef856ac2c15a99f5baadd713c3cb47047855f83bf170b9c1f4",
		"app":        "16d8be84426fc2ae1d3dbf28cebe8c9ee508b567bfb32a8cef557d171a2d78e5",
	},
	"forest": {
		"standalone": "5aad0ac003be0facdf24a379093bb0a07a2e99e2c65e89dac9cd9a4c49048ba0",
		"app":        "849613a63a136410608c7d76cb5cbd13c2eb6c5b2ed99f4bd4edfb3e3062b92b",
	},
	"rose": {
		"standalone": "44edfde3c4c7d1cedfafab529e05462f7449be9dfaed41f8969f9346aaf8ff5f",
		"app":        "7e8a79ff13adfe2bcc5b17682069c23d4e9273ab0301e3d16c3365decd6bbab6",
	},
	"high-contrast": {
		"standalone": "a22e287e4cec4fd6b4995f0363d9f9f98fdebff1fc41c5d4489eb49e2f8baa01",
		"app":        "fcda8250e4ecaa53a2e0a2fd1e158119dae89ab1d09c4afd05e3b7615fd91f06",
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
				t.Fatalf("theme %s %s config drifted from the golden: sha256 %s, want %s", label, kind, got, goldens[kind])
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
