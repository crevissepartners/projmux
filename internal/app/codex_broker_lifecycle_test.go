package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
)

// brokerTestEndpoint is one in-memory app-server connection. It records every
// request that reaches the wire, so "zero writes" is an observation rather than
// an assumption.
type brokerTestEndpoint struct {
	events chan codexappserver.Notification

	mu        sync.Mutex
	requests  []string
	answers   []string
	responses map[string]string
	closed    bool
}

func newBrokerTestEndpoint() *brokerTestEndpoint {
	return &brokerTestEndpoint{
		events:    make(chan codexappserver.Notification, 64),
		responses: map[string]string{},
	}
}

func (e *brokerTestEndpoint) Notifications() <-chan codexappserver.Notification { return e.events }

func (e *brokerTestEndpoint) Request(_ context.Context, method string, _, result any) error {
	e.mu.Lock()
	e.requests = append(e.requests, method)
	payload := e.responses[method]
	e.mu.Unlock()
	if payload == "" || result == nil {
		return nil
	}
	return json.Unmarshal([]byte(payload), result)
}

func (e *brokerTestEndpoint) RespondServerRequest(_ context.Context, rawID json.RawMessage, _ any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.answers = append(e.answers, string(rawID))
	return nil
}

func (e *brokerTestEndpoint) BootstrapThread(_ context.Context, threadID, _ string, _ []string) (codexappserver.ThreadSnapshot, error) {
	e.mu.Lock()
	e.requests = append(e.requests, "bootstrap")
	e.mu.Unlock()
	return codexappserver.ThreadSnapshot{ThreadID: threadID, RuntimeStatus: "idle"}, nil
}

func (e *brokerTestEndpoint) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	e.mu.Unlock()
	close(e.events)
	return nil
}

func (e *brokerTestEndpoint) respondWith(method, payload string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.responses[method] = payload
}

func (e *brokerTestEndpoint) emit(notification codexappserver.Notification) {
	defer func() { _ = recover() }()
	e.events <- notification
}

func (e *brokerTestEndpoint) requestCount(method string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	count := 0
	for _, seen := range e.requests {
		if seen == method {
			count++
		}
	}
	return count
}

func (e *brokerTestEndpoint) answerLedger() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.answers...)
}

// startBrokerRuntimeForTest publishes one real runtime host over an in-memory
// endpoint sequence and returns the discovery contract clients reach it by.
func startBrokerRuntimeForTest(t *testing.T, endpoints ...*brokerTestEndpoint) (codexbroker.Discovery, *int) {
	t.Helper()
	discovery, _, opens := startBrokerRuntimeWithHost(t, endpoints...)
	return discovery, opens
}

// startBrokerRuntimeHostForTest additionally hands back the host, so a test can
// take the runtime away from a bound client the way a crash would.
func startBrokerRuntimeHostForTest(t *testing.T, endpoints ...*brokerTestEndpoint) (codexbroker.Discovery, *codexbroker.Host) {
	t.Helper()
	discovery, host, _ := startBrokerRuntimeWithHost(t, endpoints...)
	return discovery, host
}

func startBrokerRuntimeWithHost(t *testing.T, endpoints ...*brokerTestEndpoint) (codexbroker.Discovery, *codexbroker.Host, *int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("codex broker runtime requires Unix filesystem semantics")
	}
	discovery, err := codexbroker.NewDiscovery(shortTempDomain(t), codexbroker.DefaultEndpointKey)
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}
	opens := 0
	var mu sync.Mutex
	broker, err := codexbroker.NewBroker(codexbroker.Config{
		Opener: func(context.Context) (codexbroker.Endpoint, error) {
			mu.Lock()
			defer mu.Unlock()
			if opens >= len(endpoints) {
				return nil, errors.New("no endpoint left in the fixture sequence")
			}
			endpoint := endpoints[opens]
			opens++
			return endpoint, nil
		},
	})
	if err != nil {
		t.Fatalf("broker: %v", err)
	}
	host, err := codexbroker.StartHost(codexbroker.HostConfig{Discovery: discovery, Broker: broker, IdleTimeout: -1})
	if err != nil {
		_ = broker.Close()
		t.Fatalf("host: %v", err)
	}
	t.Cleanup(func() {
		_ = host.Close()
		_ = broker.Close()
	})
	return discovery, host, &opens
}

// shortTempDomain returns a private state domain short enough that the derived
// Unix socket path stays inside the platform bound the discovery contract
// refuses beyond.
func shortTempDomain(t *testing.T) string {
	t.Helper()
	base := os.TempDir()
	if info, err := os.Stat("/tmp"); err == nil && info.IsDir() {
		base = "/tmp"
	}
	root, err := os.MkdirTemp(base, "pmxb")
	if err != nil {
		t.Fatalf("create state domain: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

func brokerTestIdentity(thread string) codexLifecycleIdentity {
	return codexLifecycleIdentity{
		AgentUID: "agent-" + thread, PaneUID: "pane-" + thread, RuntimeID: "%1",
		Generation: "gen-" + thread, ThreadID: thread,
	}
}

func openBrokerEpoch(t *testing.T, session *codexBrokerObserverSession) *codexBrokerLifecycleEpoch {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	connection, err := session.Open(ctx)
	if err != nil {
		t.Fatalf("open broker epoch: %v", err)
	}
	epoch, ok := connection.(*codexBrokerLifecycleEpoch)
	if !ok {
		t.Fatalf("open returned %T, want a broker epoch", connection)
	}
	return epoch
}

// TestBrokerBoundAgentsShareOneUpstreamConnectionWithIndependentBindingEpochs
// is the cutover's central claim: two exact Agents on the same endpoint - one
// from a prompted create, one from a stored resume - now hold one upstream
// connection between them and two independent binding epochs, instead of one
// app-server proxy each.
func TestBrokerBoundAgentsShareOneUpstreamConnectionWithIndependentBindingEpochs(t *testing.T) {
	endpoint := newBrokerTestEndpoint()
	discovery, opens := startBrokerRuntimeForTest(t, endpoint)

	created := newCodexBrokerObserverSessionOn(brokerTestIdentity("thread-created"), "", nil, discovery, nil)
	defer created.Close()
	resumed := newCodexBrokerObserverSessionOn(brokerTestIdentity("thread-resumed"), "", nil, discovery, nil)
	defer resumed.Close()

	first := openBrokerEpoch(t, created)
	second := openBrokerEpoch(t, resumed)

	if *opens != 1 {
		t.Fatalf("upstream connections = %d, want exactly one shared connection", *opens)
	}
	if first.fence.Connection != second.fence.Connection {
		t.Fatalf("connection epochs = %d and %d, want one shared connection epoch",
			first.fence.Connection, second.fence.Connection)
	}
	if first.fence.Binding == second.fence.Binding {
		t.Fatalf("binding epochs both = %d, want independent bindings", first.fence.Binding)
	}
	if first.snapshot.ThreadID != "thread-created" || second.snapshot.ThreadID != "thread-resumed" {
		t.Fatalf("snapshots crossed threads: %q and %q", first.snapshot.ThreadID, second.snapshot.ThreadID)
	}

	// Delivery stays exact under interleaving: each thread's event reaches only
	// the binding that named it.
	endpoint.emit(codexappserver.Notification{Method: "thread/status/changed", Params: json.RawMessage(`{"threadId":"thread-resumed","status":{"type":"active"}}`)})
	endpoint.emit(codexappserver.Notification{Method: "thread/status/changed", Params: json.RawMessage(`{"threadId":"thread-created","status":{"type":"active"}}`)})

	if got := waitForBrokerNotification(t, second); !strings.Contains(string(got.Params), "thread-resumed") {
		t.Fatalf("resumed binding received %s", got.Params)
	}
	if got := waitForBrokerNotification(t, first); !strings.Contains(string(got.Params), "thread-created") {
		t.Fatalf("created binding received %s", got.Params)
	}
}

// TestBrokerThreadlessAndForeignEventsReachNoExactAgentBinding closes the
// ambiguous-attribution row: a notification that declares no thread, and one
// that declares a thread nobody bound, must not be handed to any Agent. Same
// working directory is not identity, and neither is being the only binding.
func TestBrokerThreadlessAndForeignEventsReachNoExactAgentBinding(t *testing.T) {
	endpoint := newBrokerTestEndpoint()
	discovery, _ := startBrokerRuntimeForTest(t, endpoint)

	session := newCodexBrokerObserverSessionOn(brokerTestIdentity("thread-bound"), "/same/cwd", nil, discovery, nil)
	defer session.Close()
	epoch := openBrokerEpoch(t, session)

	for _, params := range []string{
		`{"status":{"type":"active"}}`,
		`{"threadId":"","status":{"type":"active"}}`,
		`{"threadId":"thread-foreign","status":{"type":"active"}}`,
		`{"threadId":"thread-foreign","cwd":"/same/cwd","status":{"type":"active"}}`,
	} {
		endpoint.emit(codexappserver.Notification{Method: "thread/status/changed", Params: json.RawMessage(params)})
	}
	endpoint.emit(codexappserver.Notification{Method: "thread/status/changed", Params: json.RawMessage(`{"threadId":"thread-bound","status":{"type":"idle"}}`)})

	got := waitForBrokerNotification(t, epoch)
	if !strings.Contains(string(got.Params), "thread-bound") {
		t.Fatalf("first delivered event = %s, want only the exact bound thread", got.Params)
	}
	select {
	case extra, open := <-epoch.Notifications():
		if open {
			t.Fatalf("unattributed event reached the exact binding: %s", extra.Params)
		}
		t.Fatal("binding stream closed while it was expected to stay live")
	case <-time.After(150 * time.Millisecond):
	}
}

// TestBrokerEpochRotationFencesTheOldEpochOutWithZeroEndpointWrites replaces
// the connection under a live binding. The retired epoch must keep its stream
// closed, refuse every mutation before the wire, and answer nothing; the
// replacement epoch must open only behind its own snapshot barrier.
func TestBrokerEpochRotationFencesTheOldEpochOutWithZeroEndpointWrites(t *testing.T) {
	first, second := newBrokerTestEndpoint(), newBrokerTestEndpoint()
	discovery, _ := startBrokerRuntimeForTest(t, first, second)

	session := newCodexBrokerObserverSessionOn(brokerTestIdentity("thread-rotate"), "", nil, discovery, nil)
	defer session.Close()
	stale := openBrokerEpoch(t, session)

	// Losing the endpoint is a disconnect the broker owns: the binding survives
	// it and the supervisor reconnects, so the observer above sees one closed
	// stream rather than an exhausted retry budget.
	_ = first.Close()
	waitForBrokerStreamEnd(t, stale)

	fresh := openBrokerEpoch(t, session)
	if fresh.fence.Connection == stale.fence.Connection {
		t.Fatalf("replacement kept the retired connection epoch %d", stale.fence.Connection)
	}
	if fresh.fence.Binding != stale.fence.Binding {
		t.Fatalf("binding epoch changed across a reconnect: %d -> %d", stale.fence.Binding, fresh.fence.Binding)
	}

	before := second.requestCount("turn/steer")
	if _, err := stale.SteerExactTurn(context.Background(), "thread-rotate", "turn-1", "late"); err == nil {
		t.Fatal("retired epoch steered the replacement connection")
	}
	if err := stale.RespondServerRequest(context.Background(), json.RawMessage(`7`), struct{}{}); err == nil {
		t.Fatal("retired epoch answered a server request")
	}
	if got := second.requestCount("turn/steer"); got != before {
		t.Fatalf("retired epoch wrote %d steer requests to the replacement connection", got-before)
	}
	if answers := second.answerLedger(); len(answers) != 0 {
		t.Fatalf("retired epoch answered %v on the replacement connection", answers)
	}

	second.respondWith("turn/start", `{"turn":{"id":"turn-2"}}`)
	if result, err := fresh.StartExactTurn(context.Background(), "thread-rotate", "go"); err != nil || result.TurnID != "turn-2" {
		t.Fatalf("replacement epoch start = %+v, %v", result, err)
	}
}

// TestBrokerApprovalLeaseAnswersTheExactRawRequestExactlyOnce pins the approval
// half of C-4 end to end: the inbound server request arrives as a notification
// carrying its byte-faithful raw id, the answer is admitted once, and every
// repeat - including one from a caller that kept the raw id - is refused with
// no second write.
func TestBrokerApprovalLeaseAnswersTheExactRawRequestExactlyOnce(t *testing.T) {
	endpoint := newBrokerTestEndpoint()
	discovery, _ := startBrokerRuntimeForTest(t, endpoint)

	session := newCodexBrokerObserverSessionOn(brokerTestIdentity("thread-approve"), "", nil, discovery, nil)
	defer session.Close()
	epoch := openBrokerEpoch(t, session)

	raw := json.RawMessage(`"req-9"`)
	endpoint.emit(codexappserver.Notification{
		Method:       "item/commandExecution/requestApproval",
		Params:       json.RawMessage(`{"threadId":"thread-approve","turnId":"turn-1","itemId":"item-1","startedAtMs":1,"command":"ls","cwd":"/work"}`),
		RequestID:    "req-9",
		RawRequestID: raw,
	})

	got := waitForBrokerNotification(t, epoch)
	if string(got.RawRequestID) != string(raw) {
		t.Fatalf("raw request id = %s, want %s", got.RawRequestID, raw)
	}
	if got.RequestID != "req-9" {
		t.Fatalf("normalized request id = %q, want req-9", got.RequestID)
	}
	envelope, recognized, err := codexappserver.DecodeApprovalEnvelope(got)
	if !recognized || err != nil {
		t.Fatalf("decode approval envelope: recognized=%v err=%v", recognized, err)
	}
	response, err := codexappserver.ApprovalResponse(envelope, codexappserver.DecisionDecline)
	if err != nil {
		t.Fatalf("approval response: %v", err)
	}
	if err := epoch.RespondServerRequest(context.Background(), envelope.RawRequestID, response); err != nil {
		t.Fatalf("first answer: %v", err)
	}
	if err := epoch.RespondServerRequest(context.Background(), envelope.RawRequestID, response); err == nil {
		t.Fatal("second answer to one inbound request was admitted")
	}
	if answers := endpoint.answerLedger(); len(answers) != 1 || answers[0] != string(raw) {
		t.Fatalf("answer ledger = %v, want exactly one answer for %s", answers, raw)
	}
}

// TestBrokerEpochReadsTheExactLifecycleSnapshotThroughItsOwnFence proves the
// control epoch's state input goes through the same fence its mutations do, so
// a retired connection cannot converge state it no longer owns.
func TestBrokerEpochReadsTheExactLifecycleSnapshotThroughItsOwnFence(t *testing.T) {
	endpoint := newBrokerTestEndpoint()
	endpoint.respondWith("thread/read", `{"thread":{"id":"thread-read","status":{"type":"active"},"turns":[{"id":"turn-7","status":"inProgress","startedAt":1}]}}`)
	discovery, _ := startBrokerRuntimeForTest(t, endpoint)

	session := newCodexBrokerObserverSessionOn(brokerTestIdentity("thread-read"), "", nil, discovery, nil)
	defer session.Close()
	epoch := openBrokerEpoch(t, session)

	snapshot, err := epoch.ReadLifecycleSnapshot(context.Background(), "thread-read")
	if err != nil {
		t.Fatalf("read lifecycle snapshot: %v", err)
	}
	if snapshot.ThreadID != "thread-read" || snapshot.TurnID != "turn-7" ||
		snapshot.TurnState != codexappserver.TurnStateInProgress || snapshot.ThreadState != codexappserver.ThreadStateActive {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if !epoch.LifecycleEventsAvailable() {
		t.Fatal("a bound epoch reported lifecycle events unavailable")
	}
}

// TestNativeLifecycleProducerIsExactlyOnePerActivationGeneration audits the
// single-producer rule. The decision is total, it never returns both, and the
// broker producer disables the legacy attempt ceiling rather than layering a
// second, smaller retry budget on top of the broker's own persistent reconnect.
func TestNativeLifecycleProducerIsExactlyOnePerActivationGeneration(t *testing.T) {
	if got := codexNativeLifecycleProducerFor(true); got != codexNativeProducerBroker {
		t.Fatalf("supported platform producer = %q", got)
	}
	if got := codexNativeLifecycleProducerFor(false); got != codexNativeProducerLegacyObserver {
		t.Fatalf("unsupported platform producer = %q", got)
	}
	if codexNativeProducerBroker == codexNativeProducerLegacyObserver {
		t.Fatal("the two producers are the same token")
	}
	if codexObserverUnboundedReconnect >= 0 {
		t.Fatalf("unbounded sentinel %d is a real attempt ceiling", codexObserverUnboundedReconnect)
	}
}

// TestBrokerProducerKeepsRecoveringPastTheLegacyAttemptCeiling holds the
// contract the fixed six-attempt budget violated: while the exact binding is
// still current, a producer that owns persistent reconnect keeps recovering,
// and it never publishes the terminal exhaustion fallback. The legacy budget is
// left byte-identical for the producer that still needs it.
func TestBrokerProducerKeepsRecoveringPastTheLegacyAttemptCeiling(t *testing.T) {
	sink := newRecordingCodexLifecycleSink()
	identity := brokerTestIdentity("thread-recover")
	var attempts atomic.Int64
	observer := codexNativeObserver{
		identity:    identity,
		sink:        sink,
		maxAttempts: codexObserverUnboundedReconnect,
		delay:       time.Millisecond,
		maxDelay:    time.Millisecond,
		open: func(context.Context) (codexLifecycleConnection, error) {
			attempts.Add(1)
			return nil, errors.New("endpoint is still being served")
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- observer.Run(ctx) }()

	want := int64(codexObserverReconnectMaxAttempts) * 3
	deadline := time.After(10 * time.Second)
	for attempts.Load() < want {
		select {
		case err := <-done:
			cancel()
			t.Fatalf("observer stopped after %d attempts: %v", attempts.Load(), err)
		case <-deadline:
			cancel()
			t.Fatalf("observer reached only %d attempts", attempts.Load())
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done
	for _, reason := range sink.authoritySnapshot() {
		if strings.Contains(reason, codexObserverExhaustedReason) {
			t.Fatalf("a persistent-reconnect producer published %q", codexObserverExhaustedReason)
		}
	}
}

func waitForBrokerNotification(t *testing.T, epoch *codexBrokerLifecycleEpoch) codexappserver.Notification {
	t.Helper()
	select {
	case notification, open := <-epoch.Notifications():
		if !open {
			t.Fatal("broker epoch stream closed before the expected event")
		}
		return notification
	case <-time.After(5 * time.Second):
		t.Fatal("broker epoch delivered no event")
		return codexappserver.Notification{}
	}
}

func waitForBrokerStreamEnd(t *testing.T, epoch *codexBrokerLifecycleEpoch) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case _, open := <-epoch.Notifications():
			if !open {
				return
			}
		case <-deadline:
			t.Fatal("broker epoch stream never ended")
		}
	}
}

// TestBrokerRuntimeLossEndsTheEpochAndRefusesEveryLateMutation is the
// fail-closed boundary at the top of the local IPC: when the runtime that
// granted a fence is gone, the epoch's stream ends before any caller can
// present that fence, and every late mutation and approval answer is refused.
func TestBrokerRuntimeLossEndsTheEpochAndRefusesEveryLateMutation(t *testing.T) {
	endpoint := newBrokerTestEndpoint()
	discovery, host := startBrokerRuntimeHostForTest(t, endpoint)

	session := newCodexBrokerObserverSessionOn(brokerTestIdentity("thread-crash"), "", nil, discovery, nil)
	defer session.Close()
	epoch := openBrokerEpoch(t, session)

	_ = host.Close()
	waitForBrokerStreamEnd(t, epoch)

	if _, err := epoch.StartExactTurn(context.Background(), "thread-crash", "late"); err == nil {
		t.Fatal("a turn started after the runtime was gone")
	}
	if err := epoch.RespondServerRequest(context.Background(), json.RawMessage(`1`), struct{}{}); err == nil {
		t.Fatal("an approval was answered after the runtime was gone")
	}
	if got := endpoint.requestCount("turn/start"); got != 0 {
		t.Fatalf("turn/start reached the endpoint %d times after the runtime was gone", got)
	}
	if answers := endpoint.answerLedger(); len(answers) != 0 {
		t.Fatalf("answers after the runtime was gone: %v", answers)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := session.Open(ctx); err == nil {
		t.Fatal("a session reopened against a runtime that is not published")
	}
}

// TestBrokerEpochBacklogOverflowEndsOnlyThatEpochStream keeps a stalled
// consumer's blast radius at itself. Its stream is closed so it must resync
// from a fresh snapshot rather than consuming an order with a hole in it, and
// the sibling Agent on the same shared connection keeps receiving its own
// events throughout.
func TestBrokerEpochBacklogOverflowEndsOnlyThatEpochStream(t *testing.T) {
	endpoint := newBrokerTestEndpoint()
	discovery, _ := startBrokerRuntimeForTest(t, endpoint)

	stalled := newCodexBrokerObserverSessionOn(brokerTestIdentity("thread-stalled"), "", nil, discovery, nil)
	defer stalled.Close()
	healthy := newCodexBrokerObserverSessionOn(brokerTestIdentity("thread-healthy"), "", nil, discovery, nil)
	defer healthy.Close()

	stalledEpoch := openBrokerEpoch(t, stalled)
	healthyEpoch := openBrokerEpoch(t, healthy)

	for index := range codexBrokerObserverBacklog * 16 {
		endpoint.emit(codexappserver.Notification{
			Method: "thread/status/changed",
			Params: json.RawMessage(fmt.Sprintf(`{"threadId":"thread-stalled","status":{"type":"active"},"seq":%d}`, index)),
		})
	}
	waitForBrokerStreamEnd(t, stalledEpoch)

	endpoint.emit(codexappserver.Notification{
		Method: "thread/status/changed",
		Params: json.RawMessage(`{"threadId":"thread-healthy","status":{"type":"active"}}`),
	})
	if got := waitForBrokerNotification(t, healthyEpoch); !strings.Contains(string(got.Params), "thread-healthy") {
		t.Fatalf("sibling binding received %s after the overflow", got.Params)
	}
}

// TestBrokerCutoverPreservesTheHookFallbackVocabulary is the compatibility
// audit. The public authority sources, the reasons that suppress hooks, and
// the convergence to provider-hook when native preparation fails before any
// provider mutation are all untouched by moving the producer to the broker, so
// nothing outside this package can tell which producer served a generation.
func TestBrokerCutoverPreservesTheHookFallbackVocabulary(t *testing.T) {
	for source, suppresses := range map[string]bool{
		codexAuthorityPending:      true,
		codexAuthorityControlPlane: true,
		codexAuthorityInvalidating: true,
		codexAuthorityHook:         false,
		"":                         false,
		"anything-else":            false,
	} {
		if got := codexAuthoritySuppressesHooks(source); got != suppresses {
			t.Fatalf("codexAuthoritySuppressesHooks(%q) = %v, want %v", source, got, suppresses)
		}
	}

	sink := newRecordingCodexLifecycleSink()
	identity := brokerTestIdentity("thread-fallback")
	result := convergeCodexObserverStartupFallback(sink, identity, "unavailable")
	if result.Status != codexObserverStartupFallback || result.Reason != "unavailable" {
		t.Fatalf("startup fallback = %+v", result)
	}
	authority := sink.authoritySnapshot()
	if len(authority) == 0 || !strings.Contains(authority[len(authority)-1], codexAuthorityHook) {
		t.Fatalf("pre-mutation failure did not converge to hook fallback: %v", authority)
	}
}

// TestBrokerDisconnectEndsTheEpochBeforeAnyReplacementBarrier is the promptness
// half of the disconnect contract. A binding survives an outage so the broker
// can keep reconnecting, which means the ordered stream has nothing to say
// until the next barrier closes - possibly a long outage away. The binding's
// out-of-band suspension is what retires the live epoch immediately, so the
// projection above falls back instead of holding an authority the runtime has
// already closed, and no stale barrier is ever re-served in its place.
func TestBrokerDisconnectEndsTheEpochBeforeAnyReplacementBarrier(t *testing.T) {
	endpoint := newBrokerTestEndpoint()
	discovery, _ := startBrokerRuntimeForTest(t, endpoint)

	session := newCodexBrokerObserverSessionOn(brokerTestIdentity("thread-suspend"), "", nil, discovery, nil)
	defer session.Close()
	epoch := openBrokerEpoch(t, session)

	// The fixture has no replacement endpoint, so every reconnect the
	// supervisor attempts from here refuses and no replacement barrier can
	// close.
	_ = endpoint.Close()
	waitForBrokerStreamEnd(t, epoch)

	if _, err := epoch.StartExactTurn(context.Background(), "thread-suspend", "late"); err == nil {
		t.Fatal("a retired epoch started a turn during the outage")
	}
	if got := endpoint.requestCount("turn/start"); got != 0 {
		t.Fatalf("turn/start reached the endpoint %d times during the outage", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	if _, err := session.Open(ctx); err == nil {
		t.Fatal("a session re-served the barrier of a connection that is gone")
	}
}
