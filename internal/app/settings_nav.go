package app

import (
	"strings"
)

// settingsNavKind is the row affordance contract from the Settings
// container-first + progressive disclosure model. Exactly one kind is attached
// to every node, so a row can never be both a navigation boundary and a
// mutation.
//
//   - View    — a real navigation boundary (category, collection, multi-value
//     component or action). Entering a View never changes a saved value.
//   - State   — read-only current value / source / availability. Enter is a
//     no-op consumed by the owning loop.
//   - Toggle  — a simple, immediately reversible boolean inside its owning View.
//   - Choice  — a small enum/value row that opens a compact chooser.
//   - Edit    — a path/command/name change that needs typed input or a picker.
//   - Action  — check, copy, preview, test, update: an observable execution.
//   - Confirm — remove, reset, unbind, untrust, quit: confirmed before running.
type settingsNavKind string

const (
	settingsNavView    settingsNavKind = "View"
	settingsNavState   settingsNavKind = "State"
	settingsNavToggle  settingsNavKind = "Toggle"
	settingsNavChoice  settingsNavKind = "Choice"
	settingsNavEdit    settingsNavKind = "Edit"
	settingsNavAction  settingsNavKind = "Action"
	settingsNavConfirm settingsNavKind = "Confirm"
)

// settingsNavNode is one row in the target Settings information architecture.
//
// The catalog below is the single structural source of truth for the visible
// tree: the root/category entry builders render from it, the tree golden test
// renders from it, and the reachability tests assert that every rendered picker
// value maps back onto a node here. Adding a row to a Settings loop without a
// node is a test failure rather than a silent IA drift.
type settingsNavNode struct {
	// ID is the stable dotted path of the row. It is an internal structural
	// identifier: it is never rendered and never stored in user config.
	ID string
	// Parent is the owning node ID; the two scope roots have no parent.
	Parent string
	// Label is the English display literal. Localization resolves it through
	// the shared uiTextKeys catalog, so this is also the i18n registry key.
	Label string
	Kind  settingsNavKind
	Axis  SettingsAxis
	// Value is the picker entry value rendered for this row when the row is a
	// static entry. Collection items and rows whose value carries runtime data
	// (a path, a provider id, a chord) leave it empty and declare Dynamic.
	Value string
	// Dynamic marks a collection item template: the row stands for 0..N
	// runtime rows rather than one static entry.
	Dynamic bool
	// Note records a contract the row carries into later phases, such as the
	// canonical handler a row must reuse. It is rendered into the tree golden
	// so a contract change shows up as a diff.
	Note string
}

// settingsNavScopeGlobal and settingsNavScopeProject are the two scope roots.
const (
	settingsNavScopeGlobal  = "global"
	settingsNavScopeProject = "project"
)

// Node IDs referenced from the section loops. Only the nodes that a loop needs
// to address by name are given constants; the rest are addressed through their
// parent when rendering children.
const (
	settingsNavProjects            = "global.projects"
	settingsNavProjectsPrimaryRoot = "global.projects.primary-root"
	settingsNavProjectsExtraRoots  = "global.projects.additional-roots"
	settingsNavProjectsPins        = "global.projects.pinned"
	settingsNavProjectsSidebar     = "global.projects.sidebar"
	settingsNavAI                  = "global.ai"
	settingsNavAIProviders         = "global.ai.enabled-providers"
	settingsNavAIResumePicker      = "global.ai.resume-picker"
	settingsNavNotifications       = "global.notifications"
	settingsNavNotifyDesktop       = "global.notifications.desktop-delivery"
	settingsNavNotifyProviders     = "global.notifications.provider-integrations"
	settingsNavNotifyTmuxSource    = "global.notifications.tmux-event-source"
	settingsNavNotifyAgentEvents   = "global.notifications.agent-event-behavior"
	settingsNavAutomation          = "global.automation"
	settingsNavAutomationLifecycle = "global.automation.session-lifecycle"
	settingsNavAutomationSendNoti  = "global.automation.after-notification-queued"
	settingsNavAppearance          = "global.appearance"
	settingsNavAppearanceTheme     = "global.appearance.theme"
	settingsNavStatusBar           = "global.appearance.status-bar"
	settingsNavSnapshots           = "global.snapshots"
	settingsNavKeybindings         = "global.keybindings"
	settingsNavAbout               = "global.about"
	settingsNavProjectAutomation   = "project.automation"
	settingsNavProjectTrust        = "project.automation.trust"
	settingsNavProjectHooks        = "project.automation.project-hooks"
	settingsNavProjectSnapshots    = "project.snapshots"
)

// settingsNavCatalog is the target Settings navigation, in render order.
//
// Phase 0 cuts the whole visible tree over at once, so this table is the tree
// that ships. Two boundaries are deliberate rather than accidental:
//
//   - The Status Bar container is created here in its final position, but only
//     the component rows whose control exists today are listed. Per-component
//     `Visible [Toggle]` storage for the remaining components is a later slice,
//     and drawing those rows now would be exactly the empty placeholder the
//     acceptance criteria forbid. Later slices add rows inside this container;
//     they do not move the container.
//   - Keybinding action rows are declared as one dynamic template per category
//     rather than 45 static nodes, because the action catalog already owns the
//     per-action identity and this table must not become a second copy of it.
var settingsNavCatalog = []settingsNavNode{
	{ID: settingsNavScopeGlobal, Label: "Global", Kind: settingsNavView, Axis: settingsAxisGlobal},

	// Projects -------------------------------------------------------------
	{ID: settingsNavProjects, Parent: settingsNavScopeGlobal, Label: "Projects", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsSectionProject},

	{ID: settingsNavProjectsPrimaryRoot, Parent: settingsNavProjects, Label: "Primary discovery root", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsProjectRootManage},
	{ID: settingsNavProjectsPrimaryRoot + ".state", Parent: settingsNavProjectsPrimaryRoot, Label: "Effective / Saved / Source", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},
	{ID: settingsNavProjectsPrimaryRoot + ".use-current", Parent: settingsNavProjectsPrimaryRoot, Label: "Use current directory", Kind: settingsNavEdit, Axis: settingsAxisGlobal, Value: settingsProjdirSetCurrent},
	{ID: settingsNavProjectsPrimaryRoot + ".enter-path", Parent: settingsNavProjectsPrimaryRoot, Label: "Enter path", Kind: settingsNavEdit, Axis: settingsAxisGlobal, Value: settingsProjdirSetTyped},
	{ID: settingsNavProjectsPrimaryRoot + ".clear", Parent: settingsNavProjectsPrimaryRoot, Label: "Clear saved root", Kind: settingsNavConfirm, Axis: settingsAxisGlobal, Value: settingsProjdirClear},

	{ID: settingsNavProjectsExtraRoots, Parent: settingsNavProjects, Label: "Additional discovery roots", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsWorkdirList},
	{ID: settingsNavProjectsExtraRoots + ".state", Parent: settingsNavProjectsExtraRoots, Label: "Effective roots / Source", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},
	{ID: settingsNavProjectsExtraRoots + ".item", Parent: settingsNavProjectsExtraRoots, Label: "<Discovery root>", Kind: settingsNavView, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavProjectsExtraRoots + ".item.state", Parent: settingsNavProjectsExtraRoots + ".item", Label: "Root path / Source", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},
	{ID: settingsNavProjectsExtraRoots + ".item.remove", Parent: settingsNavProjectsExtraRoots + ".item", Label: "Remove discovery root", Kind: settingsNavConfirm, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavProjectsExtraRoots + ".add-current", Parent: settingsNavProjectsExtraRoots, Label: "Add current directory", Kind: settingsNavEdit, Axis: settingsAxisGlobal, Value: settingsWorkdirAdd},
	{ID: settingsNavProjectsExtraRoots + ".add-path", Parent: settingsNavProjectsExtraRoots, Label: "Add path", Kind: settingsNavEdit, Axis: settingsAxisGlobal, Value: settingsWorkdirTyped},

	{ID: settingsNavProjectsPins, Parent: settingsNavProjects, Label: "Pinned Projects", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsProjectPins},
	{ID: settingsNavProjectsPins + ".item", Parent: settingsNavProjectsPins, Label: "<Project>", Kind: settingsNavView, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavProjectsPins + ".item.state", Parent: settingsNavProjectsPins + ".item", Label: "Display name / Unique name / UID / Root / Condition / Missing since / Runtime / Source", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},
	{ID: settingsNavProjectsPins + ".item.rebind", Parent: settingsNavProjectsPins + ".item", Label: "Rebind Project root", Kind: settingsNavEdit, Axis: settingsAxisGlobal, Dynamic: true, Note: "canonical rebind project, same UID"},
	{ID: settingsNavProjectsPins + ".item.unpin", Parent: settingsNavProjectsPins + ".item", Label: "Unpin Project", Kind: settingsNavConfirm, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavProjectsPins + ".pin-current", Parent: settingsNavProjectsPins, Label: "Pin current Project", Kind: settingsNavEdit, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavProjectsPins + ".select", Parent: settingsNavProjectsPins, Label: "Select Project to pin", Kind: settingsNavEdit, Axis: settingsAxisGlobal, Value: settingsProjectAdd},

	{ID: settingsNavProjectsSidebar, Parent: settingsNavProjects, Label: "Project Sidebar", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsProjectsSidebar},
	{ID: settingsNavProjectsSidebar + ".closed-startup", Parent: settingsNavProjectsSidebar, Label: "Closed Project startup", Kind: settingsNavChoice, Axis: settingsAxisGlobal, Dynamic: true, Note: "Use Project topology / Ask for Snapshot or Project topology"},

	// AI -------------------------------------------------------------------
	{ID: settingsNavAI, Parent: settingsNavScopeGlobal, Label: "AI", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsSectionAI},
	{ID: settingsNavAI + ".launch-target", Parent: settingsNavAI, Label: "Default launch target", Kind: settingsNavChoice, Axis: settingsAxisGlobal, Value: settingsAIDefaultMode},
	{ID: settingsNavAIProviders, Parent: settingsNavAI, Label: "Enabled providers", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsAIEnabledAgents},
	{ID: settingsNavAIProviders + ".item", Parent: settingsNavAIProviders, Label: "<Provider>", Kind: settingsNavToggle, Axis: settingsAxisGlobal, Dynamic: true, Note: "Claude / Codex / Antigravity; availability and source badge"},
	{ID: settingsNavAIResumePicker, Parent: settingsNavAI, Label: "Agent Resume Picker", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsAIResumePicker},
	{ID: settingsNavAIResumePicker + ".state", Parent: settingsNavAIResumePicker, Label: "Effective behavior / Source / Eligible phases", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue, Note: "Offline, Failed"},
	{ID: settingsNavAIResumePicker + ".new-action", Parent: settingsNavAIResumePicker, Label: "New action label", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue, Note: "Create New Agent"},
	{ID: settingsNavAIResumePicker + ".limit", Parent: settingsNavAIResumePicker, Label: "Picker limit", Kind: settingsNavChoice, Axis: settingsAxisGlobal, Value: settingsAIResumePickerLimit},
	{ID: settingsNavAIResumePicker + ".depth", Parent: settingsNavAIResumePicker, Label: "Scan depth", Kind: settingsNavChoice, Axis: settingsAxisGlobal, Value: settingsAIResumePickerDepth},

	// Notifications --------------------------------------------------------
	{ID: settingsNavNotifications, Parent: settingsNavScopeGlobal, Label: "Notifications", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsSectionNotifications},
	{ID: settingsNavNotifyDesktop, Parent: settingsNavNotifications, Label: "Desktop delivery", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsNotificationsDesktop},
	{ID: settingsNavNotifyDesktop + ".state", Parent: settingsNavNotifyDesktop, Label: "Effective sender / Source / Availability", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},
	{ID: settingsNavNotifyDesktop + ".mode", Parent: settingsNavNotifyDesktop, Label: "Delivery mode", Kind: settingsNavChoice, Axis: settingsAxisGlobal, Dynamic: true, Note: "Off / Notify"},
	{ID: settingsNavNotifyDesktop + ".dedupe", Parent: settingsNavNotifyDesktop, Label: "Dedupe window", Kind: settingsNavChoice, Axis: settingsAxisGlobal, Value: settingsNotificationsAIDedupe},
	{ID: settingsNavNotifyDesktop + ".external", Parent: settingsNavNotifyDesktop, Label: "External desktop sender / Environment source / Fallback", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},

	{ID: settingsNavNotifyProviders, Parent: settingsNavNotifications, Label: "Provider Integrations", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsNotificationsProviders},
	{ID: settingsNavNotifyProviders + ".item", Parent: settingsNavNotifyProviders, Label: "<Provider>", Kind: settingsNavView, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavNotifyProviders + ".item.state", Parent: settingsNavNotifyProviders + ".item", Label: "Wiring status / Source / Conflict / Config path", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},
	{ID: settingsNavNotifyProviders + ".item.check", Parent: settingsNavNotifyProviders + ".item", Label: "Check integration", Kind: settingsNavAction, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavNotifyProviders + ".item.setup", Parent: settingsNavNotifyProviders + ".item", Label: "Copy install or remove command", Kind: settingsNavAction, Axis: settingsAxisGlobal, Dynamic: true},

	{ID: settingsNavNotifyTmuxSource, Parent: settingsNavNotifications, Label: "tmux event source", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsNotificationsTmuxSource},
	{ID: settingsNavNotifyTmuxSource + ".state", Parent: settingsNavNotifyTmuxSource, Label: "Bell wiring status / Source / Conflict", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},
	{ID: settingsNavNotifyTmuxSource + ".check", Parent: settingsNavNotifyTmuxSource, Label: "Check", Kind: settingsNavAction, Axis: settingsAxisGlobal, Dynamic: true},

	{ID: settingsNavNotifyAgentEvents, Parent: settingsNavNotifications, Label: "Agent event behavior", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsNotificationsHookActions},
	{ID: settingsNavNotifyAgentEvents + ".item", Parent: settingsNavNotifyAgentEvents, Label: "<Provider>", Kind: settingsNavView, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavNotifyAgentEvents + ".item.event", Parent: settingsNavNotifyAgentEvents + ".item", Label: "<event>", Kind: settingsNavChoice, Axis: settingsAxisGlobal, Dynamic: true, Note: "Default / Notify / State only / Quiet"},

	// Automation -----------------------------------------------------------
	{ID: settingsNavAutomation, Parent: settingsNavScopeGlobal, Label: "Automation", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsSectionAutomation},
	{ID: settingsNavAutomationLifecycle, Parent: settingsNavAutomation, Label: "Projmux session lifecycle", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsAutomationLifecycle},
	{ID: settingsNavAutomationLifecycle + ".event", Parent: settingsNavAutomationLifecycle, Label: "<lifecycle event>", Kind: settingsNavView, Axis: settingsAxisGlobal, Dynamic: true, Note: "Before session create / After session create / After session attach"},
	{ID: settingsNavAutomationLifecycle + ".event.state", Parent: settingsNavAutomationLifecycle + ".event", Label: "Command / Effective / Source", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},
	{ID: settingsNavAutomationLifecycle + ".event.edit", Parent: settingsNavAutomationLifecycle + ".event", Label: "Add or edit command", Kind: settingsNavEdit, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavAutomationLifecycle + ".event.remove", Parent: settingsNavAutomationLifecycle + ".event", Label: "Remove command", Kind: settingsNavConfirm, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavAutomationSendNoti, Parent: settingsNavAutomation, Label: "After notification queued", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsAutomationSendNoti},
	{ID: settingsNavAutomationSendNoti + ".state", Parent: settingsNavAutomationSendNoti, Label: "Command / Effective / Source", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},
	{ID: settingsNavAutomationSendNoti + ".edit", Parent: settingsNavAutomationSendNoti, Label: "Add or edit command", Kind: settingsNavEdit, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavAutomationSendNoti + ".remove", Parent: settingsNavAutomationSendNoti, Label: "Remove command", Kind: settingsNavConfirm, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavAutomation + ".project-policy", Parent: settingsNavAutomation, Label: "Project automation policy", Kind: settingsNavToggle, Axis: settingsAxisGlobal, Dynamic: true, Note: "trust and source badge"},

	// Appearance -----------------------------------------------------------
	{ID: settingsNavAppearance, Parent: settingsNavScopeGlobal, Label: "Appearance", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsSectionStatusbar},
	{ID: settingsNavAppearanceTheme, Parent: settingsNavAppearance, Label: "Theme", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsAppearanceTheme},
	{ID: settingsNavAppearanceTheme + ".preset", Parent: settingsNavAppearanceTheme, Label: "Preset", Kind: settingsNavChoice, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavAppearanceTheme + ".tokens", Parent: settingsNavAppearanceTheme, Label: "Tokens", Kind: settingsNavView, Axis: settingsAxisGlobal, Dynamic: true, Note: "Core / Surface / State / Chrome groups"},
	{ID: settingsNavAppearanceTheme + ".tokens.item", Parent: settingsNavAppearanceTheme + ".tokens", Label: "<token>", Kind: settingsNavView, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavAppearanceTheme + ".tokens.item.state", Parent: settingsNavAppearanceTheme + ".tokens.item", Label: "Effective / Saved / Source / Fallback / Preview", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},
	{ID: settingsNavAppearanceTheme + ".tokens.item.set", Parent: settingsNavAppearanceTheme + ".tokens.item", Label: "Set value", Kind: settingsNavEdit, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavAppearanceTheme + ".tokens.item.fallback", Parent: settingsNavAppearanceTheme + ".tokens.item", Label: "Use preset fallback", Kind: settingsNavChoice, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavAppearanceTheme + ".reset", Parent: settingsNavAppearanceTheme, Label: "Reset theme", Kind: settingsNavConfirm, Axis: settingsAxisGlobal, Dynamic: true},

	{ID: settingsNavStatusBar, Parent: settingsNavAppearance, Label: "Status Bar", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsAppearanceStatusBar},
	{ID: settingsNavStatusBar + ".notifications-hud", Parent: settingsNavStatusBar, Label: "Notifications HUD", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsActionPrefixStatusbar + string(statusbarDecorationTargetNotify)},
	{ID: settingsNavStatusBar + ".notifications-hud.state", Parent: settingsNavStatusBar + ".notifications-hud", Label: "Current / Source / Preview", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},
	{ID: settingsNavStatusBar + ".notifications-hud.visible", Parent: settingsNavStatusBar + ".notifications-hud", Label: "Visible", Kind: settingsNavToggle, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavStatusBar + ".notifications-hud.icon", Parent: settingsNavStatusBar + ".notifications-hud", Label: "Notification icon", Kind: settingsNavChoice, Axis: settingsAxisGlobal, Dynamic: true, Note: "Off / Symbol / Emoji"},
	{ID: settingsNavStatusBar + ".agent-usage-hud", Parent: settingsNavStatusBar, Label: "Agent Usage HUD", Kind: settingsNavToggle, Axis: settingsAxisGlobal, Dynamic: true, Note: "agent usage source / preview badge"},
	{ID: settingsNavStatusBar + ".project", Parent: settingsNavStatusBar, Label: "Project", Kind: settingsNavToggle, Axis: settingsAxisGlobal, Dynamic: true, Note: "metadata.name / runtime fallback source / preview badge"},
	{ID: settingsNavStatusBar + ".working-directory", Parent: settingsNavStatusBar, Label: "Working directory", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsActionPrefixStatusbar + string(statusbarDecorationTargetCwd), Note: "focused Pane cwd"},
	{ID: settingsNavStatusBar + ".working-directory.state", Parent: settingsNavStatusBar + ".working-directory", Label: "Current / Source / Preview", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},
	{ID: settingsNavStatusBar + ".working-directory.visible", Parent: settingsNavStatusBar + ".working-directory", Label: "Visible", Kind: settingsNavToggle, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavStatusBar + ".working-directory.icon", Parent: settingsNavStatusBar + ".working-directory", Label: "Icon", Kind: settingsNavChoice, Axis: settingsAxisGlobal, Dynamic: true, Note: "Off / Symbol / Emoji"},
	{ID: settingsNavStatusBar + ".git", Parent: settingsNavStatusBar, Label: "Git", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsActionPrefixStatusbar + string(statusbarDecorationTargetGit)},
	{ID: settingsNavStatusBar + ".git.state", Parent: settingsNavStatusBar + ".git", Label: "Current / Source / Preview", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},
	{ID: settingsNavStatusBar + ".git.visible", Parent: settingsNavStatusBar + ".git", Label: "Visible", Kind: settingsNavToggle, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavStatusBar + ".git.icon", Parent: settingsNavStatusBar + ".git", Label: "Icon", Kind: settingsNavChoice, Axis: settingsAxisGlobal, Dynamic: true, Note: "Off / Symbol / Emoji"},
	{ID: settingsNavStatusBar + ".resources", Parent: settingsNavStatusBar, Label: "Resources", Kind: settingsNavToggle, Axis: settingsAxisGlobal, Dynamic: true, Note: "platform / source / preview badge; enablement also stops sampling"},
	{ID: settingsNavStatusBar + ".clock", Parent: settingsNavStatusBar, Label: "Clock", Kind: settingsNavToggle, Axis: settingsAxisGlobal, Dynamic: true, Note: "source / preview badge"},
	{ID: settingsNavStatusBar + ".settings-launcher", Parent: settingsNavStatusBar, Label: "Settings launcher", Kind: settingsNavToggle, Axis: settingsAxisGlobal, Dynamic: true, Note: "mouse chip only; CLI and keybinding remain"},

	{ID: settingsNavAppearance + ".locale", Parent: settingsNavAppearance, Label: "Language / Locale", Kind: settingsNavChoice, Axis: settingsAxisGlobal, Value: settingsAppearanceLanguage},
	{ID: settingsNavAppearance + ".badge", Parent: settingsNavAppearance, Label: "Agent attention badge style", Kind: settingsNavChoice, Axis: settingsAxisGlobal, Value: settingsActionPrefixAIBadgeStyle},

	// Snapshots ------------------------------------------------------------
	{ID: settingsNavSnapshots, Parent: settingsNavScopeGlobal, Label: "Snapshots", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsSectionSessionState},
	{ID: settingsNavSnapshots + ".autosave", Parent: settingsNavSnapshots, Label: "Auto-save", Kind: settingsNavView, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavSnapshots + ".autosave.state", Parent: settingsNavSnapshots + ".autosave", Label: "Effective / Source / Storage", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},
	{ID: settingsNavSnapshots + ".autosave.enabled", Parent: settingsNavSnapshots + ".autosave", Label: "Enabled", Kind: settingsNavToggle, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavSnapshots + ".autosave.interval", Parent: settingsNavSnapshots + ".autosave", Label: "Interval", Kind: settingsNavChoice, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavSnapshots + ".storage", Parent: settingsNavSnapshots, Label: "Storage / Retention", Kind: settingsNavView, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavSnapshots + ".storage.state", Parent: settingsNavSnapshots + ".storage", Label: "Location / Effective retention / Source", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},

	// Keybindings ----------------------------------------------------------
	{ID: settingsNavKeybindings, Parent: settingsNavScopeGlobal, Label: "Keybindings", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsSectionKeybindings},
	{ID: settingsNavKeybindings + "." + keyBindingCategoryLaunch, Parent: settingsNavKeybindings, Label: keyBindingCategoryLaunchLabel, Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsActionPrefixKeymapCategory + keyBindingCategoryLaunch},
	{ID: settingsNavKeybindings + "." + keyBindingCategoryLaunch + ".action", Parent: settingsNavKeybindings + "." + keyBindingCategoryLaunch, Label: "<action detail>", Kind: settingsNavView, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavKeybindings + "." + keyBindingCategoryAgentPane, Parent: settingsNavKeybindings, Label: keyBindingCategoryAgentPaneLabel, Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsActionPrefixKeymapCategory + keyBindingCategoryAgentPane},
	{ID: settingsNavKeybindings + "." + keyBindingCategoryAgentPane + ".action", Parent: settingsNavKeybindings + "." + keyBindingCategoryAgentPane, Label: "<action detail>", Kind: settingsNavView, Axis: settingsAxisGlobal, Dynamic: true, Note: "explicit current Pane anchor"},
	{ID: settingsNavKeybindings + "." + keyBindingCategoryNavigation, Parent: settingsNavKeybindings, Label: keyBindingCategoryNavigationLabel, Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsActionPrefixKeymapCategory + keyBindingCategoryNavigation},
	{ID: settingsNavKeybindings + "." + keyBindingCategoryNavigation + ".action", Parent: settingsNavKeybindings + "." + keyBindingCategoryNavigation, Label: "<action detail>", Kind: settingsNavView, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavKeybindings + "." + keyBindingCategorySurfaces, Parent: settingsNavKeybindings, Label: keyBindingCategorySurfacesLabel, Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsActionPrefixKeymapCategory + keyBindingCategorySurfaces},
	{ID: settingsNavKeybindings + "." + keyBindingCategorySurfaces + ".surface", Parent: settingsNavKeybindings + "." + keyBindingCategorySurfaces, Label: "<surface>", Kind: settingsNavView, Axis: settingsAxisGlobal, Dynamic: true, Note: "Project Sidebar / Session Picker / Notification Sidebar / Settings"},
	{ID: settingsNavKeybindings + "." + keyBindingCategorySurfaces + ".surface.action", Parent: settingsNavKeybindings + "." + keyBindingCategorySurfaces + ".surface", Label: "<action detail>", Kind: settingsNavView, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavKeybindings + "." + keyBindingCategoryInput, Parent: settingsNavKeybindings, Label: keyBindingCategoryInputLabel, Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsActionPrefixKeymapCategory + keyBindingCategoryInput},
	{ID: settingsNavKeybindings + "." + keyBindingCategoryInput + ".native-macos", Parent: settingsNavKeybindings + "." + keyBindingCategoryInput, Label: "Native macOS keybindings", Kind: settingsNavToggle, Axis: settingsAxisGlobal, Value: settingsNativeKeysToggle},

	// About ----------------------------------------------------------------
	{ID: settingsNavAbout, Parent: settingsNavScopeGlobal, Label: "About", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsSectionAbout},
	{ID: settingsNavAbout + ".state", Parent: settingsNavAbout, Label: "Version / Source / Build", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},
	{ID: settingsNavAbout + ".updates", Parent: settingsNavAbout, Label: "Updates", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsAboutUpdates},
	{ID: settingsNavAbout + ".updates.state", Parent: settingsNavAbout + ".updates", Label: "Current / Latest / Installer / Release notes", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},
	{ID: settingsNavAbout + ".updates.check", Parent: settingsNavAbout + ".updates", Label: "Check for updates", Kind: settingsNavAction, Axis: settingsAxisGlobal, Value: settingsUpdateCheck},
	{ID: settingsNavAbout + ".updates.apply", Parent: settingsNavAbout + ".updates", Label: "Update now", Kind: settingsNavAction, Axis: settingsAxisGlobal, Value: settingsUpdateApply},
	{ID: settingsNavAbout + ".welcome", Parent: settingsNavAbout, Label: "Welcome", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsWelcomeShow},
	{ID: settingsNavAbout + ".quit", Parent: settingsNavAbout, Label: "Quit Projmux", Kind: settingsNavConfirm, Axis: settingsAxisGlobal, Value: settingsQuitOpen, Note: "app-owned runtime and socket impact"},

	// Project scope --------------------------------------------------------
	{ID: settingsNavScopeProject, Label: "Project", Kind: settingsNavView, Axis: settingsAxisProject},
	{ID: settingsNavProjectAutomation, Parent: settingsNavScopeProject, Label: "Automation", Kind: settingsNavView, Axis: settingsAxisProject, Value: settingsSectionProjectAutomation},
	{ID: settingsNavProjectTrust, Parent: settingsNavProjectAutomation, Label: "Trust", Kind: settingsNavView, Axis: settingsAxisProject, Value: settingsSectionProjectTrust},
	{ID: settingsNavProjectTrust + ".state", Parent: settingsNavProjectTrust, Label: "Trust state / Project config hash / Source", Kind: settingsNavState, Axis: settingsAxisProject, Value: settingsNoopValue},
	{ID: settingsNavProjectTrust + ".approve", Parent: settingsNavProjectTrust, Label: "Trust or refresh approval", Kind: settingsNavAction, Axis: settingsAxisProject, Dynamic: true},
	{ID: settingsNavProjectTrust + ".revoke", Parent: settingsNavProjectTrust, Label: "Revoke trust", Kind: settingsNavConfirm, Axis: settingsAxisProject, Dynamic: true},
	{ID: settingsNavProjectHooks, Parent: settingsNavProjectAutomation, Label: "Project hooks", Kind: settingsNavView, Axis: settingsAxisProject, Value: settingsSectionProjectHooks},
	{ID: settingsNavProjectHooks + ".lifecycle", Parent: settingsNavProjectHooks, Label: "Session lifecycle", Kind: settingsNavView, Axis: settingsAxisProject, Value: settingsProjectAutomationLifecycle},
	{ID: settingsNavProjectHooks + ".lifecycle.event", Parent: settingsNavProjectHooks + ".lifecycle", Label: "<lifecycle event>", Kind: settingsNavView, Axis: settingsAxisProject, Dynamic: true, Note: "Before session create / After session create / After session attach"},
	{ID: settingsNavProjectHooks + ".lifecycle.event.state", Parent: settingsNavProjectHooks + ".lifecycle.event", Label: "Command / Effective / Source / Trust", Kind: settingsNavState, Axis: settingsAxisProject, Value: settingsNoopValue},
	{ID: settingsNavProjectHooks + ".lifecycle.event.edit", Parent: settingsNavProjectHooks + ".lifecycle.event", Label: "Add or edit command", Kind: settingsNavEdit, Axis: settingsAxisProject, Dynamic: true},
	{ID: settingsNavProjectHooks + ".lifecycle.event.remove", Parent: settingsNavProjectHooks + ".lifecycle.event", Label: "Remove command", Kind: settingsNavConfirm, Axis: settingsAxisProject, Dynamic: true},
	{ID: settingsNavProjectHooks + ".send-noti", Parent: settingsNavProjectHooks, Label: "After notification queued", Kind: settingsNavView, Axis: settingsAxisProject, Value: settingsProjectAutomationSendNoti},
	{ID: settingsNavProjectHooks + ".send-noti.state", Parent: settingsNavProjectHooks + ".send-noti", Label: "Command / Effective / Source / Trust", Kind: settingsNavState, Axis: settingsAxisProject, Value: settingsNoopValue},
	{ID: settingsNavProjectHooks + ".send-noti.edit", Parent: settingsNavProjectHooks + ".send-noti", Label: "Add or edit command", Kind: settingsNavEdit, Axis: settingsAxisProject, Dynamic: true},
	{ID: settingsNavProjectHooks + ".send-noti.remove", Parent: settingsNavProjectHooks + ".send-noti", Label: "Remove command", Kind: settingsNavConfirm, Axis: settingsAxisProject, Dynamic: true},

	{ID: settingsNavProjectSnapshots, Parent: settingsNavScopeProject, Label: "Snapshots", Kind: settingsNavView, Axis: settingsAxisProject, Value: settingsSectionProjectSessionState},
	{ID: settingsNavProjectSnapshots + ".autosave", Parent: settingsNavProjectSnapshots, Label: "Auto-save override", Kind: settingsNavView, Axis: settingsAxisProject, Dynamic: true},
	{ID: settingsNavProjectSnapshots + ".autosave.state", Parent: settingsNavProjectSnapshots + ".autosave", Label: "Global / Project / Effective / Source", Kind: settingsNavState, Axis: settingsAxisProject, Value: settingsNoopValue},
	{ID: settingsNavProjectSnapshots + ".autosave.choice", Parent: settingsNavProjectSnapshots + ".autosave", Label: "Inherit / Enable / Disable", Kind: settingsNavChoice, Axis: settingsAxisProject, Dynamic: true},
	{ID: settingsNavProjectSnapshots + ".saved", Parent: settingsNavProjectSnapshots, Label: "Saved Snapshots", Kind: settingsNavView, Axis: settingsAxisProject, Dynamic: true},
	{ID: settingsNavProjectSnapshots + ".saved.state", Parent: settingsNavProjectSnapshots + ".saved", Label: "Latest / Storage / Retention", Kind: settingsNavState, Axis: settingsAxisProject, Value: settingsNoopValue},
	{ID: settingsNavProjectSnapshots + ".saved.save-latest", Parent: settingsNavProjectSnapshots + ".saved", Label: "Save latest", Kind: settingsNavAction, Axis: settingsAxisProject, Dynamic: true},
	{ID: settingsNavProjectSnapshots + ".saved.save-named", Parent: settingsNavProjectSnapshots + ".saved", Label: "Save named", Kind: settingsNavEdit, Axis: settingsAxisProject, Dynamic: true},
	{ID: settingsNavProjectSnapshots + ".saved.item", Parent: settingsNavProjectSnapshots + ".saved", Label: "<snapshot>", Kind: settingsNavView, Axis: settingsAxisProject, Dynamic: true},
	{ID: settingsNavProjectSnapshots + ".saved.item.state", Parent: settingsNavProjectSnapshots + ".saved.item", Label: "Name / Created / Source / Contents", Kind: settingsNavState, Axis: settingsAxisProject, Value: settingsNoopValue},
	{ID: settingsNavProjectSnapshots + ".saved.item.preview", Parent: settingsNavProjectSnapshots + ".saved.item", Label: "Preview restore", Kind: settingsNavAction, Axis: settingsAxisProject, Dynamic: true},
	{ID: settingsNavProjectSnapshots + ".saved.item.delete", Parent: settingsNavProjectSnapshots + ".saved.item", Label: "Delete snapshot", Kind: settingsNavConfirm, Axis: settingsAxisProject, Dynamic: true},
}

// settingsNavRemovedRoots are the pre-cutover destinations that must not be
// reachable after Phase 0, as visible rows or as hidden redirects. The negative
// guard test asserts none of them is rendered from any Settings surface.
var settingsNavRemovedRoots = []string{
	settingsSectionLabs,
	settingsSectionGlobalTheme,
	settingsLabsProjectHooks,
}

// settingsNavRemovedVisibleCopy is the legacy/misleading visible vocabulary the
// cutover retires. These are display strings only: the matching keymap action
// IDs, config keys and runtime routes keep their current spelling.
var settingsNavRemovedVisibleCopy = []string{
	"AI Settings",
	"Project Picker",
	"Project Root",
	"Workdirs",
	"Enabled agents",
	"Default split mode",
	"Resume picker",
	"Session State",
	"Kill Session",
	"Delete Session",
	"Session name",
	"New window",
	"Hook quiet policy",
	"Delivery sources",
	"Labs",
	"Project recipe",
	"Effective merge view",
	"AI badge style",
	"AI panes",
	"Notify Sidebar",
	"Session Popup",
	"Quit projmux",
	"Path icon",
	"Notify icon",
	"Live system resources",
	"Project Hooks",
	// Retired keybinding containers. They named an implementation layer rather
	// than an outcome, and both of them fronted rows that did nothing.
	"Advanced...",
	"Troubleshooting",
	"Raw diagnostic view",
	"Test key delivery, Advanced",
}

var (
	settingsNavByIDIndex    map[string]settingsNavNode
	settingsNavByValueIndex map[string]settingsNavNode
	settingsNavChildIndex   map[string][]settingsNavNode
)

func init() {
	settingsNavByIDIndex = make(map[string]settingsNavNode, len(settingsNavCatalog))
	settingsNavByValueIndex = make(map[string]settingsNavNode, len(settingsNavCatalog))
	settingsNavChildIndex = make(map[string][]settingsNavNode, len(settingsNavCatalog))
	for _, node := range settingsNavCatalog {
		settingsNavByIDIndex[node.ID] = node
		if node.Value != "" && node.Value != settingsNoopValue {
			settingsNavByValueIndex[node.Value] = node
		}
		settingsNavChildIndex[node.Parent] = append(settingsNavChildIndex[node.Parent], node)
	}
}

func settingsNavByID(id string) (settingsNavNode, bool) {
	node, ok := settingsNavByIDIndex[id]
	return node, ok
}

func settingsNavByValue(value string) (settingsNavNode, bool) {
	node, ok := settingsNavByValueIndex[strings.TrimSpace(value)]
	return node, ok
}

// settingsNavChildren returns the child rows of a node in declared order.
func settingsNavChildren(id string) []settingsNavNode {
	children := settingsNavChildIndex[id]
	out := make([]settingsNavNode, len(children))
	copy(out, children)
	return out
}

// settingsNavLabel resolves a node's English display literal. An unknown ID
// returns the empty string so a caller can fail loudly rather than render a
// placeholder.
func settingsNavLabel(id string) string {
	node, ok := settingsNavByIDIndex[id]
	if !ok {
		return ""
	}
	return node.Label
}

// renderSettingsNavTree renders the catalog as the ASCII tree frozen by the
// navigation golden. The rendering is deliberately close to the design
// document's own notation so a review diff reads as an IA change.
func renderSettingsNavTree() string {
	var b strings.Builder
	b.WriteString("Settings [View]\n")
	roots := settingsNavChildren("")
	for i, root := range roots {
		writeSettingsNavNode(&b, root, "", i == len(roots)-1, true)
	}
	return b.String()
}

func writeSettingsNavNode(b *strings.Builder, node settingsNavNode, prefix string, last bool, scope bool) {
	connector := "├─ "
	childPrefix := prefix + "│  "
	if last {
		connector = "└─ "
		childPrefix = prefix + "   "
	}
	b.WriteString(prefix)
	b.WriteString(connector)
	b.WriteString(node.Label)
	b.WriteString(" [")
	if scope {
		b.WriteString("Scope ")
	}
	b.WriteString(string(node.Kind))
	if node.Dynamic {
		b.WriteString("; dynamic")
	}
	if node.Note != "" {
		b.WriteString("; ")
		b.WriteString(node.Note)
	}
	b.WriteString("]\n")
	children := settingsNavChildren(node.ID)
	for i, child := range children {
		writeSettingsNavNode(b, child, childPrefix, i == len(children)-1, false)
	}
}
