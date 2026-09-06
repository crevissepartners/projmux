package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

const (
	agentControlOpStatus    = "status"
	agentControlOpStart     = "turn-start"
	agentControlOpSteer     = "turn-steer"
	agentControlOpInterrupt = "turn-interrupt"
	agentControlOpApprovals = "approval-list"
	agentControlOpReview    = "approval-review"
)

type agentControlWire interface {
	ReadLifecycleSnapshot(context.Context, string) (codexappserver.LifecycleSnapshot, error)
	StartExactTurn(context.Context, string, string) (codexappserver.ControlResult, error)
	SteerExactTurn(context.Context, string, string, string) (codexappserver.ControlResult, error)
	InterruptExactTurn(context.Context, string, string) (codexappserver.ControlResult, error)
	RespondServerRequest(context.Context, json.RawMessage, any) error
}

type agentControlRequest struct {
	Operation  string                 `json:"operation"`
	Identity   codexLifecycleIdentity `json:"identity"`
	Epoch      string                 `json:"epoch"`
	Text       string                 `json:"text,omitempty"`
	RequestKey string                 `json:"requestKey,omitempty"`
	Decision   string                 `json:"decision,omitempty"`
}

type agentControlResponse struct {
	OK           bool                     `json:"ok"`
	Code         string                   `json:"code,omitempty"`
	Message      string                   `json:"message,omitempty"`
	Availability agentControlAvailability `json:"availability"`
	Approvals    []agentPendingApproval   `json:"approvals,omitempty"`
	ThreadID     string                   `json:"threadId,omitempty"`
	TurnID       string                   `json:"turnId,omitempty"`
}

type agentControlAvailability struct {
	Start     bool `json:"start"`
	Steer     bool `json:"steer"`
	Interrupt bool `json:"interrupt"`
	Review    bool `json:"review"`
}

type agentPendingApproval struct {
	RequestID       string                            `json:"requestId"`
	Kind            codexappserver.ApprovalKind       `json:"kind"`
	ThreadID        string                            `json:"threadId"`
	TurnID          string                            `json:"turnId"`
	ItemID          string                            `json:"itemId"`
	ApprovalID      *string                           `json:"approvalId,omitempty"`
	Command         string                            `json:"command,omitempty"`
	CWD             string                            `json:"cwd,omitempty"`
	NetworkHost     string                            `json:"networkHost,omitempty"`
	NetworkProtocol string                            `json:"networkProtocol,omitempty"`
	RequestCWD      string                            `json:"requestCwd,omitempty"`
	Reason          string                            `json:"reason,omitempty"`
	GrantRoot       *string                           `json:"grantRoot,omitempty"`
	Permissions     json.RawMessage                   `json:"permissions,omitempty"`
	Decisions       []codexappserver.ApprovalDecision `json:"decisions"`
}

type codexControlEpoch struct {
	mu          sync.Mutex
	wire        agentControlWire
	identity    codexLifecycleIdentity
	epoch       string
	current     func(codexLifecycleIdentity) bool
	active      bool
	threadState codexappserver.ThreadState
	turnID      string
	turnState   codexappserver.TurnState
	pending     map[string]codexappserver.ApprovalEnvelope
	ambiguous   map[string]struct{}
}

func newCodexControlEpoch(wire agentControlWire, identity codexLifecycleIdentity, epoch string, snapshot codexappserver.LifecycleSnapshot, current func(codexLifecycleIdentity) bool) *codexControlEpoch {
	return &codexControlEpoch{
		wire: wire, identity: identity, epoch: strings.TrimSpace(epoch), current: current, active: true,
		threadState: snapshot.ThreadState, turnID: strings.TrimSpace(snapshot.TurnID), turnState: snapshot.TurnState,
		pending: map[string]codexappserver.ApprovalEnvelope{}, ambiguous: map[string]struct{}{},
	}
}

func (e *codexControlEpoch) Revoke() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.active = false
	e.pending = nil
	e.ambiguous = nil
	e.turnID = ""
	e.turnState = codexappserver.TurnStateUnknown
}

func (e *codexControlEpoch) ApplyNotification(notification codexappserver.Notification) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.active {
		return nil
	}
	if envelope, recognized, err := codexappserver.DecodeApprovalEnvelope(notification); recognized {
		if err != nil {
			// A lifecycle event can still be valid when its response envelope is
			// incomplete. Keep the notification/focus fallback usable, but mint no
			// responder capability and therefore allow no provider write.
			return nil
		}
		if envelope.ThreadID != e.identity.ThreadID || envelope.TurnID != e.turnID || envelope.ItemID == "" {
			return nil
		}
		key := envelope.RawIDKey()
		if _, poisoned := e.ambiguous[key]; poisoned {
			return nil
		}
		if existing, ok := e.pending[key]; ok {
			if !reflect.DeepEqual(existing, envelope) {
				delete(e.pending, key)
				e.ambiguous[key] = struct{}{}
				return nil
			}
			return nil
		}
		e.pending[key] = envelope
		return nil
	}
	event, recognized, err := codexappserver.DecodeLifecycleEvent(notification)
	if err != nil || !recognized || event.ThreadID != e.identity.ThreadID {
		return err
	}
	switch event.Kind {
	case codexappserver.LifecycleTurnStarted:
		e.turnID, e.turnState, e.threadState = event.TurnID, codexappserver.TurnStateInProgress, codexappserver.ThreadStateActive
		e.pending = map[string]codexappserver.ApprovalEnvelope{}
		e.ambiguous = map[string]struct{}{}
	case codexappserver.LifecycleTurnCompleted:
		if event.TurnID == e.turnID {
			e.turnState = event.TurnState
			e.pending = map[string]codexappserver.ApprovalEnvelope{}
			e.ambiguous = map[string]struct{}{}
		}
	case codexappserver.LifecycleThreadStatus:
		e.threadState = event.ThreadState
	case codexappserver.LifecycleRequestResolved:
		var params struct {
			RequestID json.RawMessage `json:"requestId"`
			ThreadID  string          `json:"threadId"`
		}
		if json.Unmarshal(notification.Params, &params) == nil && strings.TrimSpace(params.ThreadID) == e.identity.ThreadID {
			delete(e.pending, string(params.RequestID))
			delete(e.ambiguous, string(params.RequestID))
		}
	}
	return nil
}

func (e *codexControlEpoch) Handle(ctx context.Context, request agentControlRequest) agentControlResponse {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.active || request.Epoch == "" || request.Epoch != e.epoch || request.Identity != e.identity {
		return refusedControl("stale-epoch", "exact Agent connection epoch is no longer active")
	}
	if e.current == nil || !e.current(e.identity) {
		return refusedControl("stale-binding", "exact Agent binding or activation generation changed")
	}
	if e.wire == nil {
		return refusedControl("unavailable", "native Codex control connection is unavailable")
	}
	switch request.Operation {
	case agentControlOpStatus:
		return agentControlResponse{OK: true, Availability: e.availability()}
	case agentControlOpApprovals:
		return agentControlResponse{OK: true, Availability: e.availability(), Approvals: e.approvals()}
	case agentControlOpStart:
		if strings.TrimSpace(request.Text) == "" {
			return refusedControl("stale-turn", "thread is not idle; new turn write refused")
		}
		snapshot, err := e.wire.ReadLifecycleSnapshot(ctx, e.identity.ThreadID)
		if err != nil || snapshot.ThreadID != e.identity.ThreadID || !validFreshStartSnapshot(snapshot) {
			return refusedControl("turn-state-unavailable", "fresh exact turn state is unavailable; new turn write refused")
		}
		// The snapshot request travels through the same connection/control epoch,
		// but the Registry binding can still be replaced while that read is in
		// flight. Re-check the existing binding fence before accepting either its
		// state or a provider mutation.
		if !e.current(e.identity) {
			return refusedControl("stale-binding", "exact Agent binding or activation generation changed")
		}
		e.reconcileTurn(snapshot)
		if !e.canStart() {
			return refusedControl("turn-in-progress", "exact thread already has a turn in progress")
		}
		result, err := e.wire.StartExactTurn(ctx, e.identity.ThreadID, request.Text)
		if err != nil {
			return controlWireFailure("turn-start-failed", err)
		}
		if result.ThreadID != e.identity.ThreadID || result.TurnID == "" {
			return refusedControl("protocol-error", "turn/start returned a different or incomplete identity")
		}
		e.turnID, e.turnState, e.threadState = result.TurnID, codexappserver.TurnStateInProgress, codexappserver.ThreadStateActive
		e.pending = map[string]codexappserver.ApprovalEnvelope{}
		e.ambiguous = map[string]struct{}{}
		return agentControlResponse{OK: true, ThreadID: result.ThreadID, TurnID: result.TurnID}
	case agentControlOpSteer:
		if strings.TrimSpace(request.Text) == "" || !e.canMutateCurrentTurn() {
			return refusedControl("stale-turn", "no exact active turn is available to steer")
		}
		result, err := e.wire.SteerExactTurn(ctx, e.identity.ThreadID, e.turnID, request.Text)
		if err != nil {
			return controlWireFailure("stale-turn", err)
		}
		if result.ThreadID != e.identity.ThreadID || result.TurnID != e.turnID {
			return refusedControl("protocol-error", "turn/steer returned a different identity")
		}
		return agentControlResponse{OK: true, ThreadID: result.ThreadID, TurnID: result.TurnID}
	case agentControlOpInterrupt:
		if !e.canMutateCurrentTurn() {
			return refusedControl("stale-turn", "no exact active turn is available to interrupt")
		}
		result, err := e.wire.InterruptExactTurn(ctx, e.identity.ThreadID, e.turnID)
		if err != nil {
			return controlWireFailure("turn-interrupt-failed", err)
		}
		if result.ThreadID != e.identity.ThreadID || result.TurnID != e.turnID {
			return refusedControl("protocol-error", "turn/interrupt returned a different identity")
		}
		e.turnState = codexappserver.TurnStateInterrupted
		return agentControlResponse{OK: true, ThreadID: result.ThreadID, TurnID: result.TurnID}
	case agentControlOpReview:
		return e.review(ctx, request)
	default:
		return refusedControl("invalid-operation", "unsupported exact Agent control operation")
	}
}

func (e *codexControlEpoch) review(ctx context.Context, request agentControlRequest) agentControlResponse {
	matches := make([]codexappserver.ApprovalEnvelope, 0, 1)
	for _, envelope := range e.pending {
		if envelope.RequestID == request.RequestKey {
			matches = append(matches, envelope)
		}
	}
	if len(matches) != 1 {
		return refusedControl("ambiguous-request", "pending request identity is missing, resolved, or ambiguous")
	}
	envelope := matches[0]
	decision := codexappserver.ApprovalDecision(request.Decision)
	if !slices.Contains(envelope.Decisions, decision) {
		return refusedControl("unsafe-decision", "decision is not a safe one-shot option for this request")
	}
	result, err := codexappserver.ApprovalResponse(envelope, decision)
	if err != nil {
		return controlWireFailure("unsafe-decision", err)
	}
	// Claim before write. A disconnect makes the outcome indeterminate, never
	// retryable; exactly-once is safer than synthesizing a second response.
	delete(e.pending, envelope.RawIDKey())
	if decision == codexappserver.DecisionCancel {
		e.turnState = codexappserver.TurnStateInterrupted
	}
	if err := e.wire.RespondServerRequest(ctx, envelope.RawRequestID, result); err != nil {
		return controlWireFailure("response-indeterminate", err)
	}
	return agentControlResponse{OK: true, ThreadID: envelope.ThreadID, TurnID: envelope.TurnID}
}

func (e *codexControlEpoch) availability() agentControlAvailability {
	review := false
	counts := map[string]int{}
	for _, envelope := range e.pending {
		counts[envelope.RequestID]++
	}
	for _, envelope := range e.pending {
		if counts[envelope.RequestID] == 1 && len(envelope.Decisions) > 0 {
			review = true
			break
		}
	}
	return agentControlAvailability{Start: e.canStart(), Steer: e.canMutateCurrentTurn(), Interrupt: e.canMutateCurrentTurn(), Review: review}
}

func (e *codexControlEpoch) HasActionableRequest(requestID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	matches := 0
	actionable := false
	for _, envelope := range e.pending {
		if envelope.RequestID == requestID {
			matches++
			actionable = len(envelope.Decisions) > 0
		}
	}
	return e.active && matches == 1 && actionable
}

func (e *codexControlEpoch) canStart() bool {
	return e.threadState == codexappserver.ThreadStateIdle && (e.turnID == "" || e.turnState == codexappserver.TurnStateCompleted || e.turnState == codexappserver.TurnStateFailed || e.turnState == codexappserver.TurnStateInterrupted)
}

func validFreshStartSnapshot(snapshot codexappserver.LifecycleSnapshot) bool {
	turnID := strings.TrimSpace(snapshot.TurnID)
	switch snapshot.ThreadState {
	case codexappserver.ThreadStateActive, codexappserver.ThreadStateWaitingOnApproval, codexappserver.ThreadStateWaitingOnUserInput:
		return turnID != "" && snapshot.TurnState == codexappserver.TurnStateInProgress
	case codexappserver.ThreadStateIdle:
		if turnID == "" {
			return snapshot.TurnState == "" || snapshot.TurnState == codexappserver.TurnStateUnknown
		}
		return snapshot.TurnState == codexappserver.TurnStateCompleted || snapshot.TurnState == codexappserver.TurnStateFailed || snapshot.TurnState == codexappserver.TurnStateInterrupted
	default:
		return false
	}
}

func (e *codexControlEpoch) reconcileTurn(snapshot codexappserver.LifecycleSnapshot) {
	turnChanged := e.turnID != strings.TrimSpace(snapshot.TurnID)
	e.threadState = snapshot.ThreadState
	e.turnID = strings.TrimSpace(snapshot.TurnID)
	e.turnState = snapshot.TurnState
	if turnChanged || snapshot.TurnState != codexappserver.TurnStateInProgress {
		e.pending = map[string]codexappserver.ApprovalEnvelope{}
		e.ambiguous = map[string]struct{}{}
	}
}

func (e *codexControlEpoch) canMutateCurrentTurn() bool {
	return e.turnID != "" && e.turnState == codexappserver.TurnStateInProgress && (e.threadState == codexappserver.ThreadStateActive || e.threadState == codexappserver.ThreadStateWaitingOnApproval || e.threadState == codexappserver.ThreadStateWaitingOnUserInput)
}

func (e *codexControlEpoch) approvals() []agentPendingApproval {
	out := make([]agentPendingApproval, 0, len(e.pending))
	for _, envelope := range e.pending {
		out = append(out, agentPendingApproval{
			RequestID: envelope.RequestID, Kind: envelope.Kind, ThreadID: envelope.ThreadID, TurnID: envelope.TurnID,
			ItemID: envelope.ItemID, ApprovalID: cloneOptionalString(envelope.ApprovalID), Command: envelope.Command, CWD: envelope.CWD,
			NetworkHost: envelope.NetworkHost, NetworkProtocol: envelope.NetworkProtocol, RequestCWD: envelope.RequestCWD,
			Reason:    envelope.Reason,
			GrantRoot: cloneOptionalString(envelope.GrantRoot), Permissions: append(json.RawMessage(nil), envelope.Permissions...),
			Decisions: append([]codexappserver.ApprovalDecision(nil), envelope.Decisions...),
		})
	}
	slices.SortFunc(out, func(a, b agentPendingApproval) int {
		if cmp := strings.Compare(a.RequestID, b.RequestID); cmp != 0 {
			return cmp
		}
		if cmp := strings.Compare(string(a.Kind), string(b.Kind)); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.ItemID, b.ItemID)
	})
	return out
}

func refusedControl(code, message string) agentControlResponse {
	return agentControlResponse{Code: code, Message: message}
}
func controlWireFailure(code string, err error) agentControlResponse {
	if errors.Is(err, context.DeadlineExceeded) {
		return refusedControl("timeout", "native Codex control timed out")
	}
	return refusedControl(code, "native Codex control refused the exact request")
}
func cloneOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func (r agentControlResponse) Error() error {
	if r.OK {
		return nil
	}
	if r.Message == "" {
		return fmt.Errorf("native Codex control unavailable (%s)", r.Code)
	}
	return fmt.Errorf("%s (%s)", r.Message, r.Code)
}
