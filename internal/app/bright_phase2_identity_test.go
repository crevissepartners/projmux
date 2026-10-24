package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/app/hookcmd"
	"github.com/crevissepartners/projmux/internal/app/keybinding"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
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
		"standalone": "478f83629b75dcca23b8cd1a0018d2ed58de9af66b7737bdabe038b2ebaf24e0",
		"app":        "2b83e7b23394e766e94752b22b86d6f4508f1ef0513a9b5b8725dd0fae0a5d5b",
	},
	"projmux": {
		"standalone": "a68beaf41bba695eeb76c329a3a04837c3c1828ec9e7f12f0441f87b4a3135c5",
		"app":        "f31dd33c3b0c33e8dd14d3ce3888a440f996c8427c1869b20f3b8a159acf50fe",
	},
	"blue-hour": {
		"standalone": "b6f12eb2a223e903fed9de7dd8f901448deaa2ba3f65371e8c38be01bcf2d20e",
		"app":        "8f7ef83144ed3aee230c28eb9bccbfaabc24961822f0f0ad3489b1e21976cd1a",
	},
	"carbon-violet": {
		"standalone": "b22989a74b304917b0c4cc5dfbdb8b2c968ca00786ab708372c688ded8c65a41",
		"app":        "8c12b99080b59f26d89ea6721be6c681010efa8172426166ca5b858ebde14fcd",
	},
	"ember": {
		"standalone": "a7584215ca4b86853df0b901cbadf334f758b61230f943e9b9b6c3ea5873a529",
		"app":        "baf2665a5e4e0498ba32058c141498dddc510fbc1f870f4d2a46e439a12b8df7",
	},
	"forest": {
		"standalone": "30ef8f4d79aaee2b334bfe5ff6680e6d6a8157e0b7b17275271d3f6cb66998bb",
		"app":        "101942e837b3fd588b99736cf724fd5749261f10e9c4f7f8aafec5d0ee0d2e30",
	},
	"rose": {
		"standalone": "254681e04357d2343e2ac511b6bedf08b44ecd394ac987cf0be989db2c59af1b",
		"app":        "88ff558c3405861d56d4b22324d37831f83b411e123e507465d8d68c4391f3ac",
	},
	"high-contrast": {
		"standalone": "2d7020516d4d93f098bff8a6c019aaeb681e95380e169174d53be10b9166b04a",
		"app":        "e245e7efc6c8f7aa10f47b6ad9dc100dacac9d0fc739644ae6eec2471bff1bfc",
	},
}

// TestBrightPhase2GeneratedConfigByteIdentity regenerates the standalone and
// app tmux configs for the fallback theme and every built-in preset and
// compares them against the golden digests.
//
// The digests were rebaselined once, by the interactive `run-shell`
// output-channel convergence: the status-key bindings now carry
// `#{client_tty}` so a status action's result reaches the exact client, and the
// foreground runtime-created hook is exit-guarded so a refused convergence
// cannot paint the pane that just appeared. The app digests were rebaselined
// again when the app config began replacing tmux's stock `prefix x` and
// `prefix &` with the mirror-guarded managed close keys; every standalone
// digest stayed byte-identical. Every digest was rebaselined once more when the
// MouseDown3Pane Kill item gained the identity-mirror guard with tmux's stock
// kill-pane branch (both configs) and the app config began replacing tmux's
// stock Window and Pane menus. The app digests were rebaselined again when the
// status-format[0] row stopped carrying the retired autosave job (that row and
// the embedded config digest are the only changed lines); every standalone
// digest stayed byte-identical. Every digest was rebaselined once more when the
// MouseDown1Status click command stopped passing a window format tmux does not
// define (that bind line and the app config's embedded digest are the only
// changed lines). Every digest was rebaselined once more when both configs
// began turning on the server option focus-events ahead of the pane focus hooks
// (that line and the app config's embedded digest are the only changed lines).
// Theme rendering itself is unchanged, which is what this test still pins.
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
			"standalone": source.tmuxStandaloneConfig("/usr/bin/projmux", decorations, keybinding.DefaultKeyBindingCatalog(), false),
			"app":        source.tmuxAppConfig("/usr/bin/projmux", "/bin/zsh", decorations, keybinding.DefaultKeyBindingCatalog(), false),
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
		if strings.Contains(hookTrustPromptText(), theme.ANSITextMutedStart+"choice"+theme.ANSIReset) {
			t.Fatalf("hook trust prompt still renders the fallback muted literal under a light theme")
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
	if want := theme.ANSITextMutedStart + "choice" + theme.ANSIReset; !strings.Contains(hookTrustPromptText(), want) {
		t.Fatalf("hook trust prompt muted text not restored: %q, want %q", hookTrustPromptText(), want)
	}
	if got := tmuxAIBadgeKindFg(aiBadgeKindApprovalRequired, statusSegmentRoles); got != theme.TmuxAIBadgeActionRequiredFg {
		t.Fatalf("attention badge fg not restored: %q, want %q", got, theme.TmuxAIBadgeActionRequiredFg)
	}
}

// hookTrustPromptText is what the hook-trust popup prints for an empty
// request answered with a deny.
func hookTrustPromptText() string {
	var out bytes.Buffer
	hookcmd.TrustPrompt(strings.NewReader("d\n"), &out, hooks.ProjectHookPromptRequest{})
	return out.String()
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
