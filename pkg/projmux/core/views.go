package core

// Ref is a CLI resource reference, such as "uid:agent-..." or "agent/name".
type Ref struct {
	Kind  Kind
	Value string
}

// Resource is one of the four resource kinds returned by a singular read.
// The concrete value is Project, Window, Pane, or Agent.
type Resource interface{ ResourceKind() Kind }

func (p Project) ResourceKind() Kind { return p.Kind }
func (w Window) ResourceKind() Kind  { return w.Kind }
func (p Pane) ResourceKind() Kind    { return p.Kind }
func (a Agent) ResourceKind() Kind   { return a.Kind }

// Context is invocation-scoped presentation. All keys are present in list JSON.
type Context struct {
	Value    string `json:"value"`
	Source   string `json:"source"`
	Observed bool   `json:"observed"`
}

// ProjectView is the list projection of a Project.
type ProjectView struct {
	Project
	Context Context `json:"context"`
}

// WindowView is the list projection of a Window.
type WindowView struct {
	Window
	Context Context `json:"context"`
}

// PaneView is the list projection of a Pane.
type PaneView struct {
	Pane
	Context Context `json:"context"`
}

// AgentView is the list projection of an Agent.
type AgentView struct {
	Agent
	Context Context `json:"context"`
}

// ResourceList is the CLI list envelope. Items is always a non-nil slice.
// Items contain the corresponding ProjectView, WindowView, PaneView, or AgentView.
type ResourceList struct {
	APIVersion string     `json:"apiVersion"`
	Kind       string     `json:"kind"`
	Items      []Resource `json:"items"`
}
