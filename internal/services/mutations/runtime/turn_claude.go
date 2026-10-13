package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	localstate "github.com/crevissepartners/projmux/internal/state"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ClaudeTurnInterrupter struct {
	StateDir string
	Runner   intmetadata.Runner
	Load     func() (coremetadata.Registry, error)
	Now      func() time.Time
	Guards   []func() error
}

const ClaudeTurnInterruptAuditName = "agent-turn-interrupt-audit.jsonl"

// The audit records a durable intent before the one allowed key delivery, then
// its outcome. A delivered result means tmux accepted the key, not that Claude
// completed cancellation. Via is deliberately a caller declaration.
type ClaudeTurnInterruptAudit struct {
	At       time.Time `json:"at"`
	Via      string    `json:"via"`
	AgentUID string    `json:"agentUid"`
	PaneUID  string    `json:"paneUid"`
	Runtime  string    `json:"runtimeId"`
	Result   string    `json:"result"`
	Reason   string    `json:"reason,omitempty"`
}

func WriteClaudeTurnInterruptAudit(path string, entry ClaudeTurnInterruptAudit) error {
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

func ExactClaudeTurn(registry coremetadata.Registry, agent coremetadata.Agent, now time.Time) (coremetadata.AgentRouteRef, string, error) {
	if agent.Spec.Provider != "claude" {
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

// ExactClaudePane reads the current tmux target through the UID mirror and
// verifies its runtime ID again on the exact target. The caller repeats this
// immediately before send-keys, so a replaced Pane cannot inherit the key.
func ExactClaudePane(ctx context.Context, runner intmetadata.Runner, paneUID, runtime string, guards ...func() error) (string, error) {
	for _, guard := range guards {
		if err := guard(); err != nil {
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
	format := strings.Join([]string{"#{pane_id}", "#{@projmux_pane_uid}", "#{pane_dead}"}, "\\037")
	out, err := runner.Run(ctx, "tmux", "display-message", "-p", "-t", target, format)
	if err != nil {
		return "", fmt.Errorf("read exact Claude Pane: %w", err)
	}
	fields, err := ParseClaudePaneFrame(out)
	if err != nil {
		return "", err
	}
	if fields[0] != runtime || fields[0] != target || fields[1] != paneUID || fields[2] != "0" {
		return "", errors.New("exact Claude Pane runtime, UID, or liveness changed")
	}
	return target, nil
}

func ParseClaudePaneFrame(out []byte) ([]string, error) {
	frame := string(out)
	if before, ok := strings.CutSuffix(frame, "\n"); ok {
		frame = strings.TrimSuffix(before, "\r")
	}
	if strings.ContainsAny(frame, "\r\n") || (strings.Contains(frame, "\x1f") && strings.Contains(frame, "\\037")) {
		return nil, errors.New("exact Claude Pane frame is malformed")
	}
	sep := "\\037"
	if strings.Contains(frame, "\x1f") {
		sep = "\x1f"
	} else if !strings.Contains(frame, "\\037") {
		return nil, errors.New("exact Claude Pane frame is malformed")
	}
	fields := strings.Split(frame, sep)
	if len(fields) != 3 {
		return nil, errors.New("exact Claude Pane frame is malformed")
	}
	return fields, nil
}

func (c ClaudeTurnInterrupter) Interrupt(parent context.Context, registry coremetadata.Registry, agent coremetadata.Agent, via string) (out TurnResult, resultErr error) {
	out = TurnResult{AgentUID: agent.Metadata.UID, PaneUID: agent.Status.PaneRef, Mode: "interrupt", TurnID: agent.Status.Progress.TurnRef}
	if pane, ok := registry.Pane(agent.Status.PaneRef); ok {
		out.Generation = pane.Status.Activation.Generation
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	route, runtime, err := ExactClaudeTurn(registry, agent, now())
	if err != nil {
		return out, err
	}
	if c.Runner == nil || c.StateDir == "" || c.Load == nil {
		return out, errors.New("claude turn interrupt unavailable: exact tmux route or private state path is not configured")
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	runner := c.Runner
	if _, err := ExactClaudePane(ctx, runner, route.PaneUID, runtime, c.Guards...); err != nil {
		return out, fmt.Errorf("claude turn interrupt unavailable: %w", err)
	}
	auditPath := filepath.Join(c.StateDir, ClaudeTurnInterruptAuditName)
	entry := ClaudeTurnInterruptAudit{At: now().UTC(), Via: via, AgentUID: route.AgentUID, PaneUID: route.PaneUID, Runtime: runtime, Result: "requested"}
	if err := WriteClaudeTurnInterruptAudit(auditPath, entry); err != nil {
		return out, fmt.Errorf("claude turn interrupt refused before Esc: via=%s agent=uid:%s pane=uid:%s at=%s audit log %s: %w", via, route.AgentUID, route.PaneUID, entry.At.Format(time.RFC3339Nano), auditPath, err)
	}
	fail := func(cause error) error {
		entry.At, entry.Result, entry.Reason = now().UTC(), "failed", cause.Error()
		if auditErr := WriteClaudeTurnInterruptAudit(auditPath, entry); auditErr != nil {
			return fmt.Errorf("claude turn interrupt failed: %w; could not write failure to audit log %s: %v", cause, auditPath, auditErr)
		}
		return fmt.Errorf("claude turn interrupt failed: %w", cause)
	}
	// The Registry can change between initial resolution and the durable audit.
	// Refuse an old turn or activation before addressing the tmux Pane again.
	if err := c.StillCurrent(agent, route, runtime, now); err != nil {
		return out, fail(err)
	}
	target, err := ExactClaudePane(ctx, runner, route.PaneUID, runtime, c.Guards...)
	if err != nil {
		return out, fail(err)
	}
	out.OutcomeUnknown = true
	if _, err := runner.Run(ctx, "tmux", "send-keys", "-t", target, "Escape"); err != nil {
		return out, fail(fmt.Errorf("send one Esc to exact Pane %s: %w", route.PaneUID, err))
	}
	out.Accepted, out.Delivery, out.OutcomeUnknown = true, "sent", false
	entry.At, entry.Result = now().UTC(), "delivered"
	if err := WriteClaudeTurnInterruptAudit(auditPath, entry); err != nil {
		return out, fmt.Errorf("esc delivered to Claude Agent uid:%s Pane uid:%s, but audit result write failed at %s: %w", route.AgentUID, route.PaneUID, auditPath, err)
	}
	return out, nil
}

func (c ClaudeTurnInterrupter) StillCurrent(agent coremetadata.Agent, route coremetadata.AgentRouteRef, runtime string, now func() time.Time) error {
	latest, err := c.Load()
	if err != nil {
		return fmt.Errorf("reload exact Agent: %w", err)
	}
	current, ok := latest.Agent(route.AgentUID)
	if !ok {
		return errors.New("exact Claude Agent disappeared")
	}
	currentRoute, currentRuntime, err := ExactClaudeTurn(latest, *current, now())
	if err != nil || !route.Same(currentRoute) || currentRuntime != runtime ||
		current.Status.Interaction != agent.Status.Interaction ||
		current.Status.Progress.TurnRef != agent.Status.Progress.TurnRef ||
		current.Status.Progress.StartedAt != agent.Status.Progress.StartedAt {
		return errors.New("exact Claude Agent turn or Pane activation changed")
	}
	return nil
}
