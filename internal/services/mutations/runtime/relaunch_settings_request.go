package runtime

import (
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// RelaunchPromptPlanner reads the prompt parts one relaunch passes: the agent
// guidance and the Project's label link rules. Each method may return the zero
// launch (a planner without that seam passes nothing), and each writes the
// content-addressed snapshot of what it read, as every launch plan does.
//
// GuidanceService answers all of them; the CLI answers the Claude parts
// through its resume launcher's optional seams.
type RelaunchPromptPlanner interface {
	PlanAgentGuidance(provider string, recorded map[string]string) AgentGuidanceLaunch
	PlanProjectLinks(provider string, project coremetadata.Project, recorded map[string]string) ProjectLinksLaunch
	PlanCodexAgentGuidance() AgentGuidanceLaunch
	PlanCodexProjectLinks(project coremetadata.Project) ProjectLinksLaunch
	PlanProcessAgentGuidance() AgentGuidanceLaunch
}

var _ RelaunchPromptPlanner = (*GuidanceService)(nil)

// RelaunchSettingsTarget is the Agent one relaunch resolves settings for: its
// provider (RelaunchAgentProvider), the host its relaunch launches on ("tmux"
// or "process"), its owning Project and its annotations.
type RelaunchSettingsTarget struct {
	Provider    string
	Host        string
	Project     coremetadata.Project
	Annotations map[string]string
}

// RelaunchSettingsRequest is q, what one `agent relaunch` asks of the target's
// layers, with the prompt parts that relaunch passes, the managed Codex base
// flag and the Project, plus the guidance and link rules launches it read. It
// is the one rule the CLI tmux relaunch, the CLI process relaunch and the SDK
// settings read share:
//
//   - a process launch passes the process agent guidance, and the Claude
//     label link rules or the Codex ones;
//   - a tmux Codex launch passes the Codex guidance and link rules;
//   - a tmux Claude launch passes the Claude guidance and link rules, except
//     for an Agent that records the reply-only activation, whose fixed launch
//     passes none.
//
// Nothing here admits the relaunch; the planner's snapshot writes are its only
// effect.
func RelaunchSettingsRequest(q AgentSettingsRequest, target RelaunchSettingsTarget, planner RelaunchPromptPlanner) (AgentSettingsRequest, AgentGuidanceLaunch, ProjectLinksLaunch) {
	var guidance AgentGuidanceLaunch
	var links ProjectLinksLaunch
	switch {
	case target.Host == "process":
		guidance = planner.PlanProcessAgentGuidance()
		guidance.Recorded = target.Annotations[coremetadata.AnnotationAgentGuidanceDigest]
		if target.Provider == aiModeCodex {
			links = planner.PlanCodexProjectLinks(target.Project)
		} else {
			links = planner.PlanProjectLinks(target.Provider, target.Project, target.Annotations)
		}
	case target.Provider == aiModeCodex:
		guidance, links = planner.PlanCodexAgentGuidance(), planner.PlanCodexProjectLinks(target.Project)
	case !coremetadata.RecordsDialogueReplyOnly(target.Annotations):
		guidance = planner.PlanAgentGuidance(target.Provider, target.Annotations)
		links = planner.PlanProjectLinks(target.Provider, target.Project, target.Annotations)
	}
	q = q.WithPromptParts(guidance, links)
	q.ManagedCodexBase = target.Provider == aiModeCodex
	q.ProjectUID = target.Project.Metadata.UID
	return q, guidance, links
}

// RelaunchAgentProvider is the provider a relaunch resolves agent's settings
// for: its declared provider, else the provider of its recorded conversation.
func RelaunchAgentProvider(agent coremetadata.Agent) string {
	provider := coremetadata.NormalizeProvider(agent.Spec.Provider)
	if provider == "" && agent.Status.SessionRef != nil {
		provider = coremetadata.NormalizeProvider(agent.Status.SessionRef.Provider)
	}
	return provider
}

// RelaunchCurrentHost is the host an Agent's plain relaunch launches on:
// "process" when exactly one of its Panes is a process Pane, else "tmux".
func RelaunchCurrentHost(reg coremetadata.Registry, agentUID string) string {
	if pane, ambiguous := ProcessAgentPane(reg, agentUID); pane != nil && !ambiguous {
		return "process"
	}
	return "tmux"
}
