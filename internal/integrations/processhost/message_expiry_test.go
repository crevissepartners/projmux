package processhost

import (
	"context"
	"testing"
	"time"
)

func TestClaudeMessageReservationExpiresWithoutInferringCompletion(t *testing.T) {
	for _, finish := range []bool{false, true} {
		t.Run(map[bool]string{false: "lost-helper", true: "uncertain-outcome"}[finish], func(t *testing.T) {
			h := testHost(t, func(_ *Transactions, limits *Limits) { limits.MessageReservation = 40 * time.Millisecond })
			p := start(t, h, "normal")
			turn(t, p, "first", "ordinary")
			s := observeUntil(t, p, func(s Snapshot) bool { return s.State == "ready" && s.Turn == "" })
			a := Authority{Binding: binding(), Connection: s.Connection, Session: s.Session}
			if err := p.ReserveClaudeMessage(context.Background(), a, "uncertain"); err != nil {
				t.Fatal(err)
			}
			if finish {
				if err := p.FinishClaudeMessage(context.Background(), a, "uncertain", false, true); err != nil {
					t.Fatal(err)
				}
			}
			if err := p.Interrupt(context.Background(), a, "uncertain"); err != nil {
				t.Fatal(err)
			}
			s = observeUntil(t, p, func(s Snapshot) bool { return s.MessageReservation == "expired" })
			if s.Turn != "uncertain" || s.Exit != nil || s.State != "ready" || !hasEvent(p, "message-reservation-expired") {
				t.Fatal("expiry inferred completion/exit", s)
			}
			if err := p.Turn(context.Background(), a, "next", "ordinary"); err != ErrBusy {
				t.Fatal("expired generation admitted next turn", err)
			}
			// A late helper acknowledgement is not a provider result.
			if err := p.FinishClaudeMessage(context.Background(), a, "uncertain", true, false); err != nil {
				t.Fatal(err)
			}
			s, _ = p.Observe(binding())
			if s.MessageReservation != "expired" {
				t.Fatal("late handoff undid expiry", s)
			}
			// Deliver an actual result on the owned stream; no new turn was admitted.
			p.mu.Lock()
			err := p.writeLocked(context.Background(), map[string]any{"type": "fixture-release-interrupt"})
			p.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			s = observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" })
			if s.Exit != nil || s.MessageReservation != "" {
				t.Fatal("late actual result did not settle old turn", s)
			}
			events, _, _ := p.Events(binding(), 0)
			expired, results := 0, 0
			for _, e := range events {
				if e.Kind == "message-reservation-expired" && e.Turn == "uncertain" {
					expired++
				}
				if e.Kind == "turn-result" && e.Turn == "uncertain" {
					results++
				}
			}
			if expired != 1 || results != 1 {
				t.Fatal("transition multiplicity", expired, results)
			}
			turn(t, p, "after-result", "ordinary")
			observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" })
		})
	}
}

func TestClaudeMessageExpiryThenStopAndResumeNewGeneration(t *testing.T) {
	h := testHost(t, func(_ *Transactions, l *Limits) { l.MessageReservation = 30 * time.Millisecond })
	p := start(t, h, "normal")
	turn(t, p, "first", "ordinary")
	s := observeUntil(t, p, func(s Snapshot) bool { return s.State == "ready" && s.Turn == "" })
	a := Authority{Binding: binding(), Connection: s.Connection, Session: s.Session}
	if err := p.ReserveClaudeMessage(context.Background(), a, "expired"); err != nil {
		t.Fatal(err)
	}
	s = observeUntil(t, p, func(s Snapshot) bool { return s.MessageReservation == "expired" })
	record := RecordSession("claude", s)
	stopResume(t, p)
	s, _ = p.Observe(binding())
	if s.Exit == nil || s.MessageReservation != "" {
		t.Fatal("actual Wait missing", s)
	}
	if err := p.Turn(context.Background(), a, "after-stop", "ordinary"); err != ErrClosed {
		t.Fatal(err)
	}
	next := binding()
	next.Generation = "new-generation"
	next.Operation = "new-operation"
	resumed, err := testHost(t, nil).ResumeClaude(context.Background(), Launch{Binding: next, Command: fixtureCommand("resume-claude")}, record, "resume-turn", "ordinary")
	if err != nil {
		t.Fatal(err)
	}
	defer stopResume(t, resumed)
	s = waitResume(t, resumed, func(s Snapshot) bool { return s.Turn == "" && s.State == "ready" })
	if s.Session != record.Session || s.Binding.Generation == record.Binding.Generation {
		t.Fatal("resume lost generation/session fence", s)
	}
}

func TestClaudeDefiniteHandoffDoesNotExpireRunningTurn(t *testing.T) {
	h := testHost(t, func(_ *Transactions, l *Limits) { l.MessageReservation = 20 * time.Millisecond })
	p := start(t, h, "normal")
	turn(t, p, "first", "ordinary")
	s := observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" && s.State == "ready" })
	a := Authority{Binding: binding(), Connection: s.Connection, Session: s.Session}
	if err := p.ReserveClaudeMessage(context.Background(), a, "written"); err != nil {
		t.Fatal(err)
	}
	if err := p.FinishClaudeMessage(context.Background(), a, "written", true, false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * h.limits.MessageReservation)
	s, _ = p.Observe(binding())
	if s.MessageReservation != "" || s.Turn != "written" || hasEvent(p, "message-reservation-expired") {
		t.Fatal("known handoff expired", s)
	}
}

func TestClaudeExpiredPrewriteOutcomeDoesNotReopenGeneration(t *testing.T) {
	h := testHost(t, func(_ *Transactions, l *Limits) { l.MessageReservation = 20 * time.Millisecond })
	p := start(t, h, "normal")
	turn(t, p, "first", "ordinary")
	s := observeUntil(t, p, func(s Snapshot) bool { return s.State == "ready" && s.Turn == "" })
	a := Authority{Binding: binding(), Connection: s.Connection, Session: s.Session}
	if err := p.ReserveClaudeMessage(context.Background(), a, "lost-helper"); err != nil {
		t.Fatal(err)
	}
	observeUntil(t, p, func(s Snapshot) bool { return s.MessageReservation == "expired" })
	if err := p.FinishClaudeMessage(context.Background(), a, "lost-helper", false, false); err != nil {
		t.Fatal(err)
	}
	s, _ = p.Observe(binding())
	if s.MessageReservation != "expired" || s.Turn != "lost-helper" {
		t.Fatal("late helper outcome reopened expired generation", s)
	}
	if err := p.Turn(context.Background(), a, "next", "ordinary"); err != ErrBusy {
		t.Fatal(err)
	}
}
