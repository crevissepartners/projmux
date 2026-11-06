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
// 100 bytes long. Pass a directory used for this socket alone: a parent
// directory that does not exist is created as 0700, and one that already exists
// and belongs to the current user is changed to 0700. That change stays even
// when the listen is then refused. The socket is 0600. A socket nobody listens
// on that the current user owns is replaced. A path with a live socket, or with
// anything else on it (a regular file, a symlink, another user's socket), is
// refused and left as it was. The error is the one the projmux commands report
// for the same path. It takes no Client and reads no Registry.
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

// Close stops listening and removes the socket path only while it still names
// the socket this listener created; a path that holds a different socket, or
// nothing, is left alone. That check and the removal are two steps, not one
// atomic step, so a replacement that arrives at the same path while Close runs
// can be removed: do not run another listener on the same path at the same
// time. A second Close returns the error of closing a closed listener and
// repeats the same identity-checked cleanup.
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
