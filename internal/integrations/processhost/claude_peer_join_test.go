package processhost

import (
	"context"
	"fmt"
	"testing"
)

func TestClaudeNativePeerJoinsWithoutStdinOrNewTurn(t *testing.T) {
	for _, origin := range []string{TurnOriginHost, TurnOriginProvider} {
		t.Run(origin, func(t *testing.T) {
			p := start(t, testHost(t, nil), "normal")
			a := bound(t, p)
			if origin == TurnOriginHost {
				turn(t, p, "running", "hold")
			} else {
				fixtureFrame(t, p, map[string]any{"type": "fixture-provider-output"})
			}
			before := observeUntil(t, p, func(s Snapshot) bool { return s.Turn != "" && turnOpen(p) })
			outputs := len(eventsOf(p, "output"))
			admission, err := p.ReserveClaudePeerMessage(context.Background(), a, "peer", 42)
			if err != nil || !admission.Joined || admission.Turn != before.Turn || admission.Origin != origin {
				t.Fatalf("admission %+v %v", admission, err)
			}
			after, _ := p.Observe(binding())
			if after.Turn != before.Turn || after.Session != before.Session || after.MessageReservation != "" || len(eventsOf(p, "output")) != outputs {
				t.Fatal("peer reservation wrote stdin or replaced turn", after)
			}
			if err := p.FinishClaudeMessage(context.Background(), a, "peer", true, false); err != nil {
				t.Fatal(err)
			}
			fixtureFrame(t, p, map[string]any{"type": "fixture-result"})
			observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" })
			results := eventsOf(p, "turn-result")
			if len(results) != 2 || results[1].Turn != before.Turn {
				t.Fatal("result attribution", results)
			}
			for _, kind := range []string{"peer-message-joined", "peer-message-handed-off"} {
				es := eventsOf(p, kind)
				if len(es) != 1 || es[0].Turn != before.Turn {
					t.Fatal(kind, es)
				}
			}
			fixtureFrame(t, p, map[string]any{"type": "fixture-provider-turn"})
			observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" && len(eventsOf(p, "turn-result")) == 3 })
			after, _ = p.Observe(binding())
			if after.Exit != nil || after.Failure != "" || after.Session != before.Session {
				t.Fatal("provider turn died", after)
			}
		})
	}
}

func TestClaudeNativePeerBusyRefusesBeforeWrite(t *testing.T) {
	for _, mode := range []string{"silent", "question", "interrupt", "handoff", "expired", "limit"} {
		t.Run(mode, func(t *testing.T) {
			p := start(t, testHost(t, nil), "normal")
			a := bound(t, p)
			switch mode {
			case "handoff", "expired":
				if err := p.ReserveClaudeMessage(context.Background(), a, "reserved"); err != nil {
					t.Fatal(err)
				}
				if mode == "expired" {
					p.mu.Lock()
					p.messageReservation = "expired"
					p.mu.Unlock()
				}
			case "limit":
				turn(t, p, "running", "hold")
				observeUntil(t, p, func(Snapshot) bool { return turnOpen(p) })
				for i := range ClaudeJoinedInputs {
					if _, err := p.UserInput(context.Background(), a, fmt.Sprint("operator-", i), "joined"); err != nil {
						t.Fatal(err)
					}
				}
			default:
				turn(t, p, "running", mode)
				if mode == "question" {
					observeUntil(t, p, func(s Snapshot) bool { return len(s.Pending) == 1 })
				}
				if mode == "interrupt" {
					observeUntil(t, p, func(Snapshot) bool { return turnOpen(p) })
					if err := p.Interrupt(context.Background(), a, "running"); err != nil {
						t.Fatal(err)
					}
				}
			}
			before, _ := p.Observe(binding())
			if _, err := p.ReserveClaudePeerMessage(context.Background(), a, "refused", 1); err != ErrClaudeTurnActive {
				t.Fatal(err)
			}
			after, _ := p.Observe(binding())
			if after.Sequence != before.Sequence || after.Turn != before.Turn || after.MessageReservation != before.MessageReservation || len(after.Pending) != len(before.Pending) {
				t.Fatal("busy changed ownership", after)
			}
			p.mu.Lock()
			used := p.usedTurns["refused"]
			p.mu.Unlock()
			if used {
				t.Fatal("busy consumed operation")
			}
		})
	}
}

func TestClaudeNativePeerSharesBudgetAndSettlesLateOutcomes(t *testing.T) {
	for _, outcome := range []string{"written", "unknown", "zero"} {
		t.Run(outcome, func(t *testing.T) {
			p := start(t, testHost(t, nil), "normal")
			a := bound(t, p)
			turn(t, p, "running", "hold")
			before := observeUntil(t, p, func(s Snapshot) bool { return s.Turn != "" && turnOpen(p) })
			if _, err := p.ReserveClaudePeerMessage(context.Background(), a, "peer", ClaudeJoinedInputBytes); err != nil {
				t.Fatal(err)
			}
			if _, err := p.UserInput(context.Background(), a, "operator", "joined"); err != ErrClaudeJoinLimit {
				t.Fatal("operator ignored peer bytes", err)
			}
			if _, err := p.ReserveClaudePeerMessage(context.Background(), a, "over", 1); err != ErrClaudeTurnActive {
				t.Fatal("peer ignored shared bytes", err)
			}
			if outcome == "zero" {
				if err := p.FinishClaudeMessage(context.Background(), a, "peer", false, false); err != nil {
					t.Fatal(err)
				}
				if _, err := p.UserInput(context.Background(), a, "operator", "joined"); err != nil {
					t.Fatal("zero did not refund budget", err)
				}
			}
			fixtureFrame(t, p, map[string]any{"type": "fixture-result"})
			observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" })
			fixtureFrame(t, p, map[string]any{"type": "fixture-provider-output"})
			observeUntil(t, p, func(s Snapshot) bool { return s.Turn != "" })
			if outcome != "zero" {
				if err := p.FinishClaudeMessage(context.Background(), a, "peer", outcome == "written", outcome == "unknown"); err != nil {
					t.Fatal(err)
				}
			}
			after, _ := p.Observe(binding())
			if after.Turn == "" || after.Turn == before.Turn {
				t.Fatal("late finish changed provider turn", after)
			}
			if _, err := p.ReserveClaudePeerMessage(context.Background(), a, "peer", 1); err != ErrStale {
				t.Fatal("retried finished operation", err)
			}
			if err := p.FinishClaudeMessage(context.Background(), a, "peer", true, false); err != nil {
				t.Fatal("duplicate finish", err)
			}
			kind := "peer-message-handed-off"
			if outcome == "unknown" {
				kind = "peer-message-handoff-unknown"
			}
			if outcome == "zero" {
				kind = "peer-message-prewrite-refused"
			}
			es := eventsOf(p, kind)
			if len(es) != 1 || es[0].Turn != before.Turn {
				t.Fatal("late outcome attribution", es)
			}
		})
	}
}

// An outstanding helper outcome can outlive the bounded operation history.
// Its operation remains owned by that handoff, including against operator input.
func TestClaudeNativePeerPendingOperationSurvivesHistoryEviction(t *testing.T) {
	p := start(t, testHost(t, nil), "normal")
	a := bound(t, p)
	turn(t, p, "running", "hold")
	observeUntil(t, p, func(Snapshot) bool { return turnOpen(p) })
	if _, err := p.ReserveClaudePeerMessage(context.Background(), a, "pending-peer", 1); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	for i := 0; i < p.host.limits.Events; i++ {
		p.fenceOperationLocked(fmt.Sprint("other-operation-", i))
	}
	retained := p.usedTurns["pending-peer"]
	p.mu.Unlock()
	if retained {
		t.Fatal("history eviction setup did not evict operation")
	}
	if _, err := p.UserInput(context.Background(), a, "pending-peer", "joined"); err != ErrStale {
		t.Fatal("operator reused outstanding handoff", err)
	}
	if _, err := p.ReserveClaudePeerMessage(context.Background(), a, "pending-peer", 1); err != ErrStale {
		t.Fatal("peer reused outstanding handoff", err)
	}
	if err := p.FinishClaudeMessage(context.Background(), a, "pending-peer", false, true); err != nil {
		t.Fatal("history eviction lost exact outcome", err)
	}
}

func TestClaudeNativePeerCountSharesOperatorLimit(t *testing.T) {
	p := start(t, testHost(t, nil), "normal")
	a := bound(t, p)
	turn(t, p, "running", "hold")
	observeUntil(t, p, func(Snapshot) bool { return turnOpen(p) })
	for i := range ClaudeJoinedInputs - 1 {
		if _, err := p.UserInput(context.Background(), a, fmt.Sprint("operator-", i), "joined"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.ReserveClaudePeerMessage(context.Background(), a, "eighth-peer", 1); err != nil {
		t.Fatal(err)
	}
	if err := p.FinishClaudeMessage(context.Background(), a, "eighth-peer", true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UserInput(context.Background(), a, "ninth-operator", "joined"); err != ErrClaudeJoinLimit {
		t.Fatal("operator exceeded mixed count", err)
	}
	if _, err := p.ReserveClaudePeerMessage(context.Background(), a, "ninth-peer", 1); err != ErrClaudeTurnActive {
		t.Fatal("peer exceeded mixed count", err)
	}
}
