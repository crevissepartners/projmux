package codexbroker

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// TestConcurrentBindingsRouteOutOfOrderResponsesAndInterleavedEventsToTheExactThread
// is the C-2 multiplex guarantee. Two bindings share one connection, their
// control responses come back in the opposite order to the requests, and their
// notifications are interleaved on the same stream. Every delivery must land
// on exactly the thread it names and on no other, and every response must
// reach exactly the caller that asked for it.
func TestConcurrentBindingsRouteOutOfOrderResponsesAndInterleavedEventsToTheExactThread(t *testing.T) {
	endpoint := newFakeEndpoint()
	broker, _, _ := newTestBroker(t, 16, endpoint)

	alpha, alphaFence := boundBinding(t, broker, "thread-alpha")
	beta, betaFence := boundBinding(t, broker, "thread-beta")

	// Hold both control requests so the endpoint can answer them in the
	// opposite order to the one they were issued in.
	endpoint.hold("request:turn/start@alpha")
	endpoint.hold("request:turn/start@beta")

	var wait sync.WaitGroup
	var alphaResult, betaResult string
	var alphaOutcome, betaOutcome MutationOutcome
	var alphaErr, betaErr error
	wait.Add(2)
	go func() {
		defer wait.Done()
		alphaOutcome, alphaErr = alpha.Submit(t.Context(), alphaFence,
			Mutation{Method: "turn/start@alpha", Params: map[string]string{"text": "alpha prompt"}, Result: &alphaResult})
	}()
	go func() {
		defer wait.Done()
		betaOutcome, betaErr = beta.Submit(t.Context(), betaFence,
			Mutation{Method: "turn/start@beta", Params: map[string]string{"text": "beta prompt"}, Result: &betaResult})
	}()

	waitUntil(t, "both control requests to reach the endpoint", func() bool {
		return len(endpoint.methods()) == 2
	})
	endpoint.release("request:turn/start@beta")
	endpoint.release("request:turn/start@alpha")
	wait.Wait()

	if alphaOutcome != MutationApplied || alphaErr != nil || alphaResult != "turn/start@alpha" {
		t.Fatalf("alpha mutation = %s/%v/%q", alphaOutcome, alphaErr, alphaResult)
	}
	if betaOutcome != MutationApplied || betaErr != nil || betaResult != "turn/start@beta" {
		t.Fatalf("beta mutation = %s/%v/%q", betaOutcome, betaErr, betaResult)
	}

	// Interleave the two threads' notifications on the one shared stream.
	for _, step := range []struct{ thread, method string }{
		{"thread-alpha", "item/started"},
		{"thread-beta", "item/started"},
		{"thread-alpha", "item/completed"},
		{"thread-beta", "item/completed"},
	} {
		endpoint.push(threadEvent(step.thread, step.method))
	}

	for _, binding := range []*Binding{alpha, beta} {
		var got []string
		for range 2 {
			event := nextEvent(t, binding)
			if event.Origin != EventOriginLive {
				t.Fatalf("%s event origin = %s", binding.ThreadID(), event.Origin)
			}
			var params struct {
				ThreadID string `json:"threadId"`
			}
			if err := json.Unmarshal(event.Params, &params); err != nil {
				t.Fatal(err)
			}
			if params.ThreadID != binding.ThreadID() {
				t.Fatalf("%s received an event for %s", binding.ThreadID(), params.ThreadID)
			}
			got = append(got, event.Method)
		}
		if strings.Join(got, ",") != "item/started,item/completed" {
			t.Fatalf("%s methods = %v", binding.ThreadID(), got)
		}
		assertQuiet(t, binding)
	}

	diagnostics := broker.Diagnostics()
	if diagnostics.Connects != 1 || diagnostics.Disconnects != 0 || diagnostics.RevokedBindings != 0 {
		t.Fatalf("multiplex diagnostics = %+v, want one undisturbed connection", diagnostics)
	}
}

// TestSlowBindingBacklogOverflowRevokesOnlyThatBindingAndKeepsTheSharedConnection
// is the C-2 slow-consumer failure mode. One binding stops reading until its
// bounded queue overflows. That binding is revoked with resync-required
// because its order now has a hole, while the other binding and the shared
// connection are untouched.
func TestSlowBindingBacklogOverflowRevokesOnlyThatBindingAndKeepsTheSharedConnection(t *testing.T) {
	const backlog = 2
	endpoint := newFakeEndpoint()
	broker, opener, _ := newTestBroker(t, backlog, endpoint)

	slow, _ := boundBinding(t, broker, "thread-slow")
	fast, _ := boundBinding(t, broker, "thread-fast")

	// Overrun the slow binding's queue by exactly one event.
	for range backlog + 1 {
		endpoint.push(threadEvent("thread-slow", "item/updated"))
	}
	waitUntil(t, "the slow binding to be revoked", func() bool {
		return slow.Revocation() != RefusalNone
	})
	if got := slow.Revocation(); got != RefusalResyncRequired {
		t.Fatalf("slow revocation = %s, want %s", got, RefusalResyncRequired)
	}

	// The revoked binding's stream ends after the events that did fit; it is
	// never silently gapped.
	delivered := 0
	for range slow.Events() {
		delivered++
	}
	if delivered != backlog {
		t.Fatalf("slow deliveries after the snapshot = %d, want the bounded %d", delivered, backlog)
	}
	if _, err := slow.ControlAuthority(); RefusalOf(err) != RefusalResyncRequired {
		t.Fatalf("revoked control authority = %v", err)
	}

	// The fast binding and the shared connection survive untouched.
	endpoint.push(threadEvent("thread-fast", "item/completed"))
	if event := nextEvent(t, fast); event.Method != "item/completed" {
		t.Fatalf("fast event = %+v", event)
	}
	if _, err := fast.ControlAuthority(); err != nil {
		t.Fatalf("fast control authority = %v", err)
	}
	diagnostics := broker.Diagnostics()
	if diagnostics.Connects != 1 || diagnostics.Disconnects != 0 {
		t.Fatalf("connection diagnostics = %+v, want the shared connection to survive", diagnostics)
	}
	if diagnostics.RevokedBindings != 1 || diagnostics.Bindings != 1 {
		t.Fatalf("binding diagnostics = %+v, want exactly one revoked binding", diagnostics)
	}
	if opener.count() != 1 {
		t.Fatalf("open attempts = %d, want no reconnect", opener.count())
	}
}

// TestThreadlessNotificationFansOutToNoBindingAndAttributesToNoAgent is the
// C-4 attribution guarantee. A notification that declares no thread has no
// owner: it must reach zero bindings, so it can never be attributed to an
// Agent, and neither fanning it out nor picking a nearest binding is allowed.
func TestThreadlessNotificationFansOutToNoBindingAndAttributesToNoAgent(t *testing.T) {
	threadless := []codexappserver.Notification{
		{Method: "account/updated"},
		{Method: "login/chatGptComplete", Params: json.RawMessage(`{"loginId":"login-1"}`)},
		{Method: "item/started", Params: json.RawMessage(`{"threadId":"   "}`)},
		{Method: "item/started", Params: json.RawMessage(`not json`)},
	}
	for _, notification := range threadless {
		if threadID, attributed := AttributeNotification(notification); attributed || threadID != "" {
			t.Fatalf("AttributeNotification(%s) = %q/%v, want no attribution", notification.Method, threadID, attributed)
		}
	}
	if threadID, attributed := AttributeNotification(threadEvent("thread-one", "item/started")); !attributed || threadID != "thread-one" {
		t.Fatalf("attributed notification = %q/%v", threadID, attributed)
	}

	endpoint := newFakeEndpoint()
	broker, _, _ := newTestBroker(t, 8, endpoint)
	binding, _ := boundBinding(t, broker, "thread-one")

	for _, notification := range threadless {
		endpoint.push(notification)
	}
	waitUntil(t, "every threadless notification to be classified", func() bool {
		return broker.Diagnostics().ThreadlessEvents == len(threadless)
	})

	// A notification for a thread nobody bound is also not this binding's.
	endpoint.push(threadEvent("thread-other", "item/started"))
	waitUntil(t, "the unbound notification to be classified", func() bool {
		return broker.Diagnostics().UnboundEvents == 1
	})

	assertQuiet(t, binding)
	diagnostics := broker.Diagnostics()
	if diagnostics.DeliveredEvents != 1 {
		t.Fatalf("delivered events = %d, want only the barrier snapshot", diagnostics.DeliveredEvents)
	}
	if diagnostics.BufferedEvents != 0 || diagnostics.RevokedBindings != 0 {
		t.Fatalf("threadless diagnostics = %+v", diagnostics)
	}

	// One live notification proves the binding was reachable all along, so the
	// zero fan-out above is attribution and not a stalled stream.
	endpoint.push(threadEvent("thread-one", "item/completed"))
	if event := nextEvent(t, binding); event.Method != "item/completed" {
		t.Fatalf("attributed event = %+v", event)
	}
}

// TestApprovalLeaseIsAnsweredOncePerConnectionAndDiesWithIt pins the approval
// fence. A lease binds the raw request id, the thread, and both epochs; it is
// spent by the first answer; and a connection replacement revokes it outright
// rather than letting it be replayed onto the new wire.
func TestApprovalLeaseIsAnsweredOncePerConnectionAndDiesWithIt(t *testing.T) {
	first, second := newFakeEndpoint(), newFakeEndpoint()
	broker, _, _ := newTestBroker(t, 8, first, second)
	binding, _ := boundBinding(t, broker, "thread-one")

	first.push(serverRequestEvent("thread-one", "item/commandExecution/requestApproval", "7"))
	event := nextEvent(t, binding)
	lease := event.Lease
	if !lease.held() || lease.ThreadID != "thread-one" || string(lease.RawRequestID) != "7" {
		t.Fatalf("minted lease = %+v", lease)
	}
	if lease.Fence != event.Fence {
		t.Fatalf("lease fence = %+v, event fence = %+v", lease.Fence, event.Fence)
	}

	if err := binding.Answer(t.Context(), lease, map[string]string{"decision": "accept"}); err != nil {
		t.Fatalf("first answer = %v", err)
	}
	if err := binding.Answer(t.Context(), lease, map[string]string{"decision": "accept"}); RefusalOf(err) != RefusalResponseAlreadyAnswered {
		t.Fatalf("duplicate answer = %v", err)
	}
	forged := lease
	forged.ThreadID = "thread-other"
	if err := binding.Answer(t.Context(), forged, nil); RefusalOf(err) != RefusalLeaseIdentityMismatch {
		t.Fatalf("forged thread answer = %v", err)
	}
	unheld := lease
	unheld.RawRequestID = nil
	if err := binding.Answer(t.Context(), unheld, nil); RefusalOf(err) != RefusalLeaseIdentityMismatch {
		t.Fatalf("answer without a request id = %v", err)
	}
	if got := first.answered(); len(got) != 1 || got[0] != "7" {
		t.Fatalf("answers on the first connection = %v, want exactly one", got)
	}

	// Replace the connection. The lease was minted on the old epoch, so it has
	// no authority on the new one and reaches the new wire with zero bytes.
	_ = first.Close()
	waitUntil(t, "the replacement connection to serve", func() bool {
		return broker.Diagnostics().Connects == 2
	})
	awaitSnapshot(t, binding)
	if err := binding.Answer(t.Context(), lease, nil); RefusalOf(err) != RefusalStaleConnectionEpoch {
		t.Fatalf("answer on a revoked lease = %v", err)
	}
	if got := second.answered(); len(got) != 0 {
		t.Fatalf("answers on the replacement connection = %v, want none", got)
	}
}

// TestBrokerLifecycleRefusalsAndEpochsAreClosed covers the binding lifecycle
// and endpoint-key unit surface: construction inputs, the exact-thread bind
// requirement, epochs that never repeat, and a supervisor that stops
// reconnecting exactly when no binding is left to serve.
func TestBrokerLifecycleRefusalsAndEpochsAreClosed(t *testing.T) {
	if _, err := NewBroker(Config{Opener: func(context.Context) (Endpoint, error) { return nil, nil }, Endpoint: "codex-app-server:other"}); RefusalOf(err) != RefusalEndpointUnknown {
		t.Fatalf("unknown endpoint key = %v", err)
	}
	if _, err := NewBroker(Config{}); RefusalOf(err) != RefusalEndpointUnknown {
		t.Fatalf("missing opener = %v", err)
	}
	if DefaultOpener("0.13.1", codexappserver.AttachOptions{Timeout: time.Second}) == nil {
		t.Fatal("DefaultOpener returned no opener")
	}

	first, second := newFakeEndpoint(), newFakeEndpoint()
	broker, opener, _ := newTestBroker(t, 8, first, second)
	if got := broker.Diagnostics().Endpoint; got != DefaultEndpointKey {
		t.Fatalf("endpoint key = %q, want the default", got)
	}
	if _, err := broker.Bind("   ", "/work/project", nil); RefusalOf(err) != RefusalThreadRequired {
		t.Fatalf("bind without a thread = %v", err)
	}

	binding, fence := boundBinding(t, broker, "thread-one")
	if fence != (Fence{Connection: 1, Binding: 1}) {
		t.Fatalf("first fence = %+v", fence)
	}
	if _, err := broker.Bind("thread-one", "/work/project", nil); RefusalOf(err) != RefusalBindingExists {
		t.Fatalf("duplicate bind = %v", err)
	}

	// Unbinding the last binding leaves nothing to serve, so a disconnect
	// after it must not schedule another attach.
	_ = binding.Close()
	if got := binding.Revocation(); got != RefusalBindingClosed {
		t.Fatalf("closed binding revocation = %s", got)
	}
	waitUntil(t, "the connection to be released", func() bool {
		return broker.Diagnostics().Disconnects == 1
	})
	attempts := opener.count()
	// A rebind of the same thread is a new binding epoch, never a revival of
	// the old one, so the retired fence is refused on its binding axis.
	rebound, reboundFence := boundBinding(t, broker, "thread-one")
	if reboundFence.Binding != 2 {
		t.Fatalf("rebound fence = %+v, want a fresh binding epoch", reboundFence)
	}
	if opener.count() != attempts+1 {
		t.Fatalf("open attempts = %d, want exactly one reconnect for the rebind", opener.count())
	}
	stale := Fence{Connection: reboundFence.Connection, Binding: 1}
	if outcome, err := rebound.Submit(t.Context(), stale, Mutation{Method: "turn/start"}); outcome != MutationRefused || RefusalOf(err) != RefusalStaleBindingEpoch {
		t.Fatalf("retired binding fence = %s/%v", outcome, err)
	}
	if got := second.methods(); len(got) != 0 {
		t.Fatalf("requests on the rebound connection = %v, want none", got)
	}

	_ = broker.Close()
	if got := rebound.Revocation(); got != RefusalBrokerClosed {
		t.Fatalf("revocation after Close = %s", got)
	}
	if _, err := broker.Bind("thread-two", "/work/project", nil); RefusalOf(err) != RefusalBrokerClosed {
		t.Fatalf("bind after Close = %v", err)
	}
}
