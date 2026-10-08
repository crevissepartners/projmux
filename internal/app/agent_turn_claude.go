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
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
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
		operation, err := c.interruptProcessClaudeTurn(registry, agent)
		if err != nil {
			return err
		}
		return c.writeProcessTurn(stdout, agentActionInterruptTurn, agent, operation)
	}
	return c.interruptTmuxClaudeTurn(registry, agent, via, stdout)
}

// declaredProcessTarget returns an exact process target when the invocation
// inventory declares the Agent's Pane, so the tmux transport guard admits or
// refuses each tmux command against that declaration. Otherwise it is nil.
func (c *agentCommand) declaredProcessTarget(registry coremetadata.Registry, agent coremetadata.Agent) *processTerminalTarget {
	for _, key := range c.processRuntime.inventory().Declared {
		if key.Pane == agent.Status.PaneRef {
			return &processTerminalTarget{runtime: c.processRuntime, registry: registry, paneUID: agent.Status.PaneRef}
		}
	}
	return nil
}

func (c *agentCommand) interruptTmuxClaudeTurn(registry coremetadata.Registry, agent coremetadata.Agent, via string, stdout io.Writer) error {
	processTarget := c.declaredProcessTarget(registry, agent)
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
	if err := c.claudeTurnStillCurrent(agent, route, runtime, now); err != nil {
		return fail(err)
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

func (c *agentCommand) claudeTurnStillCurrent(agent coremetadata.Agent, route coremetadata.AgentRouteRef, runtime string, now func() time.Time) error {
	latest, err := c.loadRegistry()
	if err != nil {
		return fmt.Errorf("reload exact Agent: %w", err)
	}
	current, ok := latest.Agent(route.AgentUID)
	if !ok {
		return errors.New("exact Claude Agent disappeared")
	}
	currentRoute, currentRuntime, err := exactClaudeTurn(latest, *current, now())
	if err != nil || !route.Same(currentRoute) || currentRuntime != runtime ||
		current.Status.Interaction != agent.Status.Interaction ||
		current.Status.Progress.TurnRef != agent.Status.Progress.TurnRef ||
		current.Status.Progress.StartedAt != agent.Status.Progress.StartedAt {
		return errors.New("exact Claude Agent turn or Pane activation changed")
	}
	return nil
}

// Process starts enter the provider stream as plain user frames. Coordination
// messages retain their separate native message route and envelope contract.
func (c *agentCommand) startProcessClaudeTurn(reg coremetadata.Registry, agent coremetadata.Agent, text string, stdout io.Writer) (bool, error) {
	pane, found := reg.Pane(agent.Status.PaneRef)
	if agent.Spec.Provider != aiModeClaude || !found || pane.Spec.Runtime.EffectiveKind() != coremetadata.RuntimeProcess {
		return false, nil
	}
	operation, result, err := c.callProcessForegroundAction(reg, agent, aiModeClaude, "turn", text)
	if err != nil {
		return true, err
	}
	if err = processTurnAcceptance(result); err != nil {
		return true, err
	}
	if result.Join != nil {
		return true, c.writeJoinedProcessTurn(stdout, agentActionSendTurn, agent, operation, *result.Join)
	}
	return true, c.writeProcessTurn(stdout, agentActionSendTurn, agent, operation)
}

// Explicit typed targets retain their local authority; operational commands use
// the exact host socket and kernel birth checks.
func (c *agentCommand) interruptProcessClaudeTurn(registry coremetadata.Registry, agent coremetadata.Agent) (string, error) {
	if c.processRuntime != nil && c.processRuntime.controlOverride != nil {
		turn := agent.Status.Progress.TurnRef
		return turn, c.processRuntime.controlOverride(context.Background(), registry, agent.Status.PaneRef, resourcegraph.ProcessInterrupt, turn, "")
	}
	return c.callProcessTurn(registry, agent, aiModeClaude, "interrupt", "")
}
