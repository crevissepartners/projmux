package core

// ReserveNewWindow asks ReserveAgent to create a new Window with the reserved
// Agent. Labels use the CLI key=value grammar.
type ReserveNewWindow struct {
	Name   string
	Labels []string
}

// ReserveAgentRequest reserves one Claude process Agent in a Project without
// starting it. Exactly one of Window (an existing Window) and NewWindow (a new
// Window the reservation owns) is set. The fields shared with CreateAgentRequest
// mean what they mean there; there is no Host, Detach, Pane, Placement or
// Payload, because a reservation takes no first input and starts no process.
type ReserveAgentRequest struct {
	Project string
	// Window names an existing Window of Project.
	Window string
	// NewWindow creates a Window together with the reservation.
	NewWindow *ReserveNewWindow
	Name      string
	Labels    []string
	// Provider must be claude, or empty with a profile that chooses claude.
	Provider, Profile, Instructions, Model, Effort string
	Cwd                                            string
	AddDirs                                        []string
	Creator, SocketPath                            string
}

// ReserveAgentResult identifies the reserved Agent. Its Agent is Pending, no
// process exists, and Binding is the generation a later start must present.
type ReserveAgentResult struct {
	Binding    ProcessBinding
	Agent      Agent
	Notices    []string
	Projection ResourceList
	Receipt    OperationReceipt
}
