package app

import (
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

const claudeTurnInterruptAuditName = mutationruntime.ClaudeTurnInterruptAuditName

type claudeTurnInterruptAudit = mutationruntime.ClaudeTurnInterruptAudit

// declaredProcessTarget returns an exact process target when the invocation
// inventory declares the Agent's Pane, so the tmux transport guard admits or
// refuses each tmux command against that declaration. Otherwise it is nil.
func (c *agentCommand) declaredProcessTarget(registry coremetadata.Registry, agent coremetadata.Agent) *processTerminalTarget {
	for _, key := range c.processRuntime.inventory().Declared {
		if key.Pane == agent.Status.PaneRef {
			return &processTerminalTarget{runtime: c.processRuntime, registry: registry, paneUID: agent.Status.PaneRef}
		}
	}
	return nil
}
