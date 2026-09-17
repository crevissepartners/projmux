package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
)

// Options configures the two listeners.
type Options struct {
	// Addr is the TCP address. Empty disables TCP.
	Addr string
	// SocketPath is the unix socket. Empty disables the socket.
	SocketPath string
	// Token is the start token every TCP request must carry (see NewToken).
	// Serve refuses to open TCP without one. The socket does not check it.
	Token string
	// Ready, when set, is called once both listeners are bound, with the TCP
	// address actually bound (useful when Addr asks for port 0).
	Ready func(tcpAddr string)
	Log   *slog.Logger
}

// Serve runs the API on the configured listeners until ctx is done or a
// listener fails.
func Serve(ctx context.Context, backend Backend, opts Options) error {
	if opts.Addr == "" && opts.SocketPath == "" {
		return errors.New("web: no listener configured")
	}
	if opts.Addr != "" && opts.Token == "" {
		return errors.New("web: the TCP listener needs a start token")
	}
	server := New(backend, opts.Log)
	handler := server.Handler()

	var servers []*http.Server
	errs := make(chan error, 2)
	start := func(listener net.Listener, h http.Handler) {
		srv := &http.Server{
			Handler:           h,
			ReadHeaderTimeout: 10 * time.Second,
		}
		servers = append(servers, srv)
		go func() {
			if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs <- err
			}
		}()
	}

	tcpAddr := ""
	if opts.Addr != "" {
		listener, err := net.Listen("tcp", opts.Addr)
		if err != nil {
			return fmt.Errorf("web: listen %s: %w", opts.Addr, err)
		}
		tcpAddr = listener.Addr().String()
		_, port, err := net.SplitHostPort(tcpAddr)
		if err != nil {
			_ = listener.Close()
			return fmt.Errorf("web: bound address %s: %w", tcpAddr, err)
		}
		start(listener, guardLoopback(port, requireToken(port, opts.Token, opts.Log, handler)))
	}
	if opts.SocketPath != "" {
		// The socket is created 0600 in an owner-only directory, and a live or
		// foreign socket at the path is refused rather than replaced.
		listener, err := localipc.Listen(opts.SocketPath)
		if err != nil {
			shutdown(servers)
			return fmt.Errorf("web: listen %s: %w", opts.SocketPath, err)
		}
		defer func() { _ = listener.Close() }()
		start(listener.Unix, handler)
	}
	if opts.Ready != nil {
		opts.Ready(tcpAddr)
	}

	select {
	case <-ctx.Done():
		shutdown(servers)
		return nil
	case err := <-errs:
		shutdown(servers)
		return fmt.Errorf("web: %w", err)
	}
}

func shutdown(servers []*http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, srv := range servers {
		_ = srv.Shutdown(ctx)
	}
}
