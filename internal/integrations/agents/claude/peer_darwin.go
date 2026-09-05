package claude

import (
	"errors"
	"net"

	"github.com/crevissepartners/projmux/internal/core/metadata"
	"golang.org/x/sys/unix"
)

func PeerProcess(conn *net.UnixConn) (metadata.ProcessIdentity, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return metadata.ProcessIdentity{}, errors.New("helper peer unavailable")
	}
	var pid int
	var peerErr error
	err = raw.Control(func(fd uintptr) { pid, peerErr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID) })
	if err != nil || peerErr != nil {
		return metadata.ProcessIdentity{}, errors.New("helper peer unavailable")
	}
	identity, _, err := Process(pid)
	if err != nil {
		return metadata.ProcessIdentity{}, errors.New("helper peer unavailable")
	}
	return identity, nil
}
