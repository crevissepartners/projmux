package codexbroker

import (
	"strings"
	"testing"
)

// TestRuntimeTelemetryCountsOneConnectionForConcurrentBindings is the number
// the per-Agent observer retirement is measured by.
//
// The retired producer opened one upstream connection per managed Agent, so an
// operator with three Agents saw three app-server proxies. The runtime answers
// with one connection no matter how many bindings it serves, and it says so on
// a frame that binds nothing and writes nothing upstream.
func TestRuntimeTelemetryCountsOneConnectionForConcurrentBindings(t *testing.T) {
	discovery := newRuntimeDiscovery(t)
	host, endpoint := startTestHost(t, discovery, -1, ProtocolRange{})
	conn := dialTestClient(t, discovery, ProtocolRange{})

	before := len(endpoint.methods())
	telemetry, err := conn.Stats(t.Context())
	if err != nil {
		t.Fatalf("Stats() = %v", err)
	}
	if telemetry.Runtime != host.RuntimeID() || telemetry.Protocol != conn.Protocol() {
		t.Fatalf("telemetry identity = %+v, want runtime %q protocol %d", telemetry, host.RuntimeID(), conn.Protocol())
	}
	if telemetry.Broker.Bindings != 0 || telemetry.Host.Bindings != 0 {
		t.Fatalf("an unbound runtime reported bindings: %+v", telemetry)
	}

	for _, thread := range []string{"thread-a", "thread-b", "thread-c"} {
		boundRemote(t, conn, thread)
	}
	bound, err := conn.Stats(t.Context())
	if err != nil {
		t.Fatalf("Stats() after binding = %v", err)
	}
	if bound.Broker.Bindings != 3 || bound.Host.Bindings != 3 {
		t.Fatalf("bindings = broker %d host %d, want 3/3", bound.Broker.Bindings, bound.Host.Bindings)
	}
	if open := bound.Broker.Connects - bound.Broker.Disconnects; open != 1 {
		t.Fatalf("open upstream connections = %d across 3 bindings, want exactly 1", open)
	}
	if bound.Host.LiveSessions != 1 {
		t.Fatalf("live client sessions = %d, want the single client that bound all three", bound.Host.LiveSessions)
	}
	// Reading telemetry twice adds no upstream traffic of its own: the only
	// endpoint calls between the two readings are the three bootstraps the
	// bindings needed.
	if after := len(endpoint.methods()); after != before {
		t.Fatalf("endpoint requests = %d, want the pre-binding count %d: telemetry must not reach upstream",
			after, before)
	}
	if bootstrapped := len(endpoint.bootstrapped()); bootstrapped != 3 {
		t.Fatalf("endpoint bootstraps = %d, want exactly one per binding", bootstrapped)
	}
}

// TestRuntimeTelemetryNamesEachBindingScopedFault keeps the three revocation
// classes separable.
//
// A bare revoked-bindings count cannot tell an operator whether a binding was
// evicted for overflowing its bounded queue, refused its reconnect snapshot, or
// fenced out by a replacement epoch. Those have different answers, so the
// breakdown names each one.
func TestRuntimeTelemetryNamesEachBindingScopedFault(t *testing.T) {
	endpoint := newFakeEndpoint()
	broker, _, _ := newTestBroker(t, 8, endpoint)
	for reason, count := range map[Refusal]int{
		RefusalResyncRequired:      2,
		RefusalSnapshotUnavailable: 1,
		RefusalStaleBindingEpoch:   3,
		RefusalBindingClosed:       4,
	} {
		for range count {
			broker.mu.Lock()
			if reason == RefusalBindingClosed {
				broker.diag.ReleasedBindings++
			} else {
				broker.diag.RevokedBindings++
				broker.countRevocationLocked(reason)
			}
			broker.mu.Unlock()
		}
	}
	diagnostics := broker.Diagnostics()
	if diagnostics.RevokedBindings != 6 || diagnostics.ReleasedBindings != 4 {
		t.Fatalf("revoked=%d released=%d, want 6/4", diagnostics.RevokedBindings, diagnostics.ReleasedBindings)
	}
	want := []RevocationCount{
		{Reason: RefusalResyncRequired, Count: 2},
		{Reason: RefusalSnapshotUnavailable, Count: 1},
		{Reason: RefusalStaleBindingEpoch, Count: 3},
	}
	// The projection is sorted, so two readings of one broker render
	// identically instead of in map order.
	sorted := append([]RevocationCount(nil), want...)
	for i := range sorted {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].Reason < sorted[i].Reason {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	if len(diagnostics.Revocations) != len(sorted) {
		t.Fatalf("revocation breakdown = %+v, want %+v", diagnostics.Revocations, sorted)
	}
	for i, got := range diagnostics.Revocations {
		if got != sorted[i] {
			t.Fatalf("revocation breakdown = %+v, want %+v", diagnostics.Revocations, sorted)
		}
	}
	// A voluntary release is not a fault and never enters the breakdown.
	for _, revocation := range diagnostics.Revocations {
		if revocation.Reason == RefusalBindingClosed {
			t.Fatal("a voluntary unbind was counted as a binding fault")
		}
		if strings.ContainsAny(string(revocation.Reason), " \t/\\") {
			t.Fatalf("revocation reason %q is not a bare token", revocation.Reason)
		}
	}
	// The snapshot is a value copy: mutating it cannot reach live broker state.
	diagnostics.Revocations[0].Count = 99
	if again := broker.Diagnostics(); again.Revocations[0].Count == 99 {
		t.Fatal("the telemetry projection aliases live broker state")
	}
}
