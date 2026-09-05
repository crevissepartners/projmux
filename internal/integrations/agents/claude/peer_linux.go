package claude

import (
	"errors"
	"net"

	"github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
)

// PeerProcess authenticates Projmux's private readiness helper, not a Claude
// transport peer. No provider socket is opened by this adapter.
func PeerProcess(conn *net.UnixConn) (metadata.ProcessIdentity, error) {
	identity, _, err := localipc.PeerProcess(conn)
	if err != nil {
		return metadata.ProcessIdentity{}, errors.New("helper peer unavailable")
	}
	return identity, nil
}
