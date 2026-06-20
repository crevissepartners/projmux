package theme

import (
	"strings"
	"testing"
)

// TestANSIRolesFallbackPreservesBuiltInPalette is the byte-identity guard for
// the ANSI-side adapter, mirroring TestRenderRolesFallbackPreservesBuiltInPalette.
// Every fallback-sourced role MUST equal the exact historical ANSI* literal so
// native UI rendered with the built-in fallback theme stays byte-identical.
func TestANSIRolesFallbackPreservesBuiltInPalette(t *testing.T) {
	t.Parallel()

	got := ANSIRolesFromEffective(ResolveTheme(ThemeConfig{}))
	want := ANSIRoles{
		SurfaceActive: ANSISurfaceActiveStart,
		SurfaceRaised: ANSISurfaceRaisedStart,
		SurfaceRule:   ANSISurfaceRuleStart,

		TextPrimary:   ANSITextPrimaryStart,
		TextSecondary: ANSITextSecondaryStart,
		TextMuted:     ANSITextMutedStart,
		TextDim:       ANSITextDimStart,

		AccentAction:       ANSIAccentActionStart,
		AccentActionStrong: ANSIAccentActionStrongStart,
		AccentHighlight:    ANSIAccentHighlightStart,
		AccentSettings:     ANSIAccentSettingsStart,

		StateWarning:  ANSIStateProgressStart,
		StateCritical: ANSIStateDangerStart,
		StateProgress: ANSIStateProgressStart,
		StateExisting: ANSIStateExistingStart,
		StateTagged:   ANSIStateTaggedStart,
		StatePinned:   ANSIStatePinnedStart,
		StateInfo:     ANSIStateInfoStart,

		ChipInactive: ANSIChipInactiveStart,
		ChipActive:   ANSIChipActiveStart,
		ChipDisabled: ANSIChipDisabledStart,

		AIBadgeProgress:       ANSIAIBadgeProgressStart,
		AIBadgeSuccess:        ANSIAIBadgeSuccessStart,
		AIBadgeActionRequired: ANSIAIBadgeActionRequiredStart,

		TrustTrusted:   ANSITrustTrustedStart,
		TrustStale:     ANSITrustStaleStart,
		TrustUntrusted: ANSITrustUntrustedStart,

		NotifyTitle:   ANSINotifyTitleStart,
		NotifyDim:     ANSINotifyDimStart,
		NotifyAge:     ANSINotifyAgeStart,
		NotifyProject: ANSINotifyProjectStart,
		NotifyInfo:    ANSINotifyInfoStart,
		NotifyWarn:    ANSINotifyWarnStart,
		NotifyCrit:    ANSINotifyCritStart,
		NotifyStale:   ANSINotifyStaleStart,
		NotifyGone:    ANSINotifyGoneStart,
		NotifyAgent:   ANSINotifyAgentStart,

		SwitchPath:            ANSISwitchPathStart,
		SwitchGitActive:       ANSISwitchGitActiveStart,
		SwitchGitInactive:     ANSISwitchGitInactiveStart,
		SwitchWindowTabActive: ANSISwitchWindowTabActiveStart,
		SwitchWindowTab:       ANSISwitchWindowTabStart,
		SwitchAttentionNeeds:  ANSISwitchAttentionNeedsStart,
		SwitchAttentionReady:  ANSISwitchAttentionReadyStart,
	}
	if got != want {
		t.Fatalf("fallback ANSI roles = %#v, want %#v", got, want)
	}
}

// TestANSIRolesExplicitThemeRepaintsCoreRoles proves an explicit global theme
// repaints the Tier A/B native-UI roles with a derived truecolor escape rather
// than the historical literal.
func TestANSIRolesExplicitThemeRepaintsCoreRoles(t *testing.T) {
	t.Parallel()

	got := ANSIRolesFromEffective(ResolveTheme(ThemeConfig{
		Background:    "#102030",
		Surface:       "#112233",
		SurfaceActive: "#445566",
		Foreground:    "#eef0f2",
		Muted:         "#808890",
		Accent:        "#00ddaa",
		Warning:       "#ffaa00",
		Critical:      "#dd2244",
	}))

	cases := []struct {
		name    string
		value   string
		want    string
		literal string
	}{
		{"text.primary", got.TextPrimary, "\x1b[38;2;238;240;242m", ANSITextPrimaryStart},
		{"text.muted", got.TextMuted, "\x1b[38;2;128;136;144m", ANSITextMutedStart},
		{"accent.action", got.AccentAction, "\x1b[38;2;0;221;170m", ANSIAccentActionStart},
		{"accent.action_strong", got.AccentActionStrong, "\x1b[38;2;0;221;170m", ANSIAccentActionStrongStart},
		{"accent.highlight", got.AccentHighlight, "\x1b[38;2;255;170;0m", ANSIAccentHighlightStart},
		{"state.warning", got.StateWarning, "\x1b[38;2;255;170;0m", ANSIStateProgressStart},
		{"state.critical", got.StateCritical, "\x1b[38;2;221;34;68m", ANSIStateDangerStart},
		// surface.active pairs explicit bg with the fixed white fg.
		{"surface.active", got.SurfaceActive, "\x1b[48;2;68;85;102m" + fgWhite, ANSISurfaceActiveStart},
		// surface.raised: explicit surface bg + explicit foreground.
		{"surface.raised", got.SurfaceRaised, "\x1b[48;2;17;34;51m\x1b[38;2;238;240;242m", ANSISurfaceRaisedStart},
		// surface.rule: explicit surface bg + muted fg.
		{"surface.rule", got.SurfaceRule, "\x1b[48;2;17;34;51m\x1b[38;2;128;136;144m", ANSISurfaceRuleStart},
		// text.secondary: even blend of foreground + muted.
		{"text.secondary", got.TextSecondary, ansiTruecolorFG((0xee+0x80)/2, (0xf0+0x88)/2, (0xf2+0x90)/2), ANSITextSecondaryStart},
	}
	for _, tc := range cases {
		if tc.value == tc.literal {
			t.Errorf("%s did not repaint: still the fallback literal %q", tc.name, tc.literal)
		}
		if tc.value != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.value, tc.want)
		}
	}

	// switch.path is a 256-color role: it repaints to a colourN escape.
	if !strings.HasPrefix(got.SwitchPath, "\x1b[38;5;") {
		t.Errorf("switch.path = %q, want a 256-color escape", got.SwitchPath)
	}
	if got.SwitchPath == ANSISwitchPathStart {
		t.Errorf("switch.path did not repaint: still %q", ANSISwitchPathStart)
	}
}

// TestANSIRolesProjectBadgeStaysLiteral guards the cross-surface sync role:
// badge.project (NotifyProject) is a Tier C brand literal carried verbatim on
// both adapters, so it must NOT repaint even under an explicit theme.
func TestANSIRolesProjectBadgeStaysLiteral(t *testing.T) {
	t.Parallel()

	got := ANSIRolesFromEffective(ResolveTheme(ThemeConfig{Background: "#102030", Accent: "#00ddaa"}))
	if got.NotifyProject != ANSINotifyProjectStart {
		t.Fatalf("badge.project = %q, want literal %q (cross-surface sync)", got.NotifyProject, ANSINotifyProjectStart)
	}
}
