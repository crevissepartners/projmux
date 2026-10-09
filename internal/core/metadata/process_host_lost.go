package metadata

// ReserveProcessHostLostResume atomically retires an exact activation and
// reserves its conversation. The caller must re-prove both process identities
// absent in the transaction. Unknown never becomes durable Wait evidence.
func (m Mutator) ReserveProcessHostLostResume(reg *Registry, activation ProcessActivation, binding ProcessBinding) error {
	if reg == nil {
		return stateErr("resume lost host", ErrInvalidRegistry, "registry unavailable")
	}
	if err := reg.Validate(); err != nil {
		return err
	}
	current, _, ok := reg.CurrentProcessActivation(activation.Binding)
	if !ok || current != activation {
		return stateErr("resume lost host", ErrInvalidRegistry, "activation changed")
	}
	next := reg.Clone()
	pane, _ := next.Pane(activation.Binding.PaneUID)
	record := pane.Status.ProcessSession
	if record == nil || record.Binding != activation.Binding || record.ConnectionID != activation.Binding.OperationID || !processResumableConversation(record) {
		return stateErr("resume lost host", ErrInvalidRegistry, "conversation unavailable")
	}
	receipt := TerminationEvidence{Source: TerminationSourceReconcile, Classification: TerminationUnknown, ObservedAt: m.clock()().UTC(), PaneUID: activation.Binding.PaneUID, AgentUID: activation.Binding.AgentUID, Generation: activation.Binding.Generation, OperationID: activation.Binding.OperationID}
	if stored := pane.Status.LastTermination; stored != nil && stored.Source == TerminationSourceReconcile && stored.Classification == TerminationUnknown && stored.Generation == receipt.Generation && stored.OperationID == receipt.OperationID && stored.PaneUID == receipt.PaneUID && stored.AgentUID == receipt.AgentUID {
		receipt = *stored
	}
	outcome, err := m.RecordTermination(&next, receipt)
	if err != nil {
		return err
	}
	pane, _ = next.Pane(activation.Binding.PaneUID)
	agent, _ := next.Agent(activation.Binding.AgentUID)
	if (!outcome.Applied && !outcome.Duplicate) || !SameProcessWait(pane.Status.LastTermination, &receipt) || !SameProcessWait(agent.Status.LastTermination, &receipt) {
		return stateErr("resume lost host", ErrInvalidRegistry, "unknown receipt refused")
	}
	pane.Status.Activation = PaneActivation{}
	agent.Status.Phase = PhaseOffline
	// This temporary projection reuses the reservation rules; it is never
	// persisted. The new reservation and its history retain unknown state.
	pane.Status.ProcessSession.ResumeState = ProcessResumable
	pane.Status.ProcessSession.History = nil
	if err := m.ReserveProcessResume(&next, activation.Binding, binding); err != nil {
		return err
	}
	return m.commitProcessRegistry(reg, next)
}

// RestoreProcessHostLostResume restores the dead activation only when no new
// child was spawned, so the next explicit resume must prove absence again.
func (m Mutator) RestoreProcessHostLostResume(reg *Registry, binding ProcessBinding, previous ProcessSessionRecord, activation ProcessActivation) error {
	if reg == nil || activation.Binding != previous.Binding {
		return stateErr("restore lost host", ErrInvalidRegistry, "previous activation unavailable")
	}
	next := reg.Clone()
	retired := previous
	retired.ResumeState = ProcessResumable
	retired.History = nil
	if err := m.RestoreProcessResume(&next, binding, retired); err != nil {
		return err
	}
	pane, _ := next.Pane(previous.Binding.PaneUID)
	pane.Status.ProcessSession = previous.Clone()
	pane.Status.Activation = PaneActivation{Kind: RuntimeProcess, Process: &activation, AgentUID: activation.Binding.AgentUID, Generation: activation.Binding.Generation, OperationID: activation.Binding.OperationID}
	agent, _ := next.Agent(previous.Binding.AgentUID)
	agent.Status.Phase = PhaseRunning
	return m.commitProcessRegistry(reg, next)
}
