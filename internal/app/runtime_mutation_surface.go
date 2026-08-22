package app

type runtimeMutationSurfaceDisposition string

const (
	runtimeMutationSurfacePlanned runtimeMutationSurfaceDisposition = "planned"
	runtimeMutationSurfaceExempt  runtimeMutationSurfaceDisposition = "exempt"
)

type runtimeMutationSurface struct {
	ID, Producer, Handler                string
	SemanticClass, RootKinds, OwnerRoute string
	PlanVerb, Guard, Effect              string
	// LegacyID is populated for every generated catalog surface. ID is always
	// the public Settings CanonicalID; LegacyID proves the shipped artifact
	// alias without making it mutation authority.
	LegacyID    string
	Disposition runtimeMutationSurfaceDisposition
}

var closedTmuxTopologyMutationVerbs = map[string]bool{
	"new-session": true, "new-window": true, "split-window": true,
	"kill-session": true, "kill-window": true, "kill-pane": true, "kill-server": true,
	"rename-session": true, "rename-window": true,
	"break-pane": true, "join-pane": true, "move-pane": true, "move-window": true,
	"link-window": true, "unlink-window": true, "respawn-pane": true, "respawn-window": true,
	"swap-pane": true, "swap-window": true, "rotate-window": true, "resize-pane": true,
}

func plannedSurface(id, producer, handler, class, roots, owner string, verb runtimeMutationVerb, guard, effect string) runtimeMutationSurface {
	return runtimeMutationSurface{ID: id, Producer: producer, Handler: handler, SemanticClass: class,
		RootKinds: roots, OwnerRoute: owner, PlanVerb: string(verb), Guard: guard, Effect: effect,
		Disposition: runtimeMutationSurfacePlanned}
}

func exemptSurface(id, producer, handler, class, roots, owner, reason, effect string) runtimeMutationSurface {
	return runtimeMutationSurface{ID: id, Producer: producer, Handler: handler, SemanticClass: class,
		RootKinds: roots, OwnerRoute: owner, PlanVerb: "exempt:" + id, Guard: reason, Effect: effect,
		Disposition: runtimeMutationSurfaceExempt}
}

func catalogSurface(row runtimeMutationSurface, legacyID string) runtimeMutationSurface {
	row.LegacyID = legacyID
	return row
}

// runtimeMutationSurfaces is the closed product-surface table. Each generated
// or user producer points to one owner route and every managed route points
// back to its producer. Exemptions are individual semantic actions, never
// raw-verb wildcards.
var runtimeMutationSurfaces = []runtimeMutationSurface{
	plannedSurface("project.materialize", "resource create/start and first-use Project open", "materializer", "managed topology", "Project", "Registry Project UID", mutationCreateSession, "exact route, UID/root/name absence or ownership", "canonical session/window/pane topology"),
	plannedSurface("public.create-window", "public create Window", "canonical createWindow", "managed topology", "Project|ControlSession", "declared root UID and exact anchor", mutationCreateWindow, "socket, root UID, session and anchor containment", "one owned Window and primary Pane"),
	plannedSurface("public.create-pane", "public create Pane/Agent", "canonical createPane", "managed topology", "Project|ControlSession", "declared owner Window UID and exact anchor", mutationCreatePane, "socket, root UID, Window UID and anchor containment", "one owned Pane or Agent Pane"),
	catalogSurface(plannedSurface("catalog.window.create", "generated catalog new-window (legacy alias)", "internal tmux window-create", "managed topology", "Project|ControlSession", "exact anchor Pane owner chain", mutationCreateWindow, "socket, root UID, session and anchor containment", "one owned Window and primary Pane"), "new-window"),
	catalogSurface(plannedSurface("catalog.window.rename", "generated catalog rename-window (legacy alias)", "internal tmux window-rename", "managed presentation identity", "Project|ControlSession", "exact anchor Pane owner chain", mutationRenameWindow, "socket, Window UID and containment", "display name changes; stable metadata identity remains"), "rename-window"),
	plannedSurface("pane-menu.split-right", "generated Pane menu Horizontal Split", "internal tmux pane-menu split-right", "managed topology", "Project|ControlSession", "exact clicked Pane", mutationCreatePane, "socket, root UID and anchor containment", "one owned right Pane"),
	plannedSurface("pane-menu.split-down", "generated Pane menu Vertical Split", "internal tmux pane-menu split-down", "managed topology", "Project|ControlSession", "exact clicked Pane", mutationCreatePane, "socket, root UID and anchor containment", "one owned down Pane"),
	plannedSurface("pane-menu.kill", "generated Pane menu Kill", "canonical delete Pane", "managed lifecycle", "Project|ControlSession", "exact clicked Pane UID", mutationKillPane, "batch exact socket/UID/containment before commit", "exact Pane absent after durable result flush"),
	plannedSurface("pane.canonical-delete", "resource delete Pane/Agent", "tmuxPaneDeleteRuntime", "managed lifecycle", "Project|ControlSession", "Registry Pane UID", mutationKillPane, "batch exact socket/UID/containment", "selected owned Panes absent"),
	plannedSurface("window.canonical-delete", "resource delete Window", "tmuxWindowDeleteRuntime", "managed lifecycle", "Project|ControlSession", "Registry Window UID", mutationKillWindow, "all exact targets guarded before first write", "selected owned Windows absent"),
	plannedSurface("project.delete-cascade-pane", "resource delete Project Pane cascade", "tmuxPaneDeleteRuntime", "managed lifecycle", "Project", "Registry Project UID descendant Pane closure", mutationKillPane, "all descendant Pane handles, UIDs, and containment guarded before first write", "only selected Project descendant Panes absent after durable commit"),
	plannedSurface("project.delete-cascade-window", "resource delete Project Window cascade", "tmuxWindowDeleteRuntime", "managed lifecycle", "Project", "Registry Project UID descendant Window closure", mutationKillWindow, "all descendant Window handles, UIDs, and containment guarded before first write", "only selected Project descendant Windows absent after durable commit"),
	catalogSurface(plannedSurface("catalog.project-sidebar.runtime.stop", "generated Project sidebar Ctrl-X (legacy alias Sidebar:KillSession)", "executeManagedRuntimeStop", "managed lifecycle", "Project", "resolved Project UID", mutationStopManagedSession, "exact app route and reattributed session ID/UID", "exact Project runtime session absent"), "Sidebar:KillSession"),
	catalogSurface(plannedSurface("catalog.session-picker.runtime.stop", "generated Session picker Ctrl-X (legacy alias SessionPopup:KillSession)", "executeManagedRuntimeStop", "managed lifecycle", "Project", "resolved Project UID", mutationStopManagedSession, "unknown, unmatched, and ControlSession attribution refuses with zero kill", "exact Project runtime session absent"), "SessionPopup:KillSession"),
	plannedSurface("control.bootstrap", "projmux shell explicit ControlSession", "provisionAppSession/prepareControlSession", "managed lifecycle", "ControlSession", "stable declaration then exact returned $/@/%", mutationBootstrapControlSession, "route, app marker, absence, operation lease", "owned ControlSession root/window/pane identity"),
	plannedSurface("controller.identity", "generated lifecycle trigger and explicit reconcile", "resource controller plan", "managed identity", "Project|ControlSession", "resourcegraph exact handle", mutationWriteIdentity, "controller attribution and containment", "identity mirrors only; lifecycle verbs refused"),
	plannedSurface("app.route-marker", "tmux config apply", "typed route marker write", "managed route identity", "all app-owned", "exact invocation socket", mutationWriteRouteMarker, "source-file success and app ownership", "logical route marker equals invocation route"),
	plannedSurface("layout.auto-even-split", "canonical create post-create layout", "equalizeSplitLayout", "managed presentation plan", "Project|ControlSession", "exact anchor Window and Pane handles", mutationWriteLayout, "all peer Pane containment guarded before first resize", "every peer receives one best-effort even-split resize attempt"),
	plannedSurface("startup.shell-project", "projmux shell Project-default", "ensureProjectSession/materializer", "managed lifecycle", "Project", "Registry Project UID resolved from cwd", mutationCreateSession, "exact route, Project UID/root and absence/ownership", "canonical Project runtime exists before foreground attach"),
	plannedSurface("startup.sidebar-project", "Project sidebar open", "ensureProjectSession/materializer", "managed lifecycle", "Project", "selected Registry Project UID", mutationCreateSession, "exact route, Project UID/root and absence/ownership", "selected Project runtime exists before navigation"),
	plannedSurface("startup.current-project", "current Project binding", "ensureProjectSession/materializer", "managed lifecycle", "Project", "Registry Project UID resolved from cwd", mutationCreateSession, "exact route, Project UID/root and absence/ownership", "current Project runtime exists before navigation"),
	plannedSurface("startup.attach-project", "attach Project fallback", "ensureProjectSession/materializer", "managed lifecycle", "Project", "Registry Project UID", mutationCreateSession, "exact route, Project UID/root and absence/ownership", "Project runtime exists before attach"),
	plannedSurface("startup.session-picker-project", "Session picker Project selection", "ensureProjectSession/materializer", "managed lifecycle", "Project", "selected Registry Project UID", mutationCreateSession, "exact route, Project UID/root and absence/ownership", "selected Project runtime exists before navigation"),
	exemptSurface("runtime.observation", "typed inventory readers", "tmux read adapters", "observation", "all", "explicit route", "read-only argv", "no mutation"),
	exemptSurface("config.migration", "config apply migration", "managed ingest adapter", "migration identity", "configured tmux server", "exact physically bound apply target", "pre-source physical identity; bounded compatibility metadata only", "legacy presentation/identity options converge"),
	exemptSurface("key-sequence.retirement", "config apply key state retirement", "generated key adapter", "generated artifact maintenance", "configured tmux server", "exact physically bound apply target", "pre-source physical identity; generated key state only", "retired generated state absent"),
	exemptSurface("sidebar.origin-restore", "popup close", "restoreSidebarOriginSession", "navigation", "all", "origin client/session", "focus-only", "origin session focused"),
	exemptSurface("popup.display", "generated picker and preview", "tmux popup adapter", "presentation", "all", "origin client", "popup-only", "popup displayed"),
	exemptSurface("agent.presentation", "Agent topic/interaction projection", "Pane presentation options", "presentation", "Project|ControlSession", "exact Agent Pane", "topic/state/badge only; UID/topology unchanged", "Agent presentation changes"),
	exemptSurface("startup.presentation", "persistent/ephemeral startup recipe", "startup Pane option writer", "presentation metadata", "Project|ephemeral", "exact returned Pane", "startup recipe annotations only; lifecycle/topology unchanged", "startup recipe kind and command annotations change"),
	exemptSurface("sessionstate.replay-metadata", "session-state snapshot metadata", "snapshot option adapter", "generated artifact metadata", "snapshot", "exact replay Pane/session", "replay metadata only", "snapshot provenance recorded"),
	exemptSurface("binding.convergence", "generated config lifecycle convergence", "binding convergence adapter", "managed identity", "Project|ControlSession", "exact hook socket", "identity repair uses controller policy", "blank managed mirrors repaired"),
	exemptSurface("notification.focus", "notification focus/status actions", "focus adapter", "navigation", "all", "exact source Pane/client", "focus and acknowledgement only", "source focused or notification state changes"),
	exemptSurface("create.reentrancy", "canonical create hook deferral", "create lease cleanup", "operation metadata", "Project", "exact operation session", "lease metadata only", "binding convergence deferred safely"),

	catalogSurface(plannedSurface("catalog.agent-pane.launch-default.right", "generated catalog default split", "canonical createPaneFromIntent", "managed topology", "Project|ControlSession", "exact current Pane owner chain", mutationCreatePane, "socket, root UID and anchor containment", "one owned Pane"), "ai-split-right"),
	catalogSurface(plannedSurface("catalog.agent-pane.launch-default.down", "generated catalog default split", "canonical createPaneFromIntent", "managed topology", "Project|ControlSession", "exact current Pane owner chain", mutationCreatePane, "socket, root UID and anchor containment", "one owned Pane"), "ai-split-down"),
	catalogSurface(plannedSurface("catalog.agent.create.codex.right", "generated catalog provider split", "canonical createPaneFromIntent", "managed topology", "Project|ControlSession", "exact current Pane owner chain", mutationCreatePane, "socket, root UID and anchor containment", "one owned Agent Pane"), "ai-split-codex-right"),
	catalogSurface(plannedSurface("catalog.agent.create.codex.down", "generated catalog provider split", "canonical createPaneFromIntent", "managed topology", "Project|ControlSession", "exact current Pane owner chain", mutationCreatePane, "socket, root UID and anchor containment", "one owned Agent Pane"), "ai-split-codex-down"),
	catalogSurface(plannedSurface("catalog.agent.create.claude.right", "generated catalog provider split", "canonical createPaneFromIntent", "managed topology", "Project|ControlSession", "exact current Pane owner chain", mutationCreatePane, "socket, root UID and anchor containment", "one owned Agent Pane"), "ai-split-claude-right"),
	catalogSurface(plannedSurface("catalog.agent.create.claude.down", "generated catalog provider split", "canonical createPaneFromIntent", "managed topology", "Project|ControlSession", "exact current Pane owner chain", mutationCreatePane, "socket, root UID and anchor containment", "one owned Agent Pane"), "ai-split-claude-down"),
	catalogSurface(plannedSurface("catalog.pane.create.shell.right", "generated catalog shell split", "canonical createPaneFromIntent", "managed topology", "Project|ControlSession", "exact current Pane owner chain", mutationCreatePane, "socket, root UID and anchor containment", "one owned shell Pane"), "ai-split-shell-right"),
	catalogSurface(plannedSurface("catalog.pane.create.shell.down", "generated catalog shell split", "canonical createPaneFromIntent", "managed topology", "Project|ControlSession", "exact current Pane owner chain", mutationCreatePane, "socket, root UID and anchor containment", "one owned shell Pane"), "ai-split-shell-down"),
	plannedSurface("native-picker.provider-create", "native AI provider picker selection (provider-picker)", "canonical createPaneFromIntent", "managed topology", "Project|ControlSession", "exact popup-origin Pane owner chain", mutationCreatePane, "socket, root UID and anchor containment", "one owned Agent or shell Pane"),
	plannedSurface("native-picker.resume-create", "native AI resume picker selection (resume-picker)", "canonical createPaneFromIntent", "managed topology", "Project|ControlSession", "exact popup-origin Pane owner chain", mutationCreatePane, "socket, root UID and anchor containment", "one owned Agent Pane with requested conversation"),
	catalogSurface(plannedSurface("catalog.project.open-for-current-directory", "generated catalog current Project", "switch open via canonical Project startup", "managed lifecycle/navigation", "Project", "Registry Project UID resolved from current cwd", mutationCreateSession, "exact route, Project UID/root and absence/ownership", "focus existing or canonically materialized Project"), "current-project-session"),
	catalogSurface(exemptSurface("catalog.window.focus-previous", "generated catalog previous-window", "tmux navigation", "navigation", "Project|ControlSession", "active client", "focus-only; no topology or identity change", "previous Window focused"), "previous-window"),
	catalogSurface(exemptSurface("catalog.window.focus-next", "generated catalog next-window", "tmux navigation", "navigation", "Project|ControlSession", "active client", "focus-only; no topology or identity change", "next Window focused"), "next-window"),
	catalogSurface(exemptSurface("catalog.pane.focus-left", "generated catalog pane focus", "tmux navigation", "navigation", "Project|ControlSession", "active Pane", "focus-only; no topology or identity change", "adjacent Pane focused"), "select-pane-left"),
	catalogSurface(exemptSurface("catalog.pane.focus-right", "generated catalog pane focus", "tmux navigation", "navigation", "Project|ControlSession", "active Pane", "focus-only; no topology or identity change", "adjacent Pane focused"), "select-pane-right"),
	catalogSurface(exemptSurface("catalog.pane.focus-up", "generated catalog pane focus", "tmux navigation", "navigation", "Project|ControlSession", "active Pane", "focus-only; no topology or identity change", "adjacent Pane focused"), "select-pane-up"),
	catalogSurface(exemptSurface("catalog.pane.focus-down", "generated catalog pane focus", "tmux navigation", "navigation", "Project|ControlSession", "active Pane", "focus-only; no topology or identity change", "adjacent Pane focused"), "select-pane-down"),
	catalogSurface(exemptSurface("catalog.pane.focus-last", "generated catalog last-pane", "tmux navigation", "navigation", "Project|ControlSession", "active client", "focus-only; no topology or identity change", "previous Pane focused"), "last-pane"),
	catalogSurface(exemptSurface("catalog.pane.rename", "generated catalog Pane label", "tmux presentation option", "presentation", "Project|ControlSession", "focused Pane", "label-only; Pane UID and topology unchanged", "Pane label changes"), "rename-pane-label"),
	catalogSurface(exemptSurface("catalog.mouse.toggle", "generated catalog mouse toggle", "tmux input option", "input policy", "all", "server global option", "input policy only", "mouse input mode toggles"), "toggle-mouse"),
	exemptSurface("pane.rebalance", "explicit rebalance panes", "select-layout", "presentation", "Project|ControlSession", "focused Window", "layout-only; topology and identity unchanged", "Pane geometry equalized"),
	exemptSurface("sessionstate.autosave-marker", "session-state autosave hook", "session option marker", "generated artifact metadata", "all", "observed session", "timestamp metadata only", "autosave timestamp changes"),
	exemptSurface("pane-menu.resume", "generated Pane menu AI Resume Picker", "Phase 2 resume picker", "native Agent navigation", "Project|ControlSession", "clicked Pane", "resume navigation only; selected create returns through canonical plan", "resume picker opens"),
	exemptSurface("pane-menu.swap-up", "generated Pane menu Swap Up", "tmux presentation", "presentation", "Project|ControlSession", "clicked Pane", "display-order only; identity unchanged", "Pane display order changes"),
	exemptSurface("pane-menu.swap-down", "generated Pane menu Swap Down", "tmux presentation", "presentation", "Project|ControlSession", "clicked Pane", "display-order only; identity unchanged", "Pane display order changes"),
	exemptSurface("pane-menu.mark", "generated Pane menu Mark/Unmark", "tmux presentation", "presentation", "Project|ControlSession", "clicked Pane", "mark-only", "Pane mark toggles"),
	exemptSurface("pane-menu.zoom", "generated Pane menu Zoom/Unzoom", "tmux presentation", "presentation", "Project|ControlSession", "clicked Window", "layout visibility only", "Window zoom toggles"),
	exemptSurface("pane-menu.mouse-forward", "generated MouseDown3Pane guard", "send-keys -M", "input forwarding", "all", "clicked Pane", "mouse-aware program owns event", "mouse event forwarded"),
	exemptSurface("shell.foreground-attach", "projmux shell foreground", "attach-session", "attach", "Project|ControlSession", "exact existing session", "non-creating exact attach; failed/unknown preflight cannot provision", "client attaches without provisioning authority"),
	exemptSurface("app.quit", "generated app quit", "quit route", "application lifecycle", "all app-owned runtime classes", "exact app-owned logical server", "server-wide kill requires app marker and logical route guard", "all Project, ControlSession, and ephemeral runtime on only the exact app server exits"),
	plannedSurface("attach.ensure-home", "attach auto fallback=home", "canonical ControlSession bootstrap", "managed lifecycle", "ControlSession", "home ControlSession declaration", mutationBootstrapControlSession, "exact app route, absence/ownership and operation lease", "owned Home ControlSession exists"),
	exemptSurface("attach.ephemeral-prune", "attach auto retention", "KillSession adapter", "runtime maintenance", "ephemeral", "observed ephemeral session", "attach-owned ephemeral cleanup", "stale ephemeral session absent"),
	exemptSurface("attach.ephemeral-create", "attach auto fallback=ephemeral", "CreateEphemeralSession adapter", "runtime maintenance", "ephemeral", "generated ephemeral name", "explicit ephemeral class", "one ephemeral session exists"),
	exemptSurface("standalone.prune", "explicit prune ephemeral", "prune route", "runtime maintenance", "ephemeral", "tagged ephemeral session", "explicit ephemeral class", "expired ephemeral session absent"),
	plannedSurface("manual.tagged-kill", "kill tagged", "executeUnmanagedRuntimeStop", "human runtime maintenance", "unmanaged|ephemeral", "exact observed session handle", mutationStopUnmanagedSession, "exact app route and immediate unowned/ephemeral reattribution", "selected runtime session absent"),
	plannedSurface("switch.manual-kill", "explicit switch kill", "executeUnmanagedRuntimeStop", "human runtime maintenance", "unmanaged|ephemeral", "exact observed session handle", mutationStopUnmanagedSession, "managed replacement and unknown attribution refuse", "selected runtime session absent"),
	plannedSurface("sidebar.unmanaged-candidate-stop", "Project sidebar discovered candidate Ctrl-X", "executeUnmanagedRuntimeStop", "human runtime maintenance", "unmanaged|ephemeral", "exact discovered runtime handle", mutationStopUnmanagedSession, "immediate pre-write reattribution refuses managed replacement", "selected unmanaged runtime session absent"),
	exemptSurface("replay.retired-snapshot", "explicit session-state restore", "integrations/sessionstate Replay", "generated artifact replay", "snapshot", "snapshot manifest", "isolated replay; no Registry-managed identity authority", "snapshot runtime reconstructed"),
	exemptSurface("config.apply-source", "generated tmux.conf apply", "source-file on exact physical route", "configuration convergence", "configured tmux server", "exact physically bound apply target", "pre-source physical identity; post-source app ownership and logical marker; managed handlers remain separately planned", "generated config declarations converge"),
	exemptSurface("trigger.after-new-window", "generated after-new-window hook", "observation trigger into controller.identity", "observation trigger", "all", "event exact socket", "hook argv performs no mutation; downstream writes require the typed controller plan", "controller.identity observes and may execute its guarded identity plan"),
	exemptSurface("trigger.after-split-window", "generated after-split-window hook", "observation trigger into controller.identity", "observation trigger", "all", "event exact socket", "hook argv performs no mutation; downstream writes require the typed controller plan", "controller.identity observes and may execute its guarded identity plan"),
	exemptSurface("trigger.after-kill-pane", "generated after-kill hook", "observation trigger into controller.identity", "observation trigger", "all", "event exact socket", "hook argv performs no mutation; downstream writes require the typed controller plan", "controller.identity observes and may execute its guarded identity plan"),
	exemptSurface("trigger.pane-exited", "generated pane-exited hook", "observation trigger into controller.identity", "observation trigger", "all", "event exact Pane and socket", "hook argv performs no mutation; downstream writes require the typed controller plan", "controller.identity observes and may execute its guarded identity plan"),
	exemptSurface("trigger.window-unlinked", "generated window-unlinked hook", "observation trigger into controller.identity", "observation trigger", "all", "event exact Window and socket", "hook argv performs no mutation; downstream writes require the typed controller plan", "controller.identity observes and may execute its guarded identity plan"),
	exemptSurface("trigger.attention-focus", "generated pane focus hooks", "attention state handler", "presentation trigger", "all", "event Pane", "attention metadata only", "attention arming/clearing is requested"),
	exemptSurface("trigger.recent-window-record", "generated Window selection hooks", "recent Window recorder", "navigation history trigger", "all", "active client/Window", "history metadata only", "recent Window history is refreshed"),
	exemptSurface("trigger.client-attached-welcome", "generated client-attached hook", "welcome popup handler", "presentation trigger", "all", "attached client", "popup-only", "welcome popup may open"),
	exemptSurface("config.generated-statusbar", "generated statusbar declarations and mouse handlers", "tmux presentation config", "presentation configuration", "all", "app or standalone config target", "status formats, navigation, and popup actions only", "statusbar presentation converges"),
	exemptSurface("config.generated-key-sequences", "generated key bindings and sequence state", "tmux input config", "input configuration", "all", "app or standalone config target", "closed catalog handlers and sequence metadata only", "generated keymap converges"),
	exemptSurface("resource.rename-project", "public resource rename Project", "Registry rename and Project name projection", "presentation identity", "Project", "Registry Project UID", "stable UID/topology/session name unchanged", "Project display identity changes"),
	plannedSurface("resource.rename-window", "public resource rename Window", "renameRuntimeWindow", "managed presentation identity", "Project|ControlSession", "Registry Window UID and exact runtime Window handle", mutationRenameWindow, "exact route, root UID and Window containment", "Window display name changes; stable UID/topology remain"),
	exemptSurface("resource.rename-pane", "public resource rename Pane", "Registry rename and Pane label projection", "presentation identity", "Project|ControlSession", "Registry Pane UID", "stable UID/topology unchanged", "Pane label projection changes"),
	exemptSurface("resource.rebind-project", "public Project root rebind", "Registry root and Project path projection", "configuration identity", "Project", "Registry Project UID", "Project path anchor only; runtime topology and UID unchanged", "Project root/path projection changes"),
	exemptSurface("settings.desktop-notify-option", "Settings desktop notification mode", "settings notification option writer", "presentation preference", "all", "ambient Settings tmux client", "global notification presentation option only; no managed resource authority", "notification presentation mode changes"),
	exemptSurface("settings.statusbar-decoration-option", "Settings statusbar decoration", "settings appearance option writer", "presentation preference", "all", "ambient Settings tmux client", "statusbar presentation options only; no managed resource authority", "statusbar decoration changes"),
	exemptSurface("settings.ai-badge-option", "Settings AI badge style", "settings appearance option writer", "presentation preference", "all", "ambient Settings tmux client", "Pane/statusbar presentation options only; no managed resource authority", "AI badge presentation changes"),
	exemptSurface("settings.generated-config-reload", "Settings keybinding/theme apply", "canonical generated config source", "configuration convergence", "all app-owned", "exact app logical and physical route", "generated declarations only; managed handlers remain planned", "generated config reloads"),
	exemptSurface("settings.key-sequence-retirement", "Settings generated sequence cleanup", "generated key retirement", "generated artifact maintenance", "all app-owned", "exact app logical and physical route", "retired generated sequence state only", "retired sequence state absent"),
	exemptSurface("ai.integrate-tmux-bell", "AI integration tmux bell setup", "global bell option and hook convergence", "operator configuration", "ambient operator-selected tmux", "explicit operator invocation; no managed resource authority", "bell option/hook metadata only", "AI bell integration converges or rolls back"),
	exemptSurface("notification.wsl-legacy-cleanup-marker", "legacy WSL notification cleanup", "global operation marker", "operation metadata", "ambient operator-selected tmux", "explicit notification migration; no managed resource authority", "cleanup completion marker only", "legacy cleanup remains idempotent"),
}

var nonMutationCatalogSurfaceRows = []struct{ canonical, legacy, class, effect string }{
	{"project-sidebar.toggle", "ProjectSidebarToggle", "popup navigation", "Project sidebar opens"},
	{"notification-sidebar.toggle", "NotifySidebarToggle", "popup navigation", "Notification sidebar opens"},
	{"session-picker.toggle", "SessionPopupToggle", "popup navigation", "Session picker opens"},
	{"resource-inspector.open", "Resources:Open", "read-only navigation", "resource inspector opens"},
	{"recent-windows.open", "RecentWindows:Open", "popup navigation", "recent Window picker opens"},
	{"agent-pane-launcher.toggle", "AISplitPickerToggle", "native picker navigation", "AI picker opens"},
	{"settings.toggle", "SettingsToggle", "settings navigation", "Settings opens"},
	{"agent-resume-picker.toggle", "AIResumePickerToggle", "native Agent navigation", "resume picker opens"},
	{"project-picker.toggle", "ProjectSwitcherToggle", "popup navigation", "Project picker opens"},
	{"project-sidebar.project.pin-toggle", "Sidebar:PinProject", "Registry preference", "Project pin toggles"},
	{"session-picker.snapshots.open", "SessionPopup:OpenState", "snapshot navigation", "snapshot view opens"},
	{"session-picker.preview.window-previous", "SessionPopup:CyclePreviewWindowPrev", "preview navigation", "preview Window changes"},
	{"session-picker.preview.window-next", "SessionPopup:CyclePreviewWindowNext", "preview navigation", "preview Window changes"},
	{"session-picker.preview.pane-previous", "SessionPopup:CyclePreviewPanePrev", "preview navigation", "preview Pane changes"},
	{"session-picker.preview.pane-next", "SessionPopup:CyclePreviewPaneNext", "preview navigation", "preview Pane changes"},
	{"notification-sidebar.focus-and-acknowledge", "NotifySidebar:FocusAndAck", "notification navigation", "source focused and notification acknowledged"},
	{"notification-sidebar.acknowledge", "NotifySidebar:Ack", "notification state", "notification acknowledged"},
	{"notification-sidebar.acknowledge-group", "NotifySidebar:AckGroup", "notification state", "notification group acknowledged"},
	{"notification-sidebar.clear-non-critical", "NotifySidebar:ClearNonCritical", "notification state", "non-critical notifications cleared"},
	{"notification-sidebar.clear-all", "NotifySidebar:ClearAll", "notification state", "notifications cleared"},
	{"notification-sidebar.clear-gone", "NotifySidebar:ClearGone", "notification state", "gone notifications cleared"},
	{"settings.tab-previous", "Settings:SwitchTabPrev", "settings navigation", "previous Settings tab focused"},
	{"settings.tab-next", "Settings:SwitchTabNext", "settings navigation", "next Settings tab focused"},
}

func init() {
	for _, row := range nonMutationCatalogSurfaceRows {
		runtimeMutationSurfaces = append(runtimeMutationSurfaces, catalogSurface(exemptSurface("catalog."+row.canonical,
			"generated catalog "+row.legacy, "non-mutating catalog handler", row.class, "all", "catalog action id",
			"no managed lifecycle/topology effect", row.effect), row.legacy))
	}
}
