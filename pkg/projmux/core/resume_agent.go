package core

// ResumeAgentRequest rebinds one Offline or Failed tmux Agent. Project and
// Window optionally scope Ref. No caller, active Pane, or prompt is inferred.
type ResumeAgentRequest struct {
	Ref, Project, Window string
	Model, Effort        string
	DialogueReplyOnly    bool
	SocketPath           string
}

// ResumeAgentResult identifies the committed rebind. Resume returns immediately
// after commit, without waiting for provider activation, just like the CLI.
type ResumeAgentResult struct {
	Agent      Agent
	PaneID     string
	Notices    []string
	Projection ResourceList
	Receipt    OperationReceipt
}
