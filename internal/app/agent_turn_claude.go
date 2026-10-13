package app

import (
	"context"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"io"
)

const claudeTurnInterruptAuditName = mutationruntime.ClaudeTurnInterruptAuditName

type claudeTurnInterruptAudit = mutationruntime.ClaudeTurnInterruptAudit

func exactClaudePane(ctx context.Context, runner tmuxCommandRunner, paneUID, runtime string, targets ...*processTerminalTarget) (string, error) {
	var guards []func() error
	for _, target := range targets {
		guards = append(guards, func() error { return target.admit(resourcegraph.ProcessKeys) })
	}
	return mutationruntime.ExactClaudePane(ctx, runner, paneUID, runtime, guards...)
}
func parseClaudePaneFrame(out []byte) ([]string, error) {
	return mutationruntime.ParseClaudePaneFrame(out)
}

// interruptClaudeTurn sends one Esc to a fresh in-progress Claude turn. via is
// the caller-reported client, already checked against the operatorclient rule,
// and the audit records it as received.
func (c *agentCommand) interruptClaudeTurn(registry coremetadata.Registry, agent coremetadata.Agent, via string, stdout io.Writer) error {
	if pane, ok := registry.Pane(agent.Status.PaneRef); ok && c.processRuntime.inventory().Declares(*pane) {
		operation, err := c.interruptProcessClaudeTurn(registry, agent)
		if err != nil {
			return err
		}
		return c.writeProcessTurn(stdout, agentActionInterruptTurn, agent, operation)
	}
	return c.interruptTmuxClaudeTurn(registry, agent, via, stdout)
}

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

func (c *agentCommand) interruptTmuxClaudeTurn(reg coremetadata.Registry, a coremetadata.Agent, via string, stdout io.Writer) error {
	r, err := c.claudeTurnInterrupter(context.Background(), reg, a, via)
	if err != nil {
		return err
	}
	return c.writeTurnResult(stdout, r, "interrupt")
}

// Explicit typed targets retain their local authority; operational commands use
// the exact host socket and kernel birth checks.
func (c *agentCommand) interruptProcessClaudeTurn(registry coremetadata.Registry, agent coremetadata.Agent) (string, error) {
	if c.processRuntime != nil && c.processRuntime.controlOverride != nil {
		turn := agent.Status.Progress.TurnRef
		return turn, c.processRuntime.controlOverride(context.Background(), registry, agent.Status.PaneRef, resourcegraph.ProcessInterrupt, turn, "")
	}
	return c.callProcessTurn(registry, agent, aiModeClaude, "interrupt", "")
}
