package core

// ResourceGraph is one coherent observation: a single Registry read joined to
// one tmux inventory and one process observation of that Registry.
//
// RegistryIdentity is the stat identity of the Registry file that read came
// from. It is nil when the file could not be stat'ed or changed between the
// stats taken immediately before and after the read; Graph is still complete
// in that case.
type ResourceGraph struct {
	Graph            ResourceGraphView     `json:"graph"`
	RegistryIdentity *RegistryFileIdentity `json:"registryIdentity,omitempty"`
}

// RegistryFileIdentity is the stat identity of the Registry file. Writers only
// replace the file atomically, so any rewrite changes at least one field.
// Compare whole values; no single field is an identity on its own.
type RegistryFileIdentity struct {
	Device                uint64
	Inode                 uint64
	Size                  int64
	ModTimeNanoseconds    int64
	ChangeTimeSeconds     int64
	ChangeTimeNanoseconds int64
}

// ResourceGraphView is the resolved read model: Registry rows with a runtime
// overlay, every observed runtime object with an attribution, and the reasons
// for both. Its JSON is the CLI's internal graph JSON byte for byte.
//
// Registry rows are enumerated from the Registry, so an unreadable tmux
// observation yields the same rows, those tmux would judge with Status
// "unknown" and a reason in Unavailable; an unreadable process host leaves its
// process Pane "unknown" without an Unavailable entry.
// Runtime nodes are enumerated from the observation.
type ResourceGraphView struct {
	Transport       RuntimeTransport          `json:"transport"`
	HostMode        string                    `json:"hostMode"`
	Unavailable     []RuntimeUnavailability   `json:"unavailable,omitempty"`
	Projects        []GraphProjectNode        `json:"projects,omitempty"`
	ControlSessions []GraphControlSessionNode `json:"controlSessions,omitempty"`
	Windows         []GraphWindowNode         `json:"windows,omitempty"`
	Panes           []GraphPaneNode           `json:"panes,omitempty"`
	Agents          []GraphAgentNode          `json:"agents,omitempty"`
	Runtime         []GraphRuntimeNode        `json:"runtime,omitempty"`
	Conflicts       []GraphConflict           `json:"conflicts,omitempty"`
}

// GraphRuntimeRef is the exact transport handle of one observed object. It is
// routing, never identity: store the uid and re-resolve the ref.
type GraphRuntimeRef struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Target string `json:"target,omitempty"`
	Name   string `json:"name,omitempty"`
}

// GraphConflict is one contradiction exact evidence produced. Targets are the
// tmux handles involved.
type GraphConflict struct {
	Kind    string   `json:"kind"`
	UID     string   `json:"uid"`
	Reason  string   `json:"reason"`
	Detail  string   `json:"detail"`
	Targets []string `json:"targets,omitempty"`
}

// GraphProjectNode is one Registry Project with its runtime overlay.
type GraphProjectNode struct {
	Project     Project          `json:"project"`
	Class       string           `json:"class"`
	Status      string           `json:"status"`
	MissingRoot bool             `json:"missingRoot,omitempty"`
	Runtime     *GraphRuntimeRef `json:"runtime,omitempty"`
}

// GraphControlSessionNode is one Registry ControlSession with its runtime
// overlay.
type GraphControlSessionNode struct {
	ControlSession ControlSession   `json:"controlSession"`
	Class          string           `json:"class"`
	Status         string           `json:"status"`
	Runtime        *GraphRuntimeRef `json:"runtime,omitempty"`
}

// GraphWindowNode is one Registry Window with its runtime overlay and resolved
// owner. Active is the exact window_active bit of the one bound tmux Window.
type GraphWindowNode struct {
	Window      Window           `json:"window"`
	RootKind    Kind             `json:"rootKind,omitempty"`
	RootUID     string           `json:"rootUID,omitempty"`
	ProjectUID  string           `json:"projectUID,omitempty"`
	Class       string           `json:"class"`
	Status      string           `json:"status"`
	Live        bool             `json:"live,omitempty"`
	Active      bool             `json:"active,omitempty"`
	MissingRoot bool             `json:"missingRoot,omitempty"`
	Runtime     *GraphRuntimeRef `json:"runtime,omitempty"`
}

// GraphProcessKey identifies the owned provider child a process Pane resolved
// to in this observation. It is observation evidence only, never process
// admission, binding or control authority. A key without a host still names
// the Pane and generation the graph judged; Valid reports a complete key.
type GraphProcessKey struct {
	Host       string
	Pane       string
	Generation string
}

// Valid reports whether Host, Pane and Generation are all set.
func (k GraphProcessKey) Valid() bool {
	return k.Host != "" && k.Pane != "" && k.Generation != ""
}

// GraphPaneNode is one Registry Pane with its runtime overlay and resolved
// owner chain. WindowUID is the effective containing Window. Process is set
// for a Pane judged as a process child and is excluded from JSON, so the graph
// JSON stays the CLI's.
type GraphPaneNode struct {
	Process     *GraphProcessKey `json:"-"`
	Pane        Pane             `json:"pane"`
	AgentUID    string           `json:"agentUID,omitempty"`
	WindowUID   string           `json:"windowUID,omitempty"`
	RootKind    Kind             `json:"rootKind,omitempty"`
	RootUID     string           `json:"rootUID,omitempty"`
	ProjectUID  string           `json:"projectUID,omitempty"`
	Class       string           `json:"class"`
	Status      string           `json:"status"`
	MissingRoot bool             `json:"missingRoot,omitempty"`
	Runtime     *GraphRuntimeRef `json:"runtime,omitempty"`
}

// GraphAgentNode is one Registry Agent with the overlay of its managed Pane.
// Agent.Status.Phase is reported verbatim from the Registry. Process is the
// managed Pane's process key, excluded from JSON like GraphPaneNode.Process.
type GraphAgentNode struct {
	Process     *GraphProcessKey `json:"-"`
	Agent       Agent            `json:"agent"`
	WindowUID   string           `json:"windowUID,omitempty"`
	RootKind    Kind             `json:"rootKind,omitempty"`
	RootUID     string           `json:"rootUID,omitempty"`
	ProjectUID  string           `json:"projectUID,omitempty"`
	PaneUID     string           `json:"paneUID,omitempty"`
	Class       string           `json:"class"`
	Status      string           `json:"status"`
	MissingRoot bool             `json:"missingRoot,omitempty"`
	Runtime     *GraphRuntimeRef `json:"runtime,omitempty"`
}

// GraphRuntimeNode is one observed tmux object with its attribution.
// ResourceUID is set only for the "managed" class.
type GraphRuntimeNode struct {
	Ref                   GraphRuntimeRef `json:"ref"`
	Class                 string          `json:"class"`
	UID                   string          `json:"uid,omitempty"`
	ResourceUID           string          `json:"resourceUID,omitempty"`
	ContainerID           string          `json:"containerID,omitempty"`
	AgentSessionID        string          `json:"agentSessionID,omitempty"`
	AgentThreadID         string          `json:"agentThreadID,omitempty"`
	AgentLaunchAuthorship string          `json:"agentLaunchAuthorship,omitempty"`
	Reason                string          `json:"reason,omitempty"`
}

// ControlSession is an app-owned control root bound to one exact tmux session.
// It is not a Project and has no filesystem root.
type ControlSession struct {
	APIVersion string               `json:"apiVersion"`
	Kind       Kind                 `json:"kind"`
	Metadata   ObjectMeta           `json:"metadata"`
	Spec       ControlSessionSpec   `json:"spec"`
	Status     ControlSessionStatus `json:"status,omitzero"`
}

// ControlSessionSpec names the bound tmux session.
type ControlSessionSpec struct {
	Session string `json:"session"`
}

// ControlSessionStatus carries the observed conditions of one control session.
type ControlSessionStatus struct {
	Conditions []Condition `json:"conditions,omitempty"`
}
