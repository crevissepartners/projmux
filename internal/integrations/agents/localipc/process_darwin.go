package localipc

import (
	"errors"
	"strconv"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"golang.org/x/sys/unix"
)

func Process(pid int) (coremetadata.ProcessIdentity, int, error) {
	if pid <= 0 {
		return coremetadata.ProcessIdentity{}, 0, errors.New("process unavailable")
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || info == nil || int(info.Proc.P_pid) != pid || info.Proc.P_stat == 5 {
		return coremetadata.ProcessIdentity{}, 0, errors.New("process unavailable")
	}
	start := info.Proc.P_starttime
	return coremetadata.ProcessIdentity{PID: pid, OwnerUID: info.Eproc.Ucred.Uid,
		Start: "darwin:" + strconv.FormatInt(start.Sec, 10) + ":" + strconv.FormatInt(int64(start.Usec), 10)}, int(info.Eproc.Ppid), nil
}
