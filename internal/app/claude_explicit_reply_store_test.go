package app

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
)

// compactReRegistration is what compact does to a Claude registration: the
// provider session stays, and a new lease helper registers under a new
// registration generation.
func compactReRegistration(a *coremetadata.ClaudeAuthorityRef) {
	a.LeaseProcess.Start = "test:compacted-helper"
	a.RegistrationGeneration = "compacted-registration"
}

// compactedHelperFixture delivers an original through the fixture's running
// helper (the previous helper, hub A) and then re-registers the same provider
// session with a new lease helper in the Registry file. The original's target
// carries the session-scoped incarnation, which re-registration keeps. The
// returned route is the new helper's Registry-current route, and the broker is
// the new helper's view of the same durable store.
func compactedHelperFixture(t *testing.T) (*claudeCoordinationTestFixture, *messagestore.Store, coremessage.Envelope,
	coremetadata.AgentRouteRef, *durableReplyTestBroker,
) {
	t.Helper()
	f, _, previous, _, original := sessionReplyCommandFixture(t,
		func(f *claudeCoordinationTestFixture) string { return f.route.SessionIncarnation() }, true)
	f.server.hub.mu.Lock()
	delivered := f.server.hub.messages[original.MessageRef] != nil
	f.server.hub.mu.Unlock()
	if !delivered {
		t.Fatal("previous helper did not deliver the original")
	}
	record, found, err := previous.store.Get(original.MessageRef)
	if err != nil || !found || record.Delivery.State != coremessage.StateDelivered {
		t.Fatalf("original is not delivered in the store: %+v found=%t err=%v", record.Delivery, found, err)
	}
	route := changedClaudeRoute(t, f, true, compactReRegistration)
	if route.Same(f.route) || route.SessionIncarnation() != f.route.SessionIncarnation() {
		t.Fatal("re-registration did not keep the session while replacing the helper")
	}
	return f, previous.store, original, route, &durableReplyTestBroker{store: previous.store, current: true, registryPath: f.registryPath}
}

// Acceptance 1: after compact the new helper answers the original its
// predecessor delivered, reading it from the durable store.
func TestCompactedClaudeHelperCommitsReplyToOriginalThePreviousHelperDelivered(t *testing.T) {
	_, store, original, route, broker := compactedHelperFixture(t)
	hub := newClaudeCoordinationHub()
	reply := explicitTestReply(original, "answer after compact")
	got := hub.commitExplicitReply(reply, route, broker)
	if got.Kind != "reply-accepted" || !got.ReplyCreated || got.ReplyRef != reply.MessageRef || got.Reason != "" {
		t.Fatalf("new helper refused the delivered original: %+v", got)
	}
	stored, found, err := store.Reply(original.MessageRef)
	if err != nil || !found || stored.Envelope.MessageRef != reply.MessageRef || stored.Envelope.ReplyTo != original.MessageRef {
		t.Fatalf("reply not stored: %+v found=%t err=%v", stored.Envelope, found, err)
	}
	if hub.messages[original.MessageRef] != nil {
		t.Fatal("store-found original entered the helper's pushed messages")
	}
}

// storeFileBytes is the durable store file as bytes, so a refusal can be shown
// to have written nothing.
func storeFileBytes(t *testing.T, store *messagestore.Store) []byte {
	t.Helper()
	data, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// Acceptance 2: a Registry-current helper of another provider session passes
// the fence but is not the original's target, so the store path refuses it as
// the memory path does.
func TestClaudeHelperOfAnotherSessionCannotAnswerStoredOriginal(t *testing.T) {
	f, store, original, _, broker := compactedHelperFixture(t)
	other := changedClaudeRoute(t, f, true, func(a *coremetadata.ClaudeAuthorityRef) { a.SessionID = "other-session" })
	if other.SessionIncarnation() == f.route.SessionIncarnation() {
		t.Fatal("other session kept the original's session incarnation")
	}
	if _, found, err := broker.StoredOriginal(original.MessageRef, other); err != nil || !found {
		t.Fatalf("fixture: the other session is not the Registry's current helper: found=%t err=%v", found, err)
	}
	before := storeFileBytes(t, store)
	got := newClaudeCoordinationHub().commitExplicitReply(explicitTestReply(original, "answer"), other, broker)
	if got.Kind != "reply-refused" || got.Reason != "invalid-explicit-reply-correlation" {
		t.Fatalf("another session answered the original: %+v", got)
	}
	if _, found, err := store.Reply(original.MessageRef); err != nil || found {
		t.Fatalf("another session stored a reply: found=%t err=%v", found, err)
	}
	if !bytes.Equal(before, storeFileBytes(t, store)) {
		t.Fatal("refused reply wrote the store")
	}
}

// Acceptance 3: an original in neither this helper's memory nor the store is
// reported as not found, not as a correlation mismatch.
func TestCompactedClaudeHelperReportsOriginalAbsentFromMemoryAndStore(t *testing.T) {
	_, store, original, route, broker := compactedHelperFixture(t)
	missing := original
	missing.MessageRef = "original-never-stored"
	reply := explicitTestReply(missing, "answer")
	got := newClaudeCoordinationHub().commitExplicitReply(reply, route, broker)
	if got.Kind != "reply-refused" || got.Reason != "broker-reply-original-not-found" || got.Reason == "invalid-explicit-reply-correlation" {
		t.Fatalf("missing original not reported as not found: %+v", got)
	}
	if _, found, err := store.Get(reply.MessageRef); err != nil || found {
		t.Fatalf("missing original stored a reply: found=%t err=%v", found, err)
	}
}

// Acceptance 4: a reply the previous helper already stored, committed or with
// an unknown outcome, is replayed for its own ref and refuses any other ref;
// the store keeps the one attempt.
func TestCompactedClaudeHelperReplaysOrRefusesReplyThePreviousHelperStored(t *testing.T) {
	for _, test := range []struct {
		name           string
		outcomeUnknown bool
	}{{"committed", false}, {"outcome unknown", true}} {
		t.Run(test.name, func(t *testing.T) {
			f, store, original, route, broker := compactedHelperFixture(t)
			first := explicitTestReply(original, "answer before compact")
			if got := f.server.hub.commitExplicitReply(first, f.route, f.server.broker); got.Kind != "reply-accepted" {
				t.Fatalf("previous helper did not commit: %+v", got)
			}
			if test.outcomeUnknown {
				record, _, err := store.Apply(first.MessageRef, coremessage.Event{Kind: coremessage.EventFail, MessageRef: first.MessageRef,
					ConversationRef: first.ConversationRef, Target: first.Target, ObservedAt: time.Now().UTC(),
					Reason: "provider-handoff-outcome-unknown", OutcomeUnknown: true})
				if err != nil || !record.Delivery.OutcomeUnknown {
					t.Fatalf("fixture: reply outcome not unknown: %+v %v", record.Delivery, err)
				}
			}
			hub := newClaudeCoordinationHub()
			replay := hub.commitExplicitReply(first, route, broker)
			if replay.Kind != "reply-replayed" || replay.ReplyRef != first.MessageRef || replay.ReplyCreated {
				t.Fatalf("same ref was not replayed: %+v", replay)
			}
			second := first
			second.MessageRef, second.Payload = "reply-after-compact", "answer after compact"
			conflict := hub.commitExplicitReply(second, route, broker)
			if conflict.Kind != "reply-refused" || conflict.Reason != "reply-already-committed" || conflict.ReplyRef != first.MessageRef ||
				conflict.ReplyDelivery == nil || conflict.ReplyDelivery.OutcomeUnknown != test.outcomeUnknown {
				t.Fatalf("different ref was not refused with the previous reply: %+v", conflict)
			}
			stored, found, err := store.Reply(original.MessageRef)
			if err != nil || !found || stored.Envelope.MessageRef != first.MessageRef {
				t.Fatalf("stored reply changed: %+v found=%t err=%v", stored.Envelope, found, err)
			}
			if _, found, err := store.Get(second.MessageRef); err != nil || found {
				t.Fatalf("second reply attempt stored: found=%t err=%v", found, err)
			}
		})
	}
}

// ambiguousCommitBroker loses the outcome of every reply commit.
type ambiguousCommitBroker struct {
	*durableReplyTestBroker
	commits int
}

func (b *ambiguousCommitBroker) CommitReply(coremessage.Envelope, coremessage.Envelope) (bool, error) {
	b.commits++
	return false, errors.New("private ambiguous persistence")
}

// An ambiguous commit through the store path reserves the original in this
// helper's memory, so no later call, same ref or not, writes again.
func TestCompactedClaudeHelperKeepsAmbiguousStoredReplyReserved(t *testing.T) {
	_, store, original, route, durable := compactedHelperFixture(t)
	broker := &ambiguousCommitBroker{durableReplyTestBroker: durable}
	hub := newClaudeCoordinationHub()
	reply := explicitTestReply(original, "answer")
	other := reply
	other.MessageRef = "reply-other"
	for i, attempt := range []coremessage.Envelope{reply, reply, other} {
		got := hub.commitExplicitReply(attempt, route, broker)
		if got.Kind != "reply-refused" || got.Reason != "broker-reply-outcome-unknown" || got.ReplyRef != reply.MessageRef || broker.commits != 1 {
			t.Fatalf("attempt %d: ambiguous reply was not held: %+v commits=%d", i, got, broker.commits)
		}
	}
	if hub.messages[original.MessageRef] != nil {
		t.Fatal("store-found original entered the helper's pushed messages")
	}
	if reservation := hub.storeReplies[original.MessageRef]; reservation == nil || !reservation.replyReserved {
		t.Fatal("ambiguous stored reply not reserved")
	}
	if _, found, err := store.Reply(original.MessageRef); err != nil || found {
		t.Fatalf("ambiguous broker stored a reply: found=%t err=%v", found, err)
	}
}

// Acceptance 5: a stored original is judged by the checks a pushed one is,
// so each refusal carries the memory path's token.
func TestStoredOriginalRefusesWithThePushedOriginalTokens(t *testing.T) {
	for _, test := range []struct {
		name     string
		operator bool
		deliver  bool
		expired  bool
		extended bool
		want     string
	}{
		{name: "operator origin", operator: true, deliver: true, want: coremessage.ReasonExplicitReplyOperatorOrigin},
		{name: "not delivered", want: "invalid-explicit-reply-correlation"},
		{name: "deadline expired", deliver: true, expired: true, want: "explicit-reply-deadline-expired"},
		{name: "deadline extended", deliver: true, extended: true, want: "explicit-reply-deadline-extended"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, store, base, route, broker := compactedHelperFixture(t)
			original := base
			original.MessageRef, original.ConversationRef = "original-judged", "conversation-judged"
			if test.operator {
				original.Origin, original.Source, original.Authority = coremessage.OperatorWebOrigin(), coremessage.Route{}, coremessage.OperatorAuthority()
			}
			if _, _, err := store.PutAccepted(original, "claude-coordination"); err != nil {
				t.Fatal(err)
			}
			state := agentdelivery.StateQueued
			if test.deliver {
				if err := broker.MarkHandoff(original); err != nil {
					t.Fatal(err)
				}
				if err := broker.MarkDelivered(original, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
				state = agentdelivery.StateDelivered
			}
			reply := explicitTestReply(original, "answer")
			if test.extended {
				reply.Deadline = original.Deadline.Add(time.Second)
			}
			now := time.Now().UTC()
			if test.expired {
				now = original.Deadline
			}
			pushed := newClaudeCoordinationHub()
			pushed.now = func() time.Time { return now }
			envelope := original
			pushed.messages[original.MessageRef] = &claudeCoordinationMessage{
				envelope: claudeCoordinationEnvelope{MessageRef: original.MessageRef, Deadline: original.Deadline, BrokerEnvelope: &envelope},
				delivery: agentdelivery.Delivery{MessageRef: original.MessageRef, State: state}}
			memory := pushed.commitExplicitReply(reply, route, broker)
			stored := newClaudeCoordinationHub()
			stored.now = func() time.Time { return now }
			fromStore := stored.commitExplicitReply(reply, route, broker)
			if memory.Kind != "reply-refused" || memory.Reason != test.want {
				t.Fatalf("fixture: pushed original gave %+v, want %s", memory, test.want)
			}
			if fromStore.Kind != memory.Kind || fromStore.Reason != memory.Reason {
				t.Fatalf("stored original gave %s, pushed original gave %s", fromStore.Reason, memory.Reason)
			}
			if _, found, err := store.Reply(original.MessageRef); err != nil || found {
				t.Fatalf("refused reply stored: found=%t err=%v", found, err)
			}
		})
	}
}

// Acceptance 8: a helper that is not the Registry's current authority for its
// Agent never reads the store, whatever its session, and writes nothing.
func TestNonCurrentClaudeHelperCannotAnswerStoredOriginal(t *testing.T) {
	for _, test := range []struct {
		name  string
		route func(*testing.T, *claudeCoordinationTestFixture) coremetadata.AgentRouteRef
	}{
		{"previous helper after compact", func(_ *testing.T, f *claudeCoordinationTestFixture) coremetadata.AgentRouteRef { return f.route }},
		{"replaced lease process", func(t *testing.T, f *claudeCoordinationTestFixture) coremetadata.AgentRouteRef {
			return changedClaudeRoute(t, f, false, func(a *coremetadata.ClaudeAuthorityRef) { a.LeaseProcess.Start = "test:unregistered-helper" })
		}},
		{"different registration generation", func(t *testing.T, f *claudeCoordinationTestFixture) coremetadata.AgentRouteRef {
			return changedClaudeRoute(t, f, false, func(a *coremetadata.ClaudeAuthorityRef) { a.RegistrationGeneration = "unregistered-registration" })
		}},
		{"different provider process", func(t *testing.T, f *claudeCoordinationTestFixture) coremetadata.AgentRouteRef {
			return changedClaudeRoute(t, f, false, func(a *coremetadata.ClaudeAuthorityRef) { a.Process.Start = "test:unregistered-provider" })
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, store, original, current, broker := compactedHelperFixture(t)
			route := test.route(t, f)
			if route.Same(current) || route.SessionIncarnation() != current.SessionIncarnation() {
				t.Fatal("fixture: route is current or left the session")
			}
			if _, _, err := broker.StoredOriginal(original.MessageRef, route); !errors.Is(err, errClaudeHelperNotCurrent) {
				t.Fatalf("fence let a non-current helper read the store: %v", err)
			}
			reply := explicitTestReply(original, "answer")
			before := storeFileBytes(t, store)
			got := newClaudeCoordinationHub().commitExplicitReply(reply, route, broker)
			if got.Kind != "reply-refused" || got.Reason != "invalid-explicit-reply-correlation" {
				t.Fatalf("non-current helper answered: %+v", got)
			}
			if !bytes.Equal(before, storeFileBytes(t, store)) {
				t.Fatal("non-current helper wrote the store")
			}
			if _, found, err := store.Reply(original.MessageRef); err != nil || found {
				t.Fatalf("non-current helper stored a reply: found=%t err=%v", found, err)
			}
			// Control: the Registry-current helper answers the same reply.
			if got := newClaudeCoordinationHub().commitExplicitReply(reply, current, broker); got.Kind != "reply-accepted" {
				t.Fatalf("current helper refused: %+v", got)
			}
		})
	}
}

// Acceptance 8, live broker: the real Registry fence and store view. Only the
// registration generation changes, so the lease and provider processes stay
// live and the live Current proof holds for the re-registered helper.
func TestLiveClaudeBrokerReadsStoredOriginalOnlyForRegistryCurrentHelper(t *testing.T) {
	f := newClaudeCoordinationTestFixture(t)
	live, err := newLiveClaudeDialogueBroker(f.registryPath)
	if err != nil {
		t.Fatal(err)
	}
	store := messagestore.NewStore(filepath.Dir(filepath.Dir(f.registryPath)))
	original := *dialogueForRoute("original-live", f.route, time.Now().UTC()).BrokerEnvelope
	original.Target.Incarnation = f.route.SessionIncarnation()
	original.Source = original.Target
	if _, _, err := store.PutAccepted(original, "claude-coordination"); err != nil {
		t.Fatal(err)
	}
	if err := live.MarkHandoff(original); err != nil {
		t.Fatal(err)
	}
	if err := live.MarkDelivered(original, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	previous := f.route
	route := changedClaudeRoute(t, f, true, func(a *coremetadata.ClaudeAuthorityRef) { a.RegistrationGeneration = "compacted-registration" })
	if record, found, err := live.StoredOriginal(original.MessageRef, route); err != nil || !found ||
		record.Envelope != original || record.Delivery.State != coremessage.StateDelivered {
		t.Fatalf("current helper could not read the delivered original: %+v found=%t err=%v", record.Delivery, found, err)
	}
	unregistered := changedClaudeRoute(t, f, false, func(a *coremetadata.ClaudeAuthorityRef) { a.LeaseProcess.Start = "test:unregistered-helper" })
	for _, stale := range []coremetadata.AgentRouteRef{previous, unregistered} {
		if _, found, err := live.StoredOriginal(original.MessageRef, stale); !errors.Is(err, errClaudeHelperNotCurrent) || found {
			t.Fatalf("live fence let a non-current helper read the store: found=%t err=%v", found, err)
		}
	}
	reply := explicitTestReply(original, "answer after compact")
	before := storeFileBytes(t, store)
	if got := newClaudeCoordinationHub().commitExplicitReply(reply, previous, live); got.Kind != "reply-refused" || got.Reason != "invalid-explicit-reply-correlation" {
		t.Fatalf("previous helper answered through the live broker: %+v", got)
	}
	if !bytes.Equal(before, storeFileBytes(t, store)) {
		t.Fatal("previous helper wrote the store")
	}
	got := newClaudeCoordinationHub().commitExplicitReply(reply, route, live)
	if got.Kind != "reply-accepted" || !got.ReplyCreated {
		t.Fatalf("current helper refused through the live broker: %+v", got)
	}
	if stored, found, err := store.Reply(original.MessageRef); err != nil || !found || stored.Envelope.MessageRef != reply.MessageRef {
		t.Fatalf("live reply not stored: found=%t err=%v", found, err)
	}
}
