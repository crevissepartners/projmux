package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// applyLockClock is a manual Registry clock: it reads the same instant until a
// locked callback advances it, so each acquisition waits zero and holds for
// exactly what its callback advanced.
type applyLockClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *applyLockClock) read() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *applyLockClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// lockingApplyTriggering stands in for the controller: the exhausted replay
// runs one lifecycle-reconcile transaction held for 2s, and the convergence a
// mirror-recovery transaction held for 3s and one no site names, held for 1s,
// all through a real Registry Store and the same labeling helpers the
// production sites use. err, when set, fails the convergence after its locks
// were released.
type lockingApplyTriggering struct {
	store *intmetadata.Store
	clock *applyLockClock
	err   error
}

func (r *lockingApplyTriggering) transact(ctx context.Context, kind diagnostics.ApplyLockKind, held time.Duration) error {
	lock := beginApplyLock(ctx, kind)
	defer lock.End()
	_, _, err := r.store.UpdateConvergent(func(*coremetadata.Registry) error {
		defer lock.Returned()
		lock.Phase(diagnostics.ApplyLockPhaseObserve)
		r.clock.advance(held)
		lock.Phase(diagnostics.ApplyLockPhaseCommit)
		return nil
	})
	return err
}

func (r *lockingApplyTriggering) replayExhaustedCleanExits(ctx context.Context, _ tmuxTransport) (controllerTriggerOutcome, error) {
	return controllerTriggerOutcome{reason: controllerTriggerConfigApply, converged: true},
		r.transact(ctx, diagnostics.ApplyLockKindLifecycleReconcile, 2*time.Second)
}

func (r *lockingApplyTriggering) run(ctx context.Context, trigger controllerTrigger) (controllerTriggerOutcome, error) {
	outcome := controllerTriggerOutcome{reason: trigger.reason, passes: 1, converged: true}
	if err := r.transact(ctx, diagnostics.ApplyLockKindMirrorRecovery, 3*time.Second); err != nil {
		return outcome, err
	}
	if _, err := r.store.Update(func(*coremetadata.Registry) error {
		r.clock.advance(time.Second)
		return nil
	}); err != nil {
		return outcome, err
	}
	return outcome, r.err
}

// sourcingBellApplyRunner is failingBellApplyRunner with a successful reload,
// so an apply reaches the controller steps.
type sourcingBellApplyRunner struct {
	*failingBellApplyRunner
}

func (r sourcingBellApplyRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if slices.Contains(args, "source-file") {
		return nil, nil
	}
	return r.failingBellApplyRunner.Run(ctx, name, args...)
}

// releaseCheckingJournal records every append. When the lifecycle.outcome
// arrives it checks, from inside the append, that nothing holds the Registry
// lock: a non-blocking exclusive flock on the kernel lock succeeds and the
// legacy marker is gone. err fails every append after recording it.
type releaseCheckingJournal struct {
	registryPath string
	err          error

	mu         sync.Mutex
	events     []diagnostics.Event
	checked    bool
	flockFree  bool
	markerGone bool
}

func (j *releaseCheckingJournal) Append(event diagnostics.Event) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.events = append(j.events, event)
	if event.Event == "lifecycle.outcome" {
		j.checked = true
		if file, err := os.OpenFile(j.registryPath+".flock", os.O_RDWR, 0); err == nil {
			if unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB) == nil {
				j.flockFree = true
				_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
			}
			_ = file.Close()
		}
		_, statErr := os.Stat(j.registryPath + ".lock")
		j.markerGone = errors.Is(statErr, os.ErrNotExist)
	}
	return j.err
}

type applyLockRun struct {
	err        error
	stdout     string
	stderr     string
	codex      string
	options    map[string]string
	hooks      []string
	registry   string
	journal    *releaseCheckingJournal
	recorder   *diagnostics.LifecycleRecorder
	registryAt string
}

// runApplyWithRegistryLocks runs one real config apply whose controller steps
// take the Registry lock. With a journal the invocation is scoped like
// App.execute scopes it and the Store reports to the production observer
// chain; without one there is no recorder and no observer at all.
func runApplyWithRegistryLocks(t *testing.T, journal *releaseCheckingJournal, triggerErr error) applyLockRun {
	t.Helper()
	home := t.TempDir()
	codexPath := filepath.Join(home, codexConfigRelativePath)
	writeCodexTestFile(t, codexPath, strings.ReplaceAll(codexHooksBlock(true), codexHookCommand, legacyCodexHookCommand))
	bell := &failingBellApplyRunner{
		socket:  "apply-lock-breakdown",
		options: map[string]string{"allow-passthrough": "off", "monitor-bell": "off", "bell-action": "none"},
		hooks:   []string{"run-shell -b 'echo unmanaged'", legacyTmuxBellHookCommand},
	}
	cmd := managedIngestApplyFixture(home, sourcingBellApplyRunner{bell})
	clock := &applyLockClock{now: time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)}
	registry := intmetadata.NewStore(intmetadata.PathFor(t.TempDir()))
	registry.SetClock(clock.read)
	cmd.triggerRunner = &lockingApplyTriggering{store: registry, clock: clock, err: triggerErr}

	run := applyLockRun{journal: journal, registryAt: registry.Path()}
	finish := func(error) {}
	if journal != nil {
		journal.registryPath = registry.Path()
		run.recorder = diagnostics.NewLifecycleRecorder(journal, "apply-lock-run", "1.0.0", "tmux")
		cmd.diagnostics = run.recorder
		argv := []string{"config", "apply"}
		registry.SetLockObserver(withApplyLockTally(run.recorder,
			newRegistryLockObserver(run.recorder.RegistryLock(diagnostics.Classify(argv)))))
		finish = run.recorder.BeginCommand()
	}
	var stdout, stderr bytes.Buffer
	run.err = cmd.runApply("config apply", []string{"--config", filepath.Join(home, "tmux.conf"), "--socket", bell.socket}, &stdout, &stderr)
	finish(run.err)
	run.stdout = strings.ReplaceAll(stdout.String(), home, "<home>")
	run.stderr = strings.ReplaceAll(stderr.String(), home, "<home>")
	run.codex = readCodexTestFile(t, codexPath)
	run.options, run.hooks = bell.options, bell.hooks
	if data, err := os.ReadFile(registry.Path()); err == nil {
		run.registry = string(data)
	}
	return run
}

// TestApplyOutcomeAppendedAfterRegistryLockRelease (acceptance 3, and 1 and 2
// end to end) runs a config apply whose controller steps take the Registry
// lock three times. The tmux.apply outcome is appended with the lock free --
// checked from inside the journal append -- and carries every step the apply
// entered within duration_ms, the longest hold attributed to its kind and
// step with its phases within the hold, and totals over all three
// acquisitions, the unlabeled one included. The registry.lock.acquisition
// records the same acquisitions produce keep their exact shape.
func TestApplyOutcomeAppendedAfterRegistryLockRelease(t *testing.T) {
	journal := &releaseCheckingJournal{}
	run := runApplyWithRegistryLocks(t, journal, nil)
	if run.err != nil {
		t.Fatalf("apply: %v\nstderr: %s", run.err, run.stderr)
	}
	if !journal.checked || !journal.flockFree || !journal.markerGone {
		t.Fatalf("outcome append: checked=%t flock free=%t marker gone=%t, want the Registry lock released",
			journal.checked, journal.flockFree, journal.markerGone)
	}

	var outcome *diagnostics.Event
	var acquisitions []diagnostics.Event
	for i, event := range journal.events {
		switch event.Event {
		case "lifecycle.outcome":
			outcome = &journal.events[i]
		case "registry.lock.acquisition":
			acquisitions = append(acquisitions, event)
		}
	}
	if outcome == nil || outcome.Operation != string(diagnostics.OperationTmuxApply) || outcome.Result != "success" {
		t.Fatalf("journal = %+v, want a tmux.apply success outcome", journal.events)
	}
	if journal.events[len(journal.events)-1].Event != "lifecycle.outcome" {
		t.Fatalf("the outcome is not the last record: %+v", journal.events)
	}

	steps := map[string]*int64{
		"keymap-migration": outcome.StepKeymapMigrationMS, "hook-file-migration": outcome.StepHookFileMigrationMS,
		"retired-file-reclaim": outcome.StepRetiredFileReclaimMS, "route-bind": outcome.StepRouteBindMS,
		"bell-hook-migration": outcome.StepBellHookMigrationMS, "config-write": outcome.StepConfigWriteMS,
		"key-sequence-retire": outcome.StepKeySequenceRetireMS, "source-file": outcome.StepSourceFileMS,
		"route-marker": outcome.StepRouteMarkerMS, "exhausted-replay": outcome.StepExhaustedReplayMS,
		"converge": outcome.StepConvergeMS,
	}
	var sum int64
	for name, value := range steps {
		if value == nil {
			t.Fatalf("step %s absent from %+v", name, *outcome)
		}
		sum += *value
	}
	if sum > outcome.DurationMS {
		t.Fatalf("step sum %d > duration_ms %d", sum, outcome.DurationMS)
	}

	if outcome.LongestLockKind != string(diagnostics.ApplyLockKindMirrorRecovery) || outcome.LongestLockStep != "converge" ||
		*outcome.LongestLockWaitMS != 0 || *outcome.LongestLockHeldMS != 3000 {
		t.Fatalf("longest lock = %s/%s wait=%d held=%d, want mirror-recovery/converge 0/3000",
			outcome.LongestLockKind, outcome.LongestLockStep, *outcome.LongestLockWaitMS, *outcome.LongestLockHeldMS)
	}
	if outcome.LongestLockObserveMS == nil || outcome.LongestLockCommitMS == nil || outcome.LongestLockStoreWriteMS == nil ||
		outcome.LongestLockPlanMS != nil {
		t.Fatalf("longest lock phases = observe %v plan %v commit %v store-write %v, want the three marked",
			outcome.LongestLockObserveMS, outcome.LongestLockPlanMS, outcome.LongestLockCommitMS, outcome.LongestLockStoreWriteMS)
	}
	if phases := *outcome.LongestLockObserveMS + *outcome.LongestLockCommitMS + *outcome.LongestLockStoreWriteMS; phases > *outcome.LongestLockHeldMS {
		t.Fatalf("phase sum %d > held %d", phases, *outcome.LongestLockHeldMS)
	}
	if *outcome.LockAcquisitionCount != 3 || *outcome.LockWaitTotalMS != 0 || *outcome.LockHeldTotalMS != 6000 {
		t.Fatalf("totals = %d/%d/%d, want 3 acquisitions, 0ms wait, 6000ms held",
			*outcome.LockAcquisitionCount, *outcome.LockWaitTotalMS, *outcome.LockHeldTotalMS)
	}

	// `diagnostics log` renders the breakdown.
	line := formatOperationalEvent(*outcome)
	for _, field := range []string{"step_converge_ms=", "lock_acquisition_count=3", "lock_held_total_ms=6000",
		"longest_lock_kind=mirror-recovery", "longest_lock_step=converge", "longest_lock_held_ms=3000"} {
		if !strings.Contains(line, field) {
			t.Fatalf("log line lacks %q: %s", field, line)
		}
	}

	// registry.lock.acquisition is unchanged: one record per acquisition over
	// the threshold, with exactly the fields and values it always had.
	want := []diagnostics.Event{
		registryLockGolden("update-convergent", 2000),
		registryLockGolden("update-convergent", 3000),
		registryLockGolden("update", 1000),
	}
	for i := range acquisitions {
		acquisitions[i].At = ""
	}
	if !reflect.DeepEqual(acquisitions, want) {
		t.Fatalf("registry.lock.acquisition = %+v\nwant %+v", acquisitions, want)
	}
}

func registryLockGolden(operation string, heldMS int64) diagnostics.Event {
	wait, held := int64(0), heldMS
	return diagnostics.Event{
		Level: "info", Component: "registry", Event: "registry.lock.acquisition", Result: "success",
		DurationMS: heldMS, RunID: "apply-lock-run", Version: "1.0.0", MuxBackend: "tmux",
		Command: "config", Subcommand: "apply", Operation: operation, WaitMS: &wait, LockHeldMS: &held,
	}
}

// TestApplyJournalFailureLeavesApplyUnchanged (acceptance 3) runs the same
// apply -- one that succeeds, and one whose convergence fails after its locks
// were released and rolls the managed hooks back -- with a journal refusing
// every append and with no journal at all. stdout, stderr, the error, the
// rollback, and the Registry bytes are identical.
func TestApplyJournalFailureLeavesApplyUnchanged(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{"success", nil},
		{"convergence failure rolls back", errors.New("injected convergence failure")},
	} {
		t.Run(test.name, func(t *testing.T) {
			failing := runApplyWithRegistryLocks(t, &releaseCheckingJournal{err: errors.New("journal unavailable")}, test.err)
			silent := runApplyWithRegistryLocks(t, nil, test.err)
			if len(failing.journal.events) == 0 || !failing.recorder.RecordedOutcome() {
				t.Fatal("the failing journal was never offered the apply's records")
			}
			if (failing.err == nil) != (silent.err == nil) || (failing.err != nil && failing.err.Error() != silent.err.Error()) {
				t.Fatalf("error = %v, want %v", failing.err, silent.err)
			}
			if failing.stdout != silent.stdout || failing.stderr != silent.stderr {
				t.Fatalf("output changed:\nstdout %q\nwant   %q\nstderr %q\nwant   %q", failing.stdout, silent.stdout, failing.stderr, silent.stderr)
			}
			if failing.codex != silent.codex || !reflect.DeepEqual(failing.options, silent.options) ||
				!reflect.DeepEqual(failing.hooks, silent.hooks) || failing.registry != silent.registry {
				t.Fatalf("rollback or Registry changed:\nfailing=%+v\nsilent =%+v", failing, silent)
			}
			if test.err != nil {
				if silent.err == nil || !errors.Is(silent.err, test.err) {
					t.Fatalf("apply error = %v, want the convergence failure", silent.err)
				}
				wantHooks := []string{"run-shell -b 'echo unmanaged'", legacyTmuxBellHookCommand}
				if !reflect.DeepEqual(silent.hooks, wantHooks) {
					t.Fatalf("hooks after rollback = %#v, want %#v", silent.hooks, wantHooks)
				}
			}
		})
	}
}

// TestApplyLockBreakdownLabelsTheControlTargetsTransaction drives the real
// control-target convergence inside an apply scope, with a Store fake that
// reports its acquisition through the recorder seam after the callback ran,
// as the Store does after its release: the transaction is control-targets, in
// the converge step, with its observe, plan, commit, and store-write phases
// within the hold. Outside an apply the same convergence measures nothing.
func TestApplyLockBreakdownLabelsTheControlTargetsTransaction(t *testing.T) {
	converger, store, _, _ := controlSessionFixture(t)
	writer := &appLifecycleWriter{}
	recorder := diagnostics.NewLifecycleRecorder(writer, "apply-run", "1.0.0", "tmux")
	finish := recorder.BeginCommand()
	recorder.Mark(diagnostics.OperationTmuxApply)
	apply := recorder.Apply()
	apply.Step(diagnostics.ApplyStepConverge)

	resources := store.store()
	update := resources.updateConvergent
	resources.updateConvergent = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error) {
		registry, changed, err := update(fn)
		recorder.ObserveApplyLock(diagnostics.ApplyLockObservation{Wait: time.Millisecond, Held: time.Hour, Released: true})
		return registry, changed, err
	}
	runner := &controllerTriggerRunner{runner: converger.runner, store: resources}
	target, err := tmuxSocketNameTarget(controlFixtureSocket)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.convergeControlTargets(withApplyRecorder(context.Background(), apply), target, true); err != nil {
		t.Fatalf("config-apply control convergence: %v", err)
	}
	apply.Close()
	finish(nil)

	outcome := writer.events[len(writer.events)-1]
	if outcome.LongestLockKind != string(diagnostics.ApplyLockKindControlTargets) || outcome.LongestLockStep != "converge" ||
		*outcome.LongestLockHeldMS != time.Hour.Milliseconds() || *outcome.LockAcquisitionCount != 1 {
		t.Fatalf("outcome = %+v, want one control-targets acquisition in converge", outcome)
	}
	if outcome.LongestLockObserveMS == nil || outcome.LongestLockPlanMS == nil || outcome.LongestLockCommitMS == nil ||
		outcome.LongestLockStoreWriteMS == nil {
		t.Fatalf("phases = %v %v %v %v, want all four", outcome.LongestLockObserveMS, outcome.LongestLockPlanMS,
			outcome.LongestLockCommitMS, outcome.LongestLockStoreWriteMS)
	}

	// No apply recorder on the context: nothing is named, nothing is counted.
	if transaction := beginApplyLock(context.Background(), diagnostics.ApplyLockKindControlTargets); transaction != nil {
		t.Fatal("a context without an apply opened a transaction")
	}
	markApplyLockPhase(context.Background(), diagnostics.ApplyLockPhaseCommit)
	if ctx := withApplyLockKind(context.Background(), diagnostics.ApplyLockKindPreexistingDeadAgent); ctx != context.Background() {
		t.Fatal("withApplyLockKind wrapped a context without an apply")
	}
}

// TestApplyLockKindOfTheCallerNamesASharedTransaction pins the one override:
// the pre-existing dead Agent recovery runs the shared lifecycle reconcile,
// and its acquisition is named for the recovery, not the reconcile.
func TestApplyLockKindOfTheCallerNamesASharedTransaction(t *testing.T) {
	t.Parallel()
	writer := &appLifecycleWriter{}
	recorder := diagnostics.NewLifecycleRecorder(writer, "apply-run", "1.0.0", "tmux")
	finish := recorder.BeginCommand()
	recorder.Mark(diagnostics.OperationTmuxApply)
	apply := recorder.Apply()
	apply.Step(diagnostics.ApplyStepConverge)
	ctx := withApplyLockKind(withApplyRecorder(context.Background(), apply), diagnostics.ApplyLockKindPreexistingDeadAgent)
	transaction := beginApplyLock(ctx, diagnostics.ApplyLockKindLifecycleReconcile)
	recorder.ObserveApplyLock(diagnostics.ApplyLockObservation{Held: time.Second, Released: true})
	transaction.End()
	apply.Close()
	finish(nil)
	if got := writer.events[len(writer.events)-1].LongestLockKind; got != string(diagnostics.ApplyLockKindPreexistingDeadAgent) {
		t.Fatalf("longest_lock_kind = %q, want preexisting-dead-agent", got)
	}
}
