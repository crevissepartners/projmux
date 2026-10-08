package processhost

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func turnOpen(p *Handle) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.turnOpen
}

// refuseNamed asserts a zero-write refusal with an exact named reason that
// keeps ErrBusy and leaves the operation unconsumed.
func refuseNamed(t *testing.T, p *Handle, submit func(operation string) error, want error) {
	t.Helper()
	before, _ := p.Observe(binding())
	err := submit("refused")
	if err != want || !errors.Is(err, ErrBusy) || err.Error() == ErrBusy.Error() {
		t.Fatalf("refusal %v, want named %v", err, want)
	}
	if after, _ := p.Observe(binding()); after.Sequence != before.Sequence || after.Turn != before.Turn || after.MessageReservation != before.MessageReservation {
		t.Fatalf("refusal changed state: %+v -> %+v", before, after)
	}
	p.mu.Lock()
	consumed := p.usedTurns["refused"]
	p.mu.Unlock()
	if consumed {
		t.Fatal("refusal consumed the operation")
	}
}

// Every Claude busy refusal names its reason and stays errors.Is(ErrBusy).
func TestClaudeBusyRefusalsNameTheirReason(t *testing.T) {
	join := func(p *Handle, a Authority) func(string) error {
		return func(op string) error { _, err := p.UserInput(context.Background(), a, op, "joined"); return err }
	}
	t.Run("join-unsupported", func(t *testing.T) {
		p := start(t, testHost(t, nil), "normal")
		a := bound(t, p)
		turn(t, p, "running", "hold")
		observeUntil(t, p, func(Snapshot) bool { return turnOpen(p) })
		refuseNamed(t, p, func(op string) error { return p.Turn(context.Background(), a, op, "ordinary") }, ErrClaudeJoinUnsupported)
		fixtureFrame(t, p, map[string]any{"type": "fixture-result"})
		observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" })
	})
	t.Run("turn-not-open", func(t *testing.T) {
		p := start(t, testHost(t, nil), "normal")
		a := bound(t, p)
		turn(t, p, "silent", "silent")
		refuseNamed(t, p, join(p, a), ErrClaudeTurnNotOpen)
		fixtureFrame(t, p, map[string]any{"type": "fixture-result"})
		observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" })
	})
	t.Run("message-handoff-pending", func(t *testing.T) {
		p := start(t, testHost(t, nil), "normal")
		a := bound(t, p)
		if err := p.ReserveClaudeMessage(context.Background(), a, "message"); err != nil {
			t.Fatal(err)
		}
		refuseNamed(t, p, join(p, a), ErrClaudeMessageHandoff)
		if err := p.FinishClaudeMessage(context.Background(), a, "message", false, false); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("message-handoff-expired", func(t *testing.T) {
		p := start(t, testHost(t, func(_ *Transactions, l *Limits) { l.MessageReservation = 20 * time.Millisecond }), "normal")
		a := bound(t, p)
		if err := p.ReserveClaudeMessage(context.Background(), a, "message"); err != nil {
			t.Fatal(err)
		}
		observeUntil(t, p, func(s Snapshot) bool { return s.MessageReservation == "expired" })
		refuseNamed(t, p, join(p, a), ErrClaudeMessageHandoffExpired)
		fixtureFrame(t, p, map[string]any{"type": "fixture-result"})
		observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" })
	})
	t.Run("interrupt-pending", func(t *testing.T) {
		p := start(t, testHost(t, nil), "normal")
		a := bound(t, p)
		turn(t, p, "interrupt", "interrupt")
		observeUntil(t, p, func(Snapshot) bool { return turnOpen(p) })
		if err := p.Interrupt(context.Background(), a, "interrupt"); err != nil {
			t.Fatal(err)
		}
		refuseNamed(t, p, join(p, a), ErrClaudeInterruptPending)
		fixtureFrame(t, p, map[string]any{"type": "fixture-release-interrupt"})
		observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" })
	})
	t.Run("event-limit", func(t *testing.T) {
		p := start(t, testHost(t, func(_ *Transactions, l *Limits) { l.Events = 2 }), "normal")
		a := bound(t, p)
		turn(t, p, "running", "hold")
		observeUntil(t, p, func(Snapshot) bool { return turnOpen(p) })
		// turn-submitted and input-joined fill the running turn's two slots.
		if _, err := p.UserInput(context.Background(), a, "join-1", "joined"); err != nil {
			t.Fatal(err)
		}
		refuseNamed(t, p, join(p, a), ErrClaudeEventLimit)
		fixtureFrame(t, p, map[string]any{"type": "fixture-result"})
		observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" })
	})
	for _, err := range []error{ErrClaudeJoinLimit, ErrClaudeControlPending, ErrClaudeJoinUnsupported, ErrClaudeTurnNotOpen, ErrClaudeMessageHandoff, ErrClaudeMessageHandoffExpired, ErrClaudeInterruptPending, ErrClaudeEventLimit} {
		if !errors.Is(err, ErrBusy) || !strings.HasSuffix(err.Error(), ErrBusy.Error()) {
			t.Fatalf("%v lost busy classification", err)
		}
	}
}

// A peer message turn whose handoff outcome is late or never arrives refuses
// operator input only until Claude visibly opens the turn. After that the
// input joins the message turn without changing its ownership: the turn, its
// reservation and peer admission stay as they were, and one result closes it.
func TestClaudeMessageTurnJoinsOnceClaudeOpensIt(t *testing.T) {
	for _, tc := range []struct {
		name        string
		reservation time.Duration
		state       string
		before      error
	}{
		{"handoff-pending", 30 * time.Second, "awaiting-message-handoff", ErrClaudeMessageHandoff},
		{"handoff-expired", 20 * time.Millisecond, "expired", ErrClaudeMessageHandoffExpired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			completed := make(chan Binding, 8)
			p, err := testHost(t, func(_ *Transactions, l *Limits) { l.MessageReservation = tc.reservation }).Start(context.Background(), Launch{Binding: binding(), Command: fixtureCommand("normal"), TurnCompleted: &TurnCompletion{Notify: func(b Binding) { completed <- b }}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = p.Stop(binding())
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_, _ = p.Wait(ctx, binding())
			})
			a := bound(t, p)
			awaitCompleted(t, completed)
			if err = p.ReserveClaudeMessage(context.Background(), a, "message"); err != nil {
				t.Fatal(err)
			}
			observeUntil(t, p, func(s Snapshot) bool { return s.MessageReservation == tc.state })
			if _, err = p.UserInput(context.Background(), a, "early", "joined"); err != tc.before {
				t.Fatalf("input before Claude opened the message turn: %v", err)
			}
			fixtureFrame(t, p, map[string]any{"type": "fixture-provider-output"})
			observeUntil(t, p, func(Snapshot) bool { return turnOpen(p) })
			admission, err := p.UserInput(context.Background(), a, "join", "joined")
			if err != nil || admission != (TurnAdmission{Joined: true, Turn: "message", Origin: TurnOriginMessage}) {
				t.Fatalf("join into open message turn: %+v %v", admission, err)
			}
			if s, _ := p.Observe(binding()); s.Turn != "message" || s.MessageReservation != tc.state {
				t.Fatalf("join changed message turn ownership: %+v", s)
			}
			if err = p.ReserveClaudeMessage(context.Background(), a, "peer-2"); err != ErrClaudeTurnActive {
				t.Fatalf("peer admitted into joined message turn: %v", err)
			}
			if err = p.Turn(context.Background(), a, "strict", "ordinary"); err != ErrClaudeJoinUnsupported {
				t.Fatalf("Turn joined: %v", err)
			}
			fixtureFrame(t, p, map[string]any{"type": "fixture-result"})
			observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" && s.MessageReservation == "" })
			awaitCompleted(t, completed)
			// A late handoff outcome after the result changes nothing.
			if err = p.FinishClaudeMessage(context.Background(), a, "message", true, false); err != nil {
				t.Fatal(err)
			}
			joined := eventsOf(p, "input-joined")
			if len(joined) != 1 || joined[0].Turn != "message" || !strings.Contains(string(joined[0].Raw), `"origin":"message"`) {
				t.Fatalf("joined evidence: %+v", joined)
			}
			results := eventsOf(p, "turn-result")
			if len(results) != 2 || results[1].Turn != "message" || hasEvent(p, "provider-turn-started") {
				t.Fatalf("join changed result correlation: %+v", results)
			}
			if err = p.ReserveClaudeMessage(context.Background(), a, "peer-2"); err != nil {
				t.Fatalf("peer refused after the result: %v", err)
			}
		})
	}
}

// If the helper reports a definite zero-write after an input joined the
// visibly open message turn, the frames belonged to a turn Claude opened
// itself. The joined input stays written with unknown attribution on that
// provider turn and is never resent.
func TestClaudeMessageTurnJoinedInputAfterZeroWriteNeverRetries(t *testing.T) {
	p := start(t, testHost(t, nil), "normal")
	a := bound(t, p)
	if err := p.ReserveClaudeMessage(context.Background(), a, "message"); err != nil {
		t.Fatal(err)
	}
	fixtureFrame(t, p, map[string]any{"type": "fixture-provider-output"})
	observeUntil(t, p, func(Snapshot) bool { return turnOpen(p) })
	if _, err := p.UserInput(context.Background(), a, "join", "joined"); err != nil {
		t.Fatal(err)
	}
	if err := p.FinishClaudeMessage(context.Background(), a, "message", false, false); err != nil {
		t.Fatal(err)
	}
	fixtureFrame(t, p, map[string]any{"type": "fixture-result"})
	observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" && hasEvent(p, "provider-turn-ended") })
	started := eventsOf(p, "provider-turn-started")
	var attribution struct {
		Attribution  string
		JoinedInputs []string
	}
	if len(started) != 1 || json.Unmarshal(started[0].Raw, &attribution) != nil || attribution.Attribution != "unknown" || len(attribution.JoinedInputs) != 1 || attribution.JoinedInputs[0] != "join" {
		t.Fatalf("carried attribution: %+v %+v", started, attribution)
	}
	if len(eventsOf(p, "input-joined")) != 1 || len(eventsOf(p, "joined-input-unattributed")) != 1 {
		t.Fatal("joined input was resent or lost its evidence")
	}
}
