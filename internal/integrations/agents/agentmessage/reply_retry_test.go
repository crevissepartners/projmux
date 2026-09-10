package agentmessage

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
)

func retryStoreFixture(t *testing.T) (*Store, coremessage.Envelope) {
	t.Helper()
	store := NewStoreAt(filepath.Join(t.TempDir(), "messages.json"))
	store.now = func() time.Time { return storeTestNow }
	original := storeEnvelope(1)
	original.Source.Provider, original.Target.Provider = "claude", "claude"
	if _, _, err := store.PutAccepted(original, "claude-coordination"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Apply(original.MessageRef, coremessage.Event{Kind: coremessage.EventDeliver,
		MessageRef: original.MessageRef, ConversationRef: original.ConversationRef, Target: original.Target,
		ObservedAt: original.AcceptedAt}); err != nil {
		t.Fatal(err)
	}
	return store, original
}

func retryStoreReply(t *testing.T, store *Store, original coremessage.Envelope, ref, payload string) Record {
	t.Helper()
	record, created, err := store.PutReply(original.MessageRef, ref, payload, original.Target, original.Source,
		original.AcceptedAt, original.Deadline)
	if err != nil || !created {
		t.Fatalf("PutReply: created=%t err=%v", created, err)
	}
	return record
}

func retryStoreOutcome(t *testing.T, store *Store, record Record, kind coremessage.EventKind, reason string, unknown bool) Record {
	t.Helper()
	updated, changed, err := store.Apply(record.Envelope.MessageRef, coremessage.Event{Kind: kind,
		MessageRef: record.Envelope.MessageRef, ConversationRef: record.Envelope.ConversationRef, Target: record.Envelope.Target,
		ObservedAt: record.Envelope.AcceptedAt, Reason: reason, OutcomeUnknown: unknown})
	if err != nil || !changed {
		t.Fatalf("Apply: changed=%t err=%v", changed, err)
	}
	return updated
}

func TestStoreExplicitReplyKnownZeroManualRetrySurvivesReload(t *testing.T) {
	store, original := retryStoreFixture(t)
	failed := retryStoreOutcome(t, store, retryStoreReply(t, store, original, "reply-failed", "too large"),
		coremessage.EventFail, "provider-frame-too-large: frameBytes=8193 limitBytes=8192", false)
	store = NewStoreAt(store.Path())
	store.now = func() time.Time { return storeTestNow }
	replay, created, err := store.PutReply(original.MessageRef, failed.Envelope.MessageRef, failed.Envelope.Payload,
		original.Target, original.Source, original.AcceptedAt, original.Deadline)
	if err != nil || created || replay != failed {
		t.Fatalf("same-ref replay changed failed attempt: created=%t err=%v", created, err)
	}
	for _, ref := range []string{failed.Envelope.MessageRef, original.MessageRef} {
		if _, _, err := store.PutReply(original.MessageRef, ref, "corrected", original.Target, original.Source,
			original.AcceptedAt, original.Deadline); !errors.Is(err, coremessage.ErrRetryMismatch) {
			t.Fatalf("immutable/global ref collision accepted: %v", err)
		}
	}
	retried := retryStoreReply(t, store, original, "reply-manual", "corrected")
	if retried.Envelope.ReplyTo != original.MessageRef || retried.Envelope.ConversationRef != original.ConversationRef ||
		retried.Envelope.Source != original.Target || retried.Envelope.Target != original.Source ||
		retried.Envelope.Authority != coremessage.PeerAuthority() || retried.Envelope.Deadline != original.Deadline {
		t.Fatal("retry changed original correlation or authority")
	}
	delivered := retryStoreOutcome(t, store, retried, coremessage.EventDeliver, "provider-pipe-full-frame", false)
	store = NewStoreAt(store.Path())
	store.now = func() time.Time { return storeTestNow }
	if _, _, err := store.PutReply(original.MessageRef, "reply-duplicate", "corrected", original.Target, original.Source,
		original.AcceptedAt, original.Deadline); err == nil {
		t.Fatal("delivered reply allowed another ref after reload")
	} else {
		var conflict *ReplyConflictError
		if !errors.As(err, &conflict) || conflict.Previous != delivered {
			t.Fatal("refusal lost previous ref and delivery cause")
		}
	}
	for _, want := range []Record{failed, delivered} {
		if got, found, err := store.Get(want.Envelope.MessageRef); err != nil || !found || got != want {
			t.Fatal("manual retry deleted or rewrote an attempt")
		}
	}
	if got, _, err := store.Get(original.MessageRef); err != nil || got.Envelope != original {
		t.Fatal("manual retry rewrote original")
	}
}

func TestStoreExplicitReplyNonzeroUnknownAndExpiredNeverRetry(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		kind         coremessage.EventKind
		unknown      bool
	}{
		{"delivered", "provider-pipe-full-frame", coremessage.EventDeliver, false},
		{"partial", "provider-write-partial", coremessage.EventFail, true},
		{"partial without unknown flag", "provider-write-partial", coremessage.EventFail, false},
		{"unknown", "provider-handoff-outcome-unknown", coremessage.EventFail, true},
		{"zero with unknown flag", "provider-write-zero", coremessage.EventFail, true},
		{"unrecognized failure", "future-failure", coremessage.EventFail, false},
		{"invalid size evidence", "provider-frame-too-large: frameBytes=8193 limitBytes=9000", coremessage.EventFail, false},
		{"refused without zero evidence", "provider-refused", coremessage.EventRefuse, false},
		{"expired", "deadline-expired", coremessage.EventExpire, false},
		{"stale", "target-activation-stale", coremessage.EventStale, false},
		{"pending", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, original := retryStoreFixture(t)
			previous := retryStoreReply(t, store, original, "previous", "payload")
			if tc.kind != "" {
				previous = retryStoreOutcome(t, store, previous, tc.kind, tc.reason, tc.unknown)
			}
			store = NewStoreAt(store.Path())
			store.now = func() time.Time { return storeTestNow }
			if replay, created, err := store.PutReply(original.MessageRef, "previous", "payload", original.Target, original.Source,
				original.AcceptedAt, original.Deadline); err != nil || created || replay != previous {
				t.Fatal("same ref must only return immutable receipt")
			}
			_, created, err := store.PutReply(original.MessageRef, "new-ref", "corrected", original.Target, original.Source,
				original.AcceptedAt, original.Deadline)
			var conflict *ReplyConflictError
			if created || !errors.As(err, &conflict) || conflict.Previous != previous {
				t.Fatalf("unsafe retry or missing cause: created=%t err=%v", created, err)
			}
		})
	}
}

func TestStoreExplicitReplyRetryKeepsDeadlineAndExactRoute(t *testing.T) {
	for _, mode := range []string{"original expired", "reply expired", "deadline extended", "source stale", "target stale", "codex outcome"} {
		t.Run(mode, func(t *testing.T) {
			store, original := retryStoreFixture(t)
			if mode == "codex outcome" {
				original.Source.Provider = "codex"
				state, err := store.loadLocked()
				if err != nil {
					t.Fatal(err)
				}
				state.Records[0].Envelope = original
				if err := store.writeLocked(state); err != nil {
					t.Fatal(err)
				}
			}
			retryStoreOutcome(t, store, retryStoreReply(t, store, original, "failed", "payload"), coremessage.EventFail, "provider-write-zero", false)
			source, target, accepted, deadline := original.Target, original.Source, original.AcceptedAt, original.Deadline
			switch mode {
			case "original expired":
				store.now = func() time.Time { return original.Deadline }
			case "reply expired":
				deadline = storeTestNow.Add(-time.Second)
			case "deadline extended":
				deadline = original.Deadline.Add(time.Second)
			case "source stale":
				source.Incarnation = "old"
			case "target stale":
				target.ActivationGeneration = "old"
			}
			if _, created, err := store.PutReply(original.MessageRef, "retry", "corrected", source, target, accepted, deadline); err == nil || created {
				t.Fatalf("%s allowed retry", mode)
			}
		})
	}
}

func TestStoreExplicitReplyConcurrentAttemptsAndCapacityPreserveReceipts(t *testing.T) {
	store, original := retryStoreFixture(t)
	retryStoreOutcome(t, store, retryStoreReply(t, store, original, "failed", "payload"), coremessage.EventFail, "provider-write-zero", false)
	var wg sync.WaitGroup
	created := make(chan string, 8)
	for i := range 8 {
		wg.Go(func() {
			ref := fmt.Sprintf("retry-%d", i)
			if _, fresh, _ := store.PutReply(original.MessageRef, ref, "corrected", original.Target, original.Source,
				original.AcceptedAt, original.Deadline); fresh {
				created <- ref
			}
		})
	}
	wg.Wait()
	close(created)
	if len(created) != 1 {
		t.Fatalf("concurrent dispatch permits=%d", len(created))
	}
	ref := <-created
	reply, _, _ := store.Get(ref)
	retryStoreOutcome(t, store, reply, coremessage.EventDeliver, "provider-pipe-full-frame", false)
	for i := 2; i <= maxRecords; i++ {
		_, _, _ = store.PutAccepted(storeEnvelope(i), "codex-inbox")
	}
	for _, ref := range []string{original.MessageRef, "failed", ref} {
		if _, found, err := store.Get(ref); err != nil || !found {
			t.Fatal("capacity pruned active reply correlation")
		}
	}
	if _, _, err := store.PutReply(original.MessageRef, "duplicate", "corrected", original.Target, original.Source,
		original.AcceptedAt, original.Deadline); err == nil {
		t.Fatal("capacity reopened delivered reply")
	}
}
