package processhost

import (
	"errors"
	"testing"
)

func changeClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// Owned Wait sleeps on Changed instead of polling: every event and each
// transition that changes the Snapshot without an event wakes it, and each
// channel reports one change.
func TestHandleChangedReportsSnapshotChanges(t *testing.T) {
	b := Binding{Host: "h", Agent: "a", Pane: "p", Generation: "g", Operation: "o"}
	p := &Handle{host: &Host{limits: Limits{Events: 8}}, launch: Launch{Binding: b}, requests: map[string]Request{}}
	if _, err := p.Changed(Binding{Pane: "other"}); !errors.Is(err, ErrStale) {
		t.Fatalf("stale binding: %v", err)
	}
	changed, _ := p.Changed(b)
	if again, _ := p.Changed(b); again != changed {
		t.Fatal("waiters before one change do not share its channel")
	}
	if changeClosed(changed) {
		t.Fatal("a channel closed before any change")
	}
	for _, change := range []struct {
		name  string
		apply func()
	}{
		{"output", func() { p.emitLocked("output", []byte(`{}`), nil) }},
		{"turn-submitted", func() { p.emitLocked("turn-submitted", nil, nil) }},
		{"end turn", p.endTurnLocked},
		{"clear message reservation", p.clearMessageReservationLocked},
	} {
		changed, _ = p.Changed(b)
		p.mu.Lock()
		change.apply()
		p.mu.Unlock()
		if !changeClosed(changed) {
			t.Fatalf("%s did not wake a waiter", change.name)
		}
		if next, _ := p.Changed(b); changeClosed(next) {
			t.Fatalf("a new channel reported the earlier %s", change.name)
		}
	}
	changed, _ = p.Changed(b)
	p.stop()
	if !changeClosed(changed) {
		t.Fatal("stop did not wake a waiter")
	}
}
