package app

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/crevissepartners/projmux/internal/integrations/agentcontrol"
	approvalservice "github.com/crevissepartners/projmux/internal/services/approvals"
	"io"
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// codexPermissionCapabilitySpelling is the capability cell `agent approval
// list|answer` ride on for a Codex Agent: they are the non-interactive face of
// approval.review and bind exactly as it does.
const codexPermissionCapabilitySpelling = "agent approval review"

// codexPermissionListNote is the caveat a Codex text list ends with.
const codexPermissionListNote = "note: a request may already have been answered in the Codex TUI or with `projmux agent approval review`; the first answer wins. " +
	"`agent approval answer` --allow sends accept (allow once); --deny sends decline (the turn continues) when offered, otherwise cancel (the turn stops); nothing runs either way. " +
	"review keeps every other decision\n"

// codexPermissionDecision is the one Codex decision an answer flag sends for a
// request offering offered, and false when it offers none that fits:
//
//   - --allow sends accept, and only accept. A widened grant (grant-turn, an
//     exec-policy amendment) is never sent from here.
//   - --deny sends decline when offered, so the turn continues; otherwise
//     cancel when offered, which denies and interrupts the turn. Real Codex
//     command approvals may offer accept and cancel with no decline, and
//     cancel is then the only way to deny. Nothing runs either way.
//
// The values ever sent are therefore accept, decline, and cancel.
func codexPermissionDecision(allow bool, offered []codexappserver.ApprovalDecision) (codexappserver.ApprovalDecision, bool) {
	return approvalservice.Decision(allow, offered)
}

// codexPermissionDecisionEffect is the short effect of a sent decision, as the
// answer line and the list print it.
func codexPermissionDecisionEffect(decision codexappserver.ApprovalDecision) string {
	return approvalservice.DecisionEffect(decision)
}

// codexPermissionAuditReason is how an answer's audit line carries the
// decision actually sent, in the existing Reason field: "decision=<value>".
func codexPermissionAuditReason(decision codexappserver.ApprovalDecision) string {
	return approvalservice.AuditReason(decision)
}

// codexPermissionApproval is runPermissionApproval for a Codex Agent. The
// requests are the pending app-server approvals the Agent's exact native
// control binding reports; projmux stores none of them, so there is no
// requested, expired, or closed audit line, only the allowed or denied line of
// an answer sent from here.
//
// answer runs in this order, and every refusal before step 4 writes nothing:
//  1. agent-approval-answering must be projmux, judged before any binding
//     lookup or broker call.
//  2. the request id must name exactly one pending approval.
//  3. codexPermissionDecision must find a decision it offers (accept for
//     --allow; decline, else cancel, for --deny).
//  4. the allowed or denied audit line is appended, with the decision to be
//     sent in Reason as "decision=<value>"; if that fails nothing is sent and
//     the request is still waiting.
//  5. exactly one approval-review call sends the decision. If it fails and
//     codexReviewNotDelivered confirms the decision never reached Codex, an
//     uncommitted line (send-failed) follows the answer line. Any other
//     failure leaves the line alone, since Codex may have taken the decision:
//     the log may over-report an answer, never under-report one.
//
// The answer line names the decision sent and, for a deny, whether the turn
// continues (decline) or stops (cancel).
func (c *agentCommand) codexPermissionApproval(request agentPermissionRequest, registry coremetadata.Registry, agent coremetadata.Agent, refuse func(string, string) error, stdout io.Writer) error {
	if codexProcessHookObservationOnly(c.lookupEnv) {
		return fmt.Errorf("%s: %w", request.spelling, processhost.ErrStale)
	}
	service := c.approvalService()
	if request.action == "list" {
		_, pending, err := service.Pending(context.Background(), registry, agent)
		if err != nil {
			return fmt.Errorf("%s: %w", request.spelling, err)
		}
		return c.listCodexPermissionRequests(request, agent, c.permissionAnswering(), pending, stdout)
	}
	result, err := service.AnswerCodex(context.Background(), approvalRequest(request), registry, agent)
	if err != nil {
		return err
	}
	return writePermissionServiceAnswer(request, result, stdout, io.Discard)
}

// codexReviewNotDelivered reports whether a failed approval-review call is
// confirmed not to have delivered the decision to Codex: callErr is the
// callControl error, and reviewed its response when callErr is nil.
//
// A transport error is confirmed only when no request reached the control
// server: the consumer fence refusal (*exactAgentControlBindingError from
// revalidateControlConsumerFence, returned before the transport is called),
// or a callCodexControl failure marked errAgentControlNotSent (socket path,
// dial, or request frame, all before the first byte is written). A failed
// write or read may have reached the server.
//
// A refusal is confirmed only for codes returned before the claim in
// codexControlEpoch.review (delete(e.pending, ...)) and so before
// RespondServerRequest: Handle's preamble refuses stale-epoch, stale-binding,
// and unavailable; the server refuses a malformed request frame as
// invalid-frame without calling Handle; review refuses ambiguous-request and
// unsafe-decision before the claim. response-indeterminate, timeout,
// response-too-large, and any unknown code may follow the send.
func codexReviewNotDelivered(callErr error, reviewed agentControlResponse) bool {
	return approvalservice.ReviewNotDelivered(callErr, agentcontrol.Response(reviewed))
}

func (c *agentCommand) listCodexPermissionRequests(request agentPermissionRequest, agent coremetadata.Agent, answering config.AgentApprovalAnswering, approvals []agentPendingApproval, stdout io.Writer) error {
	result, err := c.approvalService().ProjectCodex(agent, answering, approvals)
	if err != nil {
		return fmt.Errorf("%s: %w", request.spelling, err)
	}
	return c.writePermissionServiceList(request, result, stdout)
}

// codexPermissionDetails is a pending approval's kind-specific details as the
// list's toolInput: every field but the request id and kind, which the record
// already carries as id and toolName.
func codexPermissionDetails(p agentPendingApproval) (json.RawMessage, error) {
	return approvalservice.Details(p)
}

// codexPermissionSummary is the bounded audit input of a Codex answer: the
// command text of a command approval, and otherwise only the detail key names.
// The store bounds it again to agentapproval.MaxInputSummaryRunes.
func codexPermissionSummary(p agentPendingApproval) string {
	return approvalservice.Summary(p)
}

// codexPermissionAnswersText is the text list's answer column for one Codex
// request.
func codexPermissionAnswersText(view agentPermissionRecord) string {
	switch {
	case view.ambiguous:
		return "\tambiguous; answer with agent approval review"
	case len(view.Answers) == 0:
		return "\tanswers none; answer with agent approval review"
	default:
		answers := slices.Clone(view.Answers)
		if index := slices.Index(answers, "deny"); index >= 0 {
			answers[index] = "deny(" + view.DenyDecision + ": " + codexPermissionDecisionEffect(codexappserver.ApprovalDecision(view.DenyDecision)) + ")"
		}
		return "\tanswers " + strings.Join(answers, ",")
	}
}
