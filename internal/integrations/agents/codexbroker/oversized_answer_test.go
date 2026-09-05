package codexbroker

import (
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// TestOversizedAnswerEndsOneMutationAndNotTheConnectionEpoch is the C-3 blast
// radius contract for an answer the endpoint could not hand over.
//
// The upstream connection is shared by every bound thread, so retiring it for
// one unreadable answer suspends every binding at once and opens a new epoch
// that re-issues the same read. On this machine that made a self-sustaining
// cycle: one connection epoch per second for hours, with every managed Codex
// Agent flapping together. The answer must therefore end that one mutation,
// under its own reason, and leave the epoch and every neighbour untouched.
func TestOversizedAnswerEndsOneMutationAndNotTheConnectionEpoch(t *testing.T) {
	endpoint := newFakeEndpoint()
	broker, _, _ := newTestBroker(t, 8, endpoint)
	oversized, neighbour := boundBinding(t, broker, "thread-oversized")
	quiet, quietFence := boundBinding(t, broker, "thread-quiet")

	before := broker.Diagnostics()
	endpoint.fail("request:thread/read", codexappserver.ErrPayloadTooLarge)
	for range 5 {
		outcome, err := oversized.Submit(t.Context(), neighbour, Mutation{Method: "thread/read"})
		if outcome != MutationIndeterminate {
			t.Fatalf("oversized answer outcome = %s, want %s", outcome, MutationIndeterminate)
		}
		if got := RefusalOf(err); got != RefusalPayloadTooLarge {
			t.Fatalf("oversized answer reason = %q, want %q", got, RefusalPayloadTooLarge)
		}
	}

	after := broker.Diagnostics()
	if after.ConnectionEpoch != before.ConnectionEpoch || after.Disconnects != before.Disconnects {
		t.Fatalf("connection epoch %d->%d, disconnects %d->%d; the oversized answer retired the shared connection",
			before.ConnectionEpoch, after.ConnectionEpoch, before.Disconnects, after.Disconnects)
	}
	if after.Bindings != 2 || after.RevokedBindings != 0 {
		t.Fatalf("bindings = %d, revoked = %d; the oversized answer disturbed a neighbour",
			after.Bindings, after.RevokedBindings)
	}

	// The neighbour still holds live control authority on the same epoch, so
	// the failure never left this one thread.
	if _, err := quiet.ControlAuthority(); err != nil {
		t.Fatalf("neighbour control authority = %v, want it still open", err)
	}
	endpoint.push(threadEvent("thread-quiet", "thread/status/changed"))
	if event := nextEvent(t, quiet); event.Origin != EventOriginLive || event.Fence != quietFence {
		t.Fatalf("neighbour event = %+v, want a live event on the unchanged fence", event)
	}
}

// TestOversizedAnswerIsToldApartFromADisconnectBoundary keeps the two
// indeterminate outcomes distinct. Both leave a result unknown, but only one
// means the connection is gone, and an operator who reads them the same way
// looks for a broker fault that is not there.
func TestOversizedAnswerIsToldApartFromADisconnectBoundary(t *testing.T) {
	endpoint := newFakeEndpoint()
	broker, _, _ := newTestBroker(t, 8, endpoint, newFakeEndpoint())
	binding, fence := boundBinding(t, broker, "thread-one")

	endpoint.fail("request:thread/read", codexappserver.ErrPayloadTooLarge)
	if _, err := binding.Submit(t.Context(), fence, Mutation{Method: "thread/read"}); RefusalOf(err) != RefusalPayloadTooLarge {
		t.Fatalf("dropped answer reason = %q, want %q", RefusalOf(err), RefusalPayloadTooLarge)
	}
	endpoint.fail("request:turn/interrupt", codexappserver.ErrDisconnected)
	if _, err := binding.Submit(t.Context(), fence, Mutation{Method: "turn/interrupt"}); RefusalOf(err) != RefusalDisconnectBoundary {
		t.Fatalf("disconnected mutation reason = %q, want %q", RefusalOf(err), RefusalDisconnectBoundary)
	}
}
