package projmux

import (
	"errors"
	"net"

	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
)

// PrivateSocketListener is a listening Unix socket that only its owner can
// reach. It is a net.Listener, so it can be handed to http.Serve as it is.
type PrivateSocketListener struct {
	inner *localipc.Listener
}

// ListenPrivateSocket listens on the absolute, clean Unix socket path, at most
// 100 bytes long. It creates the parent directory as 0700 and the socket as
// 0600. A socket nobody listens on that the current user owns is replaced. A
// path with a live socket, or with anything else on it (a regular file, a
// symlink, another user's socket), is refused and left as it was. The error is
// the one the projmux commands report for the same path. It takes no Client and
// reads no Registry.
func ListenPrivateSocket(path string) (*PrivateSocketListener, error) {
	inner, err := localipc.Listen(path)
	if err != nil {
		return nil, err
	}
	return &PrivateSocketListener{inner: inner}, nil
}

// Accept waits for and returns the next connection.
func (l *PrivateSocketListener) Accept() (net.Conn, error) {
	if l == nil || l.inner == nil || l.inner.Unix == nil {
		return nil, errors.New("private socket listener is not open")
	}
	return l.inner.Unix.Accept()
}

// Close stops listening and removes the socket file this listener created. A
// socket that another listener has since put on the same path is not removed.
// A second Close returns the network error of closing a closed listener and
// does nothing else.
func (l *PrivateSocketListener) Close() error {
	if l == nil || l.inner == nil {
		return nil
	}
	return l.inner.Close()
}

// Addr returns the listening address.
func (l *PrivateSocketListener) Addr() net.Addr {
	if l == nil || l.inner == nil || l.inner.Unix == nil {
		return nil
	}
	return l.inner.Unix.Addr()
}

// Path returns the socket path the listener was created with.
func (l *PrivateSocketListener) Path() string {
	if l == nil || l.inner == nil {
		return ""
	}
	return l.inner.Path
}
