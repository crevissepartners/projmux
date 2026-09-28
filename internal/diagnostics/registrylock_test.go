package diagnostics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func registryLockHeld(d time.Duration) *time.Duration { return &d }

func registryLockMS(ms int64) *int64 { return &ms }

func registryLockFixture() Event {
	return Event{
		At: "2026-09-29T00:00:00Z", Level: "info", Component: "registry", Event: registryLockAcquisitionEvent,
		Result: "success", DurationMS: 1500, RunID: "registry-run", Version: "1.0.0", MuxBackend: "tmux",
		Command: "create", Subcommand: "agent", Operation: string(RegistryLockUpdate),
		WaitMS: registryLockMS(1200), LockHeldMS: registryLockMS(300),
	}
}

// TestRegistryLockRecordsOnlyAtOrAboveTheThreshold pins when an acquisition is
// worth a line: a wait or a hold of at least one second, or a timeout. Every
// result below the threshold on both counts writes nothing, and a success held
// for five seconds or more is a warning.
func TestRegistryLockRecordsOnlyAtOrAboveTheThreshold(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		acquisition RegistryLockAcquisition
		records     int
		level       string
	}{
		{"both under", RegistryLockAcquisition{Operation: RegistryLockUpdate, Result: RegistryLockSuccess, Wait: 999 * time.Millisecond, Held: registryLockHeld(999 * time.Millisecond)}, 0, ""},
		{"wait at threshold", RegistryLockAcquisition{Operation: RegistryLockUpdate, Result: RegistryLockSuccess, Wait: time.Second, Held: registryLockHeld(0)}, 1, "info"},
		{"hold at threshold", RegistryLockAcquisition{Operation: RegistryLockLoad, Result: RegistryLockSuccess, Held: registryLockHeld(time.Second)}, 1, "info"},
		{"short timeout", RegistryLockAcquisition{Operation: RegistryLockUpdate, Result: RegistryLockTimeout, Wait: time.Millisecond}, 1, "error"},
		{"hold at warn", RegistryLockAcquisition{Operation: RegistryLockUpdate, Result: RegistryLockSuccess, Held: registryLockHeld(5 * time.Second)}, 1, "warn"},
		{"hold under warn", RegistryLockAcquisition{Operation: RegistryLockUpdate, Result: RegistryLockSuccess, Held: registryLockHeld(4999 * time.Millisecond)}, 1, "info"},
		{"long mutation failure", RegistryLockAcquisition{Operation: RegistryLockUpdateConvergent, Result: RegistryLockMutationFailed, Held: registryLockHeld(6 * time.Second)}, 1, "error"},
		{"long acquire failure", RegistryLockAcquisition{Operation: RegistryLockMigrate, Result: RegistryLockAcquireFailed, Wait: 2 * time.Second}, 1, "error"},
		{"short mutation failure", RegistryLockAcquisition{Operation: RegistryLockUpdate, Result: RegistryLockMutationFailed, Wait: 10 * time.Millisecond, Held: registryLockHeld(10 * time.Millisecond)}, 0, ""},
		{"short acquire failure", RegistryLockAcquisition{Operation: RegistryLockUpdate, Result: RegistryLockAcquireFailed, Wait: 10 * time.Millisecond}, 0, ""},
		{"negative clock", RegistryLockAcquisition{Operation: RegistryLockUpdate, Result: RegistryLockSuccess, Wait: -time.Hour, Held: registryLockHeld(-time.Hour)}, 0, ""},
		{"unknown operation", RegistryLockAcquisition{Operation: "repair", Result: RegistryLockTimeout}, 0, ""},
		{"unknown result", RegistryLockAcquisition{Operation: RegistryLockUpdate, Result: "busy", Wait: time.Minute}, 0, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := NewStore(filepath.Join(t.TempDir(), "operations.jsonl"))
			owner := NewLifecycleRecorder(store, "registry-run", "1.0.0", "tmux")
			owner.RegistryLock(Classify([]string{"create", "agent"})).Record(test.acquisition)
			events, err := store.Read()
			if err != nil || len(events) != test.records {
				t.Fatalf("events=%+v err=%v, want %d records", events, err, test.records)
			}
			if test.records == 0 {
				return
			}
			event := events[0]
			if event.Level != test.level || event.Component != "registry" || event.Event != registryLockAcquisitionEvent ||
				event.Operation != string(test.acquisition.Operation) || event.WaitMS == nil {
				t.Fatalf("record = %+v, want level %s", event, test.level)
			}
			held := int64(0)
			if event.LockHeldMS != nil {
				held = *event.LockHeldMS
			}
			if event.DurationMS != *event.WaitMS+held {
				t.Fatalf("duration_ms = %d, want wait_ms + lock_held_ms = %d", event.DurationMS, *event.WaitMS+held)
			}
			if (test.acquisition.Held != nil) != (event.LockHeldMS != nil) {
				t.Fatalf("lock_held_ms = %v, want present only with a held lease", event.LockHeldMS)
			}
			if owner.RecordedOutcome() {
				t.Fatal("registry.lock.acquisition claimed the top-level command outcome")
			}
		})
	}
}

// TestRegistryLockRecordCarriesTheClosedFieldsOnly reads the stored line back
// as raw JSON: its keys are the closed allowlist and nothing from the argv the
// command class was derived from reaches the file.
func TestRegistryLockRecordCarriesTheClosedFieldsOnly(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "operations.jsonl")
	store := NewStore(path)
	class := Classify([]string{"create", "agent", "--", "secret prompt"})
	if class.Command != "create" || class.Subcommand != "agent" {
		t.Fatalf("Classify = %+v, want create agent", class)
	}
	recorder := NewLifecycleRecorder(store, "registry-run", "1.0.0", "tmux").RegistryLock(class)
	recorder.Record(RegistryLockAcquisition{Operation: RegistryLockUpdate, Result: RegistryLockSuccess, Wait: 1200 * time.Millisecond, Held: registryLockHeld(6 * time.Second)})
	recorder.Record(RegistryLockAcquisition{Operation: RegistryLockUpdate, Result: RegistryLockTimeout, Wait: 30 * time.Second})

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if strings.Contains(string(data), "secret") {
		t.Fatalf("journal carries argv text: %s", data)
	}
	allowed := []string{"at", "level", "component", "event", "result", "duration_ms", "run_id", "version", "mux_backend",
		"command", "subcommand", "operation", "wait_ms", "lock_held_ms", "kind", "code"}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("journal lines = %d, want 2:\n%s", len(lines), data)
	}
	for i, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		for key := range record {
			if !slices.Contains(allowed, key) {
				t.Fatalf("line %d carries %q outside the closed allowlist: %s", i, key, line)
			}
		}
		if record["command"] != "create" || record["subcommand"] != "agent" || record["operation"] != "update" {
			t.Fatalf("line %d = %s, want the catalog command and the store operation", i, line)
		}
	}
	var success, timeout map[string]any
	_ = json.Unmarshal([]byte(lines[0]), &success)
	_ = json.Unmarshal([]byte(lines[1]), &timeout)
	if success["level"] != "warn" || success["result"] != "success" || success["wait_ms"] != float64(1200) ||
		success["lock_held_ms"] != float64(6000) || success["duration_ms"] != float64(7200) || success["code"] != nil {
		t.Fatalf("success line = %v", success)
	}
	if timeout["level"] != "error" || timeout["result"] != "error" || timeout["kind"] != "runtime" ||
		timeout["code"] != "registry.lock.timeout" || timeout["lock_held_ms"] != nil || timeout["duration_ms"] != float64(30000) {
		t.Fatalf("timeout line = %v", timeout)
	}

	// An unclassified invocation records no command at all.
	unclassified := NewStore(filepath.Join(t.TempDir(), "operations.jsonl"))
	NewLifecycleRecorder(unclassified, "registry-run", "1.0.0", "tmux").RegistryLock(Classify([]string{"not-a-command", "secret"})).
		Record(RegistryLockAcquisition{Operation: RegistryLockLoad, Result: RegistryLockTimeout})
	events, err := unclassified.Read()
	if err != nil || len(events) != 1 || events[0].Command != "" || events[0].Subcommand != "" {
		t.Fatalf("unclassified events=%+v err=%v, want one record without a command", events, err)
	}
}

// TestRegistryLockEventClosedShape pins the validator for the new family and
// that every other family refuses its fields, its component, and level warn.
func TestRegistryLockEventClosedShape(t *testing.T) {
	t.Parallel()
	for _, operation := range registryLockOperations {
		event := registryLockFixture()
		event.Operation = string(operation)
		if _, err := sanitizeEvent(event, ""); err != nil {
			t.Fatalf("%s: %v", operation, err)
		}
	}
	for name, mutate := range map[string]func(*Event){
		"unclassified": func(e *Event) { e.Command, e.Subcommand = "", "" },
		"command only": func(e *Event) { e.Subcommand = "" },
		"zero wait":    func(e *Event) { e.WaitMS, e.DurationMS = registryLockMS(0), 300 },
		"warn at 5s":   func(e *Event) { e.Level, e.LockHeldMS, e.DurationMS = "warn", registryLockMS(5000), 6200 },
		"mutation failed": func(e *Event) {
			e.Level, e.Result, e.Kind, e.Code = "error", "error", "runtime", "registry.mutation.failed"
		},
		"timeout": func(e *Event) {
			e.Level, e.Result, e.Kind, e.Code, e.LockHeldMS, e.DurationMS = "error", "error", "runtime", "registry.lock.timeout", nil, 1200
		},
		"acquire failed": func(e *Event) {
			e.Level, e.Result, e.Kind, e.Code, e.LockHeldMS, e.DurationMS = "error", "error", "runtime", "registry.lock.acquire-failed", nil, 1200
		},
	} {
		event := registryLockFixture()
		mutate(&event)
		if _, err := sanitizeEvent(event, ""); err != nil {
			t.Fatalf("%s rejected: %v", name, err)
		}
	}
	for name, mutate := range map[string]func(*Event){
		"unknown operation":    func(e *Event) { e.Operation = "repair" },
		"missing operation":    func(e *Event) { e.Operation = "" },
		"runtime operation":    func(e *Event) { e.Operation = string(OperationSessionCreate) },
		"no wait":              func(e *Event) { e.WaitMS = nil },
		"negative wait":        func(e *Event) { e.WaitMS, e.DurationMS = registryLockMS(-1), 299 },
		"negative hold":        func(e *Event) { e.LockHeldMS, e.DurationMS = registryLockMS(-1), 1199 },
		"duration not the sum": func(e *Event) { e.DurationMS++ },
		"success without hold": func(e *Event) { e.LockHeldMS, e.DurationMS = nil, 1200 },
		"warn under 5s":        func(e *Event) { e.Level = "warn" },
		"info at 5s":           func(e *Event) { e.LockHeldMS, e.DurationMS = registryLockMS(5000), 6200 },
		"success code":         func(e *Event) { e.Code = "registry.lock.timeout" },
		"success kind":         func(e *Event) { e.Kind = "runtime" },
		"error as info":        func(e *Event) { e.Result, e.Kind, e.Code = "error", "runtime", "registry.mutation.failed" },
		"error without kind":   func(e *Event) { e.Level, e.Result, e.Code = "error", "error", "registry.mutation.failed" },
		"error without code":   func(e *Event) { e.Level, e.Result, e.Kind = "error", "error", "runtime" },
		"error foreign code": func(e *Event) {
			e.Level, e.Result, e.Kind, e.Code = "error", "error", "runtime", string(CodeSessionCreateFailed)
		},
		"timeout with hold": func(e *Event) {
			e.Level, e.Result, e.Kind, e.Code = "error", "error", "runtime", "registry.lock.timeout"
		},
		"acquire fail with hold": func(e *Event) {
			e.Level, e.Result, e.Kind, e.Code = "error", "error", "runtime", "registry.lock.acquire-failed"
		},
		"mutation fail pre-lease": func(e *Event) {
			e.Level, e.Result, e.Kind, e.Code, e.LockHeldMS, e.DurationMS = "error", "error", "runtime", "registry.mutation.failed", nil, 1200
		},
		"started":          func(e *Event) { e.Result = "started" },
		"component":        func(e *Event) { e.Component = "runtime" },
		"argv command":     func(e *Event) { e.Command = "secret" },
		"message":          func(e *Event) { e.Message = "private prompt" },
		"source":           func(e *Event) { e.Source = "manual" },
		"count":            func(e *Event) { e.ItemCount = intPointer(1) },
		"provider":         func(e *Event) { e.Provider = "codex" },
		"ai failure":       func(e *Event) { e.Failure = string(AIFailureRoute) },
		"resource result":  func(e *Event) { e.ResourceResult = string(ResourceResultError) },
		"teardown uid":     func(e *Event) { e.WindowUID = "win-abc234" },
		"agent uid":        func(e *Event) { e.AgentUID = "agent-abc234" },
		"create phase":     func(e *Event) { e.PhaseGuardMS = registryLockMS(1) },
		"spawn to release": func(e *Event) { e.SpawnToReleaseMS = registryLockMS(1) },
	} {
		t.Run(name, func(t *testing.T) {
			event := registryLockFixture()
			mutate(&event)
			if _, err := sanitizeEvent(event, ""); err == nil {
				t.Fatalf("accepted %+v", event)
			}
		})
	}

	// Every other family is valid as given and refuses each registry-only mark.
	for name, base := range otherFamilyFixtures() {
		if _, err := sanitizeEvent(base, ""); err != nil {
			t.Fatalf("%s fixture rejected: %v", name, err)
		}
		for mark, mutate := range map[string]func(*Event){
			"wait_ms":            func(e *Event) { e.WaitMS = registryLockMS(0) },
			"component registry": func(e *Event) { e.Component = "registry" },
			"level warn":         func(e *Event) { e.Level = "warn" },
		} {
			event := base
			mutate(&event)
			if _, err := sanitizeEvent(event, ""); err == nil {
				t.Fatalf("%s accepted %s", name, mark)
			}
		}
	}
}

// otherFamilyFixtures returns one valid record of each neighbouring family.
func otherFamilyFixtures() map[string]Event {
	lifecycle := Event{At: "2026-09-29T00:00:00Z", Level: "info", Component: "runtime", Event: "lifecycle.outcome", Result: "success",
		RunID: "run", Version: "1.0.0", MuxBackend: "tmux", Operation: string(OperationSessionCreate)}
	topology := Event{At: "2026-09-29T00:00:00Z", Level: "info", Component: "topology", Event: "topology.outcome", Result: "success",
		RunID: "run", Version: "1.0.0", MuxBackend: "tmux", ResumedCount: intPointer(1), SkippedCount: intPointer(0)}
	surface := Event{At: "2026-09-29T00:00:00Z", Level: "error", Component: "runtime", Event: surfaceUnshownEvent, Result: "error",
		Kind: "runtime", RunID: "run", Version: "1.0.0", MuxBackend: "tmux", Source: string(SurfaceSiteSplitFocus)}
	foreign := Event{At: "2026-09-29T00:00:00Z", Level: "info", Component: "agent", Event: agentMessageForeignSourceEvent, Result: "success",
		RunID: "run", Version: "1.0.0", MuxBackend: "tmux", AgentUID: "agent-abc234", PaneUID: "pane-xyz567"}
	return map[string]Event{
		"command.outcome":   fixtureEvent("run"),
		"lifecycle.outcome": lifecycle,
		"topology.outcome":  topology,
		"teardown":          teardownFixtureEvent(),
		"surface unshown":   surface,
		"create.outcome":    createOutcomeFixture(),
		"agent message":     foreign,
	}
}

// TestRegistryLockWriterFailureIsSilent pins best effort: a journal that
// refuses every append costs one attempt per record and nothing else, and a
// nil recorder is a no-op.
func TestRegistryLockWriterFailureIsSilent(t *testing.T) {
	t.Parallel()
	writer := &topologyFailWriter{}
	recorder := NewLifecycleRecorder(writer, "run", "1.0.0", "tmux").RegistryLock(CommandClass{Command: "create", Subcommand: "agent"})
	recorder.Record(RegistryLockAcquisition{Operation: RegistryLockUpdate, Result: RegistryLockTimeout, Wait: 30 * time.Second})
	recorder.Record(RegistryLockAcquisition{Operation: RegistryLockUpdate, Result: RegistryLockSuccess, Held: registryLockHeld(2 * time.Second)})
	if writer.calls != 2 {
		t.Fatalf("writer calls = %d, want one per record despite failures", writer.calls)
	}
	var owner *LifecycleRecorder
	if owner.RegistryLock(CommandClass{}) != nil {
		t.Fatal("nil lifecycle recorder produced a registry lock recorder")
	}
	owner.RegistryLock(CommandClass{}).Record(RegistryLockAcquisition{Operation: RegistryLockUpdate, Result: RegistryLockTimeout})
}
