package core

// ResumeAgentRequest resumes one Agent. Process Agents require Detach. Project and
// Window optionally scope Ref. No caller, active Pane, or prompt is inferred.
type ResumeAgentRequest struct {
	Ref, Project, Window string
	Model, Effort        string
	DialogueReplyOnly    bool
	SocketPath           string
	// Process-only inputs. Binding, when nonzero, fences the observed generation.
	Detach, WaitForPeer bool
	Prompt              string
	Binding             ProcessBinding
	// Preparation fences a prepared Claude first input. Nil preserves ordinary Resume.
	Preparation *AgentResumePreparation
}

// ResumeAgentResult identifies the committed rebind. Resume returns immediately
// after commit, without waiting for provider activation, just like the CLI.
type ResumeAgentResult struct {
	Agent      Agent
	PaneID     string
	Notices    []string
	Projection ResourceList
	Receipt    OperationReceipt
	Binding    ProcessBinding
	// ClaimWaiting proves a child-free owner claim, not a new provider generation.
	ClaimWaiting bool
	// MayBeTruncated copies the successful process Resume owner's existing CORE
	// Previous.MayBeTruncated advisory. True means prior input may have been cut
	// off; it proves neither actual truncation nor a recorded Wait. It is false
	// for failures, tmux, claim waiting and CORE results without that advisory.
	MayBeTruncated bool
	// Preparation is nil on success. On failure it may acknowledge the unchanged
	// plan or the same frozen plan readdressed after an exact failed child Wait.
	Preparation *AgentResumePreparation
}
