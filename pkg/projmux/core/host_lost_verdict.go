package core

// ReadAgentHostLostRequest selects one process Agent. Project and Window
// optionally scope Ref.
type ReadAgentHostLostRequest struct {
	Ref, Project, Window string
}

// AgentHostLostVerdict is a read-only judgement of whether one process Agent
// lost its host. Nothing is written to produce it. HostIdentityAbsent and
// ChildIdentityAbsent each report that the kernel proved the recorded host
// process or provider child gone (or replaced); ActivationAbsent is both.
// Candidate is true when the Agent is a host-lost resume candidate. It is
// evidence for the host-lost recovery branch of PrepareAgentResume(AllowHostLost),
// not an acceptance: the prepared launch record, claim and provider are judged
// there, and an ordinary retired or activation-less resume is Candidate=false.
// A tmux Agent or an Agent without a recorded activation is Candidate=false with
// every reason false. A live host is Candidate=false, but ChildIdentityAbsent
// is reported on its own, so it can be true beside a live host. Binding is the
// recorded activation's generation and Conversation is the provider
// conversation of the process session record; Conversation is set only for a
// candidate.
//
// History and LastTermination are, for a candidate, a copy of the process
// session record's resume history and of the candidate Pane's last termination
// evidence; they are nil for a non-candidate, and nil for a candidate that
// records none. Every call returns new copies, so two verdicts are equal under
// == only when both are nil; compare verdicts with reflect.DeepEqual.
type AgentHostLostVerdict struct {
	AgentUID, PaneUID, Conversation string
	Binding                         ProcessBinding
	Candidate                       bool
	HostIdentityAbsent              bool
	ChildIdentityAbsent             bool
	ActivationAbsent                bool
	// History is the candidate's recorded resume history, set only when
	// Candidate is true and a history is recorded; otherwise nil.
	History *ProcessResumeHistory
	// LastTermination is the candidate Pane's recorded termination evidence,
	// set only when Candidate is true and one is recorded; otherwise nil.
	LastTermination *TerminationEvidence
}
