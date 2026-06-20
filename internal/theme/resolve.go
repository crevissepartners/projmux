package theme

import (
	"fmt"
	"maps"
	"math"
	"regexp"
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
	TokenBackground    ColorToken = "background"
	TokenSurface       ColorToken = "surface"
	TokenSurfaceActive ColorToken = "surface_active"
	TokenForeground    ColorToken = "foreground"
	TokenMuted         ColorToken = "muted"
	TokenAccent        ColorToken = "accent"
	TokenCritical      ColorToken = "critical"
	TokenWarning       ColorToken = "warning"
)

// ResolverColorTokens is the stable display/serialization order for theme
// color fields.
var ResolverColorTokens = []ColorToken{
	TokenBackground,
	TokenSurface,
	TokenSurfaceActive,
	TokenForeground,
	TokenMuted,
	TokenAccent,
	TokenCritical,
	TokenWarning,
}

// ThemeConfig is the user-configurable theme section from global or project
// config.toml. Values are intentionally raw: validation belongs to the
// resolver so an invalid layer can warn and fall through to the next source.
type ThemeConfig struct {
	Preset        string
	Background    string
	Surface       string
	SurfaceActive string
	Foreground    string
	Muted         string
	Accent        string
	Critical      string
	Warning       string
}

// HasContent reports whether the config carries any theme override.
func (c ThemeConfig) HasContent() bool {
	for _, value := range []string{
		c.Preset,
		c.Background,
		c.Surface,
		c.SurfaceActive,
		c.Foreground,
		c.Muted,
		c.Accent,
		c.Critical,
		c.Warning,
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
	c.SurfaceActive = strings.TrimSpace(c.SurfaceActive)
	c.Foreground = strings.TrimSpace(c.Foreground)
	c.Muted = strings.TrimSpace(c.Muted)
	c.Accent = strings.TrimSpace(c.Accent)
	c.Critical = strings.TrimSpace(c.Critical)
	c.Warning = strings.TrimSpace(c.Warning)
}

// ColorSpec carries both terminal-native truecolor and tmux 256-color forms.
// Explicit hex values keep exact truecolor and are approximated for tmux.
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
	Preset        StringField
	Background    ColorField
	Surface       ColorField
	SurfaceActive ColorField
	Foreground    ColorField
	Muted         ColorField
	Accent        ColorField
	Critical      ColorField
	Warning       ColorField
	Warnings      []Warning
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
	WindowInactiveFg string // text on inactive    <- foreground      (colour245 fallback)
	WindowActiveBg   string // surface.active      <- surface_active  (colour240)
	WindowActiveFg   string // text on active      <- foreground      (colour231 fallback)
	StatusBg         string // status bar bg       <- background      (colour235)
	StatusFg         string // status bar fg       <- foreground      (colour245 fallback)

	// pane / focus chrome.
	PaneBorder string // pane.border          Tier A <- muted-ish (colour236)
	// focus.border — dedicated role decoupled from accent. Tier C renderer-only:
	// carries the literal colour51, independently tunable from the topic chip /
	// pointer / action that still share accent. Fallback colour51 (no visual
	// change). Phase 6 public-token candidate (focus / border).
	FocusBorder     string
	PaneTopicChipBg string // pane.topic_chip_bg   Tier A <- accent    (colour45)
	PaneTopicChipFg string // pane.topic_chip_fg   Tier B <- contrastFg (colour16)

	// focus.pane_active_bg — active-pane window-active-style tint. Decoupled from
	// surface_active: a dedicated Tier C renderer-only DARK tint one tone darker
	// than surface.base, carrying the literal colour234 (fallback colour234).
	// surface_active is a LIGHT tone (colour240) and was the wrong direction.
	// Phase 6 public-token candidate (pane_active_bg / surface_subtle).
	FocusPaneActiveBg string

	// state / severity cluster (Phase 3). Single source for the notify HUD,
	// usage HUD, and statusbar severity tints on the tmux side.
	StateWarning  string // state.warning  Tier A <- warning   (colour214)
	StateCritical string // state.critical Tier A <- critical  (colour160)
	StateProgress string // state.progress Tier C renderer-only (colour220) — Phase 6 candidate, carries literal
	StateSuccess  string // state.success  Tier C renderer-only (colour72)  — Phase 6 candidate, carries literal

	// AI-status cluster (Phase 3). One logical color per AI badge role.
	// AIProgress/AISuccess reuse the state.progress/success colors; action
	// required is kept as its OWN role and must NEVER merge into critical.
	AIProgress       string // ai.progress        <- StateProgress (colour220)
	AISuccess        string // ai.success         <- StateSuccess  (colour72)
	AIActionRequired string // ai.action_required Tier C renderer-only (colour214) — Phase 6 candidate; independent of critical

	// statusbar git segment cluster (Phase 4). The segment foreground is the
	// only foreground-derived role here; the segment bg and the staged/dirty/
	// ahead/behind state colors are renderer-only literals carried verbatim.
	GitSegmentFg string // git.segment_fg Tier A <- foreground (colour231 fallback)
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
// theme. Fallback-sourced roles deliberately reproduce the historical palette
// literals so generated fallback config stays byte-identical.
func RenderRolesFromEffective(effective EffectiveTheme) RenderRoles {
	return RenderRoles{
		WindowInactiveBg: tmuxColorOrFallback(effective.Background, TmuxWindowInactiveBg),
		WindowInactiveFg: tmuxColorOrFallback(effective.Foreground, TmuxWindowInactiveFg),
		WindowActiveBg:   tmuxColorOrFallback(effective.SurfaceActive, TmuxWindowActiveBg),
		WindowActiveFg:   tmuxColorOrFallback(effective.Foreground, TmuxWindowActiveFg),
		StatusBg:         tmuxColorOrFallback(effective.Background, TmuxWindowInactiveBg),
		StatusFg:         tmuxColorOrFallback(effective.Foreground, TmuxWindowInactiveFg),

		PaneBorder: tmuxColorOrFallback(effective.Muted, TmuxPaneBorderFg),
		// Tier C: focus.border decoupled from accent — carries the literal so the
		// active-pane border is tunable independently of the topic chip / pointer
		// / action that still derive from accent. No derivation; colour51.
		FocusBorder:     TmuxPaneActiveBorderFg,
		PaneTopicChipBg: tmuxColorOrFallback(effective.Accent, TmuxPaneActiveBg),
		PaneTopicChipFg: tmuxContrastFgOrFallback(effective.Accent, TmuxPaneActiveFg),

		// Tier C: dedicated dark active-pane tint, decoupled from surface_active.
		// Carries the literal colour234 (one tone darker than surface.base) so the
		// active pane is visibly tinted; no derivation until Phase 6.
		FocusPaneActiveBg: TmuxPaneActiveTintBg,

		// Tier A: warning/critical follow the explicit public token so an
		// explicit theme repaints the notify/usage/statusbar severity tints.
		StateWarning:  tmuxColorOrFallback(effective.Warning, TmuxStateWarningFg),
		StateCritical: tmuxColorOrFallback(effective.Critical, TmuxStateCriticalFg),
		// Tier C: renderer-only state colors with no public token yet. The
		// literal is carried under the role; no derivation until Phase 6.
		StateProgress: TmuxStateProgressFg,
		StateSuccess:  TmuxStateSuccessFg,

		// AI-status: progress/success reuse the state colors (same logical
		// color). action_required is its OWN Tier C role — keep it independent
		// of StateCritical; never merge.
		AIProgress:       TmuxStateProgressFg,
		AISuccess:        TmuxStateSuccessFg,
		AIActionRequired: TmuxAIBadgeActionRequiredFg,

		// statusbar git segment: only the segment fg follows the public
		// foreground token (Tier A). The segment bg and state colors are
		// Tier C renderer-only literals carried verbatim.
		GitSegmentFg: tmuxColorOrFallback(effective.Foreground, TmuxGitSegmentFg),
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

// TmuxRenderTokens adapts the resolver's semantic colors to the tmux 256-color
// roles currently used by generated status/window chrome. It is a stable subset
// view over RenderRoles kept for existing consumers.
type TmuxRenderTokens struct {
	WindowInactiveBg string
	WindowInactiveFg string
	WindowActiveBg   string
	WindowActiveFg   string
	StatusBg         string
	StatusFg         string
}

// TmuxRenderTokensFromEffective maps an EffectiveTheme into tmux colourN
// tokens. Fallback-sourced fields deliberately keep the historical palette
// constants so generated fallback config remains byte-identical. It is built on
// top of the role map so the two views never diverge.
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
		{Name: string(TokenSurfaceActive), Value: t.SurfaceActive.Value.Hex, Source: t.SurfaceActive.Source},
		{Name: string(TokenForeground), Value: t.Foreground.Value.Hex, Source: t.Foreground.Source},
		{Name: string(TokenMuted), Value: t.Muted.Value.Hex, Source: t.Muted.Source},
		{Name: string(TokenAccent), Value: t.Accent.Value.Hex, Source: t.Accent.Source},
		{Name: string(TokenCritical), Value: t.Critical.Value.Hex, Source: t.Critical.Source},
		{Name: string(TokenWarning), Value: t.Warning.Value.Hex, Source: t.Warning.Source},
	}
}

func tmuxColorOrFallback(field ColorField, fallback string) string {
	if field.Source == SourceFallback || strings.TrimSpace(field.Value.Tmux) == "" {
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
	"projmux-dark": {
		Name: "projmux-dark",
		Colors: map[ColorToken]ColorSpec{
			TokenBackground:    {Hex: "#182226", Tmux: TmuxWindowInactiveBg},
			TokenSurface:       {Hex: "#182226", Tmux: TmuxWindowInactiveBg},
			TokenSurfaceActive: {Hex: "#2c383d", Tmux: TmuxWindowActiveBg},
			TokenForeground:    {Hex: "#d8e0e4", Tmux: TmuxPrimaryFg},
			TokenMuted:         {Hex: "#75848c", Tmux: TmuxMutedFg},
			TokenAccent:        {Hex: "#7ac7ad", Tmux: TmuxActionBg},
			TokenCritical:      {Hex: "#ff6b6b", Tmux: TmuxStateCriticalFg},
			TokenWarning:       {Hex: "#ffcc66", Tmux: TmuxStateProgressFg},
		},
	},
	"midnight": {
		Name: "midnight",
		Colors: presetColors(map[ColorToken]string{
			TokenBackground: "#101820", TokenSurface: "#16242d", TokenSurfaceActive: "#253844",
			TokenForeground: "#e7eef2", TokenMuted: "#8296a1", TokenAccent: "#7bd3c6",
			TokenCritical: "#ff6b7a", TokenWarning: "#ffd166",
		}),
	},
	"forest": {
		Name: "forest",
		Colors: presetColors(map[ColorToken]string{
			TokenBackground: "#14201a", TokenSurface: "#1b2b22", TokenSurfaceActive: "#2b4335",
			TokenForeground: "#e0ebe4", TokenMuted: "#8fa196", TokenAccent: "#9bcf8f",
			TokenCritical: "#ff7a70", TokenWarning: "#e5c45f",
		}),
	},
	"rose": {
		Name: "rose",
		Colors: presetColors(map[ColorToken]string{
			TokenBackground: "#20151c", TokenSurface: "#2b1d27", TokenSurfaceActive: "#412b3a",
			TokenForeground: "#f0e3ea", TokenMuted: "#aa8d9c", TokenAccent: "#e12672",
			TokenCritical: "#ff6b6b", TokenWarning: "#f0c36a",
		}),
	},
	"high-contrast": {
		Name: "high-contrast",
		Colors: presetColors(map[ColorToken]string{
			TokenBackground: "#000000", TokenSurface: "#101010", TokenSurfaceActive: "#303030",
			TokenForeground: "#ffffff", TokenMuted: "#b8b8b8", TokenAccent: "#00ffd0",
			TokenCritical: "#ff4040", TokenWarning: "#ffd700",
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
	return names
}

// PresetColorHex returns the truecolor hex value for a built-in preset token.
func PresetColorHex(name string, token ColorToken) (string, bool) {
	p, ok := builtinPresets[strings.ToLower(strings.TrimSpace(name))]
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
		{source: SourceFallback, config: ThemeConfig{Preset: "projmux-dark"}},
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
	result.SurfaceActive = resolveColor(valid, TokenSurfaceActive)
	result.Foreground = resolveColor(valid, TokenForeground)
	result.Muted = resolveColor(valid, TokenMuted)
	result.Accent = resolveColor(valid, TokenAccent)
	result.Critical = resolveColor(valid, TokenCritical)
	result.Warning = resolveColor(valid, TokenWarning)
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
		p, ok := builtinPresets[name]
		if !ok {
			return resolvedLayer{}, []Warning{{
				Source: input.source, Field: "preset", Value: cfg.Preset,
				Message: "unknown theme preset; ignored this theme layer",
			}}, false
		}
		presetName = p.Name
		maps.Copy(colors, p.Colors)
	}

	for _, item := range []struct {
		token ColorToken
		value string
	}{
		{TokenBackground, cfg.Background},
		{TokenSurface, cfg.Surface},
		{TokenSurfaceActive, cfg.SurfaceActive},
		{TokenForeground, cfg.Foreground},
		{TokenMuted, cfg.Muted},
		{TokenAccent, cfg.Accent},
		{TokenCritical, cfg.Critical},
		{TokenWarning, cfg.Warning},
	} {
		if !hasThemeValue(item.value) {
			continue
		}
		hex, ok := normalizeHexColor(item.value)
		if !ok {
			warnings = append(warnings, Warning{
				Source: input.source, Field: string(item.token), Value: item.value,
				Message: "invalid hex color; ignored this theme layer",
			})
			return resolvedLayer{}, warnings, false
		}
		colors[item.token] = ColorSpec{Hex: hex, Tmux: nearestTmuxColor(hex)}
	}

	layer := resolvedLayer{source: input.source, preset: presetName, colors: colors}
	return layer, nil, true
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
	return out
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
