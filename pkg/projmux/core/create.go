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

// OperatorCreator names the in-process operator client a create runs for. Its
// client name is unexported, so decoding request data (JSON, a form, a Creator
// string) never produces one; only in-process code that calls
// NewOperatorCreator does. The zero value names no operator.
type OperatorCreator struct{ client string }

// NewOperatorCreator names client as the operator a create runs for. CreateAgent
// checks the name against the operator client rule (1-32 bytes of lowercase
// ASCII letters, digits and '-', starting with a letter) before anything
// changes and refuses a name outside it with create-creator-invalid. An empty
// name is the zero value.
func NewOperatorCreator(client string) OperatorCreator { return OperatorCreator{client: client} }

// Client is the operator client name, or empty for the zero value.
func (o OperatorCreator) Client() string { return o.client }

// CreateAgentRequest creates one Agent in an explicitly named Project and
// Window. Creator is an optional uid: Agent declaration; no caller is inferred.
// Operator, set only in process, records the operator client as the creator
// under the CLI's rule: it takes precedence over Creator, which is then reported
// in Notices as not recorded. Instructions names stored instructions, while
// Payload contains provider argv.
type CreateAgentRequest struct {
	// Host defaults to tmux. Process requires Detach; foreground SDK ownership
	// and config-based host inference are unsupported.
	Host                                           string
	Detach                                         bool
	Project, Window, Pane                          string
	Provider, Profile, Instructions, Model, Effort string
	Cwd                                            string
	AddDirs                                        []string
	InteractiveOnly, DialogueReplyOnly             bool
	Name                                           string
	Labels                                         []string
	Placement, CwdFrom                             string
	Payload                                        []string
	Creator, SocketPath                            string
	Operator                                       OperatorCreator
}

// CreateAgentResult also accompanies an activation-unconfirmed error: its
// Projection, Receipt and PaneID identify the committed resources to preserve.
type CreateAgentResult struct {
	// Binding identifies a detached process generation; zero for tmux.
	Binding    ProcessBinding
	Agent      Agent
	PaneID     string
	Notices    []string
	Projection ResourceList
	Receipt    OperationReceipt
}
