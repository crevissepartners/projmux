package app

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
)

// storedOriginalStubBroker is a broker whose durable store view returns a
// fixed answer, so each outcome of the read names its own refusal.
type storedOriginalStubBroker struct {
	failingClaudeDialogueBroker
	record messagestore.Record
	found  bool
	err    error
}

func (b *storedOriginalStubBroker) StoredOriginal(string, coremetadata.AgentRouteRef) (messagestore.Record, bool, error) {
	return b.record, b.found, b.err
}

// TestClaudeExplicitReplyNamesEachRefusalByItsCause pins C-2 at every place
// a Claude helper refuses an explicit reply: reading the original (① ②), the
// one judgement of a pushed or stored original (③), and the broker commit
// (④, which also carries the store's refusals). The correlation token is kept
// for a reply that does not match the original; every other cause, which asks
// the reader to do something else, has its own token.
func TestClaudeExplicitReplyNamesEachRefusalByItsCause(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	now := time.Now().UTC()
	correlation := coremessage.EnvelopeRefusal(coremessage.ReasonCorrelationInvalid, "reply route or conversation mismatch")
	for _, test := range []struct {
		name string
		// stored answers the original from the durable store instead of the
		// helper's pushed messages; nil pushes it.
		stored   *storedOriginalStubBroker
		noReader bool
		noBroker bool
		closed   bool
		at       time.Duration
		current  func() bool
		commit   error
		message  func(*claudeCoordinationMessage)
		reply    func(*coremessage.Envelope)
		want     string
	}{
		// ① and ②: the original is not one this helper pushed.
		{name: "no store view", noReader: true, want: "broker-reply-store-unavailable"},
		{name: "helper not current", stored: &storedOriginalStubBroker{err: errClaudeHelperNotCurrent},
			want: "broker-reply-helper-not-current"},
		{name: "store unreadable", stored: &storedOriginalStubBroker{err: errors.New("permission denied")},
			want: "broker-reply-store-unavailable"},
		{name: "store busy", stored: &storedOriginalStubBroker{err: messagestore.ErrBusy}, want: "broker-reply-store-busy"},
		{name: "store malformed", stored: &storedOriginalStubBroker{err: messagestore.ErrMalformedStore},
			want: "broker-reply-store-malformed"},
		{name: "original not stored", stored: &storedOriginalStubBroker{}, want: "broker-reply-original-not-found"},
		{name: "stored original not delivered", stored: &storedOriginalStubBroker{found: true},
			want: coremessage.ReasonBrokerReplyOriginalNotDelivered},
		// ③: the one judgement.
		{name: "helper closed", closed: true, want: "broker-reply-helper-closed"},
		{name: "no broker", noBroker: true, want: "broker-reply-unavailable"},
		{name: "original without envelope", message: func(m *claudeCoordinationMessage) { m.envelope.BrokerEnvelope = nil },
			want: "broker-reply-original-without-envelope"},
		{name: "original not delivered", message: func(m *claudeCoordinationMessage) { m.delivery.State = agentdelivery.StateFailed },
			want: coremessage.ReasonBrokerReplyOriginalNotDelivered},
		{name: "reply source is not this helper", reply: func(r *coremessage.Envelope) { r.Source.PaneUID = "pane-replaced" },
			want: "explicit-reply-source-route-stale"},
		{name: "correlation mismatch", reply: func(r *coremessage.Envelope) { r.ConversationRef = "conversation-other" },
			want: coremessage.ReasonExplicitReplyCorrelation},
		{name: "another Agent", reply: func(r *coremessage.Envelope) { r.Target.AgentUID = "agent-other" },
			want: coremessage.ReasonExplicitReplyCorrelation},
		{name: "conversation changed", reply: func(r *coremessage.Envelope) { r.Target.Incarnation = "route-other" },
			want: coremessage.ReasonExplicitReplyConversationChanged},
		{name: "reply envelope invalid", reply: func(r *coremessage.Envelope) {
			r.Payload = strings.Repeat("x", coremessage.MaxPayloadBytes+1)
		}, want: "invalid-explicit-reply-envelope"},
		{name: "deadline expired", at: 2 * time.Minute, want: "explicit-reply-deadline-expired"},
		{name: "route stale", current: func() bool { return false }, want: "explicit-reply-route-stale"},
		// ④: the broker commit, each cause it or the store found first.
		{name: "commit without store", commit: errClaudeReplyStoreUnavailable, want: "broker-reply-store-unavailable"},
		{name: "commit after the original expired", commit: errClaudeReplyOriginalExpired, want: "explicit-reply-deadline-expired"},
		{name: "commit on a stale route", commit: errClaudeReplyRouteStale, want: "explicit-reply-route-stale"},
		{name: "commit correlation mismatch", commit: correlation, want: coremessage.ReasonExplicitReplyCorrelation},
		{name: "commit conversation changed", commit: coremessage.ErrReplyConversationChanged,
			want: coremessage.ReasonExplicitReplyConversationChanged},
		{name: "store original not delivered", commit: &messagestore.ReplyConflictError{
			Reason: coremessage.ReasonBrokerReplyOriginalNotDelivered}, want: coremessage.ReasonBrokerReplyOriginalNotDelivered},
	} {
		t.Run(test.name, func(t *testing.T) {
			hub := newClaudeCoordinationHub()
			hub.now = func() time.Time { return now.Add(test.at) }
			hub.closed = test.closed
			original := dialogueForRoute("message-refusal", fixture.route, now)
			reply := explicitTestReply(*original.BrokerEnvelope, "answer")
			if test.reply != nil {
				test.reply(&reply)
			}
			failing := &failingClaudeDialogueBroker{current: test.current, replyErr: test.commit}
			var broker claudeDialogueBroker = failing
			var message *claudeCoordinationMessage
			switch {
			case test.stored != nil:
				test.stored.record.Envelope = *original.BrokerEnvelope
				broker = test.stored
			case test.noReader:
			default:
				message = &claudeCoordinationMessage{envelope: original,
					delivery: agentdelivery.Delivery{MessageRef: original.MessageRef, State: agentdelivery.StateDelivered}}
				if test.message != nil {
					test.message(message)
				}
				hub.messages[original.MessageRef] = message
			}
			if test.noBroker {
				broker = nil
			}
			got := hub.commitExplicitReply(reply, fixture.route, broker)
			if got.Kind != "reply-refused" || got.Reason != test.want {
				t.Fatalf("refusal = %s %q, want %q", got.Kind, got.Reason, test.want)
			}
			if message != nil && message.replyReserved {
				t.Fatal("a refusal left the reply reserved")
			}
			if test.commit == nil && failing.replies != 0 {
				t.Fatalf("a refusal before the commit reached the broker %d times", failing.replies)
			}
		})
	}
}

// TestClaudeExplicitReplyJudgementConditionsHaveDistinctTokens is C-2
// acceptance 1 for the judgement that once reported six conditions as one
// token: no two of them share one now.
func TestClaudeExplicitReplyJudgementConditionsHaveDistinctTokens(t *testing.T) {
	t.Parallel()
	tokens := []string{
		"broker-reply-helper-closed",
		"broker-reply-unavailable",
		"broker-reply-original-without-envelope",
		coremessage.ReasonBrokerReplyOriginalNotDelivered,
		"explicit-reply-source-route-stale",
		coremessage.ReasonExplicitReplyCorrelation,
	}
	seen := make(map[string]bool, len(tokens))
	for _, token := range tokens {
		if seen[token] {
			t.Fatalf("two judgement conditions share %q", token)
		}
		seen[token] = true
	}
}

// TestLiveClaudeBrokerCommitReplyKeepsEachCause pins site ④ at its source:
// the live broker no longer folds a missing store, a mismatch, an expired
// original, and a stale route into one correlation refusal.
func TestLiveClaudeBrokerCommitReplyKeepsEachCause(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	// CommitReply reads the real clock, and the subtests resume only when the
	// package frees a parallel slot, which can be minutes after this line. The
	// live original's deadline is therefore one no run reaches, so whether it
	// is still live never depends on how long that took.
	original := dialogueEnvelope("message-live-commit", now.Add(100*365*24*time.Hour)).BrokerEnvelope
	store := messagestore.NewNonblockingStore(t.TempDir())
	live := &liveClaudeDialogueBroker{registryPath: filepath.Join(t.TempDir(), "registry.json"), store: store}
	expired := *original
	expired.AcceptedAt, expired.Deadline = now.Add(-2*time.Minute), now.Add(-time.Minute)
	for _, test := range []struct {
		name     string
		broker   *liveClaudeDialogueBroker
		original coremessage.Envelope
		reply    func(*coremessage.Envelope)
		want     error
	}{
		{name: "nil broker", original: *original, want: errClaudeReplyStoreUnavailable},
		{name: "no store", broker: &liveClaudeDialogueBroker{}, original: *original, want: errClaudeReplyStoreUnavailable},
		{name: "correlation mismatch", broker: live, original: *original,
			reply: func(r *coremessage.Envelope) { r.ConversationRef = "conversation-other" }, want: coremessage.ErrInvalidEnvelope},
		{name: "conversation changed", broker: live, original: *original,
			reply: func(r *coremessage.Envelope) { r.Target.Incarnation = "route-other" }, want: coremessage.ErrReplyConversationChanged},
		{name: "original expired", broker: live, original: expired, want: errClaudeReplyOriginalExpired},
		{name: "route not current", broker: live, original: *original, want: errClaudeReplyRouteStale},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			reply := explicitTestReply(test.original, "answer")
			if test.reply != nil {
				test.reply(&reply)
			}
			created, err := test.broker.CommitReply(test.original, reply)
			if created || !errors.Is(err, test.want) {
				t.Fatalf("CommitReply = (%t, %v), want %v", created, err, test.want)
			}
			if test.want != coremessage.ErrInvalidEnvelope && test.want != coremessage.ErrReplyConversationChanged &&
				errors.Is(err, coremessage.ErrInvalidEnvelope) {
				t.Fatalf("%v reads as an envelope correlation refusal", err)
			}
		})
	}
}
