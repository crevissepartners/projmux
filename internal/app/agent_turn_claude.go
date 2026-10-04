package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

const claudeTurnInterruptAuditName = "agent-turn-interrupt-audit.jsonl"

// The audit records a durable intent before the one allowed key delivery, then
// its outcome. A delivered result means tmux accepted the key, not that Claude
// completed cancellation. Via is deliberately a caller declaration.
type claudeTurnInterruptAudit struct {
	At       time.Time `json:"at"`
	Via      string    `json:"via"`
	AgentUID string    `json:"agentUid"`
	PaneUID  string    `json:"paneUid"`
	Runtime  string    `json:"runtimeId"`
	Result   string    `json:"result"`
	Reason   string    `json:"reason,omitempty"`
}

func writeClaudeTurnInterruptAudit(path string, entry claudeTurnInterruptAudit) error {
	if err := localstate.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	// #nosec G304 -- path is the private state directory and a fixed filename.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, localstate.PrivateFileMode)
	if err != nil {
		return err
	}
	n, err := file.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	localstate.RepairPrivateFile(path)
	return err
}

func exactClaudeTurn(registry coremetadata.Registry, agent coremetadata.Agent, now time.Time) (coremetadata.AgentRouteRef, string, error) {
	if agent.Spec.Provider != aiModeClaude {
		return coremetadata.AgentRouteRef{}, "", errors.New("claude turn interrupt unavailable: selected Agent is not Claude")
	}
	route, reason := coremetadata.ResolveAgentRoute(registry, agent.Metadata.UID)
	if reason != "" {
		return coremetadata.AgentRouteRef{}, "", fmt.Errorf("claude turn interrupt unavailable: %s", reason)
	}
	if agent.EffectiveInteraction(now).Kind != coremetadata.InteractionInProgress {
		return coremetadata.AgentRouteRef{}, "", errors.New("claude turn interrupt unavailable: current turn is not fresh in_progress")
	}
	pane, _ := registry.Pane(route.PaneUID)
	return route, pane.Status.Activation.RuntimeID, nil
}

// exactClaudePane reads the current tmux target through the UID mirror and
// verifies its runtime ID again on the exact target. The caller repeats this
// immediately before send-keys, so a replaced Pane cannot inherit the key.
func exactClaudePane(ctx context.Context, runner tmuxCommandRunner, paneUID, runtime string, processTargets ...*processTerminalTarget) (string, error) {
	for _, target := range processTargets {
		if err := target.admit(resourcegraph.ProcessKeys); err != nil {
			return "", err
		}
	}
	target, found, err := intmetadata.NewMirror(runner).FindPaneTargetForUID(ctx, paneUID)
	if err != nil {
		return "", fmt.Errorf("read live Pane: %w", err)
	}
	if !found {
		return "", errors.New("exact Claude Pane is not live")
	}
	format := tmuxRowFormat("#{pane_id}", "#{@projmux_pane_uid}", "#{pane_dead}")
	out, err := runner.Run(ctx, "tmux", "display-message", "-p", "-t", target, format)
	if err != nil {
		return "", fmt.Errorf("read exact Claude Pane: %w", err)
	}
	fields, err := parseClaudePaneFrame(out)
	if err != nil {
		return "", err
	}
	if fields[0] != runtime || fields[0] != target || fields[1] != paneUID || fields[2] != "0" {
		return "", errors.New("exact Claude Pane runtime, UID, or liveness changed")
	}
	return target, nil
}

func parseClaudePaneFrame(out []byte) ([]string, error) {
	frame := string(out)
	if before, ok := strings.CutSuffix(frame, "\n"); ok {
		frame = strings.TrimSuffix(before, "\r")
	}
	if strings.ContainsAny(frame, "\r\n") || (strings.Contains(frame, tmuxRowSep) && strings.Contains(frame, tmuxRowSepFormat)) {
		return nil, errors.New("exact Claude Pane frame is malformed")
	}
	sep := tmuxRowSepFormat
	if strings.Contains(frame, tmuxRowSep) {
		sep = tmuxRowSep
	} else if !strings.Contains(frame, tmuxRowSepFormat) {
		return nil, errors.New("exact Claude Pane frame is malformed")
	}
	fields := strings.Split(frame, sep)
	if len(fields) != 3 {
		return nil, errors.New("exact Claude Pane frame is malformed")
	}
	return fields, nil
}

// interruptClaudeTurn sends one Esc to a fresh in-progress Claude turn. via is
// the caller-reported client, already checked against the operatorclient rule,
// and the audit records it as received.
func (c *agentCommand) interruptClaudeTurn(registry coremetadata.Registry, agent coremetadata.Agent, via string, stdout io.Writer) error {
	if pane, ok := registry.Pane(agent.Status.PaneRef); ok && c.processRuntime.inventory().Declares(*pane) {
		operation, err := c.callProcessClaudeTurn(registry, agent, "interrupt", "")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "%s agent=uid:%s turn=%s runtime=process\n", c.agentActionText(agentActionInterruptTurn), agent.Metadata.UID, operation)
		return err
	}
	var processTarget *processTerminalTarget
	for _, key := range c.processRuntime.inventory().Declared {
		if key.Pane == agent.Status.PaneRef {
			processTarget = &processTerminalTarget{runtime: c.processRuntime, registry: registry, paneUID: agent.Status.PaneRef}
			break
		}
	}
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	route, runtime, err := exactClaudeTurn(registry, agent, now())
	if err != nil {
		return err
	}
	if c.controlRoute == nil || c.controlRunner == nil || c.controlPaths == nil {
		return errors.New("claude turn interrupt unavailable: exact tmux route or private state path is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.controlTimeoutValue())
	defer cancel()
	tmuxRoute, err := c.controlRoute(ctx)
	if err != nil {
		return fmt.Errorf("claude turn interrupt unavailable: resolve exact tmux socket: %w", err)
	}
	runner := explicitTmuxRunner{runner: c.controlRunner, target: tmuxRoute.target}
	if _, err := exactClaudePane(ctx, runner, route.PaneUID, runtime, processTarget); err != nil {
		return fmt.Errorf("claude turn interrupt unavailable: %w", err)
	}
	paths, err := c.controlPaths()
	if err != nil {
		return fmt.Errorf("claude turn interrupt unavailable: resolve audit path: %w", err)
	}
	auditPath := filepath.Join(paths.StateDir, claudeTurnInterruptAuditName)
	entry := claudeTurnInterruptAudit{At: now().UTC(), Via: via, AgentUID: route.AgentUID, PaneUID: route.PaneUID, Runtime: runtime, Result: "requested"}
	if err := writeClaudeTurnInterruptAudit(auditPath, entry); err != nil {
		return fmt.Errorf("claude turn interrupt refused before Esc: via=%s agent=uid:%s pane=uid:%s at=%s audit log %s: %w", via, route.AgentUID, route.PaneUID, entry.At.Format(time.RFC3339Nano), auditPath, err)
	}
	fail := func(cause error) error {
		entry.At, entry.Result, entry.Reason = now().UTC(), "failed", cause.Error()
		if auditErr := writeClaudeTurnInterruptAudit(auditPath, entry); auditErr != nil {
			return fmt.Errorf("claude turn interrupt failed: %w; could not write failure to audit log %s: %v", cause, auditPath, auditErr)
		}
		return fmt.Errorf("claude turn interrupt failed: %w", cause)
	}
	// The Registry can change between initial resolution and the durable audit.
	// Refuse an old turn or activation before addressing the tmux Pane again.
	latest, err := c.loadRegistry()
	if err != nil {
		return fail(fmt.Errorf("reload exact Agent: %w", err))
	}
	current, ok := latest.Agent(route.AgentUID)
	if !ok {
		return fail(errors.New("exact Claude Agent disappeared"))
	}
	currentRoute, currentRuntime, err := exactClaudeTurn(latest, *current, now())
	if err != nil || !route.Same(currentRoute) || currentRuntime != runtime ||
		current.Status.Interaction != agent.Status.Interaction ||
		current.Status.Progress.TurnRef != agent.Status.Progress.TurnRef ||
		current.Status.Progress.StartedAt != agent.Status.Progress.StartedAt {
		return fail(errors.New("exact Claude Agent turn or Pane activation changed"))
	}
	target, err := exactClaudePane(ctx, runner, route.PaneUID, runtime, processTarget)
	if err != nil {
		return fail(err)
	}
	if _, err := runner.Run(ctx, "tmux", "send-keys", "-t", target, "Escape"); err != nil {
		return fail(fmt.Errorf("send one Esc to exact Pane %s: %w", route.PaneUID, err))
	}
	entry.At, entry.Result = now().UTC(), "delivered"
	if err := writeClaudeTurnInterruptAudit(auditPath, entry); err != nil {
		return fmt.Errorf("esc delivered to Claude Agent uid:%s Pane uid:%s, but audit result write failed at %s: %w", route.AgentUID, route.PaneUID, auditPath, err)
	}
	_, err = fmt.Fprintf(stdout, "%s agent=uid:%s pane=uid:%s delivery=sent (cancellation unconfirmed)\n", c.agentActionText(agentActionInterruptTurn), route.AgentUID, route.PaneUID)
	return err
}

// Process starts enter the provider stream as plain user frames. Coordination
// messages retain their separate native message route and envelope contract.
func (c *agentCommand) startProcessClaudeTurn(reg coremetadata.Registry, agent coremetadata.Agent, text string, stdout io.Writer) (bool, error) {
	pane, found := reg.Pane(agent.Status.PaneRef)
	if agent.Spec.Provider != aiModeClaude || !found || pane.Spec.Runtime.EffectiveKind() != coremetadata.RuntimeProcess {
		return false, nil
	}
	operation, err := c.callProcessClaudeTurn(reg, agent, "turn", text)
	if err != nil {
		return true, err
	}
	_, err = fmt.Fprintf(stdout, "%s agent=uid:%s turn=%s runtime=process\n", c.agentActionText(agentActionSendTurn), agent.Metadata.UID, operation)
	return true, err
}

func (c *agentCommand) callProcessClaudeTurn(reg coremetadata.Registry, agent coremetadata.Agent, action, text string) (string, error) {
	pane, found := reg.Pane(agent.Status.PaneRef)
	if !found || pane.Status.ProcessSession == nil || agent.Spec.Provider != aiModeClaude || c.controlPaths == nil {
		return "", processhost.ErrStale
	}
	session := pane.Status.ProcessSession
	activation, provider, current := reg.CurrentProcessActivation(session.Binding)
	if !current || provider != aiModeClaude {
		return "", processhost.ErrStale
	}
	paths, err := c.controlPaths()
	if err != nil {
		return "", err
	}
	socket := processClaudeHostSocket(intmetadata.PathFor(paths.StateDir), pane.Metadata.UID, session.Binding.Generation)
	identity, err := localipc.InspectOwnedSocket(socket)
	if err != nil {
		return "", fmt.Errorf("process-host-unavailable: %w", err)
	}
	binding := session.Binding
	authority := processhost.Authority{Binding: processhost.Binding{Host: binding.HostInstanceID, Project: binding.ProjectUID, Window: binding.WindowUID, Agent: binding.AgentUID, Pane: binding.PaneUID, Generation: binding.Generation, Operation: binding.OperationID}, Connection: binding.OperationID, Session: session.SessionID}
	operation, err := newCreateOperationID()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.controlTimeoutValue())
	defer cancel()
	result, err := callProcessForeground(ctx, socket, identity, activation.HostProcess, claudeProcessCheck{Foreground: &processForegroundRequest{Authority: authority, Action: action, Operation: operation, Prompt: text, Turn: session.TurnID}})
	if err != nil {
		return "", fmt.Errorf("process-host-unavailable: %w", err)
	}
	return operation, processClaudeTurnAcceptance(result)
}

func processClaudeTurnAcceptance(result processForegroundResult) error {
	switch {
	case result.Accepted:
		return nil
	case result.Busy:
		return processhost.ErrBusy
	case result.Closed:
		return processhost.ErrClosed
	default:
		return processhost.ErrStale
	}
}
