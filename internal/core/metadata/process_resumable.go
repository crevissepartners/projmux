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

// A child that never established a conversation still has a valid Wait, but
// cannot become a resume candidate. Promotion shares the retirement transaction.
func (m Mutator) promoteProcessWaitResume(reg *Registry, binding ProcessBinding, receipt TerminationEvidence) error {
	pane, _ := reg.Pane(binding.PaneUID)
	record := pane.Status.ProcessSession
	if record == nil || !processResumableConversation(record) || record.ConnectionID != binding.OperationID {
		return nil
	}
	return m.RecordProcessResumable(reg, binding, &receipt)
}

// ReserveProcessResume replaces one retired generation while preserving its
// interrupted work as history. It grants no child or control authority.
func (m Mutator) ReserveProcessResume(reg *Registry, previous ProcessBinding, binding ProcessBinding) error {
	const op = "reserve process resume"
	if reg == nil {
		return stateErr(op, ErrInvalidRegistry, "process registry is unavailable")
	}
	if err := reg.Validate(); err != nil {
		return err
	}
	pane, found := reg.Pane(previous.PaneUID)
	if !found || !reg.validProcessBinding(*pane, previous) || !reg.validProcessBinding(*pane, binding) ||
		binding.ProjectUID != previous.ProjectUID || binding.WindowUID != previous.WindowUID || binding.AgentUID != previous.AgentUID || binding.PaneUID != previous.PaneUID ||
		binding.Generation == previous.Generation || binding.OperationID == previous.OperationID || binding.HostInstanceID == previous.HostInstanceID {
		return stateErr(op, ErrInvalidRegistry, "resume requires the same owner chain and a fresh host generation")
	}
	agent, _ := reg.Agent(previous.AgentUID)
	record := pane.Status.ProcessSession
	if pane.Spec.Runtime.EffectiveKind() != RuntimeProcess || agent.Status.Phase != PhaseOffline || agent.Status.PaneRef != pane.Metadata.UID || !pane.Status.Activation.IsZero() || record == nil || record.Binding != previous || record.ResumeState != ProcessResumable {
		return stateErr(op, ErrInvalidRegistry, "retired resumable conversation is unavailable")
	}
	next := reg.Clone()
	target, _ := next.Pane(binding.PaneUID)
	current := record.Clone()
	session := current.SessionID
	if current.Provider == "codex" {
		session = current.ThreadID
	}
	current.History = &ProcessResumeHistory{Binding: previous, SessionID: session, InterruptedTurnID: current.TurnID, Expired: current.Pending}
	current.Binding, current.ConnectionID, current.TurnID, current.Pending, current.ResumeState = binding, binding.OperationID, "", nil, ProcessResumeUnknown
	target.Status.ProcessSession = current
	owner, _ := next.Agent(binding.AgentUID)
	owner.Status.Phase = PhasePending
	return m.commitProcessRegistry(reg, next)
}
