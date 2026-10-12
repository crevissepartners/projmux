package app

import (
	"context"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

var NewRuntimeLedger = mutationruntime.NewRuntimeLedger
var NewRuntimeLedgerAt = mutationruntime.NewRuntimeLedgerAt
var MaterializeMutationAction = mutationruntime.MaterializeMutationAction

func AdoptCreatedPane(ctx context.Context, runtime *materializer, paneID, sessionID, windowID string, pane coremetadata.Pane, ledger *RuntimeLedger) error {
	return mutationruntime.AdoptCreatedPane(ctx, &runtime.MaterializeRuntime, paneID, sessionID, windowID, pane, ledger)
}
