package render

import (
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/theme"
)

// bright_phase2_test.go covers the render-package pieces of the bright preset
// Phase 2 corrections: the usage bar empty-cell role (B1) and the switch
// attention dots (B2) under an explicit light theme, plus fallback
// byte-identity for both.

func renderLightTheme() theme.EffectiveTheme {
	return theme.ResolveTheme(theme.ThemeConfig{
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
	})
}

// TestBarEmptyColorForRolesFollowsUsageBarEmptyRole: fallback and zero-value
// roles keep the historical literal; a light status background derives a dark
// hex.
func TestBarEmptyColorForRolesFollowsUsageBarEmptyRole(t *testing.T) {
	t.Parallel()

	fallback := theme.RenderRolesFromEffective(theme.ResolveTheme(theme.ThemeConfig{}))
	if got := BarEmptyColorForRoles(fallback); got != theme.TmuxUsageEmptyFg {
		t.Fatalf("fallback bar empty color = %q, want %q", got, theme.TmuxUsageEmptyFg)
	}
	if got := BarEmptyColorForRoles(theme.RenderRoles{}); got != theme.TmuxUsageEmptyFg {
		t.Fatalf("zero-roles bar empty color = %q, want %q", got, theme.TmuxUsageEmptyFg)
	}
	light := theme.RenderRolesFromEffective(renderLightTheme())
	got := BarEmptyColorForRoles(light)
	if got == theme.TmuxUsageEmptyFg || !strings.HasPrefix(got, "#") {
		t.Fatalf("light bar empty color = %q, want hex derivation distinct from %q", got, theme.TmuxUsageEmptyFg)
	}
}

// TestSwitchAttentionDotsRepaintOnLightTheme proves the B2 wiring end to end:
// ApplyTheme with a light theme repoints the attention dot escapes at the
// darkened derivations, and reapplying the fallback restores byte-identity.
//
// NOTE: intentionally NOT t.Parallel() — ApplyTheme mutates the package-level
// role escapes consumed by the switch/sessions/popup formatters.
func TestSwitchAttentionDotsRepaintOnLightTheme(t *testing.T) {
	fallback := theme.ANSIRolesFromEffective(theme.ResolveTheme(theme.ThemeConfig{}))
	ApplyTheme(theme.ANSIRolesFromEffective(renderLightTheme()))
	defer ApplyTheme(fallback)

	needs := formatInlineAttentionBadge("", 2, "dot")
	ready := formatInlineAttentionBadge("", 1, "dot")
	if strings.Contains(needs, theme.ANSISwitchAttentionNeedsStart) {
		t.Fatalf("needs dot still carries the dark literal under a light theme: %q", needs)
	}
	if strings.Contains(ready, theme.ANSISwitchAttentionReadyStart) {
		t.Fatalf("ready dot still carries the dark literal under a light theme: %q", ready)
	}
	if !strings.Contains(needs, "\x1b[38;2;") || !strings.Contains(ready, "\x1b[38;2;") {
		t.Fatalf("attention dots carry no truecolor derivation: %q / %q", needs, ready)
	}

	// session-popup preview progress text follows the explicit progress token.
	part := formatPaneStatusPart("state", "thinking", true)
	if !strings.Contains(part, "\x1b[38;2;7;87;186m") {
		t.Fatalf("popup progress part = %q, want truecolor of #0757ba", part)
	}

	ApplyTheme(fallback)
	if got := formatInlineAttentionBadge("", 2, "dot"); !strings.Contains(got, theme.ANSISwitchAttentionNeedsStart) {
		t.Fatalf("needs dot not restored to the fallback literal: %q", got)
	}
	if got := formatPaneStatusPart("state", "thinking", true); !strings.Contains(got, theme.ANSIStateProgressStart) {
		t.Fatalf("popup progress part not restored to the fallback literal: %q", got)
	}
}
