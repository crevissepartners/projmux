package app

import (
	"fmt"
	"io"
	"strings"

	"github.com/crevissepartners/projmux/internal/app/usagecmd"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/theme"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// statusbarEntries renders the Appearance container. Appearance owns every
// visual surface: the Theme tokens, the Status Bar components, the UI language
// and the live Pane attention badge. Theme is a View here rather than a
// separate Settings root, which is what makes colour a single destination.
func (c *settingsCommand) statusbarEntries() []intpickercompat.Entry {
	locale := appLocale(c.homeDir, c.lookupEnv)
	badgeStyle := loadAIBadgeStyle(c.homeDir, c.lookupEnv)

	entries := make([]intpickercompat.Entry, 0, 5)
	entries = append(entries, settingsBackEntryLocale(locale))
	entries = append(entries, intpickercompat.Entry{
		Label:     settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, settingsNavLabel(settingsNavAppearanceTheme), c.themeSettingsSummary()),
		Value:     settingsAppearanceTheme,
		SearchKey: "appearance theme preset color tokens palette",
	})
	entries = append(entries, intpickercompat.Entry{
		Label:     settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, settingsNavLabel(settingsNavStatusBar), c.statusBarSummary()),
		Value:     settingsAppearanceStatusBar,
		SearchKey: "appearance status bar components notifications agent usage hud providers windows working directory git resources",
	})
	entries = append(entries, c.localeSettingsEntry())
	entries = append(entries, intpickercompat.Entry{
		Label:     settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, settingsNavLabel(settingsNavAppearance+".badge"), string(badgeStyle)+" - "+aiBadgeStylePreview(badgeStyle)),
		Value:     settingsActionPrefixAIBadgeStyle,
		SearchKey: "appearance agent attention badge style pane border " + string(badgeStyle),
	})
	return entries
}

// statusBarComponentTargets are the icon-bearing Status Bar components. The
// row-one slice adds visibility to cwd/Git in their existing detail Views;
// simple row-one booleans remain direct toggles in the parent container.
var statusBarComponentTargets = []statusbarDecorationTarget{
	statusbarDecorationTargetNotify,
	statusbarDecorationTargetCwd,
	statusbarDecorationTargetGit,
}

// statusBarEntries renders the Status Bar container. A component with more
// than one setting opens its own detail View; Resources is a plain boolean and
// therefore stays a Toggle in this view rather than gaining a route.
func (c *settingsCommand) statusBarEntries() []intpickercompat.Entry {
	locale := appLocale(c.homeDir, c.lookupEnv)
	current := c.currentStatusbarDecorations()

	entries := make([]intpickercompat.Entry, 0, 9)
	entries = append(entries, settingsBackEntryLocale(locale))
	for _, target := range statusBarComponentTargets {
		meta, ok := statusbarDecorationTargetMeta(target)
		if !ok {
			continue
		}
		mode := current.modeForTarget(target)
		state := string(mode) + " - " + statusbarDecorationPreview(target, mode)
		if target == statusbarDecorationTargetNotify {
			visibility := loadStatusbarHUDVisibilityState(c.homeDir, c.lookupEnv, statusbarHUDNotifications)
			state = string(visibility.Effective) + " - " + state + " - " + statusbarHUDVisibilitySourceLabel(visibility)
		} else {
			component := statusbarRowOneWorkingDirectory
			if target == statusbarDecorationTargetGit {
				component = statusbarRowOneGit
			}
			visibility := loadStatusbarRowOneVisibilityState(c.homeDir, c.lookupEnv, component)
			state = string(visibility.Effective) + " - " + state + " - " + statusbarHUDVisibilitySourceLabel(visibility)
		}
		entries = append(entries, intpickercompat.Entry{
			Label:     settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, meta.Name, state),
			Value:     settingsActionPrefixStatusbar + string(target),
			SearchKey: "status bar component " + string(target) + " " + string(mode) + " " + meta.Name + " " + meta.Description,
		})
		if target == statusbarDecorationTargetNotify {
			usageState := loadStatusbarHUDVisibilityState(c.homeDir, c.lookupEnv, statusbarHUDAgentUsage)
			entries = append(entries, intpickercompat.Entry{
				Label:     settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, "Agent Usage HUD", agentUsageVisibilityStateText(locale, usageState, usageState)),
				Value:     settingsAppearanceAgentUsageHUD,
				SearchKey: "status bar agent usage hud providers windows visible source preview",
			})
			entries = append(entries, c.statusBarRowOneVisibilityEntry(locale, statusbarRowOneProject))
		}
	}
	entries = append(entries, c.statusBarResourcesEntry(locale))
	entries = append(entries,
		c.statusBarRowOneVisibilityEntry(locale, statusbarRowOneClock),
		c.statusBarRowOneVisibilityEntry(locale, statusbarRowOneSettingsLauncher),
	)
	return entries
}

// statusBarResourcesEntry is the promoted Labs toggle. Off stops the segment
// and the host sampler together, which is why its off state is worded as
// enablement rather than visibility.
func (c *settingsCommand) statusBarResourcesEntry(locale i18n.Locale) intpickercompat.Entry {
	mode, source, supported := c.currentLiveResourcesMode()
	label := settingsNavLabel(settingsNavStatusBar + ".resources")
	if !supported {
		return intpickercompat.Entry{
			Label:     settingsLabelDimLocale(locale, label, "unavailable on this platform"),
			Value:     settingsNoopValue,
			SearchKey: "status bar resources cpu memory unsupported platform",
		}
	}
	glyph := settingsGlyphInactive
	color := settingsColorDim
	next := config.LiveResourcesOn
	state := settingsCatalogTextLocale(locale, "off") + " - " + settingsCatalogTextLocale(locale, "segment and host sampling stopped")
	if mode == config.LiveResourcesOn {
		glyph = settingsGlyphToggle
		color = settingsColorAdd
		next = config.LiveResourcesOff
		state = settingsCatalogTextLocale(locale, "on") + " - " + settingsCatalogTextLocale(locale, "live CPU and memory")
	}
	return intpickercompat.Entry{
		Label:     settingsLabelLocale(locale, glyph, color, label, state+" - "+settingsCatalogTextLocale(locale, source)),
		Value:     settingsActionPrefixLiveResources + string(next),
		SearchKey: "status bar resources cpu memory macOS Linux WSL on off " + source,
	}
}

func (c *settingsCommand) statusBarSummary() string {
	locale := c.locale()
	current := c.currentStatusbarDecorations()
	mode, _, supported := c.currentLiveResourcesMode()
	parts := make([]string, 0, 8)
	for _, target := range statusBarComponentTargets {
		meta, ok := statusbarDecorationTargetMeta(target)
		if !ok {
			continue
		}
		if target == statusbarDecorationTargetNotify {
			visibility := loadStatusbarHUDVisibilityState(c.homeDir, c.lookupEnv, statusbarHUDNotifications)
			parts = append(parts, settingsCatalogTextLocale(locale, meta.Name)+" "+string(visibility.Effective))
			parts = append(parts, settingsCatalogTextLocale(locale, "Agent Usage HUD")+" "+string(loadStatusbarHUDVisibilityState(c.homeDir, c.lookupEnv, statusbarHUDAgentUsage).Effective))
			parts = append(parts, settingsCatalogTextLocale(locale, "Project")+" "+string(loadStatusbarRowOneVisibilityState(c.homeDir, c.lookupEnv, statusbarRowOneProject).Effective))
			continue
		}
		component := statusbarRowOneWorkingDirectory
		if target == statusbarDecorationTargetGit {
			component = statusbarRowOneGit
		}
		parts = append(parts, settingsCatalogTextLocale(locale, meta.Name)+" "+string(loadStatusbarRowOneVisibilityState(c.homeDir, c.lookupEnv, component).Effective)+"/"+string(current.modeForTarget(target)))
	}
	if supported {
		parts = append(parts, settingsCatalogTextLocale(locale, "Resources")+" "+string(mode))
	}
	parts = append(parts,
		settingsCatalogTextLocale(locale, "Clock")+" "+string(loadStatusbarRowOneVisibilityState(c.homeDir, c.lookupEnv, statusbarRowOneClock).Effective),
		settingsCatalogTextLocale(locale, "Settings launcher")+" "+string(loadStatusbarRowOneVisibilityState(c.homeDir, c.lookupEnv, statusbarRowOneSettingsLauncher).Effective),
	)
	return strings.Join(parts, ", ")
}

func (c *settingsCommand) statusBarRowOneVisibilityEntry(locale i18n.Locale, component statusbarRowOneComponent) intpickercompat.Entry {
	state := loadStatusbarRowOneVisibilityState(c.homeDir, c.lookupEnv, component)
	next := config.StatusbarVisibilityOff
	glyph := settingsGlyphToggle
	color := settingsColorAdd
	if state.Effective == config.StatusbarVisibilityOff {
		next = config.StatusbarVisibilityOn
		glyph = settingsGlyphInactive
		color = settingsColorDim
	}
	return intpickercompat.Entry{
		Label:     settingsLabelLocale(locale, glyph, color, statusbarRowOneComponentName(component), string(state.Effective)+" - "+statusbarRowOneComponentPreview(component)+" - "+statusbarHUDVisibilitySourceLabel(state)),
		Value:     settingsActionPrefixHUDVisibility + string(component) + ":" + string(next),
		SearchKey: "status bar row one " + string(component) + " visible on off source preview",
	}
}

func statusbarRowOneComponentPreview(component statusbarRowOneComponent) string {
	switch component {
	case statusbarRowOneProject:
		return "Project name / runtime fallback"
	case statusbarRowOneWorkingDirectory:
		return "focused Pane cwd"
	case statusbarRowOneGit:
		return "branch and working tree state"
	case statusbarRowOneClock:
		return "date and time"
	case statusbarRowOneSettingsLauncher:
		return "mouse Settings chip"
	default:
		return "status segment"
	}
}

func (c *settingsCommand) statusBarHUDVisibilityEntry(locale i18n.Locale, component statusbarHUDComponent) intpickercompat.Entry {
	state := loadStatusbarHUDVisibilityState(c.homeDir, c.lookupEnv, component)
	next := config.StatusbarVisibilityOff
	glyph := settingsGlyphToggle
	color := settingsColorAdd
	if state.Effective == config.StatusbarVisibilityOff {
		next = config.StatusbarVisibilityOn
		glyph = settingsGlyphInactive
		color = settingsColorDim
	}
	source := statusbarHUDVisibilitySourceLabel(state)
	preview := "compact Provider account usage bar"
	if component == statusbarHUDNotifications {
		preview = "pending Notification queue HUD"
	}
	return intpickercompat.Entry{
		Label:     settingsLabelLocale(locale, glyph, color, statusbarHUDComponentName(component), string(state.Effective)+" - "+preview+" - "+source),
		Value:     settingsActionPrefixHUDVisibility + string(component) + ":" + string(next),
		SearchKey: "status bar " + string(component) + " visible on off source preview agent usage notifications",
	}
}

func statusbarHUDVisibilitySourceLabel(state config.StatusbarVisibilityState) string {
	source := string(state.Source)
	if state.Invalid != "" {
		source += " (invalid saved value ignored)"
	}
	return source
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
			Name:        "Working directory",
			Title:       "Status Bar - Working directory",
			Description: "focused Pane cwd segment and its icon",
		}, true
	case statusbarDecorationTargetGit:
		return statusbarDecorationTargetDetails{
			Name:        "Git",
			Title:       "Status Bar - Git",
			Description: "branch segment and its icon",
		}, true
	case statusbarDecorationTargetNotify:
		return statusbarDecorationTargetDetails{
			Name:        "Notifications HUD",
			Title:       "Status Bar - Notifications HUD",
			Description: "Notification queue HUD and its icon",
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
		case action == settingsAppearanceTheme:
			if err := c.runThemeSection(stdout, stderr); err != nil {
				return err
			}
		case action == settingsAppearanceStatusBar:
			if err := c.runStatusBarSection(stdout, stderr); err != nil {
				return err
			}
		case action == settingsActionPrefixAIBadgeStyle:
			if err := c.runAIBadgeStyleSection(stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown appearance action: %s", action)
		}
	}
}

// runStatusBarSection is the Appearance > Status Bar container.
func (c *settingsCommand) runStatusBarSection(stdout, stderr io.Writer) error {
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-status-bar",
			Entries:    c.statusBarEntries(),
			Title:      "Appearance - Status Bar",
			TitleChips: settingsPassiveRootTabChipsLocale(settingsRootTabGlobal, c.resolveSettingsProjectContext().hasProject(), c.locale()),
			Prompt:     "Settings > Appearance > Status Bar > ",
			Footer:     projmuxFooter("Enter: open/apply  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
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
		case strings.HasPrefix(action, settingsActionPrefixStatusbar):
			target, ok := parseStatusbarDecorationTarget(strings.TrimPrefix(action, settingsActionPrefixStatusbar))
			if !ok {
				return fmt.Errorf("unknown status bar component: %s", action)
			}
			if err := c.runAppearanceTargetSection(target, stdout, stderr); err != nil {
				return err
			}
		case strings.HasPrefix(action, settingsActionPrefixLiveResources):
			if err := c.executeWithFeedback(action, stdout, stderr); err != nil {
				return err
			}
		case action == settingsAppearanceAgentUsageHUD:
			if err := c.runAgentUsageHUDSection(stdout, stderr); err != nil {
				return err
			}
		case strings.HasPrefix(action, settingsActionPrefixHUDVisibility):
			if err := c.runStatusbarVisibilityMutation(strings.TrimPrefix(action, settingsActionPrefixHUDVisibility), stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown status bar action: %s", action)
		}
	}
}

func (c *settingsCommand) runAgentUsageHUDSection(stdout, stderr io.Writer) error {
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-agent-usage-hud",
			Entries:    c.agentUsageHUDEntries(),
			Title:      "Appearance - Status Bar - Agent Usage HUD",
			TitleChips: settingsPassiveRootTabChipsLocale(settingsRootTabGlobal, c.resolveSettingsProjectContext().hasProject(), c.locale()),
			Prompt:     "Settings > Appearance > Status Bar > Agent Usage HUD > ",
			Footer:     projmuxFooter("Enter: open/apply  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
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
		case strings.HasPrefix(action, settingsAppearanceAgentUsageProviderPrefix):
			provider := strings.TrimPrefix(action, settingsAppearanceAgentUsageProviderPrefix)
			if err := c.runAgentUsageProviderSection(provider, stdout, stderr); err != nil {
				return err
			}
		case strings.HasPrefix(action, settingsActionPrefixHUDVisibility):
			if err := c.runStatusbarVisibilityMutation(strings.TrimPrefix(action, settingsActionPrefixHUDVisibility), stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown Agent Usage HUD action: %s", action)
		}
	}
}

func (c *settingsCommand) runAgentUsageProviderSection(provider string, stdout, stderr io.Writer) error {
	capability, ok := agentUsageProviderCapability(provider)
	if !ok {
		return fmt.Errorf("unknown Agent Usage HUD provider: %s", provider)
	}
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-agent-usage-provider",
			Entries:    c.agentUsageProviderEntries(string(capability.ID)),
			Title:      "Agent Usage HUD - " + capability.DisplayName,
			TitleChips: settingsPassiveRootTabChipsLocale(settingsRootTabGlobal, c.resolveSettingsProjectContext().hasProject(), c.locale()),
			Prompt:     "Settings > Appearance > Status Bar > Agent Usage HUD > " + capability.DisplayName + " > ",
			Footer:     projmuxFooter("Enter: apply  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
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
		case strings.HasPrefix(action, settingsActionPrefixHUDVisibility):
			if err := c.runStatusbarVisibilityMutation(strings.TrimPrefix(action, settingsActionPrefixHUDVisibility), stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown Agent Usage HUD provider action: %s", action)
		}
	}
}

func (c *settingsCommand) agentUsageHUDEntries() []intpickercompat.Entry {
	locale := c.locale()
	overall := loadStatusbarHUDVisibilityState(c.homeDir, c.lookupEnv, statusbarHUDAgentUsage)
	entries := []intpickercompat.Entry{
		settingsBackEntryLocale(locale),
		{Label: settingsLabelInfoLocale(locale, "Current", agentUsageVisibilityStateText(locale, overall, overall), settingsCatalogTextLocale(locale, "ambient status usage projection")), Value: settingsNoopValue, SearchKey: "agent usage hud current saved effective source preview"},
		agentUsageVisibilityToggleEntry(locale, "Visible", agentUsageVisibilityLeaf{}, overall, overall),
	}
	for _, capability := range usagecmd.HUDProviderCapabilities() {
		saved := loadAgentUsageVisibilityState(c.homeDir, c.lookupEnv, agentUsageVisibilityLeaf{provider: string(capability.ID)})
		effective := gatedStatusbarVisibility(saved, overall)
		entries = append(entries, intpickercompat.Entry{
			Label:     settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, capability.DisplayName, agentUsageVisibilityStateText(locale, saved, effective)),
			Value:     settingsAppearanceAgentUsageProviderPrefix + string(capability.ID),
			SearchKey: "agent usage hud provider " + string(capability.ID) + " visible windows source",
		})
	}
	return entries
}

func (c *settingsCommand) agentUsageProviderEntries(provider string) []intpickercompat.Entry {
	locale := c.locale()
	capability, _ := agentUsageProviderCapability(provider)
	overall := loadStatusbarHUDVisibilityState(c.homeDir, c.lookupEnv, statusbarHUDAgentUsage)
	providerSaved := loadAgentUsageVisibilityState(c.homeDir, c.lookupEnv, agentUsageVisibilityLeaf{provider: provider})
	providerEffective := gatedStatusbarVisibility(providerSaved, overall)
	entries := []intpickercompat.Entry{
		settingsBackEntryLocale(locale),
		{Label: settingsLabelInfoLocale(locale, "Current", agentUsageVisibilityStateText(locale, providerSaved, providerEffective), settingsCatalogTextLocale(locale, "ambient provider usage projection")+" - "+capability.DisplayName), Value: settingsNoopValue, SearchKey: "agent usage provider current saved effective source"},
		agentUsageVisibilityToggleEntry(locale, "Visible", agentUsageVisibilityLeaf{provider: provider}, providerSaved, providerEffective),
	}
	for _, window := range capability.Windows {
		leaf := agentUsageVisibilityLeaf{provider: provider, window: window.Key}
		saved := loadAgentUsageVisibilityState(c.homeDir, c.lookupEnv, leaf)
		effective := gatedStatusbarVisibility(saved, overall, providerSaved)
		entries = append(entries, agentUsageVisibilityToggleEntry(locale, window.Label, leaf, saved, effective))
	}
	return entries
}

func agentUsageVisibilityToggleEntry(locale i18n.Locale, label string, leaf agentUsageVisibilityLeaf, saved, effective config.StatusbarVisibilityState) intpickercompat.Entry {
	next := config.StatusbarVisibilityOff
	glyph := settingsGlyphToggle
	color := settingsColorAdd
	if saved.Effective == config.StatusbarVisibilityOff {
		next = config.StatusbarVisibilityOn
		glyph = settingsGlyphInactive
		color = settingsColorDim
	}
	action := string(statusbarHUDAgentUsage) + ":" + string(next)
	if leaf.provider != "" && leaf.window == "" {
		action = agentUsageProviderVisibilityAction + ":" + leaf.provider + ":" + string(next)
	} else if leaf.provider != "" {
		action = agentUsageWindowVisibilityAction + ":" + leaf.provider + ":" + leaf.window + ":" + string(next)
	}
	return intpickercompat.Entry{
		Label:     settingsLabelLocale(locale, glyph, color, label, agentUsageVisibilityStateText(locale, saved, effective)),
		Value:     settingsActionPrefixHUDVisibility + action,
		SearchKey: "agent usage visibility " + leaf.provider + " " + leaf.window + " saved effective source on off",
	}
}

func agentUsageVisibilityStateText(locale i18n.Locale, saved, effective config.StatusbarVisibilityState) string {
	source := settingsCatalogTextLocale(locale, string(saved.Source))
	if saved.Invalid != "" {
		source += " (" + settingsCatalogTextLocale(locale, "invalid saved value ignored") + ")"
	}
	return settingsCatalogTextLocale(locale, "saved") + " " + string(saved.Effective) + " - " + settingsCatalogTextLocale(locale, "effective") + " " + string(effective.Effective) + " - " + source
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
			if err := c.runSettingsMutation("Agent attention badge style", stdout, stderr, func(io.Writer, io.Writer) error {
				return c.setAIBadgeStyle(style)
			}); err != nil {
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
	if _, ok := statusbarDecorationTargetMeta(target); !ok {
		return fmt.Errorf("unknown status bar component: %s", target)
	}
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
		case action == statusbarDecorationIconValue(target):
			if err := c.runStatusBarIconChooser(target, stdout, stderr); err != nil {
				return err
			}
		case strings.HasPrefix(action, settingsActionPrefixHUDVisibility):
			raw := strings.TrimPrefix(action, settingsActionPrefixHUDVisibility)
			if component, _, ok := parseStatusbarHUDVisibilityAction(raw); ok {
				if component != statusbarHUDNotifications || target != statusbarDecorationTargetNotify {
					return fmt.Errorf("unknown status bar component action: %s", action)
				}
			} else if component, _, ok := parseStatusbarRowOneVisibilityAction(raw); !ok {
				return fmt.Errorf("unknown status bar component action: %s", action)
			} else {
				expected := statusbarRowOneWorkingDirectory
				if target == statusbarDecorationTargetGit {
					expected = statusbarRowOneGit
				}
				if component != expected {
					return fmt.Errorf("unknown status bar component action: %s", action)
				}
			}
			if err := c.runStatusbarVisibilityMutation(raw, stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown status bar component action: %s", action)
		}
	}
}

// statusbarDecorationIconValue is the component's icon Choice row. The chooser
// it opens is a transient interaction, not a navigation boundary: closing it
// returns to the same component View.
func statusbarDecorationIconValue(target statusbarDecorationTarget) string {
	return settingsActionPrefixStatusbar + string(target) + ":icon"
}

func (c *settingsCommand) statusbarDecorationTargetOptions(target statusbarDecorationTarget) (intpickercompat.Options, error) {
	locale := appLocale(c.homeDir, c.lookupEnv)
	meta, ok := statusbarDecorationTargetMeta(target)
	if !ok {
		return intpickercompat.Options{}, fmt.Errorf("unknown status bar component: %s", target)
	}
	return intpickercompat.Options{
		UI:         "settings-statusbar-detail",
		Entries:    c.statusbarDecorationTargetEntries(target),
		Title:      meta.Title,
		TitleChips: settingsPassiveRootTabChipsLocale(settingsRootTabGlobal, c.resolveSettingsProjectContext().hasProject(), locale),
		Prompt:     "Settings > Appearance > Status Bar > " + meta.Name + " > ",
		Footer:     projmuxFooter("Enter: open  |  Back row: parent "),
		ExpectKeys: []string{"enter"},
		Bindings:   c.settingsCloseBindings(),
	}, nil
}

// statusbarDecorationTargetEntries renders one component View: the read-only
// current/source/preview row first, then the icon Choice.
func (c *settingsCommand) statusbarDecorationTargetEntries(target statusbarDecorationTarget) []intpickercompat.Entry {
	locale := appLocale(c.homeDir, c.lookupEnv)
	current := c.currentStatusbarDecorations().modeForTarget(target)
	meta, _ := statusbarDecorationTargetMeta(target)
	entries := []intpickercompat.Entry{
		settingsBackEntryLocale(locale),
		{
			Label:     settingsLabelInfoLocale(locale, "Current", string(current)+" - "+statusbarDecorationPreview(target, current), meta.Description),
			Value:     settingsNoopValue,
			SearchKey: "status bar " + string(target) + " current source preview",
		},
	}
	if target == statusbarDecorationTargetNotify {
		entries = append(entries, c.statusBarHUDVisibilityEntry(locale, statusbarHUDNotifications))
	} else if target == statusbarDecorationTargetCwd {
		entries = append(entries, c.statusBarRowOneVisibilityEntry(locale, statusbarRowOneWorkingDirectory))
	} else if target == statusbarDecorationTargetGit {
		entries = append(entries, c.statusBarRowOneVisibilityEntry(locale, statusbarRowOneGit))
	}
	entries = append(entries, intpickercompat.Entry{
		Label:     settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, statusbarDecorationIconLabel(target), string(current)),
		Value:     statusbarDecorationIconValue(target),
		SearchKey: "status bar " + string(target) + " icon off symbol emoji",
	})
	return entries
}

func (c *settingsCommand) runStatusbarVisibilityMutation(raw string, stdout, stderr io.Writer) error {
	if leaf, mode, ok := parseAgentUsageVisibilityAction(raw); ok {
		name := leaf.provider
		if capability, found := agentUsageProviderCapability(leaf.provider); found {
			name = capability.DisplayName
		}
		if leaf.window != "" {
			if window, found := agentUsageWindowCapability(leaf.provider, leaf.window); found {
				name += " " + window.Label
			}
		}
		return c.runSettingsMutation(name, stdout, stderr, func(io.Writer, io.Writer) error {
			return c.setAgentUsageVisibility(leaf, mode)
		})
	}
	if component, mode, ok := parseStatusbarHUDVisibilityAction(raw); ok {
		return c.runSettingsMutation(statusbarHUDComponentName(component), stdout, stderr, func(io.Writer, io.Writer) error {
			return c.setStatusbarHUDVisibility(component, mode)
		})
	}
	component, mode, ok := parseStatusbarRowOneVisibilityAction(raw)
	if !ok {
		return fmt.Errorf("unknown status bar visibility action: %s", raw)
	}
	return c.runSettingsMutation(statusbarRowOneComponentName(component), stdout, stderr, func(io.Writer, io.Writer) error {
		return c.setStatusbarRowOneVisibility(component, mode)
	})
}

// statusbarDecorationIconLabel keeps the Notifications HUD row worded as the
// notification icon it controls, matching the target tree.
func statusbarDecorationIconLabel(target statusbarDecorationTarget) string {
	if target == statusbarDecorationTargetNotify {
		return "Notification icon"
	}
	return "Icon"
}

// runStatusBarIconChooser is the compact off/symbol/emoji chooser.
func (c *settingsCommand) runStatusBarIconChooser(target statusbarDecorationTarget, stdout, stderr io.Writer) error {
	meta, ok := statusbarDecorationTargetMeta(target)
	if !ok {
		return fmt.Errorf("unknown status bar component: %s", target)
	}
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-statusbar-icon",
			Entries:    c.statusbarDecorationIconEntries(target),
			Title:      meta.Title + " - " + statusbarDecorationIconLabel(target),
			TitleChips: settingsPassiveRootTabChipsLocale(settingsRootTabGlobal, c.resolveSettingsProjectContext().hasProject(), c.locale()),
			Prompt:     "Settings > Appearance > Status Bar > " + meta.Name + " > " + statusbarDecorationIconLabel(target) + " > ",
			Footer:     projmuxFooter("Enter: apply  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
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
		case strings.HasPrefix(action, settingsActionPrefixStatusbar):
			raw := strings.TrimPrefix(action, settingsActionPrefixStatusbar)
			actionTarget, mode, ok := parseStatusbarDecorationDetailAction(raw)
			if !ok || actionTarget != target || !isStatusbarDecorationMode(mode) {
				return fmt.Errorf("unknown status bar icon action: %s", action)
			}
			if err := c.runSettingsMutation(meta.Name, stdout, stderr, func(io.Writer, io.Writer) error {
				return c.setStatusbarDecoration(string(actionTarget) + ":" + mode)
			}); err != nil {
				return err
			}
			return nil
		default:
			return fmt.Errorf("unknown status bar icon action: %s", action)
		}
	}
}

func (c *settingsCommand) statusbarDecorationIconEntries(target statusbarDecorationTarget) []intpickercompat.Entry {
	locale := appLocale(c.homeDir, c.lookupEnv)
	current := c.currentStatusbarDecorations().modeForTarget(target)
	entries := []intpickercompat.Entry{settingsBackEntryLocale(locale)}
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
		binaryPath, err := resolveExecutablePath()
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
