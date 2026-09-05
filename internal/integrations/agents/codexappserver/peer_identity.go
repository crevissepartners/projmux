package codexappserver

import (
	"errors"
	"net"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// PeerIdentity is the kernel process-birth witness for one direct Unix
// app-server connection. A socket pathname, PID, or negotiated version alone
// is deliberately not an endpoint witness: all three can be reused by a
// replacement process.
type PeerIdentity = coremetadata.ProcessIdentity

func peerIdentity(conn net.Conn) (PeerIdentity, error) {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return PeerIdentity{}, errors.New("codex app-server peer unavailable")
	}
	identity, err := unixPeerIdentity(unixConn)
	if err != nil || !identity.Valid() {
		return PeerIdentity{}, errors.New("codex app-server peer unavailable")
	}
	return identity, nil
}

func samePeerIdentity(expected, actual PeerIdentity) bool {
	return expected.Valid() && actual.Valid() && expected == actual
}

// SamePeerIdentity compares the complete kernel birth witness. It is exported
// for the broker's fixed-route factory; callers cannot weaken it to a PID-only
// comparison.
func SamePeerIdentity(expected, actual PeerIdentity) bool {
	return samePeerIdentity(expected, actual)
}
