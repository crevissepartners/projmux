// Package tmuxopts holds the canonical spelling of the projmux-owned tmux
// option names. It is a dependency-free leaf so the generated tmux config, the
// session-state replay adapter, and the resource metadata mirror can all name
// the same option without duplicating string literals or importing each other.
//
// These options are transport. tmux `$N`, `@N`, `%N`, `pane_title`, and
// `window_name` are runtime targets and display sources, never Projmux
// identity.
package tmuxopts

const (
	// ProjectUIDSession is session-scoped and carries the uid of the Project
	// the session projects. The session itself owns no identity.
	ProjectUIDSession = "@projmux_project_uid"
	// ProjectNameSession is session-scoped and mirrors Project metadata.name
	// for human-readable tmux inspection.
	ProjectNameSession = "@projmux_project_name"
	// ProjectPathSession is the existing session anchor holding the cwd a
	// session was created with. Legacy import reads it as the Project root.
	ProjectPathSession = "@projmux_project_path"

	// WindowUID is window-scoped and carries Window metadata.uid.
	// Window-scoped projmux options are new in the resource metadata model:
	// every projmux option that existed before was pane-, session-, or
	// global-scoped.
	WindowUID = "@projmux_window_uid"
	// WindowName is window-scoped and mirrors Window metadata.name. The same
	// value is mirrored into the tmux `window_name`.
	WindowName = "@projmux_window_name"

	// PaneUID is pane-scoped and carries Pane metadata.uid.
	PaneUID = "@projmux_pane_uid"
	// PaneName is the canonical spelling of the pane-label option. It is the
	// transport mirror for Pane metadata.name; in the resource model the
	// value is a name, not a label, and metadata.labels stays reserved for
	// key/value classification.
	PaneName = "@projmux_pane_label"

	// AgentProviderPane is the pane-scoped provider option read during legacy
	// naming migration.
	AgentProviderPane = "@projmux_ai_agent"
	// AgentTopicPane is the pane-scoped topic option. The topic is a derived
	// display source only and is never a name seed.
	AgentTopicPane = "@projmux_ai_topic"
	// AgentSessionIDPane and AgentThreadIDPane are the pane-scoped provider
	// conversation identifiers the AI routes write when they launch an agent.
	//
	// They are a live routing index, not identity: hook ingest matches an
	// incoming event against them to decide which pane it belongs to. Agent
	// runtime linkage reads them for one narrow purpose -- deciding whether a
	// live agent pane is the runtime of an Agent resource that already records
	// the same conversation in status.sessionRef. Neither value is ever a name
	// seed, a selector, or a uid.
	AgentSessionIDPane = "@projmux_ai_session_id"
	AgentThreadIDPane  = "@projmux_ai_thread_id"

	// AutomaticRenameWindow is turned off on every registry-managed Window so
	// a focused-Pane change cannot overwrite the Window name. The global
	// `automatic-rename on` default is deliberately left alone: unmanaged
	// windows keep the existing visible-pane-label rename behavior.
	AutomaticRenameWindow = "automatic-rename"
)
