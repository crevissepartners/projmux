package metadata

// MarkProcessHostLostOffline commits only the retirement half of
// ReserveProcessHostLostResume: reconcile/unknown evidence, no activation,
// Offline, and a resumable conversation. It reserves no generation. The caller
// must re-prove both process identities absent in the transaction. Unknown
// never becomes durable Wait evidence.
func (m Mutator) MarkProcessHostLostOffline(reg *Registry, activation ProcessActivation) error {
	const op = "mark lost host offline"
	if reg == nil {
		return stateErr(op, ErrInvalidRegistry, "registry unavailable")
	}
	if err := reg.Validate(); err != nil {
		return err
	}
	current, _, ok := reg.CurrentProcessActivation(activation.Binding)
	if !ok || current != activation {
		return stateErr(op, ErrInvalidRegistry, "activation changed")
	}
	next := reg.Clone()
	pane, _ := next.Pane(activation.Binding.PaneUID)
	record := pane.Status.ProcessSession
	if record == nil || record.Binding != activation.Binding || record.ConnectionID != activation.Binding.OperationID || !processResumableConversation(record) {
		return stateErr(op, ErrInvalidRegistry, "conversation unavailable")
	}
	receipt := TerminationEvidence{Source: TerminationSourceReconcile, Classification: TerminationUnknown, ObservedAt: m.clock()().UTC(), PaneUID: activation.Binding.PaneUID, AgentUID: activation.Binding.AgentUID, Generation: activation.Binding.Generation, OperationID: activation.Binding.OperationID}
	if stored := pane.Status.LastTermination; MatchesProcessHostLostRetirement(activation.Binding, stored) {
		receipt = *stored
	}
	outcome, err := m.RecordTermination(&next, receipt)
	if err != nil {
		return err
	}
	pane, _ = next.Pane(activation.Binding.PaneUID)
	agent, _ := next.Agent(activation.Binding.AgentUID)
	if (!outcome.Applied && !outcome.Duplicate) || !SameProcessWait(pane.Status.LastTermination, &receipt) || !SameProcessWait(agent.Status.LastTermination, &receipt) {
		return stateErr(op, ErrInvalidRegistry, "unknown receipt refused")
	}
	pane.Status.Activation = PaneActivation{}
	agent.Status.Phase = PhaseOffline
	pane.Status.ProcessSession.ResumeState = ProcessResumable
	return m.commitProcessRegistry(reg, next)
}

// MatchesProcessHostLostRetirement reports the exact reconcile/unknown receipt
// a lost-host retirement records for binding. It carries no exit status and
// is never Wait evidence; MatchesProcessWait still refuses it.
func MatchesProcessHostLostRetirement(binding ProcessBinding, receipt *TerminationEvidence) bool {
	return receipt != nil && receipt.Source == TerminationSourceReconcile && receipt.Classification == TerminationUnknown && !receipt.ObservedAt.IsZero() &&
		receipt.ExitCode == nil && receipt.Signal == "" &&
		receipt.PaneUID == binding.PaneUID && receipt.AgentUID == binding.AgentUID && receipt.Generation == binding.Generation && receipt.OperationID == binding.OperationID
}

// ProcessHostLostOffline reports whether the process Pane already holds a
// committed lost-host retirement of its recorded generation: no activation,
// an Offline owner, and the same reconcile/unknown receipt on Pane and Agent.
func (r Registry) ProcessHostLostOffline(paneUID string) bool {
	pane, ok := r.Pane(paneUID)
	if !ok || pane.Status.ProcessSession == nil {
		return false
	}
	agent, ok := r.Agent(pane.Status.ProcessSession.Binding.AgentUID)
	return ok && ProcessHostLostRetired(pane.Status.ProcessSession.Binding, *pane, *agent)
}

// ProcessHostLostRetired reports that pane and agent hold the committed
// lost-host retirement of exactly binding's child. Like a supervisor Wait, it
// proves that child retired, so the next writer may reserve a generation. It
// carries no exit status and never becomes Wait evidence.
func ProcessHostLostRetired(binding ProcessBinding, pane Pane, agent Agent) bool {
	return pane.Spec.Runtime.EffectiveKind() == RuntimeProcess && pane.Metadata.UID == binding.PaneUID && pane.Status.Activation.IsZero() &&
		pane.Status.ProcessSession != nil && pane.Status.ProcessSession.Binding == binding &&
		agent.Metadata.UID == binding.AgentUID && agent.Status.Phase == PhaseOffline && agent.Status.PaneRef == pane.Metadata.UID &&
		MatchesProcessHostLostRetirement(binding, pane.Status.LastTermination) && SameProcessWait(pane.Status.LastTermination, agent.Status.LastTermination)
}
