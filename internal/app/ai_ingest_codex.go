package app

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/notify"
)

type codexHookPayload struct {
	EventName      string
	ThreadID       string
	SessionID      string
	TurnID         string
	CWD            string
	TranscriptPath string
	Model          string
	ToolName       string
	ToolInput      map[string]any
}

func (c *aiCommand) ingestCodexHook(data []byte) error {
	payload, err := parseCodexHookPayload(data)
	if err != nil {
		c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Result: "error", Reason: err.Error()})
		return err
	}

	paneID := c.matchAIPane(aiPaneMatchInput{
		CWD:       payload.CWD,
		ThreadID:  payload.matchThreadID(),
		SessionID: payload.SessionID,
	})
	if paneID == "" {
		c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "ignored", Reason: "no matching pane", CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
		return nil
	}

	metadata := payload.codexHookMetadata()
	action := c.aiHookEffectiveAction(aiHookProviderCodex, payload.EventName)
	switch payload.EventName {
	case "UserPromptSubmit":
		if action.Action == aiHookActionQuiet {
			c.quietCodexHook(paneID, payload, aiHookQuietReason(action))
			return nil
		}
		c.markAIHookPane(paneID, aiModeCodex, payload.CWD, payload.matchThreadID(), payload.SessionID, "")
		if err := c.applyAIStatusWithNotify("thinking", paneID, attentionNotifyInput{
			Metadata:  metadata,
			BadgeKind: aiBadgeKindInProgress,
		}); err != nil {
			c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
			return err
		}
		c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "state", Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
		return nil
	case "Stop":
		if action.Action == aiHookActionQuiet {
			c.quietCodexHook(paneID, payload, aiHookQuietReason(action))
			return nil
		}
		c.markAIHookPane(paneID, aiModeCodex, payload.CWD, payload.matchThreadID(), payload.SessionID, "")
		body := formatCodexHookStopNotifyBody(payload)
		if action.Action == aiHookActionState {
			if err := c.applyAIStatusStateOnly("waiting", paneID, attentionNotifyInput{
				ID:        codexHookNotifyID(payload, "stop"),
				Text:      body.Text,
				Severity:  body.Severity,
				Metadata:  mergeAINotifyBodyMetadata(metadata, body),
				Force:     true,
				BadgeKind: aiBadgeKindResponseComplete,
			}); err != nil {
				c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
				return err
			}
			c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "state", Reason: aiHookStateReason(action), Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
			return nil
		}
		if err := c.applyAIStatusWithNotify("waiting", paneID, attentionNotifyInput{
			ID:        codexHookNotifyID(payload, "stop"),
			Text:      body.Text,
			Severity:  body.Severity,
			Metadata:  mergeAINotifyBodyMetadata(metadata, body),
			Force:     true,
			BadgeKind: aiBadgeKindResponseComplete,
		}); err != nil {
			c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
			return err
		}
		c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "notify", Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
		return nil
	case "PermissionRequest":
		if action.Action == aiHookActionQuiet {
			c.quietCodexHook(paneID, payload, aiHookQuietReason(action))
			return nil
		}
		c.markAIHookPane(paneID, aiModeCodex, payload.CWD, payload.matchThreadID(), payload.SessionID, "")
		body := formatCodexHookPermissionNotifyBody(payload)
		if action.Action == aiHookActionState {
			if err := c.applyAIStatusStateOnly("waiting", paneID, attentionNotifyInput{
				ID:        codexHookNotifyID(payload, "permission"),
				Text:      body.Text,
				Severity:  body.Severity,
				Metadata:  mergeAINotifyBodyMetadata(metadata, body),
				Force:     true,
				BadgeKind: aiBadgeKindApprovalRequired,
			}); err != nil {
				c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
				return err
			}
			c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "state", Reason: aiHookStateReason(action), Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
			return nil
		}
		if err := c.applyAIStatusWithNotify("waiting", paneID, attentionNotifyInput{
			ID:        codexHookNotifyID(payload, "permission"),
			Text:      body.Text,
			Severity:  body.Severity,
			Metadata:  mergeAINotifyBodyMetadata(metadata, body),
			Force:     true,
			BadgeKind: aiBadgeKindApprovalRequired,
		}); err != nil {
			c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
			return err
		}
		c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "notify", Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
		return nil
	case "PreToolUse", "PostToolUse", "PreCompact", "PostCompact", "SessionStart":
		if c.shouldPushGenericCodexHookNotify(action) {
			if err := c.pushGenericCodexHookNotify(paneID, payload); err != nil {
				c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
				return err
			}
			c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "notify", Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
			return nil
		}
		c.quietCodexHook(paneID, payload, aiHookNoHandlerReason(action))
		return nil
	default:
		c.quietCodexHook(paneID, payload, aiHookNoHandlerReason(action))
		return nil
	}
}

func (c *aiCommand) quietCodexHook(paneID string, payload codexHookPayload, reason string) {
	c.markAIHookPane(paneID, aiModeCodex, payload.CWD, payload.matchThreadID(), payload.SessionID, "")
	c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "quiet", Reason: reason, Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
}

func (c *aiCommand) shouldPushGenericCodexHookNotify(action aiHookActionResolution) bool {
	return action.Action == aiHookActionNotify && action.Source == aiHookActionSourceRuntime
}

func (c *aiCommand) pushGenericCodexHookNotify(paneID string, payload codexHookPayload) error {
	c.markAIHookPane(paneID, aiModeCodex, payload.CWD, payload.matchThreadID(), payload.SessionID, "")
	body := formatCodexGenericHookNotifyBody(payload)
	return c.applyAIStatusQueueOnly("waiting", paneID, attentionNotifyInput{
		ID:            codexHookNotifyID(payload, "generic"),
		Text:          body.Text,
		Severity:      body.Severity,
		Metadata:      mergeAINotifyBodyMetadata(payload.codexGenericHookMetadata(), body),
		Force:         true,
		SuppressHooks: true,
	})
}

func parseCodexHookPayload(data []byte) (codexHookPayload, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return codexHookPayload{}, fmt.Errorf("parse codex hook payload: %w", err)
	}
	payload := codexHookPayload{
		EventName:      firstString(raw, "hook_event_name", "event_name"),
		ThreadID:       firstString(raw, "thread_id", "thread-id"),
		SessionID:      firstString(raw, "session_id", "session-id"),
		TurnID:         firstString(raw, "turn_id", "turn-id"),
		CWD:            firstString(raw, "cwd", "workspace", "project_dir"),
		TranscriptPath: firstString(raw, "transcript_path", "transcriptPath"),
		Model:          firstString(raw, "model"),
		ToolName:       firstString(raw, "tool_name", "toolName"),
	}
	if payload.CWD == "" {
		payload.CWD = firstNestedString(raw["workspace"], "cwd", "path")
	}
	if payload.ToolName == "" {
		payload.ToolName = firstNestedString(raw["tool"], "name", "tool_name")
	}
	payload.ToolInput = mapFromAny(raw["tool_input"])
	if len(payload.ToolInput) == 0 {
		payload.ToolInput = mapFromAny(raw["input"])
	}
	if len(payload.ToolInput) == 0 {
		payload.ToolInput = mapFromAny(raw["tool"])
		delete(payload.ToolInput, "name")
		delete(payload.ToolInput, "tool_name")
	}
	return payload, nil
}

func (p codexHookPayload) codexHookMetadata() map[string]string {
	metadata := map[string]string{
		notify.MetaAgent:  aiModeCodex,
		notify.MetaEvent:  p.EventName,
		"session_id":      p.SessionID,
		"thread_id":       p.matchThreadID(),
		"turn_id":         p.TurnID,
		"cwd":             p.CWD,
		"transcript_path": p.TranscriptPath,
		"model":           p.Model,
		"tool_name":       p.ToolName,
	}
	for key, value := range p.ToolInput {
		if text := stringFromAny(value); text != "" {
			metadata["tool_input."+key] = truncateRunes(text, 160)
		}
	}
	out := make(map[string]string, len(metadata))
	for k, v := range metadata {
		if value := strings.TrimSpace(v); value != "" {
			out[k] = value
		}
	}
	return out
}

func (p codexHookPayload) codexGenericHookMetadata() map[string]string {
	metadata := map[string]string{
		"provider":       aiHookProviderCodex,
		notify.MetaAgent: aiModeCodex,
		notify.MetaEvent: p.EventName,
		"tool":           p.ToolName,
		"tool_name":      p.ToolName,
		"cwd":            p.CWD,
		"thread_id":      p.matchThreadID(),
		"session_id":     p.SessionID,
		"turn_id":        p.TurnID,
		"model":          p.Model,
	}
	out := make(map[string]string, len(metadata))
	for k, v := range metadata {
		if value := strings.TrimSpace(v); value != "" {
			out[k] = value
		}
	}
	return out
}

func (p codexHookPayload) matchThreadID() string {
	if threadID := strings.TrimSpace(p.ThreadID); threadID != "" {
		return threadID
	}
	return p.SessionID
}

func codexHookNotifyID(p codexHookPayload, kind string) string {
	parts := []string{"ai", "codex", kind}
	if value := strings.TrimSpace(p.SessionID); value != "" {
		parts = append(parts, value)
	}
	if value := strings.TrimSpace(p.TurnID); value != "" {
		parts = append(parts, value)
	}
	if kind == "permission" {
		if value := strings.TrimSpace(p.ToolName); value != "" {
			parts = append(parts, value)
		}
		if summary := formatCodexToolInputSummary(p.ToolName, p.ToolInput); summary != "" {
			parts = append(parts, truncateRunes(summary, 40))
		}
	}
	return strings.Join(parts, ":")
}
