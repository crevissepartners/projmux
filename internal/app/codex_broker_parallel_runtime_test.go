package app

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
)

// TestCodexBrokerImageFollowsTheFileInstalledAtThePath pins what keys a new
// binding: the file at the executable path, which an atomic install replaces,
// read through the `(deleted)` suffix a superseded process reports.
func TestCodexBrokerImageFollowsTheFileInstalledAtThePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "projmux")
	if err := os.WriteFile(path, []byte("old image"), 0o700); err != nil {
		t.Fatal(err)
	}
	before := codexBrokerImageOf(path)
	if before == "" || codexBrokerImageOf(path) != before {
		t.Fatalf("image of an unchanged file = %q, want one stable token", before)
	}
	if _, err := codexbroker.NewImageDiscovery("/state", codexbroker.DefaultEndpointKey, before); err != nil {
		t.Fatalf("image token %q is not a valid discovery image: %v", before, err)
	}
	staged := filepath.Join(dir, "projmux.new")
	if err := os.WriteFile(staged, []byte("old image"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(staged, path); err != nil {
		t.Fatal(err)
	}
	after := codexBrokerImageOf(path)
	if after == "" || after == before {
		t.Fatalf("image after an atomic replace = %q, want a new token (before %q)", after, before)
	}
	if got := codexBrokerImageOf(path + procDeletedSuffix); got != after {
		t.Fatalf("image read through the deleted suffix = %q, want the installed file's %q", got, after)
	}
	if got := codexBrokerImageOf(filepath.Join(dir, "absent")); got != "" {
		t.Fatalf("image of a missing executable = %q, want the empty legacy token", got)
	}
}

// startImageBrokerRuntimeForTest publishes one runtime for one image of one
// generation endpoint, with a vintage reader the test controls.
func startImageBrokerRuntimeForTest(t *testing.T, domain string, key codexbroker.EndpointKey, image string, replaced *atomic.Bool) (codexbroker.Discovery, *codexbroker.Host) {
	t.Helper()
	discovery, err := codexbroker.NewImageDiscovery(domain, key, image)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := newBrokerTestEndpoint()
	broker, err := codexbroker.NewBroker(codexbroker.Config{Endpoint: key, Opener: func(context.Context) (codexbroker.Endpoint, error) {
		return endpoint, nil
	}, Lifecycle: func(_ context.Context, expected codexappserver.PeerIdentity) (codexappserver.LifecycleEndpoint, error) {
		return &brokerTestLifecycleEndpoint{shared: endpoint, peer: endpoint.peer}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	host, err := codexbroker.StartHost(codexbroker.HostConfig{Discovery: discovery, Broker: broker, IdleTimeout: -1,
		ImageReplaced: replaced.Load})
	if err != nil {
		_ = broker.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = host.Close()
		_ = broker.Close()
	})
	return discovery, host
}

// TestNewCodexAgentAttachesToTheInstalledImageWhileTheOldRuntimeDrains is the
// Task 4 contract at the app seam: after an install a new Agent's observer
// opens authority on the installed image's runtime with no drain refusal, a
// message route proves both the old and the new Agent's authority on the
// runtime that granted it, the same thread is never bound in both, and the old
// runtime stops with its last binding.
func TestNewCodexAgentAttachesToTheInstalledImageWhileTheOldRuntimeDrains(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("codex broker runtime requires Unix filesystem semantics")
	}
	endpointRef := coremetadata.CodexEndpointRef{StateDomainID: "domain-parallel", EndpointGenerationID: "generation-parallel"}
	key, err := codexbroker.NewEndpointKey(endpointRef.StateDomainID, endpointRef.EndpointGenerationID)
	if err != nil {
		t.Fatal(err)
	}
	domain := shortTempDomain(t)
	var oldReplaced, newReplaced atomic.Bool
	oldDiscovery, oldHost := startImageBrokerRuntimeForTest(t, domain, key, "0a0a", &oldReplaced)

	oldSession := newCodexBrokerObserverSessionOn(brokerTestIdentity("thread-old"), "", nil, oldDiscovery, nil)
	oldSession.endpoint = endpointRef
	defer oldSession.Close()
	oldAuthority, err := openBrokerEpoch(t, oldSession).GenerationAuthority()
	if err != nil {
		t.Fatal(err)
	}

	// The install lands and the old runtime meets a session that drains it.
	oldReplaced.Store(true)
	if _, err := codexbroker.Dial(t.Context(), oldDiscovery, codexbroker.DialConfig{}); codexbroker.RefusalOf(err) != codexbroker.RefusalDrainRequired {
		t.Fatalf("Dial(old image) after install = %v, want drain-required", err)
	}

	newDiscovery, newHost := startImageBrokerRuntimeForTest(t, domain, key, "0b0b", &newReplaced)
	newSession := newCodexBrokerObserverSessionOn(brokerTestIdentity("thread-new"), "", nil, newDiscovery, nil)
	newSession.endpoint = endpointRef
	defer newSession.Close()
	newAuthority, err := openBrokerEpoch(t, newSession).GenerationAuthority()
	if err != nil {
		t.Fatalf("new Agent observer while the old image drains: %v", err)
	}
	if newAuthority.BrokerRuntimeID != newHost.RuntimeID() || oldAuthority.BrokerRuntimeID != oldHost.RuntimeID() {
		t.Fatalf("authorities = old %q new %q, want runtimes %q and %q", oldAuthority.BrokerRuntimeID,
			newAuthority.BrokerRuntimeID, oldHost.RuntimeID(), newHost.RuntimeID())
	}

	// A message route reaches each Agent's own runtime.
	for name, route := range map[string]coremetadata.CodexRouteAuthority{
		"old": {ThreadID: "thread-old", Authority: oldAuthority},
		"new": {ThreadID: "thread-new", Authority: newAuthority},
	} {
		if !probeCodexMessageAuthority(domain, route) {
			t.Fatalf("message authority probe for the %s Agent failed during parallel runtimes", name)
		}
	}

	// The thread the old image still holds is not bound a second time.
	duplicate := newCodexBrokerObserverSessionOn(brokerTestIdentity("thread-old"), "", nil, newDiscovery, nil)
	duplicate.endpoint = endpointRef
	defer duplicate.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, err = duplicate.Open(ctx)
	cancel()
	if codexbroker.RefusalOf(err) != codexbroker.RefusalBindingExists {
		t.Fatalf("second bind of a thread the old image holds = %v, want binding-exists", err)
	}

	// The old Agent leaves: its runtime goes, the new one keeps serving, and the
	// thread can now bind on the installed image.
	if err := oldSession.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-oldHost.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the superseded runtime did not stop after its last binding left")
	}
	if _, err := os.Lstat(oldDiscovery.SocketPath()); !os.IsNotExist(err) {
		t.Fatalf("old runtime socket after stop: %v, want removed", err)
	}
	if !probeCodexMessageAuthority(domain, coremetadata.CodexRouteAuthority{ThreadID: "thread-new", Authority: newAuthority}) {
		t.Fatal("new Agent lost its message authority when the old runtime stopped")
	}
	if probeCodexMessageAuthority(domain, coremetadata.CodexRouteAuthority{ThreadID: "thread-old", Authority: oldAuthority}) {
		t.Fatal("a stopped runtime's authority still proved")
	}
	if _, err := openBrokerEpoch(t, duplicate).GenerationAuthority(); err != nil {
		t.Fatalf("thread rebinding on the installed image after the old binding left: %v", err)
	}
}

// TestCodexMessageFromAnotherBinaryPathReachesTheRuntimeThatGrantedIt is the
// two-binary case a live machine has: an Agent bound on the runtime binary A
// started (say the web-dev build) receives a message sent by binary B (say
// ~/go/bin/projmux), whose own image keys a different runtime. The sender
// proves the target's authority on the runtime that granted it, never on the
// one its own image would start.
func TestCodexMessageFromAnotherBinaryPathReachesTheRuntimeThatGrantedIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("codex broker runtime requires Unix filesystem semantics")
	}
	endpointRef := coremetadata.CodexEndpointRef{StateDomainID: "domain-two-binaries", EndpointGenerationID: "generation-two-binaries"}
	key, err := codexbroker.NewEndpointKey(endpointRef.StateDomainID, endpointRef.EndpointGenerationID)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	binaryA, binaryB := filepath.Join(dir, "web-dev", "projmux"), filepath.Join(dir, "go-bin", "projmux")
	for _, path := range []string{binaryA, binaryB} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("same build"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	imageA, imageB := codexBrokerImageOf(binaryA), codexBrokerImageOf(binaryB)
	if imageA == "" || imageA == imageB {
		t.Fatalf("two binary paths keyed one runtime: %q %q", imageA, imageB)
	}
	domain := shortTempDomain(t)
	var replaced atomic.Bool
	discoveryA, hostA := startImageBrokerRuntimeForTest(t, domain, key, imageA, &replaced)
	session := newCodexBrokerObserverSessionOn(brokerTestIdentity("thread-web"), "", nil, discoveryA, nil)
	session.endpoint = endpointRef
	defer session.Close()
	authority, err := openBrokerEpoch(t, session).GenerationAuthority()
	if err != nil {
		t.Fatal(err)
	}

	// The sender is binary B: every new binding it makes goes to B's runtime.
	previous := codexBrokerImage
	codexBrokerImage = func() string { return imageB }
	t.Cleanup(func() { codexBrokerImage = previous })
	senderDiscovery, err := codexBrokerDiscoveryForEndpoint(domain, key)
	if err != nil {
		t.Fatal(err)
	}
	if senderDiscovery.SocketPath() == discoveryA.SocketPath() {
		t.Fatal("binary B derived binary A's runtime socket for a new binding")
	}
	route := coremetadata.CodexRouteAuthority{ThreadID: "thread-web", Authority: authority}
	if !probeCodexMessageAuthority(domain, route) {
		t.Fatal("a message from binary B could not prove the authority binary A's runtime granted")
	}
	if authority.BrokerRuntimeID != hostA.RuntimeID() {
		t.Fatalf("authority runtime = %q, want binary A's %q", authority.BrokerRuntimeID, hostA.RuntimeID())
	}
}
