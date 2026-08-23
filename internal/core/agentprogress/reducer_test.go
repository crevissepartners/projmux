package agentprogress

import (
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func TestReducerOrdersDedupesSaturatesAndClearsImmediately(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	var reducer Reducer
	reducer.Begin("turn-1", base.Add(-time.Minute), base)
	if got, changed := reducer.Flush(base); !changed || got.TurnRef != "turn-1" {
		t.Fatalf("initial flush = %+v/%t", got, changed)
	}
	if !reducer.Observe(Event{Kind: EventPlanUpdated, TurnRef: "turn-1", PlanCompleted: 2, PlanInProgress: 1, PlanTotal: 4, ObservedAt: base.Add(time.Millisecond)}) {
		t.Fatal("plan update was not semantic")
	}
	if reducer.Observe(Event{Kind: EventPlanUpdated, TurnRef: "turn-1", PlanCompleted: 2, PlanInProgress: 1, PlanTotal: 4, ObservedAt: base.Add(2 * time.Millisecond)}) {
		t.Fatal("identical plan update was semantic")
	}
	if reducer.Observe(Event{Kind: EventPlanUpdated, TurnRef: "turn-old", PlanTotal: 1, ObservedAt: base.Add(3 * time.Millisecond)}) {
		t.Fatal("foreign turn update was accepted")
	}
	if reducer.Observe(Event{Kind: EventDiffUpdated, TurnRef: "turn-1", ChangedFiles: 3, ObservedAt: base.Add(-time.Second)}) {
		t.Fatal("out-of-order update was accepted")
	}

	// Completion-before-start records the opaque id and makes the later start a
	// deterministic duplicate rather than resurrecting an already closed item.
	reducer.Observe(Event{Kind: EventItemCompleted, TurnRef: "turn-1", ItemRef: "item-closed", ObservedAt: base.Add(4 * time.Millisecond)})
	if reducer.Observe(Event{Kind: EventItemStarted, TurnRef: "turn-1", ItemRef: "item-closed", Activity: coremetadata.ProgressCommand, ObservedAt: base.Add(5 * time.Millisecond)}) {
		t.Fatal("completed item was restarted")
	}
	for i := range coremetadata.AgentProgressItemsCap - 1 {
		reducer.Observe(Event{Kind: EventItemStarted, TurnRef: "turn-1", ItemRef: string(rune('a' + i)), Activity: coremetadata.ProgressTool, ObservedAt: base.Add(time.Duration(10+i) * time.Millisecond)})
	}
	if got := reducer.progress.ActiveItemCount; got != coremetadata.AgentProgressItemsCap-1 {
		t.Fatalf("active item count = %d", got)
	}
	reducer.Observe(Event{Kind: EventItemStarted, TurnRef: "turn-1", ItemRef: "overflow", Activity: coremetadata.ProgressWebSearch, ObservedAt: base.Add(50 * time.Millisecond)})
	if got := reducer.progress; got.Activity != coremetadata.ProgressOther || got.ActiveItemCount != coremetadata.AgentProgressItemsCap-1 {
		t.Fatalf("overflow projection = %+v", got)
	}
	if got := reducer.Diagnostics(); got.Overflow != 1 || got.Dropped < 3 {
		t.Fatalf("diagnostics = %+v", got)
	}

	if !reducer.Observe(Event{Kind: EventTurnTerminal, TurnRef: "turn-1", ObservedAt: base.Add(51 * time.Millisecond)}) {
		t.Fatal("terminal did not schedule a clear")
	}
	if got, changed := reducer.Flush(base.Add(51 * time.Millisecond)); !changed || !got.IsZero() {
		t.Fatalf("terminal flush = %+v/%t, want immediate zero", got, changed)
	}
	if !reducer.progress.IsZero() {
		t.Fatal("terminal retained current history")
	}
}

func TestReducerFakeClockNeverExceedsFourWritesPerSecond(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	var reducer Reducer
	reducer.Begin("turn-1", base, base)
	writes := 0
	if _, ok := reducer.Flush(base); ok {
		writes++
	}
	for i := 1; i < 100; i++ {
		now := base.Add(time.Duration(i) * 10 * time.Millisecond)
		reducer.Observe(Event{Kind: EventDiffUpdated, TurnRef: "turn-1", ChangedFiles: uint16(i % 11), ObservedAt: now})
		if _, ok := reducer.Flush(now); ok {
			writes++
		}
	}
	if writes != 4 {
		t.Fatalf("writes in [0,1s) = %d, want 4", writes)
	}
	if at := reducer.lastWriteAt.Add(MinWriteInterval); !reducer.pending || at.Before(base.Add(time.Second)) {
		t.Fatalf("next flush = %s, want >= one second", at)
	}
}

func TestReducerDisconnectInvalidationHasZeroHistoryAndRejectsLateEvent(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	var reducer Reducer
	reducer.Begin("turn-1", base, base)
	if _, changed := reducer.Flush(base); !changed {
		t.Fatal("initial progress was not durable")
	}
	if !reducer.Invalidate() {
		t.Fatal("disconnect did not schedule immediate clear")
	}
	if got, changed := reducer.Flush(base.Add(time.Millisecond)); !changed || !got.IsZero() {
		t.Fatalf("disconnect flush = %+v/%t", got, changed)
	}
	if reducer.Observe(Event{Kind: EventDiffUpdated, TurnRef: "turn-1", ChangedFiles: 1, ObservedAt: base.Add(2 * time.Millisecond)}) {
		t.Fatal("late event repopulated invalidated progress")
	}
	if _, changed := reducer.Flush(base.Add(time.Second)); changed {
		t.Fatal("invalidated reducer retained history")
	}
}

func TestReducerMatchingTerminalClearsDespiteOlderObservedAt(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	var reducer Reducer
	reducer.Begin("turn-1", base, base)
	if _, changed := reducer.Flush(base); !changed {
		t.Fatal("initial progress was not durable")
	}
	if !reducer.Observe(Event{Kind: EventPlanUpdated, TurnRef: "turn-1", PlanTotal: 2, ObservedAt: base.Add(time.Second)}) {
		t.Fatal("newer progress event was not accepted")
	}
	if !reducer.Observe(Event{Kind: EventTurnTerminal, TurnRef: "turn-1", ObservedAt: base.Add(-time.Second)}) {
		t.Fatal("older matching terminal did not schedule immediate clear")
	}
	if got, changed := reducer.Flush(base.Add(time.Millisecond)); !changed || !got.IsZero() {
		t.Fatalf("terminal flush = %+v/%t, want immediate zero", got, changed)
	}
}

func TestReducerDiagnosticsSaturateAtUint32Boundary(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	var reducer Reducer
	reducer.Begin("turn-1", base, base)
	reducer.diagnostics = Diagnostics{Dropped: ^uint32(0) - 1, Unknown: ^uint32(0) - 1, Overflow: ^uint32(0) - 1}
	reducer.Observe(Event{TurnRef: "turn-foreign", UnknownIncrement: 10})
	reducer.items = make(map[string]itemState, coremetadata.AgentProgressItemsCap)
	for i := range coremetadata.AgentProgressItemsCap {
		reducer.items[string(rune(i+1))] = itemState{}
	}
	reducer.Observe(Event{Kind: EventItemStarted, TurnRef: "turn-1", ItemRef: "overflow", Activity: coremetadata.ProgressOther, ObservedAt: base.Add(time.Millisecond)})
	reducer.Observe(Event{Kind: EventItemStarted, TurnRef: "turn-1", ItemRef: "overflow-again", Activity: coremetadata.ProgressOther, ObservedAt: base.Add(2 * time.Millisecond)})
	if got := reducer.Diagnostics(); got != (Diagnostics{Dropped: ^uint32(0), Unknown: ^uint32(0), Overflow: ^uint32(0)}) {
		t.Fatalf("saturated diagnostics = %+v", got)
	}
}
