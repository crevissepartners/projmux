package core

// CreateWindowRequest creates one shell Window in an explicit Project.
// Labels use the CLI key=value grammar. SocketPath overrides Options.SocketPath.
type CreateWindowRequest struct {
	Project    string
	Name       string
	Labels     []string
	SocketPath string
}

// CreatePaneRequest creates one shell Pane in an explicit Project and Window.
// Pane optionally names its exact split anchor; otherwise the stored Window
// anchor is used. Placement is right (default) or down. CwdFrom is project
// (default) or pane, applying the CLI's Project-tree fallback rule.
type CreatePaneRequest struct {
	Project    string
	Window     string
	Pane       string
	Name       string
	Labels     []string
	Placement  string
	CwdFrom    string
	SocketPath string
}

// CreateWindowResult owns the created resource and CLI JSON/receipt documents.
type CreateWindowResult struct {
	Window     Window
	PaneID     string
	Projection ResourceList
	Receipt    OperationReceipt
}

// CreatePaneResult also carries the CLI's committed cwd fallback notices.
type CreatePaneResult struct {
	Pane       Pane
	PaneID     string
	Notices    []string
	Projection ResourceList
	Receipt    OperationReceipt
}
