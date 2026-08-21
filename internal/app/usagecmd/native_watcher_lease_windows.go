//go:build windows

package usagecmd

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func tryNativeWatcherPlatformLock(file *os.File) error {
	err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, new(windows.Overlapped),
	)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return errNativeWatcherLeaseBusy
	}
	return err
}

func unlockNativeWatcherPlatformLock(file *os.File) error {
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, new(windows.Overlapped))
}
