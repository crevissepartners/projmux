package app

import (
	"fmt"
	"strings"

	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

type settingsRootTab string

const (
	settingsRootTabGlobal  settingsRootTab = "global"
	settingsRootTabProject settingsRootTab = "project"
)

type SettingsAxis uint8

const (
	settingsAxisGlobal SettingsAxis = 1 << iota
	settingsAxisProject
	settingsAxisBoth = settingsAxisGlobal | settingsAxisProject
)

type settingsEntryMeta struct {
	Name  string
	Axis  SettingsAxis
	Kind  settingsEntryKind
	Owner settingsEntryOwner
}

// settingsEntryKind makes the interaction contract for every rendered
// non-empty Settings entry explicit. Passive covers both informational and
// disabled rows: owner loops must consume Enter as a no-op instead of falling
// through to an unknown-action error.
type settingsEntryKind string

const (
	settingsEntryNavigation settingsEntryKind = "navigation"
	settingsEntryActionable settingsEntryKind = "actionable"
	settingsEntryPassive    settingsEntryKind = "info-or-disabled"
)

type settingsEntryOwner uint8

const (
	settingsOwnerNone settingsEntryOwner = iota
	settingsOwnerPassiveLoop
	settingsOwnerRoot
	settingsOwnerGeneric
	settingsOwnerProjectPicker
	settingsOwnerAI
	settingsOwnerNotifications
	settingsOwnerAppearance
	settingsOwnerSessionState
	settingsOwnerKeybindings
	settingsOwnerLabs
	settingsOwnerAbout
	settingsOwnerHooks
	settingsOwnerProject
	settingsOwnerTheme
	settingsOwnerAINotifyDiagnostics
)

func settingsActionMeta(name string, axis SettingsAxis, owner settingsEntryOwner) settingsEntryMeta {
	return settingsEntryMeta{Name: name, Axis: axis, Kind: settingsEntryActionable, Owner: owner}
}

func settingsNavigationMeta(name string, axis SettingsAxis, owner settingsEntryOwner) settingsEntryMeta {
	return settingsEntryMeta{Name: name, Axis: axis, Kind: settingsEntryNavigation, Owner: owner}
}

func settingsPassiveMeta(name string, axis SettingsAxis) settingsEntryMeta {
	return settingsEntryMeta{Name: name, Axis: axis, Kind: settingsEntryPassive, Owner: settingsOwnerPassiveLoop}
}

var settingsEntryCatalog = map[string]settingsEntryMeta{
	settingsBackValue:                  settingsNavigationMeta("Back", settingsAxisBoth, settingsOwnerPassiveLoop),
	settingsNoopValue:                  settingsPassiveMeta("Info or disabled", settingsAxisBoth),
	settingsRootTabGlobalValue:         settingsNavigationMeta("Global Settings", settingsAxisBoth, settingsOwnerRoot),
	settingsRootTabProjectValue:        settingsNavigationMeta("Project Settings", settingsAxisBoth, settingsOwnerRoot),
	settingsSectionProject:             settingsNavigationMeta("Project Picker", settingsAxisGlobal, settingsOwnerRoot),
	settingsSectionGlobalHooks:         settingsNavigationMeta("Hooks", settingsAxisGlobal, settingsOwnerRoot),
	settingsSectionProjectHooks:        settingsNavigationMeta("Hooks", settingsAxisProject, settingsOwnerRoot),
	settingsSectionProjectConfig:       settingsNavigationMeta("Project recipe", settingsAxisProject, settingsOwnerRoot),
	settingsSectionProjectTrust:        settingsNavigationMeta("Trust", settingsAxisProject, settingsOwnerRoot),
	settingsSectionEffectiveMerge:      settingsNavigationMeta("Effective merge view", settingsAxisProject, settingsOwnerRoot),
	settingsSectionGlobalTheme:         settingsNavigationMeta("Theme", settingsAxisGlobal, settingsOwnerRoot),
	settingsSectionProjectSessionState: settingsNavigationMeta("Session State", settingsAxisProject, settingsOwnerRoot),
	settingsSectionAI:                  settingsNavigationMeta("AI Settings", settingsAxisGlobal, settingsOwnerRoot),
	settingsSectionNotifications:       settingsNavigationMeta("Notifications", settingsAxisGlobal, settingsOwnerRoot),
	settingsSectionStatusbar:           settingsNavigationMeta("Appearance", settingsAxisGlobal, settingsOwnerRoot),
	settingsSectionSessionState:        settingsNavigationMeta("Session State", settingsAxisGlobal, settingsOwnerRoot),
	settingsSectionKeybindings:         settingsNavigationMeta("Keybindings", settingsAxisGlobal, settingsOwnerRoot),
	settingsSectionLabs:                settingsNavigationMeta("Labs", settingsAxisGlobal, settingsOwnerRoot),
	settingsSectionAbout:               settingsNavigationMeta("About", settingsAxisGlobal, settingsOwnerRoot),
	settingsProjectAdd:                 settingsActionMeta("Add Project", settingsAxisGlobal, settingsOwnerProjectPicker),
	settingsProjectPins:                settingsNavigationMeta("Pinned Projects", settingsAxisGlobal, settingsOwnerProjectPicker),
	settingsProjectRootManage:          settingsNavigationMeta("Project Root", settingsAxisGlobal, settingsOwnerProjectPicker),
	settingsProjdirClear:               settingsActionMeta("Clear Project Root", settingsAxisGlobal, settingsOwnerProject),
	settingsProjdirSetCurrent:          settingsActionMeta("Use Current Project as Root", settingsAxisGlobal, settingsOwnerProject),
	settingsProjdirSetTyped:            settingsActionMeta("Set Project Root", settingsAxisGlobal, settingsOwnerProject),
	settingsWorkdirAdd:                 settingsActionMeta("Add Workdir", settingsAxisGlobal, settingsOwnerProjectPicker),
	settingsWorkdirList:                settingsNavigationMeta("Workdirs", settingsAxisGlobal, settingsOwnerProjectPicker),
	settingsWorkdirTyped:               settingsActionMeta("Type Workdir", settingsAxisGlobal, settingsOwnerProject),
	settingsKeybindingsBindings:        settingsNavigationMeta("Keybindings", settingsAxisGlobal, settingsOwnerKeybindings),
	settingsKeybindingsDiagnostic:      settingsNavigationMeta("Keybinding Diagnostic", settingsAxisGlobal, settingsOwnerKeybindings),
	settingsKeybindingsProbe:           settingsNavigationMeta("Keybinding Probe", settingsAxisGlobal, settingsOwnerKeybindings),
	settingsKeybindingsInit:            settingsNavigationMeta("Keybinding Init", settingsAxisGlobal, settingsOwnerKeybindings),
	settingsAIDefaultMode:              settingsNavigationMeta("Default split mode", settingsAxisGlobal, settingsOwnerAI),
	settingsAIEnabledAgents:            settingsNavigationMeta("Enabled agents", settingsAxisGlobal, settingsOwnerAI),
	settingsAIResumePicker:             settingsNavigationMeta("Resume picker", settingsAxisGlobal, settingsOwnerAI),
	settingsAIResumePickerLimit:        settingsNavigationMeta("Resume picker limit", settingsAxisGlobal, settingsOwnerAI),
	settingsAIResumePickerDepth:        settingsNavigationMeta("Resume picker depth", settingsAxisGlobal, settingsOwnerAI),
	settingsAINotifyDiagnostics:        settingsNavigationMeta("AI notify diagnostics", settingsAxisGlobal, settingsOwnerAI),
	settingsNotificationsDesktop:       settingsNavigationMeta("Desktop notification settings", settingsAxisGlobal, settingsOwnerNotifications),
	settingsNotificationsAIDedupe:      settingsNavigationMeta("AI notification dedupe", settingsAxisGlobal, settingsOwnerNotifications),
	settingsNotificationsDelivery:      settingsNavigationMeta("Delivery sources", settingsAxisGlobal, settingsOwnerNotifications),
	settingsNotificationsHookActions:   settingsNavigationMeta("Hook quiet policy", settingsAxisGlobal, settingsOwnerNotifications),
	settingsAppearanceLanguage:         settingsNavigationMeta("Language / Locale", settingsAxisGlobal, settingsOwnerAppearance),
	settingsNativeKeysToggle:           settingsActionMeta("Native macOS keybindings", settingsAxisGlobal, settingsOwnerKeybindings),
	settingsLabsProjectHooks:           settingsNavigationMeta("Project Hooks", settingsAxisGlobal, settingsOwnerLabs),
	settingsSessionStateDelete:         settingsActionMeta("Delete session snapshot", settingsAxisGlobal, settingsOwnerSessionState),
	settingsUpdateApply:                settingsActionMeta("Update Now", settingsAxisGlobal, settingsOwnerAbout),
	settingsUpdateCheck:                settingsActionMeta("Check Updates", settingsAxisGlobal, settingsOwnerAbout),
	settingsWelcomeShow:                settingsNavigationMeta("Welcome", settingsAxisGlobal, settingsOwnerAbout),
	settingsQuitOpen:                   settingsNavigationMeta("Quit projmux", settingsAxisGlobal, settingsOwnerAbout),
}

var settingsEntryPrefixCatalog = []struct {
	prefix string
	meta   settingsEntryMeta
}{
	{settingsActionPrefixAI, settingsActionMeta("AI Settings", settingsAxisGlobal, settingsOwnerAI)},
	{settingsActionPrefixAIEnabledAgent, settingsActionMeta("Enabled agents", settingsAxisGlobal, settingsOwnerAI)},
	{settingsActionPrefixAINotifyDiagnostic, settingsNavigationMeta("AI notify diagnostics", settingsAxisGlobal, settingsOwnerNotifications)},
	{settingsActionPrefixAINotifyCommand, settingsActionMeta("AI notify diagnostic command", settingsAxisGlobal, settingsOwnerAINotifyDiagnostics)},
	{settingsActionPrefixAIBadgeStyle, settingsActionMeta("AI badge style", settingsAxisGlobal, settingsOwnerAppearance)},
	{settingsActionPrefixDesktopNotifyMode, settingsActionMeta("Desktop notification mode", settingsAxisGlobal, settingsOwnerNotifications)},
	{settingsActionPrefixAINotifyDedupe, settingsActionMeta("AI notification dedupe", settingsAxisGlobal, settingsOwnerNotifications)},
	{settingsActionPrefixAIResumeLimit, settingsActionMeta("Resume picker", settingsAxisGlobal, settingsOwnerAI)},
	{settingsActionPrefixAIResumeDepth, settingsActionMeta("Resume picker depth", settingsAxisGlobal, settingsOwnerAI)},
	{settingsActionPrefixAIHookProvider, settingsNavigationMeta("Hook quiet policy", settingsAxisGlobal, settingsOwnerNotifications)},
	{settingsActionPrefixAIHookEvent, settingsNavigationMeta("Hook quiet policy", settingsAxisGlobal, settingsOwnerNotifications)},
	{settingsActionPrefixAIHookSet, settingsActionMeta("Hook quiet policy", settingsAxisGlobal, settingsOwnerNotifications)},
	{settingsActionPrefixHooks, settingsActionMeta("Project hook policy", settingsAxisGlobal, settingsOwnerLabs)},
	{settingsActionPrefixLiveResources, settingsActionMeta("Live system resources", settingsAxisGlobal, settingsOwnerLabs)},
	{settingsActionPrefixHookAdd, settingsActionMeta("Hook maker - add", settingsAxisBoth, settingsOwnerHooks)},
	{settingsActionPrefixHookEdit, settingsActionMeta("Hook maker - edit", settingsAxisBoth, settingsOwnerHooks)},
	{settingsActionPrefixHookRemove, settingsActionMeta("Hook maker - remove", settingsAxisBoth, settingsOwnerHooks)},
	{settingsActionPrefixHookView, settingsNavigationMeta("Hook maker - view", settingsAxisBoth, settingsOwnerHooks)},
	{settingsActionPrefixKeymap, settingsActionMeta("Keybindings", settingsAxisGlobal, settingsOwnerKeybindings)},
	{settingsActionPrefixLocale, settingsActionMeta("Language / Locale", settingsAxisGlobal, settingsOwnerAppearance)},
	{settingsActionPrefixPicker, settingsActionMeta("Picker backend compatibility", settingsAxisGlobal, settingsOwnerGeneric)},
	{settingsActionPrefixProjectConfig, settingsActionMeta("Project recipe", settingsAxisProject, settingsOwnerProject)},
	{settingsActionPrefixWelcome, settingsNavigationMeta("Welcome", settingsAxisGlobal, settingsOwnerAbout)},
	{settingsActionPrefixTrust, settingsActionMeta("Trust", settingsAxisProject, settingsOwnerProject)},
	{settingsActionPrefixProjdir, settingsActionMeta("Project Root", settingsAxisGlobal, settingsOwnerProject)},
	{settingsActionPrefixSessionState, settingsActionMeta("Session State", settingsAxisGlobal, settingsOwnerSessionState)},
	{settingsActionPrefixStatusbar, settingsActionMeta("Appearance", settingsAxisGlobal, settingsOwnerAppearance)},
	{settingsActionPrefixSwitch, settingsActionMeta("Pinned Projects", settingsAxisGlobal, settingsOwnerProject)},
	{settingsActionPrefixTheme, settingsActionMeta("Theme", settingsAxisBoth, settingsOwnerTheme)},
	{settingsActionPrefixUpdate, settingsActionMeta("About", settingsAxisGlobal, settingsOwnerAbout)},
	{settingsActionPrefixWorkdir, settingsActionMeta("Workdirs", settingsAxisGlobal, settingsOwnerProject)},
}

func settingsEntryMetaForValue(value string) (settingsEntryMeta, bool) {
	if meta, ok := settingsEntryCatalog[value]; ok {
		return meta, true
	}
	for _, candidate := range settingsEntryPrefixCatalog {
		if strings.HasPrefix(value, candidate.prefix) {
			return candidate.meta, true
		}
	}
	return settingsEntryMeta{}, false
}

func validateSettingsEntryContracts(options intpickercompat.Options) error {
	for _, entry := range options.Entries {
		value := strings.TrimSpace(entry.Value)
		if value == "" {
			continue
		}
		meta, ok := settingsEntryMetaForValue(value)
		if !ok || meta.Owner == settingsOwnerNone || !settingsEntryOwnerHandles(meta.Owner, value) {
			return fmt.Errorf("settings UI %q entry value %q has no owner handler contract", options.UI, value)
		}
		switch meta.Kind {
		case settingsEntryNavigation, settingsEntryActionable, settingsEntryPassive:
		default:
			return fmt.Errorf("settings UI %q entry value %q has no interaction kind", options.UI, value)
		}
	}
	return nil
}

// settingsEntryOwnerHandles is the closed reachability table shared by the
// runtime guard and tests. It mirrors the owner-loop switch boundaries rather
// than trusting an arbitrary owner label in catalog metadata.
func settingsEntryOwnerHandles(owner settingsEntryOwner, value string) bool {
	switch owner {
	case settingsOwnerPassiveLoop:
		return value == settingsBackValue || value == settingsNoopValue
	case settingsOwnerRoot:
		switch value {
		case settingsRootTabGlobalValue, settingsRootTabProjectValue,
			settingsSectionProject, settingsSectionGlobalHooks, settingsSectionProjectHooks,
			settingsSectionProjectConfig, settingsSectionProjectTrust, settingsSectionEffectiveMerge,
			settingsSectionGlobalTheme, settingsSectionProjectSessionState, settingsSectionAI,
			settingsSectionNotifications, settingsSectionStatusbar, settingsSectionSessionState,
			settingsSectionKeybindings, settingsSectionLabs, settingsSectionAbout:
			return true
		}
	case settingsOwnerGeneric:
		return strings.HasPrefix(value, settingsActionPrefixPicker)
	case settingsOwnerProjectPicker:
		switch value {
		case settingsProjectAdd, settingsProjectPins, settingsProjectRootManage, settingsWorkdirAdd, settingsWorkdirList:
			return true
		}
	case settingsOwnerAI:
		return value == settingsAIDefaultMode || value == settingsAIEnabledAgents ||
			value == settingsAIResumePicker || value == settingsAIResumePickerLimit ||
			value == settingsAIResumePickerDepth || value == settingsAINotifyDiagnostics ||
			strings.HasPrefix(value, settingsActionPrefixAI) ||
			strings.HasPrefix(value, settingsActionPrefixAIEnabledAgent) ||
			strings.HasPrefix(value, settingsActionPrefixAIResumeLimit) ||
			strings.HasPrefix(value, settingsActionPrefixAIResumeDepth)
	case settingsOwnerNotifications:
		return value == settingsNotificationsDesktop || value == settingsNotificationsAIDedupe ||
			value == settingsNotificationsDelivery || value == settingsNotificationsHookActions ||
			strings.HasPrefix(value, settingsActionPrefixAINotifyDiagnostic) ||
			strings.HasPrefix(value, settingsActionPrefixDesktopNotifyMode) ||
			strings.HasPrefix(value, settingsActionPrefixAINotifyDedupe) ||
			strings.HasPrefix(value, settingsActionPrefixAIHookProvider) ||
			strings.HasPrefix(value, settingsActionPrefixAIHookEvent) ||
			strings.HasPrefix(value, settingsActionPrefixAIHookSet)
	case settingsOwnerAppearance:
		return value == settingsAppearanceLanguage || strings.HasPrefix(value, settingsActionPrefixAIBadgeStyle) ||
			strings.HasPrefix(value, settingsActionPrefixLocale) || strings.HasPrefix(value, settingsActionPrefixStatusbar)
	case settingsOwnerSessionState:
		return value == settingsSessionStateDelete || strings.HasPrefix(value, settingsActionPrefixSessionState)
	case settingsOwnerKeybindings:
		return value == settingsKeybindingsBindings || value == settingsKeybindingsDiagnostic ||
			value == settingsKeybindingsProbe || value == settingsKeybindingsInit ||
			value == settingsNativeKeysToggle || strings.HasPrefix(value, settingsActionPrefixKeymap)
	case settingsOwnerLabs:
		return value == settingsLabsProjectHooks || strings.HasPrefix(value, settingsActionPrefixHooks) ||
			strings.HasPrefix(value, settingsActionPrefixLiveResources)
	case settingsOwnerAbout:
		return value == settingsUpdateApply || value == settingsUpdateCheck || value == settingsWelcomeShow ||
			value == settingsQuitOpen || strings.HasPrefix(value, settingsActionPrefixUpdate) ||
			strings.HasPrefix(value, settingsActionPrefixWelcome)
	case settingsOwnerHooks:
		return strings.HasPrefix(value, settingsActionPrefixHookAdd) || strings.HasPrefix(value, settingsActionPrefixHookEdit) ||
			strings.HasPrefix(value, settingsActionPrefixHookRemove) || strings.HasPrefix(value, settingsActionPrefixHookView)
	case settingsOwnerProject:
		return value == settingsProjdirClear || value == settingsProjdirSetCurrent || value == settingsProjdirSetTyped ||
			value == settingsWorkdirTyped || strings.HasPrefix(value, settingsActionPrefixProjectConfig) ||
			strings.HasPrefix(value, settingsActionPrefixTrust) || strings.HasPrefix(value, settingsActionPrefixProjdir) ||
			strings.HasPrefix(value, settingsActionPrefixSwitch) || strings.HasPrefix(value, settingsActionPrefixWorkdir)
	case settingsOwnerTheme:
		return strings.HasPrefix(value, settingsActionPrefixTheme)
	case settingsOwnerAINotifyDiagnostics:
		return strings.HasPrefix(value, settingsActionPrefixAINotifyCommand)
	}
	return false
}

const (
	settingsBackValue                      = "__settings_back__"
	settingsNoopValue                      = "__settings_noop__"
	settingsRootTabGlobalValue             = "__settings_tab_global__"
	settingsRootTabProjectValue            = "__settings_tab_project__"
	settingsSectionAI                      = "section:ai"
	settingsSectionGlobalHooks             = "section:hooks-global"
	settingsSectionProjectHooks            = "section:hooks-project"
	settingsSectionProjectConfig           = "section:project-config"
	settingsSectionProjectTrust            = "section:project-trust"
	settingsSectionEffectiveMerge          = "section:effective-merge"
	settingsSectionGlobalTheme             = "section:theme-global"
	settingsSectionProjectSessionState     = "section:project-sessionstate"
	settingsSectionKeybindings             = "section:keybindings"
	settingsSectionProject                 = "section:project-picker"
	settingsSectionNotifications           = "section:notifications"
	settingsSectionStatusbar               = "section:statusbar"
	settingsSectionSessionState            = "section:sessionstate"
	settingsSectionLabs                    = "section:labs"
	settingsSectionAbout                   = "section:about"
	settingsActionPrefixAI                 = "ai:"
	settingsActionPrefixAIEnabledAgent     = "ai-enabled-agent:"
	settingsActionPrefixAIBadgeStyle       = "ai-badge-style:"
	settingsActionPrefixAINotifyDiagnostic = "ai-notify:"
	settingsActionPrefixAINotifyCommand    = "ai-notify-command:"
	settingsActionPrefixAINotifyDedupe     = "ai-notify-dedupe:"
	settingsActionPrefixAIResumeLimit      = "ai-resume-limit:"
	settingsActionPrefixAIResumeDepth      = "ai-resume-depth:"
	settingsActionPrefixAIHookProvider     = "ai-hook-provider:"
	settingsActionPrefixAIHookEvent        = "ai-hook-event:"
	settingsActionPrefixAIHookSet          = "ai-hook-set:"
	settingsActionPrefixDesktopNotifyMode  = "desktop-notify-mode:"
	settingsActionPrefixHooks              = "project-hooks:"
	settingsActionPrefixLiveResources      = "live-resources:"
	settingsActionPrefixKeymap             = "keymap:"
	settingsActionPrefixLocale             = "locale:"
	settingsActionPrefixPicker             = "picker-backend:"
	settingsActionPrefixProjectConfig      = "project-config:"
	settingsActionPrefixWelcome            = "welcome:"
	settingsActionPrefixTrust              = "trust:"
	settingsActionPrefixProjdir            = "projdir:"
	settingsActionPrefixSessionState       = "sessionstate:"
	settingsActionPrefixStatusbar          = "statusbar-decoration:"
	settingsActionPrefixSwitch             = "switch:"
	settingsActionPrefixTheme              = "theme:"
	settingsActionPrefixUpdate             = "update:"
	settingsActionPrefixWorkdir            = "workdir:"
	settingsActionPrefixQuit               = "quit:"
	settingsProjectAdd                     = "project:add"
	settingsProjectPins                    = "project:pins"
	settingsProjectRootManage              = "project-root:manage"
	settingsProjdirClear                   = "projdir:clear"
	settingsProjdirSetCurrent              = "projdir:set-current"
	settingsProjdirSetTyped                = "projdir:set-typed"
	settingsUpdateApply                    = "update:apply"
	settingsUpdateCheck                    = "update:check"
	settingsQuitOpen                       = "quit:open"
	settingsWorkdirAdd                     = "workdir:add"
	settingsWorkdirList                    = "workdir:list"
	settingsWorkdirTyped                   = "workdir:typed"
	settingsKeybindingsBindings            = "keybindings:bindings"
	settingsKeybindingsDiagnostic          = "keybindings:diagnostic"
	settingsKeybindingsProbe               = "keybindings:probe"
	settingsKeybindingsInit                = "keybindings:init"
	settingsAIDefaultMode                  = "ai-default-mode"
	settingsAIEnabledAgents                = "ai-enabled-agents"
	settingsAIResumePicker                 = "ai-resume-picker"
	settingsAIResumePickerLimit            = "ai-resume-picker-limit"
	settingsAIResumePickerDepth            = "ai-resume-picker-depth"
	settingsAINotifyDiagnostics            = "ai-notify-diagnostics"
	settingsNotificationsDesktop           = "notifications:desktop"
	settingsNotificationsAIDedupe          = "notifications:ai-dedupe"
	settingsNotificationsDelivery          = "notifications:delivery"
	settingsNotificationsHookActions       = "notifications:hook-actions"
	settingsAppearanceLanguage             = "appearance:language"
	settingsNativeKeysToggle               = "native-keys:toggle"
	settingsLabsProjectHooks               = "labs:project-hooks"
	settingsSessionStateDelete             = "sessionstate:delete"
	settingsWelcomeShow                    = "welcome:show"
	settingsKeymapFieldPlain               = "plain"
	settingsKeymapFieldKeys                = "keys"
	settingsKeymapFieldPrefix              = "prefix"
)

func (c *settingsCommand) sectionOptions(section string) (intpickercompat.Options, error) {
	switch section {
	case settingsSectionAI:
		return intpickercompat.Options{
			UI:         "settings-ai",
			Entries:    c.aiRootEntries(),
			Title:      "AI Settings",
			Prompt:     "Settings > AI Settings > ",
			Footer:     projmuxFooter("Enter: open  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		}, nil
	case settingsSectionNotifications:
		return intpickercompat.Options{
			UI:         "settings-notifications",
			Entries:    c.notificationsEntries(),
			Title:      "Notifications - Delivery and desktop controls",
			Prompt:     "Settings > Notifications > ",
			Footer:     projmuxFooter("Enter: open  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		}, nil
	case settingsSectionGlobalHooks:
		return intpickercompat.Options{
			UI:         "settings-hooks-global",
			Entries:    c.globalHookEntries(),
			Title:      "Hooks - Global lifecycle hook paths",
			Prompt:     "Settings > Hooks > ",
			Footer:     projmuxFooter("Enter: edit/add  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		}, nil
	case settingsSectionProjectHooks:
		ctx := c.resolveSettingsProjectContext()
		return intpickercompat.Options{
			UI:         "settings-hooks-project",
			Entries:    c.projectHookEntries(ctx),
			Title:      "Hooks - Project lifecycle hook paths",
			Prompt:     "Settings > Project > Hooks > ",
			Footer:     projmuxFooter("Enter: edit/add  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		}, nil
	case settingsSectionProject:
		return intpickercompat.Options{
			UI:         "settings-project-picker",
			Entries:    c.projectPickerEntries(),
			Title:      "Project Picker - Project roots, workdirs, and pinned projects",
			Prompt:     "Settings > Project Picker > ",
			Footer:     projmuxFooter("Enter: apply  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		}, nil
	case settingsSectionStatusbar:
		ctx := c.resolveSettingsProjectContext()
		return intpickercompat.Options{
			UI:         "settings-statusbar",
			Entries:    c.statusbarEntries(),
			Title:      "Appearance - AI badge and icon decoration",
			TitleChips: settingsPassiveRootTabChipsLocale(settingsRootTabGlobal, ctx.hasProject(), c.locale()),
			Prompt:     "Settings > Appearance > ",
			Footer:     projmuxFooter("Enter: open  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		}, nil
	case settingsSectionSessionState:
		return intpickercompat.Options{
			UI:         "settings-sessionstate",
			Entries:    c.sessionStateEntries(),
			Title:      "Session State - Restore and autosave controls",
			Prompt:     "Settings > Session State > ",
			Footer:     projmuxFooter("Enter: apply  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		}, nil
	case settingsSectionProjectSessionState:
		return intpickercompat.Options{
			UI:         "settings-project-sessionstate",
			Entries:    c.projectSessionStateEntries(),
			Title:      c.projectSessionStateTitle(),
			Prompt:     "Settings > Project > Session State > ",
			Footer:     projmuxFooter("Enter: apply  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		}, nil
	case settingsSectionKeybindings:
		return c.keybindingsOptions(settingsKeybindingsBindings), nil
	case settingsSectionLabs:
		return intpickercompat.Options{
			UI:         "settings-labs",
			Entries:    c.labsEntries(),
			Title:      "Labs - Experimental features",
			Prompt:     "Settings > Labs > ",
			Footer:     projmuxFooter("Enter: apply  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		}, nil
	case settingsSectionAbout:
		return intpickercompat.Options{
			UI:         "settings-about",
			Entries:    c.aboutEntries(),
			Title:      "About - Version, updates, key setup",
			Prompt:     "Settings > About > ",
			Footer:     projmuxFooter("Enter: action  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		}, nil
	default:
		return intpickercompat.Options{}, fmt.Errorf("unknown settings section: %s", section)
	}
}
