package processhost

import (
	"context"
	"fmt"
	"testing"
)

func TestSequentialTurnsOutliveHistoryLimits(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			h := testHost(t, nil)
			var p *Handle
			var submit func(string, string) error
			if provider == "claude" {
				p = start(t, h, "normal")
				submit = func(id, prompt string) error { return p.Turn(context.Background(), authority(p), id, prompt) }
			} else {
				c, _, _ := codexStart(t, h, "codex-normal")
				p = c.handle
				submit = func(id, prompt string) error { return c.Turn(context.Background(), codexAuthority(c), id, prompt) }
			}
			for i := 1; i <= 600; i++ {
				id := fmt.Sprintf("operation-%d", i)
				if err := submit(id, "normal"); err != nil {
					t.Fatalf("turn %d: %v", i, err)
				}
				observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" && s.State == "ready" })
				if err := submit(id, "must-not-replay"); err != ErrStale {
					t.Fatalf("recent duplicate %d: %v", i, err)
				}
				p.mu.Lock()
				turns, critical := len(p.usedTurns), len(p.critical)
				p.mu.Unlock()
				if turns > h.limits.Events || critical > h.limits.Events {
					t.Fatalf("unbounded completed history: %d/%d", turns, critical)
				}
			}
			if !hasEvent(p, "stream-gap") || !hasEvent(p, "turn-result") {
				t.Fatal("lost honest gap or latest result")
			}
			// A retained, live operation cannot be displaced by a foreign request.
			if err := submit("held", "hold"); err != nil {
				t.Fatal(err)
			}
			if err := submit("held", "duplicate"); err != ErrStale {
				t.Fatal(err)
			}
			busy := ErrBusy
			if provider == "claude" {
				busy = ErrClaudeJoinUnsupported
			}
			if err := submit("new", "competing"); err != busy {
				t.Fatal(err)
			}
		})
	}
}

func TestHistoryRetainsPendingRequestEvidence(t *testing.T) {
	p := start(t, testHost(t, func(_ *Transactions, l *Limits) { l.Events = 4 }), "normal")
	for i := range 12 {
		turn(t, p, fmt.Sprint(i), "normal")
		observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" && s.State == "ready" })
	}
	turn(t, p, "question", "question")
	snap := observeUntil(t, p, func(s Snapshot) bool { return len(s.Pending) == 1 })
	if !hasEvent(p, "control-pending") {
		t.Fatal("pending request evicted")
	}
	if err := p.Respond(context.Background(), authority(p), snap.Pending[0], Response{Answers: map[string]string{"Color?": "blue"}}); err != nil {
		t.Fatal(err)
	}
	observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" })
	if err := p.Respond(context.Background(), authority(p), snap.Pending[0], Response{}); err != ErrStale {
		t.Fatal(err)
	}
}

// Output may have an older eviction cursor than independently trimmed control
// history. Its next eviction must not hide the newer control gap.
func TestHistoryGapNeverRegressesAcrossQueues(t *testing.T) {
	p := &Handle{host: &Host{limits: Limits{Events: 2}}, requests: map[string]Request{}}
	p.emitLocked("output", nil, nil)
	p.emitLocked("output", nil, nil)
	for range 4 {
		p.emitLocked("turn-result", nil, nil)
	}
	gap := p.droppedThrough
	p.emitLocked("output", nil, nil)
	if p.droppedThrough != gap {
		t.Fatalf("gap regressed from %d to %d", gap, p.droppedThrough)
	}
}

func TestHistoryReportsGapWithoutOutput(t *testing.T) {
	p := &Handle{host: &Host{limits: Limits{Events: 2}}, requests: map[string]Request{}}
	for range 4 {
		p.emitLocked("turn-result", nil, nil)
	}
	events, _, err := p.Events(Binding{}, 0)
	if err != nil || len(events) != 3 || events[0].Kind != "stream-gap" {
		t.Fatalf("critical-only eviction gap: %v %v", events, err)
	}
}
