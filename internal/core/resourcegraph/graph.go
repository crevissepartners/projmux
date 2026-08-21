package resourcegraph

import (
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// Class is the closed attribution set: what projmux may say about one object
// and, downstream, what it may do to it.
//
// The set is small on purpose. Every consumer that used to ask "is this mine?"
// asked it with a different predicate -- a uid lookup here, a session-name
// prefix there, an option probe somewhere else -- and each predicate had a
// different blind spot. One vocabulary means a later controller, picker, and
// diagnostic all refuse the same objects for the same stated reason.
type Class string

const (
	// ClassManaged is a Registry resource, or the runtime object bound to one
	// by exact uid evidence with no contradicting containment. It is the only
	// class a later reconcile may mutate.
	ClassManaged Class = "managed"
	// ClassRecoverable is a live object that mirrors a projmux uid this
	// Registry does not contain. Its identity is legible, so it is evidence for
	// an operator-driven recovery -- and it is never adopted automatically,
	// because a uid alone cannot rebuild an owner chain, a name reservation, or
	// an Agent.
	ClassRecoverable Class = "recoverable"
	// ClassControl is an app-owned session carrying the exact control role. It
	// is deliberately not a Project: Home is a control surface, and turning it
	// into a resource would give the operator's whole home directory a managed
	// identity it never asked for.
	ClassControl Class = "control"
	// ClassEphemeral is an auto-attach scratch session. It lives only in
	// runtime inventory and is never part of the Project hierarchy.
	ClassEphemeral Class = "ephemeral"
	// ClassUnattributed is an object with no mirrored identity that is
	// nonetheless inside projmux's own world: a plain window or pane an
	// operator opened inside a managed Project, or anything on a server projmux
	// started. It is visible and it is not managed.
	ClassUnattributed Class = "unattributed"
	// ClassForeign is an object with no mirrored identity and no managed
	// enclosure on a server projmux does not own. It belongs to the operator's
	// own tmux and projmux has no business touching it.
	ClassForeign Class = "foreign"
	// ClassConflict is exact evidence that contradicts itself: one uid claimed
	// by two live objects, or a claim whose containment names a different owner
	// than the Registry does. Nothing is bound, on purpose. Choosing one
	// claimant would be a coin flip that later authorizes a mutation.
	ClassConflict Class = "conflict"
)

// Classes returns the closed attribution set in declaration order.
func Classes() []Class {
	return []Class{
		ClassManaged, ClassRecoverable, ClassControl,
		ClassEphemeral, ClassUnattributed, ClassForeign, ClassConflict,
	}
}

// Status is the runtime overlay of one Registry row. The three inherited
// spellings match selector.Status exactly so a later UI cannot drift from the
// existing read verbs; StatusUnknown is the fourth state the existing rule had
// no way to express.
type Status string

const (
	// StatusLive means an exact runtime object was observed for this row.
	StatusLive Status = "live"
	// StatusOffline means the row was resolved against a readable observation
	// and no runtime object mirrors it.
	StatusOffline Status = "offline"
	// StatusMissingRoot means the owning Project lost its spec.root. It
	// outranks every runtime answer: the row needs an explicit rebind or prune
	// whatever tmux is doing.
	StatusMissingRoot Status = "missing-root"
	// StatusUnknown means the observation this row would have been judged
	// against could not be taken. It exists so a failed tmux query is not
	// reported as an offline machine: the Registry row is preserved, and the
	// Graph carries the reason in Unavailable.
	StatusUnknown Status = "unknown"
)

// ConflictReason is the closed set of contradictions exact evidence can produce.
type ConflictReason string

const (
	// ConflictDuplicateClaim is one uid mirrored by more than one live object.
	ConflictDuplicateClaim ConflictReason = "duplicate-runtime-claim"
	// ConflictOwnerMismatch is a claim whose live containment names a different
	// owner than the Registry records.
	ConflictOwnerMismatch ConflictReason = "owner-mismatch"
	// ConflictKindMismatch is a uid mirrored onto the wrong kind of tmux
	// object, such as a Pane uid on a window option.
	ConflictKindMismatch ConflictReason = "kind-mismatch"
)

// Conflict is one recorded contradiction. Targets are the exact tmux handles
// involved so an operator can go look at both sides.
type Conflict struct {
	Kind    ObjectKind     `json:"kind"`
	UID     string         `json:"uid"`
	Reason  ConflictReason `json:"reason"`
	Detail  string         `json:"detail"`
	Targets []string       `json:"targets,omitempty"`
}

// RuntimeRef is the exact transport handle of one observed object. It is
// routing, never identity: a consumer stores the uid and re-resolves the ref.
type RuntimeRef struct {
	Kind ObjectKind `json:"kind"`
	// ID is the stable tmux id -- $N, @N, or %N -- and the only safe target.
	ID string `json:"id"`
	// Target is the handle an operator would type: the session name, a
	// `session:index` window target, or the pane id.
	Target string `json:"target,omitempty"`
	// Name is the display name tmux reports, for operator context only.
	Name string `json:"name,omitempty"`
}

// ProjectNode is one Registry Project with its runtime overlay.
type ProjectNode struct {
	Project     coremetadata.Project `json:"project"`
	Class       Class                `json:"class"`
	Status      Status               `json:"status"`
	MissingRoot bool                 `json:"missingRoot,omitempty"`
	Runtime     *RuntimeRef          `json:"runtime,omitempty"`
}

// ControlSessionNode is one Registry ControlSession with its runtime overlay.
//
// A control session is a root, not a Project: it has no filesystem root and is
// bound by its exact spec.session plus the trusted control-role marker.
type ControlSessionNode struct {
	ControlSession coremetadata.ControlSession `json:"controlSession"`
	Class          Class                       `json:"class"`
	Status         Status                      `json:"status"`
	Runtime        *RuntimeRef                 `json:"runtime,omitempty"`
}

// WindowNode is one Registry Window with its runtime overlay and resolved owner.
type WindowNode struct {
	Window      coremetadata.Window `json:"window"`
	RootKind    coremetadata.Kind   `json:"rootKind,omitempty"`
	RootUID     string              `json:"rootUID,omitempty"`
	ProjectUID  string              `json:"projectUID,omitempty"`
	Class       Class               `json:"class"`
	Status      Status              `json:"status"`
	MissingRoot bool                `json:"missingRoot,omitempty"`
	Runtime     *RuntimeRef         `json:"runtime,omitempty"`
}

// PaneNode is one Registry Pane with its runtime overlay and resolved owner
// chain. WindowUID is the effective containing Window: for an Agent-owned Pane
// that is the Agent's Window, which is the containment tmux can testify to.
type PaneNode struct {
	Pane        coremetadata.Pane `json:"pane"`
	AgentUID    string            `json:"agentUID,omitempty"`
	WindowUID   string            `json:"windowUID,omitempty"`
	RootKind    coremetadata.Kind `json:"rootKind,omitempty"`
	RootUID     string            `json:"rootUID,omitempty"`
	ProjectUID  string            `json:"projectUID,omitempty"`
	Class       Class             `json:"class"`
	Status      Status            `json:"status"`
	MissingRoot bool              `json:"missingRoot,omitempty"`
	Runtime     *RuntimeRef       `json:"runtime,omitempty"`
}

// AgentNode is one Registry Agent with the overlay of its managed Pane.
//
// An Agent has no tmux object of its own, so Runtime is the ref of its current
// managed Pane and Status is that Pane's status. This is a derivation, not a
// lifecycle decision: nothing here transitions a phase, and Agent.Status.Phase
// is reported verbatim from the Registry.
type AgentNode struct {
	Agent       coremetadata.Agent `json:"agent"`
	WindowUID   string             `json:"windowUID,omitempty"`
	RootKind    coremetadata.Kind  `json:"rootKind,omitempty"`
	RootUID     string             `json:"rootUID,omitempty"`
	ProjectUID  string             `json:"projectUID,omitempty"`
	PaneUID     string             `json:"paneUID,omitempty"`
	Class       Class              `json:"class"`
	Status      Status             `json:"status"`
	MissingRoot bool               `json:"missingRoot,omitempty"`
	Runtime     *RuntimeRef        `json:"runtime,omitempty"`
}

// RuntimeNode is one observed tmux object with its attribution.
//
// Every observed object gets exactly one node, including the managed ones, so
// "classify the machine" and "overlay the Registry" are answerable from the same
// value without a second traversal.
type RuntimeNode struct {
	Ref RuntimeRef `json:"ref"`
	// Class is the attribution of this object.
	Class Class `json:"class"`
	// UID is the mirrored projmux uid, empty when the object carries none.
	UID string `json:"uid,omitempty"`
	// ResourceUID is the Registry resource this object is bound to. It is set
	// only for ClassManaged, so a consumer can never read a resource identity
	// off an object that was refused.
	ResourceUID string `json:"resourceUID,omitempty"`
	// ContainerID is the stable tmux id of the enclosing object.
	ContainerID    string `json:"containerID,omitempty"`
	AgentSessionID string `json:"agentSessionID,omitempty"`
	AgentThreadID  string `json:"agentThreadID,omitempty"`
	// Reason states why this class, in one clause.
	Reason string `json:"reason,omitempty"`
}

// Graph is the resolved read model: Registry rows with a runtime overlay, every
// observed runtime object with an attribution, and the reasons for both.
type Graph struct {
	Transport       Transport            `json:"transport"`
	HostMode        HostMode             `json:"hostMode"`
	Unavailable     []Unavailability     `json:"unavailable,omitempty"`
	Projects        []ProjectNode        `json:"projects,omitempty"`
	ControlSessions []ControlSessionNode `json:"controlSessions,omitempty"`
	Windows         []WindowNode         `json:"windows,omitempty"`
	Panes           []PaneNode           `json:"panes,omitempty"`
	Agents          []AgentNode          `json:"agents,omitempty"`
	Runtime         []RuntimeNode        `json:"runtime,omitempty"`
	Conflicts       []Conflict           `json:"conflicts,omitempty"`
}

// RuntimeOfClass returns the observed objects with class, in graph order.
func (g Graph) RuntimeOfClass(class Class) []RuntimeNode {
	var out []RuntimeNode
	for _, node := range g.Runtime {
		if node.Class == class {
			out = append(out, node)
		}
	}
	return out
}

// Resolve joins a Registry snapshot to one Inventory.
//
// The join direction is fixed: Registry rows are enumerated from the Registry
// and never from the machine, so an unreadable or empty observation produces the
// same rows with a downgraded status rather than fewer rows. Runtime objects are
// enumerated from the observation and never from the Registry, so an object
// projmux does not own still gets named and classified instead of disappearing.
//
// Resolve performs no I/O and mutates neither argument.
func Resolve(registry coremetadata.Registry, inventory Inventory) Graph {
	r := newResolver(registry, inventory)
	r.resolveClaims()
	r.buildRegistryNodes()
	r.buildRuntimeNodes()
	return r.graph()
}

type resolver struct {
	registry  coremetadata.Registry
	inventory Inventory

	sessionByID map[string]Session
	windowByID  map[string]Window

	kindByUID map[string]coremetadata.Kind

	// bound holds the single surviving claim per resource uid.
	bound map[string]RuntimeRef
	// runtimeClass holds the resolved class of each observed object, keyed by
	// its stable tmux id.
	runtimeClass map[string]Class
	// runtimeResource maps a bound runtime id back to its resource uid.
	runtimeResource map[string]string
	// runtimeReason explains one object's class.
	runtimeReason map[string]string
	// conflictedUID marks resource uids no runtime object may bind to.
	conflictedUID map[string]bool
	// managedSession and managedWindow are the enclosure sets an unattributed
	// object is recognized by.
	managedSession map[string]bool
	managedWindow  map[string]bool

	projects        []ProjectNode
	controlSessions []ControlSessionNode
	windows         []WindowNode
	panes           []PaneNode
	agents          []AgentNode
	runtime         []RuntimeNode
	conflicts       []Conflict
}

func newResolver(registry coremetadata.Registry, inventory Inventory) *resolver {
	r := &resolver{
		registry:        registry.Clone(),
		inventory:       inventory.Clone(),
		sessionByID:     map[string]Session{},
		windowByID:      map[string]Window{},
		kindByUID:       map[string]coremetadata.Kind{},
		bound:           map[string]RuntimeRef{},
		runtimeClass:    map[string]Class{},
		runtimeResource: map[string]string{},
		runtimeReason:   map[string]string{},
		conflictedUID:   map[string]bool{},
		managedSession:  map[string]bool{},
		managedWindow:   map[string]bool{},
	}
	for _, session := range r.inventory.Sessions {
		r.sessionByID[session.ID] = session
	}
	for _, window := range r.inventory.Windows {
		r.windowByID[window.ID] = window
	}
	for _, project := range r.registry.Projects {
		r.kindByUID[project.Metadata.UID] = coremetadata.KindProject
	}
	for _, control := range r.registry.ControlSessions {
		r.kindByUID[control.Metadata.UID] = coremetadata.KindControlSession
	}
	for _, window := range r.registry.Windows {
		r.kindByUID[window.Metadata.UID] = coremetadata.KindWindow
	}
	for _, pane := range r.registry.Panes {
		r.kindByUID[pane.Metadata.UID] = coremetadata.KindPane
	}
	for _, agent := range r.registry.Agents {
		r.kindByUID[agent.Metadata.UID] = coremetadata.KindAgent
	}
	return r
}
