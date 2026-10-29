package localipc

import (
	"context"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"net"
	"time"
)

// ExchangeOwnedJSON verifies the recorded socket inode and kernel peer birth
// before writing a bounded request. It performs no discovery or retries.
// Once connected, it reads the receipt until the deadline even if ctx is
// cancelled, so a request with an effect keeps a known outcome.
func ExchangeOwnedJSON(ctx context.Context, socket string, socketIdentity SocketIdentity, host coremetadata.ProcessIdentity, request, result any) error {
	return exchangeOwnedJSON(ctx, socket, socketIdentity, host, request, result, false)
}

// ReadOwnedJSON is ExchangeOwnedJSON for a request without effect: cancelling
// ctx also ends a connected read at once and returns ctx's error. Requests
// that change the owner keep ExchangeOwnedJSON.
func ReadOwnedJSON(ctx context.Context, socket string, socketIdentity SocketIdentity, host coremetadata.ProcessIdentity, request, result any) error {
	err := exchangeOwnedJSON(ctx, socket, socketIdentity, host, request, result, true)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}

func exchangeOwnedJSON(ctx context.Context, socket string, socketIdentity SocketIdentity, host coremetadata.ProcessIdentity, request, result any, wake bool) error {
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
	if wake {
		// A past deadline wakes the pending write or read.
		stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
		defer stop()
	}
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
