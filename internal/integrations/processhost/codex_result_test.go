package processhost

import (
	"context"
	"encoding/json"
	"testing"
)

func TestCodexTurnResultsShareProviderShape(t *testing.T) {
	c, _, _ := codexStart(t, testHost(t, nil), "codex-turn-refusal")
	a := codexAuthority(c)
	if err := c.Turn(context.Background(), a, "refused", "first"); err == nil {
		t.Fatal("expected server refusal")
	}
	if err := c.Turn(context.Background(), a, "next", "complete"); err != nil {
		t.Fatal(err)
	}
	observeUntil(t, c.handle, func(s Snapshot) bool { return s.Turn == "" })
	var results []codexTurnResult
	for _, e := range events(c.handle) {
		if e.Kind != "turn-result" {
			continue
		}
		var result codexTurnResult
		if err := json.Unmarshal(e.Raw, &result); err != nil {
			t.Fatal(err)
		}
		results = append(results, result)
	}
	if len(results) != 2 || results[0].ThreadID != a.Session || results[1].ThreadID != a.Session || results[0].Turn.ID != "refused" || results[0].Turn.Status != "failed" || results[0].Turn.Error == nil || results[1].Turn.ID == "" || results[1].Turn.Status != "completed" {
		t.Fatalf("inconsistent result shape: %+v", results)
	}
}
