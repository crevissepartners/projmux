package runtime

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
)

// CodexHostTransferPath is the owner-private journal an unfinished Codex host
// transfer retains for one Agent under the application state root. The CLI
// recovery and the public SDK relaunch both look for it there.
func CodexHostTransferPath(stateDir, agentUID string) string {
	sum := sha256.Sum256([]byte(agentUID))
	return filepath.Join(stateDir, "codex-host-transfers", fmt.Sprintf("%x.json", sum[:]))
}
