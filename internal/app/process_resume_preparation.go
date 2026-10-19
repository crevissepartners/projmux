package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

func preparationStale(detail string) error {
	return fmt.Errorf("%w: %s", mutationruntime.ErrResumePreparationStale, detail)
}

func preparedResumeProjection(candidate processResumeCandidate, record *deferredLaunchRecord) mutationruntime.ProcessResumePreparation {
	p := processPreparedProjection(record)
	return mutationruntime.ProcessResumePreparation{AgentUID: p.AgentUID, PaneUID: p.PaneUID, Conversation: p.Conversation, LaunchDigest: p.LaunchDigest, Binding: candidate.Record.Binding, HostLost: candidate.HostLost != nil}
}

// readResumePreparation deliberately does not call prepareDeferredLaunch:
// reconciliation can write a sidecar even when the caller only wants to read.
func (c *agentCommand) readResumePreparation(ctx context.Context, opts processAgentResumeOptions, expected *mutationruntime.ProcessResumePreparation) (mutationruntime.ProcessResumePreparation, *deferredLaunchRecord, error) {
	var zero mutationruntime.ProcessResumePreparation
	if err := ctx.Err(); err != nil {
		return zero, nil, err
	}
	// A supplied HostLost bit is evidence to compare, not admission authority.
	// Read the current candidate without reconciling, then compare the whole proof.
	if expected != nil {
		opts.allowHostLost = true
	}
	request, err := newProcessAgentResumeRequest(opts)
	if err != nil {
		return zero, nil, err
	}
	candidate, err := c.processResumeCandidate(request)
	if err != nil {
		return zero, nil, err
	}
	if candidate.Record.Provider != aiModeClaude {
		return zero, nil, usageError("agent resume: preparation requires process Claude")
	}
	record, err := c.readDeferredLaunch(candidate.Agent.Metadata.UID)
	if err != nil || record == nil {
		return zero, nil, preparationStale("prepared launch missing or damaged")
	}
	if !record.matches(candidate) {
		return zero, nil, preparationStale("prepared source changed")
	}
	if err = record.validatePermissionMode(); err != nil {
		return zero, nil, preparationStale("prepared permission changed")
	}
	if err = record.validateFiles(); err != nil {
		return zero, nil, preparationStale("prepared files changed")
	}
	proof := preparedResumeProjection(candidate, record)
	if expected != nil && proof != *expected {
		return zero, nil, preparationStale("acknowledged preparation changed")
	}
	if err = ctx.Err(); err != nil {
		return zero, nil, err
	}
	return proof, record, nil
}

func preparationOptions(p mutationruntime.ProcessResumePreparation) processAgentResumeOptions {
	return processAgentResumeOptions{Agent: selector.Ref{Kind: m.KindAgent, UID: p.AgentUID}, allowHostLost: p.HostLost}
}

func (c *agentCommand) checkResumePreparation(ctx context.Context, p *mutationruntime.ProcessResumePreparation) error {
	if p == nil {
		return nil
	}
	_, _, err := c.readResumePreparation(ctx, preparationOptions(*p), p)
	return err
}

// A failure never prepares a new plan or replays input. The existing engine
// must already have retained/readdressed this exact recipe after actual Wait.
func (c *agentCommand) failedResumePreparation(p *mutationruntime.ProcessResumePreparation, before *deferredLaunchRecord, result processAgentResumeResult) *mutationruntime.ProcessResumePreparation {
	if p == nil || before == nil {
		return nil
	}
	if proof, _, err := c.readResumePreparation(context.Background(), preparationOptions(*p), p); err == nil && result.hasNoChild() {
		return &proof
	}
	proof, record, err := c.readResumePreparation(context.Background(), processAgentResumeOptions{Agent: selector.Ref{Kind: m.KindAgent, UID: p.AgentUID}}, nil)
	if err != nil || proof.Binding != metadataProcessBinding(result.Binding) || proof.Binding == p.Binding || proof.HostLost || proof.Conversation != p.Conversation || !reflect.DeepEqual(before.Command, record.Command) || !reflect.DeepEqual(before.NewSpec, record.NewSpec) || !reflect.DeepEqual(before.NewAnnotations, record.NewAnnotations) {
		return nil
	}
	reg, err := c.loadRegistry()
	if err != nil {
		return nil
	}
	agent, ok := reg.Agent(p.AgentUID)
	pane, ambiguous := processResumePane(reg, p.AgentUID)
	if !ok || ambiguous || pane == nil || pane.Status.ProcessSession == nil || pane.Status.ProcessSession.History == nil || pane.Status.ProcessSession.History.Binding != p.Binding || !m.MatchesProcessWait(proof.Binding, pane.Status.LastTermination) || !m.SameProcessWait(pane.Status.LastTermination, agent.Status.LastTermination) {
		return nil
	}
	return &proof
}

func runResumePreparation(ctx context.Context, c *agentCommand, wire processDetachedResumeRequest) (mutationruntime.ProcessResumePreparation, error) {
	var zero mutationruntime.ProcessResumePreparation
	opts := wire.options()
	opts.Prompt = processResumeFirstFrame{}
	opts.preparation = nil
	opts.allowHostLost = wire.AllowHostLost
	if wire.PreparationAction == "validate" {
		if wire.Preparation == nil {
			return zero, preparationStale("preparation required")
		}
		opts.allowHostLost = wire.Preparation.HostLost
		proof, _, err := c.readResumePreparation(ctx, opts, wire.Preparation)
		return proof, err
	}
	if wire.PreparationAction != "prepare" {
		return zero, usageError("agent resume: invalid preparation action")
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	result, err := c.prepareOwnedClaudeResume(ctx, opts)
	if err != nil {
		return zero, err
	}
	if result.Prepared == nil || result.Owned != nil || result.State != agentProcessPrepared {
		return zero, errors.New("process-resume-not-prepared")
	}
	proof, _, err := c.readResumePreparation(ctx, opts, nil)
	return proof, err
}
