package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	claudeadapter "github.com/crevissepartners/projmux/internal/integrations/agents/claude"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// Exec replaces the hook shell: the registration process's direct parent must
// be the exact activation-exec process after it exec'd Claude. An unmanaged
// nested Claude may inherit the private activation environment, but cannot pass
// this producer check for its own SessionStart.
const claudeRegistrationHookCommand = "exec projmux internal claude-endpoint-register >/dev/null 2>&1 # " + claudeHookManagedMarker

const claudeEndpointPollInterval = 100 * time.Millisecond

// claudeEndpointBootstrap travels only over an anonymous pipe from the exact
// SessionStart hook to its detached helper. Never log or persist this value.
type claudeEndpointBootstrap struct {
	RegistryPath string
	AgentUID     string
	PaneUID      string
	Generation   string
	Registration coremetadata.ClaudeRegistration
	HookProcess  coremetadata.ProcessIdentity
	Socket       string
	Token        string
}

func (claudeEndpointBootstrap) String() string   { return "[private Claude registration]" }
func (claudeEndpointBootstrap) GoString() string { return "[private Claude registration]" }

func prepareClaudeActivationProcess(spec superviseSpec) (bool, error) {
	store := resourceStoreAtPath(spec.RegistryPath)
	claudeRegistration := false
	_, _, err := store.updateConvergent(func(reg *coremetadata.Registry) error {
		agent, ok := reg.Agent(spec.AgentUID)
		if !ok || agent.Spec.Provider != aiModeClaude {
			return nil
		}
		claudeRegistration = true
		process, _, err := claudeadapter.Process(os.Getpid())
		if err != nil {
			return errors.New("Claude process identity unavailable")
		}
		return intmetadata.DefaultMutator().RecordClaudeProcess(reg, spec.PaneUID, spec.AgentUID, spec.Generation, process)
	})
	return claudeRegistration, err
}

// Only the separate SessionStart registration hook calls this route. Existing
// status hooks and their parser/projection remain unchanged. Every failure is
// quiet and fail-closed; no raw upstream input or credential reaches errors.
func runClaudeEndpointRegistration(args []string) error {
	if len(args) != 0 {
		return nil
	}
	registryPath := os.Getenv(internalClaudeRegistryPathEnv)
	if exactActivationRegistryPath(registryPath) != nil {
		return nil
	}
	store := intmetadata.NewStore(registryPath)
	reg, err := store.LoadDegradedReadOnly()
	if err != nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 64*1024+1))
	if err != nil || len(data) > 64*1024 {
		return nil
	}
	bootstrap, ok := claudeRegistrationBootstrap(reg, registryPath, data, os.Getenv, os.Getppid())
	if !ok {
		return nil
	}
	_, _, err = store.UpdateConvergent(func(current *coremetadata.Registry) error {
		return intmetadata.DefaultMutator().BeginClaudeRegistration(current, bootstrap.PaneUID, bootstrap.AgentUID, bootstrap.Generation, bootstrap.Registration.Authority)
	})
	if err != nil {
		return nil
	}
	_ = startClaudeEndpointHelper(bootstrap)
	return nil
}

func claudeRegistrationBootstrap(reg coremetadata.Registry, registryPath string, data []byte, env func(string) string, parentPID int) (claudeEndpointBootstrap, bool) {
	var payload struct {
		Event     string `json:"hook_event_name"`
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(data, &payload) != nil || payload.Event != "SessionStart" {
		return claudeEndpointBootstrap{}, false
	}
	paneUID, generation := env(internalActivationPaneUIDEnv), env(internalActivationGenerationEnv)
	pane, ok := reg.Pane(paneUID)
	if !ok || pane.Status.Activation.Generation != generation || generation == "" || pane.Status.Activation.Claude == nil {
		return claudeEndpointBootstrap{}, false
	}
	agent, ok := reg.Agent(pane.Status.Activation.AgentUID)
	if !ok || agent.Spec.Provider != aiModeClaude || agent.Status.Phase != coremetadata.PhaseRunning ||
		agent.Status.PaneRef != paneUID || pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != coremetadata.KindAgent || pane.Metadata.OwnerUID() != agent.Metadata.UID || pane.Spec.Role != coremetadata.PaneRoleAgent {
		return claudeEndpointBootstrap{}, false
	}
	process := pane.Status.Activation.Claude.Process
	actual, _, err := claudeadapter.Process(parentPID)
	if err != nil || actual != process || actual.OwnerUID != uint32(os.Getuid()) {
		return claudeEndpointBootstrap{}, false
	}
	socket, token := env("CLAUDE_CODE_MESSAGING_SOCKET"), env("CLAUDE_CODE_MESSAGING_TOKEN")
	if socket == "" || token == "" || len(token) > 4096 || strings.ContainsAny(token, "\r\n\x00") {
		return claudeEndpointBootstrap{}, false
	}
	// Hook identities are untrusted data too. Refuse a credential or locator
	// embedded in any field destined for Registry, even when syntactically valid.
	for _, value := range []string{payload.SessionID} {
		if strings.Contains(value, token) || strings.Contains(value, socket) {
			return claudeEndpointBootstrap{}, false
		}
	}
	if _, err := inspectClaudeSocket(socket); err != nil {
		return claudeEndpointBootstrap{}, false
	}
	nonce := make([]byte, 24)
	if _, err := rand.Read(nonce); err != nil {
		return claudeEndpointBootstrap{}, false
	}
	authority := coremetadata.ClaudeAuthorityRef{SessionID: payload.SessionID, Process: process,
		RegistrationGeneration: hex.EncodeToString(nonce), LeaseProcess: process}
	if !authority.Valid() {
		return claudeEndpointBootstrap{}, false
	}
	hookProcess, _, err := claudeadapter.Process(os.Getpid())
	if err != nil {
		return claudeEndpointBootstrap{}, false
	}
	return claudeEndpointBootstrap{RegistryPath: registryPath, AgentUID: agent.Metadata.UID, PaneUID: paneUID, Generation: generation,
		Registration: coremetadata.ClaudeRegistration{Authority: authority},
		HookProcess:  hookProcess,
		Socket:       socket, Token: token}, true
}

func startClaudeEndpointHelper(bootstrap claudeEndpointBootstrap) error {
	binary, err := os.Executable()
	if err != nil {
		return errors.New("Claude helper unavailable")
	}
	input, err := json.Marshal(bootstrap)
	if err != nil {
		return errors.New("Claude helper bootstrap unavailable")
	}
	readAck, writeAck, err := os.Pipe()
	if err != nil {
		return errors.New("Claude helper acknowledgement unavailable")
	}
	defer readAck.Close()
	cmd := exec.Command(binary, "internal", "claude-endpoint-helper")
	cmd.Stdin = strings.NewReader(string(input))
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	cmd.ExtraFiles = []*os.File{writeAck}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// The official secret is transferred only in the private pipe, never in
	// helper argv or its inherited environment.
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "CLAUDE_CODE_MESSAGING_SOCKET=") && !strings.HasPrefix(value, "CLAUDE_CODE_MESSAGING_TOKEN=") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	if err := cmd.Start(); err != nil {
		_ = writeAck.Close()
		return errors.New("Claude helper start failed")
	}
	_ = writeAck.Close()
	_ = readAck.SetReadDeadline(time.Now().Add(3 * time.Second))
	var ack [1]byte
	_, err = io.ReadFull(readAck, ack[:])
	if err != nil || ack[0] != 1 {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return errors.New("Claude helper admission failed")
	}
	return cmd.Process.Release()
}

func runClaudeEndpointHelper(args []string) error {
	if len(args) != 0 {
		return nil
	}
	ack := os.NewFile(3, "claude-endpoint-ack")
	if ack == nil {
		return nil
	}
	defer ack.Close()
	if info, err := ack.Stat(); err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		return nil
	}
	var bootstrap claudeEndpointBootstrap
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 64*1024+1))
	if err != nil || len(data) > 64*1024 || json.Unmarshal(data, &bootstrap) != nil {
		return nil
	}
	if !claudeHelperProducerMatches(bootstrap, os.Getppid()) {
		return nil
	}
	_ = serveClaudeEndpoint(context.Background(), bootstrap, ack)
	return nil
}

func claudeHelperProducerMatches(bootstrap claudeEndpointBootstrap, parentPID int) bool {
	hook, providerPID, err := claudeadapter.Process(parentPID)
	if err != nil || hook != bootstrap.HookProcess || providerPID != bootstrap.Registration.Authority.Process.PID {
		return false
	}
	provider, _, err := claudeadapter.Process(providerPID)
	return err == nil && provider == bootstrap.Registration.Authority.Process
}

type claudeSocketIdentity struct {
	device uint64
	inode  uint64
	owner  uint32
	mode   os.FileMode
}

func inspectClaudeSocket(path string) (claudeSocketIdentity, error) {
	refused := errors.New("Claude socket is unavailable")
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return claudeSocketIdentity{}, refused
	}
	var first claudeSocketIdentity
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || (info.Mode()&os.ModeSymlink != 0 && !trustedDarwinTempAlias(current)) {
			return claudeSocketIdentity{}, refused
		}
		if current == path {
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 || stat.Uid != uint32(os.Getuid()) {
				return claudeSocketIdentity{}, refused
			}
			first = claudeSocketIdentity{device: uint64(stat.Dev), inode: stat.Ino, owner: stat.Uid, mode: info.Mode()}
		}
		if current == filepath.Dir(current) {
			break
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return claudeSocketIdentity{}, refused
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 || stat.Uid != uint32(os.Getuid()) {
		return claudeSocketIdentity{}, refused
	}
	last := claudeSocketIdentity{device: uint64(stat.Dev), inode: stat.Ino, owner: stat.Uid, mode: info.Mode()}
	if last != first {
		return claudeSocketIdentity{}, refused
	}
	return last, nil
}

func trustedDarwinTempAlias(path string) bool {
	if runtime.GOOS != "darwin" || (path != "/tmp" && path != "/var") {
		return false
	}
	target, err := os.Readlink(path)
	return err == nil && (target == "/private"+path || target == "private"+path)
}

// This is Projmux's own private readiness socket, never the provider address.
// It can be derived from nonsecret exact registration identity without storing
// either socket path in Registry or printing it in a capability projection.
func claudeActivationLeaseDir(registryPath, paneUID, generation string) string {
	digest := sha256.Sum256([]byte(registryPath + "\x00" + paneUID + "\x00" + generation))
	// Both supported operating systems provide /tmp. A fixed short root avoids
	// Darwin's long per-user TMPDIR exceeding sun_path and makes the creator,
	// hook, supervisor, and read-only client independent of inherited TMPDIR.
	return filepath.Join("/tmp", "pmx-ce-"+hex.EncodeToString(digest[:16]))
}

func claudeLeaseSocket(registryPath, paneUID, generation, registrationGeneration string) string {
	digest := sha256.Sum256([]byte(registrationGeneration))
	return filepath.Join(claudeActivationLeaseDir(registryPath, paneUID, generation), hex.EncodeToString(digest[:16])+".sock")
}

func privateClaudeLeaseDir(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Getuid())
}

// The supervisor knows the exact activation, so after reaping its child it can
// remove orphaned helper sockets even if a helper was killed without defers.
// This never reads or waits on Registry, touches a provider socket, or changes
// the termination journal contract. Normal Registry convergence clears the
// nonsecret registration when it consumes that exact supervisor receipt.
func cleanupClaudeActivationLeases(spec superviseSpec) {
	if spec.AgentUID == "" || spec.Generation == "" || exactActivationRegistryPath(spec.RegistryPath) != nil {
		return
	}
	dir := claudeActivationLeaseDir(spec.RegistryPath, spec.PaneUID, spec.Generation)
	if !privateClaudeLeaseDir(dir) {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".sock.json") {
			path := filepath.Join(dir, name)
			if receipt, ok := readClaudeLeaseOwner(path, spec); ok && path == claudeLeaseSocket(spec.RegistryPath, spec.PaneUID, spec.Generation, receipt.Authority.RegistrationGeneration)+".json" {
				_ = os.Remove(path)
			}
			continue
		}
		if len(name) != 37 || !strings.HasSuffix(name, ".sock") {
			continue
		}
		if _, err := hex.DecodeString(strings.TrimSuffix(name, ".sock")); err != nil {
			continue
		}
		path := filepath.Join(dir, name)
		if _, err := inspectClaudeSocket(path); err == nil {
			_ = os.Remove(path)
		}
	}
	_ = os.Remove(dir)
}

func serveClaudeEndpoint(ctx context.Context, bootstrap claudeEndpointBootstrap, ack io.Writer) error {
	if exactActivationRegistryPath(bootstrap.RegistryPath) != nil || bootstrap.Token == "" {
		return errors.New("Claude helper admission failed")
	}
	process, _, err := claudeadapter.Process(os.Getpid())
	if err != nil {
		return errors.New("Claude helper identity unavailable")
	}
	bootstrap.Registration.Authority.LeaseProcess = process
	if !bootstrap.Registration.Authority.Valid() {
		return errors.New("Claude registration identity unavailable")
	}
	socketIdentity, err := inspectClaudeSocket(bootstrap.Socket)
	if err != nil {
		return err
	}
	leasePath := claudeLeaseSocket(bootstrap.RegistryPath, bootstrap.PaneUID, bootstrap.Generation, bootstrap.Registration.Authority.RegistrationGeneration)
	if err := os.Mkdir(filepath.Dir(leasePath), 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return errors.New("Claude helper lease unavailable")
	}
	if !privateClaudeLeaseDir(filepath.Dir(leasePath)) {
		return errors.New("Claude helper lease unavailable")
	}
	defer os.Remove(filepath.Dir(leasePath))
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: leasePath, Net: "unix"})
	if err != nil {
		return errors.New("Claude helper lease unavailable")
	}
	defer listener.Close()
	listener.SetUnlinkOnClose(false)
	ownedLease, _ := os.Lstat(leasePath)
	defer func() {
		if current, err := os.Lstat(leasePath); err == nil && ownedLease != nil && os.SameFile(ownedLease, current) {
			_ = os.Remove(leasePath)
		}
	}()
	if os.Chmod(leasePath, 0o600) != nil {
		return errors.New("Claude helper lease unavailable")
	}
	leaseIdentity, err := inspectClaudeSocket(leasePath)
	if err != nil {
		return errors.New("Claude helper lease unavailable")
	}
	if writeClaudeLeaseOwner(leasePath+".json", bootstrap) != nil {
		return errors.New("Claude helper ownership unavailable")
	}
	defer os.Remove(leasePath + ".json")
	store := intmetadata.NewStore(bootstrap.RegistryPath)
	mutator := intmetadata.DefaultMutator()
	_, _, err = store.UpdateConvergent(func(reg *coremetadata.Registry) error {
		if actual, _, err := claudeadapter.Process(bootstrap.Registration.Authority.Process.PID); err != nil || actual != bootstrap.Registration.Authority.Process {
			return errors.New("Claude provider process is unavailable")
		}
		return mutator.RecordClaudeRegistration(reg, bootstrap.PaneUID, bootstrap.AgentUID, bootstrap.Generation, bootstrap.Registration)
	})
	if err != nil {
		return errors.New("Claude registration admission failed")
	}
	defer func() {
		_, _, _ = store.UpdateConvergent(func(reg *coremetadata.Registry) error {
			mutator.ClearClaudeRegistration(reg, bootstrap.PaneUID, bootstrap.AgentUID, bootstrap.Generation, bootstrap.Registration.Authority)
			return nil
		})
	}()
	initial, err := store.LoadDegradedReadOnly()
	if err != nil {
		return errors.New("Claude registration is unavailable")
	}
	expectedRoute, reason := coremetadata.ResolveAgentRoute(initial, bootstrap.AgentUID)
	if reason != "" {
		return errors.New("Claude registration is unavailable")
	}
	current := func() bool {
		if observed, err := inspectClaudeSocket(leasePath); err != nil || observed != leaseIdentity {
			return false
		}
		actual, _, err := claudeadapter.Process(bootstrap.Registration.Authority.Process.PID)
		if err != nil || actual != bootstrap.Registration.Authority.Process {
			return false
		}
		observed, err := inspectClaudeSocket(bootstrap.Socket)
		if err != nil || observed != socketIdentity {
			return false
		}
		reg, err := store.LoadDegradedReadOnly()
		if err != nil {
			return false
		}
		route, reason := coremetadata.ResolveAgentRoute(reg, bootstrap.AgentUID)
		authority, ok := route.Authority().(coremetadata.ClaudeAuthorityRef)
		return reason == "" && ok && route.Same(expectedRoute) && route.PaneUID == bootstrap.PaneUID && route.Generation == bootstrap.Generation && authority == bootstrap.Registration.Authority
	}
	if !current() {
		return errors.New("Claude registration is stale")
	}
	if _, err := ack.Write([]byte{1}); err != nil {
		return errors.New("Claude helper acknowledgement failed")
	}
	for {
		if ctx.Err() != nil || !current() {
			return nil
		}
		_ = listener.SetDeadline(time.Now().Add(claudeEndpointPollInterval))
		connection, err := listener.AcceptUnix()
		if err != nil {
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				continue
			}
			return nil
		}
		_ = connection.SetDeadline(time.Now().Add(claudeEndpointPollInterval))
		if current() {
			_, _ = connection.Write([]byte{1})
		}
		_ = connection.Close()
	}
}

func probeClaudeRegistrationLease(registryPath string, route coremetadata.AgentRouteRef) bool {
	authority, ok := route.Authority().(coremetadata.ClaudeAuthorityRef)
	if !ok || !authority.Valid() {
		return false
	}
	for _, expected := range []coremetadata.ProcessIdentity{authority.Process, authority.LeaseProcess} {
		actual, _, err := claudeadapter.Process(expected.PID)
		if err != nil || actual != expected {
			return false
		}
	}
	path := claudeLeaseSocket(registryPath, route.PaneUID, route.Generation, authority.RegistrationGeneration)
	if _, err := inspectClaudeSocket(path); err != nil {
		return false
	}
	// Dial only Projmux's readiness helper. Never connect to the provider inbox;
	// its secret path exists only in serveClaudeEndpoint's private memory.
	connection, err := net.DialTimeout("unix", path, 200*time.Millisecond)
	if err != nil {
		return false
	}
	defer connection.Close()
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		return false
	}
	peer, err := claudeadapter.PeerProcess(unixConnection)
	if err != nil || peer != authority.LeaseProcess {
		return false
	}
	_ = connection.SetDeadline(time.Now().Add(200 * time.Millisecond))
	var ready [1]byte
	_, err = io.ReadFull(connection, ready[:])
	return err == nil && ready[0] == 1
}
