//go:build !projmux_process_cli_regression

package app

import (
	"net"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
)

// These defaults have no environment, filesystem, state, or waiting effects.
// Only the explicit regression build contains the cross-process test gate.
func observeClaudeHostCheckForRegression(_ *claudeProcessService, _ claudeProcessCheck, _ coremetadata.ProcessIdentity, _ *coremetadata.ClaudeRegistration, _ claudeProcessCheckResult) {
}

func observeClaudeHelperRetainedForRegression(_ claudeEndpointBootstrap) {}

func readClaudeHostResponseForRegression(conn *net.UnixConn, result *claudeProcessCheckResult, _ claudeProcessProof, _ claudeProcessCheck) error {
	return localipc.ReadJSON(conn, result)
}
