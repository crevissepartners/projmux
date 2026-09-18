package agentmessage

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
)

// expirePruneRecord is a non-terminal record accepted two hours before
// storeTestNow with the given deadline. held moves it to held first; handoff
// addresses it to Claude and marks the provider handoff as observed, which is
// the only adapter allowed to carry that mark.
func expirePruneRecord(t *testing.T, index int, deadline time.Time, held, handoff bool) Record {
	t.Helper()
	envelope := storeEnvelope(index)
	envelope.AcceptedAt = storeTestNow.Add(-2 * time.Hour).Add(time.Duration(index) * time.Nanosecond)
	envelope.Deadline = deadline
	adapter := "codex-inbox"
	if handoff {
		envelope.Target.Provider = "claude"
		adapter = "claude-coordination"
	}
	record := capacityAcceptedRecord(t, envelope, adapter)
	record.HandoffObserved = handoff
	if held {
		delivery, changed := coremessage.Reduce(record.Delivery, envelope, coremessage.Event{Kind: coremessage.EventHold,
			MessageRef: envelope.MessageRef, ConversationRef: envelope.ConversationRef, Target: envelope.Target,
			Reason: "expire-prune-test-hold", ObservedAt: envelope.AcceptedAt.Add(time.Second)})
		if !changed {
			t.Fatalf("hold %s", envelope.MessageRef)
		}
		record.Delivery = delivery
	}
	return record
}

// expirePruneStore is a store whose clock is pinned to storeTestNow.
func expirePruneStore(t *testing.T) *Store {
	t.Helper()
	store := NewStoreAt(filepath.Join(t.TempDir(), "agent-messages", "messages.json"))
	store.now = func() time.Time { return storeTestNow }
	return store
}

// expirePruneReload reads a record back from disk through a fresh store, so the
// assertion sees what the prune committed rather than a returned copy.
func expirePruneReload(t *testing.T, store *Store, messageRef string) Record {
	t.Helper()
	record, found, err := NewStoreAt(store.Path()).Get(messageRef)
	if err != nil || !found {
		t.Fatalf("reload %s = (%t, %v)", messageRef, found, err)
	}
	return record
}

// TestPruneExpiresPastDeadlineNonTerminalRecords: an accepted and a held record
// whose deadlines have passed (one exactly at the prune clock, which Status and
// Claim also treat as passed) are expired by the prune that a new acceptance
// runs, observed at the prune clock rather than backdated to the deadline.
func TestPruneExpiresPastDeadlineNonTerminalRecords(t *testing.T) {
	t.Parallel()
	store := expirePruneStore(t)
	accepted := expirePruneRecord(t, 8101, storeTestNow.Add(-time.Hour), false, false)
	held := expirePruneRecord(t, 8102, storeTestNow, true, false)
	if held.Delivery.State != coremessage.StateHeld {
		t.Fatalf("fixture held state = %q", held.Delivery.State)
	}
	seedStore(t, store, []Record{accepted, held})

	if _, created, err := store.PutAccepted(storeEnvelope(7), "codex-inbox"); err != nil || !created {
		t.Fatalf("PutAccepted = (%t, %v)", created, err)
	}
	for _, seeded := range []Record{accepted, held} {
		got := expirePruneReload(t, store, seeded.Envelope.MessageRef)
		if got.Delivery.State != coremessage.StateExpired || got.Delivery.Reason != "deadline-expired" || got.Delivery.OutcomeUnknown {
			t.Fatalf("%s delivery = %+v, want expired/deadline-expired without unknown outcome", seeded.Envelope.MessageRef, got.Delivery)
		}
		if !got.Delivery.TerminalAt.Equal(storeTestNow) {
			t.Fatalf("%s terminalAt = %v, want the prune clock %v (deadline %v)", seeded.Envelope.MessageRef,
				got.Delivery.TerminalAt, storeTestNow, seeded.Envelope.Deadline)
		}
	}
	// Expired at the prune clock, so the same prune's retention rule did not
	// reclaim them and nothing reached the history log.
	if _, err := os.Stat(store.historyPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("history exists after a prune that only expired records: %v", err)
	}
}

// TestPruneFailsPastDeadlineRecordsWhoseHandoffWasObserved: after the provider
// handoff a passed deadline is an unknown outcome, the same event Status gives.
func TestPruneFailsPastDeadlineRecordsWhoseHandoffWasObserved(t *testing.T) {
	t.Parallel()
	store := expirePruneStore(t)
	handedOff := expirePruneRecord(t, 8201, storeTestNow.Add(-time.Hour), false, true)
	heldHandedOff := expirePruneRecord(t, 8202, storeTestNow.Add(-time.Minute), true, true)
	seedStore(t, store, []Record{handedOff, heldHandedOff})

	if _, created, err := store.PutAccepted(storeEnvelope(8), "codex-inbox"); err != nil || !created {
		t.Fatalf("PutAccepted = (%t, %v)", created, err)
	}
	for _, seeded := range []Record{handedOff, heldHandedOff} {
		got := expirePruneReload(t, store, seeded.Envelope.MessageRef)
		if got.Delivery.State != coremessage.StateFailed || got.Delivery.Reason != "provider-handoff-outcome-unknown" ||
			!got.Delivery.OutcomeUnknown || !got.HandoffObserved {
			t.Fatalf("%s = %+v handoff %t, want failed/provider-handoff-outcome-unknown with unknown outcome",
				seeded.Envelope.MessageRef, got.Delivery, got.HandoffObserved)
		}
		if !got.Delivery.TerminalAt.Equal(storeTestNow) {
			t.Fatalf("%s terminalAt = %v, want the prune clock %v", seeded.Envelope.MessageRef, got.Delivery.TerminalAt, storeTestNow)
		}
	}
}

// TestPruneLeavesUnexpiredNonTerminalRecordsAlone: a deadline even one
// nanosecond after the prune clock is not passed, whatever the state or
// handoff mark.
func TestPruneLeavesUnexpiredNonTerminalRecordsAlone(t *testing.T) {
	t.Parallel()
	build := func() []Record {
		return []Record{
			expirePruneRecord(t, 8301, storeTestNow.Add(time.Nanosecond), false, false),
			expirePruneRecord(t, 8302, storeTestNow.Add(time.Hour), true, false),
			expirePruneRecord(t, 8303, storeTestNow.Add(time.Minute), false, true),
		}
	}
	want := build()
	kept, reclaimed := pruneRecords(build(), storeTestNow)
	if len(reclaimed) != 0 || !reflect.DeepEqual(kept, want) {
		t.Fatalf("prune changed unexpired records:\n kept %+v\n want %+v\n reclaimed %+v", kept, want, reclaimed)
	}

	store := expirePruneStore(t)
	seedStore(t, store, build())
	if _, created, err := store.PutAccepted(storeEnvelope(9), "codex-inbox"); err != nil || !created {
		t.Fatalf("PutAccepted = (%t, %v)", created, err)
	}
	for _, seeded := range want {
		got := expirePruneReload(t, store, seeded.Envelope.MessageRef)
		if !reflect.DeepEqual(got.Delivery, seeded.Delivery) || got.HandoffObserved != seeded.HandoffObserved {
			t.Fatalf("%s delivery = %+v, want unchanged %+v", seeded.Envelope.MessageRef, got.Delivery, seeded.Delivery)
		}
	}
}

// TestExpiredRecordsReclaimedByPruneReachTheHistoryLog: a record the prune
// expires is an ordinary terminal record from then on, so it can be pushed out
// by the record limit at once and by the retention rule 24 hours after it
// expired, and either way it reaches the history log.
func TestExpiredRecordsReclaimedByPruneReachTheHistoryLog(t *testing.T) {
	t.Parallel()
	t.Run("capacity", func(t *testing.T) {
		t.Parallel()
		store := expirePruneStore(t)
		// Every record is non-terminal and past its deadline, so nothing is
		// reclaimable unless the prune expires them first. This is the same
		// shape TestStoreRefusesFullNonterminalCapacityWithoutChangingDisk
		// refuses with future deadlines; with past deadlines the store used to
		// refuse too, holding the slots forever.
		full := make([]Record, maxRecords)
		for i := range full {
			full[i] = expirePruneRecord(t, 8400+i, storeTestNow.Add(-time.Hour).Add(time.Duration(i)*time.Nanosecond), false, false)
		}
		for i, record := range full {
			if record.Delivery.State.Terminal() || record.Envelope.Deadline.After(storeTestNow) {
				t.Fatalf("fixture record %d is not a past-deadline non-terminal record: %+v", i, record.Delivery)
			}
		}
		seedStore(t, store, full)
		if refs := capacityStoreRefs(t, store); len(refs) != maxRecords {
			t.Fatalf("seeded store holds %d records, want %d", len(refs), maxRecords)
		}

		if _, created, err := store.PutAccepted(storeEnvelope(10), "codex-inbox"); err != nil || !created {
			t.Fatalf("PutAccepted into a store of expirable records = (%t, %v)", created, err)
		}
		lines := readHistoryLines(t, store.historyPath())
		if len(lines) != 1 {
			t.Fatalf("history lines = %d, want 1", len(lines))
		}
		line := lines[0]
		if got := historyString(t, line, "reason"); got != reclaimCapacity {
			t.Fatalf("reason = %q, want %q", got, reclaimCapacity)
		}
		if got := historyString(t, line, "state"); got != string(coremessage.StateExpired) {
			t.Fatalf("state = %q, want expired", got)
		}
		if got := historyString(t, line, "deliveryReason"); got != "deadline-expired" {
			t.Fatalf("deliveryReason = %q, want deadline-expired", got)
		}
		if got := historyString(t, line, "messageRef"); got != full[0].Envelope.MessageRef {
			t.Fatalf("reclaimed ref = %q, want the oldest %q", got, full[0].Envelope.MessageRef)
		}
		if got := historyString(t, line, "terminalAt"); got != storeTestNow.Format(time.RFC3339Nano) {
			t.Fatalf("terminalAt = %q, want the prune clock", got)
		}
		if refs := capacityStoreRefs(t, store); len(refs) != maxRecords {
			t.Fatalf("store holds %d records, want %d", len(refs), maxRecords)
		}
		if got := expirePruneReload(t, store, full[1].Envelope.MessageRef); got.Delivery.State != coremessage.StateExpired {
			t.Fatalf("kept record %s state = %q, want expired", full[1].Envelope.MessageRef, got.Delivery.State)
		}
	})
	t.Run("retention", func(t *testing.T) {
		t.Parallel()
		store := expirePruneStore(t)
		now := storeTestNow
		store.now = func() time.Time { return now }
		overdue := expirePruneRecord(t, 8700, storeTestNow.Add(-time.Hour), false, false)
		seedStore(t, store, []Record{overdue})
		put := func(index int) {
			t.Helper()
			envelope := storeEnvelope(index)
			envelope.AcceptedAt, envelope.Deadline = now, now.Add(time.Hour)
			if _, created, err := store.PutAccepted(envelope, "codex-inbox"); err != nil || !created {
				t.Fatalf("PutAccepted %d at %v = (%t, %v)", index, now, created, err)
			}
		}

		put(11)
		if got := expirePruneReload(t, store, overdue.Envelope.MessageRef); got.Delivery.State != coremessage.StateExpired ||
			!got.Delivery.TerminalAt.Equal(storeTestNow) {
			t.Fatalf("first prune = %+v, want expired at %v", got.Delivery, storeTestNow)
		}
		// Retention counts from the expiry, not the deadline: 24 hours after
		// the deadline has already passed here, 24 hours after expiry has not.
		now = storeTestNow.Add(terminalRetention)
		put(12)
		if _, found, err := store.Get(overdue.Envelope.MessageRef); err != nil || !found {
			t.Fatalf("record reclaimed before 24h from its expiry = (%t, %v)", found, err)
		}
		if _, err := os.Stat(store.historyPath()); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("history exists before any retention reclaim: %v", err)
		}

		now = storeTestNow.Add(terminalRetention + time.Second)
		put(13)
		lines := readHistoryLines(t, store.historyPath())
		if len(lines) != 1 {
			t.Fatalf("history lines = %d, want 1", len(lines))
		}
		line := lines[0]
		if got := historyString(t, line, "messageRef"); got != overdue.Envelope.MessageRef {
			t.Fatalf("reclaimed ref = %q, want %q", got, overdue.Envelope.MessageRef)
		}
		if got := historyString(t, line, "reason"); got != reclaimRetention {
			t.Fatalf("reason = %q, want %q", got, reclaimRetention)
		}
		if got := historyString(t, line, "state"); got != string(coremessage.StateExpired) {
			t.Fatalf("state = %q, want expired", got)
		}
		if got := historyString(t, line, "deliveryReason"); got != "deadline-expired" {
			t.Fatalf("deliveryReason = %q, want deadline-expired", got)
		}
		if got := historyString(t, line, "terminalAt"); got != storeTestNow.Format(time.RFC3339Nano) {
			t.Fatalf("terminalAt = %q, want the first prune clock", got)
		}
		if _, found, err := store.Get(overdue.Envelope.MessageRef); err != nil || found {
			t.Fatalf("retention-reclaimed record still in store = (%t, %v)", found, err)
		}
	})
}
