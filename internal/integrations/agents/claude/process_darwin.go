package claude

import (
	"errors"
	"strconv"

	"github.com/crevissepartners/projmux/internal/core/metadata"
	"golang.org/x/sys/unix"
)

// Process uses the kernel's absolute process birth time on both Darwin targets.
func Process(pid int) (metadata.ProcessIdentity, int, error) {
	if pid <= 0 {
		return metadata.ProcessIdentity{}, 0, errors.New("process unavailable")
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || info == nil || int(info.Proc.P_pid) != pid || info.Proc.P_stat == 5 {
		return metadata.ProcessIdentity{}, 0, errors.New("process unavailable")
	}
	start := info.Proc.P_starttime
	return metadata.ProcessIdentity{PID: pid, OwnerUID: info.Eproc.Ucred.Uid,
		Start: "darwin:" + strconv.FormatInt(start.Sec, 10) + ":" + strconv.FormatInt(int64(start.Usec), 10)}, int(info.Eproc.Ppid), nil
}
