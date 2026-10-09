package processhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// CodexCommand selects a dedicated stdio app-server, never a daemon or proxy.
// Settings and policy overrides remain explicit consumer-supplied arguments.
// The environment keeps only the allowlisted inherited variables (providerEnv).
func CodexCommand(path, dir string, env, resolvedArgs []string) (Command, error) {
	for _, arg := range resolvedArgs {
		for _, reserved := range []string{"app-server", "daemon", "proxy", "--listen", "--stdio", "--code-mode-host", "--"} {
			if arg == reserved || strings.HasPrefix(arg, reserved+"=") {
				return Command{}, fmt.Errorf("reserved app-server argument: %s", arg)
			}
		}
	}
	return Command{Path: path, Dir: dir, Env: providerEnv(env, codexEnv), Args: append([]string{"app-server", "--listen", "stdio://"}, resolvedArgs...)}, nil
}

// CodexConfig uses the existing typed launch vocabulary. Empty settings retain
// provider defaults; requested settings must be witnessed before ready.
type CodexConfig struct {
	Version, DeveloperInstructions string
	Roots                          []string
	Settings                       codexappserver.ThreadSettings
}

// CodexHandle exposes typed control for only the owned connection. It does not
// expose the Client or its response writer to another consumer.
type CodexHandle struct{ handle *Handle }

// UserTurnMode is the provider operation selected at owned admission.
type UserTurnMode string

const (
	UserTurnStart UserTurnMode = "start"
	UserTurnSteer UserTurnMode = "steer"
)

// UserTurnDelivery witnesses provider acceptance, not model consumption.
// Operation is the caller's deduplication ID; TurnID is the provider's turn ID.
type UserTurnDelivery struct {
	Mode      UserTurnMode
	Operation string
	TurnID    string
}

// DeliverUserTurn starts while idle or steers the exact admitted active turn.
// A refused or uncertain steer is never retried as another turn or a start.
func (c *CodexHandle) DeliverUserTurn(ctx context.Context, a Authority, operation, prompt string) (UserTurnDelivery, error) {
	return c.handle.adapter.(*codexAdapter).deliver(ctx, a, operation, prompt, true, false)
}

// SteerUserTurn only delivers to the running turn selected at owned admission.
// Idle refusal occurs before consuming the operation or writing provider bytes.
func (c *CodexHandle) SteerUserTurn(ctx context.Context, a Authority, operation, prompt string) (UserTurnDelivery, error) {
	return c.handle.adapter.(*codexAdapter).deliver(ctx, a, operation, prompt, true, true)
}

// A local refusal has no provider turn ID; the consumed operation is its ID.
// Completed provider payloads keep their full turn object and share this shape.
type codexTurnResult struct {
	ThreadID string          `json:"threadId"`
	Turn     codexResultTurn `json:"turn"`
}
type codexResultTurn struct {
	ID     string            `json:"id"`
	Status string            `json:"status"`
	Error  *codexResultError `json:"error,omitempty"`
}
type codexResultError struct {
	Message string `json:"message"`
}

func (h *Host) StartCodex(ctx context.Context, launch Launch, config CodexConfig) (*CodexHandle, error) {
	launch.provider = "codex"
	launch.adapter = config
	p, err := h.Start(ctx, launch)
	if p == nil {
		return nil, err
	}
	return &CodexHandle{handle: p}, err
}
func (c *CodexHandle) Observe(b Binding) (Snapshot, error) { return c.handle.Observe(b) }

// NegotiatedVersion reports only the version witnessed on this owned wire.
// It never probes PATH, a proxy, or another generation.
func (c *CodexHandle) NegotiatedVersion(b Binding) (string, error) {
	if _, err := c.handle.Observe(b); err != nil {
		return "", err
	}
	return c.handle.adapter.(*codexAdapter).client.NegotiatedVersion(), nil
}
func (c *CodexHandle) Events(b Binding, after uint64) ([]Event, Snapshot, error) {
	return c.handle.Events(b, after)
}
func (c *CodexHandle) Stop(b Binding) error { return c.handle.Stop(b) }
func (c *CodexHandle) Changed(b Binding) (<-chan struct{}, error) {
	return c.handle.Changed(b)
}
func (c *CodexHandle) CheckGeneration(ctx context.Context, b Binding) error {
	return c.handle.CheckGeneration(ctx, b)
}
func (c *CodexHandle) Wait(ctx context.Context, b Binding) (Snapshot, error) {
	return c.handle.Wait(ctx, b)
}
func (c *CodexHandle) Turn(ctx context.Context, a Authority, operation, prompt string) error {
	return c.handle.adapter.(*codexAdapter).turn(ctx, a, operation, prompt)
}
func (c *CodexHandle) Interrupt(ctx context.Context, a Authority, turn string) error {
	return c.handle.adapter.(*codexAdapter).interrupt(ctx, a, turn)
}
func (c *CodexHandle) ValidateAuthority(ctx context.Context, a Authority) error {
	return c.handle.ValidateAuthority(ctx, a)
}

func (c *CodexHandle) Expire(a Authority, token Request) error { return c.handle.Expire(a, token) }

// RespondApproval uses the exact decoded envelope and its safe decisions.
func (c *CodexHandle) RespondApproval(ctx context.Context, a Authority, token Request, decision codexappserver.ApprovalDecision) error {
	return c.handle.adapter.(*codexAdapter).respond(ctx, a, token, func(n codexappserver.Notification) (any, error) {
		envelope, ok, err := codexappserver.DecodeApprovalEnvelope(n)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrStale
		}
		return codexappserver.ApprovalResponse(envelope, decision)
	})
}

// RespondQuestion reuses the established question parser and selection rules;
// the array under each exact question ID survives onto the native response.
func (c *CodexHandle) RespondQuestion(ctx context.Context, a Authority, token Request, selections map[int]agentquestion.Selection) error {
	return c.handle.adapter.(*codexAdapter).respond(ctx, a, token, func(n codexappserver.Notification) (any, error) {
		if n.Method != "item/tool/requestUserInput" {
			return nil, ErrStale
		}
		var params struct {
			Questions json.RawMessage `json:"questions"`
		}
		if err := json.Unmarshal(n.Params, &params); err != nil {
			return nil, err
		}
		questions, err := agentquestion.ParseCodexQuestions(params.Questions)
		if err != nil {
			return nil, err
		}
		answers, err := agentquestion.BuildCodexAnswers(questions, selections)
		if err != nil {
			return nil, err
		}
		type answer struct {
			Answers []string `json:"answers"`
		}
		response := struct {
			Answers map[string]answer `json:"answers"`
		}{Answers: make(map[string]answer, len(answers))}
		for id, encoded := range answers {
			var values []string
			if err := json.Unmarshal([]byte(encoded), &values); err != nil {
				return nil, err
			}
			response.Answers[id] = answer{Answers: values}
		}
		return response, nil
	})
}

type codexAdapter struct {
	p      *Handle
	client *codexappserver.Client
	// Typed writes serialize, but the reader must keep draining while an RPC
	// waits. A bounded queue preserves start/interrupt reply ordering.
	control       chan struct{}
	awaitingReply bool                          // guarded by p.mu
	replyQueue    []codexappserver.Notification // guarded by p.mu
	// FIFO replay horizon: the last Events admitted deliveries, including
	// confirmed refusals. Eviction permits reuse beyond that bounded horizon.
	usedDeliveries map[string]bool // guarded by p.mu
	deliveryOrder  []string        // guarded by p.mu
	providerTurns  []string        // bounded provider turn ID fence, guarded by p.mu
}

func (cfg CodexConfig) clone() adapterConfig { cfg.Roots = slices.Clone(cfg.Roots); return cfg }
func (cfg CodexConfig) newAdapter(p *Handle) providerAdapter {
	return &codexAdapter{p: p, control: make(chan struct{}, 1), usedDeliveries: make(map[string]bool)}
}

func (c *codexAdapter) attach(stream io.ReadWriteCloser) {
	c.client = codexappserver.NewProcessClient(stream, c.p.host.limits.Events)
}
func (c *codexAdapter) lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case c.control <- struct{}{}:
		if err := ctx.Err(); err != nil {
			c.unlock()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-c.p.done:
		return ErrClosed
	}
}
func (c *codexAdapter) unlock() { <-c.control }
func (c *codexAdapter) initialize(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, c.p.host.limits.Startup)
	defer cancel()
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.unlock()
	cfg := c.p.launch.adapter.(CodexConfig)
	if _, err := c.client.InitializeExperimental(ctx, cfg.Version); err != nil {
		return err
	}
	// Resume can open a goal turn before its RPC reply. Keep those frames in
	// wire order until the returned thread has been committed to this generation.
	c.p.mu.Lock()
	c.awaitingReply = true
	c.p.mu.Unlock()
	var thread codexappserver.ThreadBinding
	var err error
	expected := ""
	if transfer := c.p.launch.codexTransfer; transfer != nil {
		if c.p.launch.resume != nil {
			return ErrResumeRefused
		}
		if err := transfer.Verify(ctx, transfer.Source, c.p.launch.Binding); err != nil {
			return err
		}
		expected = transfer.Source.Thread
		thread, err = c.client.ResumeThreadWithSettings(ctx, expected, c.p.launch.Command.Dir, cfg.Roots, cfg.Settings)
	} else if record := c.p.launch.resume; record != nil {
		expected = record.Session
		thread, err = c.client.ResumeThreadWithSettings(ctx, record.Session, c.p.launch.Command.Dir, cfg.Roots, cfg.Settings)
	} else {
		thread, err = c.client.StartThreadWithSettings(ctx, c.p.launch.Command.Dir, cfg.Roots, cfg.DeveloperInstructions, cfg.Settings)
	}
	if err != nil {
		return err
	}
	if expected != "" && thread.ThreadID != expected {
		return fmt.Errorf("%w: returned thread differs from source", ErrResumeRefused)
	}
	p := c.p
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state != "starting" {
		return ErrClosed
	}
	if err = p.host.tx.Commit(ctx, p.launch.Binding, thread.ThreadID); err != nil {
		return fmt.Errorf("binding CAS: %w", err)
	}
	p.session, p.state = thread.ThreadID, "ready"
	p.emitLocked("ready", nil, nil)
	c.awaitingReply = false
	return c.drainReplyLocked()
}
func (c *codexAdapter) turn(ctx context.Context, a Authority, operation, prompt string) error {
	_, err := c.deliver(ctx, a, operation, prompt, false, false)
	return err
}

func (c *codexAdapter) deliver(ctx context.Context, a Authority, operation, prompt string, allowSteer, steerOnly bool) (UserTurnDelivery, error) {
	ctx, cancel := context.WithTimeout(ctx, c.p.host.limits.Startup)
	defer cancel()
	if err := c.lock(ctx); err != nil {
		return UserTurnDelivery{}, err
	}
	defer c.unlock()
	p := c.p
	p.mu.Lock()
	if err := p.admitLocked(ctx, a); err != nil {
		p.mu.Unlock()
		return UserTurnDelivery{}, err
	}
	if operation == "" || len(operation) > 256 || p.usedTurns[operation] || c.usedDeliveries[operation] {
		p.mu.Unlock()
		return UserTurnDelivery{}, ErrStale
	}
	mode, turn := UserTurnStart, p.turn
	if steerOnly && turn == "" {
		p.mu.Unlock()
		return UserTurnDelivery{}, ErrBusy
	}
	if turn != "" {
		if !allowSteer {
			p.mu.Unlock()
			return UserTurnDelivery{}, ErrBusy
		}
		mode = UserTurnSteer
	} else if p.activeCriticalLocked() >= p.host.limits.Events {
		p.mu.Unlock()
		return UserTurnDelivery{}, ErrBusy
	}
	if allowSteer && strings.TrimSpace(prompt) == "" {
		p.mu.Unlock()
		return UserTurnDelivery{}, ErrStale
	}
	// Consume before writing, independently of request-ID fencing. Starting
	// retains the existing turn fence; steering must never clear usedRequests.
	for len(c.deliveryOrder) >= p.host.limits.Events {
		delete(c.usedDeliveries, c.deliveryOrder[0])
		c.deliveryOrder = c.deliveryOrder[1:]
	}
	c.usedDeliveries[operation] = true
	c.deliveryOrder = append(c.deliveryOrder, operation)
	if mode == UserTurnStart {
		p.rememberTurnLocked(operation)
	}
	c.awaitingReply = true
	p.mu.Unlock()
	var err error
	if mode == UserTurnSteer {
		_, err = c.client.SteerExactTurn(ctx, a.Session, turn, prompt)
	} else {
		settings := p.launch.adapter.(CodexConfig).Settings
		turn, err = c.client.StartTurnWithOptions(ctx, a.Session, prompt, operation, settings.Model, settings.Effort)
	}
	if err != nil {
		if codexappserver.IsResponseError(err) {
			p.mu.Lock()
			c.awaitingReply = false
			if mode == UserTurnStart {
				raw, _ := json.Marshal(codexTurnResult{ThreadID: a.Session, Turn: codexResultTurn{ID: operation, Status: "failed", Error: &codexResultError{Message: "server-refused"}}})
				p.emitLocked("turn-result", raw, nil)
			}
			queueErr := c.drainReplyLocked()
			p.mu.Unlock()
			if queueErr != nil {
				p.protocolFailure(queueErr)
			}
		} else {
			p.protocolFailure(err)
		}
		return UserTurnDelivery{}, err
	}
	p.mu.Lock()
	if p.state != "ready" {
		p.mu.Unlock()
		return UserTurnDelivery{}, ErrClosed
	}
	if mode == UserTurnStart {
		p.turn = turn
		c.rememberProviderTurnLocked(turn)
		p.emitLocked("turn-submitted", nil, nil)
	}
	c.awaitingReply = false
	err = c.drainReplyLocked()
	p.mu.Unlock()
	if err != nil {
		p.protocolFailure(err)
		return UserTurnDelivery{}, err
	}
	return UserTurnDelivery{Mode: mode, Operation: operation, TurnID: turn}, nil
}

// drainReplyLocked preserves wire order while the command semaphore still
// blocks another turn admission. The reader never waits on that semaphore.
func (c *codexAdapter) drainReplyLocked() error {
	queued := c.replyQueue
	c.replyQueue = nil
	for _, n := range queued {
		if err := c.consumeLocked(n); err != nil {
			return err
		}
	}
	return nil
}
func (c *codexAdapter) interrupt(ctx context.Context, a Authority, turn string) error {
	ctx, cancel := context.WithTimeout(ctx, c.p.host.limits.Startup)
	defer cancel()
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.unlock()
	p := c.p
	p.mu.Lock()
	if err := p.admitLocked(ctx, a); err != nil {
		p.mu.Unlock()
		return err
	}
	if turn == "" || turn != p.turn || p.interrupt != "" {
		p.mu.Unlock()
		return ErrStale
	}
	p.interrupt = turn
	c.awaitingReply = true
	p.mu.Unlock()
	if _, err := codexappserver.InterruptExactTurnOn(ctx, c.client, a.Session, turn); err != nil {
		if codexappserver.IsResponseError(err) {
			p.mu.Lock()
			// A confirmed server refusal did not interrupt the turn. A later
			// explicit request may try again; uncertain writes remain fenced.
			p.interrupt = ""
			c.awaitingReply = false
			p.emitLocked("interrupt-refused", nil, nil)
			queueErr := c.drainReplyLocked()
			p.mu.Unlock()
			if queueErr != nil {
				p.protocolFailure(queueErr)
			}
		} else {
			p.protocolFailure(err)
		}
		return err
	}
	p.mu.Lock()
	if p.state != "ready" {
		p.mu.Unlock()
		return ErrClosed
	}
	c.awaitingReply = false
	p.interruptAck = true
	p.emitLocked("interrupt-ack", nil, nil)
	err := c.drainReplyLocked()
	p.mu.Unlock()
	if err != nil {
		p.protocolFailure(err)
	}
	return err
}
func (c *codexAdapter) respond(ctx context.Context, a Authority, token Request, build func(codexappserver.Notification) (any, error)) error {
	ctx, cancel := context.WithTimeout(ctx, c.p.host.limits.Startup)
	defer cancel()
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.unlock()
	p := c.p
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.admitLocked(ctx, a); err != nil {
		return err
	}
	pending, ok := p.requests[token.ID]
	if !ok || token.Connection != pending.Connection || token.Session != pending.Session || token.Turn != pending.Turn || token.Kind != pending.Kind || token.Tool != pending.Tool || !bytes.Equal(token.Input, pending.Input) || p.turn != pending.Turn {
		return ErrStale
	}
	var n codexappserver.Notification
	if err := json.Unmarshal(pending.Input, &n); err != nil {
		return err
	}
	// RawRequestID is omitted by neither Notification nor this private token.
	result, err := build(n)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	delete(p.requests, token.ID)
	if err = c.client.RespondServerRequest(ctx, n.RawRequestID, result); err != nil {
		req := cloneRequest(pending)
		p.emitLocked("control-expired", nil, &req)
		p.failure = "Codex control response failed; outcome unknown"
		p.state = "stopping"
		p.expireLocked()
		_ = p.stdin.Close()
		_ = p.lifetime.Close()
		return err
	}
	req := cloneRequest(pending)
	p.emitLocked("control-answered", nil, &req)
	return nil
}
func (c *codexAdapter) readOutput() {
	defer c.client.Close()
	for n := range c.client.Notifications() {
		err := c.consume(n)
		if err != nil {
			c.p.protocolFailure(err)
			return
		}
	}
	// Stream loss is not child exit. Give the independent Wait reader one
	// grace budget, matching the Claude stream contract.
	timer := time.NewTimer(c.p.host.limits.Grace)
	defer timer.Stop()
	select {
	case <-c.p.statusDone:
		return
	case <-timer.C:
	}
	c.p.mu.Lock()
	active := c.p.state == "ready" || c.p.state == "starting"
	c.p.mu.Unlock()
	if active {
		c.p.protocolFailure(errors.New("codex connection lost"))
	}
}
func (c *codexAdapter) consume(n codexappserver.Notification) error {
	p := c.p
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state != "ready" && !(p.state == "starting" && c.awaitingReply) {
		return nil
	}
	if c.awaitingReply {
		if len(c.replyQueue) >= p.host.limits.Events {
			return ErrBusy
		}
		c.replyQueue = append(c.replyQueue, n)
		return nil
	}
	return c.consumeLocked(n)
}

// consumeLocked runs in wire order after exact turn admission is known.
func (c *codexAdapter) consumeLocked(n codexappserver.Notification) error {
	p := c.p
	var identity struct {
		ThreadID   string          `json:"threadId"`
		TurnID     string          `json:"turnId"`
		ItemID     string          `json:"itemId"`
		IsBlocking bool            `json:"isBlocking"`
		Questions  json.RawMessage `json:"questions"`
		Turn       struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if json.Unmarshal(n.Params, &identity) != nil {
		return errors.New("malformed Codex notification")
	}
	if identity.ThreadID != "" && identity.ThreadID != p.session {
		return errors.New("codex thread changed")
	}
	if identity.Turn.ID != "" {
		identity.TurnID = identity.Turn.ID
	}
	if n.RequestID != "" {
		if identity.ThreadID != p.session || identity.TurnID == "" || identity.TurnID != p.turn {
			return ErrStale
		}
		kind := "permission"
		if n.Method == "item/tool/requestUserInput" {
			if identity.ItemID == "" || !identity.IsBlocking {
				return errors.New("unsupported Codex question")
			}
			if _, err := agentquestion.ParseCodexQuestions(identity.Questions); err != nil {
				return err
			}
			kind = "question"
		} else if _, ok, err := codexappserver.DecodeApprovalEnvelope(n); err != nil {
			return err
		} else if !ok {
			return errors.New("unsupported Codex server request")
		}
		// Scalar wire kind is part of identity: numeric 1 and string "1" differ.
		key := "number:" + n.RequestID
		if bytes.HasPrefix(bytes.TrimSpace(n.RawRequestID), []byte(`"`)) {
			key = "string:" + n.RequestID
		}
		if p.usedRequests[key] {
			return nil
		}
		if len(p.requests) >= p.host.limits.Requests || len(p.usedRequests) >= p.host.limits.Events || p.activeCriticalLocked() >= p.host.limits.Events {
			return ErrBusy
		}
		raw, _ := json.Marshal(n)
		if len(raw) > p.host.limits.FrameBytes {
			return errors.New("codex token exceeds host limit")
		}
		req := Request{ID: key, Connection: p.connection, Session: p.session, Turn: p.turn, Kind: kind, Tool: n.Method, Input: raw}
		p.requests[key] = req
		p.usedRequests[key] = true
		copy := cloneRequest(req)
		p.emitLocked("control-pending", nil, &copy)
		return nil
	}
	switch n.Method {
	case "thread/tokenUsage/updated", "thread/goal/updated":
		// Session telemetry may refer to the completed root turn, including
		// immediately after resume. It carries no turn or control authority.
		if identity.ThreadID != p.session {
			return ErrStale
		}
		p.emitLocked("provider-event", n.Params, nil)
	case "turn/started":
		if identity.ThreadID != p.session || identity.TurnID == "" || len(identity.TurnID) > 256 {
			return ErrStale
		}
		if p.turn == identity.TurnID {
			// The notification for a host-submitted turn can precede its reply.
			p.emitLocked("provider-event", n.Params, nil)
			return nil
		}
		if p.turn != "" || slices.Contains(c.providerTurns, identity.TurnID) {
			return ErrStale
		}
		if p.activeCriticalLocked() >= p.host.limits.Events {
			return ErrBusy
		}
		p.turn = identity.TurnID
		c.rememberProviderTurnLocked(p.turn)
		clear(p.usedRequests)
		p.emitLocked("provider-turn-started", n.Params, nil)
	case "turn/completed":
		if identity.ThreadID != p.session || identity.TurnID == "" || identity.TurnID != p.turn {
			return ErrStale
		}
		p.expireLocked()
		p.emitLocked("turn-result", n.Params, nil)
		p.turn, p.interrupt = "", ""
		p.interruptAck = false
		p.trimCriticalLocked()
	default:
		if identity.TurnID != "" && identity.TurnID != p.turn {
			return ErrStale
		}
		p.emitLocked("provider-event", n.Params, nil)
	}
	return nil
}

func (c *codexAdapter) rememberProviderTurnLocked(turn string) {
	if len(c.providerTurns) >= c.p.host.limits.Events {
		c.providerTurns = c.providerTurns[1:]
	}
	c.providerTurns = append(c.providerTurns, turn)
}
