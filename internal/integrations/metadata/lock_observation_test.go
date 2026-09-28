package metadata

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// manualClock is a clock that stands still until the test moves it. It is how
// an observation test pins Wait and Held exactly: the only time that passes is
// the time the test adds at a known point inside or before the lease.
type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *manualClock) read() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) advance(step time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(step)
}

// recordLockObservations installs a per-Store observer that also proves the
// observation runs after the lease is gone: from inside the observer an
// independent descriptor must take the kernel lock without blocking, and the
// legacy marker must already be removed.
func recordLockObservations(t *testing.T, store *Store) *[]LockObservation {
	t.Helper()
	var observed []LockObservation
	store.SetLockObserver(func(observation LockObservation) {
		if observation.Outcome == LockOutcomeReleased {
			assertRegistryLockFree(t, store)
		}
		observed = append(observed, observation)
	})
	return &observed
}

func assertRegistryLockFree(t *testing.T, store *Store) {
	t.Helper()
	probe, err := os.OpenFile(store.flockPath, os.O_RDWR, 0)
	if err != nil {
		t.Errorf("open registry flock from the observer: %v", err)
		return
	}
	defer func() { _ = probe.Close() }()
	if err := unix.Flock(int(probe.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Errorf("the observer ran while the registry flock was still held: %v", err)
		return
	}
	_ = unix.Flock(int(probe.Fd()), unix.LOCK_UN)
	if _, err := os.Stat(store.lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the observer ran while the legacy marker still existed (stat err=%v)", err)
	}
}

// TestRegistryLockObservationReportsWaitAndHoldAfterRelease pins the seam's
// measurement: one observation per acquisition, named by the entry point, with
// Wait and Held read on the Store's injected clock, and delivered only once the
// lock is free again. It is not parallel because it also installs the
// process-wide observer, which every Store of this package would otherwise see.
func TestRegistryLockObservationReportsWaitAndHoldAfterRelease(t *testing.T) {
	clock := &manualClock{now: fixedNow}
	store := NewStore(PathFor(t.TempDir()))
	store.SetClock(clock.read)
	observed := recordLockObservations(t, store)

	// Update waits 2s for a contended kernel lock, then holds it for 3s.
	holdRegistryFlock(t, store)
	store.hooks.afterContendedFlock = func() {
		clock.advance(2 * time.Second)
		releaseRegistryFlock(t, store)
	}
	if _, err := store.Update(func(*coremetadata.Registry) error {
		clock.advance(3 * time.Second)
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	store.hooks.afterContendedFlock = nil
	if _, _, err := store.UpdateConvergent(func(*coremetadata.Registry) error {
		clock.advance(time.Second)
		return nil
	}); err != nil {
		t.Fatalf("UpdateConvergent: %v", err)
	}
	if _, err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := store.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := store.WithAdmissionBarrier(func(coremetadata.Registry) error {
		clock.advance(500 * time.Millisecond)
		return nil
	}); err != nil {
		t.Fatalf("WithAdmissionBarrier: %v", err)
	}

	want := []LockObservation{
		{Operation: LockOperationUpdate, Outcome: LockOutcomeReleased, Wait: 2 * time.Second, Held: 3 * time.Second},
		{Operation: LockOperationUpdateConvergent, Outcome: LockOutcomeReleased, Held: time.Second},
		{Operation: LockOperationLoad, Outcome: LockOutcomeReleased},
		{Operation: LockOperationMigrate, Outcome: LockOutcomeReleased},
		{Operation: LockOperationAdmissionBarrier, Outcome: LockOutcomeReleased, Held: 500 * time.Millisecond},
	}
	if !reflect.DeepEqual(*observed, want) {
		t.Fatalf("observations = %+v\nwant %+v", *observed, want)
	}

	// The process-wide observer reaches a Store without its own, and a Store
	// with its own keeps it.
	var global []LockObservation
	restore := SetLockObserver(func(observation LockObservation) { global = append(global, observation) })
	defaulted := NewStore(PathFor(t.TempDir()))
	defaulted.SetClock(clock.read)
	if _, err := defaulted.Load(); err != nil {
		t.Fatalf("Load through the process-wide observer: %v", err)
	}
	if _, err := store.Load(); err != nil {
		t.Fatalf("Load through the per-Store observer: %v", err)
	}
	restore()
	if len(global) != 1 || global[0].Operation != LockOperationLoad {
		t.Fatalf("process-wide observations = %+v, want the one Load of the Store without its own", global)
	}
	if len(*observed) != len(want)+1 {
		t.Fatalf("per-Store observations = %d, want %d: the process-wide observer replaced the Store's own", len(*observed), len(want)+1)
	}
	if _, err := defaulted.Load(); err != nil {
		t.Fatalf("Load after restore: %v", err)
	}
	if len(global) != 1 {
		t.Fatalf("the restored (absent) process-wide observer still observed: %+v", global)
	}
}

// TestRegistryLockObservationReportsTimeoutAndFailures covers the three ways an
// acquisition can end other than a clean release, and pins that observing them
// changes nothing the caller sees.
func TestRegistryLockObservationReportsTimeoutAndFailures(t *testing.T) {
	t.Parallel()

	t.Run("timeout", func(t *testing.T) {
		t.Parallel()
		store := NewStore(PathFor(t.TempDir()))
		observed := recordLockObservations(t, store)
		holdRegistryFlock(t, store)
		// Readings: the wait start, the deadline, the expired remaining check,
		// the error's own report, and the give-up -- four minutes apart.
		store.SetClock(steppingClock(fixedNow, time.Minute))
		ran := false
		registry, err := store.Update(func(*coremetadata.Registry) error {
			ran = true
			return nil
		})
		if !errors.Is(err, ErrLockTimeout) || ran || !reflect.DeepEqual(registry, coremetadata.Registry{}) {
			t.Fatalf("Update = (%+v, %v) ran=%t, want the unchanged timeout", registry, err, ran)
		}
		want := []LockObservation{{Operation: LockOperationUpdate, Outcome: LockOutcomeTimeout, Wait: 4 * time.Minute}}
		if !reflect.DeepEqual(*observed, want) {
			t.Fatalf("observations = %+v, want %+v", *observed, want)
		}
	})

	t.Run("a failing callback", func(t *testing.T) {
		t.Parallel()
		refused := errors.New("callback refused")
		run := func(observe bool) (coremetadata.Registry, error, *[]LockObservation) {
			store := testStore(t)
			var observed *[]LockObservation
			if observe {
				observed = recordLockObservations(t, store)
			}
			registry, err := store.Update(func(*coremetadata.Registry) error { return refused })
			return registry, err, observed
		}
		silentRegistry, silentErr, _ := run(false)
		registry, err, observed := run(true)
		if !errors.Is(err, refused) || err != silentErr || !reflect.DeepEqual(registry, silentRegistry) {
			t.Fatalf("observed Update = (%+v, %v), want the unobserved (%+v, %v)", registry, err, silentRegistry, silentErr)
		}
		want := []LockObservation{{Operation: LockOperationUpdate, Outcome: LockOutcomeReleased, Failed: true}}
		if !reflect.DeepEqual(*observed, want) {
			t.Fatalf("observations = %+v, want %+v", *observed, want)
		}
	})

	t.Run("an acquisition error", func(t *testing.T) {
		t.Parallel()
		store := testStore(t)
		observed := recordLockObservations(t, store)
		// A directory where the persistent flock descriptor belongs cannot be
		// opened read-write: an acquisition failure that is not the deadline.
		if err := os.MkdirAll(store.flockPath, 0o700); err != nil {
			t.Fatalf("block the flock path: %v", err)
		}
		_, err := store.Update(func(*coremetadata.Registry) error { return nil })
		if err == nil || errors.Is(err, ErrLockTimeout) {
			t.Fatalf("Update = %v, want an acquisition error that is not a timeout", err)
		}
		want := []LockObservation{{Operation: LockOperationUpdate, Outcome: LockOutcomeAcquireFailed}}
		if !reflect.DeepEqual(*observed, want) {
			t.Fatalf("observations = %+v, want %+v", *observed, want)
		}
	})
}

// TestRegistryLockObserverCannotChangeTheMutation runs the same mutations with
// an observer that panics on every call and with none: the returned Registry,
// the error, and the bytes on disk are identical.
func TestRegistryLockObserverCannotChangeTheMutation(t *testing.T) {
	t.Parallel()

	refused := errors.New("callback refused")
	run := func(observe bool) (coremetadata.Registry, coremetadata.Registry, error, string) {
		store := testStore(t)
		if observe {
			store.SetLockObserver(func(LockObservation) { panic("observer failure") })
		}
		registerProject(t, store, "/src/projmux")
		committed, err := store.Update(func(*coremetadata.Registry) error { return nil })
		if err != nil {
			t.Fatalf("Update (observe=%t): %v", observe, err)
		}
		refusedRegistry, refusedErr := store.Update(func(*coremetadata.Registry) error { return refused })
		if !reflect.DeepEqual(refusedRegistry, coremetadata.Registry{}) {
			t.Fatalf("refused Update returned %+v", refusedRegistry)
		}
		return committed, refusedRegistry, refusedErr, readFile(t, store.Path())
	}
	silentCommitted, _, silentErr, silentBytes := run(false)
	committed, _, err, bytes := run(true)
	if !reflect.DeepEqual(committed, silentCommitted) || err != silentErr || !errors.Is(err, refused) || bytes != silentBytes {
		t.Fatalf("a panicking observer changed the mutation:\nobserved=(%+v, %v)\nsilent  =(%+v, %v)\nbytes equal=%t",
			committed, err, silentCommitted, silentErr, bytes == silentBytes)
	}
}

// TestRegistryLockHolderWordsStopBeforeFlagsAndPrompts pins the holder name a
// lock timeout prints: the binary and the command words after it, never a
// flag, a uid, a path, or a prompt, and at most four words.
func TestRegistryLockHolderWordsStopBeforeFlagsAndPrompts(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		cmdline string
		want    string
		ok      bool
	}{
		{"projmux\x00create\x00agent\x00--\x00secret prompt\x00", "projmux create agent", true},
		{"/home/u/go/bin/projmux\x00internal\x00activation-exec\x00--pane\x00x", "projmux internal activation-exec", true},
		{"projmux\x00internal\x00tmux\x00converge\x00extra\x00", "projmux internal tmux converge", true},
		{"projmux\x00delete\x00agent\x00uid:agent-abc\x00--yes", "projmux delete agent", true},
		{"projmux\x00create\x00agent\x00fix the login bug\x00", "projmux create agent", true},
		{"projmux\x00open\x00/home/u/src\x00", "projmux open", true},
		{"projmux\x00tail\x0042\x00", "projmux tail", true},
		{"projmux\x00Create\x00", "projmux", true},
		{"projmux\x00cre\x07ate\x00", "projmux", true},
		{"/usr/bin/metadata.test\x00-test.v\x00", "metadata.test", true},
		{"", "", false},
		{"\x00", "", false},
		{"\x00\x00\x00", "", false},
		{"proj\x01mux\x00create\x00", "", false},
		{"proj mux\x00create\x00", "", false},
		{"/\x00create\x00", "", false},
	} {
		got, ok := holderCommandWords([]byte(test.cmdline))
		if got != test.want || ok != test.ok {
			t.Errorf("holderCommandWords(%q) = (%q, %t), want (%q, %t)", test.cmdline, got, ok, test.want, test.ok)
		}
	}
}

// TestRegistryLockTimeoutNamesTheHolderCommandWithoutItsPrompt runs a real
// holder process whose argv carries a prompt after `--`, names it in the legacy
// marker, and times out against it: the error names the holder's command and
// stops before the prompt.
func TestRegistryLockTimeoutNamesTheHolderCommandWithoutItsPrompt(t *testing.T) {
	t.Parallel()
	if _, err := os.Stat("/proc/self/cmdline"); err != nil {
		t.Skip("no /proc on this platform: the holder command is reported as unavailable")
	}

	cmd := exec.Command(os.Args[0])
	cmd.Args = []string{"projmux", "create", "agent", "--", "secret prompt text"}
	cmd.Env = append(os.Environ(), lockHolderChildEnv+"=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("holder stdin: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the holder process: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = stdin.Close()
		if err := cmd.Wait(); err != nil {
			t.Errorf("holder pid %d exit: %v", pid, err)
		}
	})

	store := NewStore(PathFor(t.TempDir()))
	holdRegistryFlock(t, store)
	writeLegacyMarker(t, store, fmt.Sprintf("pid=%d\n", pid))
	store.SetClock(steppingClock(fixedNow, time.Minute))

	_, err = store.Update(func(*coremetadata.Registry) error { return nil })
	if !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("Update = %v, want %v", err, ErrLockTimeout)
	}
	t.Logf("timeout error: %s", err)
	if want := fmt.Sprintf("holder: pid %d (projmux create agent), running", pid); !strings.Contains(err.Error(), want) {
		t.Fatalf("timeout error = %q, want it to contain %q", err, want)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("timeout error = %q prints the holder's prompt", err)
	}
	if err := os.Remove(store.lockPath); err != nil {
		t.Fatalf("remove marker fixture: %v", err)
	}
}
