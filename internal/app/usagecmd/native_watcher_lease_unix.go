//go:build !windows

package usagecmd

import (
	"errors"
	"os"
	"syscall"
)

func tryNativeWatcherPlatformLock(file *os.File) error {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return errNativeWatcherLeaseBusy
	}
	return err
}

func unlockNativeWatcherPlatformLock(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
