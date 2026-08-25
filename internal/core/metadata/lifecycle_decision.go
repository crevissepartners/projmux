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

	// A matching unlink confirms that the old runtime Window disappeared; it
	// does not turn an automatic clean exit into canonical Window-delete
	// authority. The logical Window/root identities remain and the Pane/Agent
	// plan supplies their replacement shell.
	return TeardownDecision{Action: TeardownDeletePaneAgent, RootAction: rootAction,
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
	if owner := pane.Metadata.OwnerRef; owner != nil && owner.Kind == KindAgent {
		agent, exists := registry.Agent(owner.UID)
		if !exists || agent.Status.PaneRef != pane.Metadata.UID || agent.Metadata.OwnerRef == nil ||
			agent.Metadata.OwnerRef.Kind != KindWindow {
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
		evidence.Classification != event.Classification {
		out.Decision.Action = TeardownRefuse
		out.Decision.Reason = TeardownReasonStaleGeneration
		return out, nil
	}

	desired := registry.Clone()
	mutator := Mutator{Now: func() time.Time { return now.UTC() }}
	deletedPanes := 1
	retainedPhase := AgentPhase("")
	if agentUID != "" {
		deletedPanes = 1
		exit := AgentExitNormal
		if event.Classification == TerminationIntentional {
			exit = AgentExitDeleted
		}
		retainedPhase, _ = exit.Phase()
		if _, err := mutator.ReleaseAgentPane(&desired, agentUID, exit, string(event.Classification)); err != nil {
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
	ProjectStartupWriteMaterializeRegistry   ProjectStartupWrite = "materialize-registry-topology"
	ProjectStartupWriteDeleteProjectGraph    ProjectStartupWrite = "delete-existing-project-graph"
	ProjectStartupWriteCreateProject         ProjectStartupWrite = "create-project-with-new-uid"
	ProjectStartupWriteCreateCanonicalWindow ProjectStartupWrite = "create-canonical-window"
	ProjectStartupWriteCreateCanonicalShell  ProjectStartupWrite = "create-canonical-shell"
	ProjectStartupWriteRestoreSnapshotGraph  ProjectStartupWrite = "restore-snapshot-topology"
)

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

// DecideProjectOpen returns the closed live/closed/deleted startup table. Fresh
// never reads or modifies a snapshot, and unavailable Continue never silently
// falls back to Fresh.
func DecideProjectOpen(state ProjectReopenState, action ProjectOpenAction) ProjectOpenPlan {
	assets := projectPreservedAssets()
	invalid := ProjectOpenPlan{Available: false, Source: ProjectOpenSourceNone,
		AtomicWriteSet: []ProjectStartupWrite{ProjectStartupWriteNone},
		Reason:         ProjectOpenReasonInvalid, ExternalAssets: assets}
	if action == ProjectOpenFresh {
		switch state {
		case ProjectReopenLive, ProjectReopenClosed:
			return ProjectOpenPlan{Available: true, Source: ProjectOpenSourceRoot,
				AtomicWriteSet: []ProjectStartupWrite{ProjectStartupWriteDeleteProjectGraph,
					ProjectStartupWriteCreateProject, ProjectStartupWriteCreateCanonicalWindow,
					ProjectStartupWriteCreateCanonicalShell},
				Reason: ProjectOpenReasonFreshReplace, NewProjectUID: true, ExternalAssets: assets}
		case ProjectReopenDeletedWithSnapshot, ProjectReopenDeletedWithoutSnapshot:
			return ProjectOpenPlan{Available: true, Source: ProjectOpenSourceRoot,
				AtomicWriteSet: []ProjectStartupWrite{ProjectStartupWriteCreateProject,
					ProjectStartupWriteCreateCanonicalWindow, ProjectStartupWriteCreateCanonicalShell},
				Reason: ProjectOpenReasonFreshCreate, NewProjectUID: true, ExternalAssets: assets}
		default:
			return invalid
		}
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
		return ProjectOpenPlan{Available: true, Source: ProjectOpenSourceRegistryTopology,
			AtomicWriteSet: []ProjectStartupWrite{ProjectStartupWriteMaterializeRegistry},
			Reason:         ProjectOpenReasonMaterializeClosed, ExternalAssets: assets}
	case ProjectReopenDeletedWithSnapshot:
		return ProjectOpenPlan{Available: true, Source: ProjectOpenSourceSnapshot,
			AtomicWriteSet: []ProjectStartupWrite{ProjectStartupWriteCreateProject,
				ProjectStartupWriteRestoreSnapshotGraph},
			Reason: ProjectOpenReasonRestoreSnapshot, NewProjectUID: true, ExternalAssets: assets}
	case ProjectReopenDeletedWithoutSnapshot:
		return ProjectOpenPlan{Available: false, Source: ProjectOpenSourceNone,
			AtomicWriteSet: []ProjectStartupWrite{ProjectStartupWriteNone},
			Reason:         ProjectOpenReasonNoSnapshot, ExternalAssets: assets}
	default:
		return invalid
	}
}

// EqualTeardownPlans exists for property tests and controller callers that need
// to compare two delivery orders without depending on serialization.
func EqualTeardownPlans(left, right TeardownDecision) bool {
	return left == right
}
