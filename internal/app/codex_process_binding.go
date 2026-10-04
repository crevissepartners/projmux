package app

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func processCodexHostSocket(registryPath, pane, generation string) string {
	return claudeActivationLeaseDir(registryPath, pane, generation) + "/codex-host.sock"
}

const internalCodexProcessBindingEnv = "PMX_INTERNAL_CODEX_PROCESS_BINDING"
const internalCodexProcessHostEnv = "PMX_INTERNAL_CODEX_PROCESS_HOST"

// Only the owned host may issue this non-durable endpoint proof. No broker
// epoch, Registry schema field or public route is allocated here.
type codexProcessEndpoint struct {
	binding        processhost.Binding
	evidence       coremetadata.CodexProcessRouteEvidence
	socket         string
	socketIdentity localipc.SocketIdentity
	handle         *processhost.CodexHandle
	registryPath   string
	listener       *localipc.Listener
	once           sync.Once
	closeLease     func(context.Context) error
	closeErr       error
	messages       atomic.Pointer[codexProcessMessages]
}

func (e *codexProcessEndpoint) close(ctx context.Context) error {
	e.once.Do(func() { e.closeErr = e.closeLease(ctx) })
	return e.closeErr
}
func (e *codexProcessEndpoint) authority() processhost.Authority {
	return processhost.Authority{Binding: e.binding, Session: e.evidence.ThreadID, Connection: e.evidence.Connection}
}
func (e *codexProcessEndpoint) current(ctx context.Context, evidence coremetadata.CodexProcessRouteEvidence) bool {
	if evidence != e.evidence || ctx.Err() != nil || e.handle.ValidateAuthority(ctx, e.authority()) != nil {
		return false
	}
	snap, err := e.handle.Observe(e.binding)
	if err != nil || snap.PID != e.evidence.Process.PID || (processhost.Authority{Binding: snap.Binding, Session: snap.Session, Connection: snap.Connection}) != e.authority() || snap.State != "ready" || snap.Connection != e.binding.Operation {
		return false
	}
	child, parent, err := localipc.Process(snap.PID)
	if err != nil || child != e.evidence.Process || parent != snap.SupervisorPID {
		return false
	}
	_, hostPID, err := localipc.Process(parent)
	if err != nil || hostPID != e.evidence.HostProcess.PID {
		return false
	}
	host, _, err := localipc.Process(hostPID)
	return err == nil && host == e.evidence.HostProcess
}
func (e *codexProcessEndpoint) route(ctx context.Context) (coremetadata.AgentRouteRef, error) {
	reg, err := intmetadata.NewStore(e.registryPath).LoadDegradedReadOnly()
	if err != nil {
		return coremetadata.AgentRouteRef{}, err
	}
	route, reason := coremetadata.ResolveProcessCodexRoute(reg, e.binding.Agent, e.evidence, func(proof coremetadata.CodexProcessRouteEvidence) bool { return e.current(ctx, proof) })
	if reason != "" {
		return coremetadata.AgentRouteRef{}, processhost.ErrStale
	}
	a, _ := reg.Agent(e.binding.Agent)
	w, _ := reg.Window(a.Metadata.OwnerUID())
	p, _ := reg.Pane(e.binding.Pane)
	if a.Metadata.OwnerUID() != e.binding.Window || w.Metadata.OwnerUID() != e.binding.Project || p.Status.Activation.OperationID != e.binding.Operation {
		return coremetadata.AgentRouteRef{}, processhost.ErrStale
	}
	return route, nil
}

func processCodexLaunchEnv(launch processhost.Launch, socket string) []string {
	env := processProviderLaunchEnv(launch, internalCodexProcessBindingEnv, internalCodexProcessHostEnv, socket)
	return env
}

// startProcessCodex commits only the dedicated child's typed thread binding.
// Thread initialization does not acknowledge an absent user turn. Hooks never
// supply process ownership, session identity or readiness.
func startProcessCodex(ctx context.Context, host *processhost.Host, launch processhost.Launch, config processhost.CodexConfig, registryPath string, resume ...processhost.SessionRecord) (*codexProcessEndpoint, error) {
	if host == nil || launch.Command.Env == nil || exactActivationRegistryPath(registryPath) != nil {
		return nil, errors.New("invalid process activation registry")
	}
	socket := processCodexHostSocket(registryPath, launch.Binding.Pane, launch.Binding.Generation)
	listener, closeLease, err := listenProcessHost(socket)
	if err != nil {
		return nil, err
	}
	endpoint := &codexProcessEndpoint{binding: launch.Binding, socket: socket, listener: listener, closeLease: closeLease, registryPath: registryPath}
	launch.Command.Env = processCodexLaunchEnv(launch, socket)
	launch.Completion = &processhost.Completion{Cleanup: endpoint.close}
	if len(resume) > 0 {
		endpoint.handle, err = host.ResumeCodex(ctx, launch, config, resume[0])
	} else {
		endpoint.handle, err = host.StartCodex(ctx, launch, config)
	}
	rollback := func() {
		cleanup, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), localipc.Deadline)
		_ = endpoint.close(cleanup)
		cancelCleanup()
		if endpoint.handle != nil {
			_ = endpoint.handle.Stop(launch.Binding)
			wait, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*localipc.Deadline)
			defer cancel()
			_, _ = endpoint.handle.Wait(wait, launch.Binding)
		}
	}
	if err != nil {
		rollback()
		return endpoint, err
	}
	snap, err := endpoint.handle.Observe(launch.Binding)
	if err != nil {
		rollback()
		return endpoint, err
	}
	child, _, err := localipc.Process(snap.PID)
	if err != nil {
		rollback()
		return endpoint, err
	}
	hostProcess, _, err := localipc.Process(os.Getpid())
	if err != nil {
		rollback()
		return endpoint, err
	}
	endpoint.evidence = coremetadata.CodexProcessRouteEvidence{HostInstance: launch.Binding.Host, PaneUID: launch.Binding.Pane, Generation: launch.Binding.Generation, ThreadID: snap.Session, Connection: snap.Connection, Process: child, HostProcess: hostProcess}
	endpoint.socketIdentity, err = localipc.InspectOwnedSocket(socket)
	if err != nil {
		rollback()
		return endpoint, err
	}
	// Live kernel/provider checks stay outside the Registry transaction.
	if !endpoint.current(ctx, endpoint.evidence) {
		rollback()
		return endpoint, processhost.ErrStale
	}
	b := endpoint.binding
	activation := coremetadata.ProcessActivation{Binding: coremetadata.ProcessBinding{HostInstanceID: b.Host, ProjectUID: b.Project, WindowUID: b.Window, AgentUID: b.Agent, PaneUID: b.Pane, Generation: b.Generation, OperationID: b.Operation}, HostProcess: endpoint.evidence.HostProcess, Child: endpoint.evidence.Process}
	_, _, err = intmetadata.NewStore(registryPath).UpdateConvergent(func(reg *coremetadata.Registry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Public creation reserved a Pending Agent before spawning. Publish the
		// exact witnessed child before recording its ready thread on that same
		// ownership generation; neither step adopts an existing provider.
		if err := intmetadata.DefaultMutator().RecordProcessChild(reg, activation); err != nil {
			return err
		}
		return intmetadata.DefaultMutator().RecordProcessActivation(reg, activation, endpoint.evidence.ThreadID)
	})
	if err != nil {
		rollback()
		return endpoint, err
	}
	// The reservation belongs to the create/resume caller. Route rollback
	// closes only this owned endpoint and child; the caller must persist actual
	// Wait before discarding its Registry reservation, or retain unknown refs.
	if _, err = endpoint.route(ctx); err != nil {
		rollback()
		return endpoint, err
	}
	go endpoint.serve(context.WithoutCancel(ctx))
	return endpoint, nil
}

type codexProcessExchange struct {
	Observe    *processhost.Binding `json:",omitempty"`
	Binding    processhost.Binding
	Evidence   coremetadata.CodexProcessRouteEvidence
	MessageRef string
	Foreground *processForegroundRequest
}
type codexProcessExchangeResult struct {
	Valid   bool
	Receipt *codexProcessReceipt
}

func (e *codexProcessEndpoint) serve(ctx context.Context) {
	for {
		conn, err := e.listener.Unix.AcceptUnix()
		if err != nil {
			return
		}
		e.exchange(ctx, conn)
	}
}
func (e *codexProcessEndpoint) exchange(ctx context.Context, conn *net.UnixConn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(localipc.Deadline))
	bounded, cancel := context.WithTimeout(ctx, localipc.Deadline)
	defer cancel()
	var request codexProcessExchange
	if localipc.ReadJSON(conn, &request) != nil {
		return
	}
	// Message orchestration retains its owning-host peer boundary. Foreground
	// control separately requires per-user credentials and current exact authority.
	peer, _, err := localipc.PeerProcess(conn)
	if err != nil {
		return
	}
	if request.Observe != nil {
		if int64(peer.OwnerUID) != int64(os.Getuid()) || request.Foreground != nil || request.MessageRef != "" || request.Binding != (processhost.Binding{}) || request.Evidence != (coremetadata.CodexProcessRouteEvidence{}) {
			return
		}
		result := processForegroundResult{Stale: true}
		binding := *request.Observe
		snap, readErr := e.handle.Observe(binding)
		reg, regErr := intmetadata.NewStore(e.registryPath).LoadDegradedReadOnly()
		view := processHostObservation{Binding: binding, Provider: snap.Provider, State: snap.State, Host: e.evidence.HostProcess, Child: e.evidence.Process, Exit: snap.Exit}
		if readErr == nil && regErr == nil && binding == e.binding && snap.PID == e.evidence.Process.PID && processObservationMatches(reg, binding, view) {
			result = processForegroundResult{Accepted: true, Observation: &view}
		}
		_ = localipc.WriteJSON(conn, result)
		return
	}
	if request.Foreground != nil {
		if request.MessageRef != "" {
			return
		}
		r := *request.Foreground
		current := func(ctx context.Context, a processhost.Authority) error {
			if a != e.authority() {
				return processhost.ErrStale
			}
			_, err := e.route(ctx)
			return err
		}
		var receipt *codexProcessReceipt
		result := controlProcessForeground(bounded, peer, r, current, func() error {
			if r.Action == "validate" {
				return nil
			}
			if r.Action == "message" {
				messages := e.messages.Load()
				if messages == nil || r.MessageRef == "" {
					return processhost.ErrStale
				}
				value, err := messages.receive(bounded, e, r.MessageRef)
				if err == nil {
					receipt = &value
				}
				return err
			}
			return applyCodexForeground(bounded, e.handle, r)
		})
		result.Receipt = receipt
		_ = localipc.WriteJSON(conn, result)
		return
	}
	if peer != e.evidence.HostProcess {
		return
	}
	result := codexProcessExchangeResult{}
	if request.Binding == e.binding && request.Evidence == e.evidence {
		_, err = e.route(bounded)
		result.Valid = err == nil
		messages := e.messages.Load()
		if request.MessageRef != "" && messages != nil {
			receipt, receiveErr := messages.receive(bounded, e, request.MessageRef)
			if receiveErr == nil {
				// A stale owned endpoint may still settle a receipt; receive
				// revalidates both routes before any provider write.
				result.Valid = true
				result.Receipt = &receipt
			}
		}
	}
	_ = localipc.WriteJSON(conn, result)
}
func (e *codexProcessEndpoint) call(ctx context.Context, messageRef string) (codexProcessExchangeResult, error) {
	if ctx.Err() != nil {
		return codexProcessExchangeResult{}, ctx.Err()
	}
	identity, err := localipc.InspectOwnedSocket(e.socket)
	if err != nil || identity != e.socketIdentity {
		return codexProcessExchangeResult{}, processhost.ErrStale
	}
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: e.socket, Net: "unix"})
	if err != nil {
		return codexProcessExchangeResult{}, err
	}
	defer conn.Close()
	deadline := time.Now().Add(localipc.Deadline)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	peer, _, err := localipc.PeerProcess(conn)
	if err != nil || peer != e.evidence.HostProcess {
		return codexProcessExchangeResult{}, processhost.ErrStale
	}
	if err = localipc.WriteJSON(conn, codexProcessExchange{Binding: e.binding, Evidence: e.evidence, MessageRef: messageRef}); err != nil {
		return codexProcessExchangeResult{}, err
	}
	if err = conn.CloseWrite(); err != nil {
		return codexProcessExchangeResult{}, err
	}
	var result codexProcessExchangeResult
	if err = localipc.ReadJSON(conn, &result); err != nil {
		return result, err
	}
	if !result.Valid {
		return result, processhost.ErrStale
	}
	return result, nil
}

func codexProcessHookObservationOnly(env func(string) string) bool {
	return env != nil && (env(internalCodexProcessBindingEnv) != "" || env(internalCodexProcessHostEnv) != "")
}
