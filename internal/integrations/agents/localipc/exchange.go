package localipc

import (
	"context"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"net"
	"time"
)

// ExchangeOwnedJSON verifies the recorded socket inode and kernel peer birth
// before writing a bounded request. It performs no discovery or retries.
func ExchangeOwnedJSON(ctx context.Context, socket string, socketIdentity SocketIdentity, host coremetadata.ProcessIdentity, request, result any) error {
	identity, err := InspectOwnedSocket(socket)
	if err != nil || identity != socketIdentity {
		return ErrSocketReplaced
	}
	deadline := time.Now().Add(Deadline)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	bounded, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	var dialer net.Dialer
	raw, err := dialer.DialContext(bounded, "unix", socket)
	if err != nil {
		return err
	}
	conn := raw.(*net.UnixConn)
	defer conn.Close()
	_ = conn.SetDeadline(deadline)
	peer, _, err := PeerProcess(conn)
	if err != nil || peer != host || ctx.Err() != nil {
		return ErrSocketReplaced
	}
	if err = WriteJSON(conn, request); err != nil {
		return err
	}
	if err = conn.CloseWrite(); err != nil {
		return err
	}
	return ReadJSON(conn, result)
}
