package codexappserver

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"time"
)

// OpenPrivateUnix opens and initializes an explicitly named private listener.
// It performs no discovery or lifecycle action and accepts only an absolute
// socket path. Generation hosting and ownership remain outside this adapter;
// Phase 0 uses it only for isolated fixed-version conformance.
func OpenPrivateUnix(ctx context.Context, socketPath string, timeout time.Duration, projmuxVersion string, experimental bool) (*Client, error) {
	socketPath = filepath.Clean(strings.TrimSpace(socketPath))
	if !filepath.IsAbs(socketPath) {
		return nil, ErrDisconnected
	}
	if timeout <= 0 {
		timeout = DefaultProbeTimeout
	}
	setupCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	connection, err := (&net.Dialer{}).DialContext(setupCtx, "unix", socketPath)
	if err != nil {
		return nil, classifyProxyOpenError(setupCtx, err)
	}
	websocket, err := upgradeProxyWebSocket(setupCtx, connection)
	if err != nil {
		_ = connection.Close()
		return nil, classifyProxyOpenError(setupCtx, err)
	}
	client := NewClient(websocket)
	if _, err := client.initialize(setupCtx, projmuxVersion, experimental); err != nil {
		_ = client.Close()
		return nil, classifyProxyOpenError(setupCtx, err)
	}
	return client, nil
}
