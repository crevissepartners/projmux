package state

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	// PrivateDirMode is the creation and repair mode for directories that
	// contain user-sensitive projmux state.
	PrivateDirMode os.FileMode = 0o700
	// PrivateFileMode is the creation and repair mode for user-sensitive
	// projmux state files.
	PrivateFileMode os.FileMode = 0o600
)

// EnsurePrivateDir creates path with a private default and best-effort repairs
// an existing directory. Permission repair deliberately does not fail the
// caller: DrvFs and other filesystems may reject chmod or not persist Unix mode
// bits, and state/config functionality must continue there.
func EnsurePrivateDir(path string) error {
	if strings.TrimSpace(path) == "" || path == "." {
		return nil
	}
	if err := os.MkdirAll(path, PrivateDirMode); err != nil {
		return err
	}
	repairPrivatePath(path, PrivateDirMode)
	return nil
}

// RepairPrivateFile best-effort repairs the file and its immediate containing
// directory. Missing paths and unsupported chmod semantics are harmless.
func RepairPrivateFile(path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	dir := filepath.Dir(path)
	if dir != "." {
		repairPrivatePath(dir, PrivateDirMode)
	}
	repairPrivatePath(path, PrivateFileMode)
}

func repairPrivatePath(path string, mode os.FileMode) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return
	}
	repairPrivatePathWith(path, mode, os.Chmod)
}

func repairPrivatePathWith(path string, mode os.FileMode, chmod func(string, os.FileMode) error) {
	if strings.TrimSpace(path) == "" || chmod == nil {
		return
	}
	_ = chmod(path, mode)
}
