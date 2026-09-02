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
// proof. Device/inode prove replacement at the same path; mode, size and hash
// are independently revalidated through the content-addressed lease.
type FileIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	Mode   uint32 `json:"mode"`
	Size   int64  `json:"size"`
}

// LaunchProof is the complete lifecycle authority for one private endpoint.
// Every axis must match both the stored proof and the current process,
// executable, and socket observations before any lifecycle signal is sent.
type LaunchProof struct {
	Endpoint          EndpointIdentity `json:"endpoint"`
	EndpointRuntimeID string           `json:"endpointRuntimeID"`
	PID               int              `json:"pid"`
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
	Signal(os.Signal) error
	Done() <-chan struct{}
	ExitError() error
}

type execGenerationProcess struct {
	cmd     *exec.Cmd
	done    chan struct{}
	mu      sync.Mutex
	exitErr error
}

func (process *execGenerationProcess) PID() int { return process.cmd.Process.Pid }
func (process *execGenerationProcess) Signal(signal os.Signal) error {
	return process.cmd.Process.Signal(signal)
}
func (process *execGenerationProcess) Done() <-chan struct{} { return process.done }
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
		cleanupFailedLaunch(process, cfg.SocketPath, socketInfo)
		return nil, readyErr
	}
	// Publication is a second complete lease boundary. A same-user updater (or
	// launch seam) cannot swap the executable or a sibling helper after the
	// pre-launch check and leave a ready-but-unprovable host published.
	reopened, reopenErr := verifyCompleteLease(cfg)
	currentExecutable, executableErr := os.Lstat(executable)
	if reopenErr != nil || reopened.ID != lease.ID || executableErr != nil ||
		!os.SameFile(execInfo, currentExecutable) || fileIdentity(execInfo) != fileIdentity(currentExecutable) {
		cleanupFailedLaunch(process, cfg.SocketPath, socketInfo)
		if reopenErr != nil {
			return nil, hostRefuse(HostRefusalBundleDrift, reopenErr)
		}
		return nil, hostRefuse(HostRefusalLaunchProofMismatch, executableErr)
	}
	if err := revalidateOwnerPrivateDirectory(cfg.PrivateRoot, privateRootInfo); err != nil {
		cleanupFailedLaunch(process, cfg.SocketPath, socketInfo)
		return nil, err
	}
	if err := revalidateOwnerPrivateDirectory(cfg.StateDomainPath, stateDomainInfo); err != nil {
		cleanupFailedLaunch(process, cfg.SocketPath, socketInfo)
		return nil, err
	}
	select {
	case <-process.Done():
		cleanupFailedLaunch(process, cfg.SocketPath, socketInfo)
		return nil, hostRefuse(HostRefusalProcessExited, process.ExitError())
	default:
	}
	host.socketInfo = socketInfo
	host.proof = LaunchProof{
		Endpoint: cfg.Endpoint, EndpointRuntimeID: runtimeID, PID: process.PID(), SocketPath: cfg.SocketPath,
		SocketIdentity: fileIdentity(socketInfo), ExecutablePath: executable, Executable: fileIdentity(execInfo),
		ExecutableSHA256: artifact.SHA256, BundleID: lease.ID,
	}
	return host, nil
}

func cleanupFailedLaunch(process ownedGenerationProcess, socket string, socketInfo fs.FileInfo) {
	select {
	case <-process.Done():
	default:
		_ = process.Signal(os.Kill)
		<-process.Done()
	}
	removeExactSocket(socket, socketInfo)
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
	command.Stdout, command.Stderr = devNull, devNull
	if err := command.Start(); err != nil {
		return nil, err
	}
	process := &execGenerationProcess{cmd: command, done: make(chan struct{})}
	go func() {
		err := command.Wait()
		process.mu.Lock()
		process.exitErr = err
		process.mu.Unlock()
		close(process.done)
	}()
	return process, nil
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
	select {
	case <-host.process.Done():
		return hostRefuse(HostRefusalProcessExited, nil)
	default:
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
	select {
	case <-process.Done():
	default:
		_ = process.Signal(os.Kill)
		<-process.Done()
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
	}
	return identity
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
	if err == nil && current.Mode()&os.ModeSocket != 0 && os.SameFile(owned, current) {
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
