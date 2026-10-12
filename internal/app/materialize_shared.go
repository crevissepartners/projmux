package app

import (
	"context"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

var NewRuntimeLedger = mutationruntime.NewRuntimeLedger
var NewRuntimeLedgerAt = mutationruntime.NewRuntimeLedgerAt
var MaterializeMutationAction = mutationruntime.MaterializeMutationAction

func (m *materializer) SplitPane(ctx context.Context, anchorPaneID, placement, cwd string, command []string, processAnchor *processTerminalTarget) (string, error) {
	return m.MaterializeRuntime.SplitPane(ctx, anchorPaneID, placement, cwd, command, processAnchor.admitSplitAnchor)
}

func AdoptCreatedPane(ctx context.Context, runtime *materializer, paneID, sessionID, windowID string, pane coremetadata.Pane, ledger *RuntimeLedger) error {
	return mutationruntime.AdoptCreatedPane(ctx, &runtime.MaterializeRuntime, paneID, sessionID, windowID, pane, ledger)
}
