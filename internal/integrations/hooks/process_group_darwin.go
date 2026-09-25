package hooks

import (
	"os"
	"syscall"
)

// reraiseSignal sends sig to projmux. The kill is process-directed and
// therefore not deterministic: Go exposes no thread-directed kill on darwin,
// and XNU picks the receiving thread itself, so a caller with the default
// disposition may still pass Run before it dies.
func reraiseSignal(sig os.Signal) {
	if s, ok := sig.(syscall.Signal); ok {
		_ = syscall.Kill(os.Getpid(), s)
	}
}
