package metadata

import (
	"slices"
	"strings"
	"time"
)

// AgentInteractionFreshFor is the maximum age at which a durable observation
// may be presented as current after a process restart. Provider hooks refresh
// active states; an abandoned response must not leave a live badge forever.
const AgentInteractionFreshFor = 30 * time.Minute

// ValidAgentInteractionKind reports whether kind is in the public closed set.
func ValidAgentInteractionKind(kind AgentInteractionKind) bool {
	return slices.Contains(AgentInteractionKinds(), kind)
}

// ValidAgentInteractionSource reports whether source is safe durable metadata.
// Empty remains readable for registries written before sources were introduced,
// but every new mutation must choose one of the closed values.
func ValidAgentInteractionSource(source string) bool {
	return slices.Contains(AgentInteractionSources(), AgentInteractionSource(strings.TrimSpace(source)))
}

// EffectiveInteraction returns the current read model without erasing durable
// history. Non-running lifecycle, missing pane binding, and stale observations
// are all unknown and therefore cannot retain a response-complete badge.
func (a Agent) EffectiveInteraction(now time.Time) AgentInteraction {
	observed := a.Status.Interaction
	if a.Status.Phase != PhaseRunning || strings.TrimSpace(a.Status.PaneRef) == "" {
		return AgentInteraction{Kind: InteractionUnknown}
	}
	if !ValidAgentInteractionKind(observed.Kind) || observed.Kind == InteractionUnknown {
		return AgentInteraction{Kind: InteractionUnknown}
	}
	if observed.ObservedAt.IsZero() || (!now.IsZero() && now.Sub(observed.ObservedAt) > AgentInteractionFreshFor) {
		return AgentInteraction{Kind: InteractionUnknown}
	}
	return observed
}

// SetAgentInteraction stores one semantic observation on exactly one Agent.
// Transport vocabulary is normalized by callers before reaching this mutator.
func (m Mutator) SetAgentInteraction(reg *Registry, agentUID string, kind AgentInteractionKind, source string) (Agent, error) {
	const op = "set agent interaction"
	agent, ok := reg.Agent(agentUID)
	if !ok {
		return Agent{}, stateErr(op, ErrNotFound, "agent %q does not exist", agentUID)
	}
	if !ValidAgentInteractionKind(kind) {
		return Agent{}, inputErr(op, ErrInvalidPhase, "unsupported interaction kind %q", kind)
	}
	source = strings.TrimSpace(source)
	if !ValidAgentInteractionSource(source) {
		return Agent{}, inputErr(op, ErrInvalidPhase, "unsupported interaction source %q", source)
	}
	now := m.clock()().UTC()
	agent.Status.Interaction = AgentInteraction{Kind: kind, ObservedAt: now, Source: source}
	reg.UpdatedAt = now
	return agent.Clone(), nil
}

// SetAgentTopic mutates only the non-identifying topic annotation.
func (m Mutator) SetAgentTopic(reg *Registry, agentUID, topic string) (Agent, error) {
	const op = "set agent topic"
	agent, ok := reg.Agent(agentUID)
	if !ok {
		return Agent{}, stateErr(op, ErrNotFound, "agent %q does not exist", agentUID)
	}
	if agent.Metadata.Annotations == nil {
		agent.Metadata.Annotations = map[string]string{}
	}
	topic = strings.TrimSpace(topic)
	if topic == "" {
		delete(agent.Metadata.Annotations, AnnotationAgentTopic)
		if len(agent.Metadata.Annotations) == 0 {
			agent.Metadata.Annotations = nil
		}
	} else {
		agent.Metadata.Annotations[AnnotationAgentTopic] = topic
	}
	reg.UpdatedAt = m.clock()().UTC()
	return agent.Clone(), nil
}

// SetAgentActivation records bounded launch acknowledgement metadata.
func (m Mutator) SetAgentActivation(reg *Registry, agentUID string, state AgentActivationState, source, reason string) (Agent, error) {
	const op = "set agent activation"
	agent, ok := reg.Agent(agentUID)
	if !ok {
		return Agent{}, stateErr(op, ErrNotFound, "agent %q does not exist", agentUID)
	}
	switch state {
	case ActivationNotRequested, ActivationPending, ActivationAcknowledged, ActivationUnconfirmed:
	default:
		return Agent{}, inputErr(op, ErrInvalidPhase, "unsupported activation state %q", state)
	}
	source = strings.TrimSpace(source)
	reason = strings.TrimSpace(reason)
	if source != "" && source != string(InteractionSourceProviderHook) {
		return Agent{}, inputErr(op, ErrInvalidPhase, "unsupported activation source %q", source)
	}
	if !ValidAgentActivationReason(reason) {
		return Agent{}, inputErr(op, ErrInvalidPhase, "unsupported activation reason %q", reason)
	}
	// Activation is a refinement lattice, not a freely assignable status field.
	// A timeout may make pending evidence unconfirmed, and an exact provider hook
	// may later refine either pending or unconfirmed to acknowledged. Nothing may
	// move acknowledged backwards: in particular, the timeout writer racing a
	// provider hook must become a no-op instead of erasing stronger evidence.
	current := agent.Status.Activation.State
	if current == "" {
		current = ActivationNotRequested
	}
	if !validAgentActivationTransition(current, state) {
		return agent.Clone(), nil
	}
	now := m.clock()().UTC()
	agent.Status.Activation = AgentActivation{State: state, ObservedAt: now, Source: source, Reason: reason}
	reg.UpdatedAt = now
	return agent.Clone(), nil
}

func validAgentActivationTransition(from, to AgentActivationState) bool {
	if from == to {
		return true
	}
	switch from {
	case ActivationPending:
		return to == ActivationUnconfirmed || to == ActivationAcknowledged
	case ActivationUnconfirmed:
		return to == ActivationAcknowledged
	default:
		return false
	}
}
