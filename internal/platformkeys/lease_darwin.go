//go:build darwin

package platformkeys

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// AcquireLease ensures one broker owns a tmux socket at a time.
func AcquireLease(socket string) (func(), bool, error) {
	sum := sha256.Sum256([]byte(socket))
	path := filepath.Join(os.TempDir(), fmt.Sprintf("projmux-key-broker-%x.lock", sum[:8]))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("open native key broker lease: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if err == syscall.EWOULDBLOCK {
			return func() {}, false, nil
		}
		return nil, false, fmt.Errorf("lock native key broker lease: %w", err)
	}
	release := func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}
	return release, true, nil
}
