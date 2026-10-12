package app

import (
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"strings"
	"testing"
)

func TestDetachedRelaunchRecoveryPreservesDestinationContract(t *testing.T) {
	reg := coremetadata.Registry{}
	target := coremetadata.Agent{}
	target.Metadata.UID, target.Metadata.Name, target.Spec.Provider = "agt-source", "source", aiModeClaude
	request := agentRelaunchRequest{detach: true, host: "process", model: "new-model"}
	recovery := tmuxTransferRecovery(reg, target, request)
	if strings.Contains(recovery, "--detach") || strings.Contains(recovery, "--host process") || strings.Contains(recovery, "--model") {
		t.Fatal("tmux recovery retained target-only options", recovery)
	}
	retry := relaunchRecoveryCommand(reg, target, request)
	if !strings.Contains(retry, "agent relaunch") || !strings.Contains(retry, "--detach") || !strings.Contains(retry, "--host process") {
		t.Fatal("detached retry lost lifetime/destination", retry)
	}
}
