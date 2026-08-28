package codexappserver

import (
	"context"
	"errors"
	"os/exec"
	"time"
)

// OpenDefaultProxy opens and initializes one client through Codex's official
// stdio proxy. Daemon readiness remains the caller's responsibility through
// EnsureDefaultProxyReady; this function never starts, configures, or logs in
// to Codex. The setup is bounded and every failure is collapsed to the
// existing content-free transport errors.
func OpenDefaultProxy(ctx context.Context, timeout time.Duration, projmuxVersion string) (*Client, error) {
	return openDefaultProxy(ctx, timeout, projmuxVersion, false)
}

// openDefaultProxy is the single stdio-proxy opener. experimental selects the
// explicit experimental initialize handshake; everything else, including the
// zero daemon lifecycle mutation contract, is identical on both paths.
func openDefaultProxy(ctx context.Context, timeout time.Duration, projmuxVersion string, experimental bool) (*Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := exec.LookPath("codex")
	if err != nil {
		return nil, ErrDisconnected
	}
	if timeout <= 0 {
		timeout = DefaultProbeTimeout
	}
	setupCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Do not bind the process to setupCtx: cancelling the setup deadline after
	// a successful initialize would kill a client before its capability request.
	// The returned Client owns and closes commandStream immediately after use.
	// #nosec G204 -- path comes only from exec.LookPath("codex") above and the argv is the fixed app-server proxy command.
	cmd := exec.Command(path, "app-server", "proxy")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, ErrDisconnected
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, ErrDisconnected
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, ErrDisconnected
	}
	stream := &commandStream{stdin: stdin, stdout: stdout, cmd: cmd}
	websocket, err := upgradeProxyWebSocket(setupCtx, stream)
	if err != nil {
		_ = stream.Close()
		return nil, classifyProxyOpenError(setupCtx, err)
	}
	client := NewClient(websocket)
	if _, err := client.initialize(setupCtx, projmuxVersion, experimental); err != nil {
		_ = client.Close()
		return nil, classifyProxyOpenError(setupCtx, err)
	}
	return client, nil
}

func classifyProxyOpenError(ctx context.Context, err error) error {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, ErrUnsupported):
		return ErrUnsupported
	case errors.Is(err, ErrProtocol):
		return ErrProtocol
	default:
		return ErrDisconnected
	}
}
