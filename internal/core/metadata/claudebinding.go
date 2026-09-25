package metadata

// ClaudeActivationBinding is an observation of the exact child launched by the
// activation gate. It is replaced with the Pane generation, never resumed from
// the durable AgentSessionRef. Only a public SessionStart can add Registration.
type ClaudeActivationBinding struct {
	Process                ProcessIdentity     `json:"process"`
	RegistrationSessionID  string              `json:"registrationSessionId,omitempty"`
	RegistrationGeneration string              `json:"registrationGeneration,omitempty"`
	Registration           *ClaudeRegistration `json:"registration,omitempty"`
}

// ClaudeRegistration is entirely non-secret. Names and optional public titles
// are not part of the route, authority, or registration.
type ClaudeRegistration struct {
	Authority ClaudeAuthorityRef `json:"authority"`
	Ready     bool               `json:"ready"`
}

func (m Mutator) RecordClaudeProcess(reg *Registry, paneUID, agentUID, generation string, process ProcessIdentity) error {
	const op = "record Claude process"
	pane, ok := reg.Pane(paneUID)
	agent, found := reg.Agent(agentUID)
	if !ok || !found || agent.Spec.Provider != "claude" || agent.Status.Phase != PhaseRunning ||
		agent.Status.PaneRef != paneUID || pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != KindAgent || pane.Metadata.OwnerUID() != agentUID || pane.Spec.Role != PaneRoleAgent ||
		pane.Status.Activation.AgentUID != agentUID || pane.Status.Activation.Generation != generation || generation == "" || !process.Valid() {
		return stateErr(op, ErrInvalidRegistry, "exact managed Claude activation is unavailable")
	}
	if pane.Status.Activation.Claude != nil {
		return stateErr(op, ErrInvalidRegistry, "Claude process has already been bound")
	}
	pane.Status.Activation.Claude = &ClaudeActivationBinding{Process: process}
	reg.UpdatedAt = m.clock()().UTC()
	return nil
}

// BeginClaudeRegistration claims a new registration generation
// unconditionally. A newer claim fences out an older helper still starting,
// before either can be Ready.
func (m Mutator) BeginClaudeRegistration(reg *Registry, paneUID, agentUID, generation string, authority ClaudeAuthorityRef) error {
	const op = "begin Claude registration"
	pane, ok := claudeRegistrationClaimTarget(reg, paneUID, agentUID, generation, authority)
	if !ok {
		return stateErr(op, ErrInvalidRegistry, "exact managed Claude activation is unavailable")
	}
	m.claimClaudeRegistration(reg, pane, authority)
	return nil
}

// BeginClaudeRegistrationAfter is the helper's admission claim for the
// SessionStart hook that observed priorRegistrationGeneration on the pane
// before it started the helper. It is a CAS: the claim succeeds only while the
// pane still carries that generation, and is a no-op success when it already
// carries the helper's own. Any other generation belongs to a SessionStart that
// claimed after the hook looked, so this stale helper writes nothing.
func (m Mutator) BeginClaudeRegistrationAfter(reg *Registry, paneUID, agentUID, generation, priorRegistrationGeneration string, authority ClaudeAuthorityRef) error {
	const op = "begin Claude registration"
	pane, ok := claudeRegistrationClaimTarget(reg, paneUID, agentUID, generation, authority)
	if !ok {
		return stateErr(op, ErrInvalidRegistry, "exact managed Claude activation is unavailable")
	}
	binding := pane.Status.Activation.Claude
	switch binding.RegistrationGeneration {
	case authority.RegistrationGeneration:
		if binding.RegistrationSessionID != authority.SessionID {
			return stateErr(op, ErrInvalidRegistry, "competing Claude registration claim")
		}
		return nil
	case priorRegistrationGeneration:
		m.claimClaudeRegistration(reg, pane, authority)
		return nil
	default:
		return stateErr(op, ErrInvalidRegistry, "newer Claude registration claim")
	}
}

func claudeRegistrationClaimTarget(reg *Registry, paneUID, agentUID, generation string, authority ClaudeAuthorityRef) (*Pane, bool) {
	pane, ok := reg.Pane(paneUID)
	agent, found := reg.Agent(agentUID)
	if !ok || !found || agent.Spec.Provider != "claude" || agent.Status.Phase != PhaseRunning || agent.Status.PaneRef != paneUID ||
		pane.Spec.Role != PaneRoleAgent || pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != KindAgent || pane.Metadata.OwnerUID() != agentUID ||
		pane.Status.Activation.AgentUID != agentUID || pane.Status.Activation.Generation != generation || generation == "" ||
		pane.Status.Activation.Claude == nil || pane.Status.Activation.Claude.Process != authority.Process || !authority.Valid() {
		return nil, false
	}
	return pane, true
}

func (m Mutator) claimClaudeRegistration(reg *Registry, pane *Pane, authority ClaudeAuthorityRef) {
	pane.Status.Activation.Claude.RegistrationSessionID = authority.SessionID
	pane.Status.Activation.Claude.RegistrationGeneration = authority.RegistrationGeneration
	pane.Status.Activation.Claude.Registration = nil
	reg.UpdatedAt = m.clock()().UTC()
}

func (m Mutator) RecordClaudeRegistration(reg *Registry, paneUID, agentUID, generation string, registration ClaudeRegistration) error {
	const op = "record Claude registration"
	pane, ok := reg.Pane(paneUID)
	agent, found := reg.Agent(agentUID)
	if !ok || !found || agent.Spec.Provider != "claude" || agent.Status.Phase != PhaseRunning ||
		agent.Status.PaneRef != paneUID || pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != KindAgent || pane.Metadata.OwnerUID() != agentUID || pane.Spec.Role != PaneRoleAgent ||
		pane.Status.Activation.AgentUID != agentUID || pane.Status.Activation.Generation != generation || generation == "" ||
		pane.Status.Activation.Claude == nil || pane.Status.Activation.Claude.Process != registration.Authority.Process ||
		pane.Status.Activation.Claude.RegistrationGeneration != registration.Authority.RegistrationGeneration ||
		pane.Status.Activation.Claude.RegistrationSessionID != registration.Authority.SessionID || !registration.Authority.Valid() {
		return stateErr(op, ErrInvalidRegistry, "exact managed Claude registration is unavailable")
	}
	if current := pane.Status.Activation.Claude.Registration; current != nil {
		if current.Authority == registration.Authority {
			return nil
		}
		return stateErr(op, ErrInvalidRegistry, "competing Claude registration lease")
	}
	registration.Ready = true
	pane.Status.Activation.Claude.Registration = &registration
	reg.UpdatedAt = m.clock()().UTC()
	return nil
}

// ClearClaudeRegistration is a CAS: an old helper cannot clear the new lease.
func (m Mutator) ClearClaudeRegistration(reg *Registry, paneUID, agentUID, generation string, authority ClaudeAuthorityRef) bool {
	pane, ok := reg.Pane(paneUID)
	agent, found := reg.Agent(agentUID)
	if !ok || pane.Status.Activation.Generation != generation || pane.Status.Activation.Claude == nil ||
		!found || agent.Spec.Provider != "claude" || agent.Status.PaneRef != paneUID || pane.Spec.Role != PaneRoleAgent || pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != KindAgent ||
		pane.Metadata.OwnerUID() != agentUID || pane.Status.Activation.AgentUID != agentUID ||
		pane.Status.Activation.Claude.Process != authority.Process ||
		pane.Status.Activation.Claude.RegistrationSessionID != authority.SessionID ||
		pane.Status.Activation.Claude.RegistrationGeneration != authority.RegistrationGeneration ||
		pane.Status.Activation.Claude.Registration == nil ||
		pane.Status.Activation.Claude.Registration.Authority != authority {
		return false
	}
	pane.Status.Activation.Claude.Registration = nil
	reg.UpdatedAt = m.clock()().UTC()
	return true
}

func clearClaudeRegistration(pane *Pane) {
	if pane != nil && pane.Status.Activation.Claude != nil {
		pane.Status.Activation.Claude.Registration = nil
		pane.Status.Activation.Claude.RegistrationGeneration = ""
		pane.Status.Activation.Claude.RegistrationSessionID = ""
	}
}
