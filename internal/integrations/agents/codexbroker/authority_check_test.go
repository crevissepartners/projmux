package codexbroker

import (
	"testing"
)

func TestAuthorityCheckUsesExistingExactLeaseWithoutProviderTraffic(t *testing.T) {
	discovery := newRuntimeDiscovery(t)
	host, endpoint := startTestHost(t, discovery, -1, CurrentProtocol())
	binding, fence := boundBinding(t, host.broker, "source-thread")
	client := dialTestClient(t, discovery, CurrentProtocol())
	before := host.broker.Diagnostics()
	for _, test := range []struct {
		name, runtime, thread string
		fence                 Fence
		want                  Refusal
	}{
		{name: "exact", runtime: client.Runtime(), thread: "source-thread", fence: fence, want: RefusalNone},
		{name: "old runtime", runtime: "old-runtime", thread: "source-thread", fence: fence, want: RefusalRuntimeReplaced},
		{name: "foreign thread", runtime: client.Runtime(), thread: "foreign", fence: fence, want: RefusalBindingClosed},
		{name: "old connection", runtime: client.Runtime(), thread: "source-thread", fence: Fence{Connection: fence.Connection + 1, Binding: fence.Binding}, want: RefusalStaleConnectionEpoch},
		{name: "old binding", runtime: client.Runtime(), thread: "source-thread", fence: Fence{Connection: fence.Connection, Binding: fence.Binding + 1}, want: RefusalStaleBindingEpoch},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := client.CheckAuthority(t.Context(), test.runtime, test.thread, test.fence); RefusalOf(err) != test.want {
				t.Fatalf("authority check refusal=%s want=%s", RefusalOf(err), test.want)
			}
		})
	}
	after := host.broker.Diagnostics()
	if before.Bindings != after.Bindings || before.ConnectionEpoch != after.ConnectionEpoch || before.OpenAttempts != after.OpenAttempts {
		t.Fatalf("observation changed binding/connection: before=%+v after=%+v", before, after)
	}
	endpoint.mu.Lock()
	requests, answers, snapshots := len(endpoint.requests), len(endpoint.answers), len(endpoint.snapshots)
	endpoint.mu.Unlock()
	if requests != 0 || answers != 0 || snapshots != 1 {
		t.Fatalf("authority check performed provider work: requests=%d answers=%d snapshots=%d", requests, answers, snapshots)
	}
	if err := binding.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.CheckAuthority(t.Context(), client.Runtime(), "source-thread", fence); RefusalOf(err) != RefusalBindingClosed {
		t.Fatalf("released source lease remained current: %v", err)
	}
}
