package app

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/theme"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

func (c *settingsCommand) statusbarEntries() []intpickercompat.Entry {
	locale := appLocale(c.homeDir, c.lookupEnv)
	current := c.currentStatusbarDecorations()
	badgeStyle := loadAIBadgeStyle(c.homeDir, c.lookupEnv)
	targets := []statusbarDecorationTarget{
		statusbarDecorationTargetCwd,
		statusbarDecorationTargetGit,
		statusbarDecorationTargetNotify,
	}

	entries := make([]intpickercompat.Entry, 0, len(targets)+3)
	entries = append(entries, settingsBackEntryLocale(locale))
	entries = append(entries, c.localeSettingsEntry())
	entries = append(entries, intpickercompat.Entry{
		Label:     settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, "AI badge style", string(badgeStyle)+" - "+aiBadgeStylePreview(badgeStyle)),
		Value:     settingsActionPrefixAIBadgeStyle,
		SearchKey: "appearance ai badge style semantic pane border " + string(badgeStyle),
	})
	for _, target := range targets {
		meta, ok := statusbarDecorationTargetMeta(target)
		if !ok {
			continue
		}
		mode := current.modeForTarget(target)
		entries = append(entries, intpickercompat.Entry{
			Label:     settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, meta.Name, string(mode)+" - "+statusbarDecorationPreview(target, mode)),
			Value:     settingsActionPrefixStatusbar + string(target),
			SearchKey: "appearance decoration statusbar " + string(target) + " " + string(mode) + " " + meta.Name + " " + meta.Description,
		})
	}
	return entries
}

func (s statusbarDecorationSet) modeForTarget(target statusbarDecorationTarget) config.StatusbarDecoration {
	switch target {
	case statusbarDecorationTargetCwd:
		return s.Cwd
	case statusbarDecorationTargetGit:
		return s.Git
	case statusbarDecorationTargetNotify:
		return s.Notify
	default:
		return config.StatusbarDecorationOff
	}
}

type statusbarDecorationTargetDetails struct {
	Name        string
	Title       string
	Description string
}

func statusbarDecorationTargetMeta(target statusbarDecorationTarget) (statusbarDecorationTargetDetails, bool) {
	switch target {
	case statusbarDecorationTargetCwd:
		return statusbarDecorationTargetDetails{
			Name:        "Path icon",
			Title:       "Appearance - Path icon",
			Description: "folder marker before cwd",
		}, true
	case statusbarDecorationTargetGit:
		return statusbarDecorationTargetDetails{
			Name:        "Git icon",
			Title:       "Appearance - Git icon",
			Description: "provider marker before branch",
		}, true
	case statusbarDecorationTargetNotify:
		return statusbarDecorationTargetDetails{
			Name:        "Notify icon",
			Title:       "Appearance - Notify icon",
			Description: "bell marker in notification sidebar",
		}, true
	default:
		return statusbarDecorationTargetDetails{}, false
	}
}

func (c *settingsCommand) runAppearanceSection(stdout, stderr io.Writer) error {
	for {
		options, err := c.sectionOptions(settingsSectionStatusbar)
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
		case action == settingsAppearanceLanguage:
			if err := c.runLocaleSection(stdout, stderr); err != nil {
				return err
			}
		case action == settingsActionPrefixAIBadgeStyle:
			if err := c.runAIBadgeStyleSection(stdout, stderr); err != nil {
				return err
			}
		case strings.HasPrefix(action, settingsActionPrefixStatusbar):
			target, ok := parseStatusbarDecorationTarget(strings.TrimPrefix(action, settingsActionPrefixStatusbar))
			if !ok {
				return fmt.Errorf("unknown appearance target: %s", action)
			}
			if err := c.runAppearanceTargetSection(target, stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown appearance action: %s", action)
		}
	}
}

func (c *settingsCommand) runAIBadgeStyleSection(stdout, stderr io.Writer) error {
	for {
		options := c.aiBadgeStyleOptions()
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
		case strings.HasPrefix(action, settingsActionPrefixAIBadgeStyle):
			style := strings.TrimPrefix(action, settingsActionPrefixAIBadgeStyle)
			if !isAIBadgeStyle(style) {
				return fmt.Errorf("unknown AI badge style action: %s", action)
			}
			if err := c.setAIBadgeStyle(style); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown AI badge style action: %s", action)
		}
	}
}

func (c *settingsCommand) aiBadgeStyleOptions() intpickercompat.Options {
	locale := appLocale(c.homeDir, c.lookupEnv)
	return intpickercompat.Options{
		UI:         "settings-ai-badge-style",
		Entries:    c.aiBadgeStyleEntries(),
		Title:      "Appearance - AI badge style",
		TitleChips: settingsPassiveRootTabChipsLocale(settingsRootTabGlobal, c.resolveSettingsProjectContext().hasProject(), locale),
		Prompt:     "Settings > Appearance > AI badge style > ",
		Footer:     projmuxFooter("Enter: apply  |  Back row: parent "),
		ExpectKeys: []string{"enter"},
		Bindings:   c.settingsCloseBindings(),
	}
}

func (c *settingsCommand) aiBadgeStyleEntries() []intpickercompat.Entry {
	locale := appLocale(c.homeDir, c.lookupEnv)
	current := loadAIBadgeStyle(c.homeDir, c.lookupEnv)
	entries := []intpickercompat.Entry{
		settingsBackEntryLocale(locale),
		{Label: settingsLabelInfoLocale(locale, "Current", string(current), "pane border live AI marker"), Value: settingsNoopValue},
	}
	for _, style := range aiBadgeStyles() {
		glyph := settingsGlyphInactive
		color := settingsColorDim
		desc := aiBadgeStylePreview(style) + " - " + aiBadgeStyleDescription(style)
		if style == current {
			glyph = settingsGlyphToggle
			color = settingsColorAdd
			desc += " - current"
		}
		entries = append(entries, intpickercompat.Entry{
			Label:     settingsLabelLocale(locale, glyph, color, "Preview "+string(style), desc),
			Value:     settingsActionPrefixAIBadgeStyle + string(style),
			SearchKey: "ai badge style " + string(style) + " " + aiBadgeStylePreview(style),
		})
	}
	return entries
}

func aiBadgeStyles() []config.AIBadgeStyle {
	return []config.AIBadgeStyle{
		config.AIBadgeStyleDot,
		config.AIBadgeStyleEmoji,
		config.AIBadgeStyleOff,
	}
}

func isAIBadgeStyle(value string) bool {
	switch config.AIBadgeStyle(strings.TrimSpace(value)) {
	case config.AIBadgeStyleDot, config.AIBadgeStyleEmoji, config.AIBadgeStyleOff:
		return true
	default:
		return false
	}
}

func aiBadgeStyleDescription(style config.AIBadgeStyle) string {
	switch config.NormalizeAIBadgeStyle(string(style)) {
	case config.AIBadgeStyleEmoji:
		return "emoji marker"
	case config.AIBadgeStyleOff:
		return "preserve spacing without marker"
	default:
		return "colored dot marker"
	}
}

func aiBadgeStylePreview(style config.AIBadgeStyle) string {
	switch config.NormalizeAIBadgeStyle(string(style)) {
	case config.AIBadgeStyleEmoji:
		return "⏳ prompt  ✅ complete  🔄 working"
	case config.AIBadgeStyleOff:
		return "prompt  complete  working"
	default:
		return "● prompt  ● complete  ● working"
	}
}

func (c *settingsCommand) runAppearanceTargetSection(target statusbarDecorationTarget, stdout, stderr io.Writer) error {
	for {
		options, err := c.statusbarDecorationTargetOptions(target)
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
		case strings.HasPrefix(action, settingsActionPrefixStatusbar):
			raw := strings.TrimPrefix(action, settingsActionPrefixStatusbar)
			actionTarget, mode, ok := parseStatusbarDecorationDetailAction(raw)
			if !ok || actionTarget != target || !isStatusbarDecorationMode(mode) {
				return fmt.Errorf("unknown appearance detail action: %s", action)
			}
			if err := c.setStatusbarDecoration(string(actionTarget) + ":" + mode); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown appearance detail action: %s", action)
		}
	}
}

func (c *settingsCommand) statusbarDecorationTargetOptions(target statusbarDecorationTarget) (intpickercompat.Options, error) {
	locale := appLocale(c.homeDir, c.lookupEnv)
	meta, ok := statusbarDecorationTargetMeta(target)
	if !ok {
		return intpickercompat.Options{}, fmt.Errorf("unknown appearance target: %s", target)
	}
	return intpickercompat.Options{
		UI:         "settings-statusbar-detail",
		Entries:    c.statusbarDecorationTargetEntries(target),
		Title:      meta.Title,
		TitleChips: settingsPassiveRootTabChipsLocale(settingsRootTabGlobal, c.resolveSettingsProjectContext().hasProject(), locale),
		Prompt:     "Settings > Appearance > " + meta.Name + " > ",
		Footer:     projmuxFooter("Enter: apply  |  Back row: parent "),
		ExpectKeys: []string{"enter"},
		Bindings:   c.settingsCloseBindings(),
	}, nil
}

func (c *settingsCommand) statusbarDecorationTargetEntries(target statusbarDecorationTarget) []intpickercompat.Entry {
	locale := appLocale(c.homeDir, c.lookupEnv)
	current := c.currentStatusbarDecorations().modeForTarget(target)
	meta, _ := statusbarDecorationTargetMeta(target)
	entries := []intpickercompat.Entry{
		settingsBackEntryLocale(locale),
		{Label: settingsLabelInfoLocale(locale, "Current", string(current), meta.Description), Value: settingsNoopValue},
	}
	for _, mode := range statusbarDecorationModes() {
		glyph := settingsGlyphInactive
		color := settingsColorDim
		desc := statusbarDecorationPreview(target, mode) + " - " + statusbarDecorationModeDescription(mode)
		if mode == current {
			glyph = settingsGlyphToggle
			color = settingsColorAdd
			desc += " - current"
		}
		entries = append(entries, intpickercompat.Entry{
			Label:     settingsLabelLocale(locale, glyph, color, "Preview "+string(mode), desc),
			Value:     settingsActionPrefixStatusbar + string(target) + ":" + string(mode),
			SearchKey: string(target) + " " + string(mode) + " preview " + statusbarDecorationPreview(target, mode),
		})
	}
	return entries
}

func statusbarDecorationModes() []config.StatusbarDecoration {
	return []config.StatusbarDecoration{
		config.StatusbarDecorationOff,
		config.StatusbarDecorationSymbol,
		config.StatusbarDecorationEmoji,
	}
}

func isStatusbarDecorationMode(value string) bool {
	switch config.StatusbarDecoration(strings.TrimSpace(value)) {
	case config.StatusbarDecorationOff, config.StatusbarDecorationSymbol, config.StatusbarDecorationEmoji:
		return true
	default:
		return false
	}
}

func statusbarDecorationModeDescription(mode config.StatusbarDecoration) string {
	switch mode {
	case config.StatusbarDecorationSymbol:
		return "Nerd Font-style marker"
	case config.StatusbarDecorationEmoji:
		return "emoji marker"
	default:
		return "no icon prefix"
	}
}

func statusbarDecorationPreview(target statusbarDecorationTarget, mode config.StatusbarDecoration) string {
	mode = config.NormalizeStatusbarDecoration(string(mode))
	switch target {
	case statusbarDecorationTargetCwd:
		switch mode {
		case config.StatusbarDecorationSymbol:
			return " ~/source/repos/projmux"
		case config.StatusbarDecorationEmoji:
			return "📁 ~/source/repos/projmux"
		default:
			return "~/source/repos/projmux"
		}
	case statusbarDecorationTargetGit:
		switch mode {
		case config.StatusbarDecorationSymbol:
			return " main * ↑1"
		case config.StatusbarDecorationEmoji:
			return "🐱 main * ↑1"
		default:
			return "main * ↑1"
		}
	case statusbarDecorationTargetNotify:
		switch mode {
		case config.StatusbarDecorationSymbol:
			return " Pending Notifications"
		case config.StatusbarDecorationEmoji:
			return "🔔 Pending Notifications"
		default:
			return "Pending Notifications"
		}
	default:
		return string(mode)
	}
}

func parseStatusbarDecorationTarget(value string) (statusbarDecorationTarget, bool) {
	target := statusbarDecorationTarget(strings.TrimSpace(value))
	switch target {
	case statusbarDecorationTargetCwd, statusbarDecorationTargetGit, statusbarDecorationTargetNotify:
		return target, true
	default:
		return "", false
	}
}

func parseStatusbarDecorationDetailAction(value string) (statusbarDecorationTarget, string, bool) {
	targetRaw, op, ok := strings.Cut(strings.TrimSpace(value), ":")
	if !ok {
		return "", "", false
	}
	target, ok := parseStatusbarDecorationTarget(targetRaw)
	if !ok {
		return "", "", false
	}
	op = strings.TrimSpace(op)
	if op == "" {
		return "", "", false
	}
	return target, op, true
}

func (c *settingsCommand) currentStatusbarDecorations() statusbarDecorationSet {
	return loadStatusbarDecorationSet(c.homeDir, c.lookupEnv)
}

func (c *settingsCommand) setStatusbarDecoration(value string) error {
	target, mode := parseStatusbarDecorationSetting(value)
	paths, err := statusbarConfigPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return err
	}
	path := paths.StatusbarDecorationFile()
	option := statusbarDecorationTmuxOption
	label := "decoration mode"
	if target != "" {
		path = statusbarDecorationTargetFile(paths, target)
		option = statusbarDecorationTmuxOptionForTarget(target)
		label = "decoration " + string(target)
	}
	if err := config.SaveStatusbarDecorationFile(path, mode); err != nil {
		return err
	}
	if c.lookupEnv != nil && strings.TrimSpace(c.lookupEnv("TMUX")) != "" && c.runCommand != nil {
		if err := c.runCommand("tmux", "set-option", "-g", option, string(mode)); err != nil {
			return fmt.Errorf("set live tmux decoration mode: %w", err)
		}
		_ = c.runCommand("tmux", "display-message", label+": "+string(mode))
	}
	return nil
}

func (c *settingsCommand) setAIBadgeStyle(value string) error {
	style := config.NormalizeAIBadgeStyle(value)
	if err := saveAIBadgeStyle(c.homeDir, c.lookupEnv, style); err != nil {
		return err
	}
	if c.lookupEnv != nil && strings.TrimSpace(c.lookupEnv("TMUX")) != "" && c.runCommand != nil {
		if err := c.runCommand("tmux", "set-option", "-g", aiBadgeStyleTmuxOption, string(style)); err != nil {
			return fmt.Errorf("set live tmux AI badge style option: %w", err)
		}
		source, err := configRenderThemeSource(c.homeDir, c.lookupEnv, c.resolveSettingsProjectContext().Path)
		if err != nil {
			return fmt.Errorf("resolve live tmux theme: %w", err)
		}
		if err := c.runCommand("tmux", "set-option", "-g", "pane-border-format", tmuxPaneBorderFormatWithAIBadgeStyle(style, theme.RenderRolesFromEffective(source.effective))); err != nil {
			return fmt.Errorf("set live tmux pane border format: %w", err)
		}
		binaryPath, err := os.Executable()
		if err != nil {
			return fmt.Errorf("resolve live tmux executable: %w", err)
		}
		windowStatusFormat, windowStatusCurrentFormat := tmuxWindowStatusFormats(binaryPath, source.effective)
		if err := c.runCommand("tmux", "set-option", "-g", "window-status-format", windowStatusFormat); err != nil {
			return fmt.Errorf("set live tmux window status format: %w", err)
		}
		if err := c.runCommand("tmux", "set-option", "-g", "window-status-current-format", windowStatusCurrentFormat); err != nil {
			return fmt.Errorf("set live tmux current window status format: %w", err)
		}
		_ = c.runCommand("tmux", "display-message", "AI badge style: "+string(style))
	}
	return nil
}

func parseStatusbarDecorationSetting(value string) (statusbarDecorationTarget, config.StatusbarDecoration) {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, ":")
	if len(parts) == 2 {
		target := statusbarDecorationTarget(strings.TrimSpace(parts[0]))
		switch target {
		case statusbarDecorationTargetCwd, statusbarDecorationTargetGit, statusbarDecorationTargetNotify:
			return target, config.NormalizeStatusbarDecoration(parts[1])
		}
	}
	return "", config.NormalizeStatusbarDecoration(value)
}
