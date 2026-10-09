package codexbroker

import (
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func newImageDiscovery(t *testing.T, domain, image string) Discovery {
	t.Helper()
	discovery, err := NewImageDiscovery(domain, DefaultEndpointKey, image)
	if err != nil {
		t.Fatalf("NewImageDiscovery(%q) = %v", image, err)
	}
	return discovery
}

// TestImageDiscoveryKeepsTheLegacyContractAndSeparatesImages pins the key
// derivation: an empty image is exactly the contract every older build uses,
// and two images of one endpoint never share an artifact.
func TestImageDiscoveryKeepsTheLegacyContractAndSeparatesImages(t *testing.T) {
	t.Parallel()
	domain := newRuntimeDomain(t)
	legacy, err := NewDiscovery(domain, DefaultEndpointKey)
	if err != nil {
		t.Fatalf("NewDiscovery() = %v", err)
	}
	if empty := newImageDiscovery(t, domain, ""); empty != legacy {
		t.Fatalf("empty image discovery = %+v, want the legacy contract %+v", empty, legacy)
	}
	old, current := newImageDiscovery(t, domain, "0a0a"), newImageDiscovery(t, domain, "0b0b")
	paths := map[string]bool{}
	for _, discovery := range []Discovery{legacy, old, current} {
		for _, path := range []string{discovery.SocketPath(), discovery.RecordPath(), discovery.lockPath()} {
			if paths[path] {
				t.Fatalf("artifact %s is shared between images", path)
			}
			paths[path] = true
		}
	}
	for _, image := range []string{"UPPER", "../x", "a b", string(make([]byte, maxImageBytes+1))} {
		if _, err := NewImageDiscovery(domain, DefaultEndpointKey, image); err == nil {
			t.Fatalf("NewImageDiscovery(%q) accepted an invalid image", image)
		}
	}
}

// TestNewImageRuntimeServesWhileTheSupersededImageDrains is the parallel
// runtime contract: after an install the superseded image's runtime keeps
// carrying the binding it holds and drains, the installed image's runtime takes
// new bindings at once, an existing authority is found by runtime identity,
// one thread is never bound in both, and the old runtime goes with its last
// binding while the new one keeps serving.
func TestNewImageRuntimeServesWhileTheSupersededImageDrains(t *testing.T) {
	t.Parallel()
	domain := newRuntimeDomain(t)
	oldDiscovery := newImageDiscovery(t, domain, "0a0a")
	newDiscovery := newImageDiscovery(t, domain, "0b0b")

	var replaced atomic.Bool
	oldHost, _, _ := startVintageHost(t, oldDiscovery, &replaced)
	oldConn := dialTestClient(t, oldDiscovery, ProtocolRange{})
	oldBinding, oldFence := boundRemote(t, oldConn, "thread-old")

	// The install lands. A client of the superseded image's own key still meets
	// the drain, exactly as before images existed.
	replaced.Store(true)
	if _, err := Dial(t.Context(), oldDiscovery, DialConfig{}); RefusalOf(err) != RefusalDrainRequired {
		t.Fatalf("Dial(old image) after install = %v, want drain-required", err)
	}

	// A client of the installed image reaches its own runtime and binds at once.
	newHost, _ := startTestHost(t, newDiscovery, -1, ProtocolRange{})
	newConn, err := Ensure(t.Context(), newDiscovery, EnsureConfig{})
	if err != nil {
		t.Fatalf("Ensure(new image) while the old image drains = %v", err)
	}
	t.Cleanup(func() { _ = newConn.Close() })
	if newConn.Runtime() != newHost.RuntimeID() || newConn.Runtime() == oldHost.RuntimeID() {
		t.Fatalf("new image client reached runtime %q, want %q", newConn.Runtime(), newHost.RuntimeID())
	}
	if err := UnboundElsewhere(t.Context(), newDiscovery, "thread-new", DialConfig{}); err != nil {
		t.Fatalf("UnboundElsewhere(fresh thread) = %v, want nil", err)
	}
	newBinding, newFence := boundRemote(t, newConn, "thread-new")

	// The old binding keeps control through its own connection.
	if outcome, err := oldBinding.Submit(t.Context(), oldFence, Mutation{Method: "turn/steer"}); err != nil || outcome != MutationApplied {
		t.Fatalf("old binding Submit during parallel runtimes = %s, %v", outcome, err)
	}
	if outcome, err := newBinding.Submit(t.Context(), newFence, Mutation{Method: "turn/start"}); err != nil || outcome != MutationApplied {
		t.Fatalf("new binding Submit = %s, %v", outcome, err)
	}

	// Authority is found by runtime identity, whichever image granted it.
	published, err := Published(domain)
	if err != nil || len(published) != 2 {
		t.Fatalf("Published() = %v, %v, want both runtimes", published, err)
	}
	for _, want := range []struct {
		runtime   string
		discovery Discovery
		thread    string
		fence     Fence
	}{
		{oldHost.RuntimeID(), oldDiscovery, "thread-old", oldFence},
		{newHost.RuntimeID(), newDiscovery, "thread-new", newFence},
	} {
		located, err := LocateRuntime(domain, DefaultEndpointKey, want.runtime)
		if err != nil || located != want.discovery {
			t.Fatalf("LocateRuntime(%s) = %+v, %v, want %+v", want.runtime, located, err, want.discovery)
		}
		if err := ProbeAuthority(t.Context(), located, DialConfig{}, want.runtime, want.thread, want.fence); err != nil {
			t.Fatalf("ProbeAuthority(%s) through the located runtime = %v", want.thread, err)
		}
	}
	if _, err := LocateRuntime(domain, DefaultEndpointKey, "absent-runtime"); RefusalOf(err) != RefusalHostUnavailable {
		t.Fatalf("LocateRuntime(absent) = %v, want host-unavailable", err)
	}

	// One thread is never bound in both runtimes.
	if err := UnboundElsewhere(t.Context(), newDiscovery, "thread-old", DialConfig{}); RefusalOf(err) != RefusalBindingExists {
		t.Fatalf("UnboundElsewhere(thread the old image holds) = %v, want binding-exists", err)
	}

	// The old runtime goes with its last binding; the new one keeps serving.
	if err := oldBinding.Close(); err != nil {
		t.Fatalf("old binding Close() = %v", err)
	}
	select {
	case <-oldHost.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the superseded runtime did not stop after its last binding left")
	}
	assertNoRuntimeArtifacts(t, oldDiscovery)
	if _, err := os.Lstat(newDiscovery.SocketPath()); err != nil {
		t.Fatalf("the installed image's runtime lost its socket when the old one stopped: %v", err)
	}
	if outcome, err := newBinding.Submit(t.Context(), newFence, Mutation{Method: "turn/steer"}); err != nil || outcome != MutationApplied {
		t.Fatalf("new binding Submit after the old runtime stopped = %s, %v", outcome, err)
	}
	if err := UnboundElsewhere(t.Context(), newDiscovery, "thread-old", DialConfig{}); err != nil {
		t.Fatalf("UnboundElsewhere after the old runtime stopped = %v, want nil", err)
	}
}
