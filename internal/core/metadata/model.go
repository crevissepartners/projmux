package metadata

import (
	"maps"
	"slices"
	"strings"
	"time"
)

// APIVersion is the resource API version string stamped on every resource.
const APIVersion = "projmux.io/v1alpha1"

// SchemaVersion is the current registry envelope version. A registry file
// carrying a higher value is rejected fail-closed; see schema.go.
const SchemaVersion = 1

// Kind is the closed set of Projmux resource kinds. A persistent tmux Session
// is intentionally absent: it is a 1:1 runtime projection of a Project stored
// in Project.status.session and owns no uid, name, or ownerRef of its own.
type Kind string

const (
	KindProject Kind = "Project"
	KindWindow  Kind = "Window"
	KindPane    Kind = "Pane"
	KindAgent   Kind = "Agent"
	// KindControlSession is the app-owned control session -- the Home session
	// `projmux shell` opens -- as an OWNER of Windows and Panes.
	//
	// It is a fifth root kind rather than a Project with a special role for one
	// reason that no flag can reproduce: a control session owns no filesystem
	// path, and the type below has no field to put one in. Every path-based
	// surface projmux has (managed roots, trust, rebind, cwd defaults,
	// ProjectByRoot) reads Project.Spec.Root, so a control session cannot leak
	// into any of them by omission, by a forgotten filter, or by a later
	// refactor -- the leak would not compile. $HOME is therefore permanently
	// incapable of becoming a Project or a managed root through this kind.
	KindControlSession Kind = "ControlSession"
)

// Kinds returns the closed kind set in declaration order.
func Kinds() []Kind {
	return []Kind{KindProject, KindWindow, KindPane, KindAgent, KindControlSession}
}

// OwnerRef points at the owning resource by opaque uid. It never carries a
// displayName, a tmux target, or any other non-identifying value.
type OwnerRef struct {
	Kind Kind   `json:"kind"`
	UID  string `json:"uid"`
}

// ObjectMeta is the metadata block shared by every resource.
//
//   - UID is opaque, immutable, and independent of tmux lifecycle.
//   - Name is the stable unique-within-scope query key. Project names are
//     unique across the registry; Window/Pane/Agent names are unique within
//     their ownerRef scope.
//   - DisplayName may duplicate and is never a selector, ownerRef, or identity.
//   - Labels are key/value classification input.
//   - Annotations are non-identifying metadata (AI topic, provider context).
type ObjectMeta struct {
	UID         string            `json:"uid"`
	Name        string            `json:"name"`
	DisplayName string            `json:"displayName,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	OwnerRef    *OwnerRef         `json:"ownerRef,omitempty"`
	CreatedAt   time.Time         `json:"createdAt"`
}

// Clone returns a deep copy of the metadata block.
func (m ObjectMeta) Clone() ObjectMeta {
	out := m
	out.Labels = cloneStringMap(m.Labels)
	out.Annotations = cloneStringMap(m.Annotations)
	if m.OwnerRef != nil {
		owner := *m.OwnerRef
		out.OwnerRef = &owner
	}
	return out
}

// OwnerUID returns the owning uid, or "" for a root resource.
func (m ObjectMeta) OwnerUID() string {
	if m.OwnerRef == nil {
		return ""
	}
	return m.OwnerRef.UID
}

// Condition records an observed resource condition with its first-observed
// timestamp preserved across repeat observations.
type Condition struct {
	Type             string    `json:"type"`
	Status           string    `json:"status"`
	Reason           string    `json:"reason,omitempty"`
	Message          string    `json:"message,omitempty"`
	FirstObservedAt  time.Time `json:"firstObservedAt"`
	LastTransitionAt time.Time `json:"lastTransitionAt"`
}

const (
	// ConditionMissingRoot marks a Project whose spec.root has disappeared.
	// The Project is never deleted or re-identified while it is set.
	ConditionMissingRoot = "MissingRoot"

	// ConditionMissingRuntime marks a Window or Pane whose uid is mirrored on
	// no live tmux object. It is the recorded *reason* a runtime object went
	// away, and it is deliberately not a deletion: the resource keeps its uid,
	// its name reservation, and its place in the owner tree, exactly the way
	// ConditionMissingRoot preserves a Project whose root vanished.
	//
	// It is never the source of a status read. Status is derived from a live
	// observation taken by the reading invocation (see selector.ObservedStatus);
	// this condition is the durable note the reconciler leaves behind so
	// `describe` can say why an object is offline long after the observation
	// that noticed it has been discarded.
	ConditionMissingRuntime = "MissingRuntime"

	ConditionTrue  = "True"
	ConditionFalse = "False"
)

// ReasonRuntimeUnbound is the ConditionMissingRuntime reason. It records what
// was observed -- no live tmux object mirrors this uid -- and nothing about
// why, because nothing about why is observable: a window or pane that is gone
// leaves no exit status behind for a later inventory read to recover.
const ReasonRuntimeUnbound = "RuntimeUnbound"

// RuntimeObservation is one live-tmux inventory of mirrored Projmux uids.
//
// It is the machine half of a registry-versus-machine diff: a uid the registry
// holds but this observation does not is an object whose tmux window or pane is
// gone. It is a value taken once per process invocation and thrown away, never
// a cache and never persisted, which is what makes closing a pane visible to
// the very next query without any hook firing.
//
// The empty observation means "nothing is bound", which is the fail-closed
// reading: it can only ever downgrade a resource to offline, never invent a
// live one.
type RuntimeObservation struct {
	// Windows is the set of Window uids a live tmux window still mirrors.
	Windows map[string]bool
	// Panes is the set of Pane uids a live tmux pane still mirrors.
	Panes map[string]bool
}

// BoundWindow reports whether a live tmux window still mirrors uid.
//
// There is deliberately no generic Bound(kind, uid) accessor. Only Window and
// Pane have a tmux object of their own: a Project's runtime is a tmux session
// with no @projmux uid, and an Agent has no tmux object at all. A kind-dispatch
// accessor would have to answer something for those two, and every available
// answer is a lie waiting to be trusted.
func (o RuntimeObservation) BoundWindow(uid string) bool { return o.Windows[uid] }

// BoundPane reports whether a live tmux pane still mirrors uid.
func (o RuntimeObservation) BoundPane(uid string) bool { return o.Panes[uid] }

// Clone returns a deep copy so a resolver can never observe its snapshot
// changing under it.
func (o RuntimeObservation) Clone() RuntimeObservation {
	return RuntimeObservation{Windows: cloneBoolSet(o.Windows), Panes: cloneBoolSet(o.Panes)}
}

func cloneBoolSet(in map[string]bool) map[string]bool {
	if in == nil {
		return nil
	}
	out := make(map[string]bool, len(in))
	maps.Copy(out, in)
	return out
}

// SessionProjection is the 1:1 runtime projection of a persistent tmux
// session onto its Project. Auto-attach ephemeral sessions are never recorded
// here; they live only in runtime inventory, outside the Project hierarchy.
type SessionProjection struct {
	Name string `json:"name"`
	Live bool   `json:"live"`
}

// Project is the canonical Projmux root resource.
type Project struct {
	APIVersion string        `json:"apiVersion"`
	Kind       Kind          `json:"kind"`
	Metadata   ObjectMeta    `json:"metadata"`
	Spec       ProjectSpec   `json:"spec"`
	Status     ProjectStatus `json:"status"`
}

// ProjectSpec holds the absolute filesystem root the Project is bound to.
type ProjectSpec struct {
	Root string `json:"root"`
}

// ProjectStatus carries the runtime session projection and observed conditions.
type ProjectStatus struct {
	Session    *SessionProjection `json:"session,omitempty"`
	Conditions []Condition        `json:"conditions,omitempty"`
}

// Clone returns a deep copy of the Project.
func (p Project) Clone() Project {
	out := p
	out.Metadata = p.Metadata.Clone()
	if p.Status.Session != nil {
		session := *p.Status.Session
		out.Status.Session = &session
	}
	out.Status.Conditions = slices.Clone(p.Status.Conditions)
	return out
}

// ControlSession is the app-owned control session as a Registry root.
//
// It is the second thing a Window may be owned by, and the only root kind that
// carries no filesystem path. The tmux session it is bound to is named in
// spec.session and is matched exactly, never by basename, cwd, or a name
// heuristic: an operator's session that happens to be called `home` on a server
// projmux does not own is not this resource, and the marker read in
// internal/core/resourcegraph is what decides that at observation time.
//
// It deliberately has no status.session projection either. A Project's session
// projection exists so an offline Project remembers which session name it will
// come back as; a control session's whole identity *is* that name, so a second
// copy of it in status would be a field that can disagree with spec.
type ControlSession struct {
	APIVersion string               `json:"apiVersion"`
	Kind       Kind                 `json:"kind"`
	Metadata   ObjectMeta           `json:"metadata"`
	Spec       ControlSessionSpec   `json:"spec"`
	Status     ControlSessionStatus `json:"status,omitzero"`
}

// ControlSessionSpec names the tmux session this control session is bound to.
//
// There is no Root field and there must never be one. See KindControlSession.
type ControlSessionSpec struct {
	Session string `json:"session"`
}

// ControlSessionStatus carries the observed conditions of one control session.
//
// It is `omitzero` for the same read-compatibility reason WindowStatus is: a
// control session that has never carried a condition serializes without the
// key, so the block stays additive inside schemaVersion 1.
type ControlSessionStatus struct {
	Conditions []Condition `json:"conditions,omitempty"`
}

// Clone returns a deep copy of the ControlSession.
func (c ControlSession) Clone() ControlSession {
	out := c
	out.Metadata = c.Metadata.Clone()
	out.Status.Conditions = slices.Clone(c.Status.Conditions)
	return out
}

// Window is owned by exactly one Project or ControlSession and always owns an
// initial Pane. The two owner kinds are structurally identical from a Window's
// point of view -- ownerRef is an opaque uid plus a kind -- so nothing below a
// Window needs to know which root it hangs from.
type Window struct {
	APIVersion string       `json:"apiVersion"`
	Kind       Kind         `json:"kind"`
	Metadata   ObjectMeta   `json:"metadata"`
	Spec       WindowSpec   `json:"spec"`
	Status     WindowStatus `json:"status,omitzero"`
}

// DisplayName returns the duplicate-allowed user-facing Window name. Registry
// files written before Window display names were projected leave the field
// empty, so those Windows safely fall back to their stable metadata.name until
// a runtime observation supplies the tmux window_name.
func (w Window) DisplayName() string {
	if strings.TrimSpace(w.Metadata.DisplayName) != "" {
		return w.Metadata.DisplayName
	}
	return w.Metadata.Name
}

// WindowSpec records the uid of the Pane created together with the Window.
type WindowSpec struct {
	PrimaryPaneRef string `json:"primaryPaneRef"`
}

// WindowStatus carries the observed conditions of one Window.
//
// There is deliberately no stored liveness field here, and there never will be
// one: a stored bool is exactly the defect this block replaces. Liveness is
// derived from a live observation at read time; what is stored is only the
// preserved *reason* an object stopped being bound.
//
// The field is `omitzero` rather than `omitempty` so a Window that has never
// carried a condition serializes byte-identically to a registry written before
// this field existed. That keeps the addition inside schemaVersion 1: an older
// build reading a newer file simply ignores a key it does not know, and a
// newer build reading an older file decodes the absent key to the zero value.
type WindowStatus struct {
	Conditions []Condition `json:"conditions,omitempty"`
}

// Clone returns a deep copy of the Window.
func (w Window) Clone() Window {
	out := w
	out.Metadata = w.Metadata.Clone()
	out.Status.Conditions = slices.Clone(w.Status.Conditions)
	return out
}

// PaneRole distinguishes a plain shell Pane from a Pane managed by an Agent.
type PaneRole string

const (
	PaneRoleShell PaneRole = "shell"
	PaneRoleAgent PaneRole = "agent"
)

// Pane is owned by a Window (shell role) or by an Agent (agent role).
type Pane struct {
	APIVersion string     `json:"apiVersion"`
	Kind       Kind       `json:"kind"`
	Metadata   ObjectMeta `json:"metadata"`
	Spec       PaneSpec   `json:"spec"`
	Status     PaneStatus `json:"status"`
}

// PaneSpec records the declared pane recipe inputs. Command is the one-time
// name-derivation source; it is never re-read to rename an existing Pane.
type PaneSpec struct {
	Role    PaneRole `json:"role"`
	CWD     string   `json:"cwd,omitempty"`
	Command string   `json:"command,omitempty"`
}

// PaneStatus carries the derived secondary display title and the observed
// conditions of one Pane. DisplayTitle is never a selector, an identity, or a
// Window name source.
//
// As with WindowStatus there is no stored liveness field: liveness is derived
// from a live observation at read time, and Conditions only preserves the
// reason a runtime object went away.
type PaneStatus struct {
	DisplayTitle string      `json:"displayTitle,omitempty"`
	Conditions   []Condition `json:"conditions,omitempty"`
	// Activation names the current materialization of this Pane. It is the
	// value a launched supervisor quotes back, and the only thing that
	// separates the running process from the one a resume replaced.
	Activation PaneActivation `json:"activation,omitzero"`
	// LastTermination is the durable receipt of why the *current* generation's
	// managed process stopped. Issuing a new generation clears it.
	LastTermination *TerminationEvidence `json:"lastTermination,omitempty"`
}

// Clone returns a deep copy of the Pane.
func (p Pane) Clone() Pane {
	out := p
	out.Metadata = p.Metadata.Clone()
	out.Status.Conditions = slices.Clone(p.Status.Conditions)
	out.Status.LastTermination = p.Status.LastTermination.Clone()
	return out
}

// AgentPhase is the closed Agent lifecycle state set.
type AgentPhase string

const (
	// PhasePending is an Agent whose managed Pane has not started yet.
	PhasePending AgentPhase = "Pending"
	// PhaseRunning is an Agent with a live managed Pane.
	PhaseRunning AgentPhase = "Running"
	// PhaseOffline follows a normal managed-Pane exit or an explicit pane
	// deletion. The Agent survives as a resumable resource.
	PhaseOffline AgentPhase = "Offline"
	// PhaseFailed follows a launch failure or an abnormal exit.
	PhaseFailed AgentPhase = "Failed"
)

// AgentPhases returns the closed phase set in lifecycle order.
func AgentPhases() []AgentPhase {
	return []AgentPhase{PhasePending, PhaseRunning, PhaseOffline, PhaseFailed}
}

// Agent is owned by a Window and owns its current managed Pane.
type Agent struct {
	APIVersion string      `json:"apiVersion"`
	Kind       Kind        `json:"kind"`
	Metadata   ObjectMeta  `json:"metadata"`
	Spec       AgentSpec   `json:"spec"`
	Status     AgentStatus `json:"status"`
}

// AgentSpec records the normalized provider id the Agent was created for.
type AgentSpec struct {
	Provider  string         `json:"provider,omitempty"`
	Workspace AgentWorkspace `json:"workspace,omitzero"`
}

// AgentWorkspace is the provider-neutral effective filesystem contract of one
// Agent launch. Project ownership remains on the owning Window; these paths
// only describe where the provider process is allowed to work.
type AgentWorkspace struct {
	CWD                     string   `json:"cwd"`
	AdditionalWritableRoots []string `json:"additionalWritableRoots,omitempty"`
}

// IsZero lets old registry documents omit the additive workspace block.
func (w AgentWorkspace) IsZero() bool {
	return w.CWD == "" && len(w.AdditionalWritableRoots) == 0
}

// AgentInteractionKind is the provider-neutral closed interaction vocabulary.
// It is intentionally separate from AgentPhase: interaction describes what a
// live provider is waiting on, while phase describes whether the Agent has a
// live managed Pane at all.
type AgentInteractionKind string

// AgentInteractionSource is the closed provenance vocabulary for durable
// semantic observations. It is intentionally not caller supplied: arbitrary
// strings here would turn status metadata into a prompt/credential sink.
type AgentInteractionSource string

const (
	InteractionUnknown          AgentInteractionKind = "unknown"
	InteractionIdle             AgentInteractionKind = "idle"
	InteractionInProgress       AgentInteractionKind = "in_progress"
	InteractionApprovalRequired AgentInteractionKind = "approval_required"
	InteractionInputRequired    AgentInteractionKind = "input_required"
	InteractionResponseComplete AgentInteractionKind = "response_complete"
)

const (
	InteractionSourceManual          AgentInteractionSource = "manual"
	InteractionSourceCompatibilityAI AgentInteractionSource = "compatibility-ai"
	InteractionSourceProviderHook    AgentInteractionSource = "provider-hook"
	InteractionSourceLifecycle       AgentInteractionSource = "lifecycle"
)

func AgentInteractionSources() []AgentInteractionSource {
	return []AgentInteractionSource{
		InteractionSourceManual,
		InteractionSourceCompatibilityAI,
		InteractionSourceProviderHook,
		InteractionSourceLifecycle,
	}
}

// AgentInteractionKinds returns the closed semantic set in neutral order.
func AgentInteractionKinds() []AgentInteractionKind {
	return []AgentInteractionKind{
		InteractionUnknown,
		InteractionIdle,
		InteractionInProgress,
		InteractionApprovalRequired,
		InteractionInputRequired,
		InteractionResponseComplete,
	}
}

// AgentInteraction is the last durable semantic observation. Readers use
// Agent.EffectiveInteraction to invalidate it for an Offline/Failed Agent or
// after the bounded freshness window; history may remain durable without being
// presented as current state.
type AgentInteraction struct {
	Kind       AgentInteractionKind `json:"kind"`
	ObservedAt time.Time            `json:"observedAt,omitzero"`
	Source     string               `json:"source,omitempty"`
}

func (i AgentInteraction) IsZero() bool {
	return (i.Kind == "" || i.Kind == InteractionUnknown) && i.ObservedAt.IsZero() && i.Source == ""
}

// AgentActivationState separates Pane creation from initial-task activation.
type AgentActivationState string

const (
	ActivationNotRequested   AgentActivationState = "not_requested"
	ActivationPending        AgentActivationState = "pending"
	ActivationAcknowledged   AgentActivationState = "acknowledged"
	ActivationUnconfirmed    AgentActivationState = "unconfirmed"
	ActivationReasonTimedOut                      = "provider activation acknowledgement timed out"
	ActivationReasonFailed                        = "provider activation acknowledgement failed"
)

func ValidAgentActivationReason(reason string) bool {
	switch strings.TrimSpace(reason) {
	case "", ActivationReasonTimedOut, ActivationReasonFailed:
		return true
	default:
		return false
	}
}

// AgentActivation is bounded launch metadata. It never contains prompt text,
// provider credentials, or pane content.
type AgentActivation struct {
	State      AgentActivationState `json:"state"`
	ObservedAt time.Time            `json:"observedAt,omitzero"`
	Source     string               `json:"source,omitempty"`
	Reason     string               `json:"reason,omitempty"`
}

func (a AgentActivation) IsZero() bool {
	return (a.State == "" || a.State == ActivationNotRequested) && a.ObservedAt.IsZero() && a.Source == "" && a.Reason == ""
}

// AgentStatus tracks the lifecycle phase, the current managed Pane uid, and the
// durable pointer to the provider conversation the Agent belongs to.
//
// PaneRef and SessionRef have deliberately different lifetimes. PaneRef is the
// *current* binding and is cleared by ReleaseAgentPane, DeletePane, and every
// non-Running transition. SessionRef is the durable conversation pointer and is
// never cleared by any of them: an Offline Agent that has lost its Pane still
// knows which conversation it is.
//
// SessionRef is an optional pointer with omitempty. That is the whole
// read-compatibility story for registry files written before it existed: an
// absent key decodes to nil, a nil ref re-encodes to an absent key, and the
// document round-trips byte-identically. It is additive inside schemaVersion 1
// and needs no migration step — bumping the envelope would make every already
// installed build reject the file fail-closed with ErrSchemaTooNew, which is a
// hard downgrade break bought for nothing.
//
// One conversation may be pointed at by more than one Agent. That is NOT
// prevented, and the registry treats it as legal state; see the note on
// NewAgentSessionRef and the Agent section of docs/agent-workflow.md for the
// reasoning.
type AgentStatus struct {
	Phase       AgentPhase       `json:"phase"`
	PaneRef     string           `json:"paneRef,omitempty"`
	SessionRef  *AgentSessionRef `json:"sessionRef,omitempty"`
	Interaction AgentInteraction `json:"interaction,omitzero"`
	Activation  AgentActivation  `json:"activation,omitzero"`
	Reason      string           `json:"reason,omitempty"`
	// LastTermination mirrors the receipt recorded against the Agent's current
	// managed Pane, so the evidence survives the Pane resource a canonical
	// delete removes.
	LastTermination  *TerminationEvidence `json:"lastTermination,omitempty"`
	LastTransitionAt time.Time            `json:"lastTransitionAt"`
}

// Clone returns a deep copy of the Agent.
func (a Agent) Clone() Agent {
	out := a
	out.Metadata = a.Metadata.Clone()
	out.Spec.Workspace.AdditionalWritableRoots = slices.Clone(a.Spec.Workspace.AdditionalWritableRoots)
	out.Status.SessionRef = a.Status.SessionRef.Clone()
	out.Status.LastTermination = a.Status.LastTermination.Clone()
	return out
}

// NameReservation is the persisted record of one allocated name. Reservations
// are the authority for suffix allocation: the allocator never recomputes a
// suffix from resource scan order.
//
// Scope is "" for the registry-wide Project scope and the owner uid for
// Window, Pane, and Agent names.
type NameReservation struct {
	Scope string `json:"scope,omitempty"`
	Kind  Kind   `json:"kind"`
	Name  string `json:"name"`
	UID   string `json:"uid"`
}

// Registry is the whole persisted resource set plus its name reservations.
// Slice order is insertion order and is preserved verbatim so serialization is
// deterministic without a sort pass.
type Registry struct {
	APIVersion    string    `json:"apiVersion"`
	SchemaVersion int       `json:"schemaVersion"`
	UpdatedAt     time.Time `json:"updatedAt"`
	Projects      []Project `json:"projects,omitempty"`
	// ControlSessions is `omitempty`, which is the whole no-migration story for
	// this slice: a registry written before control sessions existed decodes the
	// absent key to a nil slice and re-encodes it as an absent key, so the
	// document round-trips byte-identically and schemaVersion stays 1. Bumping
	// the envelope would make every already installed build reject the file
	// fail-closed with ErrSchemaTooNew -- a hard downgrade break bought for
	// nothing.
	ControlSessions  []ControlSession  `json:"controlSessions,omitempty"`
	Windows          []Window          `json:"windows,omitempty"`
	Panes            []Pane            `json:"panes,omitempty"`
	Agents           []Agent           `json:"agents,omitempty"`
	NameReservations []NameReservation `json:"nameReservations,omitempty"`
}

// NewRegistry returns an empty registry stamped with the current envelope.
func NewRegistry() Registry {
	return Registry{APIVersion: APIVersion, SchemaVersion: SchemaVersion}
}

// Clone returns a deep copy of the registry so a failed mutation can never
// leak a partially applied change back to the caller.
func (r Registry) Clone() Registry {
	out := r
	out.Projects = cloneSlice(r.Projects, Project.Clone)
	out.ControlSessions = cloneSlice(r.ControlSessions, ControlSession.Clone)
	out.Windows = cloneSlice(r.Windows, Window.Clone)
	out.Panes = cloneSlice(r.Panes, Pane.Clone)
	out.Agents = cloneSlice(r.Agents, Agent.Clone)
	out.NameReservations = slices.Clone(r.NameReservations)
	return out
}

func cloneSlice[T any](in []T, clone func(T) T) []T {
	if in == nil {
		return nil
	}
	out := make([]T, len(in))
	for i, item := range in {
		out[i] = clone(item)
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	maps.Copy(out, in)
	return out
}

// Project returns the Project with uid.
func (r *Registry) Project(uid string) (*Project, bool) {
	for i := range r.Projects {
		if r.Projects[i].Metadata.UID == uid {
			return &r.Projects[i], true
		}
	}
	return nil, false
}

// ControlSession returns the ControlSession with uid.
func (r *Registry) ControlSession(uid string) (*ControlSession, bool) {
	for i := range r.ControlSessions {
		if r.ControlSessions[i].Metadata.UID == uid {
			return &r.ControlSessions[i], true
		}
	}
	return nil, false
}

// ControlSessionBySession returns the ControlSession bound to an exact tmux
// session name.
//
// Matching is exact and never heuristic, for the same reason ProjectByRoot's
// is: the session name is the identity, so a trimmed, cased, or prefixed
// variant is a different session and must not merge onto this uid.
func (r *Registry) ControlSessionBySession(session string) (*ControlSession, bool) {
	session = strings.TrimSpace(session)
	if session == "" {
		return nil, false
	}
	for i := range r.ControlSessions {
		if r.ControlSessions[i].Spec.Session == session {
			return &r.ControlSessions[i], true
		}
	}
	return nil, false
}

// Window returns the Window with uid.
func (r *Registry) Window(uid string) (*Window, bool) {
	for i := range r.Windows {
		if r.Windows[i].Metadata.UID == uid {
			return &r.Windows[i], true
		}
	}
	return nil, false
}

// Pane returns the Pane with uid.
func (r *Registry) Pane(uid string) (*Pane, bool) {
	for i := range r.Panes {
		if r.Panes[i].Metadata.UID == uid {
			return &r.Panes[i], true
		}
	}
	return nil, false
}

// Agent returns the Agent with uid.
func (r *Registry) Agent(uid string) (*Agent, bool) {
	for i := range r.Agents {
		if r.Agents[i].Metadata.UID == uid {
			return &r.Agents[i], true
		}
	}
	return nil, false
}

// ProjectByRoot returns the Project bound to an exact cleaned root path. Root
// matching is exact and never heuristic: basename, git origin, inode, and scan
// order are deliberately not consulted.
func (r *Registry) ProjectByRoot(root string) (*Project, bool) {
	root = cleanRoot(root)
	if root == "" {
		return nil, false
	}
	for i := range r.Projects {
		if r.Projects[i].Spec.Root == root {
			return &r.Projects[i], true
		}
	}
	return nil, false
}

// ProjectByName returns the Project with the registry-unique name.
func (r *Registry) ProjectByName(name string) (*Project, bool) {
	for i := range r.Projects {
		if r.Projects[i].Metadata.Name == name {
			return &r.Projects[i], true
		}
	}
	return nil, false
}

// WindowsOf returns the Windows owned by projectUID in insertion order.
func (r *Registry) WindowsOf(projectUID string) []Window {
	var out []Window
	for _, window := range r.Windows {
		if window.Metadata.OwnerUID() == projectUID {
			out = append(out, window)
		}
	}
	return out
}

// PanesOf returns the Panes owned by ownerUID in insertion order. The owner is
// a Window for shell panes and an Agent for managed panes.
func (r *Registry) PanesOf(ownerUID string) []Pane {
	var out []Pane
	for _, pane := range r.Panes {
		if pane.Metadata.OwnerUID() == ownerUID {
			out = append(out, pane)
		}
	}
	return out
}

// AgentsOf returns the Agents owned by windowUID in insertion order.
func (r *Registry) AgentsOf(windowUID string) []Agent {
	var out []Agent
	for _, agent := range r.Agents {
		if agent.Metadata.OwnerUID() == windowUID {
			out = append(out, agent)
		}
	}
	return out
}
