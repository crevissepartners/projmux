package theme

import (
	"strconv"
	"strings"
)

// ANSIRoles is the ANSI-side semantic role map that native (terminal-rendered)
// UI consumes instead of bare ANSI* palette literals. It is the ANSI sibling of
// RenderRoles: one logical role, two adapters. RenderRolesFromEffective produces
// the tmux colourN view; ANSIRolesFromEffective produces the truecolor/256-color
// SGR-escape view used by the picker frame, switch list, notify sidebar, and
// settings rows.
//
// Tier classification (Phase 5 role-map design):
//
//   - Tier A: the role uses one of the 8 public tokens directly.
//   - Tier B: the role transforms a public token (blend/dim/contrast), with the
//     transform tuned so the fallback equals the historical literal.
//   - Tier C: renderer-only brand/state color not reducible to a public token;
//     the literal is carried under a role name but not derived.
//
// Every field, when its backing token is fallback-sourced, MUST reproduce the
// exact historical ANSI* literal so fallback-theme golden output stays
// byte-identical. Derivation only activates for explicit (non-fallback) tokens.
type ANSIRoles struct {
	// surface — base chrome. Tier A/B.
	SurfaceActive string // surface.active  Tier A <- surface_active+chrome_foreground (ANSISurfaceActiveStart)
	SurfaceRaised string // surface.raised  Tier B <- surface (fallback bg) + chrome_foreground (ANSISurfaceRaisedStart)
	SurfaceRule   string // surface.rule    Tier B <- surface bg + muted fg (ANSISurfaceRuleStart)

	// text. Tier A/B.
	TextPrimary   string // text.primary   Tier A <- text_primary (ANSITextPrimaryStart)
	TextSecondary string // text.secondary Tier B <- blend(text_primary,muted) (ANSITextSecondaryStart)
	TextMuted     string // text.muted     Tier A <- muted (ANSITextMutedStart)
	TextDim       string // text.dim       Tier C renderer-only (ANSITextDimStart \x1b[90m) — no truecolor literal

	// accent / focus. Tier A.
	AccentAction       string // accent.action        Tier A <- accent (ANSIAccentActionStart)
	AccentActionStrong string // accent.action_strong Tier A <- accent (ANSIAccentActionStrongStart)
	AccentHighlight    string // accent.highlight     Tier A <- warning (ANSIAccentHighlightStart)
	AccentSettings     string // accent.settings      Tier C renderer-only (ANSIAccentSettingsStart \x1b[36m)

	// state / severity. Tier A reuse warning/critical; rest Tier C.
	StateWarning  string // state.warning  Tier A <- warning  (ANSIStateProgressStart) — switch "working/needs review"
	StateCritical string // state.critical Tier A <- critical (ANSIStateDangerStart) — settings "remove"
	StateProgress string // state.progress Tier A <- progress (ANSIStateProgressStart fallback)
	StateExisting string // state.existing Tier C renderer-only (ANSIStateExistingStart \x1b[32m)
	StateTagged   string // state.tagged   Tier C renderer-only (ANSIStateTaggedStart \x1b[31m)
	StatePinned   string // state.pinned   Tier C renderer-only (ANSIStatePinnedStart \x1b[33m)
	StateInfo     string // state.info     Tier C renderer-only (ANSIStateInfoStart \x1b[34m)

	// chip. Tier B (256-color, structural).
	ChipInactive string // chip.inactive Tier C renderer-only (ANSIChipInactiveStart)
	ChipActive   string // chip.active   Tier C renderer-only (ANSIChipActiveStart)
	ChipDisabled string // chip.disabled Tier C renderer-only (ANSIChipDisabledStart)

	// AI badge cluster. Tier A (public tokens progress/success/action_required);
	// action_required stays independent of critical.
	AIBadgeProgress       string // ai.progress         Tier A <- progress (ANSIAIBadgeProgressStart fallback)
	AIBadgeSuccess        string // ai.success          Tier A <- success  (ANSIAIBadgeSuccessStart fallback)
	AIBadgeActionRequired string // ai.action_required  Tier A <- action_required (ANSIAIBadgeActionRequiredStart fallback); independent of critical

	// trust cluster. Tier C renderer-only.
	TrustTrusted   string // trust.trusted   (ANSITrustTrustedStart)
	TrustStale     string // trust.stale     (ANSITrustStaleStart)
	TrustUntrusted string // trust.untrusted (ANSITrustUntrustedStart)

	// notify sidebar cluster. Tier C renderer-only; badge.project is the
	// cross-surface sync role (tmux side: TmuxAttentionProjectBg+TmuxPrimaryFg).
	NotifyTitle   string // notify.title   (ANSINotifyTitleStart)
	NotifyDim     string // notify.dim     (ANSINotifyDimStart)
	NotifyAge     string // notify.age     (ANSINotifyAgeStart)
	NotifyProject string // badge.project  (ANSINotifyProjectStart) — cross-surface sync role
	NotifyInfo    string // notify.info    (ANSINotifyInfoStart)
	NotifyWarn    string // notify.warn    (ANSINotifyWarnStart)
	NotifyCrit    string // notify.crit    (ANSINotifyCritStart)
	NotifyStale   string // notify.stale   (ANSINotifyStaleStart)
	NotifyGone    string // notify.gone    (ANSINotifyGoneStart)
	NotifyAgent   string // notify.agent   (ANSINotifyAgentStart) — AI brand teal

	// switch (structural) cluster. Tier A/C.
	SwitchPath            string // switch.path                Tier A <- muted (ANSISwitchPathStart, 256-color)
	SwitchGitActive       string // switch.git_active          Tier C renderer-only (ANSISwitchGitActiveStart)
	SwitchGitInactive     string // switch.git_inactive        Tier C renderer-only (ANSISwitchGitInactiveStart)
	SwitchWindowTabActive string // switch.window_tab_active   Tier C renderer-only (ANSISwitchWindowTabActiveStart)
	SwitchWindowTab       string // switch.window_tab_inactive Tier C renderer-only (ANSISwitchWindowTabStart)
	SwitchAttentionNeeds  string // switch.attention_needs     Tier C renderer-only (ANSISwitchAttentionNeedsStart)
	SwitchAttentionReady  string // switch.attention_ready     Tier C renderer-only (ANSISwitchAttentionReadyStart)
}

// ANSIRolesFromEffective derives the ANSI semantic role map from an effective
// theme. Fallback-sourced roles deliberately reproduce the historical ANSI*
// palette literals so native UI rendered with the built-in fallback theme stays
// byte-identical. Derivation only activates for explicit (non-fallback) tokens.
func ANSIRolesFromEffective(effective EffectiveTheme) ANSIRoles {
	return ANSIRoles{
		// surface
		SurfaceActive: ansiBGFGOrLiteral(effective.SurfaceActive, fgWhite, ANSISurfaceActiveStart),
		// surface.raised: the (near-dead) surface token now feeds raised/rule so
		// it is wired. Fallback surface == background #182226 so the literal
		// holds; an explicit surface repaints the picker titlebar/popup body.
		SurfaceRaised: ansiBGFieldFGFieldOrLiteral(effective.Surface, effective.ChromeForeground, ANSISurfaceRaisedStart),
		SurfaceRule:   ansiBGFieldFGFieldOrLiteral(effective.Surface, effective.Muted, ANSISurfaceRuleStart),

		// text
		TextPrimary:   ansiFGOrLiteral(effective.TextPrimary, ANSITextPrimaryStart),
		TextSecondary: ansiBlendFGOrLiteral(effective.TextPrimary, effective.Muted, ANSITextSecondaryStart),
		TextMuted:     ansiFGOrLiteral(effective.Muted, ANSITextMutedStart),
		// text.dim is the \x1b[90m bright-black attribute with no truecolor
		// literal; carry it verbatim (Tier C).
		TextDim: ANSITextDimStart,

		// accent / focus
		AccentAction:       ansiFGOrLiteral(effective.Accent, ANSIAccentActionStart),
		AccentActionStrong: ansiFGOrLiteral(effective.Accent, ANSIAccentActionStrongStart),
		AccentHighlight:    ansiFGOrLiteral(effective.Warning, ANSIAccentHighlightStart),
		AccentSettings:     ANSIAccentSettingsStart,

		// state / severity — warning/critical are Tier A (same logical role as
		// RenderRoles StateWarning/StateCritical); the rest are Tier C literals.
		StateWarning:  ansiFGOrLiteral(effective.Warning, ANSIStateProgressStart),
		StateCritical: ansiFGOrLiteral(effective.Critical, ANSIStateDangerStart),
		StateProgress: ansiFGOrLiteral(effective.Progress, ANSIStateProgressStart),
		StateExisting: ANSIStateExistingStart,
		StateTagged:   ANSIStateTaggedStart,
		StatePinned:   ANSIStatePinnedStart,
		StateInfo:     ANSIStateInfoStart,

		// chip — 256-color structural literals (Tier C; no public 256-color
		// token maps cleanly, carried verbatim).
		ChipInactive: ANSIChipInactiveStart,
		ChipActive:   ANSIChipActiveStart,
		ChipDisabled: ANSIChipDisabledStart,

		// AI badge — Tier A: progress/success/action_required follow the public
		// tokens, falling back to the historical brand/severity literals when
		// unset. action_required stays independent of critical.
		AIBadgeProgress:       ansiFGOrLiteral(effective.Progress, ANSIAIBadgeProgressStart),
		AIBadgeSuccess:        ansiFGOrLiteral(effective.Success, ANSIAIBadgeSuccessStart),
		AIBadgeActionRequired: ansiFGOrLiteral(effective.ActionRequired, ANSIAIBadgeActionRequiredStart),

		// trust — brand literals (Tier C).
		TrustTrusted:   ANSITrustTrustedStart,
		TrustStale:     ANSITrustStaleStart,
		TrustUntrusted: ANSITrustUntrustedStart,

		// notify — brand/severity literals (Tier C). badge.project stays the
		// literal on both adapters so the cross-surface sync test holds.
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

		// switch — only switch.path follows the public muted token (Tier A,
		// 256-color); the rest are renderer-only literals (Tier C).
		SwitchPath:            ansi256FGOrLiteral(effective.Muted, ANSISwitchPathStart),
		SwitchGitActive:       ANSISwitchGitActiveStart,
		SwitchGitInactive:     ANSISwitchGitInactiveStart,
		SwitchWindowTabActive: ANSISwitchWindowTabActiveStart,
		SwitchWindowTab:       ANSISwitchWindowTabStart,
		SwitchAttentionNeeds:  ANSISwitchAttentionNeedsStart,
		SwitchAttentionReady:  ANSISwitchAttentionReadyStart,
	}
}

// fgWhite is the historical white foreground baked into ANSISurfaceActiveStart
// (\x1b[38;2;255;255;255m). Selected/active rows pair an explicit surface
// background with this fixed white text, matching the picker's contrast model.
const fgWhite = "\x1b[38;2;255;255;255m"

// ansiFGOrLiteral returns the truecolor foreground SGR escape for an explicit
// color field, else the historical literal for fallback-sourced fields.
func ansiFGOrLiteral(field ColorField, literal string) string {
	if field.Source == SourceFallback {
		return literal
	}
	if fg := field.Value.TruecolorFG(); fg != "" {
		return "\x1b[" + fg + "m"
	}
	return literal
}

// ansi256FGOrLiteral returns the 256-color foreground SGR escape for an explicit
// color field (mapped via its nearest-tmux approximation), else the literal.
// Used for the 256-color switch.path role so it stays in the same color space as
// the historical literal.
func ansi256FGOrLiteral(field ColorField, literal string) string {
	if field.Source == SourceFallback || strings.TrimSpace(field.Value.Tmux) == "" {
		return literal
	}
	return ANSI256FgStart(field.Value.Tmux)
}

// ansiBGFGOrLiteral pairs an explicit background field with a fixed foreground
// escape; fallback returns the literal verbatim. When the background field is the
// terminal-default sentinel it emits the foreground escape only, so the surface
// inherits the terminal background (no 48;2 sequence).
func ansiBGFGOrLiteral(bg ColorField, fg, literal string) string {
	if bg.Source == SourceFallback {
		return literal
	}
	if IsThemeDefaultSpec(bg.Value) {
		return fg
	}
	if b := bg.Value.TruecolorBG(); b != "" {
		return "\x1b[" + b + "m" + fg
	}
	return literal
}

// ansiBGFieldFGFieldOrLiteral pairs an explicit background field with an
// explicit foreground field. It derives only when at least one of the two is
// explicit, so a fully-fallback pair returns the byte-identical literal. When
// only one side is explicit the other falls back to its built-in token so the
// surface still reads correctly.
func ansiBGFieldFGFieldOrLiteral(bg, fg ColorField, literal string) string {
	if bg.Source == SourceFallback && fg.Source == SourceFallback {
		return literal
	}
	f := fg.Value.TruecolorFG()
	// Terminal-default sentinel background: emit the foreground escape only so the
	// surface inherits the terminal background (no 48;2 sequence). Fall back to a
	// foreground-token escape when the fg side is itself a fallback.
	if IsThemeDefaultSpec(bg.Value) {
		if f != "" {
			return "\x1b[" + f + "m"
		}
		return literal
	}
	b := bg.Value.TruecolorBG()
	if b == "" || f == "" {
		return literal
	}
	return "\x1b[" + b + "m\x1b[" + f + "m"
}

// ansiBlendFGOrLiteral implements the Tier B text.secondary transform: an even
// blend of the foreground and muted tokens. For a fallback theme it returns the
// historical literal (\x1b[38;2;164;176;182m) verbatim so generated output stays
// byte-identical; the blend only activates when at least one input is explicit.
func ansiBlendFGOrLiteral(primary, muted ColorField, literal string) string {
	if primary.Source == SourceFallback && muted.Source == SourceFallback {
		return literal
	}
	pr, pg, pb, ok1 := parseHexRGB(primary.Value.Hex)
	mr, mg, mb, ok2 := parseHexRGB(muted.Value.Hex)
	if !ok1 || !ok2 {
		return literal
	}
	r := (pr + mr) / 2
	g := (pg + mg) / 2
	b := (pb + mb) / 2
	return ansiTruecolorFG(r, g, b)
}

func ansiTruecolorFG(r, g, b int) string {
	return "\x1b[38;2;" + strconv.Itoa(r) + ";" + strconv.Itoa(g) + ";" + strconv.Itoa(b) + "m"
}
