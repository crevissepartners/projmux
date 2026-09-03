package codexbroker

import "testing"

// TestSessionReplacementSnapshotForwardsPendingSuspensionFirst pins the wire
// ordering at a reconnect. The in-process binding exposes the disconnect edge
// separately from its ordered events, but a remote consumer must never observe
// replacement authority before the edge that retired the previous epoch.
func TestSessionReplacementSnapshotForwardsPendingSuspensionFirst(t *testing.T) {
	s := &session{
		host:   &Host{done: make(chan struct{})},
		out:    make(chan wireReply, 2),
		closed: make(chan struct{}),
	}
	suspends := make(chan struct{}, 1)
	suspends <- struct{}{}
	event := Event{Origin: EventOriginSnapshot, Fence: Fence{Connection: 2, Binding: 1}}

	s.forwardEvent("thread-replacement", event, suspends)

	first := <-s.out
	second := <-s.out
	if first.Kind != replySuspended || first.Thread != "thread-replacement" {
		t.Fatalf("first replacement frame = %+v, want the prior suspension", first)
	}
	if second.Kind != replyEvent || second.Thread != "thread-replacement" || second.Event == nil ||
		second.Event.Origin != EventOriginSnapshot || second.Event.Fence != event.Fence {
		t.Fatalf("second replacement frame = %+v, want the exact snapshot", second)
	}
	select {
	case <-suspends:
		t.Fatal("replacement forwarding left a stale suspension edge")
	default:
	}
}
