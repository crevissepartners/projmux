package app

import (
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// mintGeneration issues one activation generation for a create/resume route.
func (c *createCommand) mintGeneration() (string, error) {
	mint := c.newGeneration
	if mint == nil {
		mint = coremetadata.NewGeneration
	}
	return mint()
}

// issuePaneActivation mints a generation and binds it to one allocated Pane
// inside the caller's open transaction.
//
// It runs in the metadata phase, before any tmux call, because the generation
// is an *input* to the launch argv: the process has to be started already
// knowing the value it will quote back. Recording it first also means a create
// that fails during materialization leaves a Pane whose generation matches
// nothing that ever ran, which is exactly the state a receipt guard rejects.
func (c *createCommand) issuePaneActivation(
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	paneUID, agentUID, operationID string,
) (superviseSpec, error) {
	return issuePaneActivation(c.mintGeneration, working, mutator, paneUID, agentUID, operationID)
}
