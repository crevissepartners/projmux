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
	var peer *unix.Ucred
	var peerErr error
	err = raw.Control(func(fd uintptr) { peer, peerErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) })
	if err != nil || peerErr != nil || peer == nil {
		return coremetadata.ProcessIdentity{}, 0, errors.New("peer unavailable")
	}
	identity, parent, err := Process(int(peer.Pid))
	if err != nil || identity.OwnerUID != peer.Uid {
		return coremetadata.ProcessIdentity{}, 0, errors.New("peer unavailable")
	}
	return identity, parent, nil
}
