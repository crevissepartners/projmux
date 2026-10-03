package processhost

import (
	"context"
	"testing"
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
