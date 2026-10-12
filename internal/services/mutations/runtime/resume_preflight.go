package runtime

// requireResumablePhase enforces the Agent lifecycle gate of resume.
func RequireResumablePhase(spelling string, agent *coremetadata.Agent) error {
	if slices.Contains(resumableAgentPhases, agent.Status.Phase) {
		return nil
	}
	if agent.Status.Phase == coremetadata.PhaseRunning {
		return usageError(fmt.Sprintf(
			"%s: agent/%s is %s and already owns a managed Pane; resume only rebinds an %s or %s Agent. To look at it, run `projmux focus pane`",
			spelling, agent.Metadata.Name, agent.Status.Phase, coremetadata.PhaseOffline, coremetadata.PhaseFailed))
	}
	return usageError(fmt.Sprintf("%s: agent/%s is %s; resume only rebinds an %s or %s Agent",
		spelling, agent.Metadata.Name, agent.Status.Phase, coremetadata.PhaseOffline, coremetadata.PhaseFailed))
}
