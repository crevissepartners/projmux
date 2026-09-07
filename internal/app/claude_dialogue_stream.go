package app

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"slices"

	"strings"
)

var errClaudeDialogueStream = errors.New("claude reply-only public stream rejected")

type claudeDialogueStream struct {
	candidate   string
	session     string
	initialized bool
	ready       bool
	hookPending map[string]string
	startupDone int
	tools       map[string]claudeDialogueObservedTool
}

func dialogueObject(value any, fields string) (map[string]any, bool) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	allowed := map[string]bool{}
	for key := range strings.FieldsSeq(fields) {
		allowed[key] = true
	}
	for key := range object {
		if !allowed[key] {
			return nil, false
		}
	}
	return object, true
}
func dialogueText(value any) bool { text, ok := value.(string); return ok && len(text) <= 65536 }
func dialogueNumber(value any) bool {
	number, ok := value.(float64)
	return ok && !math.IsNaN(number) && !math.IsInf(number, 0) && number >= 0 && number <= 1e15
}
func dialogueEmpty(value any) bool { values, ok := value.([]any); return ok && len(values) == 0 }
func dialogueStrings(value any) ([]string, bool) {
	values, ok := value.([]any)
	if !ok || len(values) > 256 {
		return nil, false
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if !dialogueText(value) {
			return nil, false
		}
		out = append(out, value.(string))
	}
	return out, true
}
func dialogueUsage(value any) bool {
	row, ok := dialogueObject(value, "input_tokens output_tokens cache_creation_input_tokens cache_read_input_tokens cache_creation inference_geo service_tier iterations output_tokens_details server_tool_use speed")
	if !ok {
		return false
	}
	for key, value := range row {
		switch key {
		case "inference_geo", "service_tier", "speed":
			if !dialogueText(value) {
				return false
			}
		case "iterations":
			if !dialogueEmpty(value) {
				return false
			}
		case "cache_creation", "output_tokens_details", "server_tool_use":
			fields := "ephemeral_1h_input_tokens ephemeral_5m_input_tokens"
			if key == "output_tokens_details" {
				fields = "thinking_tokens"
			}
			if key == "server_tool_use" {
				fields = "web_fetch_requests web_search_requests"
			}
			nested, ok := dialogueObject(value, fields)
			if !ok || len(nested) != len(strings.Fields(fields)) {
				return false
			}
			for _, number := range nested {
				if !dialogueNumber(number) || (key == "server_tool_use" && number != float64(0)) {
					return false
				}
			}
		default:
			if !dialogueNumber(value) {
				return false
			}
		}
	}
	return true
}
func dialogueModelUsage(value any) bool {
	rows, ok := value.(map[string]any)
	if !ok || len(rows) > 32 {
		return false
	}
	for model, value := range rows {
		if len(model) > 4096 {
			return false
		}
		row, ok := dialogueObject(value, "inputTokens outputTokens thinkingTokens cacheReadInputTokens cacheCreationInputTokens webSearchRequests costUSD contextWindow maxOutputTokens canonicalModel provider costBasis")
		if !ok {
			return false
		}
		for key, value := range row {
			switch key {
			case "canonicalModel", "provider":
				if !dialogueText(value) {
					return false
				}
			case "costBasis":
				if value != "list" && value != "override" {
					return false
				}
			default:
				if !dialogueNumber(value) || (key == "webSearchRequests" && value != float64(0)) {
					return false
				}
			}
		}
	}
	return true
}

func (s *claudeDialogueStream) inspect(line []byte) (*claudeDialogueObservation, error) {
	var event map[string]any
	if len(line) > 1024*1024 || json.Unmarshal(line, &event) != nil || event == nil {
		return nil, errClaudeDialogueStream
	}
	session, ok := event["session_id"].(string)
	if !ok || !validCoordinationRef(session) || (s.session != "" && s.session != session) {
		return nil, errClaudeDialogueStream
	}
	s.session = session
	refuse := func() (*claudeDialogueObservation, error) { return nil, errClaudeDialogueStream }
	observation := func(kind string) *claudeDialogueObservation {
		return &claudeDialogueObservation{Kind: kind, SessionID: session}
	}
	switch event["type"] {
	case "system":
		switch event["subtype"] {
		case "init":
			if _, ok := dialogueObject(event, "type subtype cwd session_id tools mcp_servers model permissionMode slash_commands apiKeySource claude_code_version output_style agents skills plugins uuid fast_mode_state prompt_suggestion_enabled messaging_socket_path capabilities fast_mode_disabled_reason analytics_disabled product_feedback_disabled"); !ok {
				return refuse()
			}
			tools, ok := dialogueStrings(event["tools"])
			if !ok || len(tools) != 1 || tools[0] != "Bash" || !dialogueEmpty(event["mcp_servers"]) || !dialogueEmpty(event["plugins"]) || event["claude_code_version"] != claudeFrozenFrameProviderVersion || event["permissionMode"] != "dontAsk" {
				return refuse()
			}
			if !s.initialized && (s.startupDone != 2 || len(s.hookPending) != 0) {
				return refuse()
			}
			for _, key := range []string{"analytics_disabled", "product_feedback_disabled"} {
				if value, exists := event[key]; exists {
					if _, ok := value.(bool); !ok {
						return refuse()
					}
				}
			}
			if value, exists := event["capabilities"]; exists {
				if _, ok := dialogueStrings(value); !ok {
					return refuse()
				}
			}
			if value, exists := event["fast_mode_disabled_reason"]; exists && value != "sdk_opt_in_required" {
				return refuse()
			}
			s.initialized = true
			out := observation("init")
			out.ProviderVersion = claudeFrozenFrameProviderVersion
			out.Tools = tools
			out.MCPServers = []string{}
			out.Plugins = []string{}
			return out, nil
		case "hook_started", "hook_progress", "hook_response":
			if !s.hook(event) {
				return refuse()
			}
			return nil, nil
		case "model_refusal_no_fallback":
			// The provider refused one user message. It is an outcome, not a
			// capability: no tool, no content the activation acted on, and the
			// turn simply produced no reply. Dropping it keeps the refusal
			// observable as a qualification that never completes instead of
			// collapsing the activation, which would hide why it failed.
			if _, ok := dialogueObject(event, "type subtype uuid session_id request_id content original_model refused_user_message_uuid api_refusal_category api_refusal_explanation"); !ok || !s.initialized {
				return refuse()
			}
			return nil, nil
		case "thinking_tokens":
			if _, ok := dialogueObject(event, "type subtype estimated_tokens estimated_tokens_delta uuid session_id"); !ok || !s.initialized || !dialogueNumber(event["estimated_tokens"]) || !dialogueNumber(event["estimated_tokens_delta"]) {
				return refuse()
			}
			return nil, nil
		default:
			return refuse()
		}
	case "assistant":
		if _, ok := dialogueObject(event, "type message parent_tool_use_id session_id uuid request_id timestamp"); !ok || !s.initialized {
			return refuse()
		}
		message, ok := dialogueObject(event["message"], "id type role model content stop_reason stop_sequence usage context_management diagnostics stop_details")
		if !ok || message["role"] != "assistant" {
			return refuse()
		}
		for _, key := range []string{"context_management", "diagnostics", "stop_details"} {
			if message[key] != nil {
				return refuse()
			}
		}
		if usage, exists := message["usage"]; exists && !dialogueUsage(usage) {
			return refuse()
		}
		blocks, ok := message["content"].([]any)
		if !ok || len(blocks) == 0 || len(blocks) > 256 {
			return refuse()
		}
		actions := []claudeDialogueObservedTool{}
		for _, raw := range blocks {
			block, ok := raw.(map[string]any)
			if !ok {
				return refuse()
			}
			switch block["type"] {
			case "text":
				if _, ok := dialogueObject(block, "type text"); !ok || !dialogueText(block["text"]) {
					return refuse()
				}
			case "thinking":
				if _, ok := dialogueObject(block, "type thinking signature"); !ok || len(block) != 3 || !dialogueText(block["thinking"]) || !dialogueText(block["signature"]) {
					return refuse()
				}
			case "tool_use":
				if _, ok := dialogueObject(block, "type id name input"); !ok || block["name"] != "Bash" || !s.ready {
					return refuse()
				}
				id, ok := block["id"].(string)
				if !ok || !validCoordinationRef(id) {
					return refuse()
				}
				if s.tools == nil {
					s.tools = map[string]claudeDialogueObservedTool{}
				}
				if _, exists := s.tools[id]; exists {
					return refuse()
				}
				input, ok := dialogueObject(block["input"], "command timeout description run_in_background dangerouslyDisableSandbox")
				if !ok {
					return refuse()
				}
				command, ok := input["command"].(string)
				if !ok {
					return refuse()
				}
				argv, err := parseClaudeReplyCommand(command, s.candidate)
				if err != nil {
					return refuse()
				}
				for _, key := range []string{"run_in_background", "dangerouslyDisableSandbox"} {
					if value, exists := input[key]; exists && value != false {
						return refuse()
					}
				}
				if value, exists := input["timeout"]; exists && (!dialogueNumber(value) || value.(float64) > 30000) {
					return refuse()
				}
				if len(s.tools) >= 32 {
					return refuse()
				}
				action := claudeDialogueObservedTool{ToolUseID: id, MessageRef: argv[6], TargetAgentUID: strings.TrimPrefix(argv[4], "uid:")}
				s.tools[id] = action
				actions = append(actions, action)
			default:
				return refuse()
			}
		}
		if len(actions) > 0 {
			out := observation("tool")
			out.ToolActions = actions
			return out, nil
		}
		return nil, nil
	case "user":
		if _, ok := dialogueObject(event, "type message parent_tool_use_id session_id uuid timestamp tool_use_result"); !ok || !s.ready {
			return refuse()
		}
		message, ok := dialogueObject(event["message"], "role content")
		if !ok || message["role"] != "user" {
			return refuse()
		}
		blocks, ok := message["content"].([]any)
		if !ok || len(blocks) != 1 {
			return refuse()
		}
		block, ok := dialogueObject(blocks[0], "type tool_use_id content is_error")
		if !ok || block["type"] != "tool_result" {
			return refuse()
		}
		id, ok := block["tool_use_id"].(string)
		if !ok {
			return refuse()
		}
		action, exists := s.tools[id]
		if !exists || action.ResultObserved {
			return refuse()
		}
		if value, exists := block["is_error"]; exists && value != false {
			return refuse()
		}
		if !dialogueText(block["content"]) {
			return refuse()
		}
		receipt := block["content"].(string)
		if value, exists := event["tool_use_result"]; exists {
			output, ok := dialogueObject(value, "stdout stderr interrupted")
			if !ok || !dialogueText(output["stdout"]) || output["stderr"] != "" || output["interrupted"] != false {
				return refuse()
			}
			receipt = output["stdout"].(string)
		}
		action.ResultObserved = true
		parts := strings.Split(strings.TrimSuffix(receipt, "\n"), "\t")
		if len(parts) == 2 && validCoordinationRef(parts[0]) && (parts[1] == "accepted" || parts[1] == "delivered") {
			action.ReplyRef = parts[0]
		}
		s.tools[id] = action
		out := observation("tool-result")
		out.ToolActions = []claudeDialogueObservedTool{action}
		return out, nil
	case "result":
		if _, ok := dialogueObject(event, "type subtype is_error duration_ms duration_api_ms num_turns result session_id total_cost_usd usage modelUsage permission_denials uuid errors structured_output api_error_status fast_mode_disabled_reason fast_mode_state first_content_frame_ms queued_turn_count stop_reason subagent_stats terminal_reason time_to_request_ms ttft_ms ttft_stream_ms"); !ok || !s.initialized || event["subtype"] != "success" || event["is_error"] != false {
			return refuse()
		}
		if event["structured_output"] != nil || event["api_error_status"] != nil {
			return refuse()
		}
		for _, key := range []string{"permission_denials", "errors"} {
			if value, exists := event[key]; exists && !dialogueEmpty(value) {
				return refuse()
			}
		}
		if value, exists := event["usage"]; exists && !dialogueUsage(value) {
			return refuse()
		}
		if value, exists := event["modelUsage"]; exists && !dialogueModelUsage(value) {
			return refuse()
		}
		for _, key := range []string{"duration_ms", "duration_api_ms", "num_turns", "total_cost_usd", "first_content_frame_ms", "time_to_request_ms", "ttft_ms", "ttft_stream_ms"} {
			if value, exists := event[key]; exists && !dialogueNumber(value) {
				return refuse()
			}
		}
		if value, exists := event["queued_turn_count"]; exists && value != float64(0) {
			return refuse()
		}
		if value, exists := event["stop_reason"]; exists && value != "end_turn" {
			return refuse()
		}
		if value, exists := event["terminal_reason"]; exists && value != "completed" {
			return refuse()
		}
		if value, exists := event["subagent_stats"]; exists && !dialogueZeroSubagents(value) {
			return refuse()
		}
		if value, exists := event["fast_mode_state"]; exists && !dialogueEnum(value, "off cooldown on") {
			return refuse()
		}
		if value, exists := event["fast_mode_disabled_reason"]; exists && value != "sdk_opt_in_required" {
			return refuse()
		}
		for _, action := range s.tools {
			if !action.ResultObserved {
				return refuse()
			}
		}
		if !s.ready {
			s.ready = true
			return observation("ready"), nil
		}
		return nil, nil
	case "command_lifecycle":
		// Command state telemetry. The pinned reply tool emits it while it runs.
		// It carries no content, no tool definition, and no capability: an actual
		// invocation still has to appear as a strictly validated assistant
		// tool_use block, so this event is shape-checked and dropped.
		if _, ok := dialogueObject(event, "type command_uuid state session_id uuid"); !ok || !s.initialized {
			return refuse()
		}
		if !dialogueText(event["command_uuid"]) || !dialogueText(event["state"]) {
			return refuse()
		}
		return nil, nil
	case "rate_limit_event":
		if _, ok := dialogueObject(event, "type uuid session_id rate_limit_info"); !ok || !s.initialized {
			return refuse()
		}
		// overageResetsAt and overageDisabledReason are the two observed spellings
		// of the same optional overage slot. Exactly one is present, so the row
		// stays exactly seven fields wide and the closed shape keeps its meaning.
		row, ok := dialogueObject(event["rate_limit_info"], "isUsingOverage overageResetsAt overageDisabledReason overageStatus rateLimitType resetsAt status unifiedWindows")
		if !ok || len(row) != 7 {
			return refuse()
		}
		for key, value := range row {
			switch key {
			case "isUsingOverage":
				if _, ok := value.(bool); !ok {
					return refuse()
				}
			case "overageResetsAt", "resetsAt":
				if !dialogueNumber(value) {
					return refuse()
				}
			case "overageDisabledReason":
				if value != nil && !dialogueText(value) {
					return refuse()
				}
			case "unifiedWindows":
				windows, ok := dialogueObject(value, "five_hour seven_day")
				if !ok || len(windows) != 2 {
					return refuse()
				}
				for _, value := range windows {
					window, ok := dialogueObject(value, "resetsAt utilization")
					if !ok || len(window) != 2 || !dialogueNumber(window["resetsAt"]) || !dialogueNumber(window["utilization"]) {
						return refuse()
					}
				}
			case "rateLimitType":
				if !dialogueEnum(value, "five_hour seven_day seven_day_opus seven_day_sonnet seven_day_overage_included overage") {
					return refuse()
				}
			default:
				if !dialogueEnum(value, "allowed allowed_warning rejected") {
					return refuse()
				}
			}
		}
		return nil, nil
	default:
		return refuse()
	}
}

func dialogueZeroSubagents(value any) bool {
	var expected any
	const shape = `{"by_type":{},"completed":0,"failed":0,"killed":{"parent":0,"system":0,"user":0},"max_depth":0,"refused":{"budget":0,"concurrency_limit":0,"depth_limit":0},"requested":{"background":0,"foreground":0,"unset":0},"spawned":0,"spawned_by_subagents":0,"started_in_background":0}`
	if json.Unmarshal([]byte(shape), &expected) != nil {
		return false
	}
	return reflect.DeepEqual(value, expected)
}

func (s *claudeDialogueStream) hook(event map[string]any) bool {
	if _, ok := dialogueObject(event, "type subtype hook_id hook_name hook_event uuid session_id stdout stderr output exit_code outcome"); !ok {
		return false
	}
	id, ok := event["hook_id"].(string)
	if !ok || !validCoordinationRef(id) {
		return false
	}
	name, ok := event["hook_event"].(string)
	if !ok {
		return false
	}
	switch name {
	case "SessionStart":
		if s.initialized {
			return false
		}
	case "UserPromptSubmit", "Stop", "PreToolUse":
		if !s.initialized {
			return false
		}
	default:
		return false
	}
	if !dialogueText(event["hook_name"]) {
		return false
	}
	if s.hookPending == nil {
		s.hookPending = map[string]string{}
	}
	if event["subtype"] == "hook_started" {
		if _, exists := s.hookPending[id]; exists {
			return false
		}
		if len(s.hookPending) >= 16 {
			return false
		}
		s.hookPending[id] = name
		return true
	}
	if s.hookPending[id] != name {
		return false
	}
	if value, exists := event["stderr"]; exists && value != "" {
		return false
	}
	for _, key := range []string{"stdout", "output"} {
		value, exists := event[key]
		if !exists || value == "" {
			continue
		}
		if name != "PreToolUse" {
			return false
		}
		text, ok := value.(string)
		if !ok || len(text) > 16384 {
			return false
		}
		var result map[string]any
		if json.Unmarshal([]byte(text), &result) != nil {
			return false
		}
		if _, ok := dialogueObject(result, "hookSpecificOutput"); !ok {
			return false
		}
		decision, ok := dialogueObject(result["hookSpecificOutput"], "hookEventName permissionDecision updatedInput")
		if !ok || decision["hookEventName"] != "PreToolUse" || decision["permissionDecision"] != "allow" {
			return false
		}
		input, ok := dialogueObject(decision["updatedInput"], "command timeout run_in_background")
		if !ok || input["timeout"] != float64(5000) || input["run_in_background"] != false {
			return false
		}
		marker, ok := input["command"].(string)
		if !ok || !claudeReplyMarkerPattern.MatchString(marker) {
			return false
		}
	}
	if event["subtype"] == "hook_response" {
		if event["exit_code"] != float64(0) || event["outcome"] != "success" {
			return false
		}
		delete(s.hookPending, id)
		if name == "SessionStart" {
			s.startupDone++
		}
	}
	return true
}

func dialogueEnum(value any, allowed string) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	return slices.Contains(strings.Fields(allowed), text)
}
