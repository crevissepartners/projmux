package agentmessage

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
)

// TestStorePutReplyAcceptsRoutesMirroringSessionOriginal pins what the store
// needs from a reply now that readers accept a session-scoped incarnation. The
// store has no authority to read either value, so it keeps comparing routes
// exactly: a reply mirroring a session-shaped original commits, and a reply
// in the other shape is refused without a write. The app builds the mirror.
func TestStorePutReplyAcceptsRoutesMirroringSessionOriginal(t *testing.T) {
	t.Parallel()
	const (
		sessionSource = "route-5e5510b0000000000000000000000000000a"
		sessionTarget = "route-5e5510b0000000000000000000000000000b"
	)
	for _, test := range []struct {
		name            string
		source, target  string
		wantCreated     bool
		wantErrorReason string
	}{
		{"mirrors session original", sessionTarget, sessionSource, true, ""},
		{"full source against session original", "route-target", sessionSource, false, "invalid-explicit-reply-correlation"},
		{"full target against session original", sessionTarget, "route-source", false, "invalid-explicit-reply-correlation"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := NewStoreAt(filepath.Join(t.TempDir(), "messages.json"))
			store.now = func() time.Time { return storeTestNow }
			original := storeEnvelope(1)
			original.Target.Provider = "claude"
			original.Source.Incarnation, original.Target.Incarnation = sessionSource, sessionTarget
			if _, _, err := store.PutAccepted(original, "claude-coordination"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.Apply(original.MessageRef, coremessage.Event{Kind: coremessage.EventDeliver,
				MessageRef: original.MessageRef, ConversationRef: original.ConversationRef, Target: original.Target,
				ObservedAt: original.AcceptedAt}); err != nil {
				t.Fatal(err)
			}
			source, target := original.Target, original.Source
			source.Incarnation, target.Incarnation = test.source, test.target
			record, created, err := store.PutReply(original.MessageRef, "message-reply", "answer", source, target,
				original.AcceptedAt, original.Deadline)
			if test.wantCreated {
				if err != nil || !created || record.Envelope.Source != original.Target || record.Envelope.Target != original.Source {
					t.Fatalf("PutReply = (%+v, %t, %v)", record.Envelope, created, err)
				}
				return
			}
			var conflict *ReplyConflictError
			if created || !errors.As(err, &conflict) || conflict.Reason != test.wantErrorReason {
				t.Fatalf("PutReply created=%t err=%v, want %s", created, err, test.wantErrorReason)
			}
			if _, found, err := store.Reply(original.MessageRef); err != nil || found {
				t.Fatalf("refused reply was stored: found=%t err=%v", found, err)
			}
		})
	}
}
