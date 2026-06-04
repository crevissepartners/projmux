package app

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	"github.com/crevissepartners/projmux/internal/theme"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

type themeLayer string

const (
	themeLayerGlobal  themeLayer = "global"
	themeLayerProject themeLayer = "project"
)

func (c *settingsCommand) runThemeLayerSection(layer themeLayer, stdout, stderr io.Writer) error {
	for {
		options, err := c.themeLayerOptions(layer)
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
		case action == themeAction(layer, "preset"):
			if err := c.runThemePresetSection(layer, stdout, stderr); err != nil {
				return err
			}
		case strings.HasPrefix(action, themeAction(layer, "color:")):
			token, ok := parseThemeColorAction(strings.TrimPrefix(action, themeAction(layer, "color:")))
			if !ok {
				return fmt.Errorf("unknown theme color action: %s", action)
			}
			if err := c.runThemeColorSection(layer, token, stdout, stderr); err != nil {
				return err
			}
		case action == themeAction(layer, "font-family"):
			if err := c.runThemeStringField(layer, "font_family", stdout, stderr); err != nil {
				return err
			}
		case action == themeAction(layer, "font-size"):
			if err := c.runThemeStringField(layer, "font_size", stdout, stderr); err != nil {
				return err
			}
		case action == themeAction(layer, "reset"):
			if err := c.resetThemeLayer(layer, stdout); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown theme settings action: %s", action)
		}
	}
}

func (c *settingsCommand) themeLayerOptions(layer themeLayer) (intpickercompat.Options, error) {
	entries, err := c.themeLayerEntries(layer)
	if err != nil {
		return intpickercompat.Options{}, err
	}
	title := "Theme - Global values"
	prompt := "Settings > Theme > Global > "
	ui := "settings-theme-global"
	active := settingsRootTabGlobal
	if layer == themeLayerProject {
		title = "Theme - Project overrides"
		prompt = "Settings > Project > Theme override > "
		ui = "settings-theme-project"
		active = settingsRootTabProject
	}
	return intpickercompat.Options{
		UI:         ui,
		Entries:    entries,
		Title:      title,
		TitleChips: settingsPassiveRootTabChipsLocale(active, c.resolveSettingsProjectContext().hasProject(), c.locale()),
		Prompt:     prompt,
		Footer:     projmuxFooter("Enter: open/apply  |  Back row: parent "),
		ExpectKeys: []string{"enter"},
		Bindings:   settingsCloseBindings(),
	}, nil
}

func (c *settingsCommand) themeLayerEntries(layer themeLayer) ([]intpickercompat.Entry, error) {
	cfg, err := c.themeLayerConfig(layer)
	if err != nil {
		return nil, err
	}
	entries := []intpickercompat.Entry{c.backEntry()}
	if layer == themeLayerProject {
		ctx := c.resolveSettingsProjectContext()
		if !ctx.hasProject() {
			return append(entries, intpickercompat.Entry{
				Label: c.rowLabelDim("Project theme", "disabled - no project context"),
				Value: settingsNoopValue,
			}), nil
		}
		entries = append(entries, intpickercompat.Entry{
			Label: c.rowLabelInfo("Project", ctx.Name, filepath.Join(ctx.Path, ".projmux", "config.toml")),
			Value: settingsNoopValue,
		})
	}
	entries = append(entries, intpickercompat.Entry{
		Label:     c.rowLabel(settingsGlyphOpen, settingsColorType, "Preset selector", themePresetSummary(layer, cfg.Theme)),
		Value:     themeAction(layer, "preset"),
		SearchKey: "theme preset selector swatch colors",
	})
	for _, token := range theme.ResolverColorTokens {
		entries = append(entries, intpickercompat.Entry{
			Label:     c.rowLabel(settingsGlyphOpen, settingsColorType, themeColorLabel(token), themeColorSummary(layer, cfg.Theme, token)),
			Value:     themeAction(layer, "color:"+string(token)),
			SearchKey: "theme color swatch hex input " + string(token),
		})
	}
	entries = append(entries,
		intpickercompat.Entry{
			Label: c.rowLabel(settingsGlyphType, settingsColorType, "Font family", themeStringSummary(layer, cfg.Theme.FontFamily, "font_family")),
			Value: themeAction(layer, "font-family"),
		},
		intpickercompat.Entry{
			Label: c.rowLabel(settingsGlyphType, settingsColorType, "Font size", themeStringSummary(layer, cfg.Theme.FontSize, "font_size")),
			Value: themeAction(layer, "font-size"),
		},
		intpickercompat.Entry{
			Label: c.rowLabel(settingsGlyphRemove, settingsColorRemove, "Reset theme values", themeResetDescription(layer)),
			Value: themeAction(layer, "reset"),
		},
	)
	return entries, nil
}

func (c *settingsCommand) runThemePresetSection(layer themeLayer, stdout, stderr io.Writer) error {
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-theme-preset",
			Entries:    c.themePresetEntries(layer),
			Title:      "Theme - Preset selector",
			TitleChips: settingsPassiveRootTabChipsLocale(themeLayerTab(layer), c.resolveSettingsProjectContext().hasProject(), c.locale()),
			Prompt:     themeLayerPrompt(layer) + "Preset > ",
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
		case strings.HasPrefix(action, themeAction(layer, "preset:set:")):
			preset := strings.TrimPrefix(action, themeAction(layer, "preset:set:"))
			if err := c.setThemePreset(layer, preset, stdout); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown theme preset action: %s", action)
		}
	}
}

func (c *settingsCommand) themePresetEntries(layer themeLayer) []intpickercompat.Entry {
	cfg, _ := c.themeLayerConfig(layer)
	current := strings.TrimSpace(cfg.Theme.Preset)
	entries := []intpickercompat.Entry{c.backEntry()}
	if layer == themeLayerProject {
		glyph, color := settingsGlyphInactive, settingsColorDim
		if strings.EqualFold(current, "inherit") || current == "" {
			glyph, color = settingsGlyphToggle, settingsColorAdd
		}
		entries = append(entries, intpickercompat.Entry{
			Label: c.rowLabel(glyph, color, "Inherit global preset", "explicitly follow global/fallback preset"),
			Value: themeAction(layer, "preset:set:inherit"),
		})
	}
	for _, preset := range theme.PresetNames() {
		glyph, color := settingsGlyphInactive, settingsColorDim
		if strings.EqualFold(current, preset) {
			glyph, color = settingsGlyphToggle, settingsColorAdd
		}
		entries = append(entries, intpickercompat.Entry{
			Label:     c.rowLabel(glyph, color, preset, themePresetSwatches(preset)),
			Value:     themeAction(layer, "preset:set:"+preset),
			SearchKey: "theme preset " + preset,
		})
	}
	if layer == themeLayerGlobal {
		entries = append(entries, intpickercompat.Entry{
			Label: c.rowLabel(settingsGlyphRemove, settingsColorRemove, "Clear global preset", "remove only global preset value"),
			Value: themeAction(layer, "preset:set:"),
		})
	}
	return entries
}

func (c *settingsCommand) runThemeColorSection(layer themeLayer, token theme.ColorToken, stdout, stderr io.Writer) error {
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-theme-color",
			Entries:    c.themeColorEntries(layer, token),
			Title:      "Theme - " + themeColorLabel(token),
			TitleChips: settingsPassiveRootTabChipsLocale(themeLayerTab(layer), c.resolveSettingsProjectContext().hasProject(), c.locale()),
			Prompt:     themeLayerPrompt(layer) + themeColorLabel(token) + " > ",
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
		case action == themeAction(layer, "color-type:"+string(token)):
			if err := c.runThemeColorHexInput(layer, token, stdout, stderr); err != nil {
				return err
			}
		case strings.HasPrefix(action, themeAction(layer, "color-set:"+string(token)+":")):
			value := strings.TrimPrefix(action, themeAction(layer, "color-set:"+string(token)+":"))
			if err := c.setThemeColor(layer, token, value, stdout); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown theme color detail action: %s", action)
		}
	}
}

func (c *settingsCommand) themeColorEntries(layer themeLayer, token theme.ColorToken) []intpickercompat.Entry {
	cfg, _ := c.themeLayerConfig(layer)
	entries := []intpickercompat.Entry{
		c.backEntry(),
		{Label: c.rowLabelInfo("Current", themeColorCurrentValue(layer, cfg.Theme, token), themeColorSummary(layer, cfg.Theme, token)), Value: settingsNoopValue},
	}
	if layer == themeLayerProject {
		entries = append(entries, intpickercompat.Entry{
			Label: c.rowLabel(settingsGlyphToggle, settingsColorAdd, "Inherit global", "store inherit for this project field"),
			Value: themeAction(layer, "color-set:"+string(token)+":inherit"),
		})
	}
	if inheritedPreset := strings.TrimSpace(cfg.Theme.Preset); inheritedPreset != "" && !strings.EqualFold(inheritedPreset, "inherit") {
		entries = append(entries, intpickercompat.Entry{
			Label: c.rowLabel(settingsGlyphRemove, settingsColorRemove, "Use preset value", "clear explicit token override"),
			Value: themeAction(layer, "color-set:"+string(token)+":"),
		})
	}
	entries = append(entries, intpickercompat.Entry{
		Label: c.rowLabel(settingsGlyphType, settingsColorType, "Type hex value...", "swatch + #RRGGBB input"),
		Value: themeAction(layer, "color-type:"+string(token)),
	})
	for _, preset := range theme.PresetNames() {
		hex, ok := theme.PresetColorHex(preset, token)
		if !ok {
			continue
		}
		entries = append(entries, intpickercompat.Entry{
			Label:     c.rowLabel(settingsGlyphAdd, settingsColorAdd, "Set "+preset, themeSwatch(hex)+" "+hex),
			Value:     themeAction(layer, "color-set:"+string(token)+":"+hex),
			SearchKey: "theme swatch " + preset + " " + string(token) + " " + hex,
		})
	}
	return entries
}

func (c *settingsCommand) runThemeColorHexInput(layer themeLayer, token theme.ColorToken, stdout, stderr io.Writer) error {
	cfg, _ := c.themeLayerConfig(layer)
	result, err := c.runPicker(intpickercompat.Options{
		UI:           "settings-theme-color-hex",
		Entries:      []intpickercompat.Entry{{Label: c.rowLabelInfo("Swatch", themeSwatch(themeColorCurrentValue(layer, cfg.Theme, token)), "current")}},
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
	return c.setThemeColor(layer, token, hex, stdout)
}

func (c *settingsCommand) runThemeStringField(layer themeLayer, field string, stdout, stderr io.Writer) error {
	cfg, err := c.themeLayerConfig(layer)
	if err != nil {
		return err
	}
	initial := themeStringFieldValue(cfg.Theme, field)
	if layer == themeLayerProject && initial == "" {
		initial = "inherit"
	}
	value, ok, err := c.runProjectConfigTyped("Theme - "+field, field+" > ", initial)
	if err != nil || !ok {
		return err
	}
	value = strings.TrimSpace(value)
	if field == "font_size" && value != "" && !strings.EqualFold(value, "inherit") {
		if _, err := strconv.Atoi(value); err != nil {
			fmt.Fprintf(stderr, "invalid font_size %q: use an integer or inherit\n", value)
			return nil
		}
	}
	return c.updateThemeLayer(layer, stdout, func(themeCfg *theme.ThemeConfig) {
		setThemeStringField(themeCfg, field, value)
	})
}

func (c *settingsCommand) runEffectiveThemeSection(stdout, stderr io.Writer) error {
	_ = stdout
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-theme-effective",
			Entries:    c.effectiveThemeEntries(),
			Title:      "Theme - Effective values",
			TitleChips: settingsPassiveRootTabChipsLocale(settingsRootTabProject, c.resolveSettingsProjectContext().hasProject(), c.locale()),
			Prompt:     "Settings > Project > Effective theme > ",
			Footer:     projmuxFooter("Enter: back  |  Back row: parent "),
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
		switch action {
		case settingsBackValue:
			return nil
		case settingsNoopValue:
			continue
		default:
			return fmt.Errorf("unknown effective theme action: %s", action)
		}
	}
}

func (c *settingsCommand) effectiveThemeEntries() []intpickercompat.Entry {
	entries := []intpickercompat.Entry{c.backEntry()}
	ctx := c.resolveSettingsProjectContext()
	if !ctx.hasProject() {
		return append(entries, intpickercompat.Entry{Label: c.rowLabelDim("Effective theme", "disabled - no project context"), Value: settingsNoopValue})
	}
	globalCfg, globalErr := c.currentGlobalProjectConfig()
	projectCfg, projectErr := c.currentProjectConfigForTheme()
	if globalErr != nil {
		entries = append(entries, intpickercompat.Entry{Label: c.rowLabelDim("Global parse error", globalErr.Error()), Value: settingsNoopValue})
	}
	if projectErr != nil {
		entries = append(entries, intpickercompat.Entry{Label: c.rowLabelDim("Project parse error", projectErr.Error()), Value: settingsNoopValue})
	}
	effective := theme.ResolveTheme(globalCfg.Theme, projectCfg.Theme)
	for _, field := range effective.Fields() {
		value := field.Value
		if strings.TrimSpace(value) == "" {
			value = "(unset)"
		}
		if isThemeColorField(field.Name) {
			value = themeSwatch(value) + " " + value
		}
		entries = append(entries, intpickercompat.Entry{
			Label: c.rowLabelInfo(field.Name, value, string(field.Source)),
			Value: settingsNoopValue,
		})
	}
	for _, warning := range effective.Warnings {
		entries = append(entries, intpickercompat.Entry{
			Label: c.rowLabelDim("Warning "+string(warning.Source)+" "+warning.Field, warning.Message),
			Value: settingsNoopValue,
		})
	}
	return entries
}

func (c *settingsCommand) themeLayerConfig(layer themeLayer) (hooks.ProjectConfig, error) {
	if layer == themeLayerProject {
		ctx := c.resolveSettingsProjectContext()
		if !ctx.hasProject() {
			return hooks.ProjectConfig{}, nil
		}
		return hooks.LoadProjectConfigFile(settingsProjectConfigPath(ctx))
	}
	return c.currentGlobalProjectConfig()
}

func (c *settingsCommand) setThemePreset(layer themeLayer, preset string, stdout io.Writer) error {
	return c.updateThemeLayer(layer, stdout, func(themeCfg *theme.ThemeConfig) {
		themeCfg.Preset = strings.TrimSpace(preset)
	})
}

func (c *settingsCommand) setThemeColor(layer themeLayer, token theme.ColorToken, value string, stdout io.Writer) error {
	value = strings.TrimSpace(value)
	if value != "" && !strings.EqualFold(value, "inherit") {
		hex, ok := theme.NormalizeHexColor(value)
		if !ok {
			return fmt.Errorf("invalid theme color %q", value)
		}
		value = hex
	}
	return c.updateThemeLayer(layer, stdout, func(themeCfg *theme.ThemeConfig) {
		setThemeColorField(themeCfg, token, value)
	})
}

func (c *settingsCommand) resetThemeLayer(layer themeLayer, stdout io.Writer) error {
	return c.updateThemeLayer(layer, stdout, func(themeCfg *theme.ThemeConfig) {
		*themeCfg = theme.ThemeConfig{}
	})
}

func (c *settingsCommand) updateThemeLayer(layer themeLayer, stdout io.Writer, update func(*theme.ThemeConfig)) error {
	if update == nil {
		return nil
	}
	if layer == themeLayerProject {
		ctx := c.resolveSettingsProjectContext()
		if !ctx.hasProject() {
			return errors.New("project theme requires a project context")
		}
		return c.saveProjectConfig(ctx, stdout, func(cfg *hooks.ProjectConfig) error {
			update(&cfg.Theme)
			return nil
		})
	}
	path, err := c.globalConfigPath()
	if err != nil {
		return err
	}
	if _, err := hooks.UpdateGlobalConfig(path, func(cfg *hooks.ProjectConfig) error {
		update(&cfg.Theme)
		return nil
	}); err != nil {
		return err
	}
	if stdout != nil {
		_, err = fmt.Fprintf(stdout, "wrote %s\n", path)
	}
	return err
}

func themeAction(layer themeLayer, action string) string {
	return settingsActionPrefixTheme + string(layer) + ":" + action
}

func themeLayerTab(layer themeLayer) settingsRootTab {
	if layer == themeLayerProject {
		return settingsRootTabProject
	}
	return settingsRootTabGlobal
}

func themeLayerPrompt(layer themeLayer) string {
	if layer == themeLayerProject {
		return "Settings > Project > Theme override > "
	}
	return "Settings > Theme > Global > "
}

func parseThemeColorAction(raw string) (theme.ColorToken, bool) {
	token := theme.ColorToken(strings.TrimSpace(raw))
	if slices.Contains(theme.ResolverColorTokens, token) {
		return token, true
	}
	return "", false
}

func themeColorLabel(token theme.ColorToken) string {
	return strings.ReplaceAll(string(token), "_", " ")
}

func themePresetSummary(layer themeLayer, cfg theme.ThemeConfig) string {
	value := strings.TrimSpace(cfg.Preset)
	if layer == themeLayerProject && (value == "" || strings.EqualFold(value, "inherit")) {
		return "inherit global"
	}
	if value == "" {
		return "unset - fallback preset fills missing colors"
	}
	state := "set override"
	if themeLayerHasCustomColors(cfg) {
		state = "custom from " + value
	}
	return value + " - " + state
}

func themeColorSummary(layer themeLayer, cfg theme.ThemeConfig, token theme.ColorToken) string {
	value := themeColorFieldValue(cfg, token)
	if layer == themeLayerProject && (value == "" || strings.EqualFold(value, "inherit")) {
		return "inherit global"
	}
	if value == "" {
		if cfg.Preset != "" {
			return "from preset " + cfg.Preset
		}
		return "unset"
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

func themeColorCurrentValue(layer themeLayer, cfg theme.ThemeConfig, token theme.ColorToken) string {
	value := themeColorFieldValue(cfg, token)
	if value != "" && !strings.EqualFold(value, "inherit") {
		return value
	}
	if cfg.Preset != "" && !strings.EqualFold(cfg.Preset, "inherit") {
		if hex, ok := theme.PresetColorHex(cfg.Preset, token); ok {
			return hex
		}
	}
	if layer == themeLayerProject {
		return "inherit"
	}
	return "(unset)"
}

func themeColorInitialQuery(cfg theme.ThemeConfig, token theme.ColorToken) string {
	value := themeColorFieldValue(cfg, token)
	if value != "" && !strings.EqualFold(value, "inherit") {
		return value
	}
	if cfg.Preset != "" && !strings.EqualFold(cfg.Preset, "inherit") {
		if hex, ok := theme.PresetColorHex(cfg.Preset, token); ok {
			return hex
		}
	}
	return "#"
}

func themeStringSummary(layer themeLayer, value, field string) string {
	value = strings.TrimSpace(value)
	if layer == themeLayerProject && (value == "" || strings.EqualFold(value, "inherit")) {
		return "inherit global"
	}
	if value == "" {
		return "unset"
	}
	return value + " - set override " + field
}

func themeResetDescription(layer themeLayer) string {
	if layer == themeLayerProject {
		return "remove only project theme override values"
	}
	return "remove only global theme values"
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

func themeLayerHasCustomColors(cfg theme.ThemeConfig) bool {
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
	case theme.TokenSurfaceActive:
		return strings.TrimSpace(cfg.SurfaceActive)
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
	case theme.TokenSurfaceActive:
		cfg.SurfaceActive = value
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
	}
}

func themeStringFieldValue(cfg theme.ThemeConfig, field string) string {
	switch field {
	case "font_family":
		return strings.TrimSpace(cfg.FontFamily)
	case "font_size":
		return strings.TrimSpace(cfg.FontSize)
	default:
		return ""
	}
}

func setThemeStringField(cfg *theme.ThemeConfig, field, value string) {
	switch field {
	case "font_family":
		cfg.FontFamily = value
	case "font_size":
		cfg.FontSize = value
	}
}

func isThemeColorField(name string) bool {
	for _, token := range theme.ResolverColorTokens {
		if name == string(token) {
			return true
		}
	}
	return false
}
