// Package approvals shares approval execution between CLI and SDK.
package approvals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/operatorclient"
	"github.com/crevissepartners/projmux/internal/integrations/agentcontrol"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentapproval"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

const (
	// ReasonNotFound: no request with that id belongs to the Agent.
	ReasonNotFound = "permission-not-found"
	// ReasonNotPending: the request was already answered, and the
	// first answer wins. That is an allow or deny from projmux, and equally a
	// request closed because it was answered in Claude Code's own prompt (or
	// its hook was canceled) before a projmux answer arrived.
	ReasonNotPending = "permission-not-pending"
	// ReasonExpired: the answer window ended; Claude Code's own
	// prompt decides.
	ReasonExpired = "permission-expired"
	// ReasonAnsweringOff: agent-approval-answering is not projmux.
	ReasonAnsweringOff = "permission-answering-off"
	// ReasonProviderUnsupported: the Agent is neither a Claude nor
	// a Codex Agent.
	ReasonProviderUnsupported = "permission-provider-unsupported"
	// ReasonDecisionUnavailable: the pending Codex approval offers
	// no decision the answer may send: accept for --allow, or decline and
	// then cancel for --deny (a permissions grant offers neither).
	// `agent approval review` shows the decisions it does offer.
	ReasonDecisionUnavailable = "permission-decision-unavailable"
)

type List struct {
	ReadMode string `json:"readMode,omitempty"`
	// Provider is claude or codex; it tells which fields a record carries.
	Provider  string   `json:"provider"`
	AgentUID  string   `json:"agentUID"`
	AgentName string   `json:"agentName"`
	Answering string   `json:"answering"`
	Requests  []Record `json:"requests"`
}

type Record struct {
	ID        string              `json:"id"`
	State     agentapproval.State `json:"state"`
	ToolName  string              `json:"toolName"`
	AgentType string              `json:"agentType,omitempty"`
	ToolInput json.RawMessage     `json:"toolInput"`
	CreatedAt time.Time           `json:"createdAt,omitzero"`
	Deadline  time.Time           `json:"deadline,omitzero"`
	// Answers are the `agent approval answer` flags that can settle a Codex
	// request: allow, deny, both, or none.
	Answers []string `json:"answers,omitempty"`
	// DenyDecision is the Codex decision --deny would send: decline (deny,
	// the turn continues) or cancel (deny and interrupt the turn). It is
	// empty when --deny cannot answer the request.
	DenyDecision string `json:"denyDecision,omitempty"`
	// Answerable is set on every Codex record: false when its id is
	// ambiguous across raw request ids or no answer flag applies, and
	// `agent approval review` must answer it instead.
	Answerable *bool `json:"answerable,omitempty"`
	// ambiguous marks an id that names more than one pending Codex request.
	Ambiguous bool `json:"-"`
}

func StoreRefusal(err error) (string, string) {
	switch {
	case errors.Is(err, agentapproval.ErrNotFound):
		return ReasonNotFound, "does not exist"
	case errors.Is(err, agentapproval.ErrNotPending):
		return ReasonNotPending, "is already answered; the first answer wins"
	case errors.Is(err, agentapproval.ErrExpired):
		return ReasonExpired, "expired; Claude Code's own prompt decides it"
	case errors.Is(err, agentapproval.ErrClosed):
		return ReasonNotPending, "was closed: answered in Claude Code's own prompt, or its hook ended"
	default:
		return "", ""
	}
}

func AnswerError(request Request, err error) error {
	event := agentapproval.AuditDenied
	if request.Allow {
		event = agentapproval.AuditAllowed
	}
	switch {
	case errors.Is(err, agentapproval.ErrAudit):
		return fmt.Errorf("%s: %w; permission request %q is still waiting", request.Spelling, err, request.RequestID)
	case errors.Is(err, agentapproval.ErrUncommittedAudit):
		return fmt.Errorf("%s: %w; permission request %q is still waiting, and the audit log keeps its %s line for this answer that did not take effect", request.Spelling, err, request.RequestID, event)
	case errors.Is(err, agentapproval.ErrNotCommitted):
		return fmt.Errorf("%s: %w; permission request %q is still waiting; its %s audit line is followed by an uncommitted line", request.Spelling, err, request.RequestID, event)
	default:
		return fmt.Errorf("%s: %w", request.Spelling, err)
	}
}

func Decision(allow bool, offered []codexappserver.ApprovalDecision) (codexappserver.ApprovalDecision, bool) {
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

func DecisionEffect(decision codexappserver.ApprovalDecision) string {
	switch decision {
	case codexappserver.DecisionDecline:
		return "the turn continues"
	case codexappserver.DecisionCancel:
		return "the turn stops"
	default:
		return ""
	}
}

func AuditReason(decision codexappserver.ApprovalDecision) string {
	return "decision=" + string(decision)
}

func ReviewNotDelivered(callErr error, reviewed agentcontrol.Response) bool {
	if callErr != nil {
		var fence *agentcontrol.BindingError
		return errors.As(callErr, &fence) || errors.Is(callErr, agentcontrol.ErrNotSent)
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

func Details(p agentcontrol.PendingApproval) (json.RawMessage, error) {
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

func Summary(p agentcontrol.PendingApproval) string {
	if p.Kind == codexappserver.ApprovalCommand && strings.TrimSpace(p.Command) != "" {
		return p.Command
	}
	details, err := Details(p)
	if err != nil {
		return ""
	}
	return agentapproval.InputSummary(string(p.Kind), details)
}

type Request struct {
	Spelling, RequestID, Via string
	Allow                    bool
}
type AnswerResult struct {
	RequestID, State, AgentUID, AgentName, Decision, Effect string
	Warning                                                 error
}
type Failure struct{ Code, Message string }

func (e *Failure) Error() string            { return e.Message }
func (e *Failure) MetadataUsageError() bool { return true }
func Refuse(spelling string, agent coremetadata.Agent, reason, detail string) error {
	return &Failure{Code: reason, Message: fmt.Sprintf("%s: agent/%s %s (%s); nothing was changed", spelling, agent.Metadata.Name, detail, reason)}
}
func ValidateProvider(spelling string, agent coremetadata.Agent) error {
	switch coremetadata.NormalizeProvider(agent.Spec.Provider) {
	case "claude", "codex":
		return nil
	}
	return Refuse(spelling, agent, ReasonProviderUnsupported, fmt.Sprintf("is a %q Agent; permission requests apply only to --provider claude or codex", agent.Spec.Provider))
}
func ValidateVia(spelling, via string) error {
	if agentapproval.ValidVia(via) {
		return nil
	}
	return &Failure{Code: "operator-client-invalid", Message: fmt.Sprintf("%s --via: %v; want %s, %s, or an operator client name", spelling, operatorclient.Validate(via), agentapproval.ViaPopup, agentapproval.ViaCLI)}
}

type Control interface {
	Bind(context.Context, coremetadata.Registry, coremetadata.Agent) (agentcontrol.Binding, error)
	Call(context.Context, agentcontrol.Binding, agentcontrol.Request) (agentcontrol.Response, error)
}

// Hooks retain the CLI app helper adapters on the execution path while the
// default SDK uses the same underlying policy functions directly.
type Hooks struct {
	Decision           func(bool, []codexappserver.ApprovalDecision) (codexappserver.ApprovalDecision, bool)
	AuditReason        func(codexappserver.ApprovalDecision) string
	Details            func(agentcontrol.PendingApproval) (json.RawMessage, error)
	Summary            func(agentcontrol.PendingApproval) string
	ReviewNotDelivered func(error, agentcontrol.Response) bool
	StoreRefusal       func(error) (string, string)
	AnswerError        func(Request, error) error
}
type Service struct {
	Hooks Hooks

	OpenStore func() (*agentapproval.Store, error)
	Answering func() config.AgentApprovalAnswering
	Control   Control
}

func (c Service) ListStore(ctx context.Context, agent coremetadata.Agent) (List, error) {
	if err := ctx.Err(); err != nil {
		return List{}, err
	}
	store, err := c.OpenStore()
	if err != nil {
		return List{}, fmt.Errorf("agent approval list: %w", err)
	}
	return c.listStore(agent, store)
}

func (c Service) listStore(agent coremetadata.Agent, store *agentapproval.Store) (List, error) {
	records, err := store.List(agent.Metadata.UID)
	if err != nil {
		return List{}, fmt.Errorf("agent approval list: %w", err)
	}
	out := List{Provider: "claude", AgentUID: agent.Metadata.UID, AgentName: agent.Metadata.Name, Answering: string(c.Answering()), Requests: []Record{}}
	for _, v := range records {
		if v.State == agentapproval.StateWaiting {
			out.Requests = append(out.Requests, Record{ID: v.ID, State: v.State, ToolName: v.ToolName, AgentType: v.AgentType, ToolInput: v.ToolInput, CreatedAt: v.CreatedAt, Deadline: v.Deadline})
		}
	}
	return out, nil
}

// ListFileOnly opens the saved source before classification, matching the
// consumer's source error precedence. It never invokes Control.
func (c Service) ListFileOnly(ctx context.Context, agent coremetadata.Agent) (List, error) {
	if err := ctx.Err(); err != nil {
		return List{}, err
	}
	store, err := c.OpenStore()
	if err != nil {
		return List{}, fmt.Errorf("agent approval list: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return List{}, err
	}
	provider := coremetadata.NormalizeProvider(agent.Spec.Provider)
	if provider != "claude" {
		return List{Provider: provider, AgentUID: agent.Metadata.UID, AgentName: agent.Metadata.Name, ReadMode: "single-read", Requests: []Record{}}, nil
	}
	result, err := c.listStore(agent, store)
	if err != nil {
		return List{}, err
	}
	if err := ctx.Err(); err != nil {
		return List{}, err
	}
	result.ReadMode = "stored"
	return result, nil
}

func (c Service) AnswerStore(ctx context.Context, r Request, agent coremetadata.Agent) (AnswerResult, error) {
	if err := ctx.Err(); err != nil {
		return AnswerResult{}, err
	}
	store, err := c.OpenStore()
	if err != nil {
		return AnswerResult{}, fmt.Errorf("%s: %w", r.Spelling, err)
	}
	if err := ctx.Err(); err != nil {
		return AnswerResult{}, err
	}
	answered, err := store.Answer(r.RequestID, agent.Metadata.UID, r.Allow, r.Via)
	var warning error
	if errors.Is(err, agentapproval.ErrCommittedNotSynced) {
		warning, err = err, nil
	}
	if err != nil {
		if reason, detail := c.storeRefusal(err); reason != "" {
			return AnswerResult{}, Refuse(r.Spelling, agent, reason, fmt.Sprintf("permission request %q %s", r.RequestID, detail))
		}
		return AnswerResult{}, c.answerError(r, err)
	}
	return AnswerResult{RequestID: answered.ID, State: string(answered.State), AgentUID: agent.Metadata.UID, AgentName: agent.Metadata.Name, Warning: warning}, nil
}
func (c Service) ProjectCodex(agent coremetadata.Agent, answering config.AgentApprovalAnswering, pending []agentcontrol.PendingApproval) (List, error) {
	counts := map[string]int{}
	for _, p := range pending {
		counts[p.RequestID]++
	}
	out := List{Provider: "codex", AgentUID: agent.Metadata.UID, AgentName: agent.Metadata.Name, Answering: string(answering), Requests: []Record{}}
	for _, p := range pending {
		details, err := c.details(p)
		if err != nil {
			return List{}, err
		}
		v := Record{ID: p.RequestID, State: agentapproval.StateWaiting, ToolName: string(p.Kind), ToolInput: details, Ambiguous: counts[p.RequestID] > 1}
		if !v.Ambiguous {
			if _, ok := Decision(true, p.Decisions); ok {
				v.Answers = append(v.Answers, "allow")
			}
			if d, ok := Decision(false, p.Decisions); ok {
				v.Answers = append(v.Answers, "deny")
				v.DenyDecision = string(d)
			}
		}
		answerable := len(v.Answers) > 0
		v.Answerable = &answerable
		out.Requests = append(out.Requests, v)
	}
	return out, nil
}
func (c Service) Pending(ctx context.Context, reg coremetadata.Registry, agent coremetadata.Agent) (agentcontrol.Binding, []agentcontrol.PendingApproval, error) {
	b, err := c.Control.Bind(ctx, reg, agent)
	if err != nil {
		return b, nil, err
	}
	r, err := c.Control.Call(ctx, b, agentcontrol.Request{Operation: "approval-list"})
	if err == nil {
		err = Recovery(r.Error(), b)
	}
	return b, r.Approvals, err
}
func Recovery(err error, b agentcontrol.Binding) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w; Open Codex: `projmux focus pane uid:%s --project uid:%s --window uid:%s`", err, b.Identity.PaneUID, b.ProjectUID, b.WindowUID)
}
func (c Service) AnswerCodex(ctx context.Context, r Request, reg coremetadata.Registry, agent coremetadata.Agent) (AnswerResult, error) {
	if err := ctx.Err(); err != nil {
		return AnswerResult{}, err
	}
	refuse := func(reason, detail string) error { return Refuse(r.Spelling, agent, reason, detail) }
	if c.Answering() != config.AgentApprovalAnsweringProjmux {
		return AnswerResult{}, refuse(ReasonAnsweringOff, "cannot be answered from projmux while agent-approval-answering is claude; run `projmux config agent-approvals --answering projmux` first, or answer in the Codex TUI or with `projmux agent approval review`")
	}
	b, pending, err := c.Pending(ctx, reg, agent)
	if err != nil {
		return AnswerResult{}, fmt.Errorf("%s: %w", r.Spelling, err)
	}
	matches := slices.DeleteFunc(slices.Clone(pending), func(p agentcontrol.PendingApproval) bool { return p.RequestID != r.RequestID })
	switch len(matches) {
	case 0:
		return AnswerResult{}, refuse(ReasonNotPending, fmt.Sprintf("has no pending approval request %q; it was already answered (the first answer wins) or never existed", r.RequestID))
	case 1:
	default:
		return AnswerResult{}, refuse(ReasonNotPending, fmt.Sprintf("approval request id %q is ambiguous across raw request ids; answer it with `projmux agent approval review %s`", r.RequestID, agent.Metadata.Name))
	}
	p := matches[0]
	d, ok := c.decision(r.Allow, p.Decisions)
	if !ok {
		want := "accept"
		if !r.Allow {
			want = "decline or cancel"
		}
		return AnswerResult{}, refuse(ReasonDecisionUnavailable, fmt.Sprintf("approval request %q (%s) does not offer %s; answer it with `projmux agent approval review %s`", r.RequestID, p.Kind, want, agent.Metadata.Name))
	}
	event, word := agentapproval.AuditDenied, agentapproval.StateDenied
	if r.Allow {
		event, word = agentapproval.AuditAllowed, agentapproval.StateAllowed
	}
	store, err := c.OpenStore()
	if err != nil {
		return AnswerResult{}, fmt.Errorf("%s: %w; approval request %q is still waiting", r.Spelling, err, r.RequestID)
	}
	line := agentapproval.AuditLine{Event: event, RequestID: p.RequestID, AgentUID: agent.Metadata.UID, PaneUID: b.Identity.PaneUID, ToolName: string(p.Kind), Input: c.summary(p), Via: r.Via, Reason: c.auditReason(d)}
	if err := ctx.Err(); err != nil {
		return AnswerResult{}, err
	}
	if err := store.AppendAnswerAudit(line); err != nil {
		return AnswerResult{}, fmt.Errorf("%s: %w; approval request %q is still waiting", r.Spelling, err, r.RequestID)
	}
	reviewed, callErr := c.Control.Call(ctx, b, agentcontrol.Request{Operation: "approval-review", RequestKey: p.RequestID, Decision: string(d)})
	err = callErr
	if err == nil {
		err = Recovery(reviewed.Error(), b)
	}
	if err != nil {
		if !c.reviewNotDelivered(callErr, reviewed) {
			return AnswerResult{}, fmt.Errorf("%s: %w; the decision may or may not have reached Codex", r.Spelling, err)
		}
		if auditErr := store.AppendUncommittedAudit(line, agentapproval.UncommittedSendFailed); auditErr != nil {
			return AnswerResult{}, fmt.Errorf("%s: %w; the decision did not reach Codex, so the answer did not take effect, but the audit log keeps its %s line: %w", r.Spelling, err, event, auditErr)
		}
		return AnswerResult{}, fmt.Errorf("%s: %w; the decision did not reach Codex, so the answer did not take effect; its %s audit line is followed by an uncommitted line", r.Spelling, err, event)
	}
	return AnswerResult{RequestID: p.RequestID, State: string(word), AgentUID: agent.Metadata.UID, AgentName: agent.Metadata.Name, Decision: string(d), Effect: DecisionEffect(d)}, nil
}

// Review sends only a decision present on the exact selected pending request.
// Like CLI review, it neither applies the answering setting nor appends answer audit lines.
func (c Service) Review(ctx context.Context, b agentcontrol.Binding, p agentcontrol.PendingApproval, decision string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !slices.Contains(p.Decisions, codexappserver.ApprovalDecision(decision)) {
		return &Failure{Code: ReasonDecisionUnavailable, Message: "selected approval decision is no longer available"}
	}
	r, err := c.Control.Call(ctx, b, agentcontrol.Request{Operation: "approval-review", RequestKey: p.RequestID, Decision: decision})
	if err != nil {
		return err
	}
	return Recovery(r.Error(), b)
}

func (c Service) decision(allow bool, offered []codexappserver.ApprovalDecision) (codexappserver.ApprovalDecision, bool) {
	if c.Hooks.Decision != nil {
		return c.Hooks.Decision(allow, offered)
	}
	return Decision(allow, offered)
}

func (c Service) auditReason(decision codexappserver.ApprovalDecision) string {
	if c.Hooks.AuditReason != nil {
		return c.Hooks.AuditReason(decision)
	}
	return AuditReason(decision)
}

func (c Service) details(p agentcontrol.PendingApproval) (json.RawMessage, error) {
	if c.Hooks.Details != nil {
		return c.Hooks.Details(p)
	}
	return Details(p)
}

func (c Service) summary(p agentcontrol.PendingApproval) string {
	if c.Hooks.Summary != nil {
		return c.Hooks.Summary(p)
	}
	return Summary(p)
}

func (c Service) reviewNotDelivered(err error, r agentcontrol.Response) bool {
	if c.Hooks.ReviewNotDelivered != nil {
		return c.Hooks.ReviewNotDelivered(err, r)
	}
	return ReviewNotDelivered(err, r)
}

func (c Service) storeRefusal(err error) (string, string) {
	if c.Hooks.StoreRefusal != nil {
		return c.Hooks.StoreRefusal(err)
	}
	return StoreRefusal(err)
}

func (c Service) answerError(r Request, err error) error {
	if c.Hooks.AnswerError != nil {
		return c.Hooks.AnswerError(r, err)
	}
	return AnswerError(r, err)
}
