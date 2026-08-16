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
	settingsOwnerAutomation
	settingsOwnerProjectAutomation
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

// settingsEntryCatalog maps a rendered picker value to its interaction kind,
// scope axis and owning loop. Retired routes are absent by design: a value with
// no entry here cannot be rendered at all, which is what keeps the removed
// Labs, Theme root, Project recipe and Effective merge destinations from
// coming back as a hidden redirect.
var settingsEntryCatalog = map[string]settingsEntryMeta{
	settingsBackValue:                              settingsNavigationMeta("Back", settingsAxisBoth, settingsOwnerPassiveLoop),
	settingsNoopValue:                              settingsPassiveMeta("Info or disabled", settingsAxisBoth),
	settingsRootTabGlobalValue:                     settingsNavigationMeta("Global Settings", settingsAxisBoth, settingsOwnerRoot),
	settingsRootTabProjectValue:                    settingsNavigationMeta("Project Settings", settingsAxisBoth, settingsOwnerRoot),
	settingsSectionProject:                         settingsNavigationMeta("Projects", settingsAxisGlobal, settingsOwnerRoot),
	settingsSectionAutomation:                      settingsNavigationMeta("Automation", settingsAxisGlobal, settingsOwnerRoot),
	settingsSectionProjectAutomation:               settingsNavigationMeta("Automation", settingsAxisProject, settingsOwnerRoot),
	settingsSectionGlobalHooks:                     settingsNavigationMeta("Projmux session lifecycle", settingsAxisGlobal, settingsOwnerAutomation),
	settingsSectionProjectHooks:                    settingsNavigationMeta("Project hooks", settingsAxisProject, settingsOwnerProjectAutomation),
	settingsSectionProjectTrust:                    settingsNavigationMeta("Trust", settingsAxisProject, settingsOwnerProjectAutomation),
	settingsSectionProjectSessionState:             settingsNavigationMeta("Snapshots", settingsAxisProject, settingsOwnerRoot),
	settingsSectionAI:                              settingsNavigationMeta("AI", settingsAxisGlobal, settingsOwnerRoot),
	settingsSectionNotifications:                   settingsNavigationMeta("Notifications", settingsAxisGlobal, settingsOwnerRoot),
	settingsSectionStatusbar:                       settingsNavigationMeta("Appearance", settingsAxisGlobal, settingsOwnerRoot),
	settingsSectionSessionState:                    settingsNavigationMeta("Snapshots", settingsAxisGlobal, settingsOwnerRoot),
	settingsSectionKeybindings:                     settingsNavigationMeta("Keybindings", settingsAxisGlobal, settingsOwnerRoot),
	settingsSectionAbout:                           settingsNavigationMeta("About", settingsAxisGlobal, settingsOwnerRoot),
	settingsProjectAdd:                             settingsActionMeta("Select Project to pin", settingsAxisGlobal, settingsOwnerProjectPicker),
	settingsProjectPins:                            settingsNavigationMeta("Pinned Projects", settingsAxisGlobal, settingsOwnerProjectPicker),
	settingsProjectRootManage:                      settingsNavigationMeta("Primary discovery root", settingsAxisGlobal, settingsOwnerProjectPicker),
	settingsProjectsSidebar:                        settingsNavigationMeta("Project Sidebar", settingsAxisGlobal, settingsOwnerProjectPicker),
	settingsProjdirClear:                           settingsActionMeta("Clear saved root", settingsAxisGlobal, settingsOwnerProject),
	settingsProjdirSetCurrent:                      settingsActionMeta("Use current directory", settingsAxisGlobal, settingsOwnerProject),
	settingsProjdirSetTyped:                        settingsActionMeta("Enter path", settingsAxisGlobal, settingsOwnerProject),
	settingsWorkdirAdd:                             settingsActionMeta("Add current directory", settingsAxisGlobal, settingsOwnerProjectPicker),
	settingsWorkdirList:                            settingsNavigationMeta("Additional discovery roots", settingsAxisGlobal, settingsOwnerProjectPicker),
	settingsWorkdirTyped:                           settingsActionMeta("Add path", settingsAxisGlobal, settingsOwnerProject),
	settingsKeybindingsBindings:                    settingsNavigationMeta("Keybindings", settingsAxisGlobal, settingsOwnerKeybindings),
	settingsKeybindingsDiagnostic:                  settingsNavigationMeta("Keybinding Diagnostic", settingsAxisGlobal, settingsOwnerKeybindings),
	settingsKeybindingsProbe:                       settingsNavigationMeta("Keybinding Probe", settingsAxisGlobal, settingsOwnerKeybindings),
	settingsAIDefaultMode:                          settingsNavigationMeta("Default launch target", settingsAxisGlobal, settingsOwnerAI),
	settingsAIEnabledAgents:                        settingsNavigationMeta("Enabled providers", settingsAxisGlobal, settingsOwnerAI),
	settingsAIResumePicker:                         settingsNavigationMeta("Agent Resume Picker", settingsAxisGlobal, settingsOwnerAI),
	settingsAIResumePickerLimit:                    settingsNavigationMeta("Picker limit", settingsAxisGlobal, settingsOwnerAI),
	settingsAIResumePickerDepth:                    settingsNavigationMeta("Scan depth", settingsAxisGlobal, settingsOwnerAI),
	settingsNotificationsDesktop:                   settingsNavigationMeta("Desktop delivery", settingsAxisGlobal, settingsOwnerNotifications),
	settingsNotificationsAIDedupe:                  settingsNavigationMeta("Dedupe window", settingsAxisGlobal, settingsOwnerNotifications),
	settingsNotificationsDelivery:                  settingsNavigationMeta("Provider Integrations", settingsAxisGlobal, settingsOwnerNotifications),
	settingsNotificationsProviders:                 settingsNavigationMeta("Provider Integrations", settingsAxisGlobal, settingsOwnerNotifications),
	settingsNotificationsTmuxSource:                settingsNavigationMeta("tmux event source", settingsAxisGlobal, settingsOwnerNotifications),
	settingsNotificationsHookActions:               settingsNavigationMeta("Agent event behavior", settingsAxisGlobal, settingsOwnerNotifications),
	settingsAppearanceLanguage:                     settingsNavigationMeta("Language / Locale", settingsAxisGlobal, settingsOwnerAppearance),
	settingsAppearanceTheme:                        settingsNavigationMeta("Theme", settingsAxisGlobal, settingsOwnerAppearance),
	settingsAppearanceStatusBar:                    settingsNavigationMeta("Status Bar", settingsAxisGlobal, settingsOwnerAppearance),
	settingsAutomationLifecycle:                    settingsNavigationMeta("Projmux session lifecycle", settingsAxisGlobal, settingsOwnerAutomation),
	settingsAutomationSendNoti:                     settingsNavigationMeta("After notification queued", settingsAxisGlobal, settingsOwnerAutomation),
	settingsProjectAutomationLifecycle:             settingsNavigationMeta("Session lifecycle", settingsAxisProject, settingsOwnerProjectAutomation),
	settingsProjectAutomationSendNoti:              settingsNavigationMeta("After notification queued", settingsAxisProject, settingsOwnerProjectAutomation),
	settingsNativeKeysToggle:                       settingsActionMeta("Native macOS keybindings", settingsAxisGlobal, settingsOwnerKeybindings),
	settingsSessionStateDelete:                     settingsActionMeta("Delete snapshot", settingsAxisGlobal, settingsOwnerSessionState),
	settingsSessionStateSidebarStartupPickerDetail: settingsNavigationMeta("Closed Project startup", settingsAxisGlobal, settingsOwnerProjectPicker),
	settingsAboutUpdates:                           settingsNavigationMeta("Updates", settingsAxisGlobal, settingsOwnerAbout),
	settingsUpdateApply:                            settingsActionMeta("Update now", settingsAxisGlobal, settingsOwnerAbout),
	settingsUpdateCheck:                            settingsActionMeta("Check for updates", settingsAxisGlobal, settingsOwnerAbout),
	settingsWelcomeShow:                            settingsNavigationMeta("Welcome", settingsAxisGlobal, settingsOwnerAbout),
	settingsQuitOpen:                               settingsNavigationMeta("Quit Projmux", settingsAxisGlobal, settingsOwnerAbout),
}

var settingsEntryPrefixCatalog = []struct {
	prefix string
	meta   settingsEntryMeta
}{
	{settingsActionPrefixAI, settingsActionMeta("Default launch target", settingsAxisGlobal, settingsOwnerAI)},
	{settingsActionPrefixAIEnabledAgent, settingsActionMeta("Enabled providers", settingsAxisGlobal, settingsOwnerAI)},
	{settingsActionPrefixAINotifyDiagnostic, settingsNavigationMeta("Provider Integrations", settingsAxisGlobal, settingsOwnerNotifications)},
	{settingsActionPrefixAINotifyCommand, settingsActionMeta("AI notify diagnostic command", settingsAxisGlobal, settingsOwnerAINotifyDiagnostics)},
	{settingsActionPrefixAINotifyCheck, settingsActionMeta("AI notify diagnostic check", settingsAxisGlobal, settingsOwnerAINotifyDiagnostics)},
	{settingsActionPrefixAIBadgeStyle, settingsActionMeta("Agent attention badge style", settingsAxisGlobal, settingsOwnerAppearance)},
	{settingsActionPrefixDesktopNotifyMode, settingsActionMeta("Delivery mode", settingsAxisGlobal, settingsOwnerNotifications)},
	{settingsActionPrefixAINotifyDedupe, settingsActionMeta("Dedupe window", settingsAxisGlobal, settingsOwnerNotifications)},
	{settingsActionPrefixAIResumeLimit, settingsActionMeta("Agent Resume Picker", settingsAxisGlobal, settingsOwnerAI)},
	{settingsActionPrefixAIResumeDepth, settingsActionMeta("Scan depth", settingsAxisGlobal, settingsOwnerAI)},
	{settingsActionPrefixAIHookProvider, settingsNavigationMeta("Agent event behavior", settingsAxisGlobal, settingsOwnerNotifications)},
	{settingsActionPrefixAIHookEvent, settingsNavigationMeta("Agent event behavior", settingsAxisGlobal, settingsOwnerNotifications)},
	{settingsActionPrefixAIHookSet, settingsActionMeta("Agent event behavior", settingsAxisGlobal, settingsOwnerNotifications)},
	{settingsActionPrefixHooks, settingsActionMeta("Project automation policy", settingsAxisGlobal, settingsOwnerAutomation)},
	{settingsActionPrefixLiveResources, settingsActionMeta("Resources", settingsAxisGlobal, settingsOwnerAppearance)},
	{settingsActionPrefixHookEvent + hookScopeGlobal + ":", settingsNavigationMeta("Automation event", settingsAxisGlobal, settingsOwnerAutomation)},
	{settingsActionPrefixHookEvent + hookScopeProject + ":", settingsNavigationMeta("Project automation event", settingsAxisProject, settingsOwnerProjectAutomation)},
	{settingsActionPrefixKeymapCategory, settingsNavigationMeta("Keybindings category", settingsAxisGlobal, settingsOwnerKeybindings)},
	{settingsActionPrefixKeymapSurface, settingsNavigationMeta("Keybindings surface", settingsAxisGlobal, settingsOwnerKeybindings)},
	{settingsActionPrefixWorkdirItem, settingsNavigationMeta("Additional discovery roots", settingsAxisGlobal, settingsOwnerProjectPicker)},
	{settingsActionPrefixPinItem, settingsNavigationMeta("Pinned Projects", settingsAxisGlobal, settingsOwnerProjectPicker)},
	{settingsActionPrefixSessionStateSidebarStartup, settingsActionMeta("Closed Project startup", settingsAxisGlobal, settingsOwnerProjectPicker)},
	{settingsActionPrefixHookAdd, settingsActionMeta("Hook maker - add", settingsAxisBoth, settingsOwnerHooks)},
	{settingsActionPrefixHookEdit, settingsActionMeta("Hook maker - edit", settingsAxisBoth, settingsOwnerHooks)},
	{settingsActionPrefixHookRemove, settingsActionMeta("Hook maker - remove", settingsAxisBoth, settingsOwnerHooks)},
	{settingsActionPrefixHookView, settingsNavigationMeta("Hook maker - view", settingsAxisBoth, settingsOwnerHooks)},
	{settingsActionPrefixKeymap, settingsActionMeta("Keybindings", settingsAxisGlobal, settingsOwnerKeybindings)},
	{settingsActionPrefixLocale, settingsActionMeta("Language / Locale", settingsAxisGlobal, settingsOwnerAppearance)},
	{settingsActionPrefixProjectConfig, settingsActionMeta("Project recipe", settingsAxisProject, settingsOwnerProject)},
	{settingsActionPrefixWelcome, settingsNavigationMeta("Welcome", settingsAxisGlobal, settingsOwnerAbout)},
	{settingsActionPrefixTrust, settingsActionMeta("Trust", settingsAxisProject, settingsOwnerProject)},
	{settingsActionPrefixProjdir, settingsActionMeta("Primary discovery root", settingsAxisGlobal, settingsOwnerProject)},
	{settingsActionPrefixSessionState, settingsActionMeta("Snapshots", settingsAxisGlobal, settingsOwnerSessionState)},
	{settingsActionPrefixStatusbar, settingsActionMeta("Status Bar", settingsAxisGlobal, settingsOwnerAppearance)},
	{settingsActionPrefixSwitch, settingsActionMeta("Pinned Projects", settingsAxisGlobal, settingsOwnerProject)},
	{settingsActionPrefixTheme, settingsActionMeta("Theme", settingsAxisBoth, settingsOwnerTheme)},
	{settingsActionPrefixUpdate, settingsActionMeta("About", settingsAxisGlobal, settingsOwnerAbout)},
	{settingsActionPrefixWorkdir, settingsActionMeta("Additional discovery roots", settingsAxisGlobal, settingsOwnerProject)},
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
			settingsSectionProject, settingsSectionAI, settingsSectionNotifications,
			settingsSectionAutomation, settingsSectionStatusbar, settingsSectionSessionState,
			settingsSectionKeybindings, settingsSectionAbout,
			settingsSectionProjectAutomation, settingsSectionProjectSessionState:
			return true
		}
	case settingsOwnerProjectPicker:
		switch value {
		case settingsProjectAdd, settingsProjectPins, settingsProjectRootManage, settingsWorkdirAdd,
			settingsWorkdirList, settingsProjectsSidebar, settingsSessionStateSidebarStartupPickerDetail:
			return true
		}
		return strings.HasPrefix(value, settingsActionPrefixWorkdirItem) ||
			strings.HasPrefix(value, settingsActionPrefixPinItem) ||
			strings.HasPrefix(value, settingsActionPrefixSessionStateSidebarStartup)
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
			value == settingsNotificationsProviders || value == settingsNotificationsTmuxSource ||
			strings.HasPrefix(value, settingsActionPrefixAINotifyDiagnostic) ||
			strings.HasPrefix(value, settingsActionPrefixAINotifyCheck) ||
			strings.HasPrefix(value, settingsActionPrefixDesktopNotifyMode) ||
			strings.HasPrefix(value, settingsActionPrefixAINotifyDedupe) ||
			strings.HasPrefix(value, settingsActionPrefixAIHookProvider) ||
			strings.HasPrefix(value, settingsActionPrefixAIHookEvent) ||
			strings.HasPrefix(value, settingsActionPrefixAIHookSet)
	case settingsOwnerAppearance:
		return value == settingsAppearanceLanguage || value == settingsAppearanceTheme ||
			value == settingsAppearanceStatusBar ||
			strings.HasPrefix(value, settingsActionPrefixAIBadgeStyle) ||
			strings.HasPrefix(value, settingsActionPrefixLocale) ||
			strings.HasPrefix(value, settingsActionPrefixStatusbar) ||
			strings.HasPrefix(value, settingsActionPrefixLiveResources)
	case settingsOwnerSessionState:
		return value == settingsSessionStateDelete || strings.HasPrefix(value, settingsActionPrefixSessionState)
	case settingsOwnerKeybindings:
		return value == settingsKeybindingsBindings || value == settingsKeybindingsDiagnostic ||
			value == settingsKeybindingsProbe ||
			value == settingsNativeKeysToggle || strings.HasPrefix(value, settingsActionPrefixKeymap) ||
			strings.HasPrefix(value, settingsActionPrefixKeymapCategory) ||
			strings.HasPrefix(value, settingsActionPrefixKeymapSurface)
	case settingsOwnerAutomation:
		return value == settingsSectionGlobalHooks || value == settingsAutomationLifecycle ||
			value == settingsAutomationSendNoti ||
			strings.HasPrefix(value, settingsActionPrefixHooks) ||
			strings.HasPrefix(value, settingsActionPrefixHookEvent)
	case settingsOwnerProjectAutomation:
		return value == settingsSectionProjectHooks || value == settingsSectionProjectTrust ||
			value == settingsProjectAutomationLifecycle || value == settingsProjectAutomationSendNoti ||
			strings.HasPrefix(value, settingsActionPrefixHookEvent)
	case settingsOwnerAbout:
		return value == settingsUpdateApply || value == settingsUpdateCheck || value == settingsWelcomeShow ||
			value == settingsQuitOpen || value == settingsAboutUpdates ||
			strings.HasPrefix(value, settingsActionPrefixUpdate) ||
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
		return strings.HasPrefix(value, settingsActionPrefixAINotifyCommand) ||
			strings.HasPrefix(value, settingsActionPrefixAINotifyCheck)
	}
	return false
}

const (
	settingsBackValue                      = "__settings_back__"
	settingsNoopValue                      = "__settings_noop__"
	settingsRootTabGlobalValue             = "__settings_tab_global__"
	settingsRootTabProjectValue            = "__settings_tab_project__"
	settingsSectionAI                      = "section:ai"
	settingsSectionAutomation              = "section:automation"
	settingsSectionProjectAutomation       = "section:project-automation"
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
	settingsActionPrefixAINotifyCheck      = "ai-notify-check:"
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
	settingsActionPrefixKeymapCategory     = "keymap-category:"
	settingsActionPrefixKeymapSurface      = "keymap-surface:"
	settingsActionPrefixWorkdirItem        = "workdir-item:"
	settingsActionPrefixPinItem            = "pin-item:"
	settingsActionPrefixHookEvent          = "hook-event:"
	// settingsActionPrefixSessionStateSidebarStartup keeps the shipped
	// `sessionstate:` config/action spelling while the row itself moves under
	// Projects > Project Sidebar. Only the destination and the label change.
	settingsActionPrefixSessionStateSidebarStartup = settingsActionPrefixSessionState + "sidebar-startup:"
	settingsActionPrefixLocale                     = "locale:"
	settingsActionPrefixProjectConfig              = "project-config:"
	settingsActionPrefixWelcome                    = "welcome:"
	settingsActionPrefixTrust                      = "trust:"
	settingsActionPrefixProjdir                    = "projdir:"
	settingsActionPrefixSessionState               = "sessionstate:"
	settingsActionPrefixStatusbar                  = "statusbar-decoration:"
	settingsActionPrefixSwitch                     = "switch:"
	settingsActionPrefixTheme                      = "theme:"
	settingsActionPrefixUpdate                     = "update:"
	settingsActionPrefixWorkdir                    = "workdir:"
	settingsActionPrefixQuit                       = "quit:"
	settingsProjectAdd                             = "project:add"
	settingsProjectPins                            = "project:pins"
	settingsProjectRootManage                      = "project-root:manage"
	settingsProjdirClear                           = "projdir:clear"
	settingsProjdirSetCurrent                      = "projdir:set-current"
	settingsProjdirSetTyped                        = "projdir:set-typed"
	settingsUpdateApply                            = "update:apply"
	settingsUpdateCheck                            = "update:check"
	settingsQuitOpen                               = "quit:open"
	settingsWorkdirAdd                             = "workdir:add"
	settingsWorkdirList                            = "workdir:list"
	settingsWorkdirTyped                           = "workdir:typed"
	settingsKeybindingsBindings                    = "keybindings:bindings"
	settingsKeybindingsDiagnostic                  = "keybindings:diagnostic"
	settingsKeybindingsProbe                       = "keybindings:probe"
	settingsAIDefaultMode                          = "ai-default-mode"
	settingsAIEnabledAgents                        = "ai-enabled-agents"
	settingsAIResumePicker                         = "ai-resume-picker"
	settingsAIResumePickerLimit                    = "ai-resume-picker-limit"
	settingsAIResumePickerDepth                    = "ai-resume-picker-depth"
	settingsAINotifyDiagnostics                    = "ai-notify-diagnostics"
	settingsNotificationsDesktop                   = "notifications:desktop"
	settingsNotificationsAIDedupe                  = "notifications:ai-dedupe"
	settingsNotificationsDelivery                  = "notifications:delivery"
	settingsNotificationsHookActions               = "notifications:hook-actions"
	settingsAppearanceLanguage                     = "appearance:language"
	settingsAppearanceTheme                        = "appearance:theme"
	settingsAppearanceStatusBar                    = "appearance:status-bar"
	settingsAutomationLifecycle                    = "automation:lifecycle"
	settingsAutomationSendNoti                     = "automation:send-noti"
	settingsAutomationProjectPolicy                = "automation:project-policy"
	settingsProjectAutomationLifecycle             = "project-automation:lifecycle"
	settingsProjectAutomationSendNoti              = "project-automation:send-noti"
	settingsProjectsSidebar                        = "projects:sidebar"
	settingsNotificationsProviders                 = "notifications:provider-integrations"
	settingsNotificationsTmuxSource                = "notifications:tmux-event-source"
	settingsAboutUpdates                           = "about:updates"
	settingsNativeKeysToggle                       = "native-keys:toggle"
	settingsLabsProjectHooks                       = "labs:project-hooks"
	settingsSessionStateDelete                     = "sessionstate:delete"
	settingsWelcomeShow                            = "welcome:show"
	settingsKeymapFieldPlain                       = "plain"
	settingsKeymapFieldKeys                        = "keys"
	settingsKeymapFieldPrefix                      = "prefix"
)

func (c *settingsCommand) sectionOptions(section string) (intpickercompat.Options, error) {
	switch section {
	case settingsSectionAI:
		return intpickercompat.Options{
			UI:         "settings-ai",
			Entries:    c.aiRootEntries(),
			Title:      "AI - Launch target, Providers, Agent Resume Picker",
			Prompt:     "Settings > AI > ",
			Footer:     projmuxFooter("Enter: open  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		}, nil
	case settingsSectionNotifications:
		return intpickercompat.Options{
			UI:         "settings-notifications",
			Entries:    c.notificationsEntries(),
			Title:      "Notifications - Delivery, integrations, and Agent events",
			Prompt:     "Settings > Notifications > ",
			Footer:     projmuxFooter("Enter: open  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		}, nil
	case settingsSectionAutomation:
		return intpickercompat.Options{
			UI:         "settings-automation",
			Entries:    c.automationEntries(),
			Title:      "Automation - Projmux lifecycle scripts and project policy",
			Prompt:     "Settings > Automation > ",
			Footer:     projmuxFooter("Enter: open  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		}, nil
	case settingsSectionProjectAutomation:
		return intpickercompat.Options{
			UI:         "settings-project-automation",
			Entries:    c.projectAutomationEntries(),
			Title:      "Project automation - Trust and project hooks",
			Prompt:     "Settings > Project > Automation > ",
			Footer:     projmuxFooter("Enter: open  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		}, nil
	case settingsSectionProjectHooks:
		return intpickercompat.Options{
			UI:         "settings-hooks-project",
			Entries:    c.projectHookEntries(c.resolveSettingsProjectContext()),
			Title:      "Project automation - Project hooks",
			Prompt:     "Settings > Project > Automation > Project hooks > ",
			Footer:     projmuxFooter("Enter: open  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		}, nil
	case settingsSectionProject:
		return intpickercompat.Options{
			UI:         "settings-project-picker",
			Entries:    c.projectPickerEntries(),
			Title:      "Projects - Discovery roots, pinned Projects, sidebar policy",
			Prompt:     "Settings > Projects > ",
			Footer:     projmuxFooter("Enter: back/open  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		}, nil
	case settingsSectionStatusbar:
		ctx := c.resolveSettingsProjectContext()
		return intpickercompat.Options{
			UI:         "settings-statusbar",
			Entries:    c.statusbarEntries(),
			Title:      "Appearance - Theme, Status Bar, language, Agent badge",
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
			Title:      "Snapshots - Auto-save and storage",
			Prompt:     "Settings > Snapshots > ",
			Footer:     projmuxFooter("Enter: apply  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		}, nil
	case settingsSectionProjectSessionState:
		return intpickercompat.Options{
			UI:         "settings-project-sessionstate",
			Entries:    c.projectSessionStateEntries(),
			Title:      c.projectSessionStateTitle(),
			Prompt:     "Settings > Project > Snapshots > ",
			Footer:     projmuxFooter("Enter: apply  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		}, nil
	case settingsSectionKeybindings:
		return c.keybindingsOptions(settingsKeybindingsBindings), nil
	case settingsSectionAbout:
		return intpickercompat.Options{
			UI:         "settings-about",
			Entries:    c.aboutEntries(),
			Title:      "About - Version, updates, welcome, and quit",
			Prompt:     "Settings > About > ",
			Footer:     projmuxFooter("Enter: action  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		}, nil
	default:
		return intpickercompat.Options{}, fmt.Errorf("unknown settings section: %s", section)
	}
}
