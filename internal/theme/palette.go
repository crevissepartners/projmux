// Package theme contains the built-in fallback palette used by projmux UI
// renderers. It is intentionally data-only for now: project/global theme
// resolution will layer on top of these semantic tokens in a later phase.
package theme

import "strings"

const (
	ANSIReset   = "\x1b[0m"
	ANSIBold    = "\x1b[1m"
	ANSIDim     = "\x1b[2m"
	ANSIInverse = "\x1b[7m"
)

// ANSI256FgStart returns an ANSI 256-color foreground SGR for a tmux colourN
// token. Style suffixes such as ",bold" are intentionally ignored because
// callers compose attributes separately.
func ANSI256FgStart(tmuxColor string) string {
	return "\x1b[38;5;" + strings.TrimPrefix(strings.Split(tmuxColor, ",")[0], "colour") + "m"
}

const (
	// Truecolor SGR tokens for native terminal-rendered surfaces.
	ANSISurfaceActiveStart = "\x1b[48;2;44;56;61m\x1b[38;2;255;255;255m"
	ANSISurfaceRaisedStart = "\x1b[48;2;24;34;38m\x1b[38;2;216;224;228m"
	ANSISurfaceRuleStart   = "\x1b[48;2;24;34;38m\x1b[38;2;117;132;140m"

	ANSITextPrimaryStart   = "\x1b[38;2;216;224;228m"
	ANSITextSecondaryStart = "\x1b[38;2;164;176;182m"
	ANSITextMutedStart     = "\x1b[38;2;117;132;140m"
	ANSITextDimStart       = "\x1b[90m"

	ANSIAccentActionStart       = "\x1b[38;2;141;205;142m"
	ANSIAccentActionStrongStart = "\x1b[38;2;122;199;173m"
	ANSIAccentHighlightStart    = "\x1b[38;2;255;204;102m"
	ANSIAccentSettingsStart     = "\x1b[36m"

	ANSIStateProgressStart = "\x1b[38;2;255;204;102m"
	ANSIStateDangerStart   = "\x1b[38;2;255;107;107m"
	ANSIStateExistingStart = "\x1b[32m"
	ANSIStateTaggedStart   = "\x1b[31m"
	ANSIStatePinnedStart   = "\x1b[33m"
	ANSIStateInfoStart     = "\x1b[34m"

	ANSIAIBadgeProgressStart       = ANSIStateProgressStart
	ANSIAIBadgeSuccessStart        = "\x1b[38;5;72m"
	ANSIAIBadgeActionRequiredStart = "\x1b[38;5;214m"

	ANSITrustTrustedStart   = "\x1b[38;2;154;191;136m"
	ANSITrustStaleStart     = "\x1b[38;2;177;139;212m"
	ANSITrustUntrustedStart = "\x1b[38;2;210;139;88m"
)

const (
	// 256-color SGR tokens used by native sidebar badges and chip strips.
	ANSIChipInactiveStart = "\x1b[48;5;235m\x1b[38;5;245m"
	ANSIChipActiveStart   = "\x1b[1m\x1b[48;5;240m\x1b[38;5;231m"
	ANSIChipDisabledStart = "\x1b[2m\x1b[48;5;235m\x1b[38;5;245m"

	ANSINotifyTitleStart   = "\x1b[1;38;5;220m"
	ANSINotifyDimStart     = "\x1b[38;5;245m"
	ANSINotifyAgeStart     = "\x1b[38;5;153m"
	ANSINotifyProjectStart = "\x1b[1;38;5;231;48;5;90m"
	ANSINotifyInfoStart    = "\x1b[1;38;5;16;48;5;220m"
	ANSINotifyWarnStart    = "\x1b[1;38;5;16;48;5;214m"
	ANSINotifyCritStart    = "\x1b[1;38;5;231;48;5;160m"
	ANSINotifyStaleStart   = "\x1b[2;38;5;231;48;5;240m"
	ANSINotifyGoneStart    = "\x1b[2;9;38;5;231;48;5;238m"
	ANSINotifyAgentStart   = "\x1b[1;38;5;16;48;5;37m"

	ANSISwitchPathStart            = "\x1b[38;5;242m"
	ANSISwitchGitActiveStart       = "\x1b[1;38;5;231;48;5;30m"
	ANSISwitchGitInactiveStart     = "\x1b[38;5;231;48;5;30m"
	ANSISwitchWindowTabActiveStart = "\x1b[1;38;5;231;48;5;238m"
	ANSISwitchWindowTabStart       = "\x1b[38;5;245;48;5;235m"
	ANSISwitchAttentionNeedsStart  = "\x1b[38;5;220m"
	ANSISwitchAttentionReadyStart  = "\x1b[38;5;82m"
)

const (
	// Tmux 256-color tokens for the statusbar and generated tmux config.
	TmuxWindowInactiveBg = "colour235"
	TmuxWindowInactiveFg = "colour245"
	TmuxWindowActiveBg   = "colour240"
	TmuxWindowActiveFg   = "colour231"

	TmuxIdentityBg  = "colour60"
	TmuxIdentityFg  = "colour254"
	TmuxActionBg    = "colour29"
	TmuxActionFg    = "colour230"
	TmuxPrimaryFg   = "colour231"
	TmuxSecondaryFg = "colour245"
	TmuxMutedFg     = "colour244"
	TmuxDividerFg   = "colour239"
	TmuxMutedBg     = "colour240"
	TmuxGoneBg      = "colour238"

	TmuxAccentAttentionBg  = "colour53"
	TmuxAttentionProjectBg = "colour90"
	TmuxAccentAIBg         = "colour37"
	TmuxAccentAIFg         = "colour121"

	TmuxStateProgressFg = "colour220"
	TmuxStateWarningFg  = "colour214"
	TmuxStateCriticalFg = "colour160"
	TmuxStateSuccessFg  = "colour72"
	TmuxStateStagedFg   = "colour151"
	TmuxStateDirtyFg    = "colour222"
	TmuxStateAheadFg    = "colour153"
	TmuxStateBehindFg   = "colour181"

	TmuxAIBadgeProgressFg       = TmuxStateProgressFg
	TmuxAIBadgeSuccessFg        = TmuxStateSuccessFg
	TmuxAIBadgeActionRequiredFg = TmuxStateWarningFg

	TmuxGitSegmentBg = "colour30"
	TmuxGitSegmentFg = TmuxPrimaryFg

	TmuxUsageEmptyFg = TmuxGoneBg

	TmuxPaneBorderFg       = "colour236"
	TmuxPaneActiveBorderFg = "colour51"
	TmuxPaneActiveBg       = "colour45"
	TmuxPaneActiveFg       = "colour16"
	TmuxPaneActiveTintBg   = "colour234" // active-pane window-active-style tint; one tone darker than surface.base (colour235)
	TmuxMessageBg          = "colour208"
	TmuxMessageFg          = "colour16"

	TmuxDecorationCwdFg    = "colour220"
	TmuxDecorationGitLabFg = "colour215"
)
