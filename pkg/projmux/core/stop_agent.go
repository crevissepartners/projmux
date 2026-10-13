package core

// StopAgentRequest selects one process Agent. Project and Window optionally
// scope Ref. Binding, when nonzero, requires that exact observed generation;
// a replacement is refused before effects. No ambient actor is inferred.
type StopAgentRequest struct {
	Ref, Project, Window string
	Binding              ProcessBinding
}

// StopAgentResult retains exact recovery evidence even when Stop fails.
// Stopped means actual Wait and owner retirement were observed, or a child-free
// detached claim was released. ClaimReleased never implies a new Wait record.
// Agent, Pane and conversation remain in the Registry.
type StopAgentResult struct {
	AgentUID, PaneUID      string
	Binding                ProcessBinding
	Termination            *TerminationEvidence
	Stopped, ClaimReleased bool
}
