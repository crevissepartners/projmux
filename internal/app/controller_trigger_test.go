package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// controller_trigger_test.go covers the one lifecycle/mutation entrypoint: what a
// producer may say, how a burst of producers becomes one convergence, and what
// the worker is allowed to claim afterwards.
//
// The properties here are the ones a hook burst actually exercises. Both
// pane-exit hooks fire on every pane exit in every session and the creation hooks
// fire on every window and split, so "N producers cost one worker" is not a
// nicety: N workers contending for one registry lock is how a burst turns into
// lock-attempt exhaustion instead of into a convergence.

// triggerFixture is a runner whose event log, lease, and convergence body are all
// local to one test.
type triggerFixture struct {
	runner *controllerTriggerRunner
	target explicitTmuxTarget
	// passes records the target of every convergence body invocation.
	passes []explicitTmuxTarget
	// triggers records the scope handed to each convergence body invocation.
	triggers []controllerTrigger
	// results is consumed one entry per pass; the last entry repeats.
	results []controllerPassResult
	// beforePass runs at the top of each pass, which is where a test injects a
	// producer that arrives while the worker is mid-flight.
	beforePass func(pass int)
	err        error
}

func newTriggerFixture(t *testing.T, results ...controllerPassResult) *triggerFixture {
	t.Helper()
	target, err := tmuxSocketPathTarget(filepath.Join(t.TempDir(), "managed.sock"))
	if err != nil {
		t.Fatal(err)
	}
	fixture := &triggerFixture{target: target, results: results}
	fixture.runner = &controllerTriggerRunner{
		runner: &routedTmuxRunner{},
		store:  (&fakeResourceStore{registry: coremetadata.Registry{}, now: resourceFixtureClock}).store(),
		events: controllerEventLog{dir: t.TempDir()},
		pass: func(_ context.Context, trigger controllerTrigger) (controllerPassResult, error) {
			index := len(fixture.passes)
			if fixture.beforePass != nil {
				fixture.beforePass(index + 1)
			}
			fixture.passes = append(fixture.passes, trigger.target)
			fixture.triggers = append(fixture.triggers, trigger)
			if fixture.err != nil {
				return controllerPassResult{}, fixture.err
			}
			if len(fixture.results) == 0 {
				return controllerPassResult{}, nil
			}
			if index >= len(fixture.results) {
				return fixture.results[len(fixture.results)-1], nil
			}
			return fixture.results[index], nil
		},
	}
	return fixture
}

func (f *triggerFixture) run(t *testing.T, reason controllerTriggerReason) controllerTriggerOutcome {
	t.Helper()
	outcome, err := f.runner.run(context.Background(), controllerTrigger{reason: reason, target: f.target})
	if err != nil {
		t.Fatalf("trigger %s: %v", reason, err)
	}
	return outcome
}

// TestOneProducerConvergesOnceAndProvesIt is the ordinary single-event case.
//
// A pass that wrote nothing and left no pending event is the reobservation: it is
// the only evidence available that a repeat of this trigger would do nothing, and
// producing it costs one extra pass rather than a second engine.
func TestOneProducerConvergesOnceAndProvesIt(t *testing.T) {
	t.Parallel()

	fixture := newTriggerFixture(t)
	outcome := fixture.run(t, controllerTriggerPaneKilled)

	if outcome.passes != 1 || outcome.changed != 0 || outcome.events != 1 || !outcome.converged {
		t.Fatalf("outcome = %s, want one no-op pass over one event, converged", outcome.describe())
	}
	if outcome.deferred != "" || outcome.unverified != "" {
		t.Fatalf("outcome = %s, want neither deferred nor unverified", outcome.describe())
	}
}

// TestAPassThatWroteIsRepeatedUntilOneWritesNothing pins the reobservation.
//
// A pass that changed the registry has not yet proved the change landed and
// converged: a second client racing the same repair, or a hook that rewrote an
// option back, is exactly the case a report claiming success must not hide. So
// the worker asks the machine again.
func TestAPassThatWroteIsRepeatedUntilOneWritesNothing(t *testing.T) {
	t.Parallel()

	fixture := newTriggerFixture(t,
		controllerPassResult{rebound: true},
		controllerPassResult{rebound: true},
		controllerPassResult{},
	)
	outcome := fixture.run(t, controllerTriggerConfigApply)

	if outcome.passes != 3 || outcome.changed != 2 || !outcome.converged {
		t.Fatalf("outcome = %s, want three passes, two of which wrote, then convergence", outcome.describe())
	}
}

// TestABurstOfProducersCostsOneWorker is the coalescing property.
//
// The second producer marks its event, fails to take the lease, and exits
// successfully. That is not a dropped wakeup: the holder has not acknowledged the
// event yet, so its next drain picks it up and it runs a further pass. Blocking or
// retrying in the loser would rebuild exactly the registry-lock contention the
// lease removes.
func TestABurstOfProducersCostsOneWorker(t *testing.T) {
	t.Parallel()

	fixture := newTriggerFixture(t)
	// Three more producers arrive during the first pass, from the two hooks that
	// fire on every pane exit in every session.
	fixture.beforePass = func(pass int) {
		if pass != 1 {
			return
		}
		for range 3 {
			second, err := fixture.runner.run(context.Background(), controllerTrigger{
				reason: controllerTriggerPaneKilled, target: fixture.target,
			})
			if err != nil {
				t.Fatalf("concurrent producer: %v", err)
			}
			if second.passes != 0 || second.deferred == "" {
				t.Fatalf("concurrent producer converged anyway: %s", second.describe())
			}
			if !strings.Contains(second.deferred, "another controller worker holds") {
				t.Fatalf("concurrent producer deferred for the wrong reason: %s", second.describe())
			}
		}
	}
	outcome := fixture.run(t, controllerTriggerRuntimeCreated)

	// Pass 1 drains the holder's own event; pass 2 drains the three that arrived
	// during it and finds nothing left to do.
	if outcome.passes != 2 || outcome.events != 4 || !outcome.converged {
		t.Fatalf("outcome = %s, want two passes over four events, converged", outcome.describe())
	}
	if len(fixture.passes) != 2 {
		t.Fatalf("convergence bodies run = %d, want 2 for four producers", len(fixture.passes))
	}
}

// TestPaneKilledCoalescedBehindExactPaneExitWidensEveryLaterPass prevents a
// narrow lease holder from acknowledging a broader kill event without ever
// observing the rest of the host. Once the pending event is visible, both its
// changing pass and the final no-op verification stay broad.
func TestPaneKilledCoalescedBehindExactPaneExitWidensEveryLaterPass(t *testing.T) {
	t.Parallel()

	fixture := newTriggerFixture(t,
		controllerPassResult{},
		controllerPassResult{rebound: true},
		controllerPassResult{},
	)
	fixture.beforePass = func(pass int) {
		if pass != 1 {
			return
		}
		coalesced, err := fixture.runner.run(context.Background(), controllerTrigger{
			reason: controllerTriggerPaneKilled, target: fixture.target,
		})
		if err != nil {
			t.Fatalf("coalesced pane-killed: %v", err)
		}
		if coalesced.passes != 0 || coalesced.deferred == "" {
			t.Fatalf("coalesced pane-killed = %s, want lease deferral", coalesced.describe())
		}
	}
	outcome, err := fixture.runner.run(context.Background(), controllerTrigger{
		reason: controllerTriggerPaneExited, target: fixture.target, hookPane: "%9",
	})
	if err != nil {
		t.Fatalf("pane-exited worker: %v", err)
	}
	if outcome.passes != 3 || outcome.events != 2 || !outcome.converged {
		t.Fatalf("outcome = %s, want exact pass, broad changing pass, broad verification", outcome.describe())
	}
	if len(fixture.triggers) != 3 || fixture.triggers[0].fullReobserve || fixture.triggers[0].hookPane != "%9" {
		t.Fatalf("first pass trigger = %+v, want exact %%9", fixture.triggers)
	}
	for pass, trigger := range fixture.triggers[1:] {
		if !trigger.fullReobserve || trigger.hookPane != "" || trigger.hookWindow != "" {
			t.Fatalf("later pass %d trigger = %+v, want sticky whole-host scope", pass+2, trigger)
		}
	}
}

// TestTheCoalescingLoopIsBounded keeps a producer that marks faster than a pass
// completes from pinning a worker forever.
//
// Stopping early is safe in a way that a lost event is not: convergence is derived
// from the machine rather than from the event log, so the next producer's pass
// reaches the same answer this worker would have reached on pass nine.
func TestTheCoalescingLoopIsBounded(t *testing.T) {
	t.Parallel()

	fixture := newTriggerFixture(t)
	fixture.runner.maxPasses = 3
	fixture.beforePass = func(int) {
		if err := fixture.runner.events.mark(fixture.target, controllerTriggerPaneKilled); err != nil {
			t.Fatalf("mark: %v", err)
		}
	}
	outcome := fixture.run(t, controllerTriggerPaneKilled)

	if outcome.passes != 3 {
		t.Fatalf("passes = %d, want the configured bound of 3", outcome.passes)
	}
	if outcome.converged {
		t.Fatalf("outcome = %s, want no convergence claim while an event is still pending", outcome.describe())
	}
}

// TestAnUnverifiableHostStopsWithoutClaimingConvergence pins the fail-closed
// half.
//
// An exact-host observation that could not be taken is not an answer at all. The
// worker records that the pass ran and that nobody can say whether it was enough,
// and it does not keep looping: a server that is not up will not become observable
// because we asked again.
func TestAnUnverifiableHostStopsWithoutClaimingConvergence(t *testing.T) {
	t.Parallel()

	fixture := newTriggerFixture(t, controllerPassResult{unobserved: "the exact-host observation could not be taken"})
	outcome := fixture.run(t, controllerTriggerPaneKilled)

	if outcome.passes != 1 || outcome.converged {
		t.Fatalf("outcome = %s, want one pass and no convergence claim", outcome.describe())
	}
	if outcome.unverified == "" {
		t.Fatalf("outcome = %s, want the unverified reason recorded", outcome.describe())
	}
}

// TestTheWorkerLeaseIsExclusiveAndSurvivesNoCrash covers the lease primitive.
//
// It is an advisory whole-file lock rather than a timestamped lockfile precisely
// so that a worker which is killed, panics, or is stopped mid-pass cannot leave a
// lease behind: there is nothing to break, so there is no rule for when breaking
// it is safe. A lock file with no holder is therefore acquirable immediately.
func TestTheWorkerLeaseIsExclusiveAndSurvivesNoCrash(t *testing.T) {
	t.Parallel()

	log := controllerEventLog{dir: t.TempDir()}
	target, err := tmuxSocketNameTarget("primary")
	if err != nil {
		t.Fatal(err)
	}
	release, acquired, err := log.acquire(target)
	if err != nil || !acquired {
		t.Fatalf("first acquire = %t, %v; want the lease", acquired, err)
	}
	if _, second, err := log.acquire(target); err != nil || second {
		t.Fatalf("second acquire = %t, %v; want refusal while the lease is held", second, err)
	}
	// A sibling server is a different key, so it is never blocked by this lease.
	sibling, err := tmuxSocketNameTarget("second")
	if err != nil {
		t.Fatal(err)
	}
	siblingRelease, siblingAcquired, err := log.acquire(sibling)
	if err != nil || !siblingAcquired {
		t.Fatalf("sibling acquire = %t, %v; want an independent lease", siblingAcquired, err)
	}
	siblingRelease()
	release()

	// The abandoned lock file is what a crashed worker leaves behind. It carries
	// no ownership of its own.
	if _, err := os.Stat(log.lockPath(target)); err != nil {
		t.Fatalf("lease file missing after release: %v", err)
	}
	afterCrash, acquired, err := log.acquire(target)
	if err != nil || !acquired {
		t.Fatalf("post-crash acquire = %t, %v; want the lease", acquired, err)
	}
	afterCrash()
}

// TestTheEventLogKeepsSiblingServersApart is the exact-transport property of the
// log itself.
//
// Two servers never share a queue, and the isolation is a property of the path
// rather than of a check somebody has to remember to write.
func TestTheEventLogKeepsSiblingServersApart(t *testing.T) {
	t.Parallel()

	log := controllerEventLog{dir: t.TempDir()}
	primary, err := tmuxSocketNameTarget("primary")
	if err != nil {
		t.Fatal(err)
	}
	secondary, err := tmuxSocketPathTarget(filepath.Join(t.TempDir(), "second.sock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := log.mark(primary, controllerTriggerPaneKilled); err != nil {
		t.Fatalf("mark primary: %v", err)
	}
	if err := log.mark(primary, controllerTriggerRuntimeCreated); err != nil {
		t.Fatalf("mark primary again: %v", err)
	}
	pending, err := log.pending(secondary)
	if err != nil {
		t.Fatalf("pending secondary: %v", err)
	}
	if pending {
		t.Fatal("an event on one server appeared on a sibling")
	}
	drained, err := log.drain(secondary)
	if err != nil || drained != 0 {
		t.Fatalf("drain secondary = %d, %v; want zero", drained, err)
	}
	drained, err = log.drain(primary)
	if err != nil || drained != 2 {
		t.Fatalf("drain primary = %d, %v; want the two marked events", drained, err)
	}
	// Draining is idempotent, and a never-marked server reads clean without the
	// read creating anything.
	drained, err = log.drain(primary)
	if err != nil || drained != 0 {
		t.Fatalf("repeat drain = %d, %v; want zero", drained, err)
	}
}

// TestAReadOnTheEventLogCreatesNothing keeps the log off the read path.
//
// `pending` and `drain` are consulted by a worker that may have nothing to do, and
// a read that materialized the state directory would make the trigger a producer
// of exactly the kind of write the read verbs must never perform.
func TestAReadOnTheEventLogCreatesNothing(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "absent")
	log := controllerEventLog{dir: dir}
	target, err := tmuxSocketNameTarget("primary")
	if err != nil {
		t.Fatal(err)
	}
	if pending, err := log.pending(target); err != nil || pending {
		t.Fatalf("pending = %t, %v; want a clean read", pending, err)
	}
	if drained, err := log.drain(target); err != nil || drained != 0 {
		t.Fatalf("drain = %d, %v; want a clean read", drained, err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat %s = %v; want the directory not to exist", dir, err)
	}
}

// TestTheTriggerRefusesAnythingButOneExactServerAndOneDeclaredReason is the
// producer contract.
//
// There is no zero target and no fallback to the default socket or to inherited
// $TMUX: a generated hook passes tmux's own expanded `#{socket_path}`, and apply
// passes the `-L` name it reloaded. A reason outside the closed set is refused
// rather than defaulted, so a config that emits one cannot silently converge with
// an unlabelled producer.
func TestTheTriggerRefusesAnythingButOneExactServerAndOneDeclaredReason(t *testing.T) {
	t.Parallel()

	fixture := newTriggerFixture(t)
	for _, trigger := range []controllerTrigger{
		{reason: controllerTriggerPaneKilled},
		{reason: controllerTriggerPaneKilled, target: explicitTmuxTarget{flag: "-L"}},
		{target: fixture.target},
		{reason: controllerTriggerReason("made-up"), target: fixture.target},
	} {
		if _, err := fixture.runner.run(context.Background(), trigger); err == nil {
			t.Fatalf("trigger %+v was accepted", trigger)
		}
	}
	if len(fixture.passes) != 0 {
		t.Fatalf("refused triggers ran %d convergence bodies", len(fixture.passes))
	}
}

// TestTheReasonSetIsClosedAndShared keeps the generated config and the route
// parser reading one enum.
//
// A reason a config can emit has to be a reason the binary accepts, and the only
// way to guarantee that without a second list is for both sides to read this one.
func TestTheReasonSetIsClosedAndShared(t *testing.T) {
	t.Parallel()

	reasons := controllerTriggerReasons()
	want := []controllerTriggerReason{
		controllerTriggerConfigApply,
		controllerTriggerRuntimeCreated,
		controllerTriggerPaneExited,
		controllerTriggerPaneKilled,
		controllerTriggerWindowUnlinked,
	}
	if !reflect.DeepEqual(reasons, want) {
		t.Fatalf("reasons = %v, want %v", reasons, want)
	}
	for _, reason := range reasons {
		parsed, err := parseControllerTriggerReason(string(reason))
		if err != nil || parsed != reason {
			t.Fatalf("parse %q = %q, %v", reason, parsed, err)
		}
		if !slices.Contains(controllerTriggerReasonSpellings(), string(reason)) {
			t.Fatalf("spellings %v omit %q", controllerTriggerReasonSpellings(), reason)
		}
	}
	for _, raw := range []string{"", " ", "runtime", "RUNTIME-EXITED", "config apply"} {
		if _, err := parseControllerTriggerReason(raw); err == nil {
			t.Fatalf("reason %q was accepted", raw)
		}
	}
}

// TestAConvergenceErrorReachesTheProducer keeps a real failure from being
// reported as a deferral.
//
// The three deferrals are a live create, a busy lease, and an unverifiable host.
// A reconciliation that failed is none of those, and swallowing it would make the
// hook's `|| true` guard hide a broken registry rather than a missing pane layout.
func TestAConvergenceErrorReachesTheProducer(t *testing.T) {
	t.Parallel()

	fixture := newTriggerFixture(t)
	fixture.err = errors.New("registry lock exhausted")
	_, err := fixture.runner.run(context.Background(), controllerTrigger{
		reason: controllerTriggerPaneKilled, target: fixture.target,
	})
	if err == nil || !strings.Contains(err.Error(), "registry lock exhausted") {
		t.Fatalf("err = %v, want the convergence failure", err)
	}
}

// TestTheTriggerRefusesToRunWithoutItsSeams pins the misconfiguration guards.
// None is reachable from a generated hook, and that is the point: they fail
// loudly here rather than silently reporting convergence.
func TestTheTriggerRefusesToRunWithoutItsSeams(t *testing.T) {
	t.Parallel()

	target, err := tmuxSocketNameTarget("primary")
	if err != nil {
		t.Fatal(err)
	}
	trigger := controllerTrigger{reason: controllerTriggerConfigApply, target: target}
	for name, runner := range map[string]*controllerTriggerRunner{
		"no runner": {store: (&fakeResourceStore{now: resourceFixtureClock}).store(), events: controllerEventLog{dir: t.TempDir()}},
		"no store":  {runner: &routedTmuxRunner{}, events: controllerEventLog{dir: t.TempDir()}},
		"no log":    {runner: &routedTmuxRunner{}, store: (&fakeResourceStore{now: resourceFixtureClock}).store()},
	} {
		if _, err := runner.run(context.Background(), trigger); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	var absent *controllerTriggerRunner
	if _, err := absent.run(context.Background(), trigger); err == nil {
		t.Fatal("a nil runner was accepted")
	}
}
