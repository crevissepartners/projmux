package metadata

// RecordProcessResumable promotes a conversation only after the owner has
// durably recorded this exact child Wait and retired its activation. The caller
// obtains Wait outside the Registry lock and may call this after its termination
// writer in the same transaction. This writer never retires a host itself.
// TurnID and Pending remain identities of interrupted work in this generation;
// the resume consumer moves them to history when it reserves a new generation.
func (m Mutator) RecordProcessResumable(reg *Registry, activation ProcessActivation, receipt *TerminationEvidence) error {
	const op = "record resumable process"
	if reg == nil {
		return stateErr(op, ErrInvalidRegistry, "process registry is unavailable")
	}
	if err := reg.Validate(); err != nil {
		return err
	}
	b := activation.Binding
	pane, ok := reg.Pane(b.PaneUID)
	if !ok || !reg.validProcessBinding(*pane, b) || pane.Spec.Runtime.EffectiveKind() != RuntimeProcess || !activation.HostProcess.Valid() || !activation.Child.Valid() {
		return stateErr(op, ErrInvalidRegistry, "exact process binding is unavailable")
	}
	agent, _ := reg.Agent(b.AgentUID)
	s := pane.Status.ProcessSession
	if agent.Status.Phase != PhaseOffline || !pane.Status.Activation.IsZero() || (agent.Status.PaneRef != "" && agent.Status.PaneRef != b.PaneUID) || s == nil || s.Binding != b || s.ConnectionID != b.OperationID || !processResumableConversation(s) {
		return stateErr(op, ErrInvalidRegistry, "process conversation has not been retired")
	}
	if !processResumableWait(b, receipt) || !sameEvidence(pane.Status.LastTermination, receipt) || !sameEvidence(agent.Status.LastTermination, receipt) {
		return stateErr(op, ErrInvalidRegistry, "matching durable child Wait is unavailable")
	}
	if s.ResumeState == ProcessResumable {
		return nil
	}
	next := reg.Clone()
	target, _ := next.Pane(b.PaneUID)
	target.Status.ProcessSession.ResumeState = ProcessResumable
	if err := next.Validate(); err != nil {
		return err
	}
	next.UpdatedAt = m.clock()().UTC()
	*reg = next
	return nil
}

func processResumableConversation(s *ProcessSessionRecord) bool {
	return (s.Provider == "claude" && processIdentityToken(s.SessionID)) || (s.Provider == "codex" && processIdentityToken(s.ThreadID))
}

func processResumableWait(b ProcessBinding, receipt *TerminationEvidence) bool {
	if receipt == nil || receipt.Source != TerminationSourceSupervisor || receipt.PaneUID != b.PaneUID || receipt.AgentUID != b.AgentUID || receipt.Generation != b.Generation || receipt.OperationID != b.OperationID {
		return false
	}
	code := 0
	if receipt.ExitCode != nil {
		code = *receipt.ExitCode
		if code < 0 || code > 255 || receipt.Signal != "" {
			return false
		}
	} else if !processIdentityToken(receipt.Signal) {
		return false
	}
	return receipt.Classification == ClassifyProcessExit(code, receipt.Signal)
}
