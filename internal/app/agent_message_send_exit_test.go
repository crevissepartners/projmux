package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
)

const sendExitExecutable = "/opt/pmx/projmux"

// scriptedClaudeSendAdapter answers every Submit with one scripted private
// delivery and counts the pushes. The embedded precheck adapter keeps the
// broker-shaped ExplicitReply for Claude-source replies.
type scriptedClaudeSendAdapter struct {
	*precheckClaudeAdapter
	delivery agentdelivery.Delivery
	err      error
	submits  int
}

func (a *scriptedClaudeSendAdapter) Submit(_ context.Context, _ string, _ coremetadata.AgentRouteRef, envelope coremessage.Envelope) (agentdelivery.Delivery, error) {
	a.submits++
	delivery := a.delivery
	delivery.MessageRef = envelope.MessageRef
	return delivery, a.err
}

func newScriptedClaudeSendFixture(t *testing.T, delivery agentdelivery.Delivery, err error) (*precheckFixture, *scriptedClaudeSendAdapter) {
	t.Helper()
	f := newPrecheckFixture(t, staticExecutable(sendExitExecutable))
	adapter := &scriptedClaudeSendAdapter{precheckClaudeAdapter: f.adapter, delivery: delivery, err: err}
	f.cmd.messageClaude = adapter
	return f, adapter
}

// clockAdvancingMessageStore moves the sender clock once the envelope is
// accepted, so the push that follows observes a deadline the send just minted.
type clockAdvancingMessageStore struct {
	agentMessageStore
	advance func(coremessage.Envelope)
}

func (s clockAdvancingMessageStore) PutAccepted(envelope coremessage.Envelope, adapter string) (messagestore.Record, bool, error) {
	record, created, err := s.agentMessageStore.PutAccepted(envelope, adapter)
	s.advance(envelope)
	return record, created, err
}

func persistedDelivery(t *testing.T, store interface {
	Get(string) (messagestore.Record, bool, error)
}, ref string,
) messagestore.Record {
	t.Helper()
	record, found, err := store.Get(ref)
	if err != nil || !found {
		t.Fatalf("persisted %s found=%t err=%v", ref, found, err)
	}
	return record
}

// contractUndelivered restates contract C-4 without the product judgment, so a
// mutated judgment cannot also rewrite what these tests expect. The receipt
// writer and the send exit both read that judgment.
var contractUndelivered = map[coremessage.State]bool{
	coremessage.StateRefused: true, coremessage.StateExpired: true, coremessage.StateStale: true, coremessage.StateFailed: true,
}

// assertSendExitFollowsReceipt checks one finished send against the receipt the
// store holds. stdout is exactly one receipt line. A terminal undelivered
// receipt has four fields and a nonzero non-usage error that names the ref and
// ends with the printed state, reason, outcomeUnknown and action; every other
// receipt has two fields and exits 0.
func assertSendExitFollowsReceipt(t *testing.T, stdout string, err error, ref string, delivery coremessage.Delivery) {
	t.Helper()
	if !strings.HasSuffix(stdout, "\n") || strings.Count(stdout, "\n") != 1 {
		t.Fatalf("stdout = %q, want exactly one receipt line", stdout)
	}
	fields := strings.Split(strings.TrimSuffix(stdout, "\n"), "\t")
	if !contractUndelivered[delivery.State] {
		if err != nil || len(fields) != 2 || fields[0] != ref || fields[1] != string(delivery.State) {
			t.Fatalf("stdout=%q err=%v, want %s\\t%s and exit 0", stdout, err, ref, delivery.State)
		}
		return
	}
	if err == nil || IsUsageError(err) {
		t.Fatalf("err = %v, want a nonzero non-usage exit for a %s receipt", err, delivery.State)
	}
	if len(fields) != 4 || fields[0] != ref || fields[1] != string(delivery.State) || fields[2] != delivery.Reason || fields[3] == "" {
		t.Fatalf("stdout = %q, want %s\\t%s\\t%s\\t<action>", stdout, ref, delivery.State, delivery.Reason)
	}
	detail := fmt.Sprintf("state=%s reason=%s outcomeUnknown=%t; %s", delivery.State, delivery.Reason, delivery.OutcomeUnknown, fields[3])
	if !strings.Contains(err.Error(), "Ref="+ref+" ") || !strings.HasSuffix(err.Error(), detail) {
		t.Fatalf("err = %v, want ref %s and the receipt's %q", err, ref, detail)
	}
}

func TestAgentMessageSendExitCodeFollowsReceiptStateForEveryDeliveryState(t *testing.T) {
	for _, test := range []struct {
		name    string
		private agentdelivery.Delivery
		err     error
		want    coremessage.Delivery
	}{
		{name: "delivered", private: agentdelivery.Delivery{State: agentdelivery.StateDelivered, Reason: "provider-pipe-full-frame", WaiterRef: "push-ref"},
			want: coremessage.Delivery{State: coremessage.StateDelivered, Reason: "provider-pipe-full-frame"}},
		{name: "held", private: agentdelivery.Delivery{State: agentdelivery.StateHeld},
			want: coremessage.Delivery{State: coremessage.StateHeld, Reason: "unspecified"}},
		{name: "handoff", private: agentdelivery.Delivery{State: agentdelivery.StateHandoff, WaiterRef: "push-ref"},
			want: coremessage.Delivery{State: coremessage.StateAccepted}},
		{name: "no transition", private: agentdelivery.Delivery{State: agentdelivery.StateQueued},
			want: coremessage.Delivery{State: coremessage.StateAccepted}},
		{name: "refused", private: agentdelivery.Delivery{State: agentdelivery.StateRefused, Reason: "provider-frame-unsupported"},
			want: coremessage.Delivery{State: coremessage.StateRefused, Reason: "provider-frame-unsupported"}},
		{name: "failed", private: agentdelivery.Delivery{State: agentdelivery.StateFailed, Reason: "provider-write-zero"},
			want: coremessage.Delivery{State: coremessage.StateFailed, Reason: "provider-write-zero"}},
		{name: "expired", private: agentdelivery.Delivery{State: agentdelivery.StateExpired, Reason: "ttl"},
			want: coremessage.Delivery{State: coremessage.StateExpired, Reason: "ttl"}},
		{name: "stale", private: agentdelivery.Delivery{State: agentdelivery.StateStale, Reason: "helper-stale"},
			want: coremessage.Delivery{State: coremessage.StateStale, Reason: "helper-stale"}},
		{name: "ambiguous failed", private: agentdelivery.Delivery{State: agentdelivery.StateFailed, Reason: "provider-handoff-outcome-unknown", Ambiguous: true},
			want: coremessage.Delivery{State: coremessage.StateFailed, Reason: "provider-handoff-outcome-unknown", OutcomeUnknown: true}},
		{name: "adapter error", err: errors.New("fixture submit transport failure"),
			want: coremessage.Delivery{State: coremessage.StateFailed, Reason: "provider-handoff-outcome-unknown", OutcomeUnknown: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, adapter := newScriptedClaudeSendFixture(t, test.private, test.err)
			ref := "message-exit-" + strings.ReplaceAll(test.name, " ", "-")
			stdout, err := f.send(t, f.claudeUID, ref, false, "coordination body")
			persisted := persistedDelivery(t, messagestore.NewStore(f.stateDir), ref).Delivery
			if persisted.State != test.want.State || persisted.Reason != test.want.Reason || persisted.OutcomeUnknown != test.want.OutcomeUnknown {
				t.Fatalf("persisted = %+v, want %s/%s/unknown=%t", persisted, test.want.State, test.want.Reason, test.want.OutcomeUnknown)
			}
			if adapter.submits != 1 {
				t.Fatalf("submits = %d, want one push", adapter.submits)
			}
			assertSendExitFollowsReceipt(t, stdout, err, ref, persisted)
		})
	}

	// An expired deadline reaches the real Submit validity and ends as the
	// existing refused private-frame reason, which is still undelivered.
	t.Run("expired deadline through the live adapter", func(t *testing.T) {
		f := newPrecheckFixture(t, staticExecutable(sendExitExecutable))
		f.cmd.messageClaude = liveAgentMessageClaudeAdapter{}
		past := time.Now().UTC().Add(-2 * time.Minute)
		f.cmd.messageNow = func() time.Time { return past }
		const ref = "message-exit-expired-deadline"
		stdout, _, err := runRoute(t, f.cmd, "message", "send", "uid:"+f.claudeUID, "--source", "uid:"+f.claudeUID,
			"--message-ref", ref, "--ttl", "1m", "--", "coordination body")
		record := persistedDelivery(t, messagestore.NewStore(f.stateDir), ref)
		if record.Delivery.State != coremessage.StateRefused || record.Delivery.Reason != "claude-private-frame-unsupported" {
			t.Fatalf("persisted = %+v, want refused/claude-private-frame-unsupported", record.Delivery)
		}
		// The same envelope with a live deadline is valid, so the deadline is
		// what the live adapter refused.
		target, ok := claudeTargetForRoute(f.route)
		if !ok {
			t.Fatal("exact Claude target unavailable")
		}
		now := time.Now().UTC()
		live := record.Envelope
		live.AcceptedAt, live.Deadline = now, now.Add(time.Minute)
		if claudePrivateCoordinationEnvelope(target, record.Envelope).valid(now, f.route) ||
			!claudePrivateCoordinationEnvelope(target, live).valid(now, f.route) {
			t.Fatal("fixture refusal is not the expired deadline")
		}
		assertSendExitFollowsReceipt(t, stdout, err, ref, record.Delivery)
	})
}

func TestAgentMessageSendAndExplicitReplyShareTheUndeliveredJudgment(t *testing.T) {
	for state, want := range map[coremessage.State]bool{
		"":                         false,
		coremessage.StateAccepted:  false,
		coremessage.StateHeld:      false,
		coremessage.StateDelivered: false,
		coremessage.StateRefused:   true,
		coremessage.StateExpired:   true,
		coremessage.StateStale:     true,
		coremessage.StateFailed:    true,
		"fixture-unknown-state":    false,
	} {
		for _, unknown := range []bool{false, true} {
			delivery := coremessage.Delivery{State: state, Reason: "fixture-reason", OutcomeUnknown: unknown}
			if got := agentMessageUndelivered(delivery); got != want {
				t.Errorf("agentMessageUndelivered(%q, outcomeUnknown=%t) = %t, want %t", state, unknown, got, want)
			}
		}
	}

	for _, test := range []struct {
		name, reason string
		codex        bool
		wantError    string
	}{
		{name: "claude source reply failed", reason: "provider-write-zero", wantError: "explicit reply not delivered: previousRef="},
		{name: "codex source reply failed", reason: "provider-write-zero", codex: true, wantError: "message not delivered: messageRef="},
		{name: "claude source reply accepted"},
		{name: "codex source reply accepted", codex: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newPrecheckFixture(t, staticExecutable(sendExitExecutable))
			f.adapter.reason = test.reason
			sourceUID := f.claudeUID
			if test.codex {
				sourceUID = precheckCodexSourceUID
			}
			ref := "message-exit-" + strings.ReplaceAll(test.name, " ", "-")
			stdout, err := f.send(t, sourceUID, ref, true, "reply body")
			record := persistedDelivery(t, messagestore.NewStore(f.stateDir), ref)
			if record.Envelope.ReplyTo != f.original.MessageRef || len(f.adapter.payloads) != 1 {
				t.Fatalf("reply record = %+v submits=%d, want one pushed reply", record.Envelope, len(f.adapter.payloads))
			}
			if agentMessageUndelivered(record.Delivery) != (test.reason != "") {
				t.Fatalf("persisted = %+v, want undelivered = %t", record.Delivery, test.reason != "")
			}
			assertSendExitFollowsReceipt(t, stdout, err, ref, record.Delivery)
			if test.reason == "" {
				return
			}
			if !strings.Contains(err.Error(), test.wantError+ref+" ") || !strings.HasSuffix(stdout, precheckReplyAction+"\n") {
				t.Fatalf("err = %v stdout = %q, want %q and the reply retry action", err, stdout, test.wantError+ref)
			}
		})
	}
}

func TestAgentMessageSendReplayOfTerminalUndeliveredReceiptExitsNonzero(t *testing.T) {
	t.Run("claude target", func(t *testing.T) {
		f, adapter := newScriptedClaudeSendFixture(t, agentdelivery.Delivery{State: agentdelivery.StateFailed, Reason: "provider-write-zero"}, nil)
		const ref = "message-exit-replay-claude"
		first, firstErr := f.send(t, f.claudeUID, ref, false, "coordination body")
		if firstErr == nil || adapter.submits != 1 {
			t.Fatalf("first send err=%v submits=%d, want a nonzero exit after one push", firstErr, adapter.submits)
		}
		second, secondErr := f.send(t, f.claudeUID, ref, false, "coordination body")
		if adapter.submits != 1 {
			t.Fatalf("same-ref replay pushed again: submits=%d", adapter.submits)
		}
		if second != first || secondErr == nil || secondErr.Error() != firstErr.Error() {
			t.Fatalf("replay stdout=%q err=%v, want the first %q and %v", second, secondErr, first, firstErr)
		}
		assertSendExitFollowsReceipt(t, second, secondErr, ref, persistedDelivery(t, messagestore.NewStore(f.stateDir), ref).Delivery)
	})

	t.Run("codex target", func(t *testing.T) {
		fixture := newCodexPushFixture(t)
		fixture.script(map[string]agentControlResponse{agentControlOpStart: refusal("stale-epoch")}, nil)
		const ref = "message-exit-replay-codex"
		args := []string{"message", "send", "uid:agt-alpha-codex", "--message-ref", ref, "--", "peer coordination payload"}
		first, _, firstErr := runRoute(t, fixture.cmd, args...)
		if firstErr == nil || fixture.calls[agentControlOpStart] != 1 || fixture.binding.calls != 1 {
			t.Fatalf("first send err=%v calls=%v bindings=%d, want a nonzero exit after one start", firstErr, fixture.calls, fixture.binding.calls)
		}
		second, _, secondErr := runRoute(t, fixture.cmd, args...)
		if fixture.calls[agentControlOpStart] != 1 || fixture.calls[agentControlOpSteer] != 0 || fixture.binding.calls != 1 {
			t.Fatalf("same-ref replay pushed again: calls=%v bindings=%d", fixture.calls, fixture.binding.calls)
		}
		if second != first {
			t.Fatalf("replay stdout=%q, want the first %q", second, first)
		}
		assertSendExitFollowsReceipt(t, second, secondErr, ref, persistedDelivery(t, fixture.store, ref).Delivery)
	})
}

func TestAgentMessageSendCodexTargetTerminalFailureExitsNonzeroUnderTheSameJudgment(t *testing.T) {
	t.Run("pre-dispatch deadline expired", func(t *testing.T) {
		fixture := newCodexPushFixture(t)
		fixture.cmd.messageStore = clockAdvancingMessageStore{agentMessageStore: fixture.store,
			advance: func(envelope coremessage.Envelope) { fixture.clock = envelope.Deadline }}
		const ref = "message-exit-codex-expired"
		stdout, _, err := runRoute(t, fixture.cmd, "message", "send", "uid:agt-alpha-codex",
			"--message-ref", ref, "--ttl", "1s", "--", "peer coordination payload")
		if len(fixture.calls) != 0 || fixture.binding.calls != 0 {
			t.Fatalf("expired push touched the provider: calls=%v bindings=%d", fixture.calls, fixture.binding.calls)
		}
		persisted := persistedDelivery(t, fixture.store, ref).Delivery
		if persisted.State != coremessage.StateExpired || persisted.Reason != "deadline-expired" || !agentMessageUndelivered(persisted) {
			t.Fatalf("persisted = %+v, want expired/deadline-expired", persisted)
		}
		if err == nil || IsUsageError(err) {
			t.Fatalf("err = %v, want a nonzero non-usage exit", err)
		}
		fields := receiptFields(t, stdout)
		if fields[0] != ref || fields[1] != string(persisted.State) || fields[2] != persisted.Reason ||
			fields[3] != agentMessageReceiptFailureAction(receiptFor(messagestore.Record{Envelope: coremessage.Envelope{MessageRef: ref,
				Target: fixture.route}, Delivery: persisted}), 0) {
			t.Fatalf("receipt = %q, want the persisted expired receipt first", stdout)
		}
	})

	t.Run("delivered", func(t *testing.T) {
		fixture := newCodexPushFixture(t)
		const ref = "message-exit-codex-delivered"
		stdout, _, err := runRoute(t, fixture.cmd, "message", "send", "uid:agt-alpha-codex",
			"--message-ref", ref, "--", "peer coordination payload")
		persisted := persistedDelivery(t, fixture.store, ref).Delivery
		if persisted.State != coremessage.StateDelivered || fixture.calls[agentControlOpStart] != 1 {
			t.Fatalf("persisted = %+v calls=%v, want one delivered start", persisted, fixture.calls)
		}
		assertSendExitFollowsReceipt(t, stdout, err, ref, persisted)
	})
}

func TestClassifyCodexTurnPushNeverReturnsUndeliveredWithoutCause(t *testing.T) {
	codes := []string{"turn-in-progress", "stale-epoch", "stale-binding", "unavailable", "stale-turn", "turn-state-unavailable",
		"invalid-operation", "no-active-turn", "turn-start-failed", "timeout", "protocol-error", "fixture-unrecognised-code", ""}
	for _, operation := range []string{agentControlOpStart, agentControlOpSteer} {
		responses := map[string]agentControlResponse{"zero response": {}}
		for _, code := range codes {
			responses["code "+code] = refusal(code)
		}
		for name, response := range responses {
			outcome := classifyCodexTurnPush(operation, response, nil)
			if outcome.delivered || outcome.err == nil ||
				(outcome.reason != codexPushRefusedReason && outcome.reason != codexPushUnknownReason) ||
				outcome.unknown != (outcome.reason == codexPushUnknownReason) {
				t.Errorf("%s %s outcome = %+v, want an undelivered outcome with a cause", operation, name, outcome)
			}
		}
		binding := classifyCodexTurnPush(operation, agentControlResponse{OK: true},
			fmt.Errorf("fixture wrap: %w", &exactAgentControlBindingError{Reason: "fixture binding refusal"}))
		if binding.delivered || binding.err == nil || binding.reason != codexPushRefusedReason || binding.unknown {
			t.Errorf("%s binding outcome = %+v, want a known-zero refusal with a cause", operation, binding)
		}
		transport := classifyCodexTurnPush(operation, agentControlResponse{OK: true}, errors.New("fixture transport failure"))
		if transport.delivered || transport.err == nil || transport.reason != codexPushUnknownReason || !transport.unknown {
			t.Errorf("%s transport outcome = %+v, want an ambiguous failure with a cause", operation, transport)
		}
		delivered := classifyCodexTurnPush(operation, agentControlResponse{OK: true, ThreadID: "thread-1", TurnID: "turn-1"}, nil)
		if !delivered.delivered || delivered.err != nil || delivered.steer || delivered.reason != "" {
			t.Errorf("%s delivered outcome = %+v, want delivered without a cause", operation, delivered)
		}
	}
}
