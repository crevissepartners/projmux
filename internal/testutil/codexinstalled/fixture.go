package codexinstalled

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
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
