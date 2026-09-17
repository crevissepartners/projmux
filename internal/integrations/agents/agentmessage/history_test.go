package agentmessage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

// historySchemaKeys is the fixed line schema. payload is absent by design: the
// log keeps the length only, so the assertion below is a key-set comparison and
// not an empty-string check.
var historySchemaKeys = []string{
	"acceptedAt", "adapter", "conversationRef", "deadline", "deliveryReason", "evictedAt",
	"handoffObserved", "messageRef", "outcomeUnknown", "payloadBytes", "reason",
	"schemaVersion", "source", "state", "target", "terminalAt",
}

func historyTerminalRecord(index int, acceptedAt time.Time) Record {
	envelope := storeEnvelope(index)
	envelope.AcceptedAt = acceptedAt
	envelope.Deadline = acceptedAt.Add(time.Minute)
	delivery, _ := coremessage.Reduce(coremessage.Delivery{}, envelope, coremessage.Event{Kind: coremessage.EventAccept,
		MessageRef: envelope.MessageRef, ConversationRef: envelope.ConversationRef, Target: envelope.Target,
		ObservedAt: envelope.AcceptedAt})
	delivery, _ = coremessage.Reduce(delivery, envelope, coremessage.Event{Kind: coremessage.EventDeliver,
		MessageRef: envelope.MessageRef, ConversationRef: envelope.ConversationRef, Target: envelope.Target,
		Reason: "history-test", ObservedAt: envelope.AcceptedAt.Add(time.Second)})
	return Record{Envelope: envelope, Delivery: delivery, Adapter: "codex-inbox"}
}

func seedStore(t *testing.T, store *Store, records []Record) {
	t.Helper()
	if err := store.withLock(func() error {
		return store.writeLocked(diskState{Version: storeVersion, Records: records}, nil)
	}); err != nil {
		t.Fatal(err)
	}
}

func readHistoryLines(t *testing.T, path string) []map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- test-owned temporary path.
	if err != nil {
		t.Fatalf("read history %s: %v", path, err)
	}
	text := strings.TrimSuffix(string(data), "\n")
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	out := make([]map[string]json.RawMessage, 0, len(lines))
	for i, line := range lines {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &fields); err != nil {
			t.Fatalf("history line %d is not one JSON object: %v", i, err)
		}
		out = append(out, fields)
	}
	return out
}

func historyKeys(line map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(line))
	for key := range line {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func historyString(t *testing.T, line map[string]json.RawMessage, key string) string {
	t.Helper()
	var value string
	if err := json.Unmarshal(line[key], &value); err != nil {
		t.Fatalf("history %s is not a string: %v", key, err)
	}
	return value
}

func historyInt(t *testing.T, line map[string]json.RawMessage, key string) int {
	t.Helper()
	var value int
	if err := json.Unmarshal(line[key], &value); err != nil {
		t.Fatalf("history %s is not a number: %v", key, err)
	}
	return value
}

// TestRetentionReclaimAppendsFixedSchemaLines covers acceptance 1 and 3: every
// record the retention rule reclaims becomes exactly one line carrying the
// fixed key set, reason retention, and the original payload length without the
// payload itself.
func TestRetentionReclaimAppendsFixedSchemaLines(t *testing.T) {
	t.Parallel()
	store := NewStoreAt(filepath.Join(t.TempDir(), "agent-messages", "messages.json"))
	store.now = func() time.Time { return storeTestNow }
	stale := storeTestNow.Add(-terminalRetention - 2*time.Hour)
	seeded := []Record{
		historyTerminalRecord(1001, stale),
		historyTerminalRecord(1002, stale.Add(time.Second)),
		historyTerminalRecord(1003, stale.Add(2*time.Second)),
	}
	seeded[2].Envelope.ReplyTo = seeded[0].Envelope.MessageRef
	seedStore(t, store, seeded)
	if _, err := os.Stat(store.historyPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("history exists before any reclaim: %v", err)
	}

	if _, created, err := store.PutAccepted(storeEnvelope(7), "codex-inbox"); err != nil || !created {
		t.Fatalf("PutAccepted = (%t, %v)", created, err)
	}
	lines := readHistoryLines(t, store.historyPath())
	if len(lines) != len(seeded) {
		t.Fatalf("history lines = %d, want %d", len(lines), len(seeded))
	}
	for i, line := range lines {
		want := historySchemaKeys
		if seeded[i].Envelope.ReplyTo != "" {
			want = append(append([]string{}, historySchemaKeys...), "replyTo")
			sort.Strings(want)
		}
		if got := historyKeys(line); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("history line %d keys = %v, want %v", i, got, want)
		}
		if got := historyInt(t, line, "schemaVersion"); got != 1 {
			t.Fatalf("history line %d schemaVersion = %d, want 1", i, got)
		}
		if got := historyString(t, line, "reason"); got != "retention" {
			t.Fatalf("history line %d reason = %q, want retention", i, got)
		}
		if got := historyString(t, line, "messageRef"); got != seeded[i].Envelope.MessageRef {
			t.Fatalf("history line %d messageRef = %q, want %q", i, got, seeded[i].Envelope.MessageRef)
		}
		if got := historyString(t, line, "conversationRef"); got != seeded[i].Envelope.ConversationRef {
			t.Fatalf("history line %d conversationRef = %q", i, got)
		}
		if got := historyString(t, line, "state"); got != string(coremessage.StateDelivered) {
			t.Fatalf("history line %d state = %q", i, got)
		}
		if got := historyString(t, line, "adapter"); got != "codex-inbox" {
			t.Fatalf("history line %d adapter = %q", i, got)
		}
		if got := historyString(t, line, "evictedAt"); got != storeTestNow.Format(time.RFC3339Nano) {
			t.Fatalf("history line %d evictedAt = %q, want the prune clock", i, got)
		}
		if got := historyInt(t, line, "payloadBytes"); got != len(seeded[i].Envelope.Payload) {
			t.Fatalf("history line %d payloadBytes = %d, want %d", i, got, len(seeded[i].Envelope.Payload))
		}
		var source coremessage.Route
		if err := json.Unmarshal(line["source"], &source); err != nil || !source.Same(seeded[i].Envelope.Source) {
			t.Fatalf("history line %d source = (%+v, %v)", i, source, err)
		}
		var target coremessage.Route
		if err := json.Unmarshal(line["target"], &target); err != nil || !target.Same(seeded[i].Envelope.Target) {
			t.Fatalf("history line %d target = (%+v, %v)", i, target, err)
		}
	}
	if lines[2] == nil || historyString(t, lines[2], "replyTo") != seeded[0].Envelope.MessageRef {
		t.Fatalf("reply line lost its replyTo")
	}
	info, err := os.Stat(store.historyPath())
	if err != nil || info.Mode().Perm() != localstate.PrivateFileMode {
		t.Fatalf("history mode = (%v, %v), want %v", info.Mode().Perm(), err, localstate.PrivateFileMode)
	}
	for _, record := range seeded {
		if _, found, err := store.Get(record.Envelope.MessageRef); err != nil || found {
			t.Fatalf("reclaimed %s still in store = (%t, %v)", record.Envelope.MessageRef, found, err)
		}
	}
}

// TestCapacityReclaimAppendsCapacityReason covers acceptance 2: a record pushed
// out by maxRecords uses the same line shape with reason capacity.
func TestCapacityReclaimAppendsCapacityReason(t *testing.T) {
	t.Parallel()
	store := NewStoreAt(filepath.Join(t.TempDir(), "agent-messages", "messages.json"))
	store.now = func() time.Time { return storeTestNow }
	recent := storeTestNow.Add(-time.Hour)
	full := make([]Record, maxRecords)
	for i := range full {
		full[i] = historyTerminalRecord(i+2000, recent.Add(time.Duration(i)*time.Millisecond))
	}
	seedStore(t, store, full)

	if _, created, err := store.PutAccepted(storeEnvelope(9), "codex-inbox"); err != nil || !created {
		t.Fatalf("PutAccepted = (%t, %v)", created, err)
	}
	lines := readHistoryLines(t, store.historyPath())
	if len(lines) != 1 {
		t.Fatalf("history lines = %d, want 1", len(lines))
	}
	if got := historyString(t, lines[0], "reason"); got != "capacity" {
		t.Fatalf("reason = %q, want capacity", got)
	}
	if got := historyString(t, lines[0], "messageRef"); got != full[0].Envelope.MessageRef {
		t.Fatalf("reclaimed ref = %q, want the oldest %q", got, full[0].Envelope.MessageRef)
	}
	if got := historyKeys(lines[0]); strings.Join(got, ",") != strings.Join(historySchemaKeys, ",") {
		t.Fatalf("capacity line keys = %v, want %v", got, historySchemaKeys)
	}
	if _, found, err := store.Get(full[0].Envelope.MessageRef); err != nil || found {
		t.Fatalf("reclaimed record still in store = (%t, %v)", found, err)
	}
	if _, found, err := store.Get(full[1].Envelope.MessageRef); err != nil || !found {
		t.Fatalf("next-oldest record was reclaimed too = (%t, %v)", found, err)
	}
}

// TestHistoryIsDurableBeforeTheStoreCommits covers acceptance 4: the log holds
// the reclaimed record before the store renames its new file into place, and a
// failed append leaves both the store and the log untouched.
func TestHistoryIsDurableBeforeTheStoreCommits(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := NewStoreAt(filepath.Join(dir, "agent-messages", "messages.json"))
	store.now = func() time.Time { return storeTestNow }
	stale := historyTerminalRecord(3001, storeTestNow.Add(-terminalRetention-2*time.Hour))
	seedStore(t, store, []Record{stale})

	var atRename []map[string]json.RawMessage
	store.hooks.beforeRename = func() error {
		atRename = readHistoryLines(t, store.historyPath())
		return nil
	}
	if _, created, err := store.PutAccepted(storeEnvelope(11), "codex-inbox"); err != nil || !created {
		t.Fatalf("PutAccepted = (%t, %v)", created, err)
	}
	if len(atRename) != 1 || historyString(t, atRename[0], "messageRef") != stale.Envelope.MessageRef {
		t.Fatalf("history at rename = %v, want the reclaimed record", atRename)
	}

	failing := NewStoreAt(filepath.Join(t.TempDir(), "agent-messages", "messages.json"))
	failing.now = func() time.Time { return storeTestNow }
	other := historyTerminalRecord(3002, storeTestNow.Add(-terminalRetention-2*time.Hour))
	seedStore(t, failing, []Record{other})
	injected := errors.New("injected history append failure")
	failing.hooks.beforeHistoryAppend = func() error { return injected }
	if _, _, err := failing.PutAccepted(storeEnvelope(12), "codex-inbox"); !errors.Is(err, injected) {
		t.Fatalf("PutAccepted error = %v, want the injected append failure", err)
	}
	if _, found, err := failing.Get(other.Envelope.MessageRef); err != nil || !found {
		t.Fatalf("record disappeared after a failed append = (%t, %v)", found, err)
	}
	if _, found, err := failing.Get("message-012"); err != nil || found {
		t.Fatalf("store committed after a failed append = (%t, %v)", found, err)
	}
	if _, err := os.Stat(failing.historyPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("history exists after a failed append: %v", err)
	}
}

// TestHistoryRotatesIntoOneRetainedGeneration covers acceptance 5: crossing the
// size limit moves the active log aside, and a later rotation replaces that one
// generation instead of adding another.
func TestHistoryRotatesIntoOneRetainedGeneration(t *testing.T) {
	t.Parallel()
	store := NewStoreAt(filepath.Join(t.TempDir(), "agent-messages", "messages.json"))
	store.now = func() time.Time { return storeTestNow }
	if store.historyLimit() != maxHistoryBytes || maxHistoryBytes != 8<<20 {
		t.Fatalf("default history limit = %d, want %d (8 MiB)", store.historyLimit(), maxHistoryBytes)
	}

	reclaim := func(index int) string {
		t.Helper()
		stale := historyTerminalRecord(index+4000, storeTestNow.Add(-terminalRetention-2*time.Hour))
		seedStore(t, store, []Record{stale})
		if _, created, err := store.PutAccepted(storeEnvelope(index), "codex-inbox"); err != nil || !created {
			t.Fatalf("PutAccepted %d = (%t, %v)", index, created, err)
		}
		return stale.Envelope.MessageRef
	}
	active, rotated := store.historyPath(), store.historyPath()+historyRotatedSuffix
	if err := os.MkdirAll(filepath.Dir(active), localstate.PrivateDirMode); err != nil {
		t.Fatal(err)
	}
	seedLine := "{\"seed\":true}\n"
	if err := os.WriteFile(active, []byte(seedLine), localstate.PrivateFileMode); err != nil {
		t.Fatal(err)
	}
	store.historyMaxBytes = len(seedLine) + 1

	firstRef := reclaim(21)
	if got, err := os.ReadFile(rotated); err != nil || string(got) != seedLine {
		t.Fatalf("first rotation kept = (%q, %v), want the seed generation", got, err)
	}
	lines := readHistoryLines(t, active)
	if len(lines) != 1 || historyString(t, lines[0], "messageRef") != firstRef {
		t.Fatalf("active log after the first rotation = %v", lines)
	}
	if _, err := os.Stat(active + ".2"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a third generation exists: %v", err)
	}

	secondRef := reclaim(22)
	kept := readHistoryLines(t, rotated)
	if len(kept) != 1 || historyString(t, kept[0], "messageRef") != firstRef {
		t.Fatalf("second rotation did not replace the retained generation: %v", kept)
	}
	lines = readHistoryLines(t, active)
	if len(lines) != 1 || historyString(t, lines[0], "messageRef") != secondRef {
		t.Fatalf("active log after the second rotation = %v", lines)
	}
	if _, err := os.Stat(active + ".2"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rotation added a third generation: %v", err)
	}
	if info, err := os.Stat(active); err != nil || info.Mode().Perm() != localstate.PrivateFileMode {
		t.Fatalf("rotated-into log mode = (%v, %v)", info.Mode().Perm(), err)
	}
}

func TestHistoryLineOmitsPayloadForEveryReclaimReason(t *testing.T) {
	t.Parallel()
	for _, reason := range []string{"retention", "capacity"} {
		t.Run(reason, func(t *testing.T) {
			t.Parallel()
			store := NewStoreAt(filepath.Join(t.TempDir(), "agent-messages", "messages.json"))
			store.now = func() time.Time { return storeTestNow }
			var seeded []Record
			if reason == "retention" {
				seeded = []Record{historyTerminalRecord(5001, storeTestNow.Add(-terminalRetention-2*time.Hour))}
			} else {
				seeded = make([]Record, maxRecords)
				for i := range seeded {
					seeded[i] = historyTerminalRecord(i+6000, storeTestNow.Add(-time.Hour).Add(time.Duration(i)*time.Millisecond))
				}
			}
			seedStore(t, store, seeded)
			if _, created, err := store.PutAccepted(storeEnvelope(13), "codex-inbox"); err != nil || !created {
				t.Fatalf("PutAccepted = (%t, %v)", created, err)
			}
			lines := readHistoryLines(t, store.historyPath())
			if len(lines) == 0 {
				t.Fatal("no history line")
			}
			for i, line := range lines {
				if _, present := line["payload"]; present {
					t.Fatalf("line %d carries a payload key", i)
				}
				if got := historyString(t, line, "reason"); got != reason {
					t.Fatalf("line %d reason = %q, want %q", i, got, reason)
				}
			}
			raw, err := os.ReadFile(store.historyPath()) // #nosec G304 -- test-owned temporary path.
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), seeded[0].Envelope.Payload) {
				t.Fatalf("history contains the payload text %q", seeded[0].Envelope.Payload)
			}
		})
	}
}

func TestPruneRecordsReportsWhatItReclaimed(t *testing.T) {
	t.Parallel()
	stale := historyTerminalRecord(7001, storeTestNow.Add(-terminalRetention-2*time.Hour))
	live := storeEnvelope(7002)
	live.AcceptedAt = storeTestNow
	live.Deadline = storeTestNow.Add(time.Hour)
	delivery, _ := coremessage.Reduce(coremessage.Delivery{}, live, coremessage.Event{Kind: coremessage.EventAccept,
		MessageRef: live.MessageRef, ConversationRef: live.ConversationRef, Target: live.Target, ObservedAt: live.AcceptedAt})
	kept, reclaimed := pruneRecords([]Record{stale, {Envelope: live, Delivery: delivery, Adapter: "codex-inbox"}}, storeTestNow)
	if len(kept) != 1 || kept[0].Envelope.MessageRef != live.MessageRef {
		t.Fatalf("kept = %+v, want only the live record", kept)
	}
	if len(reclaimed) != 1 || reclaimed[0].Reason != reclaimRetention ||
		reclaimed[0].Record.Envelope.MessageRef != stale.Envelope.MessageRef {
		t.Fatalf("reclaimed = %+v", reclaimed)
	}
	if reclaimed[0].Record.Envelope.Payload != stale.Envelope.Payload {
		t.Fatalf("reclaimed payload was overwritten by compaction: %q", reclaimed[0].Record.Envelope.Payload)
	}
}
