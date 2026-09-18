package agentmessage

import (
	"errors"
	"testing"
	"time"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
)

// holdStoreLock takes the store lock on another goroutine until release is
// closed. The returned channel reports the holder's own result.
func holdStoreLock(t *testing.T, owner *Store) (release chan struct{}, done chan error) {
	t.Helper()
	locked := make(chan struct{})
	release, done = make(chan struct{}), make(chan error, 1)
	go func() { done <- owner.withLock(func() error { close(locked); <-release; return nil }) }()
	<-locked
	return release, done
}

func frozenStore(store *Store) *Store {
	store.now = func() time.Time { return storeTestNow }
	return store
}

func claudeStoreEnvelope(index int) coremessage.Envelope {
	envelope := storeEnvelope(index)
	envelope.Target.Provider = "claude"
	return envelope
}

func TestBoundedWaitStoreWaitsOutABriefHolder(t *testing.T) {
	stateDir := t.TempDir()
	owner, broker := NewStore(stateDir), frozenStore(NewBoundedWaitStore(stateDir))
	if broker.lockWait != 2*time.Second || broker.nonblocking {
		t.Fatalf("broker view lockWait=%s nonblocking=%t, want a 2s bound", broker.lockWait, broker.nonblocking)
	}
	const hold = 100 * time.Millisecond
	release, done := holdStoreLock(t, owner)
	time.AfterFunc(hold, func() { close(release) })
	started := time.Now()
	record, created, err := broker.PutAccepted(claudeStoreEnvelope(1), "claude-coordination")
	waited := time.Since(started)
	if lockErr := <-done; lockErr != nil {
		t.Fatal(lockErr)
	}
	if err != nil || !created || record.Delivery.State != coremessage.StateAccepted {
		t.Fatalf("broker view behind a brief holder = (%+v, %t, %v)", record, created, err)
	}
	if waited < hold {
		t.Fatalf("broker view returned after %s, before the %s holder released", waited, hold)
	}
}

func TestBoundedWaitStoreRefusesAtItsBound(t *testing.T) {
	stateDir := t.TempDir()
	owner, broker := NewStore(stateDir), frozenStore(NewBoundedWaitStore(stateDir))
	broker.lockWait = 50 * time.Millisecond
	release, done := holdStoreLock(t, owner)
	started := time.Now()
	// The holder releases only after the broker call returns, so a view that
	// waited without a bound would never return here.
	_, _, err := broker.PutAccepted(claudeStoreEnvelope(1), "claude-coordination")
	waited := time.Since(started)
	close(release)
	if lockErr := <-done; lockErr != nil {
		t.Fatal(lockErr)
	}
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("broker view past its bound err=%v, want ErrBusy", err)
	}
	if waited < broker.lockWait {
		t.Fatalf("broker view refused after %s, before its %s bound", waited, broker.lockWait)
	}
	if _, found, err := owner.Get(claudeStoreEnvelope(1).MessageRef); err != nil || found {
		t.Fatalf("refused broker view wrote later: found=%t err=%v", found, err)
	}
}

func TestNonblockingStoreRefusesWithoutWaiting(t *testing.T) {
	stateDir := t.TempDir()
	owner, helper := NewStore(stateDir), NewNonblockingStore(stateDir)
	release, done := holdStoreLock(t, owner)
	started := time.Now()
	_, _, err := helper.PutAccepted(storeEnvelope(1), "codex-inbox")
	waited := time.Since(started)
	close(release)
	if lockErr := <-done; lockErr != nil {
		t.Fatal(lockErr)
	}
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("contended helper view err=%v, want ErrBusy", err)
	}
	if helper.lockWait != 0 || waited >= defaultLockWait {
		t.Fatalf("helper view lockWait=%s waited %s, want an immediate refusal", helper.lockWait, waited)
	}
}

func TestMatchingMarksTakeTheLockOnce(t *testing.T) {
	store := frozenStore(NewStore(t.TempDir()))
	envelope := claudeStoreEnvelope(1)
	if _, _, err := store.PutAccepted(envelope, "claude-coordination"); err != nil {
		t.Fatal(err)
	}
	locks := 0
	store.hooks.afterLock = func() { locks++ }
	marked, changed, err := store.MarkHandoffMatching(envelope, "claude-coordination")
	if err != nil || !changed || !marked.HandoffObserved || locks != 1 {
		t.Fatalf("MarkHandoffMatching = (%+v, %t, %v) locks=%d, want one lock", marked, changed, err, locks)
	}
	locks = 0
	delivered, changed, err := store.ApplyMatching(envelope, "claude-coordination", coremessage.Event{Kind: coremessage.EventDeliver,
		MessageRef: envelope.MessageRef, ConversationRef: envelope.ConversationRef, Target: envelope.Target, ObservedAt: storeTestNow.Add(time.Minute)})
	if err != nil || !changed || delivered.Delivery.State != coremessage.StateDelivered || locks != 1 {
		t.Fatalf("ApplyMatching = (%+v, %t, %v) locks=%d, want one lock", delivered, changed, err, locks)
	}
}

func TestMatchingMarksRefuseAnotherAttemptWithoutWriting(t *testing.T) {
	store := frozenStore(NewStore(t.TempDir()))
	envelope := claudeStoreEnvelope(1)
	if _, _, err := store.PutAccepted(envelope, "claude-coordination"); err != nil {
		t.Fatal(err)
	}
	changedPayload := envelope
	changedPayload.Payload = "changed"
	missing := claudeStoreEnvelope(2)
	deliver := coremessage.Event{Kind: coremessage.EventDeliver, MessageRef: envelope.MessageRef,
		ConversationRef: envelope.ConversationRef, Target: envelope.Target, ObservedAt: storeTestNow.Add(time.Minute)}
	for name, test := range map[string]struct {
		envelope coremessage.Envelope
		adapter  string
	}{
		"missing":         {missing, "claude-coordination"},
		"other adapter":   {envelope, "codex-inbox"},
		"different retry": {changedPayload, "claude-coordination"},
	} {
		if _, changed, err := store.MarkHandoffMatching(test.envelope, test.adapter); !errors.Is(err, coremessage.ErrInvalidEnvelope) || changed {
			t.Fatalf("%s MarkHandoffMatching changed=%t err=%v", name, changed, err)
		}
		if _, changed, err := store.ApplyMatching(test.envelope, test.adapter, deliver); !errors.Is(err, coremessage.ErrInvalidEnvelope) || changed {
			t.Fatalf("%s ApplyMatching changed=%t err=%v", name, changed, err)
		}
	}
	stored, found, err := store.Get(envelope.MessageRef)
	if err != nil || !found || stored.HandoffObserved || stored.Delivery.State != coremessage.StateAccepted {
		t.Fatalf("refused marks wrote: %+v found=%t err=%v", stored, found, err)
	}
}
