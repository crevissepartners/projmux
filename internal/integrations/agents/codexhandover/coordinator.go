// Package codexhandover owns the Phase 5 generation-wide destructive journal.
// Every adapter method is an Ensure operation: it must first observe the exact
// operation/tuple receipt and only perform a missing effect. That contract is
// what closes the crash gap between an external effect and the journal receipt.
package codexhandover

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	"github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexgenerationhost"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexupgrade"
)

type Decision string

const (
	DecisionReady                 Decision = "ready"
	DecisionBlocked               Decision = "blocked"
	DecisionAwaitingOwnerStop     Decision = "awaiting-owner-stop"
	DecisionRecoverSameGeneration Decision = "recover-same-generation"
	// DecisionRequestHandover is the vacant-generation entry. The rolling
	// handover request is normally fired by `agent resume` on a pinned Agent;
	// a generation whose Agents are all gone has no such caller left, so Apply
	// fires it under the same journal authority before proceeding.
	DecisionRequestHandover Decision = "request-handover"
)

type Request struct {
	OperationRef        string                            `json:"operationRef"`
	RollingOperationRef string                            `json:"rollingOperationRef"`
	Choices             []codexgeneration.NoTurnChoice    `json:"choices,omitempty"`
	OwnerStopReceipt    *codexgeneration.OwnerStopReceipt `json:"ownerStopReceipt,omitempty"`
}

type Plan struct {
	Decision              Decision                               `json:"decision"`
	OperationRef          string                                 `json:"operationRef"`
	RollingOperationRef   string                                 `json:"rollingOperationRef"`
	StateDomainID         string                                 `json:"stateDomainID,omitempty"`
	OldGenerationID       string                                 `json:"oldGenerationID,omitempty"`
	SuccessorGenerationID string                                 `json:"successorGenerationID,omitempty"`
	Owner                 codexgeneration.OwnerClass             `json:"owner,omitempty"`
	Targets               []codexgeneration.HandoverTarget       `json:"targets,omitempty"`
	Choices               []codexgeneration.NoTurnChoice         `json:"choices,omitempty"`
	Blockers              []string                               `json:"blockers,omitempty"`
	Mutations             codexgeneration.HandoverMutations      `json:"mutations"`
	ColdRecovery          *codexgeneration.ColdRecoveryOperation `json:"coldRecovery,omitempty"`
	// Vacancy and VacancyEvidence are populated only when the plan had to ask
	// whether this generation still holds anything -- that is, when the
	// version-pair receipt or the rolling handover request is missing. They are
	// the operator-visible reason a retirement did or did not open.
	Vacancy         codexgeneration.RetirementVacancy          `json:"vacancy,omitempty"`
	VacancyEvidence *codexgeneration.RetirementVacancyEvidence `json:"vacancyEvidence,omitempty"`
}

type RegistryStore interface {
	LoadSnapshot() (metadata.Registry, error)
}

// Effects is the exact production seam. Ensure methods are operation-keyed and
// convergent; implementations may never select a process, Agent, Pane, thread,
// endpoint, or lease from ambient/current state.
type Effects interface {
	EnsureSameGenerationRecovered(context.Context, string, codexupgrade.GenerationRoute) (codexgenerationhost.LaunchProof, error)
	EnsureNoTurnChoice(context.Context, string, codexgeneration.NoTurnChoice, metadata.CodexEndpointRef, metadata.CodexEndpointRef) error
	EnsureAdmissionFence(context.Context, string, metadata.CodexEndpointRef) error
	EnsureBindingFence(context.Context, string, metadata.CodexEndpointRef, []codexgeneration.HandoverTarget) error
	EnsureTargetAbsent(context.Context, string, metadata.CodexEndpointRef, codexupgrade.GenerationRoute, codexgeneration.HandoverTarget) error
	EnsureOldStopped(context.Context, string, codexupgrade.GenerationRoute, codexupgrade.GenerationRoute, []codexgeneration.HandoverTarget) error
	EnsureTargetResumed(context.Context, string, metadata.CodexEndpointRef, codexupgrade.GenerationRoute, codexgeneration.HandoverTarget) error
	EnsureTargetSnapshot(context.Context, string, metadata.CodexEndpointRef, codexupgrade.GenerationRoute, codexgeneration.HandoverTarget) error
	EnsureTargetCAS(context.Context, string, metadata.CodexEndpointRef, metadata.CodexEndpointRef, codexgeneration.HandoverTarget) error
	EnsurePaneRelaunched(context.Context, string, codexupgrade.GenerationRoute, codexgeneration.HandoverTarget) error
	EnsureRetired(context.Context, string, metadata.CodexEndpointRef) error
	EnsureLeaseReleased(context.Context, string, codexupgrade.GenerationRoute) error
	EnsureOldAuthorityRestored(context.Context, string, metadata.CodexEndpointRef, []codexgeneration.HandoverTarget) error
}

// Requester fires the exact rolling handover request for one Draining
// endpoint. `agent resume` is the only other production caller and it
// structurally requires a live pinned Agent, so a generation whose Agents were
// all deleted can never reach the request without this seam.
type Requester interface {
	RequestHandover(context.Context, metadata.CodexEndpointRef) (string, bool, error)
}

type Coordinator struct {
	Journal    *codexupgrade.Store
	Registry   RegistryStore
	Effects    Effects
	Requester  Requester
	Observe    func(context.Context, codexupgrade.GenerationRoute) error
	CanRecover func(codexupgrade.GenerationRoute) error
	Failpoint  func(string) error
	// EnumerateThreads reads the shared state domain's thread store. It
	// defaults to the real filesystem census; tests replace it. A nil result
	// error means the domain could not be read, which is never vacancy.
	EnumerateThreads func(stateDomainPath string) ([]string, error)
}

func (coordinator *Coordinator) enumerateThreads() func(string) ([]string, error) {
	if coordinator.EnumerateThreads != nil {
		return coordinator.EnumerateThreads
	}
	return enumerateStateDomainThreads
}

const (
	FailBeforePrewrite = "handover-before-prewrite"
	FailAfterPrewrite  = "handover-after-prewrite"
	FailAfterIntent    = "handover-after-intent"
	FailAfterEffect    = "handover-after-effect"
	FailAfterReceipt   = "handover-after-receipt"
)

func (coordinator *Coordinator) fail(point string) error {
	if coordinator.Failpoint == nil {
		return nil
	}
	return coordinator.Failpoint(point)
}

func (coordinator *Coordinator) observe(ctx context.Context, route codexupgrade.GenerationRoute) error {
	if coordinator.Observe != nil {
		return coordinator.Observe(ctx, route)
	}
	return codexupgrade.ObserveRoute(ctx, route)
}

func (coordinator *Coordinator) canRecover(route codexupgrade.GenerationRoute) error {
	if coordinator.CanRecover != nil {
		return coordinator.CanRecover(route)
	}
	return codexgenerationhost.ValidateDurableGenerationRecovery(route.Config.HostConfig(), route.LaunchOperationRef)
}

func (coordinator *Coordinator) Plan(ctx context.Context, request Request) Plan {
	plan := Plan{Decision: DecisionReady, OperationRef: strings.TrimSpace(request.OperationRef), RollingOperationRef: strings.TrimSpace(request.RollingOperationRef)}
	if coordinator.Journal == nil || coordinator.Registry == nil {
		plan.Blockers = append(plan.Blockers, "handover-coordinator-unconfigured")
		return blocked(plan)
	}
	if plan.OperationRef == "" || plan.RollingOperationRef == "" || plan.OperationRef != request.OperationRef || plan.RollingOperationRef != request.RollingOperationRef {
		plan.Blockers = append(plan.Blockers, "invalid-handover-request")
		return blocked(plan)
	}
	for _, choice := range request.Choices {
		if choice.Applied || choice.PaneUID != "" || choice.PaneRuntimeID != "" || choice.PaneGeneration != "" {
			plan.Blockers = append(plan.Blockers, "request-choice-cannot-carry-receipt:"+choice.AgentUID)
		}
	}
	journal, exists, err := coordinator.Journal.Load()
	if err != nil || !exists || journal.Operation == nil {
		plan.Blockers = append(plan.Blockers, "rolling-journal-unavailable")
		return blocked(plan)
	}
	op := journal.Operation
	plan.StateDomainID, plan.OldGenerationID, plan.SuccessorGenerationID = journal.StateDomainID, op.OldGenerationID, op.TargetGenerationID
	// A missing handover request is the one part of this fence a vacant
	// generation may clear: the request itself is what re-projects obligations,
	// and only a live pinned Agent can fire it. Every other mismatch here stays
	// an unconditional refusal.
	handoverRequestPending := false
	if op.OperationRef != plan.RollingOperationRef || !op.DrainPublished || op.Aborted {
		plan.Blockers = append(plan.Blockers, "exact-rolling-handover-not-requested")
	} else if !op.HandoverRequested {
		handoverRequestPending = true
	}
	oldEndpoint := metadata.CodexEndpointRef{StateDomainID: journal.StateDomainID, EndpointGenerationID: op.OldGenerationID}
	successorEndpoint := metadata.CodexEndpointRef{StateDomainID: journal.StateDomainID, EndpointGenerationID: op.TargetGenerationID}
	if journal.Handover != nil && journal.Handover.OperationRef == plan.OperationRef && journal.Handover.RollingOperationRef == plan.RollingOperationRef {
		pinned := journal.Handover
		plan.Owner, plan.Targets, plan.Choices, plan.Mutations = pinned.Owner, slices.Clone(pinned.Targets), slices.Clone(pinned.Choices), pinned.Mutations
		requestedChoices := slices.Clone(request.Choices)
		slices.SortFunc(requestedChoices, func(a, b codexgeneration.NoTurnChoice) int { return strings.Compare(a.AgentUID, b.AgentUID) })
		if !sameChoiceSet(pinned.Choices, requestedChoices) {
			plan.Blockers = append(plan.Blockers, "handover-choice-set-changed")
		}
		if pinned.Aborted {
			plan.Blockers = append(plan.Blockers, "handover-already-aborted")
		}
		if handoverRequestPending {
			// A prewritten handover always follows its request. If the rolling
			// operation says otherwise the journal disagrees with itself, and a
			// vacancy census is not the authority to resolve that.
			plan.Blockers = append(plan.Blockers, "exact-rolling-handover-not-requested")
		}
		if request.OwnerStopReceipt != nil {
			if pinned.ExternalStopReceipt != nil {
				if *pinned.ExternalStopReceipt != *request.OwnerStopReceipt {
					plan.Blockers = append(plan.Blockers, "owner-stop-receipt-changed")
				}
			} else if _, err := pinned.WithExternalStopReceipt(*request.OwnerStopReceipt); err != nil {
				plan.Blockers = append(plan.Blockers, "invalid-owner-stop-receipt")
			}
		}
		if len(plan.Blockers) != 0 {
			return blocked(plan)
		}
		if pinned.Owner != codexgeneration.OwnerProjmuxPrivate && pinned.ExternalStopReceipt == nil && request.OwnerStopReceipt == nil {
			plan.Decision = DecisionAwaitingOwnerStop
		}
		return plan
	}
	oldRoute, oldOK := journal.Route(oldEndpoint)
	successor, successorOK := journal.Route(successorEndpoint)
	if !oldOK || (oldRoute.Generation.State != codexgeneration.StateDraining && oldRoute.Generation.State != codexgeneration.StateHandoverPending) {
		plan.Blockers = append(plan.Blockers, "exact-old-handover-route-missing")
	} else {
		plan.Owner = oldRoute.Generation.Owner
	}
	if !successorOK || successor.Generation.State != codexgeneration.StateCurrent || !successor.Ready || successor.Proof == nil {
		plan.Blockers = append(plan.Blockers, "exact-successor-route-not-ready")
	}
	// The version-pair receipt proves a thread survives a cross-version resume.
	// Whether this generation has any such thread is a Registry-and-store
	// question, so the refusal is decided after the fresh census below rather
	// than here.
	qualificationMissing := journal.Qualification == nil || journal.Qualification.Validate() != nil ||
		!codexgeneration.GateQualification(*journal.Qualification).Phase2Ready
	if oldOK && oldRoute.Generation.Owner == codexgeneration.OwnerProjmuxPrivate {
		if !oldRoute.Ready || oldRoute.Proof == nil || strings.TrimSpace(oldRoute.LaunchOperationRef) == "" {
			plan.Blockers = append(plan.Blockers, "exact-old-managed-lifecycle-authority-incomplete")
		} else if journal.ColdRecovery != nil && !journal.ColdRecovery.Recovered {
			if journal.ColdRecovery.OperationRef != plan.OperationRef || journal.ColdRecovery.RollingOperationRef != plan.RollingOperationRef ||
				journal.ColdRecovery.GenerationID != plan.OldGenerationID {
				plan.Blockers = append(plan.Blockers, "another-cold-recovery-operation-active")
			} else {
				plan.Decision = DecisionRecoverSameGeneration
				recovery := *journal.ColdRecovery
				plan.ColdRecovery = &recovery
			}
		} else if err := coordinator.observe(ctx, oldRoute); err != nil {
			decision := codexgeneration.DecideColdRecovery(codexgeneration.ColdRecoveryEvidence{
				Owner: oldRoute.Generation.Owner, SameGenerationBundle: coordinator.canRecover(oldRoute) == nil,
				SameGenerationLaunchAuth: strings.TrimSpace(oldRoute.LaunchOperationRef) != "",
				QualifiedVersionPair:     journal.Qualification != nil && journal.Qualification.Validate() == nil && codexgeneration.GateQualification(*journal.Qualification).Phase2Ready,
			})
			if decision == codexgeneration.ColdRecoveryRestartSameGeneration {
				if journal.ColdRecovery != nil && (journal.ColdRecovery.OperationRef != plan.OperationRef ||
					journal.ColdRecovery.RollingOperationRef != plan.RollingOperationRef || journal.ColdRecovery.GenerationID != plan.OldGenerationID) {
					plan.Blockers = append(plan.Blockers, "another-cold-recovery-operation-active")
				} else {
					plan.Decision = DecisionRecoverSameGeneration
					if journal.ColdRecovery != nil {
						recovery := *journal.ColdRecovery
						plan.ColdRecovery = &recovery
					}
				}
			} else if decision == codexgeneration.ColdRecoveryBlocked {
				plan.Blockers = append(plan.Blockers, "exact-old-owner-readiness-unproven")
			}
		}
	}
	if successorOK && successor.Ready && successor.Proof != nil {
		if err := coordinator.observe(ctx, successor); err != nil {
			plan.Blockers = append(plan.Blockers, "exact-successor-readiness-unproven")
		}
	}
	registry, err := coordinator.Registry.LoadSnapshot()
	if err != nil {
		// No snapshot means no census, so both deferred refusals stand.
		if qualificationMissing {
			plan.Blockers = append(plan.Blockers, "version-pair-not-qualified")
		}
		if handoverRequestPending {
			plan.Blockers = append(plan.Blockers, "exact-rolling-handover-not-requested")
		}
		plan.Blockers = append(plan.Blockers, "registry-snapshot-unavailable")
		return blocked(plan)
	}
	if qualificationMissing || handoverRequestPending {
		evidence, vacancy := gatherRetirementVacancy(registry, oldEndpoint,
			stateDomainPath(journal, successorEndpoint), coordinator.enumerateThreads())
		plan.Vacancy, plan.VacancyEvidence = vacancy, &evidence
		if !vacancy.Vacant() {
			if qualificationMissing {
				plan.Blockers = append(plan.Blockers, "version-pair-not-qualified")
			}
			if handoverRequestPending {
				plan.Blockers = append(plan.Blockers, "exact-rolling-handover-not-requested")
			}
		}
	}
	choices := slices.Clone(request.Choices)
	slices.SortFunc(choices, func(a, b codexgeneration.NoTurnChoice) int { return strings.Compare(a.AgentUID, b.AgentUID) })
	choiceByAgent := make(map[string]codexgeneration.NoTurnChoice, len(choices))
	for _, choice := range choices {
		if _, duplicate := choiceByAgent[choice.AgentUID]; duplicate {
			plan.Blockers = append(plan.Blockers, "duplicate-no-turn-choice:"+choice.AgentUID)
		}
		choiceByAgent[choice.AgentUID] = choice
	}
	if request.OwnerStopReceipt != nil && (strings.TrimSpace(request.OwnerStopReceipt.ReceiptID) == "" ||
		request.OwnerStopReceipt.ReceiptID != strings.TrimSpace(request.OwnerStopReceipt.ReceiptID) || !request.OwnerStopReceipt.Endpoint.Same(oldEndpoint)) {
		plan.Blockers = append(plan.Blockers, "invalid-owner-stop-receipt")
	}
	if request.OwnerStopReceipt != nil && oldOK && oldRoute.Generation.Owner == codexgeneration.OwnerProjmuxPrivate {
		plan.Blockers = append(plan.Blockers, "managed-owner-cannot-use-external-stop-receipt")
	}
	for _, obligation := range projectExactObligations(registry, oldEndpoint) {
		switch obligation.State {
		case codexgeneration.ObligationCompletedPersisted:
			target, targetErr := exactTarget(registry, obligation.AgentUID, oldEndpoint, request.OperationRef)
			if targetErr != nil {
				plan.Blockers = append(plan.Blockers, targetErr.Error())
				continue
			}
			plan.Targets = append(plan.Targets, target)
		case codexgeneration.ObligationNoTurn:
			choice, ok := choiceByAgent[obligation.AgentUID]
			if !ok {
				plan.Blockers = append(plan.Blockers, "unresolved-no-turn:"+obligation.AgentUID)
				continue
			}
			pinned, err := validateChoice(registry, choice, oldEndpoint, successorEndpoint)
			if err != nil {
				plan.Blockers = append(plan.Blockers, err.Error())
				continue
			}
			plan.Choices = append(plan.Choices, pinned)
			delete(choiceByAgent, obligation.AgentUID)
		case codexgeneration.ObligationClosed:
		case codexgeneration.ObligationActive, codexgeneration.ObligationApprovalPending, codexgeneration.ObligationUnknown:
			plan.Blockers = append(plan.Blockers, string(obligation.State)+":"+obligation.AgentUID)
		default:
			plan.Blockers = append(plan.Blockers, "unknown:"+obligation.AgentUID)
		}
	}
	for agentUID := range choiceByAgent {
		plan.Blockers = append(plan.Blockers, "choice-target-not-no-turn:"+agentUID)
	}
	slices.SortFunc(plan.Targets, func(a, b codexgeneration.HandoverTarget) int { return strings.Compare(a.AgentUID, b.AgentUID) })
	if journal.Handover != nil {
		if journal.Handover.OperationRef != request.OperationRef || journal.Handover.RollingOperationRef != request.RollingOperationRef {
			plan.Blockers = append(plan.Blockers, "another-handover-operation-active")
		} else {
			plan.Mutations = journal.Handover.Mutations
			if !sameTargetSet(journal.Handover.Targets, plan.Targets) || !sameChoiceSet(journal.Handover.Choices, plan.Choices) {
				plan.Blockers = append(plan.Blockers, "handover-target-set-changed")
			}
		}
	}
	if len(plan.Blockers) != 0 {
		return blocked(plan)
	}
	if handoverRequestPending && plan.Decision == DecisionReady {
		// Vacant, and nothing else stands in the way. Apply fires the request;
		// Plan stays read-only and reports what Apply would do.
		plan.Decision = DecisionRequestHandover
		return plan
	}
	if oldOK && oldRoute.Generation.Owner != codexgeneration.OwnerProjmuxPrivate && request.OwnerStopReceipt == nil {
		plan.Decision = DecisionAwaitingOwnerStop
	}
	return plan
}

func blocked(plan Plan) Plan {
	slices.Sort(plan.Blockers)
	plan.Decision = DecisionBlocked
	return plan
}

func projectExactObligations(registry metadata.Registry, endpoint metadata.CodexEndpointRef) []codexgeneration.AgentObligation {
	var out []codexgeneration.AgentObligation
	for i := range registry.Agents {
		agent := registry.Agents[i]
		if agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil || agent.Status.SessionRef.Codex.Endpoint == nil ||
			!agent.Status.SessionRef.Codex.Endpoint.Same(endpoint) {
			continue
		}
		obligation, ok := codexgeneration.ProjectAgentObligation(agent, false)
		if ok {
			out = append(out, obligation)
		}
	}
	slices.SortFunc(out, func(a, b codexgeneration.AgentObligation) int { return strings.Compare(a.AgentUID, b.AgentUID) })
	return out
}

func exactTarget(registry metadata.Registry, agentUID string, endpoint metadata.CodexEndpointRef, operationRef string) (codexgeneration.HandoverTarget, error) {
	agent, ok := registry.Agent(agentUID)
	if !ok || agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil {
		return codexgeneration.HandoverTarget{}, fmt.Errorf("target-agent-missing:%s", agentUID)
	}
	ref := agent.Status.SessionRef.Codex
	pane, ok := registry.Pane(agent.Status.PaneRef)
	if !ok || agent.Status.Phase != metadata.PhaseRunning || ref.Endpoint == nil || !ref.Endpoint.Same(endpoint) || !ref.HasStartedTurn ||
		strings.TrimSpace(ref.ThreadID) == "" || pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != metadata.KindAgent ||
		pane.Metadata.OwnerRef.UID != agentUID || pane.Status.Activation.AgentUID != agentUID || pane.Status.Activation.RuntimeID == "" ||
		pane.Status.Activation.Generation == "" || pane.Status.Activation.Codex == nil || pane.Status.Activation.Codex.ThreadID != ref.ThreadID {
		return codexgeneration.HandoverTarget{}, fmt.Errorf("exact-agent-pane-thread-tuple-unavailable:%s", agentUID)
	}
	hash := sha256.Sum256([]byte(operationRef + "\x00" + agentUID))
	return codexgeneration.HandoverTarget{AgentUID: agentUID, PaneUID: pane.Metadata.UID, PaneRuntimeID: pane.Status.Activation.RuntimeID,
		PaneGeneration: pane.Status.Activation.Generation, RelaunchGeneration: "handover-" + hex.EncodeToString(hash[:12]), ThreadID: ref.ThreadID}, nil
}

func validateChoice(registry metadata.Registry, choice codexgeneration.NoTurnChoice, old, successor metadata.CodexEndpointRef) (codexgeneration.NoTurnChoice, error) {
	agent, ok := registry.Agent(choice.AgentUID)
	if !ok || agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil || agent.Status.SessionRef.Codex.Endpoint == nil ||
		!agent.Status.SessionRef.Codex.Endpoint.Same(old) || agent.Status.SessionRef.Codex.HasStartedTurn {
		return choice, fmt.Errorf("invalid-no-turn-choice:%s", choice.AgentUID)
	}
	pane, ok := registry.Pane(agent.Status.PaneRef)
	if !ok || agent.Status.Phase != metadata.PhaseRunning || pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != metadata.KindAgent ||
		pane.Metadata.OwnerRef.UID != choice.AgentUID || pane.Status.Activation.AgentUID != choice.AgentUID ||
		strings.TrimSpace(pane.Status.Activation.RuntimeID) == "" || strings.TrimSpace(pane.Status.Activation.Generation) == "" {
		return choice, fmt.Errorf("invalid-no-turn-choice:%s", choice.AgentUID)
	}
	choice.PaneUID, choice.PaneRuntimeID, choice.PaneGeneration = pane.Metadata.UID, pane.Status.Activation.RuntimeID, pane.Status.Activation.Generation
	if choice.Decision == codexgeneration.NoTurnClose && choice.ReplacementAgentUID == "" {
		return choice, nil
	}
	if choice.Decision != codexgeneration.NoTurnReplacement || choice.ReplacementAgentUID == "" || choice.ReplacementAgentUID == choice.AgentUID {
		return choice, fmt.Errorf("invalid-no-turn-choice:%s", choice.AgentUID)
	}
	replacement, ok := registry.Agent(choice.ReplacementAgentUID)
	if !ok || replacement.Status.SessionRef == nil || replacement.Status.SessionRef.Codex == nil || replacement.Status.SessionRef.Codex.Endpoint == nil ||
		!replacement.Status.SessionRef.Codex.Endpoint.Same(successor) || replacement.Status.SessionRef.Codex.HasStartedTurn {
		return choice, fmt.Errorf("invalid-explicit-replacement:%s", choice.AgentUID)
	}
	return choice, nil
}

func sameTargetSet(a, b []codexgeneration.HandoverTarget) bool {
	strip := func(in []codexgeneration.HandoverTarget) []codexgeneration.HandoverTarget {
		out := slices.Clone(in)
		for i := range out {
			out[i].SuccessorAbsentObserved = false
			out[i].ResumeIntended = false
			out[i].Resumed = false
			out[i].SnapshotObserved = false
			out[i].EndpointCAS = false
			out[i].PaneRelaunched = false
		}
		return out
	}
	return slices.Equal(strip(a), strip(b))
}

func sameChoiceSet(a, b []codexgeneration.NoTurnChoice) bool {
	strip := func(in []codexgeneration.NoTurnChoice) []codexgeneration.NoTurnChoice {
		out := slices.Clone(in)
		for i := range out {
			out[i].PaneUID = ""
			out[i].PaneRuntimeID = ""
			out[i].PaneGeneration = ""
			out[i].Applied = false
		}
		return out
	}
	return slices.Equal(strip(a), strip(b))
}

func (coordinator *Coordinator) Apply(ctx context.Context, request Request) (codexupgrade.Journal, error) {
	if coordinator.Effects == nil {
		return codexupgrade.Journal{}, errors.New("handover effects are not configured")
	}
	plan := coordinator.Plan(ctx, request)
	if plan.Decision == DecisionBlocked {
		return codexupgrade.Journal{}, fmt.Errorf("codex generation handover blocked: %s", strings.Join(plan.Blockers, ","))
	}
	if plan.Decision == DecisionRecoverSameGeneration {
		if err := coordinator.recoverSameGeneration(ctx, request, plan); err != nil {
			return codexupgrade.Journal{}, err
		}
		plan = coordinator.Plan(ctx, request)
		if plan.Decision != DecisionReady && plan.Decision != DecisionAwaitingOwnerStop && plan.Decision != DecisionRequestHandover {
			return codexupgrade.Journal{}, fmt.Errorf("codex same-generation recovery did not restore exact old readiness: %s", strings.Join(plan.Blockers, ","))
		}
	}
	if plan.Decision == DecisionRequestHandover {
		if coordinator.Requester == nil {
			return codexupgrade.Journal{}, errors.New("codex generation handover requester is not configured")
		}
		old := metadata.CodexEndpointRef{StateDomainID: plan.StateDomainID, EndpointGenerationID: plan.OldGenerationID}
		if _, _, err := coordinator.Requester.RequestHandover(ctx, old); err != nil {
			return codexupgrade.Journal{}, err
		}
		// Re-plan against the requested journal. The request re-projects the
		// obligation ledger, so this second census is the one that authorizes
		// the destructive operation.
		plan = coordinator.Plan(ctx, request)
		if plan.Decision != DecisionReady && plan.Decision != DecisionAwaitingOwnerStop {
			return codexupgrade.Journal{}, fmt.Errorf("codex vacant generation handover request did not open the exact retirement path: %s", strings.Join(plan.Blockers, ","))
		}
	}
	if err := coordinator.fail(FailBeforePrewrite); err != nil {
		return codexupgrade.Journal{}, err
	}
	journal, err := coordinator.Journal.Update(ctx, func(current *codexupgrade.Journal, exists bool) error {
		if !exists || current.Operation == nil || current.Operation.OperationRef != request.RollingOperationRef {
			return errors.New("rolling operation disappeared before handover prewrite")
		}
		if current.Handover != nil {
			if current.Handover.OperationRef != request.OperationRef {
				return errors.New("another handover operation is active")
			}
			if current.Handover.ExternalStopReceipt == nil && request.OwnerStopReceipt != nil {
				next, receiptErr := current.Handover.WithExternalStopReceipt(*request.OwnerStopReceipt)
				if receiptErr != nil {
					return receiptErr
				}
				current.Handover = &next
			}
			return nil
		}
		operation, operationErr := codexgeneration.NewHandoverOperation(request.OperationRef, request.RollingOperationRef,
			plan.StateDomainID, plan.OldGenerationID, plan.SuccessorGenerationID, plan.Owner, plan.Targets, plan.Choices, request.OwnerStopReceipt)
		if operationErr != nil {
			return operationErr
		}
		current.Handover = &operation
		return nil
	})
	if err != nil {
		return codexupgrade.Journal{}, err
	}
	if err := coordinator.fail(FailAfterPrewrite); err != nil {
		return codexupgrade.Journal{}, err
	}
	_ = journal // the durable prewrite is reloaded by Resume under the same journal authority.
	return coordinator.Resume(ctx, request.OperationRef)
}

func (coordinator *Coordinator) recoverSameGeneration(ctx context.Context, request Request, plan Plan) error {
	if err := coordinator.fail(FailBeforePrewrite); err != nil {
		return err
	}
	if _, err := coordinator.Journal.Update(ctx, func(current *codexupgrade.Journal, exists bool) error {
		if !exists || current.Operation == nil || current.Operation.OperationRef != request.RollingOperationRef {
			return errors.New("rolling operation disappeared before cold-recovery prewrite")
		}
		old := metadata.CodexEndpointRef{StateDomainID: plan.StateDomainID, EndpointGenerationID: plan.OldGenerationID}
		route, ok := current.Route(old)
		if !ok || route.Generation.Owner != codexgeneration.OwnerProjmuxPrivate || !route.Ready || route.Proof == nil || route.LaunchOperationRef == "" {
			return errors.New("exact same-generation recovery authority disappeared")
		}
		if current.ColdRecovery != nil {
			if current.ColdRecovery.OperationRef != request.OperationRef || current.ColdRecovery.RollingOperationRef != request.RollingOperationRef {
				return errors.New("another cold-recovery operation is active")
			}
			return nil
		}
		recovery, recoveryErr := codexgeneration.NewColdRecoveryOperation(request.OperationRef, request.RollingOperationRef,
			plan.StateDomainID, plan.OldGenerationID, route.LaunchOperationRef)
		if recoveryErr != nil {
			return recoveryErr
		}
		current.ColdRecovery = &recovery
		return nil
	}); err != nil {
		return err
	}
	if err := coordinator.fail(FailAfterPrewrite); err != nil {
		return err
	}
	_, err := coordinator.Journal.Update(ctx, func(current *codexupgrade.Journal, exists bool) error {
		if !exists || current.ColdRecovery == nil || current.ColdRecovery.OperationRef != request.OperationRef {
			return errors.New("cold-recovery intent lost authority")
		}
		if current.ColdRecovery.Recovered {
			return nil
		}
		old := metadata.CodexEndpointRef{StateDomainID: current.ColdRecovery.StateDomainID, EndpointGenerationID: current.ColdRecovery.GenerationID}
		route, ok := current.Route(old)
		if !ok || route.LaunchOperationRef != current.ColdRecovery.LaunchOperationRef {
			return errors.New("cold-recovery exact route drifted")
		}
		proof, ensureErr := coordinator.Effects.EnsureSameGenerationRecovered(ctx, request.OperationRef, route)
		if ensureErr != nil {
			return ensureErr
		}
		if failErr := coordinator.fail(FailAfterEffect); failErr != nil {
			return failErr
		}
		for i := range current.Routes {
			if current.Routes[i].Generation.Endpoint.Same(old) {
				copyProof := proof
				current.Routes[i].Proof = &copyProof
				current.Routes[i].Ready = true
			}
		}
		next, recordErr := current.ColdRecovery.RecordRecovered()
		if recordErr != nil {
			return recordErr
		}
		current.ColdRecovery = &next
		return nil
	})
	if err != nil {
		return fmt.Errorf("ensure exact same-generation cold recovery: %w", err)
	}
	return coordinator.fail(FailAfterReceipt)
}

func (coordinator *Coordinator) Resume(ctx context.Context, operationRef string) (codexupgrade.Journal, error) {
	if coordinator.Journal == nil || coordinator.Effects == nil {
		return codexupgrade.Journal{}, errors.New("handover coordinator is not configured")
	}
	for {
		journal, exists, err := coordinator.Journal.Load()
		if err != nil || !exists || journal.Handover == nil || journal.Handover.OperationRef != operationRef {
			return codexupgrade.Journal{}, errors.Join(errors.New("exact handover operation unavailable"), err)
		}
		action, index := journal.Handover.NextAction()
		if action == codexgeneration.HandoverActionNone || action == codexgeneration.HandoverActionAwaitOwnerStop {
			return journal, nil
		}
		if _, err := coordinator.Journal.Update(ctx, func(current *codexupgrade.Journal, exists bool) error {
			if !exists || current.Handover == nil || current.Handover.OperationRef != operationRef {
				return errors.New("handover intent lost authority")
			}
			next, intentErr := current.Handover.RecordIntent(action, index)
			if intentErr != nil {
				return intentErr
			}
			current.Handover = &next
			return nil
		}); err != nil {
			return codexupgrade.Journal{}, err
		}
		if err := coordinator.fail(FailAfterIntent); err != nil {
			return codexupgrade.Journal{}, err
		}
		_, err = coordinator.Journal.Update(ctx, func(current *codexupgrade.Journal, exists bool) error {
			if !exists || current.Handover == nil || current.Handover.OperationRef != operationRef {
				return errors.New("handover effect lost authority")
			}
			if current.Handover.AbortIntended || current.Handover.Aborted || current.Handover.PendingAction != action || current.Handover.PendingIndex != index {
				return errors.New("handover effect crossed an abort or action fence")
			}
			if ensureErr := coordinator.ensureAction(ctx, current, action, index); ensureErr != nil {
				return fmt.Errorf("ensure handover %s[%d]: %w", action, index, ensureErr)
			}
			if failErr := coordinator.fail(FailAfterEffect); failErr != nil {
				return failErr
			}
			next, recordErr := current.Handover.RecordAction(action, index)
			if recordErr != nil {
				return fmt.Errorf("target=%+v: %w", func() codexgeneration.HandoverTarget {
					if index >= 0 && index < len(current.Handover.Targets) {
						return current.Handover.Targets[index]
					}
					return codexgeneration.HandoverTarget{}
				}(), recordErr)
			}
			current.Handover = &next
			if action == codexgeneration.HandoverActionRetire {
				oldEndpoint := metadata.CodexEndpointRef{StateDomainID: next.StateDomainID, EndpointGenerationID: next.OldGenerationID}
				for i := range current.Routes {
					if current.Routes[i].Generation.Endpoint.Same(oldEndpoint) {
						current.Routes[i].Generation.State = codexgeneration.StateRetired
					}
				}
				current.Obligations = slices.DeleteFunc(current.Obligations, func(obligation codexgeneration.AgentObligation) bool {
					return obligation.EndpointGenerationID == oldEndpoint.EndpointGenerationID
				})
			}
			return nil
		})
		if err != nil {
			return codexupgrade.Journal{}, fmt.Errorf("record handover %s[%d]: %w", action, index, err)
		}
		if err := coordinator.fail(FailAfterReceipt); err != nil {
			return codexupgrade.Journal{}, err
		}
	}
}

func (coordinator *Coordinator) ensureAction(ctx context.Context, journal *codexupgrade.Journal, action codexgeneration.HandoverAction, index int) error {
	op := journal.Handover
	oldEndpoint := metadata.CodexEndpointRef{StateDomainID: op.StateDomainID, EndpointGenerationID: op.OldGenerationID}
	successorEndpoint := metadata.CodexEndpointRef{StateDomainID: op.StateDomainID, EndpointGenerationID: op.SuccessorGenerationID}
	oldRoute, oldOK := journal.Route(oldEndpoint)
	successor, successorOK := journal.Route(successorEndpoint)
	if !oldOK || !successorOK {
		return errors.New("handover route disappeared")
	}
	switch action {
	case codexgeneration.HandoverActionNoTurnChoice:
		return coordinator.Effects.EnsureNoTurnChoice(ctx, op.OperationRef, op.Choices[index], oldEndpoint, successorEndpoint)
	case codexgeneration.HandoverActionAdmissionFence:
		return coordinator.Effects.EnsureAdmissionFence(ctx, op.OperationRef, oldEndpoint)
	case codexgeneration.HandoverActionBindingFence:
		return coordinator.Effects.EnsureBindingFence(ctx, op.OperationRef, oldEndpoint, op.Targets)
	case codexgeneration.HandoverActionCheckAbsent:
		return coordinator.Effects.EnsureTargetAbsent(ctx, op.OperationRef, oldEndpoint, successor, op.Targets[index])
	case codexgeneration.HandoverActionStopOld:
		if op.Owner == codexgeneration.OwnerProjmuxPrivate {
			return coordinator.Effects.EnsureOldStopped(ctx, op.OperationRef, oldRoute, successor, op.Targets)
		}
		return nil
	case codexgeneration.HandoverActionResumeTarget:
		return coordinator.Effects.EnsureTargetResumed(ctx, op.OperationRef, oldEndpoint, successor, op.Targets[index])
	case codexgeneration.HandoverActionSnapshotTarget:
		return coordinator.Effects.EnsureTargetSnapshot(ctx, op.OperationRef, oldEndpoint, successor, op.Targets[index])
	case codexgeneration.HandoverActionCASTarget:
		return coordinator.Effects.EnsureTargetCAS(ctx, op.OperationRef, oldEndpoint, successorEndpoint, op.Targets[index])
	case codexgeneration.HandoverActionRelaunchTarget:
		return coordinator.Effects.EnsurePaneRelaunched(ctx, op.OperationRef, successor, op.Targets[index])
	case codexgeneration.HandoverActionRetire:
		return coordinator.Effects.EnsureRetired(ctx, op.OperationRef, oldEndpoint)
	case codexgeneration.HandoverActionReleaseLease:
		return coordinator.Effects.EnsureLeaseReleased(ctx, op.OperationRef, oldRoute)
	default:
		return errors.New("unknown handover action")
	}
}

func (coordinator *Coordinator) Abort(ctx context.Context, operationRef string) (codexupgrade.Journal, error) {
	if coordinator.Journal == nil || coordinator.Effects == nil {
		return codexupgrade.Journal{}, errors.New("handover coordinator is not configured")
	}
	journal, err := coordinator.Journal.Update(ctx, func(current *codexupgrade.Journal, exists bool) error {
		if !exists || current.Handover == nil || current.Handover.OperationRef != operationRef {
			return errors.New("exact handover operation unavailable")
		}
		next, requestErr := current.Handover.RequestAbort()
		if requestErr != nil {
			return requestErr
		}
		current.Handover = &next
		return nil
	})
	if err != nil {
		return codexupgrade.Journal{}, err
	}
	old := metadata.CodexEndpointRef{StateDomainID: journal.Handover.StateDomainID, EndpointGenerationID: journal.Handover.OldGenerationID}
	if err := coordinator.Effects.EnsureOldAuthorityRestored(ctx, operationRef, old, journal.Handover.Targets); err != nil {
		return codexupgrade.Journal{}, err
	}
	return coordinator.Journal.Update(ctx, func(current *codexupgrade.Journal, exists bool) error {
		if !exists || current.Handover == nil || current.Handover.OperationRef != operationRef {
			return errors.New("handover abort lost authority")
		}
		next, abortErr := current.Handover.Abort()
		if abortErr != nil {
			return abortErr
		}
		current.Handover = &next
		return nil
	})
}
