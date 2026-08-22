package app

import (
	"context"
	"errors"
	"strings"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// The exit reconciliation seam: one dirty event in, one lifecycle transition
// out.
//
// A lifecycle producer -- a pane-exit hook, a completed command, a periodic
// pass -- does not decide anything. It states that something on one exact tmux
// server may have changed and, when it can, which Pane and which materialization
// of it. Everything else is re-derived here against a *fresh* observation of
// that same server, which is what makes the event advisory rather than
// authoritative: a stale event, a duplicate event, and an event for a pane that
// has since come back all converge on the same state as no event at all.
//
// This is a one-shot. It observes, decides, writes at most once, and returns.
// There is no daemon, no queue, and no retained state between invocations.

// lifecycleDirtyEvent is one exact-host statement that a managed runtime
// object's lifecycle may have changed.
//
// Every field except the target is a *narrowing*, never a claim. The zero value
// is a legal event meaning "something on this host changed, re-derive the whole
// registry against it", which is exactly what a hook that cannot name the pane
// that died has to say: tmux fires `after-kill-pane` with an empty
// `#{hook_pane}`, so a producer that pretended to know would be inventing the
// one field that matters.
type lifecycleDirtyEvent struct {
	// target is the exact tmux server to re-observe. The zero value routes
	// through the inherited client, which is the absolute socket in $TMUX; a
	// non-zero value is an explicit -L/-S and addresses that server only.
	target explicitTmuxTarget
	// paneUID narrows the event to one Pane. Empty means the whole host.
	paneUID string
	// runtimePaneID is the exact #{hook_pane} supplied by pane-exited. It is
	// matched only against the activation handle projmux stored when that
	// generation was materialized; no name, cwd, or pane order participates.
	runtimePaneID string
	// runtimeWindowID is the exact event Window handle. pane-exited supplies its
	// window-scoped #{window_id} because tmux leaves #{hook_window} empty there;
	// window-unlinked supplies #{hook_window}. It is never inferred from a
	// missing Pane after the event.
	runtimeWindowID string
	// generation narrows the event to one materialization of that Pane. A
	// generation the Pane no longer holds projects nothing, so a receipt or an
	// absence belonging to a process a resume has already replaced cannot move
	// the current binding.
	generation string
	// teardownKind is set only for an exact topology event that may carry
	// automatic delete authority. Whole-host inventory diffs, pane-killed, and
	// window-unlinked keep the zero value and can only project retained state.
	teardownKind coremetadata.TeardownEventKind
	// receipts is the lock-free supervisor prewrite snapshot to absorb inside
	// the same Registry transaction, before absence projection consumes it.
	receipts []coremetadata.TerminationEvidence
}

// describe renders the event for a diagnostic line.
func (e lifecycleDirtyEvent) describe() string {
	out := "socket=" + e.target.label()
	if pane := strings.TrimSpace(e.paneUID); pane != "" {
		out += " pane=" + pane
	}
	if pane := strings.TrimSpace(e.runtimePaneID); pane != "" {
		out += " hook-pane=" + pane
	}
	if window := strings.TrimSpace(e.runtimeWindowID); window != "" {
		out += " hook-window=" + window
	}
	if generation := strings.TrimSpace(e.generation); generation != "" {
		out += " generation=" + generation
	}
	return out
}

// lifecycleReconcileResult is what one reconciliation did.
//
// It counts transactions separately from projections because the two answer
// different questions. Projections say what the registry now records; the
// transaction count is the cost budget, and the property worth pinning is that a
// repeat reconciliation of an already-reconciled disappearance opens zero.
type lifecycleReconcileResult struct {
	projected    []coremetadata.TerminationProjection
	cascaded     []coremetadata.PaneAgentCascadeDeletePlan
	transactions int
	// receiptsChanged reports journal evidence absorbed independently of a
	// lifecycle projection. A late supervisor refinement is a real write and
	// therefore requires the controller's following no-op verification pass.
	receiptsChanged bool
	// skipped states why the pass declined to do anything, empty when it ran.
	skipped string
	// unobserved separates the one skip a caller must not read as convergence
	// from the two it may. "Nothing left to reconcile" is a converged answer; an
	// exact-host observation that could not be taken is not an answer at all, and
	// a trigger that continued past it would rewrite the registry from a machine
	// nobody could see.
	unobserved bool
}

// changed counts the projections that altered the registry.
func (r lifecycleReconcileResult) changed() int {
	count := 0
	for _, projection := range r.projected {
		if projection.Changed {
			count++
		}
	}
	if r.receiptsChanged {
		count++
	}
	count += len(r.cascaded)
	return count
}

func paneWindowUID(registry coremetadata.Registry, pane coremetadata.Pane) (string, bool) {
	owner := pane.Metadata.OwnerRef
	if owner == nil {
		return "", false
	}
	if owner.Kind == coremetadata.KindWindow {
		_, ok := registry.Window(owner.UID)
		return owner.UID, ok
	}
	if owner.Kind != coremetadata.KindAgent {
		return "", false
	}
	agent, ok := registry.Agent(owner.UID)
	if !ok || agent.Metadata.OwnerRef == nil || agent.Metadata.OwnerRef.Kind != coremetadata.KindWindow {
		return "", false
	}
	_, ok = registry.Window(agent.Metadata.OwnerRef.UID)
	return agent.Metadata.OwnerRef.UID, ok
}

func exactJournalReceipt(receipts []coremetadata.TerminationEvidence, pane coremetadata.Pane) bool {
	stored := pane.Status.LastTermination
	if stored == nil || stored.Source != coremetadata.TerminationSourceSupervisor ||
		stored.Classification != coremetadata.TerminationNormal || stored.Generation != pane.Status.Activation.Generation {
		return false
	}
	for i := range receipts {
		receipt := receipts[i]
		if receipt.Source == stored.Source && receipt.Classification == stored.Classification &&
			receipt.PaneUID == pane.Metadata.UID && receipt.AgentUID == stored.AgentUID &&
			receipt.Generation == stored.Generation && receipt.Signal == stored.Signal &&
			receipt.OperationID == stored.OperationID && equalOptionalInt(receipt.ExitCode, stored.ExitCode) {
			return true
		}
	}
	return false
}

func equalOptionalInt(left, right *int) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// planExactPaneExitCascade converts only a positively observed exact
// pane-exited event into the Phase 2 Pane/Agent desired plan. The supervisor
// journal match is intentionally part of authority: after the resource rows
// are deleted that bounded receipt is the durable diagnostic evidence of the
// clean outcome.
func planExactPaneExitCascade(
	registry coremetadata.Registry,
	live map[string]bool,
	observedWindowUID string,
	event lifecycleDirtyEvent,
	mutator coremetadata.Mutator,
) (coremetadata.PaneAgentCascadeDeletePlan, error) {
	if event.teardownKind != coremetadata.TeardownEventPaneExited ||
		strings.TrimSpace(event.runtimePaneID) == "" || strings.TrimSpace(event.runtimeWindowID) == "" ||
		event.target.flag == "" || event.target.value == "" {
		return coremetadata.PaneAgentCascadeDeletePlan{}, nil
	}
	var pane *coremetadata.Pane
	for i := range registry.Panes {
		candidate := &registry.Panes[i]
		if candidate.Status.Activation.RuntimeID != strings.TrimSpace(event.runtimePaneID) {
			continue
		}
		if pane != nil {
			return coremetadata.PaneAgentCascadeDeletePlan{Decision: coremetadata.TeardownDecision{
				Action: coremetadata.TeardownRefuse, Reason: coremetadata.TeardownReasonConflictingOwnerFacts,
			}}, nil
		}
		pane = candidate
	}
	if pane == nil || live[pane.Metadata.UID] {
		return coremetadata.PaneAgentCascadeDeletePlan{}, nil
	}
	windowUID, ok := paneWindowUID(registry, *pane)
	if !ok {
		return coremetadata.PaneAgentCascadeDeletePlan{Decision: coremetadata.TeardownDecision{
			Action: coremetadata.TeardownRefuse, Reason: coremetadata.TeardownReasonStaleOwnerBinding,
		}}, nil
	}
	window, _ := registry.Window(windowUID)
	if strings.TrimSpace(observedWindowUID) == "" || strings.TrimSpace(observedWindowUID) != windowUID {
		return coremetadata.PaneAgentCascadeDeletePlan{Decision: coremetadata.TeardownDecision{
			Action: coremetadata.TeardownRefuse, Reason: coremetadata.TeardownReasonForeignHost,
		}}, nil
	}
	root := window.Metadata.OwnerRef
	if root == nil || (root.Kind != coremetadata.KindProject && root.Kind != coremetadata.KindControlSession) {
		return coremetadata.PaneAgentCascadeDeletePlan{Decision: coremetadata.TeardownDecision{
			Action: coremetadata.TeardownRefuse, Reason: coremetadata.TeardownReasonStaleOwnerBinding,
		}}, nil
	}

	classification := coremetadata.TerminationUnknown
	if stored := pane.Status.LastTermination; stored != nil &&
		stored.Generation == pane.Status.Activation.Generation &&
		coremetadata.ValidTerminationClassification(stored.Classification) {
		classification = stored.Classification
	}
	observation := coremetadata.TeardownObservationExactSocket
	if len(live) == 0 {
		observation = coremetadata.TeardownObservationEmpty
	}
	liveSiblingPane := false
	for i := range registry.Panes {
		sibling := registry.Panes[i]
		if sibling.Metadata.UID == pane.Metadata.UID || !live[sibling.Metadata.UID] {
			continue
		}
		if siblingWindow, exists := paneWindowUID(registry, sibling); exists && siblingWindow == windowUID {
			liveSiblingPane = true
			break
		}
	}
	liveSiblingRootWindow := false
	for _, siblingWindow := range registry.WindowsOf(root.UID) {
		if siblingWindow.Metadata.UID == windowUID {
			continue
		}
		for i := range registry.Panes {
			candidate := registry.Panes[i]
			if candidateWindow, exists := paneWindowUID(registry, candidate); exists &&
				candidateWindow == siblingWindow.Metadata.UID && live[candidate.Metadata.UID] {
				liveSiblingRootWindow = true
				break
			}
		}
		if liveSiblingRootWindow {
			break
		}
	}
	teardown := coremetadata.TeardownEvent{
		Kind: event.teardownKind, Classification: classification,
		Generation: coremetadata.TeardownGenerationCurrent, Observation: observation,
		Chain: coremetadata.TeardownOwnerChain{
			SocketIdentity: event.target.label(), PaneHandle: strings.TrimSpace(event.runtimePaneID),
			WindowHandle: strings.TrimSpace(event.runtimeWindowID), PaneUID: pane.Metadata.UID,
			WindowUID: windowUID, RootKind: root.Kind, RootUID: root.UID,
			Generation: pane.Status.Activation.Generation,
		},
		LiveSiblingPane: liveSiblingPane, LiveSiblingRootWindow: liveSiblingRootWindow,
	}
	decision := coremetadata.DecideTeardownEvent(teardown)
	if decision.Action == coremetadata.TeardownDeletePaneAgent && !exactJournalReceipt(event.receipts, *pane) {
		decision.Action = coremetadata.TeardownRefuse
		decision.Reason = coremetadata.TeardownReasonUnavailable
		return coremetadata.PaneAgentCascadeDeletePlan{Decision: decision}, nil
	}
	now := time.Now
	if mutator.Now != nil {
		now = mutator.Now
	}
	return coremetadata.PlanPaneAgentCascadeDelete(registry, teardown, now().UTC())
}

type lifecycleWindowOwnerInventory interface {
	ResolveWindowUID(context.Context, string) (string, error)
}

func lifecycleObservedWindowUID(ctx context.Context, inventory livePaneInventory, event lifecycleDirtyEvent) (string, error) {
	if event.teardownKind != coremetadata.TeardownEventPaneExited {
		return "", nil
	}
	owners, ok := inventory.(lifecycleWindowOwnerInventory)
	if !ok {
		return "", nil
	}
	return owners.ResolveWindowUID(ctx, strings.TrimSpace(event.runtimeWindowID))
}

// lifecycleInventory routes the final-snapshot observation at one exact host.
//
// It is the same mirrored-uid read the reconciler and the active-target fallback
// already share, wrapped in the same explicit-target runner the delete and
// binding-convergence routes use. A zero target falls through to the bare runner
// so an in-tmux invocation observes the server $TMUX names -- introducing a
// default socket here is the exact mistake the delete corrective removed.
func lifecycleInventory(runner tmuxCommandRunner, target explicitTmuxTarget) livePaneInventory {
	if target.flag == "" || target.value == "" {
		return intmetadata.NewMirror(runner)
	}
	return intmetadata.NewMirror(explicitTmuxRunner{runner: runner, target: target})
}

// lifecycleProjectionTargets is the pure decision half: which Panes in registry
// still need a lifecycle projection against the observation in live.
//
// A Pane qualifies when all of the following hold, and the order matters only
// for cost:
//
//   - the event does not narrow it away, by uid or by activation generation;
//   - no live tmux pane mirrors its uid in this observation;
//   - the projection has work left, which is either evidence that has not been
//     recorded or an Agent still bound to the dead Pane.
//
// The last check is what makes a repeat pass free. Without it every offline Pane
// in the registry would re-enter a write transaction on every pane exit in every
// session, forever, and change nothing each time.
func lifecycleProjectionTargets(registry coremetadata.Registry, live map[string]bool, event lifecycleDirtyEvent) []coremetadata.TerminationProjectionInput {
	narrowed := strings.TrimSpace(event.paneUID)
	runtimePane := strings.TrimSpace(event.runtimePaneID)
	var out []coremetadata.TerminationProjectionInput
	for i := range registry.Panes {
		paneUID := registry.Panes[i].Metadata.UID
		if narrowed != "" && paneUID != narrowed {
			continue
		}
		if runtimePane != "" && registry.Panes[i].Status.Activation.RuntimeID != runtimePane {
			continue
		}
		if live[paneUID] {
			continue
		}
		if generation := strings.TrimSpace(event.generation); generation != "" {
			// The generation guard is applied here as well as inside the
			// projection. Inside, it is correctness: the registry may have been
			// relaunched while the event queued. Here, it is cost -- a stale event
			// must not take the write lock to discover it had nothing to say.
			if pane, ok := registry.Pane(paneUID); !ok || pane.Status.Activation.Generation != generation {
				continue
			}
		}
		if !coremetadata.NeedsTerminationProjection(registry, paneUID) {
			continue
		}
		out = append(out, coremetadata.TerminationProjectionInput{
			PaneUID:    paneUID,
			Generation: event.generation,
		})
	}
	return out
}

// projectTerminations applies the lifecycle projection to every named Pane and
// returns what it did.
//
// It is the one transition body both trigger paths share: the reconciler's
// observation step, which rides inside somebody else's mutation transaction, and
// the standalone one-shot below. Sharing it is what keeps the two from
// disagreeing about a classification.
//
// A projection that fails is skipped rather than propagated. This body runs as
// maintenance inside another operation on the reconciler path, and one Pane that
// cannot be projected must not fail the operation that happened to trigger it.
func projectTerminations(
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	inputs []coremetadata.TerminationProjectionInput,
) []coremetadata.TerminationProjection {
	out := make([]coremetadata.TerminationProjection, 0, len(inputs))
	for _, input := range inputs {
		projection, err := mutator.ProjectTermination(working, input)
		if err != nil {
			continue
		}
		out = append(out, projection)
	}
	return out
}

// reconcileLifecycle is the one-shot exit reconciliation.
//
// The shape is the pre-existing sweep's, and every step of it is load-bearing:
//
//  1. observe the exact host once, outside any lock, and fail closed on an
//     unreadable observation. An observation that could not be taken is
//     indistinguishable from an empty one, and reading it as empty would file an
//     unknown termination against every managed Pane on a machine whose tmux
//     server simply is not up.
//  2. compute the projection set against a read-only snapshot, and return
//     without opening a transaction when it is empty. Both pane-exit hooks fire
//     on every pane exit in every session, so the common case -- nothing the
//     registry owns has anything left to reconcile -- must cost zero writes.
//  3. re-observe inside the lock and recompute. The registry may have gained a
//     freshly created Agent while this event waited for the transaction, and
//     applying the pre-lock observation to that newer registry would release the
//     new Agent and delete its still-live Pane. This is the "final exact-host
//     snapshot" the transition is actually derived from; the pre-lock read is
//     only a cost filter.
//
// It starts nothing, resumes nothing, and materializes nothing. The only tmux
// calls it makes are the two mirrored-uid reads of the observation.
func reconcileLifecycle(
	ctx context.Context,
	event lifecycleDirtyEvent,
	inventory livePaneInventory,
	store *resourceStore,
) (lifecycleReconcileResult, error) {
	var result lifecycleReconcileResult
	if inventory == nil {
		return result, errors.New("reconcile lifecycle: the tmux pane inventory is not configured")
	}
	if store == nil || store.load == nil || store.mutator == nil {
		return result, errors.New("reconcile lifecycle: the resource registry store is not configured")
	}
	live, err := inventory.LivePaneUIDs(ctx)
	if err != nil {
		result.skipped = "the exact-host observation could not be taken: " + event.describe()
		result.unobserved = true
		return result, nil
	}
	observedWindowUID, err := lifecycleObservedWindowUID(ctx, inventory, event)
	if err != nil {
		result.skipped = "the exact-host owner observation could not be taken: " + event.describe()
		result.unobserved = true
		return result, nil
	}
	registry, err := store.load()
	if err != nil {
		return result, MapMetadataError(err)
	}
	candidate := registry.Clone()
	_, _ = absorbTerminationReceipts(&candidate, store.mutator(), event.receipts)
	cascade, cascadeErr := planExactPaneExitCascade(candidate, live, observedWindowUID, event, store.mutator())
	if cascadeErr != nil {
		return result, cascadeErr
	}
	if !cascade.Changed && len(lifecycleProjectionTargets(registry, live, event)) == 0 &&
		!terminationReceiptsNeedAbsorption(registry, store.mutator(), event.receipts) {
		result.skipped = "nothing left to reconcile: " + event.describe()
		return result, nil
	}
	var observationFailed bool
	_, err = store.converge(func(working *coremetadata.Registry, mutator coremetadata.Mutator) error {
		fresh, observeErr := inventory.LivePaneUIDs(ctx)
		if observeErr != nil {
			// Abort with zero writes and preserve every resource, exactly as the
			// preflight failure above does.
			observationFailed = true
			return observeErr
		}
		freshWindowUID, observeErr := lifecycleObservedWindowUID(ctx, inventory, event)
		if observeErr != nil {
			observationFailed = true
			return observeErr
		}
		absorbed, err := absorbTerminationReceipts(working, mutator, event.receipts)
		if err != nil {
			return err
		}
		result.receiptsChanged = absorbed
		cascade, err := planExactPaneExitCascade(*working, fresh, freshWindowUID, event, mutator)
		if err != nil {
			return err
		}
		if cascade.Changed {
			*working = cascade.Desired
			result.cascaded = append(result.cascaded, cascade)
		}
		result.projected = projectTerminations(working, mutator, lifecycleProjectionTargets(*working, fresh, event))
		return nil
	})
	result.transactions = 1
	if observationFailed {
		result.projected = nil
		result.transactions = 0
		result.skipped = "the locked exact-host observation could not be taken: " + event.describe()
		result.unobserved = true
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if result.changed() == 0 {
		// The locked re-observation found every candidate live again, or every
		// projection lost a guard. The transaction was opened but the convergent
		// store wrote no bytes, and saying so is what separates that from a pass
		// that actually recorded something.
		result.skipped = "the locked exact-host observation left nothing to reconcile: " + event.describe()
	}
	return result, nil
}
