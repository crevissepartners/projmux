package codexbroker

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"testing"
	"time"
)

type transferEndpoint struct {
	*fakeEndpoint
	loaded bool
}

func (e *transferEndpoint) PeerIdentity() codexappserver.PeerIdentity {
	return codexappserver.PeerIdentity{PID: 42, OwnerUID: 1000, Start: "test:transfer"}
}
func (e *transferEndpoint) Request(ctx context.Context, method string, params, result any) error {
	switch method {
	case "thread/unsubscribe":
		e.mu.Lock()
		e.requests = append(e.requests, method)
		e.loaded = false
		e.mu.Unlock()
		return json.Unmarshal([]byte(`{"status":"unsubscribed"}`), result)
	case "thread/loaded/list":
		e.mu.Lock()
		loaded := e.loaded
		e.requests = append(e.requests, method)
		e.mu.Unlock()
		raw := `{"data":[],"nextCursor":null}`
		if loaded {
			raw = `{"data":["thread-one"],"nextCursor":null}`
		}
		return json.Unmarshal([]byte(raw), result)
	default:
		return e.fakeEndpoint.Request(ctx, method, params, result)
	}
}
func transferBroker(t *testing.T) (*Broker, *transferEndpoint, *Binding, Fence) {
	t.Helper()
	e := &transferEndpoint{fakeEndpoint: newFakeEndpoint(), loaded: true}
	b, err := NewBroker(Config{Opener: func(context.Context) (Endpoint, error) { return e, nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	bd, fence := boundBinding(t, b, "thread-one")
	return b, e, bd, fence
}
func sourceFor(fence Fence) TransferSource {
	return TransferSource{Project: "p", Window: "w", Agent: "a", Pane: "pane", Generation: "g", Operation: "op", RuntimeID: "runtime", PaneRuntimeID: "%test", Endpoint: DefaultEndpointKey, Thread: "thread-one", Fence: fence}
}

func TestTransferDrainsAdmittedControlAndPinsLastConnection(t *testing.T) {
	b, e, bd, fence := transferBroker(t)
	e.hold("request:held")
	done := make(chan error, 1)
	go func() { _, err := bd.Submit(t.Context(), fence, Mutation{Method: "held"}); done <- err }()
	waitUntil(t, "admitted request", func() bool { return len(e.methods()) == 1 })
	prepared := make(chan TransferReceipt, 1)
	failed := make(chan error, 1)
	go func() {
		r, err := b.PrepareTransfer(t.Context(), sourceFor(fence), "owner", transferTestToken(t))
		if err != nil {
			failed <- err
		} else {
			prepared <- r
		}
	}()
	waitUntil(t, "source frozen", func() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.transfers["thread-one"] != nil })
	if _, err := bd.ControlAuthority(); RefusalOf(err) != RefusalControlNotOpen {
		t.Fatal(err)
	}
	if _, err := b.Bind("thread-one", "", nil); RefusalOf(err) != RefusalControlNotOpen {
		t.Fatal(err)
	}
	if _, err := bd.Submit(t.Context(), fence, Mutation{Method: "forbidden"}); RefusalOf(err) != RefusalControlNotOpen {
		t.Fatal(err)
	}
	select {
	case <-prepared:
		t.Fatal("drain finished before admitted write")
	default:
	}
	e.release("request:held")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var receipt TransferReceipt
	select {
	case receipt = <-prepared:
	case err := <-failed:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("prepare timeout")
	}
	_ = bd.Close()
	if !b.wanted() {
		t.Fatal("last Close lost transfer connection")
	}
	retired, err := b.RetireTransfer(t.Context(), receipt, "owner")
	if err != nil || !retired.Retired {
		t.Fatalf("retire=%+v %v", retired, err)
	}
	if _, err = b.Bind("thread-one", "", nil); RefusalOf(err) != RefusalControlNotOpen {
		t.Fatal("ACK released reservation")
	}
	if err = b.finishTransfer(retired, "owner", true); err != nil {
		t.Fatal(err)
	}
	if b.wanted() {
		t.Fatal("finished transfer kept empty connection")
	}
	if got := e.methods(); len(got) != 3 {
		t.Fatalf("provider wires=%v", got)
	}
}

func TestTransferAbortAndCoordinatorDeathFence(t *testing.T) {
	b, _, bd, fence := transferBroker(t)
	r, err := b.PrepareTransfer(t.Context(), sourceFor(fence), "owner", transferTestToken(t))
	if err != nil {
		t.Fatal(err)
	}
	if err = b.finishTransfer(r, "owner", false); err != nil {
		t.Fatal(err)
	}
	if got, err := bd.ControlAuthority(); err != nil || got != fence {
		t.Fatal("abort did not restore exact surviving binding")
	}
	r, err = b.PrepareTransfer(t.Context(), sourceFor(fence), "owner", transferTestToken(t))
	if err != nil {
		t.Fatal(err)
	}
	b.abandonTransfers("owner")
	if err = b.finishTransfer(r, "owner", true); RefusalOf(err) != RefusalControlNotOpen {
		t.Fatal(err)
	}
	_ = bd.Close()
	if !b.wanted() {
		t.Fatal("orphan dropped connection fence")
	}
	if _, err = b.Bind("thread-one", "", nil); RefusalOf(err) != RefusalControlNotOpen {
		t.Fatal(err)
	}
}

func TestTransferReclaimRequalifiesOrphanWithoutReopeningOldControl(t *testing.T) {
	b, _, bd, _ := transferBroker(t)
	fence, err := bd.ControlAuthority()
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := b.PrepareTransfer(context.Background(), sourceFor(fence), "departed", transferTestToken(t))
	if err != nil {
		t.Fatal(err)
	}
	receipt, err = b.RetireTransfer(context.Background(), receipt, "departed")
	if err != nil {
		t.Fatal(err)
	}
	b.abandonTransfers("departed")
	wrong := receipt
	wrong.Peer.Start = "foreign"
	if _, err = b.ReclaimTransfer(wrong, "recovery"); err == nil {
		t.Fatal("foreign peer reclaimed reservation")
	}
	got, err := b.ReclaimTransfer(receipt, "recovery")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Retired {
		t.Fatal("lost captured retirement proof")
	}
	if _, err = bd.ControlAuthority(); err == nil {
		t.Fatal("reclaim revived old control")
	}
	if err = b.finishTransfer(got, "departed", true); err == nil {
		t.Fatal("old coordinator completed reclaimed transfer")
	}
	if _, err = b.CheckTransfer(got, "recovery"); err != nil {
		t.Fatal(err)
	}
	if err = b.finishTransfer(got, "recovery", true); err != nil {
		t.Fatal(err)
	}
}

func transferTestToken(t *testing.T) string {
	t.Helper()
	r, err := NewTransferReceipt(sourceFor(Fence{Connection: 1, Binding: 1}))
	if err != nil {
		t.Fatal(err)
	}
	return r.Token
}

func TestTransferLostPrepareAndFinishAcknowledgementsRetainExactReceipts(t *testing.T) {
	b, _, bd, fence := transferBroker(t)
	pending, err := NewTransferReceipt(sourceFor(fence))
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.PrepareTransfer(t.Context(), pending.Source, "lost-ack", pending.Token)
	if err != nil {
		t.Fatal(err)
	}
	b.abandonTransfers("lost-ack")
	witnessed, err := b.InspectTransfer(pending)
	if err != nil || !witnessed.Peer.Valid() {
		t.Fatal(err)
	}
	got, err := b.ReclaimTransfer(witnessed, "new-owner")
	if err != nil {
		t.Fatal(err)
	}
	got, err = b.RetireTransfer(t.Context(), got, "new-owner")
	if err != nil {
		t.Fatal(err)
	}
	if err = b.finishTransfer(got, "new-owner", true); err != nil {
		t.Fatal(err)
	}
	completed, err := b.InspectTransfer(got)
	if err != nil || !completed.Completed {
		t.Fatal(err)
	}
	if _, err = bd.ControlAuthority(); RefusalOf(err) != RefusalBindingClosed {
		t.Fatal("old authority revived", err)
	}
	if _, err = bd.Submit(t.Context(), fence, Mutation{Method: "old-input"}); RefusalOf(err) != RefusalBindingClosed {
		t.Fatal(err)
	}
	foreign := got
	foreign.Source.Agent = "foreign"
	if _, err = b.InspectTransfer(foreign); err == nil {
		t.Fatal("foreign completion receipt accepted")
	}
}

func TestNativeTransferGrantKeepsOrdinaryAdmissionsFrozenUntilHandoff(t *testing.T) {
	b, e, old, fence := transferBroker(t)
	r, err := b.PrepareTransfer(t.Context(), sourceFor(fence), "owner", transferTestToken(t))
	if err != nil {
		t.Fatal(err)
	}
	r, err = b.RetireTransfer(t.Context(), r, "owner")
	if err != nil {
		t.Fatal(err)
	}
	target := NativeTransferTarget{Project: "p", Window: "w", Agent: "a", Pane: "new", Generation: "newgen", Operation: "newop", RuntimeID: "%new", Thread: "thread-one"}
	planned := target
	planned.RuntimeID = ""
	if err = b.grantNativeTransfer(r, "owner", planned); err != nil {
		t.Fatal(err)
	}
	if _, err = b.resumeNativeTransfer(t.Context(), nativeResumeRequest{Receipt: r}, "owner"); err != nil {
		t.Fatal(err)
	}
	if err = b.activateNativeTransfer(r, "owner", target); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Bind("thread-one", "", nil); RefusalOf(err) != RefusalControlNotOpen {
		t.Fatal("ordinary Bind escaped", err)
	}
	wrong := NativeTransferGrant{Receipt: r, Target: target}
	wrong.Target.Pane = "other"
	if _, err = b.bindWithGrant("thread-one", "", nil, 0, &wrong); err == nil {
		t.Fatal("wrong consumer admitted")
	}
	grant := NativeTransferGrant{Receipt: r, Target: target}
	fresh, err := b.bindWithGrant("thread-one", "", nil, 0, &grant)
	if err != nil {
		t.Fatal(err)
	}
	var current Fence
	waitUntil(t, "native barrier", func() bool { current, err = fresh.ControlAuthority(); return err == nil })
	before := len(e.methods())
	if _, err = fresh.Submit(t.Context(), current, Mutation{Method: "forbidden"}); RefusalOf(err) != RefusalControlNotOpen {
		t.Fatal(err)
	}
	if err = fresh.Answer(t.Context(), ApprovalLease{ThreadID: "thread-one", Fence: current}, nil); RefusalOf(err) != RefusalControlNotOpen {
		t.Fatal(err)
	}
	if len(e.methods()) != before {
		t.Fatal("target wrote before handoff")
	}
	if _, err = old.Submit(t.Context(), fence, Mutation{Method: "old"}); err == nil {
		t.Fatal("old token revived")
	}
	if err = b.finishTransfer(r, "owner", true); err != nil {
		t.Fatal(err)
	}
	if _, err = fresh.Submit(t.Context(), current, Mutation{Method: "allowed"}); err != nil {
		t.Fatal(err)
	}
	if _, err = old.Submit(t.Context(), fence, Mutation{Method: "old"}); err == nil {
		t.Fatal("old token revived after finish")
	}
}

func (e *transferEndpoint) ResumeThreadWithSettings(ctx context.Context, thread, cwd string, roots []string, settings codexappserver.ThreadSettings) (codexappserver.ThreadBinding, error) {
	return codexappserver.ThreadBinding{ThreadID: thread}, nil
}

func TestTransferCancelDuringDrainRetainsReceiptUntilExactAbort(t *testing.T) {
	b, e, bd, fence := transferBroker(t)
	e.hold("request:held")
	done := make(chan error, 1)
	go func() { _, err := bd.Submit(t.Context(), fence, Mutation{Method: "held"}); done <- err }()
	waitUntil(t, "admitted old request", func() bool { return len(e.methods()) == 1 })
	pending, err := NewTransferReceipt(sourceFor(fence))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	stopped := make(chan error, 1)
	go func() { _, err := b.PrepareTransfer(ctx, pending.Source, "cancelled", pending.Token); stopped <- err }()
	waitUntil(t, "draining reservation", func() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.transfers[pending.Source.Thread] != nil })
	cancel()
	if err = <-stopped; err == nil {
		t.Fatal("cancel succeeded")
	}
	if _, err = bd.Submit(t.Context(), fence, Mutation{Method: "forbidden"}); err == nil {
		t.Fatal("cancel opened admission before drain")
	}
	e.release("request:held")
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	b.abandonTransfers("cancelled")
	receipt, err := b.InspectTransfer(pending)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err = b.ReclaimTransfer(receipt, "recovery")
	if err != nil {
		t.Fatal(err)
	}
	if err = b.finishTransfer(receipt, "recovery", false); err != nil {
		t.Fatal(err)
	}
	if actual, err := bd.ControlAuthority(); err != nil || actual != fence {
		t.Fatal("pre-unsubscribe abort changed source authority", err)
	}
	for _, method := range e.methods() {
		if method == "thread/unsubscribe" || method == "forbidden" {
			t.Fatal("cancel touched wire", method)
		}
	}
}

type cancelledRetirementEndpoint struct {
	*transferEndpoint
	attempted chan struct{}
}

func (e *cancelledRetirementEndpoint) Request(ctx context.Context, method string, params, result any) error {
	if method == "thread/unsubscribe" {
		if err := e.transferEndpoint.Request(ctx, method, params, result); err != nil {
			return err
		}
		close(e.attempted)
		<-ctx.Done()
		return ctx.Err()
	}
	return e.transferEndpoint.Request(ctx, method, params, result)
}
func TestTransferCancelAfterUnsubscribeKeepsFenceAndSibling(t *testing.T) {
	e := &cancelledRetirementEndpoint{transferEndpoint: &transferEndpoint{fakeEndpoint: newFakeEndpoint(), loaded: true}, attempted: make(chan struct{})}
	b, err := NewBroker(Config{Opener: func(context.Context) (Endpoint, error) { return e, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	bd, fence := boundBinding(t, b, "thread-one")
	sibling, siblingFence := boundBinding(t, b, "sibling")
	r, err := b.PrepareTransfer(t.Context(), sourceFor(fence), "owner", transferTestToken(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := b.RetireTransfer(ctx, r, "owner"); done <- err }()
	<-e.attempted
	cancel()
	if err = <-done; err == nil {
		t.Fatal("uncertain retirement succeeded")
	}
	if err = b.finishTransfer(r, "owner", false); err == nil {
		t.Fatal("uncertain unsubscribe revived source")
	}
	if _, err = bd.Submit(t.Context(), fence, Mutation{Method: "forbidden"}); err == nil {
		t.Fatal("old writer admitted")
	}
	if _, err = sibling.Submit(t.Context(), siblingFence, Mutation{Method: "sibling-write"}); err != nil {
		t.Fatal("sibling affected", err)
	}
	if _, err = b.Bind("thread-one", "", nil); err == nil {
		t.Fatal("same-thread fence released")
	}
}

type epochRetirementEndpoint struct {
	*transferEndpoint
	replace func()
}

func (e *epochRetirementEndpoint) Request(ctx context.Context, method string, params, result any) error {
	err := e.transferEndpoint.Request(ctx, method, params, result)
	if method == "thread/unsubscribe" && err == nil && e.replace != nil {
		e.replace()
	}
	return err
}
func TestTransferEpochChangeAfterAckRequiresSamePeerRequalification(t *testing.T) {
	for _, samePeer := range []bool{false, true} {
		t.Run(fmt.Sprint(samePeer), func(t *testing.T) {
			e := &epochRetirementEndpoint{transferEndpoint: &transferEndpoint{fakeEndpoint: newFakeEndpoint(), loaded: true}}
			b, err := NewBroker(Config{Opener: func(context.Context) (Endpoint, error) { return e, nil }})
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			bd, fence := boundBinding(t, b, "thread-one")
			receipt, err := b.PrepareTransfer(t.Context(), sourceFor(fence), "source", transferTestToken(t))
			if err != nil {
				t.Fatal(err)
			}
			e.replace = func() {
				b.mu.Lock()
				defer b.mu.Unlock()
				peer := b.conn.peer
				if !samePeer {
					peer.Start = "new-peer"
				}
				b.conn = &connection{epoch: b.conn.epoch + 1, endpoint: e, peer: peer, answered: map[string]struct{}{}}
			}
			if _, err = b.RetireTransfer(t.Context(), receipt, "source"); err == nil {
				t.Fatal("ACK passed changed epoch")
			}
			if len(e.methods()) != 1 {
				t.Fatal("absence read ran after epoch changed", e.methods())
			}
			if _, err = bd.Submit(t.Context(), fence, Mutation{Method: "old"}); err == nil {
				t.Fatal("old control revived")
			}
			b.abandonTransfers("source")
			got, err := b.ReclaimTransfer(receipt, "recovery")
			if !samePeer {
				if err == nil {
					t.Fatal("replacement peer was adopted")
				}
				return
			}
			if err != nil || got.Retired {
				t.Fatal("new epoch retained old retirement proof", err)
			}
			e.replace = nil
			got, err = b.RetireTransfer(t.Context(), got, "recovery")
			if err != nil || !got.Retired {
				t.Fatal("same-peer requalification failed", err)
			}
			if got.Source != receipt.Source {
				t.Fatal("historical source identity changed")
			}
			if err = b.finishTransfer(got, "recovery", true); err != nil {
				t.Fatal(err)
			}
		})
	}
}
