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
	for i := 0; i < coremetadata.AgentProgressItemsCap-1; i++ {
		reducer.Observe(Event{Kind: EventItemStarted, TurnRef: "turn-1", ItemRef: string(rune('a' + i)), Activity: coremetadata.ProgressTool, ObservedAt: base.Add(time.Duration(10+i) * time.Millisecond)})
	}
	if got := reducer.Current().ActiveItemCount; got != coremetadata.AgentProgressItemsCap-1 {
		t.Fatalf("active item count = %d", got)
	}
	reducer.Observe(Event{Kind: EventItemStarted, TurnRef: "turn-1", ItemRef: "overflow", Activity: coremetadata.ProgressWebSearch, ObservedAt: base.Add(50 * time.Millisecond)})
	if got := reducer.Current(); got.Activity != coremetadata.ProgressOther || got.ActiveItemCount != coremetadata.AgentProgressItemsCap-1 {
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
	if !reducer.Current().IsZero() {
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
	if at := reducer.NextFlushAt(); at.Before(base.Add(time.Second)) {
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
