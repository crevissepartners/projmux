package app

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	localstate "github.com/crevissepartners/projmux/internal/state"
)

const codexAuthorityFenceDir = "codex-authority-fences"

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
	// #nosec G304 -- codexAuthorityFencePath returns the private state
	// directory plus a digest of the Registry-authenticated Pane uid.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, localstate.PrivateFileMode)
	if err != nil {
		return nil, fmt.Errorf("open Codex authority fence: %w", err)
	}
	localstate.RepairPrivateFile(path)
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock Codex authority fence: %w", err)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
			_ = file.Close()
		})
	}, nil
}

func (c *aiCommand) codexAuthorityFencePath(paneUID string) (string, error) {
	if strings.TrimSpace(paneUID) == "" {
		return "", fmt.Errorf("codex authority fence requires pane uid")
	}
	paths, err := configPaths(c.homeDir, c.lookupEnv)
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
	paneUID = strings.TrimSpace(paneUID)
	if paneUID == "" {
		return "", fmt.Errorf("codex authority fence requires pane uid")
	}
	dir := filepath.Join(stateDir, codexAuthorityFenceDir)
	if err := localstate.EnsurePrivateDir(dir); err != nil {
		return "", fmt.Errorf("create Codex authority fence directory: %w", err)
	}
	digest := sha256.Sum256([]byte(paneUID))
	return filepath.Join(dir, fmt.Sprintf("%x.lock", digest)), nil
}
