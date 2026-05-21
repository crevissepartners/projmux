package app

import (
	"strings"

	"github.com/crevissepartners/projmux/internal/core/notify"
)

type aiNotifyBody struct {
	Text     string
	Severity string
}

func formatCodexHookPermissionNotifyBody(p codexHookPayload) aiNotifyBody {
	toolName := strings.TrimSpace(p.ToolName)
	if toolName == "" {
		toolName = "Tool"
	}
	text := joinAINotifyText("Codex", "Approval required", toolName)
	if summary := formatCodexToolInputSummary(toolName, p.ToolInput); summary != "" {
		text += ": " + summary
	}
	return aiNotifyBody{
		Text:     text,
		Severity: notify.SeverityCritical,
	}
}

func formatCodexHookStopNotifyBody(codexHookPayload) aiNotifyBody {
	return aiNotifyBody{
		Text:     joinAINotifyText("Codex", "Response complete"),
		Severity: notify.SeverityInfo,
	}
}

func formatCodexGenericHookNotifyBody(p codexHookPayload) aiNotifyBody {
	return aiNotifyBody{
		Text:     joinAINotifyText("Codex", p.EventName, p.ToolName),
		Severity: notify.SeverityInfo,
	}
}

func formatClaudeNotificationNotifyBody(p claudeHookPayload) aiNotifyBody {
	label, severity := claudeNotificationLabelSeverity(p.NotificationType)
	return aiNotifyBody{
		Text:     joinAINotifyText("Claude", label, p.Message),
		Severity: severity,
	}
}

func formatClaudePermissionNotifyBody(p claudeHookPayload) aiNotifyBody {
	toolName := strings.TrimSpace(p.ToolName)
	if toolName == "" {
		toolName = "Tool"
	}
	text := joinAINotifyText("Claude", "Approval required", toolName)
	if summary := formatClaudeToolInputSummary(toolName, p.ToolInput, p.ToolUseID); summary != "" {
		text += ": " + summary
	}
	return aiNotifyBody{
		Text:     text,
		Severity: notify.SeverityCritical,
	}
}

func formatClaudeStopNotifyBody(message string) aiNotifyBody {
	return aiNotifyBody{
		Text:     joinAINotifyText("Claude", "Response complete", message),
		Severity: notify.SeverityInfo,
	}
}

func formatClaudeStopFailureNotifyBody(p claudeHookPayload) aiNotifyBody {
	return aiNotifyBody{
		Text:     joinAINotifyText("Claude", "Error", p.ErrorType, p.ErrorMessage),
		Severity: notify.SeverityCritical,
	}
}

func formatClaudeSubagentStopNotifyBody(p claudeHookPayload) aiNotifyBody {
	return aiNotifyBody{
		Text:     joinAINotifyText("Claude", "Subagent stopped", p.SubagentType, p.SubagentID),
		Severity: notify.SeverityInfo,
	}
}

func formatClaudeTeammateIdleNotifyBody(p claudeHookPayload) aiNotifyBody {
	return aiNotifyBody{
		Text:     joinAINotifyText("Claude", "Teammate waiting", p.TeammateName, p.TeammateID, p.TeammateContext),
		Severity: notify.SeverityInfo,
	}
}

func joinAINotifyText(agent, category string, values ...string) string {
	parts := []string{strings.TrimSpace(agent), strings.TrimSpace(category)}
	for _, value := range values {
		if trimmed := truncateRunes(value, 80); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, " · ")
}

func claudeNotificationLabelSeverity(notificationType string) (string, string) {
	switch strings.TrimSpace(notificationType) {
	case "permission_prompt":
		return "Approval required", notify.SeverityCritical
	case "elicitation_dialog":
		return "Input required", notify.SeverityCritical
	case "idle_prompt":
		return "Response complete", notify.SeverityInfo
	default:
		return "Response complete", notify.SeverityInfo
	}
}

func formatCodexToolInputSummary(toolName string, input map[string]any) string {
	keys := []string{"command", "action", "file_path", "path", "url", "query"}
	switch strings.TrimSpace(toolName) {
	case "Bash", "Shell":
		keys = []string{"command", "description", "action"}
	case "Write", "Edit", "MultiEdit", "Read":
		keys = []string{"file_path", "path"}
	case "WebFetch", "WebSearch":
		keys = []string{"url", "query"}
	}
	for _, key := range keys {
		if value := stringFromAny(input[key]); value != "" {
			return truncateRunes(value, 80)
		}
	}
	return ""
}

func formatClaudeToolInputSummary(toolName string, input map[string]any, fallbackID string) string {
	keys := []string{"command", "file_path", "path", "url"}
	switch strings.TrimSpace(toolName) {
	case "Bash":
		keys = []string{"command", "description"}
	case "Write", "Edit", "MultiEdit", "Read":
		keys = []string{"file_path", "path"}
	case "WebFetch", "WebSearch":
		keys = []string{"url", "query"}
	}
	for _, key := range keys {
		if value := stringFromAny(input[key]); value != "" {
			return truncateRunes(value, 80)
		}
	}
	if fallback := strings.TrimSpace(fallbackID); fallback != "" {
		return fallback
	}
	return ""
}
