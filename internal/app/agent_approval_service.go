package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agentcontrol"
	"github.com/crevissepartners/projmux/internal/services/approvals"
)

type approvalControlAdapter struct{ c *agentCommand }

func (a approvalControlAdapter) Bind(_ context.Context, reg coremetadata.Registry, agent coremetadata.Agent) (agentcontrol.Binding, error) {
	b, err := a.c.bindAgentControl("agent approval review", reg, agent)
	return serviceApprovalBinding(b), err
}
func serviceApprovalBinding(b exactAgentControlBinding) agentcontrol.Binding {
	return agentcontrol.Binding{Identity: agentcontrol.Identity(b.Identity), Endpoint: b.Endpoint, Epoch: b.Epoch, StateDir: b.StateDir, ProjectUID: b.ProjectUID, WindowUID: b.WindowUID, Fence: b.Fence}
}
func (a approvalControlAdapter) Call(_ context.Context, b agentcontrol.Binding, r agentcontrol.Request) (agentcontrol.Response, error) {
	old := exactAgentControlBinding{Identity: codexLifecycleIdentity(b.Identity), Endpoint: b.Endpoint, Epoch: b.Epoch, StateDir: b.StateDir, ProjectUID: b.ProjectUID, WindowUID: b.WindowUID, Fence: b.Fence}
	out, err := a.c.callControl(old, agentControlRequest{Operation: r.Operation, RequestKey: r.RequestKey, Decision: r.Decision})
	return agentcontrol.Response(out), err
}
func (c *agentCommand) approvalService() approvals.Service {
	return approvals.Service{Hooks: approvals.Hooks{
		Decision: codexPermissionDecision, AuditReason: codexPermissionAuditReason, Details: codexPermissionDetails, Summary: codexPermissionSummary,
		ReviewNotDelivered: func(err error, r agentcontrol.Response) bool {
			return codexReviewNotDelivered(err, agentControlResponse(r))
		},
		StoreRefusal: permissionStoreRefusal,
		AnswerError: func(r approvals.Request, err error) error {
			return permissionAnswerError(agentPermissionRequest{spelling: r.Spelling, requestID: r.RequestID, allow: r.Allow, via: r.Via}, err)
		},
	}, OpenStore: c.openApprovalStore, Answering: c.permissionAnswering, Control: approvalControlAdapter{c}}
}
func approvalRequest(r agentPermissionRequest) approvals.Request {
	return approvals.Request{Spelling: r.spelling, RequestID: r.requestID, Allow: r.allow, Via: r.via}
}
func (c *agentCommand) writePermissionServiceList(r agentPermissionRequest, result approvals.List, out io.Writer) error {
	if r.json {
		encoder := json.NewEncoder(out)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(result)
	}
	view := agentPermissionList{Provider: result.Provider, AgentUID: result.AgentUID, AgentName: result.AgentName, Answering: result.Answering, Requests: []agentPermissionRecord{}}
	for _, v := range result.Requests {
		view.Requests = append(view.Requests, agentPermissionRecord{ID: v.ID, State: v.State, ToolName: v.ToolName, AgentType: v.AgentType, ToolInput: v.ToolInput, CreatedAt: v.CreatedAt, Deadline: v.Deadline, Answers: v.Answers, DenyDecision: v.DenyDecision, Answerable: v.Answerable, ambiguous: v.Ambiguous})
	}
	return writeAgentPermissionList(out, view, c.clock())
}
func writePermissionServiceAnswer(r agentPermissionRequest, result approvals.AnswerResult, out, stderr io.Writer) error {
	suffix := ""
	if result.Decision != "" {
		detail := "decision " + result.Decision
		if result.Effect != "" {
			detail += "; " + result.Effect
		}
		suffix = " (" + detail + ")"
	}
	if _, err := fmt.Fprintf(out, "%s %s for agent/%s%s\n", result.RequestID, result.State, result.AgentName, suffix); err != nil {
		return err
	}
	if result.Warning != nil {
		fmt.Fprintf(stderr, "%s: warning: %v; permission request %q is %s and needs no second answer\n", r.spelling, result.Warning, result.RequestID, result.State)
	}
	return nil
}
