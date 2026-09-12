package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexupgrade"
)

// A broker barrier is authority only while that exact connection is current.
// Keeping the closed epoch must never allow a late producer to republish it.
func TestRecoveredBrokerRetiredBarrierCannotRepublishAuthority(t *testing.T) {
	endpoint := coremetadata.CodexEndpointRef{StateDomainID: "recovery-domain", EndpointGenerationID: "codex-0.151.0"}
	key, err := codexbroker.NewEndpointKey(endpoint.StateDomainID, endpoint.EndpointGenerationID)
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := codexbroker.NewDiscovery(shortTempDomain(t), key)
	if err != nil {
		t.Fatal(err)
	}
	server := newBrokerTestEndpoint()
	broker, err := codexbroker.NewBroker(codexbroker.Config{Endpoint: key, Opener: func(context.Context) (codexbroker.Endpoint, error) { return server, nil }})
	if err != nil {
		t.Fatal(err)
	}
	host, err := codexbroker.StartHost(codexbroker.HostConfig{Discovery: discovery, Broker: broker, IdleTimeout: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	defer broker.Close()
	session := newCodexBrokerObserverSessionOn(brokerTestIdentity("recovery-thread"), "", nil, discovery, nil)
	session.endpoint = endpoint
	defer session.Close()
	epoch := openBrokerEpoch(t, session)
	if _, err := epoch.GenerationAuthority(); err != nil {
		t.Fatal(err)
	}
	if err := epoch.Close(); err != nil {
		t.Fatal(err)
	}
	if authority, err := epoch.GenerationAuthority(); err == nil {
		t.Fatalf("retired producer republished authority: %+v", authority)
	}
}

func TestRecoveredDefaultBrokerRebindsExactAuthorityAndControlConsumers(t *testing.T) {
	store, identity := phase0RNativeCodexFixture(t)
	oldRef := coremetadata.CodexEndpointRef{StateDomainID: "recovery-state", EndpointGenerationID: "codex-0.151.0"}
	newRef := coremetadata.CodexEndpointRef{StateDomainID: oldRef.StateDomainID, EndpointGenerationID: "codex-0.154.0"}
	agent, _ := store.registry.Agent(identity.AgentUID)
	agent.Status.SessionRef.Codex.Endpoint = &oldRef
	agent.Status.SessionRef.Codex.Lifecycle = &coremetadata.CodexGenerationLifecycleRef{State: coremetadata.CodexGenerationCurrent}
	command := testAICommand(t.TempDir())
	command.loadRegistry = store.store().load
	command.updateRegistry = store.store().update
	command.acquireCodexAuthority = func(string) (func(), error) { return func() {}, nil }
	sink := aiCodexLifecycleSink{command: command, runner: phase3StaticTmuxRunner{output: identity.PaneUID}}
	observer := codexNativeObserver{identity: identity, endpoint: oldRef, generationState: coremetadata.CodexGenerationCurrent, sink: sink}
	routes := map[coremetadata.CodexEndpointRef]codexbroker.Discovery{}
	servers := map[coremetadata.CodexEndpointRef]*brokerTestEndpoint{}
	hosts := map[coremetadata.CodexEndpointRef]*codexbroker.Host{}
	for _, ref := range []coremetadata.CodexEndpointRef{oldRef, newRef} {
		key, err := codexbroker.NewEndpointKey(ref.StateDomainID, ref.EndpointGenerationID)
		if err != nil {
			t.Fatal(err)
		}
		discovery, err := codexbroker.NewDiscovery(shortTempDomain(t), key)
		if err != nil {
			t.Fatal(err)
		}
		endpoint := newBrokerTestEndpoint()
		endpoint.respondWith("turn/start", `{"turn":{"id":"validation-turn"}}`)
		broker, err := codexbroker.NewBroker(codexbroker.Config{Endpoint: key, Opener: func(context.Context) (codexbroker.Endpoint, error) { return endpoint, nil }})
		if err != nil {
			t.Fatal(err)
		}
		host, err := codexbroker.StartHost(codexbroker.HostConfig{Discovery: discovery, Broker: broker, IdleTimeout: -1})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = host.Close(); _ = broker.Close() })
		routes[ref], servers[ref], hosts[ref] = discovery, endpoint, host
	}
	current := codexNativeEndpointRoute{Endpoint: oldRef, State: coremetadata.CodexGenerationCurrent, TUIExecutable: "/fixture/codex", Default: true}
	controller := defaultCodexNativeThreadController{current: func(context.Context) (codexNativeEndpointRoute, error) { return current, nil }}
	session := newCodexBrokerObserverSessionOn(identity, "", nil, routes[oldRef], nil)
	session.endpoint = oldRef
	session.recoverRoute = controller.Current
	session.routeRuntime = func(route codexNativeEndpointRoute) (codexbroker.Discovery, codexbroker.Launcher, error) {
		return routes[route.Endpoint], nil, nil
	}
	defer session.Close()
	first := openBrokerEpoch(t, session)
	before, err := first.GenerationAuthority()
	if err != nil {
		t.Fatal(err)
	}
	if err := observer.acceptGenerationAuthority(first); err != nil {
		t.Fatal(err)
	}
	current.Endpoint = newRef
	if _, err := controller.Resolve(context.Background(), oldRef); err == nil {
		t.Fatal("stored stale endpoint was blindly resolved to current")
	}
	_ = first.Close()
	next := openBrokerEpoch(t, session)
	after, err := next.GenerationAuthority()
	if err != nil {
		t.Fatal(err)
	}
	if err := observer.acceptGenerationAuthority(next); err != nil {
		t.Fatal(err)
	}
	durable, _ := store.registry.Agent(identity.AgentUID)
	pane, _ := store.registry.Pane(identity.PaneUID)
	if observer.endpoint != newRef || !durable.Status.SessionRef.Codex.Endpoint.Same(newRef) || *pane.Status.Activation.Codex.Authority != after || durable.Status.SessionRef.Codex.ThreadID != identity.ThreadID {
		t.Fatalf("consumer authority drift: endpoint=%+v authority=%+v", durable.Status.SessionRef.Codex.Endpoint, pane.Status.Activation.Codex.Authority)
	}
	writes := store.writes
	if err := observer.acceptGenerationAuthority(first); err == nil {
		t.Fatal("old observer committed after new barrier")
	}
	if store.writes != writes {
		t.Fatal("retired observer changed Registry")
	}
	binding, err := resolveExactAgentControlBinding(store.registry, *durable, agentControlLive{RuntimeID: identity.RuntimeID, PaneUID: identity.PaneUID, ThreadID: identity.ThreadID, Authority: codexAuthorityControlPlane, Epoch: "observer-2", Reason: "ready"}, true, "/state")
	if err != nil || binding.Endpoint != newRef {
		t.Fatalf("exact control consumer=%+v err=%v", binding, err)
	}

	if after.Endpoint() != newRef || after.BrokerRuntimeID != hosts[newRef].RuntimeID() || before.BrokerRuntimeID == after.BrokerRuntimeID || before.ConnectionEpoch != 1 || after.ConnectionEpoch != 1 {
		t.Fatalf("runtime/endpoint axes not exact: before=%+v after=%+v", before, after)
	}
	if _, err := first.GenerationAuthority(); err == nil {
		t.Fatal("old runtime published after replacement epoch 1")
	}
	oldWrites := servers[oldRef].requestCount("turn/start")
	if _, err := first.StartExactTurn(context.Background(), identity.ThreadID, "retired"); err == nil {
		t.Fatal("old control admitted")
	}
	if servers[oldRef].requestCount("turn/start") != oldWrites {
		t.Fatal("old runtime control reached wire")
	}
	if result, err := next.StartExactTurn(context.Background(), identity.ThreadID, "new fixture input"); err != nil || result.TurnID != "validation-turn" {
		t.Fatalf("new exact control=%+v err=%v", result, err)
	}
}

func TestDefaultBrokerEndpointRequiresProbeWireAndRouteAgreement(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	domain, err := defaultCodexStateDomainID(os.Getenv, os.UserHomeDir)
	if err != nil {
		t.Fatal(err)
	}
	exact := codexBrokerEndpointRoute{StateDomainID: domain, EndpointGenerationID: "codex-0.151.0", Default: true}
	for _, test := range []struct {
		name, probe, wire string
		route             codexBrokerEndpointRoute
		want              bool
	}{
		{name: "exact", route: exact, probe: "0.151.0", wire: "0.151.0", want: true},
		{name: "fixed socket upgraded", route: exact, probe: "0.154.0", wire: "0.154.0"},
		{name: "probe to dial race", route: exact, probe: "0.151.0", wire: "0.154.0"},
		{name: "unknown initialize", route: exact, probe: "0.151.0"},
		{name: "unknown probe", route: exact, wire: "0.151.0"},
		{name: "foreign state", route: codexBrokerEndpointRoute{Default: true, StateDomainID: "other", EndpointGenerationID: exact.EndpointGenerationID}, probe: "0.151.0", wire: "0.151.0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := defaultBrokerEndpointMatches(test.route, test.probe, test.wire); got != test.want {
				t.Fatalf("route attestation=%t want=%t", got, test.want)
			}
		})
	}
}

func TestRecoveredGenerationAuthorityRejectsStaleEndpointAndActivation(t *testing.T) {
	for _, kind := range []string{"stored-endpoint", "retired-activation", "foreign-state", "unknown-authority", "draining", "durable-thread"} {
		t.Run(kind, func(t *testing.T) {
			store, identity := phase0RNativeCodexFixture(t)
			agent, _ := store.registry.Agent(identity.AgentUID)
			previous := *agent.Status.SessionRef.Codex.Endpoint
			agent.Status.SessionRef.Codex.Lifecycle = &coremetadata.CodexGenerationLifecycleRef{State: coremetadata.CodexGenerationCurrent}
			presented := coremetadata.CodexAuthorityRef{StateDomainID: previous.StateDomainID, EndpointGenerationID: "codex-0.154.0", BrokerRuntimeID: "new-broker", ConnectionEpoch: 1, BindingEpoch: 1}
			switch kind {
			case "stored-endpoint":
				previous.EndpointGenerationID = "foreign"
			case "retired-activation":
				identity.Generation = "retired"
			case "foreign-state":
				presented.StateDomainID = "foreign"
			case "unknown-authority":
				presented.BrokerRuntimeID = ""
			case "durable-thread":
				agent.Status.SessionRef.Codex.ThreadID = "foreign-thread"
			case "draining":
				agent.Status.SessionRef.Codex.Lifecycle = &coremetadata.CodexGenerationLifecycleRef{State: coremetadata.CodexGenerationDraining, Operation: &coremetadata.CodexGenerationOperationRef{ID: "operation", Endpoint: previous}}
			}
			command := testAICommand(t.TempDir())
			command.loadRegistry = store.store().load
			command.updateRegistry = store.store().update
			command.acquireCodexAuthority = func(string) (func(), error) { return func() {}, nil }
			sink := aiCodexLifecycleSink{command: command, runner: phase3StaticTmuxRunner{output: identity.PaneUID}}
			before := store.writes
			err := sink.RebindGenerationAuthority(identity, previous, presented)
			if !errors.Is(err, errManagedAgentObservationIgnored) || store.writes != before {
				t.Fatalf("unsafe rebind error=%v writes=%d", err, store.writes-before)
			}
		})
	}
}

func TestRecoveredBrokerConcurrentOpenSharesOneCurrentView(t *testing.T) {
	endpoint := newBrokerTestEndpoint()
	discovery, opens := startBrokerRuntimeForTest(t, endpoint)
	session := newCodexBrokerObserverSessionOn(brokerTestIdentity("concurrent-open"), "", nil, discovery, nil)
	defer session.Close()
	epochs := make(chan *codexBrokerLifecycleEpoch, 2)
	for range 2 {
		go func() {
			connection, err := session.Open(context.Background())
			if err != nil {
				epochs <- nil
				return
			}
			epochs <- connection.(*codexBrokerLifecycleEpoch)
		}()
	}
	first, second := <-epochs, <-epochs
	if first == nil || second == nil || first != second || first.binding != second.binding || *opens != 1 {
		t.Fatal("concurrent Open created duplicate binding/connection")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	waitForBrokerStreamEnd(t, first)
}

func TestRecoveredBrokerOpenWaiterHonorsCancellation(t *testing.T) {
	session := newCodexBrokerObserverSessionOn(brokerTestIdentity("cancel-waiter"), "", nil, codexbroker.Discovery{}, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	session.recoverRoute = func(ctx context.Context) (codexNativeEndpointRoute, error) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		select {
		case <-release:
			return codexNativeEndpointRoute{}, context.Canceled
		case <-ctx.Done():
			return codexNativeEndpointRoute{}, ctx.Err()
		}
	}
	firstDone := make(chan error, 1)
	go func() { _, err := session.Open(context.Background()); firstDone <- err }()
	<-entered
	defer func() { close(release); <-firstDone; _ = session.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	secondDone := make(chan error, 1)
	go func() { _, err := session.Open(ctx); secondDone <- err }()
	select {
	case err := <-secondDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter cancellation=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled Open remained behind a pending route")
	}
}

func TestRecoveredBrokerCloseCancelsPendingRouteAndEnsure(t *testing.T) {
	for _, stage := range []string{"route", "ensure"} {
		t.Run(stage, func(t *testing.T) {
			discovery, err := codexbroker.NewDiscovery(shortTempDomain(t), codexbroker.DefaultEndpointKey)
			if err != nil {
				t.Fatal(err)
			}
			entered := make(chan struct{})
			pending := func(ctx context.Context) error { close(entered); <-ctx.Done(); return ctx.Err() }
			session := newCodexBrokerObserverSessionOn(brokerTestIdentity("close-"+stage), "", nil, discovery, nil)
			if stage == "route" {
				session.recoverRoute = func(ctx context.Context) (codexNativeEndpointRoute, error) {
					return codexNativeEndpointRoute{}, pending(ctx)
				}
			} else {
				session.launch = pending
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := session.Open(ctx); done <- err }()
			<-entered
			if err := session.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("closed pending Open succeeded")
				}
			case <-time.After(time.Second):
				cancel()
				<-done
				t.Fatal("Close left pending route/Ensure running")
			}
			session.mu.Lock()
			closed, binding, conn := session.closed, session.binding, session.conn
			session.mu.Unlock()
			if !closed || binding != nil || conn != nil {
				t.Fatal("closed session retained actor resources")
			}
		})
	}
}

func TestRecoveredDefaultBrokerRefusesNewRollingJournalBeforeSameEndpointReopen(t *testing.T) {
	endpoint := newBrokerTestEndpoint()
	discovery, _ := startBrokerRuntimeForTest(t, endpoint)
	session := newCodexBrokerObserverSessionOn(brokerTestIdentity("journal-reopen"), "", nil, discovery, nil)
	defer session.Close()
	route := codexNativeEndpointRoute{Endpoint: coremetadata.CodexEndpointRef{StateDomainID: "journal-domain", EndpointGenerationID: "codex-0.151.0"}, State: coremetadata.CodexGenerationCurrent, Default: true, TUIExecutable: "/fixture/codex"}
	session.endpoint = route.Endpoint
	session.recoveryGuard = func() error { return refuseDefaultRecoveryWithJournal(discovery.Domain()) }
	probes := 0
	session.recoverRoute = func(context.Context) (codexNativeEndpointRoute, error) { probes++; return route, nil }
	first := openBrokerEpoch(t, session)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	journal := codexupgrade.PathFor(discovery.Domain())
	if err := os.MkdirAll(filepath.Dir(journal), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"unreadable-by-default-recovery":"owner-private-operation"}`)
	if err := os.WriteFile(journal, original, 0o600); err != nil {
		t.Fatal(err)
	}
	before := endpoint.requestCount("bootstrap")
	_, err := session.Open(context.Background())
	var refusal *codexNativeRouteError
	if !errors.As(err, &refusal) || refusal.Reason != codexNativeReasonGenerationUnavailable {
		t.Fatalf("journal reopen refusal=%v", err)
	}
	if probes != 1 || endpoint.requestCount("bootstrap") != before || session.endpoint != route.Endpoint {
		t.Fatal("new journal was bypassed or rerouted")
	}
	after, err := os.ReadFile(journal)
	if err != nil || string(after) != string(original) {
		t.Fatal("default recovery wrote the journal")
	}
	if _, err := os.Lstat(journal + ".flock"); !os.IsNotExist(err) {
		t.Fatal("read-only guard acquired journal mutation artifacts")
	}
}
