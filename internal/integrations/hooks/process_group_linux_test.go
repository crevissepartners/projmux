package hooks

import (
	"bufio"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// TestReraiseSignalIsHandledOnTheCallingThread proves reraiseSignal aims the
// signal at the calling thread. The test blocks the signal on its own locked
// thread and re-raises it: a thread-directed signal then waits in this
// thread's private pending set (SigPnd), while a process-directed kill never
// lands there, since it goes to the shared set (ShdPnd) and another thread
// handles it. The outcome is therefore deterministic, not timing-dependent.
func TestReraiseSignalIsHandledOnTheCallingThread(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			if signal.Ignored(sig) {
				t.Skipf("%v is ignored by this process; Notify would un-ignore it", sig)
			}

			ch := make(chan os.Signal, 2)
			signal.Notify(ch, sig)

			pending, err := reraiseWhileBlockedOnThisThread(sig)
			if err != nil {
				// The signal may still be queued for ch; keep catching it.
				t.Fatalf("re-raise %v: %v", sig, err)
			}
			if !pending {
				t.Errorf("%v is not pending on the calling thread after reraiseSignal; the re-raise was not thread-directed", sig)
			}

			select {
			case got := <-ch:
				if got != sig {
					t.Fatalf("received %v, want %v", got, sig)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("%v was not delivered within 5s of the re-raise", sig)
			}
			signal.Stop(ch)
		})
	}
}

// reraiseWhileBlockedOnThisThread blocks sig on a locked thread, calls
// reraiseSignal, and reports whether sig is then in that thread's private
// pending set. It restores the mask, which delivers a pending sig on this
// thread, and unlocks the thread before it returns.
func reraiseWhileBlockedOnThisThread(sig syscall.Signal) (pending bool, err error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	var block, old unix.Sigset_t
	sigsetAdd(&block, sig)
	if err := unix.PthreadSigmask(unix.SIG_BLOCK, &block, &old); err != nil {
		return false, fmt.Errorf("block signal: %w", err)
	}
	defer func() {
		if restoreErr := unix.PthreadSigmask(unix.SIG_SETMASK, &old, nil); restoreErr != nil && err == nil {
			err = fmt.Errorf("restore signal mask: %w", restoreErr)
		}
	}()

	reraiseSignal(sig)

	mask, err := threadPendingSignals()
	if err != nil {
		return false, err
	}
	return mask&(1<<(uint(sig)-1)) != 0, nil
}

// sigsetAdd adds sig to set, whose word size depends on the architecture.
func sigsetAdd(set *unix.Sigset_t, sig syscall.Signal) {
	bit := uint(sig) - 1
	wordBits := uint(unsafe.Sizeof(set.Val[0])) * 8
	set.Val[bit/wordBits] |= 1 << (bit % wordBits)
}

// threadPendingSignals parses SigPnd, the calling thread's private pending
// set, from /proc/thread-self/status. The thread must be locked.
func threadPendingSignals() (uint64, error) {
	f, err := os.Open("/proc/thread-self/status")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		value, ok := strings.CutPrefix(scanner.Text(), "SigPnd:")
		if !ok {
			continue
		}
		return strconv.ParseUint(strings.TrimSpace(value), 16, 64)
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("no SigPnd line in /proc/thread-self/status")
}
