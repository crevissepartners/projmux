package claude

import (
	"errors"
	"net"

	"github.com/crevissepartners/projmux/internal/core/metadata"
	"golang.org/x/sys/unix"
)

// PeerProcess authenticates Projmux's private readiness helper, not a Claude
// transport peer. No provider socket is opened by this adapter.
func PeerProcess(conn *net.UnixConn) (metadata.ProcessIdentity, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return metadata.ProcessIdentity{}, errors.New("helper peer unavailable")
	}
	var peer *unix.Ucred
	var peerErr error
	err = raw.Control(func(fd uintptr) { peer, peerErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) })
	if err != nil || peerErr != nil || peer == nil {
		return metadata.ProcessIdentity{}, errors.New("helper peer unavailable")
	}
	identity, _, err := Process(int(peer.Pid))
	if err != nil || identity.OwnerUID != peer.Uid {
		return metadata.ProcessIdentity{}, errors.New("helper peer unavailable")
	}
	return identity, nil
}
