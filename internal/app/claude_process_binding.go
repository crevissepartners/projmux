package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

const (
	internalClaudeProcessBindingEnv = "PMX_INTERNAL_CLAUDE_PROCESS_BINDING"
	internalClaudeProcessHostEnv    = "PMX_INTERNAL_CLAUDE_PROCESS_HOST"
)

// This private bootstrap locator is only a claim. The owned host must prove
// the exact live child and current binding before the hook can register it.
type claudeProcessProof struct {
	Binding     processhost.Binding
	Socket      string
	Process     coremetadata.ProcessIdentity
	HostProcess coremetadata.ProcessIdentity
	HostSocket  localipc.SocketIdentity
	Session     string
}

func (claudeProcessProof) String() string   { return "[private process binding]" }
func (claudeProcessProof) GoString() string { return "[private process binding]" }

type claudeProcessCheck struct {
	Binding  processhost.Binding
	Session  string
	Register bool
	Lookup   bool
	Input    *claudeProcessInput
}

type claudeProcessInput struct {
	Connection, RegistrationGeneration, Content, Phase string
	Written, Uncertain                                 bool
}

type claudeProcessCheckResult struct {
	Process  coremetadata.ProcessIdentity
	Valid    bool
	Admitted bool
	Binding  processhost.Binding
}

// The bounded exchange service belongs to one exact child lifetime. Published
// fields are immutable after ready closes; each exchange is handled serially.
type claudeProcessService struct {
	registryPath string
	binding      processhost.Binding
	listener     *localipc.Listener
	ready        chan struct{}
	handle       *processhost.Handle
	launchErr    error
	ownedProcess coremetadata.ProcessIdentity
	once         sync.Once
	closeLease   func(context.Context) error
	closeErr     error
}

func (s *claudeProcessService) close(ctx context.Context) error {
	s.once.Do(func() { s.closeErr = s.closeLease(ctx) })
	return s.closeErr
}

// A process host creates its activation directory exclusively. Cleanup removes
// only its recorded directory inode, only when empty; it never sweeps siblings
// or deletes entries owned by another endpoint.
func listenProcessHost(socket string) (*localipc.Listener, func(context.Context) error, error) {
	dir := filepath.Dir(socket)
	if err := os.Mkdir(dir, 0700); err != nil {
		return nil, nil, err
	}
	owned, err := os.Lstat(dir)
	if err != nil {
		return nil, nil, err
	}
	removeDir := func() error {
		current, err := os.Lstat(dir)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !os.SameFile(owned, current) {
			return errors.New("process host lease directory replaced")
		}
		return os.Remove(dir)
	}
	listener, err := localipc.Listen(socket)
	if err != nil {
		return nil, nil, errors.Join(err, removeDir())
	}
	closeLease := func(ctx context.Context) error {
		socketErr := listener.Close()
		// Claude's helper owns its additional entries and removes them when host
		// authority disappears. Wait for that bounded shutdown, never remove them.
		for {
			dirErr := removeDir()
			if !errors.Is(dirErr, syscall.ENOTEMPTY) {
				return errors.Join(socketErr, dirErr)
			}
			timer := time.NewTimer(10 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return errors.Join(socketErr, dirErr, ctx.Err())
			case <-timer.C:
			}
		}
	}
	return listener, closeLease, nil
}

func (s *claudeProcessService) serve(ctx context.Context) {
	for {
		conn, err := s.listener.Unix.AcceptUnix()
		if err != nil {
			return
		}
		s.exchange(ctx, conn)
	}
}

func (s *claudeProcessService) exchange(ctx context.Context, conn *net.UnixConn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(localipc.Deadline))
	select {
	case <-s.ready:
	case <-ctx.Done():
		return
	case <-time.After(localipc.Deadline):
		return
	}
	var request claudeProcessCheck
	if localipc.ReadJSON(conn, &request) != nil || s.launchErr != nil || s.handle == nil {
		return
	}
	peer, parent, err := localipc.PeerProcess(conn)
	if err != nil || int64(peer.OwnerUID) != int64(os.Getuid()) {
		return
	}
	bounded, cancel := context.WithTimeout(ctx, localipc.Deadline)
	defer cancel()
	_ = localipc.WriteJSON(conn, s.check(bounded, request, peer, parent))
}

func (s *claudeProcessService) check(ctx context.Context, request claudeProcessCheck, peer coremetadata.ProcessIdentity, parent int) claudeProcessCheckResult {
	refused := claudeProcessCheckResult{}
	binding := request.Binding
	if request.Lookup && request.Input == nil && !request.Register && binding.Agent == s.binding.Agent && binding.Pane == s.binding.Pane && binding.Generation == s.binding.Generation {
		binding = s.binding
	}
	snap, err := s.handle.Observe(binding)
	if err != nil || binding != s.binding {
		return refused
	}
	process, _, err := localipc.Process(snap.PID)
	if err != nil || process != s.ownedProcess {
		return refused
	}
	if request.Input != nil {
		if request.Register || request.Lookup || s.handle.CheckClaudeHook(ctx, binding, process.PID, request.Session) != nil {
			return refused
		}
		return s.input(ctx, request, peer, snap)
	}
	if request.Register {
		if parent != process.PID || s.handle.BindClaudeHook(ctx, binding, process.PID, request.Session) != nil || s.recordActivation(process) != nil {
			return refused
		}
	} else if s.handle.CheckClaudeHook(ctx, binding, process.PID, request.Session) != nil {
		return refused
	}
	return claudeProcessCheckResult{Process: process, Valid: true, Binding: binding}
}

func (s *claudeProcessService) recordActivation(process coremetadata.ProcessIdentity) error {
	_, _, err := intmetadata.NewStore(s.registryPath).UpdateConvergent(func(reg *coremetadata.Registry) error {
		pane, ok := reg.Pane(s.binding.Pane)
		agent, found := reg.Agent(s.binding.Agent)
		window, windowFound := reg.Window(s.binding.Window)
		if !ok || !found || !windowFound || pane.Status.Activation.RuntimeID != "" || pane.Status.Activation.Generation != s.binding.Generation || pane.Status.Activation.AgentUID != s.binding.Agent || pane.Status.Activation.OperationID != s.binding.Operation || pane.Metadata.OwnerUID() != s.binding.Agent || agent.Status.PaneRef != s.binding.Pane || agent.Metadata.OwnerUID() != s.binding.Window || window.Metadata.OwnerUID() != s.binding.Project {
			return processhost.ErrStale
		}
		if pane.Status.Activation.Claude != nil {
			if pane.Status.Activation.Claude.Process != process {
				return processhost.ErrStale
			}
			return nil
		}
		return intmetadata.DefaultMutator().RecordClaudeProcess(reg, s.binding.Pane, s.binding.Agent, s.binding.Generation, process)
	})
	return err
}

// Input is a mutation, unlike a binding check. Only the currently registered
// helper's kernel birth identity may enter it. A payload cannot claim that role.
func (s *claudeProcessService) inputCurrent(request claudeProcessCheck, peer coremetadata.ProcessIdentity) bool {
	reg, err := intmetadata.NewStore(s.registryPath).LoadDegradedReadOnly()
	if err != nil {
		return false
	}
	pane, ok := reg.Pane(s.binding.Pane)
	if !ok || pane.Status.Activation.RuntimeID != "" || pane.Status.Activation.Generation != s.binding.Generation || pane.Status.Activation.Claude == nil {
		return false
	}
	cl := pane.Status.Activation.Claude
	return cl.Registration != nil && cl.Registration.Ready && cl.Process == s.ownedProcess && cl.Registration.Authority.Process == s.ownedProcess && cl.Registration.Authority.SessionID == request.Session && cl.Registration.Authority.LeaseProcess == peer && cl.RegistrationGeneration == request.Input.RegistrationGeneration && cl.Registration.Authority.RegistrationGeneration == request.Input.RegistrationGeneration
}

func (s *claudeProcessService) input(ctx context.Context, request claudeProcessCheck, peer coremetadata.ProcessIdentity, snap processhost.Snapshot) claudeProcessCheckResult {
	result := claudeProcessCheckResult{Process: s.ownedProcess, Binding: s.binding, Valid: true}
	if request.Input.Connection != snap.Connection || !s.inputCurrent(request, peer) {
		return result
	}
	var content claudeProviderCoordinationContent
	if json.Unmarshal([]byte(request.Input.Content), &content) != nil || content.Kind != "projmux-coordination" || content.MessageRef == "" {
		return result
	}
	authority := processhost.Authority{Binding: s.binding, Connection: snap.Connection, Session: snap.Session}
	turn := fmt.Sprintf("endpoint-%x", sha256.Sum256([]byte(content.MessageRef)))
	switch request.Input.Phase {
	case "reserve":
		store := messagestore.NewStore(filepath.Dir(filepath.Dir(s.registryPath)))
		record, found, err := store.Get(content.MessageRef)
		if err != nil || !found || !record.Envelope.Deadline.After(time.Now()) || record.Envelope.Target.AgentUID != s.binding.Agent || record.Envelope.Target.PaneUID != s.binding.Pane || record.Envelope.Target.ActivationGeneration != s.binding.Generation {
			return result
		}
		result.Admitted = s.handle.ReserveClaudeMessage(ctx, authority, turn) == nil
	case "finish":
		result.Admitted = s.handle.FinishClaudeMessage(ctx, authority, turn, request.Input.Written, request.Input.Uncertain) == nil
	}
	return result
}

func processClaudeLaunchEnv(launch processhost.Launch, registryPath, socket string) []string {
	raw, _ := json.Marshal(launch.Binding)
	env := make([]string, 0, len(launch.Command.Env)+5)
	for _, value := range launch.Command.Env {
		key, _, _ := strings.Cut(value, "=")
		if strings.HasPrefix(key, "PMX_INTERNAL_") || key == "TMUX" || key == "TMUX_PANE" || key == "__PROJMUX_RUNTIME_ANCHOR_PANE" {
			continue
		}
		env = append(env, value)
	}
	return append(env, internalClaudeProcessBindingEnv+"="+string(raw), internalClaudeProcessHostEnv+"="+socket, internalActivationPaneUIDEnv+"="+launch.Binding.Pane, internalActivationGenerationEnv+"="+launch.Binding.Generation, internalClaudeRegistryPathEnv+"="+registryPath)
}

func (s *claudeProcessService) initialize(ctx context.Context, host *processhost.Host, launch processhost.Launch) {
	s.handle, s.launchErr = host.Start(ctx, launch)
	if s.launchErr == nil {
		snap, err := s.handle.Observe(s.binding)
		if err != nil {
			s.launchErr = err
		} else {
			s.ownedProcess, _, s.launchErr = localipc.Process(snap.PID)
		}
	}
	close(s.ready)
}

func (s *claudeProcessService) rollback(ctx context.Context) {
	cleanup, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), localipc.Deadline)
	_ = s.close(cleanup)
	cancelCleanup()
	if s.handle == nil {
		return
	}
	_ = s.handle.Stop(s.binding)
	wait, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*localipc.Deadline)
	defer cancel()
	_, _ = s.handle.Wait(wait, s.binding)
}

// startProcessClaude is dormant until a foreground consumer supplies exact
// ownership transactions. It does not allocate an activation or choose policy.
func startProcessClaude(ctx context.Context, host *processhost.Host, launch processhost.Launch, registryPath string) (*processhost.Handle, error) {
	if host == nil || launch.Command.Env == nil || exactActivationRegistryPath(registryPath) != nil {
		return nil, errors.New("invalid process activation registry")
	}
	socket := processClaudeHostSocket(registryPath, launch.Binding.Pane, launch.Binding.Generation)
	listener, closeLease, err := listenProcessHost(socket)
	if err != nil {
		return nil, err
	}
	service := &claudeProcessService{registryPath: registryPath, binding: launch.Binding, listener: listener, closeLease: closeLease, ready: make(chan struct{})}
	// Startup cancellation does not shorten the already owned child lifetime.
	lifetime := context.WithoutCancel(ctx)
	go service.serve(lifetime)
	launch.Command.Env = processClaudeLaunchEnv(launch, registryPath, socket)
	launch.Completion = &processhost.Completion{Cleanup: service.close}
	service.initialize(ctx, host, launch)
	if service.launchErr != nil {
		service.rollback(ctx)
		return service.handle, service.launchErr
	}
	return service.handle, nil
}

// The helper preserves native post/receipt semantics. The host admits its
// MessageRef before any write and records the outcome without inferring cancel.
type processClaudeProviderPoster struct {
	proof                  claudeProcessProof
	registrationGeneration string
	native                 claudeProviderPoster
	current                func() bool
}

func (p *processClaudeProviderPoster) exchange(content, phase string, outcome claudeProviderPostOutcome) (bool, error) {
	conn, err := dialProcessClaudeHost(p.proof)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	request := claudeProcessCheck{Binding: p.proof.Binding, Session: p.proof.Session, Input: &claudeProcessInput{Connection: p.proof.Binding.Operation, RegistrationGeneration: p.registrationGeneration, Content: content, Phase: phase, Written: outcome.FullFrameWritten, Uncertain: outcome.Ambiguous()}}
	if err = localipc.WriteJSON(conn, request); err != nil {
		return false, err
	}
	if err = conn.CloseWrite(); err != nil {
		return false, err
	}
	var result claudeProcessCheckResult
	if err = localipc.ReadJSON(conn, &result); err != nil {
		return false, err
	}
	if !result.Valid || result.Binding != p.proof.Binding || result.Process != p.proof.Process {
		return false, processhost.ErrStale
	}
	return result.Admitted, nil
}

func (p *processClaudeProviderPoster) Post(content string, fence func() bool) (claudeProviderPostOutcome, error) {
	refused := claudeProviderPostOutcome{Reason: "provider-prewrite-refused"}
	if p.current == nil || !p.current() || (fence != nil && !fence()) || p.native == nil {
		return refused, processhost.ErrStale
	}
	admitted, err := p.exchange(content, "reserve", claudeProviderPostOutcome{})
	// A lost reservation reply cannot trigger a native write. The host snapshot
	// retains the pending reservation; there is no automatic replay.
	if err != nil || !admitted {
		return refused, processhost.ErrBusy
	}
	outcome, postErr := p.native.Post(content, fence)
	_, finishErr := p.exchange(content, "finish", outcome)
	if finishErr != nil && postErr == nil && !outcome.FullFrameWritten {
		postErr = finishErr
	}
	return outcome, postErr
}

func dialProcessClaudeHost(proof claudeProcessProof) (*net.UnixConn, error) {
	identity, err := localipc.InspectOwnedSocket(proof.Socket)
	if err != nil || identity != proof.HostSocket || !proof.HostProcess.Valid() || !proof.Process.Valid() {
		return nil, processhost.ErrStale
	}
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: proof.Socket, Net: "unix"})
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(localipc.Deadline))
	peer, _, err := localipc.PeerProcess(conn)
	if err != nil || peer != proof.HostProcess {
		_ = conn.Close()
		return nil, processhost.ErrStale
	}
	return conn, nil
}

func checkClaudeProcessHost(proof claudeProcessProof, register bool) bool {
	conn, err := dialProcessClaudeHost(proof)
	if err != nil {
		return false
	}
	defer conn.Close()
	if err = localipc.WriteJSON(conn, claudeProcessCheck{Binding: proof.Binding, Session: proof.Session, Register: register}); err != nil {
		return false
	}
	if conn.CloseWrite() != nil {
		return false
	}
	var result claudeProcessCheckResult
	if localipc.ReadJSON(conn, &result) != nil || !result.Valid || !result.Process.Valid() {
		return false
	}
	return result.Process == proof.Process && result.Binding == proof.Binding
}

func claudeProcessHookProof(env func(string) string, session string, parentPID int) (claudeProcessProof, bool) {
	var binding processhost.Binding
	if json.Unmarshal([]byte(env(internalClaudeProcessBindingEnv)), &binding) != nil {
		return claudeProcessProof{}, false
	}
	if !coremetadata.ValidCodexIdentityToken(session) {
		return claudeProcessProof{}, false
	}
	process, supervisorPID, err := localipc.Process(parentPID)
	if err != nil {
		return claudeProcessProof{}, false
	}
	_, hostPID, err := localipc.Process(supervisorPID)
	if err != nil {
		return claudeProcessProof{}, false
	}
	hostProcess, _, err := localipc.Process(hostPID)
	if err != nil {
		return claudeProcessProof{}, false
	}
	socket := env(internalClaudeProcessHostEnv)
	socketIdentity, err := localipc.InspectOwnedSocket(socket)
	if err != nil {
		return claudeProcessProof{}, false
	}
	proof := claudeProcessProof{Binding: binding, Socket: socket, Process: process, HostProcess: hostProcess, HostSocket: socketIdentity, Session: session}
	if err != nil || binding.Pane != env(internalActivationPaneUIDEnv) || binding.Generation != env(internalActivationGenerationEnv) || !checkClaudeProcessHost(proof, true) {
		return claudeProcessProof{}, false
	}
	return proof, true
}

// Presence routes even a stale/malformed process hook away from tmux control.
// Losing host authority never falls back to a second control writer.
func claudeProcessHookObservationOnly() bool {
	return os.Getenv(internalClaudeProcessBindingEnv) != "" || os.Getenv(internalClaudeProcessHostEnv) != ""
}

func processClaudeHostSocket(registryPath, pane, generation string) string {
	return claudeActivationLeaseDir(registryPath, pane, generation) + "/host.sock"
}

// discoverProcessClaudeProof only reads a host the Registry's exact provider
// process proves is its grandparent. A payload cannot select another host.
func discoverProcessClaudeProof(registryPath string, reg coremetadata.Registry, agentUID string) (claudeProcessProof, bool) {
	agent, ok := reg.Agent(agentUID)
	if !ok {
		return claudeProcessProof{}, false
	}
	pane, ok := reg.Pane(agent.Status.PaneRef)
	if !ok || pane.Status.Activation.RuntimeID != "" || pane.Status.Activation.Claude == nil || pane.Status.Activation.Claude.Registration == nil {
		return claudeProcessProof{}, false
	}
	activation := pane.Status.Activation
	process, supervisor, err := localipc.Process(activation.Claude.Process.PID)
	if err != nil || process != activation.Claude.Process {
		return claudeProcessProof{}, false
	}
	_, hostPID, err := localipc.Process(supervisor)
	if err != nil {
		return claudeProcessProof{}, false
	}
	hostProcess, _, err := localipc.Process(hostPID)
	if err != nil {
		return claudeProcessProof{}, false
	}
	socket := processClaudeHostSocket(registryPath, pane.Metadata.UID, activation.Generation)
	identity, err := localipc.InspectOwnedSocket(socket)
	if err != nil {
		return claudeProcessProof{}, false
	}
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		return claudeProcessProof{}, false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(localipc.Deadline))
	peer, _, err := localipc.PeerProcess(conn)
	if err != nil || peer != hostProcess {
		return claudeProcessProof{}, false
	}
	session := activation.Claude.RegistrationSessionID
	request := claudeProcessCheck{Binding: processhost.Binding{Agent: agentUID, Pane: pane.Metadata.UID, Generation: activation.Generation}, Session: session, Lookup: true}
	if localipc.WriteJSON(conn, request) != nil || conn.CloseWrite() != nil {
		return claudeProcessProof{}, false
	}
	var result claudeProcessCheckResult
	if localipc.ReadJSON(conn, &result) != nil || !result.Valid || result.Process != process || result.Binding.Agent != agentUID || result.Binding.Pane != pane.Metadata.UID || result.Binding.Generation != activation.Generation || result.Binding.Host == "" {
		return claudeProcessProof{}, false
	}
	return claudeProcessProof{Binding: result.Binding, Process: process, Session: session, Socket: socket, HostProcess: hostProcess, HostSocket: identity}, true
}

func processClaudeRouteResolver(registryPath string, proof claudeProcessProof) func(coremetadata.Registry, string) (coremetadata.AgentRouteRef, string) {
	return func(reg coremetadata.Registry, agentUID string) (coremetadata.AgentRouteRef, string) {
		current := proof
		if agentUID != proof.Binding.Agent {
			if route, reason := coremetadata.ResolveAgentRoute(reg, agentUID); reason == "" {
				return route, reason
			}
			var ok bool
			current, ok = discoverProcessClaudeProof(registryPath, reg, agentUID)
			if !ok {
				return coremetadata.AgentRouteRef{}, "process Claude authority is unavailable"
			}
		}
		evidence := coremetadata.ClaudeProcessRouteEvidence{HostInstance: current.Binding.Host, PaneUID: current.Binding.Pane, Generation: current.Binding.Generation, SessionID: current.Session, Process: current.Process}
		return coremetadata.ResolveProcessClaudeRoute(reg, agentUID, evidence, func(e coremetadata.ClaudeProcessRouteEvidence) bool {
			return e == evidence && checkClaudeProcessHost(current, false)
		})
	}
}
