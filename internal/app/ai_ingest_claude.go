package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/notify"
)

const claudeTranscriptTailLimit = 256 * 1024

type claudeHookPayload struct {
	EventName        string
	SessionID        string
	CWD              string
	TranscriptPath   string
	NotificationType string
	Message          string
	Prompt           string
	ToolName         string
	ToolUseID        string
	ToolInput        map[string]any
	ErrorType        string
	ErrorMessage     string
	SubagentType     string
	SubagentID       string
	TeammateName     string
	TeammateID       string
	TeammateContext  string
}

func (c *aiCommand) ingestClaudeHook(data []byte) error {
	payload, err := parseClaudeHookPayload(data)
	if err != nil {
		c.appendAIIngestLog(aiIngestLogEntry{Source: "claude-hook", Result: "error", Reason: err.Error()})
		return err
	}

	paneID := c.matchAIPane(aiPaneMatchInput{
		CWD:       payload.CWD,
		SessionID: payload.SessionID,
	})
	if paneID == "" {
		c.appendAIIngestLog(aiIngestLogEntry{Source: "claude-hook", Event: payload.EventName, Result: "ignored", Reason: "no matching pane", CWD: payload.CWD, SessionID: payload.SessionID})
		return nil
	}
	defer c.flushPendingAgentSessionRef(paneID)

	c.markAIHookPane(paneID, aiModeClaude, payload.CWD, "", payload.SessionID, payload.TranscriptPath)
	metadata := payload.claudeMetadata()
	action := c.aiHookEffectiveAction(aiHookProviderClaude, payload.EventName)

	switch payload.EventName {
	case "UserPromptSubmit":
		return c.ingestClaudeUserPromptSubmit(paneID, payload, metadata, action)
	case "Notification":
		return c.ingestClaudeNotification(paneID, payload, metadata, action)
	case "PermissionRequest":
		return c.ingestClaudePermissionRequest(paneID, payload, metadata, action)
	case "Stop":
		return c.ingestClaudeStop(paneID, payload, metadata, action)
	case "StopFailure":
		return c.ingestClaudeStopFailure(paneID, payload, metadata, action)
	case "SubagentStop":
		return c.ingestClaudeSubagentStop(paneID, payload, metadata, action)
	case "PreToolUse", "PostToolUse", "PostToolUseFailure", "PostToolBatch", "PermissionDenied", "UserPromptExpansion", "SessionStart", "SubagentStart", "PreCompact", "PostCompact", "SessionEnd", "Setup", "TaskCreated", "TaskCompleted", "Elicitation", "ElicitationResult", "ConfigChange", "InstructionsLoaded", "WorktreeCreate", "WorktreeRemove", "CwdChanged", "FileChanged":
		c.quietClaudeHook(paneID, payload, aiHookNoHandlerReason(action))
		return nil
	case "TeammateIdle":
		return c.ingestClaudeTeammateIdle(paneID, payload, metadata, action)
	default:
		c.quietClaudeHook(paneID, payload, aiHookNoHandlerReason(action))
		return nil
	}
}

func (c *aiCommand) ingestClaudeUserPromptSubmit(paneID string, payload claudeHookPayload, metadata map[string]string, action aiHookActionResolution) error {
	if action.Action == aiHookActionQuiet {
		c.quietClaudeHook(paneID, payload, aiHookQuietReason(action))
		return nil
	}
	if err := c.applyAIStatusWithNotify("thinking", paneID, attentionNotifyInput{
		Metadata:  metadata,
		BadgeKind: aiBadgeKindInProgress,
	}); err != nil {
		c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "error", err.Error()))
		return err
	}
	c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "state", ""))
	return nil
}

func (c *aiCommand) ingestClaudeNotification(paneID string, payload claudeHookPayload, metadata map[string]string, action aiHookActionResolution) error {
	if action.Action == aiHookActionQuiet {
		c.quietClaudeHook(paneID, payload, aiHookQuietReason(action))
		return nil
	}
	body := formatClaudeNotificationNotifyBody(payload)
	return c.emitClaudeHookStatus(paneID, payload, action, attentionNotifyInput{
		ID:        claudeNotifyID(payload),
		Text:      body.Text,
		Severity:  body.Severity,
		Metadata:  mergeAINotifyBodyMetadata(metadata, body),
		Force:     true,
		BadgeKind: aiBadgeKindForNotifyCategory(body.Category),
	})
}

func (c *aiCommand) ingestClaudePermissionRequest(paneID string, payload claudeHookPayload, metadata map[string]string, action aiHookActionResolution) error {
	if action.Action == aiHookActionQuiet {
		c.quietClaudeHook(paneID, payload, aiHookQuietReason(action))
		return nil
	}
	body := formatClaudePermissionNotifyBody(payload)
	return c.emitClaudeHookStatus(paneID, payload, action, attentionNotifyInput{
		ID:        claudePermissionNotifyID(payload),
		Text:      body.Text,
		Severity:  body.Severity,
		Metadata:  mergeAINotifyBodyMetadata(metadata, body),
		Force:     true,
		BadgeKind: aiBadgeKindApprovalRequired,
	})
}

func (c *aiCommand) ingestClaudeStop(paneID string, payload claudeHookPayload, metadata map[string]string, action aiHookActionResolution) error {
	if action.Action == aiHookActionQuiet {
		c.quietClaudeHook(paneID, payload, aiHookQuietReason(action))
		return nil
	}
	message := readClaudeTranscriptLastAssistantText(payload.TranscriptPath)
	body := formatClaudeStopNotifyBody(message)
	return c.emitClaudeHookStatus(paneID, payload, action, attentionNotifyInput{
		ID:        claudeStopNotifyID(payload),
		Text:      body.Text,
		Severity:  body.Severity,
		Metadata:  mergeAINotifyBodyMetadata(metadata, body),
		Force:     true,
		BadgeKind: aiBadgeKindResponseComplete,
	})
}

func (c *aiCommand) ingestClaudeStopFailure(paneID string, payload claudeHookPayload, metadata map[string]string, action aiHookActionResolution) error {
	if action.Action == aiHookActionQuiet {
		c.quietClaudeHook(paneID, payload, aiHookQuietReason(action))
		return nil
	}
	body := formatClaudeStopFailureNotifyBody(payload)
	return c.emitClaudeHookStatus(paneID, payload, action, attentionNotifyInput{
		ID:       claudeExtraNotifyID(payload, "stop-failure", payload.ErrorType, payload.ErrorMessage),
		Text:     body.Text,
		Severity: body.Severity,
		Metadata: mergeAINotifyBodyMetadata(metadata, body),
		Force:    true,
	})
}

func (c *aiCommand) ingestClaudeSubagentStop(paneID string, payload claudeHookPayload, metadata map[string]string, action aiHookActionResolution) error {
	if action.Action == aiHookActionNotify {
		body := formatClaudeSubagentStopNotifyBody(payload)
		if err := c.applyAIStatusWithNotify("waiting", paneID, attentionNotifyInput{
			ID:       claudeExtraNotifyID(payload, "subagent-stop", payload.SubagentType, payload.SubagentID),
			Text:     body.Text,
			Severity: body.Severity,
			Metadata: mergeAINotifyBodyMetadata(metadata, body),
			Force:    true,
		}); err != nil {
			c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "error", err.Error()))
			return err
		}
		c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "notify", ""))
		return nil
	}
	if action.Action == aiHookActionState {
		if err := c.applyAIStatusStateOnly("waiting", paneID, attentionNotifyInput{
			ID:       claudeExtraNotifyID(payload, "subagent-stop", payload.SubagentType, payload.SubagentID),
			Text:     formatClaudeSubagentStopNotifyBody(payload).Text,
			Severity: notify.SeverityInfo,
			Metadata: mergeAINotifyBodyMetadata(metadata, formatClaudeSubagentStopNotifyBody(payload)),
			Force:    true,
		}); err != nil {
			c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "error", err.Error()))
			return err
		}
		c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "state", aiHookStateReason(action)))
		return nil
	}
	c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "quiet", "high-volume event"))
	return nil
}

func (c *aiCommand) ingestClaudeTeammateIdle(paneID string, payload claudeHookPayload, metadata map[string]string, action aiHookActionResolution) error {
	if action.Action == aiHookActionQuiet {
		c.quietClaudeHook(paneID, payload, aiHookQuietReason(action))
		return nil
	}
	body := formatClaudeTeammateIdleNotifyBody(payload)
	return c.emitClaudeHookStatus(paneID, payload, action, attentionNotifyInput{
		ID:        claudeExtraNotifyID(payload, "teammate-idle", payload.TeammateName, payload.TeammateID, payload.TeammateContext),
		Text:      body.Text,
		Severity:  body.Severity,
		Metadata:  mergeAINotifyBodyMetadata(metadata, body),
		Force:     true,
		BadgeKind: aiBadgeKindResponseComplete,
	})
}

// emitClaudeHookStatus applies the state-only vs state+notify split shared by
// most claude hook handlers and writes the matching ingest log entry.
func (c *aiCommand) emitClaudeHookStatus(paneID string, payload claudeHookPayload, action aiHookActionResolution, input attentionNotifyInput) error {
	if action.Action == aiHookActionState {
		if err := c.applyAIStatusStateOnly("waiting", paneID, input); err != nil {
			c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "error", err.Error()))
			return err
		}
		c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "state", aiHookStateReason(action)))
		return nil
	}
	if err := c.applyAIStatusWithNotify("waiting", paneID, input); err != nil {
		c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "error", err.Error()))
		return err
	}
	c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "notify", ""))
	return nil
}

func claudeHookLogEntry(paneID string, payload claudeHookPayload, result, reason string) aiIngestLogEntry {
	return aiIngestLogEntry{Source: "claude-hook", Event: payload.EventName, Result: result, Reason: reason, Pane: paneID, CWD: payload.CWD, SessionID: payload.SessionID}
}

func (c *aiCommand) quietClaudeHook(paneID string, payload claudeHookPayload, reason string) {
	c.markAIHookPane(paneID, aiModeClaude, payload.CWD, "", payload.SessionID, payload.TranscriptPath)
	c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "quiet", reason))
}

func parseClaudeHookPayload(data []byte) (claudeHookPayload, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return claudeHookPayload{}, fmt.Errorf("parse claude hook payload: %w", err)
	}
	payload := claudeHookPayload{
		EventName:        firstString(raw, "hook_event_name", "event_name"),
		SessionID:        firstString(raw, "session_id", "session-id"),
		CWD:              firstString(raw, "cwd", "workspace", "project_dir"),
		TranscriptPath:   firstString(raw, "transcript_path", "transcriptPath"),
		NotificationType: firstString(raw, "notification_type", "notificationType"),
		Message:          firstString(raw, "message", "text"),
		Prompt:           firstString(raw, "prompt", "user_prompt"),
		ToolName:         firstString(raw, "tool_name", "toolName"),
		ToolUseID:        firstString(raw, "tool_use_id", "toolUseID", "id"),
		ErrorType:        firstString(raw, "error_type", "errorType", "failure_type", "failureType"),
		ErrorMessage:     firstString(raw, "error_message", "errorMessage", "message", "reason"),
		SubagentType:     firstString(raw, "subagent_type", "subagentType", "agent_type", "agentType"),
		SubagentID:       firstString(raw, "subagent_id", "subagentId", "agent_id", "agentId"),
		TeammateName:     firstString(raw, "teammate_name", "teammateName", "teammate"),
		TeammateID:       firstString(raw, "teammate_id", "teammateId"),
		TeammateContext:  firstString(raw, "teammate_context", "teammateContext", "context", "reason", "message"),
	}
	if payload.CWD == "" {
		payload.CWD = firstNestedString(raw["workspace"], "cwd", "path")
	}
	if payload.Message == "" {
		payload.Message = firstNestedString(raw["notification"], "message", "text")
	}
	if payload.NotificationType == "" {
		payload.NotificationType = firstNestedString(raw["notification"], "notification_type", "type")
	}
	if payload.ToolName == "" {
		payload.ToolName = firstNestedString(raw["tool"], "name", "tool_name")
	}
	if payload.ToolUseID == "" {
		payload.ToolUseID = firstNestedString(raw["tool"], "id", "tool_use_id")
	}
	if payload.ErrorType == "" {
		payload.ErrorType = firstNestedString(raw["error"], "type", "name", "code")
	}
	if payload.ErrorMessage == "" {
		payload.ErrorMessage = firstNestedString(raw["error"], "message", "text", "reason")
	}
	if payload.SubagentType == "" {
		payload.SubagentType = firstNestedString(raw["subagent"], "type", "name", "kind")
	}
	if payload.SubagentID == "" {
		payload.SubagentID = firstNestedString(raw["subagent"], "id", "subagent_id", "agent_id")
	}
	if payload.TeammateName == "" {
		payload.TeammateName = firstNestedString(raw["teammate"], "name", "type", "kind")
	}
	if payload.TeammateID == "" {
		payload.TeammateID = firstNestedString(raw["teammate"], "id", "teammate_id")
	}
	if payload.TeammateContext == "" {
		payload.TeammateContext = firstNestedString(raw["teammate"], "context", "status", "reason", "message")
	}
	payload.ToolInput = mapFromAny(raw["tool_input"])
	if len(payload.ToolInput) == 0 {
		payload.ToolInput = mapFromAny(raw["input"])
	}
	if len(payload.ToolInput) == 0 {
		payload.ToolInput = mapFromAny(raw["tool"])
		delete(payload.ToolInput, "name")
		delete(payload.ToolInput, "tool_name")
		delete(payload.ToolInput, "id")
		delete(payload.ToolInput, "tool_use_id")
	}
	return payload, nil
}

func (p claudeHookPayload) claudeMetadata() map[string]string {
	metadata := map[string]string{
		notify.MetaAgent:    aiModeClaude,
		notify.MetaEvent:    p.EventName,
		"session_id":        p.SessionID,
		"cwd":               p.CWD,
		"transcript_path":   p.TranscriptPath,
		"notification_type": p.NotificationType,
		"prompt":            truncateRunes(p.Prompt, 60),
		"tool_name":         p.ToolName,
		"tool_use_id":       p.ToolUseID,
		"error_type":        p.ErrorType,
		"error_message":     truncateRunes(p.ErrorMessage, 160),
		"subagent_type":     p.SubagentType,
		"subagent_id":       p.SubagentID,
		"teammate_name":     p.TeammateName,
		"teammate_id":       p.TeammateID,
		"teammate_context":  truncateRunes(p.TeammateContext, 160),
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

func claudeNotifyID(p claudeHookPayload) string {
	parts := []string{"ai", "claude", "notification"}
	if value := strings.TrimSpace(p.SessionID); value != "" {
		parts = append(parts, value)
	}
	if value := strings.TrimSpace(p.NotificationType); value != "" {
		parts = append(parts, value)
	}
	if value := strings.TrimSpace(p.Message); value != "" {
		parts = append(parts, truncateRunes(value, 40))
	}
	return strings.Join(parts, ":")
}

func claudePermissionNotifyID(p claudeHookPayload) string {
	sessionID := strings.TrimSpace(p.SessionID)
	toolUseID := strings.TrimSpace(p.ToolUseID)
	switch {
	case sessionID != "" && toolUseID != "":
		return "ai:claude:permission:" + sessionID + ":" + toolUseID
	case toolUseID != "":
		return "ai:claude:permission:" + toolUseID
	default:
		return ""
	}
}

func claudeStopNotifyID(p claudeHookPayload) string {
	if sessionID := strings.TrimSpace(p.SessionID); sessionID != "" {
		return "ai:claude:stop:" + sessionID
	}
	return ""
}

func claudeExtraNotifyID(p claudeHookPayload, kind string, values ...string) string {
	parts := []string{"ai", "claude", kind}
	if value := strings.TrimSpace(p.SessionID); value != "" {
		parts = append(parts, value)
	}
	for _, value := range values {
		if trimmed := truncateRunes(value, 40); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, ":")
}

func readClaudeTranscriptLastAssistantText(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return ""
	}
	size := info.Size()
	if size <= 0 {
		return ""
	}
	start := int64(0)
	if size > claudeTranscriptTailLimit {
		start = size - claudeTranscriptTailLimit
	}
	buf := make([]byte, size-start)
	if _, err := file.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(buf)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if text := claudeAssistantTextFromJSONLine(lines[i]); text != "" {
			return text
		}
	}
	return ""
}

func claudeAssistantTextFromJSONLine(line string) string {
	var raw map[string]any
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return ""
	}
	if strings.EqualFold(stringFromAny(raw["role"]), "assistant") {
		return claudeContentText(raw["content"])
	}
	message := mapFromAny(raw["message"])
	if strings.EqualFold(stringFromAny(message["role"]), "assistant") {
		return claudeContentText(message["content"])
	}
	return ""
}

func claudeContentText(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		for _, item := range v {
			if text := firstNestedString(item, "text"); text != "" {
				return text
			}
			if text := stringFromAny(item); text != "" {
				return text
			}
		}
	case map[string]any:
		return firstString(v, "text", "content")
	}
	return ""
}
