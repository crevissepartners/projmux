package agentmessage

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

var messageTestNow = time.Date(2026, 9, 6, 1, 2, 3, 0, time.UTC)

func messageEnvelopeFixture() Envelope {
	return Envelope{
		Version: Version, MessageRef: "message-one", ConversationRef: "conversation-one",
		Source:    Route{AgentUID: "agent-source", PaneUID: "pane-source", ActivationGeneration: "generation-source", Provider: "codex"},
		Target:    Route{AgentUID: "agent-target", PaneUID: "pane-target", ActivationGeneration: "generation-target", Provider: "claude"},
		Authority: PeerAuthority(), Payload: "untrusted coordination text", AcceptedAt: messageTestNow,
		Deadline: messageTestNow.Add(time.Minute),
	}
}

func eventFor(envelope Envelope, kind EventKind) Event {
	return Event{Kind: kind, MessageRef: envelope.MessageRef, ConversationRef: envelope.ConversationRef,
		Target: envelope.Target, Reason: string(kind), ObservedAt: messageTestNow.Add(time.Second)}
}

func TestEnvelopeBoundsAndReplyCorrelation(t *testing.T) {
	t.Parallel()
	original := messageEnvelopeFixture()
	if err := original.Validate(); err != nil {
		t.Fatal(err)
	}
	reply := messageEnvelopeFixture()
	reply.MessageRef = "message-reply"
	reply.ReplyTo = original.MessageRef
	reply.ConversationRef = original.ConversationRef
	reply.Source, reply.Target = original.Target, original.Source
	if err := ValidateReply(original, reply); err != nil {
		t.Fatal(err)
	}

	for _, mutate := range []func(*Envelope){
		func(e *Envelope) { e.Version++ },
		func(e *Envelope) { e.MessageRef = "message\x1b]52;c;Zm9v\a" },
		func(e *Envelope) { e.ConversationRef = "conversation\u0085control" },
		func(e *Envelope) { e.Payload = "" },
		func(e *Envelope) { e.Payload = string(make([]byte, MaxPayloadBytes+1)) },
		func(e *Envelope) { e.Authority.Permission = "approval" },
		func(e *Envelope) { e.Source.Provider = "unknown" },
		func(e *Envelope) { e.Deadline = e.AcceptedAt.Add(MaxTTL + time.Nanosecond) },
	} {
		candidate := original
		mutate(&candidate)
		if candidate.Validate() == nil {
			t.Fatalf("invalid envelope accepted: %+v", candidate)
		}
	}
	for _, mutate := range []func(*Envelope){
		func(e *Envelope) { e.MessageRef = original.MessageRef },
		func(e *Envelope) { e.ReplyTo = "foreign" },
		func(e *Envelope) { e.ConversationRef = "foreign" },
		func(e *Envelope) { e.Source = original.Source },
		func(e *Envelope) { e.Target = original.Target },
	} {
		candidate := reply
		mutate(&candidate)
		if ValidateReply(original, candidate) == nil {
			t.Fatalf("mis-correlated reply accepted: %+v", candidate)
		}
	}
}

func TestSameEnvelopeRetryCannotChangeImmutableInputOrExtendTTL(t *testing.T) {
	t.Parallel()
	stored := messageEnvelopeFixture()
	retry := stored
	retry.AcceptedAt = stored.AcceptedAt.Add(time.Hour)
	retry.Deadline = retry.AcceptedAt.Add(stored.Deadline.Sub(stored.AcceptedAt))
	if !stored.SameRetry(retry) {
		t.Fatal("same caller envelope was not idempotent")
	}
	for _, mutate := range []func(*Envelope){
		func(e *Envelope) { e.Payload = "changed" },
		func(e *Envelope) { e.Source.ActivationGeneration = "" },
		func(e *Envelope) { e.Target.ActivationGeneration = "changed" },
		func(e *Envelope) { e.Deadline = e.Deadline.Add(time.Second) },
		func(e *Envelope) { e.ReplyTo = "other" },
	} {
		candidate := retry
		mutate(&candidate)
		if stored.SameRetry(candidate) {
			t.Fatalf("mismatched retry accepted: %+v", candidate)
		}
	}
}

func TestEnvelopeRetryAndReplyCorrelationRandomizedProperties(t *testing.T) {
	t.Parallel()
	for seed := range int64(512) {
		random := rand.New(rand.NewSource(seed)) // #nosec G404 -- deterministic property exploration.
		original := messageEnvelopeFixture()
		original.MessageRef = fmt.Sprintf("message-%d-%d", seed, random.Uint64())
		original.ConversationRef = fmt.Sprintf("conversation-%d-%d", seed, random.Uint64())
		original.Payload = fmt.Sprintf("payload-%d-%d", seed, random.Uint64())
		original.AcceptedAt = messageTestNow.Add(time.Duration(random.Intn(1000)) * time.Millisecond)
		original.Deadline = original.AcceptedAt.Add(time.Duration(random.Intn(3600)+1) * time.Second)
		if err := original.Validate(); err != nil {
			t.Fatalf("seed=%d generated invalid envelope: %v", seed, err)
		}

		retry := original
		retry.AcceptedAt = retry.AcceptedAt.Add(time.Hour)
		retry.Deadline = retry.AcceptedAt.Add(original.Deadline.Sub(original.AcceptedAt))
		if !original.SameRetry(retry) {
			t.Fatalf("seed=%d exact retry rejected", seed)
		}
		mismatch := retry
		switch random.Intn(7) {
		case 0:
			mismatch.Payload += "-foreign"
		case 1:
			mismatch.Source.AgentUID += "-foreign"
		case 2:
			mismatch.Target.PaneUID += "-foreign"
		case 3:
			mismatch.Target.ActivationGeneration += "-foreign"
		case 4:
			mismatch.ReplyTo = "foreign"
		case 5:
			mismatch.ConversationRef += "-foreign"
		case 6:
			mismatch.Deadline = mismatch.Deadline.Add(time.Nanosecond)
		}
		if original.SameRetry(mismatch) {
			t.Fatalf("seed=%d mismatched retry accepted: %#v", seed, mismatch)
		}

		reply := original
		reply.MessageRef += "-reply"
		reply.ReplyTo = original.MessageRef
		reply.Source, reply.Target = original.Target, original.Source
		if err := ValidateReply(original, reply); err != nil {
			t.Fatalf("seed=%d exact reply rejected: %v", seed, err)
		}
		foreign := reply
		switch random.Intn(4) {
		case 0:
			foreign.ReplyTo += "-foreign"
		case 1:
			foreign.ConversationRef += "-foreign"
		case 2:
			foreign.Source.AgentUID += "-foreign"
		case 3:
			foreign.Target.ActivationGeneration += "-foreign"
		}
		if ValidateReply(original, foreign) == nil {
			t.Fatalf("seed=%d foreign reply accepted: %#v", seed, foreign)
		}
	}
}

func TestAuthorityMatrixIsExhaustiveAndPeerNeverEscalates(t *testing.T) {
	t.Parallel()
	want := map[Principal]map[Action]bool{
		PrincipalHuman: {
			ActionCoordinationSend: true, ActionCoordinationRead: true, ActionCoordinationReply: true,
			ActionTurnStart: true, ActionTurnSteer: true, ActionTurnInterrupt: true, ActionConfigWrite: true,
		},
		PrincipalPeer: {
			ActionCoordinationSend: true, ActionCoordinationRead: true, ActionCoordinationReply: true,
		},
		PrincipalProviderRuntime:  {ActionToolOrConnector: true, ActionModelHistoryWrite: true},
		PrincipalApprovalReviewer: {ActionApprovalReview: true},
	}
	seen := 0
	for _, principal := range Principals() {
		for _, action := range Actions() {
			seen++
			if got := Authorize(principal, action); got != want[principal][action] {
				t.Errorf("Authorize(%s, %s) = %t", principal, action, got)
			}
		}
	}
	if seen != len(Principals())*len(Actions()) {
		t.Fatalf("authority matrix cells = %d", seen)
	}
}

func TestPublicReducerTransitionTable(t *testing.T) {
	t.Parallel()
	envelope := messageEnvelopeFixture()
	accepted, changed := Reduce(Delivery{}, envelope, eventFor(envelope, EventAccept))
	if !changed || accepted.State != StateAccepted {
		t.Fatalf("accept = (%+v, %t)", accepted, changed)
	}
	held, changed := Reduce(accepted, envelope, eventFor(envelope, EventHold))
	if !changed || held.State != StateHeld {
		t.Fatalf("hold = (%+v, %t)", held, changed)
	}
	for _, test := range []struct {
		kind EventKind
		want State
	}{
		{EventDeliver, StateDelivered}, {EventRefuse, StateRefused}, {EventExpire, StateExpired},
		{EventStale, StateStale}, {EventFail, StateFailed},
	} {
		for _, initial := range []Delivery{accepted, held} {
			event := eventFor(envelope, test.kind)
			event.OutcomeUnknown = test.kind == EventFail
			got, changed := Reduce(initial, envelope, event)
			if !changed || got.State != test.want || got.State.Terminal() != true || got.OutcomeUnknown != event.OutcomeUnknown {
				t.Errorf("%s from %s = (%+v, %t)", test.kind, initial.State, got, changed)
			}
		}
	}
	for _, event := range []Event{
		eventFor(envelope, EventDeliver),
		{Kind: EventHold, MessageRef: "foreign", ConversationRef: envelope.ConversationRef, Target: envelope.Target, ObservedAt: messageTestNow},
		{Kind: EventHold, MessageRef: envelope.MessageRef, ConversationRef: "foreign", Target: envelope.Target, ObservedAt: messageTestNow},
		{Kind: EventHold, MessageRef: envelope.MessageRef, ConversationRef: envelope.ConversationRef, Target: envelope.Source, ObservedAt: messageTestNow},
	} {
		got, changed := Reduce(accepted, envelope, event)
		if event.Kind == EventDeliver {
			continue
		}
		if changed || !reflect.DeepEqual(got, accepted) {
			t.Errorf("foreign event changed state: %+v", event)
		}
	}
	terminal, _ := Reduce(accepted, envelope, eventFor(envelope, EventDeliver))
	for _, kind := range []EventKind{EventAccept, EventHold, EventDeliver, EventRefuse, EventExpire, EventStale, EventFail} {
		got, changed := Reduce(terminal, envelope, eventFor(envelope, kind))
		if changed || !reflect.DeepEqual(got, terminal) {
			t.Errorf("terminal changed on %s", kind)
		}
	}
	foreignCurrent := accepted
	foreignCurrent.MessageRef = "foreign"
	if got, changed := Reduce(foreignCurrent, envelope, eventFor(envelope, EventHold)); changed || !reflect.DeepEqual(got, foreignCurrent) {
		t.Fatalf("foreign current delivery changed: %#v", got)
	}
	early := eventFor(envelope, EventHold)
	early.ObservedAt = envelope.AcceptedAt.Add(-time.Nanosecond)
	if got, changed := Reduce(accepted, envelope, early); changed || !reflect.DeepEqual(got, accepted) {
		t.Fatalf("early event changed state: %#v", got)
	}
}

func TestPublicReducerMatchesReferenceModelForRandomSequences(t *testing.T) {
	t.Parallel()
	envelope := messageEnvelopeFixture()
	kinds := []EventKind{EventAccept, EventHold, EventDeliver, EventRefuse, EventExpire, EventStale, EventFail}
	for seed := range int64(512) {
		random := rand.New(rand.NewSource(seed)) // #nosec G404 -- deterministic state exploration.
		var got, want Delivery
		for step := range 128 {
			event := eventFor(envelope, kinds[random.Intn(len(kinds))])
			if random.Intn(7) == 0 {
				event.MessageRef = "foreign"
			}
			if random.Intn(7) == 0 {
				event.Target = envelope.Source
			}
			event.OutcomeUnknown = random.Intn(2) == 1
			got, _ = Reduce(got, envelope, event)
			want = referenceReduce(want, envelope, event)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("seed=%d step=%d event=%+v got=%+v want=%+v", seed, step, event, got, want)
			}
		}
	}
}

func referenceReduce(current Delivery, envelope Envelope, event Event) Delivery {
	if event.MessageRef != envelope.MessageRef || event.ConversationRef != envelope.ConversationRef || event.Target != envelope.Target ||
		event.ObservedAt.Before(envelope.AcceptedAt) || (current.State != "" && (current.MessageRef != envelope.MessageRef || current.ConversationRef != envelope.ConversationRef)) || current.State.Terminal() {
		return current
	}
	if current.State == "" {
		if event.Kind == EventAccept {
			return Delivery{MessageRef: envelope.MessageRef, ConversationRef: envelope.ConversationRef, State: StateAccepted, AcceptedAt: envelope.AcceptedAt}
		}
		return current
	}
	if event.Kind == EventHold && current.State == StateAccepted {
		current.State, current.Reason = StateHeld, string(EventHold)
		return current
	}
	if current.State != StateAccepted && current.State != StateHeld {
		return current
	}
	terminal := map[EventKind]State{EventDeliver: StateDelivered, EventRefuse: StateRefused, EventExpire: StateExpired, EventStale: StateStale, EventFail: StateFailed}
	state, ok := terminal[event.Kind]
	if !ok {
		return current
	}
	current.State, current.Reason, current.TerminalAt = state, string(event.Kind), event.ObservedAt
	current.OutcomeUnknown = event.Kind == EventFail && event.OutcomeUnknown
	return current
}
