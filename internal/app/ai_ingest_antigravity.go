package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/app/usagecmd"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/notify"
	antigravityadapter "github.com/crevissepartners/projmux/internal/core/usage/adapters/antigravity"
)

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
	AgentState                 string
	ContextWindow              string
	ContextUsedPercentage      float64
	ContextUsedPercentageSet   bool
	ContextRemainingPercentage float64
	ContextRemainingPercentSet bool
	ContextTotalInputTokens    int64
	ContextTotalOutputTokens   int64
	ContextWindowSize          int64
	ContextCurrentInputTokens  int64
	ContextCurrentOutputTokens int64
	ContextCacheCreationTokens int64
	ContextCacheReadTokens     int64
	QuotaSet                   bool
	QuotaBuckets               []antigravityadapter.QuotaBucketRecord
}

func (c *aiCommand) ingestAntigravityHook(data []byte, explicitEvent string) error {
	payload, err := parseAntigravityHookPayload(data, explicitEvent)
	if err != nil {
		c.appendAIIngestLog(aiIngestLogEntry{Source: "antigravity-hook", Result: "error", Reason: err.Error()})
		return err
	}

	paneID := c.matchAIPane(aiPaneMatchInput{
		CWD:      payload.CWD,
		ThreadID: payload.ConversationID,
	})
	if paneID == "" {
		c.appendAIIngestLog(aiIngestLogEntry{Source: "antigravity-hook", Event: payload.EventName, Result: "ignored", Reason: "no matching pane", CWD: payload.CWD, ThreadID: payload.ConversationID})
		return nil
	}
	defer c.flushPendingAgentSessionRef(paneID)

	c.markAIHookPane(paneID, aiModeAntigravity, payload.CWD, payload.ConversationID, payload.ConversationID, payload.TranscriptPath)
	c.persistAntigravityContextUsage(payload)
	c.persistAntigravityQuotaUsage(payload)
	metadata := payload.antigravityMetadata()
	action := c.aiHookEffectiveAction(aiHookProviderAntigravity, payload.EventName)
	// Statusline is a legacy/manual signal rather than one of the five official
	// v1.1.12 hook events. Preserve its Phase 0b notify behavior without keeping
	// it in the official catalog.
	if payload.EventName == "Statusline" && !action.Known {
		action.Action = aiHookActionNotify
	}

	switch payload.EventName {
	case "Stop":
		if action.Action == aiHookActionQuiet {
			c.quietAntigravityHook(paneID, payload, aiHookQuietReason(action))
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
				c.appendAIIngestLog(aiIngestLogEntry{Source: "antigravity-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, ThreadID: payload.ConversationID})
				return err
			}
			c.appendAIIngestLog(aiIngestLogEntry{Source: "antigravity-hook", Event: payload.EventName, Result: "state", Reason: aiHookStateReason(action), Pane: paneID, CWD: payload.CWD, ThreadID: payload.ConversationID})
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
			c.appendAIIngestLog(aiIngestLogEntry{Source: "antigravity-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, ThreadID: payload.ConversationID})
			return err
		}
		c.appendAIIngestLog(aiIngestLogEntry{Source: "antigravity-hook", Event: payload.EventName, Result: "notify", Reason: antigravityStopDiagnosticReason(payload), Pane: paneID, CWD: payload.CWD, ThreadID: payload.ConversationID})
		return nil
	case "Statusline":
		if payload.ToolConfirmationPending {
			if action.Action == aiHookActionQuiet {
				c.quietAntigravityHook(paneID, payload, aiHookQuietReason(action))
				return nil
			}
			body := formatAntigravityApprovalNotifyBody(payload)
			input := attentionNotifyInput{
				ID:        antigravityNotifyID(payload, "approval"),
				Text:      body.Text,
				Severity:  body.Severity,
				Metadata:  mergeAINotifyBodyMetadata(metadata, body),
				BadgeKind: aiBadgeKindApprovalRequired,
			}
			if action.Action == aiHookActionState {
				if err := c.applyAIStatusStateOnly("waiting", paneID, input); err != nil {
					c.appendAIIngestLog(aiIngestLogEntry{Source: "antigravity-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, ThreadID: payload.ConversationID})
					return err
				}
				c.appendAIIngestLog(aiIngestLogEntry{Source: "antigravity-hook", Event: payload.EventName, Result: "state", Reason: aiHookStateReason(action), Pane: paneID, CWD: payload.CWD, ThreadID: payload.ConversationID})
				return nil
			}
			// Stable queue ID replaces repeated approval snapshots, while the
			// desktop path uses its normal time-window dedupe (Force=false).
			if err := c.applyAIStatusStateOnly("waiting", paneID, input); err != nil {
				c.appendAIIngestLog(aiIngestLogEntry{Source: "antigravity-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, ThreadID: payload.ConversationID})
				return err
			}
			_ = c.notifyAIWithInput(paneID, input)
			input.PaneID = paneID
			input.Lookup = c.notifyLookup()
			c.notifyProducer().PushReplyReady(input)
			c.appendAIIngestLog(aiIngestLogEntry{Source: "antigravity-hook", Event: payload.EventName, Result: "notify", Pane: paneID, CWD: payload.CWD, ThreadID: payload.ConversationID})
			return nil
		}
		switch strings.ToLower(strings.TrimSpace(payload.AgentState)) {
		case "thinking", "working", "tool_use":
			if c.preserveAntigravityTerminalState(paneID) {
				c.quietAntigravityHook(paneID, payload, "late busy statusline; preserving existing completion or approval state")
				return nil
			}
			if err := c.applyAIStatusStateOnly("thinking", paneID, attentionNotifyInput{Metadata: metadata, BadgeKind: aiBadgeKindInProgress}); err != nil {
				c.appendAIIngestLog(aiIngestLogEntry{Source: "antigravity-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, ThreadID: payload.ConversationID})
				return err
			}
			c.appendAIIngestLog(aiIngestLogEntry{Source: "antigravity-hook", Event: payload.EventName, Result: "state", Reason: "statusline agent_state is busy", Pane: paneID, CWD: payload.CWD, ThreadID: payload.ConversationID})
			return nil
		case "idle":
			// Idle is observational only. Stop/completion or approval attention
			// must not be cleared by a later quiet statusline refresh.
			c.quietAntigravityHook(paneID, payload, "statusline agent_state is idle; preserving existing completion or attention state")
			return nil
		default:
			c.quietAntigravityHook(paneID, payload, aiHookQuietReason(action))
			return nil
		}
	case "PreInvocation":
		if action.Action == aiHookActionQuiet {
			c.quietAntigravityHook(paneID, payload, aiHookQuietReason(action))
			return nil
		}
		if err := c.applyAIStatusStateOnly("thinking", paneID, attentionNotifyInput{
			Metadata:  metadata,
			BadgeKind: aiBadgeKindInProgress,
		}); err != nil {
			c.appendAIIngestLog(aiIngestLogEntry{Source: "antigravity-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, ThreadID: payload.ConversationID})
			return err
		}
		c.appendAIIngestLog(aiIngestLogEntry{Source: "antigravity-hook", Event: payload.EventName, Result: "state", Reason: "invocation started", Pane: paneID, CWD: payload.CWD, ThreadID: payload.ConversationID})
		return nil
	case "PostInvocation":
		c.quietAntigravityHook(paneID, payload, aiHookNoHandlerReason(action))
		return nil
	case "PostToolUse":
		reason := aiHookNoHandlerReason(action)
		if payload.Error != "" {
			reason = "tool error: " + truncateRunes(payload.Error, 160) + "; " + reason
		}
		c.quietAntigravityHook(paneID, payload, reason)
		return nil
	default:
		c.quietAntigravityHook(paneID, payload, aiHookNoHandlerReason(action))
		return nil
	}
}

func (c *aiCommand) preserveAntigravityTerminalState(paneID string) bool {
	state := strings.ToLower(strings.TrimSpace(c.readTmuxPaneOption(paneID, aiPaneStateOption)))
	if state == "waiting" || state == "ready" {
		return true
	}
	return strings.TrimSpace(c.readTmuxPaneOption(paneID, attentionStateOption)) == attentionStateReply
}

// persistAntigravityContextUsage records the latest context-window
// percentage carried by an antigravity hook into the usage state
// directory so the usage adapter (and thus the HUD/status bar) can surface
// it. This remains independent from account quota persistence. A missing or
// unparseable value, or a write failure, is silently ignored — usage is a
// non-critical side channel of hook ingest.
func (c *aiCommand) persistAntigravityContextUsage(payload antigravityHookPayload) {
	pct, ok := payload.ContextUsedPercentage, payload.ContextUsedPercentageSet
	if !ok {
		pct, ok = antigravityadapter.ParsePercent(payload.ContextWindow)
	}
	if !ok {
		return
	}
	baseDir, err := c.usageStateDir()
	if err != nil {
		return
	}
	_ = antigravityadapter.WriteContext(baseDir, antigravityadapter.ContextRecord{
		ConversationID: payload.ConversationID,
		Pct:            pct,
		UpdatedAt:      c.now().UTC(),
	})
}

// persistAntigravityQuotaUsage records an explicitly present quota map. An
// absent map leaves the last observation untouched (statusline payloads may be
// context-only); an explicit empty or wholly invalid map clears prior buckets.
func (c *aiCommand) persistAntigravityQuotaUsage(payload antigravityHookPayload) {
	if !payload.QuotaSet {
		return
	}
	baseDir, err := c.usageStateDir()
	if err != nil {
		return
	}
	_ = antigravityadapter.WriteQuota(baseDir, antigravityadapter.QuotaRecord{
		Buckets:   append([]antigravityadapter.QuotaBucketRecord(nil), payload.QuotaBuckets...),
		UpdatedAt: c.now().UTC(),
	})
}

// usageStateDir resolves the directory the usage snapshot cache and the
// antigravity context sidecar live in. It mirrors usagecmd.Command.resolveStateDir
// so the ingest writer and the adapter reader agree even when
// PROJMUX_USAGE_STATE_DIR redirects the cache to a synced location.
func (c *aiCommand) usageStateDir() (string, error) {
	if override := strings.TrimSpace(c.env(usagecmd.StateDirEnvVar)); override != "" {
		return override, nil
	}
	homeDir, err := c.homeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	paths, err := config.Homes{
		HomeDir:    homeDir,
		ConfigHome: c.env("XDG_CONFIG_HOME"),
		StateHome:  c.env("XDG_STATE_HOME"),
	}.Paths()
	if err != nil {
		return "", err
	}
	return filepath.Join(paths.StateDir, "usage"), nil
}

func (c *aiCommand) quietAntigravityHook(paneID string, payload antigravityHookPayload, reason string) {
	c.markAIHookPane(paneID, aiModeAntigravity, payload.CWD, payload.ConversationID, payload.ConversationID, payload.TranscriptPath)
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
		AgentState:            firstString(raw, "agent_state", "agentState"),
		ContextWindow:         stringFromAny(firstAny(raw, "context_window", "contextWindow")),
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
	statusline := firstAny(raw, "statusline", "status_line", "statusLine", "status")
	if payload.AgentState == "" {
		payload.AgentState = firstNestedString(statusline, "agent_state", "agentState")
	}
	if payload.ContextWindow == "" {
		payload.ContextWindow = stringFromAny(firstNestedAny(statusline, "context_window", "contextWindow"))
	}
	contextWindow := mapFromAny(raw["context_window"])
	if len(contextWindow) == 0 {
		contextWindow = mapFromAny(firstNestedAny(statusline, "context_window"))
	}
	if value, ok := firstAntigravityFloat(contextWindow, "used_percentage"); ok {
		payload.ContextUsedPercentage = value
		payload.ContextUsedPercentageSet = true
	}
	if value, ok := firstAntigravityFloat(contextWindow, "remaining_percentage"); ok {
		payload.ContextRemainingPercentage = value
		payload.ContextRemainingPercentSet = true
	}
	payload.ContextTotalInputTokens, _ = firstAntigravityInt64(contextWindow, "total_input_tokens")
	payload.ContextTotalOutputTokens, _ = firstAntigravityInt64(contextWindow, "total_output_tokens")
	payload.ContextWindowSize, _ = firstAntigravityInt64(contextWindow, "context_window_size")
	currentUsage := mapFromAny(contextWindow["current_usage"])
	payload.ContextCurrentInputTokens, _ = firstAntigravityInt64(currentUsage, "input_tokens")
	payload.ContextCurrentOutputTokens, _ = firstAntigravityInt64(currentUsage, "output_tokens")
	payload.ContextCacheCreationTokens, _ = firstAntigravityInt64(currentUsage, "cache_creation_input_tokens")
	payload.ContextCacheReadTokens, _ = firstAntigravityInt64(currentUsage, "cache_read_input_tokens")
	quota, quotaSet := antigravityQuotaMap(raw, statusline)
	payload.QuotaSet = quotaSet
	if quotaSet {
		payload.QuotaBuckets = parseAntigravityQuotaBuckets(quota)
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
	if value, ok := firstNestedBool(statusline, "tool_confirmation_pending", "toolConfirmationPending"); ok {
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
		return "Statusline"
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
		"agent_state":             p.AgentState,
		"context_window":          p.ContextWindow,
	}
	if p.ContextUsedPercentageSet {
		metadata["context_window_used_percentage"] = fmt.Sprintf("%g", p.ContextUsedPercentage)
	}
	if p.ContextRemainingPercentSet {
		metadata["context_window_remaining_percentage"] = fmt.Sprintf("%g", p.ContextRemainingPercentage)
	}
	for key, value := range map[string]int64{
		"context_window_total_input_tokens":    p.ContextTotalInputTokens,
		"context_window_total_output_tokens":   p.ContextTotalOutputTokens,
		"context_window_size":                  p.ContextWindowSize,
		"context_window_input_tokens":          p.ContextCurrentInputTokens,
		"context_window_output_tokens":         p.ContextCurrentOutputTokens,
		"context_window_cache_creation_tokens": p.ContextCacheCreationTokens,
		"context_window_cache_read_tokens":     p.ContextCacheReadTokens,
	} {
		if value > 0 {
			metadata[key] = fmt.Sprintf("%d", value)
		}
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

func firstAntigravityFloat(raw map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		if value, ok := raw[key].(float64); ok {
			return value, true
		}
	}
	return 0, false
}

func firstAntigravityInt64(raw map[string]any, keys ...string) (int64, bool) {
	for _, key := range keys {
		if value, ok := raw[key].(float64); ok && !math.IsNaN(value) && !math.IsInf(value, 0) && value >= math.MinInt64 && value <= math.MaxInt64 && value == math.Trunc(value) {
			return int64(value), true
		}
	}
	return 0, false
}

func antigravityQuotaMap(raw map[string]any, statusline any) (map[string]any, bool) {
	if value, ok := raw["quota"]; ok {
		return mapFromAny(value), true
	}
	if nested := mapFromAny(statusline); len(nested) > 0 {
		if value, ok := nested["quota"]; ok {
			return mapFromAny(value), true
		}
	}
	return nil, false
}

func parseAntigravityQuotaBuckets(quota map[string]any) []antigravityadapter.QuotaBucketRecord {
	ids := make([]string, 0, len(quota))
	for id := range quota {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	buckets := make([]antigravityadapter.QuotaBucketRecord, 0, len(ids))
	for _, id := range ids {
		rawBucket := mapFromAny(quota[id])
		remaining, ok := firstAntigravityFloat(rawBucket, "remaining_fraction")
		if !ok {
			continue
		}
		bucket := antigravityadapter.QuotaBucketRecord{
			ID:                id,
			RemainingFraction: remaining,
		}
		if rawReset := firstString(rawBucket, "reset_time"); rawReset != "" {
			if parsed, err := time.Parse(time.RFC3339, rawReset); err == nil {
				bucket.ResetTime = parsed.UTC()
			}
		}
		if seconds, ok := firstAntigravityInt64(rawBucket, "reset_in_seconds"); ok {
			bucket.ResetInSeconds = &seconds
		}
		if antigravityadapter.ValidQuotaBucket(bucket) {
			buckets = append(buckets, bucket)
		}
	}
	return buckets
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
