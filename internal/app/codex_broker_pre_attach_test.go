package app

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
)

// The tests in this file pin the window between a closed snapshot barrier and
// the observer publishing it as the live epoch. The broker replays what it
// buffered during the barrier right after the snapshot event, and upstream
// Codex replays a still-open approval request on thread/resume, so that window
// is exactly where a request sent before the observer attached arrives.
//
// None of them runs the pump goroutine. Each test binds a real broker runtime,
// reads the binding's ordered stream itself, and hands every event to admit -
// the pump's whole per-event step - so the order snapshot, pre-attach events,
// publish is fixed by the test rather than by scheduling.

type preAttachBinding struct {
	session  *codexBrokerObserverSession
	binding  *codexbroker.RemoteBinding
	ready    chan codexBrokerEpochRecord
	suspends <-chan struct{}
}

// bindBrokerSessionWithoutPump establishes the session's connection and
// binding the way ensure does, but leaves the ordered stream to the test.
func bindBrokerSessionWithoutPump(t *testing.T, session *codexBrokerObserverSession) preAttachBinding {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := codexbroker.Ensure(ctx, session.discovery, codexbroker.EnsureConfig{StartupTimeout: codexBrokerObserverStartupTimeout})
	if err != nil {
		t.Fatalf("ensure broker connection: %v", err)
	}
	binding, err := conn.Bind(ctx, session.identity.ThreadID, session.cwd, session.roots)
	if err != nil {
		_ = conn.Close()
		t.Fatalf("bind exact thread: %v", err)
	}
	ready := make(chan codexBrokerEpochRecord, 1)
	pumped := make(chan struct{})
	close(pumped)
	session.mu.Lock()
	session.conn, session.binding, session.ready, session.pumped = conn, binding, ready, pumped
	session.mu.Unlock()
	t.Cleanup(func() { _ = session.Close() })
	return preAttachBinding{session: session, binding: binding, ready: ready, suspends: binding.Suspensions()}
}

// next reads the binding's next ordered event and admits it.
func (b preAttachBinding) next(t *testing.T) codexbroker.Event {
	t.Helper()
	event := b.read(t)
	b.session.admit(event, b.ready, b.suspends)
	return event
}

func (b preAttachBinding) read(t *testing.T) codexbroker.Event {
	t.Helper()
	select {
	case event, open := <-b.binding.Events():
		if !open {
			t.Fatal("binding stream closed before the expected event")
		}
		return event
	case <-time.After(5 * time.Second):
		t.Fatal("binding delivered no event")
		return codexbroker.Event{}
	}
}

// publish consumes the offered barrier the way Open does.
func (b preAttachBinding) publish(t *testing.T) *codexBrokerLifecycleEpoch {
	t.Helper()
	var record codexBrokerEpochRecord
	select {
	case record = <-b.ready:
	default:
		t.Fatal("no closed barrier was offered")
	}
	epoch, err := b.session.publish(record)
	if err != nil {
		t.Fatalf("publish barrier: %v", err)
	}
	return epoch
}

// attachControlEpoch builds the exact-Agent control epoch the way the observer
// does - from a fenced lifecycle read - and applies every notification the
// epoch has already delivered.
func attachControlEpoch(t *testing.T, epoch *codexBrokerLifecycleEpoch, identity codexLifecycleIdentity) (*codexControlEpoch, []codexappserver.Notification) {
	t.Helper()
	snapshot, err := epoch.ReadLifecycleSnapshot(context.Background(), identity.ThreadID)
	if err != nil {
		t.Fatalf("read lifecycle snapshot: %v", err)
	}
	control := newCodexControlEpoch(epoch, identity, "epoch-1", snapshot, func(codexLifecycleIdentity) bool { return true })
	return control, applyDeliveredNotifications(t, epoch, control)
}

func applyDeliveredNotifications(t *testing.T, epoch *codexBrokerLifecycleEpoch, control *codexControlEpoch) []codexappserver.Notification {
	t.Helper()
	var delivered []codexappserver.Notification
	for len(epoch.Notifications()) > 0 {
		notification := <-epoch.Notifications()
		if err := control.ApplyNotification(notification); err != nil {
			t.Fatalf("apply %s: %v", notification.Method, err)
		}
		delivered = append(delivered, notification)
	}
	return delivered
}

func listPreAttachApprovals(t *testing.T, control *codexControlEpoch, identity codexLifecycleIdentity) []agentPendingApproval {
	t.Helper()
	response := control.Handle(context.Background(), agentControlRequest{Operation: agentControlOpApprovals, Identity: identity, Epoch: "epoch-1"})
	if !response.OK {
		t.Fatalf("approval-list refused: %+v", response)
	}
	return response.Approvals
}

func preAttachEndpoint(threadID string) *brokerTestEndpoint {
	endpoint := newBrokerTestEndpoint()
	endpoint.respondWith("thread/read", fmt.Sprintf(
		`{"thread":{"id":%q,"status":{"type":"active"},"turns":[{"id":"turn-1","status":"inProgress","startedAt":1}]}}`, threadID))
	return endpoint
}

func preAttachApproval(threadID, requestID string) codexappserver.Notification {
	return codexappserver.Notification{
		Method: "item/commandExecution/requestApproval",
		Params: json.RawMessage(fmt.Sprintf(
			`{"threadId":%q,"turnId":"turn-1","itemId":"item-1","startedAtMs":1,"command":"ls","cwd":"/work"}`, threadID)),
		RequestID:    requestID,
		RawRequestID: json.RawMessage(fmt.Sprintf("%q", requestID)),
	}
}

func admitSnapshot(t *testing.T, bound preAttachBinding) {
	t.Helper()
	if event := bound.next(t); event.Origin != codexbroker.EventOriginSnapshot {
		t.Fatalf("first binding event = %q (%s), want the snapshot barrier", event.Method, event.Origin)
	}
}

// TestBrokerObserverKeepsApprovalRequestSentBeforeAttach is the observed
// failure: an approval request replayed right after the snapshot, before the
// observer published the barrier, must still reach the control epoch intact
// and be answerable exactly once through the lease minted for its raw id.
func TestBrokerObserverKeepsApprovalRequestSentBeforeAttach(t *testing.T) {
	const threadID = "thread-pre-attach"
	endpoint := preAttachEndpoint(threadID)
	discovery, _ := startBrokerRuntimeForTest(t, endpoint)
	identity := brokerTestIdentity(threadID)
	bound := bindBrokerSessionWithoutPump(t, newCodexBrokerObserverSessionOn(identity, "", nil, discovery, nil))

	admitSnapshot(t, bound)
	endpoint.emit(preAttachApproval(threadID, "req-9"))
	if event := bound.next(t); string(event.Lease.RawRequestID) != `"req-9"` {
		t.Fatalf("pre-attach event lease raw id = %s, want \"req-9\"", event.Lease.RawRequestID)
	}

	epoch := bound.publish(t)
	control, _ := attachControlEpoch(t, epoch, identity)

	approvals := listPreAttachApprovals(t, control, identity)
	if len(approvals) != 1 {
		t.Fatalf("approval-list = %+v, want the one pre-attach request", approvals)
	}
	got := approvals[0]
	if got.RequestID != "req-9" || got.Kind != codexappserver.ApprovalCommand ||
		got.ThreadID != threadID || got.TurnID != "turn-1" || got.ItemID != "item-1" || got.Command != "ls" {
		t.Fatalf("pre-attach approval = %+v", got)
	}
	if !slices.Contains(got.Decisions, codexappserver.DecisionDecline) {
		t.Fatalf("pre-attach approval decisions = %v, want decline offered", got.Decisions)
	}

	review := agentControlRequest{
		Operation: agentControlOpReview, Identity: identity, Epoch: "epoch-1",
		RequestKey: "req-9", Decision: string(codexappserver.DecisionDecline),
	}
	if response := control.Handle(context.Background(), review); !response.OK {
		t.Fatalf("answer pre-attach request: %+v", response)
	}
	if response := control.Handle(context.Background(), review); response.OK {
		t.Fatal("a second answer to the pre-attach request was admitted")
	}
	if answers := endpoint.answerLedger(); len(answers) != 1 || answers[0] != `"req-9"` {
		t.Fatalf("answer ledger = %v, want exactly one answer for \"req-9\"", answers)
	}
}

// TestBrokerObserverDropsPreAttachRequestResolvedBeforeAttach keeps the held
// order honest: a request resolved before the observer attached is resolved
// in the epoch too, so it is never offered for an answer.
func TestBrokerObserverDropsPreAttachRequestResolvedBeforeAttach(t *testing.T) {
	const threadID = "thread-pre-resolved"
	endpoint := preAttachEndpoint(threadID)
	discovery, _ := startBrokerRuntimeForTest(t, endpoint)
	identity := brokerTestIdentity(threadID)
	bound := bindBrokerSessionWithoutPump(t, newCodexBrokerObserverSessionOn(identity, "", nil, discovery, nil))

	admitSnapshot(t, bound)
	endpoint.emit(preAttachApproval(threadID, "req-9"))
	bound.next(t)
	endpoint.emit(codexappserver.Notification{
		Method: "serverRequest/resolved",
		Params: json.RawMessage(fmt.Sprintf(`{"threadId":%q,"requestId":"req-9"}`, threadID)),
	})
	if event := bound.next(t); event.Method != "serverRequest/resolved" {
		t.Fatalf("second pre-attach event = %q, want serverRequest/resolved", event.Method)
	}

	epoch := bound.publish(t)
	control, delivered := attachControlEpoch(t, epoch, identity)
	if methods := notificationMethods(delivered); !slices.Equal(methods, []string{"item/commandExecution/requestApproval", "serverRequest/resolved"}) {
		t.Fatalf("delivered pre-attach order = %v", methods)
	}
	if approvals := listPreAttachApprovals(t, control, identity); len(approvals) != 0 {
		t.Fatalf("approval-list = %+v, want the resolved request absent", approvals)
	}
}

// TestBrokerObserverDropsPreAttachRequestWhoseTurnEnded is the turn-boundary
// half: a request whose turn completed before the observer attached is closed
// with that turn, not revived by the replay.
func TestBrokerObserverDropsPreAttachRequestWhoseTurnEnded(t *testing.T) {
	const threadID = "thread-pre-completed"
	endpoint := preAttachEndpoint(threadID)
	discovery, _ := startBrokerRuntimeForTest(t, endpoint)
	identity := brokerTestIdentity(threadID)
	bound := bindBrokerSessionWithoutPump(t, newCodexBrokerObserverSessionOn(identity, "", nil, discovery, nil))

	admitSnapshot(t, bound)
	endpoint.emit(preAttachApproval(threadID, "req-9"))
	bound.next(t)
	endpoint.emit(codexappserver.Notification{
		Method: "turn/completed",
		Params: json.RawMessage(fmt.Sprintf(`{"threadId":%q,"turn":{"id":"turn-1","status":"completed"}}`, threadID)),
	})
	if event := bound.next(t); event.Method != "turn/completed" {
		t.Fatalf("second pre-attach event = %q, want turn/completed", event.Method)
	}

	epoch := bound.publish(t)
	control, delivered := attachControlEpoch(t, epoch, identity)
	if methods := notificationMethods(delivered); !slices.Equal(methods, []string{"item/commandExecution/requestApproval", "turn/completed"}) {
		t.Fatalf("delivered pre-attach order = %v", methods)
	}
	if approvals := listPreAttachApprovals(t, control, identity); len(approvals) != 0 {
		t.Fatalf("approval-list = %+v, want the ended turn's request absent", approvals)
	}
}

// TestBrokerObserverListsPreAttachRequestOnce proves holding adds no copy: the
// held request is delivered once at publish, a reopen of the same barrier does
// not deliver it again, and the same request replayed live afterwards is still
// one entry.
func TestBrokerObserverListsPreAttachRequestOnce(t *testing.T) {
	const threadID = "thread-pre-once"
	endpoint := preAttachEndpoint(threadID)
	discovery, _ := startBrokerRuntimeForTest(t, endpoint)
	identity := brokerTestIdentity(threadID)
	bound := bindBrokerSessionWithoutPump(t, newCodexBrokerObserverSessionOn(identity, "", nil, discovery, nil))

	admitSnapshot(t, bound)
	endpoint.emit(preAttachApproval(threadID, "req-9"))
	bound.next(t)

	epoch := bound.publish(t)
	if held := len(epoch.Notifications()); held != 1 {
		t.Fatalf("publish delivered %d held events, want exactly the one pre-attach request", held)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if reopened, err := bound.session.Open(ctx); err != nil || reopened != epoch {
		t.Fatalf("reopen of the live barrier = %p, %v; want the same epoch", reopened, err)
	}
	if held := len(epoch.Notifications()); held != 1 {
		t.Fatalf("reopen redelivered held events: %d queued", held)
	}
	control, _ := attachControlEpoch(t, epoch, identity)

	endpoint.emit(preAttachApproval(threadID, "req-9"))
	bound.next(t)
	delivered := applyDeliveredNotifications(t, epoch, control)
	if len(delivered) != 1 || delivered[0].RequestID != "req-9" {
		t.Fatalf("live duplicate delivered %v", notificationMethods(delivered))
	}
	approvals := listPreAttachApprovals(t, control, identity)
	if len(approvals) != 1 || approvals[0].RequestID != "req-9" {
		t.Fatalf("approval-list = %+v, want one entry for req-9", approvals)
	}
}

// TestBrokerObserverHoldsOnlyTheOfferedBarriersEvents keeps the fence the
// admission rule: an event stamped with any other connection epoch is not the
// offered barrier's, so it is never handed to the epoch that barrier becomes.
func TestBrokerObserverHoldsOnlyTheOfferedBarriersEvents(t *testing.T) {
	const threadID = "thread-pre-fence"
	endpoint := preAttachEndpoint(threadID)
	discovery, _ := startBrokerRuntimeForTest(t, endpoint)
	identity := brokerTestIdentity(threadID)
	bound := bindBrokerSessionWithoutPump(t, newCodexBrokerObserverSessionOn(identity, "", nil, discovery, nil))

	admitSnapshot(t, bound)
	endpoint.emit(preAttachApproval(threadID, "req-stale"))
	stale := bound.read(t)
	stale.Fence.Connection++
	stale.Lease.Fence = stale.Fence
	bound.session.admit(stale, bound.ready, bound.suspends)
	endpoint.emit(preAttachApproval(threadID, "req-9"))
	bound.next(t)

	epoch := bound.publish(t)
	control, delivered := attachControlEpoch(t, epoch, identity)
	if len(delivered) != 1 || delivered[0].RequestID != "req-9" {
		t.Fatalf("delivered %d held events (%v), want only the offered barrier's req-9", len(delivered), delivered)
	}
	approvals := listPreAttachApprovals(t, control, identity)
	if len(approvals) != 1 || approvals[0].RequestID != "req-9" {
		t.Fatalf("approval-list = %+v, want only req-9", approvals)
	}
}

// TestBrokerObserverDropsEventsAfterTheEpochClosedBeforeRepublish keeps the
// hold to the window before a barrier's first publish. After the observer
// closes a published epoch it republishes the same barrier and reads a fresh
// lifecycle snapshot that restates the gap, so an event from that gap is
// dropped as before rather than replayed over the fresher state.
func TestBrokerObserverDropsEventsAfterTheEpochClosedBeforeRepublish(t *testing.T) {
	const threadID = "thread-pre-republish"
	endpoint := preAttachEndpoint(threadID)
	discovery, _ := startBrokerRuntimeForTest(t, endpoint)
	identity := brokerTestIdentity(threadID)
	bound := bindBrokerSessionWithoutPump(t, newCodexBrokerObserverSessionOn(identity, "", nil, discovery, nil))

	admitSnapshot(t, bound)
	first := bound.publish(t)
	if err := first.Close(); err != nil {
		t.Fatalf("close published epoch: %v", err)
	}
	endpoint.emit(preAttachApproval(threadID, "req-gap"))
	if event := bound.next(t); event.Fence != first.fence {
		t.Fatalf("gap event fence = %+v, want the published barrier's %+v", event.Fence, first.fence)
	}
	bound.session.mu.Lock()
	held := len(bound.session.held)
	bound.session.mu.Unlock()
	if held != 0 {
		t.Fatalf("held %d events after the barrier was already published", held)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connection, err := bound.session.Open(ctx)
	if err != nil {
		t.Fatalf("republish the closed epoch's barrier: %v", err)
	}
	republished, ok := connection.(*codexBrokerLifecycleEpoch)
	if !ok || republished == first || republished.fence != first.fence {
		t.Fatalf("republish = %T %p, want a new epoch on the same barrier", connection, connection)
	}
	control, delivered := attachControlEpoch(t, republished, identity)
	if len(delivered) != 0 {
		t.Fatalf("republish replayed %v from the closed-epoch gap", notificationMethods(delivered))
	}
	if approvals := listPreAttachApprovals(t, control, identity); len(approvals) != 0 {
		t.Fatalf("approval-list = %+v, want the gap request absent", approvals)
	}
}

// TestBrokerObserverPreAttachOverflowRetiresTheBinding mirrors the live
// backlog bound before attach: one event past the bound is not dropped
// silently, it retires the exact binding so the next Open resyncs from a fresh
// snapshot barrier.
func TestBrokerObserverPreAttachOverflowRetiresTheBinding(t *testing.T) {
	const threadID = "thread-pre-overflow"
	endpoint := preAttachEndpoint(threadID)
	discovery, _ := startBrokerRuntimeForTest(t, endpoint)
	identity := brokerTestIdentity(threadID)
	bound := bindBrokerSessionWithoutPump(t, newCodexBrokerObserverSessionOn(identity, "", nil, discovery, nil))

	admitSnapshot(t, bound)
	endpoint.emit(codexappserver.Notification{
		Method: "thread/status/changed",
		Params: json.RawMessage(fmt.Sprintf(`{"threadId":%q,"status":{"type":"active"}}`, threadID)),
	})
	event := bound.read(t)
	for range codexBrokerObserverBacklog {
		bound.session.admit(event, bound.ready, bound.suspends)
	}
	bound.session.mu.Lock()
	held, binding := len(bound.session.held), bound.session.binding
	bound.session.mu.Unlock()
	if held != codexBrokerObserverBacklog || binding != bound.binding {
		t.Fatalf("at the bound: held=%d binding kept=%t", held, binding == bound.binding)
	}

	bound.session.admit(event, bound.ready, bound.suspends)
	bound.session.mu.Lock()
	held, binding, pending := len(bound.session.held), bound.session.binding, bound.session.hasPending
	bound.session.mu.Unlock()
	if held != 0 || binding != nil || pending {
		t.Fatalf("past the bound: held=%d binding=%p pending=%t, want the binding retired", held, binding, pending)
	}
	if _, err := bound.binding.ControlAuthority(); err == nil {
		t.Fatal("overflowed binding still grants control authority")
	}
}

func notificationMethods(notifications []codexappserver.Notification) []string {
	methods := make([]string, 0, len(notifications))
	for _, notification := range notifications {
		methods = append(methods, notification.Method)
	}
	return methods
}
