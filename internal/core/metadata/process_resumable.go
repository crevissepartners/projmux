package metadata

// RecordProcessResumable promotes a conversation only after the owner has
// durably recorded this exact child Wait and retired its activation. The caller
// obtains Wait outside the Registry lock and may call this after its termination
// writer in the same transaction. This writer never retires a host itself.
// The retired generation and operation identify the child in the Wait receipt;
// there is no current activation against which to compare process birth data.
// TurnID and Pending remain identities of interrupted work in this generation;
// the resume consumer moves them to history when it reserves a new generation.
func (m Mutator) RecordProcessResumable(reg *Registry, b ProcessBinding, receipt *TerminationEvidence) error {
	const op = "record resumable process"
	if reg == nil {
		return stateErr(op, ErrInvalidRegistry, "process registry is unavailable")
	}
	if err := reg.Validate(); err != nil {
		return err
	}
	pane, ok := reg.Pane(b.PaneUID)
	if !ok || !reg.validProcessBinding(*pane, b) || pane.Spec.Runtime.EffectiveKind() != RuntimeProcess {
		return stateErr(op, ErrInvalidRegistry, "exact process binding is unavailable")
	}
	agent, _ := reg.Agent(b.AgentUID)
	s := pane.Status.ProcessSession
	if agent.Status.Phase != PhaseOffline || !pane.Status.Activation.IsZero() || (agent.Status.PaneRef != "" && agent.Status.PaneRef != b.PaneUID) || s == nil || s.Binding != b || s.ConnectionID != b.OperationID || !processResumableConversation(s) {
		return stateErr(op, ErrInvalidRegistry, "process conversation has not been retired")
	}
	if !MatchesProcessWait(b, receipt) || !SameProcessWait(pane.Status.LastTermination, receipt) || !SameProcessWait(agent.Status.LastTermination, receipt) {
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
