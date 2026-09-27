package agentmessage

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

// writeStoreFile writes the store the way a committed write leaves it, without
// the lock file a Store write would also create.
func writeStoreFile(t *testing.T, stateDir string, records []Record) string {
	t.Helper()
	dir := filepath.Join(stateDir, storeDirName)
	if err := os.MkdirAll(dir, localstate.PrivateDirMode); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(diskState{Version: storeVersion, Records: records})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, storeFileName)
	if err := os.WriteFile(path, append(data, '\n'), localstate.PrivateFileMode); err != nil {
		t.Fatal(err)
	}
	return path
}

// retentionReclaimed is records as pruneRecords hands them over when they
// outlived terminalRetention.
func retentionReclaimed(records []Record) []reclaimedRecord {
	reclaimed := make([]reclaimedRecord, 0, len(records))
	for _, record := range records {
		reclaimed = append(reclaimed, reclaimedRecord{Record: record, Reason: reclaimRetention})
	}
	return reclaimed
}

// historyLines renders records in the full-route shape: every route key and
// the deadline, as lines were written before routes were narrowed. Such lines
// stay on disk until rotation replaces them.
func historyLines(t *testing.T, records ...Record) string {
	t.Helper()
	var buf strings.Builder
	for _, line := range newHistoryRecords(retentionReclaimed(records), storeTestNow) {
		data, err := json.Marshal(line)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(data)
		buf.WriteByte('\n')
	}
	return buf.String()
}

// currentHistoryLines renders records as the writer renders reclaimed ones
// now, through encodeHistoryLine.
func currentHistoryLines(t *testing.T, records ...Record) string {
	t.Helper()
	var buf strings.Builder
	for _, line := range newHistoryRecords(retentionReclaimed(records), storeTestNow) {
		data, err := encodeHistoryLine(line)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(data)
		buf.WriteByte('\n')
	}
	return buf.String()
}

func writeHistoryFile(t *testing.T, stateDir, name, content string) {
	t.Helper()
	dir := filepath.Join(stateDir, storeDirName)
	if err := os.MkdirAll(dir, localstate.PrivateDirMode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), localstate.PrivateFileMode); err != nil {
		t.Fatal(err)
	}
}

func archiveRecordRefs(archive Archive) []string {
	var refs []string
	for _, record := range archive.Records {
		refs = append(refs, record.Envelope.MessageRef)
	}
	return refs
}

func archiveHistoryRefs(archive Archive) []string {
	var refs []string
	for _, entry := range archive.History {
		refs = append(refs, entry.MessageRef)
	}
	return refs
}

func TestReadArchiveReadsTheStoreThenBothHistoryGenerations(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	live := historyTerminalRecord(1, storeTestNow)
	current := historyTerminalRecord(2, storeTestNow.Add(-time.Hour))
	current.Envelope.ReplyTo = live.Envelope.MessageRef
	rotated := historyTerminalRecord(3, storeTestNow.Add(-2*time.Hour))
	writeStoreFile(t, stateDir, []Record{live})
	// A newer schema adds a key; the known fields are still read.
	newer := strings.Replace(historyLines(t, rotated), `"schemaVersion":1`, `"schemaVersion":2,"futureField":{"x":1}`, 1)
	writeHistoryFile(t, stateDir, historyFileName, historyLines(t, current))
	writeHistoryFile(t, stateDir, historyFileName+historyRotatedSuffix, newer)

	archive, err := ReadArchive(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.Records) != 1 || archive.Records[0] != live {
		t.Fatalf("records = %+v, want the stored record with its payload", archive.Records)
	}
	if got := archiveHistoryRefs(archive); !slices.Equal(got, []string{current.Envelope.MessageRef, rotated.Envelope.MessageRef}) {
		t.Fatalf("history refs = %v, want the current generation then the rotated one", got)
	}
	first, second := archive.History[0], archive.History[1]
	if first.ReplyTo != live.Envelope.MessageRef || first.ConversationRef != current.Envelope.ConversationRef ||
		!first.Source.Same(current.Envelope.Source) || !first.Target.Same(current.Envelope.Target) ||
		!first.AcceptedAt.Equal(current.Envelope.AcceptedAt) || first.PayloadBytes != len(current.Envelope.Payload) ||
		first.State != current.Delivery.State || first.SchemaVersion != historySchemaVersion {
		t.Fatalf("current entry = %+v", first)
	}
	if second.SchemaVersion != 2 || second.MessageRef != rotated.Envelope.MessageRef || !second.Source.Same(rotated.Envelope.Source) {
		t.Fatalf("a newer-schema line lost its known fields: %+v", second)
	}
	if archive.Skipped != 0 {
		t.Fatalf("skipped = %d, want 0", archive.Skipped)
	}
}

func TestReadArchiveSkipsATornLastHistoryLine(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	kept := historyTerminalRecord(1, storeTestNow)
	torn := historyLines(t, historyTerminalRecord(2, storeTestNow))
	writeHistoryFile(t, stateDir, historyFileName, historyLines(t, kept)+torn[:len(torn)/2])

	archive, err := ReadArchive(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if archive.Skipped != 1 || !slices.Equal(archiveHistoryRefs(archive), []string{kept.Envelope.MessageRef}) {
		t.Fatalf("archive = %+v, want the whole line and one skipped", archive)
	}
}

func TestReadArchiveRefusesAStoreItCannotReadOrValidate(t *testing.T) {
	t.Parallel()
	malformed := t.TempDir()
	path := writeStoreFile(t, malformed, nil)
	if err := os.WriteFile(path, []byte("{\"version\":2,\"records\":[],\"extra\":1}\n"), localstate.PrivateFileMode); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadArchive(malformed); !errors.Is(err, ErrMalformedStore) || !strings.Contains(err.Error(), path) {
		t.Fatalf("malformed store error = %v, want ErrMalformedStore naming %s", err, path)
	}

	unreadable := t.TempDir()
	if err := os.MkdirAll(filepath.Join(unreadable, storeDirName, storeFileName), localstate.PrivateDirMode); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadArchive(unreadable); err == nil || !strings.Contains(err.Error(), "agent message store") {
		t.Fatalf("unreadable store error = %v", err)
	}

	unreadableLog := t.TempDir()
	if err := os.MkdirAll(filepath.Join(unreadableLog, storeDirName, historyFileName), localstate.PrivateDirMode); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadArchive(unreadableLog); err == nil || !strings.Contains(err.Error(), "history") {
		t.Fatalf("unreadable history error = %v", err)
	}
}

// TestReadArchiveCreatesNothingAndLeavesTheStoreUntouched pins the lock-free
// contract: no directory, lock file, or temporary file, and a store whose bytes
// and mtime are exactly what they were.
func TestReadArchiveCreatesNothingAndLeavesTheStoreUntouched(t *testing.T) {
	t.Parallel()
	empty := t.TempDir()
	archive, err := ReadArchive(empty)
	if err != nil || len(archive.Records) != 0 || len(archive.History) != 0 || archive.Skipped != 0 {
		t.Fatalf("empty state dir = (%+v, %v)", archive, err)
	}
	if entries, err := os.ReadDir(empty); err != nil || len(entries) != 0 {
		t.Fatalf("read created %v (%v) in an empty state dir", entries, err)
	}

	stateDir := t.TempDir()
	path := writeStoreFile(t, stateDir, []Record{historyTerminalRecord(1, storeTestNow)})
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadArchive(stateDir); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("store bytes changed (%v)", err)
	}
	if info, err := os.Stat(path); err != nil || !info.ModTime().Equal(old) {
		t.Fatalf("store mtime = (%v, %v), want %v", info.ModTime(), err, old)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 || entries[0].Name() != storeFileName {
		t.Fatalf("store dir after a read = %v (%v), want only %s", entries, err, storeFileName)
	}
}

// TestReadArchiveKeepsARecordReclaimedBetweenTheStoreAndHistoryReads runs the
// real writer between the two reads: it appends the reclaimed line, then
// renames the store without it. The record is read from the store and again
// from the log, never from neither.
func TestReadArchiveKeepsARecordReclaimedBetweenTheStoreAndHistoryReads(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	store := NewStore(stateDir)
	store.now = func() time.Time { return storeTestNow }
	stale := historyTerminalRecord(1, storeTestNow.Add(-terminalRetention-2*time.Hour))
	seedStore(t, store, []Record{stale})

	archive, err := readArchive(stateDir, readHooks{afterStoreRead: func() {
		if _, created, err := store.PutAccepted(storeEnvelope(2), "codex-inbox"); err != nil || !created {
			t.Errorf("PutAccepted = (%t, %v)", created, err)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Get(stale.Envelope.MessageRef); err != nil || found {
		t.Fatalf("the writer did not reclaim the record = (%t, %v)", found, err)
	}
	if got := archiveRecordRefs(archive); !slices.Equal(got, []string{stale.Envelope.MessageRef}) {
		t.Fatalf("store refs = %v, want the record as the store held it", got)
	}
	if archive.Records[0].Envelope.Payload != stale.Envelope.Payload {
		t.Fatalf("store copy lost its payload")
	}
	if got := archiveHistoryRefs(archive); !slices.Equal(got, []string{stale.Envelope.MessageRef}) {
		t.Fatalf("history refs = %v, want the reclaimed line", got)
	}
}

// TestReadArchiveKeepsRetainedLinesWhenHistoryRotatesBetweenReads rotates the
// log between the two generation reads. The generation that was current is
// renamed to the retained one and read again from there; the generation the
// rotation replaced is no longer retained.
func TestReadArchiveKeepsRetainedLinesWhenHistoryRotatesBetweenReads(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	store := NewStore(stateDir)
	store.now = func() time.Time { return storeTestNow }
	stale := historyTerminalRecord(1, storeTestNow.Add(-terminalRetention-2*time.Hour))
	seedStore(t, store, []Record{stale})
	current := historyTerminalRecord(2, storeTestNow.Add(-48*time.Hour))
	replaced := historyTerminalRecord(3, storeTestNow.Add(-72*time.Hour))
	writeHistoryFile(t, stateDir, historyFileName, historyLines(t, current))
	writeHistoryFile(t, stateDir, historyFileName+historyRotatedSuffix, historyLines(t, replaced))

	archive, err := readArchive(stateDir, readHooks{betweenHistories: func() {
		store.historyMaxBytes = 1
		if _, created, err := store.PutAccepted(storeEnvelope(4), "codex-inbox"); err != nil || !created {
			t.Errorf("PutAccepted = (%t, %v)", created, err)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := os.ReadFile(store.historyPath() + historyRotatedSuffix)
	if err != nil || string(rotated) != historyLines(t, current) {
		t.Fatalf("the writer did not rotate the current generation: %q (%v)", rotated, err)
	}
	if got := archiveHistoryRefs(archive); !slices.Equal(got, []string{current.Envelope.MessageRef, current.Envelope.MessageRef}) {
		t.Fatalf("history refs = %v, want the rotated generation read from both names", got)
	}
	// The record the rotating write reclaimed was still in the store when the
	// store was read.
	if got := archiveRecordRefs(archive); !slices.Equal(got, []string{stale.Envelope.MessageRef}) {
		t.Fatalf("store refs = %v", got)
	}
}

// TestReadArchiveReadsBothHistoryLineShapes pins that one generation holding a
// full-route line written before routes were narrowed and lines the writer
// writes now reads every field a reader uses from each. The operator line has
// an origin and no source.
func TestReadArchiveReadsBothHistoryLineShapes(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	old := historyTerminalRecord(1, storeTestNow.Add(-2*time.Hour))
	current := historyTerminalRecord(2, storeTestNow.Add(-time.Hour))
	current.Envelope.ReplyTo = old.Envelope.MessageRef
	operatorEnvelope := operatorStoreEnvelope("message-operator", storeTestNow.Add(-30*time.Minute))
	operator := Record{Envelope: operatorEnvelope, Adapter: "claude-coordination",
		Delivery: terminalDelivery(operatorEnvelope, coremessage.Event{Kind: coremessage.EventDeliver,
			ObservedAt: operatorEnvelope.AcceptedAt.Add(time.Second)})}
	oldLine := historyLines(t, old)
	newLines := currentHistoryLines(t, current, operator)

	// The fixture has to stay two shapes: the old line carries every route key
	// and the deadline, the new ones only agentUID and provider and no
	// deadline, which then decodes as zero.
	decode := func(line string) (map[string]json.RawMessage, historyRecord) {
		t.Helper()
		var keys map[string]json.RawMessage
		var record historyRecord
		if err := json.Unmarshal([]byte(line), &keys); err != nil {
			t.Fatalf("decode %s: %v", line, err)
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode %s: %v", line, err)
		}
		return keys, record
	}
	routeKeys := func(raw json.RawMessage) []string {
		t.Helper()
		var route map[string]json.RawMessage
		if err := json.Unmarshal(raw, &route); err != nil {
			t.Fatalf("decode route %s: %v", raw, err)
		}
		keys := make([]string, 0, len(route))
		for key := range route {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		return keys
	}
	narrowKeys := []string{"agentUID", "provider"}
	oldKeys, oldRecord := decode(strings.TrimSuffix(oldLine, "\n"))
	if _, ok := oldKeys["deadline"]; !ok || oldRecord.Deadline.IsZero() || oldRecord.Source.PaneUID == "" ||
		slices.Equal(routeKeys(oldKeys["target"]), narrowKeys) {
		t.Fatalf("old fixture line is not the full-route shape: %s", oldLine)
	}
	lines := strings.Split(strings.TrimSuffix(newLines, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("new lines = %q, want two", lines)
	}
	for i, line := range lines {
		keys, record := decode(line)
		_, deadline := keys["deadline"]
		_, outcomeUnknown := keys["outcomeUnknown"]
		if deadline || outcomeUnknown || !record.Deadline.IsZero() || strings.Contains(line, `"paneUID"`) ||
			strings.Contains(line, `"activationGeneration"`) || strings.Contains(line, `"incarnation"`) ||
			!slices.Equal(routeKeys(keys["target"]), narrowKeys) {
			t.Fatalf("new line %d is not the narrow shape: %s", i, line)
		}
		if _, source := keys["source"]; source == (i == 1) {
			t.Fatalf("new line %d source presence is wrong: %s", i, line)
		}
		if i == 0 && !slices.Equal(routeKeys(keys["source"]), narrowKeys) {
			t.Fatalf("new line source is not narrowed: %s", line)
		}
	}
	writeHistoryFile(t, stateDir, historyFileName, oldLine+newLines)

	archive, err := ReadArchive(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if archive.Skipped != 0 || len(archive.Records) != 0 {
		t.Fatalf("archive = %+v, want no skipped line and no store", archive)
	}
	if got := archiveHistoryRefs(archive); !slices.Equal(got, []string{old.Envelope.MessageRef,
		current.Envelope.MessageRef, operator.Envelope.MessageRef}) {
		t.Fatalf("history refs = %v, want the file's order", got)
	}
	narrow := func(route coremessage.Route) coremessage.Route {
		return coremessage.Route{AgentUID: route.AgentUID, Provider: route.Provider}
	}
	for i, want := range []struct {
		record         Record
		source, target coremessage.Route
	}{
		{old, old.Envelope.Source, old.Envelope.Target},
		{current, narrow(current.Envelope.Source), narrow(current.Envelope.Target)},
		{operator, coremessage.Route{}, narrow(operator.Envelope.Target)},
	} {
		entry, envelope := archive.History[i], want.record.Envelope
		if entry.SchemaVersion != historySchemaVersion || entry.MessageRef != envelope.MessageRef ||
			entry.ConversationRef != envelope.ConversationRef || entry.ReplyTo != envelope.ReplyTo ||
			entry.State != want.record.Delivery.State || !entry.AcceptedAt.Equal(envelope.AcceptedAt) ||
			entry.PayloadBytes != len(envelope.Payload) || entry.Source != want.source || entry.Target != want.target {
			t.Fatalf("entry %d = %+v, want %s with source %+v and target %+v", i, entry, envelope.MessageRef,
				want.source, want.target)
		}
	}
	if second := archive.History[1]; second.ReplyTo != old.Envelope.MessageRef ||
		second.Source.AgentUID != "agent-source" || second.Target.Provider != "codex" {
		t.Fatalf("new line lost its edge: %+v", second)
	}
}

// TestReadArchiveExposesOriginAndPayloadDigestOfReclaimedRecords reclaims an
// Agent record and an operator record through the store and reads them back:
// the operator entry has its origin and no source, the Agent entry its source
// and no origin, and both carry the digest of their own payload.
func TestReadArchiveExposesOriginAndPayloadDigestOfReclaimedRecords(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	store := NewStoreAt(filepath.Join(stateDir, storeDirName, storeFileName))
	store.now = func() time.Time { return storeTestNow }
	stale := storeTestNow.Add(-terminalRetention - 2*time.Hour)
	agent := historyTerminalRecord(9101, stale)
	operatorEnvelope := operatorStoreEnvelope("message-operator-digest", stale.Add(time.Second))
	operator := Record{Envelope: operatorEnvelope, Adapter: "claude-coordination",
		Delivery: terminalDelivery(operatorEnvelope, coremessage.Event{Kind: coremessage.EventDeliver,
			ObservedAt: operatorEnvelope.AcceptedAt.Add(time.Second)})}
	seedStore(t, store, []Record{agent, operator})
	if _, created, err := store.PutAccepted(storeEnvelope(14), "codex-inbox"); err != nil || !created {
		t.Fatalf("PutAccepted = (%t, %v)", created, err)
	}

	archive, err := ReadArchive(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if archive.Skipped != 0 || !slices.Equal(archiveHistoryRefs(archive),
		[]string{agent.Envelope.MessageRef, operator.Envelope.MessageRef}) {
		t.Fatalf("archive history = %+v, want the Agent then the operator entry", archive.History)
	}
	agentEntry, operatorEntry := archive.History[0], archive.History[1]
	if agentEntry.Origin != (coremessage.Origin{}) || agentEntry.Source.AgentUID != agent.Envelope.Source.AgentUID {
		t.Fatalf("Agent entry origin %+v source %+v, want no origin and source %s", agentEntry.Origin, agentEntry.Source,
			agent.Envelope.Source.AgentUID)
	}
	if !operatorEntry.Origin.Operator() || operatorEntry.Source != (coremessage.Route{}) {
		t.Fatalf("operator entry origin %+v source %+v, want operator origin and no source", operatorEntry.Origin,
			operatorEntry.Source)
	}
	for _, check := range []struct {
		entry   HistoryEntry
		payload string
	}{{agentEntry, agent.Envelope.Payload}, {operatorEntry, operator.Envelope.Payload}} {
		if want := coremessage.PayloadSHA256(check.payload); check.entry.PayloadSHA256 != want {
			t.Fatalf("%s PayloadSHA256 = %q, want %q", check.entry.MessageRef, check.entry.PayloadSHA256, want)
		}
	}
	if agentEntry.PayloadSHA256 == operatorEntry.PayloadSHA256 {
		t.Fatal("two different payloads read with the same digest")
	}
}

// TestReadArchiveReadsALineWithoutAPayloadDigest reads lines exactly as the
// writer wrote them before payloadSHA256 existed: they are not skipped, every
// other field is intact, and the digest reads as empty, meaning not recorded.
func TestReadArchiveReadsALineWithoutAPayloadDigest(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	const oldLines = `{"schemaVersion":1,"evictedAt":"2026-09-18T02:54:58.263232329Z","reason":"capacity","adapter":"codex-inbox","messageRef":"message-old-agent","conversationRef":"conversation-old-agent","replyTo":"message-old-parent","state":"delivered","deliveryReason":"unspecified","handoffObserved":false,"acceptedAt":"2026-09-18T00:54:57.275578008Z","terminalAt":"2026-09-18T00:54:58.510145899Z","payloadBytes":2027,"source":{"agentUID":"agent-source","provider":"claude"},"target":{"agentUID":"agent-target","provider":"codex"}}
{"schemaVersion":1,"evictedAt":"2026-09-18T02:54:58.263232329Z","reason":"retention","adapter":"claude-coordination","messageRef":"message-old-operator","conversationRef":"conversation-old-operator","state":"delivered","deliveryReason":"unspecified","handoffObserved":true,"acceptedAt":"2026-09-18T00:55:57.275578008Z","terminalAt":"2026-09-18T00:55:58.510145899Z","payloadBytes":42,"origin":{"kind":"operator","client":"web"},"target":{"agentUID":"agent-claude-b","provider":"claude"}}
`
	if strings.Contains(oldLines, "payloadSHA256") {
		t.Fatal("the fixture must be the shape written before the digest existed")
	}
	writeHistoryFile(t, stateDir, historyFileName, oldLines)

	archive, err := ReadArchive(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if archive.Skipped != 0 || len(archive.History) != 2 {
		t.Fatalf("archive = %+v, want two entries and nothing skipped", archive)
	}
	acceptedAt := time.Date(2026, 9, 18, 0, 54, 57, 275578008, time.UTC)
	agent, operator := archive.History[0], archive.History[1]
	if agent.PayloadSHA256 != "" || operator.PayloadSHA256 != "" {
		t.Fatalf("digests = %q, %q, want empty (not recorded)", agent.PayloadSHA256, operator.PayloadSHA256)
	}
	if agent.SchemaVersion != 1 || agent.MessageRef != "message-old-agent" ||
		agent.ConversationRef != "conversation-old-agent" || agent.ReplyTo != "message-old-parent" ||
		agent.State != coremessage.StateDelivered || !agent.AcceptedAt.Equal(acceptedAt) || agent.PayloadBytes != 2027 ||
		agent.Origin != (coremessage.Origin{}) ||
		agent.Source != (coremessage.Route{AgentUID: "agent-source", Provider: "claude"}) ||
		agent.Target != (coremessage.Route{AgentUID: "agent-target", Provider: "codex"}) {
		t.Fatalf("old Agent entry = %+v", agent)
	}
	if operator.MessageRef != "message-old-operator" || operator.PayloadBytes != 42 || !operator.Origin.Operator() ||
		operator.Source != (coremessage.Route{}) ||
		operator.Target != (coremessage.Route{AgentUID: "agent-claude-b", Provider: "claude"}) {
		t.Fatalf("old operator entry = %+v", operator)
	}
}
