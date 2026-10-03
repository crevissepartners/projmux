package app

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
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
}

type claudeProcessCheckResult struct {
	Process coremetadata.ProcessIdentity
	Valid   bool
	Binding processhost.Binding
}

// startProcessClaude is dormant until a foreground consumer supplies exact
// ownership transactions. It does not allocate a Registry activation, choose
// policy, or expose a runtime kind. The listener belongs to this host lifetime.
func startProcessClaude(ctx context.Context, host *processhost.Host, launch processhost.Launch, registryPath string) (*processhost.Handle, error) {
	if host == nil || launch.Command.Env == nil || exactActivationRegistryPath(registryPath) != nil {
		return nil, errors.New("invalid process activation registry")
	}
	socket := processClaudeHostSocket(registryPath, launch.Binding.Pane, launch.Binding.Generation)
	listener, err := localipc.Listen(socket)
	if err != nil {
		return nil, err
	}
	var once sync.Once
	closeListener := func() { once.Do(func() { _ = listener.Close() }) }
	initialized := make(chan struct{})
	var handle *processhost.Handle
	var launchErr error
	var ownedProcess coremetadata.ProcessIdentity
	go func() {
		for {
			conn, err := listener.Unix.AcceptUnix()
			if err != nil {
				return
			}
			// One bounded exchange at a time; a hook cannot create unbounded workers.
			func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(localipc.Deadline))
				select {
				case <-initialized:
				case <-ctx.Done():
					return
				case <-time.After(localipc.Deadline):
					return
				}
				var request claudeProcessCheck
				if localipc.ReadJSON(conn, &request) != nil || launchErr != nil || handle == nil {
					return
				}
				peer, parent, err := localipc.PeerProcess(conn)
				if err != nil || peer.OwnerUID != uint32(os.Getuid()) {
					return
				}
				result := claudeProcessCheckResult{}
				binding := request.Binding
				if request.Lookup && binding.Agent == launch.Binding.Agent && binding.Pane == launch.Binding.Pane && binding.Generation == launch.Binding.Generation {
					binding = launch.Binding
				}
				snap, err := handle.Observe(binding)
				if err == nil && binding == launch.Binding {
					process, _, inspectErr := localipc.Process(snap.PID)
					if inspectErr == nil && process == ownedProcess && (!request.Register || parent == process.PID) {
						bounded, cancel := context.WithTimeout(ctx, localipc.Deadline)
						if request.Register {
							err = handle.BindClaudeHook(bounded, binding, process.PID, request.Session)
						} else {
							err = handle.CheckClaudeHook(bounded, binding, process.PID, request.Session)
						}
						cancel()
						if err == nil && request.Register {
							_, _, err = intmetadata.NewStore(registryPath).UpdateConvergent(func(reg *coremetadata.Registry) error {
								pane, ok := reg.Pane(launch.Binding.Pane)
								agent, found := reg.Agent(launch.Binding.Agent)
								window, windowFound := reg.Window(launch.Binding.Window)
								if !ok || !found || !windowFound || pane.Status.Activation.RuntimeID != "" ||
									pane.Status.Activation.Generation != launch.Binding.Generation || pane.Status.Activation.AgentUID != launch.Binding.Agent ||
									pane.Status.Activation.OperationID != launch.Binding.Operation || pane.Metadata.OwnerUID() != launch.Binding.Agent ||
									agent.Status.PaneRef != launch.Binding.Pane || agent.Metadata.OwnerUID() != launch.Binding.Window || window.Metadata.OwnerUID() != launch.Binding.Project {
									return processhost.ErrStale
								}
								if pane.Status.Activation.Claude != nil {
									if pane.Status.Activation.Claude.Process != process {
										return processhost.ErrStale
									}
									return nil
								}
								return intmetadata.DefaultMutator().RecordClaudeProcess(reg, launch.Binding.Pane, launch.Binding.Agent, launch.Binding.Generation, process)
							})
						}
						if err == nil {
							result = claudeProcessCheckResult{Process: process, Valid: true, Binding: launch.Binding}
						}
					}
				}
				_ = localipc.WriteJSON(conn, result)
			}()
		}
	}()
	raw, err := json.Marshal(launch.Binding)
	if err != nil {
		closeListener()
		return nil, err
	}
	// Strip inherited private bindings and tmux anchors before injecting the
	// creator's exact activation. Never encode a process PID as a tmux Pane.
	env := make([]string, 0, len(launch.Command.Env)+5)
	for _, value := range launch.Command.Env {
		key, _, _ := strings.Cut(value, "=")
		if strings.HasPrefix(key, "PMX_INTERNAL_") || key == "TMUX" || key == "TMUX_PANE" || key == "__PROJMUX_RUNTIME_ANCHOR_PANE" {
			continue
		}
		env = append(env, value)
	}
	launch.Command.Env = append(env, internalClaudeProcessBindingEnv+"="+string(raw), internalClaudeProcessHostEnv+"="+socket,
		internalActivationPaneUIDEnv+"="+launch.Binding.Pane, internalActivationGenerationEnv+"="+launch.Binding.Generation, internalClaudeRegistryPathEnv+"="+registryPath)
	handle, launchErr = host.Start(ctx, launch)
	if launchErr == nil {
		snap, observeErr := handle.Observe(launch.Binding)
		if observeErr != nil {
			launchErr = observeErr
		} else {
			ownedProcess, _, launchErr = localipc.Process(snap.PID)
		}
	}
	close(initialized)
	if launchErr != nil {
		closeListener()
		if handle != nil {
			_ = handle.Stop(launch.Binding)
			wait, cancel := context.WithTimeout(context.Background(), 2*localipc.Deadline)
			_, _ = handle.Wait(wait, launch.Binding)
			cancel()
		}
		return handle, launchErr
	}
	go func() { _, _ = handle.Wait(context.Background(), launch.Binding); closeListener() }()
	return handle, nil
}

func checkClaudeProcessHost(proof claudeProcessProof, register bool) bool {
	identity, err := localipc.InspectOwnedSocket(proof.Socket)
	if err != nil || identity != proof.HostSocket || !proof.HostProcess.Valid() || !proof.Process.Valid() {
		return false
	}
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: proof.Socket, Net: "unix"})
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(localipc.Deadline))
	peer, _, err := localipc.PeerProcess(conn)
	if err != nil || peer != proof.HostProcess {
		return false
	}
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
