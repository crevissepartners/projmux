package diagnostics

import (
	"path/filepath"
	"testing"
	"time"
)

func createDuration(d time.Duration) *time.Duration { return &d }

func createLockHeld(ms int64) *int64 { return &ms }

func createOutcomeFixture() Event {
	return Event{
		At: "2026-09-19T00:00:00Z", Level: "info", Component: "create", Event: createOutcomeEvent,
		Result: "success", DurationMS: 40, RunID: "create-run", Version: "1.0.0", MuxBackend: "tmux",
		Operation: string(CreateKindWindow), LockHeldMS: createLockHeld(30),
	}
}

func TestCreateOutcomeRoundTripKeepsKindAndLockHeld(t *testing.T) {
	t.Parallel()
	store := NewStore(filepath.Join(t.TempDir(), "operations.jsonl"))
	owner := NewLifecycleRecorder(store, "create-run", "1.0.0", "tmux")
	recorder := owner.Create()
	recorder.Record(CreateOutcome{Kind: CreateKindAgent, Result: LifecycleSuccess, Duration: 90 * time.Millisecond, LockHeld: createDuration(60 * time.Millisecond)})
	recorder.Record(CreateOutcome{Kind: CreateKindResume, Result: LifecycleError, Duration: 5 * time.Millisecond})
	events, err := store.Read()
	if err != nil || len(events) != 2 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	ok := events[0]
	if ok.Component != "create" || ok.Event != "create.outcome" || ok.Operation != "agent" || ok.Result != "success" ||
		ok.Level != "info" || ok.Kind != "" || ok.DurationMS != 90 || ok.LockHeldMS == nil || *ok.LockHeldMS != 60 {
		t.Fatalf("success outcome = %+v", ok)
	}
	failed := events[1]
	if failed.Operation != "resume" || failed.Result != "error" || failed.Level != "error" || failed.Kind != "runtime" ||
		failed.DurationMS != 5 || failed.LockHeldMS != nil {
		t.Fatalf("error outcome before the lock = %+v", failed)
	}
	// Measurement is not an outcome: the invocation's command.outcome stays.
	if owner.RecordedOutcome() {
		t.Fatal("create.outcome claimed the top-level command outcome")
	}
}

func TestCreateOutcomeRecordsEveryCallAndClampsTimings(t *testing.T) {
	t.Parallel()
	store := NewStore(filepath.Join(t.TempDir(), "operations.jsonl"))
	recorder := NewLifecycleRecorder(store, "create-run", "1.0.0", "tmux").Create()
	for range 3 {
		recorder.Record(CreateOutcome{Kind: CreateKindPane, Result: LifecycleSuccess, Duration: 10 * time.Millisecond, LockHeld: createDuration(time.Second)})
	}
	recorder.Record(CreateOutcome{Kind: CreateKindPane, Result: LifecycleSuccess, Duration: -time.Second, LockHeld: createDuration(-time.Second)})
	events, err := store.Read()
	if err != nil || len(events) != 4 {
		t.Fatalf("one record per call: events=%+v err=%v", events, err)
	}
	for _, event := range events {
		if event.LockHeldMS == nil || *event.LockHeldMS < 0 || *event.LockHeldMS > event.DurationMS {
			t.Fatalf("timings not clamped to 0 <= lock_held_ms <= duration_ms: %+v", event)
		}
	}
}

func TestCreateOutcomeDropsUnknownKindAndResult(t *testing.T) {
	t.Parallel()
	for _, outcome := range []CreateOutcome{
		{Kind: "", Result: LifecycleSuccess},
		{Kind: "rename", Result: LifecycleSuccess},
		{Kind: "project", Result: LifecycleSuccess},
		{Kind: CreateKindWindow, Result: "started"},
	} {
		writer := &topologyFailWriter{}
		NewLifecycleRecorder(writer, "run", "1.0.0", "tmux").Create().Record(outcome)
		if writer.calls != 0 {
			t.Fatalf("accepted %+v", outcome)
		}
	}
}

func TestCreateOutcomeWriterFailureAndNilRecorderAreSilent(t *testing.T) {
	t.Parallel()
	writer := &topologyFailWriter{}
	recorder := NewLifecycleRecorder(writer, "run", "1.0.0", "tmux").Create()
	recorder.Record(CreateOutcome{Kind: CreateKindWindow, Result: LifecycleError})
	recorder.Record(CreateOutcome{Kind: CreateKindWindow, Result: LifecycleError})
	if writer.calls != 2 {
		t.Fatalf("writer calls = %d, want one per record despite failures", writer.calls)
	}
	var owner *LifecycleRecorder
	if owner.Create() != nil {
		t.Fatal("nil lifecycle recorder produced a create recorder")
	}
	owner.Create().Record(CreateOutcome{Kind: CreateKindWindow, Result: LifecycleSuccess})
}

func TestCreateOutcomeEventClosedShape(t *testing.T) {
	t.Parallel()
	for _, kind := range createKinds {
		event := createOutcomeFixture()
		event.Operation = string(kind)
		if _, err := sanitizeEvent(event, ""); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
	}
	for name, mutate := range map[string]func(*Event){
		"no lock":        func(e *Event) { e.LockHeldMS = nil },
		"zero lock":      func(e *Event) { e.LockHeldMS = createLockHeld(0) },
		"lock is whole":  func(e *Event) { e.LockHeldMS = createLockHeld(e.DurationMS) },
		"error":          func(e *Event) { e.Level, e.Result, e.Kind = "error", "error", "runtime" },
		"error pre-lock": func(e *Event) { e.Level, e.Result, e.Kind, e.LockHeldMS = "error", "error", "runtime", nil },
	} {
		event := createOutcomeFixture()
		mutate(&event)
		if _, err := sanitizeEvent(event, ""); err != nil {
			t.Fatalf("%s rejected: %v", name, err)
		}
	}
	for name, mutate := range map[string]func(*Event){
		"unknown kind":     func(e *Event) { e.Operation = "rename" },
		"missing kind":     func(e *Event) { e.Operation = "" },
		"runtime op":       func(e *Event) { e.Operation = string(OperationSessionCreate) },
		"negative lock":    func(e *Event) { e.LockHeldMS = createLockHeld(-1) },
		"lock > duration":  func(e *Event) { e.LockHeldMS = createLockHeld(e.DurationMS + 1) },
		"component":        func(e *Event) { e.Component = "runtime" },
		"started":          func(e *Event) { e.Result = "started" },
		"error as info":    func(e *Event) { e.Result, e.Kind = "error", "runtime" },
		"error no kind":    func(e *Event) { e.Level, e.Result = "error", "error" },
		"success kind":     func(e *Event) { e.Kind = "runtime" },
		"command":          func(e *Event) { e.Command, e.Subcommand = "create", "window" },
		"message":          func(e *Event) { e.Message = "private prompt" },
		"code":             func(e *Event) { e.Code = string(CodeSessionCreateFailed) },
		"source":           func(e *Event) { e.Source = "manual" },
		"count":            func(e *Event) { e.ItemCount = intPointer(1) },
		"topology count":   func(e *Event) { e.ResumedCount = intPointer(1) },
		"teardown uid":     func(e *Event) { e.WindowUID = "win-alpha" },
		"provider":         func(e *Event) { e.Provider = "codex" },
		"resource result":  func(e *Event) { e.ResourceResult = string(ResourceResultError) },
		"ai failure field": func(e *Event) { e.Failure = string(AIFailureRoute) },
	} {
		t.Run(name, func(t *testing.T) {
			event := createOutcomeFixture()
			mutate(&event)
			if _, err := sanitizeEvent(event, ""); err == nil {
				t.Fatalf("accepted %+v", event)
			}
		})
	}
	// The new field and component belong to create.outcome alone.
	for _, family := range []string{"command.outcome", "lifecycle.outcome", "topology.outcome", teardownDecisionEvent, surfaceUnshownEvent} {
		event := fixtureEvent("run")
		event.Event = family
		event.LockHeldMS = createLockHeld(0)
		if _, err := sanitizeEvent(event, ""); err == nil {
			t.Fatalf("lock_held_ms accepted by %s", family)
		}
	}
	event := fixtureEvent("run")
	event.Component = "create"
	if _, err := sanitizeEvent(event, ""); err == nil {
		t.Fatal("component create accepted on command.outcome")
	}
}
