package diagnostics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestHistoricalSessionStateOutcomeRecordsStayReadable(t *testing.T) {
	zero, one, two := 0, 1, 2
	base := Event{At: "2026-08-14T00:00:00Z", Level: "info", Component: "session-state", Event: "session-state.outcome", Result: "success", DurationMS: 3, RunID: "run", Version: "0.15.3", MuxBackend: "tmux"}
	records := map[string]func(*Event){
		"save success": func(e *Event) {
			e.Operation, e.Source = string(OperationSessionStateSave), string(SessionStateSourceManual)
			e.WindowCount, e.PaneCount, e.ShellRecipeCount, e.AgentRecipeCount, e.StartupRecipeCount = &one, &two, &one, &one, &zero
		},
		"restore success": func(e *Event) {
			e.Operation, e.Source = string(OperationSessionStateRestore), string(SessionStateSourceStartupNamed)
			e.WindowCount, e.PaneCount, e.ShellRecipeCount, e.AgentRecipeCount, e.StartupRecipeCount = &one, &one, &one, &zero, &zero
		},
		"delete success": func(e *Event) {
			e.Operation, e.Source, e.ItemCount = string(OperationSessionStateDelete), string(SessionStateSourcePrune), &two
		},
		"autosave error": func(e *Event) {
			e.Operation, e.Source = string(OperationSessionStateAutosave), string(SessionStateSourceAutosave)
			e.Level, e.Result, e.Kind, e.Code = "error", "error", "runtime", string(CodeSessionStateAutosaveFailed)
		},
		"settings save error": func(e *Event) {
			e.Operation, e.Source = string(OperationSessionStateSave), string(SessionStateSourceSettingsLatest)
			e.Level, e.Result, e.Kind, e.Code = "error", "error", "runtime", string(CodeSessionStateSaveFailed)
		},
	}
	for name, edit := range records {
		t.Run(name, func(t *testing.T) {
			event := base
			edit(&event)
			if _, err := sanitizeEvent(event, ""); err != nil {
				t.Fatalf("historical record rejected: %v (%+v)", err, event)
			}
			raw, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "logs", LogFileName)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			events, err := NewStore(path).Read()
			if err != nil {
				t.Fatalf("Read() error = %v", err)
			}
			if len(events) != 1 || events[0].Event != "session-state.outcome" || events[0].Operation != event.Operation {
				t.Fatalf("Read() = %+v, want the historical session-state.outcome record", events)
			}
		})
	}
}

func TestHistoricalSessionStateCommandOutcomesStayReadableButLiveArgvIsUnclassified(t *testing.T) {
	base := Event{At: "2026-08-14T00:00:00Z", Level: "info", Component: "cli", Event: "command.outcome", Result: "success", RunID: "run", Version: "0.15.3", MuxBackend: "tmux"}
	for _, class := range [][2]string{{"session-state", "save"}, {"session-state", "restore"}, {"session-state", "delete"}, {"prune", "session-state"}} {
		event := base
		event.Command, event.Subcommand = class[0], class[1]
		if _, err := sanitizeEvent(event, ""); err != nil {
			t.Fatalf("historical command.outcome %v rejected: %v", class, err)
		}
	}
	for _, args := range [][]string{
		{"session-state", "save"},
		{"create", "snapshot"},
		{"get", "snapshots"},
		{"delete", "snapshot"},
		{"restore", "snapshot"},
	} {
		if got := Classify(args); got.Command != "" || got.Subcommand != "" || got.StateChanging {
			t.Fatalf("Classify(%q) = %+v, want the removed route unclassified", args, got)
		}
	}
	for _, args := range [][]string{{"prune", "snapshot", "--apply"}, {"prune", "session-state", "delete"}} {
		if got := Classify(args); got.Subcommand != "" || got.StateChanging {
			t.Fatalf("Classify(%q) = %+v, want no retired subcommand or mutation", args, got)
		}
	}
}

func TestSessionStateOutcomeRejectsOpenSchemaShapes(t *testing.T) {
	zero, one := 0, 1
	base := Event{At: "2026-08-14T00:00:00Z", Level: "info", Component: "session-state", Event: "session-state.outcome", Result: "success", DurationMS: 1, RunID: "run", Version: "0.10.0", MuxBackend: "tmux", Operation: string(OperationSessionStateSave), Source: string(SessionStateSourceManual), WindowCount: &one, PaneCount: &one, ShellRecipeCount: &one, AgentRecipeCount: &zero, StartupRecipeCount: &zero}
	tests := []struct {
		name string
		edit func(*Event)
	}{
		{"wrong component", func(e *Event) { e.Component = "runtime" }},
		{"unknown source", func(e *Event) { e.Source = "popup-raw" }},
		{"wrong code", func(e *Event) {
			e.Result, e.Level, e.Kind, e.Code = "error", "error", "runtime", string(CodeSessionStateDeleteFailed)
			e.WindowCount, e.PaneCount, e.ShellRecipeCount, e.AgentRecipeCount, e.StartupRecipeCount = nil, nil, nil, nil, nil
		}},
		{"negative count", func(e *Event) { negative := -1; e.WindowCount = &negative }},
		{"delete snapshot count", func(e *Event) { e.Operation = string(OperationSessionStateDelete); e.ItemCount = &one }},
		{"message", func(e *Event) { e.Message = "raw" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := base
			tt.edit(&event)
			if _, err := sanitizeEvent(event, ""); err == nil {
				t.Fatalf("sanitizeEvent(%#v) succeeded", event)
			}
		})
	}
}

func TestOutcomeEventFamiliesRejectCrossFamilyOperations(t *testing.T) {
	base := Event{At: "2026-08-14T00:00:00Z", Level: "info", Component: "runtime", Event: "lifecycle.start", Result: "started", DurationMS: 0, RunID: "run", Version: "0.10.0", MuxBackend: "tmux", Operation: string(OperationSessionStateSave)}
	if _, err := sanitizeEvent(base, ""); err == nil {
		t.Fatal("lifecycle.start accepted Session State operation")
	}
	base.Event, base.Result = "lifecycle.outcome", "success"
	if _, err := sanitizeEvent(base, ""); err == nil {
		t.Fatal("lifecycle.outcome accepted Session State operation")
	}
	zero := 0
	base = Event{At: "2026-08-14T00:00:00Z", Level: "info", Component: "session-state", Event: "session-state.outcome", Result: "success", DurationMS: 0, RunID: "run", Version: "0.10.0", MuxBackend: "tmux", Operation: string(OperationSessionCreate), Source: string(SessionStateSourceManual), WindowCount: &zero, PaneCount: &zero, ShellRecipeCount: &zero, AgentRecipeCount: &zero, StartupRecipeCount: &zero}
	if _, err := sanitizeEvent(base, ""); err == nil {
		t.Fatal("session-state.outcome accepted runtime lifecycle operation")
	}
}
