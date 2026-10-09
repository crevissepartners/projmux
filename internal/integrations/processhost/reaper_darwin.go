package processhost

import (
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"syscall"
	"time"
)

func prepareReaper() error { return nil }

// macOS has no subreaper: launchd adopts and reaps orphan descendants.
func reapOrphans(int) (stop func()) { return func() {} }

func reapGroup(group int, grace time.Duration) error {
	return waitGroupAbsent(group, grace, func() error { return syscall.Kill(-group, 0) })
}

func waitGroupAbsent(group int, grace time.Duration, probe func() error) error {
	// macOS reparents orphan descendants to launchd, which reaps them. Wait for
	// the exact process group to disappear rather than counting a kill as proof.
	deadline := time.Now().Add(grace)
	for {
		err := probe()
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		// A group containing only zombies can return EPERM while launchd is
		// still reaping it. EPERM is not absence: only ESRCH completes cleanup.
		// Persistent denial remains a bounded failure, never a success receipt.
		if err != nil && !errors.Is(err, syscall.EPERM) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("owned group %d absence unconfirmed (last probe: %v)", group, err)
		}
		time.Sleep(time.Millisecond)
	}
}

// Darwin's sysctl reports the owned, unreaped child's exit without consuming
// wait status. SZOMB=5 is the public extern_proc state in XNU bsd/sys/proc.h:
// https://github.com/apple/darwin-xnu/blob/main/bsd/sys/proc.h
// No PID lookup confers ownership here: only ServeSupervisor's own child enters.
func observeChildExit(pid int) error {
	const zombie = 5
	for {
		info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
		if err != nil {
			return err
		}
		if int(info.Proc.P_pid) != pid || int(info.Eproc.Ppid) != os.Getpid() || int(info.Eproc.Pgid) != pid {
			return errors.New("owned child observation lost")
		}
		if info.Proc.P_stat == zombie {
			return nil
		}
		time.Sleep(time.Millisecond)
	}
}
