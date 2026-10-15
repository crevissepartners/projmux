package core

// DeleteRequest supplies one explicit ref for tmux Window, Pane or Agent deletion.
// One Ref may select multiple resources, returned in Plan.Targets. Confirm uses
// the CLI rule, including the exemption for one named leaf Pane. Ref is a name
// or uid:<uid>; selection never infers an active Pane. Window scopes only Pane
// and Agent targets. SocketPath overrides Options.SocketPath; empty routing
// refuses like a CLI delete outside tmux with no socket flags.
type DeleteRequest struct {
	Kind       Kind
	Ref        string
	Project    string
	Window     string
	SocketPath string
	DryRun     bool
	Confirm    bool
}

// DeleteWindow is one exact live tmux target in the reviewed deletion plan.
type DeleteWindow struct {
	UID         string
	WindowID    string
	SessionID   string
	SessionName string
	RootKind    Kind
	RootUID     string
	EndsSession bool
	// Self is always false: the SDK has no active Pane.
	Self bool
}

// DeleteResult returns the resource and live plan even when Confirm is missing.
// A committed result carries the CLI receipt. Notice reports a best-effort
// deletion-record warning without failing the committed transaction.
type DeleteResult struct {
	Plan         DeletePlan
	Windows      []DeleteWindow
	Panes        []DeletePane
	RegistryOnly []DeleteRegistryOnly
	// Processes is the public CLI's process Agent plan. It contains preflight
	// evidence on DryRun, confirmation refusal, or execution failure, and final
	// evidence after a committed deletion. It is not authority for a later Stop.
	Processes   []DeleteProcess
	Receipt     OperationReceipt
	OperationID string
	Committed   bool
	Notice      string
}

// DeleteProcess contains only fields shown by the process delete CLI. Runtime
// is live classification (running, offline, unknown, or stopped), not Agent phase.
// HostPID is zero when the CLI does not report a live owner PID.
type DeleteProcess struct {
	AgentUID  string
	WindowUID string
	RootKind  Kind
	RootUID   string
	Runtime   string
	Evidence  string
	HostPID   int
}

// DeletePane is one exact live tmux Pane in the reviewed deletion plan.
// EndsWindow marks a required replacement shell; its owning Window is preserved.
// Self is always false because the SDK has no active Pane.
type DeletePane struct {
	ResourceUID   string
	PaneUID       string
	PaneID        string
	WindowUID     string
	WindowID      string
	SessionID     string
	SessionName   string
	RootKind      Kind
	RootUID       string
	EndsWindow    bool
	EndsSession   bool
	Self          bool
	OwnerKind     Kind
	OwnerUID      string
	AgentUID      string
	PaneRole      PaneRole
	Generation    string
	SupervisorPID int
	RequireDead   bool
}

// DeleteRegistryOnly describes exact Offline or Failed lifecycle evidence that
// authorizes deletion without a live Pane mirror.
type DeleteRegistryOnly struct {
	ResourceUID string
	Kind        string
	Evidence    string
	WindowUID   string
	RootKind    Kind
	RootUID     string
}
