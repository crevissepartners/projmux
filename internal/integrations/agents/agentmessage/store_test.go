package agentmessage

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
)

var storeTestNow = time.Date(2026, 9, 6, 2, 3, 4, 0, time.UTC)

func storeEnvelope(index int) coremessage.Envelope {
	return coremessage.Envelope{
		Version: coremessage.Version, MessageRef: fmt.Sprintf("message-%03d", index), ConversationRef: fmt.Sprintf("conversation-%03d", index),
		Source:    coremessage.Route{AgentUID: "agent-source", PaneUID: "pane-source", ActivationGeneration: "generation-source", Provider: "claude"},
		Target:    coremessage.Route{AgentUID: "agent-target", PaneUID: "pane-target", ActivationGeneration: "generation-target", Provider: "codex"},
		Authority: coremessage.PeerAuthority(), Payload: fmt.Sprintf("payload-%03d", index),
		AcceptedAt: storeTestNow.Add(time.Duration(index) * time.Nanosecond), Deadline: storeTestNow.Add(time.Hour + time.Duration(index)*time.Nanosecond),
	}
}

func TestStoreRetryRestartAndSecretFreeWire(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "messages", "messages.json")
	store := NewStoreAt(path)
	envelope := storeEnvelope(1)
	first, created, err := store.PutAccepted(envelope, "codex-inbox")
	if err != nil || !created || first.Delivery.State != coremessage.StateAccepted {
		t.Fatalf("PutAccepted = (%+v, %t, %v)", first, created, err)
	}
	retry := envelope
	retry.AcceptedAt = retry.AcceptedAt.Add(time.Minute)
	retry.Deadline = retry.Deadline.Add(time.Minute)
	second, created, err := store.PutAccepted(retry, "codex-inbox")
	if err != nil || created || second != first {
		t.Fatalf("idempotent retry = (%+v, %t, %v)", second, created, err)
	}
	retry.Payload = "changed"
	if _, _, err := store.PutAccepted(retry, "codex-inbox"); !errors.Is(err, coremessage.ErrRetryMismatch) {
		t.Fatalf("mismatched retry error = %v", err)
	}

	restarted := NewStoreAt(path)
	got, found, err := restarted.Get(envelope.MessageRef)
	if err != nil || !found || got != first {
		t.Fatalf("restart Get = (%+v, %t, %v)", got, found, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"token", "socket", "threadId", "sessionId", "locator", "credential"} {
		if strings.Contains(strings.ToLower(string(data)), strings.ToLower(forbidden)) {
			t.Errorf("store wire retained forbidden field %q: %s", forbidden, data)
		}
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("store mode = %v", info.Mode())
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o700 {
		t.Fatalf("store directory mode = %v", info.Mode())
	}
}

func TestStoreRefusesFullNonterminalCapacityWithoutChangingDisk(t *testing.T) {
	t.Parallel()
	store := NewStoreAt(filepath.Join(t.TempDir(), "messages.json"))
	records := make([]Record, maxRecords)
	for i := range records {
		envelope := storeEnvelope(i + 100)
		delivery, changed := coremessage.Reduce(coremessage.Delivery{}, envelope, coremessage.Event{Kind: coremessage.EventAccept,
			MessageRef: envelope.MessageRef, ConversationRef: envelope.ConversationRef, Target: envelope.Target, ObservedAt: envelope.AcceptedAt})
		if !changed {
			t.Fatalf("accept record %d", i)
		}
		records[i] = Record{Envelope: envelope, Delivery: delivery, Adapter: "codex-inbox"}
	}
	if err := store.withLock(func() error { return store.writeLocked(diskState{Version: storeVersion, Records: records}) }); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := store.PutAccepted(storeEnvelope(999), "codex-inbox"); !errors.Is(err, ErrCapacity) || created {
		t.Fatalf("capacity put = created %t err %v", created, err)
	}
	after, err := os.ReadFile(store.Path())
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("capacity refusal changed disk: err=%v", err)
	}
}

func TestStoreRejectsOversizedFileOnRestart(t *testing.T) {
	t.Parallel()
	store := NewStoreAt(filepath.Join(t.TempDir(), "messages.json"))
	if _, _, err := store.PutAccepted(storeEnvelope(1), "codex-inbox"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path(), bytes.Repeat([]byte{'x'}, maxStoreBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewStoreAt(store.Path()).Get("message-001"); !errors.Is(err, ErrMalformedStore) {
		t.Fatalf("oversized restart error = %v", err)
	}
}

func TestStoreRejectsUnknownProviderSecretFieldOnRestart(t *testing.T) {
	t.Parallel()
	store := NewStoreAt(filepath.Join(t.TempDir(), "messages.json"))
	if _, _, err := store.PutAccepted(storeEnvelope(1), "codex-inbox"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte(`"adapter":"codex-inbox"`),
		[]byte(`"adapter":"codex-inbox","token":"must-not-enter-v1-schema"`), 1)
	if err := os.WriteFile(store.Path(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewStoreAt(store.Path()).Get("message-001"); !errors.Is(err, ErrMalformedStore) {
		t.Fatalf("unknown provider secret field restart error = %v", err)
	}
}

func TestStoreAtomicClaimIsOldestExactActivationAndTerminalOnce(t *testing.T) {
	t.Parallel()
	store := NewStoreAt(filepath.Join(t.TempDir(), "messages.json"))
	first, second := storeEnvelope(1), storeEnvelope(2)
	if _, _, err := store.PutAccepted(second, "codex-inbox"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.PutAccepted(first, "codex-inbox"); err != nil {
		t.Fatal(err)
	}
	foreign := first.Target
	foreign.ActivationGeneration = "replacement"
	if _, claimed, err := store.Claim(foreign, storeTestNow.Add(time.Minute)); err != nil || claimed {
		t.Fatalf("foreign claim = %t, %v", claimed, err)
	}
	claimed, ok, err := store.Claim(first.Target, storeTestNow.Add(time.Minute))
	if err != nil || !ok || claimed.Envelope.MessageRef != first.MessageRef || claimed.Envelope.Payload != first.Payload ||
		claimed.Delivery.State != coremessage.StateDelivered || claimed.Delivery.Reason != "target-self-claim" {
		t.Fatalf("first claim = (%+v, %t, %v)", claimed, ok, err)
	}
	duplicate, ok, err := store.Claim(first.Target, storeTestNow.Add(2*time.Minute))
	if err != nil || !ok || duplicate.Envelope.MessageRef != second.MessageRef {
		t.Fatalf("second claim = (%+v, %t, %v)", duplicate, ok, err)
	}
	if _, ok, err := store.Claim(first.Target, storeTestNow.Add(3*time.Minute)); err != nil || ok {
		t.Fatalf("duplicate terminal claim = %t, %v", ok, err)
	}
}

func TestStoreConcurrentWritersAndClaimersSerialize(t *testing.T) {
	t.Parallel()
	store := NewStoreAt(filepath.Join(t.TempDir(), "messages.json"))
	const count = 64
	var writers sync.WaitGroup
	errs := make(chan error, count)
	for index := range count {
		writers.Add(1)
		go func(index int) {
			defer writers.Done()
			_, _, err := store.PutAccepted(storeEnvelope(index), "codex-inbox")
			errs <- err
		}(index)
	}
	writers.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	claimed := make(chan string, count)
	claimErrs := make(chan error, count)
	var readers sync.WaitGroup
	for range count {
		readers.Go(func() {
			if record, ok, err := store.Claim(storeEnvelope(0).Target, storeTestNow.Add(time.Minute)); err != nil {
				claimErrs <- err
			} else if ok {
				claimed <- record.Envelope.MessageRef
			}
		})
	}
	readers.Wait()
	close(claimed)
	close(claimErrs)
	for err := range claimErrs {
		if err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	for ref := range claimed {
		if seen[ref] {
			t.Fatalf("message %s claimed twice", ref)
		}
		seen[ref] = true
	}
	if len(seen) != count {
		t.Fatalf("claimed %d records, want %d", len(seen), count)
	}
}

func TestStoreAtomicReplaceFailurePreservesPriorBytes(t *testing.T) {
	t.Parallel()
	store := NewStoreAt(filepath.Join(t.TempDir(), "messages.json"))
	if _, _, err := store.PutAccepted(storeEnvelope(1), "codex-inbox"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	store.hooks.beforeRename = func() error { return errors.New("injected before rename") }
	if _, _, err := store.PutAccepted(storeEnvelope(2), "codex-inbox"); err == nil {
		t.Fatal("injected failure succeeded")
	}
	after, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("failed atomic replace changed prior bytes")
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(store.Path()), ".messages.tmp-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("staged files = %v, err=%v", matches, err)
	}
}

func TestStoreBoundsRetentionTimeoutAndMalformedRestart(t *testing.T) {
	t.Parallel()
	store := NewStoreAt(filepath.Join(t.TempDir(), "messages.json"))
	expired := storeEnvelope(1)
	expired.Deadline = expired.AcceptedAt.Add(time.Second)
	if _, _, err := store.PutAccepted(expired, "codex-inbox"); err != nil {
		t.Fatal(err)
	}
	status, found, err := store.Status(expired.MessageRef, expired.Deadline.Add(time.Second))
	if err != nil || !found || status.Delivery.State != coremessage.StateExpired {
		t.Fatalf("expired status = (%+v, %t, %v)", status, found, err)
	}

	oversized := storeEnvelope(2)
	oversized.Payload = strings.Repeat("x", coremessage.MaxPayloadBytes+1)
	if _, _, err := store.PutAccepted(oversized, "codex-inbox"); !errors.Is(err, coremessage.ErrInvalidEnvelope) {
		t.Fatalf("oversized payload error = %v", err)
	}

	old := make([]Record, maxRecords)
	for i := range old {
		envelope := storeEnvelope(i + 1000)
		envelope.AcceptedAt = storeTestNow.Add(-terminalRetention - 2*time.Hour).Add(time.Duration(i) * time.Nanosecond)
		envelope.Deadline = envelope.AcceptedAt.Add(time.Minute)
		delivery, _ := coremessage.Reduce(coremessage.Delivery{}, envelope, coremessage.Event{Kind: coremessage.EventAccept,
			MessageRef: envelope.MessageRef, ConversationRef: envelope.ConversationRef, Target: envelope.Target, ObservedAt: envelope.AcceptedAt})
		delivery, _ = coremessage.Reduce(delivery, envelope, coremessage.Event{Kind: coremessage.EventDeliver,
			MessageRef: envelope.MessageRef, ConversationRef: envelope.ConversationRef, Target: envelope.Target,
			Reason: "old", ObservedAt: envelope.AcceptedAt.Add(time.Second)})
		old[i] = Record{Envelope: envelope, Delivery: delivery, Adapter: "codex-inbox"}
	}
	store.hooks = storeHooks{}
	store.now = func() time.Time { return storeTestNow }
	if err := store.withLock(func() error { return store.writeLocked(diskState{Version: storeVersion, Records: old}) }); err != nil {
		t.Fatal(err)
	}
	if _, created, err := store.PutAccepted(storeEnvelope(3), "codex-inbox"); err != nil || !created {
		t.Fatalf("retention put = %t, %v", created, err)
	}
	if _, found, err := store.Get(old[0].Envelope.MessageRef); err != nil || found {
		t.Fatalf("old terminal retained = %t, %v", found, err)
	}

	if err := os.WriteFile(store.Path(), []byte(`{"version":1,"records":[]} trailing`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewStoreAt(store.Path()).Get("anything"); !errors.Is(err, ErrMalformedStore) {
		t.Fatalf("malformed restart error = %v", err)
	}
}

func TestStorePostHandoffDeadlineIsFailedUnknownAcrossRestart(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "messages.json")
	store := NewStoreAt(path)
	envelope := storeEnvelope(77)
	envelope.Target.Provider = "claude"
	if _, _, err := store.PutAccepted(envelope, "claude-coordination"); err != nil {
		t.Fatal(err)
	}
	if marked, changed, err := store.MarkHandoff(envelope.MessageRef); err != nil || !changed || !marked.HandoffObserved {
		t.Fatalf("mark handoff = (%#v, %t, %v)", marked, changed, err)
	}
	restarted := NewStoreAt(path)
	status, found, err := restarted.Status(envelope.MessageRef, envelope.Deadline.Add(time.Second))
	if err != nil || !found || status.Delivery.State != coremessage.StateFailed || !status.Delivery.OutcomeUnknown ||
		status.Delivery.Reason != "provider-handoff-outcome-unknown" {
		t.Fatalf("post-handoff timeout = (%#v, %t, %v)", status, found, err)
	}
}

func TestCodexClaimLeavesUnrelatedOverdueClaudeHandoffUnchanged(t *testing.T) {
	t.Parallel()
	store := NewStoreAt(filepath.Join(t.TempDir(), "messages.json"))
	claude := storeEnvelope(81)
	claude.Target.Provider = "claude"
	claude.Deadline = claude.AcceptedAt.Add(time.Second)
	if _, _, err := store.PutAccepted(claude, "claude-coordination"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.MarkHandoff(claude.MessageRef); err != nil {
		t.Fatal(err)
	}
	codex := storeEnvelope(82)
	codex.AcceptedAt = claude.Deadline.Add(time.Second)
	codex.Deadline = codex.AcceptedAt.Add(time.Minute)
	if _, _, err := store.PutAccepted(codex, "codex-inbox"); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := store.Claim(codex.Target, codex.AcceptedAt); err != nil || !claimed {
		t.Fatalf("Codex claim = %t, %v", claimed, err)
	}
	status, found, err := store.Get(claude.MessageRef)
	if err != nil || !found || status.Delivery.State != coremessage.StateAccepted || !status.HandoffObserved {
		t.Fatalf("Claude handoff after Codex claim = (%#v, %t, %v)", status, found, err)
	}
}

func TestMarkHandoffRefusesCodexRecordWithoutChangingDisk(t *testing.T) {
	t.Parallel()
	store := NewStoreAt(filepath.Join(t.TempDir(), "messages.json"))
	envelope := storeEnvelope(91)
	if _, _, err := store.PutAccepted(envelope, "codex-inbox"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if _, changed, err := store.MarkHandoff(envelope.MessageRef); err == nil || changed {
		t.Fatalf("Codex handoff = changed %t err %v", changed, err)
	}
	after, err := os.ReadFile(store.Path())
	if err != nil || string(after) != string(before) {
		t.Fatalf("Codex handoff changed disk: err=%v", err)
	}
	if _, found, err := NewStoreAt(store.Path()).Get(envelope.MessageRef); err != nil || !found {
		t.Fatalf("restart after refusal = found %t err %v", found, err)
	}
}
