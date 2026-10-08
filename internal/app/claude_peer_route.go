package app

import (
	"context"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
)

// tmuxClaudeRouteResolver is a tmux Claude helper's resolver. Its own Agent is
// proved by its tmux registration alone; a peer may be process-hosted and
// shares the process helper's resolver.
func tmuxClaudeRouteResolver(registryPath, self string) func(coremetadata.Registry, string) (coremetadata.AgentRouteRef, string) {
	return func(reg coremetadata.Registry, agentUID string) (coremetadata.AgentRouteRef, string) {
		if agentUID == self {
			return coremetadata.ResolveAgentRoute(reg, agentUID)
		}
		return resolveClaudePeerRoute(registryPath, reg, agentUID)
	}
}

// resolveClaudePeerRoute proves another Agent's route at a Claude target
// helper, tmux or process alike. A tmux route is resolved first and exactly as
// before. A process-hosted Claude or Codex Agent is then proved only by the
// same live recheck its own sends use: the exact provider process chain and
// registration for Claude, the host validate for Codex. The Registry alone
// selects the Agent; no envelope or payload value chooses the authority.
func resolveClaudePeerRoute(registryPath string, reg coremetadata.Registry, agentUID string) (coremetadata.AgentRouteRef, string) {
	route, reason := coremetadata.ResolveAgentRoute(reg, agentUID)
	if reason == "" {
		return route, reason
	}
	agent, found := reg.Agent(agentUID)
	if !found || !processAgentAnswers(reg, *agent) {
		return route, reason
	}
	switch agent.Spec.Provider {
	case aiModeClaude:
		proof, ok := discoverProcessClaudeProof(registryPath, reg, agentUID)
		if !ok || proof.Binding.Agent != agentUID {
			return coremetadata.AgentRouteRef{}, "process Claude authority is unavailable"
		}
		return processClaudeRouteResolver(registryPath, proof)(reg, agentUID)
	case aiModeCodex:
		ctx, cancel := context.WithTimeout(context.Background(), localipc.Deadline)
		defer cancel()
		route, err := resolveLiveProcessCodexRoute(ctx, registryPath, reg, agentUID)
		if err != nil {
			return coremetadata.AgentRouteRef{}, "process Codex authority is unavailable"
		}
		return route, ""
	}
	return route, reason
}
