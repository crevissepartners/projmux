package diagnostics

import (
	"errors"
	"sync"
	"testing"
	"time"
)

type recordingEventWriter struct {
	mu     sync.Mutex
	events []Event
	err    error
}

func (w *recordingEventWriter) Append(event Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events = append(w.events, event)
	return w.err
}

func (w *recordingEventWriter) snapshot() []Event {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]Event(nil), w.events...)
}

func TestLifecycleRecorderOperationSuccessAndFailureTable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		operation Operation
		code      Code
	}{
		{OperationSessionCreate, CodeSessionCreateFailed},
		{OperationSessionAttach, CodeSessionAttachFailed},
		{OperationSessionSwitch, CodeSessionSwitchFailed},
		{OperationSessionKill, CodeSessionKillFailed},
		{OperationTmuxApply, CodeTmuxApplyFailed},
	}
	for _, tt := range tests {
		for _, fail := range []bool{false, true} {
			name := string(tt.operation) + map[bool]string{false: "/success", true: "/failure"}[fail]
			t.Run(name, func(t *testing.T) {
				writer := &recordingEventWriter{}
				recorder := NewLifecycleRecorder(writer, "same-run", "0.10.0", "tmux")
				finish := recorder.BeginCommand()
				recorder.Mark(tt.operation)
				var commandErr error
				if fail {
					commandErr = errors.New("private command detail")
				}
				finish(commandErr)

				events := writer.snapshot()
				if len(events) != 2 {
					t.Fatalf("events = %#v, want start and one outcome", events)
				}
				if events[0].Event != "lifecycle.start" || events[0].Result != "started" || events[1].Event != "lifecycle.outcome" {
					t.Fatalf("events = %#v, want start -> outcome", events)
				}
				if events[0].RunID != "same-run" || events[1].RunID != "same-run" || events[0].Operation != string(tt.operation) || events[1].Operation != string(tt.operation) {
					t.Fatalf("correlation = %#v", events)
				}
				if fail {
					if events[1].Result != "error" || events[1].Code != string(tt.code) || events[1].Message != "" {
						t.Fatalf("failure outcome = %#v", events[1])
					}
				} else if events[1].Result != "success" || events[1].Code != "" {
					t.Fatalf("success outcome = %#v", events[1])
				}
			})
		}
	}
}

func TestLifecycleRecorderCoalescesCreateThenSwitchIntoOneOutcome(t *testing.T) {
	t.Parallel()
	writer := &recordingEventWriter{}
	recorder := NewLifecycleRecorder(writer, "composite-run", "0.10.0", "tmux")
	finish := recorder.BeginCommand()
	recorder.Mark(OperationSessionCreate)
	recorder.Mark(OperationSessionSwitch)
	finish(nil)
	finish(errors.New("late duplicate"))

	events := writer.snapshot()
	if len(events) != 2 || events[0].Operation != string(OperationSessionCreate) || events[1].Operation != string(OperationSessionCreate) || events[1].Result != "success" {
		t.Fatalf("composite events = %#v, want create start plus one success outcome", events)
	}
}

func TestLifecycleRecorderWriteFailureIsBestEffortAndStillOwnsOutcome(t *testing.T) {
	t.Parallel()
	writer := &recordingEventWriter{err: errors.New("permission denied")}
	recorder := NewLifecycleRecorder(writer, "blocked-run", "0.10.0", "tmux")
	finish := recorder.BeginCommand()
	recorder.Mark(OperationSessionKill)
	original := errors.New("tmux kill failed")
	finish(original)

	if !recorder.RecordedOutcome() {
		t.Fatal("logical lifecycle outcome was lost after append failure")
	}
	if events := writer.snapshot(); len(events) != 2 {
		t.Fatalf("append attempts = %d, want start and outcome", len(events))
	}
	fallback := NewStore(t.TempDir() + "/fallback.jsonl")
	if err := RecordOutcome(fallback, []string{"kill", "tagged"}, "blocked-run", "0.10.0", "tmux", time.Now(), original, false, recorder.RecordedOutcome()); err != nil {
		t.Fatalf("suppressed fallback error = %v", err)
	}
	if events, err := fallback.Read(); err != nil || len(events) != 0 {
		t.Fatalf("fallback events = %#v err=%v, want none after logical ownership", events, err)
	}
}

func TestLifecycleRecorderSuppressesTopLevelDuplicate(t *testing.T) {
	t.Parallel()
	path := t.TempDir() + "/operations.jsonl"
	store := NewStore(path)
	recorder := NewLifecycleRecorder(store, "owned-run", "0.10.0", "tmux")
	finish := recorder.BeginCommand()
	recorder.Mark(OperationSessionAttach)
	finish(nil)
	if err := RecordOutcome(store, []string{"attach", "auto"}, "owned-run", "0.10.0", "tmux", time.Now(), nil, false, recorder.RecordedOutcome()); err != nil {
		t.Fatalf("RecordOutcome() error = %v", err)
	}
	events, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Event != "lifecycle.start" || events[1].Event != "lifecycle.outcome" {
		t.Fatalf("events = %#v, want lifecycle pair only", events)
	}
}

func TestLifecycleRecorderConcurrentMarksAndFinishRemainSingle(t *testing.T) {
	t.Parallel()
	writer := &recordingEventWriter{}
	recorder := NewLifecycleRecorder(writer, "race-run", "0.10.0", "tmux")
	finish := recorder.BeginCommand()
	var wg sync.WaitGroup
	for _, operation := range []Operation{OperationSessionCreate, OperationSessionAttach, OperationSessionSwitch, OperationSessionKill} {
		wg.Go(func() {
			recorder.Mark(operation)
		})
	}
	wg.Wait()
	for range 8 {
		wg.Go(func() {
			finish(nil)
		})
	}
	wg.Wait()
	if events := writer.snapshot(); len(events) != 2 {
		t.Fatalf("events = %#v, want one start and one outcome", events)
	}
}
