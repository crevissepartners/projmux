package app

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

// holdMessageStoreLock takes the same advisory lock the store takes, from an
// independent open file description, so the broker observes real contention
// rather than an in-process substitute.
func holdMessageStoreLock(t *testing.T, storePath string) (release func()) {
	t.Helper()
	if err := localstate.EnsurePrivateDir(filepath.Dir(storePath)); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(storePath+".flock", os.O_CREATE|os.O_RDWR, localstate.PrivateFileMode)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
			_ = lock.Close()
		})
	}
	t.Cleanup(release)
	return release
}

// brokerWithAcceptedRecord builds the live broker over a live coordination
// fixture, so MarkHandoff passes its route check and reaches the store, and
// seeds one accepted Claude coordination record for it to mark.
func brokerWithAcceptedRecord(t *testing.T, ref string) (*liveClaudeDialogueBroker, coremessage.Envelope, string) {
	t.Helper()
	fixture := newClaudeCoordinationTestFixture(t)
	broker, err := newLiveClaudeDialogueBroker(fixture.registryPath)
	if err != nil {
		t.Fatal(err)
	}
	envelope := *dialogueForRoute(ref, fixture.route, time.Now().UTC()).BrokerEnvelope
	envelope.Source = envelope.Target // Both ends resolve to the fixture's live route.
	if !broker.Current(envelope) {
		t.Fatal("fixture route is not current")
	}
	stateDir := filepath.Dir(filepath.Dir(fixture.registryPath))
	if _, _, err := messagestore.NewStore(stateDir).PutAccepted(envelope, "claude-coordination"); err != nil {
		t.Fatal(err)
	}
	return broker, envelope, broker.store.Path()
}

// TestBrokerPushRecordsWaitWhileReplyCommitRefuses contends each path of one
// live broker in turn. The push records wait out a brief holder instead of
// turning a delivered message into a persist failure; the reply commit and its
// status read still refuse at once, before any durable write.
func TestBrokerPushRecordsWaitWhileReplyCommitRefuses(t *testing.T) {
	broker, envelope, path := brokerWithAcceptedRecord(t, "message-broker-contention")
	if broker.pushStore.Path() != path {
		t.Fatalf("push store=%q reply store=%q, want one inbox", broker.pushStore.Path(), path)
	}
	const hold = 100 * time.Millisecond
	for _, mark := range []struct {
		name string
		run  func() error
	}{
		{"MarkHandoff", func() error { return broker.MarkHandoff(envelope) }},
		{"MarkDelivered", func() error { return broker.MarkDelivered(envelope, time.Now().UTC()) }},
	} {
		time.AfterFunc(hold, holdMessageStoreLock(t, path))
		started := time.Now()
		err := mark.run()
		if waited := time.Since(started); err != nil || waited < hold {
			t.Fatalf("%s behind a brief holder err=%v waited=%s, want success after %s", mark.name, err, waited, hold)
		}
	}
	record, found, err := messagestore.NewStoreAt(path).Get(envelope.MessageRef)
	if err != nil || !found || !record.HandoffObserved || record.Delivery.State != coremessage.StateDelivered {
		t.Fatalf("push records = %+v found=%t err=%v", record, found, err)
	}

	now := time.Now().UTC()
	reply := coremessage.Envelope{Version: coremessage.Version, MessageRef: "reply-broker-contention",
		ConversationRef: envelope.ConversationRef, ReplyTo: envelope.MessageRef, Source: envelope.Target, Target: envelope.Source,
		Authority: coremessage.PeerAuthority(), Payload: "answer", AcceptedAt: now, Deadline: envelope.Deadline}
	release := holdMessageStoreLock(t, path)
	defer release()
	for _, commit := range []struct {
		name string
		run  func() error
	}{
		{"ReplyStatus", func() error { _, _, err := broker.ReplyStatus(envelope.MessageRef); return err }},
		{"CommitReply", func() error { _, err := broker.CommitReply(envelope, reply); return err }},
	} {
		started := time.Now()
		err := commit.run()
		// The holder outlives these calls, so a view that waited would take
		// at least the push bound before refusing.
		if waited := time.Since(started); !errors.Is(err, messagestore.ErrBusy) || waited >= 2*time.Second {
			t.Fatalf("contended %s err=%v waited=%s, want an immediate ErrBusy", commit.name, err, waited)
		}
	}
}
