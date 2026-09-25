package hooks

import (
	"os"
	"runtime"
	"syscall"
)

// reraiseSignal sends sig to the calling thread. An unblocked signal aimed at
// the current thread is handled before tgkill returns, so a caller with the
// default disposition dies here rather than after the hook runner returns. A
// process-directed kill gives no such guarantee: the kernel may hand it to
// another thread while this one keeps running.
func reraiseSignal(sig os.Signal) {
	s, ok := sig.(syscall.Signal)
	if !ok {
		return
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	_ = syscall.Tgkill(os.Getpid(), syscall.Gettid(), s)
}
