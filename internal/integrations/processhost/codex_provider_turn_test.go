package processhost

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

func TestCodexProviderGoalTurnKeepsGenerationAndAcceptsInput(t *testing.T) {
	c, _, log := codexStart(t, testHost(t, nil), "codex-normal")
	a := codexAuthority(c)
	if err := c.Turn(context.Background(), a, "initial", "goal"); err != nil {
		t.Fatal(err)
	}
	s := observeUntil(t, c.handle, func(s Snapshot) bool { return s.Turn == "goal-next" })
	if s.State != "ready" || s.Exit != nil || !hasEvent(c.handle, "provider-turn-started") {
		t.Fatalf("goal turn: %+v", s)
	}
	d, err := c.DeliverUserTurn(context.Background(), a, "next-message", "continue")
	if err != nil || d.Mode != UserTurnSteer || d.TurnID != s.Turn {
		t.Fatalf("delivery: %+v %v", d, err)
	}
	if err = c.Interrupt(context.Background(), a, s.Turn); err != nil {
		t.Fatal(err)
	}
	observeUntil(t, c.handle, func(s Snapshot) bool { return s.Turn == "" })
	if err = c.Turn(context.Background(), a, "after-goal", "controls"); err != nil {
		t.Fatal(err)
	}
	observeUntil(t, c.handle, func(s Snapshot) bool { return len(s.Pending) == 2 })
	if countMethod(codexWire(t, log), "turn/steer") != 1 {
		t.Fatal("next input did not steer goal turn")
	}
}

func TestCodexResumeAdmitsGoalTurnBeforeReply(t *testing.T) {
	old, launch, log := codexStart(t, testHost(t, nil), "codex-goal-resume")
	prior, err := old.Observe(launch.Binding)
	if err != nil {
		t.Fatal(err)
	}
	record := savedRecord(t, "codex", prior)
	stopResume(t, old.handle)
	h := testHost(t, nil)
	launch.Binding = resumedBinding()
	launch.Binding.Host = h.instance
	c, err := h.ResumeCodex(context.Background(), launch, codexSettings(), record)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopResume(t, c.handle) })
	s := waitResume(t, c.handle, func(s Snapshot) bool { return s.Turn == "goal-resumed" })
	if s.Session != prior.Session || s.State != "ready" || s.Exit != nil {
		t.Fatalf("resume: %+v", s)
	}
	d, err := c.DeliverUserTurn(context.Background(), Authority{Binding: s.Binding, Connection: s.Connection, Session: s.Session}, "after-resume", "continue")
	if err != nil || d.Mode != UserTurnSteer || d.TurnID != s.Turn {
		t.Fatalf("resume delivery: %+v %v", d, err)
	}
	if countMethod(codexWire(t, log), "thread/start") != 1 || countMethod(codexWire(t, log), "thread/resume") != 1 {
		t.Fatal("resume changed conversation")
	}
}

func TestCodexProviderTurnFences(t *testing.T) {
	for _, tc := range []struct {
		name, method, thread, turn string
		active                     bool
	}{
		{"other thread", "turn/started", "other", "new", false},
		{"missing thread", "turn/started", "", "new", false},
		{"missing turn", "turn/started", "thread", "", false},
		{"overlapping turn", "turn/started", "thread", "new", true},
		{"replayed start", "turn/started", "thread", "old", false},
		{"unopened output", "item/started", "thread", "new", false},
		{"late completion", "turn/completed", "thread", "old", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _, _ := codexStart(t, testHost(t, nil), "codex-normal")
			p := c.handle
			p.mu.Lock()
			defer p.mu.Unlock()
			adapter := p.adapter.(*codexAdapter)
			adapter.rememberProviderTurnLocked("old")
			if tc.active {
				p.turn = "active"
			}
			before := p.turn
			raw, _ := json.Marshal(map[string]any{"threadId": tc.thread, "turn": map[string]string{"id": tc.turn}})
			err := adapter.consumeLocked(codexappserver.Notification{Method: tc.method, Params: raw})
			if err == nil || (tc.thread == "thread" && !errors.Is(err, ErrStale)) || p.turn != before {
				t.Fatalf("fence: %v turn %q", err, p.turn)
			}
		})
	}
}
