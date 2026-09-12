package codexinstalled

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

const (
	DefaultSmokeRootEnv = "PROJMUX_CODEX_DAEMON_SMOKE_ROOT"
	defaultTimeout      = 15 * time.Second
	maxCommandOutput    = 32 * 1024
)

type Fixture struct {
	Root       string
	CodexHome  string
	SocketPath string
	Workspace  string

	realCodex        string
	shimPath         string
	ledger           *Ledger
	startResultPath  string
	versions         VersionTuple
	managed          bool
	ownsState        bool
	direct           *DirectEndpoint
	directSocketInfo fs.FileInfo
	managedPID       int
	managedStarted   bool
}

type daemonVersion struct {
	Status              string `json:"status"`
	Backend             string `json:"backend"`
	ManagedCodexVersion string `json:"managedCodexVersion"`
	CLIVersion          string `json:"cliVersion"`
	AppServerVersion    string `json:"appServerVersion"`
	SocketPath          string `json:"socketPath"`
	PID                 int    `json:"pid"`
}

func SmokeRoot(envName string) (string, bool, error) {
	root := strings.TrimSpace(os.Getenv(envName))
	if root == "" {
		return "", false, nil
	}
	root = filepath.Clean(root)
	tmpRoot := filepath.Clean(os.TempDir())
	if !filepath.IsAbs(root) || root == tmpRoot || !strings.HasPrefix(root, tmpRoot+string(filepath.Separator)) {
		return "", true, fmt.Errorf("smoke root must be an isolated child of %s", tmpRoot)
	}
	return root, true, nil
}

func NewClean(root string) (*Fixture, error) {
	if err := validateInheritedEnvironment(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create installed Codex root: %w", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read installed Codex root: %w", err)
	}
	if len(entries) != 0 {
		return nil, fmt.Errorf("installed Codex root must start empty: entries=%d", len(entries))
	}
	return newFixture(root, false)
}

// NewExisting adopts only the existing CODEX_HOME as a non-owned input. It is
// used by the optional model-dependent broker smokes, whose endpoint lifecycle
// remains outside Phase 0. Only the shim and ledger it creates are cleaned.
func NewExisting(root string) (*Fixture, error) {
	if err := validateInheritedEnvironment(); err != nil {
		return nil, err
	}
	return newFixture(root, true)
}

func newFixture(root string, existing bool) (*Fixture, error) {
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("installed Codex root must be absolute")
	}
	realCodex, err := exec.LookPath("codex")
	if err != nil {
		return nil, fmt.Errorf("installed codex executable: %w", err)
	}
	realCodex, err = filepath.Abs(realCodex)
	if err != nil {
		return nil, fmt.Errorf("absolute installed codex executable: %w", err)
	}
	ledgerPath := filepath.Join(root, "codex-command-ledger")
	startResultPath := filepath.Join(root, "managed-start-result")
	for _, supportArtifact := range []string{filepath.Join(root, "fixture-bin"), ledgerPath, startResultPath} {
		if _, err := os.Lstat(supportArtifact); err == nil {
			return nil, fmt.Errorf("installed Codex support artifact already exists: %s", filepath.Base(supportArtifact))
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("inspect installed Codex support artifact: %w", err)
		}
	}
	shimPath, err := writeLedgerShim(root)
	if err != nil {
		return nil, err
	}
	fixture := &Fixture{
		Root:            root,
		CodexHome:       filepath.Join(root, "codex-home"),
		SocketPath:      filepath.Join(root, "codex-home", "app-server-control", "app-server-control.sock"),
		Workspace:       filepath.Join(root, "workspace"),
		realCodex:       realCodex,
		shimPath:        shimPath,
		ledger:          newLedger(ledgerPath),
		startResultPath: startResultPath,
		versions:        VersionTuple{}.normalized(),
		ownsState:       !existing,
	}
	if existing {
		if got := filepath.Clean(os.Getenv("CODEX_HOME")); got != fixture.CodexHome {
			_ = fixture.cleanupSupportArtifacts()
			return nil, fmt.Errorf("CODEX_HOME = %q, want %q", got, fixture.CodexHome)
		}
	} else {
		for _, path := range []string{fixture.CodexHome, fixture.Workspace} {
			if err := os.MkdirAll(path, 0o700); err != nil {
				_ = fixture.Cleanup()
				return nil, fmt.Errorf("create installed Codex owned root: %w", err)
			}
		}
	}
	fixture.discoverCLI()
	return fixture, nil
}

func validateInheritedEnvironment() error {
	for _, inherited := range []string{"TMUX", "TMUX_PANE"} {
		if _, present := os.LookupEnv(inherited); present {
			return fmt.Errorf("%s must be removed for the installed Codex smoke", inherited)
		}
	}
	return nil
}

func (fixture *Fixture) ApplyEnv(setenv func(string, string)) {
	setenv("CODEX_HOME", fixture.CodexHome)
	setenv("PROJMUX_CODEX_INSTALLED_HOME", fixture.CodexHome)
	setenv("PROJMUX_CODEX_INSTALLED_REAL", fixture.realCodex)
	setenv("PROJMUX_CODEX_INSTALLED_LEDGER", fixture.ledger.path)
	setenv("PROJMUX_CODEX_INSTALLED_START_RESULT", fixture.startResultPath)
	setenv("PATH", filepath.Dir(fixture.shimPath)+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func (fixture *Fixture) Versions() VersionTuple { return fixture.versions.normalized() }

func (fixture *Fixture) Ledger() *Ledger { return fixture.ledger }

func (fixture *Fixture) ProvisionManagedPayload() Result {
	if fixture.managed {
		return NewResult(fixture.versions, TopologyManaged, StageProvision, ResultPass, "managed-payload-provisioned")
	}
	real, err := filepath.EvalSymlinks(fixture.realCodex)
	if err != nil {
		return NewResult(fixture.versions, TopologyManaged, StageProvision, ResultInfraError, "managed-payload-resolution-failed")
	}
	releaseRoot := filepath.Dir(filepath.Dir(real))
	if filepath.Base(filepath.Dir(real)) != "bin" {
		return NewResult(fixture.versions, TopologyManaged, StageProvision, ResultUnsupported, "managed-payload-layout-unsupported")
	}
	packageManifest := filepath.Join(releaseRoot, "codex-package.json")
	info, err := os.Stat(packageManifest)
	if err != nil || !info.Mode().IsRegular() {
		return NewResult(fixture.versions, TopologyManaged, StageProvision, ResultUnsupported, "managed-payload-manifest-missing")
	}
	if info.Size() <= 0 || info.Size() > maxCommandOutput {
		return NewResult(fixture.versions, TopologyManaged, StageProvision, ResultInfraError, "managed-payload-manifest-invalid")
	}
	rawManifest, err := os.ReadFile(packageManifest) // #nosec G304 -- resolved installed payload manifest.
	if err != nil {
		return NewResult(fixture.versions, TopologyManaged, StageProvision, ResultInfraError, "managed-payload-manifest-read-failed")
	}
	var manifest struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(rawManifest, &manifest); err != nil || strings.TrimSpace(manifest.Version) == "" {
		return NewResult(fixture.versions, TopologyManaged, StageProvision, ResultInfraError, "managed-payload-manifest-invalid")
	}
	standalone := filepath.Join(fixture.CodexHome, "packages", "standalone")
	if err := os.MkdirAll(standalone, 0o700); err != nil {
		return NewResult(fixture.versions, TopologyManaged, StageProvision, ResultInfraError, "managed-payload-root-create-failed")
	}
	current := filepath.Join(standalone, "current")
	if err := os.Symlink(releaseRoot, current); err != nil {
		return NewResult(fixture.versions, TopologyManaged, StageProvision, ResultInfraError, "managed-payload-link-failed")
	}
	fixture.managed = true
	fixture.versions.Managed = strings.TrimSpace(manifest.Version)
	fixture.ledger.record(Command{Scope: ScopeIsolated, Operation: "managed-payload-provision", Mutation: MutationEndpointLifecycle})
	return NewResult(fixture.versions, TopologyManaged, StageProvision, ResultPass, "managed-payload-provisioned")
}

type DirectEndpoint struct {
	fixture *Fixture
	command *exec.Cmd
	exited  chan error
	health  codexappserver.Health
	closed  bool
}

func (endpoint *DirectEndpoint) Health() codexappserver.Health { return endpoint.health }

// StartDirect owns an isolated default-socket process. An optional exact
// executable lets the fixture model an external upgrade: PATH/managed bytes
// may be N+1 while the live unmanaged endpoint remains N.
func (fixture *Fixture) StartDirect(ctx context.Context, projmuxVersion string, executableOverride ...string) (*DirectEndpoint, Result) {
	if fixture.direct != nil {
		return nil, NewResult(fixture.versions, TopologyDirect, StageStart, ResultInfraError, "direct-endpoint-already-owned")
	}
	executable := fixture.realCodex
	if len(executableOverride) > 1 {
		return nil, NewResult(fixture.versions, TopologyDirect, StageStart, ResultInfraError, "direct-executable-invalid")
	}
	if len(executableOverride) == 1 {
		executable = executableOverride[0]
	}
	executable = filepath.Clean(strings.TrimSpace(executable))
	info, err := os.Stat(executable)
	if !filepath.IsAbs(executable) || err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return nil, NewResult(fixture.versions, TopologyDirect, StageStart, ResultInfraError, "direct-executable-invalid")
	}
	fixture.ledger.record(Command{Scope: ScopeIsolated, Operation: "direct-start", Mutation: MutationEndpointLifecycle})
	command := exec.CommandContext(ctx, executable, "app-server", "--listen", "unix://") // #nosec G204 -- explicit exact executable and fixed argv.
	command.Env = isolatedEnvironment(os.Environ(), fixture.CodexHome)
	var output boundedBuffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		return nil, NewResult(fixture.versions, TopologyDirect, StageStart, ResultInfraError, "direct-process-start-failed")
	}
	endpoint := &DirectEndpoint{fixture: fixture, command: command, exited: make(chan error, 1)}
	fixture.direct = endpoint
	go func() { endpoint.exited <- command.Wait() }()

	readyCtx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
	for {
		select {
		case <-readyCtx.Done():
			_ = endpoint.forceCleanup()
			return nil, NewResult(fixture.versions, TopologyDirect, StageReady, ResultInfraError, "direct-readiness-timeout")
		case <-endpoint.exited:
			endpoint.closed = true
			fixture.direct = nil
			return nil, NewResult(fixture.versions, TopologyDirect, StageReady, ResultInfraError, "direct-process-exited-before-ready")
		default:
		}
		health := codexappserver.ProbeDefaultProxy(readyCtx, codexappserver.DefaultProbeTimeout, projmuxVersion, true)
		fixture.observeHealth(health)
		if health.EndpointReadiness == codexappserver.EndpointReady {
			info, err := os.Lstat(fixture.SocketPath)
			if err == nil && info.Mode()&os.ModeSocket != 0 {
				endpoint.health = health
				fixture.directSocketInfo = info
				return endpoint, NewResult(fixture.versions, TopologyDirect, StageReady, ResultPass, "direct-endpoint-ready")
			}
		}
		select {
		case <-readyCtx.Done():
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (endpoint *DirectEndpoint) Close(ctx context.Context) Result {
	if endpoint.closed {
		return NewResult(endpoint.fixture.versions, TopologyDirect, StageClose, ResultInfraError, "direct-endpoint-already-closed")
	}
	endpoint.fixture.ledger.record(Command{Scope: ScopeIsolated, Operation: "direct-close", Mutation: MutationEndpointLifecycle})
	if endpoint.command.Process == nil {
		return NewResult(endpoint.fixture.versions, TopologyDirect, StageClose, ResultInfraError, "direct-process-missing")
	}
	_ = endpoint.command.Process.Signal(syscall.SIGTERM)
	graceCtx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
	select {
	case <-graceCtx.Done():
		endpoint.fixture.ledger.record(Command{Scope: ScopeIsolated, Operation: "direct-force-close", Mutation: MutationEndpointLifecycle})
		if err := endpoint.command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return NewResult(endpoint.fixture.versions, TopologyDirect, StageClose, ResultFail, "direct-kill-failed")
		}
		if err := waitForProcessExit(endpoint.exited, defaultTimeout); err != nil {
			return NewResult(endpoint.fixture.versions, TopologyDirect, StageClose, ResultFail, "direct-kill-wait-timeout")
		}
	case <-endpoint.exited:
	}
	endpoint.markClosed()
	removed, err := removeExactResidualSocket(endpoint.fixture.SocketPath, endpoint.fixture.directSocketInfo)
	if err != nil {
		return NewResult(endpoint.fixture.versions, TopologyDirect, StageClose, ResultInfraError, "direct-socket-observation-failed")
	}
	if !removed {
		return NewResult(endpoint.fixture.versions, TopologyDirect, StageClose, ResultFail, "direct-foreign-socket-preserved")
	}
	endpoint.fixture.directSocketInfo = nil
	return NewResult(endpoint.fixture.versions, TopologyDirect, StageClose, ResultPass, "direct-endpoint-closed")
}

func (endpoint *DirectEndpoint) forceCleanup() error {
	if endpoint.closed {
		return nil
	}
	endpoint.fixture.ledger.record(Command{Scope: ScopeIsolated, Operation: "direct-force-close", Mutation: MutationEndpointLifecycle})
	if endpoint.command.Process != nil {
		_ = endpoint.command.Process.Kill()
	}
	if err := waitForProcessExit(endpoint.exited, defaultTimeout); err != nil {
		return err
	}
	endpoint.markClosed()
	return nil
}

func (endpoint *DirectEndpoint) markClosed() {
	endpoint.closed = true
	endpoint.fixture.direct = nil
}

func waitForProcessExit(exited <-chan error, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-exited:
		return nil
	case <-timer.C:
		return fmt.Errorf("direct process cleanup timed out")
	}
}

func removeExactResidualSocket(path string, readyInfo fs.FileInfo) (bool, error) {
	currentInfo, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if readyInfo == nil || currentInfo.Mode()&os.ModeSocket == 0 || !os.SameFile(readyInfo, currentInfo) {
		return false, nil
	}
	if err := os.Remove(path); err != nil {
		return false, err
	}
	return true, nil
}

func (fixture *Fixture) RunManagedLifecycle(ctx context.Context, projmuxVersion string) Result {
	provision := fixture.ProvisionManagedPayload()
	if provision.Class != ResultPass {
		return provision
	}
	first, err := codexappserver.EnsureDefaultProxyReady(ctx, codexappserver.TriggerNativeUserAction, projmuxVersion, true)
	if err != nil {
		return NewResult(fixture.versions, TopologyManaged, StageStart, ResultInfraError, "managed-start-context-failed")
	}
	fixture.observeHealth(first)
	fixture.managedStarted = first.Lifecycle == codexappserver.LifecycleStarted
	if first.Lifecycle == codexappserver.LifecycleStartFailed && first.LifecycleReason == codexappserver.LifecycleReasonStartManagedPayloadMissing {
		return NewResult(fixture.versions, TopologyManaged, StageStart, ResultUnsupported, "managed-payload-missing")
	}
	if first.EndpointReadiness != codexappserver.EndpointReady ||
		(first.Lifecycle != codexappserver.LifecycleStarted && first.Lifecycle != codexappserver.LifecycleAlreadyRunning) {
		return NewResult(fixture.versions, TopologyManaged, StageStart, ResultInfraError, "managed-endpoint-not-ready")
	}
	if first.Lifecycle != codexappserver.LifecycleStarted {
		return fixture.retireAfterFailure(ctx, StageStart, "managed-start-ownership-not-observed", ResultFail)
	}
	started, err := fixture.readManagedStartResult()
	if err != nil {
		return fixture.retireAfterFailure(ctx, StageStart, "managed-start-result-unavailable", ResultInfraError)
	}
	status, err := fixture.readDaemonVersion(ctx)
	if err != nil {
		return fixture.retireAfterFailure(ctx, StageReady, "managed-status-unavailable", ResultInfraError)
	}
	if status.Backend != "pid" {
		return fixture.retireAfterFailure(ctx, StageReady, "managed-backend-not-pid", ResultFail)
	}
	if filepath.Clean(status.SocketPath) != fixture.SocketPath {
		return fixture.retireAfterFailure(ctx, StageReady, "managed-socket-not-exact", ResultFail)
	}
	managedPID, err := fixture.readManagedPID()
	if err != nil {
		return fixture.retireAfterFailure(ctx, StageReady, "managed-pid-missing", ResultFail)
	}
	if err := validateManagedIdentity(status, started.PID, managedPID, fixture.SocketPath); err != nil {
		return fixture.retireAfterFailure(ctx, StageReady, "managed-identity-mismatch", ResultFail)
	}
	fixture.managedPID = managedPID
	fixture.observeDaemonVersion(status)

	second, err := codexappserver.EnsureDefaultProxyReady(ctx, codexappserver.TriggerNativeUserAction, projmuxVersion, true)
	if err != nil {
		return fixture.retireAfterFailure(ctx, StageReuse, "managed-reuse-context-failed", ResultInfraError)
	}
	fixture.observeHealth(second)
	if second.Lifecycle != codexappserver.LifecycleAlreadyRunning {
		return fixture.retireAfterFailure(ctx, StageReuse, "managed-reuse-not-idempotent", ResultFail)
	}

	stopCtx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
	command := exec.CommandContext(stopCtx, "codex", "app-server", "daemon", "stop")
	command.Env = isolatedEnvironment(os.Environ(), fixture.CodexHome)
	var output boundedBuffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		return fixture.retireAfterFailure(ctx, StageRetire, "managed-stop-failed", ResultFail)
	}
	if err := waitForRetirement(ctx, fixture.SocketPath, fixture.managedPID); err != nil {
		return fixture.retireAfterFailure(ctx, StageRetire, "managed-retirement-incomplete", ResultFail)
	}
	fixture.managedPID = 0
	fixture.managedStarted = false
	return NewResult(fixture.versions, TopologyManaged, StageRetire, ResultPass, "managed-endpoint-started-reused-retired")
}

func (fixture *Fixture) retireAfterFailure(_ context.Context, stage Stage, reason string, class ResultClass) Result {
	if fixture.managedPID > 0 {
		fixture.ledger.record(Command{Scope: ScopeIsolated, Operation: "managed-signal-cleanup", Mutation: MutationEndpointLifecycle})
		cleanupCtx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
		defer cancel()
		killErr := syscall.Kill(fixture.managedPID, syscall.SIGTERM)
		if killErr != nil && !errors.Is(killErr, syscall.ESRCH) {
			return NewResult(fixture.versions, TopologyManaged, stage, ResultInfraError, reason+"-cleanup-incomplete")
		}
		if err := waitForRetirement(cleanupCtx, fixture.SocketPath, fixture.managedPID); err != nil {
			return NewResult(fixture.versions, TopologyManaged, stage, ResultInfraError, reason+"-cleanup-incomplete")
		}
		fixture.managedPID = 0
		fixture.managedStarted = false
	}
	if fixture.managedStarted {
		fixture.ledger.record(Command{Scope: ScopeIsolated, Operation: "managed-stop-cleanup", Mutation: MutationEndpointLifecycle})
		stopCtx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
		defer cancel()
		command := exec.CommandContext(stopCtx, fixture.realCodex, "app-server", "daemon", "stop") // #nosec G204 -- installed executable and fixed argv.
		command.Env = isolatedEnvironment(os.Environ(), fixture.CodexHome)
		command.Stdout = &boundedBuffer{}
		command.Stderr = &boundedBuffer{}
		if err := command.Run(); err != nil {
			return NewResult(fixture.versions, TopologyManaged, stage, ResultInfraError, reason+"-cleanup-incomplete")
		}
		if err := waitForRetirement(stopCtx, fixture.SocketPath, 0); err != nil {
			return NewResult(fixture.versions, TopologyManaged, stage, ResultInfraError, reason+"-cleanup-incomplete")
		}
		fixture.managedStarted = false
	}
	return NewResult(fixture.versions, TopologyManaged, stage, class, reason)
}

func (fixture *Fixture) readDaemonVersion(ctx context.Context) (daemonVersion, error) {
	commandCtx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
	command := exec.CommandContext(commandCtx, "codex", "app-server", "daemon", "version")
	command.Env = isolatedEnvironment(os.Environ(), fixture.CodexHome)
	var output boundedBuffer
	command.Stdout = &output
	command.Stderr = &boundedBuffer{}
	if err := command.Run(); err != nil {
		return daemonVersion{}, err
	}
	var status daemonVersion
	if err := json.Unmarshal(output.Bytes(), &status); err != nil {
		return daemonVersion{}, err
	}
	return status, nil
}

func (fixture *Fixture) readManagedPID() (int, error) {
	pidPath := filepath.Join(fixture.CodexHome, "app-server-daemon", "app-server.pid")
	return readManagedPIDAt(pidPath)
}

func readManagedPIDAt(pidPath string) (int, error) {
	info, err := os.Lstat(pidPath)
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 512 {
		return 0, fmt.Errorf("managed pid artifact is unavailable")
	}
	raw, err := os.ReadFile(pidPath) // #nosec G304 -- exact contained public daemon PID artifact.
	if err != nil {
		return 0, err
	}
	var artifact struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(raw, &artifact); err != nil || artifact.PID <= 0 {
		return 0, fmt.Errorf("managed pid artifact is invalid")
	}
	return artifact.PID, nil
}

func (fixture *Fixture) readManagedStartResult() (daemonVersion, error) {
	return readManagedStartResultAt(fixture.startResultPath)
}

func readManagedStartResultAt(path string) (daemonVersion, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return daemonVersion{}, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxCommandOutput {
		return daemonVersion{}, fmt.Errorf("managed start result is unavailable")
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- exact contained public start result.
	if err != nil {
		return daemonVersion{}, err
	}
	var result daemonVersion
	if err := json.Unmarshal(raw, &result); err != nil || result.Status != "started" || result.PID <= 0 {
		return daemonVersion{}, fmt.Errorf("managed start result is invalid")
	}
	return result, nil
}

func validateManagedIdentity(status daemonVersion, startedPID, artifactPID int, socketPath string) error {
	if status.Backend != "pid" || filepath.Clean(status.SocketPath) != filepath.Clean(socketPath) ||
		startedPID <= 0 || artifactPID <= 0 || startedPID != artifactPID {
		return fmt.Errorf("managed identity does not match its contained public evidence")
	}
	return nil
}

func waitForRetirement(ctx context.Context, socketPath string, pid int) error {
	deadline := time.Now().Add(defaultTimeout)
	for {
		_, socketErr := os.Lstat(socketPath)
		processGone := pid <= 0 || errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
		if errors.Is(socketErr, fs.ErrNotExist) && processGone {
			return nil
		}
		if socketErr != nil && !errors.Is(socketErr, fs.ErrNotExist) {
			return socketErr
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("managed endpoint retirement deadline exceeded")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (fixture *Fixture) observeHealth(health codexappserver.Health) {
	if health.CLIVersion != "" {
		fixture.versions.CLI = health.CLIVersion
	}
	if health.ManagedVersion != "" {
		fixture.versions.Managed = health.ManagedVersion
	}
	if health.RunningVersion != "" {
		fixture.versions.AppServer = health.RunningVersion
	}
	fixture.versions = fixture.versions.normalized()
}

func (fixture *Fixture) observeDaemonVersion(status daemonVersion) {
	fixture.versions = VersionTuple{
		CLI:       status.CLIVersion,
		Managed:   status.ManagedCodexVersion,
		AppServer: status.AppServerVersion,
	}.normalized()
}

func (fixture *Fixture) discoverCLI() {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, fixture.realCodex, "--version") // #nosec G204 -- installed executable and fixed argv.
	command.Env = isolatedEnvironment(os.Environ(), fixture.CodexHome)
	var output boundedBuffer
	command.Stdout = &output
	command.Stderr = &output
	fixture.ledger.record(Command{Scope: ScopeIsolated, Operation: "cli-version", Mutation: MutationNone})
	if command.Run() == nil {
		fields := strings.Fields(string(output.Bytes()))
		if len(fields) > 0 {
			fixture.versions.CLI = fields[len(fields)-1]
		}
	}
	fixture.versions = fixture.versions.normalized()
}

func (fixture *Fixture) Cleanup() error {
	var errs []error
	if fixture.direct != nil {
		errs = append(errs, fixture.direct.forceCleanup())
	}
	if fixture.managedPID > 0 {
		fixture.ledger.record(Command{Scope: ScopeIsolated, Operation: "managed-signal-cleanup", Mutation: MutationEndpointLifecycle})
		if err := syscall.Kill(fixture.managedPID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			errs = append(errs, err)
		}
		if err := waitForRetirement(context.Background(), fixture.SocketPath, fixture.managedPID); err != nil {
			errs = append(errs, err)
		} else {
			fixture.managedPID = 0
			fixture.managedStarted = false
		}
	}
	if fixture.managedPID == 0 && fixture.managedStarted {
		fixture.ledger.record(Command{Scope: ScopeIsolated, Operation: "managed-stop-cleanup", Mutation: MutationEndpointLifecycle})
		ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
		command := exec.CommandContext(ctx, fixture.realCodex, "app-server", "daemon", "stop") // #nosec G204 -- installed executable and fixed argv.
		command.Env = isolatedEnvironment(os.Environ(), fixture.CodexHome)
		command.Stdout = &boundedBuffer{}
		command.Stderr = &boundedBuffer{}
		if err := command.Run(); err != nil {
			errs = append(errs, err)
		} else if err := waitForRetirement(ctx, fixture.SocketPath, 0); err != nil {
			errs = append(errs, err)
		} else {
			fixture.managedStarted = false
		}
		cancel()
	}
	if fixture.direct != nil || fixture.managedPID > 0 || fixture.managedStarted {
		return errors.Join(errs...)
	}
	if fixture.ownsState {
		directSocketRemoved, err := removeExactResidualSocket(fixture.SocketPath, fixture.directSocketInfo)
		if err != nil {
			errs = append(errs, fmt.Errorf("inspect exact direct residual socket: %w", err))
			return errors.Join(errs...)
		}
		if !directSocketRemoved {
			errs = append(errs, fmt.Errorf("replacement socket at direct endpoint path was preserved"))
			return errors.Join(errs...)
		}
		fixture.directSocketInfo = nil
		for _, owned := range []string{fixture.CodexHome, fixture.Workspace} {
			if err := os.RemoveAll(owned); err != nil {
				errs = append(errs, err)
			}
		}
	}
	errs = append(errs, fixture.cleanupSupportArtifacts())
	return errors.Join(errs...)
}

func (fixture *Fixture) cleanupSupportArtifacts() error {
	var errs []error
	for _, owned := range []string{filepath.Dir(fixture.shimPath), fixture.ledger.path, fixture.startResultPath} {
		if err := os.RemoveAll(owned); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func isolatedEnvironment(environment []string, codexHome string) []string {
	filtered := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "CODEX_HOME", "TMUX", "TMUX_PANE":
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(filtered, "CODEX_HOME="+codexHome)
}

type boundedBuffer struct {
	buffer bytes.Buffer
}

func (buffer *boundedBuffer) Write(data []byte) (int, error) {
	original := len(data)
	remaining := maxCommandOutput - buffer.buffer.Len()
	if len(data) > remaining {
		data = data[:max(remaining, 0)]
	}
	_, _ = buffer.buffer.Write(data)
	return original, nil
}

func (buffer *boundedBuffer) Bytes() []byte { return buffer.buffer.Bytes() }

// ManagerIsolation is explicit launcher evidence. Namespace identity is read
// again inside the fixture; an opt-in flag or private HOME alone is not proof
// that the official manager cannot reach a host daemon.
type ManagerIsolation struct {
	HostNamespaces map[string]string `json:"hostNamespaces"`
}

func (isolation ManagerIsolation) Verify() (map[string]string, error) {
	if err := validateInheritedEnvironment(); err != nil {
		return nil, err
	}
	for _, key := range []string{"DBUS_SESSION_BUS_ADDRESS", "DBUS_SYSTEM_BUS_ADDRESS", "DBUS_STARTER_ADDRESS"} {
		if os.Getenv(key) != "" {
			return nil, fmt.Errorf("ambient manager routing: %s", key)
		}
	}
	namespaces := map[string]string{}
	for _, kind := range []string{"pid", "mnt", "net"} {
		observed, err := os.Readlink("/proc/self/ns/" + kind)
		host := isolation.HostNamespaces[kind]
		if err != nil || host == "" || host == observed {
			return nil, fmt.Errorf("private %s namespace is unproved", kind)
		}
		namespaces[kind] = observed
	}
	init, err := os.ReadFile("/proc/1/comm")
	if err != nil || (!strings.Contains(string(init), "docker-init") && !strings.Contains(string(init), "tini")) {
		return nil, errors.New("private PID namespace needs an init reaper")
	}
	for _, path := range []string{"/run/dbus/system_bus_socket", "/var/run/docker.sock", fmt.Sprintf("/run/user/%d/bus", os.Getuid())} {
		if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
			return nil, errors.New("ambient manager socket is exposed")
		}
	}
	return namespaces, nil
}

// ManagedDaemonProof contains only process and route identity, never provider
// payloads, command output, credentials, prompts, or approvals.
type ManagedDaemonProof struct {
	Backend    string            `json:"backend"`
	Version    string            `json:"version"`
	PID        int               `json:"pid"`
	Birth      string            `json:"birth"`
	Executable string            `json:"executable"`
	SHA256     string            `json:"sha256"`
	Socket     string            `json:"socket"`
	Namespaces map[string]string `json:"namespaces"`
}

type ManagedDaemon struct {
	fixture    *Fixture
	isolation  ManagerIsolation
	Proof      ManagedDaemonProof
	socketInfo fs.FileInfo
}

// SelectManagedRelease points only this fixture's current link at an explicit
// read-only release mount. It refuses to switch while a fixture daemon is live.
func (fixture *Fixture) SelectManagedRelease(release string) error {
	if !fixture.ownsState || fixture.managedStarted {
		return errors.New("managed release switch requires an owned stopped fixture")
	}
	release, err := filepath.EvalSymlinks(release)
	if err != nil || !filepath.IsAbs(release) {
		return errors.New("managed release is unavailable")
	}
	mounts, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return errors.New("read-only release mount proof is unavailable")
	}
	readonly := false
	for line := range strings.SplitSeq(string(mounts), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 6 && fields[4] == release {
			for option := range strings.SplitSeq(fields[5], ",") {
				if option == "ro" {
					readonly = true
				}
			}
		}
	}
	if !readonly {
		return errors.New("managed release must be an exact read-only mount")
	}
	raw, err := os.ReadFile(filepath.Join(release, "codex-package.json"))
	if err != nil {
		return errors.New("managed release manifest missing")
	}
	var manifest struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(raw, &manifest) != nil || !codexappserver.IsSafeDiagnosticVersion(manifest.Version) {
		return errors.New("managed release manifest invalid")
	}
	current := filepath.Join(fixture.CodexHome, "packages", "standalone", "current")
	if err := os.MkdirAll(filepath.Dir(current), 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(current); err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return errors.New("private current artifact is not a link")
		}
		if err := os.Remove(current); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Symlink(release, current); err != nil {
		return err
	}
	fixture.realCodex = filepath.Join(current, "bin", "codex")
	fixture.versions.Managed = manifest.Version
	fixture.managed = true
	return nil
}

// StartManagedRecovery uses only the official manager after private namespace
// and stopped pid-backend proofs. Failures retain evidence; there is no signal
// fallback for an unknown or mismatched manager.
func (fixture *Fixture) StartManagedRecovery(ctx context.Context, isolation ManagerIsolation) (*ManagedDaemon, error) {
	namespaces, err := isolation.Verify()
	if err != nil {
		return nil, err
	}
	if !fixture.ownsState || !fixture.managed || fixture.managedStarted {
		return nil, errors.New("managed fixture is not prepared and stopped")
	}
	for _, key := range []string{"HOME", "CODEX_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_RUNTIME_DIR", "TMUX_TMPDIR"} {
		value := filepath.Clean(os.Getenv(key))
		if !strings.HasPrefix(value, fixture.Root+string(filepath.Separator)) {
			return nil, fmt.Errorf("%s is outside the fixture", key)
		}
	}
	status, err := fixture.managedCommand(ctx, "version")
	if err != nil || status.Backend != "pid" || status.Status != "stopped" {
		return nil, errors.New("official manager is ambient, unknown, or already running")
	}
	started, err := fixture.managedCommand(ctx, "start")
	if err != nil {
		return nil, err
	}
	// Do not issue any mutation on a failed identity proof. The namespace owner
	// can retire its entire container without guessing a daemon PID.
	if started.Backend != "pid" || started.Status != "started" || started.PID <= 0 {
		return nil, errors.New("official start did not prove a new pid manager")
	}
	fixture.managedStarted = true
	status, err = fixture.managedCommand(ctx, "version")
	if err != nil {
		return nil, err
	}
	artifactPID, err := fixture.readManagedPID()
	if err != nil || validateManagedIdentity(status, started.PID, artifactPID, fixture.SocketPath) != nil {
		return nil, errors.New("manager PID/artifact/socket mismatch")
	}
	if status.Status != "running" || status.AppServerVersion != fixture.versions.Managed || status.ManagedCodexVersion != fixture.versions.Managed || status.CLIVersion != fixture.versions.Managed {
		return nil, errors.New("manager version provenance mismatch")
	}
	birth, err := managedProcessBirth(started.PID)
	if err != nil {
		return nil, err
	}
	executable, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", started.PID))
	if err != nil {
		return nil, err
	}
	expected, err := filepath.EvalSymlinks(fixture.realCodex)
	if err != nil || executable != expected {
		return nil, errors.New("manager executable does not match selected release")
	}
	digest, err := FileSHA256(fmt.Sprintf("/proc/%d/exe", started.PID))
	if err != nil {
		return nil, err
	}
	expectedDigest, err := FileSHA256(expected)
	if err != nil || expectedDigest != digest {
		return nil, errors.New("manager executable digest mismatch")
	}
	info, err := os.Lstat(fixture.SocketPath)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return nil, errors.New("managed private socket is unavailable")
	}
	fixture.managedPID = started.PID
	return &ManagedDaemon{fixture: fixture, isolation: isolation, socketInfo: info, Proof: ManagedDaemonProof{Backend: "pid", Version: status.AppServerVersion, PID: started.PID, Birth: birth, Executable: executable, SHA256: digest, Socket: fixture.SocketPath, Namespaces: namespaces}}, nil
}

func (daemon *ManagedDaemon) Stop(ctx context.Context) error {
	if daemon == nil || !daemon.fixture.managedStarted {
		return errors.New("managed daemon is not owned")
	}
	if _, err := daemon.isolation.Verify(); err != nil {
		return err
	}
	fixture := daemon.fixture
	status, err := fixture.managedCommand(ctx, "version")
	pid, pidErr := fixture.readManagedPID()
	birth, birthErr := managedProcessBirth(daemon.Proof.PID)
	info, socketErr := os.Lstat(fixture.SocketPath)
	digest, digestErr := FileSHA256(fmt.Sprintf("/proc/%d/exe", daemon.Proof.PID))
	if err != nil || pidErr != nil || birthErr != nil || socketErr != nil || digestErr != nil || status.Backend != "pid" || pid != daemon.Proof.PID || birth != daemon.Proof.Birth || digest != daemon.Proof.SHA256 || status.SocketPath != daemon.Proof.Socket || !os.SameFile(info, daemon.socketInfo) {
		return errors.New("managed stop refused: process birth, artifact, or socket changed")
	}
	stopped, err := fixture.managedCommand(ctx, "stop")
	if err != nil || stopped.Backend != "pid" || stopped.Status != "stopped" {
		return errors.New("official managed stop failed")
	}
	if err := waitForRetirement(ctx, fixture.SocketPath, daemon.Proof.PID); err != nil {
		return err
	}
	fixture.managedPID, fixture.managedStarted = 0, false
	return nil
}

func (fixture *Fixture) managedCommand(ctx context.Context, action string) (daemonVersion, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(commandCtx, fixture.realCodex, "app-server", "daemon", action) // #nosec G204 -- fixture-owned explicit release and fixed manager argv.
	command.Env = isolatedEnvironment(os.Environ(), fixture.CodexHome)
	var output boundedBuffer
	command.Stdout = &output
	command.Stderr = &boundedBuffer{}
	if err := command.Run(); err != nil {
		return daemonVersion{}, fmt.Errorf("official daemon %s failed: %w", action, err)
	}
	var status daemonVersion
	if json.Unmarshal(output.Bytes(), &status) != nil {
		return status, errors.New("official manager returned invalid identity")
	}
	mutation := MutationEndpointLifecycle
	if action == "version" {
		mutation = MutationNone
	}
	fixture.ledger.record(Command{Scope: ScopeIsolated, Operation: "managed-" + action, Mutation: mutation})
	return status, nil
}

func managedProcessBirth(pid int) (string, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	at := strings.LastIndex(string(raw), ") ")
	if at < 0 {
		return "", errors.New("process birth unavailable")
	}
	fields := strings.Fields(string(raw)[at+2:])
	if len(fields) < 20 || fields[0] == "Z" {
		return "", errors.New("process is not live")
	}
	return fields[19], nil
}

func FileSHA256(path string) (string, error) {
	file, err := os.Open(path) // #nosec G304 -- explicit fixture binary or verified process executable.
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// OwnedProcess is a content-free process-birth observation in the private PID
// namespace. It is evidence only and grants no signal authority.
type OwnedProcess struct {
	PID        int    `json:"pid"`
	Birth      string `json:"birth"`
	Executable string `json:"executable"`
}

func (fixture *Fixture) OwnedProcesses() ([]OwnedProcess, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	processes := []OwnedProcess{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "environ"))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		owned := false
		for value := range strings.SplitSeq(string(raw), "\x00") {
			if value == "CODEX_HOME="+fixture.CodexHome {
				owned = true
			}
		}
		if !owned {
			continue
		}
		birth, err := managedProcessBirth(pid)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		executable, err := os.Readlink(filepath.Join("/proc", entry.Name(), "exe"))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		processes = append(processes, OwnedProcess{PID: pid, Birth: birth, Executable: executable})
	}
	return processes, nil
}
