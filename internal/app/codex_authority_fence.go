package app

import (
	"fmt"
	"github.com/crevissepartners/projmux/internal/integrations/agentcontrol"
	"strings"

	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

const codexAuthorityFenceDir = agentcontrol.FenceDir

// acquireCodexAuthorityFence serializes the native authority transition with
// the complete provider-hook semantic write set for one resource Pane. The
// kernel owns the lock lifetime: normal returns unlock explicitly, while a
// panic, signal, or process exit closes the descriptor and cannot strand a
// reconnect behind a stale userspace token.
func (c *aiCommand) acquireCodexAuthorityFence(paneUID string) (func(), error) {
	if c == nil {
		return nil, fmt.Errorf("codex authority fence requires command")
	}
	if c != nil && c.acquireCodexAuthority != nil {
		return c.acquireCodexAuthority(paneUID)
	}
	path, err := c.codexAuthorityFencePath(paneUID)
	if err != nil {
		return nil, err
	}
	return mutationruntime.AcquireNativeAuthorityFenceAt(path)
}

func (c *aiCommand) codexAuthorityFencePath(paneUID string) (string, error) {
	if strings.TrimSpace(paneUID) == "" {
		return "", fmt.Errorf("codex authority fence requires pane uid")
	}
	paths, err := c.observerConfigPaths()
	if err != nil {
		return "", fmt.Errorf("resolve Codex authority fence: %w", err)
	}
	return codexAuthorityFencePathIn(paths.StateDir, paneUID)
}

// codexAuthorityFencePathIn derives the exact fence file for one Pane inside a
// private state directory. The derivation lives here once because the writer
// and every reader that must observe a settled authority transition have to
// contend on the same kernel lock; restating the digest or the directory on
// either side would silently split them into two independent fences.
func codexAuthorityFencePathIn(stateDir, paneUID string) (string, error) {
	return agentcontrol.FencePath(stateDir, paneUID)
}

// codexAuthorityFenceFileIn derives the fence file without creating anything.
//
// It is split out for the one caller that must not create: a diagnostics read
// establishes whether a Pane's transitions are published under a fence at all,
// and a reader that created the directory on the way to asking would answer its
// own question. Every other caller goes through codexAuthorityFencePathIn, so
// the digest and the directory name are still stated once.
func codexAuthorityFenceFileIn(stateDir, paneUID string) (string, error) {
	return agentcontrol.FenceFile(stateDir, paneUID)
}
