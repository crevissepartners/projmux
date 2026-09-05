package codexbroker

import (
	"runtime"
	"testing"
)

// TestLastBindingRevokedByOverflowFoldsTheUpstreamConnection pins the invariant
// wanted() exists for against the involuntary revocation paths.
//
// Bind and Binding.Close both signal the supervisor, because both change what
// the connection has left to serve. The involuntary paths - a bounded delivery
// queue that overflowed, a thread-scoped snapshot refusal - removed the binding
// from routing without a wake, so a runtime could reach zero bindings with
// nothing pending on the wake channel. serve then stays in its select on a
// connection nobody is bound to, and its own comment on that case already says
// what should have happened: "a bind joined, or the last binding left".
//
// The observable consequence is an upstream app-server connection held for a
// runtime that has no work, until the host's idle timer eventually recycles the
// whole runtime. Nothing rebinding is what makes it reachable, so this test
// binds exactly one thread and never replaces it.
func TestLastBindingRevokedByOverflowFoldsTheUpstreamConnection(t *testing.T) {
	before := runtime.NumGoroutine()
	endpoint := newFakeEndpoint()
	// A one-deep queue makes the overflow exact rather than a volume guess:
	// the barrier snapshot is consumed by boundBinding, the first live event
	// fills the slot, the second has nowhere to go.
	broker, _, _ := newTestBroker(t, 1, endpoint)
	binding, _ := boundBinding(t, broker, "thread-alone")

	endpoint.push(threadEvent("thread-alone", "item/started"))
	endpoint.push(threadEvent("thread-alone", "item/completed"))

	waitUntil(t, "the unread binding to be revoked for resync", func() bool {
		return binding.Revocation() == RefusalResyncRequired
	})
	if bindings := broker.Diagnostics().Bindings; bindings != 0 {
		t.Fatalf("bindings after the only one was revoked = %d, want 0", bindings)
	}

	waitUntil(t, "the upstream connection to be folded", func() bool {
		select {
		case <-endpoint.dead:
			return true
		default:
			return false
		}
	})
	if err := broker.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	assertNoGoroutineLeak(t, before)
}
