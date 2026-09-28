package agentmessage

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
)

// TestStorePutReplyNamesEachRefusalByItsCause pins the store's half of C-2.
// An original that never reached its target has nothing to answer, which is
// not a correlation mismatch; the correlation token is kept for a reply whose
// Agents do not match the original.
func TestStorePutReplyNamesEachRefusalByItsCause(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		undelivered bool
		at          time.Duration
		mutate      func(source, target *coremessage.Route)
		wantReason  string
	}{
		{name: "original not delivered", undelivered: true, wantReason: coremessage.ReasonBrokerReplyOriginalNotDelivered},
		{name: "another Agent", mutate: func(_, target *coremessage.Route) { target.AgentUID = "agent-other" },
			wantReason: coremessage.ReasonExplicitReplyCorrelation},
		{name: "another provider", mutate: func(source, _ *coremessage.Route) { source.Provider = "codex" },
			wantReason: coremessage.ReasonExplicitReplyCorrelation},
		{name: "another conversation", mutate: func(_, target *coremessage.Route) { target.Incarnation = "route-other" },
			wantReason: coremessage.ReasonExplicitReplyConversationChanged},
		{name: "original expired", at: 2 * time.Hour, wantReason: "explicit-reply-deadline-expired"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := NewStoreAt(filepath.Join(t.TempDir(), "messages.json"))
			store.now = func() time.Time { return storeTestNow }
			original := storeEnvelope(1)
			original.Target.Provider = "claude"
			if _, _, err := store.PutAccepted(original, "claude-coordination"); err != nil {
				t.Fatal(err)
			}
			if !test.undelivered {
				if _, _, err := store.Apply(original.MessageRef, coremessage.Event{Kind: coremessage.EventDeliver,
					MessageRef: original.MessageRef, ConversationRef: original.ConversationRef, Target: original.Target,
					ObservedAt: original.AcceptedAt}); err != nil {
					t.Fatal(err)
				}
			}
			store.now = func() time.Time { return storeTestNow.Add(test.at) }
			source, target := original.Target, original.Source
			if test.mutate != nil {
				test.mutate(&source, &target)
			}
			_, created, err := store.PutReply(original.MessageRef, "message-reply", "answer", source, target,
				original.AcceptedAt, original.Deadline)
			var conflict *ReplyConflictError
			if created || !errors.As(err, &conflict) || conflict.Reason != test.wantReason {
				t.Fatalf("PutReply created=%t err=%v, want %s", created, err, test.wantReason)
			}
			if _, found, err := store.Reply(original.MessageRef); err != nil || found {
				t.Fatalf("refused reply was stored: found=%t err=%v", found, err)
			}
		})
	}
}
