package transcript

import "testing"

func TestTurnsCarryTheModelTheProviderRanWith(t *testing.T) {
	assistant := map[string]any{
		"type":    "assistant",
		"effort":  "high",
		"message": map[string]any{"role": "assistant", "model": "claude-opus-5", "content": []any{map[string]any{"type": "text", "text": "done"}}},
	}
	turn, ok := parseClaudeLine(assistant)
	if !ok || turn.Model != "claude-opus-5" || turn.Effort != "high" {
		t.Fatalf("claude turn = %v %+v", ok, turn)
	}
	synthetic := map[string]any{
		"type":    "assistant",
		"message": map[string]any{"role": "assistant", "model": "<synthetic>", "content": "API Error"},
	}
	if turn, _ := parseClaudeLine(synthetic); turn.Model != "" {
		t.Fatalf("synthetic model kept: %+v", turn)
	}

	context := map[string]any{
		"type":    "turn_context",
		"payload": map[string]any{"model": "gpt-6-astra", "effort": "xhigh", "cwd": "/x"},
	}
	turn, ok = parseCodexLine(context)
	if !ok || turn.Kind != KindContext || turn.Model != "gpt-6-astra" || turn.Effort != "xhigh" || !hasContent(turn) || speaks(turn) {
		t.Fatalf("codex context = %v %+v", ok, turn)
	}
	if _, ok := parseCodexLine(map[string]any{"type": "turn_context", "payload": map[string]any{}}); ok {
		t.Fatal("a context without a model became a turn")
	}
}
