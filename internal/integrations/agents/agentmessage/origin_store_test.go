package agentmessage

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
)

// v2FixtureDir holds a store and history log written by the Store API of the
// build before store version 3, for a fixed clock. They are that build's bytes,
// not hand-written ones.
const v2FixtureDir = "testdata/v2-store"

// v2FixtureNow is shortly after the fixture's last write, before any of its
// deadlines.
var v2FixtureNow = time.Date(2026, 9, 2, 12, 0, 10, 0, time.UTC)

// The v2Build types are the store schema the previous build decodes, field for
// field, with its DisallowUnknownFields and version rule. A helper from that
// build keeps running across an install and shares the store file, so what it
// accepts is the compatibility contract.
type v2BuildRoute struct {
	AgentUID             string `json:"agentUID"`
	PaneUID              string `json:"paneUID"`
	ActivationGeneration string `json:"activationGeneration"`
	Provider             string `json:"provider"`
	Incarnation          string `json:"incarnation"`
}

type v2BuildEnvelope struct {
	Version         int                   `json:"version"`
	MessageRef      string                `json:"messageRef"`
	ConversationRef string                `json:"conversationRef"`
	ReplyTo         string                `json:"replyTo,omitempty"`
	Source          v2BuildRoute          `json:"source"`
	Target          v2BuildRoute          `json:"target"`
	Authority       coremessage.Authority `json:"authority"`
	Payload         string                `json:"payload"`
	AcceptedAt      time.Time             `json:"acceptedAt"`
	Deadline        time.Time             `json:"deadline"`
}

type v2BuildState struct {
	Version int `json:"version"`
	Records []struct {
		Envelope        v2BuildEnvelope      `json:"envelope"`
		Delivery        coremessage.Delivery `json:"delivery"`
		Adapter         string               `json:"adapter"`
		HandoffObserved bool                 `json:"handoffObserved,omitempty"`
	} `json:"records"`
}

func decodeAsV2Build(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state v2BuildState
	if err := decoder.Decode(&state); err != nil {
		return err
	}
	if state.Version != storeVersion && state.Version != legacyStoreVersion {
		return ErrMalformedStore
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrMalformedStore
	}
	return nil
}

// copyV2Fixture installs the fixture store and history into a fresh private
// store directory and returns a store on it.
func copyV2Fixture(t *testing.T) *Store {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "agent-messages")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{storeFileName, historyFileName} {
		data, err := os.ReadFile(filepath.Join(v2FixtureDir, name)) // #nosec G304 -- fixed test fixture.
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	store := NewStoreAt(filepath.Join(dir, storeFileName))
	store.now = func() time.Time { return v2FixtureNow }
	return store
}

func readStoreFile(t *testing.T, store *Store) []byte {
	t.Helper()
	data, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func storeFileVersion(t *testing.T, data []byte) int {
	t.Helper()
	var head struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		t.Fatal(err)
	}
	return head.Version
}

func operatorStoreEnvelope(ref string, acceptedAt time.Time) coremessage.Envelope {
	return coremessage.Envelope{Version: coremessage.Version, MessageRef: ref, ConversationRef: "conversation-" + ref,
		Origin: coremessage.OperatorWebOrigin(),
		Target: coremessage.Route{AgentUID: "agent-claude-b", PaneUID: "pane-claude-b", ActivationGeneration: "generation-claude-b",
			Provider: "claude", Incarnation: "claude-b-incarnation"},
		Authority: coremessage.OperatorAuthority(), Payload: "operator text for " + ref,
		AcceptedAt: acceptedAt, Deadline: acceptedAt.Add(time.Hour)}
}

func TestStoreReadsTheV2FixtureFromThePreviousBuild(t *testing.T) {
	t.Parallel()
	store := copyV2Fixture(t)
	want := map[string]coremessage.State{
		"message-to-codex": coremessage.StateDelivered, "message-to-claude": coremessage.StateDelivered,
		"message-reply": coremessage.StateAccepted, "message-pending": coremessage.StateAccepted,
		"message-failed": coremessage.StateFailed, "message-expired": coremessage.StateExpired,
	}
	for ref, state := range want {
		record, found, err := store.Get(ref)
		if err != nil || !found || record.Delivery.State != state || record.Envelope.Operator() {
			t.Fatalf("Get(%s) = (%+v, %t, %v), want state %s", ref, record, found, err, state)
		}
	}
	if reply, found, err := store.Reply("message-to-claude"); err != nil || !found || reply.Envelope.MessageRef != "message-reply" {
		t.Fatalf("Reply = (%+v, %t, %v)", reply, found, err)
	}
}

// TestStoreKeepsAnAgentOnlyStoreAtVersion2 is the install and rollback
// guarantee: with no operator record, this build rewrites the previous build's
// file byte for byte and every later write stays one that build accepts.
func TestStoreKeepsAnAgentOnlyStoreAtVersion2(t *testing.T) {
	t.Parallel()
	store := copyV2Fixture(t)
	before := readStoreFile(t, store)
	if err := store.withLock(func() error {
		state, err := store.loadLocked()
		if err != nil {
			return err
		}
		return store.writeLocked(state, nil)
	}); err != nil {
		t.Fatal(err)
	}
	if after := readStoreFile(t, store); !bytes.Equal(after, before) {
		t.Fatalf("rewritten v2 store changed bytes:\n%s\nwant\n%s", after, before)
	}

	pending, found, err := store.Get("message-pending")
	if err != nil || !found {
		t.Fatalf("Get pending = (%t, %v)", found, err)
	}
	if _, changed, err := store.ApplyMatching(pending.Envelope, "claude-coordination", coremessage.Event{Kind: coremessage.EventDeliver,
		MessageRef: pending.Envelope.MessageRef, ConversationRef: pending.Envelope.ConversationRef,
		Target: pending.Envelope.Target, ObservedAt: v2FixtureNow}); err != nil || !changed {
		t.Fatalf("ApplyMatching = (%t, %v)", changed, err)
	}
	if _, created, err := store.PutAccepted(storeEnvelope(41), "codex-inbox"); err != nil || !created {
		t.Fatalf("PutAccepted = (%t, %v)", created, err)
	}
	data := readStoreFile(t, store)
	if version := storeFileVersion(t, data); version != storeVersion {
		t.Fatalf("agent-only store version = %d, want %d", version, storeVersion)
	}
	if err := decodeAsV2Build(data); err != nil {
		t.Fatalf("previous build refuses an agent-only store this build wrote: %v", err)
	}
	if strings.Contains(string(data), `"origin"`) {
		t.Fatalf("agent-only store carries an origin key: %s", data)
	}
}

// TestStoreOperatorRecordRoundTripsAsVersion3 covers the whole operator
// lifecycle the store takes part in, across a reload.
func TestStoreOperatorRecordRoundTripsAsVersion3(t *testing.T) {
	t.Parallel()
	store := copyV2Fixture(t)
	envelope := operatorStoreEnvelope("message-operator", v2FixtureNow)
	first, created, err := store.PutAccepted(envelope, "claude-coordination")
	if err != nil || !created || first.Delivery.State != coremessage.StateAccepted {
		t.Fatalf("PutAccepted = (%+v, %t, %v)", first, created, err)
	}
	data := readStoreFile(t, store)
	if version := storeFileVersion(t, data); version != operatorStoreVersion {
		t.Fatalf("store version with an operator record = %d, want %d", version, operatorStoreVersion)
	}
	if decodeAsV2Build(data) == nil {
		t.Fatal("previous build accepted a store holding operator input")
	}
	retry := envelope
	retry.AcceptedAt, retry.Deadline = retry.AcceptedAt.Add(time.Second), retry.Deadline.Add(time.Second)
	if replay, created, err := store.PutAccepted(retry, "claude-coordination"); err != nil || created || replay != first {
		t.Fatalf("retry = (%+v, %t, %v)", replay, created, err)
	}
	if _, _, err := store.PutAccepted(envelope, "codex-inbox"); err == nil {
		t.Fatal("operator record accepted on the Codex adapter")
	}
	if _, changed, err := store.MarkHandoffMatching(envelope, "claude-coordination"); err != nil || !changed {
		t.Fatalf("MarkHandoffMatching = (%t, %v)", changed, err)
	}
	// The Codex self-claim inbox never hands operator input out.
	codex := coremessage.Route{AgentUID: "agent-codex-c", PaneUID: "pane-codex-c", ActivationGeneration: "generation-codex-c",
		Provider: "codex", Incarnation: "codex-c-incarnation"}
	if claimed, ok, err := store.Claim(codex, v2FixtureNow); err != nil || (ok && claimed.Envelope.Operator()) {
		t.Fatalf("Claim = (%+v, %t, %v)", claimed, ok, err)
	}
	delivered, changed, err := store.ApplyMatching(envelope, "claude-coordination", coremessage.Event{Kind: coremessage.EventDeliver,
		MessageRef: envelope.MessageRef, ConversationRef: envelope.ConversationRef, Target: envelope.Target,
		ObservedAt: v2FixtureNow.Add(time.Second)})
	if err != nil || !changed || delivered.Delivery.State != coremessage.StateDelivered || !delivered.HandoffObserved {
		t.Fatalf("ApplyMatching = (%+v, %t, %v)", delivered, changed, err)
	}
	if status, found, err := store.Status(envelope.MessageRef, v2FixtureNow.Add(2*time.Hour)); err != nil || !found || status != delivered {
		t.Fatalf("Status = (%+v, %t, %v)", status, found, err)
	}

	reloaded, found, err := NewStoreAt(store.Path()).Get(envelope.MessageRef)
	if err != nil || !found || reloaded != delivered || !reloaded.Envelope.Operator() || reloaded.Envelope.Source != (coremessage.Route{}) {
		t.Fatalf("reload = (%+v, %t, %v)", reloaded, found, err)
	}
	var disk struct {
		Records []map[string]json.RawMessage `json:"records"`
	}
	if err := json.Unmarshal(readStoreFile(t, store), &disk); err != nil {
		t.Fatal(err)
	}
	for _, record := range disk.Records {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(record["envelope"], &fields); err != nil {
			t.Fatal(err)
		}
		_, hasOrigin := fields["origin"]
		_, hasSource := fields["source"]
		if hasOrigin == hasSource {
			t.Fatalf("record envelope has origin=%t source=%t, want exactly one: %s", hasOrigin, hasSource, record["envelope"])
		}
	}
	// Agent records written alongside it keep their v2 bytes.
	for _, ref := range []string{"message-to-codex", "message-to-claude"} {
		record, _, err := store.Get(ref)
		if err != nil {
			t.Fatal(err)
		}
		fixture, err := os.ReadFile(filepath.Join(v2FixtureDir, storeFileName))
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(record.Envelope)
		if err != nil || !bytes.Contains(fixture, encoded) {
			t.Fatalf("agent envelope %s bytes changed next to an operator record: %s", ref, encoded)
		}
	}
}

func TestStoreRefusesAReplyToOperatorInputWithItsReasonToken(t *testing.T) {
	t.Parallel()
	store := copyV2Fixture(t)
	envelope := operatorStoreEnvelope("message-operator-reply", v2FixtureNow)
	if _, _, err := store.PutAccepted(envelope, "claude-coordination"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ApplyMatching(envelope, "claude-coordination", coremessage.Event{Kind: coremessage.EventDeliver,
		MessageRef: envelope.MessageRef, ConversationRef: envelope.ConversationRef, Target: envelope.Target,
		ObservedAt: v2FixtureNow}); err != nil {
		t.Fatal(err)
	}
	before := readStoreFile(t, store)
	codex := coremessage.Route{AgentUID: "agent-codex-c", PaneUID: "pane-codex-c", ActivationGeneration: "generation-codex-c",
		Provider: "codex", Incarnation: "codex-c-incarnation"}
	_, created, err := store.PutReply(envelope.MessageRef, "message-operator-answer", "answer", envelope.Target, codex,
		v2FixtureNow, v2FixtureNow.Add(time.Minute))
	var conflict *ReplyConflictError
	if created || !errors.As(err, &conflict) || conflict.Reason != coremessage.ReasonExplicitReplyOperatorOrigin {
		t.Fatalf("PutReply = (%t, %v), want reason %q", created, err, coremessage.ReasonExplicitReplyOperatorOrigin)
	}
	if after := readStoreFile(t, store); !bytes.Equal(after, before) {
		t.Fatal("refused reply changed the store")
	}
}

func TestStoreRefusesAnOriginInsideAVersion1Or2File(t *testing.T) {
	t.Parallel()
	for _, version := range []int{legacyStoreVersion, storeVersion} {
		store := copyV2Fixture(t)
		var disk map[string]any
		if err := json.Unmarshal(readStoreFile(t, store), &disk); err != nil {
			t.Fatal(err)
		}
		disk["version"] = version
		record := disk["records"].([]any)[0].(map[string]any)
		envelope := record["envelope"].(map[string]any)
		envelope["version"] = coremessage.Version
		if version == legacyStoreVersion {
			envelope["version"] = 1
		}
		envelope["origin"] = map[string]any{"kind": coremessage.OriginKindOperator, "client": coremessage.OriginClientWeb}
		data, err := json.Marshal(disk)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store.Path(), data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.Get("message-to-codex"); !errors.Is(err, ErrMalformedStore) {
			t.Fatalf("version %d with an origin: Get error = %v, want ErrMalformedStore", version, err)
		}
	}
}

// TestStoreReturnsToVersion2OnceOperatorRecordsAreReclaimed covers the second
// half of the write rule: version 3 lasts only while operator input is stored.
// Both ways an operator record leaves (delivered and expired, each past
// retention) end in a version 2 file the previous build reads again.
func TestStoreReturnsToVersion2OnceOperatorRecordsAreReclaimed(t *testing.T) {
	t.Parallel()
	store := copyV2Fixture(t)
	now := v2FixtureNow
	store.now = func() time.Time { return now }
	delivered := operatorStoreEnvelope("message-operator-delivered", now)
	expired := operatorStoreEnvelope("message-operator-expired", now.Add(time.Second))
	for _, envelope := range []coremessage.Envelope{delivered, expired} {
		if _, _, err := store.PutAccepted(envelope, "claude-coordination"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := store.ApplyMatching(delivered, "claude-coordination", coremessage.Event{Kind: coremessage.EventDeliver,
		MessageRef: delivered.MessageRef, ConversationRef: delivered.ConversationRef, Target: delivered.Target,
		ObservedAt: now.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if version := storeFileVersion(t, readStoreFile(t, store)); version != operatorStoreVersion {
		t.Fatalf("version = %d, want %d", version, operatorStoreVersion)
	}

	if record, _, err := store.Status(expired.MessageRef, expired.Deadline); err != nil || record.Delivery.State != coremessage.StateExpired {
		t.Fatalf("Status at deadline = (%+v, %v)", record, err)
	}
	now = now.Add(expired.Deadline.Sub(expired.AcceptedAt) + terminalRetention + time.Hour)
	agent := storeEnvelope(42)
	agent.AcceptedAt, agent.Deadline = now, now.Add(time.Hour)
	if _, created, err := store.PutAccepted(agent, "codex-inbox"); err != nil || !created {
		t.Fatalf("PutAccepted = (%t, %v)", created, err)
	}
	data := readStoreFile(t, store)
	if version := storeFileVersion(t, data); version != storeVersion {
		t.Fatalf("version after operator records were reclaimed = %d, want %d", version, storeVersion)
	}
	if err := decodeAsV2Build(data); err != nil {
		t.Fatalf("previous build refuses the store after operator records were reclaimed: %v", err)
	}
	for _, envelope := range []coremessage.Envelope{delivered, expired} {
		if _, found, err := store.Get(envelope.MessageRef); err != nil || found {
			t.Fatalf("%s still stored: (%t, %v)", envelope.MessageRef, found, err)
		}
	}

	// The reclaimed operator lines name their origin and no source.
	lines := readHistoryLines(t, store.historyPath())
	operatorLines := 0
	for _, line := range lines {
		ref := historyString(t, line, "messageRef")
		if ref != delivered.MessageRef && ref != expired.MessageRef {
			continue
		}
		operatorLines++
		if _, ok := line["source"]; ok {
			t.Fatalf("operator history line carries a source: %v", line)
		}
		var origin coremessage.Origin
		if err := json.Unmarshal(line["origin"], &origin); err != nil || !origin.Operator() {
			t.Fatalf("operator history line origin = %s (%v)", line["origin"], err)
		}
	}
	if operatorLines != 2 {
		t.Fatalf("operator history lines = %d, want 2", operatorLines)
	}
}

// TestAgentHistoryLineBytesAreUnchanged decodes the previous build's history
// line and encodes it again: an Agent line gains no key and loses none.
func TestAgentHistoryLineBytesAreUnchanged(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join(v2FixtureDir, historyFileName))
	if err != nil {
		t.Fatal(err)
	}
	for line := range bytes.SplitSeq(bytes.TrimSuffix(data, []byte("\n")), []byte("\n")) {
		var record historyRecord
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(record)
		if err != nil || !bytes.Equal(encoded, line) {
			t.Fatalf("agent history line =\n%s\nwant\n%s", encoded, line)
		}
	}
}
