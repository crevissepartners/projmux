package app

import (
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

type agentPaneNameHandoff struct{ name, sourceUID, reason string }

func selectAgentPaneNameHandoff(r coremetadata.Registry, a coremetadata.Agent, u []string) agentPaneNameHandoff {
	v := mutationruntime.SelectAgentPaneNameHandoff(r, a, u)
	return agentPaneNameHandoff{v.Name, v.SourceUID, v.Reason}
}
func agentPaneNameNotice(n, r string) string { return mutationruntime.AgentPaneNameNotice(n, r) }
func attachAgentPaneWithName(r *coremetadata.Registry, m coremetadata.Mutator, u, c, n, o string) (coremetadata.Pane, string, error) {
	return mutationruntime.AttachAgentPaneWithName(r, m, u, c, n, o)
}
