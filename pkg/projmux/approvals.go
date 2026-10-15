package projmux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agentcontrol"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentapproval"
	"github.com/crevissepartners/projmux/internal/services/approvals"
	"github.com/crevissepartners/projmux/internal/services/questions"
	"github.com/crevissepartners/projmux/internal/services/resources"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

const (
	CodePermissionNotFound            Code = "permission-not-found"
	CodePermissionNotPending          Code = "permission-not-pending"
	CodePermissionExpired             Code = "permission-expired"
	CodePermissionAnsweringOff        Code = "permission-answering-off"
	CodePermissionProviderUnsupported Code = "permission-provider-unsupported"
	CodePermissionDecisionUnavailable Code = "permission-decision-unavailable"
	CodeOperatorClientInvalid         Code = "operator-client-invalid"
	CodePermissionControlUnavailable  Code = "permission-control-unavailable"
	// Native approval transport refusals preserve fencing and input failure kinds.
	CodeControlStaleEpoch       Code = "stale-epoch"
	CodeControlStaleBinding     Code = "stale-binding"
	CodeControlUnavailable      Code = "unavailable"
	CodeControlInvalidFrame     Code = "invalid-frame"
	CodeControlAmbiguousRequest Code = "ambiguous-request"
	CodeControlUnsafeDecision   Code = "unsafe-decision"
)

// ApprovalControl provides an isolated exact Agent control seam. It must reject
// stale identities and honor context. Nil uses the existing fenced native transport.
// Results contain display projections only; raw provider request identities stay private.
type ApprovalControl interface {
	ListApprovals(context.Context, string) ([]core.PendingApproval, error)
	ReviewApproval(context.Context, string, string, core.OfferedDecision) error
}
type AnswerApprovalRequest struct {
	Agent, RequestID string
	Allow            bool
	Via              string
}
type ReviewApprovalRequest struct {
	Agent, RequestID string
	Decision         core.OfferedDecision
}

func (c *Client) approvalService() approvals.Service {
	var control approvals.Control = agentcontrol.Client{StateDir: c.paths.StateDir, Transport: c.transport, LoadRegistry: func() (metadata.Registry, error) { return resources.Load(c.paths) }}
	if c.approvalControl != nil {
		control = publicApprovalControl{c.approvalControl}
	}
	return approvals.Service{OpenStore: func() (*agentapproval.Store, error) { return agentapproval.NewStore(c.paths.StateDir), nil }, Answering: func() config.AgentApprovalAnswering {
		v, _ := config.LoadAgentApprovalAnsweringFile(c.paths.AgentApprovalAnsweringFile())
		return v
	}, Control: control}
}
func (c *Client) approvalAgent(ctx context.Context, ref, spelling, field string) (metadata.Registry, metadata.Agent, error) {
	if err := ctx.Err(); err != nil {
		return metadata.Registry{}, metadata.Agent{}, err
	}
	if c.pathsErr != nil {
		return metadata.Registry{}, metadata.Agent{}, resourceRefusal(c.pathsErr)
	}
	reg, err := resources.Load(c.paths)
	if err != nil {
		return reg, metadata.Agent{}, resourceRefusal(err)
	}
	agent, err := questions.ResolveAgent(reg, ref)
	if err != nil {
		return reg, agent, resourceRefusal(selectorInputError(err, field))
	}
	if spelling == "agent approval review" {
		if err := agentcontrol.RequireCapability(spelling, agent); err != nil {
			return reg, agent, &Refusal{Code: CodePermissionProviderUnsupported, message: err.Error()}
		}
		return reg, agent, nil
	}
	return reg, agent, approvalRefusal(approvals.ValidateProvider(spelling, agent))
}
func processApproval(reg metadata.Registry, agent metadata.Agent) bool {
	pane, ok := reg.Pane(agent.Status.PaneRef)
	return (agent.Spec.Provider == "claude" || agent.Spec.Provider == "codex") && ok && pane.Metadata.OwnerUID() == agent.Metadata.UID && pane.Spec.Runtime.EffectiveKind() == metadata.RuntimeProcess
}

// ListApprovals reads one explicit Agent's waiting requests. It never starts a provider.
func (c *Client) ListApprovals(ctx context.Context, agentRef string) (core.ApprovalList, error) {
	reg, agent, err := c.approvalAgent(ctx, agentRef, "agent approval list", "ListApprovals agentRef")
	if err != nil {
		return core.ApprovalList{}, err
	}
	service := c.approvalService()
	var result approvals.List
	if metadata.NormalizeProvider(agent.Spec.Provider) == "codex" && !processApproval(reg, agent) {
		_, pending, callErr := service.Pending(ctx, reg, agent)
		err = callErr
		if err != nil {
			err = fmt.Errorf("agent approval list: %w", err)
		}
		if err == nil {
			result, err = service.ProjectCodex(agent, service.Answering(), pending)
		}
	} else {
		result, err = service.ListStore(ctx, agent)
	}
	if err != nil {
		return core.ApprovalList{}, approvalRefusal(err)
	}
	var out core.ApprovalList
	b, err := json.Marshal(result)
	if err == nil {
		err = json.Unmarshal(b, &out)
	}
	return out, err
}

// AnswerApproval allows or denies one request once, with the CLI's channel default
// and validation. Audit precedes native transmission. Delivery is never retried.
func (c *Client) AnswerApproval(ctx context.Context, input AnswerApprovalRequest) (core.ApprovalReceipt, error) {
	via := input.Via
	if via == "" {
		via = agentapproval.ViaCLI
	}
	if err := approvals.ValidateVia("agent approval answer", via); err != nil {
		return core.ApprovalReceipt{}, approvalRefusal(err)
	}
	reg, agent, err := c.approvalAgent(ctx, input.Agent, "agent approval answer", "AnswerApprovalRequest.Agent")
	if err != nil {
		return core.ApprovalReceipt{}, err
	}
	service := c.approvalService()
	r := approvals.Request{Spelling: "agent approval answer", RequestID: strings.TrimSpace(input.RequestID), Allow: input.Allow, Via: via}
	var result approvals.AnswerResult
	if metadata.NormalizeProvider(agent.Spec.Provider) == "codex" && !processApproval(reg, agent) {
		result, err = service.AnswerCodex(ctx, r, reg, agent)
	} else {
		if !processApproval(reg, agent) && service.Answering() != config.AgentApprovalAnsweringProjmux {
			return core.ApprovalReceipt{}, approvalRefusal(approvals.Refuse(r.Spelling, agent, approvals.ReasonAnsweringOff, "has no captured permission requests while agent-approval-answering is claude; run `projmux config agent-approvals --answering projmux` first"))
		}
		result, err = service.AnswerStore(ctx, r, agent)
	}
	out := core.ApprovalReceipt{RequestID: result.RequestID, State: result.State, AgentUID: result.AgentUID, AgentName: result.AgentName, Decision: core.OfferedDecision(result.Decision), Effect: result.Effect}
	if result.Warning != nil {
		out.Warning = result.Warning.Error()
	}
	return out, approvalRefusal(err)
}

// ReviewApproval replaces the CLI picker with an explicit offered decision.
// It follows native exact review, including its independence from answering policy
// and lack of answer audit lines. It never focuses a window or widens offered choices.
func (c *Client) ReviewApproval(ctx context.Context, input ReviewApprovalRequest) (core.ApprovalReceipt, error) {
	reg, agent, err := c.approvalAgent(ctx, input.Agent, "agent approval review", "ReviewApprovalRequest.Agent")
	if err != nil {
		return core.ApprovalReceipt{}, err
	}
	service := c.approvalService()
	b, pending, err := service.Pending(ctx, reg, agent)
	if err != nil {
		return core.ApprovalReceipt{}, approvalRefusal(err)
	}
	matches := slices.DeleteFunc(slices.Clone(pending), func(v agentcontrol.PendingApproval) bool { return v.RequestID != input.RequestID })
	if len(matches) != 1 {
		message := "no exact unresolved approval request matches; response write refused"
		if len(matches) > 1 {
			message = "normalized request id is ambiguous across raw scalar identities; response write refused"
		}
		return core.ApprovalReceipt{}, &Refusal{Code: CodePermissionNotPending, message: approvals.Recovery(errors.New(message), b).Error()}
	}
	err = service.Review(ctx, b, matches[0], string(input.Decision))
	if err != nil {
		return core.ApprovalReceipt{}, approvalRefusal(err)
	}
	return core.ApprovalReceipt{RequestID: input.RequestID, AgentUID: agent.Metadata.UID, AgentName: agent.Metadata.Name, Decision: input.Decision}, nil
}
func approvalRefusal(err error) error {
	var failure *approvals.Failure
	if errors.As(err, &failure) {
		message := err.Error()
		switch Code(failure.Code) {
		case CodeOperatorClientInvalid:
			if detail, ok := strings.CutPrefix(message, "agent approval answer --via: "); ok {
				message = "AnswerApprovalRequest.Via: " + detail
			}
		}
		return &Refusal{Code: Code(failure.Code), message: message}
	}
	var control *agentcontrol.Refusal
	if errors.As(err, &control) {
		code := Code(control.Code)
		switch code {
		case CodeControlStaleEpoch, CodeControlStaleBinding, CodeControlUnavailable, CodeControlInvalidFrame, CodeControlAmbiguousRequest, CodeControlUnsafeDecision:
		default:
			code = CodePermissionControlUnavailable
		}
		return &Refusal{Code: code, message: err.Error()}
	}
	var binding *agentcontrol.BindingError
	if errors.As(err, &binding) {
		return &Refusal{Code: CodePermissionControlUnavailable, message: err.Error()}
	}
	return err
}

type publicApprovalControl struct{ ApprovalControl }

func (p publicApprovalControl) Bind(ctx context.Context, reg metadata.Registry, a metadata.Agent) (agentcontrol.Binding, error) {
	if err := ctx.Err(); err != nil {
		return agentcontrol.Binding{}, err
	}
	b := agentcontrol.Binding{Identity: agentcontrol.Identity{AgentUID: a.Metadata.UID, PaneUID: a.Status.PaneRef}}
	if window, ok := reg.Window(a.Metadata.OwnerUID()); ok {
		b.WindowUID = window.Metadata.UID
		if project, ok := reg.Project(window.Metadata.OwnerUID()); ok {
			b.ProjectUID = project.Metadata.UID
		}
	}
	return b, nil
}
func (p publicApprovalControl) Call(ctx context.Context, b agentcontrol.Binding, r agentcontrol.Request) (agentcontrol.Response, error) {
	switch r.Operation {
	case "approval-list":
		v, err := p.ListApprovals(ctx, b.Identity.AgentUID)
		if err != nil {
			return agentcontrol.Response{}, err
		}
		var out []agentcontrol.PendingApproval
		data, err := json.Marshal(v)
		if err == nil {
			err = json.Unmarshal(data, &out)
		}
		return agentcontrol.Response{OK: err == nil, Approvals: out}, err
	case "approval-review":
		err := p.ReviewApproval(ctx, b.Identity.AgentUID, r.RequestKey, core.OfferedDecision(r.Decision))
		return agentcontrol.Response{OK: err == nil}, err
	}
	return agentcontrol.Response{}, fmt.Errorf("unsupported approval operation %q", r.Operation)
}
