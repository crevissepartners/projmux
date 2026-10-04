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
	Observe    *processhost.Binding `json:",omitempty"`
	Binding    processhost.Binding
	Session    string
	Register   bool
	Lookup     bool
	Input      *claudeProcessInput
	Helper     *claudeProcessRegistration
	Foreground *processForegroundRequest
}

// Helper registration authority is live host state, never a durable Registry
// member. Its claim is admitted only from the hook's kernel-verified child.
type claudeProcessRegistration struct {
	Phase, PriorGeneration string
	Hook                   coremetadata.ProcessIdentity
	Registration           coremetadata.ClaudeRegistration
}

type claudeProcessInput struct {
	Connection, RegistrationGeneration, Content, Phase string
	Written, Uncertain                                 bool
}

type claudeProcessCheckResult struct {
	Process                coremetadata.ProcessIdentity
	Valid                  bool
	Admitted               bool
	Binding                processhost.Binding
	Registration           *coremetadata.ClaudeRegistration
	RegistrationGeneration string
}

// The bounded exchange service belongs to one exact child lifetime. Published
// fields are immutable after ready closes; each exchange is handled serially.
type claudeProcessService struct {
	registryPath           string
	binding                processhost.Binding
	listener               *localipc.Listener
	ready                  chan struct{}
	handle                 *processhost.Handle
	launchErr              error
	ownedProcess           coremetadata.ProcessIdentity
	ownedHostProcess       coremetadata.ProcessIdentity
	once                   sync.Once
	closeLease             func(context.Context) error
	closeErr               error
	registration           *coremetadata.ClaudeRegistration
	registrationGeneration string
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
		if errors.Is(err, os.ErrExist) {
			return nil, nil, errors.Join(processhost.ErrBusy, err)
		}
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
	if request.Observe != nil {
		if request.Foreground != nil || request.Helper != nil || request.Input != nil || request.Register || request.Lookup || request.Session != "" || request.Binding != (processhost.Binding{}) {
			return
		}
		result := processForegroundResult{Stale: true}
		binding := *request.Observe
		snap, readErr := s.handle.Observe(binding)
		host, _, hostErr := localipc.Process(os.Getpid())
		reg, regErr := intmetadata.NewStore(s.registryPath).LoadDegradedReadOnly()
		view := processHostObservation{Binding: binding, Provider: snap.Provider, State: snap.State, Host: host, Child: s.ownedProcess, Exit: snap.Exit}
		if readErr == nil && hostErr == nil && regErr == nil && binding == s.binding && snap.PID == s.ownedProcess.PID && processObservationMatches(reg, binding, view) {
			result = processForegroundResult{Accepted: true, Observation: &view}
		}
		_ = localipc.WriteJSON(conn, result)
		return
	}
	if request.Foreground != nil {
		if request.Helper != nil || request.Input != nil || request.Register || request.Lookup {
			return
		}
		r := *request.Foreground
		result := controlProcessForeground(bounded, peer, r, s.currentForeground, func() error { return applyClaudeForeground(bounded, s.handle, r) })
		_ = localipc.WriteJSON(conn, result)
		return
	}
	_ = localipc.WriteJSON(conn, s.check(bounded, request, peer, parent))
}

// claudeProcessOperation rejects mixed requests on both sides of the socket.
func claudeProcessOperation(r claudeProcessCheck) (string, bool) {
	if r.Observe != nil || r.Foreground != nil {
		return "", false
	}
	operation := "check"
	for _, candidate := range []struct {
		name    string
		present bool
	}{{"input", r.Input != nil}, {"register", r.Register}, {"lookup", r.Lookup}, {"helper", r.Helper != nil}} {
		if !candidate.present {
			continue
		}
		if operation != "check" {
			return "", false
		}
		operation = candidate.name
	}
	return operation, true
}

func (s *claudeProcessService) check(ctx context.Context, request claudeProcessCheck, peer coremetadata.ProcessIdentity, parent int) claudeProcessCheckResult {
	refused := claudeProcessCheckResult{}
	operation, valid := claudeProcessOperation(request)
	if !valid {
		return refused
	}
	binding := request.Binding
	snap, err := s.handle.Observe(binding)
	if err != nil || binding != s.binding {
		return refused
	}
	process, _, err := localipc.Process(snap.PID)
	if err != nil || process != s.ownedProcess {
		return refused
	}
	if operation == "register" {
		if parent != process.PID || s.handle.BindClaudeHook(ctx, binding, process.PID, request.Session) != nil || s.recordActivation(process, request.Session) != nil {
			return refused
		}
	} else if s.handle.CheckClaudeHook(ctx, binding, process.PID, request.Session) != nil {
		return refused
	}
	switch operation {
	case "input":
		return s.input(ctx, request, peer, snap)
	case "helper":
		proof := claudeProcessProof{Binding: binding, Process: process, HostProcess: s.ownedHostProcess, Session: request.Session}
		next, generation, err := admitClaudeProcessRegistration(s.registryPath, proof, *request.Helper, peer, parent, s.registration, s.registrationGeneration)
		if err != nil {
			return refused
		}
		s.registration, s.registrationGeneration = next, generation
	}
	result := claudeProcessCheckResult{Process: process, Valid: true, Binding: binding, RegistrationGeneration: s.registrationGeneration}
	if s.registration != nil {
		copy := *s.registration
		result.Registration = &copy
	}
	return result
}

// The client preflight and host admission use the same ownership and birth
// checks; the host remains the sole writer of live registration state.
func admitClaudeProcessRegistration(path string, proof claudeProcessProof, claim claudeProcessRegistration, peer coremetadata.ProcessIdentity, parent int, current *coremetadata.ClaudeRegistration, generation string) (*coremetadata.ClaudeRegistration, string, error) {
	if !currentClaudeProcessRegistrationOwner(path, proof) || !validClaudeProcessHelperPeer(proof, claim, peer, parent) {
		return nil, "", processhost.ErrStale
	}
	return nextClaudeProcessRegistration(current, generation, claim)
}

func currentClaudeProcessRegistrationOwner(path string, proof claudeProcessProof) bool {
	reg, err := intmetadata.NewStore(path).LoadDegradedReadOnly()
	if err != nil {
		return false
	}
	b := proof.Binding
	binding := coremetadata.ProcessBinding{HostInstanceID: b.Host, ProjectUID: b.Project, WindowUID: b.Window, AgentUID: b.Agent, PaneUID: b.Pane, Generation: b.Generation, OperationID: b.Operation}
	activation, provider, ok := reg.CurrentProcessActivation(binding)
	agent, found := reg.Agent(b.Agent)
	return ok && provider == "claude" && found && agent.Status.Phase == coremetadata.PhaseRunning && activation.Child == proof.Process && activation.HostProcess == proof.HostProcess
}

func validClaudeProcessHelperPeer(proof claudeProcessProof, claim claudeProcessRegistration, peer coremetadata.ProcessIdentity, parent int) bool {
	a := claim.Registration.Authority
	if !a.Valid() || a.Process != proof.Process || a.SessionID != proof.Session || a.LeaseProcess != peer {
		return false
	}
	if claim.Phase == "clear" {
		return true
	}
	hook, provider, err := localipc.Process(parent)
	return err == nil && hook == claim.Hook && provider == proof.Process.PID
}

func nextClaudeProcessRegistration(current *coremetadata.ClaudeRegistration, generation string, claim claudeProcessRegistration) (*coremetadata.ClaudeRegistration, string, error) {
	authority := claim.Registration.Authority
	switch claim.Phase {
	case "clear":
		if current == nil || current.Authority != authority {
			return nil, "", processhost.ErrStale
		}
		return nil, generation, nil
	case "register":
		switch generation {
		case authority.RegistrationGeneration:
			if current == nil || current.Authority != authority {
				return nil, "", processhost.ErrStale
			}
			return current, generation, nil
		case claim.PriorGeneration:
			registration := claim.Registration
			registration.Ready = true
			return &registration, authority.RegistrationGeneration, nil
		}
	}
	return nil, "", processhost.ErrStale
}

// Foreground control revalidates the same immutable owned child and Registry
// chain as registration, without registering hooks or writing Registry state.
func (s *claudeProcessService) currentForeground(ctx context.Context, a processhost.Authority) error {
	if a.Binding != s.binding || s.handle.ValidateAuthority(ctx, a) != nil {
		return processhost.ErrStale
	}
	snap, err := s.handle.Observe(s.binding)
	if err != nil || (snap.State != "ready" && snap.State != "starting") {
		return processhost.ErrStale
	}
	child, parent, err := localipc.Process(snap.PID)
	if err != nil || child != s.ownedProcess {
		return processhost.ErrStale
	}
	_, hostPID, err := localipc.Process(parent)
	if err != nil || hostPID != os.Getpid() {
		return processhost.ErrStale
	}
	reg, err := intmetadata.NewStore(s.registryPath).LoadDegradedReadOnly()
	if err != nil {
		return err
	}
	if !s.ownershipCurrent(reg, child) {
		return processhost.ErrStale
	}
	return nil
}

// ownershipCurrent is the shared exact Registry fence for registration,
// helper input and foreground control. Child/session proofs remain separate.
func (s *claudeProcessService) ownershipCurrent(reg coremetadata.Registry, child coremetadata.ProcessIdentity) bool {
	pane, ok := reg.Pane(s.binding.Pane)
	agent, found := reg.Agent(s.binding.Agent)
	if !ok || !found || pane.Spec.Runtime.EffectiveKind() != coremetadata.RuntimeProcess || agent.Spec.Provider != "claude" || agent.Status.Phase != coremetadata.PhaseRunning {
		return false
	}
	binding := coremetadata.ProcessBinding{HostInstanceID: s.binding.Host, ProjectUID: s.binding.Project, WindowUID: s.binding.Window, AgentUID: s.binding.Agent, PaneUID: s.binding.Pane, Generation: s.binding.Generation, OperationID: s.binding.Operation}
	activation, provider, current := reg.CurrentProcessActivation(binding)
	if current {
		return provider == "claude" && activation.Child == child && activation.HostProcess == s.ownedHostProcess
	}
	// Before SessionStart, the reserved durable session identifies the generation.
	// A partial activation is never written to schema v5.
	return pane.Status.Activation.IsZero() && pane.Status.ProcessSession != nil && pane.Status.ProcessSession.Provider == "claude" && pane.Status.ProcessSession.Binding == binding && pane.Metadata.OwnerUID() == s.binding.Agent && agent.Status.PaneRef == s.binding.Pane && agent.Metadata.OwnerUID() == s.binding.Window

}

func (s *claudeProcessService) recordActivation(process coremetadata.ProcessIdentity, session string) error {
	host, _, err := localipc.Process(os.Getpid())
	if err != nil {
		return err
	}
	activation := coremetadata.ProcessActivation{Binding: coremetadata.ProcessBinding{HostInstanceID: s.binding.Host, ProjectUID: s.binding.Project, WindowUID: s.binding.Window, AgentUID: s.binding.Agent, PaneUID: s.binding.Pane, Generation: s.binding.Generation, OperationID: s.binding.Operation}, HostProcess: host, Child: process}
	_, _, err = intmetadata.NewStore(s.registryPath).UpdateConvergent(func(reg *coremetadata.Registry) error {
		if !s.ownershipCurrent(*reg, process) {
			return processhost.ErrStale
		}
		return intmetadata.DefaultMutator().RecordProcessActivation(reg, activation, session)
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
	if !s.ownershipCurrent(reg, s.ownedProcess) {
		return false
	}
	registration := s.registration
	return registration != nil && registration.Ready && registration.Authority.Process == s.ownedProcess && registration.Authority.SessionID == request.Session && registration.Authority.LeaseProcess == peer && registration.Authority.RegistrationGeneration == request.Input.RegistrationGeneration

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
	env := processProviderLaunchEnv(launch, internalClaudeProcessBindingEnv, internalClaudeProcessHostEnv, socket)
	return append(env, internalActivationPaneUIDEnv+"="+launch.Binding.Pane, internalActivationGenerationEnv+"="+launch.Binding.Generation, internalClaudeRegistryPathEnv+"="+registryPath)
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
	if s.launchErr == nil {
		s.ownedHostProcess, _, s.launchErr = localipc.Process(os.Getpid())
	}
	if s.launchErr == nil {
		s.launchErr = s.recordChild()
	}
	close(s.ready)
}

func (s *claudeProcessService) recordChild() error {
	child, parent, err := localipc.Process(s.ownedProcess.PID)
	if err != nil || child != s.ownedProcess {
		return processhost.ErrStale
	}
	_, hostPID, err := localipc.Process(parent)
	if err != nil || hostPID != s.ownedHostProcess.PID {
		return processhost.ErrStale
	}
	b := s.binding
	activation := coremetadata.ProcessActivation{Binding: coremetadata.ProcessBinding{HostInstanceID: b.Host, ProjectUID: b.Project, WindowUID: b.Window, AgentUID: b.Agent, PaneUID: b.Pane, Generation: b.Generation, OperationID: b.Operation}, HostProcess: s.ownedHostProcess, Child: child}
	_, _, err = intmetadata.NewStore(s.registryPath).UpdateConvergent(func(reg *coremetadata.Registry) error {
		return intmetadata.DefaultMutator().RecordProcessChild(reg, activation)
	})
	return err
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
	service, err := prepareProcessClaude(ctx, launch, registryPath)
	if err != nil {
		return nil, err
	}
	launch.Command.Env = processClaudeLaunchEnv(launch, registryPath, service.listener.Unix.Addr().String())
	launch.Completion = &processhost.Completion{Cleanup: service.close}
	service.initialize(ctx, host, launch)
	if service.launchErr != nil {
		service.rollback(ctx)
		return service.handle, service.launchErr
	}
	return service.handle, nil
}

// prepareProcessClaude owns only the listener and startup service lifetime.
func prepareProcessClaude(ctx context.Context, launch processhost.Launch, registryPath string) (*claudeProcessService, error) {
	socket := processClaudeHostSocket(registryPath, launch.Binding.Pane, launch.Binding.Generation)
	listener, closeLease, err := listenProcessHost(socket)
	if err != nil {
		return nil, err
	}
	service := &claudeProcessService{registryPath: registryPath, binding: launch.Binding, listener: listener, closeLease: closeLease, ready: make(chan struct{})}
	// Startup cancellation does not shorten the already owned child lifetime.
	lifetime := context.WithoutCancel(ctx)
	go service.serve(lifetime)
	return service, nil
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

func exchangeClaudeProcessHost(proof claudeProcessProof, request claudeProcessCheck) (claudeProcessCheckResult, error) {
	if _, valid := claudeProcessOperation(request); !valid {
		return claudeProcessCheckResult{}, processhost.ErrStale
	}
	conn, err := dialProcessClaudeHost(proof)
	if err != nil {
		return claudeProcessCheckResult{}, err
	}
	defer conn.Close()
	if err = localipc.WriteJSON(conn, request); err != nil {
		return claudeProcessCheckResult{}, err
	}
	if err = conn.CloseWrite(); err != nil {
		return claudeProcessCheckResult{}, err
	}
	var result claudeProcessCheckResult
	if err = localipc.ReadJSON(conn, &result); err != nil {
		return result, err
	}
	if !result.Valid || result.Process != proof.Process || result.Binding != proof.Binding {
		return result, processhost.ErrStale
	}
	return result, nil
}

func checkClaudeProcessHost(proof claudeProcessProof, register bool) bool {
	_, err := exchangeClaudeProcessHost(proof, claudeProcessCheck{Binding: proof.Binding, Session: proof.Session, Register: register})
	return err == nil
}

func registerClaudeProcessHelper(bootstrap claudeEndpointBootstrap, phase string) error {
	if bootstrap.ProcessProof == nil {
		return processhost.ErrStale
	}
	proof := *bootstrap.ProcessProof
	claim := claudeProcessRegistration{Phase: phase, PriorGeneration: bootstrap.PriorRegistrationGeneration, Hook: bootstrap.HookProcess, Registration: bootstrap.Registration}
	current, err := exchangeClaudeProcessHost(proof, claudeProcessCheck{Binding: proof.Binding, Session: proof.Session, Lookup: true})
	if err != nil {
		return err
	}
	peer, parent, err := localipc.Process(os.Getpid())
	if err != nil {
		return err
	}
	if _, _, err = admitClaudeProcessRegistration(bootstrap.RegistryPath, proof, claim, peer, parent, current.Registration, current.RegistrationGeneration); err != nil {
		return err
	}
	_, err = exchangeClaudeProcessHost(proof, claudeProcessCheck{Binding: proof.Binding, Session: proof.Session, Helper: &claim})
	return err
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
	if !ok || pane.Spec.Runtime.EffectiveKind() != coremetadata.RuntimeProcess {
		return claudeProcessProof{}, false
	}
	activation := pane.Status.Activation.Process
	if activation == nil || pane.Status.ProcessSession == nil {
		return claudeProcessProof{}, false
	}
	binding := activation.Binding
	current, provider, valid := reg.CurrentProcessActivation(binding)
	if !valid || provider != "claude" {
		return claudeProcessProof{}, false
	}
	process, supervisor, err := localipc.Process(current.Child.PID)
	if err != nil || process != current.Child {
		return claudeProcessProof{}, false
	}
	_, hostPID, err := localipc.Process(supervisor)
	if err != nil {
		return claudeProcessProof{}, false
	}
	hostProcess, _, err := localipc.Process(hostPID)
	if err != nil || hostProcess != current.HostProcess {
		return claudeProcessProof{}, false
	}
	socket := processClaudeHostSocket(registryPath, pane.Metadata.UID, binding.Generation)
	identity, err := localipc.InspectOwnedSocket(socket)
	if err != nil {
		return claudeProcessProof{}, false
	}
	proof := claudeProcessProof{Binding: processSchemaBinding(binding), Process: process, Session: pane.Status.ProcessSession.SessionID, Socket: socket, HostProcess: hostProcess, HostSocket: identity}
	_, err = lookupClaudeProcessRegistration(proof)
	return proof, err == nil

}

func lookupClaudeProcessRegistration(proof claudeProcessProof) (*coremetadata.ClaudeRegistration, error) {
	result, err := exchangeClaudeProcessHost(proof, claudeProcessCheck{Binding: proof.Binding, Session: proof.Session, Lookup: true})
	if err != nil {
		return nil, err
	}
	if result.Registration == nil || !result.Registration.Ready {
		return nil, processhost.ErrStale
	}
	return result.Registration, nil
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
		registration, err := lookupClaudeProcessRegistration(current)
		if err != nil {
			return coremetadata.AgentRouteRef{}, "process Claude authority is unavailable"
		}
		evidence := coremetadata.ClaudeProcessRouteEvidence{HostInstance: current.Binding.Host, PaneUID: current.Binding.Pane, Generation: current.Binding.Generation, SessionID: current.Session, Process: current.Process, HostProcess: current.HostProcess, Registration: *registration}
		return coremetadata.ResolveProcessClaudeRoute(reg, agentUID, evidence, func(e coremetadata.ClaudeProcessRouteEvidence) bool {
			live, err := lookupClaudeProcessRegistration(current)
			return err == nil && e == evidence && *live == e.Registration
		})

	}
}
