package localipc

import (
	"errors"
	"net"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"golang.org/x/sys/unix"
)

func PeerProcess(conn *net.UnixConn) (coremetadata.ProcessIdentity, int, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return coremetadata.ProcessIdentity{}, 0, errors.New("peer unavailable")
	}
	var pid int
	var peerErr error
	err = raw.Control(func(fd uintptr) { pid, peerErr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID) })
	if err != nil || peerErr != nil {
		return coremetadata.ProcessIdentity{}, 0, errors.New("peer unavailable")
	}
	identity, parent, err := Process(pid)
	if err != nil {
		return coremetadata.ProcessIdentity{}, 0, errors.New("peer unavailable")
	}
	return identity, parent, nil
}
