package runtime

import (
	"fmt"
	"slices"

	"github.com/crevissepartners/projmux/internal/services/mutations"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// requireResumablePhase enforces the Agent lifecycle gate of resume.
func RequireResumablePhase(spelling string, agent *coremetadata.Agent) error {
	if slices.Contains(ResumableAgentPhases, agent.Status.Phase) {
		return nil
	}
	if agent.Status.Phase == coremetadata.PhaseRunning {
		return createUsage(fmt.Sprintf(
			"%s: agent/%s is %s and already owns a managed Pane; resume only rebinds an %s or %s Agent. To look at it, run `projmux focus pane`",
			spelling, agent.Metadata.Name, agent.Status.Phase, coremetadata.PhaseOffline, coremetadata.PhaseFailed))
	}
	return createUsage(fmt.Sprintf("%s: agent/%s is %s; resume only rebinds an %s or %s Agent",
		spelling, agent.Metadata.Name, agent.Status.Phase, coremetadata.PhaseOffline, coremetadata.PhaseFailed))
}

func ValidateResumeAgent(spelling string, agent *coremetadata.Agent, model, effort string, dialogue bool, fields AgentInputFields) error {
	if err := RequireResumablePhase(spelling, agent); err != nil {
		return &mutations.Failure{Code: "resume-phase-not-resumable", Cause: err}
	}
	provider := coremetadata.NormalizeProvider(agent.Spec.Provider)
	if provider == "" && agent.Status.SessionRef != nil {
		provider = coremetadata.NormalizeProvider(agent.Status.SessionRef.Provider)
	}
	if fields.Prefix != "" && (model != "" || effort != "") && provider != "claude" && provider != "codex" {
		return &mutations.Failure{Code: "resume-request-invalid", Cause: createUsage(fmt.Sprintf("%s %s and %s require a Claude or Codex Agent; nothing was changed", spelling, fields.Option("model"), fields.Option("effort")))}
	}
	if err := fields.RequireLaunchOptions(spelling, provider, model, effort, dialogue, "nothing was changed"); err != nil {
		return &mutations.Failure{Code: "resume-request-invalid", Cause: err}
	}
	if err := fields.RequireDialogueMode(agent.Spec.Provider, dialogue, nil); err != nil {
		return &mutations.Failure{Code: "resume-request-invalid", Cause: err}
	}
	if (model != "" || effort != "") && coremetadata.RecordsDialogueReplyOnly(agent.Metadata.Annotations) {
		return createAgentUsage("reply-only-launch-fixed", fmt.Sprintf("%s: agent/%s records the reply-only activation (%s), whose fixed launch cannot carry a model, an effort, or a reset; create a new Agent to run with it (reply-only-launch-fixed); nothing was changed", spelling, agent.Metadata.Name, fields.Option("dialogue-reply-only")))
	}
	return nil
}
