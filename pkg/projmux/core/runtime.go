package core

// RuntimeObjectKind selects a tmux object inventory, independently of Registry kinds.
type RuntimeObjectKind string

const (
	RuntimeSessions RuntimeObjectKind = "sessions"
	RuntimeWindows  RuntimeObjectKind = "windows"
	RuntimePanes    RuntimeObjectKind = "panes"
)

// RuntimeResource is the Registry identity bound to a managed runtime row.
type RuntimeResource struct {
	Kind string `json:"kind"`
	UID  string `json:"uid"`
	Name string `json:"name,omitempty"`
}

// RuntimeConflict is one contradiction naming a runtime object as claimant.
type RuntimeConflict struct {
	UID     string   `json:"uid"`
	Reason  string   `json:"reason"`
	Detail  string   `json:"detail,omitempty"`
	Targets []string `json:"targets,omitempty"`
}

// RuntimeRow describes an observed object and its attribution. ID is the stable
// tmux handle; Target is its fully qualified coordinate. Neither is a selector.
type RuntimeRow struct {
	Kind        string            `json:"kind"`
	ID          string            `json:"id"`
	Target      string            `json:"target,omitempty"`
	Name        string            `json:"name,omitempty"`
	Class       string            `json:"class"`
	UID         string            `json:"uid,omitempty"`
	ContainerID string            `json:"containerID,omitempty"`
	SessionID   string            `json:"sessionID,omitempty"`
	Resource    *RuntimeResource  `json:"resource,omitempty"`
	Reason      string            `json:"reason,omitempty"`
	Conflicts   []RuntimeConflict `json:"conflicts,omitempty"`
}

// RuntimeTransport identifies the exact server and routing provenance.
type RuntimeTransport struct {
	Kind   string `json:"kind"`
	Value  string `json:"value,omitempty"`
	Source string `json:"source"`
}

// RuntimeUnavailability states why an observation scope could not be read.
type RuntimeUnavailability struct {
	Scope  string `json:"scope"`
	Reason string `json:"reason"`
}

// RuntimeReport matches get runtime JSON. Items is always a non-nil slice.
// A missing or unreachable server is an unavailable report, not an error.
type RuntimeReport struct {
	APIVersion  string                  `json:"apiVersion"`
	Kind        string                  `json:"kind"`
	Transport   RuntimeTransport        `json:"transport"`
	HostMode    string                  `json:"hostMode"`
	Unavailable []RuntimeUnavailability `json:"unavailable,omitempty"`
	Items       []RuntimeRow            `json:"items"`
}
