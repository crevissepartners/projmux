package processhost

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func modelBoundary(t *testing.T, p *Handle, session, message string) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"type": "stream_event", "session_id": session, "event": map[string]any{"type": "message_start", "message": map[string]any{"id": message}}})
	if err := p.consume(raw); err != nil {
		t.Fatal(err)
	}
}

func joinBudget(t *testing.T, p *Handle, count, size int) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.joined) != count || p.joinedBytes != size || len(p.joinedBudget) != count {
		t.Fatalf("budget: %v %d %+v", p.joined, p.joinedBytes, p.joinedBudget)
	}
}

func TestClaudeJoinConsumptionRequiresConfirmedWriteAndFreshBoundary(t *testing.T) {
	p := start(t, testHost(t, nil), "normal")
	a := bound(t, p)
	turn(t, p, "running", "hold")
	observeUntil(t, p, func(Snapshot) bool { return turnOpen(p) })
	for _, op := range []string{"pending", "unknown", "written"} {
		if _, err := p.ReserveClaudePeerMessage(context.Background(), a, op, 10); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.FinishClaudeMessage(context.Background(), a, "unknown", false, true); err != nil {
		t.Fatal(err)
	}
	if err := p.FinishClaudeMessage(context.Background(), a, "written", true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UserInput(context.Background(), a, "operator", "joined"); err != nil {
		t.Fatal(err)
	}
	// Output, tool events, missing identities, and duplicate response starts must
	// never retire a write that happened after that response started.
	for _, raw := range []string{
		`{"type":"assistant","session_id":"session"}`,
		`{"type":"stream_event","session_id":"session","event":{"type":"content_block_start"}}`,
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"missing-session"}}}`,
		`{"type":"stream_event","session_id":"session","parent_tool_use_id":"child-tool","event":{"type":"message_start","message":{"id":"child-model"}}}`,
	} {
		if err := p.consume([]byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	joinBudget(t, p, 4, 36)
	modelBoundary(t, p, a.Session, "")
	joinBudget(t, p, 4, 36)
	modelBoundary(t, p, a.Session, "model-1")
	joinBudget(t, p, 2, 20)
	if err := p.FinishClaudeMessage(context.Background(), a, "pending", true, false); err != nil {
		t.Fatal(err)
	}
	modelBoundary(t, p, a.Session, "model-1")
	joinBudget(t, p, 2, 20)
	modelBoundary(t, p, a.Session, "model-2")
	joinBudget(t, p, 1, 10)
	if err := p.FinishClaudeMessage(context.Background(), a, "unknown", true, false); err != nil {
		t.Fatal(err)
	}
	modelBoundary(t, p, a.Session, "model-3")
	joinBudget(t, p, 1, 10) // uncertain remains charged; no retry upgrades it
	events := eventsOf(p, "joined-inputs-consumed")
	if len(events) != 2 || events[0].Turn != "running" || !strings.Contains(string(events[0].Raw), `"bytes":16`) {
		t.Fatal(events)
	}
}

func TestClaudeJoinConsumptionFreesCountAndBytesAndWakesWithoutResult(t *testing.T) {
	p := start(t, testHost(t, nil), "normal")
	a := bound(t, p)
	turn(t, p, "running", "hold")
	observeUntil(t, p, func(Snapshot) bool { return turnOpen(p) })
	woke := make(chan Binding, 32)
	p.mu.Lock()
	p.launch.JoinConsumed = &JoinConsumption{Notify: func(b Binding) {
		// Observe takes the same mutex: the callback must run outside it.
		_, err := p.Observe(b)
		if err == nil {
			woke <- b
		}
	}}
	p.mu.Unlock()
	for batch := range 3 {
		for i := range ClaudeJoinedInputs {
			op := fmt.Sprintf("peer-%d-%d", batch, i)
			if _, err := p.ReserveClaudePeerMessage(context.Background(), a, op, ClaudeJoinedInputBytes/ClaudeJoinedInputs); err != nil {
				t.Fatal(err)
			}
			if err := p.FinishClaudeMessage(context.Background(), a, op, true, false); err != nil {
				t.Fatal(err)
			}
		}
		joinBudget(t, p, 8, ClaudeJoinedInputBytes)
		if _, err := p.ReserveClaudePeerMessage(context.Background(), a, "over-peer", 1); err != ErrClaudeTurnActive {
			t.Fatal(err)
		}
		if _, err := p.UserInput(context.Background(), a, "over-operator", "joined"); err != ErrClaudeJoinLimit {
			t.Fatal(err)
		}
		modelBoundary(t, p, a.Session, fmt.Sprint("model-", batch))
		joinBudget(t, p, 0, 0)
		select {
		case b := <-woke:
			if b != binding() {
				t.Fatal(b)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("consumption wake missing")
		}
	}
	s, _ := p.Observe(binding())
	if s.Turn != "running" || s.Session != a.Session || len(eventsOf(p, "turn-result")) != 1 {
		t.Fatal("consumption completed/replaced the turn", s)
	}
	// A join after the last boundary still carries unknown attribution when the
	// turn ends; consumption does not change the old late-result path.
	if _, err := p.UserInput(context.Background(), a, "late", "joined"); err != nil {
		t.Fatal(err)
	}
	fixtureFrame(t, p, map[string]any{"type": "fixture-result"})
	observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" })
	fixtureFrame(t, p, map[string]any{"type": "fixture-provider-turn"})
	observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" && len(eventsOf(p, "turn-result")) == 3 })
	unattributed := eventsOf(p, "joined-input-unattributed")
	if len(unattributed) != 1 || !strings.Contains(string(unattributed[0].Raw), `"late"`) || strings.Contains(string(unattributed[0].Raw), "peer-") {
		t.Fatal(unattributed)
	}
}

func TestClaudeJoinConsumptionNeverUsesOldTurnOrSession(t *testing.T) {
	p := start(t, testHost(t, nil), "normal")
	a := bound(t, p)
	turn(t, p, "running", "hold")
	observeUntil(t, p, func(Snapshot) bool { return turnOpen(p) })
	if _, err := p.ReserveClaudePeerMessage(context.Background(), a, "old-peer", 19); err != nil {
		t.Fatal(err)
	}
	fixtureFrame(t, p, map[string]any{"type": "fixture-result"})
	observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" })
	turn(t, p, "next", "hold")
	observeUntil(t, p, func(Snapshot) bool { return turnOpen(p) })
	if _, err := p.UserInput(context.Background(), a, "new-operator", "joined"); err != nil {
		t.Fatal(err)
	}
	if err := p.FinishClaudeMessage(context.Background(), a, "old-peer", true, false); err != nil {
		t.Fatal(err)
	}
	joinBudget(t, p, 1, 6)
	raw := []byte(`{"type":"stream_event","session_id":"different","event":{"type":"message_start","message":{"id":"model"}}}`)
	if err := p.consume(raw); err == nil {
		t.Fatal("stale session accepted")
	}
	joinBudget(t, p, 1, 6)
	modelBoundary(t, p, a.Session, "model")
	joinBudget(t, p, 0, 0)
}
