// Package codexgenerationhost owns private, versioned Codex app-server
// processes. It is deliberately separate from codexbroker: broker routing
// stays transport-only and imports no bundle, process, Registry, tmux, or CLI
// policy.
package codexgenerationhost

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbundle"
	"github.com/fsnotify/fsnotify"
)

const privateGenerationReadyTimeout = 15 * time.Second

const privateRuntimeIDBytes = 16

type EndpointIdentity = codexbroker.EndpointIdentity

type bundleArtifactRequirement struct {
	path  string
	roles []codexbundle.Role
}

// qualifiedBundleRequirements returns a value-owned copy of the closed Phase
// 0 app-server/TUI/sibling-helper release set. There is no mutable package
// slice or caller configuration that can weaken paths or roles.
func qualifiedBundleRequirements() [4]bundleArtifactRequirement {
	return [4]bundleArtifactRequirement{
		{path: "bin/codex", roles: []codexbundle.Role{codexbundle.RoleServer, codexbundle.RoleTUI}},
		{path: "bin/codex-code-mode-host", roles: []codexbundle.Role{codexbundle.RoleHelper}},
		{path: "codex-path/rg", roles: []codexbundle.Role{codexbundle.RoleHelper}},
		{path: "codex-resources/bwrap", roles: []codexbundle.Role{codexbundle.RoleHelper}},
	}
}

// CompleteBundleArtifactPaths returns a copy for packagers and tests that need
// to construct the closed qualified layout.
func CompleteBundleArtifactPaths() []string {
	requirements := qualifiedBundleRequirements()
	paths := make([]string, 0, len(requirements))
	for _, requirement := range requirements {
		paths = append(paths, requirement.path)
	}
	return paths
}

type HostRefusal string

const (
	HostRefusalNone                HostRefusal = "none"
	HostRefusalConfigInvalid       HostRefusal = "config-invalid"
	HostRefusalPrivateRootInvalid  HostRefusal = "private-root-invalid"
	HostRefusalBundleIncomplete    HostRefusal = "bundle-incomplete"
	HostRefusalBundleDrift         HostRefusal = "bundle-drift"
	HostRefusalSocketOccupied      HostRefusal = "socket-occupied"
	HostRefusalLaunchFailed        HostRefusal = "launch-failed"
	HostRefusalReadinessFailed     HostRefusal = "readiness-failed"
	HostRefusalLaunchProofMismatch HostRefusal = "launch-proof-mismatch"
	HostRefusalProcessExited       HostRefusal = "process-exited"
	HostRefusalLeaseHeld           HostRefusal = "lease-held"
)

type HostError struct {
	Refusal HostRefusal
	err     error
}

func (err *HostError) Error() string {
	return "Codex private generation host refused: " + string(err.Refusal)
}
func (err *HostError) Unwrap() error { return err.err }

func HostRefusalOf(err error) HostRefusal {
	var hostErr *HostError
	if errors.As(err, &hostErr) {
		return hostErr.Refusal
	}
	return HostRefusalNone
}

func hostRefuse(reason HostRefusal, err error) error { return &HostError{Refusal: reason, err: err} }

// FileIdentity is the local immutable object identity captured in a launch
// proof. Device/inode plus change time prove replacement at the same path even
// when a filesystem immediately reuses an inode; mode, size and hash are
// independently revalidated through the content-addressed lease.
type FileIdentity struct {
	Device                uint64 `json:"device"`
	Inode                 uint64 `json:"inode"`
	Mode                  uint32 `json:"mode"`
	Size                  int64  `json:"size"`
	ChangeTimeSeconds     int64  `json:"changeTimeSeconds"`
	ChangeTimeNanoseconds int64  `json:"changeTimeNanoseconds"`
}

// LaunchProof is the complete lifecycle authority for one private endpoint.
// Every axis must match both the stored proof and the current process,
// executable, and socket observations before any lifecycle signal is sent.
type LaunchProof struct {
	Endpoint          EndpointIdentity `json:"endpoint"`
	EndpointRuntimeID string           `json:"endpointRuntimeID"`
	PID               int              `json:"pid"`
	ProcessGroupID    int              `json:"processGroupID"`
	SocketPath        string           `json:"socketPath"`
	SocketIdentity    FileIdentity     `json:"socketIdentity"`
	ExecutablePath    string           `json:"executablePath"`
	Executable        FileIdentity     `json:"executableIdentity"`
	ExecutableSHA256  string           `json:"executableSHA256"`
	BundleID          string           `json:"bundleID"`
}

type LifecycleAction string

const (
	LifecycleStop    LifecycleAction = "stop"
	LifecycleRestart LifecycleAction = "restart"
	LifecycleKill    LifecycleAction = "kill"
)

// LifecycleMutation is one exact owned signal. Refused decisions never enter
// this ledger, so its length is the lifecycle argv/effect count.
type LifecycleMutation struct {
	Action LifecycleAction `json:"action"`
	PID    int             `json:"pid"`
}

// PrivateGenerationConfig is the closed launch input for one Preparing host.
type PrivateGenerationConfig struct {
	Endpoint         EndpointIdentity
	StateDomainPath  string
	PrivateRoot      string
	SocketPath       string
	LeaseRoot        string
	RequiredProtocol codexbundle.ProtocolRange
	ReadyTimeout     time.Duration
	Environment      []string

	// ready is a deterministic test seam. Production uses an initialized
	// private app-server handshake, not socket existence alone.
	ready func(context.Context, string) error
	// launch is a deterministic process-recorder seam. Production launches the
	// exact verified leased executable with a fixed app-server argv.
	launch func(string, []string, []string) (ownedGenerationProcess, error)
}

type ownedGenerationProcess interface {
	PID() int
	ProcessGroupID() int
	ValidateProcessGroup() error
	Signal(os.Signal) error
	SignalProcessGroup(os.Signal) error
	Done() <-chan struct{}
	SessionDone() <-chan struct{}
	ExitError() error
}

type execGenerationProcess struct {
	cmd            *exec.Cmd
	processGroupID int
	done           chan struct{}
	sessionDone    chan struct{}
	mu             sync.Mutex
	exitErr        error
}

func (process *execGenerationProcess) PID() int            { return process.cmd.Process.Pid }
func (process *execGenerationProcess) ProcessGroupID() int { return process.processGroupID }
func (process *execGenerationProcess) ValidateProcessGroup() error {
	actual, err := syscall.Getpgid(process.PID())
	if err != nil {
		return err
	}
	if actual != process.processGroupID || actual != process.PID() {
		return fmt.Errorf("private process group changed")
	}
	return nil
}
func (process *execGenerationProcess) Signal(signal os.Signal) error {
	if err := process.ValidateProcessGroup(); err != nil {
		return err
	}
	return process.SignalProcessGroup(signal)
}
func (process *execGenerationProcess) SignalProcessGroup(signal os.Signal) error {
	unixSignal, ok := signal.(syscall.Signal)
	if !ok {
		return fmt.Errorf("unsupported private process signal")
	}
	return syscall.Kill(-process.processGroupID, unixSignal)
}
func (process *execGenerationProcess) Done() <-chan struct{}        { return process.done }
func (process *execGenerationProcess) SessionDone() <-chan struct{} { return process.sessionDone }
func (process *execGenerationProcess) ExitError() error {
	process.mu.Lock()
	defer process.mu.Unlock()
	return process.exitErr
}

// PrivateGenerationHost owns one unique private app-server process, versioned
// socket, immutable bundle lease, and exact lifecycle proof. It never discovers
// or mutates the official default endpoint.
type PrivateGenerationHost struct {
	mu sync.Mutex

	config     PrivateGenerationConfig
	lease      codexbundle.Lease
	proof      LaunchProof
	argv       []string
	process    ownedGenerationProcess
	socketInfo fs.FileInfo
	execInfo   fs.FileInfo
	mutations  []LifecycleMutation
	leaseHeld  bool
	closed     bool
}

// StartPrivateGeneration verifies the complete immutable bundle before the
// first process launch and waits for an initialized private handshake before
// publishing a ready Preparing host.
func StartPrivateGeneration(ctx context.Context, cfg PrivateGenerationConfig) (*PrivateGenerationHost, error) {
	if !cfg.Endpoint.Valid() || !absoluteNonRoot(cfg.StateDomainPath) || !absoluteNonRoot(cfg.PrivateRoot) || !absoluteNonRoot(cfg.SocketPath) ||
		!absoluteNonRoot(cfg.LeaseRoot) || !cfg.RequiredProtocol.Valid() {
		return nil, hostRefuse(HostRefusalConfigInvalid, nil)
	}
	if filepath.Dir(filepath.Clean(cfg.SocketPath)) != filepath.Clean(cfg.PrivateRoot) ||
		filepath.Base(cfg.SocketPath) != "codex-"+cfg.Endpoint.EndpointGenerationID+".sock" {
		return nil, hostRefuse(HostRefusalPrivateRootInvalid, nil)
	}
	privateRootInfo, err := ownerPrivateDirectory(cfg.PrivateRoot)
	if err != nil {
		return nil, err
	}
	stateDomainInfo, err := ownerPrivateDirectory(cfg.StateDomainPath)
	if err != nil {
		return nil, err
	}
	lease, err := verifyCompleteLease(cfg)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(cfg.SocketPath); err == nil {
		return nil, hostRefuse(HostRefusalSocketOccupied, nil)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, hostRefuse(HostRefusalSocketOccupied, err)
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, hostRefuse(HostRefusalReadinessFailed, err)
	}
	defer watcher.Close()
	if err := watcher.Add(filepath.Dir(cfg.SocketPath)); err != nil {
		return nil, hostRefuse(HostRefusalReadinessFailed, err)
	}
	runtimeID, err := randomRuntimeID(privateRuntimeIDBytes)
	if err != nil {
		return nil, hostRefuse(HostRefusalLaunchFailed, err)
	}
	servers := lease.Paths(codexbundle.RoleServer)
	if len(servers) != 1 {
		return nil, hostRefuse(HostRefusalBundleIncomplete, nil)
	}
	executable := servers[0]
	execInfo, err := os.Lstat(executable)
	if err != nil || !execInfo.Mode().IsRegular() || execInfo.Mode()&os.ModeSymlink != 0 {
		return nil, hostRefuse(HostRefusalBundleDrift, err)
	}
	artifact, ok := leaseArtifact(lease, executable)
	if !ok {
		return nil, hostRefuse(HostRefusalBundleIncomplete, nil)
	}
	argv := []string{executable, "app-server", "--listen", "unix://" + cfg.SocketPath}
	environment := privateGenerationEnvironment(cfg)
	launcher := cfg.launch
	if launcher == nil {
		launcher = launchOwnedGenerationProcess
	}
	process, err := launcher(executable, argv[1:], environment)
	if err != nil {
		return nil, hostRefuse(HostRefusalLaunchFailed, err)
	}
	host := &PrivateGenerationHost{
		config: cfg, lease: lease, argv: append([]string(nil), argv...), process: process,
		execInfo: execInfo, leaseHeld: true,
	}
	timeout := cfg.ReadyTimeout
	if timeout <= 0 {
		timeout = privateGenerationReadyTimeout
	}
	readyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ready := cfg.ready
	if ready == nil {
		ready = readyPrivateGeneration
	}
	socketInfo, readyErr := awaitPrivateGenerationReady(readyCtx, cfg.SocketPath, process, ready, watcher)
	if readyErr != nil {
		if cleanupErr := cleanupFailedLaunch(process, cfg.SocketPath, socketInfo); cleanupErr != nil {
			return nil, cleanupErr
		}
		return nil, readyErr
	}
	// Publication is a second complete lease boundary. A same-user updater (or
	// launch seam) cannot swap the executable or a sibling helper after the
	// pre-launch check and leave a ready-but-unprovable host published.
	reopened, reopenErr := verifyCompleteLease(cfg)
	currentExecutable, executableErr := os.Lstat(executable)
	if reopenErr != nil || reopened.ID != lease.ID || executableErr != nil ||
		!os.SameFile(execInfo, currentExecutable) || fileIdentity(execInfo) != fileIdentity(currentExecutable) {
		if cleanupErr := cleanupFailedLaunch(process, cfg.SocketPath, socketInfo); cleanupErr != nil {
			return nil, cleanupErr
		}
		if reopenErr != nil {
			return nil, hostRefuse(HostRefusalBundleDrift, reopenErr)
		}
		return nil, hostRefuse(HostRefusalLaunchProofMismatch, executableErr)
	}
	if err := revalidateOwnerPrivateDirectory(cfg.PrivateRoot, privateRootInfo); err != nil {
		if cleanupErr := cleanupFailedLaunch(process, cfg.SocketPath, socketInfo); cleanupErr != nil {
			return nil, cleanupErr
		}
		return nil, err
	}
	if err := revalidateOwnerPrivateDirectory(cfg.StateDomainPath, stateDomainInfo); err != nil {
		if cleanupErr := cleanupFailedLaunch(process, cfg.SocketPath, socketInfo); cleanupErr != nil {
			return nil, cleanupErr
		}
		return nil, err
	}
	select {
	case <-process.Done():
		if cleanupErr := cleanupFailedLaunch(process, cfg.SocketPath, socketInfo); cleanupErr != nil {
			return nil, cleanupErr
		}
		return nil, hostRefuse(HostRefusalProcessExited, process.ExitError())
	default:
	}
	if process.ProcessGroupID() != process.PID() {
		if cleanupErr := cleanupFailedLaunch(process, cfg.SocketPath, socketInfo); cleanupErr != nil {
			return nil, cleanupErr
		}
		return nil, hostRefuse(HostRefusalLaunchProofMismatch, nil)
	}
	if err := process.ValidateProcessGroup(); err != nil {
		if cleanupErr := cleanupFailedLaunch(process, cfg.SocketPath, socketInfo); cleanupErr != nil {
			return nil, cleanupErr
		}
		return nil, hostRefuse(HostRefusalLaunchProofMismatch, err)
	}
	host.socketInfo = socketInfo
	host.proof = LaunchProof{
		Endpoint: cfg.Endpoint, EndpointRuntimeID: runtimeID, PID: process.PID(), ProcessGroupID: process.ProcessGroupID(),
		SocketPath:     cfg.SocketPath,
		SocketIdentity: fileIdentity(socketInfo), ExecutablePath: executable, Executable: fileIdentity(execInfo),
		ExecutableSHA256: artifact.SHA256, BundleID: lease.ID,
	}
	return host, nil
}

// ObservePrivateGeneration revalidates an already published exact private
// launch proof without acquiring a process handle or lifecycle authority. It
// is a read-only readiness barrier used after coordinator restart; it cannot
// stop, restart, kill, adopt, or release the generation.
func ObservePrivateGeneration(ctx context.Context, cfg PrivateGenerationConfig, proof LaunchProof) error {
	if !cfg.Endpoint.Valid() || proof.Endpoint != cfg.Endpoint || proof.EndpointRuntimeID == "" ||
		proof.PID <= 0 || proof.ProcessGroupID != proof.PID || proof.SocketPath != cfg.SocketPath ||
		!absoluteNonRoot(cfg.StateDomainPath) || !absoluteNonRoot(cfg.PrivateRoot) ||
		!absoluteNonRoot(cfg.SocketPath) || !absoluteNonRoot(cfg.LeaseRoot) || !cfg.RequiredProtocol.Valid() {
		return hostRefuse(HostRefusalLaunchProofMismatch, nil)
	}
	if _, err := ownerPrivateDirectory(cfg.PrivateRoot); err != nil {
		return err
	}
	if _, err := ownerPrivateDirectory(cfg.StateDomainPath); err != nil {
		return err
	}
	lease, err := verifyCompleteLease(cfg)
	if err != nil || lease.ID != proof.BundleID {
		return hostRefuse(HostRefusalBundleDrift, err)
	}
	servers := lease.Paths(codexbundle.RoleServer)
	if len(servers) != 1 || servers[0] != proof.ExecutablePath {
		return hostRefuse(HostRefusalLaunchProofMismatch, nil)
	}
	artifact, ok := leaseArtifact(lease, proof.ExecutablePath)
	if !ok || artifact.SHA256 != proof.ExecutableSHA256 {
		return hostRefuse(HostRefusalLaunchProofMismatch, nil)
	}
	executable, err := os.Lstat(proof.ExecutablePath)
	if err != nil || executable.Mode()&os.ModeSymlink != 0 || !executable.Mode().IsRegular() || fileIdentity(executable) != proof.Executable {
		return hostRefuse(HostRefusalLaunchProofMismatch, err)
	}
	socket, err := os.Lstat(proof.SocketPath)
	if err != nil || socket.Mode()&os.ModeSocket == 0 || fileIdentity(socket) != proof.SocketIdentity {
		return hostRefuse(HostRefusalLaunchProofMismatch, err)
	}
	processGroupID, err := syscall.Getpgid(proof.PID)
	if err != nil || processGroupID != proof.ProcessGroupID {
		return hostRefuse(HostRefusalLaunchProofMismatch, err)
	}
	if err := syscall.Kill(proof.PID, 0); err != nil {
		return hostRefuse(HostRefusalProcessExited, err)
	}
	ready := cfg.ready
	if ready == nil {
		ready = readyPrivateGeneration
	}
	if err := ready(ctx, proof.SocketPath); err != nil {
		return hostRefuse(HostRefusalReadinessFailed, err)
	}
	latest, err := os.Lstat(proof.SocketPath)
	if err != nil || fileIdentity(latest) != proof.SocketIdentity {
		return hostRefuse(HostRefusalLaunchProofMismatch, err)
	}
	return nil
}

// ObservePrivateGenerationRoute revalidates the complete leased release set,
// the exact private app-server authority, and the single RoleTUI artifact used
// to materialize a Pane. Calling this immediately inside the admission barrier
// prevents an arbitrary absolute executable or a drifted lease member from
// becoming the Current generation's TUI route.
func ObservePrivateGenerationRoute(ctx context.Context, cfg PrivateGenerationConfig, proof LaunchProof, tuiPath string) error {
	if err := ObservePrivateGeneration(ctx, cfg, proof); err != nil {
		return err
	}
	return observeExactTUIArtifact(cfg, proof.BundleID, tuiPath)
}

// VerifiedBundleIdentity is the content-free result of reopening one complete
// immutable generation lease. It lets the rolling coordinator bind a Phase 0
// qualification version pair and exact request paths to real leased artifacts
// before any candidate process can be prepared.
type VerifiedBundleIdentity struct {
	ID         string
	Version    string
	ServerPath string
	TUIPath    string
}

// VerifyPrivateGenerationBundle is a read-only preflight. It validates the
// exact owner-private state/runtime roots, requires the socket to be directly
// owned by that runtime root, and reopens the complete server/TUI/helper lease.
func VerifyPrivateGenerationBundle(cfg PrivateGenerationConfig) (VerifiedBundleIdentity, error) {
	if !cfg.Endpoint.Valid() || !absoluteNonRoot(cfg.StateDomainPath) || !absoluteNonRoot(cfg.PrivateRoot) ||
		!absoluteNonRoot(cfg.SocketPath) || !absoluteNonRoot(cfg.LeaseRoot) || !cfg.RequiredProtocol.Valid() ||
		filepath.Dir(cfg.SocketPath) != cfg.PrivateRoot {
		return VerifiedBundleIdentity{}, hostRefuse(HostRefusalConfigInvalid, nil)
	}
	if _, err := ownerPrivateDirectory(cfg.PrivateRoot); err != nil {
		return VerifiedBundleIdentity{}, err
	}
	if _, err := ownerPrivateDirectory(cfg.StateDomainPath); err != nil {
		return VerifiedBundleIdentity{}, err
	}
	lease, err := verifyCompleteLease(cfg)
	if err != nil {
		return VerifiedBundleIdentity{}, err
	}
	servers, tuis := lease.Paths(codexbundle.RoleServer), lease.Paths(codexbundle.RoleTUI)
	if len(servers) != 1 || len(tuis) != 1 {
		return VerifiedBundleIdentity{}, hostRefuse(HostRefusalBundleIncomplete, nil)
	}
	return VerifiedBundleIdentity{ID: lease.ID, Version: lease.Manifest.Version, ServerPath: servers[0], TUIPath: tuis[0]}, nil
}

func observeExactTUIArtifact(cfg PrivateGenerationConfig, bundleID, tuiPath string) error {
	lease, err := verifyCompleteLease(cfg)
	if err != nil || lease.ID != bundleID {
		return hostRefuse(HostRefusalBundleDrift, err)
	}
	tuis := lease.Paths(codexbundle.RoleTUI)
	if len(tuis) != 1 || !absoluteNonRoot(tuiPath) || tuiPath != tuis[0] {
		return hostRefuse(HostRefusalLaunchProofMismatch, nil)
	}
	info, err := os.Lstat(tuiPath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return hostRefuse(HostRefusalBundleDrift, err)
	}
	return nil
}

func cleanupFailedLaunch(process ownedGenerationProcess, socket string, socketInfo fs.FileInfo) error {
	if err := terminateOwnedSession(process); err != nil {
		return err
	}
	removeExactSocket(socket, socketInfo)
	return nil
}

func launchOwnedGenerationProcess(executable string, args, environment []string) (ownedGenerationProcess, error) {
	// #nosec G204 -- executable is an exact verified content-addressed lease path and args are the fixed private app-server route.
	command := exec.Command(executable, args...)
	command.Env = environment
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	defer devNull.Close()
	sessionRead, sessionWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	command.Stdout, command.Stderr = devNull, devNull
	// fd 3 is a process-session lifetime token. It is intentionally inherited
	// across exec by the private app-server and its helpers. The parent holds
	// only the read end, so EOF is semantic evidence that every token-bearing
	// process in the owned session has exited or closed its token.
	command.ExtraFiles = []*os.File{sessionWrite}
	if err := command.Start(); err != nil {
		_ = sessionRead.Close()
		_ = sessionWrite.Close()
		return nil, err
	}
	process := &execGenerationProcess{
		cmd: command, processGroupID: command.Process.Pid,
		done: make(chan struct{}), sessionDone: make(chan struct{}),
	}
	go func() {
		defer sessionRead.Close()
		buffer := make([]byte, 1)
		for {
			if _, readErr := sessionRead.Read(buffer); readErr != nil {
				break
			}
		}
		close(process.sessionDone)
	}()
	go func() {
		err := command.Wait()
		process.mu.Lock()
		process.exitErr = err
		process.mu.Unlock()
		close(process.done)
	}()
	if err := sessionWrite.Close(); err != nil {
		return nil, errors.Join(err, terminateOwnedSession(process))
	}
	processGroupID, err := syscall.Getpgid(command.Process.Pid)
	if err != nil || processGroupID != command.Process.Pid {
		cleanupErr := terminateOwnedSession(process)
		if err != nil {
			return nil, errors.Join(err, cleanupErr)
		}
		return nil, errors.Join(fmt.Errorf("private process did not become its session group leader"), cleanupErr)
	}
	return process, nil
}

func terminateOwnedSession(process ownedGenerationProcess) error {
	sessionDone := process.SessionDone()
	leaderExited, sessionExited := false, false
	select {
	case <-process.Done():
		leaderExited = true
	default:
	}
	select {
	case <-sessionDone:
		sessionExited = true
	default:
	}
	if leaderExited && sessionExited {
		return nil
	}
	if !leaderExited {
		if err := process.Signal(os.Kill); err != nil {
			select {
			case <-process.Done():
				leaderExited = true
			default:
				return hostRefuse(HostRefusalLaunchProofMismatch, err)
			}
		}
	}
	if leaderExited && !sessionExited {
		// A live inherited token is exact evidence that this owned private
		// session still has a descendant. The leader PID can no longer be
		// queried, so signal only the originally captured Setsid PGID.
		if err := process.SignalProcessGroup(os.Kill); err != nil {
			select {
			case <-sessionDone:
				sessionExited = true
			default:
				return hostRefuse(HostRefusalLaunchProofMismatch, err)
			}
		}
	}
	<-process.Done()
	if !sessionExited {
		<-sessionDone
	}
	return nil
}

func readyPrivateGeneration(ctx context.Context, socket string) error {
	client, err := codexappserver.OpenPrivateUnix(ctx, socket, time.Second, "projmux-private-generation", true)
	if err != nil {
		return err
	}
	return client.Close()
}

func awaitPrivateGenerationReady(
	ctx context.Context,
	socket string,
	process ownedGenerationProcess,
	ready func(context.Context, string) error,
	watcher *fsnotify.Watcher,
) (fs.FileInfo, error) {
	observe := func() (fs.FileInfo, bool) {
		info, statErr := os.Lstat(socket)
		if statErr != nil || info.Mode()&os.ModeSocket == 0 {
			return info, false
		}
		if err := ready(ctx, socket); err != nil {
			return info, false
		}
		latest, latestErr := os.Lstat(socket)
		return latest, latestErr == nil && os.SameFile(info, latest)
	}
	if info, ok := observe(); ok {
		return info, nil
	}
	for {
		select {
		case <-process.Done():
			return nil, hostRefuse(HostRefusalProcessExited, process.ExitError())
		case <-ctx.Done():
			return nil, hostRefuse(HostRefusalReadinessFailed, ctx.Err())
		case event, open := <-watcher.Events:
			if !open {
				return nil, hostRefuse(HostRefusalReadinessFailed, nil)
			}
			if filepath.Clean(event.Name) == filepath.Clean(socket) &&
				event.Op&(fsnotify.Create|fsnotify.Rename|fsnotify.Write) != 0 {
				if info, ok := observe(); ok {
					return info, nil
				}
			}
		case err, open := <-watcher.Errors:
			if open && err != nil {
				return nil, hostRefuse(HostRefusalReadinessFailed, err)
			}
		}
	}
}

// Proof returns a value copy of the exact lifecycle authority.
func (host *PrivateGenerationHost) Proof() LaunchProof {
	host.mu.Lock()
	defer host.mu.Unlock()
	return host.proof
}

// LaunchArgv returns the immutable leased launch argv. Mutable current links or
// source release directories never participate in it.
func (host *PrivateGenerationHost) LaunchArgv() []string {
	host.mu.Lock()
	defer host.mu.Unlock()
	return append([]string(nil), host.argv...)
}

func (host *PrivateGenerationHost) Mutations() []LifecycleMutation {
	host.mu.Lock()
	defer host.mu.Unlock()
	return append([]LifecycleMutation(nil), host.mutations...)
}

func (host *PrivateGenerationHost) LeaseHeld() bool {
	host.mu.Lock()
	defer host.mu.Unlock()
	return host.leaseHeld
}

// Stop sends SIGTERM only after all proof axes and the complete leased bundle
// revalidate. Any drift returns with mutation count zero.
func (host *PrivateGenerationHost) Stop(presented LaunchProof) error {
	return host.signal(LifecycleStop, presented, syscall.SIGTERM)
}

// Kill sends SIGKILL only under the same exact proof.
func (host *PrivateGenerationHost) Kill(presented LaunchProof) error {
	return host.signal(LifecycleKill, presented, os.Kill)
}

// Restart validates before recording or signalling. The actual successor
// launch belongs to a new StartPrivateGeneration call after the process-exit
// barrier; this method therefore cannot partially launch under a drifted proof.
func (host *PrivateGenerationHost) Restart(presented LaunchProof) error {
	return host.signal(LifecycleRestart, presented, syscall.SIGTERM)
}

func (host *PrivateGenerationHost) signal(action LifecycleAction, presented LaunchProof, signal os.Signal) error {
	host.mu.Lock()
	defer host.mu.Unlock()
	if err := host.validateProofLocked(presented); err != nil {
		return err
	}
	if err := host.process.Signal(signal); err != nil {
		return hostRefuse(HostRefusalProcessExited, err)
	}
	host.mutations = append(host.mutations, LifecycleMutation{Action: action, PID: host.proof.PID})
	return nil
}

func (host *PrivateGenerationHost) validateProofLocked(presented LaunchProof) error {
	if host.closed || presented != host.proof || !host.leaseHeld {
		return hostRefuse(HostRefusalLaunchProofMismatch, nil)
	}
	if host.process.PID() != host.proof.PID || host.process.ProcessGroupID() != host.proof.ProcessGroupID {
		return hostRefuse(HostRefusalLaunchProofMismatch, nil)
	}
	select {
	case <-host.process.Done():
		return hostRefuse(HostRefusalProcessExited, nil)
	default:
	}
	if err := host.process.ValidateProcessGroup(); err != nil {
		return hostRefuse(HostRefusalLaunchProofMismatch, err)
	}
	lease, err := verifyCompleteLease(host.config)
	if err != nil || lease.ID != host.proof.BundleID {
		return hostRefuse(HostRefusalBundleDrift, err)
	}
	executable, err := os.Lstat(host.proof.ExecutablePath)
	if err != nil || !os.SameFile(host.execInfo, executable) || fileIdentity(executable) != host.proof.Executable {
		return hostRefuse(HostRefusalLaunchProofMismatch, err)
	}
	socket, err := os.Lstat(host.proof.SocketPath)
	if err != nil || socket.Mode()&os.ModeSocket == 0 || !os.SameFile(host.socketInfo, socket) ||
		fileIdentity(socket) != host.proof.SocketIdentity {
		return hostRefuse(HostRefusalLaunchProofMismatch, err)
	}
	return nil
}

// ReleaseLease is deliberately closed in Phase 2. Only the later handover
// journal owner can establish terminal retirement authority; a local boolean
// or process exit is not enough to make bundle bytes eligible for GC.
func (host *PrivateGenerationHost) ReleaseLease() error {
	return hostRefuse(HostRefusalLeaseHeld, nil)
}

// Close performs test/operator cleanup for this exact private process. It is
// not a lifecycle adoption path: only the process handle and socket inode this
// host created can be touched.
func (host *PrivateGenerationHost) Close() error {
	host.mu.Lock()
	if host.closed {
		host.mu.Unlock()
		return nil
	}
	host.closed = true
	process, socket, socketInfo := host.process, host.proof.SocketPath, host.socketInfo
	host.mu.Unlock()
	if err := terminateOwnedSession(process); err != nil {
		host.mu.Lock()
		host.closed = false
		host.mu.Unlock()
		return err
	}
	removeExactSocket(socket, socketInfo)
	return nil
}

func verifyCompleteLease(cfg PrivateGenerationConfig) (codexbundle.Lease, error) {
	lease, err := codexbundle.Open(filepath.Clean(cfg.LeaseRoot), cfg.RequiredProtocol)
	if err != nil {
		return codexbundle.Lease{}, hostRefuse(HostRefusalBundleDrift, err)
	}
	requirements := qualifiedBundleRequirements()
	if len(lease.Manifest.Artifacts) != len(requirements) {
		return codexbundle.Lease{}, hostRefuse(HostRefusalBundleIncomplete, nil)
	}
	present := make(map[string]codexbundle.Artifact, len(lease.Manifest.Artifacts))
	for _, artifact := range lease.Manifest.Artifacts {
		present[artifact.Path] = artifact
	}
	for _, requirement := range requirements {
		artifact, ok := present[filepath.ToSlash(filepath.Clean(requirement.path))]
		if !ok || !slices.Equal(artifact.Roles, requirement.roles) {
			return codexbundle.Lease{}, hostRefuse(HostRefusalBundleIncomplete, nil)
		}
	}
	if len(lease.Paths(codexbundle.RoleServer)) != 1 || len(lease.Paths(codexbundle.RoleTUI)) != 1 ||
		len(lease.Paths(codexbundle.RoleHelper)) < 1 {
		return codexbundle.Lease{}, hostRefuse(HostRefusalBundleIncomplete, nil)
	}
	return lease, nil
}

func leaseArtifact(lease codexbundle.Lease, absolute string) (codexbundle.Artifact, bool) {
	for _, artifact := range lease.Manifest.Artifacts {
		if filepath.Join(lease.Root, filepath.FromSlash(artifact.Path)) == absolute && slices.Contains(artifact.Roles, codexbundle.RoleServer) {
			return artifact, true
		}
	}
	return codexbundle.Artifact{}, false
}

func privateGenerationEnvironment(cfg PrivateGenerationConfig) []string {
	environment := cfg.Environment
	if environment == nil {
		environment = os.Environ()
	}
	filtered := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if name == "TMUX" || name == "TMUX_PANE" || name == "CODEX_HOME" {
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(filtered, "CODEX_HOME="+filepath.Clean(cfg.StateDomainPath))
}

func absoluteNonRoot(path string) bool {
	return path != "" && path == strings.TrimSpace(path) && filepath.IsAbs(path) &&
		path == filepath.Clean(path) && filepath.Clean(path) != filepath.Clean(string(filepath.Separator))
}

func fileIdentity(info fs.FileInfo) FileIdentity {
	identity := FileIdentity{Mode: uint32(info.Mode()), Size: info.Size()}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		identity.Device = uint64(stat.Dev)
		identity.Inode = uint64(stat.Ino)
		identity.ChangeTimeSeconds, identity.ChangeTimeNanoseconds = statChangeTime(stat)
	}
	return identity
}

// Stat_t spells the change-time field Ctim on Linux and Ctimespec on Darwin.
// Reflection keeps this package inside the repository's explicit two-OS
// contract without platform build files or narrowing conversions.
func statChangeTime(stat *syscall.Stat_t) (int64, int64) {
	value := reflect.ValueOf(stat).Elem()
	for _, name := range []string{"Ctim", "Ctimespec"} {
		field := value.FieldByName(name)
		if !field.IsValid() {
			continue
		}
		seconds, nanoseconds := field.FieldByName("Sec"), field.FieldByName("Nsec")
		if seconds.IsValid() && nanoseconds.IsValid() && seconds.CanInt() && nanoseconds.CanInt() {
			return seconds.Int(), nanoseconds.Int()
		}
	}
	return 0, 0
}

func ownerPrivateDirectory(path string) (fs.FileInfo, error) {
	info, err := os.Lstat(filepath.Clean(path))
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return nil, hostRefuse(HostRefusalPrivateRootInvalid, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int64(stat.Uid) != int64(os.Geteuid()) {
		return nil, hostRefuse(HostRefusalPrivateRootInvalid, nil)
	}
	return info, nil
}

func revalidateOwnerPrivateDirectory(path string, expected fs.FileInfo) error {
	current, err := ownerPrivateDirectory(path)
	if err != nil || expected == nil || !os.SameFile(expected, current) || expected.Mode() != current.Mode() {
		return hostRefuse(HostRefusalPrivateRootInvalid, err)
	}
	return nil
}

func removeExactSocket(path string, owned fs.FileInfo) {
	if owned == nil || strings.TrimSpace(path) == "" {
		return
	}
	current, err := os.Lstat(path)
	if err == nil && current.Mode()&os.ModeSocket != 0 && os.SameFile(owned, current) &&
		fileIdentity(owned) == fileIdentity(current) {
		_ = os.Remove(path)
	}
}

func (proof LaunchProof) String() string {
	return fmt.Sprintf("endpoint=%s/%s runtime=%s pid=%d bundle=%s",
		proof.Endpoint.StateDomainID, proof.Endpoint.EndpointGenerationID, proof.EndpointRuntimeID, proof.PID, proof.BundleID)
}

func randomRuntimeID(width int) (string, error) {
	value := make([]byte, width)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}
