package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/notify"
)

// antigravityLegacyStatuslineEvent is the event name an older projmux
// statusLine bridge passed to this route. projmux no longer ingests the
// statusLine; a leftover bridge must stay a silent no-op until the next
// install removes it.
const antigravityLegacyStatuslineEvent = "Statusline"

type antigravityHookPayload struct {
	EventName                  string
	CWD                        string
	ConversationID             string
	WorkspacePaths             []string
	TranscriptPath             string
	ArtifactDirectoryPath      string
	ModelName                  string
	InvocationNum              int
	InvocationNumSet           bool
	InitialNumSteps            int
	InitialNumStepsSet         bool
	ToolCall                   map[string]any
	StepIdx                    int
	StepIdxSet                 bool
	ExecutionNum               int
	ExecutionNumSet            bool
	TerminationReason          string
	Error                      string
	FullyIdle                  bool
	FullyIdleSet               bool
	ToolConfirmationPending    bool
	ToolConfirmationPendingSet bool
}

func (c *aiCommand) ingestAntigravityHook(data []byte, explicitEvent, explicitPane string) error {
	payload, err := parseAntigravityHookPayload(data, explicitEvent)
	if err != nil {
		c.appendAIIngestLog(aiIngestLogEntry{Source: "antigravity-hook", Result: "error", Reason: aiIngestFailureReason(aiIngestReasonHookPayloadInvalid, err)})
		return err
	}
	if payload.EventName == antigravityLegacyStatuslineEvent {
		return nil
	}

	paneID, matchReason := c.matchAIPane(aiPaneMatchInput{
		ExplicitPane: explicitPane,
		Provider:     aiModeAntigravity,
		CWD:          payload.CWD,
		ThreadID:     payload.ConversationID,
	})
	if paneID == "" {
		c.appendAIIngestLog(aiIngestLogEntry{Source: "antigravity-hook", Event: payload.EventName, Result: "ignored", Reason: aiIngestRecordReason(matchReason), CWD: payload.CWD, ThreadID: payload.ConversationID})
		return nil
	}
	defer c.flushPendingAgentSessionRef(paneID)

	c.markAIHookPane(paneID, aiModeAntigravity, payload.CWD, payload.ConversationID, payload.ConversationID, payload.TranscriptPath)
	metadata := payload.antigravityMetadata()
	action := c.aiHookEffectiveAction(aiHookProviderAntigravity, payload.EventName)

	switch payload.EventName {
	case "Stop":
		if action.Action == aiHookActionQuiet {
			c.quietAntigravityHook(paneID, payload, aiIngestRecordReason(aiHookQuietReason(action)))
			return nil
		}
		body := formatAntigravityStopNotifyBody(payload)
		if action.Action == aiHookActionState {
			if err := c.applyAIStatusStateOnly("waiting", paneID, attentionNotifyInput{
				ID:        antigravityNotifyID(payload, "stop"),
				Text:      body.Text,
				Severity:  body.Severity,
				Metadata:  mergeAINotifyBodyMetadata(metadata, body),
				Force:     true,
				BadgeKind: aiBadgeKindForNotifyCategory(body.Category),
			}); err != nil {
				c.appendAIIngestLog(aiIngestLogEntry{Source: "antigravity-hook", Event: payload.EventName, Result: "error", Reason: aiIngestFailureReason(aiIngestReasonStatusApplyFailed, err), Pane: paneID, CWD: payload.CWD, ThreadID: payload.ConversationID})
				return err
			}
			c.appendAIIngestLog(aiIngestLogEntry{Source: "antigravity-hook", Event: payload.EventName, Result: "state", Reason: aiIngestRecordReason(aiHookStateReason(action)), Pane: paneID, CWD: payload.CWD, ThreadID: payload.ConversationID})
			return nil
		}
		if err := c.applyAIStatusWithNotify("waiting", paneID, attentionNotifyInput{
			ID:        antigravityNotifyID(payload, "stop"),
			Text:      body.Text,
			Severity:  body.Severity,
			Metadata:  mergeAINotifyBodyMetadata(metadata, body),
			Force:     true,
			BadgeKind: aiBadgeKindForNotifyCategory(body.Category),
		}); err != nil {
			c.appendAIIngestLog(aiIngestLogEntry{Source: "antigravity-hook", Event: payload.EventName, Result: "error", Reason: aiIngestFailureReason(aiIngestReasonStatusApplyFailed, err), Pane: paneID, CWD: payload.CWD, ThreadID: payload.ConversationID})
			return err
		}
		c.appendAIIngestLog(aiIngestLogEntry{Source: "antigravity-hook", Event: payload.EventName, Result: "notify", Reason: antigravityStopDiagnosticReason(payload), Pane: paneID, CWD: payload.CWD, ThreadID: payload.ConversationID})
		return nil
	case "PreInvocation":
		if action.Action == aiHookActionQuiet {
			c.quietAntigravityHook(paneID, payload, aiIngestRecordReason(aiHookQuietReason(action)))
			return nil
		}
		if err := c.applyAIStatusStateOnly("thinking", paneID, attentionNotifyInput{
			Metadata:  metadata,
			BadgeKind: aiBadgeKindInProgress,
		}); err != nil {
			c.appendAIIngestLog(aiIngestLogEntry{Source: "antigravity-hook", Event: payload.EventName, Result: "error", Reason: aiIngestFailureReason(aiIngestReasonStatusApplyFailed, err), Pane: paneID, CWD: payload.CWD, ThreadID: payload.ConversationID})
			return err
		}
		c.appendAIIngestLog(aiIngestLogEntry{Source: "antigravity-hook", Event: payload.EventName, Result: "state", Reason: aiIngestReasonInvocationStart, Pane: paneID, CWD: payload.CWD, ThreadID: payload.ConversationID})
		return nil
	case "PostInvocation":
		c.quietAntigravityHook(paneID, payload, aiIngestRecordReason(aiHookNoHandlerReason(action)))
		return nil
	case "PostToolUse":
		// A tool failure is the more specific cause, so it replaces the
		// no-handler token rather than being prefixed to it. The provider's own
		// error text is dropped: it is the shape that carried an absolute path
		// into this log, and the reason column holds one bounded token.
		reason := aiIngestRecordReason(aiHookNoHandlerReason(action))
		if payload.Error != "" {
			reason = aiIngestReasonToolError
		}
		c.quietAntigravityHook(paneID, payload, reason)
		return nil
	default:
		c.quietAntigravityHook(paneID, payload, aiIngestRecordReason(aiHookNoHandlerReason(action)))
		return nil
	}
}

// quietAntigravityHook only records the quiet outcome. Every caller is reached
// from ingestAntigravityHook, which already marked the Pane before dispatching.
func (c *aiCommand) quietAntigravityHook(paneID string, payload antigravityHookPayload, reason aiIngestReason) {
	c.appendAIIngestLog(aiIngestLogEntry{Source: "antigravity-hook", Event: payload.EventName, Result: "quiet", Reason: reason, Pane: paneID, CWD: payload.CWD, ThreadID: payload.ConversationID})
}

func parseAntigravityHookPayload(data []byte, explicitEvent string) (antigravityHookPayload, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return antigravityHookPayload{}, fmt.Errorf("parse antigravity hook payload: %w", err)
	}
	eventName := firstString(raw, "hook_event_name", "event_name", "eventName", "event", "type")
	payload := antigravityHookPayload{
		EventName:             eventName,
		CWD:                   firstString(raw, "cwd", "workspace", "project_dir", "projectDir"),
		ConversationID:        firstString(raw, "conversation_id", "conversationId", "session_id", "sessionId"),
		TranscriptPath:        firstString(raw, "transcript_path", "transcriptPath"),
		ArtifactDirectoryPath: firstString(raw, "artifactDirectoryPath", "artifact_directory_path"),
		ModelName:             firstString(raw, "modelName", "model_name", "model"),
		ToolCall:              mapFromAny(firstAny(raw, "toolCall", "tool_call")),
		TerminationReason:     firstString(raw, "termination_reason", "terminationReason"),
		Error:                 antigravityErrorString(raw["error"]),
	}
	payload.WorkspacePaths = antigravityWorkspacePaths(firstAny(raw, "workspacePaths", "workspace_paths"))
	if payload.CWD == "" {
		payload.CWD = firstNestedString(raw["workspace"], "cwd", "path")
	}
	if payload.CWD == "" && len(payload.WorkspacePaths) > 0 {
		payload.CWD = payload.WorkspacePaths[0]
	}
	if payload.TranscriptPath == "" {
		payload.TranscriptPath = firstNestedString(raw["transcript"], "path", "transcriptPath")
	}
	if value, ok := firstBool(raw, "fully_idle", "fullyIdle"); ok {
		payload.FullyIdle = value
		payload.FullyIdleSet = true
	}
	if value, ok := firstAntigravityInt(raw, "invocationNum", "invocation_num"); ok {
		payload.InvocationNum = value
		payload.InvocationNumSet = true
	}
	if value, ok := firstAntigravityInt(raw, "initialNumSteps", "initial_num_steps"); ok {
		payload.InitialNumSteps = value
		payload.InitialNumStepsSet = true
	}
	if value, ok := firstAntigravityInt(raw, "stepIdx", "step_idx"); ok {
		payload.StepIdx = value
		payload.StepIdxSet = true
	}
	if value, ok := firstAntigravityInt(raw, "executionNum", "execution_num"); ok {
		payload.ExecutionNum = value
		payload.ExecutionNumSet = true
	}
	if value, ok := firstBool(raw, "tool_confirmation_pending", "toolConfirmationPending"); ok {
		payload.ToolConfirmationPending = value
		payload.ToolConfirmationPendingSet = true
	}
	if strings.TrimSpace(explicitEvent) != "" {
		payload.EventName = explicitEvent
	}
	payload.EventName = normalizeAntigravityEventName(payload.EventName)
	if payload.EventName == "" {
		payload.EventName = "Unknown"
	}
	return payload, nil
}

func normalizeAntigravityEventName(name string) string {
	trimmed := strings.TrimSpace(name)
	switch strings.ToLower(strings.ReplaceAll(trimmed, "_", "")) {
	case "pretooluse":
		return "PreToolUse"
	case "preinvocation":
		return "PreInvocation"
	case "postinvocation":
		return "PostInvocation"
	case "posttooluse":
		return "PostToolUse"
	case "stop":
		return "Stop"
	case "statusline", "status":
		return antigravityLegacyStatuslineEvent
	default:
		return trimmed
	}
}

func (p antigravityHookPayload) antigravityMetadata() map[string]string {
	metadata := map[string]string{
		notify.MetaAgent:          aiModeAntigravity,
		notify.MetaEvent:          p.EventName,
		"conversation_id":         p.ConversationID,
		"cwd":                     p.CWD,
		"transcript_path":         p.TranscriptPath,
		"artifact_directory_path": p.ArtifactDirectoryPath,
		"model_name":              p.ModelName,
		"termination_reason":      p.TerminationReason,
		"error":                   truncateRunes(p.Error, 160),
	}
	if name := firstString(p.ToolCall, "name"); name != "" {
		metadata["tool_name"] = name
	}
	if p.InvocationNumSet {
		metadata["invocation_num"] = fmt.Sprintf("%d", p.InvocationNum)
	}
	if p.InitialNumStepsSet {
		metadata["initial_num_steps"] = fmt.Sprintf("%d", p.InitialNumSteps)
	}
	if p.StepIdxSet {
		metadata["step_idx"] = fmt.Sprintf("%d", p.StepIdx)
	}
	if p.ExecutionNumSet {
		metadata["execution_num"] = fmt.Sprintf("%d", p.ExecutionNum)
	}
	if p.EventName == "Stop" {
		classification := antigravityTerminationClassification(p)
		metadata["termination_class"] = classification
	}
	if p.FullyIdleSet {
		metadata["fully_idle"] = boolMetadataValue(p.FullyIdle)
	}
	if p.ToolConfirmationPendingSet {
		metadata["tool_confirmation_pending"] = boolMetadataValue(p.ToolConfirmationPending)
	}
	out := make(map[string]string, len(metadata))
	for k, v := range metadata {
		if value := strings.TrimSpace(v); value != "" {
			out[k] = value
		}
	}
	return out
}

func antigravityWorkspacePaths(value any) []string {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	paths := make([]string, 0, len(values))
	for _, value := range values {
		path := strings.TrimSpace(stringFromAny(value))
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

func firstAntigravityInt(raw map[string]any, keys ...string) (int, bool) {
	for _, key := range keys {
		switch value := raw[key].(type) {
		case float64:
			if value == float64(int(value)) {
				return int(value), true
			}
		case json.Number:
			parsed, err := value.Int64()
			if err == nil {
				return int(parsed), true
			}
		case int:
			return value, true
		}
	}
	return 0, false
}

func antigravityHookResponse(event string) ([]byte, error) {
	switch normalizeAntigravityEventName(event) {
	case "Stop":
		// The Stop contract requires a decision. Any value other than
		// "continue" allows the already-requested stop to complete.
		return []byte(`{"decision":"stop"}`), nil
	case "PreInvocation", "PostInvocation", "PostToolUse":
		return []byte(`{}`), nil
	case "PreToolUse":
		return nil, errors.New("antigravity PreToolUse response is intentionally unsupported because it changes permission policy")
	default:
		return []byte(`{}`), nil
	}
}

func antigravityNotifyID(p antigravityHookPayload, kind string) string {
	parts := []string{"ai", "antigravity", kind}
	if value := strings.TrimSpace(p.ConversationID); value != "" {
		parts = append(parts, value)
	}
	return strings.Join(parts, ":")
}

func antigravityErrorString(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case map[string]any:
		if text := firstString(v, "message", "text", "reason", "type", "code"); text != "" {
			return text
		}
	}
	return stringFromAny(value)
}
