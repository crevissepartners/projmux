package app

import "testing"

func TestCodexHookStopUsesLastAssistantMessageLikeClaude(t *testing.T) {
	payload, err := parseCodexHookPayload([]byte(`{"hook_event_name":"Stop","last-assistant-message":"  Final reply  "}`))
	if err != nil {
		t.Fatal(err)
	}
	got := formatCodexHookStopNotifyBody(payload)
	if got.Text != formatClaudeStopNotifyBody("  Final reply  ").Text || got.Text != "Final reply" || got.Agent != "codex" || got.Category != "response_complete" {
		t.Fatalf("Codex stop = %#v", got)
	}
	if empty := formatCodexHookStopNotifyBody(codexHookPayload{}); empty.Text != "Ready" {
		t.Fatalf("empty fallback = %#v", empty)
	}
}
