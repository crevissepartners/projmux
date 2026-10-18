package core

// AgentResumePreparation is observed evidence of a frozen Claude resume plan,
// not authority or a writer grant. Session authorization, mutation locks and
// acknowledgement storage belong to the consumer.
type AgentResumePreparation struct {
	AgentUID, PaneUID, Conversation, LaunchDigest string
	Binding                                       ProcessBinding
	HostLost                                      bool
}

// PrepareAgentResumeRequest explicitly prepares a process Claude Agent without
// starting a provider or retaining a claim. A nonzero Binding fences its source.
// AllowHostLost opts into the existing absent-host candidate checks.
type PrepareAgentResumeRequest struct {
	Ref, Project, Window string
	Binding              ProcessBinding
	AllowHostLost        bool
}

type PrepareAgentResumeResult struct{ Preparation AgentResumePreparation }

// ValidateAgentResumePreparationRequest reads and validates an acknowledged
// preparation without reconciling or changing its durable plan.
type ValidateAgentResumePreparationRequest struct {
	Ref, Project, Window string
	Preparation          AgentResumePreparation
}
