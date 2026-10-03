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
	for _, mode := range []string{"definite-failure", "uncertain-interrupt", "uncertain-stop"} {
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
			if err := p.Turn(context.Background(), a, "busy", "ordinary"); err != ErrBusy {
				t.Fatal("busy admitted", err)
			}
			if err := p.ReserveClaudeMessage(context.Background(), a, "message-1"); err != ErrStale {
				t.Fatal("duplicate admitted", err)
			}
			uncertain := mode != "definite-failure"
			if err := p.FinishClaudeMessage(context.Background(), a, "message-1", false, uncertain); err != nil {
				t.Fatal(err)
			}
			if mode == "uncertain-stop" {
				if err := p.Stop(binding()); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				s, err := p.Wait(ctx, binding())
				if err != nil || s.Exit == nil || s.MessageReservation != "" {
					t.Fatal("Stop did not resolve owned lifetime", s, err)
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
