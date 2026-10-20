package agentmessage

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
)

func deliveredCapacityRecords(t *testing.T, count int) []Record {
	t.Helper()
	records := make([]Record, count)
	for i := range records {
		envelope := storeEnvelope(i)
		envelope.Target.Provider = "claude"
		envelope.AcceptedAt = storeTestNow.Add(-time.Hour).Add(time.Duration(i) * time.Second)
		records[i] = capacityTerminalRecord(t, envelope, "claude-coordination", coremessage.EventDeliver, "delivered")
	}
	return records
}

func deliveredCapacityStore(t *testing.T, records []Record) *Store {
	t.Helper()
	store := NewStoreAt(filepath.Join(t.TempDir(), "messages.json"))
	store.now = func() time.Time { return storeTestNow }
	seedStore(t, store, records)
	return store
}

func TestDeliveredCapacityPolicyOneTargetCannotBlockSends(t *testing.T) {
	records := deliveredCapacityRecords(t, 255)
	records = append(records, capacityAcceptedRecord(t, storeEnvelope(500), "codex-inbox"))
	store := deliveredCapacityStore(t, records)
	for _, target := range []string{"agent-other", "agent-target"} {
		envelope := storeEnvelope(600)
		envelope.MessageRef += "-" + target
		envelope.Target.AgentUID = target
		envelope.Target.Provider = "claude"
		if _, created, err := store.PutAccepted(envelope, "claude-coordination"); err != nil || !created {
			t.Fatalf("send to %s: created=%t err=%v", target, created, err)
		}
	}
	for _, index := range []int{0, 1} {
		if _, found, err := store.Get(fmt.Sprintf("message-%03d", index)); err != nil || found {
			t.Fatalf("oldest delivered %d retained=%t err=%v", index, found, err)
		}
	}
	// Exercise every member of the latest 64, after pressure, through durable reply admission.
	for i := 191; i < 255; i++ {
		original := records[i].Envelope
		if _, created, err := store.PutReply(original.MessageRef, fmt.Sprintf("message-reply-%d", i), "answer",
			original.Target, original.Source, storeTestNow, original.Deadline); err != nil || !created {
			t.Fatalf("latest-64 reply %d: created=%t err=%v", i, created, err)
		}
	}
}

func TestDeliveredCapacityPolicySelfMessagesAreReclaimable(t *testing.T) {
	records := deliveredCapacityRecords(t, 1)
	records[0].Envelope.Source = records[0].Envelope.Target
	for i := 1; i < maxRecords; i++ {
		records = append(records, capacityAcceptedRecord(t, storeEnvelope(i+500), "codex-inbox"))
	}
	store := deliveredCapacityStore(t, records)
	if _, created, err := store.PutAccepted(storeEnvelope(999), "codex-inbox"); err != nil || !created {
		t.Fatalf("self delivered blocks send: created=%t err=%v", created, err)
	}
	if _, found, err := store.Get(records[0].Envelope.MessageRef); err != nil || found {
		t.Fatalf("self delivered retained=%t err=%v", found, err)
	}
}

func TestDeliveredCapacityPolicyNewestByAcceptedAtPerTarget(t *testing.T) {
	records := deliveredCapacityRecords(t, 65)
	// Input order must not choose the protection set; equal times have a stable ref tie-break.
	records[0], records[64] = records[64], records[0]
	other := deliveredCapacityRecords(t, 1)[0]
	other.Envelope.MessageRef = "message-other-target"
	other.Envelope.Target.AgentUID = "agent-other"
	records = append(records, other)
	for len(records) < maxRecords {
		records = append(records, capacityAcceptedRecord(t, storeEnvelope(1000+len(records)), "codex-inbox"))
	}
	kept, reclaimed := pruneRecords(records, storeTestNow)
	if len(kept) != 255 || len(reclaimed) != 1 || reclaimed[0].Record.Envelope.MessageRef != "message-000" {
		t.Fatalf("latest per-target protection: kept=%d reclaimed=%+v", len(kept), reclaimed)
	}
}

func TestDeliveredCapacityPolicyCapacityNamesLargestTarget(t *testing.T) {
	records := make([]Record, maxRecords)
	for i := range records {
		envelope := storeEnvelope(i)
		if i < 20 {
			envelope.Target.AgentUID = "agent-other"
		}
		records[i] = capacityAcceptedRecord(t, envelope, "codex-inbox")
	}
	store := deliveredCapacityStore(t, records)
	_, created, err := store.PutAccepted(storeEnvelope(999), "codex-inbox")
	if created || !errors.Is(err, ErrCapacity) || !strings.HasPrefix(err.Error(), ErrCapacity.Error()) ||
		!strings.Contains(err.Error(), "target=agent-target") || !strings.Contains(err.Error(), "records=236") {
		t.Fatalf("capacity diagnostics: created=%t err=%v", created, err)
	}
}

func TestDeliveredCapacityPolicyAttemptsStayWithOriginal(t *testing.T) {
	for _, state := range []string{"failed", "unknown", "pending", "delivered"} {
		t.Run(state, func(t *testing.T) {
			records := deliveredCapacityRecords(t, 65)
			original := records[0].Envelope
			attempt := replyEnvelope(original, "message-attempt", "reply", original.Target, original.Source, original.AcceptedAt, original.Deadline)
			record := capacityTerminalRecord(t, attempt, "claude-coordination", coremessage.EventFail, "provider-write-zero")
			switch state {
			case "unknown":
				record.Delivery.OutcomeUnknown = true
			case "pending":
				record = capacityAcceptedRecord(t, attempt, "claude-coordination")
			case "delivered":
				record = capacityTerminalRecord(t, attempt, "claude-coordination", coremessage.EventDeliver, "delivered")
			}
			// Make the attempt older than its original to catch attempt-only eviction.
			record.Envelope.AcceptedAt = original.AcceptedAt.Add(-time.Minute)
			records = append(records, record)
			for len(records) < maxRecords {
				records = append(records, capacityAcceptedRecord(t, storeEnvelope(1000+len(records)), "codex-inbox"))
			}
			kept, reclaimed := pruneRecords(records, storeTestNow)
			hasOriginal, hasAttempt := false, false
			for _, r := range kept {
				hasOriginal = hasOriginal || r.Envelope.MessageRef == original.MessageRef
				hasAttempt = hasAttempt || r.Envelope.MessageRef == attempt.MessageRef
			}
			if hasOriginal != hasAttempt {
				t.Fatalf("split original/attempt: original=%t attempt=%t", hasOriginal, hasAttempt)
			}
			if state == "pending" || state == "delivered" {
				// Pending attempts cannot be silently evicted; independently protected delivered attempts stay protected.
				if !hasOriginal || len(reclaimed) != 0 {
					t.Fatalf("protected group lost: original=%t reclaimed=%d", hasOriginal, len(reclaimed))
				}
			} else if hasOriginal || len(reclaimed) != 2 {
				t.Fatalf("unprotected terminal group not reclaimed together: original=%t reclaimed=%d", hasOriginal, len(reclaimed))
			}
		})
	}
}

func TestDeliveredCapacityPolicyPinsAndProtectedOriginalKeepAttempts(t *testing.T) {
	for _, mode := range []string{"protected-original", "pinned-original", "pinned-attempt"} {
		t.Run(mode, func(t *testing.T) {
			records := deliveredCapacityRecords(t, 65)
			index := 0
			if mode == "protected-original" {
				index = 64
			}
			original := records[index].Envelope
			attempt := replyEnvelope(original, "message-attempt", "reply", original.Target, original.Source, original.AcceptedAt, original.Deadline)
			record := capacityTerminalRecord(t, attempt, "claude-coordination", coremessage.EventFail, "provider-write-zero")
			// A much older terminal timestamp tests retention as well as capacity.
			record.Delivery.TerminalAt = storeTestNow.Add(-48 * time.Hour)
			records = append(records, record)
			for len(records) < maxRecords {
				records = append(records, capacityAcceptedRecord(t, storeEnvelope(1000+len(records)), "codex-inbox"))
			}
			var pins []string
			if mode == "pinned-original" {
				pins = []string{original.MessageRef}
			}
			if mode == "pinned-attempt" {
				pins = []string{attempt.MessageRef}
			}
			kept, _ := pruneRecords(records, storeTestNow, pins...)
			found := map[string]bool{}
			for _, r := range kept {
				found[r.Envelope.MessageRef] = true
			}
			if !found[original.MessageRef] || !found[attempt.MessageRef] {
				t.Fatalf("protected group split or lost: original=%t attempt=%t", found[original.MessageRef], found[attempt.MessageRef])
			}
		})
	}
}

func TestDeliveredCapacityPolicyCodexStillReclaimsOldestDelivered(t *testing.T) {
	records := deliveredCapacityRecords(t, 1)
	records[0].Envelope.Target.Provider = "codex"
	records[0].Adapter = "codex-inbox"
	for len(records) < maxRecords {
		records = append(records, capacityAcceptedRecord(t, storeEnvelope(1000+len(records)), "codex-inbox"))
	}
	kept, reclaimed := pruneRecords(records, storeTestNow)
	if len(kept) != 255 || len(reclaimed) != 1 || reclaimed[0].Record.Envelope.MessageRef != "message-000" {
		t.Fatalf("Codex delivered protection changed: kept=%d reclaimed=%+v", len(kept), reclaimed)
	}
}

func TestDeliveredCapacityPolicyByteCapacityAndTieDiagnostics(t *testing.T) {
	store := NewStoreAt(filepath.Join(t.TempDir(), "messages.json"))
	records := make([]Record, maxRecords)
	for i := range records {
		envelope := storeEnvelope(i)
		envelope.Payload = strings.Repeat("\x01", 4096)
		if i%2 == 0 {
			envelope.Target.AgentUID = "agent-a"
		}
		records[i] = capacityAcceptedRecord(t, envelope, "codex-inbox")
	}
	err := store.withLock(func() error { return store.writeLocked(diskState{Records: records}, nil) })
	if !errors.Is(err, ErrCapacity) || err.Error() != ErrCapacity.Error()+": target=agent-a records=128" {
		t.Fatalf("byte capacity diagnostics: %v", err)
	}
}
