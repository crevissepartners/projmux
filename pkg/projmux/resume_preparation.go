package projmux

import (
	"context"
	"errors"

	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/internal/services/resources"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// CodeResumePreparationStale identifies a missing, damaged or replaced proof.
const CodeResumePreparationStale Code = "process-resume-preparation-stale"

// PrepareAgentResume freezes the existing Claude plan without a provider,
// claim or lifetime. The returned proof is observed evidence, not authority;
// session authorization, mutation locks and acknowledgement storage remain
// the consumer's responsibility. Ordinary Resume is unchanged.
func (c *Client) PrepareAgentResume(ctx context.Context, req core.PrepareAgentResumeRequest) (core.PrepareAgentResumeResult, error) {
	var result core.PrepareAgentResumeResult
	wire, err := c.resumePreparationRequest(ctx, req.Ref, req.Project, req.Window, req.Binding)
	if err != nil {
		return result, err
	}
	wire.PreparationAction = "prepare"
	wire.AllowHostLost = req.AllowHostLost
	reply, err := mutationruntime.ExecuteProcessResumePreparation(ctx, c.processCreateContext, wire)
	if err != nil {
		return result, preparationRefusal(err, wire.Agent)
	}
	if err = unregisterCopy(reply.Preparation, &result.Preparation); err != nil {
		return result, err
	}
	return result, nil
}

// ValidateAgentResumePreparation only reads the frozen plan, source and files.
// It never reconciles a plan, acquires a claim or starts a provider. A successful
// read does not replace the independent owner's first-input proof fence.
func (c *Client) ValidateAgentResumePreparation(ctx context.Context, req core.ValidateAgentResumePreparationRequest) (core.AgentResumePreparation, error) {
	var result core.AgentResumePreparation
	wire, err := c.resumePreparationRequest(ctx, req.Ref, req.Project, req.Window, core.ProcessBinding{})
	if err != nil {
		return result, err
	}
	wire.PreparationAction = "validate"
	var proof mutationruntime.ProcessResumePreparation
	if err = unregisterCopy(req.Preparation, &proof); err != nil {
		return result, err
	}
	if proof.AgentUID != wire.Agent || proof.Binding != wire.Previous {
		return result, &Refusal{Code: CodeResumePreparationStale, AgentUID: wire.Agent, message: "process-resume-preparation-stale: source binding changed"}
	}
	wire.Preparation = &proof
	reply, err := mutationruntime.ExecuteProcessResumePreparation(ctx, c.processCreateContext, wire)
	if err != nil {
		return result, preparationRefusal(err, wire.Agent)
	}
	if err = unregisterCopy(reply.Preparation, &result); err != nil {
		return result, err
	}
	return result, nil
}

func (c *Client) resumePreparationRequest(ctx context.Context, ref, project, window string, binding core.ProcessBinding) (mutationruntime.ProcessDetachedResumeRequest, error) {
	var wire mutationruntime.ProcessDetachedResumeRequest
	if err := ctx.Err(); err != nil {
		return wire, err
	}
	if ref == "" {
		return wire, &Refusal{Code: CodeSelectorInvalid, message: "preparation requires one Agent reference"}
	}
	query, err := mutationQuery(core.KindAgent, ref, project, window, "PrepareAgentResumeRequest")
	if err != nil {
		return wire, resourceRefusal(err)
	}
	if c.pathsErr != nil {
		return wire, resourceRefusal(c.pathsErr)
	}
	reg, err := resources.Load(c.paths)
	if err != nil {
		return wire, resourceRefusal(err)
	}
	resolution, err := resources.Resolve(reg, m.RuntimeObservation{}, nil, m.KindAgent, query)
	if err != nil {
		return wire, resourceRefusal(err)
	}
	if err = resources.Enforce(selector.Target{Verb: selector.VerbResume, Kind: m.KindAgent}, query, resolution); err != nil {
		return wire, resourceRefusal(err)
	}
	agent, _ := reg.Agent(resolution.Matches[0].UID)
	pane, ambiguous := mutationruntime.ProcessAgentPane(reg, agent.Metadata.UID)
	if agent.Spec.Provider != "claude" || pane == nil || pane.Spec.Runtime.EffectiveKind() != m.RuntimeProcess {
		return wire, &Refusal{Code: CodeResumeRequestInvalid, message: "preparation requires process Claude"}
	}
	if ambiguous || pane.Status.ProcessSession == nil || pane.Metadata.OwnerUID() != agent.Metadata.UID || (agent.Status.PaneRef != "" && agent.Status.PaneRef != pane.Metadata.UID) {
		return wire, &Refusal{Code: CodeResumePlanInvalid, AgentUID: agent.Metadata.UID, message: "preparation requires an exact reciprocal Agent/Pane binding"}
	}
	var observed core.ProcessBinding
	if err = unregisterCopy(pane.Status.ProcessSession.Binding, &observed); err != nil {
		return wire, err
	}
	if binding != (core.ProcessBinding{}) && binding != observed {
		return wire, &Refusal{Code: CodeResumePlanInvalid, AgentUID: agent.Metadata.UID, message: "process resume binding changed"}
	}
	wire.Agent = agent.Metadata.UID
	wire.Previous = pane.Status.ProcessSession.Binding
	return wire, nil
}

func preparationRefusal(err error, agent string) error {
	var failure *mutationruntime.ProcessResumeFailure
	if errors.As(err, &failure) {
		detail := &ResumeProcessError{Code: Code(failure.Code), cause: err}
		_ = unregisterCopy(failure.Owner, &detail.Owner)
		b := failure.Binding
		_ = unregisterCopy(m.ProcessBinding{HostInstanceID: b.Host, ProjectUID: b.Project, WindowUID: b.Window, AgentUID: b.Agent, PaneUID: b.Pane, Generation: b.Generation, OperationID: b.Operation}, &detail.Binding)
		return &Refusal{Code: detail.Code, AgentUID: agent, message: err.Error(), cause: detail}
	}
	return resumeRefusal(err)
}
