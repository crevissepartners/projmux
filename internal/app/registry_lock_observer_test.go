package app

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// registryLockArgv is an invocation whose argv carries a prompt the journal
// must never hold.
var registryLockArgv = []string{"create", "agent", "--project", "alpha", "--", "secret prompt text"}

// steppedRegistryClock advances by step on every read, so every Registry lock
// acquisition in these tests waits and holds for whole seconds of simulated
// time and crosses the journal threshold without real waiting.
func steppedRegistryClock(step time.Duration) func() time.Time {
	var mu sync.Mutex
	now := time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(step)
		return now
	}
}

// TestRegistryLockObserverJournalsTheInvokingCommand drives a real Registry
// Store through the app observer onto a real journal: one acquisition over the
// threshold is one registry.lock.acquisition, attributed to the invocation's
// catalog command and the Store entry point, with no argv text.
func TestRegistryLockObserverJournalsTheInvokingCommand(t *testing.T) {
	t.Parallel()
	journalPath := filepath.Join(t.TempDir(), "operations.jsonl")
	journal := diagnostics.NewStore(journalPath)
	lifecycle := diagnostics.NewLifecycleRecorder(journal, "registry-lock-run", "1.0.0", "tmux")

	registry := intmetadata.NewStore(intmetadata.PathFor(t.TempDir()))
	registry.SetClock(steppedRegistryClock(time.Second))
	registry.SetLockObserver(newRegistryLockObserver(lifecycle.RegistryLock(diagnostics.Classify(registryLockArgv))))
	if _, err := registry.Update(func(*coremetadata.Registry) error { return nil }); err != nil {
		t.Fatalf("Update: %v", err)
	}

	events, err := journal.Read()
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("journal = %+v, want exactly one registry.lock.acquisition", events)
	}
	event := events[0]
	if event.Component != "registry" || event.Event != "registry.lock.acquisition" || event.Command != "create" ||
		event.Subcommand != "agent" || event.Operation != "update" || event.Result != "success" ||
		event.WaitMS == nil || *event.WaitMS < 1000 || event.LockHeldMS == nil || event.DurationMS != *event.WaitMS+*event.LockHeldMS {
		t.Fatalf("registry.lock.acquisition = %+v", event)
	}
	data, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatalf("read journal bytes: %v", err)
	}
	for _, private := range []string{"secret", "alpha", "--project"} {
		if strings.Contains(string(data), private) {
			t.Fatalf("journal carries argv text %q: %s", private, data)
		}
	}
	if lifecycle.RecordedOutcome() {
		t.Fatal("registry.lock.acquisition claimed the top-level command outcome")
	}

	// Below the threshold nothing is written: a frozen clock waits and holds
	// for zero.
	quiet := diagnostics.NewStore(filepath.Join(t.TempDir(), "operations.jsonl"))
	frozen := intmetadata.NewStore(intmetadata.PathFor(t.TempDir()))
	frozen.SetClock(func() time.Time { return time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC) })
	frozen.SetLockObserver(newRegistryLockObserver(
		diagnostics.NewLifecycleRecorder(quiet, "registry-lock-run", "1.0.0", "tmux").RegistryLock(diagnostics.Classify(registryLockArgv))))
	if _, err := frozen.Update(func(*coremetadata.Registry) error { return nil }); err != nil {
		t.Fatalf("frozen Update: %v", err)
	}
	if events, err := quiet.Read(); err != nil || len(events) != 0 {
		t.Fatalf("under-threshold journal = %+v err=%v, want nothing", events, err)
	}

	if newRegistryLockObserver(nil) != nil {
		t.Fatal("a nil recorder produced an observer")
	}
}

type failingRegistryLockJournal struct {
	mu    sync.Mutex
	calls int
}

func (w *failingRegistryLockJournal) Append(diagnostics.Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls++
	return errors.New("journal unavailable")
}

// TestRegistryLockJournalFailureLeavesTheMutationUnchanged runs the same
// Registry mutations with a journal that refuses every append and with no
// observer at all: the returned Registry, the error, and the registry file
// bytes are identical.
func TestRegistryLockJournalFailureLeavesTheMutationUnchanged(t *testing.T) {
	t.Parallel()
	refused := errors.New("callback refused")
	type outcome struct {
		committed coremetadata.Registry
		refused   coremetadata.Registry
		err       error
		bytes     string
	}
	run := func(writer diagnostics.EventWriter) outcome {
		store := intmetadata.NewStore(intmetadata.PathFor(t.TempDir()))
		store.SetClock(steppedRegistryClock(time.Second))
		if writer != nil {
			lifecycle := diagnostics.NewLifecycleRecorder(writer, "registry-lock-run", "1.0.0", "tmux")
			store.SetLockObserver(newRegistryLockObserver(lifecycle.RegistryLock(diagnostics.Classify(registryLockArgv))))
		}
		committed, err := store.Update(func(*coremetadata.Registry) error { return nil })
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		refusedRegistry, refusedErr := store.Update(func(*coremetadata.Registry) error { return refused })
		data, readErr := os.ReadFile(store.Path())
		if readErr != nil {
			t.Fatalf("read registry: %v", readErr)
		}
		return outcome{committed: committed, refused: refusedRegistry, err: refusedErr, bytes: string(data)}
	}
	writer := &failingRegistryLockJournal{}
	failing := run(writer)
	silent := run(nil)
	if writer.calls != 2 {
		t.Fatalf("journal appends = %d, want one per acquisition over the threshold", writer.calls)
	}
	if !reflect.DeepEqual(failing.committed, silent.committed) || !reflect.DeepEqual(failing.refused, silent.refused) ||
		failing.err != silent.err || !errors.Is(failing.err, refused) || failing.bytes != silent.bytes {
		t.Fatalf("a failing journal changed the mutation:\nfailing=%+v\nsilent =%+v", failing, silent)
	}
}

// TestRegistryLockObserverHonorsTheNoWriteBoundaries installs the process-wide
// observer the way main does and drives a Store without an observer of its own
// through a slow, locked Update. Doctor, the support report, and a retired
// no-write argv never append, while an ordinary command journals its slow
// acquisition. It is not parallel because the observer is process-wide.
func TestRegistryLockObserverHonorsTheNoWriteBoundaries(t *testing.T) {
	for _, test := range []struct {
		name    string
		args    []string
		records int
	}{
		{"doctor", []string{"doctor"}, 0},
		{"doctor with a bad flag", []string{"doctor", "--no-such-flag"}, 0},
		{"support report", []string{"diagnostics", "report", "--output", "/tmp/x"}, 0},
		{"retired no-write argv", []string{"status", "secret"}, 0},
		{"create agent", registryLockArgv, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			journal := diagnostics.NewStore(filepath.Join(t.TempDir(), "operations.jsonl"))
			lifecycle := diagnostics.NewLifecycleRecorder(journal, "registry-lock-run", "1.0.0", "tmux")
			restore := ObserveRegistryLock(lifecycle, test.args)
			store := intmetadata.NewStore(intmetadata.PathFor(t.TempDir()))
			store.SetClock(steppedRegistryClock(time.Second))
			_, err := store.Update(func(*coremetadata.Registry) error { return nil })
			restore()
			if err != nil {
				t.Fatalf("Update: %v", err)
			}
			events, err := journal.Read()
			if err != nil {
				t.Fatalf("read journal: %v", err)
			}
			if len(events) != test.records {
				t.Fatalf("journal = %+v, want %d records for %q", events, test.records, test.args)
			}
		})
	}
	if diagnostics.JournalForbidden(registryLockArgv) {
		t.Fatal("create agent is journal-forbidden")
	}
}
