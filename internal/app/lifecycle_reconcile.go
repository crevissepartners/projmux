package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
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
	target tmuxTransport
	// paneUID narrows the event to one Pane. Empty means the whole host.
	paneUID string
	// runtimePaneID is the exact #{hook_pane} supplied by pane-exited. It is
	// matched against the activation handle projmux stored when that generation
	// was materialized and, when tmux retained the Pane, narrowed again by the
	// exact mirrored uid whose current pane_dead observation is positive. tmux
	// reuses %N handles, so the handle alone is not a durable identity.
	runtimePaneID    string
	runtimeSessionID string
	// runtimeWindowID is the exact event Window handle supplied by
	// window-unlinked's #{hook_window}. pane-exited cannot name the dead Window:
	// both current-context #{window_id} and #{session_id} may already point at a
	// surviving client Window, so its owner handles come from the Window's last
	// exact live Registry observation instead.
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
	// pinStore is the external preference half of a final Project cascade. It is
	// unused for Pane/Agent, non-last Window, and ControlSession outcomes.
	pinStore pinSetStore
	// exhaustedReplay requires the narrow startup-only normal receipt and
	// current activation operation checks before this event may write.
	exhaustedReplay bool
}

// describe renders the event for a diagnostic line.
func (e lifecycleDirtyEvent) describe() string {
	out := "socket=" + e.target.Label()
	if pane := strings.TrimSpace(e.paneUID); pane != "" {
		out += " pane=" + pane
	}
	if pane := strings.TrimSpace(e.runtimePaneID); pane != "" {
		out += " hook-pane=" + pane
	}
	if session := strings.TrimSpace(e.runtimeSessionID); session != "" {
		out += " hook-session=" + session
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
	pending      []coremetadata.PaneTeardownEvidencePlan
	rootCascaded []coremetadata.WindowRootCascadeDeletePlan
	// awaitingPaneExit says an exact unlink found no matching stored pane-exit
	// evidence. It is controller transport state, not Registry authority.
	awaitingPaneExit bool
	transactions     int
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

// lifecycleCleanupRetryError is the typed failure surface for an exact clean
// Agent exit whose dead tmux Pane could not be removed. The Registry transaction
// is aborted, so the same-generation Pane/evidence owner chain remains available
// for a strict retry; the reason is stable and machine-checkable even though the
// underlying tmux error is not.
type lifecycleCleanupRetryError struct {
	Reason coremetadata.TeardownReason
	Target paneLiveDeleteTarget
	Err    error
}

func (e *lifecycleCleanupRetryError) Error() string {
	return fmt.Sprintf("%s: socket-scoped dead Pane cleanup target %s uid %s: %v",
		e.Reason, e.Target.PaneID, e.Target.PaneUID, e.Err)
}

func (e *lifecycleCleanupRetryError) Unwrap() error { return e.Err }

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
	count += len(r.pending)
	count += len(r.rootCascaded)
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

// exactExhaustedCleanExitReceipt selects the one receipt a startup replay may
// offer to the existing planner. It deliberately requires the production
// positive dead-Pane mirror; cached Registry locators and absence-only test
// inventories cannot upgrade a terminal event into cleanup authority.
func exactExhaustedCleanExitReceipt(registry coremetadata.Registry, dead lifecycleDeadPaneSnapshot, event lifecycleDirtyEvent) (coremetadata.TerminationEvidence, bool) {
	if !event.exhaustedReplay || dead.legacy || strings.TrimSpace(event.runtimePaneID) == "" {
		return coremetadata.TerminationEvidence{}, false
	}
	var observed *intmetadata.DeadPaneObservation
	for i := range dead.observations {
		candidate := &dead.observations[i]
		if candidate.PaneID != strings.TrimSpace(event.runtimePaneID) || strings.TrimSpace(candidate.PaneUID) == "" {
			continue
		}
		if observed != nil {
			return coremetadata.TerminationEvidence{}, false
		}
		observed = candidate
	}
	if observed == nil {
		return coremetadata.TerminationEvidence{}, false
	}
	pane, ok := registry.Pane(observed.PaneUID)
	if !ok || pane.Status.Activation.RuntimeID != observed.PaneID ||
		strings.TrimSpace(pane.Status.Activation.Generation) == "" ||
		strings.TrimSpace(pane.Status.Activation.OperationID) == "" ||
		pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != coremetadata.KindAgent ||
		pane.Metadata.OwnerRef.UID != observed.AgentUID || pane.Status.Activation.AgentUID != observed.AgentUID {
		return coremetadata.TerminationEvidence{}, false
	}
	agent, ok := registry.Agent(observed.AgentUID)
	if !ok || agent.Status.PaneRef != pane.Metadata.UID {
		return coremetadata.TerminationEvidence{}, false
	}
	if stored := pane.Status.LastTermination; stored != nil &&
		(stored.Source != coremetadata.TerminationSourceSupervisor ||
			stored.Classification != coremetadata.TerminationNormal ||
			stored.Generation != pane.Status.Activation.Generation ||
			stored.OperationID != pane.Status.Activation.OperationID) {
		return coremetadata.TerminationEvidence{}, false
	}
	for _, receipt := range event.receipts {
		if receipt.Source != coremetadata.TerminationSourceSupervisor ||
			receipt.Classification != coremetadata.TerminationNormal ||
			receipt.ExitCode == nil || *receipt.ExitCode != 0 || strings.TrimSpace(receipt.Signal) != "" ||
			receipt.PaneUID != pane.Metadata.UID || receipt.AgentUID != observed.AgentUID ||
			receipt.Generation != pane.Status.Activation.Generation ||
			receipt.OperationID != pane.Status.Activation.OperationID {
			continue
		}
		return receipt, true
	}
	return coremetadata.TerminationEvidence{}, false
}

func equalOptionalInt(left, right *int) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func stableDeadPaneAuthorityConflict(reason coremetadata.TeardownReason, detail string) error {
	return fmt.Errorf("stable dead Pane authority conflict (%s): %s", reason, detail)
}

// planExactPaneExitCascade converts only a positively observed exact
// pane-exited event into the Phase 2 Pane/Agent desired plan. The supervisor
// journal match is intentionally part of authority: after the resource rows
// are deleted that bounded receipt is the durable diagnostic evidence of the
// clean outcome.
type exactLifecycleCascadePlan struct {
	Desired     coremetadata.Registry
	Changed     bool
	awaiting    bool
	paneAgent   coremetadata.PaneAgentCascadeDeletePlan
	pending     coremetadata.PaneTeardownEvidencePlan
	root        coremetadata.WindowRootCascadeDeletePlan
	deadCleanup *paneLiveDeleteTarget
}

func planExactLifecycleCascade(
	registry coremetadata.Registry,
	live map[string]bool,
	dead []intmetadata.DeadPaneObservation,
	liveHostPanes int,
	liveWindows map[string]bool,
	liveWindowSessions map[string]int,
	event lifecycleDirtyEvent,
	mutator coremetadata.Mutator,
) (exactLifecycleCascadePlan, error) {
	if (event.teardownKind != coremetadata.TeardownEventPaneExited &&
		event.teardownKind != coremetadata.TeardownEventWindowUnlinked) ||
		event.target.Flag() == "" || event.target.Value == "" {
		return exactLifecycleCascadePlan{}, nil
	}
	if event.teardownKind == coremetadata.TeardownEventWindowUnlinked {
		if strings.TrimSpace(event.runtimeWindowID) == "" || strings.TrimSpace(event.runtimeSessionID) == "" {
			return exactLifecycleCascadePlan{}, nil
		}
		return planExactWindowUnlinkCascade(registry, live, liveWindows, liveWindowSessions, event, mutator)
	}
	if strings.TrimSpace(event.runtimePaneID) == "" {
		return exactLifecycleCascadePlan{}, nil
	}
	var observed *intmetadata.DeadPaneObservation
	for i := range dead {
		candidate := &dead[i]
		if candidate.PaneID != strings.TrimSpace(event.runtimePaneID) ||
			(strings.TrimSpace(event.paneUID) != "" && candidate.PaneUID != strings.TrimSpace(event.paneUID)) {
			continue
		}
		if observed != nil {
			return exactLifecycleCascadePlan{}, stableDeadPaneAuthorityConflict(
				coremetadata.TeardownReasonConflictingOwnerFacts, "current tmux locator names multiple dead Pane observations")
		}
		observed = candidate
	}
	retainedDead := observed != nil
	if observed == nil {
		var absent *coremetadata.Pane
		for i := range registry.Panes {
			candidate := &registry.Panes[i]
			if candidate.Spec.Role == coremetadata.PaneRoleAgent ||
				(strings.TrimSpace(event.paneUID) != "" && candidate.Metadata.UID != strings.TrimSpace(event.paneUID)) ||
				(strings.TrimSpace(event.paneUID) == "" && candidate.Status.Activation.RuntimeID != strings.TrimSpace(event.runtimePaneID)) {
				continue
			}
			if absent != nil {
				return exactLifecycleCascadePlan{paneAgent: coremetadata.PaneAgentCascadeDeletePlan{Decision: coremetadata.TeardownDecision{
					Action: coremetadata.TeardownRefuse, Reason: coremetadata.TeardownReasonConflictingOwnerFacts,
				}}}, nil
			}
			absent = candidate
		}
		if absent == nil {
			return exactLifecycleCascadePlan{}, nil
		}
		rows := lifecycleLegacyDeadPaneObservations(registry, lifecycleDeadPaneSnapshot{
			uids: map[string]bool{absent.Metadata.UID: true}, legacy: true,
		})
		if len(rows) != 1 {
			return exactLifecycleCascadePlan{}, nil
		}
		observed = &rows[0]
	}
	if exactTmuxHandle(observed.SessionID, "$") == "" || exactTmuxHandle(observed.WindowID, "@") == "" ||
		exactTmuxHandle(observed.PaneID, "%") == "" || strings.TrimSpace(observed.PaneUID) == "" ||
		strings.TrimSpace(observed.WindowUID) == "" || strings.TrimSpace(observed.SessionName) == "" {
		return exactLifecycleCascadePlan{}, stableDeadPaneAuthorityConflict(
			coremetadata.TeardownReasonConflictingOwnerFacts, "current dead Pane observation has incomplete stable containment")
	}
	pane, ok := registry.Pane(observed.PaneUID)
	if !ok || (strings.TrimSpace(event.generation) != "" && pane.Status.Activation.Generation != strings.TrimSpace(event.generation)) {
		return exactLifecycleCascadePlan{}, stableDeadPaneAuthorityConflict(
			coremetadata.TeardownReasonStaleGeneration, "observed Pane UID or activation generation is not current Registry authority")
	}
	if pane.Metadata.OwnerRef == nil || observed.OwnerKind != string(pane.Metadata.OwnerRef.Kind) ||
		observed.OwnerUID != pane.Metadata.OwnerRef.UID || observed.PaneRole != string(pane.Spec.Role) {
		return exactLifecycleCascadePlan{}, stableDeadPaneAuthorityConflict(
			coremetadata.TeardownReasonConflictingOwnerFacts, "current Pane ownerRef or role mirror conflicts with the stable Registry graph")
	}
	if pane.Metadata.OwnerRef.Kind == coremetadata.KindAgent {
		if observed.AgentUID != pane.Metadata.OwnerRef.UID {
			return exactLifecycleCascadePlan{}, stableDeadPaneAuthorityConflict(
				coremetadata.TeardownReasonConflictingOwnerFacts, "current Agent UID mirror conflicts with Pane ownerRef")
		}
	} else if strings.TrimSpace(observed.AgentUID) != "" {
		return exactLifecycleCascadePlan{}, stableDeadPaneAuthorityConflict(
			coremetadata.TeardownReasonConflictingOwnerFacts, "non-Agent Pane carries a foreign Agent UID mirror")
	}
	windowUID, ok := paneWindowUID(registry, *pane)
	if !ok {
		return exactLifecycleCascadePlan{}, stableDeadPaneAuthorityConflict(
			coremetadata.TeardownReasonStaleOwnerBinding, "Pane ownerRef does not resolve one stable Window")
	}
	window, _ := registry.Window(windowUID)
	if observed.WindowUID != windowUID {
		return exactLifecycleCascadePlan{}, stableDeadPaneAuthorityConflict(
			coremetadata.TeardownReasonConflictingOwnerFacts, "current Window UID mirror conflicts with the stable owner graph")
	}
	root := window.Metadata.OwnerRef
	if root == nil || (root.Kind != coremetadata.KindProject && root.Kind != coremetadata.KindControlSession) {
		return exactLifecycleCascadePlan{}, stableDeadPaneAuthorityConflict(
			coremetadata.TeardownReasonStaleOwnerBinding, "Window ownerRef does not resolve one managed root")
	}
	sessionName := lifecycleRootSessionName(registry, *root)
	if sessionName == "" || sessionName != observed.SessionName {
		return exactLifecycleCascadePlan{}, stableDeadPaneAuthorityConflict(
			coremetadata.TeardownReasonUnavailable, "current session name conflicts with the stable root declaration")
	}
	switch root.Kind {
	case coremetadata.KindProject:
		if observed.ProjectUID != root.UID || strings.TrimSpace(observed.SessionRole) != "" {
			return exactLifecycleCascadePlan{}, stableDeadPaneAuthorityConflict(
				coremetadata.TeardownReasonConflictingOwnerFacts, "current Project UID or session role conflicts with the stable root")
		}
	case coremetadata.KindControlSession:
		if strings.TrimSpace(observed.ProjectUID) != "" || observed.SessionRole != resourcegraph.ControlSessionRole {
			return exactLifecycleCascadePlan{}, stableDeadPaneAuthorityConflict(
				coremetadata.TeardownReasonConflictingOwnerFacts, "current ControlSession role or Project UID mirror conflicts with the stable root")
		}
	}

	classification := coremetadata.TerminationUnknown
	if stored := pane.Status.LastTermination; stored != nil &&
		stored.Generation == pane.Status.Activation.Generation &&
		coremetadata.ValidTerminationClassification(stored.Classification) {
		classification = stored.Classification
	}
	observation := coremetadata.TeardownObservationExactSocket
	if liveHostPanes == 0 {
		observation = coremetadata.TeardownObservationEmpty
	}
	deadUIDs := lifecycleDeadPaneUIDs(dead)
	liveSiblingPane := false
	for i := range registry.Panes {
		sibling := registry.Panes[i]
		if sibling.Metadata.UID == pane.Metadata.UID || !live[sibling.Metadata.UID] || deadUIDs[sibling.Metadata.UID] {
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
				candidateWindow == siblingWindow.Metadata.UID && live[candidate.Metadata.UID] && !deadUIDs[candidate.Metadata.UID] {
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
			SocketIdentity: event.target.Label(), SessionHandle: observed.SessionID, PaneHandle: observed.PaneID,
			WindowHandle: observed.WindowID, PaneUID: pane.Metadata.UID,
			WindowUID: windowUID, RootKind: root.Kind, RootUID: root.UID,
			Generation: pane.Status.Activation.Generation,
		},
		LiveSiblingPane: liveSiblingPane, LiveSiblingRootWindow: liveSiblingRootWindow,
	}
	decision := coremetadata.DecideTeardownEvent(teardown)
	if decision.Action == coremetadata.TeardownDeletePaneAgent && !exactJournalReceipt(event.receipts, *pane) {
		return exactLifecycleCascadePlan{}, stableDeadPaneAuthorityConflict(
			coremetadata.TeardownReasonUnavailable, "normal teardown lacks one exact current-generation supervisor journal receipt")
	}
	now := time.Now
	if mutator.Now != nil {
		now = mutator.Now
	}
	if liveSiblingPane {
		plan, err := coremetadata.PlanPaneAgentCascadeDelete(registry, teardown, now().UTC())
		cascade := exactLifecycleCascadePlan{Desired: plan.Desired, Changed: plan.Changed, paneAgent: plan}
		if err == nil && plan.Changed && retainedDead {
			target := lifecycleDeadPaneTarget(teardown, sessionName, *pane)
			cascade.deadCleanup = &target
		}
		return cascade, err
	}
	pending, err := coremetadata.PlanPaneTeardownEvidence(registry, teardown, now().UTC())
	cascade := exactLifecycleCascadePlan{Desired: pending.Desired, Changed: pending.Changed, pending: pending}
	if err == nil && pending.Changed && retainedDead {
		target := lifecycleDeadPaneTarget(teardown, sessionName, *pane)
		cascade.deadCleanup = &target
	}
	return cascade, err
}

// narrowLifecycleDeadPaneEvent binds a reusable tmux %N hook handle back to
// the one Registry Pane uid that the same exact socket currently reports as a
// retained dead Pane. Historical Pane resources deliberately keep activation
// diagnostics, including their old runtime handle, so matching the handle alone
// eventually becomes ambiguous on a long-lived server. The positive pane_dead
// mirror is current-generation evidence and safely removes that ambiguity. A
// missing or contradictory observation leaves the event unchanged and the
// existing fail-closed conflict path intact.
func narrowLifecycleDeadPaneEvent(dead []intmetadata.DeadPaneObservation, event lifecycleDirtyEvent) lifecycleDirtyEvent {
	if event.teardownKind != coremetadata.TeardownEventPaneExited ||
		strings.TrimSpace(event.paneUID) != "" || strings.TrimSpace(event.runtimePaneID) == "" {
		return event
	}
	resolved := ""
	for i := range dead {
		candidate := dead[i]
		if candidate.PaneID != strings.TrimSpace(event.runtimePaneID) || strings.TrimSpace(candidate.PaneUID) == "" {
			continue
		}
		if resolved != "" {
			return event
		}
		resolved = candidate.PaneUID
	}
	if resolved != "" {
		event.paneUID = resolved
	}
	return event
}

func planExactWindowUnlinkCascade(
	registry coremetadata.Registry,
	live, liveWindows map[string]bool,
	liveWindowSessions map[string]int,
	event lifecycleDirtyEvent,
	mutator coremetadata.Mutator,
) (exactLifecycleCascadePlan, error) {
	if liveWindows == nil || liveWindowSessions == nil {
		return exactLifecycleCascadePlan{}, nil
	}
	var pane *coremetadata.Pane
	for i := range registry.Panes {
		candidate := &registry.Panes[i]
		evidence := candidate.Status.Teardown
		if evidence == nil || evidence.SocketIdentity != event.target.Label() ||
			evidence.RuntimeSessionID != strings.TrimSpace(event.runtimeSessionID) ||
			evidence.RuntimeWindowID != strings.TrimSpace(event.runtimeWindowID) {
			continue
		}
		if pane != nil {
			return exactLifecycleCascadePlan{root: coremetadata.WindowRootCascadeDeletePlan{Decision: coremetadata.TeardownDecision{
				Action: coremetadata.TeardownRefuse, Reason: coremetadata.TeardownReasonConflictingOwnerFacts,
			}}}, nil
		}
		pane = candidate
	}
	if pane == nil || pane.Status.Teardown == nil {
		return exactLifecycleCascadePlan{awaiting: true}, nil
	}
	evidence := pane.Status.Teardown
	if live[pane.Metadata.UID] || liveWindows[evidence.WindowUID] {
		return exactLifecycleCascadePlan{root: coremetadata.WindowRootCascadeDeletePlan{Decision: coremetadata.TeardownDecision{
			Action: coremetadata.TeardownRefuse, Reason: coremetadata.TeardownReasonConflictingOwnerFacts,
		}}}, nil
	}
	for i := range registry.Panes {
		candidate := registry.Panes[i]
		if candidate.Metadata.UID == pane.Metadata.UID || !live[candidate.Metadata.UID] {
			continue
		}
		if windowUID, ok := paneWindowUID(registry, candidate); ok && windowUID == evidence.WindowUID {
			return exactLifecycleCascadePlan{root: coremetadata.WindowRootCascadeDeletePlan{Decision: coremetadata.TeardownDecision{
				Action: coremetadata.TeardownRefuse, Reason: coremetadata.TeardownReasonLiveSiblingPane,
			}}}, nil
		}
	}
	liveSiblingRootWindows := 0
	for _, sibling := range registry.WindowsOf(evidence.RootUID) {
		if sibling.Metadata.UID != evidence.WindowUID && liveWindows[sibling.Metadata.UID] {
			liveSiblingRootWindows++
		}
	}
	// Every remaining runtime Window in the exact event session must be an exact
	// sibling Registry Window. Unmirrored or cross-session rows make the pair
	// foreign rather than broadening deletion authority.
	if liveWindowSessions[evidence.RuntimeSessionID] != liveSiblingRootWindows {
		return exactLifecycleCascadePlan{root: coremetadata.WindowRootCascadeDeletePlan{Decision: coremetadata.TeardownDecision{
			Action: coremetadata.TeardownRefuse, Reason: coremetadata.TeardownReasonForeignHost,
		}}}, nil
	}
	chain := coremetadata.TeardownOwnerChain{
		SocketIdentity: evidence.SocketIdentity, SessionHandle: evidence.RuntimeSessionID, PaneHandle: evidence.RuntimePaneID,
		WindowHandle: evidence.RuntimeWindowID, PaneUID: pane.Metadata.UID, WindowUID: evidence.WindowUID,
		RootKind: evidence.RootKind, RootUID: evidence.RootUID, Generation: evidence.Generation,
	}
	paneEvent := coremetadata.TeardownEvent{
		Kind: coremetadata.TeardownEventPaneExited, Classification: evidence.Classification,
		Generation: coremetadata.TeardownGenerationCurrent, Observation: coremetadata.TeardownObservationExactSocket,
		Chain: chain, LiveSiblingRootWindow: liveSiblingRootWindows > 0,
	}
	unlinked := paneEvent
	unlinked.Kind = coremetadata.TeardownEventWindowUnlinked
	now := time.Now
	if mutator.Now != nil {
		now = mutator.Now
	}
	plan, err := coremetadata.PlanWindowRootCascadeDelete(registry, paneEvent, unlinked, now().UTC())
	return exactLifecycleCascadePlan{Desired: plan.Desired, Changed: plan.Changed, root: plan}, err
}

func lifecycleRootSessionName(registry coremetadata.Registry, root coremetadata.OwnerRef) string {
	switch root.Kind {
	case coremetadata.KindProject:
		project, ok := registry.Project(root.UID)
		if ok && project.Status.Session != nil {
			return strings.TrimSpace(project.Status.Session.Name)
		}
	case coremetadata.KindControlSession:
		control, ok := registry.ControlSession(root.UID)
		if ok {
			return strings.TrimSpace(control.Spec.Session)
		}
	}
	return ""
}

func lifecycleDeadPaneTarget(event coremetadata.TeardownEvent, sessionName string, pane coremetadata.Pane) paneLiveDeleteTarget {
	target := paneLiveDeleteTarget{
		PaneUID: event.Chain.PaneUID, PaneID: event.Chain.PaneHandle,
		WindowUID: event.Chain.WindowUID, WindowID: event.Chain.WindowHandle,
		SessionID: event.Chain.SessionHandle, SessionName: strings.TrimSpace(sessionName),
		RootKind: event.Chain.RootKind, RootUID: event.Chain.RootUID,
		EndsWindow: !event.LiveSiblingPane, RequireDead: true,
		PaneRole: pane.Spec.Role, Generation: pane.Status.Activation.Generation,
	}
	if pane.Metadata.OwnerRef != nil {
		target.OwnerKind = pane.Metadata.OwnerRef.Kind
		target.OwnerUID = pane.Metadata.OwnerRef.UID
		if pane.Metadata.OwnerRef.Kind == coremetadata.KindAgent {
			target.AgentUID = pane.Metadata.OwnerRef.UID
		}
	}
	return target
}

type lifecycleLiveWindowInventory interface {
	LiveWindowUIDs(context.Context) (map[string]bool, error)
}

type lifecycleHostPaneInventory interface {
	LivePaneCount(context.Context) (int, error)
}

func lifecycleObservedHostPaneCount(ctx context.Context, inventory livePaneInventory, event lifecycleDirtyEvent) (int, error) {
	if event.teardownKind != coremetadata.TeardownEventPaneExited {
		return 0, nil
	}
	host, ok := inventory.(lifecycleHostPaneInventory)
	if !ok {
		return 0, errors.New("exact-host Pane count is unavailable")
	}
	return host.LivePaneCount(ctx)
}

type lifecycleLiveWindowSessionInventory interface {
	LiveWindowSessionCounts(context.Context) (map[string]int, error)
}

// lifecycleDeadPaneCleanupInventory is implemented only by the exact
// production inventory and focused lifecycle fixtures. It removes the exact
// remain-on-exit dead Pane before the pending receipt is committed.
type lifecycleDeadPaneCleanupInventory interface {
	CleanupLifecycleDeadPane(context.Context, paneLiveDeleteTarget) error
}

type lifecycleDeadPaneInventory interface {
	DeadPaneObservations(context.Context) ([]intmetadata.DeadPaneObservation, error)
}

type lifecycleLegacyDeadPaneInventory interface {
	DeadPaneUIDs(context.Context) (map[string]bool, error)
}

type lifecycleDeadPaneSnapshot struct {
	observations []intmetadata.DeadPaneObservation
	uids         map[string]bool
	legacy       bool
}

type exactLifecycleInventory struct {
	intmetadata.Mirror
	runtime *tmuxPaneDeleteRuntime
}

func (i *exactLifecycleInventory) CleanupLifecycleDeadPane(ctx context.Context, target paneLiveDeleteTarget) error {
	if i == nil || i.runtime == nil {
		return errors.New("lifecycle dead Pane cleanup runtime is not configured")
	}
	// Bind the runtime mutation route from the exact retained dead Pane before
	// building the kill plan; an absent route can never authorize cleanup.
	i.runtime.useRouteAnchor(target.PaneID)
	if err := i.runtime.guardSocketIdentity(ctx); err != nil {
		return fmt.Errorf("bind exact dead Agent Pane cleanup route: %w", err)
	}
	return i.runtime.kill(ctx, target)
}

func lifecycleObservedDeadPanes(ctx context.Context, inventory livePaneInventory, event lifecycleDirtyEvent) (lifecycleDeadPaneSnapshot, error) {
	if event.teardownKind != coremetadata.TeardownEventPaneExited {
		return lifecycleDeadPaneSnapshot{}, nil
	}
	dead, ok := inventory.(lifecycleDeadPaneInventory)
	if ok {
		observed, err := dead.DeadPaneObservations(ctx)
		return lifecycleDeadPaneSnapshot{observations: observed, uids: lifecycleDeadPaneUIDs(observed)}, err
	}
	legacy, ok := inventory.(lifecycleLegacyDeadPaneInventory)
	if !ok {
		// Older/non-authoritative inventory implementations may still project
		// absence, but they can never grant exact dead-Pane cleanup authority.
		return lifecycleDeadPaneSnapshot{}, nil
	}
	uids, err := legacy.DeadPaneUIDs(ctx)
	return lifecycleDeadPaneSnapshot{uids: uids, legacy: true}, err
}

func lifecycleDeadPaneUIDs(observed []intmetadata.DeadPaneObservation) map[string]bool {
	uids := make(map[string]bool, len(observed))
	for _, pane := range observed {
		if uid := strings.TrimSpace(pane.PaneUID); uid != "" {
			uids[uid] = true
		}
	}
	return uids
}

// lifecycleLegacyDeadPaneObservations is test-adapter compatibility only. The
// production Mirror implements DeadPaneObservations and never enters this
// cached-locator bridge.
func lifecycleLegacyDeadPaneObservations(registry coremetadata.Registry, dead lifecycleDeadPaneSnapshot) []intmetadata.DeadPaneObservation {
	if !dead.legacy {
		return dead.observations
	}
	observed := make([]intmetadata.DeadPaneObservation, 0, len(dead.uids))
	for uid := range dead.uids {
		pane, ok := registry.Pane(uid)
		if !ok {
			continue
		}
		windowUID, ok := paneWindowUID(registry, *pane)
		if !ok {
			continue
		}
		window, _ := registry.Window(windowUID)
		root := window.Metadata.OwnerRef
		if root == nil {
			continue
		}
		row := intmetadata.DeadPaneObservation{
			SessionID: window.Status.RuntimeSessionID, SessionName: lifecycleRootSessionName(registry, *root),
			WindowID: window.Status.RuntimeID, PaneID: pane.Status.Activation.RuntimeID,
			WindowUID: windowUID, PaneUID: uid,
			OwnerKind: string(pane.Metadata.OwnerRef.Kind), OwnerUID: pane.Metadata.OwnerRef.UID,
			PaneRole: string(pane.Spec.Role),
		}
		if pane.Metadata.OwnerRef.Kind == coremetadata.KindAgent {
			row.AgentUID = pane.Metadata.OwnerRef.UID
		}
		if root.Kind == coremetadata.KindProject {
			row.ProjectUID = root.UID
		} else if root.Kind == coremetadata.KindControlSession {
			row.SessionRole = resourcegraph.ControlSessionRole
		}
		observed = append(observed, row)
	}
	return observed
}

func lifecycleObservedLiveWindows(ctx context.Context, inventory livePaneInventory, event lifecycleDirtyEvent) (map[string]bool, error) {
	if event.teardownKind != coremetadata.TeardownEventWindowUnlinked {
		return nil, nil
	}
	windows, ok := inventory.(lifecycleLiveWindowInventory)
	if !ok {
		return nil, errors.New("exact-host Window inventory is unavailable")
	}
	return windows.LiveWindowUIDs(ctx)
}

func lifecycleObservedLiveWindowSessions(ctx context.Context, inventory livePaneInventory, event lifecycleDirtyEvent) (map[string]int, error) {
	if event.teardownKind != coremetadata.TeardownEventWindowUnlinked {
		return nil, nil
	}
	sessions, ok := inventory.(lifecycleLiveWindowSessionInventory)
	if !ok {
		return nil, errors.New("exact-host Window session inventory is unavailable")
	}
	return sessions.LiveWindowSessionCounts(ctx)
}

// lifecycleInventory routes the final-snapshot observation at one exact host.
//
// It is the same mirrored-uid read the reconciler and the active-target fallback
// already share, wrapped in the same explicit-target runner the delete and
// binding-convergence routes use. A zero target falls through to the bare runner
// so an in-tmux invocation observes the server $TMUX names -- introducing a
// default socket here is the exact mistake the delete corrective removed.
func lifecycleInventory(runner tmuxCommandRunner, target tmuxTransport) livePaneInventory {
	routed := runner
	if target.Flag() != "" && target.Value != "" {
		routed = explicitTmuxRunner{runner: runner, target: target}
	}
	runtime := newTmuxPaneDeleteRuntime()
	runtime.runner = runner
	runtime.useExactTarget(target)
	return &exactLifecycleInventory{Mirror: intmetadata.NewMirror(routed), runtime: runtime}
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
	// A typed window-unlinked event can consume only its exact stored pane-exit
	// evidence. It must never widen into a whole-host absence projection: that
	// would turn an unknown/foreign/empty inventory into replacement authority.
	if event.teardownKind == coremetadata.TeardownEventWindowUnlinked {
		return nil
	}
	narrowed := strings.TrimSpace(event.paneUID)
	runtimePane := strings.TrimSpace(event.runtimePaneID)
	var out []coremetadata.TerminationProjectionInput
	for i := range registry.Panes {
		paneUID := registry.Panes[i].Metadata.UID
		// A clean last-Pane receipt retains the complete subtree until its exact
		// window-unlinked half arrives. A generic absence projection must not
		// release the Agent binding or invalidate the Window anchor in between.
		if registry.Panes[i].Status.Teardown != nil {
			continue
		}
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

func lifecycleEffectiveLivePanes(live, dead map[string]bool) map[string]bool {
	if len(dead) == 0 {
		return live
	}
	effective := make(map[string]bool, len(live))
	for uid, present := range live {
		if present && !dead[uid] {
			effective[uid] = true
		}
	}
	return effective
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
// It resumes no Agent. On an authorized exact retained pane-exited event it
// cleans only the exact owned dead Pane before committing its bounded pending
// evidence. All other events remain observation-only.
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
	liveHostPanes, err := lifecycleObservedHostPaneCount(ctx, inventory, event)
	if err != nil {
		return lifecycleReconcileResult{skipped: fmt.Sprintf("observe exact host for %s: %v", event.describe(), err), unobserved: true}, nil
	}
	dead, err := lifecycleObservedDeadPanes(ctx, inventory, event)
	if err != nil {
		return lifecycleReconcileResult{skipped: fmt.Sprintf("observe exact dead Panes for %s: %v", event.describe(), err), unobserved: true}, nil
	}
	liveWindows, err := lifecycleObservedLiveWindows(ctx, inventory, event)
	if err != nil {
		result.skipped = "the exact-host Window observation could not be taken: " + event.describe()
		result.unobserved = true
		return result, nil
	}
	liveWindowSessions, err := lifecycleObservedLiveWindowSessions(ctx, inventory, event)
	if err != nil {
		result.skipped = "the exact-host Window session observation could not be taken: " + event.describe()
		result.unobserved = true
		return result, nil
	}
	registry, err := store.load()
	if err != nil {
		return result, MapMetadataError(err)
	}
	if event.exhaustedReplay {
		receipt, eligible := exactExhaustedCleanExitReceipt(registry, dead, event)
		if !eligible {
			result.skipped = "exhausted clean-exit event lacks current exact authority: " + event.describe()
			return result, nil
		}
		event.receipts = []coremetadata.TerminationEvidence{receipt}
	}
	candidate := registry.Clone()
	_, _ = absorbTerminationReceipts(&candidate, store.mutator(), event.receipts)
	deadObservations := lifecycleLegacyDeadPaneObservations(candidate, dead)
	candidateEvent := narrowLifecycleDeadPaneEvent(deadObservations, event)
	cascade, cascadeErr := planExactLifecycleCascade(candidate, live, deadObservations, liveHostPanes, liveWindows, liveWindowSessions, candidateEvent, store.mutator())
	if cascadeErr != nil {
		return result, cascadeErr
	}
	if event.exhaustedReplay && !cascade.paneAgent.Changed {
		result.skipped = "exhausted clean-exit event did not produce the exact Pane/Agent cascade: " + event.describe()
		return result, nil
	}
	if !cascade.Changed && len(lifecycleProjectionTargets(registry, lifecycleEffectiveLivePanes(live, dead.uids), candidateEvent)) == 0 &&
		!terminationReceiptsNeedAbsorption(registry, store.mutator(), event.receipts) {
		result.awaitingPaneExit = cascade.awaiting
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
		freshHostPanes, observeErr := lifecycleObservedHostPaneCount(ctx, inventory, event)
		if observeErr != nil {
			observationFailed = true
			return fmt.Errorf("reobserve exact host for %s: %w", event.describe(), observeErr)
		}
		freshDead, observeErr := lifecycleObservedDeadPanes(ctx, inventory, event)
		if observeErr != nil {
			observationFailed = true
			return fmt.Errorf("reobserve exact dead Panes for %s: %w", event.describe(), observeErr)
		}
		freshWindows, observeErr := lifecycleObservedLiveWindows(ctx, inventory, event)
		if observeErr != nil {
			observationFailed = true
			return observeErr
		}
		freshWindowSessions, observeErr := lifecycleObservedLiveWindowSessions(ctx, inventory, event)
		if observeErr != nil {
			observationFailed = true
			return observeErr
		}
		applyTo := working
		if event.exhaustedReplay {
			candidate := working.Clone()
			receipt, eligible := exactExhaustedCleanExitReceipt(candidate, freshDead, event)
			if !eligible {
				return nil
			}
			event.receipts = []coremetadata.TerminationEvidence{receipt}
			applyTo = &candidate
		}
		absorbed, err := absorbTerminationReceipts(applyTo, mutator, event.receipts)
		if err != nil {
			return err
		}
		result.receiptsChanged = absorbed
		freshDeadObservations := lifecycleLegacyDeadPaneObservations(*applyTo, freshDead)
		lockedEvent := narrowLifecycleDeadPaneEvent(freshDeadObservations, event)
		cascade, err := planExactLifecycleCascade(*applyTo, fresh, freshDeadObservations, freshHostPanes, freshWindows, freshWindowSessions, lockedEvent, mutator)
		if err != nil {
			return err
		}
		if event.exhaustedReplay && !cascade.paneAgent.Changed {
			result.receiptsChanged = false
			return nil
		}
		result.awaitingPaneExit = cascade.awaiting
		if cascade.Changed {
			var runtime lifecycleDeadPaneCleanupInventory
			if cascade.deadCleanup != nil {
				var ok bool
				runtime, ok = inventory.(lifecycleDeadPaneCleanupInventory)
				if !ok {
					return errors.New("exact dead Agent Pane cleanup runtime is unavailable; Registry was retained")
				}
			}
			// Remove the exact retained dead tmux Pane before publishing the
			// desired Registry graph. If cleanup fails, converge aborts with the
			// old Pane row and same-generation evidence intact, so the next event
			// can prove the same authority and retry instead of falling through to
			// stale-owner-binding.
			if cascade.deadCleanup != nil {
				if cleanupErr := runtime.CleanupLifecycleDeadPane(ctx, *cascade.deadCleanup); cleanupErr != nil {
					return &lifecycleCleanupRetryError{
						Reason: coremetadata.TeardownReasonDeadPaneCleanupRetry,
						Target: *cascade.deadCleanup,
						Err:    cleanupErr,
					}
				}
			}
			*working = cascade.Desired
			switch {
			case cascade.root.Changed:
				result.rootCascaded = append(result.rootCascaded, cascade.root)
			case cascade.pending.Changed:
				result.pending = append(result.pending, cascade.pending)
			case cascade.paneAgent.Changed:
				result.cascaded = append(result.cascaded, cascade.paneAgent)
			}
		}
		result.projected = projectTerminations(working, mutator, lifecycleProjectionTargets(*working, lifecycleEffectiveLivePanes(fresh, freshDead.uids), lockedEvent))
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
