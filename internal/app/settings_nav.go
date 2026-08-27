package app

import (
	"strings"

	"github.com/crevissepartners/projmux/internal/i18n"
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
	// Label and LabelKey are the fallback copy and its explicit localization
	// key. Static rows never recover the key by reverse-looking up Label.
	Label    string
	LabelKey i18n.Key
	// EntryLabel and EntryLabelKey preserve the shipped picker copy when it
	// intentionally differs from the structural tree label. Both still live
	// on this one descriptor; builders must use the effective entry label and
	// never recover its key from uiTextKeys.
	EntryLabel    string
	EntryLabelKey i18n.Key
	Kind          settingsNavKind
	// EntryKind is the picker interaction projection. It is separate from the
	// IA affordance because a Choice row navigates to a chooser while the
	// chooser's dynamic options are actionable.
	EntryKind settingsEntryKind
	Axis      SettingsAxis
	// Owner is the one loop authorized to consume the row's picker value.
	// Dynamic templates carry the owner too; prefix matchers only point at a
	// template and cannot invent a different authority.
	Owner settingsEntryOwner
	// Value is the picker entry value rendered for this row when the row is a
	// static entry. Collection items and rows whose value carries runtime data
	// (a path, a provider id, a chord) leave it empty and declare Dynamic.
	Value string
	// Dynamic marks a collection item template: the row stands for 0..N
	// runtime rows rather than one static entry.
	Dynamic bool
	// Hidden marks infrastructure rows (Back, passive no-op, tab selectors and
	// compatibility values) that participate in the exact static catalog but
	// are not part of the visible Settings IA tree.
	Hidden bool
	// Note records a contract the row carries into later phases, such as the
	// canonical handler a row must reuse. It is rendered into the tree golden
	// so a contract change shows up as a diff.
	Note string
}

// settingsNavScopeGlobal and settingsNavScopeProject are the two scope roots.
const (
	settingsNavScopeGlobal  = "global"
	settingsNavScopeProject = "project"

	settingsNavInternalBack         = "internal.back"
	settingsNavInternalNoop         = "internal.noop"
	settingsNavInternalTabGlobal    = "internal.tab.global"
	settingsNavInternalTabProject   = "internal.tab.project"
	settingsNavInternalWorkdirTyped = "internal.workdir.typed"
)

// Node IDs referenced from the section loops. Only the nodes that a loop needs
// to address by name are given constants; the rest are addressed through their
// parent when rendering children.
const (
	settingsNavProjects            = "global.projects"
	settingsNavProjectsPrimaryRoot = "global.projects.primary-root"
	settingsNavProjectsExtraRoots  = "global.projects.additional-roots"
	settingsNavProjectsPins        = "global.projects.pinned"
	settingsNavProjectsCandidates  = "global.projects.candidate-pins"
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

// settingsNodeCatalog is the target Settings navigation, in render order.
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
var settingsNodeCatalog = []settingsNavNode{
	// Infrastructure rows share the same canonical descriptor catalog as the
	// visible tree, but do not render as tree children.
	{ID: settingsNavInternalBack, Label: "Back", LabelKey: "settings.text.back", Kind: settingsNavView, EntryKind: settingsEntryNavigation, Axis: settingsAxisBoth, Value: settingsBackValue, Owner: settingsOwnerPassiveLoop, Hidden: true},
	{ID: settingsNavInternalNoop, Label: "Info or disabled", LabelKey: "settings.node.info_or_disabled", Kind: settingsNavState, EntryKind: settingsEntryPassive, Axis: settingsAxisBoth, Value: settingsNoopValue, Owner: settingsOwnerPassiveLoop, Hidden: true},
	{ID: settingsNavInternalTabGlobal, Label: "Global Settings", LabelKey: i18n.KeySettingsRootGlobalTab, Kind: settingsNavView, EntryKind: settingsEntryNavigation, Axis: settingsAxisBoth, Value: settingsRootTabGlobalValue, Owner: settingsOwnerRoot, Hidden: true},
	{ID: settingsNavInternalTabProject, Label: "Project Settings", LabelKey: i18n.KeySettingsRootProjectTab, Kind: settingsNavView, EntryKind: settingsEntryNavigation, Axis: settingsAxisBoth, Value: settingsRootTabProjectValue, Owner: settingsOwnerRoot, Hidden: true},
	{ID: "internal.hooks.global", Label: "Projmux session lifecycle", LabelKey: "settings.text.projmux_session_lifecycle", Kind: settingsNavView, EntryKind: settingsEntryNavigation, Axis: settingsAxisGlobal, Value: settingsSectionGlobalHooks, Owner: settingsOwnerAutomation, Hidden: true},
	{ID: "internal.notifications.delivery", Label: "Provider Integrations", LabelKey: "settings.text.provider_integrations", Kind: settingsNavView, EntryKind: settingsEntryNavigation, Axis: settingsAxisGlobal, Value: settingsNotificationsDelivery, Owner: settingsOwnerNotifications, Hidden: true},
	{ID: "internal.keybindings.bindings", Label: "Keybindings", LabelKey: "settings.text.keybindings", Kind: settingsNavView, EntryKind: settingsEntryNavigation, Axis: settingsAxisGlobal, Value: settingsKeybindingsBindings, Owner: settingsOwnerKeybindings, Hidden: true},
	{ID: "internal.keybindings.diagnostic", Label: "Keybinding Diagnostic", LabelKey: "settings.node.keybinding_diagnostic", Kind: settingsNavView, EntryKind: settingsEntryNavigation, Axis: settingsAxisGlobal, Value: settingsKeybindingsDiagnostic, Owner: settingsOwnerKeybindings, Hidden: true},
	{ID: "internal.keybindings.probe", Label: "Keybinding Probe", LabelKey: "settings.node.keybinding_probe", Kind: settingsNavView, EntryKind: settingsEntryNavigation, Axis: settingsAxisGlobal, Value: settingsKeybindingsProbe, Owner: settingsOwnerKeybindings, Hidden: true},
	{ID: "internal.ai.notify-diagnostics", Label: "Provider Integrations", LabelKey: "settings.text.provider_integrations", Kind: settingsNavView, EntryKind: settingsEntryNavigation, Axis: settingsAxisGlobal, Value: settingsAINotifyDiagnostics, Owner: settingsOwnerAI, Hidden: true},
	{ID: settingsNavInternalWorkdirTyped, Label: "Type path manually...", LabelKey: "settings.text.type_path_manually", Kind: settingsNavEdit, Axis: settingsAxisGlobal, Value: settingsWorkdirTyped, Owner: settingsOwnerProject, Hidden: true},

	{ID: settingsNavScopeGlobal, Label: "Global", LabelKey: i18n.KeySettingsRootGlobalTab, Kind: settingsNavView, Axis: settingsAxisGlobal},

	// Projects -------------------------------------------------------------
	{ID: settingsNavProjects, Parent: settingsNavScopeGlobal, Label: "Projects", LabelKey: "picker.crumb.projects", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsSectionProject, Owner: settingsOwnerRoot},

	{ID: settingsNavProjectsPrimaryRoot, Parent: settingsNavProjects, Label: "Primary discovery root", LabelKey: "settings.text.primary_discovery_root", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsProjectRootManage, Owner: settingsOwnerProjectPicker},
	{ID: settingsNavProjectsPrimaryRoot + ".state", Parent: settingsNavProjectsPrimaryRoot, Label: "Effective / Saved / Source", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},
	{ID: settingsNavProjectsPrimaryRoot + ".use-current", Parent: settingsNavProjectsPrimaryRoot, Label: "Use current directory", LabelKey: "settings.text.use_current_directory", EntryLabel: "Use Current Project as Root", EntryLabelKey: "settings.text.use_current_project_root", Kind: settingsNavEdit, Axis: settingsAxisGlobal, Value: settingsProjdirSetCurrent, Owner: settingsOwnerProject},
	{ID: settingsNavProjectsPrimaryRoot + ".enter-path", Parent: settingsNavProjectsPrimaryRoot, Label: "Enter path", LabelKey: "settings.text.enter_path", Kind: settingsNavEdit, Axis: settingsAxisGlobal, Value: settingsProjdirSetTyped, Owner: settingsOwnerProject},
	{ID: settingsNavProjectsPrimaryRoot + ".clear", Parent: settingsNavProjectsPrimaryRoot, Label: "Clear saved root", LabelKey: "settings.text.clear_saved_root", Kind: settingsNavConfirm, Axis: settingsAxisGlobal, Value: settingsProjdirClear, Owner: settingsOwnerProject},

	{ID: settingsNavProjectsExtraRoots, Parent: settingsNavProjects, Label: "Additional discovery roots", LabelKey: "settings.text.additional_discovery_roots", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsWorkdirList, Owner: settingsOwnerProjectPicker},
	{ID: settingsNavProjectsExtraRoots + ".state", Parent: settingsNavProjectsExtraRoots, Label: "Effective roots / Source", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},
	{ID: settingsNavProjectsExtraRoots + ".item", Parent: settingsNavProjectsExtraRoots, Label: "<Discovery root>", Kind: settingsNavView, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavProjectsExtraRoots + ".item.state", Parent: settingsNavProjectsExtraRoots + ".item", Label: "Root path / Source", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},
	{ID: settingsNavProjectsExtraRoots + ".item.remove", Parent: settingsNavProjectsExtraRoots + ".item", Label: "Remove discovery root", LabelKey: "settings.text.remove_discovery_root", Kind: settingsNavConfirm, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavProjectsExtraRoots + ".add-current", Parent: settingsNavProjectsExtraRoots, Label: "Add current directory", LabelKey: "settings.text.add_current_directory", Kind: settingsNavEdit, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavProjectsExtraRoots + ".add-path", Parent: settingsNavProjectsExtraRoots, Label: "Add path", LabelKey: "settings.text.add_path", Kind: settingsNavEdit, Axis: settingsAxisGlobal, Value: settingsWorkdirAdd, Owner: settingsOwnerProjectPicker},

	{ID: settingsNavProjectsPins, Parent: settingsNavProjects, Label: "Pinned Projects", LabelKey: "settings.text.pinned_projects", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsProjectPins, Owner: settingsOwnerProjectPicker},
	{ID: settingsNavProjectsPins + ".item", Parent: settingsNavProjectsPins, Label: "<Project>", Kind: settingsNavView, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavProjectsPins + ".item.state", Parent: settingsNavProjectsPins + ".item", Label: "Display name / Unique name / UID / Root / Condition / Missing since / Runtime / Source", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},
	{ID: settingsNavProjectsPins + ".item.rebind", Parent: settingsNavProjectsPins + ".item", Label: "Rebind Project root", LabelKey: "settings.text.rebind_project_root", Kind: settingsNavEdit, Axis: settingsAxisGlobal, Dynamic: true, Note: "canonical rebind project, same UID"},
	{ID: settingsNavProjectsPins + ".item.unpin", Parent: settingsNavProjectsPins + ".item", Label: "Unpin Project", LabelKey: "settings.text.unpin_project", Kind: settingsNavConfirm, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavProjectsPins + ".pin-current", Parent: settingsNavProjectsPins, Label: "Pin current Project", LabelKey: "settings.text.pin_current_project", Kind: settingsNavEdit, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavProjectsPins + ".select", Parent: settingsNavProjectsPins, Label: "Select Project to pin", LabelKey: "settings.text.select_project_to_pin", Kind: settingsNavEdit, Axis: settingsAxisGlobal, Value: settingsProjectAdd, Owner: settingsOwnerProjectPicker},

	{ID: settingsNavProjectsCandidates, Parent: settingsNavProjects, Label: "Candidate Pins", LabelKey: "settings.text.candidate_pins", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsProjectCandidatePins, Owner: settingsOwnerProjectPicker},
	{ID: settingsNavProjectsCandidates + ".item", Parent: settingsNavProjectsCandidates, Label: "<Candidate>", Kind: settingsNavView, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavProjectsCandidates + ".item.state", Parent: settingsNavProjectsCandidates + ".item", Label: "Path / Registration", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},
	{ID: settingsNavProjectsCandidates + ".item.register", Parent: settingsNavProjectsCandidates + ".item", Label: "Register as Project", LabelKey: "settings.text.register_as_project", Kind: settingsNavConfirm, Axis: settingsAxisGlobal, Dynamic: true, Note: "canonical create project, this exact path only"},
	{ID: settingsNavProjectsCandidates + ".item.unpin", Parent: settingsNavProjectsCandidates + ".item", Label: "Unpin candidate", LabelKey: "settings.text.unpin_candidate", Kind: settingsNavConfirm, Axis: settingsAxisGlobal, Dynamic: true},

	{ID: settingsNavProjectsSidebar, Parent: settingsNavProjects, Label: "Project Sidebar", LabelKey: "settings.text.project_sidebar", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsProjectsSidebar, Owner: settingsOwnerProjectPicker},
	{ID: settingsNavProjectsSidebar + ".closed-startup", Parent: settingsNavProjectsSidebar, Label: "Closed Project startup", LabelKey: "settings.text.closed_project_startup", Kind: settingsNavChoice, Axis: settingsAxisGlobal, Dynamic: true, Note: "Continue project / Open fresh"},
	{ID: settingsNavProjectsSidebar + ".runtime-diagnostics", Parent: settingsNavProjectsSidebar, Label: "Runtime diagnostics", LabelKey: "picker.runtime.title", Kind: settingsNavChoice, Axis: settingsAxisGlobal, Dynamic: true, Note: "When needed / Always"},

	// AI -------------------------------------------------------------------
	{ID: settingsNavAI, Parent: settingsNavScopeGlobal, Label: "AI", LabelKey: "settings.node.ai", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsSectionAI, Owner: settingsOwnerRoot},
	{ID: settingsNavAI + ".launch-target", Parent: settingsNavAI, Label: "Default launch target", LabelKey: "settings.text.default_launch_target", Kind: settingsNavChoice, Axis: settingsAxisGlobal, Value: settingsAIDefaultMode, Owner: settingsOwnerAI},
	{ID: settingsNavAIProviders, Parent: settingsNavAI, Label: "Enabled providers", LabelKey: "settings.text.enabled_providers", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsAIEnabledAgents, Owner: settingsOwnerAI},
	{ID: settingsNavAIProviders + ".item", Parent: settingsNavAIProviders, Label: "<Provider>", Kind: settingsNavToggle, Axis: settingsAxisGlobal, Dynamic: true, Note: "Claude / Codex / Antigravity; availability and source badge"},
	{ID: settingsNavAI + ".codex-health", Parent: settingsNavAI, Label: "Codex control plane / App Server / Hook fallback / Unavailable", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue, Note: "read-only; capability selected"},
	{ID: settingsNavAIResumePicker, Parent: settingsNavAI, Label: "Agent Resume Picker", LabelKey: "settings.text.agent_resume_picker", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsAIResumePicker, Owner: settingsOwnerAI},
	{ID: settingsNavAIResumePicker + ".state", Parent: settingsNavAIResumePicker, Label: "Effective behavior / Source / Eligible phases", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue, Note: "Offline, Failed"},
	{ID: settingsNavAIResumePicker + ".new-action", Parent: settingsNavAIResumePicker, Label: "New action label", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue, Note: "Create New Agent"},
	{ID: settingsNavAIResumePicker + ".limit", Parent: settingsNavAIResumePicker, Label: "Picker limit", LabelKey: "settings.text.ai_resume_picker_limit_row", Kind: settingsNavChoice, Axis: settingsAxisGlobal, Value: settingsAIResumePickerLimit, Owner: settingsOwnerAI},
	{ID: settingsNavAIResumePicker + ".depth", Parent: settingsNavAIResumePicker, Label: "Scan depth", LabelKey: "settings.text.ai_resume_picker_depth_row", Kind: settingsNavChoice, Axis: settingsAxisGlobal, Value: settingsAIResumePickerDepth, Owner: settingsOwnerAI},

	// Notifications --------------------------------------------------------
	{ID: settingsNavNotifications, Parent: settingsNavScopeGlobal, Label: "Notifications", LabelKey: "settings.root.notifications", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsSectionNotifications, Owner: settingsOwnerRoot},
	{ID: settingsNavNotifyDesktop, Parent: settingsNavNotifications, Label: "Desktop delivery", LabelKey: "settings.text.desktop_delivery", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsNotificationsDesktop, Owner: settingsOwnerNotifications},
	{ID: settingsNavNotifyDesktop + ".state", Parent: settingsNavNotifyDesktop, Label: "Effective sender / Source / Availability", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},
	{ID: settingsNavNotifyDesktop + ".mode", Parent: settingsNavNotifyDesktop, Label: "Delivery mode", LabelKey: "settings.text.delivery_mode", Kind: settingsNavChoice, Axis: settingsAxisGlobal, Dynamic: true, Note: "Off / Notify"},
	{ID: settingsNavNotifyDesktop + ".dedupe", Parent: settingsNavNotifyDesktop, Label: "Dedupe window", LabelKey: "settings.text.dedupe_window", Kind: settingsNavChoice, Axis: settingsAxisGlobal, Value: settingsNotificationsAIDedupe, Owner: settingsOwnerNotifications},
	{ID: settingsNavNotifyDesktop + ".external", Parent: settingsNavNotifyDesktop, Label: "External desktop sender / Environment source / Fallback", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},

	{ID: settingsNavNotifyProviders, Parent: settingsNavNotifications, Label: "Provider Integrations", LabelKey: "settings.text.provider_integrations", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsNotificationsProviders, Owner: settingsOwnerNotifications},
	{ID: settingsNavNotifyProviders + ".item", Parent: settingsNavNotifyProviders, Label: "<Provider>", Kind: settingsNavView, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavNotifyProviders + ".item.state", Parent: settingsNavNotifyProviders + ".item", Label: "Wiring status / Source / Conflict / Config path", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},
	{ID: settingsNavNotifyProviders + ".item.check", Parent: settingsNavNotifyProviders + ".item", Label: "Check integration", Kind: settingsNavAction, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavNotifyProviders + ".item.setup", Parent: settingsNavNotifyProviders + ".item", Label: "Copy install or remove command", Kind: settingsNavAction, Axis: settingsAxisGlobal, Dynamic: true},

	{ID: settingsNavNotifyTmuxSource, Parent: settingsNavNotifications, Label: "tmux event source", LabelKey: "settings.text.tmux_event_source", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsNotificationsTmuxSource, Owner: settingsOwnerNotifications},
	{ID: settingsNavNotifyTmuxSource + ".state", Parent: settingsNavNotifyTmuxSource, Label: "Bell wiring status / Source / Conflict", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},
	{ID: settingsNavNotifyTmuxSource + ".check", Parent: settingsNavNotifyTmuxSource, Label: "Check", Kind: settingsNavAction, Axis: settingsAxisGlobal, Dynamic: true},

	{ID: settingsNavNotifyAgentEvents, Parent: settingsNavNotifications, Label: "Agent event behavior", LabelKey: "settings.text.agent_event_behavior", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsNotificationsHookActions, Owner: settingsOwnerNotifications},
	{ID: settingsNavNotifyAgentEvents + ".item", Parent: settingsNavNotifyAgentEvents, Label: "<Provider>", Kind: settingsNavView, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavNotifyAgentEvents + ".item.event", Parent: settingsNavNotifyAgentEvents + ".item", Label: "<event>", Kind: settingsNavChoice, Axis: settingsAxisGlobal, Dynamic: true, Note: "Default / Notify / State only / Quiet"},

	// Automation -----------------------------------------------------------
	{ID: settingsNavAutomation, Parent: settingsNavScopeGlobal, Label: "Automation", LabelKey: "settings.text.automation", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsSectionAutomation, Owner: settingsOwnerRoot},
	{ID: settingsNavAutomationLifecycle, Parent: settingsNavAutomation, Label: "Projmux session lifecycle", LabelKey: "settings.text.projmux_session_lifecycle", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsAutomationLifecycle, Owner: settingsOwnerAutomation},
	{ID: settingsNavAutomationLifecycle + ".event", Parent: settingsNavAutomationLifecycle, Label: "<lifecycle event>", Kind: settingsNavView, Axis: settingsAxisGlobal, Dynamic: true, Note: "Before session create / After session create / After session attach"},
	{ID: settingsNavAutomationLifecycle + ".event.state", Parent: settingsNavAutomationLifecycle + ".event", Label: "Command / Effective / Source", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},
	{ID: settingsNavAutomationLifecycle + ".event.edit", Parent: settingsNavAutomationLifecycle + ".event", Label: "Add or edit command", Kind: settingsNavEdit, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavAutomationLifecycle + ".event.remove", Parent: settingsNavAutomationLifecycle + ".event", Label: "Remove command", Kind: settingsNavConfirm, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavAutomationSendNoti, Parent: settingsNavAutomation, Label: "After notification queued", LabelKey: "settings.text.after_notification_queued", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsAutomationSendNoti, Owner: settingsOwnerAutomation},
	{ID: settingsNavAutomationSendNoti + ".state", Parent: settingsNavAutomationSendNoti, Label: "Command / Effective / Source", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},
	{ID: settingsNavAutomationSendNoti + ".edit", Parent: settingsNavAutomationSendNoti, Label: "Add or edit command", Kind: settingsNavEdit, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavAutomationSendNoti + ".remove", Parent: settingsNavAutomationSendNoti, Label: "Remove command", Kind: settingsNavConfirm, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavAutomation + ".project-policy", Parent: settingsNavAutomation, Label: "Project automation policy", LabelKey: "settings.text.project_automation_policy", Kind: settingsNavToggle, Axis: settingsAxisGlobal, Dynamic: true, Note: "trust and source badge"},

	// Appearance -----------------------------------------------------------
	{ID: settingsNavAppearance, Parent: settingsNavScopeGlobal, Label: "Appearance", LabelKey: "settings.root.appearance", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsSectionStatusbar, Owner: settingsOwnerRoot},
	{ID: settingsNavAppearanceTheme, Parent: settingsNavAppearance, Label: "Theme", LabelKey: "settings.text.theme", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsAppearanceTheme, Owner: settingsOwnerAppearance},
	{ID: settingsNavAppearanceTheme + ".preset", Parent: settingsNavAppearanceTheme, Label: "Preset", LabelKey: "settings.text.theme_preset", Kind: settingsNavChoice, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavAppearanceTheme + ".tokens", Parent: settingsNavAppearanceTheme, Label: "Tokens", LabelKey: "settings.text.theme_tokens", Kind: settingsNavView, Axis: settingsAxisGlobal, Dynamic: true, Note: "Core / Surface / State / Chrome groups"},
	{ID: settingsNavAppearanceTheme + ".tokens.item", Parent: settingsNavAppearanceTheme + ".tokens", Label: "<token>", Kind: settingsNavView, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavAppearanceTheme + ".tokens.item.state", Parent: settingsNavAppearanceTheme + ".tokens.item", Label: "Effective / Saved / Source / Fallback / Preview", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},
	{ID: settingsNavAppearanceTheme + ".tokens.item.set", Parent: settingsNavAppearanceTheme + ".tokens.item", Label: "Set value", Kind: settingsNavEdit, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavAppearanceTheme + ".tokens.item.fallback", Parent: settingsNavAppearanceTheme + ".tokens.item", Label: "Use preset fallback", Kind: settingsNavChoice, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavAppearanceTheme + ".reset", Parent: settingsNavAppearanceTheme, Label: "Reset theme", LabelKey: "settings.text.reset_theme", Kind: settingsNavConfirm, Axis: settingsAxisGlobal, Dynamic: true},

	{ID: settingsNavStatusBar, Parent: settingsNavAppearance, Label: "Status Bar", LabelKey: "settings.text.status_bar", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsAppearanceStatusBar, Owner: settingsOwnerAppearance},
	{ID: settingsNavStatusBar + ".notifications-hud", Parent: settingsNavStatusBar, Label: "Notifications HUD", LabelKey: "settings.text.notifications_hud", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsActionPrefixStatusbar + string(statusbarDecorationTargetNotify), Owner: settingsOwnerAppearance},
	{ID: settingsNavStatusBar + ".notifications-hud.state", Parent: settingsNavStatusBar + ".notifications-hud", Label: "Current / Source / Preview", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},
	{ID: settingsNavStatusBar + ".notifications-hud.visible", Parent: settingsNavStatusBar + ".notifications-hud", Label: "Visible", Kind: settingsNavToggle, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavStatusBar + ".notifications-hud.icon", Parent: settingsNavStatusBar + ".notifications-hud", Label: "Notification icon", Kind: settingsNavChoice, Axis: settingsAxisGlobal, Dynamic: true, Note: "Off / Symbol / Emoji"},
	{ID: settingsNavStatusBar + ".agent-usage-hud", Parent: settingsNavStatusBar, Label: "Agent Usage HUD", LabelKey: "settings.node.agent_usage_hud", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsAppearanceAgentUsageHUD, Owner: settingsOwnerAppearance, Note: "agent usage source / preview badge"},
	{ID: settingsNavStatusBar + ".agent-usage-hud.state", Parent: settingsNavStatusBar + ".agent-usage-hud", Label: "Saved / Effective / Source / Preview", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},
	{ID: settingsNavStatusBar + ".agent-usage-hud.visible", Parent: settingsNavStatusBar + ".agent-usage-hud", Label: "Visible", Kind: settingsNavToggle, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavStatusBar + ".agent-usage-hud.provider", Parent: settingsNavStatusBar + ".agent-usage-hud", Label: "<Usage provider>", Kind: settingsNavView, Axis: settingsAxisGlobal, Dynamic: true, Note: "aiprovider.UsageSupported order"},
	{ID: settingsNavStatusBar + ".agent-usage-hud.provider.state", Parent: settingsNavStatusBar + ".agent-usage-hud.provider", Label: "Saved / Effective / Source / Preview", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},
	{ID: settingsNavStatusBar + ".agent-usage-hud.provider.visible", Parent: settingsNavStatusBar + ".agent-usage-hud.provider", Label: "Visible", Kind: settingsNavToggle, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavStatusBar + ".agent-usage-hud.provider.window", Parent: settingsNavStatusBar + ".agent-usage-hud.provider", Label: "<Supported HUD window>", Kind: settingsNavToggle, Axis: settingsAxisGlobal, Dynamic: true, Note: "5h / Weekly capability"},
	{ID: settingsNavStatusBar + ".project", Parent: settingsNavStatusBar, Label: "Project", Kind: settingsNavToggle, Axis: settingsAxisGlobal, Dynamic: true, Note: "metadata.name / runtime fallback source / preview badge"},
	{ID: settingsNavStatusBar + ".working-directory", Parent: settingsNavStatusBar, Label: "Working directory", LabelKey: "settings.text.working_directory", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsActionPrefixStatusbar + string(statusbarDecorationTargetCwd), Owner: settingsOwnerAppearance, Note: "focused Pane cwd"},
	{ID: settingsNavStatusBar + ".working-directory.state", Parent: settingsNavStatusBar + ".working-directory", Label: "Current / Source / Preview", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},
	{ID: settingsNavStatusBar + ".working-directory.visible", Parent: settingsNavStatusBar + ".working-directory", Label: "Visible", Kind: settingsNavToggle, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavStatusBar + ".working-directory.icon", Parent: settingsNavStatusBar + ".working-directory", Label: "Icon", Kind: settingsNavChoice, Axis: settingsAxisGlobal, Dynamic: true, Note: "Off / Symbol / Emoji"},
	{ID: settingsNavStatusBar + ".git", Parent: settingsNavStatusBar, Label: "Git", LabelKey: "settings.node.git", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsActionPrefixStatusbar + string(statusbarDecorationTargetGit), Owner: settingsOwnerAppearance},
	{ID: settingsNavStatusBar + ".git.state", Parent: settingsNavStatusBar + ".git", Label: "Current / Source / Preview", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},
	{ID: settingsNavStatusBar + ".git.visible", Parent: settingsNavStatusBar + ".git", Label: "Visible", Kind: settingsNavToggle, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavStatusBar + ".git.icon", Parent: settingsNavStatusBar + ".git", Label: "Icon", Kind: settingsNavChoice, Axis: settingsAxisGlobal, Dynamic: true, Note: "Off / Symbol / Emoji"},
	{ID: settingsNavStatusBar + ".resources", Parent: settingsNavStatusBar, Label: "Resources", Kind: settingsNavToggle, Axis: settingsAxisGlobal, Dynamic: true, Note: "platform / source / preview badge; enablement also stops sampling"},
	{ID: settingsNavStatusBar + ".clock", Parent: settingsNavStatusBar, Label: "Clock", Kind: settingsNavToggle, Axis: settingsAxisGlobal, Dynamic: true, Note: "source / preview badge"},
	{ID: settingsNavStatusBar + ".settings-launcher", Parent: settingsNavStatusBar, Label: "Settings launcher", Kind: settingsNavToggle, Axis: settingsAxisGlobal, Dynamic: true, Note: "mouse chip only; CLI and keybinding remain"},

	{ID: settingsNavAppearance + ".locale", Parent: settingsNavAppearance, Label: "Language / Locale", LabelKey: "settings.text.language_locale", Kind: settingsNavChoice, Axis: settingsAxisGlobal, Value: settingsAppearanceLanguage, Owner: settingsOwnerAppearance},
	{ID: settingsNavAppearance + ".badge", Parent: settingsNavAppearance, Label: "Agent attention badge style", LabelKey: "settings.text.agent_attention_badge_style", Kind: settingsNavChoice, Axis: settingsAxisGlobal, Value: settingsActionPrefixAIBadgeStyle, Owner: settingsOwnerAppearance},

	// Snapshots ------------------------------------------------------------
	{ID: settingsNavSnapshots, Parent: settingsNavScopeGlobal, Label: "Snapshots", LabelKey: "settings.text.snapshots", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsSectionSessionState, Owner: settingsOwnerRoot},
	{ID: settingsNavSnapshots + ".autosave", Parent: settingsNavSnapshots, Label: "Auto-save", LabelKey: "settings.text.auto_save", Kind: settingsNavView, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavSnapshots + ".autosave.state", Parent: settingsNavSnapshots + ".autosave", Label: "Effective / Source / Storage", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},
	{ID: settingsNavSnapshots + ".autosave.enabled", Parent: settingsNavSnapshots + ".autosave", Label: "Enabled", Kind: settingsNavToggle, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavSnapshots + ".autosave.interval", Parent: settingsNavSnapshots + ".autosave", Label: "Interval", Kind: settingsNavChoice, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavSnapshots + ".storage", Parent: settingsNavSnapshots, Label: "Storage / Retention", LabelKey: "settings.text.storage_retention", Kind: settingsNavView, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavSnapshots + ".storage.state", Parent: settingsNavSnapshots + ".storage", Label: "Location / Effective retention / Source", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},

	// Keybindings ----------------------------------------------------------
	{ID: settingsNavKeybindings, Parent: settingsNavScopeGlobal, Label: "Keybindings", LabelKey: "settings.text.keybindings", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsSectionKeybindings, Owner: settingsOwnerRoot},
	{ID: settingsNavKeybindings + "." + keyBindingCategoryLaunch, Parent: settingsNavKeybindings, Label: keyBindingCategoryLaunchLabel, LabelKey: "settings.text.keybinding_category_launch", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsActionPrefixKeymapCategory + keyBindingCategoryLaunch, Owner: settingsOwnerKeybindings},
	{ID: settingsNavKeybindings + "." + keyBindingCategoryLaunch + ".action", Parent: settingsNavKeybindings + "." + keyBindingCategoryLaunch, Label: "<action detail>", Kind: settingsNavView, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavKeybindings + "." + keyBindingCategoryAgentPane, Parent: settingsNavKeybindings, Label: keyBindingCategoryAgentPaneLabel, LabelKey: "settings.text.keybinding_category_agent_pane", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsActionPrefixKeymapCategory + keyBindingCategoryAgentPane, Owner: settingsOwnerKeybindings},
	{ID: settingsNavKeybindings + "." + keyBindingCategoryAgentPane + ".action", Parent: settingsNavKeybindings + "." + keyBindingCategoryAgentPane, Label: "<action detail>", Kind: settingsNavView, Axis: settingsAxisGlobal, Dynamic: true, Note: "explicit current Pane anchor"},
	{ID: settingsNavKeybindings + "." + keyBindingCategoryNavigation, Parent: settingsNavKeybindings, Label: keyBindingCategoryNavigationLabel, LabelKey: "settings.text.keybinding_category_navigation", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsActionPrefixKeymapCategory + keyBindingCategoryNavigation, Owner: settingsOwnerKeybindings},
	{ID: settingsNavKeybindings + "." + keyBindingCategoryNavigation + ".action", Parent: settingsNavKeybindings + "." + keyBindingCategoryNavigation, Label: "<action detail>", Kind: settingsNavView, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavKeybindings + "." + keyBindingCategorySurfaces, Parent: settingsNavKeybindings, Label: keyBindingCategorySurfacesLabel, LabelKey: "settings.text.keybinding_category_surfaces", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsActionPrefixKeymapCategory + keyBindingCategorySurfaces, Owner: settingsOwnerKeybindings},
	{ID: settingsNavKeybindings + "." + keyBindingCategorySurfaces + ".surface", Parent: settingsNavKeybindings + "." + keyBindingCategorySurfaces, Label: "<surface>", Kind: settingsNavView, Axis: settingsAxisGlobal, Dynamic: true, Note: "Project Sidebar / Session Picker / Notification Sidebar / Settings"},
	{ID: settingsNavKeybindings + "." + keyBindingCategorySurfaces + ".surface.action", Parent: settingsNavKeybindings + "." + keyBindingCategorySurfaces + ".surface", Label: "<action detail>", Kind: settingsNavView, Axis: settingsAxisGlobal, Dynamic: true},
	{ID: settingsNavKeybindings + "." + keyBindingCategoryInput, Parent: settingsNavKeybindings, Label: keyBindingCategoryInputLabel, LabelKey: "settings.text.keybinding_category_input", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsActionPrefixKeymapCategory + keyBindingCategoryInput, Owner: settingsOwnerKeybindings},
	{ID: settingsNavKeybindings + "." + keyBindingCategoryInput + ".native-macos", Parent: settingsNavKeybindings + "." + keyBindingCategoryInput, Label: "Native macOS keybindings", LabelKey: "settings.text.native_macos_keybindings", Kind: settingsNavToggle, Axis: settingsAxisGlobal, Value: settingsNativeKeysToggle, Owner: settingsOwnerKeybindings},

	// About ----------------------------------------------------------------
	{ID: settingsNavAbout, Parent: settingsNavScopeGlobal, Label: "About", LabelKey: "settings.text.about", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsSectionAbout, Owner: settingsOwnerRoot},
	{ID: settingsNavAbout + ".state", Parent: settingsNavAbout, Label: "Version / Source / Build", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},
	{ID: settingsNavAbout + ".updates", Parent: settingsNavAbout, Label: "Updates", LabelKey: "settings.text.updates", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsAboutUpdates, Owner: settingsOwnerAbout},
	{ID: settingsNavAbout + ".updates.state", Parent: settingsNavAbout + ".updates", Label: "Current / Latest / Installer / Release notes", Kind: settingsNavState, Axis: settingsAxisGlobal, Value: settingsNoopValue},
	{ID: settingsNavAbout + ".updates.check", Parent: settingsNavAbout + ".updates", Label: "Check for updates", LabelKey: "settings.text.check_for_updates", Kind: settingsNavAction, Axis: settingsAxisGlobal, Value: settingsUpdateCheck, Owner: settingsOwnerAbout},
	{ID: settingsNavAbout + ".updates.apply", Parent: settingsNavAbout + ".updates", Label: "Update now", LabelKey: "settings.text.update_now", Kind: settingsNavAction, Axis: settingsAxisGlobal, Value: settingsUpdateApply, Owner: settingsOwnerAbout},
	{ID: settingsNavAbout + ".welcome", Parent: settingsNavAbout, Label: "Welcome", LabelKey: "settings.about.welcome", Kind: settingsNavView, Axis: settingsAxisGlobal, Value: settingsWelcomeShow, Owner: settingsOwnerAbout},
	{ID: settingsNavAbout + ".quit", Parent: settingsNavAbout, Label: "Quit Projmux", LabelKey: "settings.text.quit_projmux", Kind: settingsNavConfirm, EntryKind: settingsEntryNavigation, Axis: settingsAxisGlobal, Value: settingsQuitOpen, Owner: settingsOwnerAbout, Note: "app-owned runtime and socket impact"},

	// Project scope --------------------------------------------------------
	{ID: settingsNavScopeProject, Label: "Project", LabelKey: i18n.KeySettingsRootProjectTab, Kind: settingsNavView, Axis: settingsAxisProject},
	{ID: settingsNavProjectAutomation, Parent: settingsNavScopeProject, Label: "Automation", LabelKey: "settings.text.automation", Kind: settingsNavView, Axis: settingsAxisProject, Value: settingsSectionProjectAutomation, Owner: settingsOwnerRoot},
	{ID: settingsNavProjectTrust, Parent: settingsNavProjectAutomation, Label: "Trust", LabelKey: "settings.text.trust", Kind: settingsNavView, Axis: settingsAxisProject, Value: settingsSectionProjectTrust, Owner: settingsOwnerProjectAutomation},
	{ID: settingsNavProjectTrust + ".state", Parent: settingsNavProjectTrust, Label: "Trust state / Project config hash / Source", Kind: settingsNavState, Axis: settingsAxisProject, Value: settingsNoopValue},
	{ID: settingsNavProjectTrust + ".approve", Parent: settingsNavProjectTrust, Label: "Trust or refresh approval", Kind: settingsNavAction, Axis: settingsAxisProject, Dynamic: true},
	{ID: settingsNavProjectTrust + ".revoke", Parent: settingsNavProjectTrust, Label: "Revoke trust", Kind: settingsNavConfirm, Axis: settingsAxisProject, Dynamic: true},
	{ID: settingsNavProjectHooks, Parent: settingsNavProjectAutomation, Label: "Project hooks", LabelKey: "settings.text.project_hooks_lower", Kind: settingsNavView, Axis: settingsAxisProject, Value: settingsSectionProjectHooks, Owner: settingsOwnerProjectAutomation},
	{ID: settingsNavProjectHooks + ".lifecycle", Parent: settingsNavProjectHooks, Label: "Session lifecycle", LabelKey: "settings.text.session_lifecycle", Kind: settingsNavView, Axis: settingsAxisProject, Value: settingsProjectAutomationLifecycle, Owner: settingsOwnerProjectAutomation},
	{ID: settingsNavProjectHooks + ".lifecycle.event", Parent: settingsNavProjectHooks + ".lifecycle", Label: "<lifecycle event>", Kind: settingsNavView, Axis: settingsAxisProject, Dynamic: true, Note: "Before session create / After session create / After session attach"},
	{ID: settingsNavProjectHooks + ".lifecycle.event.state", Parent: settingsNavProjectHooks + ".lifecycle.event", Label: "Command / Effective / Source / Trust", Kind: settingsNavState, Axis: settingsAxisProject, Value: settingsNoopValue},
	{ID: settingsNavProjectHooks + ".lifecycle.event.edit", Parent: settingsNavProjectHooks + ".lifecycle.event", Label: "Add or edit command", Kind: settingsNavEdit, Axis: settingsAxisProject, Dynamic: true},
	{ID: settingsNavProjectHooks + ".lifecycle.event.remove", Parent: settingsNavProjectHooks + ".lifecycle.event", Label: "Remove command", Kind: settingsNavConfirm, Axis: settingsAxisProject, Dynamic: true},
	{ID: settingsNavProjectHooks + ".send-noti", Parent: settingsNavProjectHooks, Label: "After notification queued", LabelKey: "settings.text.after_notification_queued", Kind: settingsNavView, Axis: settingsAxisProject, Value: settingsProjectAutomationSendNoti, Owner: settingsOwnerProjectAutomation},
	{ID: settingsNavProjectHooks + ".send-noti.state", Parent: settingsNavProjectHooks + ".send-noti", Label: "Command / Effective / Source / Trust", Kind: settingsNavState, Axis: settingsAxisProject, Value: settingsNoopValue},
	{ID: settingsNavProjectHooks + ".send-noti.edit", Parent: settingsNavProjectHooks + ".send-noti", Label: "Add or edit command", Kind: settingsNavEdit, Axis: settingsAxisProject, Dynamic: true},
	{ID: settingsNavProjectHooks + ".send-noti.remove", Parent: settingsNavProjectHooks + ".send-noti", Label: "Remove command", Kind: settingsNavConfirm, Axis: settingsAxisProject, Dynamic: true},

	{ID: settingsNavProjectSnapshots, Parent: settingsNavScopeProject, Label: "Snapshots", LabelKey: "settings.text.snapshots", Kind: settingsNavView, Axis: settingsAxisProject, Value: settingsSectionProjectSessionState, Owner: settingsOwnerRoot},
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
	settingsNavByIDIndex = make(map[string]settingsNavNode, len(settingsNodeCatalog))
	settingsNavByValueIndex = make(map[string]settingsNavNode, len(settingsNodeCatalog))
	settingsNavChildIndex = make(map[string][]settingsNavNode, len(settingsNodeCatalog))
	for _, node := range settingsNodeCatalog {
		settingsNavByIDIndex[node.ID] = node
		if node.Value != "" && (node.Value != settingsNoopValue || node.Hidden) {
			settingsNavByValueIndex[node.Value] = node
		}
		if !node.Hidden {
			settingsNavChildIndex[node.Parent] = append(settingsNavChildIndex[node.Parent], node)
		}
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

// settingsNavLabelLocale projects a static label directly from the node's
// explicit key. It intentionally does not consult the literal-to-key UI
// registry used for legacy/composed picker text.
func settingsNavLabelLocale(locale i18n.Locale, id string) string {
	node, ok := settingsNavByIDIndex[id]
	if !ok {
		return ""
	}
	label, key := node.entryLabel()
	if key == "" {
		return label
	}
	return localizeText(locale, key, label)
}

func (node settingsNavNode) entryLabel() (string, i18n.Key) {
	if node.EntryLabel != "" || node.EntryLabelKey != "" {
		return node.EntryLabel, node.EntryLabelKey
	}
	return node.Label, node.LabelKey
}

func (node settingsNavNode) interactionKind() settingsEntryKind {
	if node.EntryKind != "" {
		return node.EntryKind
	}
	switch node.Kind {
	case settingsNavView, settingsNavChoice:
		return settingsEntryNavigation
	case settingsNavState:
		return settingsEntryPassive
	case settingsNavToggle, settingsNavEdit, settingsNavAction, settingsNavConfirm:
		return settingsEntryActionable
	default:
		return ""
	}
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
