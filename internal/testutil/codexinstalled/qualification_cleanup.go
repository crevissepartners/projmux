package codexinstalled

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const qualificationRootPrefix = "projmux-installed-qualification-"

// CleanupQualificationRoot removes only the isolated root created by the
// scheduled runner. If a managed daemon outlived go test, its two canonical
// contained identity artifacts must agree before this function signals it.
func CleanupQualificationRoot(root string) error {
	return cleanupQualificationRoot(root, defaultTimeout)
}

func cleanupQualificationRoot(root string, timeout time.Duration) error {
	root = filepath.Clean(root)
	tmpRoot := filepath.Clean(os.TempDir())
	if !filepath.IsAbs(root) || filepath.Dir(root) != tmpRoot || !strings.HasPrefix(filepath.Base(root), qualificationRootPrefix) {
		return fmt.Errorf("qualification cleanup root is outside its exact runner boundary")
	}
	if _, err := os.Lstat(root); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect qualification cleanup root: %w", err)
	}

	codexHome := filepath.Join(root, "codex-home")
	socketPath := filepath.Join(codexHome, "app-server-control", "app-server-control.sock")
	pidPath := filepath.Join(codexHome, "app-server-daemon", "app-server.pid")
	pid, pidErr := readManagedPIDAt(pidPath)
	_, socketErr := os.Lstat(socketPath)
	socketPresent := socketErr == nil
	if socketErr != nil && !errors.Is(socketErr, fs.ErrNotExist) {
		return fmt.Errorf("inspect qualification managed socket: %w", socketErr)
	}

	if pidErr != nil {
		if !errors.Is(pidErr, fs.ErrNotExist) || socketPresent {
			return fmt.Errorf("refuse qualification cleanup without exact managed PID evidence: %w", pidErr)
		}
		return os.RemoveAll(root)
	}
	processPresent, err := processExists(pid)
	if err != nil {
		return fmt.Errorf("inspect qualification managed process: %w", err)
	}
	if !processPresent && !socketPresent {
		return os.RemoveAll(root)
	}

	started, err := readManagedStartResultAt(filepath.Join(root, "managed-start-result"))
	if err != nil {
		return fmt.Errorf("refuse qualification cleanup without exact managed start evidence: %w", err)
	}
	if err := validateManagedIdentity(started, started.PID, pid, socketPath); err != nil {
		return fmt.Errorf("refuse qualification cleanup for mismatched managed identity: %w", err)
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("signal exact qualification managed process: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := waitForRetirement(ctx, socketPath, pid); err != nil {
		return fmt.Errorf("qualification managed process did not retire: %w", err)
	}
	return os.RemoveAll(root)
}

func processExists(pid int) (bool, error) {
	err := syscall.Kill(pid, 0)
	switch {
	case err == nil, errors.Is(err, syscall.EPERM):
		return true, nil
	case errors.Is(err, syscall.ESRCH):
		return false, nil
	default:
		return false, err
	}
}
