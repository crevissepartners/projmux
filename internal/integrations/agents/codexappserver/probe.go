package codexappserver

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"time"
)

const DefaultProbeTimeout = 750 * time.Millisecond

type commandStream struct {
	stdin  io.WriteCloser
	stdout io.ReadCloser
	cmd    *exec.Cmd
}

func (s *commandStream) Read(p []byte) (int, error)  { return s.stdout.Read(p) }
func (s *commandStream) Write(p []byte) (int, error) { return s.stdin.Write(p) }
func (s *commandStream) Close() error {
	_ = s.stdin.Close()
	_ = s.stdout.Close()
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	return s.cmd.Wait()
}

// ProbeDefaultProxy performs an initialize-only probe against the existing
// local app-server control socket through Codex's stdio proxy. It never starts,
// restarts, configures, logs into, or otherwise mutates the daemon.
func ProbeDefaultProxy(ctx context.Context, timeout time.Duration, projmuxVersion string, hookAvailable bool) Health {
	return probeProxy(ctx, timeout, projmuxVersion, hookAvailable, exec.LookPath, func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, "codex", "app-server", "proxy")
	})
}

func probeProxy(ctx context.Context, timeout time.Duration, projmuxVersion string, hookAvailable bool, lookPath func(string) (string, error), command func(context.Context) *exec.Cmd) Health {
	if timeout <= 0 {
		timeout = DefaultProbeTimeout
	}
	if _, err := lookPath("codex"); err != nil {
		return Decide(AvailabilityUnavailable, ReasonExecutableMissing, "", EndpointStdioProxy, ConnectionDisconnected, hookAvailable)
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := command(probeCtx)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Decide(AvailabilityUnavailable, ReasonEndpointUnavailable, "", EndpointStdioProxy, ConnectionDisconnected, hookAvailable)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return Decide(AvailabilityUnavailable, ReasonEndpointUnavailable, "", EndpointStdioProxy, ConnectionDisconnected, hookAvailable)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return Decide(AvailabilityUnavailable, ReasonEndpointUnavailable, "", EndpointStdioProxy, ConnectionDisconnected, hookAvailable)
	}
	stream := &commandStream{stdin: stdin, stdout: stdout, cmd: cmd}
	websocket, upgradeErr := upgradeProxyWebSocket(probeCtx, stream)
	if upgradeErr != nil {
		_ = stream.Close()
		switch {
		case errors.Is(upgradeErr, context.DeadlineExceeded), errors.Is(probeCtx.Err(), context.DeadlineExceeded):
			return Decide(AvailabilityTimeout, ReasonTimeout, "", EndpointStdioProxy, ConnectionTimedOut, hookAvailable)
		case errors.Is(upgradeErr, ErrProtocol):
			return Decide(AvailabilityProtocolError, ReasonProtocolError, "", EndpointStdioProxy, ConnectionProtocolErr, hookAvailable)
		default:
			return Decide(AvailabilityUnavailable, ReasonEndpointUnavailable, "", EndpointStdioProxy, ConnectionDisconnected, hookAvailable)
		}
	}
	client := NewClient(websocket)
	version, initErr := client.Initialize(probeCtx, projmuxVersion)
	_ = client.Close()
	if initErr == nil {
		return Decide(AvailabilityAvailable, ReasonNone, version, EndpointStdioProxy, ConnectionReady, hookAvailable)
	}
	switch {
	case errors.Is(initErr, context.DeadlineExceeded), errors.Is(probeCtx.Err(), context.DeadlineExceeded):
		return Decide(AvailabilityTimeout, ReasonTimeout, "", EndpointStdioProxy, ConnectionTimedOut, hookAvailable)
	case errors.Is(initErr, ErrUnsupported):
		return Decide(AvailabilityUnsupported, ReasonUnsupported, "", EndpointStdioProxy, ConnectionDisconnected, hookAvailable)
	case errors.Is(initErr, ErrProtocol):
		return Decide(AvailabilityProtocolError, ReasonProtocolError, "", EndpointStdioProxy, ConnectionProtocolErr, hookAvailable)
	default:
		return Decide(AvailabilityUnavailable, ReasonEndpointUnavailable, "", EndpointStdioProxy, ConnectionDisconnected, hookAvailable)
	}
}
