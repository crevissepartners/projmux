package app

import (
	"errors"
	"github.com/crevissepartners/projmux/internal/config"
	m "github.com/crevissepartners/projmux/internal/core/metadata"
	receipt "github.com/crevissepartners/projmux/internal/core/receipt"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

func (c *agentCommand) bindProcessResumeContext(creator *createCommand, frozen mutationruntime.ProcessCreateContext) {
	c.store = creator.store
	c.loadRegistry = creator.store.snapshot
	c.lookupEnv = creator.lookupEnv
	c.messagePaths = defaultAgentMessagePaths(frozen.Paths)
	c.messageStore = newAgentMessageStore(frozen.Paths.StateDir)
	c.messageRoute = liveAgentMessageRouteResolver{registryPath: c.messagePaths.registryPath}
	c.controlPaths = func() (config.Paths, error) { return frozen.Paths, nil }
}

func validateSDKProcessResumeWire(wire processDetachedResumeRequest) error {
	if wire.Agent == "" || wire.Previous.AgentUID != wire.Agent || wire.Previous.PaneUID == "" || wire.Previous.Generation == "" {
		return errors.New("process-detached-resume-binding-invalid")
	}
	if wire.PreparationAction != "" {
		if wire.PreparationAction != "prepare" && wire.PreparationAction != "validate" || wire.WaitForPeer || wire.Prompt != (processResumeFirstFrame{}) || wire.Model != "" || wire.Effort != "" {
			return usageError("agent resume: invalid preparation request")
		}
	} else if wire.Preparation != nil {
		p := wire.Preparation
		if wire.WaitForPeer || wire.Prompt.Kind != "user" || wire.Model != "" || wire.Effort != "" {
			return usageError("agent resume: preparation requires an explicit Claude first input")
		}
		if p.AgentUID != wire.Agent || p.Binding != wire.Previous {
			return preparationStale("request binding differs")
		}
	}
	_, err := newProcessAgentResumeRequest(wire.options())
	if err != nil {
		return err
	}
	if wire.WaitForPeer && wire.Prompt.Kind != "" {
		return usageError("agent resume: --wait-for-peer cannot be combined with a prompt or --dialogue-reply-only")
	}
	return nil
}

func processResumeCommitted(c *agentCommand, binding m.ProcessBinding, notices []string) (*mutationruntime.ProcessCreateCommitted, error) {
	reg, err := c.loadRegistry()
	if err != nil {
		return nil, err
	}
	agent, ok := reg.Agent(binding.AgentUID)
	if !ok {
		return nil, errors.New("process-detached-agent-missing")
	}
	pane, ok := reg.Pane(binding.PaneUID)
	if !ok {
		return nil, errors.New("process-detached-pane-missing")
	}
	return &mutationruntime.ProcessCreateCommitted{Agent: agent.Clone(), Pane: pane.Clone(), Receipt: receipt.OperationReceipt(processResumeReceipt(processSchemaBinding(binding), agent.Metadata.Name)), Notices: notices}, nil
}
