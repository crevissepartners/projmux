package app

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// createOutcomeStep is the stepped outcome clock's tick. A transaction that
// enters the Registry mutation reads the clock four times -- start, lock entry,
// lock return, end -- so it records duration 3 steps and lock hold 1 step.
const createOutcomeStep = 10 * time.Millisecond

func steppedCreateOutcomeClock() func() time.Time {
	var mu sync.Mutex
	now := time.Unix(1_700_000_000, 0)
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(createOutcomeStep)
		return now
	}
}

// recordCreateOutcomes wires create onto a real journal store with the stepped
// clock and returns that store, so every record also passes the closed schema.
func recordCreateOutcomes(t *testing.T, create *createCommand) *diagnostics.Store {
	t.Helper()
	store := diagnostics.NewStore(filepath.Join(t.TempDir(), "operations.jsonl"))
	create.outcomes = diagnostics.NewLifecycleRecorder(store, "create-outcome-run", "1.0.0", "tmux").Create()
	create.outcomeClock = steppedCreateOutcomeClock()
	return store
}

func readCreateOutcomes(t *testing.T, store *diagnostics.Store) []diagnostics.Event {
	t.Helper()
	events, err := store.Read()
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	return slices.DeleteFunc(events, func(event diagnostics.Event) bool { return event.Event != "create.outcome" })
}

// assertOneCreateOutcome requires exactly one create.outcome of kind and result
// and the timing contract 0 <= lock_held_ms <= duration_ms.
func assertOneCreateOutcome(t *testing.T, store *diagnostics.Store, kind diagnostics.CreateKind, result string) diagnostics.Event {
	t.Helper()
	events := readCreateOutcomes(t, store)
	if len(events) != 1 {
		t.Fatalf("create.outcome records = %+v, want exactly one", events)
	}
	event := events[0]
	if event.Component != "create" || event.Operation != string(kind) || event.Result != result {
		t.Fatalf("create.outcome = %+v, want kind %s result %s", event, kind, result)
	}
	if event.LockHeldMS != nil && (*event.LockHeldMS < 0 || *event.LockHeldMS > event.DurationMS) {
		t.Fatalf("create.outcome timings break 0 <= lock_held_ms <= duration_ms: %+v", event)
	}
	return event
}

func assertCreateOutcomeLockedTimings(t *testing.T, event diagnostics.Event) {
	t.Helper()
	if event.LockHeldMS == nil {
		t.Fatalf("create.outcome has no lock_held_ms though the mutation ran: %+v", event)
	}
	if want := 3 * createOutcomeStep.Milliseconds(); event.DurationMS != want {
		t.Fatalf("duration_ms = %d, want %d (start to return on the stepped clock)", event.DurationMS, want)
	}
	if want := createOutcomeStep.Milliseconds(); *event.LockHeldMS != want {
		t.Fatalf("lock_held_ms = %d, want %d (closure entry to update return)", *event.LockHeldMS, want)
	}
}

// TestCreateOutcomeCLIRoutesRecordOneLinePerTransaction is acceptance item 1.
func TestCreateOutcomeCLIRoutesRecordOneLinePerTransaction(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		kind diagnostics.CreateKind
		run  func(t *testing.T) (*diagnostics.Store, error)
	}{
		{diagnostics.CreateKindWindow, func(t *testing.T) (*diagnostics.Store, error) {
			create, _ := newTestResourceCreateCommand(t, newFakeResourceStore(t), newFakeTmux())
			journal := recordCreateOutcomes(t, create)
			_, _, err := runRoute(t, create, "window", "--project", "beta")
			return journal, err
		}},
		{diagnostics.CreateKindPane, func(t *testing.T) (*diagnostics.Store, error) {
			create, _ := newTestResourceCreateCommand(t, newFakeResourceStore(t), newFakeTmux())
			journal := recordCreateOutcomes(t, create)
			_, _, err := runRoute(t, create, "pane", "--project", "beta", "--window", "main")
			return journal, err
		}},
		{diagnostics.CreateKindAgent, func(t *testing.T) (*diagnostics.Store, error) {
			create, _ := newTestAgentCreateCommand(t, newFakeResourceStore(t), newFakeTmux())
			journal := recordCreateOutcomes(t, create)
			_, _, err := runRoute(t, create, "agent", "--provider", "codex", "--interactive-only", "--project", "alpha", "--window", "main")
			return journal, err
		}},
		{diagnostics.CreateKindResume, func(t *testing.T) (*diagnostics.Store, error) {
			store := newFakeResourceStore(t)
			setFixtureSessionRef(t, store, "agt-beta-codex", resumeFixtureRef(resourceFixtureClock))
			agent, launcher, _, _ := newTestAgentResumeCommand(t, store, newFakeTmux())
			enablePinnedNativeResumeFixture(t, agent, store, "agt-beta-codex", launcher)
			journal := recordCreateOutcomes(t, agent.rebind.create)
			_, _, err := runRoute(t, agent, "resume", "codex", "--project", "beta")
			return journal, err
		}},
	} {
		t.Run(string(test.kind), func(t *testing.T) {
			t.Parallel()
			journal, err := test.run(t)
			if err != nil {
				t.Fatalf("create %s: %v", test.kind, err)
			}
			assertCreateOutcomeLockedTimings(t, assertOneCreateOutcome(t, journal, test.kind, "success"))
		})
	}
}

// TestCreateOutcomeRecordsEveryTransactionOfOneProcess pins "no once": a
// process that runs several transactions records each of them.
func TestCreateOutcomeRecordsEveryTransactionOfOneProcess(t *testing.T) {
	t.Parallel()
	create, _ := newTestResourceCreateCommand(t, newFakeResourceStore(t), newFakeTmux())
	journal := recordCreateOutcomes(t, create)
	for range 2 {
		if _, _, err := runRoute(t, create, "window", "--project", "beta"); err != nil {
			t.Fatalf("create window: %v", err)
		}
	}
	if events := readCreateOutcomes(t, journal); len(events) != 2 {
		t.Fatalf("create.outcome records = %+v, want one per transaction", events)
	}
}

// TestCreateOutcomeUIIntentRoutesRecordOneLine is acceptance item 2.
func TestCreateOutcomeUIIntentRoutesRecordOneLine(t *testing.T) {
	for _, test := range []struct {
		name string
		kind diagnostics.CreateKind
		run  func(fx canonicalRootFixture) error
	}{
		{"createWindowFromIntent", diagnostics.CreateKindWindow, func(fx canonicalRootFixture) error {
			_, err := fx.create.createWindowFromIntent(windowCreateIntent{anchorPaneID: fx.originID}, ioDiscard{}, ioDiscard{})
			return err
		}},
		{"createCanonicalIntentPane", diagnostics.CreateKindPane, func(fx canonicalRootFixture) error {
			_, err := fx.create.createFromIntent(agentPaneIntent{
				producer: canonicalProducerPaneMenu, placement: "right", anchorPaneID: fx.originID,
			}, ioDiscard{}, ioDiscard{})
			return err
		}},
		{"createCanonicalIntentAgent", diagnostics.CreateKindAgent, func(fx canonicalRootFixture) error {
			_, err := fx.create.createFromIntent(agentPaneIntent{
				producer: canonicalProducerProviderPicker, provider: aiModeClaude, placement: "right", anchorPaneID: fx.originID,
			}, ioDiscard{}, ioDiscard{})
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fx := canonicalFixture(t, false)
			journal := recordCreateOutcomes(t, fx.create)
			if err := test.run(fx); err != nil {
				t.Fatalf("%s: %v", test.name, err)
			}
			assertCreateOutcomeLockedTimings(t, assertOneCreateOutcome(t, journal, test.kind, "success"))
		})
	}
}

// TestCreateOutcomeWindowRenameIntentRecordsNothing pins the owner ruling: the
// generated Window rename runs through the same transaction and is not a
// create, so it writes zero create.outcome records.
func TestCreateOutcomeWindowRenameIntentRecordsNothing(t *testing.T) {
	fx := canonicalFixture(t, false)
	session, window, _ := fx.tmux.pane(fx.originID)
	session.windows = slices.DeleteFunc(session.windows, func(candidate *fakeTmuxWindow) bool {
		return candidate != window && candidate.opts[tmuxopts.WindowUID] == ""
	})
	journal := recordCreateOutcomes(t, fx.create)
	runGeneratedRename(t, fx, coremetadata.KindWindow, fx.originID, "renamed-window")
	if window.name != "renamed-window" {
		t.Fatalf("tmux window_name = %q: the rename transaction did not run", window.name)
	}
	if events := readCreateOutcomes(t, journal); len(events) != 0 {
		t.Fatalf("Window rename wrote create.outcome records %+v, want none", events)
	}
}

// TestCreateOutcomeFailureRecordsOneErrorLine is acceptance item 3: a failure
// inside the mutation (rolled back) and one before the lock each record one
// error line, and only the one that entered the mutation has a lock hold.
func TestCreateOutcomeFailureRecordsOneErrorLine(t *testing.T) {
	t.Parallel()
	t.Run("rollback", func(t *testing.T) {
		t.Parallel()
		tmux := newFakeTmux()
		create, sessions := newTestResourceCreateCommand(t, newFakeResourceStore(t), tmux)
		sessions.preCreateErr = errors.New(`pre-create hook for tmux session "beta": exited with status 7`)
		journal := recordCreateOutcomes(t, create)
		if _, _, err := runRoute(t, create, "window", "--project", "beta"); err == nil {
			t.Fatal("a pre-create refusal still created the Window")
		}
		event := assertOneCreateOutcome(t, journal, diagnostics.CreateKindWindow, "error")
		if event.Level != "error" || event.Kind != "runtime" {
			t.Fatalf("error outcome = %+v, want level error kind runtime", event)
		}
		assertCreateOutcomeLockedTimings(t, event)
	})
	t.Run("before the lock", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		create, _ := newTestResourceCreateCommand(t, store, newFakeTmux())
		create.newOperationID = func() (string, error) { return "", errors.New("operation id unavailable") }
		journal := recordCreateOutcomes(t, create)
		if _, _, err := runRoute(t, create, "window", "--project", "beta"); err == nil {
			t.Fatal("create succeeded without an operation id")
		}
		event := assertOneCreateOutcome(t, journal, diagnostics.CreateKindWindow, "error")
		if event.LockHeldMS != nil || store.transactions != 0 {
			t.Fatalf("outcome=%+v transactions=%d, want no lock hold and no transaction", event, store.transactions)
		}
	})
}

type failingCreateOutcomeWriter struct{ calls int }

func (w *failingCreateOutcomeWriter) Append(diagnostics.Event) error {
	w.calls++
	return errors.New("journal unavailable")
}

// TestCreateOutcomeJournalFailureNeverChangesTheCreate is acceptance item 3's
// second half: the same create with a failing journal and with no recorder at
// all returns the same error and writes the same stdout and stderr.
func TestCreateOutcomeJournalFailureNeverChangesTheCreate(t *testing.T) {
	t.Parallel()
	type outcome struct {
		stdout, stderr, err string
	}
	run := func(t *testing.T, failPreCreate bool, writer diagnostics.EventWriter) outcome {
		create, sessions := newTestResourceCreateCommand(t, newFakeResourceStore(t), newFakeTmux())
		if failPreCreate {
			sessions.preCreateErr = errors.New(`pre-create hook for tmux session "beta": exited with status 7`)
		}
		if writer != nil {
			create.outcomes = diagnostics.NewLifecycleRecorder(writer, "create-outcome-run", "1.0.0", "tmux").Create()
		}
		stdout, stderr, err := runRoute(t, create, "window", "--project", "beta")
		got := outcome{stdout: stdout, stderr: stderr}
		if err != nil {
			got.err = err.Error()
		}
		return got
	}
	for _, failPreCreate := range []bool{false, true} {
		writer := &failingCreateOutcomeWriter{}
		failing := run(t, failPreCreate, writer)
		silent := run(t, failPreCreate, nil)
		if writer.calls != 1 {
			t.Fatalf("failing writer calls = %d, want 1", writer.calls)
		}
		if failing != silent {
			t.Fatalf("a failing journal changed the create (pre-create failure %t):\nfailing=%+v\nsilent =%+v", failPreCreate, failing, silent)
		}
		if (failing.err != "") != failPreCreate {
			t.Fatalf("create error = %q, want failure %t", failing.err, failPreCreate)
		}
	}
}

// orderedCreateOutcomeWriter asserts the Registry lock is released whenever a
// create.outcome is appended.
type orderedCreateOutcomeWriter struct {
	t      *testing.T
	locked *bool
	calls  int
}

func (w *orderedCreateOutcomeWriter) Append(diagnostics.Event) error {
	w.calls++
	if *w.locked {
		w.t.Error("create.outcome was appended while the Registry lock was held")
	}
	return nil
}

// TestCreateOutcomeIsAppendedAfterTheRegistryLockIsReleased is acceptance
// item 4, for a committed create and for a rolled-back one.
func TestCreateOutcomeIsAppendedAfterTheRegistryLockIsReleased(t *testing.T) {
	t.Parallel()
	for _, failPreCreate := range []bool{false, true} {
		create, sessions := newTestResourceCreateCommand(t, newFakeResourceStore(t), newFakeTmux())
		if failPreCreate {
			sessions.preCreateErr = errors.New(`pre-create hook for tmux session "beta": exited with status 7`)
		}
		locked, entered := false, false
		update := create.store.update
		create.store.update = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
			locked = true
			defer func() { locked = false }()
			return update(func(working *coremetadata.Registry) error {
				entered = true
				return fn(working)
			})
		}
		writer := &orderedCreateOutcomeWriter{t: t, locked: &locked}
		create.outcomes = diagnostics.NewLifecycleRecorder(writer, "create-outcome-run", "1.0.0", "tmux").Create()
		_, _, err := runRoute(t, create, "window", "--project", "beta")
		if (err != nil) != failPreCreate || !entered {
			t.Fatalf("create err=%v entered=%t, want failure %t inside the mutation", err, entered, failPreCreate)
		}
		if writer.calls != 1 {
			t.Fatalf("create.outcome appends = %d, want exactly 1", writer.calls)
		}
	}
}

// TestCreateOutcomeNilRecorderWritesNothing is acceptance item 5: a nil
// recorder -- a fixture, the Project startup helper, the web API's app --
// neither writes nor panics, on success and on failure.
func TestCreateOutcomeNilRecorderWritesNothing(t *testing.T) {
	t.Parallel()
	var owner *diagnostics.LifecycleRecorder
	for _, failPreCreate := range []bool{false, true} {
		create, sessions := newTestResourceCreateCommand(t, newFakeResourceStore(t), newFakeTmux())
		if failPreCreate {
			sessions.preCreateErr = errors.New(`pre-create hook for tmux session "beta": exited with status 7`)
		}
		create.outcomes = owner.Create()
		if create.outcomes != nil {
			t.Fatal("a nil lifecycle recorder produced a create recorder")
		}
		if _, _, err := runRoute(t, create, "window", "--project", "beta"); (err != nil) != failPreCreate {
			t.Fatalf("create err = %v, want failure %t", err, failPreCreate)
		}
	}
	var nilCreate *createCommand
	if err := nilCreate.transact(diagnostics.CreateKindWindow, nil); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("nil create transact = %v, want the not-configured refusal", err)
	}
}

// TestApplicationGraphWiresTheCreateOutcomeRecorder pins the wiring: the app
// graph's create command records on the invocation recorder, and the web
// layer's nil-recorder app records nothing.
func TestApplicationGraphWiresTheCreateOutcomeRecorder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".state"))
	if got := NewWithLifecycleDiagnostics(nil).create.outcomes; got != nil {
		t.Fatalf("nil-recorder app create recorder = %v, want nil", got)
	}
	recorder := diagnostics.NewLifecycleRecorder(&failingCreateOutcomeWriter{}, "create-outcome-run", "1.0.0", "tmux")
	graph := NewWithLifecycleDiagnostics(recorder)
	if graph.create.outcomes == nil {
		t.Fatal("the app graph's create command has no create.outcome recorder")
	}
	if graph.agent.rebind == nil || graph.agent.rebind.create != graph.create {
		t.Fatal("agent resume does not transact on the app graph's create command")
	}
}

func TestFormatOperationalCreateEventShowsKindAndLockHeld(t *testing.T) {
	t.Parallel()
	held := int64(12)
	event := diagnostics.Event{
		At: "2026-09-19T01:02:03Z", Level: "info", Component: "create", Event: "create.outcome", Result: "success",
		DurationMS: 30, RunID: "safe-create-run", Version: "1.0.0", MuxBackend: "tmux",
		Operation: string(diagnostics.CreateKindPane), LockHeldMS: &held,
	}
	got := formatOperationalEvent(event)
	for _, want := range []string{"create create.outcome success", "operation=pane", "duration_ms=30 lock_held_ms=12 run_id=safe-create-run"} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatted create event = %q, want %q", got, want)
		}
	}
	event.LockHeldMS = nil
	if got := formatOperationalEvent(event); strings.Contains(got, "lock_held_ms") {
		t.Fatalf("formatted pre-lock create event = %q, want no lock_held_ms", got)
	}
}
