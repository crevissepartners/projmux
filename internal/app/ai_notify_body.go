package app

import (
	"maps"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/notify"
)

type aiNotifyBody struct {
	Text     string
	Severity string
	Agent    string
	Category string
}

func formatCodexHookPermissionNotifyBody(p codexHookPayload) aiNotifyBody {
	toolName := strings.TrimSpace(p.ToolName)
	if toolName == "" {
		toolName = "Tool"
	}
	text := toolName
	if summary := formatCodexToolInputSummary(toolName, p.ToolInput); summary != "" {
		text += ": " + summary
	}
	return aiNotifyBody{
		Text:     text,
		Severity: notify.SeverityCritical,
		Agent:    "codex",
		Category: "approval_required",
	}
}

func formatCodexHookStopNotifyBody(codexHookPayload) aiNotifyBody {
	return aiNotifyBody{
		Text:     "Ready",
		Severity: notify.SeverityInfo,
		Agent:    "codex",
		Category: "response_complete",
	}
}

func formatCodexGenericHookNotifyBody(p codexHookPayload) aiNotifyBody {
	return aiNotifyBody{
		Text:     joinAINotifyText("", p.EventName, p.ToolName),
		Severity: notify.SeverityInfo,
		Agent:    "codex",
		Category: strings.TrimSpace(p.EventName),
	}
}

func formatClaudeNotificationNotifyBody(p claudeHookPayload) aiNotifyBody {
	category, severity := claudeNotificationCategorySeverity(p.NotificationType)
	return aiNotifyBody{
		Text:     defaultString(strings.TrimSpace(p.Message), "Ready"),
		Severity: severity,
		Agent:    "claude",
		Category: category,
	}
}

func formatClaudePermissionNotifyBody(p claudeHookPayload) aiNotifyBody {
	toolName := strings.TrimSpace(p.ToolName)
	if toolName == "" {
		toolName = "Tool"
	}
	text := toolName
	if summary := formatClaudeToolInputSummary(toolName, p.ToolInput, p.ToolUseID); summary != "" {
		text += ": " + summary
	}
	return aiNotifyBody{
		Text:     text,
		Severity: notify.SeverityCritical,
		Agent:    "claude",
		Category: "approval_required",
	}
}

func formatClaudeStopNotifyBody(message string) aiNotifyBody {
	return aiNotifyBody{
		Text:     defaultString(strings.TrimSpace(message), "Ready"),
		Severity: notify.SeverityInfo,
		Agent:    "claude",
		Category: "response_complete",
	}
}

func formatClaudeStopFailureNotifyBody(p claudeHookPayload) aiNotifyBody {
	return aiNotifyBody{
		Text:     joinAINotifyText(p.ErrorType, p.ErrorMessage),
		Severity: notify.SeverityCritical,
		Agent:    "claude",
		Category: "error",
	}
}

func formatClaudeSubagentStopNotifyBody(p claudeHookPayload) aiNotifyBody {
	return aiNotifyBody{
		Text:     joinAINotifyText(p.SubagentType, p.SubagentID),
		Severity: notify.SeverityInfo,
		Agent:    "claude",
		Category: "subagent_stopped",
	}
}

func formatClaudeTeammateIdleNotifyBody(p claudeHookPayload) aiNotifyBody {
	return aiNotifyBody{
		Text:     joinAINotifyText(p.TeammateName, p.TeammateID, p.TeammateContext),
		Severity: notify.SeverityInfo,
		Agent:    "claude",
		Category: "teammate_waiting",
	}
}

func formatAntigravityApprovalNotifyBody(p antigravityHookPayload) aiNotifyBody {
	return aiNotifyBody{
		Text:     defaultString(joinAINotifyText("Approval needed", p.AgentState), "Approval needed"),
		Severity: notify.SeverityCritical,
		Agent:    "antigravity",
		Category: "approval_required",
	}
}

func formatAntigravityStopNotifyBody(p antigravityHookPayload) aiNotifyBody {
	if antigravityHookHasError(p) {
		return aiNotifyBody{
			Text:     defaultString(joinAINotifyText(p.TerminationReason, p.Error), "Error"),
			Severity: notify.SeverityCritical,
			Agent:    "antigravity",
			Category: "error",
		}
	}
	return aiNotifyBody{
		Text:     "Ready",
		Severity: notify.SeverityInfo,
		Agent:    "antigravity",
		Category: "response_complete",
	}
}

func antigravityHookHasError(p antigravityHookPayload) bool {
	if strings.TrimSpace(p.Error) != "" {
		return true
	}
	reason := strings.ToLower(strings.TrimSpace(p.TerminationReason))
	switch reason {
	case "", "stop", "stopped", "complete", "completed", "success", "normal":
		return false
	default:
		return true
	}
}

func joinAINotifyText(agent, category string, values ...string) string {
	parts := []string{}
	if trimmed := strings.TrimSpace(agent); trimmed != "" {
		parts = append(parts, trimmed)
	}
	if trimmed := strings.TrimSpace(category); trimmed != "" {
		parts = append(parts, trimmed)
	}
	for _, value := range values {
		if trimmed := truncateRunes(value, 80); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, " · ")
}

func claudeNotificationCategorySeverity(notificationType string) (string, string) {
	switch strings.TrimSpace(notificationType) {
	case "permission_prompt":
		return "approval_required", notify.SeverityCritical
	case "elicitation_dialog":
		return "input_required", notify.SeverityCritical
	case "idle_prompt":
		return "response_complete", notify.SeverityInfo
	default:
		return "response_complete", notify.SeverityInfo
	}
}

func mergeAINotifyBodyMetadata(metadata map[string]string, body aiNotifyBody) map[string]string {
	merged := make(map[string]string, len(metadata)+2)
	maps.Copy(merged, metadata)
	if body.Agent != "" {
		merged[notify.MetaAgent] = body.Agent
	}
	if body.Category != "" {
		merged[notify.MetaCategory] = body.Category
	}
	return merged
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
