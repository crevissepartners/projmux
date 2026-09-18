package agentmessage

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
)

// capacityOriginal is the request a reply answers. It is addressed to Codex and
// is the oldest record in the fixture, so a prune that does not pin it reclaims
// it before anything else: the delivered-to-Claude protection set does not
// cover it.
func capacityOriginal() coremessage.Envelope {
	original := storeEnvelope(1)
	original.AcceptedAt = storeTestNow.Add(-3 * time.Hour)
	original.Deadline = storeTestNow.Add(time.Hour)
	return original
}

func capacityAcceptedRecord(t *testing.T, envelope coremessage.Envelope, adapter string) Record {
	t.Helper()
	delivery, changed := coremessage.Reduce(coremessage.Delivery{}, envelope, coremessage.Event{Kind: coremessage.EventAccept,
		MessageRef: envelope.MessageRef, ConversationRef: envelope.ConversationRef, Target: envelope.Target,
		ObservedAt: envelope.AcceptedAt})
	if !changed {
		t.Fatalf("accept %s", envelope.MessageRef)
	}
	return Record{Envelope: envelope, Delivery: delivery, Adapter: adapter}
}

func capacityTerminalRecord(t *testing.T, envelope coremessage.Envelope, adapter string, kind coremessage.EventKind, reason string) Record {
	t.Helper()
	record := capacityAcceptedRecord(t, envelope, adapter)
	delivery, changed := coremessage.Reduce(record.Delivery, envelope, coremessage.Event{Kind: kind,
		MessageRef: envelope.MessageRef, ConversationRef: envelope.ConversationRef, Target: envelope.Target,
		Reason: reason, ObservedAt: envelope.AcceptedAt.Add(time.Minute)})
	if !changed {
		t.Fatalf("terminal %s", envelope.MessageRef)
	}
	record.Delivery = delivery
	return record
}

// capacityFixture fills a store to exactly maxRecords with the original, any
// extra records the caller supplies, and fillers. Terminal fillers went
// terminal well inside the retention window, so only the record limit can
// reclaim them and the reclaim reason is capacity rather than retention.
func capacityFixture(t *testing.T, terminalFillers bool, extra ...Record) *Store {
	t.Helper()
	store := NewStoreAt(filepath.Join(t.TempDir(), "messages.json"))
	store.now = func() time.Time { return storeTestNow }
	original := capacityOriginal()
	records := append([]Record{capacityTerminalRecord(t, original, "codex-inbox", coremessage.EventDeliver, "delivered")}, extra...)
	for i := len(records); i < maxRecords; i++ {
		filler := storeEnvelope(i + 100)
		filler.AcceptedAt = storeTestNow.Add(-2 * time.Hour).Add(time.Duration(i) * time.Nanosecond)
		filler.Deadline = filler.AcceptedAt.Add(time.Hour)
		if terminalFillers {
			records = append(records, capacityTerminalRecord(t, filler, "codex-inbox", coremessage.EventDeliver, "filler"))
			continue
		}
		// A past-deadline non-terminal filler would be expired by prune and so be
		// reclaimable; a future deadline keeps it genuinely non-reclaimable.
		filler.Deadline = storeTestNow.Add(time.Hour)
		records = append(records, capacityAcceptedRecord(t, filler, "codex-inbox"))
	}
	seedStore(t, store, records)
	return store
}

func capacityStoreRefs(t *testing.T, store *Store) []string {
	t.Helper()
	var refs []string
	if err := store.withLock(func() error {
		state, err := store.loadLocked()
		if err != nil {
			return err
		}
		for _, record := range state.Records {
			refs = append(refs, record.Envelope.MessageRef)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(refs)
	return refs
}

// TestFirstExplicitReplyAttemptReclaimsRoomInAFullStore covers acceptance 1, 2
// and 4: a first attempt on a full store is accepted, the original it answers
// survives the prune that made room, and the record that was reclaimed instead
// reaches the history log with reason capacity.
func TestFirstExplicitReplyAttemptReclaimsRoomInAFullStore(t *testing.T) {
	t.Parallel()
	store := capacityFixture(t, true)
	original := capacityOriginal()
	reply, created, err := store.PutReply(original.MessageRef, "message-reply-first", "first attempt",
		original.Target, original.Source, storeTestNow, original.Deadline)
	if err != nil || !created {
		t.Fatalf("first attempt in a full store = created %t err %v", created, err)
	}
	if reply.Envelope.ReplyTo != original.MessageRef || reply.Adapter != "claude-coordination" {
		t.Fatalf("first attempt lost its correlation: %+v", reply.Envelope)
	}
	if _, found, err := store.Get(original.MessageRef); err != nil || !found {
		t.Fatalf("the prune reclaimed the original it made room for: found=%t err=%v", found, err)
	}
	// The oldest unprotected terminal filler goes instead of the older original.
	if _, found, err := store.Get("message-101"); err != nil || found {
		t.Fatalf("oldest unprotected filler retained = %t, %v", found, err)
	}
	refs := capacityStoreRefs(t, store)
	if len(refs) != maxRecords {
		t.Fatalf("store holds %d records, want %d", len(refs), maxRecords)
	}
	lines := readHistoryLines(t, filepath.Join(filepath.Dir(store.Path()), historyFileName))
	if len(lines) != 1 {
		t.Fatalf("history lines = %d, want 1", len(lines))
	}
	if ref := historyString(t, lines[0], "messageRef"); ref != "message-101" {
		t.Fatalf("history messageRef = %q, want message-101", ref)
	}
	if reason := historyString(t, lines[0], "reason"); reason != reclaimCapacity {
		t.Fatalf("history reason = %q, want %q", reason, reclaimCapacity)
	}
}

// TestPruneRecordsPinsTheOriginalAndEveryAttemptOnIt covers acceptance 2 at the
// prune itself: pinning an original also protects every stored attempt that
// names it, and without the pin all three records are reclaimable.
func TestPruneRecordsPinsTheOriginalAndEveryAttemptOnIt(t *testing.T) {
	t.Parallel()
	original := storeEnvelope(1)
	original.AcceptedAt = storeTestNow.Add(-48 * time.Hour)
	original.Deadline = original.AcceptedAt.Add(time.Hour)
	attempt := replyEnvelope(original, "message-reply-old", "earlier attempt", original.Target, original.Source,
		original.AcceptedAt, original.Deadline)
	unrelated := storeEnvelope(2)
	unrelated.AcceptedAt = storeTestNow.Add(-48 * time.Hour)
	unrelated.Deadline = unrelated.AcceptedAt.Add(time.Hour)
	build := func() []Record {
		return []Record{
			capacityTerminalRecord(t, original, "codex-inbox", coremessage.EventDeliver, "delivered"),
			capacityTerminalRecord(t, attempt, "claude-coordination", coremessage.EventFail, "provider-write-zero"),
			capacityTerminalRecord(t, unrelated, "codex-inbox", coremessage.EventDeliver, "delivered"),
		}
	}
	kept, reclaimed := pruneRecords(build(), storeTestNow, original.MessageRef)
	if len(kept) != 2 || kept[0].Envelope.MessageRef != original.MessageRef || kept[1].Envelope.MessageRef != attempt.MessageRef {
		t.Fatalf("pinned original and its attempt were not kept: %+v", kept)
	}
	if len(reclaimed) != 1 || reclaimed[0].Record.Envelope.MessageRef != unrelated.MessageRef ||
		reclaimed[0].Reason != reclaimRetention {
		t.Fatalf("reclaimed = %+v, want only the unrelated record", reclaimed)
	}
	if kept, reclaimed := pruneRecords(build(), storeTestNow); len(kept) != 0 || len(reclaimed) != 3 {
		t.Fatalf("without the pin the same records are not all reclaimable: kept=%d reclaimed=%d", len(kept), len(reclaimed))
	}
}

// TestRecoveryReplyAttemptStillRefusesAFullStore covers acceptance 3: a second
// attempt following a known-zero failure is recovery, so it is refused at
// capacity instead of discarding the history it recovers from.
func TestRecoveryReplyAttemptStillRefusesAFullStore(t *testing.T) {
	t.Parallel()
	original := capacityOriginal()
	attempt := replyEnvelope(original, "message-reply-zero", "zero write", original.Target, original.Source,
		storeTestNow, original.Deadline)
	store := capacityFixture(t, true, capacityTerminalRecord(t, attempt, "claude-coordination",
		coremessage.EventFail, "provider-write-zero"))
	before, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	refsBefore := capacityStoreRefs(t, store)
	if _, created, err := store.PutReply(original.MessageRef, "message-reply-second", "corrected",
		original.Target, original.Source, storeTestNow, original.Deadline); !errors.Is(err, ErrCapacity) || created {
		t.Fatalf("recovery attempt in a full store = created %t err %v", created, err)
	}
	after, err := os.ReadFile(store.Path())
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("refused recovery attempt changed the store: err=%v", err)
	}
	refsAfter := capacityStoreRefs(t, store)
	if len(refsAfter) != len(refsBefore) {
		t.Fatalf("record count changed: %d -> %d", len(refsBefore), len(refsAfter))
	}
	for i := range refsBefore {
		if refsBefore[i] != refsAfter[i] {
			t.Fatalf("ref set changed at %d: %q -> %q", i, refsBefore[i], refsAfter[i])
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(store.Path()), historyFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused recovery attempt wrote history: %v", err)
	}
}

// TestFirstExplicitReplyAttemptRefusesWhenNothingIsReclaimable covers
// acceptance 5: making room is the record limit's existing rule, not an
// exemption from it, so a full store of non-terminal records still refuses.
func TestFirstExplicitReplyAttemptRefusesWhenNothingIsReclaimable(t *testing.T) {
	t.Parallel()
	store := capacityFixture(t, false)
	original := capacityOriginal()
	before, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := store.PutReply(original.MessageRef, "message-reply-first", "first attempt",
		original.Target, original.Source, storeTestNow, original.Deadline); !errors.Is(err, ErrCapacity) || created {
		t.Fatalf("first attempt with nothing reclaimable = created %t err %v", created, err)
	}
	after, err := os.ReadFile(store.Path())
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("refused first attempt changed the store: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(store.Path()), historyFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused first attempt wrote history: %v", err)
	}
}
