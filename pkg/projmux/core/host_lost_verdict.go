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
// Candidate is true when the Agent is a host-lost resume candidate. It is a
// necessary condition of a PrepareAgentResume(AllowHostLost) acceptance, not a
// sufficient one: the prepared launch record, claim and provider are judged
// there. A tmux Agent, a live host or an Agent without a recorded activation
// is Candidate=false with every reason false. Binding is the recorded
// activation's generation and Conversation is the provider conversation of the
// process session record; Conversation is set only for a candidate.
type AgentHostLostVerdict struct {
	AgentUID, PaneUID, Conversation string
	Binding                         ProcessBinding
	Candidate                       bool
	HostIdentityAbsent              bool
	ChildIdentityAbsent             bool
	ActivationAbsent                bool
}
