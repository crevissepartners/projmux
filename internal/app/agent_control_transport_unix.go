package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/crevissepartners/projmux/internal/integrations/agentcontrol"
	"io"
	"net"
	"os"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
)

const (
	agentControlDirName      = agentcontrol.ControlDir
	agentControlMaxFrame     = localipc.MaxFrameBytes
	agentControlDeadline     = localipc.Deadline
	agentControlDialTimeout  = localipc.DialTimeout
	agentControlMaxSocketLen = localipc.MaxSocketPath
)

type codexControlServer struct {
	listener *net.UnixListener
	path     string
	info     localipc.SocketIdentity
	epoch    *codexControlEpoch
	done     chan struct{}
}

func startCodexControlServer(stateDir string, endpoint coremetadata.CodexEndpointRef, epoch *codexControlEpoch) (*codexControlServer, error) {
	if epoch == nil || !endpoint.Valid() || !epoch.identity.valid() || epoch.epoch == "" {
		return nil, errors.New("exact Agent control server has no active identity or epoch")
	}
	path, err := agentControlSocketPath(stateDir, endpoint, epoch.identity)
	if err != nil {
		return nil, err
	}
	if err := prepareAgentControlSocket(path); err != nil {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen exact Agent control socket: %w", err)
	}
	listener.SetUnlinkOnClose(false)
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("secure exact Agent control socket: %w", err)
	}
	info, err := localipc.InspectOwnedSocket(path)
	if err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, errors.New("exact Agent control listener is not a socket")
	}
	server := &codexControlServer{listener: listener, path: path, info: info, epoch: epoch, done: make(chan struct{})}
	go server.serve()
	return server, nil
}

func (s *codexControlServer) serve() {
	defer close(s.done)
	for {
		conn, err := s.listener.AcceptUnix()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *codexControlServer) handle(conn *net.UnixConn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(agentControlDeadline))
	var request agentControlRequest
	if err := localipc.ReadJSON(conn, &request); err != nil {
		detail := "exact Agent control request was malformed"
		if errors.Is(err, localipc.ErrFrameTooLarge) {
			detail = "exact Agent control request exceeded the bounded frame"
		}
		s.writeResponse(conn, refusedControl("invalid-frame", detail))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), agentControlDeadline)
	defer cancel()
	s.writeResponse(conn, s.epoch.Handle(ctx, request))
}

func (s *codexControlServer) writeResponse(conn *net.UnixConn, response agentControlResponse) {
	payload, err := localipc.MarshalJSON(response)
	if err != nil {
		payload, _ = localipc.MarshalJSON(refusedControl("response-too-large", "exact Agent control detail is too large to display safely"))
	}
	_, _ = conn.Write(payload)
}

func (s *codexControlServer) Close() error {
	if s == nil {
		return nil
	}
	if s.epoch != nil {
		s.epoch.Revoke()
	}
	// Revocation is independent of listener lifetime: a transport that never
	// became externally reachable still has an epoch that must be made stale.
	if s.listener == nil {
		return nil
	}
	err := s.listener.Close()
	<-s.done
	if current, statErr := localipc.InspectOwnedSocket(s.path); statErr == nil && current == s.info {
		_ = os.Remove(s.path)
	}
	return err
}

// errAgentControlNotSent marks a callCodexControl failure that returned before
// any request byte was written to the control socket: the socket path, the
// dial, or the request frame failed, so the server never saw the request.
// Every failure from the write onward may have reached the server and is not
// marked.
var errAgentControlNotSent = agentcontrol.ErrNotSent

// agentControlNotSentError carries a failure unchanged in its text while
// errors.Is also matches errAgentControlNotSent.
type agentControlNotSentError struct{ err error }

func (e *agentControlNotSentError) Error() string   { return e.err.Error() }
func (e *agentControlNotSentError) Unwrap() []error { return []error{e.err, errAgentControlNotSent} }

func agentControlNotSent(err error) error { return &agentControlNotSentError{err: err} }

func callCodexControl(ctx context.Context, stateDir string, endpoint coremetadata.CodexEndpointRef, identity codexLifecycleIdentity, request agentControlRequest) (agentControlResponse, error) {
	r, err := agentcontrol.Call(ctx, stateDir, endpoint, agentcontrol.Identity(identity), agentcontrol.Request{Operation: request.Operation, Identity: agentcontrol.Identity(request.Identity), Epoch: request.Epoch, Text: request.Text, RequestKey: request.RequestKey, Decision: request.Decision}, hasTrailingJSON)
	if errors.Is(err, errAgentControlNotSent) {
		err = agentControlNotSent(err)
	}
	return agentControlResponse(r), err
}

func hasTrailingJSON(decoder *json.Decoder) bool {
	var extra any
	err := decoder.Decode(&extra)
	return !errors.Is(err, io.EOF)
}

func agentControlSocketPath(stateDir string, endpoint coremetadata.CodexEndpointRef, identity codexLifecycleIdentity) (string, error) {
	return agentcontrol.SocketPath(stateDir, endpoint, agentcontrol.Identity(identity))
}

func prepareAgentControlSocket(path string) error {
	return localipc.PrepareSocket(path)
}
