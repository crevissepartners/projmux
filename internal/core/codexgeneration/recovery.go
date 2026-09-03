package codexgeneration

import (
	"errors"
	"strings"
)

// ColdRecoveryDecision is the closed recovery ordering for a generation that
// disappeared without a terminal handover receipt. Reusing its exact verified
// bundle and endpoint identity always wins over cross-generation fallback.
type ColdRecoveryDecision string

const (
	ColdRecoveryRestartSameGeneration ColdRecoveryDecision = "restart-same-generation"
	ColdRecoveryQualifiedHandover     ColdRecoveryDecision = "qualified-handover"
	ColdRecoveryBlocked               ColdRecoveryDecision = "blocked"
)

type ColdRecoveryEvidence struct {
	Owner                    OwnerClass
	SameGenerationBundle     bool
	SameGenerationLaunchAuth bool
	QualifiedVersionPair     bool
}

func DecideColdRecovery(evidence ColdRecoveryEvidence) ColdRecoveryDecision {
	if evidence.Owner == OwnerProjmuxPrivate && evidence.SameGenerationBundle && evidence.SameGenerationLaunchAuth {
		return ColdRecoveryRestartSameGeneration
	}
	if evidence.QualifiedVersionPair {
		return ColdRecoveryQualifiedHandover
	}
	return ColdRecoveryBlocked
}

// ColdRecoveryOperation is the content-free, exact-generation receipt for a
// same-generation durable-host restart attempted before cross-generation
// fallback. Its launch operation ref is the original generation creation
// authority; a handover operation may only link to it, never mint another
// process authority for the same endpoint.
type ColdRecoveryOperation struct {
	JournalVersion      int    `json:"journalVersion"`
	OperationRef        string `json:"operationRef"`
	RollingOperationRef string `json:"rollingOperationRef"`
	StateDomainID       string `json:"stateDomainID"`
	GenerationID        string `json:"generationID"`
	LaunchOperationRef  string `json:"launchOperationRef"`
	Intended            bool   `json:"intended"`
	Recovered           bool   `json:"recovered"`
	Mutations           int    `json:"mutations"`
}

const ColdRecoveryJournalVersion = 1

func NewColdRecoveryOperation(operationRef, rollingRef, stateDomainID, generationID, launchRef string) (ColdRecoveryOperation, error) {
	op := ColdRecoveryOperation{JournalVersion: ColdRecoveryJournalVersion, OperationRef: operationRef,
		RollingOperationRef: rollingRef, StateDomainID: stateDomainID, GenerationID: generationID,
		LaunchOperationRef: launchRef, Intended: true}
	return op, op.Validate()
}

func (op ColdRecoveryOperation) Validate() error {
	if op.JournalVersion != ColdRecoveryJournalVersion || !validIdentityToken(op.OperationRef) ||
		!validIdentityToken(op.RollingOperationRef) || !validIdentityToken(op.StateDomainID) ||
		!validIdentityToken(op.GenerationID) || !validIdentityToken(op.LaunchOperationRef) || !op.Intended ||
		op.Mutations < 0 || op.Mutations > 1 || op.Recovered != (op.Mutations == 1) ||
		strings.TrimSpace(op.OperationRef) != op.OperationRef {
		return errors.New("invalid-cold-recovery-operation")
	}
	return nil
}

func (op ColdRecoveryOperation) RecordRecovered() (ColdRecoveryOperation, error) {
	if err := op.Validate(); err != nil {
		return op, err
	}
	if op.Recovered {
		return op, nil
	}
	op.Recovered, op.Mutations = true, 1
	return op, op.Validate()
}
