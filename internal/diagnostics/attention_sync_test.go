package diagnostics

import (
	"path/filepath"
	"testing"
)

func TestAttentionSyncClosedVocabularyAndShape(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "operations.jsonl"))
	recorder := NewLifecycleRecorder(store, "op-fixture", "test", MuxBackend())
	for _, kind := range []string{"lock-timeout", "notify-write", "io", "projection"} {
		recorder.RecordAttentionSync("agent-fixture", kind, false)
	}
	recorder.RecordAttentionSync("agent-fixture", "", true)
	recorder.RecordAttentionSync("agent-fixture", "private prompt", false)
	events, err := store.Read()
	if err != nil || len(events) != 5 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	for name, mutate := range map[string]func(*Event){
		"raw content":    func(e *Event) { e.Message = "private prompt" },
		"unknown error":  func(e *Event) { e.Code = "attention.sync.secret" },
		"invalid uid":    func(e *Event) { e.AgentUID = "/private/path" },
		"foreign field":  func(e *Event) { e.Generation = "gen-fixture" },
		"invalid result": func(e *Event) { e.Result = "success" },
	} {
		t.Run(name, func(t *testing.T) {
			event := events[0]
			mutate(&event)
			if err := store.Append(event); err == nil {
				t.Fatal("unsafe shape accepted")
			}
		})
	}
}
