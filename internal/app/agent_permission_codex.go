package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentapproval"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
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
	candidates := []codexappserver.ApprovalDecision{codexappserver.DecisionDecline, codexappserver.DecisionCancel}
	if allow {
		candidates = []codexappserver.ApprovalDecision{codexappserver.DecisionAccept}
	}
	for _, decision := range candidates {
		if slices.Contains(offered, decision) {
			return decision, true
		}
	}
	return "", false
}

// codexPermissionDecisionEffect is the short effect of a sent decision, as the
// answer line and the list print it.
func codexPermissionDecisionEffect(decision codexappserver.ApprovalDecision) string {
	switch decision {
	case codexappserver.DecisionDecline:
		return "the turn continues"
	case codexappserver.DecisionCancel:
		return "the turn stops"
	default:
		return ""
	}
}

// codexPermissionAuditReason is how an answer's audit line carries the
// decision actually sent, in the existing Reason field: "decision=<value>".
func codexPermissionAuditReason(decision codexappserver.ApprovalDecision) string {
	return "decision=" + string(decision)
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
	answering := c.permissionAnswering()
	if request.action == "answer" && answering != config.AgentApprovalAnsweringProjmux {
		return refuse(permissionReasonAnsweringOff, "cannot be answered from projmux while agent-approval-answering is claude; run `projmux config agent-approvals --answering projmux` first, or answer in the Codex TUI or with `projmux agent approval review`")
	}
	binding, err := c.bindAgentControl(codexPermissionCapabilitySpelling, registry, agent)
	if err != nil {
		return fmt.Errorf("%s: %w", request.spelling, err)
	}
	response, err := c.callControl(binding, agentControlRequest{Operation: agentControlOpApprovals})
	if err != nil {
		return fmt.Errorf("%s: %w", request.spelling, err)
	}
	if err := response.Error(); err != nil {
		return fmt.Errorf("%s: %w", request.spelling, addOpenCodexBindingRecovery(err, binding))
	}
	if request.action == "list" {
		return c.listCodexPermissionRequests(request, agent, answering, response.Approvals, stdout)
	}

	matches := slices.DeleteFunc(slices.Clone(response.Approvals), func(p agentPendingApproval) bool { return p.RequestID != request.requestID })
	switch len(matches) {
	case 0:
		return refuse(permissionReasonNotPending, fmt.Sprintf("has no pending approval request %q; it was already answered (the first answer wins) or never existed", request.requestID))
	case 1:
	default:
		return refuse(permissionReasonNotPending, fmt.Sprintf("approval request id %q is ambiguous across raw request ids; answer it with `projmux agent approval review %s`", request.requestID, agent.Metadata.Name))
	}
	pending := matches[0]
	decision, ok := codexPermissionDecision(request.allow, pending.Decisions)
	if !ok {
		want := "accept"
		if !request.allow {
			want = "decline or cancel"
		}
		return refuse(permissionReasonDecisionUnavailable, fmt.Sprintf("approval request %q (%s) does not offer %s; answer it with `projmux agent approval review %s`", request.requestID, pending.Kind, want, agent.Metadata.Name))
	}

	event, word := agentapproval.AuditDenied, agentapproval.StateDenied
	if request.allow {
		event, word = agentapproval.AuditAllowed, agentapproval.StateAllowed
	}
	store, err := c.openApprovalStore()
	if err != nil {
		return fmt.Errorf("%s: %w; approval request %q is still waiting", request.spelling, err, request.requestID)
	}
	line := agentapproval.AuditLine{
		Event: event, RequestID: pending.RequestID, AgentUID: agent.Metadata.UID, PaneUID: binding.Identity.PaneUID,
		ToolName: string(pending.Kind), Input: codexPermissionSummary(pending), Via: request.via, Reason: codexPermissionAuditReason(decision),
	}
	if err := store.AppendAnswerAudit(line); err != nil {
		return fmt.Errorf("%s: %w; approval request %q is still waiting", request.spelling, err, request.requestID)
	}
	// The line is on disk. A failed send is compensated with an uncommitted
	// line only when the decision is confirmed not to have reached Codex;
	// otherwise the provider may or may not have taken it, and the line stays
	// alone, erring on the side of recording an answer that did not land.
	reviewed, callErr := c.callControl(binding, agentControlRequest{Operation: agentControlOpReview, RequestKey: pending.RequestID, Decision: string(decision)})
	err = callErr
	if err == nil {
		if refusal := reviewed.Error(); refusal != nil {
			err = addOpenCodexBindingRecovery(refusal, binding)
		}
	}
	if err != nil {
		if !codexReviewNotDelivered(callErr, reviewed) {
			return fmt.Errorf("%s: %w; the decision may or may not have reached Codex", request.spelling, err)
		}
		if auditErr := store.AppendUncommittedAudit(line, agentapproval.UncommittedSendFailed); auditErr != nil {
			return fmt.Errorf("%s: %w; the decision did not reach Codex, so the answer did not take effect, but the audit log keeps its %s line: %w", request.spelling, err, event, auditErr)
		}
		return fmt.Errorf("%s: %w; the decision did not reach Codex, so the answer did not take effect; its %s audit line is followed by an uncommitted line", request.spelling, err, event)
	}
	detail := "decision " + string(decision)
	if effect := codexPermissionDecisionEffect(decision); effect != "" {
		detail += "; " + effect
	}
	_, err = fmt.Fprintf(stdout, "%s %s for agent/%s (%s)\n", pending.RequestID, word, agent.Metadata.Name, detail)
	return err
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
	if callErr != nil {
		var fence *exactAgentControlBindingError
		return errors.As(callErr, &fence) || errors.Is(callErr, errAgentControlNotSent)
	}
	if reviewed.OK {
		return false
	}
	switch reviewed.Code {
	case "stale-epoch", "stale-binding", "unavailable", "invalid-frame", "ambiguous-request", "unsafe-decision":
		return true
	default:
		return false
	}
}

func (c *agentCommand) listCodexPermissionRequests(request agentPermissionRequest, agent coremetadata.Agent, answering config.AgentApprovalAnswering, approvals []agentPendingApproval, stdout io.Writer) error {
	counts := map[string]int{}
	for _, p := range approvals {
		counts[p.RequestID]++
	}
	result := agentPermissionList{Provider: aiModeCodex, AgentUID: agent.Metadata.UID, AgentName: agent.Metadata.Name, Answering: string(answering), Requests: []agentPermissionRecord{}}
	for _, p := range approvals {
		details, err := codexPermissionDetails(p)
		if err != nil {
			return fmt.Errorf("%s: %w", request.spelling, err)
		}
		record := agentPermissionRecord{ID: p.RequestID, State: agentapproval.StateWaiting, ToolName: string(p.Kind), ToolInput: details, ambiguous: counts[p.RequestID] > 1}
		if !record.ambiguous {
			if _, ok := codexPermissionDecision(true, p.Decisions); ok {
				record.Answers = append(record.Answers, "allow")
			}
			if decision, ok := codexPermissionDecision(false, p.Decisions); ok {
				record.Answers = append(record.Answers, "deny")
				record.DenyDecision = string(decision)
			}
		}
		answerable := len(record.Answers) > 0
		record.Answerable = &answerable
		result.Requests = append(result.Requests, record)
	}
	if request.json {
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(result)
	}
	return writeAgentPermissionList(stdout, result, c.clock())
}

// codexPermissionDetails is a pending approval's kind-specific details as the
// list's toolInput: every field but the request id and kind, which the record
// already carries as id and toolName.
func codexPermissionDetails(p agentPendingApproval) (json.RawMessage, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	delete(fields, "requestId")
	delete(fields, "kind")
	return json.Marshal(fields)
}

// codexPermissionSummary is the bounded audit input of a Codex answer: the
// command text of a command approval, and otherwise only the detail key names.
// The store bounds it again to agentapproval.MaxInputSummaryRunes.
func codexPermissionSummary(p agentPendingApproval) string {
	if p.Kind == codexappserver.ApprovalCommand && strings.TrimSpace(p.Command) != "" {
		return p.Command
	}
	details, err := codexPermissionDetails(p)
	if err != nil {
		return ""
	}
	return agentapproval.InputSummary(string(p.Kind), details)
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
