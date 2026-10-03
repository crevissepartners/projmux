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
func CodexCommand(path, dir string, env, resolvedArgs []string) (Command, error) {
	for _, arg := range resolvedArgs {
		for _, reserved := range []string{"app-server", "daemon", "proxy", "--listen", "--stdio", "--code-mode-host", "--"} {
			if arg == reserved || strings.HasPrefix(arg, reserved+"=") {
				return Command{}, fmt.Errorf("reserved app-server argument: %s", arg)
			}
		}
	}
	return Command{Path: path, Dir: dir, Env: slices.Clone(env), Args: append([]string{"app-server", "--listen", "stdio://"}, resolvedArgs...)}, nil
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

func (h *Host) StartCodex(ctx context.Context, launch Launch, config CodexConfig) (*CodexHandle, error) {
	launch.adapter = config
	p, err := h.Start(ctx, launch)
	if p == nil {
		return nil, err
	}
	return &CodexHandle{handle: p}, err
}
func (c *CodexHandle) Observe(b Binding) (Snapshot, error) { return c.handle.Observe(b) }
func (c *CodexHandle) Events(b Binding, after uint64) ([]Event, Snapshot, error) {
	return c.handle.Events(b, after)
}
func (c *CodexHandle) Stop(b Binding) error { return c.handle.Stop(b) }
func (c *CodexHandle) Wait(ctx context.Context, b Binding) (Snapshot, error) {
	return c.handle.Wait(ctx, b)
}
func (c *CodexHandle) Turn(ctx context.Context, a Authority, operation, prompt string) error {
	return c.handle.adapter.(*codexAdapter).turn(ctx, a, operation, prompt)
}
func (c *CodexHandle) Interrupt(ctx context.Context, a Authority, turn string) error {
	return c.handle.adapter.(*codexAdapter).interrupt(ctx, a, turn)
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
	// Serializes typed controls and projections so notifications preceding a
	// turn/start answer cannot outrun the exact turn ID returned by that answer.
	control chan struct{}
}

func (cfg CodexConfig) clone() adapterConfig { cfg.Roots = slices.Clone(cfg.Roots); return cfg }
func (cfg CodexConfig) newAdapter(p *Handle) providerAdapter {
	return &codexAdapter{p: p, control: make(chan struct{}, 1)}
}

func (c *codexAdapter) attach(stream io.ReadWriteCloser) { c.client = codexappserver.NewClient(stream) }
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
	thread, err := c.client.StartThreadWithSettings(ctx, c.p.launch.Command.Dir, cfg.Roots, cfg.DeveloperInstructions, cfg.Settings)
	if err != nil {
		return err
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
	return nil
}
func (c *codexAdapter) turn(ctx context.Context, a Authority, operation, prompt string) error {
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
	if operation == "" || len(operation) > 256 || p.usedTurns[operation] {
		p.mu.Unlock()
		return ErrStale
	}
	if p.turn != "" || len(p.usedTurns) >= p.host.limits.Events || len(p.critical) >= p.host.limits.Events {
		p.mu.Unlock()
		return ErrBusy
	}
	// Consume operation before the write. Even a refused operation cannot be
	// replayed; an uncertain outcome still terminates the owned connection.
	p.usedTurns[operation] = true
	p.mu.Unlock()
	settings := p.launch.adapter.(CodexConfig).Settings
	turn, err := c.client.StartTurnWithOptions(ctx, a.Session, prompt, operation, settings.Model, settings.Effort)
	if err != nil {
		if codexappserver.IsResponseError(err) {
			p.mu.Lock()
			raw, _ := json.Marshal(map[string]string{"status": "failed", "operation": operation, "reason": "server-refused"})
			p.emitLocked("turn-result", raw, nil)
			p.mu.Unlock()
		} else {
			p.protocolFailure(err)
		}
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state != "ready" {
		return ErrClosed
	}
	p.turn = turn
	p.emitLocked("turn-submitted", nil, nil)
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
	p.mu.Unlock()
	if _, err := codexappserver.InterruptExactTurnOn(ctx, c.client, a.Session, turn); err != nil {
		if codexappserver.IsResponseError(err) {
			p.mu.Lock()
			p.emitLocked("interrupt-refused", nil, nil)
			p.mu.Unlock()
		} else {
			p.protocolFailure(err)
		}
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state != "ready" {
		return ErrClosed
	}
	p.interruptAck = true
	p.emitLocked("interrupt-ack", nil, nil)
	return nil
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
		ctx, cancel := context.WithTimeout(context.Background(), c.p.host.limits.Startup)
		err := c.lock(ctx)
		cancel()
		if err == nil {
			err = c.consume(n)
			c.unlock()
		}
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
	if p.state != "ready" {
		return nil
	}
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
		if len(p.requests) >= p.host.limits.Requests || len(p.usedRequests) >= p.host.limits.Events || len(p.critical) >= p.host.limits.Events {
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
	case "turn/completed":
		if identity.ThreadID != p.session || identity.TurnID == "" || identity.TurnID != p.turn {
			return ErrStale
		}
		p.expireLocked()
		p.emitLocked("turn-result", n.Params, nil)
		p.turn, p.interrupt = "", ""
		p.interruptAck = false
	default:
		if identity.TurnID != "" && identity.TurnID != p.turn {
			return ErrStale
		}
		p.emitLocked("provider-event", n.Params, nil)
	}
	return nil
}
