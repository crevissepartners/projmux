package codexgeneration

import (
	"errors"
	"slices"
	"strings"
)

// RollingJournalVersion is the durable Phase 4 admission-switch operation
// schema. It deliberately contains only stable identities and content-free
// obligation classifications.
const RollingJournalVersion = 1

type RollingPhase string

const (
	RollingPlanned          RollingPhase = "planned"
	RollingCandidateReady   RollingPhase = "candidate-ready"
	RollingAdmissionCurrent RollingPhase = "admission-current"
	RollingDraining         RollingPhase = "draining"
	RollingHandoverPending  RollingPhase = "handover-pending"
	RollingAborted          RollingPhase = "aborted"
)

func validRollingPhase(phase RollingPhase) bool {
	return slices.Contains([]RollingPhase{
		RollingPlanned, RollingCandidateReady, RollingAdmissionCurrent,
		RollingDraining, RollingHandoverPending, RollingAborted,
	}, phase)
}

// DrainLedgerEntry is the complete Phase 4 retirement-readiness projection.
// It has no field capable of storing prompt, transcript, message, approval
// content, path, socket, credential, or provider payload material.
type DrainLedgerEntry struct {
	AgentUID             string          `json:"agentUID"`
	EndpointGenerationID string          `json:"endpointGenerationID"`
	State                ObligationState `json:"state"`
	BlocksHandover       bool            `json:"blocksHandover"`
}

// ProjectDrainLedger classifies every exact old-generation obligation. Active,
// pending, no-turn and unknown rows block destructive handover; a completed
// persisted thread is explicitly eligible. Closed rows are retained as
// non-blocking audit facts rather than silently disappearing.
func ProjectDrainLedger(oldGenerationID string, obligations []AgentObligation) ([]DrainLedgerEntry, error) {
	if !validIdentityToken(oldGenerationID) {
		return nil, errors.New("old-generation-required")
	}
	seen := make(map[string]struct{}, len(obligations))
	ledger := make([]DrainLedgerEntry, 0, len(obligations))
	for _, obligation := range obligations {
		if obligation.EndpointGenerationID != oldGenerationID {
			continue
		}
		if !validIdentityToken(obligation.AgentUID) || !validObligationState(obligation.State) {
			return nil, errors.New("invalid-obligation")
		}
		if _, exists := seen[obligation.AgentUID]; exists {
			return nil, errors.New("duplicate-agent-obligation")
		}
		seen[obligation.AgentUID] = struct{}{}
		blocks := obligation.State == ObligationActive || obligation.State == ObligationApprovalPending ||
			obligation.State == ObligationNoTurn || obligation.State == ObligationUnknown
		ledger = append(ledger, DrainLedgerEntry{
			AgentUID: obligation.AgentUID, EndpointGenerationID: oldGenerationID,
			State: obligation.State, BlocksHandover: blocks,
		})
	}
	slices.SortFunc(ledger, func(a, b DrainLedgerEntry) int { return strings.Compare(a.AgentUID, b.AgentUID) })
	return ledger, nil
}

// RollingUpgradeOperation is the crash-resumable Phase 4 journal projection.
// Receipts are monotonic. In particular, admission-current and drain-publish
// can each be committed at most once, and the handover request is one
// generation-wide operation ref rather than one request per Agent.
type RollingUpgradeOperation struct {
	JournalVersion          int                       `json:"journalVersion"`
	OperationRef            string                    `json:"operationRef"`
	StateDomainID           string                    `json:"stateDomainID"`
	OldGenerationID         string                    `json:"oldGenerationID"`
	TargetGenerationID      string                    `json:"targetGenerationID"`
	Phase                   RollingPhase              `json:"phase"`
	CandidateLaunchIntended bool                      `json:"candidateLaunchIntended"`
	CandidateStarted        bool                      `json:"candidateStarted"`
	CandidateReady          bool                      `json:"candidateReady"`
	AdmissionCommitted      bool                      `json:"admissionCommitted"`
	DrainPublished          bool                      `json:"drainPublished"`
	HandoverRequested       bool                      `json:"handoverRequested"`
	AbortIntended           bool                      `json:"abortIntended"`
	Aborted                 bool                      `json:"aborted"`
	Ledger                  []DrainLedgerEntry        `json:"ledger,omitempty"`
	Mutations               RollingOperationMutations `json:"mutations"`
}

// RollingOperationMutations is a content-free receipt counter. OldEndpointStop
// and every Phase 5 effect must remain zero in every valid Phase 4 operation.
type RollingOperationMutations struct {
	CandidateLaunchIntent int `json:"candidateLaunchIntent"`
	CandidateStart        int `json:"candidateStart"`
	CandidateCleanup      int `json:"candidateCleanup"`
	AdmissionCommit       int `json:"admissionCommit"`
	DrainPublish          int `json:"drainPublish"`
	HandoverRequest       int `json:"handoverRequest"`
	OldEndpointStop       int `json:"oldEndpointStop"`
	SuccessorResume       int `json:"successorResume"`
	EndpointRefCAS        int `json:"endpointRefCAS"`
	PaneRelaunch          int `json:"paneRelaunch"`
	Retirement            int `json:"retirement"`
	LeaseRelease          int `json:"leaseRelease"`
	ForeignAdoption       int `json:"foreignAdoption"`
}

func NewRollingUpgradeOperation(operationRef, stateDomainID, oldGenerationID, targetGenerationID string) (RollingUpgradeOperation, error) {
	op := RollingUpgradeOperation{
		JournalVersion: RollingJournalVersion, OperationRef: operationRef,
		StateDomainID: stateDomainID, OldGenerationID: oldGenerationID,
		TargetGenerationID: targetGenerationID, Phase: RollingPlanned,
	}
	if err := op.Validate(); err != nil {
		return RollingUpgradeOperation{}, err
	}
	return op, nil
}

func (op RollingUpgradeOperation) Validate() error {
	if op.JournalVersion != RollingJournalVersion || !validIdentityToken(op.OperationRef) ||
		!validIdentityToken(op.StateDomainID) || !validIdentityToken(op.OldGenerationID) ||
		!validIdentityToken(op.TargetGenerationID) || op.OldGenerationID == op.TargetGenerationID ||
		!validRollingPhase(op.Phase) {
		return errors.New("invalid-rolling-operation")
	}
	if op.Mutations.CandidateLaunchIntent < 0 || op.Mutations.CandidateLaunchIntent > 1 ||
		op.Mutations.CandidateStart < 0 || op.Mutations.CandidateStart > 1 ||
		op.Mutations.CandidateCleanup < 0 || op.Mutations.CandidateCleanup > 1 ||
		op.Mutations.AdmissionCommit < 0 || op.Mutations.AdmissionCommit > 1 ||
		op.Mutations.DrainPublish < 0 || op.Mutations.DrainPublish > 1 ||
		op.Mutations.HandoverRequest < 0 || op.Mutations.HandoverRequest > 1 {
		return errors.New("duplicate-phase4-effect")
	}
	if op.Mutations.OldEndpointStop != 0 || op.Mutations.SuccessorResume != 0 ||
		op.Mutations.EndpointRefCAS != 0 || op.Mutations.PaneRelaunch != 0 ||
		op.Mutations.Retirement != 0 || op.Mutations.LeaseRelease != 0 ||
		op.Mutations.ForeignAdoption != 0 {
		return errors.New("phase5-effect-in-phase4-operation")
	}
	if op.CandidateLaunchIntended != (op.Mutations.CandidateLaunchIntent == 1) ||
		op.CandidateStarted != (op.Mutations.CandidateStart == 1) ||
		op.AdmissionCommitted != (op.Mutations.AdmissionCommit == 1) ||
		op.DrainPublished != (op.Mutations.DrainPublish == 1) ||
		op.HandoverRequested != (op.Mutations.HandoverRequest == 1) {
		return errors.New("rolling-receipt-mismatch")
	}
	if op.CandidateStarted && !op.CandidateLaunchIntended || op.CandidateReady && !op.CandidateStarted ||
		op.AdmissionCommitted && !op.CandidateReady || op.DrainPublished && !op.AdmissionCommitted ||
		op.HandoverRequested && !op.DrainPublished {
		return errors.New("rolling-receipt-order")
	}
	if op.Aborted && op.Phase != RollingAborted {
		return errors.New("rolling-abort-phase-mismatch")
	}
	if op.AbortIntended && op.AdmissionCommitted {
		return errors.New("abort-intent-after-admission")
	}
	if op.Aborted && !op.AbortIntended {
		return errors.New("rolling-abort-without-intent")
	}
	if op.Mutations.CandidateCleanup != 0 && (!op.CandidateStarted || !op.Aborted || op.AdmissionCommitted) {
		return errors.New("candidate-cleanup-receipt-mismatch")
	}
	for _, row := range op.Ledger {
		if !validIdentityToken(row.AgentUID) || row.EndpointGenerationID != op.OldGenerationID ||
			!validObligationState(row.State) || row.BlocksHandover !=
			(row.State == ObligationActive || row.State == ObligationApprovalPending || row.State == ObligationNoTurn || row.State == ObligationUnknown) {
			return errors.New("invalid-drain-ledger")
		}
	}
	return nil
}

type RollingAction string

const (
	RollingActionNone             RollingAction = "none"
	RollingActionPrepareCandidate RollingAction = "prepare-candidate"
	RollingActionCommitAdmission  RollingAction = "commit-admission"
	RollingActionPublishDrain     RollingAction = "publish-drain"
)

// NextAction returns the sole missing Phase 4 effect. A resumed coordinator
// therefore cannot replay a receipt that is already durable.
func (op RollingUpgradeOperation) NextAction() RollingAction {
	if op.Aborted || op.AbortIntended {
		return RollingActionNone
	}
	if !op.CandidateReady {
		return RollingActionPrepareCandidate
	}
	if !op.AdmissionCommitted {
		return RollingActionCommitAdmission
	}
	if !op.DrainPublished {
		return RollingActionPublishDrain
	}
	return RollingActionNone
}

// RequestAbort is the durable pre-effect fence between Abort and Resume.
// Once this receipt wins the journal lock, Resume has no next action and can
// never publish admission-current while exact candidate cleanup is in flight.
func (op RollingUpgradeOperation) RequestAbort() (RollingUpgradeOperation, bool, error) {
	if err := op.Validate(); err != nil {
		return op, false, err
	}
	if op.AdmissionCommitted {
		return op, false, errors.New("admission-already-committed")
	}
	if op.AbortIntended {
		return op, false, nil
	}
	op.AbortIntended = true
	return op, true, op.Validate()
}

// RecordCandidateLaunchIntent is the main-journal mirror of the durable
// operation-owned launch intent. It precedes every possible process effect.
func (op RollingUpgradeOperation) RecordCandidateLaunchIntent() (RollingUpgradeOperation, bool, error) {
	if err := op.Validate(); err != nil {
		return op, false, err
	}
	if op.Aborted || op.AbortIntended || op.AdmissionCommitted {
		return op, false, errors.New("candidate-launch-intent-not-authorized")
	}
	if op.CandidateLaunchIntended {
		return op, false, nil
	}
	op.CandidateLaunchIntended = true
	op.Mutations.CandidateLaunchIntent = 1
	return op, true, op.Validate()
}

// RecordCandidateStart copies the supervisor's durable running receipt. It is
// separate from readiness so a crash exactly between launch and proof remains
// representable and recoverable without another start.
func (op RollingUpgradeOperation) RecordCandidateStart() (RollingUpgradeOperation, bool, error) {
	if err := op.Validate(); err != nil {
		return op, false, err
	}
	if op.Aborted || op.AbortIntended || !op.CandidateLaunchIntended || op.AdmissionCommitted {
		return op, false, errors.New("candidate-start-not-authorized")
	}
	if op.CandidateStarted {
		return op, false, nil
	}
	op.CandidateStarted = true
	op.Mutations.CandidateStart = 1
	return op, true, op.Validate()
}

// RecordAction records one completed external effect. Re-recording the same
// action is an idempotent no-op, while out-of-order actions are refused.
func (op RollingUpgradeOperation) RecordAction(action RollingAction, ledger []DrainLedgerEntry) (RollingUpgradeOperation, bool, error) {
	if err := op.Validate(); err != nil {
		return op, false, err
	}
	if op.Aborted {
		return op, false, errors.New("rolling-operation-aborted")
	}
	switch action {
	case RollingActionPrepareCandidate:
		if op.CandidateReady {
			return op, false, nil
		}
		if op.NextAction() != action || !op.CandidateStarted {
			return op, false, errors.New("rolling-action-out-of-order")
		}
		op.CandidateReady = true
		op.Phase = RollingCandidateReady
	case RollingActionCommitAdmission:
		if op.AdmissionCommitted {
			return op, false, nil
		}
		if op.NextAction() != action {
			return op, false, errors.New("rolling-action-out-of-order")
		}
		op.AdmissionCommitted = true
		op.Mutations.AdmissionCommit = 1
		op.Phase = RollingAdmissionCurrent
	case RollingActionPublishDrain:
		if op.DrainPublished {
			return op, false, nil
		}
		if op.NextAction() != action {
			return op, false, errors.New("rolling-action-out-of-order")
		}
		for _, row := range ledger {
			if row.EndpointGenerationID != op.OldGenerationID {
				return op, false, errors.New("drain-ledger-generation-mismatch")
			}
		}
		op.DrainPublished = true
		op.Mutations.DrainPublish = 1
		op.Ledger = slices.Clone(ledger)
		op.Phase = RollingDraining
	default:
		return op, false, errors.New("unknown-rolling-action")
	}
	if err := op.Validate(); err != nil {
		return RollingUpgradeOperation{}, false, err
	}
	return op, true, nil
}

// RequestGenerationHandover promotes any Draining Offline resume into the one
// generation-wide Phase 5 request. Repeated Agent resumes reuse OperationRef
// and do not add another receipt or per-Agent operation.
func (op RollingUpgradeOperation) RequestGenerationHandover() (RollingUpgradeOperation, bool, error) {
	if err := op.Validate(); err != nil {
		return op, false, err
	}
	if !op.DrainPublished || op.Aborted {
		return op, false, errors.New("generation-not-draining")
	}
	if op.HandoverRequested {
		return op, false, nil
	}
	op.HandoverRequested = true
	op.Mutations.HandoverRequest = 1
	op.Phase = RollingHandoverPending
	return op, true, op.Validate()
}

// Abort is intentionally non-destructive. Phase 4 may close an operation only
// before admission changes; reverting admission or stopping either endpoint is
// a separate lifecycle effect and is therefore not smuggled into this method.
func (op RollingUpgradeOperation) Abort() (RollingUpgradeOperation, bool, error) {
	if err := op.Validate(); err != nil {
		return op, false, err
	}
	if op.Aborted {
		return op, false, nil
	}
	if !op.AbortIntended {
		return op, false, errors.New("abort-intent-required")
	}
	if op.CandidateStarted || op.CandidateReady || op.AdmissionCommitted {
		return op, false, errors.New("prepared-candidate-requires-exact-cleanup")
	}
	op.Aborted = true
	op.Phase = RollingAborted
	return op, true, op.Validate()
}

// AbortPreparedCandidate records that the operation-owned candidate was
// stopped and its runtime socket/intent were cleaned before admission commit.
// Its immutable bundle lease is deliberately retained: LeaseRelease and all
// other Phase 5 effects remain zero.
func (op RollingUpgradeOperation) AbortPreparedCandidate() (RollingUpgradeOperation, bool, error) {
	if err := op.Validate(); err != nil {
		return op, false, err
	}
	if op.Aborted {
		return op, false, nil
	}
	if !op.AbortIntended {
		return op, false, errors.New("abort-intent-required")
	}
	if !op.CandidateStarted || op.AdmissionCommitted {
		return op, false, errors.New("candidate-cleanup-not-authorized")
	}
	op.Mutations.CandidateCleanup = 1
	op.Aborted = true
	op.Phase = RollingAborted
	return op, true, op.Validate()
}

// AbortRecoveredCandidate catches up the main journal when the supervisor's
// durable receipt proves that a process started before the coordinator died.
// It is called only after exact guard-owned cleanup succeeds.
func (op RollingUpgradeOperation) AbortRecoveredCandidate() (RollingUpgradeOperation, bool, error) {
	if err := op.Validate(); err != nil {
		return op, false, err
	}
	if op.Aborted {
		return op, false, nil
	}
	if !op.AbortIntended {
		return op, false, errors.New("abort-intent-required")
	}
	if op.AdmissionCommitted {
		return op, false, errors.New("candidate-cleanup-not-authorized")
	}
	if !op.CandidateLaunchIntended {
		op.CandidateLaunchIntended = true
		op.Mutations.CandidateLaunchIntent = 1
	}
	if !op.CandidateStarted {
		op.CandidateStarted = true
		op.Mutations.CandidateStart = 1
	}
	op.Mutations.CandidateCleanup = 1
	op.Aborted = true
	op.Phase = RollingAborted
	return op, true, op.Validate()
}
