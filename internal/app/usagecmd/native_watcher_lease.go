package usagecmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	localstate "github.com/crevissepartners/projmux/internal/state"
)

var errNativeWatcherLeaseBusy = errors.New("codex native watcher lease busy")

func acquireNativeWatcherLease(path string) (func(), bool, error) {
	if err := localstate.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return nil, false, fmt.Errorf("create codex native watcher lease directory: %w", err)
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, false, errors.New("codex native watcher lease is a symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	// #nosec G304 -- path is the resolved usage-state directory joined with the fixed watcher lease name; the private parent and preceding symlink check are enforced here.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, localstate.PrivateFileMode)
	if err != nil {
		return nil, false, fmt.Errorf("open codex native watcher lease: %w", err)
	}
	localstate.RepairPrivateFile(path)
	if err := tryNativeWatcherPlatformLock(file); err != nil {
		_ = file.Close()
		if errors.Is(err, errNativeWatcherLeaseBusy) {
			return func() {}, false, nil
		}
		return nil, false, err
	}
	release := func() {
		_ = unlockNativeWatcherPlatformLock(file)
		_ = file.Close()
	}
	return release, true, nil
}
