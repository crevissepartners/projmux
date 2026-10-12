package core

// DeleteRequest supplies one explicit ref for exact tmux Window deletion.
// One Ref may select multiple resources, returned in Plan.Targets; more than one
// requires Confirm, using the same confirmation rule as the CLI. Pane and Agent kinds
// are reserved for the next SDK slice and currently refused. Ref is a name or
// uid:<uid>; selection never infers an active Pane. Window scopes only Pane and
// Agent targets. SocketPath overrides Options.SocketPath; empty routing refuses
// the same way as a CLI delete outside tmux with no socket flags.
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
	Plan        DeletePlan
	Windows     []DeleteWindow
	Receipt     OperationReceipt
	OperationID string
	Committed   bool
	Notice      string
}
