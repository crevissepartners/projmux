package app

import (
	"errors"
	"os"
	"path/filepath"
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
	released := false
	release = func() {
		if released {
			return
		}
		released = true
		_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
		_ = lock.Close()
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

// TestBrokerDurableRecordLosesToContention pins the reported failure mode:
// one held lock is enough to turn a delivered message into a persist failure.
func TestBrokerDurableRecordLosesToContention(t *testing.T) {
	broker, envelope, path := brokerWithAcceptedRecord(t, "message-broker-contention")
	holdMessageStoreLock(t, path)
	if err := broker.MarkDelivered(envelope, time.Now().UTC()); !errors.Is(err, messagestore.ErrBusy) {
		t.Fatalf("contended broker MarkDelivered err=%v, want ErrBusy", err)
	}
	if err := broker.MarkHandoff(envelope); !errors.Is(err, messagestore.ErrBusy) {
		t.Fatalf("contended broker MarkHandoff err=%v, want ErrBusy", err)
	}
}
