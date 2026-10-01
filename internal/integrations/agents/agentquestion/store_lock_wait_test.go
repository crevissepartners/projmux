package agentquestion

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	localstate "github.com/crevissepartners/projmux/internal/state"
)

// holdStoreLock takes the store's lock the way another writer does, through
// its own open of the lock file, and returns the call that lets it go.
func holdStoreLock(t *testing.T, store *Store) func() {
	t.Helper()
	lock, err := os.OpenFile(store.Path()+".flock", os.O_CREATE|os.O_RDWR, localstate.PrivateFileMode)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			unix.Flock(int(lock.Fd()), unix.LOCK_UN) //nolint:errcheck -- releasing an owned advisory lock.
			lock.Close()
		})
	}
	t.Cleanup(release)
	return release
}

// waitedPast returns a lockWaiting hook and a channel it closes once a queued
// write has waited longer than bound.
func waitedPast(bound time.Duration) (func(time.Duration), <-chan struct{}) {
	past := make(chan struct{})
	var once sync.Once
	return func(waited time.Duration) {
		if waited > bound {
			once.Do(func() { close(past) })
		}
	}, past
}

func assertStillWaiting(t *testing.T, store *Store, id string) {
	t.Helper()
	record, ok, err := store.Get(id)
	if err != nil || !ok || record.State != StateWaiting {
		t.Fatalf("record = %+v, %v, %v; want it still waiting", record, ok, err)
	}
}

func newQuestion(clock *testClock, n int) Record {
	return Record{
		ID: testID(n), AgentUID: "agt-a", PaneUID: "pan-agt-a", SessionID: "session-1", ToolUseID: "toolu_1",
		Questions: json.RawMessage(testQuestionsJSON), Deadline: clock.Now().Add(5 * time.Minute),
	}
}

// TestStoreLockWaitBoundsPerWrite pins the two product bounds and which write
// asks for which: Answer and Create wait out a holder for up to ten seconds,
// every other write keeps two.
func TestStoreLockWaitBoundsPerWrite(t *testing.T) {
	t.Parallel()

	if lockWait != 2*time.Second || patientLockWait != 10*time.Second {
		t.Fatalf("lockWait = %v, patientLockWait = %v; want 2s and 10s", lockWait, patientLockWait)
	}
	store, clock := newTestStore(t)
	var mu sync.Mutex
	var asked []time.Duration
	store.lockWaitFor = func(bound time.Duration) time.Duration {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, bound)
		return bound
	}
	for _, write := range []struct {
		name string
		run  func() error
		want time.Duration
	}{
		{"Create", func() error { _, err := store.Create(newQuestion(clock, 1)); return err }, patientLockWait},
		{"Answer", func() error { _, err := store.Answer(testID(1), "agt-a", testAnswers); return err }, patientLockWait},
		{"Settle", func() error { _, err := store.Settle(testID(1)); return err }, lockWait},
		{"Close", func() error {
			createTestRecord(t, store, clock, 2, "agt-a")
			_, err := store.Close(testID(2), CloseReasonPopupDismissed)
			return err
		}, lockWait},
		{"CloseAnsweredElsewhere", func() error {
			_, err := store.CloseAnsweredElsewhere(testID(1), "agt-a", "session-1", "request-1")
			return err
		}, lockWait},
		{"CloseAgent", func() error { _, err := store.CloseAgent("agt-a"); return err }, lockWait},
	} {
		mu.Lock()
		asked = nil
		mu.Unlock()
		if err := write.run(); err != nil {
			t.Fatalf("%s: %v", write.name, err)
		}
		mu.Lock()
		got := slices.Clone(asked)
		mu.Unlock()
		if n := len(got); n == 0 || got[n-1] != write.want {
			t.Fatalf("%s asked for lock bounds %v; want its own write to ask for %v", write.name, got, write.want)
		}
	}
}

// TestStoreAnswerAndCreateWaitOutAHolderPastTheOldBound is the slow-disk
// case: another writer keeps the lock longer than the two-second bound but
// inside the patient one. Answer to another question and Create of a new one
// must queue behind it and succeed. The holder lets go only after it saw the
// queued write wait past the old bound, so the order of events decides the
// test, not the clock; the old bound is shortened so the test stays fast.
func TestStoreAnswerAndCreateWaitOutAHolderPastTheOldBound(t *testing.T) {
	t.Parallel()

	const oldBound = 20 * time.Millisecond
	for _, write := range []string{"Answer", "Create"} {
		t.Run(write, func(t *testing.T) {
			t.Parallel()

			store, clock := newTestStore(t)
			createTestRecord(t, store, clock, 1, "agt-a")
			other := createTestRecord(t, store, clock, 2, "agt-a")
			store.lockWaitFor = func(bound time.Duration) time.Duration {
				if bound == lockWait {
					return oldBound
				}
				return bound
			}
			hook, past := waitedPast(2 * oldBound)
			store.lockWaiting = hook
			release := holdStoreLock(t, store)

			done := make(chan error, 1)
			go func() {
				switch write {
				case "Answer":
					_, err := store.Answer(other.ID, "agt-a", testAnswers)
					done <- err
				default:
					_, err := store.Create(newQuestion(clock, 3))
					done <- err
				}
			}()
			select {
			case <-past:
			case err := <-done:
				t.Fatalf("%s returned %v while another writer still held the lock; want it to wait past the old bound", write, err)
			}
			release()
			if err := <-done; err != nil {
				t.Fatalf("%s after the holder let go: %v", write, err)
			}
			switch write {
			case "Answer":
				if record, ok, err := store.Get(other.ID); err != nil || !ok || record.State != StateAnswered {
					t.Fatalf("answered record = %+v, %v, %v; want it answered", record, ok, err)
				}
			default:
				assertStillWaiting(t, store, testID(3))
			}
		})
	}
}

// TestStoreAnswerAndCreateRefuseAsBusyPastThePatientBound keeps the
// non-guarantee: a holder that outlasts the patient bound still makes Answer
// and Create refuse with the unchanged busy error, and the question stays
// waiting. The holder lets go only after the write returned.
func TestStoreAnswerAndCreateRefuseAsBusyPastThePatientBound(t *testing.T) {
	t.Parallel()

	const patientBound = 30 * time.Millisecond
	for _, write := range []string{"Answer", "Create"} {
		t.Run(write, func(t *testing.T) {
			t.Parallel()

			store, clock := newTestStore(t)
			record := createTestRecord(t, store, clock, 1, "agt-a")
			store.lockWaitFor = func(bound time.Duration) time.Duration {
				if bound == patientLockWait {
					return patientBound
				}
				return bound
			}
			release := holdStoreLock(t, store)

			started := time.Now()
			var err error
			switch write {
			case "Answer":
				_, err = store.Answer(record.ID, "agt-a", testAnswers)
			default:
				_, err = store.Create(newQuestion(clock, 2))
			}
			waited := time.Since(started)
			release()
			if !errors.Is(err, ErrBusy) || err.Error() != "agent question store is busy" {
				t.Fatalf("%s err = %v; want %q", write, err, ErrBusy)
			}
			if waited < patientBound {
				t.Fatalf("%s gave up after %v; want it to wait the whole bound %v", write, waited, patientBound)
			}
			assertStillWaiting(t, store, record.ID)
			if _, ok, err := store.Get(testID(2)); err != nil || ok {
				t.Fatalf("refused Create left a record: %v, %v", ok, err)
			}
		})
	}
}

// TestStoreSettleStillRefusesAsBusyAtTwoSeconds holds the bound the hook's
// settle loop relies on, with the product value: Settle behind a holder gives
// up at two seconds, well before the patient bound, and the question stays
// waiting. The holder lets go only after Settle returned.
func TestStoreSettleStillRefusesAsBusyAtTwoSeconds(t *testing.T) {
	t.Parallel()

	store, clock := newTestStore(t)
	record := createTestRecord(t, store, clock, 1, "agt-a")
	release := holdStoreLock(t, store)

	started := time.Now()
	_, err := store.Settle(record.ID)
	waited := time.Since(started)
	release()
	if !errors.Is(err, ErrBusy) || err.Error() != "agent question store is busy" {
		t.Fatalf("Settle err = %v; want %q", err, ErrBusy)
	}
	if waited < lockWait || waited >= patientLockWait {
		t.Fatalf("Settle gave up after %v; want at least %v and under %v", waited, lockWait, patientLockWait)
	}
	assertStillWaiting(t, store, record.ID)
}
