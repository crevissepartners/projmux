package codexbroker

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"
)

// TestLongDisconnectReconnectsWhileABindingRemainsAndFencesTheOldEpochOut is
// the C-2 reconnect scope and failure contract. A disconnect longer than 3.5
// seconds must still be recovered for as long as one binding remains, and the
// revoked epoch must be inert afterwards: neither its events nor a fence
// minted on it may write over the epoch that replaced it.
//
// The 3.5 seconds are injected through the clock, so the outage is exact and
// the test costs no wall time.
func TestLongDisconnectReconnectsWhileABindingRemainsAndFencesTheOldEpochOut(t *testing.T) {
	first, second := newFakeEndpoint(), newFakeEndpoint()
	// Four refused attaches sit between the two connections: 250ms + 500ms +
	// 1s + 2s of capped exponential backoff is a 3.75s gap.
	broker, opener, clock := newTestBroker(t, 8, first, nil, nil, nil, nil, second)
	binding, staleFence := boundBinding(t, broker, "thread-one")
	broker.mu.Lock()
	staleConn := broker.conn
	broker.mu.Unlock()

	downAt := clock.Now()
	_ = first.Close()
	waitUntil(t, "the old epoch to be revoked", func() bool {
		return broker.Diagnostics().Disconnects == 1
	})
	if _, err := binding.ControlAuthority(); RefusalOf(err) != RefusalControlNotOpen {
		t.Fatalf("control authority while disconnected = %v", err)
	}

	for _, wait := range []time.Duration{
		250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second,
	} {
		waitUntil(t, "the next reconnect backoff to be scheduled", func() bool {
			return clock.pending() == 1
		})
		clock.Advance(wait)
	}
	waitUntil(t, "the replacement connection to serve", func() bool {
		return broker.Diagnostics().Connects == 2
	})
	liveFence := awaitSnapshot(t, binding).Fence

	if gap := clock.Now().Sub(downAt); gap < 3500*time.Millisecond {
		t.Fatalf("outage = %s, want the reconnect to survive more than 3.5s", gap)
	}
	if liveFence != (Fence{Connection: 2, Binding: 1}) {
		t.Fatalf("replacement fence = %+v", liveFence)
	}
	if got := broker.Diagnostics().ConnectionEpoch; got != 2 {
		t.Fatalf("connection epoch = %d, want the old epoch to be retired", got)
	}

	// An event from the retired epoch cannot reach the binding.
	broker.routeNotification(staleConn, threadEvent("thread-one", "item/started"))
	if got := broker.Diagnostics().StaleEvents; got != 1 {
		t.Fatalf("stale events = %d, want the retired epoch's event dropped", got)
	}
	assertQuiet(t, binding)

	// A fence minted on the retired epoch cannot write, on either connection.
	outcome, err := binding.Submit(t.Context(), staleFence, Mutation{Method: "turn/start"})
	if outcome != MutationRefused || RefusalOf(err) != RefusalStaleConnectionEpoch {
		t.Fatalf("retired fence mutation = %s/%v", outcome, err)
	}
	if got := first.methods(); len(got) != 0 {
		t.Fatalf("requests on the retired connection = %v, want none", got)
	}
	if got := second.methods(); len(got) != 0 {
		t.Fatalf("requests on the live connection = %v, want none", got)
	}

	// The current fence still works, so the refusal above is fencing and not a
	// broker that stopped writing.
	if outcome, err := binding.Submit(t.Context(), liveFence, Mutation{Method: "turn/start"}); outcome != MutationApplied || err != nil {
		t.Fatalf("live fence mutation = %s/%v", outcome, err)
	}

	// Once no binding remains there is nothing to reconnect for.
	attempts := opener.count()
	_ = binding.Close()
	waitUntil(t, "the live connection to be released", func() bool {
		return broker.Diagnostics().Disconnects == 2
	})
	if opener.count() != attempts || clock.pending() != 0 {
		t.Fatalf("open attempts = %d (was %d), pending waits = %d; want no reconnect without a binding",
			opener.count(), attempts, clock.pending())
	}
}

// TestMutationAtADisconnectBoundaryEndsIndeterminateWithZeroResends is the C-2
// non-guarantee. A control request whose answer is lost at a disconnect has an
// unknown result, and guessing either way is worse than saying so: it is
// classified indeterminate, recorded with exactly one attempt, and never put
// back on the replacement connection.
func TestMutationAtADisconnectBoundaryEndsIndeterminateWithZeroResends(t *testing.T) {
	first, second := newFakeEndpoint(), newFakeEndpoint()
	broker, _, _ := newTestBroker(t, 8, first, second)
	binding, fence := boundBinding(t, broker, "thread-one")

	first.hold("request:turn/interrupt")
	var outcome MutationOutcome
	var submitErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		outcome, submitErr = binding.Submit(t.Context(), fence, Mutation{Method: "turn/interrupt"})
	}()
	waitUntil(t, "the mutation to reach the endpoint", func() bool {
		return len(first.methods()) == 1
	})
	_ = first.Close()
	<-done

	if outcome != MutationIndeterminate || RefusalOf(submitErr) != RefusalDisconnectBoundary {
		t.Fatalf("mutation at the disconnect boundary = %s/%v", outcome, submitErr)
	}
	ledger := broker.WriteLedger()
	want := WriteRecord{Fence: fence, Method: "turn/interrupt", Outcome: MutationIndeterminate, Attempts: 1}
	if len(ledger) != 1 || ledger[0] != want {
		t.Fatalf("write ledger = %+v, want exactly %+v", ledger, want)
	}

	waitUntil(t, "the replacement connection to serve", func() bool {
		return broker.Diagnostics().Connects == 2
	})
	liveFence := awaitSnapshot(t, binding).Fence
	if got := second.methods(); len(got) != 0 {
		t.Fatalf("requests on the replacement connection = %v, want zero automatic resends", got)
	}
	diagnostics := broker.Diagnostics()
	if diagnostics.Resends != 0 || diagnostics.Indeterminate != 1 || diagnostics.Applied != 0 || diagnostics.Refused != 0 {
		t.Fatalf("mutation diagnostics = %+v", diagnostics)
	}

	// An endpoint that answers with an error is a refusal, not an unknown, so
	// the indeterminate class stays reserved for the disconnect boundary.
	second.fail("request:turn/steer", errors.New("upstream rejected the steer"))
	if outcome, err := binding.Submit(t.Context(), liveFence, Mutation{Method: "turn/steer"}); outcome != MutationRefused || err == nil {
		t.Fatalf("answered error = %s/%v", outcome, err)
	}
	ledger = broker.WriteLedger()
	if len(ledger) != 2 || ledger[1].Outcome != MutationRefused || ledger[1].Attempts != 1 {
		t.Fatalf("write ledger after the refusal = %+v", ledger)
	}
	if got := broker.Diagnostics(); got.Resends != 0 || got.Refused != 1 {
		t.Fatalf("refusal diagnostics = %+v", got)
	}
}

// TestReconnectBackoffStaysInsideItsCappedJitterBounds pins the supervisor's
// wait schedule. The floor keeps a refusing endpoint from becoming a hot loop
// and the cap keeps a long outage from stranding a binding, so both bounds are
// asserted rather than the exact sequence.
func TestReconnectBackoffStaysInsideItsCappedJitterBounds(t *testing.T) {
	for _, jitter := range []float64{-1, 0, 0.25, 0.5, 1, 2} {
		previous := time.Duration(0)
		for attempt := 1; attempt <= 30; attempt++ {
			delay := backoffDelay(attempt, jitter)
			if delay < baseReconnectDelay/2 || delay > maxReconnectDelay {
				t.Fatalf("backoffDelay(%d, %v) = %s, outside [%s, %s]",
					attempt, jitter, delay, baseReconnectDelay/2, maxReconnectDelay)
			}
			if delay < previous {
				t.Fatalf("backoffDelay(%d, %v) = %s, shrank from %s", attempt, jitter, delay, previous)
			}
			previous = delay
		}
		if previous != backoffDelay(1000, jitter) {
			t.Fatalf("backoff at jitter %v did not settle on its cap", jitter)
		}
	}
	if got := backoffDelay(0, 0); got != backoffDelay(1, 0) {
		t.Fatalf("backoffDelay(0, 0) = %s, want the first attempt's wait", got)
	}
	if got, want := backoffDelay(1, 0), baseReconnectDelay/2; got != want {
		t.Fatalf("unjittered first wait = %s, want %s", got, want)
	}
	if got, want := backoffDelay(1, 1), baseReconnectDelay; got != want {
		t.Fatalf("fully jittered first wait = %s, want %s", got, want)
	}
	if got, want := backoffDelay(30, 1), maxReconnectDelay; got != want {
		t.Fatalf("capped wait = %s, want %s", got, want)
	}
}

// TestConcurrentBindUnbindReconnectAndSlowConsumerAreRaceAndLeakFree is the
// race and leak layer. Binds, unbinds, connection replacement, event delivery,
// and a consumer that never reads all run at once; afterwards the broker must
// close cleanly with no goroutine left behind.
func TestConcurrentBindUnbindReconnectAndSlowConsumerAreRaceAndLeakFree(t *testing.T) {
	before := runtime.NumGoroutine()
	opener := &recyclingOpener{}
	broker, err := NewBroker(Config{
		Opener:  opener.open,
		Clock:   newFakeClock(),
		Jitter:  func() float64 { return 0 },
		Backlog: 4,
	})
	if err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var workers sync.WaitGroup
	for worker := range 6 {
		workers.Go(func() {
			threadID := fmt.Sprintf("thread-%d", worker)
			for range 15 {
				binding, err := broker.Bind(threadID, "/work/project", nil)
				if err != nil {
					continue
				}
				// Half the workers are deliberate slow consumers that let
				// their bounded queue overflow.
				if worker%2 == 0 {
					for drained := 0; drained < 3; drained++ {
						select {
						case _, ok := <-binding.Events():
							if !ok {
								drained = 3
							}
						default:
							drained = 3
						}
					}
				}
				_ = binding.Close()
			}
		})
	}
	workers.Add(2)
	go func() {
		defer workers.Done()
		for round := range 400 {
			if endpoint := opener.live(); endpoint != nil {
				endpoint.push(threadEvent(fmt.Sprintf("thread-%d", round%6), "item/updated"))
			}
			runtime.Gosched()
		}
	}()
	go func() {
		defer workers.Done()
		for range 8 {
			if endpoint := opener.live(); endpoint != nil {
				_ = endpoint.Close()
			}
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
			}
		}
	}()

	workers.Wait()
	close(stop)
	if err := broker.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	// Close is idempotent and concurrent-safe.
	var closers sync.WaitGroup
	for range 4 {
		closers.Go(func() {
			_ = broker.Close()
		})
	}
	closers.Wait()
	if got := broker.Diagnostics().Bindings; got != 0 {
		t.Fatalf("bindings after Close = %d", got)
	}
	assertNoGoroutineLeak(t, before)
}
