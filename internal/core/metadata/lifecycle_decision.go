package metadata

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// TeardownEventKind is the closed set of runtime topology events accepted by
// the automatic teardown authority kernel. Provider commands, pane contents,
// prompts, shell history, and transcripts are deliberately not inputs.
type TeardownEventKind string

const (
	TeardownEventPaneExited     TeardownEventKind = "pane-exited"
	TeardownEventWindowUnlinked TeardownEventKind = "window-unlinked"
)

// TeardownEventKinds returns the closed event vocabulary.
func TeardownEventKinds() []TeardownEventKind {
	return []TeardownEventKind{TeardownEventPaneExited, TeardownEventWindowUnlinked}
}

// TeardownGeneration classifies an event's generation guard.
type TeardownGeneration string

const (
	TeardownGenerationCurrent TeardownGeneration = "current"
	TeardownGenerationStale   TeardownGeneration = "stale"
)

// TeardownObservation says what the final inventory pass proved about the
// exact tmux server named by an event. Only ExactSocket is positive authority;
// every other value is an explicit fail-closed observation, not an absence.
type TeardownObservation string

const (
	TeardownObservationExactSocket      TeardownObservation = "exact-socket"
	TeardownObservationUnavailable      TeardownObservation = "unavailable"
	TeardownObservationEmpty            TeardownObservation = "empty"
	TeardownObservationNoServer         TeardownObservation = "no-server"
	TeardownObservationPermissionDenied TeardownObservation = "permission-denied"
	TeardownObservationSiblingSocket    TeardownObservation = "sibling-socket"
	TeardownObservationForeignHost      TeardownObservation = "foreign-host"
)

// TeardownObservations returns the closed final-observation vocabulary.
func TeardownObservations() []TeardownObservation {
	return []TeardownObservation{
		TeardownObservationExactSocket,
		TeardownObservationUnavailable,
		TeardownObservationEmpty,
		TeardownObservationNoServer,
		TeardownObservationPermissionDenied,
		TeardownObservationSiblingSocket,
		TeardownObservationForeignHost,
	}
}

// TeardownAction is the bounded Registry action an authority decision permits.
type TeardownAction string

const (
	TeardownRetain          TeardownAction = "retain"
	TeardownDeletePaneAgent TeardownAction = "delete-pane-agent"
	TeardownDeleteWindow    TeardownAction = "delete-window"
	TeardownRefuse          TeardownAction = "refuse"
)

// RootTeardownAction states what happens at the owner-root boundary.
type RootTeardownAction string

const (
	RootTeardownRetainProject        RootTeardownAction = "retain-project"
	RootTeardownDeleteProject        RootTeardownAction = "delete-project"
	RootTeardownRetainControlSession RootTeardownAction = "retain-control-session"
)

// ReopenIdentity states which Registry identity a subsequent open may use.
type ReopenIdentity string

const (
	ReopenIdentitySameProjectUID ReopenIdentity = "same-project-uid"
	ReopenIdentityNewProjectUID  ReopenIdentity = "new-project-uid"
	ReopenIdentityNotApplicable  ReopenIdentity = "not-applicable"
)

// AssetDisposition makes every external-asset cell explicit.
type AssetDisposition string

const (
	AssetPreserve      AssetDisposition = "preserve"
	AssetNotApplicable AssetDisposition = "not-applicable"
)

// ExternalAssetOutcome is the non-Registry boundary of a root decision.
type ExternalAssetOutcome struct {
	RootDirectory AssetDisposition
	GitMetadata   AssetDisposition
	Worktrees     AssetDisposition
	SnapshotBytes AssetDisposition
}

// TeardownReason is a closed diagnostic vocabulary for the decision table.
type TeardownReason string

const (
	TeardownReasonInvalidInput          TeardownReason = "invalid-input"
	TeardownReasonStaleGeneration       TeardownReason = "stale-generation"
	TeardownReasonUnavailable           TeardownReason = "observation-unavailable"
	TeardownReasonEmptyObservation      TeardownReason = "empty-observation"
	TeardownReasonNoServer              TeardownReason = "no-server"
	TeardownReasonPermissionDenied      TeardownReason = "permission-denied"
	TeardownReasonSiblingSocket         TeardownReason = "sibling-socket"
	TeardownReasonForeignHost           TeardownReason = "foreign-host"
	TeardownReasonNonCausalTermination  TeardownReason = "non-causal-termination"
	TeardownReasonPaneTeardown          TeardownReason = "pane-teardown"
	TeardownReasonAwaitingPaneExit      TeardownReason = "awaiting-pane-exit"
	TeardownReasonAwaitingWindowUnlink  TeardownReason = "awaiting-window-unlink"
	TeardownReasonLiveSiblingPane       TeardownReason = "live-sibling-pane"
	TeardownReasonWindowTeardown        TeardownReason = "window-teardown"
	TeardownReasonProjectTeardown       TeardownReason = "project-teardown"
	TeardownReasonMixedOwnerChain       TeardownReason = "mixed-owner-chain"
	TeardownReasonConflictingOwnerFacts TeardownReason = "conflicting-owner-facts"
	TeardownReasonStaleOwnerBinding     TeardownReason = "stale-owner-binding"
	// TeardownReasonDeadPaneCleanupRetry is the exact retryable boundary after
	// a clean current-generation decision but before the dead tmux Pane and its
	// managed Pane resource have converged. It never grants authority by itself;
	// the next pass must re-prove the same supervisor evidence, generation,
	// owner chain, socket, and dead mirror.
	TeardownReasonDeadPaneCleanupRetry TeardownReason = "exact-dead-pane-cleanup-retry"
)

// TeardownOwnerChain is the exact Registry chain an event claims.
type TeardownOwnerChain struct {
	SocketIdentity string
	SessionHandle  string
	PaneHandle     string
	WindowHandle   string
	PaneUID        string
	WindowUID      string
	RootKind       Kind
	RootUID        string
	Generation     string
}

// TeardownEvent is one bounded input to the pure authority kernel.
type TeardownEvent struct {
	Kind                  TeardownEventKind
	Classification        TerminationClassification
	Generation            TeardownGeneration
	Observation           TeardownObservation
	Chain                 TeardownOwnerChain
	LiveSiblingPane       bool
	LiveSiblingRootWindow bool
}

// TeardownDecision is one total decision-table cell.
type TeardownDecision struct {
	Action         TeardownAction
	RootAction     RootTeardownAction
	Reason         TeardownReason
	ExternalAssets ExternalAssetOutcome
	ReopenIdentity ReopenIdentity
}

func rootDefaults(kind Kind) (RootTeardownAction, ExternalAssetOutcome, ReopenIdentity, bool) {
	switch kind {
	case KindProject:
		return RootTeardownRetainProject, ExternalAssetOutcome{
			RootDirectory: AssetPreserve,
			GitMetadata:   AssetPreserve,
			Worktrees:     AssetPreserve,
			SnapshotBytes: AssetPreserve,
		}, ReopenIdentitySameProjectUID, true
	case KindControlSession:
		return RootTeardownRetainControlSession, ExternalAssetOutcome{
			RootDirectory: AssetNotApplicable,
			GitMetadata:   AssetNotApplicable,
			Worktrees:     AssetNotApplicable,
			SnapshotBytes: AssetNotApplicable,
		}, ReopenIdentityNotApplicable, true
	default:
		return RootTeardownRetainProject, ExternalAssetOutcome{
			RootDirectory: AssetNotApplicable,
			GitMetadata:   AssetNotApplicable,
			Worktrees:     AssetNotApplicable,
			SnapshotBytes: AssetNotApplicable,
		}, ReopenIdentityNotApplicable, false
	}
}

func validTeardownEventKind(kind TeardownEventKind) bool {
	return kind == TeardownEventPaneExited || kind == TeardownEventWindowUnlinked
}

func validTeardownObservation(observation TeardownObservation) bool {
	return slices.Contains(TeardownObservations(), observation)
}

func validOwnerChain(chain TeardownOwnerChain) bool {
	return strings.TrimSpace(chain.SocketIdentity) != "" &&
		strings.TrimSpace(chain.PaneHandle) != "" &&
		strings.TrimSpace(chain.WindowHandle) != "" &&
		strings.TrimSpace(chain.PaneUID) != "" &&
		strings.TrimSpace(chain.WindowUID) != "" &&
		strings.TrimSpace(chain.RootUID) != "" &&
		strings.TrimSpace(chain.Generation) != ""
}

func observationRefusal(observation TeardownObservation) TeardownReason {
	switch observation {
	case TeardownObservationUnavailable:
		return TeardownReasonUnavailable
	case TeardownObservationEmpty:
		return TeardownReasonEmptyObservation
	case TeardownObservationNoServer:
		return TeardownReasonNoServer
	case TeardownObservationPermissionDenied:
		return TeardownReasonPermissionDenied
	case TeardownObservationSiblingSocket:
		return TeardownReasonSiblingSocket
	case TeardownObservationForeignHost:
		return TeardownReasonForeignHost
	default:
		return TeardownReasonInvalidInput
	}
}

// DecideTeardownEvent evaluates one event without mutating Registry or runtime
// state. A window-unlinked event is never sufficient by itself: aggregation
// must pair it with the exact causal pane-exited event.
func DecideTeardownEvent(event TeardownEvent) TeardownDecision {
	rootAction, assets, reopen, validRoot := rootDefaults(event.Chain.RootKind)
	out := TeardownDecision{
		Action:         TeardownRefuse,
		RootAction:     rootAction,
		Reason:         TeardownReasonInvalidInput,
		ExternalAssets: assets,
		ReopenIdentity: reopen,
	}
	if !validRoot || !validTeardownEventKind(event.Kind) ||
		!ValidTerminationClassification(event.Classification) ||
		!validTeardownObservation(event.Observation) || !validOwnerChain(event.Chain) {
		return out
	}
	if event.Generation != TeardownGenerationCurrent && event.Generation != TeardownGenerationStale {
		return out
	}
	if event.Generation == TeardownGenerationStale {
		out.Reason = TeardownReasonStaleGeneration
		return out
	}
	if event.Observation != TeardownObservationExactSocket {
		out.Reason = observationRefusal(event.Observation)
		return out
	}
	if event.Classification != TerminationNormal && event.Classification != TerminationIntentional {
		out.Action = TeardownRetain
		out.Reason = TeardownReasonNonCausalTermination
		return out
	}
	if event.Kind == TeardownEventWindowUnlinked {
		out.Action = TeardownRetain
		out.Reason = TeardownReasonAwaitingPaneExit
		return out
	}
	out.Action = TeardownDeletePaneAgent
	out.Reason = TeardownReasonPaneTeardown
	if !event.LiveSiblingPane {
		out.Reason = TeardownReasonAwaitingWindowUnlink
	}
	return out
}

// AggregateTeardownEvents folds the two causal event kinds for one exact owner
// chain. It is intentionally insensitive to event delivery order.
func AggregateTeardownEvents(events []TeardownEvent) TeardownDecision {
	if len(events) == 0 {
		return genericTeardownRefusal(TeardownReasonInvalidInput)
	}
	first := events[0]
	rootAction, assets, reopen, _ := rootDefaults(first.Chain.RootKind)
	refused := TeardownDecision{Action: TeardownRefuse, RootAction: rootAction,
		Reason: TeardownReasonInvalidInput, ExternalAssets: assets, ReopenIdentity: reopen}
	for _, event := range events[1:] {
		if event.Chain != first.Chain {
			return genericTeardownRefusal(TeardownReasonMixedOwnerChain)
		}
		if event.LiveSiblingPane != first.LiveSiblingPane ||
			event.LiveSiblingRootWindow != first.LiveSiblingRootWindow {
			refused.Reason = TeardownReasonConflictingOwnerFacts
			return refused
		}
	}

	paneExit, windowUnlink := false, false
	terminalReason := TeardownReason("")
	terminalDecision := TeardownDecision{}
	for _, event := range events {
		decision := DecideTeardownEvent(event)
		if decision.Action == TeardownRefuse || decision.Reason == TeardownReasonNonCausalTermination {
			// Pick a stable reason instead of whichever failed event happened to
			// arrive first. Duplicate and adversarial permutations therefore
			// produce byte-identical plans.
			if terminalReason == "" || decision.Reason < terminalReason {
				terminalReason = decision.Reason
				terminalDecision = decision
			}
			continue
		}
		switch event.Kind {
		case TeardownEventPaneExited:
			paneExit = true
		case TeardownEventWindowUnlinked:
			windowUnlink = true
		}
	}
	if terminalReason != "" {
		return terminalDecision
	}
	if !paneExit {
		return TeardownDecision{Action: TeardownRetain, RootAction: rootAction,
			Reason: TeardownReasonAwaitingPaneExit, ExternalAssets: assets, ReopenIdentity: reopen}
	}
	if first.LiveSiblingPane {
		if windowUnlink {
			refused.Reason = TeardownReasonConflictingOwnerFacts
			return refused
		}
		return TeardownDecision{Action: TeardownDeletePaneAgent, RootAction: rootAction,
			Reason: TeardownReasonLiveSiblingPane, ExternalAssets: assets, ReopenIdentity: reopen}
	}
	if !windowUnlink {
		return TeardownDecision{Action: TeardownDeletePaneAgent, RootAction: rootAction,
			Reason: TeardownReasonAwaitingWindowUnlink, ExternalAssets: assets, ReopenIdentity: reopen}
	}

	// The pair is the bounded Window-close authority. The owning root is always
	// retained: an ordinary clean close removes desired Window topology, while
	// only explicit Project deletion or Fresh may retire the Project identity.
	return TeardownDecision{Action: TeardownDeleteWindow, RootAction: rootAction,
		Reason: TeardownReasonWindowTeardown, ExternalAssets: assets, ReopenIdentity: reopen}
}

func genericTeardownRefusal(reason TeardownReason) TeardownDecision {
	return TeardownDecision{
		Action: TeardownRefuse, RootAction: RootTeardownRetainProject, Reason: reason,
		ExternalAssets: ExternalAssetOutcome{
			RootDirectory: AssetNotApplicable, GitMetadata: AssetNotApplicable,
			Worktrees: AssetNotApplicable, SnapshotBytes: AssetNotApplicable,
		}, ReopenIdentity: ReopenIdentityNotApplicable,
	}
}

// ProjectCascadeDeletePlan is a pure desired-Registry plan. The source
// Registry is never mutated; applying the composite write is a later phase.
type ProjectCascadeDeletePlan struct {
	ProjectUID          string
	Root                string
	Desired             Registry
	Changed             bool
	DeletedProjects     int
	DeletedWindows      int
	DeletedPanes        int
	DeletedAgents       int
	DeletedReservations int
	ExternalAssets      ExternalAssetOutcome
	ReopenIdentity      ReopenIdentity
}

// PaneAgentCascadeDeletePlan is the desired-Registry plan for one qualifying
// exact pane-exited event. The Pane row is released while its owning Agent and
// Window identities survive. When it was the last descendant, the plan adds
// the minimum replacement shell in the same desired graph.
type PaneAgentCascadeDeletePlan struct {
	Decision      TeardownDecision
	Desired       Registry
	Changed       bool
	PaneUID       string
	AgentUID      string
	DeletedPanes  int
	DeletedAgents int
	Evidence      *TerminationEvidence
}

// PaneTeardownEvidencePlan persists the exact causal half of a last-Pane
// cascade while retaining the complete Registry graph. It performs no parent
// deletion until the matching window-unlinked event arrives.
type PaneTeardownEvidencePlan struct {
	Decision TeardownDecision
	Desired  Registry
	Changed  bool
	Evidence PaneTeardownEvidence
}

// WindowRootCascadeDeletePlan is the schema-valid desired Registry produced by
// one exact pane-exited/window-unlinked pair.
type WindowRootCascadeDeletePlan struct {
	Operation           ProjectLifecycleOperation
	Decision            TeardownDecision
	Desired             Registry
	Changed             bool
	PaneUID             string
	AgentUID            string
	WindowUID           string
	RootKind            Kind
	RootUID             string
	ProjectRoot         string
	DeletedProjects     int
	DeletedWindows      int
	DeletedPanes        int
	DeletedAgents       int
	DeletedReservations int
}

// PlanPaneAgentCascadeDelete converts one exact lifecycle decision into a
// schema-valid desired Registry without mutating the source document.
//
// The complete owner chain is revalidated here even when the caller already
// derived it from a fresh observation. This is the locked generation/owner
// guard: a late receipt or a resumed Agent cannot delete the binding that
// replaced the event's materialization.
func PlanPaneAgentCascadeDelete(registry Registry, event TeardownEvent, now time.Time) (PaneAgentCascadeDeletePlan, error) {
	const op = "plan pane agent cascade delete"
	out := PaneAgentCascadeDeletePlan{Decision: DecideTeardownEvent(event)}
	if out.Decision.Action != TeardownDeletePaneAgent {
		return out, nil
	}
	// A last-Pane event must keep the complete Registry subtree until its exact
	// window-unlinked half arrives. Deleting the Pane here would repair the
	// retained Window with a replacement shell and lose the causal fixed point.
	if !event.LiveSiblingPane {
		out.Decision.Action = TeardownRetain
		out.Decision.Reason = TeardownReasonAwaitingWindowUnlink
		return out, nil
	}
	if now.IsZero() {
		return PaneAgentCascadeDeletePlan{}, inputErr(op, ErrInvalidRegistry, "plan timestamp is required")
	}
	if err := registry.Validate(); err != nil {
		return PaneAgentCascadeDeletePlan{}, fmt.Errorf("%s source Registry: %w", op, err)
	}
	pane, ok := registry.Pane(strings.TrimSpace(event.Chain.PaneUID))
	if !ok || pane.Status.Activation.Generation != strings.TrimSpace(event.Chain.Generation) ||
		pane.Status.Activation.RuntimeID != strings.TrimSpace(event.Chain.PaneHandle) {
		out.Decision.Action = TeardownRefuse
		out.Decision.Reason = TeardownReasonStaleGeneration
		return out, nil
	}

	windowUID := ""
	agentUID := ""
	releasedSameGeneration := false
	if owner := pane.Metadata.OwnerRef; owner != nil && owner.Kind == KindAgent {
		agent, exists := registry.Agent(owner.UID)
		if !exists || agent.Metadata.OwnerRef == nil || agent.Metadata.OwnerRef.Kind != KindWindow {
			out.Decision.Action = TeardownRefuse
			out.Decision.Reason = TeardownReasonStaleOwnerBinding
			return out, nil
		}
		switch agent.Status.PaneRef {
		case pane.Metadata.UID:
			// The normal ordering: the exact dead observation reached the
			// lifecycle transaction before a generic absence projection released
			// the Agent binding.
		case "":
			// The fast ordering: a generic projection already released this
			// generation. Accept only the exact terminal half it left behind. A
			// resumed Agent always has a non-empty new binding and cannot enter
			// this exception.
			paneEvidence := pane.Status.LastTermination
			agentEvidence := agent.Status.LastTermination
			if agent.Status.Phase != PhaseOffline || paneEvidence == nil || agentEvidence == nil ||
				paneEvidence.Source != TerminationSourceSupervisor || paneEvidence.Classification != TerminationNormal ||
				paneEvidence.Generation != pane.Status.Activation.Generation || !sameEvidence(paneEvidence, agentEvidence) {
				out.Decision.Action = TeardownRefuse
				out.Decision.Reason = TeardownReasonStaleOwnerBinding
				return out, nil
			}
			releasedSameGeneration = true
		default:
			out.Decision.Action = TeardownRefuse
			out.Decision.Reason = TeardownReasonStaleOwnerBinding
			return out, nil
		}
		agentUID = agent.Metadata.UID
		windowUID = agent.Metadata.OwnerRef.UID
	} else if owner != nil && owner.Kind == KindWindow {
		windowUID = owner.UID
	} else {
		out.Decision.Action = TeardownRefuse
		out.Decision.Reason = TeardownReasonStaleOwnerBinding
		return out, nil
	}
	window, ok := registry.Window(windowUID)
	if !ok || window.Metadata.UID != event.Chain.WindowUID || window.Metadata.OwnerRef == nil ||
		window.Metadata.OwnerRef.Kind != event.Chain.RootKind || window.Metadata.OwnerRef.UID != event.Chain.RootUID {
		out.Decision.Action = TeardownRefuse
		out.Decision.Reason = TeardownReasonStaleOwnerBinding
		return out, nil
	}
	evidence := pane.Status.LastTermination
	if evidence == nil || evidence.Generation != pane.Status.Activation.Generation ||
		evidence.Classification != event.Classification || evidence.Source != TerminationSourceSupervisor ||
		evidence.Classification != TerminationNormal {
		out.Decision.Action = TeardownRefuse
		out.Decision.Reason = TeardownReasonStaleGeneration
		return out, nil
	}

	desired := registry.Clone()
	mutator := Mutator{Now: func() time.Time { return now.UTC() }}
	deletedPanes := 1
	retainedPhase := AgentPhase("")
	if agentUID != "" && !releasedSameGeneration {
		deletedPanes = 1
		exit := AgentExitNormal
		retainedPhase, _ = exit.Phase()
		if _, err := mutator.ReleaseAgentPane(&desired, agentUID, exit, string(event.Classification)); err != nil {
			return PaneAgentCascadeDeletePlan{}, err
		}
	} else if agentUID != "" {
		retainedPhase = PhaseOffline
		if err := mutator.DeletePane(&desired, pane.Metadata.UID); err != nil {
			return PaneAgentCascadeDeletePlan{}, err
		}
	} else if err := mutator.DeletePane(&desired, pane.Metadata.UID); err != nil {
		return PaneAgentCascadeDeletePlan{}, err
	}
	if err := desired.Validate(); err != nil {
		return PaneAgentCascadeDeletePlan{}, fmt.Errorf("%s desired Registry: %w", op, err)
	}
	if _, exists := desired.Pane(pane.Metadata.UID); exists {
		return PaneAgentCascadeDeletePlan{}, stateErr(op, ErrInvalidRegistry,
			"pane %q survived its delete plan", pane.Metadata.UID)
	}
	if agentUID != "" {
		if agent, exists := desired.Agent(agentUID); !exists || agent.Status.Phase != retainedPhase || agent.Status.PaneRef != "" {
			return PaneAgentCascadeDeletePlan{}, stateErr(op, ErrInvalidRegistry,
				"agent %q was not retained offline after pane release", agentUID)
		}
	}
	return PaneAgentCascadeDeletePlan{
		Decision: out.Decision, Desired: desired, Changed: true,
		PaneUID: pane.Metadata.UID, AgentUID: agentUID,
		DeletedPanes: deletedPanes, DeletedAgents: 0,
		Evidence: evidence.Clone(),
	}, nil
}

// PlanPaneTeardownEvidence records the exact clean last-Pane half while
// retaining the complete Window subtree. The matching window-unlinked event is
// the only consumer allowed to turn this receipt into Window deletion.
func PlanPaneTeardownEvidence(registry Registry, event TeardownEvent, now time.Time) (PaneTeardownEvidencePlan, error) {
	const op = "plan pane teardown evidence"
	out := PaneTeardownEvidencePlan{Decision: DecideTeardownEvent(event)}
	if out.Decision.Action != TeardownDeletePaneAgent || event.LiveSiblingPane ||
		out.Decision.Reason != TeardownReasonAwaitingWindowUnlink {
		return out, nil
	}
	if strings.TrimSpace(event.Chain.SessionHandle) == "" {
		out.Decision.Action = TeardownRefuse
		out.Decision.Reason = TeardownReasonInvalidInput
		return out, nil
	}
	if now.IsZero() {
		return PaneTeardownEvidencePlan{}, inputErr(op, ErrInvalidRegistry, "plan timestamp is required")
	}
	if err := registry.Validate(); err != nil {
		return PaneTeardownEvidencePlan{}, fmt.Errorf("%s source Registry: %w", op, err)
	}
	pane, ok := registry.Pane(strings.TrimSpace(event.Chain.PaneUID))
	if !ok || pane.Status.Activation.Generation != strings.TrimSpace(event.Chain.Generation) ||
		pane.Status.Activation.RuntimeID != strings.TrimSpace(event.Chain.PaneHandle) {
		out.Decision.Action = TeardownRefuse
		out.Decision.Reason = TeardownReasonStaleGeneration
		return out, nil
	}
	windowUID := ""
	if owner := pane.Metadata.OwnerRef; owner != nil && owner.Kind == KindAgent {
		agent, exists := registry.Agent(owner.UID)
		if !exists || agent.Metadata.OwnerRef == nil || agent.Metadata.OwnerRef.Kind != KindWindow {
			out.Decision.Action = TeardownRefuse
			out.Decision.Reason = TeardownReasonStaleOwnerBinding
			return out, nil
		}
		switch agent.Status.PaneRef {
		case pane.Metadata.UID:
		case "":
			paneEvidence := pane.Status.LastTermination
			agentEvidence := agent.Status.LastTermination
			if agent.Status.Phase != PhaseOffline || paneEvidence == nil || agentEvidence == nil ||
				paneEvidence.Source != TerminationSourceSupervisor || paneEvidence.Classification != TerminationNormal ||
				paneEvidence.Generation != pane.Status.Activation.Generation || !sameEvidence(paneEvidence, agentEvidence) {
				out.Decision.Action = TeardownRefuse
				out.Decision.Reason = TeardownReasonStaleOwnerBinding
				return out, nil
			}
		default:
			out.Decision.Action = TeardownRefuse
			out.Decision.Reason = TeardownReasonStaleOwnerBinding
			return out, nil
		}
		windowUID = agent.Metadata.OwnerRef.UID
	} else if owner != nil && owner.Kind == KindWindow {
		windowUID = owner.UID
	} else {
		out.Decision.Action = TeardownRefuse
		out.Decision.Reason = TeardownReasonStaleOwnerBinding
		return out, nil
	}
	if windowUID != strings.TrimSpace(event.Chain.WindowUID) {
		out.Decision.Action = TeardownRefuse
		out.Decision.Reason = TeardownReasonStaleOwnerBinding
		return out, nil
	}
	window, ok := registry.Window(windowUID)
	if !ok || window.Metadata.OwnerRef == nil || window.Metadata.OwnerRef.Kind != event.Chain.RootKind ||
		window.Metadata.OwnerRef.UID != event.Chain.RootUID ||
		window.Status.RuntimeSessionID != strings.TrimSpace(event.Chain.SessionHandle) ||
		window.Status.RuntimeID != strings.TrimSpace(event.Chain.WindowHandle) {
		out.Decision.Action = TeardownRefuse
		out.Decision.Reason = TeardownReasonStaleOwnerBinding
		return out, nil
	}
	termination := pane.Status.LastTermination
	if termination == nil || termination.Source != TerminationSourceSupervisor ||
		termination.Classification != TerminationNormal ||
		termination.Generation != event.Chain.Generation || termination.Classification != event.Classification {
		out.Decision.Action = TeardownRefuse
		out.Decision.Reason = TeardownReasonStaleGeneration
		return out, nil
	}
	evidence := PaneTeardownEvidence{
		SocketIdentity: strings.TrimSpace(event.Chain.SocketIdentity), RuntimeSessionID: strings.TrimSpace(event.Chain.SessionHandle),
		RuntimePaneID: strings.TrimSpace(event.Chain.PaneHandle), RuntimeWindowID: strings.TrimSpace(event.Chain.WindowHandle),
		WindowUID: windowUID, RootKind: event.Chain.RootKind, RootUID: event.Chain.RootUID,
		Generation: strings.TrimSpace(event.Chain.Generation), Classification: event.Classification,
		ObservedAt: now.UTC(),
	}
	desired := registry.Clone()
	stored, _ := desired.Pane(pane.Metadata.UID)
	if prior := stored.Status.Teardown; prior != nil &&
		prior.SocketIdentity == evidence.SocketIdentity && prior.RuntimeSessionID == evidence.RuntimeSessionID &&
		prior.RuntimePaneID == evidence.RuntimePaneID && prior.RuntimeWindowID == evidence.RuntimeWindowID &&
		prior.WindowUID == evidence.WindowUID && prior.RootKind == evidence.RootKind && prior.RootUID == evidence.RootUID &&
		prior.Generation == evidence.Generation && prior.Classification == evidence.Classification {
		out.Desired = desired
		out.Evidence = *prior.Clone()
		return out, nil
	}
	stored.Status.Teardown = evidence.Clone()
	desired.UpdatedAt = now.UTC()
	if err := desired.Validate(); err != nil {
		return PaneTeardownEvidencePlan{}, fmt.Errorf("%s desired Registry: %w", op, err)
	}
	return PaneTeardownEvidencePlan{Decision: out.Decision, Desired: desired, Changed: true, Evidence: evidence}, nil
}

// PlanWindowRootCascadeDelete consumes one stored exact Pane receipt together
// with its matching window-unlinked event. It deletes only that Window subtree;
// Project and ControlSession roots always survive.
func PlanWindowRootCascadeDelete(registry Registry, paneEvent, unlinkEvent TeardownEvent, now time.Time) (WindowRootCascadeDeletePlan, error) {
	const op = "plan window root cascade delete"
	decision := AggregateTeardownEvents([]TeardownEvent{paneEvent, unlinkEvent})
	out := WindowRootCascadeDeletePlan{Decision: decision}
	if decision.Action != TeardownDeleteWindow {
		return out, nil
	}
	if now.IsZero() {
		return WindowRootCascadeDeletePlan{}, inputErr(op, ErrInvalidRegistry, "plan timestamp is required")
	}
	if err := registry.Validate(); err != nil {
		return WindowRootCascadeDeletePlan{}, fmt.Errorf("%s source Registry: %w", op, err)
	}
	pane, ok := registry.Pane(strings.TrimSpace(paneEvent.Chain.PaneUID))
	if !ok || pane.Status.Teardown == nil {
		out.Decision.Action = TeardownRefuse
		out.Decision.Reason = TeardownReasonStaleGeneration
		return out, nil
	}
	// A durable receipt is not timeless authority. If its Agent has since
	// resumed on a different Pane, the old Pane/Window chain is stale even when
	// all tmux handles in the receipt still compare equal. The only released
	// binding accepted here is the exact same-generation terminal fixed point
	// established by Phase 0.
	if owner := pane.Metadata.OwnerRef; owner != nil && owner.Kind == KindAgent {
		agent, exists := registry.Agent(owner.UID)
		if !exists || agent.Metadata.OwnerRef == nil || agent.Metadata.OwnerRef.Kind != KindWindow {
			out.Decision.Action = TeardownRefuse
			out.Decision.Reason = TeardownReasonStaleOwnerBinding
			return out, nil
		}
		switch agent.Status.PaneRef {
		case pane.Metadata.UID:
		case "":
			paneTermination := pane.Status.LastTermination
			agentTermination := agent.Status.LastTermination
			if agent.Status.Phase != PhaseOffline || paneTermination == nil || agentTermination == nil ||
				paneTermination.Source != TerminationSourceSupervisor || paneTermination.Classification != TerminationNormal ||
				paneTermination.Generation != pane.Status.Activation.Generation || !sameEvidence(paneTermination, agentTermination) {
				out.Decision.Action = TeardownRefuse
				out.Decision.Reason = TeardownReasonStaleOwnerBinding
				return out, nil
			}
		default:
			out.Decision.Action = TeardownRefuse
			out.Decision.Reason = TeardownReasonStaleOwnerBinding
			return out, nil
		}
	}
	evidence := pane.Status.Teardown
	if evidence.SocketIdentity != paneEvent.Chain.SocketIdentity ||
		evidence.RuntimeSessionID != paneEvent.Chain.SessionHandle ||
		evidence.RuntimePaneID != paneEvent.Chain.PaneHandle ||
		evidence.RuntimeWindowID != paneEvent.Chain.WindowHandle ||
		evidence.WindowUID != paneEvent.Chain.WindowUID || evidence.RootKind != paneEvent.Chain.RootKind ||
		evidence.RootUID != paneEvent.Chain.RootUID || evidence.Generation != paneEvent.Chain.Generation ||
		evidence.Classification != paneEvent.Classification {
		out.Decision.Action = TeardownRefuse
		out.Decision.Reason = TeardownReasonStaleOwnerBinding
		return out, nil
	}
	window, ok := registry.Window(paneEvent.Chain.WindowUID)
	if !ok || window.Metadata.OwnerRef == nil || window.Metadata.OwnerRef.Kind != paneEvent.Chain.RootKind ||
		window.Metadata.OwnerRef.UID != paneEvent.Chain.RootUID {
		out.Decision.Action = TeardownRefuse
		out.Decision.Reason = TeardownReasonStaleOwnerBinding
		return out, nil
	}
	desired := registry.Clone()
	mutator := Mutator{Now: func() time.Time { return now.UTC() }}
	deletedPanes := len(registry.PanesOf(window.Metadata.UID))
	deletedAgents := len(registry.AgentsOf(window.Metadata.UID))
	for _, agent := range registry.AgentsOf(window.Metadata.UID) {
		deletedPanes += len(registry.PanesOf(agent.Metadata.UID))
	}
	reservationsBefore := len(registry.NameReservations)
	if err := mutator.DeleteWindow(&desired, window.Metadata.UID); err != nil {
		return WindowRootCascadeDeletePlan{}, err
	}
	if err := desired.Validate(); err != nil {
		return WindowRootCascadeDeletePlan{}, fmt.Errorf("%s desired Registry: %w", op, err)
	}
	return WindowRootCascadeDeletePlan{
		Operation: ProjectLifecycleOperationCloseWindow,
		Decision:  decision, Desired: desired, Changed: true, PaneUID: pane.Metadata.UID,
		WindowUID: window.Metadata.UID, RootKind: paneEvent.Chain.RootKind, RootUID: paneEvent.Chain.RootUID,
		DeletedWindows: 1, DeletedPanes: deletedPanes, DeletedAgents: deletedAgents,
		DeletedReservations: reservationsBefore - len(desired.NameReservations),
	}, nil
}

// PlanProjectCascadeDelete removes one Project graph on a clone and returns the
// schema-valid desired Registry without touching filesystem or snapshot data.
func PlanProjectCascadeDelete(registry Registry, projectUID string, now time.Time) (ProjectCascadeDeletePlan, error) {
	const op = "plan project cascade delete"
	if now.IsZero() {
		return ProjectCascadeDeletePlan{}, inputErr(op, ErrInvalidRegistry, "plan timestamp is required")
	}
	if err := registry.Validate(); err != nil {
		return ProjectCascadeDeletePlan{}, fmt.Errorf("%s source Registry: %w", op, err)
	}
	project, ok := registry.Project(strings.TrimSpace(projectUID))
	if !ok {
		return ProjectCascadeDeletePlan{}, stateErr(op, ErrNotFound, "Project %q does not exist", projectUID)
	}
	desired := registry.Clone()
	if err := (Mutator{Now: func() time.Time { return now.UTC() }}).DeleteProject(&desired, project.Metadata.UID); err != nil {
		return ProjectCascadeDeletePlan{}, err
	}
	if err := desired.Validate(); err != nil {
		return ProjectCascadeDeletePlan{}, fmt.Errorf("%s desired Registry: %w", op, err)
	}
	return ProjectCascadeDeletePlan{
		ProjectUID: project.Metadata.UID, Root: project.Spec.Root, Desired: desired,
		Changed: true, DeletedProjects: 1,
		DeletedWindows:      len(registry.WindowsOf(project.Metadata.UID)),
		DeletedPanes:        len(registry.projectPanes(project.Metadata.UID)),
		DeletedAgents:       projectAgentCount(registry, project.Metadata.UID),
		DeletedReservations: len(registry.NameReservations) - len(desired.NameReservations),
		ExternalAssets: ExternalAssetOutcome{
			RootDirectory: AssetPreserve, GitMetadata: AssetPreserve,
			Worktrees: AssetPreserve, SnapshotBytes: AssetPreserve,
		},
		ReopenIdentity: ReopenIdentityNewProjectUID,
	}, nil
}

func projectAgentCount(registry Registry, projectUID string) int {
	count := 0
	for _, window := range registry.WindowsOf(projectUID) {
		count += len(registry.AgentsOf(window.Metadata.UID))
	}
	return count
}

// ProjectReopenState is the closed startup state table.
type ProjectReopenState string

const (
	ProjectReopenLive                   ProjectReopenState = "live"
	ProjectReopenClosed                 ProjectReopenState = "closed"
	ProjectReopenDeletedWithSnapshot    ProjectReopenState = "deleted-with-snapshot"
	ProjectReopenDeletedWithoutSnapshot ProjectReopenState = "deleted-without-snapshot"
)

// ProjectOpenAction is the user's one-step startup choice.
type ProjectOpenAction string

const (
	ProjectOpenContinue ProjectOpenAction = "continue"
	ProjectOpenFresh    ProjectOpenAction = "open-fresh"
)

// ProjectOpenSource is the authority from which topology is opened.
type ProjectOpenSource string

const (
	ProjectOpenSourceLiveRuntime      ProjectOpenSource = "live-runtime"
	ProjectOpenSourceRegistryTopology ProjectOpenSource = "registry-topology"
	ProjectOpenSourceSnapshot         ProjectOpenSource = "snapshot"
	ProjectOpenSourceRoot             ProjectOpenSource = "filesystem-root"
	ProjectOpenSourceNone             ProjectOpenSource = "none"
)

// ProjectStartupWrite is one member of an atomic startup write set.
type ProjectStartupWrite string

const (
	ProjectStartupWriteNone                  ProjectStartupWrite = "no-write"
	ProjectStartupWriteStopRuntime           ProjectStartupWrite = "stop-managed-runtime"
	ProjectStartupWriteMaterializeRegistry   ProjectStartupWrite = "materialize-registry-topology"
	ProjectStartupWriteDeleteProjectGraph    ProjectStartupWrite = "delete-existing-project-graph"
	ProjectStartupWriteCreateProject         ProjectStartupWrite = "create-project-with-new-uid"
	ProjectStartupWriteCreateCanonicalWindow ProjectStartupWrite = "create-canonical-window"
	ProjectStartupWriteCreateCanonicalShell  ProjectStartupWrite = "create-canonical-shell"
	ProjectStartupWriteRestoreSnapshotGraph  ProjectStartupWrite = "restore-snapshot-topology"
)

// ProjectLifecycleState is the desired Project shape at the lifecycle/startup
// boundary. Runtime liveness is deliberately absent: a missing tmux server is
// observation, never Project-delete authority.
type ProjectLifecycleState string

const (
	ProjectLifecycleRetainedWindows ProjectLifecycleState = "retained-window"
	ProjectLifecycleZeroWindows     ProjectLifecycleState = "zero-window"
	ProjectLifecycleDeleted         ProjectLifecycleState = "deleted"
)

// ProjectLifecycleAction is the complete user-intent vocabulary at this
// boundary. Keeping one action on one plan makes stop, ordinary Window close,
// Project unregister, and Fresh replacement impossible to conflate.
type ProjectLifecycleAction string

const (
	ProjectLifecycleStop          ProjectLifecycleAction = "stop"
	ProjectLifecycleContinue      ProjectLifecycleAction = "continue"
	ProjectLifecycleFresh         ProjectLifecycleAction = "fresh"
	ProjectLifecycleDeleteProject ProjectLifecycleAction = "delete-project"
)

// ProjectLifecycleOperation is the single mutation class carried by an
// executable plan. close-window is produced only by the causal Window plan;
// the four Project actions can therefore be compared without sharing effects.
type ProjectLifecycleOperation string

const (
	ProjectLifecycleOperationNone          ProjectLifecycleOperation = "none"
	ProjectLifecycleOperationStop          ProjectLifecycleOperation = "stop"
	ProjectLifecycleOperationContinue      ProjectLifecycleOperation = "continue"
	ProjectLifecycleOperationFresh         ProjectLifecycleOperation = "fresh"
	ProjectLifecycleOperationDeleteProject ProjectLifecycleOperation = "delete-project"
	ProjectLifecycleOperationCloseWindow   ProjectLifecycleOperation = "close-window"
)

type ProjectUIDOutcome string

const (
	ProjectUIDPreserved ProjectUIDOutcome = "preserved"
	ProjectUIDReplaced  ProjectUIDOutcome = "replaced"
	ProjectUIDCreated   ProjectUIDOutcome = "created"
	ProjectUIDRemoved   ProjectUIDOutcome = "removed"
	ProjectUIDAbsent    ProjectUIDOutcome = "absent"
)

type ProjectDescendantUIDOutcome string

const (
	ProjectDescendantUIDsPreserved ProjectDescendantUIDOutcome = "preserved"
	ProjectDescendantUIDsCreated   ProjectDescendantUIDOutcome = "created"
	ProjectDescendantUIDsReplaced  ProjectDescendantUIDOutcome = "replaced"
	ProjectDescendantUIDsRemoved   ProjectDescendantUIDOutcome = "removed"
	ProjectDescendantUIDsAbsent    ProjectDescendantUIDOutcome = "absent"
)

// ProjectLifecyclePlan is one cell of the retained/zero/deleted ×
// Stop/Continue/Fresh/delete table. AtomicWriteSet names Registry/runtime
// effects; a no-op is explicit rather than represented by a blank cell.
type ProjectLifecyclePlan struct {
	State          ProjectLifecycleState
	Action         ProjectLifecycleAction
	Operation      ProjectLifecycleOperation
	Available      bool
	ProjectUID     ProjectUIDOutcome
	DescendantUIDs ProjectDescendantUIDOutcome
	AtomicWriteSet []ProjectStartupWrite
	ExternalAssets ExternalAssetOutcome
	Reason         string
}

// ProjectLifecyclePreconditions carries external evidence required by one
// cell without widening the closed three-state table. A usable snapshot is
// relevant only to deleted+Continue; runtime absence is deliberately not a
// precondition because it never grants Project identity authority.
type ProjectLifecyclePreconditions struct {
	UsableSnapshot bool
}

// DecideProjectLifecycle returns the single lifecycle/startup state table.
// Ordinary clean Window close is intentionally not an action in this table: it
// is owned by the causal Window-close plan and never appears in these write
// sets.
func DecideProjectLifecycle(state ProjectLifecycleState, action ProjectLifecycleAction, preconditions ProjectLifecyclePreconditions) ProjectLifecyclePlan {
	plan := ProjectLifecyclePlan{
		State: state, Action: action, Available: true,
		Operation:      lifecycleOperationForAction(action),
		ExternalAssets: projectPreservedAssets(),
	}
	switch state {
	case ProjectLifecycleRetainedWindows:
		switch action {
		case ProjectLifecycleStop:
			plan.ProjectUID, plan.DescendantUIDs = ProjectUIDPreserved, ProjectDescendantUIDsPreserved
			plan.AtomicWriteSet, plan.Reason = []ProjectStartupWrite{ProjectStartupWriteStopRuntime}, "stop-runtime-preserve-desired-graph"
		case ProjectLifecycleContinue:
			plan.ProjectUID, plan.DescendantUIDs = ProjectUIDPreserved, ProjectDescendantUIDsPreserved
			plan.AtomicWriteSet, plan.Reason = []ProjectStartupWrite{ProjectStartupWriteMaterializeRegistry}, "materialize-same-desired-graph"
		case ProjectLifecycleFresh:
			plan.ProjectUID, plan.DescendantUIDs = ProjectUIDReplaced, ProjectDescendantUIDsReplaced
			plan.AtomicWriteSet, plan.Reason = freshReplacementWriteSet(), "atomically-replace-project-identity"
		case ProjectLifecycleDeleteProject:
			plan.ProjectUID, plan.DescendantUIDs = ProjectUIDRemoved, ProjectDescendantUIDsRemoved
			plan.AtomicWriteSet, plan.Reason = []ProjectStartupWrite{ProjectStartupWriteDeleteProjectGraph}, "explicitly-unregister-project"
		default:
			return unavailableProjectLifecyclePlan(state, action, "invalid-action")
		}
	case ProjectLifecycleZeroWindows:
		switch action {
		case ProjectLifecycleStop:
			plan.ProjectUID, plan.DescendantUIDs = ProjectUIDPreserved, ProjectDescendantUIDsAbsent
			plan.AtomicWriteSet, plan.Reason = []ProjectStartupWrite{ProjectStartupWriteNone}, "runtime-already-absent"
		case ProjectLifecycleContinue:
			plan.ProjectUID, plan.DescendantUIDs = ProjectUIDPreserved, ProjectDescendantUIDsCreated
			plan.AtomicWriteSet = []ProjectStartupWrite{ProjectStartupWriteCreateCanonicalWindow, ProjectStartupWriteCreateCanonicalShell}
			plan.Reason = "bootstrap-new-canonical-descendants"
		case ProjectLifecycleFresh:
			plan.ProjectUID, plan.DescendantUIDs = ProjectUIDReplaced, ProjectDescendantUIDsCreated
			plan.AtomicWriteSet, plan.Reason = freshReplacementWriteSet(), "atomically-replace-zero-window-project"
		case ProjectLifecycleDeleteProject:
			plan.ProjectUID, plan.DescendantUIDs = ProjectUIDRemoved, ProjectDescendantUIDsAbsent
			plan.AtomicWriteSet, plan.Reason = []ProjectStartupWrite{ProjectStartupWriteDeleteProjectGraph}, "explicitly-unregister-zero-window-project"
		default:
			return unavailableProjectLifecyclePlan(state, action, "invalid-action")
		}
	case ProjectLifecycleDeleted:
		switch action {
		case ProjectLifecycleContinue:
			if !preconditions.UsableSnapshot {
				return unavailableProjectLifecyclePlan(state, action, "no-usable-snapshot")
			}
			plan.ProjectUID, plan.DescendantUIDs = ProjectUIDCreated, ProjectDescendantUIDsCreated
			plan.AtomicWriteSet = []ProjectStartupWrite{ProjectStartupWriteCreateProject, ProjectStartupWriteRestoreSnapshotGraph}
			plan.Reason = "restore-usable-snapshot-under-new-identity"
		case ProjectLifecycleFresh:
			plan.ProjectUID, plan.DescendantUIDs = ProjectUIDCreated, ProjectDescendantUIDsCreated
			plan.AtomicWriteSet = []ProjectStartupWrite{ProjectStartupWriteCreateProject, ProjectStartupWriteCreateCanonicalWindow, ProjectStartupWriteCreateCanonicalShell}
			plan.Reason = "create-fresh-project"
		case ProjectLifecycleStop, ProjectLifecycleDeleteProject:
			return unavailableProjectLifecyclePlan(state, action, "project-is-not-registered")
		default:
			return unavailableProjectLifecyclePlan(state, action, "invalid-action")
		}
	default:
		return unavailableProjectLifecyclePlan(state, action, "invalid-state")
	}
	return plan
}

func freshReplacementWriteSet() []ProjectStartupWrite {
	return []ProjectStartupWrite{
		ProjectStartupWriteDeleteProjectGraph,
		ProjectStartupWriteCreateProject,
		ProjectStartupWriteCreateCanonicalWindow,
		ProjectStartupWriteCreateCanonicalShell,
	}
}

func lifecycleOperationForAction(action ProjectLifecycleAction) ProjectLifecycleOperation {
	switch action {
	case ProjectLifecycleStop:
		return ProjectLifecycleOperationStop
	case ProjectLifecycleContinue:
		return ProjectLifecycleOperationContinue
	case ProjectLifecycleFresh:
		return ProjectLifecycleOperationFresh
	case ProjectLifecycleDeleteProject:
		return ProjectLifecycleOperationDeleteProject
	default:
		return ProjectLifecycleOperationNone
	}
}

func unavailableProjectLifecyclePlan(state ProjectLifecycleState, action ProjectLifecycleAction, reason string) ProjectLifecyclePlan {
	return ProjectLifecyclePlan{
		State: state, Action: action, Available: false,
		Operation:  lifecycleOperationForAction(action),
		ProjectUID: ProjectUIDAbsent, DescendantUIDs: ProjectDescendantUIDsAbsent,
		AtomicWriteSet: []ProjectStartupWrite{ProjectStartupWriteNone},
		ExternalAssets: projectPreservedAssets(), Reason: reason,
	}
}

// ProjectFreshReplacementPlan is the complete Registry preimage and desired
// result for one Fresh commit. The caller commits Desired atomically; any plan
// or store failure retains Preimage byte-for-byte.
type ProjectFreshReplacementPlan struct {
	Operation     ProjectLifecycleOperation
	OldProjectUID string
	NewProjectUID string
	Preimage      Registry
	Desired       Registry
	WriteSet      []ProjectStartupWrite
}

// PlanProjectFreshReplacement removes one exact Project graph from a clone and
// registers a new canonical Project/Window/shell graph for the same root. It
// never mutates registry, filesystem, Git/worktree state, or snapshots.
func PlanProjectFreshReplacement(registry Registry, projectUID string, opts RegisterProjectOptions, mutator Mutator) (ProjectFreshReplacementPlan, error) {
	project, ok := registry.Project(strings.TrimSpace(projectUID))
	if !ok {
		return ProjectFreshReplacementPlan{}, stateErr("fresh project", ErrNotFound, "Project %q does not exist", projectUID)
	}
	state := ProjectLifecycleZeroWindows
	if len(registry.WindowsOf(project.Metadata.UID)) != 0 {
		state = ProjectLifecycleRetainedWindows
	}
	cell := DecideProjectLifecycle(state, ProjectLifecycleFresh, ProjectLifecyclePreconditions{})
	if !cell.Available {
		return ProjectFreshReplacementPlan{}, stateErr("fresh project", ErrInvalidRegistry, "Fresh is unavailable for state %q", state)
	}
	plan := ProjectFreshReplacementPlan{
		Operation:     ProjectLifecycleOperationFresh,
		OldProjectUID: project.Metadata.UID,
		Preimage:      registry.Clone(),
		WriteSet:      slices.Clone(cell.AtomicWriteSet),
	}

	desired := registry.Clone()
	if err := mutator.DeleteProject(&desired, project.Metadata.UID); err != nil {
		return plan, err
	}
	opts.Root = project.Spec.Root
	if strings.TrimSpace(opts.Name) == "" {
		opts.Name = project.Metadata.Name
	}
	if strings.TrimSpace(opts.DisplayName) == "" {
		opts.DisplayName = project.Metadata.DisplayName
	}
	if opts.Labels == nil {
		opts.Labels = cloneStringMap(project.Metadata.Labels)
	}
	if opts.Annotations == nil {
		opts.Annotations = cloneStringMap(project.Metadata.Annotations)
	}
	uidSource := mutator.NewUID
	if uidSource == nil {
		uidSource = NewUID
	}
	mutator.NewUID = func(kind Kind) (string, error) {
		uid, err := uidSource(kind)
		if err == nil && kind == KindProject && plan.NewProjectUID == "" {
			plan.NewProjectUID = strings.TrimSpace(uid)
		}
		return uid, err
	}
	registered, err := mutator.RegisterProject(&desired, opts)
	if err != nil {
		return plan, err
	}
	plan.NewProjectUID = registered.Project.Metadata.UID
	if registered.Project.Metadata.UID == project.Metadata.UID {
		return plan, stateErr("fresh project", ErrInvalidRegistry, "Fresh reused old Project uid %q", project.Metadata.UID)
	}
	claimants := 0
	for _, candidate := range desired.Projects {
		if candidate.Spec.Root == project.Spec.Root {
			claimants++
		}
	}
	if claimants != 1 {
		return plan, stateErr("fresh project", ErrInvalidRegistry, "root %q has %d Project claimants after replacement", project.Spec.Root, claimants)
	}
	if err := desired.Validate(); err != nil {
		return plan, err
	}
	plan.Desired = desired
	return plan, nil
}

// ProjectOpenReason makes unavailable and invalid cells non-ambiguous.
type ProjectOpenReason string

const (
	ProjectOpenReasonAttachLive        ProjectOpenReason = "attach-live-project"
	ProjectOpenReasonMaterializeClosed ProjectOpenReason = "materialize-closed-project"
	ProjectOpenReasonRestoreSnapshot   ProjectOpenReason = "restore-usable-snapshot"
	ProjectOpenReasonNoSnapshot        ProjectOpenReason = "no-usable-snapshot"
	ProjectOpenReasonFreshReplace      ProjectOpenReason = "replace-existing-project"
	ProjectOpenReasonFreshCreate       ProjectOpenReason = "create-fresh-project"
	ProjectOpenReasonInvalid           ProjectOpenReason = "invalid-state-or-action"
)

// ProjectOpenPlan is one total startup state-table cell.
type ProjectOpenPlan struct {
	Available         bool
	Source            ProjectOpenSource
	AtomicWriteSet    []ProjectStartupWrite
	Reason            ProjectOpenReason
	NewProjectUID     bool
	AdditionalConfirm bool
	ExternalAssets    ExternalAssetOutcome
}

func projectPreservedAssets() ExternalAssetOutcome {
	return ExternalAssetOutcome{RootDirectory: AssetPreserve, GitMetadata: AssetPreserve,
		Worktrees: AssetPreserve, SnapshotBytes: AssetPreserve}
}

// DecideProjectOpen is the compatibility projection used by the older
// live/closed/deleted-with-snapshot UI classifier. It adds runtime/source labels
// only; Fresh and Continue identity outcomes and write sets always come from
// DecideProjectLifecycle. Unavailable Continue never silently falls back to
// Fresh.
func DecideProjectOpen(state ProjectReopenState, action ProjectOpenAction) ProjectOpenPlan {
	assets := projectPreservedAssets()
	invalid := ProjectOpenPlan{Available: false, Source: ProjectOpenSourceNone,
		AtomicWriteSet: []ProjectStartupWrite{ProjectStartupWriteNone},
		Reason:         ProjectOpenReasonInvalid, ExternalAssets: assets}
	if action == ProjectOpenFresh {
		lifecycleState := ProjectLifecycleDeleted
		if state == ProjectReopenLive || state == ProjectReopenClosed {
			lifecycleState = ProjectLifecycleRetainedWindows
		} else if state != ProjectReopenDeletedWithSnapshot && state != ProjectReopenDeletedWithoutSnapshot {
			return invalid
		}
		cell := DecideProjectLifecycle(lifecycleState, ProjectLifecycleFresh, ProjectLifecyclePreconditions{})
		reason := ProjectOpenReasonFreshCreate
		if lifecycleState != ProjectLifecycleDeleted {
			reason = ProjectOpenReasonFreshReplace
		}
		return ProjectOpenPlan{Available: cell.Available, Source: ProjectOpenSourceRoot,
			AtomicWriteSet: slices.Clone(cell.AtomicWriteSet), Reason: reason,
			NewProjectUID: cell.ProjectUID == ProjectUIDReplaced || cell.ProjectUID == ProjectUIDCreated, ExternalAssets: cell.ExternalAssets}
	}
	if action != ProjectOpenContinue {
		return invalid
	}
	switch state {
	case ProjectReopenLive:
		return ProjectOpenPlan{Available: true, Source: ProjectOpenSourceLiveRuntime,
			AtomicWriteSet: []ProjectStartupWrite{ProjectStartupWriteNone},
			Reason:         ProjectOpenReasonAttachLive, ExternalAssets: assets}
	case ProjectReopenClosed:
		cell := DecideProjectLifecycle(ProjectLifecycleRetainedWindows, ProjectLifecycleContinue, ProjectLifecyclePreconditions{})
		return ProjectOpenPlan{Available: cell.Available, Source: ProjectOpenSourceRegistryTopology,
			AtomicWriteSet: slices.Clone(cell.AtomicWriteSet),
			Reason:         ProjectOpenReasonMaterializeClosed, ExternalAssets: cell.ExternalAssets}
	case ProjectReopenDeletedWithSnapshot:
		cell := DecideProjectLifecycle(ProjectLifecycleDeleted, ProjectLifecycleContinue, ProjectLifecyclePreconditions{UsableSnapshot: true})
		return ProjectOpenPlan{Available: cell.Available, Source: ProjectOpenSourceSnapshot,
			AtomicWriteSet: slices.Clone(cell.AtomicWriteSet), Reason: ProjectOpenReasonRestoreSnapshot,
			NewProjectUID: cell.ProjectUID == ProjectUIDCreated, ExternalAssets: cell.ExternalAssets}
	case ProjectReopenDeletedWithoutSnapshot:
		cell := DecideProjectLifecycle(ProjectLifecycleDeleted, ProjectLifecycleContinue, ProjectLifecyclePreconditions{})
		return ProjectOpenPlan{Available: cell.Available, Source: ProjectOpenSourceNone,
			AtomicWriteSet: slices.Clone(cell.AtomicWriteSet), Reason: ProjectOpenReasonNoSnapshot,
			NewProjectUID: false, ExternalAssets: cell.ExternalAssets}
	default:
		return invalid
	}
}

// EqualTeardownPlans exists for property tests and controller callers that need
// to compare two delivery orders without depending on serialization.
func EqualTeardownPlans(left, right TeardownDecision) bool {
	return left == right
}
