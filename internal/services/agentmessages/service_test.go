package agentmessages

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
	message "github.com/crevissepartners/projmux/internal/core/agentmessage"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	store "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
)

func receiptFixture(t *testing.T) (Service, store.Record) {
	t.Helper()
	now := time.Now().UTC()
	e := message.Envelope{Version: message.Version, MessageRef: "message-fixed", ConversationRef: "conversation-fixed", Authority: message.PeerAuthority(), Payload: "untrusted peer text", AcceptedAt: now, Deadline: now.Add(time.Minute),
		Source: message.Route{AgentUID: "agent-source", PaneUID: "pane-source", ActivationGeneration: "gen-source", Provider: "codex", Incarnation: "route-source"},
		Target: message.Route{AgentUID: "agent-target", PaneUID: "pane-target", ActivationGeneration: "gen-target", Provider: "claude", Incarnation: "route-target"}}
	disk := store.NewStore(t.TempDir())
	r, _, err := disk.PutAccepted(e, "claude-coordination")
	if err != nil {
		t.Fatal(err)
	}
	return Service{Store: disk, Now: func() time.Time { return now }}, r
}

func TestStatusTerminalRetainsReceiptWithoutRegistryOrTransport(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		s, r := receiptFixture(t)
		kind := message.EventDeliver
		if unknown {
			kind = message.EventFail
		}
		stored, _, err := s.Store.Apply(r.Envelope.MessageRef, s.event(r, kind, "fixture-result", unknown))
		if err != nil {
			t.Fatal(err)
		}
		// No Registry or transport is configured. A terminal read needs neither.
		got, err := s.Status(context.Background(), r.Envelope.MessageRef)
		if err != nil || got != ReceiptFor(stored) {
			t.Fatalf("receipt=%+v err=%v", got, err)
		}
		if got.Delivery.OutcomeUnknown != unknown {
			t.Fatal("uncertainty changed")
		}
	}
}

func TestStatusProjectsRemovedTargetAndHandoffUncertainty(t *testing.T) {
	for _, handoff := range []bool{false, true} {
		s, r := receiptFixture(t)
		if handoff {
			var err error
			r, _, err = s.Store.MarkHandoff(r.Envelope.MessageRef)
			if err != nil {
				t.Fatal(err)
			}
		}
		s.LoadRegistry = func() (metadata.Registry, error) { return metadata.Registry{}, nil }
		got, err := s.Status(context.Background(), r.Envelope.MessageRef)
		if err != nil {
			t.Fatal(err)
		}
		state, reason := message.StateStale, "target-removed"
		if handoff {
			state, reason = message.StateFailed, "provider-handoff-outcome-unknown"
		}
		if got.Delivery.State != state || got.Delivery.Reason != reason || got.Delivery.OutcomeUnknown != handoff {
			t.Fatalf("%+v", got.Delivery)
		}
		stored, found, err := s.Store.Get(r.Envelope.MessageRef)
		if err != nil || !found || ReceiptFor(stored) != got {
			t.Fatalf("store=%+v found=%v err=%v", stored, found, err)
		}
	}
}

func TestCancellationAndMissingSourceReadNothing(t *testing.T) {
	s := Service{} // Any accidental store or Registry access would panic.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Status(ctx, "message-fixed"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := s.Send(ctx, SendRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if result, err := s.Send(context.Background(), SendRequest{Target: "uid:agent-target", Text: "peer", TTL: time.Minute}); err == nil || result.Receipt.MessageRef != "" {
		t.Fatalf("%+v %v", result, err)
	}
}

type projectionFailureStore struct {
	Store
	cause error
}

func (s projectionFailureStore) Apply(string, message.Event) (store.Record, bool, error) {
	return store.Record{}, false, s.cause
}
func (s projectionFailureStore) MarkHandoff(string) (store.Record, bool, error) {
	return store.Record{}, false, s.cause
}

func TestSDKClaudeProjectionFailureRetainsReceiptAndUncertainty(t *testing.T) {
	for _, state := range []agentdelivery.State{agentdelivery.StateDelivered, agentdelivery.StateHeld, agentdelivery.StateHandoff} {
		t.Run(string(state), func(t *testing.T) {
			s, r := receiptFixture(t)
			disk := s.Store
			cause := errors.New("injected projection failure")
			s.Store = projectionFailureStore{Store: disk, cause: cause}
			result, err := s.projectClaudeSend(r, agentdelivery.Delivery{MessageRef: r.Envelope.MessageRef, State: state}, nil)
			if !errors.Is(err, cause) || result.Envelope.MessageRef != r.Envelope.MessageRef || result.Delivery.State != r.Delivery.State || !result.Delivery.OutcomeUnknown || result.Delivery.Reason != "provider-handoff-outcome-unknown" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			durable, found, err := disk.Get(r.Envelope.MessageRef)
			if err != nil || !found || ReceiptFor(durable) != ReceiptFor(r) {
				t.Fatalf("durable state changed: %+v/%t/%v", durable, found, err)
			}
		})
	}
}
