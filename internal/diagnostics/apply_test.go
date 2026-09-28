package diagnostics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// applyClock is a manual clock: it reads the same instant until the test
// advances it, so every step and phase has an exact, chosen length.
type applyClock struct {
	mu  sync.Mutex
	now time.Time
}

func newApplyClock() *applyClock {
	return &applyClock{now: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)}
}

func (c *applyClock) read() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *applyClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func applyMS(ms int64) *int64 { return &ms }

// applyRecorderFixture opens a tmux.apply command scope on a manual clock.
func applyRecorderFixture(writer EventWriter) (*LifecycleRecorder, *applyClock, func(error)) {
	clock := newApplyClock()
	recorder := NewLifecycleRecorder(writer, "apply-run", "1.0.0", "tmux")
	recorder.now = clock.read
	finish := recorder.BeginCommand()
	recorder.Mark(OperationTmuxApply)
	return recorder, clock, finish
}

// applyOutcomeFixture is a valid tmux.apply lifecycle.outcome carrying every
// breakdown field.
func applyOutcomeFixture() Event {
	return Event{
		At: "2026-09-29T00:00:00Z", Level: "info", Component: "runtime", Event: "lifecycle.outcome",
		Result: "success", DurationMS: 100, RunID: "apply-run", Version: "1.0.0", MuxBackend: "tmux",
		Operation:             string(OperationTmuxApply),
		StepKeymapMigrationMS: applyMS(10), StepConvergeMS: applyMS(50),
		LockAcquisitionCount: intPointer(2), LockWaitTotalMS: applyMS(30), LockHeldTotalMS: applyMS(70),
		LongestLockKind: string(ApplyLockKindMirrorRecovery), LongestLockStep: "converge",
		LongestLockWaitMS: applyMS(20), LongestLockHeldMS: applyMS(60),
		LongestLockObserveMS: applyMS(10), LongestLockPlanMS: applyMS(10), LongestLockCommitMS: applyMS(10), LongestLockStoreWriteMS: applyMS(10),
	}
}

// TestApplyOutcomeStepsAreClosedNamesWithinDuration (acceptance 1) drives an
// apply's steps on an injected clock: every step the apply entered is one
// closed-name field with its exact length rounded down to whole milliseconds,
// a step it never entered is absent, the steps are disjoint, and their sum
// stays within duration_ms.
func TestApplyOutcomeStepsAreClosedNamesWithinDuration(t *testing.T) {
	t.Parallel()
	writer := &recordingEventWriter{}
	recorder, clock, finish := applyRecorderFixture(writer)
	apply := recorder.Apply()
	if apply == nil || recorder.Apply() != apply {
		t.Fatal("a tmux.apply scope did not open exactly one apply recorder")
	}
	clock.advance(5 * time.Millisecond) // before the first step: unattributed
	apply.Step(ApplyStepKeymapMigration)
	clock.advance(7 * time.Millisecond)
	apply.Step(ApplyStepHookFileMigration)
	clock.advance(8 * time.Millisecond)
	apply.Step(ApplyStepRetiredFileReclaim)
	clock.advance(1500 * time.Microsecond)
	apply.Step(ApplyStepRouteBind)
	clock.advance(8500 * time.Microsecond)
	apply.Step(ApplyStepConfigWrite)
	clock.advance(10999 * time.Microsecond)
	apply.Close()
	apply.Step(ApplyStepSourceFile) // after Close: ignored
	clock.advance(4001 * time.Microsecond)
	finish(nil)

	events := writer.snapshot()
	if len(events) != 2 {
		t.Fatalf("events = %+v, want start and outcome", events)
	}
	outcome := events[1]
	if outcome.Event != "lifecycle.outcome" || outcome.Operation != string(OperationTmuxApply) || outcome.DurationMS != 45 {
		t.Fatalf("outcome = %+v, want a 45ms tmux.apply outcome", outcome)
	}
	want := map[string]int64{
		"step_keymap_migration_ms": 7, "step_hook_file_migration_ms": 8, "step_retired_file_reclaim_ms": 1,
		"step_route_bind_ms": 8, "step_config_write_ms": 10,
	}
	got := map[string]int64{}
	var sum int64
	for step, value := range outcome.applyStepFields() {
		if value != nil {
			got[applyStepTable[step].field] = *value
			sum += *value
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("step fields = %v, want %v", got, want)
	}
	if sum > outcome.DurationMS {
		t.Fatalf("step sum %d > duration_ms %d", sum, outcome.DurationMS)
	}
	if outcome.LockAcquisitionCount == nil || *outcome.LockAcquisitionCount != 0 || *outcome.LockWaitTotalMS != 0 ||
		*outcome.LockHeldTotalMS != 0 || outcome.LongestLockKind != "" {
		t.Fatalf("lock fields = %+v, want zero totals and no longest lock", outcome)
	}
	if _, err := sanitizeEvent(outcome, ""); err != nil {
		t.Fatalf("recorded outcome rejected: %v", err)
	}

	// A breakdown whose steps would exceed duration_ms (a clock that went
	// backwards) keeps the outcome and the lock totals and drops the steps.
	backwards := &recordingEventWriter{}
	recorder, clock, finish = applyRecorderFixture(backwards)
	apply = recorder.Apply()
	apply.Step(ApplyStepConverge)
	clock.advance(20 * time.Millisecond)
	apply.Close()
	clock.advance(-10 * time.Millisecond)
	finish(nil)
	dropped := backwards.snapshot()[1]
	for _, value := range dropped.applyStepFields() {
		if value != nil {
			t.Fatalf("steps beyond duration_ms were written: %+v", dropped)
		}
	}
	if dropped.LockAcquisitionCount == nil || dropped.DurationMS != 10 {
		t.Fatalf("outcome without steps = %+v", dropped)
	}
}

// TestApplyStepTableMatchesTheEventSchema pins that applyStepTable is the one
// authority: its names are distinct, and its fields are exactly the step
// fields the Event writes, in ApplyStep order.
func TestApplyStepTableMatchesTheEventSchema(t *testing.T) {
	t.Parallel()
	var event Event
	var steps [applyStepCount]*int64
	for i := range steps {
		steps[i] = applyMS(int64(i))
	}
	event.setApplyStepFields(steps)
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	_ = json.Unmarshal(data, &record)
	names := map[string]bool{}
	for step, row := range applyStepTable {
		if names[row.name] || row.name == applyLockStepOther || !strings.HasPrefix(row.field, "step_") {
			t.Fatalf("step %d row %+v is not a distinct closed name", step, row)
		}
		names[row.name] = true
		if record[row.field] != float64(step) {
			t.Fatalf("field %q = %v, want the ApplyStep %d value", row.field, record[row.field], step)
		}
		if *event.applyStepFields()[step] != int64(step) {
			t.Fatalf("applyStepFields()[%d] is not %s", step, row.field)
		}
	}
	// The operator doc names every step field and every transaction kind.
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "operational-diagnostics.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range applyStepTable {
		if !strings.Contains(string(doc), "`"+row.field+"`") {
			t.Fatalf("docs/operational-diagnostics.md does not name %s", row.field)
		}
	}
	for _, kind := range applyLockKinds {
		if !strings.Contains(string(doc), "`"+string(kind)+"`") {
			t.Fatalf("docs/operational-diagnostics.md does not name kind %s", kind)
		}
	}
}

// TestApplyOutcomeLongestLockAndTotals (acceptance 2) drives fake Store
// observations through the recorder seam: the released acquisition held
// longest wins with its kind, the step it ran in, its wait and hold, and its
// in-lock phases; every acquisition, an unlabeled one and a timeout included,
// counts in the totals; and a phase breakdown beyond the hold is dropped while
// the rest of the longest lock stays.
func TestApplyOutcomeLongestLockAndTotals(t *testing.T) {
	t.Parallel()
	writer := &recordingEventWriter{}
	recorder, clock, finish := applyRecorderFixture(writer)
	apply := recorder.Apply()

	apply.Step(ApplyStepExhaustedReplay)
	replay := apply.BeginLock(ApplyLockKindLifecycleReconcile)
	replay.Phase(ApplyLockPhaseObserve)
	clock.advance(30 * time.Millisecond)
	replay.Returned()
	clock.advance(time.Millisecond)
	recorder.ObserveApplyLock(ApplyLockObservation{Wait: 5 * time.Millisecond, Held: 400 * time.Millisecond, Released: true})
	replay.End()

	apply.Step(ApplyStepConverge)
	recovery := apply.BeginLock(ApplyLockKindMirrorRecovery)
	recovery.Phase(ApplyLockPhaseObserve)
	clock.advance(100 * time.Millisecond)
	recovery.Phase(ApplyLockPhasePlan)
	clock.advance(50*time.Millisecond + 900*time.Microsecond)
	recovery.Phase(ApplyLockPhaseCommit)
	clock.advance(200 * time.Millisecond)
	recovery.Phase(ApplyLockPhaseObserve) // phases accumulate
	clock.advance(10 * time.Millisecond)
	recovery.Returned()
	recovery.Phase(ApplyLockPhaseCommit) // after the callback returned: ignored
	clock.advance(25 * time.Millisecond)
	recorder.ObserveApplyLock(ApplyLockObservation{Wait: 70 * time.Millisecond, Held: 900 * time.Millisecond, Released: true})
	recovery.End()

	// An acquisition no site named, and a timeout, count in the totals only.
	recorder.ObserveApplyLock(ApplyLockObservation{Wait: 2 * time.Millisecond, Held: 60 * time.Millisecond, Released: true})
	recorder.ObserveApplyLock(ApplyLockObservation{Wait: 30 * time.Second, Held: time.Hour})
	// A transaction whose Store call failed before locking names nothing later.
	apply.BeginLock(ApplyLockKindSessionLower).End()
	apply.Close()
	clock.advance(time.Millisecond)
	finish(nil)

	outcome := writer.snapshot()[1]
	if outcome.LongestLockKind != string(ApplyLockKindMirrorRecovery) || outcome.LongestLockStep != "converge" ||
		*outcome.LongestLockWaitMS != 70 || *outcome.LongestLockHeldMS != 900 {
		t.Fatalf("longest lock = %s/%s wait=%v held=%v, want mirror-recovery/converge 70/900",
			outcome.LongestLockKind, outcome.LongestLockStep, *outcome.LongestLockWaitMS, *outcome.LongestLockHeldMS)
	}
	phases := map[string]int64{}
	for phase, value := range outcome.applyLockPhaseFields() {
		if value != nil {
			phases[[]string{"observe", "plan", "commit", "store-write"}[phase]] = *value
		}
	}
	if want := map[string]int64{"observe": 110, "plan": 50, "commit": 200, "store-write": 25}; !reflect.DeepEqual(phases, want) {
		t.Fatalf("phases = %v, want %v", phases, want)
	}
	if sum := phases["observe"] + phases["plan"] + phases["commit"] + phases["store-write"]; sum > *outcome.LongestLockHeldMS {
		t.Fatalf("phase sum %d > held %d", sum, *outcome.LongestLockHeldMS)
	}
	if *outcome.LockAcquisitionCount != 4 || *outcome.LockWaitTotalMS != 30077 || *outcome.LockHeldTotalMS != 1360 {
		t.Fatalf("totals = %d/%d/%d, want 4 acquisitions, 30077ms wait, 1360ms held",
			*outcome.LockAcquisitionCount, *outcome.LockWaitTotalMS, *outcome.LockHeldTotalMS)
	}
	if _, err := sanitizeEvent(outcome, ""); err != nil {
		t.Fatalf("recorded outcome rejected: %v", err)
	}

	// An unlabeled acquisition that held longest is `other`, in the step it
	// ran in, with no phases; outside every step it is in step `other`.
	unlabeled := &recordingEventWriter{}
	recorder, _, finish = applyRecorderFixture(unlabeled)
	apply = recorder.Apply()
	apply.Step(ApplyStepConverge)
	recorder.ObserveApplyLock(ApplyLockObservation{Held: 3 * time.Second, Released: true})
	apply.Close()
	finish(nil)
	if got := unlabeled.snapshot()[1]; got.LongestLockKind != "other" || got.LongestLockStep != "converge" ||
		*got.LongestLockHeldMS != 3000 || got.LongestLockObserveMS != nil || got.LongestLockStoreWriteMS != nil {
		t.Fatalf("unlabeled longest = %+v", got)
	}
	stepless := &recordingEventWriter{}
	recorder, _, finish = applyRecorderFixture(stepless)
	recorder.Apply()
	recorder.ObserveApplyLock(ApplyLockObservation{Held: time.Second, Released: true})
	finish(nil)
	if got := stepless.snapshot()[1]; got.LongestLockKind != "other" || got.LongestLockStep != "other" {
		t.Fatalf("stepless longest = %+v", got)
	}

	// Phases past the hold -- measured on a clock that disagrees with the
	// Store's -- are dropped whole; the kind, step, and timings stay.
	skewed := &recordingEventWriter{}
	recorder, clock, finish = applyRecorderFixture(skewed)
	apply = recorder.Apply()
	apply.Step(ApplyStepConverge)
	transaction := apply.BeginLock(ApplyLockKindBindingConverge)
	transaction.Phase(ApplyLockPhaseCommit)
	clock.advance(2 * time.Second)
	transaction.Returned()
	recorder.ObserveApplyLock(ApplyLockObservation{Held: time.Second, Released: true})
	transaction.End()
	apply.Close()
	finish(nil)
	got := skewed.snapshot()[1]
	if got.LongestLockKind != string(ApplyLockKindBindingConverge) || *got.LongestLockHeldMS != 1000 {
		t.Fatalf("skewed longest = %+v", got)
	}
	for _, value := range got.applyLockPhaseFields() {
		if value != nil {
			t.Fatalf("phases beyond the hold were written: %+v", got)
		}
	}

	// Observations reach only a live tmux.apply scope.
	other := &recordingEventWriter{}
	session := NewLifecycleRecorder(other, "apply-run", "1.0.0", "tmux")
	done := session.BeginCommand()
	session.Mark(OperationSessionCreate)
	if session.Apply() != nil {
		t.Fatal("a session.create scope opened an apply recorder")
	}
	session.ObserveApplyLock(ApplyLockObservation{Held: time.Second, Released: true})
	done(nil)
	if got := other.snapshot()[1]; got.hasApplyFields() {
		t.Fatalf("session.create outcome carries apply fields: %+v", got)
	}
	var nilRecorder *LifecycleRecorder
	nilRecorder.ObserveApplyLock(ApplyLockObservation{Released: true})
	if nilRecorder.Apply() != nil {
		t.Fatal("a nil recorder opened an apply recorder")
	}
}

// TestApplyOutcomeKeysAreTheClosedAllowlist (acceptance 4) round-trips a fully
// populated tmux.apply outcome through a real journal: the line's keys are
// exactly the lifecycle.outcome keys plus the apply breakdown allowlist, and
// the only string values it adds are closed names.
func TestApplyOutcomeKeysAreTheClosedAllowlist(t *testing.T) {
	t.Parallel()
	store := NewStore(filepath.Join(t.TempDir(), "operations.jsonl"))
	recorder, clock, finish := applyRecorderFixture(store)
	apply := recorder.Apply()
	for step := range applyStepCount {
		apply.Step(step)
		clock.advance(time.Millisecond)
	}
	transaction := apply.BeginLock(ApplyLockKindControlTargets)
	transaction.Phase(ApplyLockPhaseObserve)
	transaction.Phase(ApplyLockPhasePlan)
	transaction.Phase(ApplyLockPhaseCommit)
	transaction.Returned()
	recorder.ObserveApplyLock(ApplyLockObservation{Wait: time.Millisecond, Held: 5 * time.Millisecond, Released: true})
	transaction.End()
	apply.Close()
	clock.advance(time.Millisecond)
	finish(nil)

	events, err := store.Read()
	if err != nil || len(events) != 2 {
		t.Fatalf("journal = %+v err=%v", events, err)
	}
	data, err := json.Marshal(events[1])
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	_ = json.Unmarshal(data, &record)
	allowed := []string{"at", "level", "component", "event", "result", "duration_ms", "run_id", "version", "mux_backend", "operation",
		"lock_acquisition_count", "lock_wait_total_ms", "lock_held_total_ms", "longest_lock_kind", "longest_lock_step",
		"longest_lock_wait_ms", "longest_lock_held_ms", "longest_lock_observe_ms", "longest_lock_plan_ms",
		"longest_lock_commit_ms", "longest_lock_store_write_ms"}
	for _, row := range applyStepTable {
		allowed = append(allowed, row.field)
	}
	var keys []string
	for key := range record {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	sort.Strings(allowed)
	if !slices.Equal(keys, allowed) {
		t.Fatalf("keys = %v\nwant %v", keys, allowed)
	}
	if record["longest_lock_kind"] != "control-targets" || record["longest_lock_step"] != "converge" {
		t.Fatalf("closed names = %v/%v", record["longest_lock_kind"], record["longest_lock_step"])
	}
	// The read-only runtime projection accepts the record as an apply outcome.
	health, err := ReadRuntimeHealth(store)
	if err != nil || health.Malformed != 0 || health.Apply != RuntimeHealthy {
		t.Fatalf("runtime health = %+v err=%v, want a healthy apply and no malformed line", health, err)
	}
}

// TestApplyBreakdownFieldsClosedShape (acceptance 4) pins the validator: the
// fields are admitted on a tmux.apply lifecycle.outcome only, with closed
// names, non-negative values, and every sum invariant, and every other family
// refuses each one of them.
func TestApplyBreakdownFieldsClosedShape(t *testing.T) {
	t.Parallel()
	if _, err := sanitizeEvent(applyOutcomeFixture(), ""); err != nil {
		t.Fatalf("fixture rejected: %v", err)
	}
	for name, mutate := range map[string]func(*Event){
		"no longest lock": func(e *Event) {
			e.LongestLockKind, e.LongestLockStep, e.LongestLockWaitMS, e.LongestLockHeldMS = "", "", nil, nil
			e.LongestLockObserveMS, e.LongestLockPlanMS, e.LongestLockCommitMS, e.LongestLockStoreWriteMS = nil, nil, nil, nil
		},
		"no phases": func(e *Event) { e.LongestLockObserveMS, e.LongestLockStoreWriteMS = nil, nil },
		"steps only": func(e *Event) {
			*e = Event{At: e.At, Level: e.Level, Component: e.Component, Event: e.Event, Result: e.Result, DurationMS: 5, RunID: e.RunID, Version: e.Version, MuxBackend: e.MuxBackend, Operation: e.Operation, StepConvergeMS: applyMS(5)}
		},
		"step other": func(e *Event) { e.LongestLockStep = "other" },
		"every kind": func(e *Event) { e.LongestLockKind = string(ApplyLockKindOther) },
		"error outcome": func(e *Event) {
			e.Level, e.Result, e.Kind, e.Code = "error", "error", "runtime", string(CodeTmuxApplyFailed)
		},
		"reload skipped":   func(e *Event) { e.Code = string(CodeTmuxApplyReloadSkipped) },
		"steps are whole":  func(e *Event) { e.StepKeymapMigrationMS = applyMS(50) },
		"phases are whole": func(e *Event) { e.LongestLockCommitMS = applyMS(30) },
		"zero acquisitions": func(e *Event) {
			*e = applyOutcomeFixture()
			e.LongestLockKind = ""
			e.LongestLockStep = ""
			e.LongestLockWaitMS, e.LongestLockHeldMS = nil, nil
			e.LongestLockObserveMS, e.LongestLockPlanMS, e.LongestLockCommitMS, e.LongestLockStoreWriteMS = nil, nil, nil, nil
			e.LockAcquisitionCount = intPointer(0)
		},
	} {
		event := applyOutcomeFixture()
		mutate(&event)
		if _, err := sanitizeEvent(event, ""); err != nil {
			t.Fatalf("%s rejected: %v", name, err)
		}
	}
	for name, mutate := range map[string]func(*Event){
		"unknown kind":           func(e *Event) { e.LongestLockKind = "registry-write" },
		"free-text kind":         func(e *Event) { e.LongestLockKind = "/home/alice/.state" },
		"unknown step":           func(e *Event) { e.LongestLockStep = "reload" },
		"negative step":          func(e *Event) { e.StepConvergeMS = applyMS(-1) },
		"steps > duration":       func(e *Event) { e.StepKeymapMigrationMS = applyMS(51) },
		"negative count":         func(e *Event) { e.LockAcquisitionCount = intPointer(-1) },
		"negative wait total":    func(e *Event) { e.LockWaitTotalMS = applyMS(-1) },
		"negative held total":    func(e *Event) { e.LockHeldTotalMS = applyMS(-1) },
		"partial totals":         func(e *Event) { e.LockHeldTotalMS = nil },
		"longest without totals": func(e *Event) { e.LockAcquisitionCount, e.LockWaitTotalMS, e.LockHeldTotalMS = nil, nil, nil },
		"longest with no count":  func(e *Event) { e.LockAcquisitionCount = intPointer(0) },
		"partial longest":        func(e *Event) { e.LongestLockStep = "" },
		"longest without held":   func(e *Event) { e.LongestLockHeldMS = nil },
		"negative longest wait":  func(e *Event) { e.LongestLockWaitMS = applyMS(-1) },
		"longest wait > total":   func(e *Event) { e.LongestLockWaitMS = applyMS(31) },
		"longest held > total":   func(e *Event) { e.LongestLockHeldMS = applyMS(71) },
		"negative phase":         func(e *Event) { e.LongestLockPlanMS = applyMS(-1) },
		"phases > held":          func(e *Event) { e.LongestLockCommitMS = applyMS(31) },
		"phases without longest": func(e *Event) {
			e.LongestLockKind, e.LongestLockStep, e.LongestLockWaitMS, e.LongestLockHeldMS = "", "", nil, nil
		},
		"session create":  func(e *Event) { e.Operation = string(OperationSessionCreate) },
		"lifecycle start": func(e *Event) { e.Event, e.Result = "lifecycle.start", "started" },
	} {
		t.Run(name, func(t *testing.T) {
			event := applyOutcomeFixture()
			mutate(&event)
			if _, err := sanitizeEvent(event, ""); err == nil {
				t.Fatalf("accepted %+v", event)
			}
		})
	}

	// Every other family refuses each field on its own.
	fields := map[string]func(*Event){
		"lock_acquisition_count": func(e *Event) { e.LockAcquisitionCount = intPointer(0) },
		"lock_wait_total_ms":     func(e *Event) { e.LockWaitTotalMS = applyMS(0) },
		"lock_held_total_ms":     func(e *Event) { e.LockHeldTotalMS = applyMS(0) },
		"longest_lock_kind":      func(e *Event) { e.LongestLockKind = string(ApplyLockKindOther) },
		"longest_lock_step":      func(e *Event) { e.LongestLockStep = "converge" },
		"longest_lock_wait_ms":   func(e *Event) { e.LongestLockWaitMS = applyMS(0) },
		"longest_lock_held_ms":   func(e *Event) { e.LongestLockHeldMS = applyMS(0) },
		"longest_lock_phase_ms":  func(e *Event) { e.LongestLockStoreWriteMS = applyMS(0) },
	}
	for step := range applyStepCount {
		fields[applyStepTable[step].field] = func(e *Event) {
			var steps [applyStepCount]*int64
			steps[step] = applyMS(0)
			e.setApplyStepFields(steps)
		}
	}
	session := applyOutcomeFixture()
	session = Event{At: session.At, Level: "info", Component: "runtime", Event: "lifecycle.outcome", Result: "success",
		DurationMS: 100, RunID: "run", Version: "1.0.0", MuxBackend: "tmux", Operation: string(OperationSessionCreate)}
	start := session
	start.Event, start.Result, start.Operation = "lifecycle.start", "started", string(OperationTmuxApply)
	families := map[string]Event{
		"command.outcome":            fixtureEvent("run"),
		"lifecycle.outcome session":  session,
		"lifecycle.start tmux.apply": start,
		"create.outcome":             createOutcomeFixture(),
		"registry.lock.acquisition":  registryLockFixture(),
		"topology.teardown":          teardownFixtureEvent(),
	}
	for familyName, base := range families {
		if _, err := sanitizeEvent(base, ""); err != nil {
			t.Fatalf("%s base rejected: %v", familyName, err)
		}
		for field, set := range fields {
			event := base
			set(&event)
			if _, err := sanitizeEvent(event, ""); err == nil {
				t.Fatalf("%s accepted %s", familyName, field)
			}
		}
	}
}
