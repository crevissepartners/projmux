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
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
)

const (
	agentControlOpStatus    = "status"
	agentControlOpStart     = "turn-start"
	agentControlOpSteer     = "turn-steer"
	agentControlOpDeliver   = "turn-deliver"
	agentControlOpInterrupt = "turn-interrupt"
	agentControlOpApprovals = "approval-list"
	agentControlOpReview    = "approval-review"

	agentControlAcceptanceProvider  = "provider"
	agentControlDeliveryUnconfirmed = "unconfirmed"
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
	Acceptance   string                   `json:"acceptance,omitempty"`
	Delivery     string                   `json:"delivery,omitempty"`
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
	retryWait   func(context.Context) error
}

func newCodexControlEpoch(wire agentControlWire, identity codexLifecycleIdentity, epoch string, snapshot codexappserver.LifecycleSnapshot, current func(codexLifecycleIdentity) bool) *codexControlEpoch {
	return &codexControlEpoch{
		wire: wire, identity: identity, epoch: strings.TrimSpace(epoch), current: current, active: true,
		threadState: snapshot.ThreadState, turnID: strings.TrimSpace(snapshot.TurnID), turnState: snapshot.TurnState,
		pending: map[string]codexappserver.ApprovalEnvelope{}, ambiguous: map[string]struct{}{},
		retryWait: waitCodexLifecycleRetry,
	}
}

func waitCodexLifecycleRetry(ctx context.Context) error {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

const (
	// lifecycleReadRefusedReason is what the two admission refusals say: the
	// endpoint or the one-second retry window would not serve this read yet,
	// and the next attempt is expected to.
	lifecycleReadRefusedReason = "fresh exact turn state read was refused"
	// lifecycleDrainRefusedReason is what a drain says instead. A lifecycle
	// read opens its own request-owned connection, so it meets the handshake
	// drain gate that an already-bound turn write never passes through; the
	// refusal therefore outlives every retry until the install's replacement
	// takes over. Saying so is the whole difference an operator can act on.
	lifecycleDrainRefusedReason = "an install drain refused the fresh exact turn state read"
)

// The three writes that make a fresh lifecycle read first, named as the
// refusal lines name them.
const (
	agentControlWriteStart   = "new turn write"
	agentControlWriteSteer   = "steer write"
	agentControlWriteDeliver = "turn write"
)

// lifecycleReadRefusal folds a lifecycle read error into the closed token the
// caller reports and the words that say why the read did not happen. The
// admission refusals and a drain are told apart here rather than at the three
// call sites, so neither can drift into the other's wording.
func lifecycleReadRefusal(err error) (string, string, bool) {
	switch codexbroker.RefusalOf(err) {
	case codexbroker.RefusalLifecycleRetry:
		return string(codexbroker.RefusalLifecycleRetry), lifecycleReadRefusedReason, true
	case codexbroker.RefusalLifecycleBusy:
		return string(codexbroker.RefusalLifecycleBusy), lifecycleReadRefusedReason, true
	case codexbroker.RefusalDrainRequired:
		return string(codexbroker.RefusalDrainRequired), lifecycleDrainRefusedReason, true
	default:
		return "", "", false
	}
}

// drainedWriteRefusal is the drain refusal a write returns when the state this
// epoch already tracks cannot carry it. Losing the read is not a reason to
// assert a state that was never confirmed, so the line names the drain rather
// than the turn state it could not check.
func drainedWriteRefusal(write string) agentControlResponse {
	return refusedControl(string(codexbroker.RefusalDrainRequired), lifecycleDrainRefusedReason+"; "+write+" refused")
}

// freshTurnState is what one write site acts on after its fresh lifecycle read.
type freshTurnState struct {
	// snapshot is the served lifecycle state, empty unless the read succeeded.
	snapshot codexappserver.LifecycleSnapshot
	// drained marks the one refusal that does not by itself stop the write. A
	// drain refuses the read because the read opens its own connection and so
	// meets the handshake drain gate; the binding underneath it is untouched
	// and keeps carrying turn writes. Cutting control here would sever what the
	// broker deliberately kept, so the site falls back to the lifecycle state
	// this epoch already tracks and refuses only if that cannot carry the write.
	drained bool
	// refusal is what the site must return when the read neither served a
	// usable snapshot nor met a drain.
	refusal *agentControlResponse
}

// reconcile folds a served snapshot into the epoch. A drained read has no
// snapshot to fold, and the tracked state stands as it is.
func (s freshTurnState) reconcile(e *codexControlEpoch) {
	if !s.drained {
		e.reconcileTurn(s.snapshot)
	}
}

// readTurnState makes the fresh lifecycle read one write needs and classifies
// the result. write names that write in any refusal line.
func (e *codexControlEpoch) readTurnState(ctx context.Context, write string) freshTurnState {
	snapshot, err := e.wire.ReadLifecycleSnapshot(ctx, e.identity.ThreadID)
	return e.classifyTurnState(snapshot, err, write)
}

func (e *codexControlEpoch) classifyTurnState(snapshot codexappserver.LifecycleSnapshot, err error, write string) freshTurnState {
	if codexbroker.RefusalOf(err) == codexbroker.RefusalDrainRequired {
		return freshTurnState{drained: true}
	}
	if code, reason, ok := lifecycleReadRefusal(err); ok {
		refusal := refusedControl(code, reason+"; "+write+" refused")
		return freshTurnState{refusal: &refusal}
	}
	var reason string
	switch {
	case err != nil:
		// The read error can carry provider detail, so the line never quotes it.
		reason = "fresh exact turn state is unavailable"
	case snapshot.ThreadID != e.identity.ThreadID:
		reason = "fresh exact turn state read returned a different thread"
	case !validFreshStartSnapshot(snapshot), write == agentControlWriteSteer && snapshot.ThreadState == codexappserver.ThreadStateSystemError:
		// Only a new turn may follow a system error; steer keeps refusing it here.
		reason = "fresh exact turn state cannot take a write (thread=" + string(closedThreadState(snapshot.ThreadState)) + " turn=" + closedTurnStateLabel(snapshot.TurnState) + ")"
	default:
		return freshTurnState{snapshot: snapshot}
	}
	refusal := refusedControl("turn-state-unavailable", reason+"; "+write+" refused")
	return freshTurnState{refusal: &refusal}
}

// closedThreadState keeps a refusal line to the closed thread-state set, so a
// state the normalizer never produced cannot put provider text on the line.
func closedThreadState(state codexappserver.ThreadState) codexappserver.ThreadState {
	switch state {
	case codexappserver.ThreadStateNotLoaded, codexappserver.ThreadStateIdle, codexappserver.ThreadStateActive,
		codexappserver.ThreadStateWaitingOnApproval, codexappserver.ThreadStateWaitingOnUserInput, codexappserver.ThreadStateSystemError:
		return state
	default:
		return codexappserver.ThreadStateUnknown
	}
}

// closedTurnStateLabel is closedThreadState for the latest turn; a thread with
// no turn reads as none.
func closedTurnStateLabel(state codexappserver.TurnState) string {
	switch state {
	case "":
		return "none"
	case codexappserver.TurnStateInProgress, codexappserver.TurnStateCompleted, codexappserver.TurnStateFailed, codexappserver.TurnStateInterrupted:
		return string(state)
	default:
		return string(codexappserver.TurnStateUnknown)
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
	case agentControlOpDeliver:
		return e.deliver(ctx, request)
	case agentControlOpStart:
		if strings.TrimSpace(request.Text) == "" {
			return refusedControl("stale-turn", "thread is not idle; new turn write refused")
		}
		state := e.readTurnState(ctx, agentControlWriteStart)
		if state.refusal != nil {
			return *state.refusal
		}
		// The snapshot request travels through the same connection/control epoch,
		// but the Registry binding can still be replaced while that read is in
		// flight. Re-check the existing binding fence before accepting either its
		// state or a provider mutation. A drain took the read, never the fence.
		if !e.current(e.identity) {
			return refusedControl("stale-binding", "exact Agent binding or activation generation changed")
		}
		state.reconcile(e)
		if !e.canStart() {
			if state.drained {
				return drainedWriteRefusal(agentControlWriteStart)
			}
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
		if strings.TrimSpace(request.Text) == "" {
			return refusedControl("stale-turn", "no exact active turn is available to steer")
		}
		if !e.canMutateCurrentTurn() {
			return refusedControl("no-active-turn", "no exact active turn is available to steer")
		}
		expectedTurnID := e.turnID
		state := e.readTurnState(ctx, agentControlWriteSteer)
		if state.refusal != nil {
			return *state.refusal
		}
		// The lifecycle read is content-free and travels through the same exact
		// connection epoch. Re-check the Agent/Pane/runtime/generation/thread
		// binding after it returns; this mutex keeps the control epoch fixed.
		// Reconcile even a terminal or different turn, but never retarget this
		// request from the cached exact turn to the newly observed one.
		if !e.current(e.identity) {
			return refusedControl("turn-state-unavailable", "fresh exact turn state is unavailable; steer write refused")
		}
		state.reconcile(e)
		if e.turnID != expectedTurnID || !e.canMutateCurrentTurn() {
			// This site checks the tracked state once before the read too, so
			// today a drain never arrives here: nothing between the two checks
			// can change the answer. The branch is kept because that ordering
			// is the only reason, and a reordering must not silently turn an
			// unconfirmed state into a claim that no turn is active.
			if state.drained {
				return drainedWriteRefusal(agentControlWriteSteer)
			}
			return refusedControl("no-active-turn", "no exact active turn is available to steer")
		}
		result, err := e.wire.SteerExactTurn(ctx, e.identity.ThreadID, expectedTurnID, request.Text)
		if err != nil {
			return controlWireFailure("stale-turn", err)
		}
		if result.ThreadID != e.identity.ThreadID || result.TurnID != expectedTurnID {
			return refusedControl("protocol-error", "turn/steer returned a different identity")
		}
		// A body-free turn/steer response proves only that the provider accepted
		// the request. It carries no acknowledgement of TUI display, model
		// consumption, or goal continuation.
		return agentControlResponse{
			OK: true, ThreadID: result.ThreadID, TurnID: result.TurnID,
			Acceptance: agentControlAcceptanceProvider, Delivery: agentControlDeliveryUnconfirmed,
		}
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

func (e *codexControlEpoch) deliver(ctx context.Context, request agentControlRequest) agentControlResponse {
	if strings.TrimSpace(request.Text) == "" {
		return refusedControl("stale-turn", "input is empty; turn write refused")
	}
	snapshot, err := e.wire.ReadLifecycleSnapshot(ctx, e.identity.ThreadID)
	if codexbroker.RefusalOf(err) == codexbroker.RefusalLifecycleRetry {
		if e.retryWait == nil || e.retryWait(ctx) != nil {
			return refusedControl(string(codexbroker.RefusalLifecycleRetry), "fresh exact turn state retry window did not pass; turn write refused")
		}
		snapshot, err = e.wire.ReadLifecycleSnapshot(ctx, e.identity.ThreadID)
	}
	state := e.classifyTurnState(snapshot, err, agentControlWriteDeliver)
	if state.refusal != nil {
		return *state.refusal
	}
	if !e.current(e.identity) {
		return refusedControl("stale-binding", "exact Agent binding or activation generation changed")
	}
	state.reconcile(e)
	if e.canStart() {
		result, writeErr := e.wire.StartExactTurn(ctx, e.identity.ThreadID, request.Text)
		if writeErr != nil {
			return controlWireFailure("turn-start-failed", writeErr)
		}
		if result.ThreadID != e.identity.ThreadID || result.TurnID == "" {
			return refusedControl("protocol-error", "turn/start returned a different or incomplete identity")
		}
		e.turnID, e.turnState, e.threadState = result.TurnID, codexappserver.TurnStateInProgress, codexappserver.ThreadStateActive
		e.pending = map[string]codexappserver.ApprovalEnvelope{}
		e.ambiguous = map[string]struct{}{}
		return agentControlResponse{OK: true, ThreadID: result.ThreadID, TurnID: result.TurnID}
	}
	if !e.canMutateCurrentTurn() {
		if state.drained {
			return drainedWriteRefusal(agentControlWriteDeliver)
		}
		return refusedControl("no-active-turn", "no exact active turn is available to steer")
	}
	expectedTurnID := e.turnID
	result, writeErr := e.wire.SteerExactTurn(ctx, e.identity.ThreadID, expectedTurnID, request.Text)
	if writeErr != nil {
		return controlWireFailure("stale-turn", writeErr)
	}
	if result.ThreadID != e.identity.ThreadID || result.TurnID != expectedTurnID {
		return refusedControl("protocol-error", "turn/steer returned a different identity")
	}
	return agentControlResponse{OK: true, ThreadID: result.ThreadID, TurnID: result.TurnID,
		Acceptance: agentControlAcceptanceProvider, Delivery: agentControlDeliveryUnconfirmed}
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

// canStart admits a new turn on an idle thread or on one a provider error left
// in system-error, as long as its latest turn is not still in progress.
func (e *codexControlEpoch) canStart() bool {
	return (e.threadState == codexappserver.ThreadStateIdle || e.threadState == codexappserver.ThreadStateSystemError) && (e.turnID == "" || e.turnState == codexappserver.TurnStateCompleted || e.turnState == codexappserver.TurnStateFailed || e.turnState == codexappserver.TurnStateInterrupted)
}

func validFreshStartSnapshot(snapshot codexappserver.LifecycleSnapshot) bool {
	turnID := strings.TrimSpace(snapshot.TurnID)
	switch snapshot.ThreadState {
	case codexappserver.ThreadStateActive, codexappserver.ThreadStateWaitingOnApproval, codexappserver.ThreadStateWaitingOnUserInput:
		return turnID != "" && snapshot.TurnState == codexappserver.TurnStateInProgress
	case codexappserver.ThreadStateIdle, codexappserver.ThreadStateSystemError:
		// A provider error fails the turn and leaves the thread in system-error;
		// app-server still starts the next turn there and returns it to idle.
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
	return &agentControlRefusal{Code: r.Code, Message: r.Message}
}

// agentControlRefusal is a refused control response. Its text is what the CLI
// has always printed; the type keeps the refusal code reachable with
// errors.As for callers that branch on it rather than on the text.
type agentControlRefusal struct {
	Code    string
	Message string
}

func (e *agentControlRefusal) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("native Codex control unavailable (%s)", e.Code)
	}
	return fmt.Sprintf("%s (%s)", e.Message, e.Code)
}
