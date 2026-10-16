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
}
