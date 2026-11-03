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
type AgentHostLostVerdict struct {
	AgentUID, PaneUID, Conversation string
	Binding                         ProcessBinding
	Candidate                       bool
	HostIdentityAbsent              bool
	ChildIdentityAbsent             bool
	ActivationAbsent                bool
}
