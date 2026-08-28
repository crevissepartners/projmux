package codexbroker

import (
	"errors"
	"testing"
)

// TestReconnectSnapshotAndLiveEventsMergeIntoOneOrderBehindTheBarrier is the
// C-2 barrier guarantee. Upstream has no pre-turn subscription that can be
// confirmed, so the only gap-free order is: buffer live events from the moment
// the connection opens, take the snapshot, then fold the buffer in behind it.
// Whatever the race between the snapshot and the live leg, the binding sees
// exactly one order, and control authority stays shut until the merge is done.
func TestReconnectSnapshotAndLiveEventsMergeIntoOneOrderBehindTheBarrier(t *testing.T) {
	// The three rows are the whole race matrix: live events entirely before
	// the snapshot lands, straddling it, and entirely after it.
	t.Run("live events arrive before the snapshot", func(t *testing.T) {
		endpoint := newFakeEndpoint()
		endpoint.hold("snapshot:thread-one")
		broker, _, _ := newTestBroker(t, 8, endpoint)
		binding, err := broker.Bind("thread-one", "/work/project", nil)
		if err != nil {
			t.Fatal(err)
		}
		waitUntil(t, "the barrier to request its snapshot", func() bool {
			return endpoint.visited("snapshot:thread-one") == 1
		})

		// Control authority is shut for as long as the barrier is open, and a
		// mutation attempted now reaches the wire with zero bytes.
		if _, err := binding.ControlAuthority(); RefusalOf(err) != RefusalControlNotOpen {
			t.Fatalf("control authority during the barrier = %v", err)
		}
		outcome, err := binding.Submit(t.Context(), Fence{Connection: 1, Binding: 1}, Mutation{Method: "turn/start"})
		if outcome != MutationRefused || RefusalOf(err) != RefusalControlNotOpen {
			t.Fatalf("mutation during the barrier = %s/%v", outcome, err)
		}
		if got := endpoint.methods(); len(got) != 0 {
			t.Fatalf("requests during the barrier = %v, want none", got)
		}
		if got := broker.WriteLedger(); len(got) != 0 {
			t.Fatalf("ledger during the barrier = %v, want no write", got)
		}

		endpoint.push(threadEvent("thread-one", "item/started"))
		endpoint.push(threadEvent("thread-one", "item/completed"))
		waitUntil(t, "both live events to be buffered", func() bool {
			return broker.Diagnostics().BufferedEvents == 2
		})
		endpoint.release("snapshot:thread-one")
		assertMergedOrder(t, binding, "item/started", "item/completed")

		fence, err := binding.ControlAuthority()
		if err != nil || fence != (Fence{Connection: 1, Binding: 1}) {
			t.Fatalf("control authority after the barrier = %+v/%v", fence, err)
		}
	})

	t.Run("live events straddle the snapshot", func(t *testing.T) {
		endpoint := newFakeEndpoint()
		endpoint.hold("snapshot:thread-one")
		broker, _, _ := newTestBroker(t, 8, endpoint)
		binding, err := broker.Bind("thread-one", "/work/project", nil)
		if err != nil {
			t.Fatal(err)
		}
		waitUntil(t, "the barrier to request its snapshot", func() bool {
			return endpoint.visited("snapshot:thread-one") == 1
		})
		endpoint.push(threadEvent("thread-one", "item/started"))
		waitUntil(t, "the first live event to be buffered", func() bool {
			return broker.Diagnostics().BufferedEvents == 1
		})
		endpoint.release("snapshot:thread-one")
		endpoint.push(threadEvent("thread-one", "item/completed"))
		assertMergedOrder(t, binding, "item/started", "item/completed")
	})

	t.Run("live events arrive after the barrier closed", func(t *testing.T) {
		endpoint := newFakeEndpoint()
		broker, _, _ := newTestBroker(t, 8, endpoint)
		binding, _ := boundBinding(t, broker, "thread-one")
		endpoint.push(threadEvent("thread-one", "item/started"))
		endpoint.push(threadEvent("thread-one", "item/completed"))
		for index, want := range []string{"item/started", "item/completed"} {
			event := nextEvent(t, binding)
			if event.Origin != EventOriginLive || event.Method != want || event.Sequence != uint64(index+2) {
				t.Fatalf("event %d = %+v, want live %s at sequence %d", index, event, want, index+2)
			}
		}
		if got := broker.Diagnostics().BufferedEvents; got != 0 {
			t.Fatalf("buffered events after the barrier = %d, want none", got)
		}
	})
}

// TestBarrierSnapshotFailureRevokesOnlyTheRefusedThread keeps the barrier's
// fault surface binding-scoped. A live endpoint that refuses one thread's
// snapshot is a thread-scoped fault, so only that binding is revoked; a
// disconnect during the snapshot is connection-scoped and the binding simply
// waits for the next epoch to re-run its barrier.
func TestBarrierSnapshotFailureRevokesOnlyTheRefusedThread(t *testing.T) {
	t.Run("a live endpoint refusing one thread", func(t *testing.T) {
		endpoint := newFakeEndpoint()
		endpoint.fail("snapshot:thread-gone", errors.New("no such thread"))
		broker, opener, _ := newTestBroker(t, 8, endpoint)
		survivor, _ := boundBinding(t, broker, "thread-live")
		doomed, err := broker.Bind("thread-gone", "/work/project", nil)
		if err != nil {
			t.Fatal(err)
		}
		waitUntil(t, "the refused thread to be revoked", func() bool {
			return doomed.Revocation() != RefusalNone
		})
		if got := doomed.Revocation(); got != RefusalSnapshotUnavailable {
			t.Fatalf("revocation = %s, want %s", got, RefusalSnapshotUnavailable)
		}
		endpoint.push(threadEvent("thread-live", "item/completed"))
		if event := nextEvent(t, survivor); event.Method != "item/completed" {
			t.Fatalf("survivor event = %+v", event)
		}
		if got := broker.Diagnostics().Disconnects; got != 0 {
			t.Fatalf("disconnects = %d, want the shared connection untouched", got)
		}
		if opener.count() != 1 {
			t.Fatalf("open attempts = %d, want no reconnect", opener.count())
		}
	})

	t.Run("a disconnect during the snapshot", func(t *testing.T) {
		first, second := newFakeEndpoint(), newFakeEndpoint()
		first.hold("snapshot:thread-one")
		broker, _, _ := newTestBroker(t, 8, first, second)
		binding, err := broker.Bind("thread-one", "/work/project", nil)
		if err != nil {
			t.Fatal(err)
		}
		waitUntil(t, "the first barrier to request its snapshot", func() bool {
			return first.visited("snapshot:thread-one") == 1
		})
		_ = first.Close()
		event := awaitSnapshot(t, binding)
		if event.Fence != (Fence{Connection: 2, Binding: 1}) {
			t.Fatalf("snapshot fence = %+v, want the replacement epoch", event.Fence)
		}
		if binding.Revocation() != RefusalNone {
			t.Fatalf("binding revoked by a connection-scoped fault: %s", binding.Revocation())
		}
		if got := first.bootstrapped(); len(got) != 0 {
			t.Fatalf("first connection produced a snapshot after closing: %v", got)
		}
	})
}

// assertMergedOrder requires the binding's stream to be exactly one snapshot
// followed by the named live events, numbered as one continuous order.
func assertMergedOrder(t *testing.T, binding *Binding, methods ...string) {
	t.Helper()
	snapshot := awaitSnapshot(t, binding)
	if snapshot.Sequence != 1 {
		t.Fatalf("snapshot sequence = %d, want the order to start at 1", snapshot.Sequence)
	}
	for index, want := range methods {
		event := nextEvent(t, binding)
		if event.Origin != EventOriginLive || event.Method != want {
			t.Fatalf("merged event %d = %+v, want live %s", index, event, want)
		}
		if event.Sequence != uint64(index+2) {
			t.Fatalf("merged event %d sequence = %d, want %d", index, event.Sequence, index+2)
		}
		if event.Fence != snapshot.Fence {
			t.Fatalf("merged event %d fence = %+v, want %+v", index, event.Fence, snapshot.Fence)
		}
	}
	assertQuiet(t, binding)
}
