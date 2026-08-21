package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/core/controller"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

// The one lifecycle/mutation trigger entrypoint.
//
// Before this file there were three. `internal tmux reconcile-bindings` ran the
// binding half of convergence and executed the reconciler's mirror writes
// directly; `internal tmux release-dead-agent-panes` ran the exit projection on
// its own transaction; `config apply` ran the first and not the second. All
// three answered the same question -- "the machine may have moved, bring the
// registry and the mirrors back together" -- and all three answered it with a
// different subset of the same pass. Two of them wrote the same drift twice on
// the same pane exit, because a full reconciliation already projects the
// terminations the dedicated sweep was there to project.
//
// So the sequence is here, once, and a producer no longer chooses it. A producer
// states a reason and one exact server; this file decides that convergence means
// a fail-closed observation, one locked reconciliation, and a repeat pass that
// proves the first one landed. Adding a fourth producer adds a reason constant,
// not a fourth definition of convergence.
//
// Three properties are contractual:
//
//  1. At most one worker converges one exact server at a time. Not because two
//     would corrupt anything -- the registry's own lock prevents that -- but
//     because both hooks fire on every pane exit in every session, and N workers
//     contending for one registry lock is how a burst turns into lock-attempt
//     exhaustion instead of into a convergence.
//  2. A losing producer exits successfully. Its event is already recorded and the
//     holder has not yet acknowledged it, so the holder will run a further pass
//     for it. Blocking or retrying here would rebuild the contention the lease
//     just removed.
//  3. The pass repeats until one of them writes nothing. That final no-op pass is
//     the reobservation: the only way to learn that a write landed and did not
//     converge -- a second client racing the same repair, a hook that rewrote the
//     option back -- is to ask the machine again.

// controllerTriggerReason names the producer of one dirty event.
//
// It is a closed set. The worker uses it only to choose the safe observation
// scope: pane-exited may use its exact hook Pane, while kill and coalesced paths
// must re-observe the whole host. A reason never supplies a termination
// classification or bypasses the shared convergence body.
type controllerTriggerReason string

const (
	// controllerTriggerConfigApply is the generated-config reload boundary. It
	// is the one producer with no hook session, because the reload is what
	// installs the hooks.
	controllerTriggerConfigApply controllerTriggerReason = "config-apply"
	// controllerTriggerRuntimeCreated is tmux's after-new-window and
	// after-split-window.
	controllerTriggerRuntimeCreated controllerTriggerReason = "runtime-created"
	// controllerTriggerPaneExited is tmux's pane-exited. It carries exact
	// #{hook_pane} evidence and reobserves only that activation handle.
	controllerTriggerPaneExited controllerTriggerReason = "pane-exited"
	// controllerTriggerPaneKilled is tmux's after-kill-pane. That hook cannot
	// name the dead Pane, so it retains the whole-host reobservation.
	controllerTriggerPaneKilled controllerTriggerReason = "pane-killed"
	// controllerTriggerWindowUnlinked is tmux's window-unlinked. A standalone
	// firing with exact #{hook_window} is explicit Window termination evidence;
	// the absence pass remains whole-host because the dead window no longer has
	// a queryable Registry mirror.
	controllerTriggerWindowUnlinked controllerTriggerReason = "window-unlinked"
)

// controllerTriggerReasons returns the closed reason set in declaration order.
// The generated hook bodies and the route parser share it, so a reason cannot be
// spelled in a config that the binary refuses to parse.
func controllerTriggerReasons() []controllerTriggerReason {
	return []controllerTriggerReason{
		controllerTriggerConfigApply,
		controllerTriggerRuntimeCreated,
		controllerTriggerPaneExited,
		controllerTriggerPaneKilled,
		controllerTriggerWindowUnlinked,
	}
}

func parseControllerTriggerReason(raw string) (controllerTriggerReason, error) {
	reason := controllerTriggerReason(strings.TrimSpace(raw))
	if slices.Contains(controllerTriggerReasons(), reason) {
		return reason, nil
	}
	spellings := make([]string, 0, len(controllerTriggerReasons()))
	for _, candidate := range controllerTriggerReasons() {
		spellings = append(spellings, string(candidate))
	}
	return "", fmt.Errorf("controller trigger requires one of --reason %s", strings.Join(spellings, ", "))
}

// controllerTrigger is one producer's statement that an exact server may have
// moved.
//
// It carries no claim about the resulting Registry state. Exact hook handles
// are event evidence when tmux supplies them; after-kill-pane supplies none, and
// a coalesced worker discards narrow evidence and re-derives the answer from a
// whole-host fresh observation.
type controllerTrigger struct {
	reason controllerTriggerReason
	// target is the exact server. There is no zero value and no fallback to the
	// default socket or to inherited $TMUX: a generated hook passes tmux's own
	// expanded `#{socket_path}`, and apply passes the `-L` name it reloaded.
	target explicitTmuxTarget
	// session is the hook's `#{session_id}`, which carries the create-operation
	// lease. It is empty for apply, which is not caused by a create.
	session string
	// hookPane and hookWindow are exact stable tmux handles supplied by the
	// corresponding hook. They are evidence, never selector guesses.
	hookPane   string
	hookWindow string
	// fullReobserve is set internally once this worker coalesces another event.
	// It is sticky for the rest of the worker, including its verification pass.
	fullReobserve bool
}

func (t controllerTrigger) widened() controllerTrigger {
	t.hookPane = ""
	t.hookWindow = ""
	t.fullReobserve = true
	return t
}

func (t controllerTrigger) describe() string {
	out := string(t.reason) + " socket=" + t.target.label()
	if session := strings.TrimSpace(t.session); session != "" {
		out += " session=" + session
	}
	if pane := strings.TrimSpace(t.hookPane); pane != "" {
		out += " hook-pane=" + pane
	}
	if window := strings.TrimSpace(t.hookWindow); window != "" {
		out += " hook-window=" + window
	}
	return out
}

// controllerTriggerOutcome is what one triggered invocation did. It is the
// diagnostic surface of the trigger and the assertion surface of its tests.
type controllerTriggerOutcome struct {
	reason controllerTriggerReason
	// deferred states why no pass ran, and is empty when one did. The two
	// reachable values are a live create that owns the registry lock and another
	// worker holding this server's lease.
	deferred string
	// passes counts the convergence passes this worker ran.
	passes int
	// changed counts the passes that wrote registry bytes.
	changed int
	// events counts the dirty events this worker acknowledged, including its
	// own. A value above one is coalescing: that many producers cost one worker.
	events int
	// converged reports that the last pass wrote nothing, which is the evidence
	// a repeat of this trigger would be a no-op.
	converged bool
	// unverified states why convergence could not be confirmed. It is set when
	// the pass ran but the exact-host reobservation could not be taken, which is
	// a different answer from both "converged" and "deferred": work happened and
	// nobody can say whether it was enough.
	unverified string
}

func (o controllerTriggerOutcome) describe() string {
	if o.deferred != "" {
		return string(o.reason) + " deferred: " + o.deferred
	}
	out := fmt.Sprintf("%s passes=%d changed=%d events=%d converged=%t",
		o.reason, o.passes, o.changed, o.events, o.converged)
	if o.unverified != "" {
		out += " unverified: " + o.unverified
	}
	return out
}

// controllerTriggerMaxPasses bounds the coalescing loop.
//
// A bound is required because a producer can mark faster than a pass completes,
// and an unbounded loop would then never return. Stopping early is safe in a way
// that a lost event is not: convergence is derived from the machine rather than
// from the event log, so the next producer's pass reaches the same answer this
// worker would have reached on pass nine.
const controllerTriggerMaxPasses = 8

const (
	terminationReceiptWaitTimeout = 750 * time.Millisecond
	terminationReceiptPoll        = 25 * time.Millisecond
)

// controllerTriggering is the one convergence entrypoint as its callers see it.
//
// It exists so a route can be tested for what it hands the controller -- the
// exact target, the hook session, the reason -- without also running a
// reconciliation. The production implementation is the only one; a fake here
// records the trigger and performs nothing.
type controllerTriggering interface {
	run(context.Context, controllerTrigger) (controllerTriggerOutcome, error)
}

// controllerTriggerRunner is the one convergence entrypoint.
type controllerTriggerRunner struct {
	runner tmuxCommandRunner
	store  *resourceStore
	events controllerEventLog
	// receipts is the append-only supervisor prewrite journal. It is read
	// outside the Registry transaction and absorbed before lifecycle projection.
	receipts terminationJournal
	// newReconciler is the binding-convergence seam. Tests install a scripted
	// reconciler; production builds the real one against the routed runner.
	newReconciler func(tmuxCommandRunner, sessionLister) *registryReconciler
	// observe is the fail-closed exact-host preflight. An observation that could
	// not be taken is indistinguishable from an empty one, and reading it as
	// empty would file an unknown termination against every managed Pane on a
	// machine whose tmux server simply is not up.
	observe func(explicitTmuxTarget) livePaneInventory
	// deferToCreate reports that a live canonical create owns the registry lock
	// for this hook's session.
	deferToCreate func(context.Context, explicitTmuxTarget, string) (bool, error)
	// newOperationID labels each pass's registry transaction.
	newOperationID func() (string, error)
	// pass is the convergence body. Production leaves it nil and uses converge
	// below; a test installs one so the loop's own behavior -- coalescing, the
	// repeat that proves convergence, the bound -- is observable without a
	// reconciliation in the way.
	pass      func(context.Context, controllerTrigger) (controllerPassResult, error)
	maxPasses int
	// Receipt wait tunables are test seams for the bounded kill race. Production
	// uses the constants above; beforeReceiptWait only observes that waiting has
	// begun and is nil outside deterministic tests.
	receiptWaitTimeout time.Duration
	receiptPoll        time.Duration
	beforeReceiptWait  func()
}

var _ controllerTriggering = (*controllerTriggerRunner)(nil)

func newControllerTriggerRunner(runner tmuxCommandRunner, store *resourceStore,
	newReconciler func(tmuxCommandRunner, sessionLister) *registryReconciler,
	homeDir func() (string, error), lookupEnv func(string) string) (*controllerTriggerRunner, error) {
	events, err := newControllerEventLog(homeDir, lookupEnv)
	if err != nil {
		return nil, err
	}
	receipts, err := newTerminationJournal(homeDir, lookupEnv)
	if err != nil {
		return nil, err
	}
	return &controllerTriggerRunner{
		runner:         runner,
		store:          store,
		events:         events,
		receipts:       receipts,
		newReconciler:  newReconciler,
		newOperationID: newCreateOperationID,
	}, nil
}

// run answers one trigger.
func (r *controllerTriggerRunner) run(ctx context.Context, trigger controllerTrigger) (controllerTriggerOutcome, error) {
	outcome := controllerTriggerOutcome{reason: trigger.reason}
	if r == nil {
		return outcome, errors.New("controller trigger runner is not configured")
	}
	if !slices.Contains(controllerTriggerReasons(), trigger.reason) {
		return outcome, fmt.Errorf("controller trigger carries no reason: %s", trigger.describe())
	}
	if trigger.target.flag == "" || trigger.target.value == "" {
		return outcome, errors.New("controller trigger requires an explicit tmux target")
	}
	if r.runner == nil {
		return outcome, errors.New("controller trigger requires a tmux runner")
	}
	if r.store == nil || r.store.load == nil || r.store.updateConvergent == nil || r.store.mutator == nil {
		return outcome, errors.New("controller trigger requires the resource registry store")
	}

	// The create lease is checked before the event is even recorded. A hook
	// caused by the create that already holds the registry lock has nothing to
	// converge: the create is mid-transaction and will mirror its own uids, so
	// recording an event would only guarantee a redundant pass after it commits.
	if session := strings.TrimSpace(trigger.session); session != "" {
		defers := r.deferToCreate
		if defers == nil {
			defers = func(ctx context.Context, target explicitTmuxTarget, session string) (bool, error) {
				return deferBindingConvergence(ctx, r.runner, target, session)
			}
		}
		deferred, err := defers(ctx, trigger.target, session)
		if err != nil {
			return outcome, err
		}
		if deferred {
			outcome.deferred = "a live canonical create owns this session: " + trigger.describe()
			return outcome, nil
		}
	}

	if err := r.events.mark(trigger.target, trigger.reason); err != nil {
		return outcome, err
	}
	release, acquired, err := r.events.acquire(trigger.target)
	if err != nil {
		return outcome, err
	}
	if !acquired {
		// The holder has not acknowledged the event marked above, so it will run
		// a further pass for it. This is the coalescing case and it is a success.
		outcome.deferred = "another controller worker holds this server's lease: " + trigger.describe()
		return outcome, nil
	}
	defer release()

	maxPasses := r.maxPasses
	if maxPasses <= 0 {
		maxPasses = controllerTriggerMaxPasses
	}
	passTrigger := trigger
	for range maxPasses {
		drained, err := r.events.drain(trigger.target)
		if err != nil {
			return outcome, err
		}
		outcome.events += drained
		if drained > 1 {
			passTrigger = passTrigger.widened()
		}
		body := r.pass
		var pass controllerPassResult
		if body == nil {
			pass, err = r.converge(ctx, passTrigger)
		} else {
			pass, err = body(ctx, passTrigger)
		}
		if err != nil {
			return outcome, err
		}
		outcome.passes++
		changed := pass.changed()
		if changed {
			outcome.changed++
		}
		if pass.unobserved != "" {
			// The verification stage could not read the exact host, so this
			// worker cannot claim the pass converged and must not keep looping to
			// find out: a server that is not up will not become observable
			// because we asked again. Recording why, and not claiming
			// convergence, is the whole of the honest answer here.
			outcome.unverified = pass.unobserved
			return outcome, nil
		}
		pending, err := r.events.pending(trigger.target)
		if err != nil {
			return outcome, err
		}
		if pending {
			// The pass cannot know which semantics an event arriving behind it
			// carried because drain intentionally acknowledges only before work.
			// Widen once and keep it widened through the final no-op verification.
			passTrigger = passTrigger.widened()
		}
		// Converged means this pass wrote nothing and nobody has asked for
		// another look. Either half alone is not evidence: a no-op pass with a
		// pending event has not seen what that event is about, and a pass that
		// wrote has not yet proved the write landed.
		if !changed && !pending {
			outcome.converged = true
			return outcome, nil
		}
	}
	return outcome, nil
}

// controllerPassResult is what one convergence pass did.
type controllerPassResult struct {
	// unobserved states why the pass declined to touch anything, empty when it
	// ran. It is the fail-closed outcome of an exact-host observation that could
	// not be taken.
	unobserved string
	// rebound reports whether the binding stage wrote registry bytes.
	rebound bool
	// residualExits counts lifecycle transitions the verification stage had to
	// record because the binding stage's own projection did not. It is expected
	// to be zero, and a nonzero value is the signal worth surfacing: it means the
	// two halves of one pass disagreed about what the machine holds.
	residualExits int
}

func (r controllerPassResult) changed() bool {
	return r.rebound || r.residualExits > 0
}

// converge runs one convergence pass against one exact server.
//
// Two stages, in this order, and the order is the whole reason a single
// entrypoint is worth having:
//
//  1. The binding reconciliation. It imports the live sessions it can attribute,
//     reapplies the bindings it can prove, projects the lifecycle of every
//     managed Pane whose runtime object died, and records why a Window or Pane
//     lost one -- all inside one registry transaction, against one observation
//     taken inside the lock.
//  2. The exit-half reobservation. It re-observes the same exact host and asks
//     whether any managed Pane still has a lifecycle transition left.
//
// Stage 1 contains the lifecycle projection rather than stage 2 supplying it, and
// that is not an accident of where the code lives. The projection has to run
// *after* the binding steps of the same pass: those steps are what mirror a
// Window's and a Pane's uid onto their tmux objects, so a projection taken before
// them would diff the registry against an inventory that does not yet carry the
// uids this pass is about to write -- and would offline an Agent the instant it
// was imported. Any ordering that puts an exit stage first files an unknown
// termination against every Pane the pass was on its way to binding.
//
// So stage 2 is verification, not work. Its pre-lock filter finds nothing left to
// project in the ordinary case and returns without opening a transaction, which
// is what makes it cheap enough to run on every trigger; the one tmux read it
// costs is the fail-closed check the pass needs anyway. A nonzero
// residualExits is therefore a finding rather than a routine outcome.
//
// The registry commit of stage 1 is convergent: a pass whose projected registry
// is byte-equal to the stored one writes no bytes and reports false, which is
// what makes the repeat pass in run cheap enough to serve as the whole trigger's
// reobservation.
func (r *controllerTriggerRunner) converge(ctx context.Context, trigger controllerTrigger) (controllerPassResult, error) {
	var pass controllerPassResult
	target := trigger.target
	receipts, err := r.receipts.read()
	if err != nil {
		return pass, err
	}
	if trigger.reason == controllerTriggerPaneKilled || trigger.reason == controllerTriggerWindowUnlinked || trigger.fullReobserve {
		receipts, err = r.awaitKillTerminationReceipts(ctx, trigger, receipts)
		if err != nil {
			return pass, err
		}
	}
	if !trigger.fullReobserve && (trigger.reason == controllerTriggerPaneExited || trigger.reason == controllerTriggerWindowUnlinked) {
		observe := r.observe
		if observe == nil {
			observe = func(target explicitTmuxTarget) livePaneInventory {
				return lifecycleInventory(r.runner, target)
			}
		}
		exits, err := reconcileLifecycle(ctx, lifecycleDirtyEvent{
			target:        target,
			runtimePaneID: trigger.hookPane, runtimeWindowID: trigger.hookWindow,
			receipts: receipts,
		}, observe(target), r.store)
		if err != nil {
			return pass, err
		}
		if exits.unobserved {
			pass.unobserved = exits.skipped
			return pass, nil
		}
		pass.residualExits = exits.changed()
		return pass, nil
	}
	routed := explicitTmuxRunner{runner: r.runner, target: target}
	newReconciler := r.newReconciler
	if newReconciler == nil {
		newReconciler = newRegistryReconciler
	}
	reconciler := newReconciler(routed, inttmux.NewClient(routed))
	// Hook convergence is an automatic recovery trigger. Clear exact D3
	// orphan mirrors through the same closed L8 authority cell used by public
	// reconcile before the legacy binding walk sees them; then keep D2/D3
	// import disabled in that walk.
	recovered, err := runLockedAutomaticMirrorRecovery(ctx, r.store, r.runner, target, controller.RecoveryHookConverge)
	if err != nil {
		return pass, err
	}
	reconciler.refuseForeign = true
	if err := requireAutomaticRecoveryPaths("hook-mirror-repair", "hook-status-projection"); err != nil {
		return pass, err
	}
	newOperationID := r.newOperationID
	if newOperationID == nil {
		newOperationID = newCreateOperationID
	}
	operationID, err := newOperationID()
	if err != nil {
		return pass, err
	}
	rebound, err := r.store.converge(func(working *coremetadata.Registry, mutator coremetadata.Mutator) error {
		if _, err := absorbTerminationReceipts(working, mutator, receipts); err != nil {
			return err
		}
		return reconciler.reconcile(ctx, working, mutator, operationID)
	})
	if err != nil {
		return pass, err
	}
	pass.rebound = rebound || recovered > 0

	observe := r.observe
	if observe == nil {
		observe = func(target explicitTmuxTarget) livePaneInventory {
			return lifecycleInventory(r.runner, target)
		}
	}
	exits, err := reconcileLifecycle(ctx, lifecycleDirtyEvent{
		target: target, runtimeWindowID: trigger.hookWindow, receipts: receipts,
	}, observe(target), r.store)
	if err != nil {
		return pass, err
	}
	if exits.unobserved {
		pass.unobserved = exits.skipped
		return pass, nil
	}
	pass.residualExits = exits.changed()
	return pass, nil
}

// runLockedAutomaticMirrorRecovery is the production L8 boundary. Runtime is
// observed and classified while the Registry lock protects the exact Registry
// bytes used by the classifier. The callback never changes those bytes, so the
// convergent store remains a Registry-write no-op.
func runLockedAutomaticMirrorRecovery(ctx context.Context, store *resourceStore, runner tmuxCommandRunner,
	target explicitTmuxTarget, trigger controller.RecoveryTrigger) (int, error) {
	if store == nil || store.updateConvergent == nil {
		return 0, errors.New("automatic recovery write store is not configured")
	}
	recovered := 0
	_, _, err := store.updateConvergent(func(working *coremetadata.Registry) error {
		before := working.Clone()
		var recoveryErr error
		recovered, recoveryErr = runAutomaticMirrorRecovery(ctx, runner, target, working.Clone(), trigger)
		if recoveryErr != nil {
			return recoveryErr
		}
		return verifyAutomaticRecoveryRegistryUnchanged(before, *working)
	})
	if err != nil {
		return 0, err
	}
	return recovered, nil
}

func verifyAutomaticRecoveryRegistryUnchanged(before, after coremetadata.Registry) error {
	if !reflect.DeepEqual(before, after) {
		return errors.New("automatic L8 changed Registry bytes")
	}
	return nil
}

func runAutomaticMirrorRecovery(ctx context.Context, runner tmuxCommandRunner, target explicitTmuxTarget,
	registry coremetadata.Registry, trigger controller.RecoveryTrigger) (int, error) {
	if !trigger.Valid() {
		return 0, fmt.Errorf("automatic recovery trigger %q is outside the closed authority table", trigger)
	}
	pathName := map[controller.RecoveryTrigger]string{
		controller.RecoveryHookConverge: "hook-orphan-mirror-discard",
		controller.RecoveryExplicit:     "explicit-orphan-mirror-discard",
		controller.RecoveryProjectOpen:  "project-open-orphan-mirror-discard",
	}[trigger]
	if err := requireAutomaticRecoveryPaths(pathName); err != nil {
		return 0, err
	}
	transport := controllerTransport(target)
	inventory := intmetadata.NewInventoryObserver(runner, transport).Observe(ctx)
	graph := resourcegraph.Resolve(registry, inventory)
	candidates := controllerRecoveryCandidates(graph, trigger)
	if len(candidates) == 0 {
		return 0, nil
	}
	handles := controller.IndexHandles(graph)
	actions, _ := controller.Authorize(handles, controllerGuardFields, controller.Grant{}, candidates)
	plan := controller.NewPlan(graph.Transport, graph.HostMode, actions, nil)
	for _, action := range plan.Refusals() {
		return 0, fmt.Errorf("automatic recovery refused %s: %s", action.Key, action.Reason)
	}
	kernel := &resourceControllerKernel{target: target, runner: runner}
	if err := kernel.guardPlan(ctx, "", plan.Writes()); err != nil {
		return 0, err
	}
	routed := explicitTmuxRunner{runner: runner, target: target}
	for _, action := range plan.Writes() {
		if _, err := routed.Run(ctx, "tmux", action.Args...); err != nil {
			return 0, fmt.Errorf("execute automatic recovery %s: %w", action.Key, err)
		}
	}
	return len(graph.RuntimeOfClass(resourcegraph.ClassRecoverable)), nil
}

// awaitKillTerminationReceipts closes the tmux kill/reap race without taking
// the Registry lock. after-kill-pane and window-unlinked can run before the
// surviving supervisor has reaped SIGHUP and appended its receipt; projecting
// unknown immediately would then settle the event with no later wakeup. Only a
// newly absent managed activation with no applicable supervisor/control receipt
// waits, and the wait is bounded. Voluntary exact pane-exited never enters here.
func (r *controllerTriggerRunner) awaitKillTerminationReceipts(ctx context.Context, trigger controllerTrigger,
	receipts []coremetadata.TerminationEvidence) ([]coremetadata.TerminationEvidence, error) {
	observe := r.observe
	if observe == nil {
		observe = func(target explicitTmuxTarget) livePaneInventory {
			return lifecycleInventory(r.runner, target)
		}
	}
	live, err := observe(trigger.target).LivePaneUIDs(ctx)
	if err != nil {
		// The ordinary convergence pass owns fail-closed reporting. An unreadable
		// host is not evidence of an absent activation and therefore never waits.
		return receipts, nil
	}
	registry, err := r.store.load()
	if err != nil {
		return nil, MapMetadataError(err)
	}
	needsWait := func(snapshot []coremetadata.TerminationEvidence) bool {
		candidate := registry.Clone()
		_, _ = absorbTerminationReceipts(&candidate, r.store.mutator(), snapshot)
		for i := range candidate.Panes {
			pane := &candidate.Panes[i]
			if live[pane.Metadata.UID] || strings.TrimSpace(pane.Status.Activation.Generation) == "" {
				continue
			}
			stored := pane.Status.LastTermination
			if stored != nil && stored.Generation == pane.Status.Activation.Generation {
				if stored.Source == coremetadata.TerminationSourceSupervisor ||
					stored.Classification == coremetadata.TerminationIntentional {
					continue
				}
				// A racing runtime-created pass may already have settled the
				// absence as reconcile/unknown. On a kill trigger that is still a
				// provisional answer worth the same bounded supervisor wait.
				if stored.Source == coremetadata.TerminationSourceReconcile &&
					stored.Classification == coremetadata.TerminationUnknown {
					return true
				}
			}
			if coremetadata.NeedsTerminationProjection(candidate, pane.Metadata.UID) {
				return true
			}
		}
		return false
	}
	if !needsWait(receipts) {
		return receipts, nil
	}
	if r.beforeReceiptWait != nil {
		r.beforeReceiptWait()
	}
	timeout := r.receiptWaitTimeout
	if timeout <= 0 {
		timeout = terminationReceiptWaitTimeout
	}
	poll := r.receiptPoll
	if poll <= 0 {
		poll = terminationReceiptPoll
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return receipts, ctx.Err()
		case <-ticker.C:
			fresh, readErr := r.receipts.read()
			if readErr != nil {
				return nil, readErr
			}
			receipts = fresh
			if !needsWait(receipts) {
				return receipts, nil
			}
		case <-timer.C:
			// One final lock-free read closes the boundary with an append racing
			// the timeout tick; absence after this remains bounded unknown.
			fresh, readErr := r.receipts.read()
			if readErr != nil {
				return nil, readErr
			}
			return fresh, nil
		}
	}
}

// controllerTriggerReasonSpellings renders the closed reason set for a help
// line. The route parser and the generated hook bodies both read the same set,
// so a reason a config can emit is always a reason the binary accepts.
func controllerTriggerReasonSpellings() []string {
	out := make([]string, 0, len(controllerTriggerReasons()))
	for _, reason := range controllerTriggerReasons() {
		out = append(out, string(reason))
	}
	return out
}
