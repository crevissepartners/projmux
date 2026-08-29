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
	"syscall"
	"time"
)

const (
	agentControlDirName      = "agent-control"
	agentControlMaxFrame     = 64 << 10
	agentControlDeadline     = 5 * time.Second
	agentControlDialTimeout  = 500 * time.Millisecond
	agentControlMaxSocketLen = 100
)

type codexControlServer struct {
	listener *net.UnixListener
	path     string
	info     os.FileInfo
	epoch    *codexControlEpoch
	done     chan struct{}
}

func startCodexControlServer(stateDir string, epoch *codexControlEpoch) (*codexControlServer, error) {
	if epoch == nil || !epoch.identity.valid() || epoch.epoch == "" {
		return nil, errors.New("exact Agent control server has no active identity or epoch")
	}
	path, err := agentControlSocketPath(stateDir, epoch.identity)
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
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("secure exact Agent control socket: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
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
	payload, err := io.ReadAll(io.LimitReader(conn, agentControlMaxFrame+1))
	if err != nil || len(payload) > agentControlMaxFrame {
		s.writeResponse(conn, refusedControl("invalid-frame", "exact Agent control request exceeded the bounded frame"))
		return
	}
	var request agentControlRequest
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&request); err != nil || hasTrailingJSON(decoder) {
		s.writeResponse(conn, refusedControl("invalid-frame", "exact Agent control request was malformed"))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), agentControlDeadline)
	defer cancel()
	s.writeResponse(conn, s.epoch.Handle(ctx, request))
}

func (s *codexControlServer) writeResponse(conn *net.UnixConn, response agentControlResponse) {
	var payload bytes.Buffer
	if err := json.NewEncoder(&payload).Encode(response); err != nil || payload.Len() > agentControlMaxFrame {
		payload.Reset()
		_ = json.NewEncoder(&payload).Encode(refusedControl("response-too-large", "exact Agent control detail is too large to display safely"))
	}
	_, _ = conn.Write(payload.Bytes())
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
	if current, statErr := os.Lstat(s.path); statErr == nil && os.SameFile(s.info, current) && current.Mode()&os.ModeSocket != 0 {
		_ = os.Remove(s.path)
	}
	return err
}

func callCodexControl(ctx context.Context, stateDir string, identity codexLifecycleIdentity, request agentControlRequest) (agentControlResponse, error) {
	path, err := agentControlSocketPath(stateDir, identity)
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

func agentControlSocketPath(stateDir string, identity codexLifecycleIdentity) (string, error) {
	stateDir = filepath.Clean(stateDir)
	if stateDir == "." || !filepath.IsAbs(stateDir) {
		return "", errors.New("exact Agent control requires an absolute state directory")
	}
	sum := sha256.Sum256([]byte(identity.AgentUID + "\x00" + identity.PaneUID + "\x00" + identity.Generation))
	name := "control-" + hex.EncodeToString(sum[:12]) + ".sock"
	dir := filepath.Join(stateDir, agentControlDirName)
	path := filepath.Join(dir, name)
	if len(path) > agentControlMaxSocketLen {
		return "", errors.New("exact Agent control socket path exceeds the platform-safe bound")
	}
	return path, nil
}

func prepareAgentControlSocket(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create exact Agent control directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ownedByCurrentUser(info) {
		return errors.New("exact Agent control directory is not a private owned directory")
	}
	// #nosec G302 -- 0700 is the intentional private mode for the owner-traversable control socket directory.
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("secure exact Agent control directory: %w", err)
	}
	info, err = os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect exact Agent control socket: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 || !ownedByCurrentUser(info) {
		return errors.New("exact Agent control endpoint collision is not an owned socket")
	}
	conn, dialErr := net.DialTimeout("unix", path, agentControlDialTimeout)
	if dialErr == nil {
		_ = conn.Close()
		return errors.New("exact Agent control endpoint is already active")
	}
	latest, latestErr := os.Lstat(path)
	if latestErr != nil || !os.SameFile(info, latest) || latest.Mode()&os.ModeSocket == 0 || !ownedByCurrentUser(latest) {
		return errors.New("exact Agent control stale endpoint changed during ownership check")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale exact Agent control socket: %w", err)
	}
	return nil
}

func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}
