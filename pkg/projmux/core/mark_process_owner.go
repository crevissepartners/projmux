package core

// MarkProcessOwnerRequest asks to write process-owner=Owner on the Agent that
// Binding names. Every Binding field is required; take it unchanged from the
// Resume, Relaunch or StartReserved result. ReplaceForeign lets the write
// overwrite a different non-empty owner.
type MarkProcessOwnerRequest struct {
	Binding        ProcessBinding
	Owner          string
	ReplaceForeign bool
}

// MarkProcessOwnerResult reports the write. Agent is the projection from the
// same Registry commit. Changed is false when the Agent already held Owner.
// Previous is the process-owner value before the call on every success: Owner
// itself when unchanged, empty when none was set.
type MarkProcessOwnerResult struct {
	Agent    Agent
	Changed  bool
	Previous string
}
