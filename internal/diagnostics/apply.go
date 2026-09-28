package diagnostics

import (
	"fmt"
	"sync"
	"time"
)

// ApplyStep is the closed name of one step of a `tmux.apply` lifecycle -- the
// `config apply` route and its hidden `internal tmux apply` spelling. The steps
// run in this order; an apply that returns early never enters the later ones.
type ApplyStep int

const (
	ApplyStepKeymapMigration ApplyStep = iota
	ApplyStepHookFileMigration
	ApplyStepRetiredFileReclaim
	ApplyStepRouteBind
	ApplyStepBellHookMigration
	ApplyStepConfigWrite
	ApplyStepKeySequenceRetire
	ApplyStepSourceFile
	ApplyStepRouteMarker
	ApplyStepExhaustedReplay
	ApplyStepConverge
	applyStepCount
)

// applyStepTable is the one authority for the apply step vocabulary: each
// step's closed name, which `longest_lock_step` also carries, and the
// lifecycle.outcome field its duration is recorded under. The Event struct
// tags and docs/operational-diagnostics.md must match it row for row.
//
//   - keymap-migration: the keymap schema migration before any write.
//   - hook-file-migration: the managed agent hook file migration.
//   - retired-file-reclaim: reclaiming the retired snapshot, Codex generation,
//     and sidebar startup files, and seeding the status bar defaults.
//   - route-bind: binding the apply to one exact live app-owned tmux server.
//   - bell-hook-migration: the managed tmux bell hook migration on that server.
//   - config-write: writing the generated tmux config.
//   - key-sequence-retire: retiring the previously recorded key sequence state.
//   - source-file: the route guard and the tmux source-file of the new config.
//   - route-marker: recording the logical socket marker on the server.
//   - exhausted-replay: replaying retry-exhausted clean-exit events.
//   - converge: the config-apply controller convergence.
var applyStepTable = [applyStepCount]struct{ name, field string }{
	ApplyStepKeymapMigration:    {"keymap-migration", "step_keymap_migration_ms"},
	ApplyStepHookFileMigration:  {"hook-file-migration", "step_hook_file_migration_ms"},
	ApplyStepRetiredFileReclaim: {"retired-file-reclaim", "step_retired_file_reclaim_ms"},
	ApplyStepRouteBind:          {"route-bind", "step_route_bind_ms"},
	ApplyStepBellHookMigration:  {"bell-hook-migration", "step_bell_hook_migration_ms"},
	ApplyStepConfigWrite:        {"config-write", "step_config_write_ms"},
	ApplyStepKeySequenceRetire:  {"key-sequence-retire", "step_key_sequence_retire_ms"},
	ApplyStepSourceFile:         {"source-file", "step_source_file_ms"},
	ApplyStepRouteMarker:        {"route-marker", "step_route_marker_ms"},
	ApplyStepExhaustedReplay:    {"exhausted-replay", "step_exhausted_replay_ms"},
	ApplyStepConverge:           {"converge", "step_converge_ms"},
}

// applyLockStepOther is the `longest_lock_step` of an acquisition made while
// no apply step was open.
const applyLockStepOther = "other"

// ApplyLockKind is the closed name of the Registry transaction an apply lock
// acquisition belongs to. A site that takes the lock during an apply without
// naming its kind is recorded as ApplyLockKindOther.
type ApplyLockKind string

const (
	// ApplyLockKindPreexistingDeadAgent is the lifecycle reconcile transaction
	// the config-apply recovery of one pre-existing dead Agent Pane runs.
	ApplyLockKindPreexistingDeadAgent ApplyLockKind = "preexisting-dead-agent"
	// ApplyLockKindControlTargets is one control-session identity binding
	// transaction.
	ApplyLockKindControlTargets ApplyLockKind = "control-targets"
	// ApplyLockKindMirrorRecovery is the locked automatic orphan-mirror
	// recovery.
	ApplyLockKindMirrorRecovery ApplyLockKind = "mirror-recovery"
	// ApplyLockKindBindingConverge is the Registry binding reconciliation of a
	// full controller pass.
	ApplyLockKindBindingConverge ApplyLockKind = "binding-converge"
	// ApplyLockKindLifecycleReconcile is the dead Pane lifecycle cascade
	// transaction of a controller pass.
	ApplyLockKindLifecycleReconcile ApplyLockKind = "lifecycle-reconcile"
	// ApplyLockKindSessionLower is the window-unlinked session projection lower.
	ApplyLockKindSessionLower ApplyLockKind = "session-lower"
	// ApplyLockKindOther is every acquisition no site labeled.
	ApplyLockKindOther ApplyLockKind = "other"
)

var applyLockKinds = [...]ApplyLockKind{
	ApplyLockKindPreexistingDeadAgent, ApplyLockKindControlTargets, ApplyLockKindMirrorRecovery, ApplyLockKindBindingConverge, ApplyLockKindLifecycleReconcile,
	ApplyLockKindSessionLower, ApplyLockKindOther,
}

func validApplyLockKind(kind ApplyLockKind) bool {
	for _, candidate := range applyLockKinds {
		if kind == candidate {
			return true
		}
	}
	return false
}

func validApplyLockStep(step string) bool {
	if step == applyLockStepOther {
		return true
	}
	for _, row := range applyStepTable {
		if step == row.name {
			return true
		}
	}
	return false
}

// ApplyLockPhase is the closed name of one part of a transaction's lock hold,
// measured inside the transaction's callback. The first phase starts where
// the site marks it, after the Store's locked read; each mark closes the open
// phase and opens the next, and the callback returning closes the last. What
// the site does not mark -- the grant, the locked read, and anything before
// the first mark -- is left unattributed.
//
//   - observe: live tmux reads and their classification.
//   - plan: plan build and reconcile computation over the Registry.
//   - commit: Registry and tmux writes inside the callback.
//   - store-write: from the callback returning to the release being observed:
//     normalize, validate, the durable write, and the unlock.
type ApplyLockPhase int

const (
	ApplyLockPhaseObserve ApplyLockPhase = iota
	ApplyLockPhasePlan
	ApplyLockPhaseCommit
	ApplyLockPhaseStoreWrite
	applyLockPhaseCount
)

// ApplyLockPhases holds one duration per ApplyLockPhase; nil is a phase the
// transaction never entered.
type ApplyLockPhases [applyLockPhaseCount]*time.Duration

// ApplyLockObservation is one Registry lock acquisition as the Store measured
// it. Held is meaningful only when Released.
type ApplyLockObservation struct {
	Wait     time.Duration
	Held     time.Duration
	Released bool
}

// ApplyLockTransactionRecord is the acquisition an apply held longest.
type ApplyLockTransactionRecord struct {
	Kind   ApplyLockKind
	Step   string
	Wait   time.Duration
	Held   time.Duration
	Phases ApplyLockPhases
}

// ApplyBreakdown is the finished step and lock breakdown of one apply. A nil
// step was never entered, and Longest is nil when no acquisition held a lease.
type ApplyBreakdown struct {
	Steps        [applyStepCount]*time.Duration
	Acquisitions int
	Wait         time.Duration
	Held         time.Duration
	Longest      *ApplyLockTransactionRecord
}

// ApplyRecorder measures one apply: its steps on the lifecycle recorder's own
// clock, and every Registry lock acquisition the Store reports while it is
// open. It belongs to the command scope that Marked tmux.apply, so the
// lifecycle.outcome of that scope carries what it measured, and nothing else
// does.
//
// Steps are disjoint: opening a step closes the open one on the same clock
// reading. The lock side keeps one pending transaction -- the kind a site
// named just before its Store call, and the phases it marked inside the
// callback -- which the next observation consumes. Observations arrive on the
// goroutine that took the lock, after the release, so a pending transaction
// is always the one that observation measured unless a site forgot to name
// itself; then it is `other`.
type ApplyRecorder struct {
	now func() time.Time

	mu            sync.Mutex
	closed        bool
	step          ApplyStep
	stepOpen      bool
	stepStartedAt time.Time
	steps         [applyStepCount]*time.Duration

	pending      *ApplyLockTransaction
	acquisitions int
	wait, held   time.Duration
	longest      *ApplyLockTransactionRecord
}

// ApplyLockTransaction is one site's named Registry transaction, pending until
// the Store observes the acquisition it made.
type ApplyLockTransaction struct {
	apply *ApplyRecorder
	kind  ApplyLockKind

	phase          ApplyLockPhase
	phaseOpen      bool
	phaseStartedAt time.Time
	phases         ApplyLockPhases
	returned       bool
	returnedAt     time.Time
}

// Apply opens the apply recorder of the active command scope. It is nil unless
// that scope is live and Marked tmux.apply, so a nil recorder, a scope of any
// other operation, and a finished scope all measure nothing and read no clock.
// A second call returns the same recorder.
func (r *LifecycleRecorder) Apply() *ApplyRecorder {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	scope := r.command
	if scope == nil || scope.finished || scope.operation != OperationTmuxApply {
		return nil
	}
	if scope.apply == nil {
		scope.apply = &ApplyRecorder{now: r.now}
	}
	return scope.apply
}

// ObserveApplyLock hands one Registry lock observation to the active apply
// recorder, if there is one. It never reads the clock unless an apply is open.
func (r *LifecycleRecorder) ObserveApplyLock(observation ApplyLockObservation) {
	if r == nil {
		return
	}
	r.mu.Lock()
	var apply *ApplyRecorder
	if scope := r.command; scope != nil && !scope.finished {
		apply = scope.apply
	}
	r.mu.Unlock()
	apply.observe(observation)
}

// Step closes the open step and opens step on one clock reading.
func (a *ApplyRecorder) Step(step ApplyStep) {
	if a == nil || step < 0 || step >= applyStepCount {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return
	}
	now := a.now()
	a.closeStep(now)
	a.step, a.stepOpen, a.stepStartedAt = step, true, now
}

// Close closes the open step and stops measuring: later steps and lock
// observations are ignored.
func (a *ApplyRecorder) Close() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return
	}
	if a.stepOpen {
		a.closeStep(a.now())
	}
	a.closed, a.pending = true, nil
}

func (a *ApplyRecorder) closeStep(now time.Time) {
	if !a.stepOpen {
		return
	}
	elapsed := now.Sub(a.stepStartedAt)
	if previous := a.steps[a.step]; previous != nil {
		elapsed += *previous
	}
	a.steps[a.step] = &elapsed
	a.stepOpen = false
}

// BeginLock names the Registry transaction the caller is about to run. It
// reads no clock. The caller ends it after the Store call returns.
func (a *ApplyRecorder) BeginLock(kind ApplyLockKind) *ApplyLockTransaction {
	if a == nil || !validApplyLockKind(kind) {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	transaction := &ApplyLockTransaction{apply: a, kind: kind}
	a.pending = transaction
	return transaction
}

// MarkLockPhase marks phase on the pending transaction; it is a no-op when
// none is pending.
func (a *ApplyRecorder) MarkLockPhase(phase ApplyLockPhase) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pending != nil {
		a.pending.markLocked(phase)
	}
}

// Phase closes the open in-lock phase and opens phase. Store-write is not
// markable: it is measured from Returned to the observation.
func (t *ApplyLockTransaction) Phase(phase ApplyLockPhase) {
	if t == nil {
		return
	}
	t.apply.mu.Lock()
	defer t.apply.mu.Unlock()
	if t.apply.pending == t {
		t.markLocked(phase)
	}
}

func (t *ApplyLockTransaction) markLocked(phase ApplyLockPhase) {
	if t.returned || phase < 0 || phase >= ApplyLockPhaseStoreWrite {
		return
	}
	now := t.apply.now()
	t.closePhase(now)
	t.phase, t.phaseOpen, t.phaseStartedAt = phase, true, now
}

func (t *ApplyLockTransaction) closePhase(now time.Time) {
	if !t.phaseOpen {
		return
	}
	elapsed := now.Sub(t.phaseStartedAt)
	if previous := t.phases[t.phase]; previous != nil {
		elapsed += *previous
	}
	t.phases[t.phase] = &elapsed
	t.phaseOpen = false
}

// Returned marks the transaction's callback returning: it closes the open
// phase and starts the store-write phase.
func (t *ApplyLockTransaction) Returned() {
	if t == nil {
		return
	}
	t.apply.mu.Lock()
	defer t.apply.mu.Unlock()
	if t.apply.pending != t || t.returned {
		return
	}
	now := t.apply.now()
	t.closePhase(now)
	t.returned, t.returnedAt = true, now
}

// End clears the transaction when no observation consumed it -- the Store call
// failed before it took the lock -- so it cannot name a later acquisition.
func (t *ApplyLockTransaction) End() {
	if t == nil {
		return
	}
	t.apply.mu.Lock()
	defer t.apply.mu.Unlock()
	if t.apply.pending == t {
		t.apply.pending = nil
	}
}

// observe counts one acquisition into the apply totals and consumes the
// pending transaction. Only a released lease can be the longest hold.
func (a *ApplyRecorder) observe(observation ApplyLockObservation) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return
	}
	transaction := a.pending
	a.pending = nil
	wait := max(observation.Wait, 0)
	a.acquisitions++
	a.wait += wait
	if !observation.Released {
		return
	}
	held := max(observation.Held, 0)
	a.held += held
	if a.longest != nil && held <= a.longest.Held {
		return
	}
	record := &ApplyLockTransactionRecord{Kind: ApplyLockKindOther, Step: applyLockStepOther, Wait: wait, Held: held}
	if a.stepOpen {
		record.Step = applyStepTable[a.step].name
	}
	if transaction != nil {
		record.Kind = transaction.kind
		record.Phases = transaction.phases
		if transaction.returned {
			storeWrite := a.now().Sub(transaction.returnedAt)
			record.Phases[ApplyLockPhaseStoreWrite] = &storeWrite
		}
	}
	a.longest = record
}

// breakdown is the finished measurement. A step still open is not reported.
func (a *ApplyRecorder) breakdown() ApplyBreakdown {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := ApplyBreakdown{Steps: a.steps, Acquisitions: a.acquisitions, Wait: a.wait, Held: a.held}
	if a.longest != nil {
		longest := *a.longest
		out.Longest = &longest
	}
	return out
}

// setApplyBreakdown writes the breakdown onto a tmux.apply lifecycle.outcome.
// Each duration is rounded down to whole milliseconds. The step fields are
// written only when their sum stays within duration_ms, and the phase fields
// only when their sum stays within the longest hold, the way setCreatePhases
// drops a breakdown that a non-monotonic clock pushed past its bound.
func (event *Event) setApplyBreakdown(breakdown ApplyBreakdown) {
	var steps [applyStepCount]*int64
	var stepSum int64
	for step, duration := range breakdown.Steps {
		if duration == nil {
			continue
		}
		ms := max(*duration, 0).Milliseconds()
		steps[step] = &ms
		stepSum += ms
	}
	if stepSum <= event.DurationMS {
		event.setApplyStepFields(steps)
	}
	count := breakdown.Acquisitions
	wait, held := max(breakdown.Wait, 0).Milliseconds(), max(breakdown.Held, 0).Milliseconds()
	event.LockAcquisitionCount, event.LockWaitTotalMS, event.LockHeldTotalMS = &count, &wait, &held
	longest := breakdown.Longest
	if longest == nil {
		return
	}
	kind, step := string(longest.Kind), longest.Step
	longestWait, longestHeld := max(longest.Wait, 0).Milliseconds(), max(longest.Held, 0).Milliseconds()
	event.LongestLockKind, event.LongestLockStep = kind, step
	event.LongestLockWaitMS, event.LongestLockHeldMS = &longestWait, &longestHeld
	var phases [applyLockPhaseCount]*int64
	var phaseSum int64
	for phase, duration := range longest.Phases {
		if duration == nil {
			continue
		}
		ms := max(*duration, 0).Milliseconds()
		phases[phase] = &ms
		phaseSum += ms
	}
	if phaseSum <= longestHeld {
		event.LongestLockObserveMS, event.LongestLockPlanMS = phases[ApplyLockPhaseObserve], phases[ApplyLockPhasePlan]
		event.LongestLockCommitMS, event.LongestLockStoreWriteMS = phases[ApplyLockPhaseCommit], phases[ApplyLockPhaseStoreWrite]
	}
}

func (event *Event) setApplyStepFields(steps [applyStepCount]*int64) {
	event.StepKeymapMigrationMS, event.StepHookFileMigrationMS = steps[ApplyStepKeymapMigration], steps[ApplyStepHookFileMigration]
	event.StepRetiredFileReclaimMS, event.StepRouteBindMS = steps[ApplyStepRetiredFileReclaim], steps[ApplyStepRouteBind]
	event.StepBellHookMigrationMS, event.StepConfigWriteMS = steps[ApplyStepBellHookMigration], steps[ApplyStepConfigWrite]
	event.StepKeySequenceRetireMS, event.StepSourceFileMS = steps[ApplyStepKeySequenceRetire], steps[ApplyStepSourceFile]
	event.StepRouteMarkerMS, event.StepExhaustedReplayMS = steps[ApplyStepRouteMarker], steps[ApplyStepExhaustedReplay]
	event.StepConvergeMS = steps[ApplyStepConverge]
}

// applyStepFields lists the step fields in ApplyStep order.
func (event Event) applyStepFields() [applyStepCount]*int64 {
	return [applyStepCount]*int64{
		event.StepKeymapMigrationMS, event.StepHookFileMigrationMS, event.StepRetiredFileReclaimMS,
		event.StepRouteBindMS, event.StepBellHookMigrationMS, event.StepConfigWriteMS,
		event.StepKeySequenceRetireMS, event.StepSourceFileMS, event.StepRouteMarkerMS,
		event.StepExhaustedReplayMS, event.StepConvergeMS,
	}
}

// applyLockPhaseFields lists the longest transaction's phase fields in
// ApplyLockPhase order.
func (event Event) applyLockPhaseFields() [applyLockPhaseCount]*int64 {
	return [applyLockPhaseCount]*int64{
		event.LongestLockObserveMS, event.LongestLockPlanMS, event.LongestLockCommitMS, event.LongestLockStoreWriteMS,
	}
}

// hasApplyFields reports any tmux.apply breakdown field, which only a
// tmux.apply lifecycle.outcome may carry.
func (event Event) hasApplyFields() bool {
	for _, value := range event.applyStepFields() {
		if value != nil {
			return true
		}
	}
	for _, value := range event.applyLockPhaseFields() {
		if value != nil {
			return true
		}
	}
	return event.LockAcquisitionCount != nil || event.LockWaitTotalMS != nil || event.LockHeldTotalMS != nil ||
		event.LongestLockKind != "" || event.LongestLockStep != "" || event.LongestLockWaitMS != nil || event.LongestLockHeldMS != nil
}

// validateApplyBreakdown admits the breakdown fields on a tmux.apply
// lifecycle.outcome only, and there enforces every invariant setApplyBreakdown
// writes: closed kind and step names, non-negative values, the steps within
// duration_ms, the lock totals present together, the longest acquisition
// complete and within the totals, and its phases within its hold.
func validateApplyBreakdown(event Event) error {
	if !event.hasApplyFields() {
		return nil
	}
	if event.Event != "lifecycle.outcome" || Operation(event.Operation) != OperationTmuxApply {
		return fmt.Errorf("apply breakdown fields on unrelated event")
	}
	var stepSum int64
	for _, value := range event.applyStepFields() {
		if value == nil {
			continue
		}
		if *value < 0 {
			return fmt.Errorf("invalid apply step")
		}
		stepSum += *value
	}
	if stepSum > event.DurationMS {
		return fmt.Errorf("apply steps exceed the duration")
	}
	totals := event.LockAcquisitionCount != nil
	if totals != (event.LockWaitTotalMS != nil) || totals != (event.LockHeldTotalMS != nil) {
		return fmt.Errorf("incomplete apply lock totals")
	}
	if totals && (*event.LockAcquisitionCount < 0 || *event.LockWaitTotalMS < 0 || *event.LockHeldTotalMS < 0) {
		return fmt.Errorf("invalid apply lock totals")
	}
	longest := event.LongestLockKind != ""
	if longest != (event.LongestLockStep != "") || longest != (event.LongestLockWaitMS != nil) || longest != (event.LongestLockHeldMS != nil) {
		return fmt.Errorf("incomplete apply longest lock")
	}
	phases := event.applyLockPhaseFields()
	if !longest {
		for _, value := range phases {
			if value != nil {
				return fmt.Errorf("apply lock phases without a longest lock")
			}
		}
		return nil
	}
	if !totals || *event.LockAcquisitionCount < 1 {
		return fmt.Errorf("apply longest lock without lock totals")
	}
	if !validApplyLockKind(ApplyLockKind(event.LongestLockKind)) || !validApplyLockStep(event.LongestLockStep) {
		return fmt.Errorf("invalid apply longest lock name")
	}
	held := *event.LongestLockHeldMS
	if *event.LongestLockWaitMS < 0 || held < 0 || *event.LongestLockWaitMS > *event.LockWaitTotalMS || held > *event.LockHeldTotalMS {
		return fmt.Errorf("invalid apply longest lock timing")
	}
	var phaseSum int64
	for _, value := range phases {
		if value == nil {
			continue
		}
		if *value < 0 {
			return fmt.Errorf("invalid apply lock phase")
		}
		phaseSum += *value
	}
	if phaseSum > held {
		return fmt.Errorf("apply lock phases exceed the longest hold")
	}
	return nil
}
