package app

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
)

type recoveryAdmissionClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []recoveryAdmissionTimer
}

type recoveryAdmissionTimer struct {
	at    time.Time
	ready chan time.Time
}

func (clock *recoveryAdmissionClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *recoveryAdmissionClock) After(delay time.Duration) <-chan time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	ready := make(chan time.Time, 1)
	clock.timers = append(clock.timers, recoveryAdmissionTimer{clock.now.Add(delay), ready})
	return ready
}

func (clock *recoveryAdmissionClock) advance(delay time.Duration) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = clock.now.Add(delay)
	var pending []recoveryAdmissionTimer
	for _, timer := range clock.timers {
		if timer.at.After(clock.now) {
			pending = append(pending, timer)
		} else {
			timer.ready <- clock.now
		}
	}
	clock.timers = pending
}

// Recording the returned typed refusal does not replace the real session,
// owned IPC read, broker admission or control handler.
type recordingAdmissionEpoch struct {
	*codexBrokerLifecycleEpoch
	mu      sync.Mutex
	refusal codexbroker.Refusal
}

func (epoch *recordingAdmissionEpoch) ReadLifecycleSnapshot(ctx context.Context, thread string) (codexappserver.LifecycleSnapshot, error) {
	snapshot, err := epoch.codexBrokerLifecycleEpoch.ReadLifecycleSnapshot(ctx, thread)
	epoch.mu.Lock()
	epoch.refusal = codexbroker.RefusalOf(err)
	epoch.mu.Unlock()
	return snapshot, err
}

func (epoch *recordingAdmissionEpoch) lastRefusal() codexbroker.Refusal {
	epoch.mu.Lock()
	defer epoch.mu.Unlock()
	return epoch.refusal
}

func TestObserverReadyControlStartPreservesOwnedReadAdmissionAndFreshness(t *testing.T) {
	identity := brokerTestIdentity("thread-admission")
	ref := coremetadata.CodexEndpointRef{StateDomainID: "admission-state", EndpointGenerationID: "codex-0.154.0"}
	key, err := codexbroker.NewEndpointKey(ref.StateDomainID, ref.EndpointGenerationID)
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := codexbroker.NewDiscovery(shortTempDomain(t), key)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := newBrokerTestEndpoint()
	idle := `{"thread":{"id":"thread-admission","status":{"type":"idle"},"turns":[{"id":"old-turn","status":"completed"}]}}`
	endpoint.respondWith("thread/read", idle)
	endpoint.respondWith("turn/start", `{"turn":{"id":"new-turn"}}`)
	clock := &recoveryAdmissionClock{now: time.Unix(1700000000, 0)}
	var owned atomic.Int64
	broker, err := codexbroker.NewBroker(codexbroker.Config{
		Endpoint: key, Clock: clock,
		Opener: func(context.Context) (codexbroker.Endpoint, error) { return endpoint, nil },
		Lifecycle: func(_ context.Context, expected codexappserver.PeerIdentity) (codexappserver.LifecycleEndpoint, error) {
			if !codexappserver.SamePeerIdentity(expected, endpoint.peer) {
				return nil, codexappserver.ErrEndpointChanged
			}
			owned.Add(1)
			return &brokerTestLifecycleEndpoint{shared: endpoint, peer: endpoint.peer}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	host, err := codexbroker.StartHost(codexbroker.HostConfig{Discovery: discovery, Broker: broker, IdleTimeout: -1})
	if err != nil {
		_ = broker.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close(); _ = broker.Close() })
	session := newCodexBrokerObserverSessionOn(identity, "", nil, discovery, nil)
	session.endpoint = ref
	t.Cleanup(func() { _ = session.Close() })
	var wire *recordingAdmissionEpoch
	var control *codexControlEpoch
	startup := make(chan codexObserverStartupResult, 2)
	sink := newRecordingCodexLifecycleSink()
	observer := codexNativeObserver{
		identity: identity, sink: sink, requireControl: true,
		open: func(ctx context.Context) (codexLifecycleConnection, error) {
			connection, err := session.Open(ctx)
			if err != nil {
				return nil, err
			}
			wire = &recordingAdmissionEpoch{codexBrokerLifecycleEpoch: connection.(*codexBrokerLifecycleEpoch)}
			return wire, nil
		},
		startControl: func(epoch *codexControlEpoch) (*codexControlServer, error) {
			control = epoch
			return &codexControlServer{epoch: epoch}, nil
		},
		reportStartup: func(result codexObserverStartupResult) { startup <- result },
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- observer.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("observer did not stop")
		}
	})
	ready := waitForCodexObserverStartupResult(t, startup, codexObserverStartupReady)
	authority, err := wire.GenerationAuthority()
	if err != nil || authority.Endpoint() != ref || authority.BrokerRuntimeID != host.RuntimeID() || owned.Load() != 1 {
		t.Fatalf("ready without current authority and one owned startup read: authority=%+v owned=%d err=%v", authority, owned.Load(), err)
	}
	request := agentControlRequest{Identity: identity, Epoch: ready.Epoch, Operation: agentControlOpStatus}
	status := control.Handle(ctx, request)
	if !status.OK || !status.Availability.Start {
		t.Fatalf("status=%+v", status)
	}
	var admission installedRecoveryAdmission
	observation := installedRecoveryObservation{
		Stage: "ready", Identity: installedRecoveryAgent{Agent: identity.AgentUID, Pane: identity.PaneUID, Thread: identity.ThreadID, Authority: authority, ControlEpoch: ready.Epoch},
		Control: &installedRecoveryControl{OK: status.OK, Availability: status.Availability},
	}
	if result := admission.observe(clock.Now(), observation); result.Stage != "waiting-read-admission" || owned.Load() != 1 || endpoint.requestCount("turn/start") != 0 {
		t.Fatal("fixture admission wait issued a read or input")
	}
	request.Operation = agentControlOpStart
	request.Text = "fresh synthetic immediate input"
	if result := control.Handle(ctx, request); result.Code != "turn-state-unavailable" || wire.lastRefusal() != codexbroker.RefusalLifecycleRetry || owned.Load() != 1 || endpoint.requestCount("turn/start") != 0 {
		t.Fatalf("immediate start did not reproduce read admission: result=%+v refusal=%s owned=%d", result, wire.lastRefusal(), owned.Load())
	}
	t.Log("current authority + status Start=true -> lifecycle-retry -> turn-state-unavailable; owned opens=1, turn/start writes=0")
	clock.advance(time.Second - time.Nanosecond)
	if result := admission.observe(clock.Now(), observation); result.Stage != "waiting-read-admission" {
		t.Fatal("fixture admitted before the broker boundary")
	}
	request.Text = "distinct synthetic input before boundary"
	if result := control.Handle(ctx, request); result.Code != "turn-state-unavailable" || wire.lastRefusal() != codexbroker.RefusalLifecycleRetry || owned.Load() != 1 {
		t.Fatalf("retry fence shortened: %+v", result)
	}
	// The next admitted read must observe new state, not reuse readiness.
	endpoint.respondWith("thread/read", `{"thread":{"id":"thread-admission","status":{"type":"active"},"turns":[{"id":"other-turn","status":"inProgress"}]}}`)
	clock.advance(time.Nanosecond)
	if result := admission.observe(clock.Now(), observation); result.Stage != "ready" || owned.Load() != 1 || endpoint.requestCount("turn/start") != 0 {
		t.Fatal("fixture wait changed authority or submitted work")
	}
	request.Text = "distinct synthetic input at boundary"
	if result := control.Handle(ctx, request); result.Code != "turn-in-progress" || wire.lastRefusal() != codexbroker.RefusalNone || owned.Load() != 2 || endpoint.requestCount("turn/start") != 0 {
		t.Fatalf("fresh read bypassed: %+v owned=%d", result, owned.Load())
	}
	clock.advance(time.Second)
	endpoint.respondWith("thread/read", idle)
	request.Text = "distinct synthetic admitted input"
	if result := control.Handle(ctx, request); !result.OK || result.TurnID != "new-turn" || owned.Load() != 3 || endpoint.requestCount("turn/start") != 1 {
		t.Fatalf("admitted exact start=%+v owned=%d", result, owned.Load())
	}
	control.Revoke()
	clock.advance(time.Second)
	request.Text = "distinct synthetic retired input"
	if result := control.Handle(ctx, request); result.Code != "stale-epoch" || owned.Load() != 3 || endpoint.requestCount("turn/start") != 1 {
		t.Fatalf("retired authority wrote: %+v", result)
	}
}
