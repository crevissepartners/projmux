package app

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// preexistingDeadPaneBlockerReason is the closed diagnostic vocabulary of the
// startup-only producer. These reasons grant no lifecycle authority; each one
// means the exact pass stopped before its first Registry or tmux write.
type preexistingDeadPaneBlockerReason string

const (
	preexistingBlockerObservation       preexistingDeadPaneBlockerReason = "observation-unavailable"
	preexistingBlockerEmptyObservation  preexistingDeadPaneBlockerReason = "empty-observation"
	preexistingBlockerForeignSocket     preexistingDeadPaneBlockerReason = "foreign-socket"
	preexistingBlockerForeignPane       preexistingDeadPaneBlockerReason = "foreign-pane"
	preexistingBlockerAmbiguousPane     preexistingDeadPaneBlockerReason = "ambiguous-pane"
	preexistingBlockerOwnerDrift        preexistingDeadPaneBlockerReason = "owner-drift"
	preexistingBlockerActivationDrift   preexistingDeadPaneBlockerReason = "activation-drift"
	preexistingBlockerContainmentDrift  preexistingDeadPaneBlockerReason = "containment-drift"
	preexistingBlockerLocatorDrift      preexistingDeadPaneBlockerReason = "locator-drift"
	preexistingBlockerSupervisorActive  preexistingDeadPaneBlockerReason = "supervisor-active"
	preexistingBlockerSupervisorUnknown preexistingDeadPaneBlockerReason = "supervisor-unobservable"
	preexistingBlockerReceiptConflict   preexistingDeadPaneBlockerReason = "receipt-conflict"
	preexistingBlockerLastPane          preexistingDeadPaneBlockerReason = "last-pane-excluded"
)

type preexistingDeadPaneBlocker struct {
	Reason  preexistingDeadPaneBlockerReason
	PaneUID string
	Detail  string
}

func (e *preexistingDeadPaneBlocker) Error() string {
	pane := strings.TrimSpace(e.PaneUID)
	if pane == "" {
		pane = "<unresolved>"
	}
	return fmt.Sprintf("preexisting dead Agent Pane blocker (%s) pane=%s: %s", e.Reason, pane, e.Detail)
}

type preexistingDeadPaneCandidate struct {
	observation intmetadata.DeadPaneObservation
	pane        coremetadata.Pane
	agent       coremetadata.Agent
	supervisor  int
}

// reconcileOnePreexistingDeadAgentPane is the startup candidate producer. One
// call offers at most one exact candidate to the existing lifecycle
// transaction; controllerTriggerRunner's bounded repeat loop is the only batch
// mechanism, so no broad --all/name/cwd/order/age policy exists here.
func (r *controllerTriggerRunner) reconcileOnePreexistingDeadAgentPane(
	ctx context.Context,
	target tmuxTransport,
) (controllerPassResult, bool, error) {
	var pass controllerPassResult
	route, err := resolveControllerRuntimeMutationRoute(ctx, r.runner, target, func(string) string { return "" })
	if err != nil || route.authority == nil || route.authority.Class != runtimeMutationRouteApp {
		detail := "exact app socket authority is unavailable"
		if err != nil {
			detail = err.Error()
		}
		pass.refused = (&preexistingDeadPaneBlocker{Reason: preexistingBlockerForeignSocket, Detail: detail}).Error()
		return pass, true, nil
	}

	var inventory livePaneInventory
	if r.observe != nil {
		inventory = r.observe(target)
	} else {
		inventory = lifecycleInventory(r.runner, target)
	}
	liveCount, err := lifecycleObservedHostPaneCount(ctx, inventory, lifecycleDirtyEvent{teardownKind: coremetadata.TeardownEventPaneExited})
	if err != nil {
		pass.unobserved = (&preexistingDeadPaneBlocker{Reason: preexistingBlockerObservation, Detail: err.Error()}).Error()
		return pass, true, nil
	}
	if liveCount == 0 {
		pass.refused = (&preexistingDeadPaneBlocker{Reason: preexistingBlockerEmptyObservation, Detail: "exact app socket returned no Pane rows"}).Error()
		return pass, true, nil
	}
	deadInventory, ok := inventory.(lifecycleDeadPaneInventory)
	if !ok {
		pass.refused = (&preexistingDeadPaneBlocker{Reason: preexistingBlockerObservation, Detail: "positive dead Pane inventory is unavailable"}).Error()
		return pass, true, nil
	}
	dead, err := deadInventory.DeadPaneObservations(ctx)
	if err != nil {
		pass.unobserved = (&preexistingDeadPaneBlocker{Reason: preexistingBlockerObservation, Detail: err.Error()}).Error()
		return pass, true, nil
	}
	if len(dead) == 0 {
		return pass, false, nil
	}
	registry, err := r.store.load()
	if err != nil {
		return pass, true, MapMetadataError(err)
	}
	receipts, err := r.receipts.read()
	if err != nil {
		return pass, true, err
	}
	alive := r.processAlive
	if alive == nil {
		alive = notifyQueueEventProcessAlive
	}
	candidate, blocker := selectPreexistingDeadAgentPaneCandidate(registry, dead, receipts, alive, r.store.mutator())
	if blocker != nil {
		pass.refused = blocker.Error()
		return pass, true, nil
	}
	if candidate == nil {
		return pass, false, nil
	}

	exits, err := reconcileLifecycle(ctx, lifecycleDirtyEvent{
		target: target, paneUID: candidate.pane.Metadata.UID,
		runtimePaneID:       candidate.observation.PaneID,
		generation:          candidate.pane.Status.Activation.Generation,
		operationID:         candidate.pane.Status.Activation.OperationID,
		teardownKind:        coremetadata.TeardownEventPaneExited,
		receipts:            receipts,
		preexistingRecovery: true,
		supervisorPID:       candidate.supervisor,
		processAlive:        alive,
	}, inventory, r.store)
	if err != nil {
		var authority *preexistingDeadPaneBlocker
		if errorsAsPreexistingBlocker(err, &authority) {
			pass.refused = authority.Error()
			return pass, true, nil
		}
		// The shared lifecycle planner deliberately retains its established
		// retry error for hook-driven clean exits. At this startup-only seam the
		// same stable Project/Window/session-marker conflict is a closed typed
		// refusal: startup must not enqueue or widen it into absence recovery.
		if strings.Contains(err.Error(), "stable dead Pane authority conflict") {
			pass.refused = (&preexistingDeadPaneBlocker{
				Reason:  preexistingBlockerContainmentDrift,
				PaneUID: candidate.pane.Metadata.UID,
				Detail:  err.Error(),
			}).Error()
			return pass, true, nil
		}
		return pass, true, err
	}
	if exits.unobserved {
		pass.unobserved = exits.skipped
		return pass, true, nil
	}
	pass.residualExits = exits.changed()
	return pass, true, nil
}

// errorsAsPreexistingBlocker is kept tiny so the producer does not expose its
// private blocker type through a public command or transport shape.
func errorsAsPreexistingBlocker(err error, target **preexistingDeadPaneBlocker) bool {
	for err != nil {
		if blocker, ok := err.(*preexistingDeadPaneBlocker); ok {
			*target = blocker
			return true
		}
		type unwrapper interface{ Unwrap() error }
		unwrapped, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = unwrapped.Unwrap()
	}
	return false
}

func selectPreexistingDeadAgentPaneCandidate(
	registry coremetadata.Registry,
	dead []intmetadata.DeadPaneObservation,
	receipts []coremetadata.TerminationEvidence,
	processAlive func(int) bool,
	mutator coremetadata.Mutator,
) (*preexistingDeadPaneCandidate, *preexistingDeadPaneBlocker) {
	rows := slices.Clone(dead)
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].PaneUID != rows[j].PaneUID {
			return rows[i].PaneUID < rows[j].PaneUID
		}
		return rows[i].PaneID < rows[j].PaneID
	})
	seenUID, seenRuntime := map[string]bool{}, map[string]bool{}
	var candidates []preexistingDeadPaneCandidate
	for _, observed := range rows {
		uid := strings.TrimSpace(observed.PaneUID)
		pane, exists := registry.Pane(uid)
		if uid == "" {
			// A wholly unmirrored dead shell is outside Registry authority. Partial
			// managed mirrors are not: they are a foreign/ambiguous blocker.
			if observed.OwnerUID == "" && observed.AgentUID == "" && observed.WindowUID == "" && observed.ProjectUID == "" {
				continue
			}
			return nil, &preexistingDeadPaneBlocker{Reason: preexistingBlockerForeignPane, Detail: "dead Pane carries partial managed identity without a Pane UID"}
		}
		if !exists {
			return nil, &preexistingDeadPaneBlocker{Reason: preexistingBlockerForeignPane, PaneUID: uid, Detail: "dead Pane UID is not present in the current Registry"}
		}
		managedAgentShape := pane.Spec.Role == coremetadata.PaneRoleAgent || observed.PaneRole == string(coremetadata.PaneRoleAgent) || observed.OwnerKind == string(coremetadata.KindAgent)
		if !managedAgentShape {
			continue
		}
		if seenUID[uid] || seenRuntime[observed.PaneID] {
			return nil, &preexistingDeadPaneBlocker{Reason: preexistingBlockerAmbiguousPane, PaneUID: uid, Detail: "positive dead inventory repeats a stable UID or runtime locator"}
		}
		seenUID[uid], seenRuntime[observed.PaneID] = true, true
		owner := pane.Metadata.OwnerRef
		if owner == nil || owner.Kind != coremetadata.KindAgent || pane.Spec.Role != coremetadata.PaneRoleAgent ||
			observed.OwnerKind != string(coremetadata.KindAgent) || observed.OwnerUID != owner.UID || observed.AgentUID != owner.UID {
			return nil, &preexistingDeadPaneBlocker{Reason: preexistingBlockerOwnerDrift, PaneUID: uid, Detail: "Pane ownerRef/role and current owner mirrors do not form one Agent tuple"}
		}
		agent, ok := registry.Agent(owner.UID)
		if !ok || agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.PaneRef != uid {
			return nil, &preexistingDeadPaneBlocker{Reason: preexistingBlockerOwnerDrift, PaneUID: uid, Detail: "owner Agent is absent, terminal, or no longer binds this Pane"}
		}
		activation := pane.Status.Activation
		if strings.TrimSpace(activation.Generation) == "" || strings.TrimSpace(activation.OperationID) == "" ||
			activation.AgentUID != owner.UID {
			return nil, &preexistingDeadPaneBlocker{Reason: preexistingBlockerActivationDrift, PaneUID: uid, Detail: "current activation lacks exact Agent UID, generation, or operation"}
		}
		if activation.RuntimeID != strings.TrimSpace(observed.PaneID) || exactTmuxHandle(observed.PaneID, "%") == "" {
			return nil, &preexistingDeadPaneBlocker{Reason: preexistingBlockerLocatorDrift, PaneUID: uid, Detail: "current activation runtime locator does not equal the positive dead Pane"}
		}
		pid, err := strconv.Atoi(strings.TrimSpace(observed.PanePID))
		if err != nil || pid <= 0 || processAlive == nil {
			return nil, &preexistingDeadPaneBlocker{Reason: preexistingBlockerSupervisorUnknown, PaneUID: uid, Detail: "original supervisor PID observation is empty or unreadable"}
		}
		if processAlive(pid) {
			return nil, &preexistingDeadPaneBlocker{Reason: preexistingBlockerSupervisorActive, PaneUID: uid, Detail: fmt.Sprintf("original supervisor PID %d is still active", pid)}
		}
		if blocker := preflightPreexistingReceiptCompatibility(registry, *pane, *agent, receipts, mutator); blocker != nil {
			return nil, blocker
		}
		candidates = append(candidates, preexistingDeadPaneCandidate{observation: observed, pane: pane.Clone(), agent: agent.Clone(), supervisor: pid})
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	return &candidates[0], nil
}

func preflightPreexistingReceiptCompatibility(
	registry coremetadata.Registry,
	pane coremetadata.Pane,
	agent coremetadata.Agent,
	receipts []coremetadata.TerminationEvidence,
	mutator coremetadata.Mutator,
) *preexistingDeadPaneBlocker {
	activation := pane.Status.Activation
	var authoritative *coremetadata.TerminationEvidence
	if stored := pane.Status.LastTermination; stored != nil && stored.Generation == activation.Generation {
		if stored.PaneUID != pane.Metadata.UID || stored.AgentUID != agent.Metadata.UID || stored.OperationID != activation.OperationID {
			return &preexistingDeadPaneBlocker{Reason: preexistingBlockerReceiptConflict, PaneUID: pane.Metadata.UID, Detail: "stored current-generation receipt conflicts with activation identity"}
		}
		authoritative = stored.Clone()
	}
	for _, receipt := range receipts {
		sameActivationHint := receipt.PaneUID == pane.Metadata.UID ||
			(receipt.AgentUID == agent.Metadata.UID && receipt.Generation == activation.Generation && receipt.OperationID == activation.OperationID)
		if !sameActivationHint {
			continue
		}
		if receipt.PaneUID == pane.Metadata.UID && receipt.Generation != activation.Generation {
			// Append-only stale delivery is an expected no-op.
			continue
		}
		if receipt.PaneUID != pane.Metadata.UID || receipt.AgentUID != agent.Metadata.UID ||
			receipt.Generation != activation.Generation || receipt.OperationID != activation.OperationID {
			return &preexistingDeadPaneBlocker{Reason: preexistingBlockerReceiptConflict, PaneUID: pane.Metadata.UID, Detail: "journal receipt conflicts with Pane/Agent/generation/operation containment"}
		}
		probe := registry.Clone()
		outcome, err := mutator.RecordTermination(&probe, receipt)
		if err != nil || outcome.Stale {
			return &preexistingDeadPaneBlocker{Reason: preexistingBlockerReceiptConflict, PaneUID: pane.Metadata.UID, Detail: "journal receipt is incompatible with the current lifecycle contract"}
		}
		if authoritative != nil && !equalTerminationEvidence(*authoritative, receipt) {
			return &preexistingDeadPaneBlocker{Reason: preexistingBlockerReceiptConflict, PaneUID: pane.Metadata.UID, Detail: "current-generation receipts disagree on source, classification, or wait status"}
		}
		copy := receipt
		authoritative = copy.Clone()
	}
	return nil
}

func equalTerminationEvidence(left, right coremetadata.TerminationEvidence) bool {
	return left.Source == right.Source && left.Classification == right.Classification &&
		left.PaneUID == right.PaneUID && left.AgentUID == right.AgentUID && left.Generation == right.Generation &&
		left.Signal == right.Signal && left.OperationID == right.OperationID && equalOptionalInt(left.ExitCode, right.ExitCode)
}

func planPreexistingDeadAgentPaneRecovery(
	registry coremetadata.Registry,
	live map[string]bool,
	dead []intmetadata.DeadPaneObservation,
	observed intmetadata.DeadPaneObservation,
	pane coremetadata.Pane,
	windowUID string,
	window coremetadata.Window,
	root coremetadata.OwnerRef,
	sessionName string,
	event lifecycleDirtyEvent,
	mutator coremetadata.Mutator,
) (exactLifecycleCascadePlan, error) {
	pid, err := strconv.Atoi(strings.TrimSpace(observed.PanePID))
	if err != nil || pid <= 0 || pid != event.supervisorPID || event.processAlive == nil {
		return exactLifecycleCascadePlan{}, &preexistingDeadPaneBlocker{Reason: preexistingBlockerSupervisorUnknown, PaneUID: pane.Metadata.UID, Detail: "locked observation cannot reproduce the original supervisor PID"}
	}
	if event.processAlive(pid) {
		return exactLifecycleCascadePlan{}, &preexistingDeadPaneBlocker{Reason: preexistingBlockerSupervisorActive, PaneUID: pane.Metadata.UID, Detail: fmt.Sprintf("original supervisor PID %d became active before the first write", pid)}
	}
	agent, ok := registry.Agent(pane.Metadata.OwnerUID())
	if !ok || agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.PaneRef != pane.Metadata.UID ||
		pane.Status.Activation.AgentUID != agent.Metadata.UID || pane.Status.Activation.Generation != event.generation ||
		pane.Status.Activation.OperationID == "" || pane.Status.Activation.OperationID != event.operationID {
		return exactLifecycleCascadePlan{}, &preexistingDeadPaneBlocker{Reason: preexistingBlockerActivationDrift, PaneUID: pane.Metadata.UID, Detail: "locked owner/activation tuple drifted"}
	}
	if blocker := preflightPreexistingReceiptCompatibility(registry, pane, *agent, event.receipts, mutator); blocker != nil {
		return exactLifecycleCascadePlan{}, blocker
	}
	deadUIDs := lifecycleDeadPaneUIDs(dead)
	liveSibling := false
	for i := range registry.Panes {
		sibling := registry.Panes[i]
		if sibling.Metadata.UID == pane.Metadata.UID || !live[sibling.Metadata.UID] || deadUIDs[sibling.Metadata.UID] {
			continue
		}
		if ownerWindow, exists := paneWindowUID(registry, sibling); exists && ownerWindow == windowUID {
			liveSibling = true
			break
		}
	}
	if !liveSibling || window.Spec.AnchorPaneRef == pane.Metadata.UID {
		return exactLifecycleCascadePlan{}, &preexistingDeadPaneBlocker{Reason: preexistingBlockerLastPane, PaneUID: pane.Metadata.UID, Detail: "cleanup would replace the Window anchor or cross the last-Pane boundary"}
	}

	desired := registry.Clone()
	projection, err := mutator.ProjectTermination(&desired, coremetadata.TerminationProjectionInput{
		PaneUID: pane.Metadata.UID, Generation: pane.Status.Activation.Generation,
	})
	if err != nil {
		return exactLifecycleCascadePlan{}, err
	}
	if !projection.Changed || projection.AgentUID != agent.Metadata.UID || projection.Phase == coremetadata.PhaseRunning {
		return exactLifecycleCascadePlan{}, &preexistingDeadPaneBlocker{Reason: preexistingBlockerActivationDrift, PaneUID: pane.Metadata.UID, Detail: "existing lifecycle disposition did not release the Running Agent"}
	}
	projectedAgent, _ := desired.Agent(agent.Metadata.UID)
	if !reflect.DeepEqual(projectedAgent.Status.SessionRef, agent.Status.SessionRef) {
		return exactLifecycleCascadePlan{}, fmt.Errorf("preexisting dead Agent Pane recovery changed sessionRef")
	}
	evidencePane, _ := desired.Pane(pane.Metadata.UID)
	evidence := evidencePane.Status.LastTermination.Clone()
	if err := mutator.DeletePane(&desired, pane.Metadata.UID); err != nil {
		return exactLifecycleCascadePlan{}, err
	}
	// DeletePane's generic retained-Window repair may populate an optional
	// default-shell convenience ref. Startup recovery owns no Window write, and
	// the preflight proved the original anchor still resolves, so preserve the
	// exact source Window bytes instead of accepting that unrelated refinement.
	afterWindow, _ := desired.Window(windowUID)
	*afterWindow = window.Clone()
	if _, exists := desired.Pane(pane.Metadata.UID); exists {
		return exactLifecycleCascadePlan{}, fmt.Errorf("preexisting dead Agent Pane survived desired cleanup")
	}
	if after, exists := desired.Agent(agent.Metadata.UID); !exists || after.Status.Phase == coremetadata.PhaseRunning || after.Status.PaneRef != "" || !reflect.DeepEqual(after.Status.SessionRef, agent.Status.SessionRef) {
		return exactLifecycleCascadePlan{}, fmt.Errorf("preexisting dead Agent did not converge to terminal state")
	}
	if after, exists := desired.Window(windowUID); !exists || !reflect.DeepEqual(*after, window) {
		return exactLifecycleCascadePlan{}, &preexistingDeadPaneBlocker{Reason: preexistingBlockerLastPane, PaneUID: pane.Metadata.UID, Detail: "desired cleanup changed the owning Window"}
	}
	teardown := coremetadata.TeardownEvent{
		Kind:           coremetadata.TeardownEventPaneExited,
		Classification: evidence.Classification,
		Generation:     coremetadata.TeardownGenerationCurrent,
		Observation:    coremetadata.TeardownObservationExactSocket,
		Chain: coremetadata.TeardownOwnerChain{
			SocketIdentity: event.target.Label(), SessionHandle: observed.SessionID, PaneHandle: observed.PaneID,
			WindowHandle: observed.WindowID, PaneUID: pane.Metadata.UID, WindowUID: windowUID,
			RootKind: root.Kind, RootUID: root.UID, Generation: pane.Status.Activation.Generation,
		},
		LiveSiblingPane: true,
	}
	plan := coremetadata.PaneAgentCascadeDeletePlan{
		Decision: coremetadata.TeardownDecision{Action: coremetadata.TeardownDeletePaneAgent, Reason: coremetadata.TeardownReasonPaneTeardown},
		Desired:  desired, Changed: true, PaneUID: pane.Metadata.UID, AgentUID: agent.Metadata.UID,
		DeletedPanes: 1, Evidence: evidence,
	}
	target := lifecyclePreexistingDeadPaneTarget(teardown, sessionName, pane, pid)
	return exactLifecycleCascadePlan{Desired: desired, Changed: true, paneAgent: plan, deadCleanup: &target}, nil
}
