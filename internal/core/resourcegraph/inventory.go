package resourcegraph

import (
	"slices"
	"strings"
)

// ObjectKind is the closed set of tmux runtime object kinds.
//
// It is deliberately separate from metadata.Kind. A tmux session is a runtime
// object with no resource kind of its own -- it is the 1:1 projection of a
// Project -- and Agent is a resource kind with no tmux object at all. Sharing
// one enum would force both lies.
type ObjectKind string

const (
	ObjectSession ObjectKind = "session"
	ObjectWindow  ObjectKind = "window"
	ObjectPane    ObjectKind = "pane"
)

// ObjectKinds returns the runtime kinds outermost first, which is the order a
// topology is read in.
func ObjectKinds() []ObjectKind {
	return []ObjectKind{ObjectSession, ObjectWindow, ObjectPane}
}

func objectKindRank(kind ObjectKind) int {
	return slices.Index(ObjectKinds(), kind)
}

// Session is one observed tmux session with the projmux options that carry
// exact evidence about it.
//
// ID is the stable `$N` session id, and it is the only routing handle here: a
// session name can be reused or renamed between an observation and a later
// call, an id cannot. Name is reported for display.
type Session struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// ProjectUID is @projmux_project_uid: the uid of the Project this session
	// projects. The session owns no identity of its own.
	ProjectUID string `json:"projectUID,omitempty"`
	// ProjectName is @projmux_project_name, a display mirror.
	ProjectName string `json:"projectName,omitempty"`
	// Root is @projmux_project_path, the cwd anchor the session was created
	// with. It is reported, never matched: root-based attribution is exactly
	// the heuristic merge this package refuses.
	Root string `json:"root,omitempty"`
	// Role is the raw @projmux_session_role value. Only the exact
	// ControlSessionRole spelling on an app-owned host means anything.
	Role string `json:"role,omitempty"`
	// Ephemeral is @projmux_ephemeral == EphemeralMarker: an auto-attach
	// scratch session, which is never a Registry Project.
	Ephemeral bool `json:"ephemeral,omitempty"`
}

// Window is one observed tmux window.
type Window struct {
	// ID is the stable `@N` window id.
	ID string `json:"id"`
	// SessionID is the `$N` id of the containing session. Containment comes
	// from tmux's own ids rather than from a name join, so a duplicate or
	// renamed session name cannot re-parent a window.
	SessionID string `json:"sessionID,omitempty"`
	// Index is the tmux window index, reported so a `session:index` target can
	// be rendered for an operator.
	Index string `json:"index,omitempty"`
	// DisplayName is the tmux window_name.
	DisplayName string `json:"displayName,omitempty"`
	// UID is @projmux_window_uid.
	UID string `json:"uid,omitempty"`
	// MirroredName is @projmux_window_name, the stable-name mirror.
	MirroredName string `json:"mirroredName,omitempty"`
}

// Pane is one observed tmux pane.
type Pane struct {
	// ID is the stable `%N` pane id, which is also its tmux target.
	ID string `json:"id"`
	// WindowID is the `@N` id of the containing window.
	WindowID string `json:"windowID,omitempty"`
	// UID is @projmux_pane_uid.
	UID string `json:"uid,omitempty"`
	// MirroredName is @projmux_pane_label, the Pane stable-name mirror.
	MirroredName string `json:"mirroredName,omitempty"`
	// AgentProvider is @projmux_ai_agent. Its presence proves the pane was
	// launched as an agent pane; it is not an Agent uid, because no tmux option
	// carries one.
	AgentProvider string `json:"agentProvider,omitempty"`
	// Title is the tmux pane_title, a display source only.
	Title string `json:"title,omitempty"`
}

// Scope names one independently observable half of an Inventory.
//
// The scopes are separate so a partial failure stays partial. A windows query
// that fails must not discard a successful panes query: a Pane whose tmux pane
// is provably gone is still offline even when the window inventory could not be
// read.
type Scope string

const (
	ScopeHostMode Scope = "host-mode"
	ScopeSessions Scope = "sessions"
	ScopeWindows  Scope = "windows"
	ScopePanes    Scope = "panes"
)

// Scopes returns the scopes in observation order.
func Scopes() []Scope {
	return []Scope{ScopeHostMode, ScopeSessions, ScopeWindows, ScopePanes}
}

func scopeRank(scope Scope) int {
	return slices.Index(Scopes(), scope)
}

// Unavailability is one scope that could not be observed, with the reason.
//
// It is the whole answer to "why is this row not live". Without it a failed
// tmux query and an empty tmux server produce the same graph, and a consumer
// cannot tell "nothing is running" from "I could not look".
type Unavailability struct {
	Scope  Scope  `json:"scope"`
	Reason string `json:"reason"`
}

// Inventory is one observation of exactly one tmux server.
//
// It is a value, not a reader: Resolve takes it as data so the join is pure and
// so a test can state a machine state directly instead of scripting tmux
// output.
type Inventory struct {
	Transport   Transport        `json:"transport"`
	HostMode    HostMode         `json:"hostMode"`
	Sessions    []Session        `json:"sessions,omitempty"`
	Windows     []Window         `json:"windows,omitempty"`
	Panes       []Pane           `json:"panes,omitempty"`
	Unavailable []Unavailability `json:"unavailable,omitempty"`
}

// Clone returns a deep copy so a resolved graph can never observe its snapshot
// changing under it.
func (i Inventory) Clone() Inventory {
	out := i
	out.Sessions = slices.Clone(i.Sessions)
	out.Windows = slices.Clone(i.Windows)
	out.Panes = slices.Clone(i.Panes)
	out.Unavailable = slices.Clone(i.Unavailable)
	return out
}

// Available reports whether scope was observed successfully.
func (i Inventory) Available(scope Scope) bool {
	_, unavailable := i.Unavailability(scope)
	return !unavailable
}

// Unavailability returns the recorded failure for scope, if any.
func (i Inventory) Unavailability(scope Scope) (Unavailability, bool) {
	for _, entry := range i.Unavailable {
		if entry.Scope == scope {
			return entry, true
		}
	}
	return Unavailability{}, false
}

// withUnavailable records a scope failure, keeping the first reason observed for
// a scope so a retry cannot overwrite the original cause.
func (i Inventory) withUnavailable(scope Scope, reason string) Inventory {
	if _, exists := i.Unavailability(scope); exists {
		return i
	}
	i.Unavailable = append(i.Unavailable, Unavailability{Scope: scope, Reason: strings.TrimSpace(reason)})
	slices.SortStableFunc(i.Unavailable, func(a, b Unavailability) int {
		return scopeRank(a.Scope) - scopeRank(b.Scope)
	})
	return i
}

// MarkUnavailable is the adapter-facing spelling of a scope failure.
func (i Inventory) MarkUnavailable(scope Scope, reason string) Inventory {
	return i.withUnavailable(scope, reason)
}

// isControlSession reports whether this session carries the exact control role
// on an app-owned host.
//
// Both halves are required. A control marker on a server projmux does not own
// proves nothing: any process can set an option on the operator's tmux, and
// honoring it there would let an unrelated session claim a projmux role.
func (s Session) isControlSession(host HostMode) bool {
	return host == HostModeAppOwned && strings.TrimSpace(s.Role) == ControlSessionRole
}
