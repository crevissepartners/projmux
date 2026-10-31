package core

// StartReservedAgentRequest starts the provider process of one Agent that
// ReserveAgent left Pending. The reservation already fixed the Project, Window,
// provider and launch, so none of them can be given here. Its creator was
// recorded when it was reserved.
type StartReservedAgentRequest struct {
	// Agent is the exact reference uid:<agent> of a reserved Agent.
	Agent string
}

// StartReservedAgentResult has the shape of CreateAgentResult for a detached
// process Agent: the Binding is the one the reservation returned.
type StartReservedAgentResult struct {
	Binding    ProcessBinding
	Agent      Agent
	Notices    []string
	Projection ResourceList
	Receipt    OperationReceipt
}
