package transcript

import (
	"encoding/json"
	"strings"
)

// parseClaudeLine reads the Claude Code session jsonl.
func parseClaudeLine(raw map[string]any) (Turn, bool) {
	kind, _ := raw["type"].(string)
	if kind == "attachment" {
		return parseClaudeAttachment(raw)
	}
	if kind != "user" && kind != "assistant" {
		return Turn{}, false
	}
	message, _ := raw["message"].(map[string]any)
	if message == nil {
		return Turn{}, false
	}
	// Harness bookkeeping is written as `user` records too. `isMeta` marks the
	// caveat Claude Code prepends to a local command, and a compaction summary
	// is the harness talking to itself; neither is something anyone said.
	//
	// isMeta alone is not enough to drop a record: a peer message delivered
	// while the session is idle is also written as a meta `user` record, with
	// origin.kind "peer". Dropping every meta record hid exactly the messages
	// the web client sends whenever the agent was not mid-turn.
	if raw["isCompactSummary"] == true {
		return Turn{}, false
	}
	if raw["isMeta"] == true && !fromPeer(raw) {
		return Turn{}, false
	}
	text, tools, thinking := flattenContent(message["content"])
	images := countImages(message["content"])
	if text == "" && len(tools) == 0 && !thinking && images == 0 {
		return Turn{}, false
	}
	at := stringOf(raw["timestamp"])
	if kind == "user" {
		if turn, ok := harnessTurn(text, at); ok {
			return turn, true
		}
		if turn, handled := localCommandTurn(text, at); handled {
			return turn, turn.Text != ""
		}
	}
	text = stripReminders(text)
	if text == "" && len(tools) == 0 && !thinking && images == 0 {
		return Turn{}, false
	}
	// A coordination frame arrives as a user turn; show the message rather
	// than the transport envelope around it.
	if frame, ok := unwrapCoordination(text); ok {
		return frame.turn(at, coordinationKindDirect), true
	}
	turn := Turn{Role: kind, Text: text, At: at, Kind: kind, Thinking: thinking, Tools: tools, Images: images}
	if kind == "assistant" {
		turn.Model, turn.Effort = claudeModel(message), stringOf(raw["effort"])
	}
	return turn, true
}

// KindContext is a turn that records only the model a provider switched to.
const KindContext = "context"

// claudeModel reads the model an assistant record came from. Claude Code
// writes "<synthetic>" on messages it made up itself, such as an API error.
func claudeModel(message map[string]any) string {
	model := stringOf(message["model"])
	if strings.HasPrefix(model, "<") {
		return ""
	}
	return model
}

// parseClaudeAttachment reads a message that arrived while the session was
// already working.
//
// Claude Code does not record those as a `user` turn: a message sent mid-turn
// is written as an `attachment` record of type `queued_command`, carrying the
// prompt and who it came from. Reading only `user` records therefore loses
// every message sent to a busy session — which is most of them, and was the
// reason peer messages sent from the web client appeared not to arrive at all.
// In one measured session log, 66 coordination messages were queued this way
// against 1 that landed as a plain user turn.
func parseClaudeAttachment(raw map[string]any) (Turn, bool) {
	attachment, _ := raw["attachment"].(map[string]any)
	if attachment == nil || stringOf(attachment["type"]) != "queued_command" {
		return Turn{}, false
	}
	// The prompt is a string, or a content array when the message carries an
	// image; reading only the string form dropped every such message.
	text, _, _ := flattenContent(attachment["prompt"])
	images := countImages(attachment["prompt"])
	if text == "" && images == 0 {
		return Turn{}, false
	}
	at := stringOf(attachment["timestamp"])
	if at == "" {
		at = stringOf(raw["timestamp"])
	}
	if turn, ok := harnessTurn(text, at); ok {
		return turn, true
	}
	if frame, ok := unwrapCoordination(text); ok {
		return frame.turn(at, coordinationKindQueued), true
	}
	text = stripReminders(text)
	if text == "" && images == 0 {
		return Turn{}, false
	}
	return Turn{Role: "user", Text: text, At: at, Kind: "queued", Images: images}, true
}

// fromPeer reports whether a record was delivered by another session.
func fromPeer(raw map[string]any) bool {
	origin, _ := raw["origin"].(map[string]any)
	return stringOf(origin["kind"]) == "peer"
}

// localCommandTurn reads the records a slash command leaves behind.
//
// Typing `/login` writes `<command-name>/login</command-name>…` and then
// `<local-command-stdout>Login successful</local-command-stdout>` as two user
// records. Shown raw they are markup in the operator's column; shown as what
// they are, they are a command the operator ran and the one line it answered.
//
// handled is true when the text was such a record, even one that should not be
// shown (the caveat); the caller then drops a turn with empty Text.
func localCommandTurn(text, at string) (turn Turn, handled bool) {
	trimmed := strings.TrimSpace(text)
	switch {
	case strings.HasPrefix(trimmed, "<command-name>"):
		name := between(trimmed, "<command-name>", "</command-name>")
		if args := strings.TrimSpace(between(trimmed, "<command-args>", "</command-args>")); args != "" {
			name += " " + args
		}
		return Turn{Role: "user", Text: name, At: at, Kind: "command"}, true
	case strings.HasPrefix(trimmed, "<local-command-stdout>"):
		out := strings.TrimSpace(between(trimmed, "<local-command-stdout>", "</local-command-stdout>"))
		return Turn{Role: "system", Text: out, At: at, Kind: "command-output"}, true
	case strings.HasPrefix(trimmed, "<local-command-caveat>"):
		return Turn{}, true
	}
	return Turn{}, false
}

func between(text, open, closing string) string {
	_, after, ok := strings.Cut(text, open)
	if !ok {
		return ""
	}
	rest := after
	if before, _, ok := strings.Cut(rest, closing); ok {
		return before
	}
	return rest
}

// parseCodexLine reads a Codex rollout jsonl, keeping only real messages and
// tool calls.
func parseCodexLine(raw map[string]any) (Turn, bool) {
	payload, _ := raw["payload"].(map[string]any)
	if payload == nil {
		return Turn{}, false
	}
	at := stringOf(raw["timestamp"])
	switch stringOf(raw["type"]) {
	case "response_item":
	case "turn_context":
		// Each turn records the model and effort it runs with, so a change
		// made mid-session shows up on the next turn.
		model := stringOf(payload["model"])
		if model == "" {
			return Turn{}, false
		}
		return Turn{Role: "system", Kind: KindContext, At: at, Model: model, Effort: stringOf(payload["effort"])}, true
	default:
		return Turn{}, false
	}
	switch stringOf(payload["type"]) {
	case "message":
		// handled below
	case "function_call", "custom_tool_call":
		// `function_call` carries JSON arguments, `custom_tool_call` a raw
		// input string; both name the call id the output will quote.
		input := stringOf(payload["arguments"])
		if input == "" {
			input = stringOf(payload["input"])
		}
		clipped, cut := clip(input, toolTextLimit)
		call := ToolCall{
			ID:      stringOf(payload["call_id"]),
			Name:    stringOf(payload["name"]),
			Input:   clipped,
			Clipped: cut,
			Summary: summarizeCodexInput(input),
		}
		return Turn{Role: "tool", At: at, Kind: "tool_call", Tools: []ToolCall{call}}, true
	case "function_call_output", "custom_tool_call_output":
		result := stringOf(payload["output"])
		if result == "" {
			if nested, ok := payload["output"].(map[string]any); ok {
				result = stringOf(nested["content"])
			}
		}
		clipped, cut := clip(result, toolTextLimit)
		return Turn{
			Role: "tool", At: at, Kind: "tool_result",
			Tools: []ToolCall{{ID: stringOf(payload["call_id"]), Result: clipped, Clipped: cut}},
		}, true
	default:
		return Turn{}, false
	}
	role := stringOf(payload["role"])
	// `developer` carries the injected system prompt, not conversation.
	if role != "user" && role != "assistant" {
		return Turn{}, false
	}
	text, _, _ := flattenContent(payload["content"])
	if text == "" {
		return Turn{}, false
	}
	return Turn{Role: role, Text: text, At: at, Kind: "message"}, true
}

// summarizeCodexInput reads the one interesting argument out of a call whose
// arguments are a JSON string rather than a decoded object.
func summarizeCodexInput(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(input), &decoded); err == nil {
		if summary := toolSummary(decoded); summary != "" {
			return summary
		}
	}
	return clipSummary(input)
}

// parseAntigravityLine reads the Antigravity trajectory transcript.
func parseAntigravityLine(raw map[string]any) (Turn, bool) {
	kind := stringOf(raw["type"])
	text := stringOf(raw["content"])
	if text == "" {
		return Turn{}, false
	}
	role := "assistant"
	switch stringOf(raw["source"]) {
	case "USER_EXPLICIT", "USER":
		role = "user"
	case "SYSTEM":
		role = "system"
	}
	// GENERIC steps are the tool and environment records of a trajectory.
	if kind == "GENERIC" {
		role = "tool"
	}
	return Turn{Role: role, Text: text, At: stringOf(raw["created_at"]), Kind: kind}, true
}

// questionToolName is the Claude tool whose input the client renders as a
// form rather than a tool row.
const questionToolName = "AskUserQuestion"

// questionInputLimit bounds a question's input. It is far above
// toolTextLimit because the form needs the whole input to render at all.
const questionInputLimit = 64 * 1024

// flattenContent splits a content array into prose, tool calls, and whether a
// reasoning block was present.
//
// A `tool_result` block carries no name — only the id of the call it answers —
// so it comes back as a ToolCall with just ID and Result set, and linkTools
// merges it into the call afterwards.
func flattenContent(value any) (text string, tools []ToolCall, thinking bool) {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed), nil, false
	case []any:
		var parts []string
		for _, item := range typed {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			switch stringOf(block["type"]) {
			case "text", "input_text", "output_text", "":
				if text := strings.TrimSpace(stringOf(block["text"])); text != "" {
					parts = append(parts, text)
				}
			case "tool_use":
				tools = append(tools, toolUseCall(block))
			case "tool_result":
				result, _, _ := flattenContent(block["content"])
				result = stripReminders(result)
				isError, _ := block["is_error"].(bool)
				clipped, cut := clip(result, toolTextLimit)
				tools = append(tools, ToolCall{
					ID:      stringOf(block["tool_use_id"]),
					Result:  clipped,
					Error:   isError,
					Clipped: cut,
				})
			case "thinking":
				thinking = true
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n")), tools, thinking
	}
	return "", nil, false
}

// countImages counts image blocks in a content array.
func countImages(value any) int {
	items, _ := value.([]any)
	n := 0
	for _, item := range items {
		if block, ok := item.(map[string]any); ok && stringOf(block["type"]) == "image" {
			n++
		}
	}
	return n
}

// toolUseCall reads one Claude `tool_use` block.
func toolUseCall(block map[string]any) ToolCall {
	input, _ := block["input"].(map[string]any)
	encoded, _ := json.MarshalIndent(input, "", "  ")
	name := stringOf(block["name"])
	// A question is rendered as a form from its input, so its input has to
	// arrive whole: clipping it to the display limit cut the JSON mid-string
	// and the card fell back to a plain tool row. Compact encoding keeps that
	// whole input small.
	limit := toolTextLimit
	if name == questionToolName {
		encoded, _ = json.Marshal(input)
		limit = questionInputLimit
	}
	clipped, cut := clip(string(encoded), limit)
	return ToolCall{
		ID:      stringOf(block["id"]),
		Name:    name,
		Summary: toolSummary(input),
		Input:   clipped,
		Clipped: cut,
	}
}
