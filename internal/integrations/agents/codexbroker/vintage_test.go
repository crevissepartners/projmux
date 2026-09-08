package codexbroker

import (
	"encoding/json"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// startVintageHost publishes one in-process runtime whose image vintage is
// whatever the returned reader is told to say.
func startVintageHost(t *testing.T, discovery Discovery, replaced *atomic.Bool) (*Host, *fakeEndpoint, *atomic.Int64) {
	t.Helper()
	endpoint := newFakeEndpoint()
	broker, _, _ := newTestBroker(t, 8, endpoint)
	var reads atomic.Int64
	host, err := StartHost(HostConfig{
		Discovery:   discovery,
		Broker:      broker,
		IdleTimeout: -1,
		ImageReplaced: func() bool {
			reads.Add(1)
			return replaced.Load()
		},
	})
	if err != nil {
		t.Fatalf("StartHost() = %v", err)
	}
	t.Cleanup(func() { _ = host.Close() })
	return host, endpoint, &reads
}

// TestBrokerRuntimeDrainsWhenItsOwnImageWasReplaced is the vintage entry
// condition of the drain.
//
// The drain already had one door: a client whose protocol window this runtime
// cannot negotiate. An install does not come through it. The new binary usually
// speaks the same protocol, so a compatible handshake says nothing about which
// image is behind it, and before this trigger a runtime published by the
// superseded build kept serving every client of the build that replaced it.
//
// What this fixes has to be exactly that and nothing more, so the test holds
// four things at once: a runtime on the installed image is not drained, a
// runtime whose image was unlinked is, the refusal is the one the drain already
// used rather than a new word on the wire, and live work is carried to its end
// instead of being severed.
func TestBrokerRuntimeDrainsWhenItsOwnImageWasReplaced(t *testing.T) {
	t.Parallel()

	t.Run("a runtime still on the installed image serves normally", func(t *testing.T) {
		discovery := newRuntimeDiscovery(t)
		var replaced atomic.Bool
		host, _, reads := startVintageHost(t, discovery, &replaced)

		conn := dialTestClient(t, discovery, ProtocolRange{})
		if conn.Runtime() != host.RuntimeID() {
			t.Fatalf("client reached runtime %q, want %q", conn.Runtime(), host.RuntimeID())
		}
		if host.Stats().Draining {
			t.Fatal("a runtime on the installed image entered a drain")
		}
		if reads.Load() == 0 {
			t.Fatal("the vintage reader was never consulted")
		}
	})

	t.Run("a nil reader never drains", func(t *testing.T) {
		discovery := newRuntimeDiscovery(t)
		host, _ := startTestHost(t, discovery, -1, ProtocolRange{})

		dialTestClient(t, discovery, ProtocolRange{})
		if host.Stats().Draining {
			t.Fatal("a host with no vintage reader entered a drain")
		}
	})

	t.Run("an unlinked image drains through the door the protocol trigger uses", func(t *testing.T) {
		discovery := newRuntimeDiscovery(t)
		var replaced atomic.Bool
		host, endpoint, reads := startVintageHost(t, discovery, &replaced)

		// One client is already working when the install lands.
		live := dialTestClient(t, discovery, ProtocolRange{})
		binding, fence := boundRemote(t, live, "thread-one")
		runtimeBefore := host.RuntimeID()
		socketBefore := statOf(t, discovery.SocketPath())

		// The publication happens: this runtime's image is unlinked under it.
		replaced.Store(true)

		// A client of the newly installed build arrives. Its protocol window is
		// identical, so only vintage can refuse it.
		_, err := Dial(t.Context(), discovery, DialConfig{})
		if RefusalOf(err) != RefusalDrainRequired {
			t.Fatalf("Dial() after a publication = %v, want drain-required", err)
		}
		if !host.Stats().Draining {
			t.Fatal("a superseded runtime did not enter a drain")
		}
		if host.RuntimeID() != runtimeBefore {
			t.Fatal("the runtime was replaced rather than drained")
		}
		if after := statOf(t, discovery.SocketPath()); !os.SameFile(socketBefore, after) {
			t.Fatal("the published socket was replaced rather than drained")
		}

		// Live work is carried to its end. This is the whole reason the trigger
		// enters a drain instead of a shutdown.
		endpoint.push(codexappserver.Notification{
			Method: "item/updated",
			Params: json.RawMessage(`{"threadId":"thread-one"}`),
		})
		if event := nextRemoteEvent(t, binding); event.Origin != EventOriginLive {
			t.Fatalf("the live binding stopped delivering during a vintage drain: %+v", event)
		}
		if outcome, err := binding.Submit(t.Context(), fence, Mutation{Method: "turn/steer"}); err != nil ||
			outcome != MutationApplied {
			t.Fatalf("the live binding lost control authority during a vintage drain: %s, %v", outcome, err)
		}
		if _, err := live.Bind(t.Context(), "thread-two", "", nil); RefusalOf(err) != RefusalDrainRequired {
			t.Fatalf("bind during a vintage drain = %v, want drain-required", err)
		}

		// Vintage is monotonic, so the answer is read once and remembered
		// rather than re-read per arriving session.
		before := reads.Load()
		if _, err := Dial(t.Context(), discovery, DialConfig{}); RefusalOf(err) != RefusalDrainRequired {
			t.Fatalf("second Dial() during a vintage drain = %v, want drain-required", err)
		}
		if after := reads.Load(); after != before {
			t.Fatalf("vintage reads = %d after a settled drain, want %d", after, before)
		}

		// And the drain ends the runtime once the work it had accepted is done.
		if err := binding.Close(); err != nil {
			t.Fatalf("Close() = %v", err)
		}
		select {
		case <-host.Done():
		case <-time.After(10 * time.Second):
			t.Fatal("the drained runtime did not stop after its last binding was removed")
		}
		assertNoRuntimeArtifacts(t, discovery)
	})

	t.Run("an idle superseded runtime goes as soon as it is asked", func(t *testing.T) {
		discovery := newRuntimeDiscovery(t)
		var replaced atomic.Bool
		replaced.Store(true)
		host, _, _ := startVintageHost(t, discovery, &replaced)

		// This is the shape the install pass meets: nothing is bound, so the
		// drain has no work to carry and the runtime closes immediately. The
		// install never signals it.
		if _, err := Dial(t.Context(), discovery, DialConfig{}); RefusalOf(err) != RefusalDrainRequired {
			t.Fatalf("Dial() = %v, want drain-required", err)
		}
		select {
		case <-host.Done():
		case <-time.After(10 * time.Second):
			t.Fatal("an idle superseded runtime did not stop after it drained")
		}
		assertNoRuntimeArtifacts(t, discovery)
	})
}
