package app

import (
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	"github.com/crevissepartners/projmux/internal/theme"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// Theme is a global user preference. Settings only edits the global [theme] in
// ~/.config/projmux/config.toml and shows the resolved global > built-in
// fallback effective values. Project-local [theme] is deprecated migration
// data and is not editable or resolvable here.

// themeTokenGroup is a Settings presentation grouping for the color tokens.
type themeTokenGroup struct {
	Prefix string
	Tokens []theme.ColorToken
}

// themeTokenGroups orders the theme editor's color rows by how often users
// touch them and by semantic cluster, instead of the flat serialization order.
// It is a DISPLAY concern only — theme.ResolverColorTokens stays the stable
// serialization/storage order. Every editable non-legacy token must appear here
// exactly once; TestThemeTokenGroupsCoverEditableTokens guards that invariant.
var themeTokenGroups = []themeTokenGroup{
	{Prefix: "[core]", Tokens: []theme.ColorToken{
		theme.TokenBackground, theme.TokenTextPrimary, theme.TokenAccent,
	}},
	{Prefix: "[surface]", Tokens: []theme.ColorToken{
		theme.TokenSurface, theme.TokenStatusBackground, theme.TokenSurfaceActive, theme.TokenMuted,
	}},
	{Prefix: "[state]", Tokens: []theme.ColorToken{
		theme.TokenCritical, theme.TokenWarning, theme.TokenProgress, theme.TokenSuccess, theme.TokenActionRequired,
	}},
	{Prefix: "[chrome]", Tokens: []theme.ColorToken{
		theme.TokenChromeForeground, theme.TokenPaneActiveBg, theme.TokenFocus,
	}},
}

func (c *settingsCommand) runThemeSection(stdout, stderr io.Writer) error {
	for {
		options, err := c.themeOptions()
		if err != nil {
			return err
		}
		result, err := c.runPicker(options)
		if err != nil {
			return err
		}
		action := strings.TrimSpace(result.Value)
		if result.Key != "enter" || action == "" {
			return errSettingsClosed
		}
		switch {
		case action == settingsBackValue:
			return nil
		case action == settingsNoopValue:
			continue
		case action == themeAction("preset"):
			if err := c.runThemePresetSection(stdout, stderr); err != nil {
				return err
			}
		case strings.HasPrefix(action, themeAction("color:")):
			token, ok := parseThemeColorAction(strings.TrimPrefix(action, themeAction("color:")))
			if !ok {
				return fmt.Errorf("unknown theme color action: %s", action)
			}
			if err := c.runThemeColorSection(token, stdout, stderr); err != nil {
				return err
			}
		case action == themeAction("reset"):
			if err := c.resetTheme(stdout); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown theme settings action: %s", action)
		}
	}
}

func (c *settingsCommand) themeOptions() (intpickercompat.Options, error) {
	entries, err := c.themeEntries()
	if err != nil {
		return intpickercompat.Options{}, err
	}
	return intpickercompat.Options{
		UI:         "settings-theme-global",
		Entries:    entries,
		Title:      "Theme - Global values",
		TitleChips: settingsPassiveRootTabChipsLocale(settingsRootTabGlobal, c.resolveSettingsProjectContext().hasProject(), c.locale()),
		Prompt:     "Settings > Theme > Global > ",
		Footer:     projmuxFooter("Enter: open/apply  |  Back row: parent "),
		ExpectKeys: []string{"enter"},
		Bindings:   settingsCloseBindings(),
	}, nil
}

func (c *settingsCommand) currentGlobalProjectConfig() (hooks.ProjectConfig, error) {
	path, err := c.globalConfigPath()
	if err != nil {
		return hooks.ProjectConfig{}, err
	}
	return hooks.LoadGlobalConfig(path)
}

func (c *settingsCommand) themeEntries() ([]intpickercompat.Entry, error) {
	cfg, err := c.currentGlobalProjectConfig()
	if err != nil {
		return nil, err
	}
	entries := []intpickercompat.Entry{c.backEntry()}
	entries = append(entries, intpickercompat.Entry{
		Label:     c.rowLabel(settingsGlyphOpen, settingsColorType, "Preset selector", themePresetSummary(cfg.Theme)),
		Value:     themeAction("preset"),
		SearchKey: "theme preset selector swatch colors",
	})
	effective := theme.ResolveTheme(cfg.Theme)
	// Present the editable color tokens grouped by priority/meaning while keeping
	// the rows directly selectable. `foreground` remains parseable as a legacy
	// config key, but Settings steers users to text_primary/chrome_foreground.
	for _, group := range themeTokenGroups {
		for _, token := range group.Tokens {
			entries = append(entries, intpickercompat.Entry{
				Label:     c.rowLabel(settingsGlyphOpen, settingsColorType, themeColorLabel(token), group.Prefix+" "+themeColorSummaryEffective(cfg.Theme, effective, token)),
				Value:     themeAction("color:" + string(token)),
				SearchKey: "theme color " + group.Prefix + " " + strings.Trim(group.Prefix, "[]") + " swatch hex input " + string(token),
			})
		}
	}
	entries = append(entries,
		intpickercompat.Entry{
			Label: c.rowLabel(settingsGlyphRemove, settingsColorRemove, "Reset theme values", "remove only global theme values"),
			Value: themeAction("reset"),
		},
	)
	// Surface resolver warnings inline (the removed Effective theme view used to
	// show these). Dim info rows after the token rows.
	for _, warning := range effective.Warnings {
		entries = append(entries, intpickercompat.Entry{
			Label: c.rowLabelDim("Warning "+string(warning.Source)+" "+warning.Field, warning.Message),
			Value: settingsNoopValue,
		})
	}
	return entries, nil
}

func (c *settingsCommand) runThemePresetSection(stdout, stderr io.Writer) error {
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-theme-preset",
			Entries:    c.themePresetEntries(),
			Title:      "Theme - Preset selector",
			TitleChips: settingsPassiveRootTabChipsLocale(settingsRootTabGlobal, c.resolveSettingsProjectContext().hasProject(), c.locale()),
			Prompt:     "Settings > Theme > Global > Preset > ",
			Footer:     projmuxFooter("Enter: apply preset  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   settingsCloseBindings(),
		})
		if err != nil {
			return err
		}
		action := strings.TrimSpace(result.Value)
		if result.Key != "enter" || action == "" {
			return errSettingsClosed
		}
		switch {
		case action == settingsBackValue:
			return nil
		case action == settingsNoopValue:
			continue
		case strings.HasPrefix(action, themeAction("preset:set:")):
			preset := strings.TrimPrefix(action, themeAction("preset:set:"))
			if err := c.setThemePreset(preset, stdout); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown theme preset action: %s", action)
		}
	}
}

func (c *settingsCommand) themePresetEntries() []intpickercompat.Entry {
	cfg, _ := c.currentGlobalProjectConfig()
	current := strings.TrimSpace(cfg.Theme.Preset)
	entries := []intpickercompat.Entry{c.backEntry()}
	for _, preset := range theme.PresetNames() {
		glyph, color := settingsGlyphInactive, settingsColorDim
		if strings.EqualFold(current, preset) {
			glyph, color = settingsGlyphToggle, settingsColorAdd
		}
		entries = append(entries, intpickercompat.Entry{
			Label:     c.rowLabel(glyph, color, preset, themePresetSwatches(preset)),
			Value:     themeAction("preset:set:" + preset),
			SearchKey: "theme preset " + preset,
		})
	}
	entries = append(entries, intpickercompat.Entry{
		Label: c.rowLabel(settingsGlyphRemove, settingsColorRemove, "Clear global preset", "remove only global preset value"),
		Value: themeAction("preset:set:"),
	})
	return entries
}

func (c *settingsCommand) runThemeColorSection(token theme.ColorToken, stdout, stderr io.Writer) error {
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-theme-color",
			Entries:    c.themeColorEntries(token),
			Title:      "Theme - " + themeColorLabel(token),
			TitleChips: settingsPassiveRootTabChipsLocale(settingsRootTabGlobal, c.resolveSettingsProjectContext().hasProject(), c.locale()),
			Prompt:     "Settings > Theme > Global > " + themeColorLabel(token) + " > ",
			Footer:     projmuxFooter("Enter: apply swatch or open hex input  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   settingsCloseBindings(),
		})
		if err != nil {
			return err
		}
		action := strings.TrimSpace(result.Value)
		if result.Key != "enter" || action == "" {
			return errSettingsClosed
		}
		switch {
		case action == settingsBackValue:
			return nil
		case action == settingsNoopValue:
			continue
		case action == themeAction("color-type:"+string(token)):
			if err := c.runThemeColorHexInput(token, stdout, stderr); err != nil {
				return err
			}
		case action == themeAction("color-grid:"+string(token)):
			if err := c.runThemeColorGrid(token, stdout, stderr); err != nil {
				return err
			}
		case strings.HasPrefix(action, themeAction("color-set:"+string(token)+":")):
			value := strings.TrimPrefix(action, themeAction("color-set:"+string(token)+":"))
			if err := c.setThemeColor(token, value, stdout); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown theme color detail action: %s", action)
		}
	}
}

func (c *settingsCommand) themeColorEntries(token theme.ColorToken) []intpickercompat.Entry {
	cfg, _ := c.currentGlobalProjectConfig()
	entries := []intpickercompat.Entry{
		c.backEntry(),
		{Label: c.rowLabelInfo("Current", themeColorCurrentValue(cfg.Theme, token), themeColorSummary(cfg.Theme, token)), Value: settingsNoopValue},
	}
	if inheritedPreset := strings.TrimSpace(cfg.Theme.Preset); inheritedPreset != "" && !strings.EqualFold(inheritedPreset, "inherit") {
		entries = append(entries, intpickercompat.Entry{
			Label: c.rowLabel(settingsGlyphRemove, settingsColorRemove, "Use preset value", "clear explicit token override"),
			Value: themeAction("color-set:" + string(token) + ":"),
		})
	}
	entries = append(entries, intpickercompat.Entry{
		Label: c.rowLabel(settingsGlyphType, settingsColorType, "Type hex value...", "swatch + #RRGGBB input"),
		Value: themeAction("color-type:" + string(token)),
	})
	entries = append(entries, intpickercompat.Entry{
		Label:     c.rowLabel(settingsGlyphOpen, settingsColorType, "Pick from 256-color grid...", "browse swatches with a live preview"),
		Value:     themeAction("color-grid:" + string(token)),
		SearchKey: "theme color grid 256 swatch picker " + string(token),
	})
	if theme.TokenSupportsDefaultSentinel(token) {
		entries = append(entries, intpickercompat.Entry{
			Label:     c.rowLabel(settingsGlyphAdd, settingsColorAdd, "Terminal default", "keep "+themeColorLabel(token)+" at the terminal background"),
			Value:     themeAction("color-set:" + string(token) + ":" + theme.ThemeDefaultSentinel),
			SearchKey: "theme terminal default background " + string(token),
		})
	}
	for _, preset := range theme.PresetNames() {
		hex, ok := theme.PresetColorHex(preset, token)
		if !ok {
			continue
		}
		entries = append(entries, intpickercompat.Entry{
			Label:     c.rowLabel(settingsGlyphAdd, settingsColorAdd, "Set "+preset, themeSwatch(hex)+" "+hex),
			Value:     themeAction("color-set:" + string(token) + ":" + hex),
			SearchKey: "theme swatch " + preset + " " + string(token) + " " + hex,
		})
	}
	return entries
}

func (c *settingsCommand) runThemeColorHexInput(token theme.ColorToken, stdout, stderr io.Writer) error {
	cfg, _ := c.currentGlobalProjectConfig()
	result, err := c.runPicker(intpickercompat.Options{
		UI:           "settings-theme-color-hex",
		Entries:      []intpickercompat.Entry{{Label: c.rowLabelInfo("Swatch", themeSwatch(themeColorCurrentValue(cfg.Theme, token)), "current")}},
		AcceptQuery:  true,
		InitialQuery: themeColorInitialQuery(cfg.Theme, token),
		Title:        "Theme - " + themeColorLabel(token) + " hex input",
		Prompt:       themeColorLabel(token) + " hex > ",
		Footer:       projmuxFooter("Enter: save #RRGGBB "),
		ExpectKeys:   []string{"enter"},
		Bindings:     settingsCloseBindings(),
	})
	if err != nil {
		return err
	}
	if result.Key != "enter" {
		return nil
	}
	hex, ok := theme.NormalizeHexColor(result.Query)
	if !ok {
		fmt.Fprintf(stderr, "invalid theme color %q: use #RRGGBB\n", strings.TrimSpace(result.Query))
		return nil
	}
	return c.setThemeColor(token, hex, stdout)
}

func (c *settingsCommand) runThemeColorGrid(token theme.ColorToken, stdout, stderr io.Writer) error {
	result, err := c.runPicker(intpickercompat.Options{
		UI:        "settings-theme-color-grid",
		ColorGrid: true,
		Title:     "Theme - " + themeColorLabel(token) + " color grid",
		Header:    "Arrows: move  |  Enter: pick swatch  |  h: hex input",
		Footer:    projmuxFooter("Enter: pick swatch  |  h: hex input  |  Esc: back "),
		Bindings:  settingsCloseBindings(),
	})
	if err != nil {
		return err
	}
	switch result.Key {
	case "enter":
		hex, ok := theme.NormalizeHexColor(result.Value)
		if !ok {
			fmt.Fprintf(stderr, "invalid theme color %q: use #RRGGBB\n", strings.TrimSpace(result.Value))
			return nil
		}
		return c.setThemeColor(token, hex, stdout)
	case "hex":
		return c.runThemeColorHexInput(token, stdout, stderr)
	default:
		return nil
	}
}

func (c *settingsCommand) setThemePreset(preset string, stdout io.Writer) error {
	return c.updateTheme(stdout, func(themeCfg *theme.ThemeConfig) {
		themeCfg.Preset = strings.TrimSpace(preset)
	})
}

func (c *settingsCommand) setThemeColor(token theme.ColorToken, value string, stdout io.Writer) error {
	value = strings.TrimSpace(value)
	switch {
	case value == "":
		// clear / use preset value
	case strings.EqualFold(value, theme.ThemeDefaultSentinel) && theme.TokenSupportsDefaultSentinel(token):
		// terminal-default sentinel: store the normalized sentinel string verbatim
		// so the resolver can pin the pane/popup background to the terminal default.
		value = theme.ThemeDefaultSentinel
	default:
		hex, ok := theme.NormalizeHexColor(value)
		if !ok {
			return fmt.Errorf("invalid theme color %q", value)
		}
		value = hex
	}
	return c.updateTheme(stdout, func(themeCfg *theme.ThemeConfig) {
		setThemeColorField(themeCfg, token, value)
	})
}

func (c *settingsCommand) resetTheme(stdout io.Writer) error {
	return c.updateTheme(stdout, func(themeCfg *theme.ThemeConfig) {
		*themeCfg = theme.ThemeConfig{}
	})
}

func (c *settingsCommand) updateTheme(stdout io.Writer, update func(*theme.ThemeConfig)) error {
	if update == nil {
		return nil
	}
	path, err := c.globalConfigPath()
	if err != nil {
		return err
	}
	if _, err := hooks.UpdateGlobalConfig(path, func(cfg *hooks.ProjectConfig) error {
		update(&cfg.Theme)
		return nil
	}); err != nil {
		return c.finishThemeApply("", err, stdout)
	}
	return c.finishThemeApply(path, nil, stdout)
}

// finishThemeApply mirrors finishKeymapApply for the global theme path: after the
// durable config.toml save it regenerates the generated tmux app config and
// `tmux source-file`-reloads it via the shared regenerateAndReloadTmuxConfig
// core, then emits a staged Saved/Prepared/Running session report with the same
// graceful no-server tone ("Next: run `projmux tmux apply` ...") as the keymap
// path. This is what makes a Settings color set/reset live-apply without a
// manual `projmux tmux apply`.
func (c *settingsCommand) finishThemeApply(path string, saveErr error, stdout io.Writer) error {
	report := keymapApplyReport{
		Saved:    keymapApplyStage{Status: keymapApplyOK},
		Prepared: keymapApplyStage{Status: keymapApplySkipped, Detail: "waiting for saved theme"},
		Live:     keymapApplyStage{Status: keymapApplySkipped, Detail: "waiting for prepared config"},
	}
	if strings.TrimSpace(path) != "" {
		report.Saved.Detail = "config.toml: " + path
	}
	if saveErr != nil {
		report.Saved = keymapApplyStage{Status: keymapApplyFailed, Detail: keymapApplyDiagnostic("config.toml", saveErr)}
		report.Prepared = keymapApplyStage{Status: keymapApplySkipped, Detail: "theme was not saved"}
		report.Live = keymapApplyStage{Status: keymapApplySkipped, Detail: "theme was not saved"}
		_ = writeThemeApplyReport(stdout, report)
		return fmt.Errorf("save theme: %w", saveErr)
	}
	prepared, live, applyErr := c.regenerateAndReloadTmuxConfig()
	report.Prepared = prepared
	report.Live = live
	if applyErr != nil {
		_ = writeThemeApplyReport(stdout, report)
		if prepared.Status == keymapApplyFailed {
			return fmt.Errorf("update theme runtime config: %w", applyErr)
		}
		return fmt.Errorf("reload active tmux theme: %w", applyErr)
	}
	return writeThemeApplyReport(stdout, report)
}

// writeThemeApplyReport renders the theme apply staging report. It reuses the
// keymap report stage/line helpers for identical formatting but with
// theme-appropriate headline and recovery wording.
func writeThemeApplyReport(w io.Writer, report keymapApplyReport) error {
	if w == nil {
		return nil
	}
	if report.Saved.Status == keymapApplyOK && report.Prepared.Status == keymapApplyOK && report.Live.Status == keymapApplyOK {
		if _, err := fmt.Fprintln(w, "theme saved and applied"); err != nil {
			return err
		}
		for _, line := range []string{
			keymapApplyLine("Saved", report.Saved, false),
			keymapApplyLine("Prepared", report.Prepared, false),
			keymapApplyLine("Running session", report.Live, false),
		} {
			if _, err := fmt.Fprintln(w, line); err != nil {
				return err
			}
		}
		return nil
	}

	if _, err := fmt.Fprintln(w, "theme apply status"); err != nil {
		return err
	}
	for _, line := range []string{
		keymapApplyLine("Saved", report.Saved, true),
		keymapApplyLine("Prepared", report.Prepared, true),
		keymapApplyLine("Running session", report.Live, true),
	} {
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	switch {
	case report.Saved.Status == keymapApplyFailed:
		_, err := fmt.Fprintln(w, "Recovery: fix the config.toml problem, then try the Settings change again.")
		return err
	case report.Prepared.Status == keymapApplyFailed:
		_, err := fmt.Fprintln(w, "Recovery: resolve the generated tmux config error, then run `projmux tmux apply`.")
		return err
	case report.Live.Status == keymapApplyFailed:
		_, err := fmt.Fprintln(w, "Recovery: fix the live tmux reload issue, then run `projmux tmux apply`.")
		return err
	case report.Live.Status == keymapApplySkipped:
		_, err := fmt.Fprintln(w, "Next: run `projmux tmux apply` to sync a running projmux tmux server.")
		return err
	default:
		return nil
	}
}

func themeAction(action string) string {
	return settingsActionPrefixTheme + action
}

func parseThemeColorAction(raw string) (theme.ColorToken, bool) {
	token := theme.ColorToken(strings.TrimSpace(raw))
	if slices.Contains(themeSettingsColorTokens(), token) {
		return token, true
	}
	return "", false
}

func themeSettingsColorTokens() []theme.ColorToken {
	var out []theme.ColorToken
	for _, group := range themeTokenGroups {
		out = append(out, group.Tokens...)
	}
	return out
}

func themeColorLabel(token theme.ColorToken) string {
	return strings.ReplaceAll(string(token), "_", " ")
}

func themePresetSummary(cfg theme.ThemeConfig) string {
	value := strings.TrimSpace(cfg.Preset)
	if value == "" {
		return "unset - fallback preset fills missing colors"
	}
	state := "set override"
	if themeHasCustomColors(cfg) {
		state = "custom from " + value
	}
	return value + " - " + state
}

func themeColorSummary(cfg theme.ThemeConfig, token theme.ColorToken) string {
	value := themeColorFieldValue(cfg, token)
	if value == "" {
		if cfg.Preset != "" {
			return "from preset " + cfg.Preset
		}
		return "unset"
	}
	if strings.EqualFold(value, theme.ThemeDefaultSentinel) {
		return "Terminal default - overrides preset fill"
	}
	if cfg.Preset != "" && !strings.EqualFold(cfg.Preset, "inherit") {
		if presetHex, ok := theme.PresetColorHex(cfg.Preset, token); ok {
			if strings.EqualFold(value, presetHex) {
				return themeSwatch(value) + " " + value + " - equivalent to " + cfg.Preset
			}
			return themeSwatch(value) + " " + value + " - custom from " + cfg.Preset
		}
	}
	return themeSwatch(value) + " " + value + " - set override"
}

// themeColorSummaryEffective renders the Global-view summary for a token. It
// always starts with the currently effective preview, then adds source/detail
// text so the token list can be scanned without opening each token.
func themeColorSummaryEffective(cfg theme.ThemeConfig, effective theme.EffectiveTheme, token theme.ColorToken) string {
	preview := themeEffectiveColorPreview(effective, token)
	detail := themeColorSummaryEffectiveDetail(cfg, effective, token)
	if preview == "" {
		return detail
	}
	if detail == "" {
		return preview
	}
	return preview + " " + detail
}

func themeColorSummaryEffectiveDetail(cfg theme.ThemeConfig, effective theme.EffectiveTheme, token theme.ColorToken) string {
	if strings.EqualFold(themeColorFieldValue(cfg, token), theme.ThemeDefaultSentinel) {
		return "Terminal default - overrides preset fill"
	}
	value := themeColorFieldValue(cfg, token)
	if value != "" {
		if cfg.Preset != "" && !strings.EqualFold(cfg.Preset, "inherit") {
			if presetHex, ok := theme.PresetColorHex(cfg.Preset, token); ok {
				if strings.EqualFold(value, presetHex) {
					return "equivalent to " + cfg.Preset
				}
				return "custom from " + cfg.Preset
			}
		}
		return "set override"
	}
	if strings.TrimSpace(cfg.Preset) != "" {
		return "from preset " + strings.TrimSpace(cfg.Preset)
	}
	field := effectiveColorField(effective, token)
	if field.Source == theme.SourceGlobal && legacyForegroundFillsToken(cfg, token) {
		return "legacy foreground"
	}
	if field.Source != theme.SourceFallback {
		return string(field.Source)
	}
	if theme.IsThemeDefaultSpec(field.Value) || strings.TrimSpace(field.Value.Hex) != "" {
		return "(fallback) - " + string(field.Source)
	}
	return "unset"
}

func themeEffectiveColorPreview(effective theme.EffectiveTheme, token theme.ColorToken) string {
	field := effectiveColorField(effective, token)
	if theme.IsThemeDefaultSpec(field.Value) {
		if field.Source == theme.SourceFallback {
			return settingsDim("default")
		}
		return "default"
	}
	value := strings.TrimSpace(field.Value.Hex)
	if value == "" {
		return ""
	}
	preview := themeSwatch(value) + " " + value
	if field.Source == theme.SourceFallback {
		return settingsDim(preview)
	}
	return preview
}

// settingsDim wraps text in the SGR dim attribute so fallback rows read as muted
// in the Global theme view.
func settingsDim(s string) string {
	return "\x1b[2m" + s + "\x1b[0m"
}

func effectiveColorField(effective theme.EffectiveTheme, token theme.ColorToken) theme.ColorField {
	switch token {
	case theme.TokenBackground:
		return effective.Background
	case theme.TokenSurface:
		return effective.Surface
	case theme.TokenStatusBackground:
		return effective.StatusBackground
	case theme.TokenSurfaceActive:
		return effective.SurfaceActive
	case theme.TokenChromeForeground:
		return effective.ChromeForeground
	case theme.TokenTextPrimary:
		return effective.TextPrimary
	case theme.TokenForeground:
		return effective.Foreground
	case theme.TokenMuted:
		return effective.Muted
	case theme.TokenAccent:
		return effective.Accent
	case theme.TokenCritical:
		return effective.Critical
	case theme.TokenWarning:
		return effective.Warning
	case theme.TokenProgress:
		return effective.Progress
	case theme.TokenSuccess:
		return effective.Success
	case theme.TokenActionRequired:
		return effective.ActionRequired
	case theme.TokenPaneActiveBg:
		return effective.PaneActiveBg
	case theme.TokenFocus:
		return effective.Focus
	default:
		return theme.ColorField{}
	}
}

func themeColorCurrentValue(cfg theme.ThemeConfig, token theme.ColorToken) string {
	value := themeColorFieldValue(cfg, token)
	if strings.EqualFold(value, theme.ThemeDefaultSentinel) {
		return "Terminal default"
	}
	if value != "" {
		return value
	}
	if legacyForegroundFillsToken(cfg, token) {
		return strings.TrimSpace(cfg.Foreground)
	}
	if cfg.Preset != "" && !strings.EqualFold(cfg.Preset, "inherit") {
		if hex, ok := theme.PresetColorHex(cfg.Preset, token); ok {
			return hex
		}
	}
	return "(unset)"
}

func themeColorInitialQuery(cfg theme.ThemeConfig, token theme.ColorToken) string {
	value := themeColorFieldValue(cfg, token)
	if strings.EqualFold(value, theme.ThemeDefaultSentinel) {
		// The sentinel is not a hex value; start the hex input empty so the user
		// can type a color (Terminal default is set via its own row).
		return "#"
	}
	if value != "" {
		return value
	}
	if legacyForegroundFillsToken(cfg, token) {
		return strings.TrimSpace(cfg.Foreground)
	}
	if cfg.Preset != "" && !strings.EqualFold(cfg.Preset, "inherit") {
		if hex, ok := theme.PresetColorHex(cfg.Preset, token); ok {
			return hex
		}
	}
	return "#"
}

func legacyForegroundFillsToken(cfg theme.ThemeConfig, token theme.ColorToken) bool {
	switch token {
	case theme.TokenChromeForeground:
		return strings.TrimSpace(cfg.ChromeForeground) == "" && strings.TrimSpace(cfg.Foreground) != ""
	case theme.TokenTextPrimary:
		return strings.TrimSpace(cfg.TextPrimary) == "" && strings.TrimSpace(cfg.Foreground) != ""
	default:
		return false
	}
}

func themeSwatch(hex string) string {
	if normalized, ok := theme.NormalizeHexColor(hex); ok {
		return "\x1b[48;2;" + hexRGBSGR(normalized) + "m  \x1b[0m"
	}
	return "  "
}

func hexRGBSGR(hex string) string {
	r, _ := strconv.ParseInt(hex[1:3], 16, 0)
	g, _ := strconv.ParseInt(hex[3:5], 16, 0)
	b, _ := strconv.ParseInt(hex[5:7], 16, 0)
	return fmt.Sprintf("%d;%d;%d", r, g, b)
}

func themePresetSwatches(preset string) string {
	var parts []string
	for _, token := range []theme.ColorToken{theme.TokenBackground, theme.TokenSurfaceActive, theme.TokenAccent, theme.TokenWarning} {
		if hex, ok := theme.PresetColorHex(preset, token); ok {
			parts = append(parts, themeSwatch(hex))
		}
	}
	return strings.Join(parts, " ")
}

func themeHasCustomColors(cfg theme.ThemeConfig) bool {
	for _, token := range theme.ResolverColorTokens {
		if strings.TrimSpace(themeColorFieldValue(cfg, token)) != "" {
			return true
		}
	}
	return false
}

func themeColorFieldValue(cfg theme.ThemeConfig, token theme.ColorToken) string {
	switch token {
	case theme.TokenBackground:
		return strings.TrimSpace(cfg.Background)
	case theme.TokenSurface:
		return strings.TrimSpace(cfg.Surface)
	case theme.TokenStatusBackground:
		return strings.TrimSpace(cfg.StatusBackground)
	case theme.TokenSurfaceActive:
		return strings.TrimSpace(cfg.SurfaceActive)
	case theme.TokenChromeForeground:
		return strings.TrimSpace(cfg.ChromeForeground)
	case theme.TokenTextPrimary:
		return strings.TrimSpace(cfg.TextPrimary)
	case theme.TokenForeground:
		return strings.TrimSpace(cfg.Foreground)
	case theme.TokenMuted:
		return strings.TrimSpace(cfg.Muted)
	case theme.TokenAccent:
		return strings.TrimSpace(cfg.Accent)
	case theme.TokenCritical:
		return strings.TrimSpace(cfg.Critical)
	case theme.TokenWarning:
		return strings.TrimSpace(cfg.Warning)
	case theme.TokenProgress:
		return strings.TrimSpace(cfg.Progress)
	case theme.TokenSuccess:
		return strings.TrimSpace(cfg.Success)
	case theme.TokenActionRequired:
		return strings.TrimSpace(cfg.ActionRequired)
	case theme.TokenPaneActiveBg:
		return strings.TrimSpace(cfg.PaneActiveBg)
	case theme.TokenFocus:
		return strings.TrimSpace(cfg.Focus)
	default:
		return ""
	}
}

func setThemeColorField(cfg *theme.ThemeConfig, token theme.ColorToken, value string) {
	switch token {
	case theme.TokenBackground:
		cfg.Background = value
	case theme.TokenSurface:
		cfg.Surface = value
	case theme.TokenStatusBackground:
		cfg.StatusBackground = value
	case theme.TokenSurfaceActive:
		cfg.SurfaceActive = value
	case theme.TokenChromeForeground:
		cfg.ChromeForeground = value
	case theme.TokenTextPrimary:
		cfg.TextPrimary = value
	case theme.TokenForeground:
		cfg.Foreground = value
	case theme.TokenMuted:
		cfg.Muted = value
	case theme.TokenAccent:
		cfg.Accent = value
	case theme.TokenCritical:
		cfg.Critical = value
	case theme.TokenWarning:
		cfg.Warning = value
	case theme.TokenProgress:
		cfg.Progress = value
	case theme.TokenSuccess:
		cfg.Success = value
	case theme.TokenActionRequired:
		cfg.ActionRequired = value
	case theme.TokenPaneActiveBg:
		cfg.PaneActiveBg = value
	case theme.TokenFocus:
		cfg.Focus = value
	}
}
