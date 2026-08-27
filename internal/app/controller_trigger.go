package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/controller"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/pins"
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
	// #{hook_pane}; its owner Window's exact live $N/@N binding is persisted by
	// observation because current-context hook formats can name a survivor.
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
	// session is a create hook's current `#{session_id}` or window-unlinked's
	// exact `#{hook_session}`. It is empty for apply and pane-exited.
	session string
	// hookPane and hookWindow are exact stable tmux handles supplied by the
	// corresponding hook. They are evidence, never selector guesses.
	hookPane   string
	hookWindow string
	// retry counts bounded event-log replays: either an unlink that arrived
	// before its causal pane-exited half or a convergence pass that returned an
	// error. It is controller transport state only and never becomes Registry
	// teardown evidence.
	retry int
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
	// changed counts passes that executed Registry or tmux convergence writes.
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
	// refused is an exact declarative-plan reason. A refusal performs zero
	// writes and is neither convergence nor an execution error.
	refused string
	// retryReason records the exact typed lifecycle reason that forced a
	// bounded in-worker retry before convergence. It survives the successful
	// final pass so a manual invocation can distinguish clean first-attempt
	// convergence from recovery of a transient runtime cleanup failure.
	retryReason coremetadata.TeardownReason
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
	if o.refused != "" {
		out += " refused: " + o.refused
	}
	if o.retryReason != "" {
		out += " retry: " + string(o.retryReason)
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

// controllerTriggerMaxRetries bounds replay of one event whose convergence
// body returns an error. The event remains durable after the bound so a later
// producer can acquire the lease and retry it; the current producer still gets
// the terminal error instead of a false convergence claim.
const controllerTriggerMaxRetries = 3

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
	pins     pinSetStore
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
	// beforeLeaseRelease is a focused handoff-race seam. Production leaves it
	// nil; tests mark an event after the holder's final pending check but before
	// unlock to prove that producer cannot be stranded behind the old lease.
	beforeLeaseRelease func()
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
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return nil, fmt.Errorf("resolve lifecycle pin store: %w", err)
	}
	return &controllerTriggerRunner{
		runner:         runner,
		store:          store,
		events:         events,
		receipts:       receipts,
		pins:           pins.NewDefaultStore(paths),
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
	if session := strings.TrimSpace(trigger.session); session != "" && trigger.reason == controllerTriggerRuntimeCreated {
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

	if err := r.events.mark(trigger); err != nil {
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
	defer func() {
		if release != nil {
			release()
		}
	}()

	maxPasses := r.maxPasses
	if maxPasses <= 0 {
		maxPasses = controllerTriggerMaxPasses
	}
	var carriedUnlinks []controllerTrigger
	for range maxPasses {
		drained, err := r.events.drain(trigger.target)
		if err != nil {
			return outcome, err
		}
		outcome.events += len(drained)
		drained = append(drained, carriedUnlinks...)
		carriedUnlinks = nil
		sortControllerTriggers(drained)
		batchChanged := false
		sawPaneExit := false
		passTriggers := controllerPassTriggers(drained, trigger)
		retryQueued := false
		for passIndex, passTrigger := range passTriggers {
			body := r.pass
			var pass controllerPassResult
			if body == nil {
				pass, err = r.converge(ctx, passTrigger)
			} else {
				pass, err = body(ctx, passTrigger)
			}
			outcome.passes++
			if err != nil {
				var cleanupRetry *lifecycleCleanupRetryError
				if errors.As(err, &cleanupRetry) && passTrigger.reason == controllerTriggerPaneExited &&
					!passTrigger.fullReobserve {
					outcome.retryReason = cleanupRetry.Reason
				}
				canRetry := passTrigger.retry < controllerTriggerMaxRetries
				if canRetry {
					passTrigger.retry++
				}
				// drain acknowledges before a pass so a concurrent producer cannot
				// be lost. Restore the failing pass and every pass this batch has not
				// reached before either retrying or returning: otherwise an ordinary
				// Registry-lock race turns one hook into a permanently dead Pane.
				requeue := append([]controllerTrigger{passTrigger}, passTriggers[passIndex+1:]...)
				for _, pendingTrigger := range requeue {
					if markErr := r.events.mark(pendingTrigger); markErr != nil {
						return outcome, fmt.Errorf("%w; requeue controller event: %v", err, markErr)
					}
				}
				if canRetry {
					retryQueued = true
					break
				}
				return outcome, err
			}
			if pass.refused != "" {
				outcome.refused = pass.refused
				return outcome, nil
			}
			if passTrigger.reason == controllerTriggerPaneExited {
				sawPaneExit = true
			}
			if passTrigger.reason == controllerTriggerWindowUnlinked && !passTrigger.fullReobserve && pass.awaitingPaneExit && !sawPaneExit && passTrigger.retry < controllerTriggerMaxRetries {
				passTrigger.retry++
				carriedUnlinks = append(carriedUnlinks, passTrigger)
			}
			if pass.changed() {
				batchChanged = true
				outcome.changed++
			}
			if pass.unobserved == "" {
				continue
			}
			// The verification stage could not read the exact host, so this
			// worker cannot claim the pass converged and must not keep looping to
			// find out: a server that is not up will not become observable
			// because we asked again. Recording why, and not claiming
			// convergence, is the whole of the honest answer here.
			outcome.unverified = pass.unobserved
			return outcome, nil
		}
		if retryQueued {
			continue
		}
		pending, err := r.events.pending(trigger.target)
		if err != nil {
			return outcome, err
		}
		if len(carriedUnlinks) != 0 {
			if pending {
				continue
			}
			for _, unlink := range carriedUnlinks {
				if err := r.events.mark(unlink); err != nil {
					return outcome, err
				}
			}
			outcome.deferred = "window-unlinked is awaiting its causal pane-exited event: " + carriedUnlinks[0].describe()
			return outcome, nil
		}
		// Converged means this pass wrote nothing and nobody has asked for
		// another look. Either half alone is not evidence: a no-op pass with a
		// pending event has not seen what that event is about, and a pass that
		// wrote has not yet proved the write landed.
		if !batchChanged && !pending {
			// Close the only lost-wakeup gap in mark-before-acquire coalescing:
			// a producer may publish after the pending check above, then lose its
			// non-blocking acquire while this holder still owns the lease. Release
			// first and check the durable queue again. A later producer observes no
			// holder and becomes the worker; an earlier loser is visible here.
			if r.beforeLeaseRelease != nil {
				r.beforeLeaseRelease()
			}
			release()
			release = nil
			pending, err = r.events.pending(trigger.target)
			if err != nil {
				return outcome, err
			}
			if !pending {
				outcome.converged = true
				return outcome, nil
			}
			var reacquired bool
			release, reacquired, err = r.events.acquire(trigger.target)
			if err != nil {
				return outcome, err
			}
			if !reacquired {
				// Another producer won the handoff and owns the queued event.
				outcome.deferred = "another controller worker acquired the release handoff: " + trigger.describe()
				return outcome, nil
			}
			continue
		}
	}
	return outcome, nil
}

func controllerPassTriggers(events []controllerTrigger, fallback controllerTrigger) []controllerTrigger {
	if len(events) == 0 {
		return []controllerTrigger{fallback.widened()}
	}
	out := make([]controllerTrigger, 0, len(events)+1)
	generic := false
	genericRetry := fallback.retry
	for _, event := range events {
		if event.reason == controllerTriggerPaneExited || event.reason == controllerTriggerWindowUnlinked {
			out = append(out, event)
			continue
		}
		generic = true
		genericRetry = max(genericRetry, event.retry)
	}
	if generic || len(out) == 0 {
		widened := fallback.widened()
		widened.retry = genericRetry
		out = append(out, widened)
	}
	return out
}

func sortControllerTriggers(events []controllerTrigger) {
	sort.SliceStable(events, func(i, j int) bool {
		return controllerEventPriority(events[i].reason) < controllerEventPriority(events[j].reason)
	})
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
	// controlRoot reports that config apply converged the declared Home control
	// target before generic binding reconciliation.
	controlRoot      bool
	awaitingPaneExit bool
	refused          string
}

func (r controllerPassResult) changed() bool {
	return r.rebound || r.residualExits > 0 || r.controlRoot
}

// converge runs one convergence pass against one exact server.
//
// Three stages, in this order, and the order is the whole reason a single
// entrypoint is worth having:
//
//  1. Control-target convergence. An exact config declaration may create the
//     Home identity; lifecycle triggers may repair only identities the Registry
//     already knows. A contradictory claimant refuses the entire pass before
//     generic reconciliation can write.
//  2. The binding reconciliation. It imports the live sessions it can attribute,
//     reapplies the bindings it can prove, projects the lifecycle of every
//     managed Pane whose runtime object died, and records why a Window or Pane
//     lost one -- all inside one registry transaction, against one observation
//     taken inside the lock.
//  3. The exit-half reobservation. It re-observes the same exact host and asks
//     whether any managed Pane still has a lifecycle transition left.
//
// Stage 2 contains the lifecycle projection rather than stage 3 supplying it, and
// that is not an accident of where the code lives. The projection has to run
// *after* the binding steps of the same pass: those steps are what mirror a
// Window's and a Pane's uid onto their tmux objects, so a projection taken before
// them would diff the registry against an inventory that does not yet carry the
// uids this pass is about to write -- and would offline an Agent the instant it
// was imported. Any ordering that puts an exit stage first files an unknown
// termination against every Pane the pass was on its way to binding.
//
// So stage 3 is verification, not work. Its pre-lock filter finds nothing left to
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
	if r.runner != nil {
		control, err := r.convergeControlTargets(ctx, target, trigger.reason == controllerTriggerConfigApply)
		if err != nil {
			return pass, err
		}
		if control.skipped != "" {
			pass.refused = string(control.skipped)
			return pass, nil
		}
		pass.controlRoot = control.changed
	}
	receipts, err := r.receipts.read()
	if err != nil {
		return pass, err
	}
	if trigger.reason == controllerTriggerPaneExited || trigger.reason == controllerTriggerPaneKilled ||
		trigger.reason == controllerTriggerWindowUnlinked || trigger.fullReobserve {
		receipts, err = r.awaitRuntimeExitTerminationReceipts(ctx, trigger, receipts)
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
		dirty := lifecycleDirtyEvent{
			target:           target,
			runtimeSessionID: trigger.session,
			runtimePaneID:    trigger.hookPane, runtimeWindowID: trigger.hookWindow,
			receipts: receipts, pinStore: r.pins,
		}
		if trigger.reason == controllerTriggerPaneExited {
			dirty.teardownKind = coremetadata.TeardownEventPaneExited
		} else {
			dirty.teardownKind = coremetadata.TeardownEventWindowUnlinked
		}
		exits, err := reconcileLifecycle(ctx, dirty, observe(target), r.store)
		if err != nil {
			return pass, err
		}
		if exits.unobserved {
			pass.unobserved = exits.skipped
			return pass, nil
		}
		pass.residualExits = exits.changed()
		pass.awaitingPaneExit = exits.awaitingPaneExit
		return pass, nil
	}
	route, err := resolveControllerRuntimeMutationRoute(ctx, r.runner, target, func(string) string { return "" })
	if err != nil {
		return pass, err
	}
	routed := explicitTmuxRunner{runner: r.runner, target: explicitTmuxTarget{flag: "-S", value: route.expectedSocketPath}}
	newReconciler := r.newReconciler
	if newReconciler == nil {
		reconciler := newRegistryReconcilerWithRoute(routed, inttmux.NewClient(routed), route)
		newReconciler = func(tmuxCommandRunner, sessionLister) *registryReconciler { return reconciler }
	}
	reconciler := newReconciler(routed, inttmux.NewClient(routed))
	// Hook convergence is an automatic recovery trigger. Clear exact D3
	// orphan mirrors through the same closed L8 authority cell used by public
	// reconcile before the legacy binding walk sees them; then keep D2/D3
	// import disabled in that walk.
	recovered, err := runLockedAutomaticMirrorRecovery(ctx, r.store, r.runner, target, controller.RecoveryHookConverge, route)
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

// convergeControlTargets continuously repairs every live exact
// ControlSession identity already present in the Registry. Config apply adds
// the canonical Home declaration, which is the one authority allowed to mint a
// missing root; hook triggers may only continue identities the Registry already
// records.
func (r *controllerTriggerRunner) convergeControlTargets(ctx context.Context, target explicitTmuxTarget, declareHome bool) (controlSessionConvergence, error) {
	routed := explicitTmuxRunner{runner: r.runner, target: target}
	out, err := routed.Run(ctx, "tmux", "list-sessions", "-F", "#{session_name}")
	if err != nil {
		if tmuxSessionAbsent(err) || tmuxServerUnreachable(err) {
			return controlSessionConvergence{}, nil
		}
		return controlSessionConvergence{}, fmt.Errorf("observe declared control target %q: %w", defaultAppSession, err)
	}
	live := map[string]bool{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if name := strings.TrimSpace(line); name != "" {
			live[name] = true
		}
	}
	registry, err := r.store.load()
	if err != nil {
		return controlSessionConvergence{}, err
	}
	declared := map[string]bool{}
	if declareHome {
		declared[defaultAppSession] = true
	}
	sessions := make([]string, 0, len(registry.ControlSessions)+1)
	for _, record := range registryResourceRecords(registry) {
		control, ok := record.value.(coremetadata.ControlSession)
		if !ok {
			continue
		}
		sessions = append(sessions, control.Spec.Session)
	}
	if declareHome {
		sessions = append(sessions, defaultAppSession)
	}
	slices.Sort(sessions)
	sessions = slices.Compact(sessions)
	converger := newControlSessionConverger(r.runner, "")
	converger.resources = r.store
	var aggregate controlSessionConvergence
	for _, session := range sessions {
		if !live[session] {
			continue
		}
		result, convergeErr := converger.convergeTargetWithEvidence(ctx, target, session, declared[session])
		if convergeErr != nil {
			return aggregate, convergeErr
		}
		if result.skipped != "" {
			return result, nil
		}
		aggregate.changed = aggregate.changed || result.changed
		aggregate.writes += result.writes
		aggregate.windows += result.windows
		aggregate.panes += result.panes
	}
	return aggregate, nil
}

// runLockedAutomaticMirrorRecovery is the production L8 boundary. Runtime is
// observed and classified while the Registry lock protects the exact Registry
// bytes used by the classifier. The callback never changes those bytes, so the
// convergent store remains a Registry-write no-op.
func runLockedAutomaticMirrorRecovery(ctx context.Context, store *resourceStore, runner tmuxCommandRunner,
	target explicitTmuxTarget, trigger controller.RecoveryTrigger, routeHint ...runtimeMutationRoute) (int, error) {
	if store == nil || store.updateConvergent == nil {
		return 0, errors.New("automatic recovery write store is not configured")
	}
	recovered := 0
	_, _, err := store.updateConvergent(func(working *coremetadata.Registry) error {
		before := working.Clone()
		var recoveryErr error
		recovered, recoveryErr = runAutomaticMirrorRecovery(ctx, runner, target, working.Clone(), trigger, routeHint...)
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
	registry coremetadata.Registry, trigger controller.RecoveryTrigger, routeHint ...runtimeMutationRoute) (int, error) {
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
	var route runtimeMutationRoute
	if len(routeHint) > 0 {
		route = routeHint[0]
		if err := guardResolvedRuntimeMutationRoute(ctx, runner, route); err != nil {
			return 0, err
		}
	} else {
		var err error
		route, err = resolveControllerRuntimeMutationRoute(ctx, runner, target, func(string) string { return "" })
		if err != nil {
			return 0, err
		}
	}
	exactTarget := explicitTmuxTarget{flag: "-S", value: route.expectedSocketPath}
	inventory := intmetadata.NewInventoryObserver(runner, controllerTransport(exactTarget)).Observe(ctx)
	inventory.Transport = controllerTransport(target)
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
	kernel := &resourceControllerKernel{target: target, runner: runner, route: &route}
	if err := kernel.guardPlan(ctx, "", plan.Writes()); err != nil {
		return 0, err
	}
	if err := executeControllerRuntimeMutations(ctx, runner, route, plan.Writes()); err != nil {
		return 0, fmt.Errorf("execute automatic recovery: %w", err)
	}
	return len(graph.RuntimeOfClass(resourcegraph.ClassRecoverable)), nil
}

// awaitRuntimeExitTerminationReceipts closes the tmux hook/reap race without
// taking the Registry lock. pane-exited can run before a normal wait status is
// journaled; kill and unlink hooks can likewise precede the surviving
// supervisor's SIGHUP receipt. An exact pane-exited wait is restricted to its
// one stored activation handle. Wider kill/unlink observations retain their
// existing whole-host behavior. Every wait is bounded, and a receipt-free
// timeout still projects honest unknown rather than inventing normal authority.
func (r *controllerTriggerRunner) awaitRuntimeExitTerminationReceipts(ctx context.Context, trigger controllerTrigger,
	receipts []coremetadata.TerminationEvidence) ([]coremetadata.TerminationEvidence, error) {
	observe := r.observe
	if observe == nil {
		observe = func(target explicitTmuxTarget) livePaneInventory {
			return lifecycleInventory(r.runner, target)
		}
	}
	inventory := observe(trigger.target)
	live, err := inventory.LivePaneUIDs(ctx)
	if err != nil {
		// The ordinary convergence pass owns fail-closed reporting. An unreadable
		// host is not evidence of an absent activation and therefore never waits.
		return receipts, nil
	}
	dead := map[string]bool{}
	if retained, ok := inventory.(lifecycleDeadPaneInventory); ok {
		observed, observeErr := retained.DeadPaneObservations(ctx)
		dead, err = lifecycleDeadPaneUIDs(observed), observeErr
		if err != nil {
			// As with an unreadable live inventory, a failed dead-state query is
			// not evidence that a mirrored Pane is still executing. The ordinary
			// convergence pass owns the fail-closed diagnostic.
			return receipts, nil
		}
	} else if retained, ok := inventory.(lifecycleLegacyDeadPaneInventory); ok {
		dead, err = retained.DeadPaneUIDs(ctx)
		if err != nil {
			return receipts, nil
		}
	}
	effectiveLive := lifecycleEffectiveLivePanes(live, dead)
	registry, err := r.store.load()
	if err != nil {
		return nil, MapMetadataError(err)
	}
	needsWait := func(snapshot []coremetadata.TerminationEvidence) bool {
		candidate := registry.Clone()
		_, _ = absorbTerminationReceipts(&candidate, r.store.mutator(), snapshot)
		for i := range candidate.Panes {
			pane := &candidate.Panes[i]
			if trigger.reason == controllerTriggerPaneExited &&
				pane.Status.Activation.RuntimeID != strings.TrimSpace(trigger.hookPane) {
				continue
			}
			if effectiveLive[pane.Metadata.UID] || strings.TrimSpace(pane.Status.Activation.Generation) == "" {
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
