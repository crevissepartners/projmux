package metadata

import "strings"

// CodexActivationObservation is the complete identity a native app-server
// create/resume or later exact event is allowed to commit. It is deliberately
// separate from AgentSessionObservation: turn identity is scoped to one Pane
// generation and never becomes a durable conversation pointer.
type CodexActivationObservation struct {
	AgentUID   string
	PaneUID    string
	Generation string
	ThreadID   string
	TurnID     string
	Endpoint   CodexEndpointRef
}

// StageCodexEndpoint records the exact managed generation selected for a
// native create before the provider is allowed to mint a thread. The
// temporarily conversation-free session ref is valid only inside the caller's
// Registry transaction: BindCodexActivation must complete it before commit.
// Keeping this write independent of Pane activation generation prevents a
// rematerialized Pane from silently changing provider endpoint ownership.
func (m Mutator) StageCodexEndpoint(reg *Registry, agentUID string, endpoint CodexEndpointRef) error {
	const op = "prepare Codex endpoint"
	agentUID = strings.TrimSpace(agentUID)
	if agentUID == "" || !endpoint.Valid() {
		return inputErr(op, ErrInvalidRegistry, "exact Agent and endpoint generation are required")
	}
	agent, ok := reg.Agent(agentUID)
	if !ok || agent.Spec.Provider != "codex" || agent.Status.Phase != PhaseRunning {
		return stateErr(op, ErrInvalidRegistry, "Agent is missing, non-Codex, or not Running")
	}
	if !agent.Status.SessionRef.Empty() {
		return stateErr(op, ErrInvalidRegistry, "Agent already has a durable provider session ref")
	}
	prepared := endpoint
	agent.Status.SessionRef = &AgentSessionRef{
		Provider:   "codex",
		ObservedAt: m.clock()().UTC(),
		Codex: &CodexSessionRef{
			Endpoint:  &prepared,
			Lifecycle: &CodexGenerationLifecycleRef{State: CodexGenerationCurrent},
		},
	}
	reg.UpdatedAt = m.clock()().UTC()
	return nil
}

// BindCodexActivation commits the first native binding for one exact running
// Agent materialization. It also records the returned thread as the Agent's
// durable Codex sessionRef. A create may fill an empty ref; a resume may only
// reuse the already stored same thread.
func (m Mutator) BindCodexActivation(reg *Registry, obs CodexActivationObservation) (bool, error) {
	const op = "bind Codex activation"
	obs = normalizeCodexActivationObservation(obs)
	if obs.AgentUID == "" || obs.PaneUID == "" || obs.Generation == "" || obs.ThreadID == "" || !obs.Endpoint.Valid() {
		return false, inputErr(op, ErrInvalidRegistry, "exact Agent, Pane, activation generation, thread, and endpoint generation are required")
	}
	agent, pane, ok := exactCodexActivationTarget(reg, obs)
	if !ok {
		return false, stateErr(op, ErrInvalidRegistry, "Agent/Pane activation binding is stale or ambiguous")
	}
	if ref := agent.Status.SessionRef; !ref.Empty() {
		if ref.Provider != "codex" || ref.Codex == nil || ref.Codex.Endpoint == nil ||
			!ref.Codex.Endpoint.Same(obs.Endpoint) ||
			(strings.TrimSpace(ref.Codex.ThreadID) != "" && strings.TrimSpace(ref.Codex.ThreadID) != obs.ThreadID) {
			return false, stateErr(op, ErrInvalidRegistry, "Agent sessionRef names a different conversation")
		}
		firstThreadBinding := strings.TrimSpace(ref.Codex.ThreadID) == ""
		ref.Codex.ThreadID = obs.ThreadID
		ref.Codex.HasStartedTurn = ref.Codex.HasStartedTurn || obs.TurnID != ""
		if firstThreadBinding {
			ref.ObservedAt = m.clock()().UTC()
		}
	} else {
		ref, built := NewAgentSessionRef(AgentSessionObservation{Provider: "codex", ThreadID: obs.ThreadID, Endpoint: &obs.Endpoint}, m.clock()().UTC())
		if !built {
			return false, inputErr(op, ErrInvalidRegistry, "native Codex thread is empty")
		}
		agent.Status.SessionRef = ref
	}
	if pane.Status.Activation.Codex != nil &&
		pane.Status.Activation.Codex.ThreadID == obs.ThreadID && pane.Status.Activation.Codex.TurnID == obs.TurnID {
		return false, nil
	}
	agent.Status.Progress = AgentProgress{}
	pane.Status.Activation.Codex = &CodexActivationBinding{ThreadID: obs.ThreadID, TurnID: obs.TurnID}
	if obs.TurnID != "" {
		if _, err := m.SetAgentActivation(reg, obs.AgentUID, ActivationAcknowledged, string(InteractionSourceProviderControl), ""); err != nil {
			return false, err
		}
	}
	reg.UpdatedAt = m.clock()().UTC()
	return true, nil
}

// RefineCodexActivation updates only the turn of an already exact native
// binding. Late generations, foreign Panes/Agents, and another thread are
// normal rejected observations: they return changed=false and write nothing.
func (m Mutator) RefineCodexActivation(reg *Registry, obs CodexActivationObservation) (bool, error) {
	obs = normalizeCodexActivationObservation(obs)
	agent, pane, ok := exactCodexActivationTarget(reg, obs)
	if !ok || pane.Status.Activation.Codex == nil ||
		pane.Status.Activation.Codex.ThreadID != obs.ThreadID || obs.ThreadID == "" || obs.TurnID == "" ||
		agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil ||
		agent.Status.SessionRef.Codex.Endpoint == nil || !agent.Status.SessionRef.Codex.Endpoint.Same(obs.Endpoint) ||
		strings.TrimSpace(agent.Status.SessionRef.Codex.ThreadID) != obs.ThreadID {
		return false, nil
	}
	if pane.Status.Activation.Codex.TurnID == obs.TurnID {
		if agent.Status.SessionRef.Codex.HasStartedTurn {
			return false, nil
		}
		agent.Status.SessionRef.Codex.HasStartedTurn = true
		reg.UpdatedAt = m.clock()().UTC()
		return true, nil
	}
	pane.Status.Activation.Codex.TurnID = obs.TurnID
	agent.Status.Progress = AgentProgress{}
	agent.Status.SessionRef.Codex.HasStartedTurn = true
	reg.UpdatedAt = m.clock()().UTC()
	return true, nil
}

func normalizeCodexActivationObservation(obs CodexActivationObservation) CodexActivationObservation {
	obs.AgentUID = strings.TrimSpace(obs.AgentUID)
	obs.PaneUID = strings.TrimSpace(obs.PaneUID)
	obs.Generation = strings.TrimSpace(obs.Generation)
	obs.ThreadID = strings.TrimSpace(obs.ThreadID)
	obs.TurnID = strings.TrimSpace(obs.TurnID)
	obs.Endpoint.StateDomainID = strings.TrimSpace(obs.Endpoint.StateDomainID)
	obs.Endpoint.EndpointGenerationID = strings.TrimSpace(obs.Endpoint.EndpointGenerationID)
	return obs
}

func exactCodexActivationTarget(reg *Registry, obs CodexActivationObservation) (*Agent, *Pane, bool) {
	agent, agentOK := reg.Agent(obs.AgentUID)
	pane, paneOK := reg.Pane(obs.PaneUID)
	if !agentOK || !paneOK || agent.Status.Phase != PhaseRunning || agent.Status.PaneRef != obs.PaneUID ||
		pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != KindAgent || pane.Metadata.OwnerRef.UID != obs.AgentUID ||
		pane.Status.Activation.AgentUID != obs.AgentUID || pane.Status.Activation.Generation != obs.Generation {
		return nil, nil, false
	}
	return agent, pane, true
}
