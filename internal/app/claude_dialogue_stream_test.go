package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func dialogueStreamEvent(t *testing.T, s *claudeDialogueStream, event map[string]any) (*claudeDialogueObservation, error) {
	t.Helper()
	event["session_id"] = "session-owned"
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return s.inspect(data)
}
func readyDialogueStream(t *testing.T) *claudeDialogueStream {
	t.Helper()
	s := &claudeDialogueStream{candidate: "/owned/projmux"}
	for i := range 2 {
		for _, sub := range []string{"hook_started", "hook_response"} {
			_, err := dialogueStreamEvent(t, s, map[string]any{"type": "system", "subtype": sub, "hook_id": fmt.Sprintf("hook-%d", i), "hook_event": "SessionStart", "hook_name": "SessionStart:startup", "exit_code": 0, "outcome": "success"})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	out, err := dialogueStreamEvent(t, s, map[string]any{"type": "system", "subtype": "init", "tools": []string{"Bash"}, "mcp_servers": []any{}, "plugins": []any{}, "permissionMode": "dontAsk", "claude_code_version": claudeFrozenFrameProviderVersion, "messaging_socket_path": "discarded-private-locator"})
	if err != nil || out == nil || out.Kind != "init" {
		t.Fatal("init failed", err)
	}
	body, _ := json.Marshal(out)
	if strings.Contains(string(body), "discarded") {
		t.Fatal("locator retained")
	}
	out, err = dialogueStreamEvent(t, s, map[string]any{"type": "result", "subtype": "success", "is_error": false})
	if err != nil || out == nil || out.Kind != "ready" {
		t.Fatal("initial ready failed", err)
	}
	return s
}
func dialogueToolEvent(id, command string) map[string]any {
	return map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{"command": command}}}}}
}

func TestClaudeDialogueStreamDiscardsTextThinkingAndPairsOnlyExactReplyTool(t *testing.T) {
	s := readyDialogueStream(t)
	out, err := dialogueStreamEvent(t, s, map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "thinking", "thinking": "never retained", "signature": "never retained either"}, map[string]any{"type": "text", "text": "ordinary model text is not reply authority"}}}})
	if err != nil || out != nil {
		t.Fatal("text became a coordination event", err)
	}
	command := "/owned/projmux agent message send uid:source --reply-to message-owned -- 'explicit reply'"
	out, err = dialogueStreamEvent(t, s, dialogueToolEvent("tool-owned", command))
	if err != nil || out == nil || out.Kind != "tool" {
		t.Fatal("explicit action rejected", err)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "explicit reply") || strings.Contains(string(raw), command) {
		t.Fatal("command/body forwarded")
	}
	event := map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "tool-owned", "content": "message-reply\taccepted\n", "is_error": false}}}, "tool_use_result": map[string]any{"stdout": "message-reply\taccepted\n", "stderr": "", "interrupted": false}}
	if _, err := dialogueStreamEvent(t, s, event); err != nil {
		t.Fatal(err)
	}
	if _, err := dialogueStreamEvent(t, s, event); err == nil {
		t.Fatal("replayed tool result admitted")
	}
}

func TestClaudeDialogueStreamRejectsUnknownEffectsAndBoundsState(t *testing.T) {
	for _, event := range []map[string]any{
		{"type": "system", "subtype": "plugin_install"},
		{"type": "result", "subtype": "success", "is_error": true},
		{"type": "result", "subtype": "success", "is_error": false, "subagent_stats": map[string]any{"unknown": 0}},
		{"type": "result", "subtype": "success", "is_error": false, "usage": map[string]any{"server_tool_use": map[string]any{"web_fetch_requests": 1, "web_search_requests": 0}}},
		{"type": "result", "subtype": "success", "is_error": false, "usage": map[string]any{"CLAUDE_CODE_MESSAGING_TOKEN": "not retained"}},
		{"type": "result", "subtype": "success", "is_error": false, "fast_mode_state": map[string]any{}},
		{"type": "result", "subtype": "success", "is_error": false, "modelUsage": map[string]any{"model[public suffix]": map[string]any{"webSearchRequests": 1}}},
		dialogueToolEvent("bad", "cat /outside"),
	} {
		s := readyDialogueStream(t)
		if _, err := dialogueStreamEvent(t, s, event); err == nil {
			t.Fatal("unknown/effectful metadata admitted")
		}
	}
	s := readyDialogueStream(t)
	s.ready = false
	if _, err := dialogueStreamEvent(t, s, dialogueToolEvent("before", "/owned/projmux agent message send uid:source --reply-to message-a -- 'text'")); err == nil {
		t.Fatal("tool before inbound-ready admitted")
	}
	s = readyDialogueStream(t)
	for i := range 16 {
		if _, err := dialogueStreamEvent(t, s, map[string]any{"type": "system", "subtype": "hook_started", "hook_id": fmt.Sprintf("pending-%d", i), "hook_name": "Stop", "hook_event": "Stop"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := dialogueStreamEvent(t, s, map[string]any{"type": "system", "subtype": "hook_started", "hook_id": "too-many", "hook_name": "Stop", "hook_event": "Stop"}); err == nil {
		t.Fatal("unbounded hook map")
	}
	s = readyDialogueStream(t)
	for i := range 32 {
		if _, err := dialogueStreamEvent(t, s, dialogueToolEvent(fmt.Sprintf("tool-%d", i), "/owned/projmux agent message send uid:source --reply-to message-a -- 'text'")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := dialogueStreamEvent(t, s, dialogueToolEvent("too-many", "/owned/projmux agent message send uid:source --reply-to message-a -- 'text'")); err == nil {
		t.Fatal("unbounded tool map")
	}
	if _, err := s.inspect([]byte(strings.Repeat(" ", 1024*1024+1))); err == nil {
		t.Fatal("oversized line admitted")
	}
}
