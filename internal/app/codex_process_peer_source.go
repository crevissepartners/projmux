package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	claudeadapter "github.com/crevissepartners/projmux/internal/integrations/agents/claude"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// This separate, read-only service cannot wait for a peer admission or another
// source socket. The main endpoint retains its existing serial mutation loop.
type codexPeerSource struct {
	mu               sync.Mutex
	listener         *localipc.Listener
	active           *net.UnixConn
	retired, started bool
	done             chan struct{}
	cancel           context.CancelFunc
	closeOnce        sync.Once
	closeErr         error
}

func processCodexPeerSourceSocket(registryPath, pane, generation string) string {
	return filepath.Join(filepath.Dir(processCodexHostSocket(registryPath, pane, generation)), "peer-source.sock")
}

// Ownership is acquired before provider birth, before completion can retire it.
func newCodexPeerSource(socket string) (*codexPeerSource, error) {
	l, err := localipc.Listen(socket)
	if err != nil {
		return nil, err
	}
	return &codexPeerSource{listener: l, done: make(chan struct{})}, nil
}

func (s *codexPeerSource) start(ctx context.Context, e *codexProcessEndpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.retired || s.started || ctx.Err() != nil {
		return processhost.ErrClosed
	}
	serveCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.cancel, s.started = cancel, true
	// Publication and starting the actual loop share the retirement fence.
	go s.serve(serveCtx, e)
	return nil
}

func (s *codexPeerSource) serve(ctx context.Context, e *codexProcessEndpoint) {
	defer close(s.done)
	for {
		conn, err := s.listener.Unix.AcceptUnix()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.retired {
			s.mu.Unlock()
			_ = conn.Close()
			return
		}
		s.active = conn
		s.mu.Unlock()
		s.exchange(ctx, e, conn)
		_ = conn.Close()
		s.mu.Lock()
		s.active = nil
		s.mu.Unlock()
	}
}

func (s *codexPeerSource) exchange(ctx context.Context, e *codexProcessEndpoint, conn *net.UnixConn) {
	_ = conn.SetDeadline(time.Now().Add(localipc.Deadline))
	bounded, cancel := context.WithTimeout(ctx, localipc.Deadline)
	defer cancel()
	var request codexProcessExchange
	result := processForegroundResult{Stale: true}
	var raw json.RawMessage
	if localipc.ReadJSON(conn, &raw) != nil {
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil {
		_ = localipc.WriteJSON(conn, result)
		return
	}
	peer, _, err := localipc.PeerProcess(conn)
	if err == nil && request.Foreground != nil && s.current() {
		want := codexProcessExchange{Foreground: &processForegroundRequest{Action: "validate", Authority: e.authority()}}
		// An exact closed shape excludes every mutation, prompt and mixed field.
		if reflect.DeepEqual(request, want) {
			result = controlProcessForeground(bounded, peer, *request.Foreground, func(ctx context.Context, a processhost.Authority) error {
				if a != e.authority() {
					return processhost.ErrStale
				}
				_, err := e.route(ctx)
				return err
			}, func() error { return nil })
		}
	}
	if !s.current() {
		result = processForegroundResult{Stale: true}
	}
	_ = localipc.WriteJSON(conn, result)
}

func (s *codexPeerSource) current() bool {
	s.mu.Lock()
	retired := s.retired
	s.mu.Unlock()
	identity, err := localipc.InspectOwnedSocket(s.listener.Path)
	return !retired && err == nil && identity == s.listener.Identity()
}

func (s *codexPeerSource) close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() { s.closeErr = s.retire(ctx) })
	return s.closeErr
}

func (s *codexPeerSource) retire(ctx context.Context) error {
	s.mu.Lock()
	s.retired = true
	if s.cancel != nil {
		s.cancel()
	}
	if s.active != nil {
		_ = s.active.Close()
	}
	_ = s.listener.Unix.Close() // SetUnlinkOnClose(false): preserve exact ownership.
	started := s.started
	s.mu.Unlock()
	if started {
		select {
		case <-s.done:
		case <-ctx.Done():
			return ctx.Err() // No lease removal before actual join.
		}
	}
	// A never-started loop has no join receipt. Only its owned socket is retired.
	return localipc.RemoveOwnedSocket(s.listener.Path, s.listener.Identity())
}

func resolveCodexPeerSourceRoute(ctx context.Context, registryPath string, reg coremetadata.Registry, uid string) (coremetadata.AgentRouteRef, error) {
	if err := ctx.Err(); err != nil {
		return coremetadata.AgentRouteRef{}, err
	}
	bounded, cancel := context.WithTimeout(ctx, localipc.Deadline)
	defer cancel()
	a, ok := reg.Agent(uid)
	if !ok {
		return coremetadata.AgentRouteRef{}, processhost.ErrStale
	}
	p, ok := reg.Pane(a.Status.PaneRef)
	if !ok || p.Spec.Runtime.EffectiveKind() != coremetadata.RuntimeProcess {
		route, reason := coremetadata.ResolveAgentRoute(reg, uid)
		if reason != "" {
			return route, errors.New(reason)
		}
		if a.Spec.Provider == aiModeClaude {
			if err := validatePeerClaudeLease(bounded, registryPath, route); err != nil {
				return coremetadata.AgentRouteRef{}, err
			}
		}
		return route, nil
	}
	if a.Spec.Provider == aiModeCodex {
		return resolvePeerProcessCodexRoute(bounded, registryPath, reg, uid)
	}
	if a.Spec.Provider != aiModeClaude || p.Status.ProcessSession == nil {
		return coremetadata.AgentRouteRef{}, processhost.ErrStale
	}
	b := p.Status.ProcessSession.Binding
	activation, provider, valid := reg.CurrentProcessActivation(b)
	if !valid || provider != aiModeClaude {
		return coremetadata.AgentRouteRef{}, processhost.ErrStale
	}
	child, supervisor, err := localipc.Process(activation.Child.PID)
	if err != nil || child != activation.Child {
		return coremetadata.AgentRouteRef{}, processhost.ErrStale
	}
	_, hostPID, err := localipc.Process(supervisor)
	if err != nil {
		return coremetadata.AgentRouteRef{}, processhost.ErrStale
	}
	host, _, err := localipc.Process(hostPID)
	if err != nil || host != activation.HostProcess {
		return coremetadata.AgentRouteRef{}, processhost.ErrStale
	}
	socket := processClaudeHostSocket(registryPath, p.Metadata.UID, b.Generation)
	identity, err := localipc.InspectOwnedSocket(socket)
	if err != nil {
		return coremetadata.AgentRouteRef{}, err
	}
	proof := claudeProcessProof{Binding: processSchemaBinding(b), Process: child, HostProcess: host, HostSocket: identity, Socket: socket, Session: p.Status.ProcessSession.SessionID}
	lookup := func() (*coremetadata.ClaudeRegistration, error) {
		return lookupPeerClaudeRegistration(bounded, proof)
	}
	registration, err := lookup()
	if err != nil {
		return coremetadata.AgentRouteRef{}, err
	}
	evidence := coremetadata.ClaudeProcessRouteEvidence{HostInstance: b.HostInstanceID, PaneUID: b.PaneUID, Generation: b.Generation, SessionID: proof.Session, Process: child, HostProcess: host, Registration: *registration}
	route, reason := coremetadata.ResolveProcessClaudeRoute(reg, uid, evidence, func(e coremetadata.ClaudeProcessRouteEvidence) bool {
		live, err := lookup()
		return err == nil && *live == e.Registration && bounded.Err() == nil
	})
	if reason != "" {
		return route, errors.New(reason)
	}
	if err := validatePeerClaudeLease(bounded, registryPath, route); err != nil {
		return coremetadata.AgentRouteRef{}, err
	}
	return route, nil
}

// Consume the existing lease readiness/probe contract within the same caller
// budget as registration lookup, without changing any Claude product code.
func validatePeerClaudeLease(ctx context.Context, path string, route coremetadata.AgentRouteRef) error {
	authority, ok := route.Authority().(coremetadata.ClaudeAuthorityRef)
	if !ok || !authority.Valid() {
		return processhost.ErrStale
	}
	for _, expected := range []coremetadata.ProcessIdentity{authority.Process, authority.LeaseProcess} {
		actual, _, err := localipc.Process(expected.PID)
		if err != nil || actual != expected {
			return processhost.ErrStale
		}
	}
	socket := claudeadapter.LeaseSocket(path, route.PaneUID, route.Generation, authority.RegistrationGeneration)
	identity, err := claudeadapter.InspectSocket(socket)
	if err != nil {
		return err
	}
	dialCtx, dialCancel := context.WithTimeout(ctx, claudeadapter.LeaseDialTimeout)
	var dialer net.Dialer
	raw, err := dialer.DialContext(dialCtx, "unix", socket)
	dialCancel()
	if err != nil {
		return err
	}
	conn := raw.(*net.UnixConn)
	defer conn.Close()
	deadline, ok := ctx.Deadline()
	if !ok {
		return processhost.ErrStale
	}
	if stage := time.Now().Add(claudeadapter.LeaseReadinessReadTimeout); stage.Before(deadline) {
		deadline = stage
	}
	_ = conn.SetDeadline(deadline)
	peer, _, err := localipc.PeerProcess(conn)
	if err != nil || peer != authority.LeaseProcess || ctx.Err() != nil {
		return processhost.ErrStale
	}
	current, err := claudeadapter.InspectSocket(socket)
	if err != nil || current != identity {
		return processhost.ErrStale
	}
	var ready [1]byte
	if _, err := io.ReadFull(conn, ready[:]); err != nil {
		return err
	}
	if ready[0] != 1 {
		return processhost.ErrStale
	}
	target, ok := claudeadapter.TargetForRoute(route)
	if !ok {
		return processhost.ErrStale
	}
	probeCtx, probeCancel := context.WithTimeout(ctx, claudeadapter.LeaseCoordinationProbeTimeout)
	defer probeCancel()
	response, err := claudeadapter.CallCoordination[json.RawMessage, json.RawMessage, json.RawMessage, json.RawMessage, json.RawMessage, json.RawMessage, json.RawMessage, json.RawMessage, json.RawMessage](probeCtx, path, route, claudeadapter.CoordinationRequest[json.RawMessage, json.RawMessage, json.RawMessage, json.RawMessage, json.RawMessage]{Version: claudeadapter.CoordinationVersion, Operation: "probe", Target: target})
	if err != nil {
		return err
	}
	if response.Kind != "ready" || ctx.Err() != nil {
		return processhost.ErrStale
	}
	current, err = claudeadapter.InspectSocket(socket)
	if err != nil || current != identity {
		return processhost.ErrStale
	}
	return nil
}

// The caller supplies one budget shared by all registration exchanges.
func lookupPeerClaudeRegistration(ctx context.Context, proof claudeProcessProof) (*coremetadata.ClaudeRegistration, error) {
	var result claudeProcessCheckResult
	err := localipc.ExchangeOwnedJSON(ctx, proof.Socket, proof.HostSocket, proof.HostProcess, claudeProcessCheck{Binding: proof.Binding, Session: proof.Session, Lookup: true}, &result)
	if err != nil {
		return nil, err
	}
	if !result.Valid || result.Process != proof.Process || result.Binding != proof.Binding || result.Registration == nil || !result.Registration.Ready {
		return nil, processhost.ErrStale
	}
	return result.Registration, nil
}
