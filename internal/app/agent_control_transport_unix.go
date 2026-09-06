package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
)

const (
	agentControlDirName      = "agent-control"
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

func callCodexControl(ctx context.Context, stateDir string, endpoint coremetadata.CodexEndpointRef, identity codexLifecycleIdentity, request agentControlRequest) (agentControlResponse, error) {
	path, err := agentControlSocketPath(stateDir, endpoint, identity)
	if err != nil {
		return agentControlResponse{}, err
	}
	dialer := net.Dialer{Timeout: agentControlDialTimeout}
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return agentControlResponse{}, fmt.Errorf("native Codex control endpoint unavailable: %w", err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(agentControlDeadline))
	}
	var requestPayload bytes.Buffer
	if err := json.NewEncoder(&requestPayload).Encode(request); err != nil || requestPayload.Len() > agentControlMaxFrame {
		return agentControlResponse{}, errors.New("native Codex control request exceeds the bounded frame")
	}
	if _, err := conn.Write(requestPayload.Bytes()); err != nil {
		return agentControlResponse{}, fmt.Errorf("write native Codex control request: %w", err)
	}
	if unixConn, ok := conn.(*net.UnixConn); ok {
		_ = unixConn.CloseWrite()
	}
	payload, err := io.ReadAll(io.LimitReader(conn, agentControlMaxFrame+1))
	if err != nil || len(payload) > agentControlMaxFrame {
		return agentControlResponse{}, errors.New("native Codex control response exceeds the bounded frame")
	}
	var response agentControlResponse
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&response); err != nil {
		return agentControlResponse{}, fmt.Errorf("read native Codex control response: %w", err)
	}
	if hasTrailingJSON(decoder) {
		return agentControlResponse{}, errors.New("read native Codex control response: trailing frame data")
	}
	return response, nil
}

func hasTrailingJSON(decoder *json.Decoder) bool {
	var extra any
	err := decoder.Decode(&extra)
	return !errors.Is(err, io.EOF)
}

func agentControlSocketPath(stateDir string, endpoint coremetadata.CodexEndpointRef, identity codexLifecycleIdentity) (string, error) {
	stateDir = filepath.Clean(stateDir)
	if stateDir == "." || !filepath.IsAbs(stateDir) || !endpoint.Valid() || !identity.valid() {
		return "", errors.New("exact Agent control requires an absolute state directory and durable endpoint/thread identity")
	}
	// The local control address is stable for one durable endpoint/thread and
	// deliberately excludes PaneActivation.Generation. That activation epoch
	// fences writes inside the request, but it is not app-server route identity
	// and cannot retarget an Agent when admission-current changes.
	sum := sha256.Sum256([]byte(endpoint.StateDomainID + "\x00" + endpoint.EndpointGenerationID + "\x00" +
		identity.AgentUID + "\x00" + identity.PaneUID + "\x00" + identity.ThreadID))
	name := "control-" + hex.EncodeToString(sum[:12]) + ".sock"
	dir := filepath.Join(stateDir, agentControlDirName)
	path := filepath.Join(dir, name)
	if len(path) > agentControlMaxSocketLen {
		return "", errors.New("exact Agent control socket path exceeds the platform-safe bound")
	}
	return path, nil
}

func prepareAgentControlSocket(path string) error {
	return localipc.PrepareSocket(path)
}
