package metadata

import (
	"slices"
	"strings"
)

const (
	AgentProgressPlanCap  = 99
	AgentProgressFilesCap = 999
	AgentProgressItemsCap = 32
	AgentProgressSource   = "provider-control-plane"
)

func ValidAgentProgressActivity(activity AgentProgressActivity) bool {
	return activity == "" || slices.Contains(AgentProgressActivities(), activity)
}

func validateAgentProgress(op string, agent Agent) error {
	p := agent.Status.Progress
	if p.IsZero() {
		return nil
	}
	if agent.Status.Phase != PhaseRunning || strings.TrimSpace(agent.Status.PaneRef) == "" {
		return stateErr(op, ErrInvalidRegistry, "agent %q retains progress without a running Pane", agent.Metadata.Name)
	}
	if strings.TrimSpace(p.TurnRef) == "" || p.Source != AgentProgressSource || p.ObservedAt.IsZero() {
		return stateErr(op, ErrInvalidRegistry, "agent %q has incomplete progress authority", agent.Metadata.Name)
	}
	if !ValidAgentProgressActivity(p.Activity) || p.PlanCompleted > AgentProgressPlanCap ||
		p.PlanInProgress > AgentProgressPlanCap || p.PlanTotal > AgentProgressPlanCap ||
		p.PlanCompleted+p.PlanInProgress > p.PlanTotal || p.ChangedFiles > AgentProgressFilesCap ||
		p.ActiveItemCount > AgentProgressItemsCap {
		return stateErr(op, ErrInvalidRegistry, "agent %q has progress outside the hard budget", agent.Metadata.Name)
	}
	return nil
}

// SetAgentProgress commits one already-bounded exact-turn projection. The
// caller supplies the current turn from the same activation binding checked in
// its transaction; a mismatch is refused without turning progress into a
// second conversation authority.
func (m Mutator) SetAgentProgress(reg *Registry, agentUID, currentTurn string, progress AgentProgress) (Agent, bool, error) {
	const op = "set agent progress"
	agent, ok := reg.Agent(agentUID)
	if !ok {
		return Agent{}, false, stateErr(op, ErrNotFound, "agent %q does not exist", agentUID)
	}
	currentTurn = strings.TrimSpace(currentTurn)
	if progress.IsZero() {
		if agent.Status.Progress.IsZero() {
			return agent.Clone(), false, nil
		}
		agent.Status.Progress = AgentProgress{}
		reg.UpdatedAt = m.clock()().UTC()
		return agent.Clone(), true, nil
	}
	progress.TurnRef = strings.TrimSpace(progress.TurnRef)
	if currentTurn == "" || progress.TurnRef != currentTurn {
		return Agent{}, false, stateErr(op, ErrInvalidRegistry, "progress turn is not the current activation turn")
	}
	pane, paneOK := reg.Pane(agent.Status.PaneRef)
	if !paneOK || pane.Status.Activation.Codex == nil || strings.TrimSpace(pane.Status.Activation.Codex.TurnID) != currentTurn {
		return Agent{}, false, stateErr(op, ErrInvalidRegistry, "progress turn is not the current exact Pane activation binding")
	}
	if err := validateAgentProgress(op, Agent{Metadata: agent.Metadata, Status: AgentStatus{
		Phase: agent.Status.Phase, PaneRef: agent.Status.PaneRef, Progress: progress,
	}}); err != nil {
		return Agent{}, false, err
	}
	if agent.Status.Progress == progress {
		return agent.Clone(), false, nil
	}
	agent.Status.Progress = progress
	reg.UpdatedAt = m.clock()().UTC()
	return agent.Clone(), true, nil
}
