package agentmessage

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
)

func holdStoreRecord(t *testing.T, store *Store, envelope coremessage.Envelope) {
	t.Helper()
	if _, _, err := store.PutAccepted(envelope, "claude-coordination"); err != nil {
		t.Fatal(err)
	}
	if record, _, err := store.Apply(envelope.MessageRef, coremessage.Event{Kind: coremessage.EventHold, MessageRef: envelope.MessageRef,
		ConversationRef: envelope.ConversationRef, Target: envelope.Target, Reason: "target-awaiting-operator",
		ObservedAt: envelope.AcceptedAt}); err != nil || record.Delivery.State != coremessage.StateHeld {
		t.Fatalf("hold %s = %+v err=%v", envelope.MessageRef, record.Delivery, err)
	}
}

func TestHeldForListsOnlyHeldClaudeRecordsOfOneTargetOldestFirst(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	store := frozenStore(NewStore(stateDir))
	if held, err := store.HeldFor("agent-target"); err != nil || len(held) != 0 {
		t.Fatalf("empty store HeldFor = %v, %v", held, err)
	}
	if entries, err := os.ReadDir(stateDir); err != nil || len(entries) != 0 {
		t.Fatalf("HeldFor on a missing store created %v (err %v)", entries, err)
	}
	// Held out of acceptance order, one accepted, one held for another Agent.
	for _, index := range []int{3, 1, 2} {
		holdStoreRecord(t, store, claudeStoreEnvelope(index))
	}
	if _, _, err := store.PutAccepted(claudeStoreEnvelope(4), "claude-coordination"); err != nil {
		t.Fatal(err)
	}
	other := claudeStoreEnvelope(5)
	other.Target.AgentUID = "agent-other"
	holdStoreRecord(t, store, other)

	held, err := store.HeldFor("agent-target")
	if err != nil {
		t.Fatal(err)
	}
	var refs []string
	for _, record := range held {
		refs = append(refs, record.Envelope.MessageRef)
	}
	if want := []string{"message-001", "message-002", "message-003"}; !slices.Equal(refs, want) {
		t.Fatalf("HeldFor = %v, want %v", refs, want)
	}
}

func TestLockTargetReleaseSerializesOneTargetAndBoundsTheWait(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	store := NewStore(stateDir)
	unlock, err := store.LockTargetRelease("agent-target", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.LockTargetRelease("agent-target", 30*time.Millisecond); !errors.Is(err, ErrBusy) {
		t.Fatalf("second holder error = %v, want ErrBusy at the bound", err)
	}
	other, err := store.LockTargetRelease("agent-other", 30*time.Millisecond)
	if err != nil {
		t.Fatalf("another target waited on this target's lock: %v", err)
	}
	other()
	time.AfterFunc(50*time.Millisecond, unlock)
	started := time.Now()
	again, err := store.LockTargetRelease("agent-target", 2*time.Second)
	if err != nil || time.Since(started) < 50*time.Millisecond {
		t.Fatalf("waiter = %v after %s, want the lock once the holder released", err, time.Since(started))
	}
	again()
	if _, err := os.Stat(filepath.Join(stateDir, storeDirName, storeFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the release lock wrote the store: %v", err)
	}
}
