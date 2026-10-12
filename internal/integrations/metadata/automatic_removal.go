package metadata

import (
	"fmt"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"io"
)

// RecordAutomaticRemovals appends one best-effort row per removed Agent after commit.
// Automatic routes carry no ambient actor identity.
func RecordAutomaticRemovals(stateDir func() (string, error), before, after coremetadata.Registry, operation string, stderr io.Writer) {
	for _, agent := range coremetadata.RemovedAgents(before, after) {
		uid, err := coremetadata.NewUID(coremetadata.KindAgent)
		if err != nil {
			uid = ""
		}
		target := DeletionTarget{Kind: string(coremetadata.KindAgent), UID: agent.Metadata.UID, Name: agent.Metadata.Name}
		affected := []DeletionAffected{{Kind: target.Kind, UID: target.UID, Name: target.Name, Action: DeletionAffectedDeleted}}
		RecordDeletion(stateDir, NewDeletionRecord(operation, uid, DeletionVia("automatic"), DeletionActor{}, []DeletionTarget{target}, affected), stderr)
	}
}
func RecordProtectionUnavailable(stateDir func() (string, error), stderr io.Writer) {
	uid, err := coremetadata.NewUID(coremetadata.KindAgent)
	if err != nil {
		uid = ""
	}
	RecordDeletion(stateDir, NewDeletionRecord("registry-recovery-restore", uid, DeletionVia("automatic"), DeletionActor{Basis: "protection-unavailable"}, nil, nil), stderr)
	if stderr != nil {
		fmt.Fprintln(stderr, "registry recovery: protection-unavailable")
	}
}
