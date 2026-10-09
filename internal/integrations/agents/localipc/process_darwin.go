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
	infos, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if errors.Is(err, unix.ESRCH) {
		return coremetadata.ProcessIdentity{}, 0, ErrProcessAbsent
	}
	if err != nil {
		return coremetadata.ProcessIdentity{}, 0, err
	}
	if len(infos) == 0 {
		return coremetadata.ProcessIdentity{}, 0, ErrProcessAbsent
	}
	if len(infos) != 1 {
		return coremetadata.ProcessIdentity{}, 0, errors.New("process identity unavailable")
	}
	info := infos[0]
	if info.Proc.P_pid == 0 || info.Proc.P_stat == 5 {
		return coremetadata.ProcessIdentity{}, 0, ErrProcessAbsent
	}
	if int(info.Proc.P_pid) != pid {
		return coremetadata.ProcessIdentity{}, 0, errors.New("process identity unavailable")
	}
	start := info.Proc.P_starttime
	return coremetadata.ProcessIdentity{PID: pid, OwnerUID: info.Eproc.Ucred.Uid,
		Start: "darwin:" + strconv.FormatInt(start.Sec, 10) + ":" + strconv.FormatInt(int64(start.Usec), 10)}, int(info.Eproc.Ppid), nil
}
