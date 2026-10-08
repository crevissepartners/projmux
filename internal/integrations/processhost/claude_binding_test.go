package processhost

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestClaudeHookBindingRequiresOwnedChildAndExactGeneration(t *testing.T) {
	p := start(t, testHost(t, nil), "normal")
	s, _ := p.Observe(binding())
	wrong := binding()
	wrong.Generation = "old"
	for _, tc := range []struct {
		b       Binding
		pid     int
		session string
	}{
		{wrong, s.PID, "session"}, {binding(), s.PID + 1, "session"}, {binding(), s.PID, ""},
	} {
		if err := p.BindClaudeHook(context.Background(), tc.b, tc.pid, tc.session); err != ErrStale {
			t.Fatalf("self-asserted binding admitted: %v", err)
		}
	}
	if err := p.BindClaudeHook(context.Background(), binding(), s.PID, "session"); err != nil {
		t.Fatal(err)
	}
	if err := p.BindClaudeHook(context.Background(), binding(), s.PID, "session"); err != nil {
		t.Fatal(err)
	}
	if err := p.BindClaudeHook(context.Background(), binding(), s.PID, "other"); err != ErrStale {
		t.Fatalf("session replaced: %v", err)
	}
	s, _ = p.Observe(binding())
	if s.Session != "" || s.State != "starting" {
		t.Fatalf("hook invented readiness: %+v", s)
	}
	turn(t, p, "first", "ordinary")
	observeUntil(t, p, func(s Snapshot) bool { return s.State == "ready" && s.Turn == "" })
}

func TestClaudeStreamInitMustAgreeWithVerifiedHook(t *testing.T) {
	p := start(t, testHost(t, nil), "normal")
	s, _ := p.Observe(binding())
	if err := p.BindClaudeHook(context.Background(), binding(), s.PID, "wrong-session"); err != nil {
		t.Fatal(err)
	}
	turn(t, p, "first", "ordinary")
	s = observeUntil(t, p, func(s Snapshot) bool { return s.Exit != nil })
	if s.Session != "" {
		t.Fatalf("wrong session committed: %+v", s)
	}
}

func TestClaudeMessageReservationReleaseAndUncertainty(t *testing.T) {
	for _, mode := range []string{"definite-failure", "uncertain-interrupt", "uncertain-stop", "uncertain-idle-stop"} {
		t.Run(mode, func(t *testing.T) {
			p := start(t, testHost(t, nil), "normal")
			turn(t, p, "first", "ordinary")
			s := observeUntil(t, p, func(s Snapshot) bool { return s.State == "ready" && s.Turn == "" })
			a := Authority{Binding: binding(), Connection: s.Connection, Session: s.Session}
			if err := p.ReserveClaudeMessage(context.Background(), a, "message-1"); err != nil {
				t.Fatal(err)
			}
			s, _ = p.Observe(binding())
			if s.MessageReservation != "awaiting-message-handoff" || s.Turn != "message-1" {
				t.Fatal(s)
			}
			if err := p.Turn(context.Background(), a, "busy", "ordinary"); err != ErrClaudeJoinUnsupported {
				t.Fatal("busy admitted", err)
			}
			if err := p.ReserveClaudeMessage(context.Background(), a, "message-1"); err != ErrStale {
				t.Fatal("duplicate admitted", err)
			}
			uncertain := mode != "definite-failure"
			if err := p.FinishClaudeMessage(context.Background(), a, "message-1", false, uncertain); err != nil {
				t.Fatal(err)
			}
			if mode == "uncertain-idle-stop" {
				if err := p.Interrupt(context.Background(), a, "message-1"); err != nil {
					t.Fatal(err)
				}
				observeUntil(t, p, func(Snapshot) bool { return hasEvent(p, "interrupt-ack") })
				s, _ = p.Observe(binding())
				if s.Turn != "message-1" || s.MessageReservation != "awaiting-message-handoff" || s.Exit != nil {
					t.Fatal("idle ack inferred cancellation", s)
				}
				if err := p.Turn(context.Background(), a, "blocked-after-idle-ack", "ordinary"); err != ErrClaudeJoinUnsupported {
					t.Fatal("idle ack admitted next turn", err)
				}
			}
			if mode == "uncertain-stop" || mode == "uncertain-idle-stop" {
				if err := p.Stop(binding()); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				s, err := p.Wait(ctx, binding())
				if err != nil || s.Exit == nil || s.MessageReservation != "" {
					t.Fatal("Stop did not resolve owned lifetime", s, err)
				}
				if err := p.Turn(context.Background(), a, "after-stop", "ordinary"); err != ErrClosed {
					t.Fatal("old generation reopened", err)
				}
				return
			}
			if uncertain {
				if err := p.Interrupt(context.Background(), a, "message-1"); err != nil {
					t.Fatal(err)
				}
				observeUntil(t, p, func(s Snapshot) bool { return hasEvent(p, "interrupt-ack") })
				s, _ = p.Observe(binding())
				if s.Turn != "message-1" || s.Exit != nil {
					t.Fatal("ack inferred cancellation", s)
				}
				p.mu.Lock()
				err := p.writeLocked(context.Background(), map[string]any{"type": "fixture-release-interrupt"})
				p.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
				s = observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" })
				if s.Exit != nil || !hasEvent(p, "turn-result") {
					t.Fatal("interrupt inferred exit/result", s)
				}
			}
			s, _ = p.Observe(binding())
			if s.Turn != "" || s.MessageReservation != "" {
				t.Fatal("reservation blocks next turn", s)
			}
			turn(t, p, "next", "ordinary")
			observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" })
			if err := p.ReserveClaudeMessage(context.Background(), a, "message-1"); err != ErrStale {
				t.Fatal("released ID replayed", err)
			}
		})
	}
}

func TestClaudeUnknownFramesRemainBoundedObservations(t *testing.T) {
	p := start(t, testHost(t, nil), "normal")
	turn(t, p, "unknown", "unknown-noise")
	s := observeUntil(t, p, func(s Snapshot) bool { return s.State == "ready" && s.Turn == "" })
	if s.Exit != nil || s.Failure != "" || len(s.Pending) != 0 {
		t.Fatal("observation promoted to control/exit", s)
	}
	events, _, err := p.Events(binding(), 0)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, e := range events {
		if bytes.Contains(e.Raw, []byte("command_lifecycle")) {
			count++
			if e.Kind != "provider-event" {
				t.Fatal("unknown frame promoted", e.Kind)
			}
		}
	}
	if count == 0 || count > p.host.limits.Events {
		t.Fatal("observation not bounded", count)
	}
	turn(t, p, "after-observations", "ordinary")
	observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" })
}

func TestClaudeBusyPeerPreservesActiveOwnershipAndResultWake(t *testing.T) {
	completed := make(chan Binding, 4)
	h := testHost(t, nil)
	p, err := h.Start(context.Background(), Launch{Binding: binding(), Command: fixtureCommand("normal"), TurnCompleted: &TurnCompletion{Notify: func(b Binding) { completed <- b }}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = p.Stop(binding())
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = p.Wait(ctx, binding())
	})
	turn(t, p, "first", "ordinary")
	observeUntil(t, p, func(s Snapshot) bool { return s.State == "ready" && s.Turn == "" })
	select {
	case b := <-completed:
		if b != binding() {
			t.Fatal(b)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("result wake missing")
	}
	turn(t, p, "active", "question")
	before := observeUntil(t, p, func(s Snapshot) bool { return len(s.Pending) == 1 })
	a := authority(p)
	if err = p.ReserveClaudeMessage(context.Background(), a, "peer"); err != ErrClaudeTurnActive {
		t.Fatalf("busy classification: %v", err)
	}
	after, _ := p.Observe(binding())
	if after.Turn != before.Turn || len(after.Pending) != 1 || after.Pending[0].ID != before.Pending[0].ID || after.MessageReservation != "" {
		t.Fatalf("ownership changed: %+v", after)
	}
	select {
	case <-completed:
		t.Fatal("busy refusal woke result")
	default:
	}
	if err = p.Respond(context.Background(), a, before.Pending[0], Response{Answers: map[string]string{"Color?": "blue"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-completed:
	case <-time.After(5 * time.Second):
		t.Fatal("second result wake missing")
	}
	if err = p.ReserveClaudeMessage(context.Background(), a, "peer"); err != nil {
		t.Fatal("busy consumed peer ID", err)
	}
}
