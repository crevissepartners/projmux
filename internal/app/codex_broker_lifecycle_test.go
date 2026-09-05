package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
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

func TestGenerationRoutedBrokerEpochPublishesItsExactRuntimeAuthority(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("codex broker runtime requires Unix filesystem semantics")
	}
	endpointRef := coremetadata.CodexEndpointRef{StateDomainID: "domain-exact", EndpointGenerationID: "generation-exact"}
	key, err := codexbroker.NewEndpointKey(endpointRef.StateDomainID, endpointRef.EndpointGenerationID)
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := codexbroker.NewDiscovery(shortTempDomain(t), key)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := newBrokerTestEndpoint()
	broker, err := codexbroker.NewBroker(codexbroker.Config{Endpoint: key, Opener: func(context.Context) (codexbroker.Endpoint, error) {
		return endpoint, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	host, err := codexbroker.StartHost(codexbroker.HostConfig{Discovery: discovery, Broker: broker, IdleTimeout: -1})
	if err != nil {
		_ = broker.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = host.Close()
		_ = broker.Close()
	})
	session := newCodexBrokerObserverSessionOn(brokerTestIdentity("thread-generation"), "", nil, discovery, nil)
	session.endpoint = endpointRef
	defer session.Close()
	epoch := openBrokerEpoch(t, session)
	authority, err := epoch.GenerationAuthority()
	if err != nil {
		t.Fatal(err)
	}
	if !authority.Endpoint().Same(endpointRef) || authority.BrokerRuntimeID != host.RuntimeID() ||
		authority.ConnectionEpoch == 0 || authority.BindingEpoch == 0 {
		t.Fatalf("generation authority = %+v, runtime=%q", authority, host.RuntimeID())
	}
	if strings.Contains(authority.BrokerRuntimeID, "observer-") {
		t.Fatalf("observer-local identity leaked into broker authority: %+v", authority)
	}
}

func TestBrokerObserverConcurrentCloseAndPublishKeepsSessionClosed(t *testing.T) {
	endpoint := newBrokerTestEndpoint()
	discovery, _ := startBrokerRuntimeForTest(t, endpoint)
	session := newCodexBrokerObserverSessionOn(brokerTestIdentity("thread-close-publish"), "", nil, discovery, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, ready, err := session.ensure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var record codexBrokerEpochRecord
	select {
	case record = <-ready:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	start := make(chan struct{})
	var (
		epoch      *codexBrokerLifecycleEpoch
		publishErr error
		closeErr   error
		group      sync.WaitGroup
	)
	group.Add(2)
	go func() {
		defer group.Done()
		<-start
		epoch, publishErr = session.publish(record)
	}()
	go func() {
		defer group.Done()
		<-start
		closeErr = session.Close()
	}()
	close(start)
	group.Wait()
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if publishErr == nil {
		waitForBrokerStreamEnd(t, epoch)
	}
	session.mu.Lock()
	closed, current, binding, conn := session.closed, session.current, session.binding, session.conn
	session.mu.Unlock()
	if !closed || current != nil || binding != nil || conn != nil {
		t.Fatalf("concurrent close/publish resurrected the session: closed=%t current=%p binding=%p conn=%p publishErr=%v",
			closed, current, binding, conn, publishErr)
	}
	if _, err := session.Open(ctx); err == nil {
		t.Fatal("closed session reopened after concurrent publish")
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

// TestBrokerObserverReplacementSnapshotConsumesPriorSuspensionEdge pins the
// second half of reconnect ordering. Even when select receives the buffered
// snapshot first, the observer consumes its causally prior suspension before
// rotating, so that edge cannot later retire the replacement epoch.
func TestBrokerObserverReplacementSnapshotConsumesPriorSuspensionEdge(t *testing.T) {
	ready := make(chan codexBrokerEpochRecord, 1)
	suspends := make(chan struct{}, 1)
	suspends <- struct{}{}
	old := &codexBrokerLifecycleEpoch{notifications: make(chan codexappserver.Notification)}
	session := &codexBrokerObserverSession{current: old}
	replacement := codexBrokerEpochRecord{
		fence:    codexbroker.Fence{Connection: 2, Binding: 1},
		snapshot: codexappserver.ThreadSnapshot{ThreadID: "thread-replacement"},
	}

	session.rotateAfterPendingSuspension(replacement, ready, suspends)

	if _, open := <-old.Notifications(); open {
		t.Fatal("replacement rotation left the previous epoch open")
	}
	if got := <-ready; got.fence != replacement.fence || got.snapshot.ThreadID != replacement.snapshot.ThreadID {
		t.Fatalf("replacement record = %+v, want %+v", got, replacement)
	}
	select {
	case <-suspends:
		t.Fatal("replacement rotation left a stale suspension edge")
	default:
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

// retiredObserverAttemptCeiling is the fixed reconnect budget the per-Agent
// app-server proxy observer used to exhaust before dropping a live activation
// onto hook fallback. The constant is gone from the product; the number is kept
// here so the retirement stays measurable rather than merely asserted.
const retiredObserverAttemptCeiling = 6

// retiredObserverExhaustionReason is the terminal authority reason that budget
// published. No product path may publish it again.
const retiredObserverExhaustionReason = "reconnect-exhausted"

// TestNativeLifecycleProducerIsExactlyOnePerActivationGeneration audits the
// single-producer rule after the per-Agent observer retirement.
//
// The rule used to be enforced by a selection made once per activation
// generation, with the legacy proxy observer reachable as the other branch.
// There is no other branch now: `internal/app` opens no app-server proxy of its
// own at all, and the native lifecycle observer is built with exactly one
// connection opener, so a dual-write window has nothing to open it with.
func TestNativeLifecycleProducerIsExactlyOnePerActivationGeneration(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fileSet := token.NewFileSet()
	inspected, openers := 0, 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fileSet, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		inspected++
		ast.Inspect(file, func(node ast.Node) bool {
			if selector, ok := node.(*ast.SelectorExpr); ok {
				if ident, ok := selector.X.(*ast.Ident); ok &&
					ident.Name == "codexappserver" && selector.Sel.Name == "OpenDefaultProxy" {
					t.Fatalf("%s opens a per-Agent app-server proxy; the broker binding is the only native producer", name)
				}
			}
			function, ok := node.(*ast.FuncDecl)
			if !ok || function.Name.Name != "runCodexNativeLifecycleObserver" {
				return true
			}
			ast.Inspect(function.Body, func(inner ast.Node) bool {
				pair, ok := inner.(*ast.KeyValueExpr)
				if !ok {
					return true
				}
				if key, ok := pair.Key.(*ast.Ident); ok && key.Name == "open" {
					openers++
				}
				return true
			})
			return true
		})
	}
	if inspected < 50 {
		t.Fatalf("inspected %d files, expected the whole package", inspected)
	}
	if openers != 1 {
		t.Fatalf("the native lifecycle observer is built with %d connection openers, want exactly 1", openers)
	}
}

// TestBrokerProducerKeepsRecoveringPastTheLegacyAttemptCeiling holds the
// contract the fixed six-attempt budget violated: while the exact binding is
// still current, the observer keeps recovering, and it never publishes the
// terminal exhaustion fallback. Since the retirement there is no other budget
// to fall back to, so this is now the whole reconnect contract rather than one
// producer's exemption from it.
func TestBrokerProducerKeepsRecoveringPastTheLegacyAttemptCeiling(t *testing.T) {
	sink := newRecordingCodexLifecycleSink()
	identity := brokerTestIdentity("thread-recover")
	var attempts atomic.Int64
	observer := codexNativeObserver{
		identity: identity,
		sink:     sink,
		delay:    time.Millisecond,
		maxDelay: time.Millisecond,
		open: func(context.Context) (codexLifecycleConnection, error) {
			attempts.Add(1)
			return nil, errors.New("endpoint is still being served")
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- observer.Run(ctx) }()

	want := int64(retiredObserverAttemptCeiling) * 3
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
		if strings.Contains(reason, retiredObserverExhaustionReason) {
			t.Fatalf("a persistent-reconnect producer published %q", retiredObserverExhaustionReason)
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

// codexOverflowFixture is the deterministic consumer-side backlog overflow
// harness for C-2.
//
// The overflow it produces is the real one: a real broker runtime, a real
// binding session, a real epoch whose bounded stream fills while the observer
// above it is held inside one sink write, and therefore the real
// `end(backlog-overflow)` + `resync` pair. Nothing about the storm that
// produced the field sample is simulated except its size, and no live process
// is involved.
//
// It also owns the two knobs the recovery seam turns on: whether the exact
// binding stops being readable, and whether that is permanent.
type codexOverflowFixture struct {
	*recordingCodexLifecycleSink

	stallMu sync.Mutex
	stall   bool
	stalled chan struct{}
	resume  chan struct{}

	bindingMu   sync.Mutex
	armOn       string
	armed       bool
	falseLeft   int
	falseAlways bool
}

func newCodexOverflowFixture() *codexOverflowFixture {
	return &codexOverflowFixture{
		recordingCodexLifecycleSink: newRecordingCodexLifecycleSink(),
		stalled:                     make(chan struct{}),
		resume:                      make(chan struct{}),
	}
}

// stallNextApply holds the observer inside its next semantic sink write. That
// is the whole stalled consumer: the event loop is a single goroutine, so
// while it is parked here it reads nothing from the epoch's bounded stream.
func (f *codexOverflowFixture) stallNextApply() {
	f.stallMu.Lock()
	f.stall = true
	f.stallMu.Unlock()
}

func (f *codexOverflowFixture) releaseApply() { close(f.resume) }

// loseBindingOn arms the binding-read failure at the exact authority write it
// names, so the seam is entered at a fixed point in the loop rather than at a
// wall-clock moment.
func (f *codexOverflowFixture) loseBindingOn(authority string, samples int, always bool) {
	f.bindingMu.Lock()
	f.armOn, f.falseLeft, f.falseAlways = authority, samples, always
	f.bindingMu.Unlock()
}

func (f *codexOverflowFixture) Apply(identity codexLifecycleIdentity, projection codexLifecycleProjection) error {
	f.stallMu.Lock()
	stall := f.stall
	f.stall = false
	f.stallMu.Unlock()
	if stall {
		close(f.stalled)
		<-f.resume
	}
	return f.recordingCodexLifecycleSink.Apply(identity, projection)
}

func (f *codexOverflowFixture) SetAuthority(identity codexLifecycleIdentity, source, epoch, reason string) error {
	err := f.recordingCodexLifecycleSink.SetAuthority(identity, source, epoch, reason)
	f.bindingMu.Lock()
	if f.armOn != "" && f.armOn == source+":"+reason {
		f.armOn, f.armed = "", true
	}
	f.bindingMu.Unlock()
	return err
}

func (f *codexOverflowFixture) BindingCurrent(identity codexLifecycleIdentity) bool {
	f.bindingMu.Lock()
	if f.armed {
		if f.falseAlways {
			f.bindingMu.Unlock()
			return false
		}
		if f.falseLeft > 0 {
			f.falseLeft--
			f.bindingMu.Unlock()
			return false
		}
	}
	f.bindingMu.Unlock()
	return f.recordingCodexLifecycleSink.BindingCurrent(identity)
}

// codexOverflowRun is one live observer under one live broker session.
type codexOverflowRun struct {
	endpoint   *brokerTestEndpoint
	session    *codexBrokerObserverSession
	journal    *recordingCodexObserverJournal
	startups   chan codexObserverStartupResult
	done       chan error
	cancel     context.CancelFunc
	opens      *atomic.Int64
	openErr    *atomic.Pointer[string]
	firstEpoch string
}

// lastOpenError renders the most recent replacement-open failure, so a run
// that never reaches a new epoch names why instead of only saying it did not.
func (r codexOverflowRun) lastOpenError() string {
	if rendered := r.openErr.Load(); rendered != nil {
		return *rendered
	}
	return "<none>"
}

// startCodexOverflowObserver wires the real broker session under the real
// observer loop and returns once the first epoch is ready.
func startCodexOverflowObserver(t *testing.T, threadID string, sink *codexOverflowFixture) codexOverflowRun {
	t.Helper()
	endpoint := newBrokerTestEndpoint()
	endpoint.respondWith("thread/read", `{"thread":{"id":"`+threadID+`","status":{"type":"active"}}}`)
	discovery, _ := startBrokerRuntimeForTest(t, endpoint)

	// One sibling Agent holds its own binding on the same shared connection for
	// the whole run. It is what the fleet always looks like, and it keeps this
	// fixture measuring the thing it is named for: without it the overflowing
	// binding is the broker's last one, so the resync also retires the upstream
	// connection, and the replacement Open would be waiting on a full endpoint
	// reconnect rather than on this Phase's recovery path.
	sibling := newCodexBrokerObserverSessionOn(brokerTestIdentity(threadID+"-sibling"), "", nil, discovery, nil)
	t.Cleanup(func() { _ = sibling.Close() })
	openBrokerEpoch(t, sibling)

	identity := brokerTestIdentity(threadID)
	session := newCodexBrokerObserverSessionOn(identity, "", nil, discovery, nil)
	t.Cleanup(func() { _ = session.Close() })

	journal := &recordingCodexObserverJournal{}
	startups := make(chan codexObserverStartupResult, 8)
	opens := &atomic.Int64{}
	openErr := &atomic.Pointer[string]{}
	observer := codexNativeObserver{
		identity: identity, sink: sink,
		delay: time.Millisecond, maxDelay: 2 * time.Millisecond, bindingTimeout: 100 * time.Millisecond,
		open: func(ctx context.Context) (codexLifecycleConnection, error) {
			opens.Add(1)
			connection, err := session.Open(ctx)
			if err != nil {
				rendered := err.Error()
				openErr.Store(&rendered)
			}
			return connection, err
		},
		transitions:   newCodexObserverLogJournal(journal.append, func() time.Time { return time.Unix(0, 0).UTC() }),
		reportStartup: func(result codexObserverStartupResult) { startups <- result },
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- observer.Run(ctx) }()

	first := ""
	select {
	case result := <-startups:
		if result.Status != codexObserverStartupReady || result.Epoch == "" {
			t.Fatalf("first startup result = %+v, want a ready epoch", result)
		}
		first = result.Epoch
	case <-time.After(20 * time.Second):
		t.Fatal("the observer never reached its first ready epoch")
	}
	return codexOverflowRun{
		endpoint: endpoint, session: session, journal: journal, startups: startups,
		done: done, cancel: cancel, opens: opens, openErr: openErr, firstEpoch: first,
	}
}

// overflowTheConsumerBacklog parks the observer inside one sink write, then
// overruns the epoch's bounded stream and waits until the session has actually
// retired that binding. The wait is on the resync itself, not on a duration,
// so the overflow is a fact of the fixture rather than a race with it.
func overflowTheConsumerBacklog(t *testing.T, run codexOverflowRun, sink *codexOverflowFixture, threadID string) {
	t.Helper()
	sink.stallNextApply()
	emit := func(seq int) {
		run.endpoint.emit(codexappserver.Notification{
			Method: "thread/status/changed",
			Params: json.RawMessage(fmt.Sprintf(`{"threadId":%q,"status":{"type":"active"},"seq":%d}`, threadID, seq)),
		})
	}
	emit(-1)
	select {
	case <-sink.stalled:
	case <-time.After(20 * time.Second):
		t.Fatal("the observer never stalled inside a sink write")
	}
	// Just past the app-side bound. Overrunning by more would put pressure on
	// the broker queue and the remote binding channel too, and the cause under
	// test is specifically the consumer-side one.
	for index := range codexBrokerObserverBacklog + 8 {
		emit(index)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		run.session.mu.Lock()
		retired := run.session.binding == nil && run.session.current == nil
		run.session.mu.Unlock()
		if retired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the bounded stream never overflowed into a resync")
		}
		time.Sleep(2 * time.Millisecond)
	}
	sink.releaseApply()
}

// TestObserverRecoversIntoANewEpochAfterConsumerBacklogOverflow is the C-2
// Guarantee, pinned end to end instead of assumed.
//
// It excludes two of the three candidate seams by observation rather than by
// argument. Seam 3 - "resync clears state and nobody calls Open again" - is
// excluded because the open counter advances past the overflow. Seam 2 - "the
// replacement Open keeps failing" - is excluded because that second Open
// returns a barrier whose epoch label and ready authority are both published.
// What remains, and what the next test owns, is the binding predicate.
func TestObserverRecoversIntoANewEpochAfterConsumerBacklogOverflow(t *testing.T) {
	const threadID = "thread-overflow-recovers"
	sink := newCodexOverflowFixture()
	run := startCodexOverflowObserver(t, threadID, sink)
	openedBefore := run.opens.Load()

	overflowTheConsumerBacklog(t, run, sink, threadID)

	deadline := time.After(20 * time.Second)
	var recovered codexObserverStartupResult
	for recovered.Status != codexObserverStartupReady {
		select {
		case result := <-run.startups:
			recovered = result
		case err := <-run.done:
			t.Fatalf("the observer stopped instead of recovering: err=%v authorities=%v records=%v",
				err, sink.authoritySnapshot(), run.journal.snapshot())
		case <-deadline:
			t.Fatalf("no replacement epoch: authorities=%v lastOpen=%s records=%v",
				sink.authoritySnapshot(), run.lastOpenError(), run.journal.snapshot())
		}
	}
	if recovered.Epoch == run.firstEpoch {
		t.Fatalf("the replacement epoch reused the overflowed label %q", run.firstEpoch)
	}
	run.cancel()
	<-run.done

	if run.opens.Load() <= openedBefore {
		t.Fatalf("Open was not called again after the resync: %d then %d", openedBefore, run.opens.Load())
	}
	authorities := sink.authoritySnapshot()
	want := []string{
		codexAuthorityControlPlane + ":" + string(codexObserverReasonReady),
		codexAuthorityInvalidating + ":" + string(codexObserverReasonBacklogOverflow),
		codexAuthorityControlPlane + ":" + string(codexObserverReasonReady),
	}
	if len(authorities) < len(want) {
		t.Fatalf("authority writes = %v, want at least %v", authorities, want)
	}
	for index, expected := range want {
		if authorities[index] != expected {
			t.Fatalf("authority write %d = %q, want %q (all: %v)", index, authorities[index], expected, authorities)
		}
	}
	// The overflowed epoch's own transition triple names the consumer side. An
	// overflow is not an endpoint suspension and must never be reported as one.
	overflowRecords := []string{}
	for _, entry := range run.journal.snapshot() {
		if entry.Epoch == run.firstEpoch {
			overflowRecords = append(overflowRecords, entry.Event+":"+string(entry.Reason))
		}
	}
	wantRecords := []string{
		string(codexObserverTransitionConnected) + ":" + string(codexObserverReasonReady),
		string(codexObserverTransitionDisconnected) + ":" + string(codexObserverReasonBacklogOverflow),
		string(codexObserverTransitionReconnecting) + ":" + string(codexObserverReasonBacklogOverflow),
	}
	if !slices.Equal(overflowRecords, wantRecords) {
		t.Fatalf("the overflowed epoch's records = %v, want %v", overflowRecords, wantRecords)
	}
	// A recovering observer leaves no terminal record. That absence is half of
	// the distinction the next test owns.
	if reasons := journalReasons(run.journal.snapshot(), codexObserverTransitionStopped); len(reasons) != 0 {
		t.Fatalf("a recovered observer recorded a terminal stop: %v", reasons)
	}
}

// TestBacklogOverflowStopIsDistinguishableFromARecoveringInvalidating is the
// C-2 Failure.Detection surface.
//
// When the exact binding stops being current at the overflow exit, the
// recovery scheduler is right to stop: a replaced activation has its own
// observer. What was wrong is that it stopped invisibly. The Pane keeps the
// `invalidating|<epoch>|backlog-overflow` projection published just before,
// SetAuthority refuses every later correction by design, and the pane-option
// diagnostic renders that Pane with an `active` epoch status - byte-identical
// to the pane in the test above that did recover. The terminal journal record
// is the only place the two can be told apart, and this test owns it.
func TestBacklogOverflowStopIsDistinguishableFromARecoveringInvalidating(t *testing.T) {
	const threadID = "thread-overflow-stuck"
	sink := newCodexOverflowFixture()
	sink.loseBindingOn(codexAuthorityInvalidating+":"+string(codexObserverReasonBacklogOverflow), 0, true)
	run := startCodexOverflowObserver(t, threadID, sink)

	overflowTheConsumerBacklog(t, run, sink, threadID)

	select {
	case err := <-run.done:
		if err != nil {
			t.Fatalf("the terminal stop reported an error: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("the observer neither recovered nor stopped: %v", run.journal.snapshot())
	}

	authorities := sink.authoritySnapshot()
	last := authorities[len(authorities)-1]
	if last != codexAuthorityInvalidating+":"+string(codexObserverReasonBacklogOverflow) {
		t.Fatalf("the stuck Pane's last authority = %q, want the held overflow projection (all: %v)", last, authorities)
	}
	if slices.Contains(authorities, codexAuthorityHook+":"+string(codexObserverReasonBacklogOverflow)) {
		t.Fatalf("a held overflow published provider-hook fallback: %v", authorities)
	}

	entries := run.journal.snapshot()
	var stopped *aiIngestLogEntry
	for index := range entries {
		if entries[index].Event == string(codexObserverTransitionStopped) {
			stopped = &entries[index]
		}
	}
	if stopped == nil {
		t.Fatalf("the terminal stop left no record, so it is indistinguishable from a recovering observer: %+v", entries)
	}
	if string(stopped.Reason) != string(codexObserverReasonBacklogOverflow) {
		t.Fatalf("the terminal record renamed the cause: %+v", *stopped)
	}
	if stopped.Result != codexObserverTransitionStuckResult {
		t.Fatalf("the terminal record result = %q, want %q", stopped.Result, codexObserverTransitionStuckResult)
	}
	if stopped.Epoch == "" || stopped.Pane == "" || stopped.ThreadID != threadID {
		t.Fatalf("the terminal record lost its routing identity: %+v", *stopped)
	}
	// The record is the last word: a stopped observer never reconnects behind
	// its own terminal record.
	if entries[len(entries)-1].Event != string(codexObserverTransitionStopped) {
		t.Fatalf("a transition followed the terminal stop: %+v", entries)
	}
}

// TestBacklogOverflowRecoverySurvivesATransientBindingReadFailure is the
// production defect this Phase found at candidate seam 1.
//
// BindingCurrent answers one false for two different questions: the binding
// was replaced, and the binding could not be read right now. Its production
// implementation returns false when loadRegistry fails and when the
// `tmux show-options` invocation fails, neither of which is evidence that this
// activation is gone. continueRecovery took its only non-cancel exit on a
// single such sample, so one failed read during the reconnect window retired a
// live activation for good and left its Pane on the invalidating projection
// with no producer left to move it - the exact frozen shape the field sample
// showed. The loop head has always waited out that window; the recovery
// scheduler now makes the same bounded wait.
func TestBacklogOverflowRecoverySurvivesATransientBindingReadFailure(t *testing.T) {
	const threadID = "thread-overflow-blip"
	sink := newCodexOverflowFixture()
	// Three unreadable samples, then the same live binding reads normally.
	sink.loseBindingOn(codexAuthorityInvalidating+":"+string(codexObserverReasonBacklogOverflow), 3, false)
	run := startCodexOverflowObserver(t, threadID, sink)

	overflowTheConsumerBacklog(t, run, sink, threadID)

	deadline := time.After(20 * time.Second)
	for {
		select {
		case result := <-run.startups:
			if result.Status == codexObserverStartupReady {
				run.cancel()
				<-run.done
				if reasons := journalReasons(run.journal.snapshot(), codexObserverTransitionStopped); len(reasons) != 0 {
					t.Fatalf("a transient read failure recorded a terminal stop: %v", reasons)
				}
				return
			}
			if result.Status == codexObserverStartupStale {
				t.Fatalf("a transient binding read failure retired a live activation: %v", run.journal.snapshot())
			}
		case err := <-run.done:
			t.Fatalf("the observer stopped on a transient binding read failure: err=%v authorities=%v records=%v",
				err, sink.authoritySnapshot(), run.journal.snapshot())
		case <-deadline:
			t.Fatalf("no replacement epoch after the blip: lastOpen=%s records=%v",
				run.lastOpenError(), run.journal.snapshot())
		}
	}
}
