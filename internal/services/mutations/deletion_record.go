package mutations

import (
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"io"
)

const (
	DeletionRecordsFile                = intmetadata.DeletionRecordsFile
	DeletionRecordSchemaVersion        = intmetadata.DeletionRecordSchemaVersion
	DeletionViaCLI                     = intmetadata.DeletionViaCLI
	DeletionViaUI                      = intmetadata.DeletionViaUI
	DeletionViaPrune                   = intmetadata.DeletionViaPrune
	DeletionViaSDK                     = intmetadata.DeletionViaSDK
	DeletionOperationDeleteWindow      = intmetadata.DeletionOperationDeleteWindow
	DeletionOperationDeletePane        = intmetadata.DeletionOperationDeletePane
	DeletionOperationDeleteAgent       = intmetadata.DeletionOperationDeleteAgent
	DeletionOperationUnregisterProject = intmetadata.DeletionOperationUnregisterProject
	DeletionOperationDeleteProject     = intmetadata.DeletionOperationDeleteProject
	DeletionOperationPruneAgent        = intmetadata.DeletionOperationPruneAgent
	DeletionOperationPruneProject      = intmetadata.DeletionOperationPruneProject
	DeletionSkipStateDirUnavailable    = intmetadata.DeletionSkipStateDirUnavailable
	DeletionSkipAppendFailed           = intmetadata.DeletionSkipAppendFailed
	DeletionSkipOperationIDMissing     = intmetadata.DeletionSkipOperationIDMissing
	DeletionNotRecordedDiagnosticFmt   = intmetadata.DeletionNotRecordedDiagnosticFmt
)

type DeletionVia = intmetadata.DeletionVia
type DeletionRecord = intmetadata.DeletionRecord
type DeletionActor = intmetadata.DeletionActor
type DeletionTarget = intmetadata.DeletionTarget
type DeletionAffected = intmetadata.DeletionAffected
type DeletionRecordLog = intmetadata.DeletionRecordLog

func NewDeletionRecord(operation, operationID string, via DeletionVia, actor DeletionActor, targets []DeletionTarget, affected []DeletionAffected) DeletionRecord {
	return intmetadata.NewDeletionRecord(operation, operationID, via, actor, targets, affected)
}
func DeletionAffectedBetween(before, after coremetadata.Registry) []DeletionAffected {
	return intmetadata.DeletionAffectedBetween(before, after)
}
func RecordDeletion(stateDir func() (string, error), record DeletionRecord, stderr io.Writer) {
	intmetadata.RecordDeletion(stateDir, record, stderr)
}

func DeletionTargetsOf(plan DeletePlan) []DeletionTarget {
	targets := make([]DeletionTarget, 0, len(plan.Targets))
	for _, target := range plan.Targets {
		targets = append(targets, DeletionTarget{Kind: string(plan.Kind), UID: target.Match.UID, Name: target.Match.Name})
	}
	return targets
}

const DeletionAffectedDeleted = intmetadata.DeletionAffectedDeleted
