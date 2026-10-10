package diagnostics

import (
	"path/filepath"
	"testing"
)

func TestOwnerStopClosedVocabularyAndShape(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "operations.jsonl"))
	recorder := NewLifecycleRecorder(store, "op-fixture", "test", MuxBackend())
	for _, reason := range []OwnerStopReason{OwnerStopSIGINT, OwnerStopSIGTERM, OwnerStopStdinEOF, OwnerStopControl, OwnerStopGeneration, OwnerStopProviderExit, OwnerStopOther} {
		recorder.RecordOwnerStop(OwnerStopRecord{Reason: reason, AgentUID: "agent-fixture", PaneUID: "pane-fixture", Generation: "gen-fixture", OwnerPID: 1, OwnerPPID: 2, ParentComm: "process-host"})
	}
	events, err := store.Read()
	if err != nil || len(events) != 7 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	for name, mutate := range map[string]func(*Event){
		"unknown code":   func(e *Event) { e.Code = "owner.stop.prompt-content" },
		"argv":           func(e *Event) { e.ParentComm = "secret --token=foo" },
		"long comm":      func(e *Event) { e.ParentComm = string(make([]byte, 65)) },
		"pid":            func(e *Event) { e.OwnerPID = -1 },
		"generation":     func(e *Event) { e.Generation = "/private/path" },
		"uid":            func(e *Event) { e.AgentUID = "%12" },
		"content":        func(e *Event) { e.Message = "prompt" },
		"foreign fields": func(e *Event) { e.Source = "stdin" },
		"unrelated event": func(e *Event) {
			e.Event = "command.outcome"
			e.Component = "cli"
			e.Code = ""
			e.AgentUID = ""
			e.PaneUID = ""
		},
	} {
		t.Run(name, func(t *testing.T) {
			event := events[0]
			mutate(&event)
			if err := store.Append(event); err == nil {
				t.Fatal("unbounded or unrelated value accepted")
			}
		})
	}
	event := events[0]
	event.ParentComm = ""
	event.OwnerPPID = 0
	if err := store.Append(event); err != nil {
		t.Fatalf("unavailable parent: %v", err)
	}
}
