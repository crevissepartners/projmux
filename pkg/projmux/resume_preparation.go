package projmux

import (
	"context"
	"errors"

	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
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
	result.SettingsApplication = publicSettingsApplication(reply.SettingsApplication)
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
	proof, err := acknowledgedProof(req.Preparation, wire)
	if err != nil {
		return result, err
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

// ValidateAgentResumePreparationFromPrepared judges the same read as
// ValidateAgentResumePreparation against a Registry snapshot the caller
// already holds. It starts no process and re-reads no Registry file; the
// deferred launch record, its files and the kernel liveness of the recorded
// host are still read. A nil, zero or foreign-root handle is a
// CodePreparedSnapshotInvalid refusal. Refusal codes match the other entry,
// except that a cancellation after the entry check returns the context error
// as it is, and ResumeProcessError.Owner is empty because no owner process
// exists. Registry, sidecar and kernel observations are not one atomic
// snapshot, and no operations.jsonl outcome is appended.
func (c *Client) ValidateAgentResumePreparationFromPrepared(ctx context.Context, prepared *PreparedRegistrySnapshot, req core.ValidateAgentResumePreparationRequest) (core.AgentResumePreparation, error) {
	var result core.AgentResumePreparation
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if c.pathsErr != nil {
		return result, resourceRefusal(c.pathsErr)
	}
	if !prepared.valid() {
		return result, &Refusal{Code: CodePreparedSnapshotInvalid, message: "prepared Registry snapshot was not prepared by PrepareRegistrySnapshot"}
	}
	if prepared.root != c.preparedRoot() {
		return result, &Refusal{Code: CodePreparedSnapshotInvalid, message: "prepared Registry snapshot belongs to a different state root"}
	}
	if req.Ref == "" {
		return result, &Refusal{Code: CodeSelectorInvalid, message: "preparation requires one Agent reference"}
	}
	query, err := mutationQuery(core.KindAgent, req.Ref, req.Project, req.Window, "PrepareAgentResumeRequest")
	if err != nil {
		return result, resourceRefusal(err)
	}
	wire, err := resumePreparationRequestFrom(prepared.registry, query, core.ProcessBinding{})
	if err != nil {
		return result, err
	}
	proof, err := acknowledgedProof(req.Preparation, wire)
	if err != nil {
		return result, err
	}
	read := mutationruntime.ProcessResumeCandidateRequest{
		Agent:         selector.Ref{Kind: m.KindAgent, UID: wire.Agent},
		AllowHostLost: true,
		RegistryPath:  c.preparedRoot(),
	}
	fail := func(err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(err, ctxErr) {
			return err
		}
		detail := &ResumeProcessError{Code: Code(mutationruntime.ProcessResumeErrorCode(err, nil)), cause: err}
		_ = unregisterCopy(wire.Previous, &detail.Binding)
		return &Refusal{Code: detail.Code, AgentUID: wire.Agent, message: err.Error(), cause: detail}
	}
	if wire.Previous.PaneUID == "" || wire.Previous.Generation == "" {
		return result, fail(errors.New("process-detached-resume-binding-invalid"))
	}
	candidate, err := mutationruntime.ResolveProcessResumeCandidate(prepared.registry, read)
	if err != nil {
		return result, fail(err)
	}
	if candidate.Record.Binding != wire.Previous {
		return result, fail(processhost.ErrStale)
	}
	// A supplied HostLost bit is evidence to compare, so host-lost candidates
	// are admitted whenever a proof is supplied.
	got, _, err := mutationruntime.ReadProcessResumePreparation(ctx, prepared.registry, read, &proof)
	if err != nil {
		return result, fail(err)
	}
	if err = unregisterCopy(got, &result); err != nil {
		return result, err
	}
	return result, nil
}

// acknowledgedProof copies the caller's proof and refuses one whose Agent or
// source binding is not the observed one.
func acknowledgedProof(preparation core.AgentResumePreparation, wire mutationruntime.ProcessDetachedResumeRequest) (mutationruntime.ProcessResumePreparation, error) {
	var proof mutationruntime.ProcessResumePreparation
	if err := unregisterCopy(preparation, &proof); err != nil {
		return proof, err
	}
	if proof.AgentUID != wire.Agent || proof.Binding != wire.Previous {
		return proof, &Refusal{Code: CodeResumePreparationStale, AgentUID: wire.Agent, message: "process-resume-preparation-stale: source binding changed"}
	}
	return proof, nil
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
	return resumePreparationRequestFrom(reg, query, binding)
}

// resumePreparationRequestFrom judges the request against a Registry the
// caller already holds; it reads no file.
func resumePreparationRequestFrom(reg m.Registry, query selector.Query, binding core.ProcessBinding) (mutationruntime.ProcessDetachedResumeRequest, error) {
	var wire mutationruntime.ProcessDetachedResumeRequest
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
