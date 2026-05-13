package app

import (
	"strings"

	"github.com/crevissepartners/projmux/internal/core/notify"
)

type aiNotifyBody struct {
	Text     string
	Severity string
}

func formatCodexTurnCompleteNotifyBody(p codexNotifyPayload) aiNotifyBody {
	return aiNotifyBody{
		Text: joinAINotifyText("Codex", "응답 완료", p.LastAssistantMessage),
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
	text := joinAINotifyText("Claude", "승인 필요", toolName)
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
		Text:     joinAINotifyText("Claude", "응답 완료", message),
		Severity: notify.SeverityInfo,
	}
}

func formatClaudeStopFailureNotifyBody(p claudeHookPayload) aiNotifyBody {
	return aiNotifyBody{
		Text:     joinAINotifyText("Claude", "오류", p.ErrorType, p.ErrorMessage),
		Severity: notify.SeverityCritical,
	}
}

func formatClaudeSubagentStopNotifyBody(p claudeHookPayload) aiNotifyBody {
	return aiNotifyBody{
		Text:     joinAINotifyText("Claude", "서브에이전트 종료", p.SubagentType, p.SubagentID),
		Severity: notify.SeverityInfo,
	}
}

func formatClaudeTeammateIdleNotifyBody(p claudeHookPayload) aiNotifyBody {
	return aiNotifyBody{
		Text:     joinAINotifyText("Claude", "팀메이트 대기", p.TeammateName, p.TeammateID, p.TeammateContext),
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
		return "승인 필요", notify.SeverityCritical
	case "elicitation_dialog":
		return "입력 필요", notify.SeverityCritical
	case "idle_prompt":
		return "응답 완료", notify.SeverityInfo
	default:
		return "응답 완료", notify.SeverityInfo
	}
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
