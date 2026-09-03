package codexgeneration

import (
	"errors"
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/metadata"
)

// HandoverJournalVersion is intentionally independent of RollingJournalVersion.
// The Phase 4 admission receipt remains immutable while Phase 5 appends one
// linked, generation-wide destructive operation.
const HandoverJournalVersion = 1

type HandoverPhase string

const (
	HandoverPlanned           HandoverPhase = "planned"
	HandoverAwaitingOwnerStop HandoverPhase = "awaiting-owner-stop"
	HandoverFenced            HandoverPhase = "fenced"
	HandoverOldStopped        HandoverPhase = "old-stopped"
	HandoverResuming          HandoverPhase = "resuming"
	HandoverCuttingOver       HandoverPhase = "cutting-over"
	HandoverTerminal          HandoverPhase = "terminal"
	HandoverComplete          HandoverPhase = "complete"
	HandoverAborted           HandoverPhase = "aborted"
)

type NoTurnDecision string

const (
	NoTurnClose       NoTurnDecision = "close"
	NoTurnReplacement NoTurnDecision = "replacement"
)

type NoTurnChoice struct {
	AgentUID            string         `json:"agentUID"`
	Decision            NoTurnDecision `json:"decision"`
	ReplacementAgentUID string         `json:"replacementAgentUID,omitempty"`
	PaneUID             string         `json:"paneUID,omitempty"`
	PaneRuntimeID       string         `json:"paneRuntimeID,omitempty"`
	PaneGeneration      string         `json:"paneGeneration,omitempty"`
	Applied             bool           `json:"applied"`
}

type OwnerStopReceipt struct {
	ReceiptID string                    `json:"receiptID"`
	Endpoint  metadata.CodexEndpointRef `json:"endpoint"`
}

// HandoverTarget is the complete identity-only tuple captured before any old
// process or Pane child is touched. Workspace paths and provider content are
// deliberately absent; adapters re-read the exact Agent after every receipt.
type HandoverTarget struct {
	AgentUID                string `json:"agentUID"`
	PaneUID                 string `json:"paneUID"`
	PaneRuntimeID           string `json:"paneRuntimeID"`
	PaneGeneration          string `json:"paneGeneration"`
	RelaunchGeneration      string `json:"relaunchGeneration"`
	ThreadID                string `json:"threadID"`
	SuccessorAbsentObserved bool   `json:"successorAbsentObserved"`
	ResumeIntended          bool   `json:"resumeIntended"`
	Resumed                 bool   `json:"resumed"`
	SnapshotObserved        bool   `json:"snapshotObserved"`
	EndpointCAS             bool   `json:"endpointCAS"`
	PaneRelaunched          bool   `json:"paneRelaunched"`
}

type HandoverMutations struct {
	NoTurnChoice      int `json:"noTurnChoice"`
	AdmissionFence    int `json:"admissionFence"`
	BindingFence      int `json:"bindingFence"`
	SuccessorAbsence  int `json:"successorAbsence"`
	OldEndpointStop   int `json:"oldEndpointStop"`
	SuccessorResume   int `json:"successorResume"`
	SuccessorSnapshot int `json:"successorSnapshot"`
	EndpointRefCAS    int `json:"endpointRefCAS"`
	PaneRelaunch      int `json:"paneRelaunch"`
	Retirement        int `json:"retirement"`
	LeaseRelease      int `json:"leaseRelease"`
	ForeignLifecycle  int `json:"foreignLifecycle"`
}

// HandoverOperation is a monotonic target-set journal. An intent bit always
// precedes its external effect. Effect adapters must reconcile that intent by
// exact tuple and operation id, making a coordinator restart convergent rather
// than permission to replay a provider/process/tmux call.
type HandoverOperation struct {
	JournalVersion        int               `json:"journalVersion"`
	OperationRef          string            `json:"operationRef"`
	RollingOperationRef   string            `json:"rollingOperationRef"`
	StateDomainID         string            `json:"stateDomainID"`
	OldGenerationID       string            `json:"oldGenerationID"`
	SuccessorGenerationID string            `json:"successorGenerationID"`
	Owner                 OwnerClass        `json:"owner"`
	Phase                 HandoverPhase     `json:"phase"`
	Choices               []NoTurnChoice    `json:"choices,omitempty"`
	Targets               []HandoverTarget  `json:"targets,omitempty"`
	ExternalStopReceipt   *OwnerStopReceipt `json:"externalStopReceipt,omitempty"`
	AdmissionFenced       bool              `json:"admissionFenced"`
	BindingFenced         bool              `json:"bindingFenced"`
	StopIntended          bool              `json:"stopIntended"`
	OldStopped            bool              `json:"oldStopped"`
	Retired               bool              `json:"retired"`
	LeaseReleased         bool              `json:"leaseReleased"`
	AbortIntended         bool              `json:"abortIntended"`
	Aborted               bool              `json:"aborted"`
	PendingAction         HandoverAction    `json:"pendingAction,omitempty"`
	PendingIndex          int               `json:"pendingIndex,omitempty"`
	Mutations             HandoverMutations `json:"mutations"`
}

func NewHandoverOperation(operationRef, rollingRef, stateDomainID, oldGenerationID, successorGenerationID string, owner OwnerClass, targets []HandoverTarget, choices []NoTurnChoice, receipt *OwnerStopReceipt) (HandoverOperation, error) {
	op := HandoverOperation{
		JournalVersion: HandoverJournalVersion, OperationRef: operationRef, RollingOperationRef: rollingRef,
		StateDomainID: stateDomainID, OldGenerationID: oldGenerationID, SuccessorGenerationID: successorGenerationID,
		Owner: owner, Phase: HandoverPlanned, Targets: slices.Clone(targets), Choices: slices.Clone(choices), PendingAction: HandoverActionNone,
	}
	if receipt != nil {
		copy := *receipt
		op.ExternalStopReceipt = &copy
	}
	if owner != OwnerProjmuxPrivate && receipt == nil {
		op.Phase = HandoverAwaitingOwnerStop
	}
	if err := op.Validate(); err != nil {
		return HandoverOperation{}, err
	}
	return op, nil
}

func validHandoverPhase(phase HandoverPhase) bool {
	return slices.Contains([]HandoverPhase{HandoverPlanned, HandoverAwaitingOwnerStop, HandoverFenced,
		HandoverOldStopped, HandoverResuming, HandoverCuttingOver, HandoverTerminal, HandoverComplete, HandoverAborted}, phase)
}

func (op HandoverOperation) Validate() error {
	if op.JournalVersion != HandoverJournalVersion || !validIdentityToken(op.OperationRef) ||
		!validIdentityToken(op.RollingOperationRef) || !validIdentityToken(op.StateDomainID) ||
		!validIdentityToken(op.OldGenerationID) || !validIdentityToken(op.SuccessorGenerationID) ||
		op.OldGenerationID == op.SuccessorGenerationID || !validOwnerClass(op.Owner) || !validHandoverPhase(op.Phase) {
		return errors.New("invalid-handover-operation")
	}
	if op.Mutations.ForeignLifecycle != 0 {
		return errors.New("foreign-lifecycle-effect")
	}
	if op.PendingAction == HandoverActionNone {
		if op.PendingIndex != 0 {
			return errors.New("handover-pending-action-mismatch")
		}
	} else {
		want, wantIndex := op.nextAction()
		if op.PendingAction != want || op.PendingIndex != wantIndex || want == HandoverActionAwaitOwnerStop {
			return errors.New("handover-pending-action-mismatch")
		}
	}
	counts := []int{op.Mutations.AdmissionFence, op.Mutations.BindingFence, op.Mutations.OldEndpointStop,
		op.Mutations.Retirement, op.Mutations.LeaseRelease}
	for _, count := range counts {
		if count < 0 || count > 1 {
			return errors.New("duplicate-generation-handover-effect")
		}
	}
	if op.Mutations.NoTurnChoice < 0 || op.Mutations.NoTurnChoice > len(op.Choices) ||
		op.Mutations.SuccessorAbsence < 0 || op.Mutations.SuccessorAbsence > len(op.Targets) ||
		op.Mutations.SuccessorResume < 0 || op.Mutations.SuccessorResume > len(op.Targets) ||
		op.Mutations.SuccessorSnapshot < 0 || op.Mutations.SuccessorSnapshot > len(op.Targets) ||
		op.Mutations.EndpointRefCAS < 0 || op.Mutations.EndpointRefCAS > len(op.Targets) ||
		op.Mutations.PaneRelaunch < 0 || op.Mutations.PaneRelaunch > len(op.Targets) {
		return errors.New("duplicate-target-handover-effect")
	}
	if op.AdmissionFenced != (op.Mutations.AdmissionFence == 1) || op.BindingFenced != (op.Mutations.BindingFence == 1) ||
		op.Retired != (op.Mutations.Retirement == 1) || op.LeaseReleased != (op.Mutations.LeaseRelease == 1) {
		return errors.New("handover-receipt-mismatch")
	}
	if op.Owner == OwnerProjmuxPrivate {
		if op.OldStopped != (op.Mutations.OldEndpointStop == 1) {
			return errors.New("handover-stop-receipt-mismatch")
		}
	} else if op.Mutations.OldEndpointStop != 0 || (op.OldStopped && (op.ExternalStopReceipt == nil || !op.StopIntended)) {
		return errors.New("foreign-stop-receipt-mismatch")
	}
	if op.BindingFenced && !op.AdmissionFenced || op.StopIntended && (!op.AdmissionFenced || !op.BindingFenced) ||
		op.OldStopped && (!op.AdmissionFenced || !op.BindingFenced) || op.Retired && !allTargetsRelaunched(op.Targets) ||
		op.LeaseReleased && !op.Retired {
		return errors.New("handover-receipt-order")
	}
	if op.Owner == OwnerProjmuxPrivate && op.ExternalStopReceipt != nil {
		return errors.New("managed-owner-cannot-use-external-stop-receipt")
	}
	if op.ExternalStopReceipt != nil && (!validIdentityToken(op.ExternalStopReceipt.ReceiptID) ||
		!op.ExternalStopReceipt.Endpoint.Same(metadata.CodexEndpointRef{StateDomainID: op.StateDomainID, EndpointGenerationID: op.OldGenerationID})) {
		return errors.New("invalid-owner-stop-receipt")
	}
	seenAgent, seenPane, seenThread := map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}
	absences, resumes, snapshots, cases, relaunches := 0, 0, 0, 0, 0
	for _, target := range op.Targets {
		if !validIdentityToken(target.AgentUID) || !validIdentityToken(target.PaneUID) ||
			strings.TrimSpace(target.PaneRuntimeID) == "" || strings.TrimSpace(target.PaneGeneration) == "" ||
			!validIdentityToken(target.RelaunchGeneration) || !validIdentityToken(target.ThreadID) {
			return errors.New("invalid-handover-target")
		}
		for value, set := range map[string]map[string]struct{}{target.AgentUID: seenAgent, target.PaneUID: seenPane, target.ThreadID: seenThread} {
			if _, exists := set[value]; exists {
				return errors.New("duplicate-handover-identity")
			}
			set[value] = struct{}{}
		}
		if target.SuccessorAbsentObserved && !op.BindingFenced || target.ResumeIntended && (!op.OldStopped || !target.SuccessorAbsentObserved) || target.Resumed && !target.ResumeIntended || target.SnapshotObserved && !target.Resumed ||
			target.EndpointCAS && !target.SnapshotObserved || target.PaneRelaunched && !target.EndpointCAS {
			return errors.New("target-handover-receipt-order")
		}
		if target.SuccessorAbsentObserved {
			absences++
		}
		if target.Resumed {
			resumes++
		}
		if target.SnapshotObserved {
			snapshots++
		}
		if target.EndpointCAS {
			cases++
		}
		if target.PaneRelaunched {
			relaunches++
		}
	}
	if absences != op.Mutations.SuccessorAbsence || resumes != op.Mutations.SuccessorResume || snapshots != op.Mutations.SuccessorSnapshot ||
		cases != op.Mutations.EndpointRefCAS || relaunches != op.Mutations.PaneRelaunch {
		return errors.New("target-handover-receipt-mismatch")
	}
	choiceCount := 0
	seenChoiceAgent, seenReplacement := map[string]struct{}{}, map[string]struct{}{}
	for _, choice := range op.Choices {
		if !validIdentityToken(choice.AgentUID) || !validIdentityToken(choice.PaneUID) || strings.TrimSpace(choice.PaneRuntimeID) == "" ||
			strings.TrimSpace(choice.PaneGeneration) == "" || (choice.Decision != NoTurnClose && choice.Decision != NoTurnReplacement) ||
			(choice.Decision == NoTurnReplacement) != validIdentityToken(choice.ReplacementAgentUID) || choice.AgentUID == choice.ReplacementAgentUID {
			return errors.New("invalid-no-turn-choice")
		}
		if _, duplicate := seenChoiceAgent[choice.AgentUID]; duplicate {
			return errors.New("duplicate-no-turn-choice-agent")
		}
		if _, overlapsTarget := seenAgent[choice.AgentUID]; overlapsTarget {
			return errors.New("choice-target-identity-overlap")
		}
		if _, replacementSubject := seenReplacement[choice.AgentUID]; replacementSubject {
			return errors.New("choice-replacement-identity-overlap")
		}
		seenChoiceAgent[choice.AgentUID] = struct{}{}
		if choice.Decision == NoTurnReplacement {
			if _, duplicate := seenReplacement[choice.ReplacementAgentUID]; duplicate {
				return errors.New("duplicate-no-turn-replacement")
			}
			if _, overlapsTarget := seenAgent[choice.ReplacementAgentUID]; overlapsTarget {
				return errors.New("replacement-target-identity-overlap")
			}
			if _, overlapsSubject := seenChoiceAgent[choice.ReplacementAgentUID]; overlapsSubject {
				return errors.New("choice-replacement-identity-overlap")
			}
			seenReplacement[choice.ReplacementAgentUID] = struct{}{}
		}
		if choice.Applied {
			choiceCount++
		}
	}
	if choiceCount != op.Mutations.NoTurnChoice {
		return errors.New("no-turn-choice-receipt-mismatch")
	}
	if op.Aborted && (!op.AbortIntended || op.OldStopped || op.Phase != HandoverAborted) {
		return errors.New("invalid-handover-abort")
	}
	if op.AbortIntended && op.OldStopped {
		return errors.New("post-stop-abort-forbidden")
	}
	return nil
}

// RequestAbort is the durable pre-effect fence between Abort and Resume. A
// stop intent is already the forward-only boundary because the coordinator
// may have performed the exact stop before losing its receipt.
func (op HandoverOperation) RequestAbort() (HandoverOperation, error) {
	if err := op.Validate(); err != nil {
		return op, err
	}
	if op.OldStopped || op.StopIntended {
		return op, errors.New("post-stop-handover-must-resume-forward")
	}
	if op.Aborted || op.AbortIntended {
		return op, nil
	}
	if op.PendingAction == HandoverActionNoTurnChoice || slices.ContainsFunc(op.Choices, func(choice NoTurnChoice) bool { return choice.Applied }) {
		return op, errors.New("pending-no-turn-choice-must-resume-forward")
	}
	op.AbortIntended = true
	op.PendingAction, op.PendingIndex = HandoverActionNone, 0
	return op, op.Validate()
}

func allTargetsSnapshot(targets []HandoverTarget) bool {
	return !slices.ContainsFunc(targets, func(t HandoverTarget) bool { return !t.SnapshotObserved })
}
func allTargetsRelaunched(targets []HandoverTarget) bool {
	return !slices.ContainsFunc(targets, func(t HandoverTarget) bool { return !t.PaneRelaunched })
}

type HandoverAction string

const (
	HandoverActionNone           HandoverAction = "none"
	HandoverActionNoTurnChoice   HandoverAction = "no-turn-choice"
	HandoverActionAdmissionFence HandoverAction = "admission-fence"
	HandoverActionBindingFence   HandoverAction = "binding-fence"
	HandoverActionCheckAbsent    HandoverAction = "check-successor-absent"
	HandoverActionAwaitOwnerStop HandoverAction = "await-owner-stop"
	HandoverActionStopOld        HandoverAction = "stop-old"
	HandoverActionResumeTarget   HandoverAction = "resume-target"
	HandoverActionSnapshotTarget HandoverAction = "snapshot-target"
	HandoverActionCASTarget      HandoverAction = "cas-target"
	HandoverActionRelaunchTarget HandoverAction = "relaunch-target"
	HandoverActionRetire         HandoverAction = "retire"
	HandoverActionReleaseLease   HandoverAction = "release-lease"
)

func (op HandoverOperation) NextAction() (HandoverAction, int) {
	return op.nextAction()
}

func (op HandoverOperation) nextAction() (HandoverAction, int) {
	if op.Aborted || op.AbortIntended || op.LeaseReleased {
		return HandoverActionNone, -1
	}
	for i, choice := range op.Choices {
		if !choice.Applied {
			return HandoverActionNoTurnChoice, i
		}
	}
	if !op.AdmissionFenced {
		return HandoverActionAdmissionFence, -1
	}
	if !op.BindingFenced {
		return HandoverActionBindingFence, -1
	}
	for i, target := range op.Targets {
		if !target.SuccessorAbsentObserved {
			return HandoverActionCheckAbsent, i
		}
	}
	if !op.OldStopped {
		if op.Owner != OwnerProjmuxPrivate && op.ExternalStopReceipt == nil {
			return HandoverActionAwaitOwnerStop, -1
		}
		return HandoverActionStopOld, -1
	}
	for i, target := range op.Targets {
		if !target.Resumed {
			return HandoverActionResumeTarget, i
		}
	}
	for i, target := range op.Targets {
		if !target.SnapshotObserved {
			return HandoverActionSnapshotTarget, i
		}
	}
	if !allTargetsSnapshot(op.Targets) {
		return HandoverActionNone, -1
	}
	for i, target := range op.Targets {
		if !target.EndpointCAS {
			return HandoverActionCASTarget, i
		}
		if !target.PaneRelaunched {
			return HandoverActionRelaunchTarget, i
		}
	}
	if !op.Retired {
		return HandoverActionRetire, -1
	}
	if !op.LeaseReleased {
		return HandoverActionReleaseLease, -1
	}
	return HandoverActionNone, -1
}

func (op HandoverOperation) WithExternalStopReceipt(receipt OwnerStopReceipt) (HandoverOperation, error) {
	if op.Owner == OwnerProjmuxPrivate || op.OldStopped || op.Aborted || op.ExternalStopReceipt != nil {
		return op, errors.New("owner-stop-receipt-not-authorized")
	}
	op.ExternalStopReceipt = &receipt
	op.Phase = HandoverPlanned
	return op, op.Validate()
}

func (op HandoverOperation) RecordIntent(action HandoverAction, index int) (HandoverOperation, error) {
	if err := op.Validate(); err != nil {
		return op, err
	}
	op.Targets = slices.Clone(op.Targets)
	if action == HandoverActionNone || action == HandoverActionAwaitOwnerStop {
		return op, errors.New("unsupported-handover-intent")
	}
	want, wantIndex := op.NextAction()
	if want != action || wantIndex != index {
		return op, errors.New("handover-intent-out-of-order")
	}
	if op.PendingAction != HandoverActionNone {
		if op.PendingAction == action && op.PendingIndex == index {
			return op, nil
		}
		return op, errors.New("another-handover-action-is-pending")
	}
	op.PendingAction, op.PendingIndex = action, index
	if action == HandoverActionStopOld {
		op.StopIntended = true
	} else if action == HandoverActionResumeTarget {
		op.Targets[index].ResumeIntended = true
	}
	return op, op.Validate()
}

func (op HandoverOperation) RecordAction(action HandoverAction, index int) (HandoverOperation, error) {
	if err := op.Validate(); err != nil {
		return op, err
	}
	op.Targets = slices.Clone(op.Targets)
	op.Choices = slices.Clone(op.Choices)
	want, wantIndex := op.NextAction()
	if want != action || wantIndex != index || op.PendingAction != action || op.PendingIndex != index {
		return op, errors.New("handover-action-out-of-order")
	}
	switch action {
	case HandoverActionNoTurnChoice:
		op.Choices[index].Applied = true
		op.Mutations.NoTurnChoice++
	case HandoverActionAdmissionFence:
		op.AdmissionFenced = true
		op.Mutations.AdmissionFence = 1
	case HandoverActionBindingFence:
		op.BindingFenced = true
		op.Mutations.BindingFence = 1
		op.Phase = HandoverFenced
	case HandoverActionCheckAbsent:
		op.Targets[index].SuccessorAbsentObserved = true
		op.Mutations.SuccessorAbsence++
	case HandoverActionStopOld:
		if !op.StopIntended {
			return op, errors.New("old-stop-intent-required")
		}
		op.OldStopped = true
		if op.Owner == OwnerProjmuxPrivate {
			op.Mutations.OldEndpointStop = 1
		}
		op.Phase = HandoverOldStopped
	case HandoverActionResumeTarget:
		if !op.Targets[index].ResumeIntended {
			return op, errors.New("successor-resume-intent-required")
		}
		op.Targets[index].Resumed = true
		op.Mutations.SuccessorResume++
		op.Phase = HandoverResuming
	case HandoverActionSnapshotTarget:
		op.Targets[index].SnapshotObserved = true
		op.Mutations.SuccessorSnapshot++
	case HandoverActionCASTarget:
		op.Targets[index].EndpointCAS = true
		op.Mutations.EndpointRefCAS++
		op.Phase = HandoverCuttingOver
	case HandoverActionRelaunchTarget:
		op.Targets[index].PaneRelaunched = true
		op.Mutations.PaneRelaunch++
	case HandoverActionRetire:
		op.Retired = true
		op.Mutations.Retirement = 1
		op.Phase = HandoverTerminal
	case HandoverActionReleaseLease:
		op.LeaseReleased = true
		op.Mutations.LeaseRelease = 1
		op.Phase = HandoverComplete
	default:
		return op, errors.New("unknown-handover-action")
	}
	op.PendingAction, op.PendingIndex = HandoverActionNone, 0
	return op, op.Validate()
}

func (op HandoverOperation) Abort() (HandoverOperation, error) {
	if err := op.Validate(); err != nil {
		return op, err
	}
	if op.OldStopped || op.StopIntended {
		return op, errors.New("post-stop-handover-must-resume-forward")
	}
	if op.Aborted {
		return op, nil
	}
	if !op.AbortIntended {
		return op, errors.New("handover-abort-intent-required")
	}
	op.Aborted, op.Phase = true, HandoverAborted
	op.PendingAction, op.PendingIndex = HandoverActionNone, 0
	// The old generation remains authoritative. Fences and explicit no-turn
	// choices are converged back by the coordinator before this receipt is final.
	op.AdmissionFenced, op.BindingFenced = false, false
	op.Mutations.AdmissionFence, op.Mutations.BindingFence = 0, 0
	return op, op.Validate()
}
