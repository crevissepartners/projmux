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

// CodexHandoverTarget is the immutable Agent↔Pane↔thread tuple captured by a
// generation-wide handover journal before the old owner is stopped.
type CodexHandoverTarget struct {
	AgentUID           string
	PaneUID            string
	PaneRuntimeID      string
	PaneGeneration     string
	RelaunchGeneration string
	ThreadID           string
}

// RecordCodexHandoverResume persists the exact at-most-once provider effect
// receipt after thread/resume succeeds and before the coordinator returns from
// its Ensure effect. Reapplying the identical receipt is a no-op; any tuple or
// endpoint drift refuses the write.
func (m Mutator) RecordCodexHandoverResume(reg *Registry, target CodexHandoverTarget, oldEndpoint, successor CodexEndpointRef, operationID string) (bool, error) {
	const op = "record Codex generation handover resume"
	agent, agentOK := reg.Agent(strings.TrimSpace(target.AgentUID))
	pane, paneOK := reg.Pane(strings.TrimSpace(target.PaneUID))
	if !agentOK || !paneOK || agent.Status.Phase != PhaseRunning || agent.Status.PaneRef != target.PaneUID ||
		pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != KindAgent || pane.Metadata.OwnerRef.UID != target.AgentUID ||
		pane.Status.Activation.AgentUID != target.AgentUID || pane.Status.Activation.RuntimeID != target.PaneRuntimeID ||
		pane.Status.Activation.Generation != target.PaneGeneration || pane.Status.Activation.Codex == nil ||
		pane.Status.Activation.Codex.ThreadID != target.ThreadID || agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil ||
		agent.Status.SessionRef.Codex.Endpoint == nil || !agent.Status.SessionRef.Codex.Endpoint.Same(oldEndpoint) ||
		agent.Status.SessionRef.Codex.ThreadID != target.ThreadID || !agent.Status.SessionRef.Codex.HasStartedTurn {
		return false, stateErr(op, ErrInvalidRegistry, "exact Agent/Pane/thread tuple changed")
	}
	receipt := CodexHandoverResumeReceipt{OperationID: strings.TrimSpace(operationID), SuccessorEndpoint: successor,
		AgentUID: target.AgentUID, PaneUID: target.PaneUID, PaneRuntimeID: target.PaneRuntimeID,
		PaneGeneration: target.PaneGeneration, ThreadID: target.ThreadID}
	if !receipt.ValidFor(&oldEndpoint) {
		return false, inputErr(op, ErrInvalidRegistry, "exact operation and endpoint pair are required")
	}
	if current := agent.Status.SessionRef.Codex.HandoverResume; current != nil {
		if *current == receipt {
			return false, nil
		}
		return false, stateErr(op, ErrInvalidRegistry, "another handover resume receipt already owns the thread")
	}
	agent.Status.SessionRef.Codex.HandoverResume = &receipt
	reg.UpdatedAt = m.clock()().UTC()
	return true, nil
}

// CASCodexHandoverTarget moves one exact completed thread to its successor
// endpoint while retaining the Agent and Pane identities. It also issues the
// relaunch activation generation in the same Registry transaction, so a late
// old-provider receipt is stale before tmux is allowed to respawn the child.
func (m Mutator) CASCodexHandoverTarget(reg *Registry, target CodexHandoverTarget, oldEndpoint, successor CodexEndpointRef, operationID string) (bool, error) {
	const op = "CAS Codex generation handover"
	target.AgentUID, target.PaneUID = strings.TrimSpace(target.AgentUID), strings.TrimSpace(target.PaneUID)
	target.PaneRuntimeID, target.PaneGeneration = strings.TrimSpace(target.PaneRuntimeID), strings.TrimSpace(target.PaneGeneration)
	target.RelaunchGeneration, target.ThreadID = strings.TrimSpace(target.RelaunchGeneration), strings.TrimSpace(target.ThreadID)
	if target.AgentUID == "" || target.PaneUID == "" || target.PaneRuntimeID == "" || target.PaneGeneration == "" ||
		target.RelaunchGeneration == "" || target.ThreadID == "" || !oldEndpoint.Valid() || !successor.Valid() ||
		oldEndpoint.StateDomainID != successor.StateDomainID || oldEndpoint.Same(successor) || strings.TrimSpace(operationID) == "" {
		return false, inputErr(op, ErrInvalidRegistry, "exact target, endpoints, and operation are required")
	}
	agent, agentOK := reg.Agent(target.AgentUID)
	pane, paneOK := reg.Pane(target.PaneUID)
	if !agentOK || !paneOK || agent.Status.Phase != PhaseRunning || agent.Status.PaneRef != target.PaneUID ||
		pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != KindAgent || pane.Metadata.OwnerRef.UID != target.AgentUID ||
		pane.Status.Activation.AgentUID != target.AgentUID || pane.Status.Activation.RuntimeID != target.PaneRuntimeID {
		return false, stateErr(op, ErrInvalidRegistry, "Agent/Pane identity is stale or ambiguous")
	}
	ref := agent.Status.SessionRef
	if ref == nil || ref.Provider != "codex" || ref.Codex == nil || ref.Codex.Endpoint == nil ||
		strings.TrimSpace(ref.Codex.ThreadID) != target.ThreadID || !ref.Codex.HasStartedTurn {
		return false, stateErr(op, ErrInvalidRegistry, "target is not a persisted exact Codex thread")
	}
	for i := range reg.Agents {
		other := &reg.Agents[i]
		if other.Metadata.UID == target.AgentUID || other.Status.SessionRef == nil || other.Status.SessionRef.Codex == nil ||
			other.Status.SessionRef.Codex.Endpoint == nil {
			continue
		}
		if strings.TrimSpace(other.Status.SessionRef.Codex.ThreadID) == target.ThreadID && other.Status.SessionRef.Codex.Endpoint.Same(successor) {
			return false, stateErr(op, ErrInvalidRegistry, "successor thread already has another Agent owner")
		}
	}
	if ref.Codex.Endpoint.Same(successor) && pane.Status.Activation.Generation == target.RelaunchGeneration &&
		pane.Status.Activation.OperationID == operationID && pane.Status.Activation.Codex != nil && pane.Status.Activation.Codex.ThreadID == target.ThreadID {
		return false, nil
	}
	if !ref.Codex.Endpoint.Same(oldEndpoint) || pane.Status.Activation.Generation != target.PaneGeneration ||
		pane.Status.Activation.Codex == nil || pane.Status.Activation.Codex.ThreadID != target.ThreadID {
		return false, stateErr(op, ErrInvalidRegistry, "handover compare-and-swap tuple changed")
	}
	endpoint := successor
	ref.Codex.Endpoint = &endpoint
	ref.Codex.Lifecycle = &CodexGenerationLifecycleRef{State: CodexGenerationCurrent}
	ref.Codex.HandoverResume = nil
	now := m.clock()().UTC()
	pane.Status.Activation = PaneActivation{
		Generation: target.RelaunchGeneration, RuntimeID: target.PaneRuntimeID, AgentUID: target.AgentUID,
		OperationID: strings.TrimSpace(operationID), StartedAt: now,
		Codex: &CodexActivationBinding{ThreadID: target.ThreadID},
	}
	pane.Status.LastTermination, pane.Status.Teardown = nil, nil
	agent.Status.LastTermination = nil
	reg.UpdatedAt = now
	return true, nil
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
