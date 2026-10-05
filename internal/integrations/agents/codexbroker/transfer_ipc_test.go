package codexbroker

import (
	"context"
	"testing"
	"time"
)

func TestTransferIPCOrphanPinsIdleHostAndExactRecovery(t *testing.T) {
	discovery := newRuntimeDiscovery(t)
	e := &transferEndpoint{fakeEndpoint: newFakeEndpoint(), loaded: true}
	b, err := NewBroker(Config{Opener: func(context.Context) (Endpoint, error) { return e, nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	host, err := StartHost(HostConfig{Discovery: discovery, Broker: b, IdleTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })
	sourceClient := dialTestClient(t, discovery, ProtocolRange{})
	old, fence := boundRemote(t, sourceClient, "thread-one")
	conn, err := DialTransfer(t.Context(), discovery, DialConfig{})
	if err != nil {
		t.Fatal(err)
	}
	source := sourceFor(fence)
	source.RuntimeID = conn.runtime
	source.Endpoint = discovery.Endpoint()
	pending, err := NewTransferReceipt(source)
	if err != nil {
		t.Fatal(err)
	}
	transfer, err := conn.PrepareTransfer(t.Context(), pending)
	if err != nil {
		t.Fatal(err)
	}
	receipt := transfer.Receipt()
	if _, err = conn.Bind(t.Context(), "foreign", "", nil); RefusalOf(err) != RefusalRequestUnknown {
		t.Fatal(err)
	}
	before := len(e.methods())
	if _, err = old.Submit(t.Context(), fence, Mutation{Method: "forbidden"}); RefusalOf(err) != RefusalControlNotOpen {
		t.Fatal(err)
	}
	if len(e.methods()) != before {
		t.Fatal("frozen control reached provider")
	}
	_ = old.Close()
	_ = sourceClient.Close()
	host.idleFired()
	select {
	case <-host.Done():
		t.Fatal("reservation did not pin idle Host")
	default:
	}
	if err = transfer.Retire(t.Context()); err != nil {
		t.Fatal(err)
	}
	receipt = transfer.Receipt()
	_ = transfer.Close()
	waitUntil(t, "orphaned transfer", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.transfers[source.Thread] != nil && b.transfers[source.Thread].orphaned
	})
	// A draining image accepts only recovery of existing request-owned work.
	host.mu.Lock()
	host.draining = true
	host.mu.Unlock()
	recoveryConn, err := DialTransfer(context.Background(), discovery, DialConfig{})
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := recoveryConn.ReclaimTransfer(t.Context(), receipt)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	if err = recovered.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = recoveryConn.PrepareTransfer(t.Context(), pending); RefusalOf(err) != RefusalDrainRequired {
		t.Fatal(err)
	}
	if err = recovered.Finish(t.Context()); err != nil {
		t.Fatal(err)
	}
	if b.hasTransfers() {
		t.Fatal("handoff left reservation")
	}
}
