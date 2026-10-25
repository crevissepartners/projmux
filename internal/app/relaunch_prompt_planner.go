package app

import (
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

// relaunchPromptPlanner answers the prompt parts a relaunch passes
// (mutationruntime.RelaunchSettingsRequest): the Claude parts through the
// resume launcher's optional seams, which pass nothing when the launcher has
// none, and the Codex and process parts through guidance.
type relaunchPromptPlanner struct {
	launcher any
	guidance *mutationruntime.GuidanceService
	// processGuidance, when set, replaces guidance's process guidance: a move
	// from process to tmux passes the guidance of its tmux launch.
	processGuidance func() mutationruntime.AgentGuidanceLaunch
}

var _ mutationruntime.RelaunchPromptPlanner = relaunchPromptPlanner{}

func (p relaunchPromptPlanner) PlanAgentGuidance(provider string, recorded map[string]string) mutationruntime.AgentGuidanceLaunch {
	return planAgentGuidanceWith(p.launcher, provider, recorded).shared()
}

func (p relaunchPromptPlanner) PlanProjectLinks(provider string, project coremetadata.Project, recorded map[string]string) mutationruntime.ProjectLinksLaunch {
	return planProjectLinksWith(p.launcher, provider, project, recorded).shared()
}

func (p relaunchPromptPlanner) PlanCodexAgentGuidance() mutationruntime.AgentGuidanceLaunch {
	return p.guidance.PlanCodexAgentGuidance()
}

func (p relaunchPromptPlanner) PlanCodexProjectLinks(project coremetadata.Project) mutationruntime.ProjectLinksLaunch {
	return p.guidance.PlanCodexProjectLinks(project)
}

func (p relaunchPromptPlanner) PlanProcessAgentGuidance() mutationruntime.AgentGuidanceLaunch {
	if p.processGuidance != nil {
		return p.processGuidance()
	}
	return p.guidance.PlanProcessAgentGuidance()
}
