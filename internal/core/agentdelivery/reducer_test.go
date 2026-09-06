package agentdelivery

import (
	"math/rand"
	"testing"
)

func TestDeliveryTransitionTable(t *testing.T) {
	queue := Delivery{MessageRef: "message-1", State: StateQueued}
	held := Delivery{MessageRef: "message-1", State: StateHeld}
	handoff := Delivery{MessageRef: "message-1", State: StateHandoff, WaiterRef: "waiter-1"}
	for _, test := range []struct {
		name       string
		current    Delivery
		event      Event
		want       State
		changed    bool
		ambiguous  bool
		wantReason string
	}{
		{name: "empty queues", event: Event{Kind: EventQueue, MessageRef: "message-1"}, want: StateQueued, changed: true},
		{name: "queued holds without waiter", current: queue, event: Event{Kind: EventHold, MessageRef: "message-1", Reason: "no-waiter"}, want: StateHeld, changed: true, wantReason: "no-waiter"},
		{name: "queued begins handoff", current: queue, event: Event{Kind: EventBeginHandoff, MessageRef: "message-1", WaiterRef: "waiter-1"}, want: StateHandoff, changed: true},
		{name: "held begins handoff", current: held, event: Event{Kind: EventBeginHandoff, MessageRef: "message-1", WaiterRef: "waiter-1"}, want: StateHandoff, changed: true},
		{name: "full write and exact helper receipt delivers", current: handoff, event: Event{Kind: EventDeliver, MessageRef: "message-1", WaiterRef: "waiter-1", FullFrameWritten: true, HelperReceipt: true}, want: StateDelivered, changed: true, wantReason: "provider-pipe-full-frame"},
		{name: "partial write cannot deliver", current: handoff, event: Event{Kind: EventDeliver, MessageRef: "message-1", WaiterRef: "waiter-1", FullFrameWritten: false, HelperReceipt: true}, want: StateHandoff},
		{name: "missing helper receipt cannot deliver", current: handoff, event: Event{Kind: EventDeliver, MessageRef: "message-1", WaiterRef: "waiter-1", FullFrameWritten: true, HelperReceipt: false}, want: StateHandoff},
		{name: "foreign waiter cannot deliver", current: handoff, event: Event{Kind: EventDeliver, MessageRef: "message-1", WaiterRef: "waiter-2", FullFrameWritten: true, HelperReceipt: true}, want: StateHandoff},
		{name: "provider refusal is terminal", current: held, event: Event{Kind: EventRefuse, MessageRef: "message-1", Reason: "provider-refused"}, want: StateRefused, changed: true, wantReason: "provider-refused"},
		{name: "pre-handoff ttl expires", current: held, event: Event{Kind: EventExpire, MessageRef: "message-1", Reason: "ttl"}, want: StateExpired, changed: true, wantReason: "ttl"},
		{name: "handoff timeout is ambiguous failure", current: handoff, event: Event{Kind: EventExpire, MessageRef: "message-1"}, want: StateFailed, changed: true, ambiguous: true, wantReason: "observation-timeout"},
		{name: "pre-handoff replacement is stale", current: held, event: Event{Kind: EventStale, MessageRef: "message-1", Reason: "helper-restart"}, want: StateStale, changed: true, wantReason: "helper-restart"},
		{name: "post-handoff replacement is ambiguous failure", current: handoff, event: Event{Kind: EventStale, MessageRef: "message-1"}, want: StateFailed, changed: true, ambiguous: true, wantReason: "delivery-outcome-unknown"},
		{name: "pre-handoff failure is terminal", current: queue, event: Event{Kind: EventFail, MessageRef: "message-1", Reason: "transport-failed"}, want: StateFailed, changed: true, wantReason: "transport-failed"},
		{name: "out of order delivery is ignored", current: queue, event: Event{Kind: EventDeliver, MessageRef: "message-1", WaiterRef: "waiter-1", FullFrameWritten: true, HelperReceipt: true}, want: StateQueued},
		{name: "foreign message is ignored", current: held, event: Event{Kind: EventExpire, MessageRef: "message-2"}, want: StateHeld},
		{name: "duplicate queue is ignored", current: queue, event: Event{Kind: EventQueue, MessageRef: "message-1"}, want: StateQueued},
		{name: "terminal is once", current: Delivery{MessageRef: "message-1", State: StateDelivered}, event: Event{Kind: EventFail, MessageRef: "message-1"}, want: StateDelivered},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, changed := Reduce(test.current, test.event)
			if got.State != test.want || changed != test.changed || got.Ambiguous != test.ambiguous || (test.wantReason != "" && got.Reason != test.wantReason) {
				t.Fatalf("Reduce(%+v, %+v) = (%+v, %v)", test.current, test.event, got, changed)
			}
		})
	}
}

func TestDeliveryReducerRandomSequencesAreTerminalOnceAndNeverAutoResend(t *testing.T) {
	for seed := range int64(256) {
		random := rand.New(rand.NewSource(seed)) // #nosec G404 -- deterministic state-machine exploration.
		state := Delivery{}
		terminal := Delivery{}
		for step := range 256 {
			event := Event{Kind: []EventKind{EventQueue, EventHold, EventBeginHandoff, EventDeliver, EventRefuse, EventExpire, EventStale, EventFail}[random.Intn(8)], MessageRef: "message-1", WaiterRef: "waiter-1", FullFrameWritten: random.Intn(2) == 1, HelperReceipt: random.Intn(2) == 1}
			next, _ := Reduce(state, event)
			if terminal.State != "" && next != terminal {
				t.Fatalf("seed %d step %d changed terminal %+v to %+v", seed, step, terminal, next)
			}
			if state.State == StateHandoff && next.State == StateQueued {
				t.Fatalf("seed %d step %d automatically resent handoff", seed, step)
			}
			state = next
			if state.State.Terminal() {
				terminal = state
			}
		}
	}
}
