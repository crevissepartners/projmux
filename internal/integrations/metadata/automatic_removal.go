package metadata

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"io"
)

// RecordAutomaticRemovals appends one best-effort row per removed Agent after commit.
// Automatic routes carry no ambient actor identity.

// NewOperationID mints the shared operation identifier for creation and deletion.
func NewOperationID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("create: read operation id entropy: %w", err)
	}
	return "op-" + hex.EncodeToString(b), nil
}
func RecordAutomaticRemovals(stateDir func() (string, error), before, after coremetadata.Registry, operation string, stderr io.Writer) {
	recordAutomaticRemovals(stateDir, before, after, operation, DeletionActor{}, false, stderr)
}
func recordAutomaticRemovals(stateDir func() (string, error), before, after coremetadata.Registry, operation string, actor DeletionActor, unknown bool, stderr io.Writer) {
	removed := coremetadata.RemovedAgents(before, after)
	if len(removed) == 0 && !unknown {
		return
	}
	operationID, _ := NewOperationID()
	for _, agent := range removed {
		target := DeletionTarget{Kind: string(coremetadata.KindAgent), UID: agent.Metadata.UID, Name: agent.Metadata.Name}
		affected := []DeletionAffected{{Kind: target.Kind, UID: target.UID, Name: target.Name, Action: DeletionAffectedDeleted}}
		RecordDeletion(stateDir, NewDeletionRecord(operation, operationID, DeletionVia("automatic"), actor, []DeletionTarget{target}, affected), stderr)
	}
	if unknown && len(removed) == 0 {
		RecordDeletion(stateDir, NewDeletionRecord(operation, operationID, DeletionVia("automatic"), actor, nil, nil), stderr)
	}
}
func RecordProtectionUnavailable(stateDir func() (string, error), before, after coremetadata.Registry, stderr io.Writer) {
	recordAutomaticRemovals(stateDir, before, after, "registry-recovery-restore", DeletionActor{Basis: "protection-unavailable"}, true, stderr)
	if stderr != nil {
		fmt.Fprintln(stderr, "registry recovery: protection-unavailable")
	}
}
