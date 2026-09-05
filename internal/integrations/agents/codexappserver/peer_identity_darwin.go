package codexappserver

import (
	"errors"
	"net"
	"strconv"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"golang.org/x/sys/unix"
)

func unixPeerIdentity(conn *net.UnixConn) (coremetadata.ProcessIdentity, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return coremetadata.ProcessIdentity{}, errors.New("peer unavailable")
	}
	var pid int
	var peerErr error
	if err := raw.Control(func(fd uintptr) {
		pid, peerErr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	}); err != nil || peerErr != nil {
		return coremetadata.ProcessIdentity{}, errors.New("peer unavailable")
	}
	return processIdentity(pid)
}

func processIdentity(pid int) (coremetadata.ProcessIdentity, error) {
	if pid <= 0 {
		return coremetadata.ProcessIdentity{}, errors.New("process unavailable")
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || info == nil || int(info.Proc.P_pid) != pid || info.Proc.P_stat == 5 {
		return coremetadata.ProcessIdentity{}, errors.New("process unavailable")
	}
	start := info.Proc.P_starttime
	return coremetadata.ProcessIdentity{
		PID: pid, OwnerUID: info.Eproc.Ucred.Uid,
		Start: "darwin:" + strconv.FormatInt(start.Sec, 10) + ":" + strconv.FormatInt(int64(start.Usec), 10),
	}, nil
}
