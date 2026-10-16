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
			result, err := s.ProjectClaudeSend(r, agentdelivery.Delivery{MessageRef: r.Envelope.MessageRef, State: state}, nil)
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

type acceptedReadFailureStore struct {
	Store
	cause error
}

func (s acceptedReadFailureStore) Get(string) (store.Record, bool, error) {
	return store.Record{}, false, s.cause
}

func TestAcceptedRPCReadFailurePreservesPartialReceiptAndCause(t *testing.T) {
	for _, reason := range []string{"explicit-reply-receipt-unavailable", "process-peer-receipt-unavailable"} {
		for _, cause := range []error{errors.New("injected receipt read failure"), nil} {
			s, r := receiptFixture(t)
			disk := s.Store
			s.Store = acceptedReadFailureStore{Store: disk, cause: cause}
			_, err := s.acceptedReceipt(r.Envelope, r.Envelope.MessageRef, reason)
			expectedCause := cause
			if expectedCause == nil {
				expectedCause = store.ErrNotFound
			}
			var partial *PartialError
			if !errors.As(err, &partial) || !errors.Is(err, expectedCause) || partial.Receipt.MessageRef != r.Envelope.MessageRef || partial.Receipt.ConversationRef != r.Envelope.ConversationRef || partial.Receipt.Source != r.Envelope.Source || partial.Receipt.Target != r.Envelope.Target || partial.Receipt.Deadline != r.Envelope.Deadline || partial.Receipt.Delivery.State != message.StateFailed || !partial.Receipt.Delivery.OutcomeUnknown || partial.Receipt.Delivery.Reason != reason {
				t.Fatalf("partial=%+v err=%v", partial, err)
			}
			if err.Error() != expectedCause.Error() {
				t.Fatalf("CLI error text changed: %v", err)
			}
			durable, found, err := disk.Get(r.Envelope.MessageRef)
			if err != nil || !found || ReceiptFor(durable) != ReceiptFor(r) {
				t.Fatalf("read failure changed receipt: %+v/%t/%v", durable, found, err)
			}
		}
	}
}

func TestSendPrefersPartialReceiptAfterAcceptedRPCReport(t *testing.T) {
	s, r := receiptFixture(t)
	cause := errors.New("injected post-RPC receipt failure")
	source, target := metadata.Agent{}, metadata.Agent{}
	source.Metadata.UID = r.Envelope.Source.AgentUID
	target.Metadata.UID = r.Envelope.Target.AgentUID
	s.LoadRegistry = func() (metadata.Registry, error) { return metadata.Registry{}, nil }
	s.ResolveSource = func(context.Context, metadata.Registry, string, string) (metadata.Agent, error) { return source, nil }
	s.ResolveAgent = func(context.Context, metadata.Registry, string, string) (metadata.Agent, error) { return target, nil }
	s.RequireCapability = func(string, metadata.Agent) error { return nil }
	s.ResolveRoute = func(context.Context, metadata.Registry, metadata.Agent) (metadata.AgentRouteRef, error) {
		return metadata.AgentRouteRef{}, nil
	}
	s.Deferred = func(ctx context.Context, reg metadata.Registry, source, target metadata.Agent, route metadata.AgentRouteRef, ref, reply, text string, ttl time.Duration, report func(Receipt, int) error) (bool, error) {
		if err := report(ReceiptFor(r), 12); err != nil {
			return true, err
		}
		return true, partialUnknown(r.Envelope, "process-peer-receipt-unavailable", cause)
	}
	result, err := s.Send(context.Background(), SendRequest{Source: "uid:source", Target: "uid:target", Text: "peer", TTL: time.Minute})
	if !errors.Is(err, cause) || result.Receipt.MessageRef != r.Envelope.MessageRef || !result.Receipt.Delivery.OutcomeUnknown || result.Receipt.Delivery.Reason != "process-peer-receipt-unavailable" || result.ContentBytes != 12 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func (s projectionFailureStore) Status(string, time.Time) (store.Record, bool, error) {
	return store.Record{}, false, s.cause
}

func TestStatusRefreshFailurePreservesLastConfirmedReceipt(t *testing.T) {
	for _, deferred := range []bool{false, true} {
		s, r := receiptFixture(t)
		disk := s.Store
		reg := metadata.Registry{}
		if deferred {
			var err error
			r, _, err = disk.Apply(r.Envelope.MessageRef, s.event(r, message.EventHold, "target-awaiting-resume", false))
			if err != nil {
				t.Fatal(err)
			}
			target := metadata.Agent{}
			target.Metadata.UID = r.Envelope.Target.AgentUID
			target.Spec.Provider = "claude"
			reg.Agents = []metadata.Agent{target}
			s.RequireCapability = RequireCapability
			s.DeferredHoldReason = "target-awaiting-resume"
		}
		s.LoadRegistry = func() (metadata.Registry, error) { return reg, nil }
		cause := errors.New("injected refresh storage failure")
		s.Store = projectionFailureStore{Store: disk, cause: cause}
		receipt, err := s.Status(context.Background(), r.Envelope.MessageRef)
		if !errors.Is(err, cause) || receipt != ReceiptFor(r) {
			t.Fatalf("receipt=%+v err=%v", receipt, err)
		}
		durable, found, err := disk.Get(r.Envelope.MessageRef)
		if err != nil || !found || ReceiptFor(durable) != ReceiptFor(r) {
			t.Fatalf("refresh changed receipt: %+v/%t/%v", durable, found, err)
		}
	}
}

func TestHeldReleaseStartupFailurePreservesReceiptAndCause(t *testing.T) {
	s, r := receiptFixture(t)
	target := metadata.Agent{}
	target.Metadata.UID = r.Envelope.Target.AgentUID
	target.Spec.Provider = "claude"
	target.Status.Phase = metadata.PhaseRunning
	target.Status.PaneRef = r.Envelope.Target.PaneUID
	target.Status.Interaction = metadata.AgentInteraction{Kind: metadata.InteractionApprovalRequired, ObservedAt: s.clock(), Source: string(metadata.InteractionSourceProviderHook)}
	cause := errors.New("injected release startup failure")
	launches := 0
	result, err := s.DeliverOrHold(context.Background(), r, target, metadata.AgentRouteRef{}, r.Envelope,
		func(context.Context, store.Record, metadata.Agent, metadata.AgentRouteRef, message.Envelope) (store.Record, error) {
			t.Fatal("blocked peer dispatched")
			return store.Record{}, nil
		},
		func() error { launches++; return cause })
	if !errors.Is(err, cause) || launches != 1 || result.Envelope.MessageRef != r.Envelope.MessageRef || result.Delivery.State != message.StateHeld || result.Delivery.OutcomeUnknown {
		t.Fatalf("result=%+v err=%v launches=%d", result, err, launches)
	}
	durable, found, err := s.Store.Get(r.Envelope.MessageRef)
	if err != nil || !found || ReceiptFor(durable) != ReceiptFor(result) {
		t.Fatalf("held receipt changed: %+v/%t/%v", durable, found, err)
	}
}

func TestPartialReceiptPreservesCLITextAndStorageCause(t *testing.T) {
	_, record := receiptFixture(t)
	cause := errors.New("private storage cause")
	partial := partialUnknown(record.Envelope, "explicit-reply-receipt-unavailable", errors.New("agent message send: explicit reply receipt unavailable"))
	partial.Cause = cause
	if !errors.Is(partial, cause) || partial.Error() != "agent message send: explicit reply receipt unavailable" || partial.Receipt.MessageRef != record.Envelope.MessageRef || !partial.Receipt.Delivery.OutcomeUnknown {
		t.Fatalf("partial=%+v", partial)
	}
}
