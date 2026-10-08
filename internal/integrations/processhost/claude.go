package processhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ClaudeCommand preserves the resolved policy/settings argv and adds only the
// stream transport. It refuses transport overrides to keep framing unambiguous.
func ClaudeCommand(path, dir string, env, resolvedArgs []string) (Command, error) {
	for _, arg := range resolvedArgs {
		for _, reserved := range []string{"--input-format", "--output-format", "--permission-prompt-tool", "--print", "-p", "--"} {
			if arg == reserved || len(arg) > len(reserved) && arg[:len(reserved)+1] == reserved+"=" {
				return Command{}, fmt.Errorf("reserved stream argument: %s", arg)
			}
		}
	}
	args := append([]string{}, resolvedArgs...)
	args = append(args, "--print", "--verbose", "--input-format", "stream-json", "--output-format", "stream-json", "--include-partial-messages", "--permission-prompt-tool", "stdio")
	return Command{Path: path, Dir: dir, Env: append([]string{}, env...), Args: args}, nil
}

// Authority includes the connection/session as observed from this exact handle.
// A first Turn uses the empty session; subsequent operations use the bound one.
type Authority struct {
	Binding             Binding
	Connection, Session string
}

// Request is a single-writer control token, never a provider-selected policy.
type Request struct {
	ID, Connection, Session, Turn, Kind, Tool string
	Input                                     json.RawMessage
}

func cloneRequest(r Request) Request { r.Input = bytes.Clone(r.Input); return r }

// BindClaudeHook fences a SessionStart observation on the host's owned child.
// The caller must verify the hook peer's kernel parent identity before passing
// pid. This does not promote hook data to stream readiness or commit a session:
// the first system/init must independently agree before the binding is ready.
func (p *Handle) BindClaudeHook(ctx context.Context, binding Binding, pid int, session string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.checkClaudeHookLocked(ctx, binding, pid, session); err != nil {
		return err
	}
	p.hookSession = session
	return nil
}

// CheckClaudeHook only revalidates an already observed SessionStart. It cannot
// create session authority from a lease/helper payload.
func (p *Handle) CheckClaudeHook(ctx context.Context, binding Binding, pid int, session string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.hookSession == "" || p.hookSession != session {
		return ErrStale
	}
	return p.checkClaudeHookLocked(ctx, binding, pid, session)
}

func (p *Handle) checkClaudeHookLocked(ctx context.Context, binding Binding, pid int, session string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if binding != p.launch.Binding || p.adapter != nil || pid <= 0 || pid != p.pid || session == "" || len(session) > 256 ||
		(p.session != "" && p.session != session) || (p.hookSession != "" && p.hookSession != session) {
		return ErrStale
	}
	if p.state != "starting" && p.state != "ready" {
		return ErrClosed
	}
	current, cancel := context.WithTimeout(ctx, p.host.limits.Startup)
	defer cancel()
	if err := p.host.tx.Current(current, binding); err != nil {
		return err
	}
	return current.Err()
}

// Response discriminates question answers from permission decisions. Deny is
// the only negative response; timeouts/transport loss never synthesize Allow.
type Response struct {
	Answers map[string]string
	Allow   bool
	Deny    string
}

func (p *Handle) admitLocked(ctx context.Context, a Authority) error {
	if a.Binding != p.launch.Binding || a.Connection != p.connection || a.Session != p.session {
		return ErrStale
	}
	return p.admitOwnershipLocked(ctx, a.Binding)
}

// Lifecycle admission depends on the owned generation, even before init has
// established a conversation. Turns and answers still require full authority.
func (p *Handle) admitOwnershipLocked(ctx context.Context, binding Binding) error {
	if binding != p.launch.Binding {
		return ErrStale
	}
	if p.state != "starting" && p.state != "ready" {
		return ErrClosed
	}
	currentCtx, cancel := context.WithTimeout(ctx, p.host.limits.Startup)
	defer cancel()
	return p.host.tx.Current(currentCtx, p.launch.Binding)
}

// ValidateOwnership validates an exact process lifetime for Stop. It grants
// no session, turn or answer authority.
func (p *Handle) ValidateOwnership(ctx context.Context, binding Binding) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.admitOwnershipLocked(ctx, binding)
}

// ValidateAuthority checks current ownership before a consumer projects a
// pending token into an answer domain. Snapshot presence alone is not admission.
func (p *Handle) ValidateAuthority(ctx context.Context, a Authority) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.admitLocked(ctx, a)
}

func (p *Handle) writeLocked(ctx context.Context, frame any) error {
	raw, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	if len(raw)+1 > p.host.limits.FrameBytes {
		return errors.New("control frame exceeds limit")
	}
	deadline := time.Now().Add(p.host.limits.Write)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = p.stdin.SetWriteDeadline(deadline); err != nil {
		return err
	}
	raw = append(raw, '\n')
	n, err := p.stdin.Write(raw)
	if err != nil || n != len(raw) {
		// A partial write is an uncertain protocol operation. Never retry it.
		p.failure = "control write failed; outcome unknown"
		p.state = "stopping"
		p.expireLocked()
		_ = p.lifetime.Close()
		if err == nil {
			err = errors.New("short control write")
		}
	}
	return err
}

// Turn submits exactly one user input. Its return acknowledges the wire write,
// not provider readiness or completion. A retained turn ID never writes again.
func (p *Handle) Turn(ctx context.Context, a Authority, turn, prompt string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.admitLocked(ctx, a); err != nil {
		return err
	}
	if turn == "" || len(turn) > 256 || p.usedTurns[turn] {
		return ErrStale
	}
	if p.turn != "" || p.activeCriticalLocked() >= p.host.limits.Events {
		return ErrBusy
	}
	p.turn = turn
	p.rememberTurnLocked(turn)
	frame := map[string]any{"type": "user", "session_id": p.session, "parent_tool_use_id": nil, "message": map[string]any{"role": "user", "content": prompt}}
	if err := p.writeLocked(ctx, frame); err != nil {
		if p.state != "stopping" {
			p.turn = ""
			p.forgetUnwrittenTurnLocked(turn)
		}
		return err
	}
	if p.session == "" {
		go p.awaitInitialization()
	}
	p.emitLocked("turn-submitted", nil, nil)
	return nil
}

// ErrClaudeTurnActive proves reservation refused before any provider write.
var ErrClaudeTurnActive = fmt.Errorf("claude turn active: %w", ErrBusy)

// ReserveClaudeMessage admits endpoint input under the same mutex as Turn.
// Only the host's verified helper boundary may call it. No provider write is
// performed here; the reservation remains visible if that boundary disappears.
func (p *Handle) ReserveClaudeMessage(ctx context.Context, a Authority, turn string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.admitLocked(ctx, a); err != nil {
		return err
	}
	if p.adapter != nil || turn == "" || len(turn) > 256 || p.usedTurns[turn] {
		return ErrStale
	}
	if p.turn != "" {
		return ErrClaudeTurnActive
	}
	if p.activeCriticalLocked() >= p.host.limits.Events {
		return ErrBusy
	}
	p.turn = turn
	p.rememberTurnLocked(turn)
	p.messageReservation = "awaiting-message-handoff"
	p.messageOutcomeRecorded = false
	p.startMessageReservationTimerLocked(turn)
	p.emitLocked("message-reserved", nil, nil)
	return nil
}

// FinishClaudeMessage records a proven write outcome, never provider completion.
// A definite zero-write releases the reservation but keeps the operation ID
// consumed. Uncertain delivery expires visibly but keeps turn admission fenced
// until an actual result or exit; expiry never infers provider completion.
func (p *Handle) FinishClaudeMessage(ctx context.Context, a Authority, turn string, written, uncertain bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.admitLocked(ctx, a); err != nil {
		return err
	}
	if !p.usedTurns[turn] || turn == "" || written && uncertain {
		return ErrStale
	}
	// The stream can complete before the helper reports its write outcome.
	if p.turn != turn || p.messageOutcomeRecorded {
		return nil
	}
	if p.messageReservation == "" {
		return ErrStale
	}
	p.messageOutcomeRecorded = true
	if written {
		if p.messageReservation != "expired" {
			p.clearMessageReservationLocked()
		}
		p.emitLocked("message-handed-off", nil, nil)
	} else if uncertain {
		// A late handoff outcome cannot undo the expiry fence.
		if p.messageReservation != "expired" {
			p.messageReservation = "awaiting-message-handoff"
		}
		p.emitLocked("message-handoff-unknown", nil, nil)
	} else {
		p.emitLocked("message-prewrite-refused", nil, nil)
		if p.messageReservation != "expired" {
			p.turn = ""
			p.clearMessageReservationLocked()
		}
	}
	return nil
}

// Timer identity fences callbacks already waiting on the mutex when a result
// completes the reservation or another reservation replaces it.
func (p *Handle) startMessageReservationTimerLocked(turn string) {
	p.stopMessageReservationTimerLocked()
	var timer *time.Timer
	timer = time.AfterFunc(p.host.limits.MessageReservation, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.messageReservationTimer != timer || p.turn != turn || p.messageReservation != "awaiting-message-handoff" || (p.state != "starting" && p.state != "ready") {
			return
		}
		p.messageReservationTimer = nil
		p.messageReservation = "expired"
		p.emitLocked("message-reservation-expired", nil, nil)
	})
	p.messageReservationTimer = timer
}

func (p *Handle) stopMessageReservationTimerLocked() {
	if p.messageReservationTimer != nil {
		p.messageReservationTimer.Stop()
		p.messageReservationTimer = nil
	}
}

func (p *Handle) clearMessageReservationLocked() {
	p.stopMessageReservationTimerLocked()
	p.messageReservation = ""
}

// Respond is the only response writer. Hook consumers can observe Request but
// cannot obtain another writable token; duplicate/stale replies write zero bytes.
func (p *Handle) Respond(ctx context.Context, a Authority, token Request, response Response) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.admitLocked(ctx, a); err != nil {
		return err
	}
	pending, ok := p.requests[token.ID]
	if !ok || token.Connection != pending.Connection || token.Session != pending.Session || token.Turn != pending.Turn || token.Kind != pending.Kind || token.Tool != pending.Tool || !bytes.Equal(token.Input, pending.Input) || p.turn != pending.Turn {
		return ErrStale
	}
	var result any
	if pending.Kind == "question" {
		if response.Allow || response.Deny != "" || len(response.Answers) == 0 {
			return errors.New("question requires answers")
		}
		var input map[string]any
		if err := json.Unmarshal(pending.Input, &input); err != nil {
			return err
		}
		input["answers"] = response.Answers
		result = map[string]any{"behavior": "allow", "updatedInput": input}
	} else {
		if len(response.Answers) > 0 || response.Allow && response.Deny != "" || !response.Allow && response.Deny == "" {
			return errors.New("permission requires exactly allow or deny")
		}
		if response.Allow {
			var input any
			if err := json.Unmarshal(pending.Input, &input); err != nil {
				return err
			}
			result = map[string]any{"behavior": "allow", "updatedInput": input}
		} else {
			result = map[string]any{"behavior": "deny", "message": response.Deny}
		}
	}
	// Consume before writing: an uncertain write is never retried.
	delete(p.requests, token.ID)
	if err := p.writeLocked(ctx, map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": token.ID, "response": result}}); err != nil {
		req := cloneRequest(pending)
		p.emitLocked("control-expired", nil, &req)
		return err
	}
	req := cloneRequest(pending)
	p.emitLocked("control-answered", nil, &req)
	return nil
}

// Expire closes a pending token without an allow or any wire write. Policy and
// deadlines belong to the consumer; the host does not invent a timeout policy.
func (p *Handle) Expire(a Authority, token Request) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if a.Binding != p.launch.Binding || a.Connection != p.connection || a.Session != p.session {
		return ErrStale
	}
	pending, ok := p.requests[token.ID]
	if !ok || pending.Turn != token.Turn || pending.Connection != token.Connection {
		return ErrStale
	}
	delete(p.requests, token.ID)
	req := cloneRequest(pending)
	p.emitLocked("control-expired", nil, &req)
	return nil
}

func (p *Handle) Interrupt(ctx context.Context, a Authority, turn string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.admitLocked(ctx, a); err != nil {
		return err
	}
	if turn == "" || turn != p.turn || p.interrupt != "" {
		return ErrStale
	}
	p.interrupt = "interrupt-" + turn
	return p.writeLocked(ctx, map[string]any{"type": "control_request", "request_id": p.interrupt, "request": map[string]any{"subtype": "interrupt"}})
}

func (p *Handle) expireLocked() {
	for id, request := range p.requests {
		req := cloneRequest(request)
		p.emitLocked("control-expired", nil, &req)
		delete(p.requests, id)
	}
}

func (p *Handle) consume(raw []byte) error {
	var frame struct {
		Type      string `json:"type"`
		Subtype   string `json:"subtype"`
		Session   string `json:"session_id"`
		RequestID string `json:"request_id"`
		Request   struct {
			Subtype string          `json:"subtype"`
			Tool    string          `json:"tool_name"`
			Input   json.RawMessage `json:"input"`
		} `json:"request"`
		Response struct {
			Subtype   string `json:"subtype"`
			RequestID string `json:"request_id"`
		} `json:"response"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		return fmt.Errorf("malformed Claude frame: %w", err)
	}
	if frame.Type == "" {
		return errors.New("claude frame missing type")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state == "stopping" || p.state == "exited" || p.state == "unknown" {
		return nil
	}
	if frame.Session != "" && p.session != "" && frame.Session != p.session {
		return errors.New("provider session changed")
	}
	switch frame.Type {
	case "system":
		if frame.Subtype != "init" {
			p.emitLocked("provider-event", raw, nil)
			return nil
		}
		if frame.Session == "" || p.turn == "" || (p.hookSession != "" && p.hookSession != frame.Session) {
			return errors.New("init without session or first input")
		}
		if session := p.launch.expectedResumeSession(); session != "" && frame.Session != session {
			return errors.New("claude resume returned a different session")
		}
		if p.session == frame.Session {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), p.host.limits.Startup)
		defer cancel()
		if err := p.host.tx.Commit(ctx, p.launch.Binding, frame.Session); err != nil {
			return fmt.Errorf("binding CAS: %w", err)
		}
		p.session, p.state = frame.Session, "ready"
		p.emitLocked("ready", nil, nil)
	case "control_request":
		if p.session == "" || p.turn == "" || frame.RequestID == "" {
			return errors.New("unbound control request")
		}
		if frame.Request.Subtype != "can_use_tool" || frame.Request.Tool == "" {
			return errors.New("unsupported control request")
		}
		if p.usedRequests[frame.RequestID] {
			return nil
		}
		if len(p.requests) >= p.host.limits.Requests || len(p.usedRequests) >= p.host.limits.Events || p.activeCriticalLocked() >= p.host.limits.Events {
			return errors.New("control request capacity exceeded")
		}
		var input map[string]any
		if err := json.Unmarshal(frame.Request.Input, &input); err != nil || input == nil {
			return errors.New("invalid tool input")
		}
		kind := "permission"
		if frame.Request.Tool == "AskUserQuestion" {
			kind = "question"
		}
		request := Request{ID: frame.RequestID, Connection: p.connection, Session: p.session, Turn: p.turn, Kind: kind, Tool: frame.Request.Tool, Input: bytes.Clone(frame.Request.Input)}
		p.requests[request.ID] = request
		p.usedRequests[request.ID] = true
		req := cloneRequest(request)
		p.emitLocked("control-pending", nil, &req)
	case "control_cancel_request":
		// The provider abandons this exact connection-local request. Cancel is
		// terminal, idempotent and never produces a permission response.
		if frame.RequestID == "" {
			return errors.New("cancel without request identity")
		}
		if request, ok := p.requests[frame.RequestID]; ok {
			delete(p.requests, frame.RequestID)
			req := cloneRequest(request)
			p.emitLocked("control-expired", raw, &req)
		}
	case "control_response":
		if p.interrupt == "" || frame.Response.RequestID != p.interrupt {
			return errors.New("unexpected control response")
		}
		if frame.Response.Subtype != "success" {
			return errors.New("interrupt rejected")
		}
		if !p.interruptAck {
			p.emitLocked("interrupt-ack", nil, nil)
			p.interruptAck = true
		}
	case "result":
		if p.turn == "" || p.session == "" || frame.Session != p.session {
			return errors.New("unbound turn result")
		}
		p.expireLocked()
		p.emitLocked("turn-result", raw, nil)
		p.turn, p.interrupt = "", ""
		p.clearMessageReservationLocked()
		p.interruptAck = false
		p.trimCriticalLocked()
		if completed := p.launch.TurnCompleted; completed != nil && completed.Notify != nil {
			go completed.Notify(p.launch.Binding)
		}
	case "rate_limit_event":
		p.emitLocked("provider-event", raw, nil)
	case "stream_event", "assistant", "user", "tool_progress", "tool_use_summary":
		p.emitLocked("output", raw, nil)
	default:
		p.emitLocked("provider-event", raw, nil)
		return nil
	}
	return nil
}

func (p *Handle) awaitInitialization() {
	timer := time.NewTimer(p.host.limits.Startup)
	defer timer.Stop()
	select {
	case <-p.done:
		return
	case <-timer.C:
	}
	p.mu.Lock()
	expired := p.state == "starting" && p.session == ""
	p.mu.Unlock()
	if expired {
		p.protocolFailure(errors.New("provider initialization timeout"))
	}
}
