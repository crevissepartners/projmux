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
	Name     string
	LabelKey string
	Axis     SettingsAxis
	Kind     settingsEntryKind
	Owner    settingsEntryOwner
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

type settingsDirectionalIntent uint8

const (
	settingsDirectionalStay settingsDirectionalIntent = iota
	settingsDirectionalForward
	settingsDirectionalBack
)

// settingsDirectionalIntentFor is the shared Settings arrow-key policy. Right
// follows only catalogued navigation rows (the explicit Back row is not a
// forward destination); actionable and passive rows stay in the current View.
// Left follows the actual picker boundary: a rendered Back row means the View
// has one parent, while its absence identifies a root or transient input.
func settingsDirectionalIntentFor(key, value string, hasParent bool) settingsDirectionalIntent {
	switch strings.TrimSpace(key) {
	case "right":
		value = strings.TrimSpace(value)
		meta, ok := settingsEntryMetaForValue(value)
		if ok && meta.Kind == settingsEntryNavigation && value != settingsBackValue {
			return settingsDirectionalForward
		}
	case "left":
		if hasParent {
			return settingsDirectionalBack
		}
	}
	return settingsDirectionalStay
}

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

func settingsActionMeta(name, labelKey string, axis SettingsAxis, owner settingsEntryOwner) settingsEntryMeta {
	return settingsEntryMeta{Name: name, LabelKey: labelKey, Axis: axis, Kind: settingsEntryActionable, Owner: owner}
}

func settingsNavigationMeta(name, labelKey string, axis SettingsAxis, owner settingsEntryOwner) settingsEntryMeta {
	return settingsEntryMeta{Name: name, LabelKey: labelKey, Axis: axis, Kind: settingsEntryNavigation, Owner: owner}
}

func settingsEntryMetaFromNode(node settingsNavNode) settingsEntryMeta {
	label, key := node.entryLabel()
	return settingsEntryMeta{
		Name:     label,
		LabelKey: string(key),
		Axis:     node.Axis,
		Kind:     node.interactionKind(),
		Owner:    node.Owner,
	}
}

// settingsDynamicEntryCatalog is the bounded authority for values whose
// provider, path, event, or chord suffix is supplied at runtime. Static values
// never appear here and are resolved exclusively through settingsNodeCatalog.
var settingsDynamicEntryCatalog = []struct {
	prefix string
	nodeID string
	meta   settingsEntryMeta
}{
	{settingsAppearanceAgentUsageProviderPrefix, settingsNavStatusBar + ".agent-usage-hud.provider", settingsNavigationMeta("Agent Usage HUD provider", "settings.node.agent_usage_hud", settingsAxisGlobal, settingsOwnerAppearance)},
	{settingsActionPrefixAI, "", settingsActionMeta("Default launch target", "settings.text.default_launch_target", settingsAxisGlobal, settingsOwnerAI)},
	{settingsActionPrefixAIEnabledAgent, "", settingsActionMeta("Enabled providers", "settings.text.enabled_providers", settingsAxisGlobal, settingsOwnerAI)},
	{settingsActionPrefixAINotifyDiagnostic, settingsNavNotifyProviders + ".item", settingsNavigationMeta("Provider Integrations", "settings.text.provider_integrations", settingsAxisGlobal, settingsOwnerNotifications)},
	{settingsActionPrefixAINotifyCommand, "", settingsActionMeta("AI notify diagnostic command", "settings.text.provider_integrations", settingsAxisGlobal, settingsOwnerAINotifyDiagnostics)},
	{settingsActionPrefixAINotifyCheck, "", settingsActionMeta("AI notify diagnostic check", "settings.text.provider_integrations", settingsAxisGlobal, settingsOwnerAINotifyDiagnostics)},
	{settingsActionPrefixAIBadgeStyle, "", settingsActionMeta("Agent attention badge style", "settings.text.agent_attention_badge_style", settingsAxisGlobal, settingsOwnerAppearance)},
	{settingsActionPrefixDesktopNotifyMode, "", settingsActionMeta("Delivery mode", "settings.text.delivery_mode", settingsAxisGlobal, settingsOwnerNotifications)},
	{settingsActionPrefixAINotifyDedupe, "", settingsActionMeta("Dedupe window", "settings.text.dedupe_window", settingsAxisGlobal, settingsOwnerNotifications)},
	{settingsActionPrefixAIResumeLimit, "", settingsActionMeta("Agent Resume Picker", "settings.text.agent_resume_picker", settingsAxisGlobal, settingsOwnerAI)},
	{settingsActionPrefixAIResumeDepth, "", settingsActionMeta("Scan depth", "settings.text.ai_resume_picker_depth_row", settingsAxisGlobal, settingsOwnerAI)},
	{settingsActionPrefixAIHookProvider, settingsNavNotifyAgentEvents + ".item", settingsNavigationMeta("Agent event behavior", "settings.text.agent_event_behavior", settingsAxisGlobal, settingsOwnerNotifications)},
	{settingsActionPrefixAIHookEvent, settingsNavNotifyAgentEvents + ".item.event", settingsNavigationMeta("Agent event behavior", "settings.text.agent_event_behavior", settingsAxisGlobal, settingsOwnerNotifications)},
	{settingsActionPrefixAIHookSet, "", settingsActionMeta("Agent event behavior", "settings.text.agent_event_behavior", settingsAxisGlobal, settingsOwnerNotifications)},
	{settingsActionPrefixAISemanticEvent, settingsNavNotifyAgentEvents + ".item.event", settingsNavigationMeta("Native semantic policy", "settings.text.native_semantic_policy", settingsAxisGlobal, settingsOwnerNotifications)},
	{settingsActionPrefixAISemanticSet, "", settingsActionMeta("Native semantic policy", "settings.text.native_semantic_policy", settingsAxisGlobal, settingsOwnerNotifications)},
	{settingsActionPrefixHooks, "", settingsActionMeta("Project automation policy", "settings.text.project_automation_policy", settingsAxisGlobal, settingsOwnerAutomation)},
	{settingsActionPrefixLiveResources, "", settingsActionMeta("Resources", "settings.keybinding.resources.name", settingsAxisGlobal, settingsOwnerAppearance)},
	{settingsActionPrefixHUDVisibility, "", settingsActionMeta("Status Bar visibility", "settings.text.status_bar", settingsAxisGlobal, settingsOwnerAppearance)},
	{settingsActionPrefixHookEvent + hookScopeGlobal + ":", settingsNavAutomationLifecycle + ".event", settingsNavigationMeta("Automation event", "settings.text.automation", settingsAxisGlobal, settingsOwnerAutomation)},
	{settingsActionPrefixHookEvent + hookScopeProject + ":", settingsNavProjectHooks + ".lifecycle.event", settingsNavigationMeta("Project automation event", "settings.text.project_automation_policy", settingsAxisProject, settingsOwnerProjectAutomation)},
	{settingsActionPrefixKeymapSurface, settingsNavKeybindings + "." + keyBindingCategorySurfaces + ".surface", settingsNavigationMeta("Keybindings surface", "settings.text.keybindings", settingsAxisGlobal, settingsOwnerKeybindings)},
	{settingsActionPrefixWorkdirItem, settingsNavProjectsExtraRoots + ".item", settingsNavigationMeta("Additional discovery roots", "settings.text.additional_discovery_roots", settingsAxisGlobal, settingsOwnerProjectPicker)},
	{settingsActionPrefixPinItem, settingsNavProjectsPins + ".item", settingsNavigationMeta("Pinned Projects", "settings.text.pinned_projects", settingsAxisGlobal, settingsOwnerProjectPicker)},
	{settingsActionPrefixCandidatePinItem, settingsNavProjectsCandidates + ".item", settingsNavigationMeta("Candidate Pins", "settings.text.candidate_pins", settingsAxisGlobal, settingsOwnerProjectPicker)},
	{settingsActionPrefixSessionStateSidebarStartup, "", settingsActionMeta("Closed Project startup", "settings.text.closed_project_startup", settingsAxisGlobal, settingsOwnerProjectPicker)},
	{settingsActionPrefixRuntimeDiagnostics, "", settingsActionMeta("Runtime diagnostics", "picker.runtime.title", settingsAxisGlobal, settingsOwnerProjectPicker)},
	{settingsActionPrefixHookAdd, "", settingsActionMeta("Hook maker - add", "settings.text.hooks", settingsAxisBoth, settingsOwnerHooks)},
	{settingsActionPrefixHookEdit, "", settingsActionMeta("Hook maker - edit", "settings.text.hooks", settingsAxisBoth, settingsOwnerHooks)},
	{settingsActionPrefixHookRemove, "", settingsActionMeta("Hook maker - remove", "settings.text.hooks", settingsAxisBoth, settingsOwnerHooks)},
	{settingsActionPrefixHookView + hookScopeGlobal + ":", settingsNavAutomationLifecycle + ".event", settingsNavigationMeta("Hook maker - view", "settings.text.hooks", settingsAxisGlobal, settingsOwnerHooks)},
	{settingsActionPrefixHookView + hookScopeProject + ":", settingsNavProjectHooks + ".lifecycle.event", settingsNavigationMeta("Hook maker - view", "settings.text.hooks", settingsAxisProject, settingsOwnerHooks)},
	{settingsActionPrefixKeymap, "", settingsActionMeta("Keybindings", "settings.text.keybindings", settingsAxisGlobal, settingsOwnerKeybindings)},
	{settingsActionPrefixLocale, "", settingsActionMeta("Language / Locale", "settings.text.language_locale", settingsAxisGlobal, settingsOwnerAppearance)},
	{settingsActionPrefixTrust, "", settingsActionMeta("Trust", "settings.text.trust", settingsAxisProject, settingsOwnerProject)},
	{settingsActionPrefixProjdir, "", settingsActionMeta("Primary discovery root", "settings.text.primary_discovery_root", settingsAxisGlobal, settingsOwnerProject)},
	{settingsActionPrefixSessionState, "", settingsActionMeta("Snapshots", "settings.text.snapshots", settingsAxisGlobal, settingsOwnerSessionState)},
	{settingsActionPrefixStatusbar, "", settingsActionMeta("Status Bar", "settings.text.status_bar", settingsAxisGlobal, settingsOwnerAppearance)},
	{settingsActionPrefixSwitch, "", settingsActionMeta("Pinned Projects", "settings.text.pinned_projects", settingsAxisGlobal, settingsOwnerProject)},
	{settingsActionPrefixTheme, "", settingsActionMeta("Theme", "settings.text.theme", settingsAxisBoth, settingsOwnerTheme)},
	{settingsActionPrefixUpdate, "", settingsActionMeta("About", "settings.text.about", settingsAxisGlobal, settingsOwnerAbout)},
	{settingsActionPrefixWorkdir, "", settingsActionMeta("Additional discovery roots", "settings.text.additional_discovery_roots", settingsAxisGlobal, settingsOwnerProject)},
}

func settingsEntryMetaForValue(value string) (settingsEntryMeta, bool) {
	value = strings.TrimSpace(value)
	if node, ok := settingsNavByValue(value); ok && !node.Dynamic {
		return settingsEntryMetaFromNode(node), true
	}
	return settingsDynamicEntryMetaForValue(value)
}

func settingsDynamicEntryMetaForValue(value string) (settingsEntryMeta, bool) {
	for _, candidate := range settingsDynamicEntryCatalog {
		if !strings.HasPrefix(value, candidate.prefix) {
			continue
		}
		if len(value) == len(candidate.prefix) {
			return settingsEntryMeta{}, false
		}
		if candidate.meta.Kind == settingsEntryNavigation && !settingsDynamicNavigationAuthorized(candidate.nodeID, candidate.meta) {
			return settingsEntryMeta{}, false
		}
		return candidate.meta, true
	}
	return settingsEntryMeta{}, false
}

// settingsDynamicNavigationAuthorized prevents a runtime prefix from becoming
// an independent navigation graph. Every navigation matcher must project onto
// a declared dynamic node in the static IA catalog with the same axis.
func settingsDynamicNavigationAuthorized(nodeID string, meta settingsEntryMeta) bool {
	node, ok := settingsNavByID(nodeID)
	if !ok || !node.Dynamic || node.Axis != meta.Axis {
		return false
	}
	return node.Kind == settingsNavView || node.Kind == settingsNavChoice
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
		case settingsProjectAdd, settingsProjectPins, settingsProjectCandidatePins,
			settingsProjectRootManage, settingsWorkdirAdd,
			settingsWorkdirList, settingsProjectsSidebar, settingsSessionStateSidebarStartupPickerDetail,
			settingsRuntimeDiagnosticsVisibilityDetail:
			return true
		}
		return strings.HasPrefix(value, settingsActionPrefixWorkdirItem) ||
			strings.HasPrefix(value, settingsActionPrefixPinItem) ||
			strings.HasPrefix(value, settingsActionPrefixCandidatePinItem) ||
			strings.HasPrefix(value, settingsActionPrefixSessionStateSidebarStartup) ||
			strings.HasPrefix(value, settingsActionPrefixRuntimeDiagnostics)
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
			strings.HasPrefix(value, settingsActionPrefixAIHookSet) ||
			strings.HasPrefix(value, settingsActionPrefixAISemanticEvent) ||
			strings.HasPrefix(value, settingsActionPrefixAISemanticSet)
	case settingsOwnerAppearance:
		return value == settingsAppearanceLanguage || value == settingsAppearanceTheme ||
			value == settingsAppearanceStatusBar ||
			value == settingsAppearanceAgentUsageHUD ||
			strings.HasPrefix(value, settingsAppearanceAgentUsageProviderPrefix) ||
			strings.HasPrefix(value, settingsActionPrefixAIBadgeStyle) ||
			strings.HasPrefix(value, settingsActionPrefixLocale) ||
			strings.HasPrefix(value, settingsActionPrefixStatusbar) ||
			strings.HasPrefix(value, settingsActionPrefixHUDVisibility) ||
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
			value == settingsWorkdirTyped ||
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
	settingsSectionProjectTrust            = "section:project-trust"
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
	settingsActionPrefixAISemanticEvent    = "ai-semantic-event:"
	settingsActionPrefixAISemanticSet      = "ai-semantic-set:"
	settingsActionPrefixDesktopNotifyMode  = "desktop-notify-mode:"
	settingsActionPrefixHooks              = "project-hooks:"
	settingsActionPrefixLiveResources      = "live-resources:"
	settingsActionPrefixHUDVisibility      = "statusbar-visibility:"
	settingsActionPrefixKeymap             = "keymap:"
	settingsActionPrefixKeymapCategory     = "keymap-category:"
	settingsActionPrefixKeymapSurface      = "keymap-surface:"
	settingsActionPrefixWorkdirItem        = "workdir-item:"
	settingsActionPrefixPinItem            = "pin-item:"
	settingsActionPrefixCandidatePinItem   = "candidate-pin-item:"
	settingsActionPrefixHookEvent          = "hook-event:"
	// settingsActionPrefixRuntimeDiagnostics owns the Projects sidebar
	// Runtime diagnostics visibility choice. It is its own spelling rather
	// than a `sessionstate:` reuse because the preference is a sidebar
	// presentation policy and touches no snapshot state.
	settingsActionPrefixRuntimeDiagnostics = "runtime-diagnostics:"
	// settingsActionPrefixSessionStateSidebarStartup keeps the shipped
	// `sessionstate:` config/action spelling while the row itself moves under
	// Projects > Project Sidebar. Only the destination and the label change.
	settingsActionPrefixSessionStateSidebarStartup = settingsActionPrefixSessionState + "sidebar-startup:"
	settingsActionPrefixLocale                     = "locale:"
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
	settingsProjectCandidatePins                   = "project:candidate-pins"
	settingsProjectRootManage                      = "project-root:manage"
	settingsProjdirClear                           = "projdir:clear"
	settingsProjdirSetCurrent                      = "projdir:set-current"
	settingsProjdirSetTyped                        = "projdir:set-typed"
	settingsUpdateApply                            = "update:apply"
	settingsUpdateCheck                            = "update:check"
	settingsUpdateReleaseChannel                   = "update:release-channel"
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
	settingsAppearanceAgentUsageHUD                = "appearance:status-bar:agent-usage-hud"
	settingsAppearanceAgentUsageProviderPrefix     = "appearance:status-bar:agent-usage-provider:"
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
