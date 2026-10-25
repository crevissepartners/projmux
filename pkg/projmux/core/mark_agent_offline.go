package core

// MarkAgentOfflineRequest selects one process Agent. Project and Window
// optionally scope Ref. Binding, when nonzero, requires that exact recorded
// generation; a replacement is refused before effects.
type MarkAgentOfflineRequest struct {
	Ref, Project, Window string
	Binding              ProcessBinding
}

// MarkAgentOfflineResult reports one lost-host retirement. Changed is false
// when the Agent already held this exact retirement. Termination is the
// reconcile/unknown record without exit status; it is never Wait evidence and
// the result is never a stop. Agent, Pane and conversation remain.
type MarkAgentOfflineResult struct {
	AgentUID, PaneUID string
	Binding           ProcessBinding
	Termination       *TerminationEvidence
	Changed           bool
	Receipt           OperationReceipt
}
