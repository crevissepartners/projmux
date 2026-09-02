package codexbroker

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestGenerationEndpointKeyRoundTripTable(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		domain     string
		generation string
		want       Refusal
	}{
		{name: "opaque", domain: "state-domain-a", generation: "generation-152-0", want: RefusalNone},
		{name: "canonical punctuation", domain: "domain:one_two", generation: "generation:two.one", want: RefusalNone},
		{name: "slash", domain: "domain/foreign", generation: "generation", want: RefusalEndpointIdentityInvalid},
		{name: "missing domain", generation: "generation", want: RefusalEndpointIdentityInvalid},
		{name: "missing generation", domain: "domain", want: RefusalEndpointIdentityInvalid},
		{name: "newline", domain: "domain\nforeign", generation: "generation", want: RefusalEndpointIdentityInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			key, err := NewEndpointKey(test.domain, test.generation)
			if got := RefusalOf(err); got != test.want {
				t.Fatalf("refusal = %s, want %s", got, test.want)
			}
			if test.want != RefusalNone {
				return
			}
			identity, ok := key.Identity()
			if !ok || identity != (EndpointIdentity{StateDomainID: test.domain, EndpointGenerationID: test.generation}) {
				t.Fatalf("identity = %+v, %v", identity, ok)
			}
			if key == DefaultEndpointKey {
				t.Fatal("a private generation collapsed onto the unmanaged default endpoint")
			}
		})
	}
	canonical, err := NewEndpointKey("f", "generation")
	if err != nil {
		t.Fatal(err)
	}
	// Raw base64 accepts non-zero trailing bits for some spellings. The pool
	// must still reject a second encoding of the same durable identity.
	nonCanonical := EndpointKey(strings.Replace(string(canonical), "Zg:", "Zh:", 1))
	if _, ok := nonCanonical.Identity(); ok {
		t.Fatalf("non-canonical generation key decoded: %q", nonCanonical)
	}
}

func TestGenerationPoolReconnectIsGenerationLocalAndCrossRouteWritesZero(t *testing.T) {
	oldFirst, oldSecond, current := newFakeEndpoint(), newFakeEndpoint(), newFakeEndpoint()
	oldOpener := &scriptedOpener{steps: []*fakeEndpoint{oldFirst, oldSecond}}
	currentOpener := &scriptedOpener{steps: []*fakeEndpoint{current}}
	oldClock, currentClock := newFakeClock(), newFakeClock()
	oldIdentity := EndpointIdentity{StateDomainID: "domain-shared", EndpointGenerationID: "generation-old"}
	currentIdentity := EndpointIdentity{StateDomainID: "domain-shared", EndpointGenerationID: "generation-current"}
	pool := NewGenerationPool()
	t.Cleanup(func() { _ = pool.Close() })
	if err := pool.Prepare(PoolConfig{Endpoint: oldIdentity, Opener: oldOpener.open, Clock: oldClock, Jitter: func() float64 { return 1 }}); err != nil {
		t.Fatal(err)
	}
	if err := pool.Prepare(PoolConfig{Endpoint: currentIdentity, Opener: currentOpener.open, Clock: currentClock, Jitter: func() float64 { return 1 }}); err != nil {
		t.Fatal(err)
	}
	oldRoute := GenerationRoute{Endpoint: oldIdentity, ThreadID: "thread-old"}
	currentRoute := GenerationRoute{Endpoint: currentIdentity, ThreadID: "thread-current"}
	oldBinding, err := pool.BindExisting(oldRoute, "/old", nil)
	if err != nil {
		t.Fatal(err)
	}
	currentBinding, err := pool.BindExisting(currentRoute, "/current", nil)
	if err != nil {
		t.Fatal(err)
	}
	awaitPooledSnapshot(t, oldBinding, "thread-old")
	awaitPooledSnapshot(t, currentBinding, "thread-current")
	currentAuthority, err := currentBinding.ControlAuthority()
	if err != nil {
		t.Fatal(err)
	}
	currentLedger := generationLedgerFor(t, pool, currentIdentity)

	// The same exact provider thread token cannot silently become a binding in
	// another generation.
	_, err = pool.BindExisting(GenerationRoute{Endpoint: currentIdentity, ThreadID: "thread-old"}, "/foreign", nil)
	if got := RefusalOf(err); got != RefusalRouteMismatch {
		t.Fatalf("same-thread cross-generation bind refusal = %s", got)
	}
	if current.visited("snapshot:thread-old") != 0 {
		t.Fatal("cross-generation bind reached the sibling endpoint")
	}

	// Replacing G(N)'s connection is driven by its own semantic disconnect and
	// injected clock. G(N+1)'s opener, authority, and wire remain untouched.
	_ = oldFirst.Close()
	select {
	case <-oldBinding.Suspensions():
	case <-time.After(5 * time.Second):
		t.Fatal("old generation did not publish its disconnect boundary")
	}
	awaitPooledSnapshot(t, oldBinding, "thread-old")
	if currentOpener.count() != 1 {
		t.Fatalf("current generation opens = %d, want 1", currentOpener.count())
	}
	afterAuthority, err := currentBinding.ControlAuthority()
	if err != nil {
		t.Fatal(err)
	}
	if afterAuthority != currentAuthority {
		t.Fatalf("current authority changed across sibling reconnect: before=%+v after=%+v", currentAuthority, afterAuthority)
	}
	afterLedger := generationLedgerFor(t, pool, currentIdentity)
	if !reflect.DeepEqual(afterLedger, currentLedger) {
		t.Fatalf("current ledger changed across sibling reconnect:\nbefore=%+v\nafter=%+v", currentLedger, afterLedger)
	}

	wrongRoute := GenerationRoute{Endpoint: oldIdentity, ThreadID: currentRoute.ThreadID}
	if _, err := currentBinding.Submit(context.Background(), wrongRoute, currentAuthority, Mutation{Method: "thread/start"}); RefusalOf(err) != RefusalRouteMismatch {
		t.Fatalf("cross-route submit = %v", err)
	}
	if len(current.methods()) != 0 || len(oldSecond.methods()) != 0 {
		t.Fatalf("cross-route provider writes: current=%v old=%v", current.methods(), oldSecond.methods())
	}
}

func TestGenerationPoolBrokerRestartRestoresExactBindingsAndFencesOldRuntime(t *testing.T) {
	first, replacement := newFakeEndpoint(), newFakeEndpoint()
	firstOpener := &scriptedOpener{steps: []*fakeEndpoint{first}}
	replacementOpener := &scriptedOpener{steps: []*fakeEndpoint{replacement}}
	identity := EndpointIdentity{StateDomainID: "domain-restart", EndpointGenerationID: "generation-152-1"}
	route := GenerationRoute{Endpoint: identity, ThreadID: "thread-exact"}
	pool := NewGenerationPool()
	t.Cleanup(func() { _ = pool.Close() })
	if err := pool.Prepare(PoolConfig{Endpoint: identity, Opener: firstOpener.open}); err != nil {
		t.Fatal(err)
	}
	binding, err := pool.BindExisting(route, "/exact/cwd", []string{"/exact/root"})
	if err != nil {
		t.Fatal(err)
	}
	initialSnapshot := awaitPooledSnapshot(t, binding, route.ThreadID)
	if initialSnapshot.Snapshot.CWD != "/exact/cwd" {
		t.Fatalf("initial binding CWD = %q", initialSnapshot.Snapshot.CWD)
	}
	oldAuthority, err := binding.ControlAuthority()
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.RestartBroker(identity, replacementOpener.open); err != nil {
		t.Fatal(err)
	}
	restoredSnapshot := awaitPooledSnapshot(t, binding, route.ThreadID)
	if restoredSnapshot.Snapshot.CWD != initialSnapshot.Snapshot.CWD {
		t.Fatalf("restored binding CWD = %q, want %q", restoredSnapshot.Snapshot.CWD, initialSnapshot.Snapshot.CWD)
	}
	newAuthority, err := binding.ControlAuthority()
	if err != nil {
		t.Fatal(err)
	}
	if oldAuthority.Runtime == newAuthority.Runtime {
		t.Fatal("broker restart reused its runtime identity")
	}
	if oldAuthority.Fence.Binding != newAuthority.Fence.Binding {
		t.Fatalf("exact binding epoch changed across restart: old=%+v new=%+v", oldAuthority, newAuthority)
	}
	if got := replacement.bootstrapped(); !reflect.DeepEqual(got, []string{route.ThreadID}) {
		t.Fatalf("restored bindings = %v", got)
	}
	if _, err := binding.Submit(context.Background(), route, oldAuthority, Mutation{Method: "thread/start"}); RefusalOf(err) != RefusalBrokerRuntimeStale {
		t.Fatalf("old runtime authority = %v", err)
	}
	if len(replacement.methods()) != 0 {
		t.Fatalf("old runtime wrote replacement wire: %v", replacement.methods())
	}
	if _, err := binding.Submit(context.Background(), route, newAuthority, Mutation{Method: "thread/start"}); err != nil {
		t.Fatalf("new authority submit: %v", err)
	}
	if got := replacement.methods(); !reflect.DeepEqual(got, []string{"thread/start"}) {
		t.Fatalf("replacement writes = %v", got)
	}
	ledger := generationLedgerFor(t, pool, identity)
	if ledger.Restarts != 1 || ledger.BindingRestores != 1 || len(ledger.Bindings) != 1 || ledger.Bindings[0].ThreadID != route.ThreadID {
		t.Fatalf("restart ledger = %+v", ledger)
	}
	if ledger.Bindings[0].BindingEpoch != newAuthority.Fence.Binding {
		t.Fatalf("binding ledger epoch = %+v authority=%+v", ledger.Bindings[0], newAuthority)
	}
}

func TestGenerationPoolBrokerRestartPreservesSparseBindingEpochLedger(t *testing.T) {
	first, replacement := newFakeEndpoint(), newFakeEndpoint()
	identity := EndpointIdentity{StateDomainID: "domain-sparse", EndpointGenerationID: "generation-sparse"}
	pool := NewGenerationPool()
	t.Cleanup(func() { _ = pool.Close() })
	if err := pool.Prepare(PoolConfig{Endpoint: identity, Opener: (&scriptedOpener{steps: []*fakeEndpoint{first}}).open}); err != nil {
		t.Fatal(err)
	}
	bindings := make([]*PooledBinding, 0, 3)
	authorities := make([]PoolAuthority, 0, 3)
	for _, threadID := range []string{"thread-one", "thread-two", "thread-three"} {
		binding, err := pool.BindExisting(GenerationRoute{Endpoint: identity, ThreadID: threadID}, "/"+threadID, nil)
		if err != nil {
			t.Fatal(err)
		}
		bindings = append(bindings, binding)
		awaitPooledSnapshot(t, binding, threadID)
		authority, err := binding.ControlAuthority()
		if err != nil {
			t.Fatal(err)
		}
		authorities = append(authorities, authority)
	}
	if err := bindings[1].Close(); err != nil {
		t.Fatal(err)
	}
	if err := pool.RestartBroker(identity, (&scriptedOpener{steps: []*fakeEndpoint{replacement}}).open); err != nil {
		t.Fatal(err)
	}
	for _, index := range []int{0, 2} {
		awaitPooledSnapshot(t, bindings[index], bindings[index].route.ThreadID)
		authority, err := bindings[index].ControlAuthority()
		if err != nil {
			t.Fatal(err)
		}
		if authority.Fence.Binding != authorities[index].Fence.Binding {
			t.Fatalf("sparse binding %s epoch changed: before=%d after=%d", bindings[index].route.ThreadID, authorities[index].Fence.Binding, authority.Fence.Binding)
		}
	}
	ledger := generationLedgerFor(t, pool, identity)
	if len(ledger.Bindings) != 2 || ledger.Bindings[0].BindingEpoch != 1 || ledger.Bindings[1].BindingEpoch != 3 {
		// Ledger rows sort by thread token while retaining their sparse epochs.
		t.Fatalf("sparse exact binding ledger = %+v", ledger.Bindings)
	}
}

func TestGenerationPoolFailedRestartRetryKeepsLedgerExactAndFencesOldRuntime(t *testing.T) {
	first, reconnected := newFakeEndpoint(), newFakeEndpoint()
	oldOpener := &scriptedOpener{steps: []*fakeEndpoint{first, reconnected}}
	failedReplacement, successfulReplacement := newFakeEndpoint(), newFakeEndpoint()
	identity := EndpointIdentity{StateDomainID: "domain-retry", EndpointGenerationID: "generation-retry"}
	route := GenerationRoute{Endpoint: identity, ThreadID: "thread-retry"}
	clock := newFakeClock()
	pool := NewGenerationPool()
	t.Cleanup(func() { _ = pool.Close() })
	if err := pool.Prepare(PoolConfig{Endpoint: identity, Opener: oldOpener.open, Clock: clock, Jitter: func() float64 { return 1 }}); err != nil {
		t.Fatal(err)
	}
	binding, err := pool.BindExisting(route, "/retry/cwd", []string{"/retry/root"})
	if err != nil {
		t.Fatal(err)
	}
	awaitPooledSnapshot(t, binding, route.ThreadID)
	_ = first.Close()
	select {
	case <-binding.Suspensions():
	case <-time.After(5 * time.Second):
		t.Fatal("original broker did not cross its reconnect barrier")
	}
	awaitPooledSnapshot(t, binding, route.ThreadID)
	oldAuthority, err := binding.ControlAuthority()
	if err != nil {
		t.Fatal(err)
	}
	before := generationLedgerFor(t, pool, identity)
	if before.Initializes != 2 || before.Reconnects != 1 || before.Snapshots != 2 {
		t.Fatalf("pre-restart ledger = %+v", before)
	}

	pool.beforeRestartSwap = func() error { return errors.New("deterministic restore receipt failure") }
	err = pool.RestartBroker(identity, (&scriptedOpener{steps: []*fakeEndpoint{failedReplacement}}).open)
	if RefusalOf(err) != RefusalBindingRestoreFailed {
		t.Fatalf("failed restart refusal = %v", err)
	}
	if afterFailure := generationLedgerFor(t, pool, identity); !reflect.DeepEqual(afterFailure, before) {
		t.Fatalf("failed restart changed cumulative ledger:\nbefore=%+v\nafter=%+v", before, afterFailure)
	}

	pool.beforeRestartSwap = nil
	if err := pool.RestartBroker(identity, (&scriptedOpener{steps: []*fakeEndpoint{successfulReplacement}}).open); err != nil {
		t.Fatal(err)
	}
	awaitPooledSnapshot(t, binding, route.ThreadID)
	afterRetry := generationLedgerFor(t, pool, identity)
	if afterRetry.Initializes != before.Initializes+1 || afterRetry.Reconnects != before.Reconnects ||
		afterRetry.Snapshots != before.Snapshots+1 || afterRetry.Restarts != 1 ||
		afterRetry.BindingRestores != 1 || len(afterRetry.Bindings) != 1 ||
		afterRetry.Bindings[0].BindingEpoch != before.Bindings[0].BindingEpoch {
		t.Fatalf("retry ledger double-counted or lost exact binding: before=%+v after=%+v", before, afterRetry)
	}
	if afterRetry.BrokerRuntimeID == oldAuthority.Runtime {
		t.Fatal("successful retry retained old broker runtime identity")
	}
	if _, err := binding.Submit(context.Background(), route, oldAuthority, Mutation{Method: "thread/start"}); RefusalOf(err) != RefusalBrokerRuntimeStale {
		t.Fatalf("old runtime authority after retry = %v", err)
	}
	if len(successfulReplacement.methods()) != 0 {
		t.Fatalf("old runtime authority wrote after retry: %v", successfulReplacement.methods())
	}
}

func TestPreparingGenerationReadyStillHasZeroCreateAdmission(t *testing.T) {
	endpoint := newFakeEndpoint()
	opener := &scriptedOpener{steps: []*fakeEndpoint{endpoint}}
	identity := EndpointIdentity{StateDomainID: "domain-dark", EndpointGenerationID: "generation-ready"}
	pool := NewGenerationPool()
	t.Cleanup(func() { _ = pool.Close() })
	if err := pool.Prepare(PoolConfig{Endpoint: identity, Opener: opener.open}); err != nil {
		t.Fatal(err)
	}
	binding, err := pool.BindExisting(GenerationRoute{Endpoint: identity, ThreadID: "thread-existing"}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	awaitPooledSnapshot(t, binding, "thread-existing")
	ledger := generationLedgerFor(t, pool, identity)
	if !ledger.Preparing || !ledger.Ready || ledger.Initializes != 1 || ledger.Snapshots != 1 {
		t.Fatalf("dark readiness ledger = %+v", ledger)
	}
	before := append([]string(nil), endpoint.methods()...)
	err = pool.AdmitCreate(GenerationRoute{Endpoint: identity, ThreadID: "thread-new"})
	if RefusalOf(err) != RefusalAdmissionClosed {
		t.Fatalf("create admission = %v", err)
	}
	if !reflect.DeepEqual(endpoint.methods(), before) || endpoint.visited("snapshot:thread-new") != 0 {
		t.Fatal("Preparing create admission reached the provider")
	}
}

func TestGenerationPoolStateTableKeepsPreparingDarkAndBounded(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		prepare    []EndpointIdentity
		admit      EndpointIdentity
		want       Refusal
		wantLedger int
	}{
		{
			name:    "not-ready Preparing admission closed",
			prepare: []EndpointIdentity{{StateDomainID: "domain-a", EndpointGenerationID: "g1"}},
			admit:   EndpointIdentity{StateDomainID: "domain-a", EndpointGenerationID: "g1"},
			want:    RefusalAdmissionClosed, wantLedger: 1,
		},
		{
			name:    "unknown generation",
			prepare: []EndpointIdentity{{StateDomainID: "domain-a", EndpointGenerationID: "g1"}},
			admit:   EndpointIdentity{StateDomainID: "domain-a", EndpointGenerationID: "missing"},
			want:    RefusalEndpointUnknown, wantLedger: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool := NewGenerationPool()
			defer pool.Close()
			for _, identity := range test.prepare {
				if err := pool.Prepare(PoolConfig{Endpoint: identity, Opener: func(context.Context) (Endpoint, error) {
					return nil, errOpenerExhausted
				}}); err != nil {
					t.Fatal(err)
				}
			}
			err := pool.AdmitCreate(GenerationRoute{Endpoint: test.admit, ThreadID: "thread-new"})
			if RefusalOf(err) != test.want || len(pool.Ledger()) != test.wantLedger {
				t.Fatalf("admission=%v ledger=%+v", err, pool.Ledger())
			}
		})
	}

	pool := NewGenerationPool()
	defer pool.Close()
	var opens int
	opener := func(context.Context) (Endpoint, error) { opens++; return newFakeEndpoint(), nil }
	for _, generation := range []string{"g1", "g2"} {
		if err := pool.Prepare(PoolConfig{Endpoint: EndpointIdentity{StateDomainID: "bounded-domain", EndpointGenerationID: generation}, Opener: opener}); err != nil {
			t.Fatal(err)
		}
	}
	err := pool.Prepare(PoolConfig{Endpoint: EndpointIdentity{StateDomainID: "bounded-domain", EndpointGenerationID: "g3"}, Opener: opener})
	if RefusalOf(err) != RefusalGenerationCapacityExceeded {
		t.Fatalf("third generation = %v", err)
	}
	if len(pool.Ledger()) != 2 || opens != 0 {
		t.Fatalf("third generation had effects: ledger=%+v opens=%d", pool.Ledger(), opens)
	}
}

func TestGenerationPoolRestartFenceRejectsBindingThatMissedTheSnapshot(t *testing.T) {
	first, replacement := newFakeEndpoint(), newFakeEndpoint()
	firstOpener := &scriptedOpener{steps: []*fakeEndpoint{first}}
	replacementOpener := &scriptedOpener{steps: []*fakeEndpoint{replacement}}
	identity := EndpointIdentity{StateDomainID: "domain-race", EndpointGenerationID: "generation-race"}
	pool := NewGenerationPool()
	t.Cleanup(func() { _ = pool.Close() })
	if err := pool.Prepare(PoolConfig{Endpoint: identity, Opener: firstOpener.open}); err != nil {
		t.Fatal(err)
	}
	reached, release := make(chan struct{}), make(chan struct{})
	pool.beforeBindCommit = func() {
		close(reached)
		<-release
	}
	bindResult := make(chan error, 1)
	go func() {
		_, err := pool.BindExisting(GenerationRoute{Endpoint: identity, ThreadID: "thread-raced"}, "", nil)
		bindResult <- err
	}()
	<-reached
	if err := pool.RestartBroker(identity, replacementOpener.open); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-bindResult; RefusalOf(err) != RefusalBrokerRuntimeStale {
		t.Fatalf("raced bind refusal = %v", err)
	}
	ledger := generationLedgerFor(t, pool, identity)
	if len(ledger.Bindings) != 0 || ledger.BindingRestores != 0 || replacement.visited("snapshot:thread-raced") != 0 {
		t.Fatalf("raced binding escaped restart fence: ledger=%+v snapshots=%v", ledger, replacement.bootstrapped())
	}
}

func TestGenerationPoolRestartFenceRefusesControlAndWriteUntilExactSwap(t *testing.T) {
	first, replacement := newFakeEndpoint(), newFakeEndpoint()
	identity := EndpointIdentity{StateDomainID: "domain-linear", EndpointGenerationID: "generation-linear"}
	route := GenerationRoute{Endpoint: identity, ThreadID: "thread-linear"}
	pool := NewGenerationPool()
	t.Cleanup(func() { _ = pool.Close() })
	if err := pool.Prepare(PoolConfig{Endpoint: identity, Opener: (&scriptedOpener{steps: []*fakeEndpoint{first}}).open}); err != nil {
		t.Fatal(err)
	}
	binding, err := pool.BindExisting(route, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	awaitPooledSnapshot(t, binding, route.ThreadID)
	oldAuthority, err := binding.ControlAuthority()
	if err != nil {
		t.Fatal(err)
	}
	reached, release := make(chan struct{}), make(chan struct{})
	pool.beforeRestartSwap = func() error {
		close(reached)
		<-release
		return nil
	}
	restartResult := make(chan error, 1)
	go func() {
		restartResult <- pool.RestartBroker(identity, (&scriptedOpener{steps: []*fakeEndpoint{replacement}}).open)
	}()
	<-reached
	if _, err := binding.ControlAuthority(); RefusalOf(err) != RefusalBrokerRestarting {
		t.Fatalf("control authority during restart = %v", err)
	}
	if _, err := binding.Submit(context.Background(), route, oldAuthority, Mutation{Method: "thread/start"}); RefusalOf(err) != RefusalBrokerRestarting {
		t.Fatalf("submit during restart = %v", err)
	}
	if len(first.methods()) != 0 || len(replacement.methods()) != 0 {
		t.Fatalf("restart-fenced authority wrote: old=%v replacement=%v", first.methods(), replacement.methods())
	}
	close(release)
	if err := <-restartResult; err != nil {
		t.Fatal(err)
	}
	awaitPooledSnapshot(t, binding, route.ThreadID)
	if _, err := binding.Submit(context.Background(), route, oldAuthority, Mutation{Method: "thread/start"}); RefusalOf(err) != RefusalBrokerRuntimeStale {
		t.Fatalf("old authority after exact swap = %v", err)
	}
	if len(replacement.methods()) != 0 {
		t.Fatalf("old runtime wrote after exact swap: %v", replacement.methods())
	}
}

func awaitPooledSnapshot(t *testing.T, binding *PooledBinding, threadID string) Event {
	t.Helper()
	select {
	case event, open := <-binding.Events():
		if !open {
			t.Fatal("pooled binding closed before snapshot")
		}
		if event.Origin != EventOriginSnapshot || event.Snapshot.ThreadID != threadID {
			t.Fatalf("snapshot = %+v", event)
		}
		return event
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for pooled snapshot")
		return Event{}
	}
}

func generationLedgerFor(t *testing.T, pool *GenerationPool, identity EndpointIdentity) GenerationLedger {
	t.Helper()
	for _, ledger := range pool.Ledger() {
		if ledger.Endpoint == identity {
			return ledger
		}
	}
	t.Fatalf("no ledger for %+v", identity)
	return GenerationLedger{}
}
