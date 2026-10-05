package metadata

const nativeTransferPlannedReason = "native-transfer-planned"

// ReserveNativeAgentPane allocates a real native target before its provider
// initialization. It grants no runtime, process, or control authority. The
// Pending phase fences ordinary resume while a request-owned transfer owns it.
func (m Mutator) ReserveNativeAgentPane(reg *Registry, agentUID string, input BootstrapPane, operation string) (Pane, error) {
	const op = "reserve native agent pane"
	agent, ok := reg.Agent(agentUID)
	if !ok {
		return Pane{}, stateErr(op, ErrNotFound, "agent %q does not exist", agentUID)
	}
	if (agent.Status.Phase != PhaseOffline && agent.Status.Phase != PhaseFailed) || agent.Status.PaneRef != "" || agent.Spec.Provider != "codex" || agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil || agent.Status.SessionRef.Codex.ThreadID == "" {
		return Pane{}, stateErr(op, ErrInvalidPhase, "exact offline Codex conversation required")
	}
	// Attach supplies the existing UID/name/owner/anchor allocation rules. Only
	// this additive reservation finishes Pending rather than claiming a launch.
	pane, err := m.AttachAgentPane(reg, agentUID, input, operation)
	if err != nil {
		return Pane{}, err
	}
	agent, _ = reg.Agent(agentUID)
	agent.Status.Phase = PhasePending
	agent.Status.Reason = nativeTransferPlannedReason
	return pane, nil
}

// nativePaneReservation identifies an actual target that has not started yet.
// Runtime absence cannot be exit evidence for this exact reservation. Once a
// runtime or provider binding exists, ordinary termination projection applies.
func nativePaneReservation(reg *Registry, pane Pane) bool {
	agent, bound := boundAgentFor(reg, pane)
	activation := pane.Status.Activation
	return bound && pane.Spec.Role == PaneRoleAgent && pane.Spec.Runtime.EffectiveKind() == RuntimeTmux && agent.Status.Phase == PhasePending && agent.Status.Reason == nativeTransferPlannedReason &&
		agent.Spec.Provider == "codex" && agent.Status.SessionRef != nil && agent.Status.SessionRef.Codex != nil && agent.Status.SessionRef.Codex.ThreadID != "" &&
		activation.AgentUID == agent.Metadata.UID && activation.Generation != "" && activation.OperationID != "" && activation.RuntimeID == "" &&
		activation.Kind != RuntimeProcess && activation.Process == nil && activation.Codex == nil && activation.Claude == nil && pane.Status.ProcessSession == nil && pane.Status.LastTermination == nil && pane.Status.Teardown == nil
}
