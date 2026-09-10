package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
)

// countingControlBindingLookup makes binding resolutions countable, so a test
// can prove a push stopped before the Registry/live-activation read rather than
// inferring it from the absence of a transport call.
type countingControlBindingLookup struct {
	inner agentControlBindingLookup
	err   error
	calls int
}

func (l *countingControlBindingLookup) Live(ctx context.Context, paneUID string) (agentControlLive, bool, error) {
	l.calls++
	if l.err != nil {
		return agentControlLive{}, false, l.err
	}
	return l.inner.Live(ctx, paneUID)
}

// applyFailingMessageStore fails only the terminal write. Every other operation
// stays on the real store, so the sender-visible receipt can be compared with
// what the store actually holds.
type applyFailingMessageStore struct {
	agentMessageStore
	err error
}

func (s applyFailingMessageStore) Apply(string, coremessage.Event) (messagestore.Record, bool, error) {
	return messagestore.Record{}, false, s.err
}

type codexPushFixture struct {
	cmd      *agentCommand
	store    *messagestore.Store
	registry coremetadata.Registry
	binding  *countingControlBindingLookup
	route    coremessage.Route
	target   coremetadata.Agent
	clock    time.Time
	calls    map[string]int
}

// newCodexPushFixture reuses the control-seam fixture style: a static live
// binding, a scripted control transport, and the same Registry the exact
// control CLI tests drive.
func newCodexPushFixture(t *testing.T) *codexPushFixture {
	t.Helper()
	cmd, registryStore, live := exactControlCLICommand(t)
	registry := registryStore.registry.Clone()
	target, ok := registry.Agent("agt-alpha-codex")
	if !ok {
		t.Fatal("codex target fixture is missing")
	}
	fixture := &codexPushFixture{
		cmd: cmd, store: messagestore.NewStore(t.TempDir()), registry: registry,
		binding: &countingControlBindingLookup{inner: live}, target: target.Clone(),
		// The store prunes terminal records against its own real clock, so the
		// fixture clock stays close to real time.
		clock: time.Now().UTC(), calls: map[string]int{},
	}
	fixture.route = publicMessageRoute(mustMessageRoute(t, registry, "agt-alpha-codex"))
	cmd.activeTarget = insideTmux("pan-alpha-codex", "win-alpha-main").lookup
	cmd.messagePaths = agentMessagePaths{loadRegistry: func() (coremetadata.Registry, error) { return registry.Clone(), nil }}
	cmd.messageStore = fixture.store
	cmd.messageRoute = &traceMessageRouteResolver{route: mustMessageRoute(t, registry, "agt-alpha-codex")}
	cmd.messageNow = func() time.Time { return fixture.clock }
	cmd.controlBinding = fixture.binding
	cmd.loadRegistry = func() (coremetadata.Registry, error) { return registry.Clone(), nil }
	fixture.script(nil, nil)
	return fixture
}

// script answers each control operation with one scripted response or error and
// counts the calls per operation.
func (f *codexPushFixture) script(responses map[string]agentControlResponse, errs map[string]error) {
	f.cmd.controlCall = func(_ context.Context, _ string, _ coremetadata.CodexEndpointRef, _ codexLifecycleIdentity,
		request agentControlRequest,
	) (agentControlResponse, error) {
		f.calls[request.Operation]++
		if err, ok := errs[request.Operation]; ok && err != nil {
			return agentControlResponse{}, err
		}
		if response, ok := responses[request.Operation]; ok {
			return response, nil
		}
		return agentControlResponse{OK: true, ThreadID: "thread-1", TurnID: "turn-1"}, nil
	}
}

func (f *codexPushFixture) accept(t *testing.T, ref string) messagestore.Record {
	t.Helper()
	envelope := coremessage.Envelope{Version: coremessage.Version, MessageRef: ref,
		ConversationRef: conversationRefFor(ref), Source: f.route, Target: f.route,
		Authority: coremessage.PeerAuthority(), Payload: "peer coordination payload",
		AcceptedAt: f.clock, Deadline: f.clock.Add(10 * time.Minute)}
	record, created, err := f.store.PutAccepted(envelope, "codex-inbox")
	if err != nil || !created {
		t.Fatalf("accept %s: created=%t err=%v", ref, created, err)
	}
	return record
}

// staleFenceRegistry answers the first Registry read with the binding the push
// resolves and every later read with a changed authority, so the consumer fence
// revalidation inside callControl refuses before transport.
func (f *codexPushFixture) staleFenceRegistry(t *testing.T) {
	t.Helper()
	stale := f.registry.Clone()
	pane, ok := stale.Pane("pan-alpha-codex")
	if !ok || pane.Status.Activation.Codex == nil || pane.Status.Activation.Codex.Authority == nil {
		t.Fatal("codex authority fixture is missing")
	}
	authority := *pane.Status.Activation.Codex.Authority
	authority.BindingEpoch++
	pane.Status.Activation.Codex.Authority = &authority
	reads := 0
	f.cmd.loadRegistry = func() (coremetadata.Registry, error) {
		reads++
		if reads == 1 {
			return f.registry.Clone(), nil
		}
		return stale.Clone(), nil
	}
}

func refusal(code string) agentControlResponse { return refusedControl(code, "fixture refusal") }

func TestCodexCoordinationPushClassifiesNativeOutcomesForSenders(t *testing.T) {
	type pushCase struct {
		name         string
		prepare      func(t *testing.T, f *codexPushFixture)
		responses    map[string]agentControlResponse
		errs         map[string]error
		wantState    coremessage.State
		wantReason   string
		wantUnknown  bool
		wantStarts   int
		wantSteers   int
		wantBindings int
	}
	cases := []pushCase{
		{
			name:      "start succeeds",
			wantState: coremessage.StateDelivered, wantReason: "provider-turn-push",
			wantStarts: 1, wantBindings: 1,
		},
		{
			name:      "turn-in-progress falls through to a steer that succeeds",
			responses: map[string]agentControlResponse{agentControlOpStart: refusal("turn-in-progress")},
			wantState: coremessage.StateDelivered, wantReason: "provider-turn-push",
			wantStarts: 1, wantSteers: 1, wantBindings: 1,
		},
		{
			name: "render failure never reaches the binding",
			prepare: func(_ *testing.T, f *codexPushFixture) {
				f.cmd.messageCodexContent = func(coremessage.Envelope) (string, error) {
					return "", errors.New("fixture render failure")
				}
			},
			wantState: coremessage.StateFailed, wantReason: "codex-turn-content-build-failed",
		},
		{
			name:      "missing native control seam",
			prepare:   func(_ *testing.T, f *codexPushFixture) { f.cmd.loadRegistry = nil },
			wantState: coremessage.StateFailed, wantReason: "codex-native-control-unconfigured",
		},
		{
			name: "binding resolution failure",
			prepare: func(_ *testing.T, f *codexPushFixture) {
				f.binding.err = errors.New("fixture live binding failure")
			},
			wantState: coremessage.StateFailed, wantReason: "codex-native-binding-unavailable",
			wantBindings: 1,
		},
		{
			name:      "consumer fence refuses before transport",
			prepare:   func(t *testing.T, f *codexPushFixture) { f.staleFenceRegistry(t) },
			wantState: coremessage.StateFailed, wantReason: codexPushRefusedReason,
			wantBindings: 1,
		},
		{
			name:      "transport failure is ambiguous",
			errs:      map[string]error{agentControlOpStart: errors.New("fixture transport failure")},
			wantState: coremessage.StateFailed, wantReason: codexPushUnknownReason, wantUnknown: true,
			wantStarts: 1, wantBindings: 1,
		},
	}
	for _, code := range []string{"stale-epoch", "stale-binding", "unavailable", "stale-turn", "turn-state-unavailable", "invalid-operation"} {
		cases = append(cases, pushCase{
			name:      "start known-zero " + code,
			responses: map[string]agentControlResponse{agentControlOpStart: refusal(code)},
			wantState: coremessage.StateFailed, wantReason: codexPushRefusedReason,
			wantStarts: 1, wantBindings: 1,
		})
	}
	for _, code := range []string{"turn-start-failed", "timeout", "protocol-error", "fixture-unrecognised-code"} {
		cases = append(cases, pushCase{
			name:      "start ambiguous " + code,
			responses: map[string]agentControlResponse{agentControlOpStart: refusal(code)},
			wantState: coremessage.StateFailed, wantReason: codexPushUnknownReason, wantUnknown: true,
			wantStarts: 1, wantBindings: 1,
		})
	}
	for _, code := range []string{"stale-epoch", "stale-binding", "unavailable", "no-active-turn", "turn-state-unavailable", "invalid-operation"} {
		cases = append(cases, pushCase{
			name: "steer known-zero " + code,
			responses: map[string]agentControlResponse{
				agentControlOpStart: refusal("turn-in-progress"), agentControlOpSteer: refusal(code),
			},
			wantState: coremessage.StateFailed, wantReason: codexPushRefusedReason,
			wantStarts: 1, wantSteers: 1, wantBindings: 1,
		})
	}
	// `stale-turn` is ambiguous for turn-steer and known-zero for turn-start:
	// the steer spelling is controlWireFailure after the provider write left.
	for _, code := range []string{"stale-turn", "timeout", "protocol-error", "fixture-unrecognised-code"} {
		cases = append(cases, pushCase{
			name: "steer ambiguous " + code,
			responses: map[string]agentControlResponse{
				agentControlOpStart: refusal("turn-in-progress"), agentControlOpSteer: refusal(code),
			},
			wantState: coremessage.StateFailed, wantReason: codexPushUnknownReason, wantUnknown: true,
			wantStarts: 1, wantSteers: 1, wantBindings: 1,
		})
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCodexPushFixture(t)
			fixture.script(test.responses, test.errs)
			if test.prepare != nil {
				test.prepare(t, fixture)
			}
			record := fixture.accept(t, "message-classify")
			updated, err := fixture.cmd.pushCodexCoordination(record, fixture.target, record.Envelope)
			if (err != nil) != (test.wantState != coremessage.StateDelivered) {
				t.Fatalf("push error = %v, want failure = %t", err, test.wantState != coremessage.StateDelivered)
			}
			if updated.Delivery.State != test.wantState || updated.Delivery.Reason != test.wantReason ||
				updated.Delivery.OutcomeUnknown != test.wantUnknown {
				t.Fatalf("delivery = %+v, want %s/%s/unknown=%t", updated.Delivery, test.wantState, test.wantReason, test.wantUnknown)
			}
			if fixture.calls[agentControlOpStart] != test.wantStarts || fixture.calls[agentControlOpSteer] != test.wantSteers ||
				fixture.binding.calls != test.wantBindings {
				t.Fatalf("start=%d steer=%d bindings=%d, want %d/%d/%d", fixture.calls[agentControlOpStart],
					fixture.calls[agentControlOpSteer], fixture.binding.calls, test.wantStarts, test.wantSteers, test.wantBindings)
			}
			stored, found, getErr := fixture.store.Get("message-classify")
			if getErr != nil || !found || stored.Delivery != updated.Delivery {
				t.Fatalf("stored delivery = %+v found=%t err=%v, want the receipt's %+v", stored.Delivery, found, getErr, updated.Delivery)
			}
		})
	}
}

func TestCodexCoordinationPushExpiresBeforeAnyProviderCall(t *testing.T) {
	fixture := newCodexPushFixture(t)
	record := fixture.accept(t, "message-expired")
	// The fake clock reaches the envelope deadline before the push runs.
	fixture.clock = record.Envelope.Deadline
	updated, err := fixture.cmd.pushCodexCoordination(record, fixture.target, record.Envelope)
	if err == nil {
		t.Fatal("expired push error = nil, want the sender-visible cause")
	}
	if updated.Delivery.State != coremessage.StateExpired || updated.Delivery.Reason != "deadline-expired" ||
		updated.Delivery.OutcomeUnknown {
		t.Fatalf("delivery = %+v, want expired/deadline-expired", updated.Delivery)
	}
	if len(fixture.calls) != 0 || fixture.binding.calls != 0 {
		t.Fatalf("expired push touched the provider: calls=%v bindings=%d", fixture.calls, fixture.binding.calls)
	}
	// `deadline-expired` is deliberately the token the store's own deadline
	// event uses, so status reports exactly what the sender already saw.
	stdout, _, statusErr := runRoute(t, fixture.cmd, "message", "status", "message-expired")
	if statusErr != nil {
		t.Fatal(statusErr)
	}
	state, reason := receiptStateAndReason(t, stdout)
	if state != string(updated.Delivery.State) || reason != updated.Delivery.Reason {
		t.Fatalf("status = %s/%s, want the receipt's %s/%s", state, reason, updated.Delivery.State, updated.Delivery.Reason)
	}
}

func TestCodexCoordinationPushFailuresExitNonzeroWithCauseAndStatusParity(t *testing.T) {
	for _, test := range []struct {
		name       string
		prepare    func(t *testing.T, f *codexPushFixture)
		wantState  string
		wantReason string
		wantAction string
	}{
		{
			name:      "native control unconfigured",
			prepare:   func(_ *testing.T, f *codexPushFixture) { f.cmd.loadRegistry = nil },
			wantState: "failed", wantReason: "codex-native-control-unconfigured",
			wantAction: "check exact Agent native control availability before retrying",
		},
		{
			name: "native binding unavailable",
			prepare: func(_ *testing.T, f *codexPushFixture) {
				f.binding.err = errors.New("fixture live binding failure")
			},
			wantState: "failed", wantReason: "codex-native-binding-unavailable",
			wantAction: "check exact Agent native control availability before retrying",
		},
		{
			name: "content build failed",
			prepare: func(_ *testing.T, f *codexPushFixture) {
				f.cmd.messageCodexContent = func(coremessage.Envelope) (string, error) {
					return "", errors.New("fixture render failure")
				}
			},
			wantState: "failed", wantReason: "codex-turn-content-build-failed",
			wantAction: "correct message content or configuration before retrying",
		},
		{
			name: "known zero-write refusal",
			prepare: func(_ *testing.T, f *codexPushFixture) {
				f.script(map[string]agentControlResponse{agentControlOpStart: refusal("stale-epoch")}, nil)
			},
			wantState: "failed", wantReason: codexPushRefusedReason,
			wantAction: "no turn was written; retry manually once the target thread state allows it",
		},
		{
			name: "ambiguous outcome",
			prepare: func(_ *testing.T, f *codexPushFixture) {
				f.script(map[string]agentControlResponse{agentControlOpStart: refusal("timeout")}, nil)
			},
			wantState: "failed", wantReason: codexPushUnknownReason,
			wantAction: "inspect provider outcome; do not resend while unknown; automatic resend disabled",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCodexPushFixture(t)
			test.prepare(t, fixture)
			stdout, _, err := runRoute(t, fixture.cmd, "message", "send", "uid:agt-alpha-codex",
				"--message-ref", "message-send-failure", "--", "peer coordination payload")
			if err == nil {
				t.Fatal("send error = nil, want a nonzero exit for an undelivered push")
			}
			if IsUsageError(err) {
				t.Fatalf("send error = %v, want a runtime failure rather than a usage dump", err)
			}
			fields := receiptFields(t, stdout)
			if fields[0] != "message-send-failure" || fields[1] != test.wantState ||
				fields[2] != test.wantReason || fields[3] != test.wantAction {
				t.Fatalf("receipt = %q, want ref/%s/%s/%s", stdout, test.wantState, test.wantReason, test.wantAction)
			}
			statusOut, _, statusErr := runRoute(t, fixture.cmd, "message", "status", "message-send-failure")
			if statusErr != nil {
				t.Fatal(statusErr)
			}
			state, reason := receiptStateAndReason(t, statusOut)
			if state != test.wantState || reason != test.wantReason {
				t.Fatalf("status = %s/%s, want the send receipt's %s/%s", state, reason, test.wantState, test.wantReason)
			}
		})
	}

	// Both persistence branches keep the sender informed even though the store
	// write is exactly what failed, so `agent message status` cannot match the
	// send receipt for either of them. That limit is the reason the failure
	// cause is never overwritten by the persistence error.
	t.Run("failure event whose persistence fails keeps the original cause", func(t *testing.T) {
		fixture := newCodexPushFixture(t)
		fixture.script(map[string]agentControlResponse{agentControlOpStart: refusal("stale-epoch")}, nil)
		fixture.cmd.messageStore = applyFailingMessageStore{agentMessageStore: fixture.store, err: errors.New("fixture persist failure")}
		stdout, _, err := runRoute(t, fixture.cmd, "message", "send", "uid:agt-alpha-codex",
			"--message-ref", "message-persist-failure", "--", "peer coordination payload")
		if err == nil || IsUsageError(err) {
			t.Fatalf("send error = %v, want a nonzero non-usage exit", err)
		}
		if !strings.Contains(err.Error(), "persist coordination outcome") {
			t.Fatalf("send error = %v, want the persistence failure named in the Go error", err)
		}
		fields := receiptFields(t, stdout)
		if fields[1] != "failed" || fields[2] != codexPushRefusedReason {
			t.Fatalf("receipt = %q, want the original cause token", stdout)
		}
		stored, found, getErr := fixture.store.Get("message-persist-failure")
		if getErr != nil || !found || stored.Delivery.State != coremessage.StateAccepted {
			t.Fatalf("stored = %+v found=%t err=%v, want the unpersisted accepted record", stored.Delivery, found, getErr)
		}
	})

	t.Run("delivered turn whose persistence fails discourages resend", func(t *testing.T) {
		fixture := newCodexPushFixture(t)
		fixture.cmd.messageStore = applyFailingMessageStore{agentMessageStore: fixture.store, err: errors.New("fixture persist failure")}
		stdout, _, err := runRoute(t, fixture.cmd, "message", "send", "uid:agt-alpha-codex",
			"--message-ref", "message-delivered-persist", "--", "peer coordination payload")
		if err == nil || IsUsageError(err) {
			t.Fatalf("send error = %v, want a nonzero non-usage exit", err)
		}
		fields := receiptFields(t, stdout)
		if fields[1] != "failed" || fields[2] != "broker-delivery-persist-failed" ||
			fields[3] != "inspect provider outcome; do not resend while unknown; automatic resend disabled" {
			t.Fatalf("receipt = %q, want the ambiguous persist projection", stdout)
		}
		if fixture.calls[agentControlOpStart] != 1 || fixture.calls[agentControlOpSteer] != 0 {
			t.Fatalf("calls = %v, want exactly one start", fixture.calls)
		}
	})
}

func TestCodexCoordinationPushKeepsTerminalReceiptAndSkipsExtraSteer(t *testing.T) {
	fixture := newCodexPushFixture(t)
	record := fixture.accept(t, "message-terminal")
	delivered, err := fixture.cmd.pushCodexCoordination(record, fixture.target, record.Envelope)
	if err != nil {
		t.Fatal(err)
	}
	if fixture.calls[agentControlOpStart] != 1 || fixture.calls[agentControlOpSteer] != 0 {
		t.Fatalf("calls = %v, want one start and no steer after a successful start", fixture.calls)
	}
	if delivered.Delivery.State != coremessage.StateDelivered || delivered.Delivery.Reason != "provider-turn-push" ||
		delivered.Delivery.OutcomeUnknown {
		t.Fatalf("delivery = %+v, want delivered/provider-turn-push", delivered.Delivery)
	}
	// The delivered reason names the push and nothing beyond it: a pushed turn
	// is not evidence of model consumption, acknowledgement, or completion.
	for _, forbidden := range []string{"consum", "acknowledg", "complete", "processed", "read", "answer"} {
		if strings.Contains(delivered.Delivery.Reason, forbidden) {
			t.Fatalf("delivered reason %q claims more than the push", delivered.Delivery.Reason)
		}
	}
	// Reduce is terminal-once, so a second push over the terminal record adds no
	// event and leaves the stored delivery byte-identical.
	again, err := fixture.cmd.pushCodexCoordination(delivered, fixture.target, delivered.Envelope)
	if err != nil {
		t.Fatal(err)
	}
	if again.Delivery != delivered.Delivery {
		t.Fatalf("second push changed the terminal delivery: %+v, want %+v", again.Delivery, delivered.Delivery)
	}
	stored, found, getErr := fixture.store.Get("message-terminal")
	if getErr != nil || !found || stored.Delivery != delivered.Delivery {
		t.Fatalf("stored delivery = %+v found=%t err=%v, want %+v", stored.Delivery, found, getErr, delivered.Delivery)
	}
}

func receiptFields(t *testing.T, stdout string) []string {
	t.Helper()
	fields := strings.Split(strings.TrimSuffix(stdout, "\n"), "\t")
	if len(fields) != 4 {
		t.Fatalf("receipt = %q, want ref\\tstate\\treason\\taction on one line", stdout)
	}
	return fields
}

func receiptStateAndReason(t *testing.T, stdout string) (string, string) {
	t.Helper()
	fields := receiptFields(t, stdout)
	return fields[1], fields[2]
}
