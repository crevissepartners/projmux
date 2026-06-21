package theme

import (
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Source identifies the layer that supplied an effective theme field. Theme
// is a global user preference: the resolver only knows the global user theme
// and the built-in fallback. Project-local theme is no longer a source.
type Source string

const (
	SourceGlobal   Source = "global"
	SourceFallback Source = "fallback"
)

// ColorToken is a resolver-facing semantic color token. Renderer-specific
// roles can map onto these stable names before adapting to ANSI or tmux.
type ColorToken string

const (
	TokenBackground       ColorToken = "background"
	TokenSurface          ColorToken = "surface"
	TokenStatusBackground ColorToken = "status_background"
	TokenSurfaceActive    ColorToken = "surface_active"
	TokenChromeForeground ColorToken = "chrome_foreground"
	TokenTextPrimary      ColorToken = "text_primary"
	TokenForeground       ColorToken = "foreground"
	TokenMuted            ColorToken = "muted"
	TokenAccent           ColorToken = "accent"
	TokenCritical         ColorToken = "critical"
	TokenWarning          ColorToken = "warning"
	TokenProgress         ColorToken = "progress"
	TokenSuccess          ColorToken = "success"
	TokenActionRequired   ColorToken = "action_required"
	TokenPaneActiveBg     ColorToken = "pane_active_bg"
	TokenFocus            ColorToken = "focus"
)

// ResolverColorTokens is the stable display/serialization order for theme
// color fields.
var ResolverColorTokens = []ColorToken{
	TokenBackground,
	TokenSurface,
	TokenStatusBackground,
	TokenSurfaceActive,
	TokenChromeForeground,
	TokenTextPrimary,
	TokenForeground,
	TokenMuted,
	TokenAccent,
	TokenCritical,
	TokenWarning,
	TokenProgress,
	TokenSuccess,
	TokenActionRequired,
	TokenPaneActiveBg,
	TokenFocus,
}

// ThemeConfig is the user-configurable theme section from global or project
// config.toml. Values are intentionally raw: validation belongs to the
// resolver so an invalid layer can warn and fall through to the next source.
type ThemeConfig struct {
	Preset           string
	Background       string
	Surface          string
	StatusBackground string
	SurfaceActive    string
	ChromeForeground string
	TextPrimary      string
	Foreground       string
	Muted            string
	Accent           string
	Critical         string
	Warning          string
	Progress         string
	Success          string
	ActionRequired   string
	PaneActiveBg     string
	Focus            string
}

// HasContent reports whether the config carries any theme override.
func (c ThemeConfig) HasContent() bool {
	for _, value := range []string{
		c.Preset,
		c.Background,
		c.Surface,
		c.StatusBackground,
		c.SurfaceActive,
		c.ChromeForeground,
		c.TextPrimary,
		c.Foreground,
		c.Muted,
		c.Accent,
		c.Critical,
		c.Warning,
		c.Progress,
		c.Success,
		c.ActionRequired,
		c.PaneActiveBg,
		c.Focus,
	} {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

// Normalize trims stored theme values. It does not validate them; invalid
// values are surfaced by ResolveTheme warnings.
func (c *ThemeConfig) Normalize() {
	c.Preset = strings.TrimSpace(c.Preset)
	c.Background = strings.TrimSpace(c.Background)
	c.Surface = strings.TrimSpace(c.Surface)
	c.StatusBackground = strings.TrimSpace(c.StatusBackground)
	c.SurfaceActive = strings.TrimSpace(c.SurfaceActive)
	c.ChromeForeground = strings.TrimSpace(c.ChromeForeground)
	c.TextPrimary = strings.TrimSpace(c.TextPrimary)
	c.Foreground = strings.TrimSpace(c.Foreground)
	c.Muted = strings.TrimSpace(c.Muted)
	c.Accent = strings.TrimSpace(c.Accent)
	c.Critical = strings.TrimSpace(c.Critical)
	c.Warning = strings.TrimSpace(c.Warning)
	c.Progress = strings.TrimSpace(c.Progress)
	c.Success = strings.TrimSpace(c.Success)
	c.ActionRequired = strings.TrimSpace(c.ActionRequired)
	c.PaneActiveBg = strings.TrimSpace(c.PaneActiveBg)
	c.Focus = strings.TrimSpace(c.Focus)
}

// ColorSpec carries exact truecolor plus a nearest xterm-256 approximation.
// Tmux style roles prefer Hex for explicit values; the 256-color form remains
// for renderer paths that intentionally need colourN/ANSI-256 compatibility.
type ColorSpec struct {
	Hex  string
	Tmux string
}

// TruecolorFG returns the foreground SGR token for this color without the CSI
// wrapper. Renderers can compose it with other attributes.
func (c ColorSpec) TruecolorFG() string {
	r, g, b, ok := parseHexRGB(c.Hex)
	if !ok {
		return ""
	}
	return fmt.Sprintf("38;2;%d;%d;%d", r, g, b)
}

// TruecolorBG returns the background SGR token for this color without the CSI
// wrapper. Renderers can compose it with other attributes.
func (c ColorSpec) TruecolorBG() string {
	r, g, b, ok := parseHexRGB(c.Hex)
	if !ok {
		return ""
	}
	return fmt.Sprintf("48;2;%d;%d;%d", r, g, b)
}

type ColorField struct {
	Value  ColorSpec
	Source Source
}

type StringField struct {
	Value  string
	Source Source
}

type EffectiveField struct {
	Name   string
	Value  string
	Source Source
}

// Warning describes a skipped theme layer. One invalid value invalidates only
// that layer; lower-priority layers still participate.
type Warning struct {
	Source  Source
	Field   string
	Value   string
	Message string
}

type EffectiveTheme struct {
	Preset           StringField
	Background       ColorField
	Surface          ColorField
	StatusBackground ColorField
	SurfaceActive    ColorField
	ChromeForeground ColorField
	TextPrimary      ColorField
	Foreground       ColorField
	Muted            ColorField
	Accent           ColorField
	Critical         ColorField
	Warning          ColorField
	Progress         ColorField
	Success          ColorField
	ActionRequired   ColorField
	PaneActiveBg     ColorField
	Focus            ColorField
	Warnings         []Warning
}

// RenderRoles is the semantic role -> tmux color map that app chrome consumes
// instead of bare palette literals. It is the Phase 2 foundation ("골대"): the
// renderer asks for a role, not a colourN constant, so an explicit global theme
// can repaint chrome by repointing the role's derivation.
//
// Each role is classified by a derivation tier (see the Phase 2 role-map design
// note ~/Obsidian/.../Theme semantic role map 토큰 조합 설계):
//
//   - Tier A: the role uses one of the 8 public tokens directly.
//   - Tier B: the role transforms a public token (contrast fg, tint/blend),
//     with the transform tuned so the fallback equals the historical literal.
//   - Tier C: renderer-only brand/state color not yet reducible to a public
//     token; the literal is carried under a role name but not derived.
//
// Phase 2 only WIRES the window/status/pane chrome roles below. Other clusters
// (state/AI/git/usage/notify/decoration/kube/native-UI) keep consuming literals
// directly and are intentionally absent here until later phases promote them.
type RenderRoles struct {
	// surface — base chrome. Tier A.
	WindowInactiveBg string // surface.base       <- background      (colour235)
	WindowInactiveFg string // text on inactive    <- chrome_foreground (colour245 fallback)
	WindowActiveBg   string // surface.active      <- surface_active  (colour240)
	WindowActiveFg   string // text on active      <- chrome_foreground (colour231 fallback)
	// status bar bg — bottom status line group. Follows `status_background`.
	// Popup/native frames consume `surface` directly, so status can now be tuned
	// without repainting Settings/recent/picker popup surfaces.
	StatusBg string // status bar bg       <- status_background (colour235)
	StatusFg string // status bar fg       <- chrome_foreground (colour245 fallback)

	// pane / focus chrome.
	PaneBorder string // pane.border          Tier A <- muted-ish (colour236)
	// focus.border — dedicated role decoupled from accent. Tier A (public token
	// `focus`): derives from the explicit focus token, falling back to the
	// literal colour51 when unset so the active-pane border is unchanged. Tunable
	// independently of the topic chip / pointer / action that still share accent.
	FocusBorder     string
	PaneTopicChipBg string // pane.topic_chip_bg   Tier A <- accent    (colour45)
	PaneTopicChipFg string // pane.topic_chip_fg   Tier B <- contrastFg (colour16)

	// focus.pane_active_bg — active-pane window-active-style tint. Decoupled from
	// surface_active: a dedicated DARK tint one tone darker than surface.base.
	// Tier A (public token `pane_active_bg`): derives from the explicit token,
	// falling back to the literal colour234 when unset. surface_active is a LIGHT
	// tone (colour240) and was the wrong direction for this role.
	FocusPaneActiveBg string

	// general/pane-body inactive bg — general (pane) bg group (Phase 6b). Tier A
	// (public token `background`): drives tmux `window-style` for inactive panes.
	// Fallback is the terminal default literal "default" (NOT a colour literal),
	// so an unset background keeps `window-style "bg=default"`; an explicit
	// background repaints the pane body.
	// Separated from surface/status roles so pane body, popup/native frames, and
	// the bottom status bar can be tuned independently.
	PaneInactiveBg string

	// state / severity cluster (Phase 3). Single source for the notify HUD,
	// usage HUD, and statusbar severity tints on the tmux side.
	StateWarning  string // state.warning  Tier A <- warning   (colour214)
	StateCritical string // state.critical Tier A <- critical  (colour160)
	StateProgress string // state.progress Tier A <- progress (colour220 fallback)
	StateSuccess  string // state.success  Tier A <- success  (colour72 fallback)

	// AI-status cluster (Phase 3). One logical color per AI badge role.
	// AIProgress/AISuccess reuse the state.progress/success colors; action
	// required is kept as its OWN role and must NEVER merge into critical.
	AIProgress       string // ai.progress        Tier A <- progress (colour220 fallback)
	AISuccess        string // ai.success         Tier A <- success  (colour72 fallback)
	AIActionRequired string // ai.action_required Tier A <- action_required (colour214 fallback); independent of critical

	// statusbar git segment cluster (Phase 4). The segment foreground is the
	// only chrome_foreground-derived role here; the segment bg and the staged/dirty/
	// ahead/behind state colors are renderer-only literals carried verbatim.
	GitSegmentFg string // git.segment_fg Tier A <- chrome_foreground (colour231 fallback)
	GitSegmentBg string // git.segment_bg Tier C renderer-only (colour30)
	GitStaged    string // git.staged     Tier C renderer-only (colour151)
	GitDirty     string // git.dirty      Tier C renderer-only (colour222)
	GitAhead     string // git.ahead      Tier C renderer-only (colour153) — also the github-decoration source
	GitBehind    string // git.behind     Tier C renderer-only (colour181)

	// statusbar decoration cluster (Phase 4). cwd keeps its OWN role even though
	// it shares colour220 with state.progress; the github/generic-git decoration
	// colors reuse GitAhead/GitStaged (single source) and so have no field here.
	DecorationCwd    string // decoration.cwd    Tier C renderer-only (colour220) — independent of state.progress
	DecorationGitLab string // decoration.gitlab Tier C renderer-only (colour215)

	// statusbar kube cluster (Phase 4). tmux named colors carried verbatim for
	// output compatibility with the existing segment.
	KubeContext   string // kube.context   Tier C renderer-only ("red")
	KubeNamespace string // kube.namespace Tier C renderer-only ("blue")

	// statusbar identity/action chrome (Phase 4). Renderer-only literals.
	IdentityBg string // identity.bg Tier C renderer-only (colour60)
	IdentityFg string // identity.fg Tier C renderer-only (colour254)
	ActionBg   string // action.bg   Tier C renderer-only (colour29)
	ActionFg   string // action.fg   Tier C renderer-only (colour230)

	// statusbar divider (Phase 4). Renderer-only literal; kept simple, not derived.
	DividerFg string // divider.fg Tier C renderer-only (colour239)
}

// RenderRolesFromEffective derives the semantic role map from an effective
// theme. Fallback-sourced roles keep their historical palette literals unless a
// role intentionally inherits the terminal default.
func RenderRolesFromEffective(effective EffectiveTheme) RenderRoles {
	return RenderRoles{
		WindowInactiveBg: tmuxColorOrFallback(effective.Background, TmuxWindowInactiveBg),
		WindowInactiveFg: tmuxColorOrFallback(effective.ChromeForeground, TmuxWindowInactiveFg),
		WindowActiveBg:   tmuxColorOrFallback(effective.SurfaceActive, TmuxWindowActiveBg),
		WindowActiveFg:   tmuxColorOrFallback(effective.ChromeForeground, TmuxWindowActiveFg),
		// Bottom status group: StatusBg follows `status_background`, not
		// `surface`. Fallback stays colour235 so the status bar keeps its own
		// historical background.
		StatusBg: tmuxColorOrFallback(effective.StatusBackground, TmuxWindowInactiveBg),
		StatusFg: tmuxColorOrFallback(effective.ChromeForeground, TmuxWindowInactiveFg),

		PaneBorder: tmuxColorOrFallback(effective.Muted, TmuxPaneBorderFg),
		// Tier A: focus.border follows the explicit `focus` public token, falling
		// back to the literal colour51 so the active-pane border is unchanged when
		// unset. Tunable independently of the topic chip / pointer / action that
		// still derive from accent.
		FocusBorder:     tmuxColorOrFallback(effective.Focus, TmuxPaneActiveBorderFg),
		PaneTopicChipBg: tmuxColorOrFallback(effective.Accent, TmuxPaneActiveBg),
		PaneTopicChipFg: tmuxContrastFgOrFallback(effective.Accent, TmuxPaneActiveFg),

		// Tier A: dedicated dark active-pane tint, decoupled from surface_active.
		// Follows the explicit `pane_active_bg` public token, falling back to the
		// literal colour234 (one tone darker than surface.base) when unset.
		FocusPaneActiveBg: tmuxColorOrFallback(effective.PaneActiveBg, TmuxPaneActiveTintBg),

		// General/pane bg group (Phase 6b): inactive-pane window-style follows the
		// explicit `background` public token, falling back to the terminal default
		// literal "default" (not a colour) so unset background keeps the pane body
		// on the terminal background.
		PaneInactiveBg: tmuxColorOrFallback(effective.Background, "default"),

		// Tier A: warning/critical follow the explicit public token so an
		// explicit theme repaints the notify/usage/statusbar severity tints.
		StateWarning:  tmuxColorOrFallback(effective.Warning, TmuxStateWarningFg),
		StateCritical: tmuxColorOrFallback(effective.Critical, TmuxStateCriticalFg),
		// Tier A: progress/success follow the explicit public tokens, falling
		// back to the historical literals (colour220/colour72) when unset.
		StateProgress: tmuxColorOrFallback(effective.Progress, TmuxStateProgressFg),
		StateSuccess:  tmuxColorOrFallback(effective.Success, TmuxStateSuccessFg),

		// AI-status: progress/success reuse the progress/success public tokens
		// (same logical color). action_required is its OWN Tier A role —
		// keep it independent of StateCritical; never merge.
		AIProgress:       tmuxColorOrFallback(effective.Progress, TmuxStateProgressFg),
		AISuccess:        tmuxColorOrFallback(effective.Success, TmuxStateSuccessFg),
		AIActionRequired: tmuxColorOrFallback(effective.ActionRequired, TmuxAIBadgeActionRequiredFg),

		// statusbar git segment: only the segment fg follows the public
		// chrome_foreground token (Tier A). The segment bg and state colors are
		// Tier C renderer-only literals carried verbatim.
		GitSegmentFg: tmuxColorOrFallback(effective.ChromeForeground, TmuxGitSegmentFg),
		GitSegmentBg: TmuxGitSegmentBg,
		GitStaged:    TmuxStateStagedFg,
		GitDirty:     TmuxStateDirtyFg,
		GitAhead:     TmuxStateAheadFg,
		GitBehind:    TmuxStateBehindFg,

		// statusbar decoration: cwd is its OWN Tier C role (independent of
		// state.progress). github/generic-git decorations reuse GitAhead/
		// GitStaged above, so only cwd/gitlab carry their own literal here.
		DecorationCwd:    TmuxDecorationCwdFg,
		DecorationGitLab: TmuxDecorationGitLabFg,

		// kube: tmux named colors carried verbatim (Tier C).
		KubeContext:   TmuxKubeContextFg,
		KubeNamespace: TmuxKubeNamespaceFg,

		// identity/action chrome: Tier C renderer-only literals.
		IdentityBg: TmuxIdentityBg,
		IdentityFg: TmuxIdentityFg,
		ActionBg:   TmuxActionBg,
		ActionFg:   TmuxActionFg,

		// divider: Tier C renderer-only literal.
		DividerFg: TmuxDividerFg,
	}
}

// TmuxRenderTokens adapts the resolver's semantic colors to the tmux style roles
// currently used by generated status/window chrome. Explicit theme values use
// exact #RRGGBB colors; fallback values keep historical colourN literals. It is
// a stable subset view over RenderRoles kept for existing consumers.
type TmuxRenderTokens struct {
	WindowInactiveBg string
	WindowInactiveFg string
	WindowActiveBg   string
	WindowActiveFg   string
	StatusBg         string
	StatusFg         string
}

// TmuxRenderTokensFromEffective maps an EffectiveTheme into tmux style tokens.
// Fallback-sourced fields keep their role-map values. It is built on top of the
// role map so the two views never diverge.
func TmuxRenderTokensFromEffective(effective EffectiveTheme) TmuxRenderTokens {
	roles := RenderRolesFromEffective(effective)
	return TmuxRenderTokens{
		WindowInactiveBg: roles.WindowInactiveBg,
		WindowInactiveFg: roles.WindowInactiveFg,
		WindowActiveBg:   roles.WindowActiveBg,
		WindowActiveFg:   roles.WindowActiveFg,
		StatusBg:         roles.StatusBg,
		StatusFg:         roles.StatusFg,
	}
}

// Fields returns every effective field with its source label in stable order.
func (t EffectiveTheme) Fields() []EffectiveField {
	return []EffectiveField{
		{Name: "preset", Value: t.Preset.Value, Source: t.Preset.Source},
		{Name: string(TokenBackground), Value: t.Background.Value.Hex, Source: t.Background.Source},
		{Name: string(TokenSurface), Value: t.Surface.Value.Hex, Source: t.Surface.Source},
		{Name: string(TokenStatusBackground), Value: t.StatusBackground.Value.Hex, Source: t.StatusBackground.Source},
		{Name: string(TokenSurfaceActive), Value: t.SurfaceActive.Value.Hex, Source: t.SurfaceActive.Source},
		{Name: string(TokenChromeForeground), Value: t.ChromeForeground.Value.Hex, Source: t.ChromeForeground.Source},
		{Name: string(TokenTextPrimary), Value: t.TextPrimary.Value.Hex, Source: t.TextPrimary.Source},
		{Name: string(TokenForeground), Value: t.Foreground.Value.Hex, Source: t.Foreground.Source},
		{Name: string(TokenMuted), Value: t.Muted.Value.Hex, Source: t.Muted.Source},
		{Name: string(TokenAccent), Value: t.Accent.Value.Hex, Source: t.Accent.Source},
		{Name: string(TokenCritical), Value: t.Critical.Value.Hex, Source: t.Critical.Source},
		{Name: string(TokenWarning), Value: t.Warning.Value.Hex, Source: t.Warning.Source},
		{Name: string(TokenProgress), Value: t.Progress.Value.Hex, Source: t.Progress.Source},
		{Name: string(TokenSuccess), Value: t.Success.Value.Hex, Source: t.Success.Source},
		{Name: string(TokenActionRequired), Value: t.ActionRequired.Value.Hex, Source: t.ActionRequired.Source},
		{Name: string(TokenPaneActiveBg), Value: t.PaneActiveBg.Value.Hex, Source: t.PaneActiveBg.Source},
		{Name: string(TokenFocus), Value: t.Focus.Value.Hex, Source: t.Focus.Source},
	}
}

func tmuxColorOrFallback(field ColorField, fallback string) string {
	if IsThemeDefaultSpec(field.Value) {
		return ThemeDefaultSentinel
	}
	if field.Source == SourceFallback {
		return fallback
	}
	if hex := strings.TrimSpace(field.Value.Hex); hex != "" {
		return hex
	}
	if strings.TrimSpace(field.Value.Tmux) == "" {
		return fallback
	}
	return field.Value.Tmux
}

// tmuxContrastFgOrFallback implements the Tier B contrastFg transform: for an
// explicit theme it picks a dark (colour16) or light (colour231) foreground by
// the background's luminance; for a fallback theme it returns the historical
// literal verbatim so generated config stays byte-identical.
func tmuxContrastFgOrFallback(bg ColorField, fallback string) string {
	if bg.Source == SourceFallback || strings.TrimSpace(bg.Value.Hex) == "" {
		return fallback
	}
	r, g, b, ok := parseHexRGB(bg.Value.Hex)
	if !ok {
		return fallback
	}
	// Rec. 601 luma; threshold mid-range -> dark fg on light bg, light on dark.
	if 0.299*float64(r)+0.587*float64(g)+0.114*float64(b) >= 140 {
		return "colour16"
	}
	return "colour231"
}

type preset struct {
	Name   string
	Colors map[ColorToken]ColorSpec
}

var builtinPresets = map[string]preset{
	"projmux": {
		Name: "projmux",
		Colors: map[ColorToken]ColorSpec{
			TokenBackground:       {Tmux: ThemeDefaultSentinel},
			TokenSurface:          {Tmux: ThemeDefaultSentinel},
			TokenStatusBackground: {Hex: "#182226", Tmux: TmuxWindowInactiveBg},
			TokenSurfaceActive:    {Hex: "#2c383d", Tmux: TmuxWindowActiveBg},
			TokenChromeForeground: {Hex: "#d8e0e4", Tmux: TmuxPrimaryFg},
			TokenTextPrimary:      {Hex: "#d8e0e4", Tmux: TmuxPrimaryFg},
			TokenForeground:       {Hex: "#d8e0e4", Tmux: TmuxPrimaryFg},
			TokenMuted:            {Hex: "#75848c", Tmux: TmuxMutedFg},
			TokenAccent:           {Hex: "#7ac7ad", Tmux: TmuxActionBg},
			TokenCritical:         {Hex: "#ff6b6b", Tmux: TmuxStateCriticalFg},
			TokenWarning:          {Hex: "#ffcc66", Tmux: TmuxStateProgressFg},
			TokenProgress:         {Hex: "#ffcc66", Tmux: TmuxStateProgressFg},
			TokenSuccess:          {Hex: "#5faf87", Tmux: TmuxStateSuccessFg},
			TokenActionRequired:   {Hex: "#ffaf00", Tmux: TmuxAIBadgeActionRequiredFg},
			TokenPaneActiveBg:     {Tmux: ThemeDefaultSentinel},
			TokenFocus:            {Hex: "#00ffff", Tmux: TmuxPaneActiveBorderFg},
		},
	},
	"blue-hour": {
		Name: "blue-hour",
		Colors: presetColors(map[ColorToken]string{
			TokenBackground: "#1e1e2e", TokenSurface: "#16242d", TokenStatusBackground: "#132b38", TokenSurfaceActive: "#4a5878",
			TokenChromeForeground: "#acb6bf", TokenTextPrimary: "#acb6bf", TokenForeground: "#acb6bf", TokenMuted: "#4a5878", TokenAccent: "#3d8fd1",
			TokenCritical: "#ec6a88", TokenWarning: "#efb472",
			TokenProgress: "#5ca7e4", TokenSuccess: "#3fdaa4", TokenActionRequired: "#ffca85",
			TokenPaneActiveBg: "#000000", TokenFocus: "#5ca7e4",
		}),
	},
	"carbon-violet": {
		Name: "carbon-violet",
		Colors: presetColors(map[ColorToken]string{
			TokenBackground: "#1f2026", TokenSurface: "#23212b", TokenStatusBackground: "#1a1822", TokenSurfaceActive: "#3a3b44",
			TokenChromeForeground: "#d6d3df", TokenTextPrimary: "#d6d3df", TokenForeground: "#d6d3df", TokenMuted: "#8d8996", TokenAccent: "#b48ead",
			TokenCritical: "#ff5f5f", TokenWarning: "#d7af5f",
			TokenProgress: "#87afff", TokenSuccess: "#87af5f", TokenActionRequired: "#ff8700",
			TokenPaneActiveBg: "#000000", TokenFocus: "#b48ead",
		}),
	},
	"ember": {
		Name: "ember",
		Colors: presetColors(map[ColorToken]string{
			TokenBackground: "#211816", TokenSurface: "#2b201c", TokenSurfaceActive: "#473026",
			TokenChromeForeground: "#f3e4d7", TokenTextPrimary: "#f3e4d7", TokenForeground: "#f3e4d7", TokenMuted: "#b09282", TokenAccent: "#ff9f5f",
			TokenCritical: "#ff5f5f", TokenWarning: "#d7af00",
			TokenProgress: "#ffd75f", TokenSuccess: "#87af5f", TokenActionRequired: "#ff8700",
			TokenPaneActiveBg: "#1c1c1c", TokenFocus: "#ffaf5f",
		}),
	},
	"forest": {
		Name: "forest",
		Colors: presetColors(map[ColorToken]string{
			TokenBackground: "#14201a", TokenSurface: "#1b2b22", TokenSurfaceActive: "#2b4335",
			TokenChromeForeground: "#e0ebe4", TokenTextPrimary: "#e0ebe4", TokenForeground: "#e0ebe4", TokenMuted: "#8fa196", TokenAccent: "#9bcf8f",
			TokenCritical: "#ff7a70", TokenWarning: "#e5c45f",
			TokenProgress: "#ffcc66", TokenSuccess: "#5faf87", TokenActionRequired: "#ffaf00",
			TokenPaneActiveBg: "#1c1c1c", TokenFocus: "#00ffff",
		}),
	},
	"rose": {
		Name: "rose",
		Colors: presetColors(map[ColorToken]string{
			TokenBackground: "#20151c", TokenSurface: "#2b1d27", TokenSurfaceActive: "#412b3a",
			TokenChromeForeground: "#f0e3ea", TokenTextPrimary: "#f0e3ea", TokenForeground: "#f0e3ea", TokenMuted: "#aa8d9c", TokenAccent: "#e12672",
			TokenCritical: "#ff6b6b", TokenWarning: "#f0c36a",
			TokenProgress: "#ffcc66", TokenSuccess: "#5faf87", TokenActionRequired: "#ffaf00",
			TokenPaneActiveBg: "#1c1c1c", TokenFocus: "#00ffff",
		}),
	},
	"high-contrast": {
		Name: "high-contrast",
		Colors: presetColors(map[ColorToken]string{
			TokenBackground: "#000000", TokenSurface: "#000000", TokenSurfaceActive: "#005fff",
			TokenChromeForeground: "#ffffff", TokenTextPrimary: "#ffffff", TokenForeground: "#ffffff", TokenMuted: "#bfbfbf", TokenAccent: "#00ffff",
			TokenCritical: "#ff0000", TokenWarning: "#ffff00",
			TokenProgress: "#00bfff", TokenSuccess: "#00ff66", TokenActionRequired: "#ffaf00",
			TokenPaneActiveBg: "#080808", TokenFocus: "#00ffff",
		}),
	},
}

// PresetNames returns the built-in preset config values in stable order.
func PresetNames() []string {
	names := make([]string, 0, len(builtinPresets))
	for name := range builtinPresets {
		names = append(names, name)
	}
	sort.Strings(names)
	priority := []string{"projmux", "high-contrast", "blue-hour", "carbon-violet"}
	out := make([]string, 0, len(names))
	for _, name := range priority {
		if i := slices.Index(names, name); i >= 0 {
			out = append(out, name)
			names = append(names[:i], names[i+1:]...)
		}
	}
	return append(out, names...)
}

// PresetColorHex returns the truecolor hex value for a built-in preset token.
func PresetColorHex(name string, token ColorToken) (string, bool) {
	p, ok := builtinPresetByName(name)
	if !ok {
		return "", false
	}
	color, ok := p.Colors[token]
	if !ok {
		return "", false
	}
	return color.Hex, true
}

// NormalizeHexColor trims and normalizes a #RRGGBB value for storage.
func NormalizeHexColor(value string) (string, bool) {
	return normalizeHexColor(value)
}

// ResolveTheme computes global > built-in fallback effective theme values.
// Theme is a global user preference; project-local [theme] is intentionally
// not a source. The fallback preset fills any token the global theme leaves
// unset.
func ResolveTheme(global ThemeConfig) EffectiveTheme {
	layers := []layerInput{
		{source: SourceGlobal, config: global},
		{source: SourceFallback, config: ThemeConfig{Preset: "projmux"}},
	}

	valid := make([]resolvedLayer, 0, len(layers))
	var warnings []Warning
	for _, layer := range layers {
		if layer.source != SourceFallback && !layer.config.HasContent() {
			continue
		}
		resolved, layerWarnings, ok := resolveLayer(layer)
		warnings = append(warnings, layerWarnings...)
		if ok {
			valid = append(valid, resolved)
		}
	}

	result := EffectiveTheme{Warnings: warnings}
	result.Preset = resolveString(valid, func(l resolvedLayer) (string, bool) { return l.preset, l.preset != "" })
	result.Background = resolveColor(valid, TokenBackground)
	result.Surface = resolveColor(valid, TokenSurface)
	result.StatusBackground = resolveColor(valid, TokenStatusBackground)
	result.SurfaceActive = resolveColor(valid, TokenSurfaceActive)
	result.ChromeForeground = resolveColor(valid, TokenChromeForeground)
	result.TextPrimary = resolveColor(valid, TokenTextPrimary)
	result.Foreground = resolveColor(valid, TokenForeground)
	result.Muted = resolveColor(valid, TokenMuted)
	result.Accent = resolveColor(valid, TokenAccent)
	result.Critical = resolveColor(valid, TokenCritical)
	result.Warning = resolveColor(valid, TokenWarning)
	result.Progress = resolveColor(valid, TokenProgress)
	result.Success = resolveColor(valid, TokenSuccess)
	result.ActionRequired = resolveColor(valid, TokenActionRequired)
	result.PaneActiveBg = resolveColor(valid, TokenPaneActiveBg)
	result.Focus = resolveColor(valid, TokenFocus)
	return result
}

type layerInput struct {
	source Source
	config ThemeConfig
}

type resolvedLayer struct {
	source Source
	preset string
	colors map[ColorToken]ColorSpec
}

func resolveLayer(input layerInput) (resolvedLayer, []Warning, bool) {
	cfg := input.config
	cfg.Normalize()
	var warnings []Warning
	presetName := ""
	colors := map[ColorToken]ColorSpec{}
	if hasThemeValue(cfg.Preset) {
		name := strings.ToLower(cfg.Preset)
		p, ok := builtinPresetByName(name)
		if !ok {
			return resolvedLayer{}, []Warning{{
				Source: input.source, Field: "preset", Value: cfg.Preset,
				Message: "unknown theme preset; ignored this theme layer",
			}}, false
		}
		presetName = p.Name
		maps.Copy(colors, p.Colors)
	}

	explicit := map[ColorToken]ColorSpec{}
	for _, item := range []struct {
		token ColorToken
		value string
	}{
		{TokenBackground, cfg.Background},
		{TokenSurface, cfg.Surface},
		{TokenStatusBackground, cfg.StatusBackground},
		{TokenSurfaceActive, cfg.SurfaceActive},
		{TokenChromeForeground, cfg.ChromeForeground},
		{TokenTextPrimary, cfg.TextPrimary},
		{TokenForeground, cfg.Foreground},
		{TokenMuted, cfg.Muted},
		{TokenAccent, cfg.Accent},
		{TokenCritical, cfg.Critical},
		{TokenWarning, cfg.Warning},
		{TokenProgress, cfg.Progress},
		{TokenSuccess, cfg.Success},
		{TokenActionRequired, cfg.ActionRequired},
		{TokenPaneActiveBg, cfg.PaneActiveBg},
		{TokenFocus, cfg.Focus},
	} {
		if !hasThemeValue(item.value) {
			continue
		}
		// "terminal default" sentinel: terminal-backed background tokens support
		// pinning their background to the terminal default. This is treated as a
		// real explicit value so it overrides the preset fill within the global
		// layer (explicit default > preset). It must be intercepted BEFORE
		// normalizeHexColor, which would reject "default" and drop the whole
		// layer. A ColorSpec with no Hex and Tmux="default" makes tmux roles emit
		// `bg=default` and ANSI surfaces fall back to the terminal background (no
		// 48;2 / 48;5 sequence).
		if isThemeDefaultSentinel(item.value) {
			if tokenSupportsDefaultSentinel(item.token) {
				spec := ColorSpec{Tmux: ThemeDefaultSentinel}
				colors[item.token] = spec
				explicit[item.token] = spec
				continue
			}
			warnings = append(warnings, Warning{
				Source: input.source, Field: string(item.token), Value: item.value,
				Message: "terminal default is only valid for background-like tokens; ignored this theme layer",
			})
			return resolvedLayer{}, warnings, false
		}
		hex, ok := normalizeHexColor(item.value)
		if !ok {
			warnings = append(warnings, Warning{
				Source: input.source, Field: string(item.token), Value: item.value,
				Message: "invalid hex color; ignored this theme layer",
			})
			return resolvedLayer{}, warnings, false
		}
		spec := ColorSpec{Hex: hex, Tmux: nearestTmuxColor(hex)}
		colors[item.token] = spec
		explicit[item.token] = spec
	}
	applyForegroundAliases(colors, explicit)

	layer := resolvedLayer{source: input.source, preset: presetName, colors: colors}
	return layer, nil, true
}

func builtinPresetByName(name string) (preset, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	p, ok := builtinPresets[name]
	return p, ok
}

func applyForegroundAliases(colors, explicit map[ColorToken]ColorSpec) {
	if legacy, ok := explicit[TokenForeground]; ok {
		if _, set := explicit[TokenChromeForeground]; !set {
			colors[TokenChromeForeground] = legacy
		}
		if _, set := explicit[TokenTextPrimary]; !set {
			colors[TokenTextPrimary] = legacy
		}
	}
	if _, ok := colors[TokenForeground]; ok {
		return
	}
	if text, ok := colors[TokenTextPrimary]; ok {
		colors[TokenForeground] = text
		return
	}
	if chrome, ok := colors[TokenChromeForeground]; ok {
		colors[TokenForeground] = chrome
	}
}

func resolveColor(layers []resolvedLayer, token ColorToken) ColorField {
	for _, layer := range layers {
		if value, ok := layer.colors[token]; ok {
			return ColorField{Value: value, Source: layer.source}
		}
	}
	return ColorField{}
}

func resolveString(layers []resolvedLayer, value func(resolvedLayer) (string, bool)) StringField {
	for _, layer := range layers {
		if v, ok := value(layer); ok {
			return StringField{Value: v, Source: layer.source}
		}
	}
	return StringField{Source: SourceFallback}
}

func hasThemeValue(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && strings.EqualFold(value, "inherit") == false
}

// ThemeDefaultSentinel is the explicit "terminal default" value a user can set
// on terminal-backed background tokens. It pins that role's background to the
// terminal default even when a preset is chosen (explicit default > preset fill
// > unset/fallback). On the tmux side it surfaces as `bg=default`; on the ANSI
// side it produces no background sequence so the terminal background shows
// through.
const ThemeDefaultSentinel = "default"

// isThemeDefaultSentinel reports whether value is the terminal-default sentinel.
func isThemeDefaultSentinel(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), ThemeDefaultSentinel)
}

// tokenSupportsDefaultSentinel restricts the "default" sentinel to tokens that
// paint terminal-backed backgrounds. Other tokens treat "default" as an invalid
// hex.
func tokenSupportsDefaultSentinel(token ColorToken) bool {
	switch token {
	case TokenBackground, TokenSurface, TokenStatusBackground, TokenSurfaceActive, TokenPaneActiveBg:
		return true
	default:
		return false
	}
}

// IsThemeDefaultSpec reports whether a resolved ColorSpec is the terminal-default
// sentinel (no Hex, Tmux == "default"). Renderers use this to emit a terminal
// reset / `bg=default` instead of a concrete color.
func IsThemeDefaultSpec(spec ColorSpec) bool {
	return strings.TrimSpace(spec.Hex) == "" && spec.Tmux == ThemeDefaultSentinel
}

// TokenSupportsDefaultSentinel is the exported predicate Settings uses to decide
// whether to offer the "Terminal default" choice for a token.
func TokenSupportsDefaultSentinel(token ColorToken) bool {
	return tokenSupportsDefaultSentinel(token)
}

var hexColorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func normalizeHexColor(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if !hexColorPattern.MatchString(value) {
		return "", false
	}
	return strings.ToLower(value), true
}

func parseHexRGB(hex string) (int, int, int, bool) {
	hex, ok := normalizeHexColor(hex)
	if !ok {
		return 0, 0, 0, false
	}
	r, _ := strconv.ParseInt(hex[1:3], 16, 0)
	g, _ := strconv.ParseInt(hex[3:5], 16, 0)
	b, _ := strconv.ParseInt(hex[5:7], 16, 0)
	return int(r), int(g), int(b), true
}

func presetColors(values map[ColorToken]string) map[ColorToken]ColorSpec {
	out := map[ColorToken]ColorSpec{}
	for token, hex := range values {
		normalized, ok := normalizeHexColor(hex)
		if !ok {
			continue
		}
		out[token] = ColorSpec{Hex: normalized, Tmux: nearestTmuxColor(normalized)}
	}
	return fillPresetStatusBackground(out)
}

func fillPresetStatusBackground(colors map[ColorToken]ColorSpec) map[ColorToken]ColorSpec {
	if _, ok := colors[TokenStatusBackground]; ok {
		return colors
	}
	if surface, ok := colors[TokenSurface]; ok {
		colors[TokenStatusBackground] = surface
	}
	return colors
}

func nearestTmuxColor(hex string) string {
	r, g, b, ok := parseHexRGB(hex)
	if !ok {
		return ""
	}
	bestIndex := 0
	bestDistance := math.MaxFloat64
	for i := range 256 {
		pr, pg, pb := xterm256RGB(i)
		d := colorDistance(r, g, b, pr, pg, pb)
		if d < bestDistance {
			bestDistance = d
			bestIndex = i
		}
	}
	return "colour" + strconv.Itoa(bestIndex)
}

func xterm256RGB(index int) (int, int, int) {
	base := [16][3]int{
		{0, 0, 0}, {128, 0, 0}, {0, 128, 0}, {128, 128, 0},
		{0, 0, 128}, {128, 0, 128}, {0, 128, 128}, {192, 192, 192},
		{128, 128, 128}, {255, 0, 0}, {0, 255, 0}, {255, 255, 0},
		{0, 0, 255}, {255, 0, 255}, {0, 255, 255}, {255, 255, 255},
	}
	if index < 16 {
		c := base[index]
		return c[0], c[1], c[2]
	}
	if index < 232 {
		n := index - 16
		steps := [6]int{0, 95, 135, 175, 215, 255}
		return steps[n/36], steps[(n/6)%6], steps[n%6]
	}
	gray := 8 + (index-232)*10
	return gray, gray, gray
}

func colorDistance(r1, g1, b1, r2, g2, b2 int) float64 {
	dr := float64(r1 - r2)
	dg := float64(g1 - g2)
	db := float64(b1 - b2)
	return dr*dr + dg*dg + db*db
}
