package core

// GraphRuntimeInventory is one tmux observation a caller already holds: the
// exact transport and host mode it was read from, every observed session,
// window and pane, and the scopes that could not be read. It is a value, not
// a route, so the same inventory always resolves the same way. Unavailable
// distinguishes "nothing is running" from "could not look".
type GraphRuntimeInventory struct {
	Transport   RuntimeTransport        `json:"transport"`
	HostMode    string                  `json:"hostMode"`
	Sessions    []GraphRuntimeSession   `json:"sessions,omitempty"`
	Windows     []GraphRuntimeWindow    `json:"windows,omitempty"`
	Panes       []GraphRuntimePane      `json:"panes,omitempty"`
	Unavailable []RuntimeUnavailability `json:"unavailable,omitempty"`
}

// GraphRuntimeSession is one observed tmux session. ID is the stable `$N`
// handle; ProjectUID, ProjectName, Root, Role and Ephemeral are the projmux
// session options as observed. None of them is identity on its own.
type GraphRuntimeSession struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	ProjectUID  string `json:"projectUID,omitempty"`
	ProjectName string `json:"projectName,omitempty"`
	Root        string `json:"root,omitempty"`
	Role        string `json:"role,omitempty"`
	Ephemeral   bool   `json:"ephemeral,omitempty"`
}

// GraphRuntimeWindow is one observed tmux window. ID is the stable `@N`
// handle, SessionID its containing session, and Active the exact
// window_active fact of this observation.
type GraphRuntimeWindow struct {
	ID           string `json:"id"`
	SessionID    string `json:"sessionID,omitempty"`
	Index        string `json:"index,omitempty"`
	DisplayName  string `json:"displayName,omitempty"`
	UID          string `json:"uid,omitempty"`
	MirroredName string `json:"mirroredName,omitempty"`
	Active       bool   `json:"active,omitempty"`
}

// GraphRuntimePane is one observed tmux pane. ID is the stable `%N` handle and
// WindowID its containing window. The Agent fields are observed routing
// evidence, never resource identity.
type GraphRuntimePane struct {
	ID                    string `json:"id"`
	WindowID              string `json:"windowID,omitempty"`
	UID                   string `json:"uid,omitempty"`
	MirroredName          string `json:"mirroredName,omitempty"`
	AgentProvider         string `json:"agentProvider,omitempty"`
	AgentLaunchAuthorship string `json:"agentLaunchAuthorship,omitempty"`
	AgentSessionID        string `json:"agentSessionID,omitempty"`
	AgentThreadID         string `json:"agentThreadID,omitempty"`
	Title                 string `json:"title,omitempty"`
}

// ResourceContextSource names the input that supplied a ResourceContext.
type ResourceContextSource string

const (
	ContextSourceProjectRoot    ResourceContextSource = "project-root-basename"
	ContextSourceControlSession ResourceContextSource = "control-session"
	ContextSourceLiveWindowName ResourceContextSource = "live-window-name"
	ContextSourceAgentTopic     ResourceContextSource = "agent-topic"
	ContextSourceAgentProvider  ResourceContextSource = "agent-provider"
	ContextSourceCommand        ResourceContextSource = "command-executable"
	ContextSourceWindowFallback ResourceContextSource = "window-fallback"
	ContextSourceLivePaneTitle  ResourceContextSource = "live-pane-title"
)

// ResourceContext is presentation derived for one resource from one graph. It
// is never an address or selector. Observed is true only when Value came from
// the exact live runtime object bound to the resource. An unknown resource has
// the zero value; it never falls back to the resource name.
type ResourceContext struct {
	Value    string                `json:"value,omitempty"`
	Source   ResourceContextSource `json:"source,omitempty"`
	Observed bool                  `json:"observed"`
}
