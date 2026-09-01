package codexappserver

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestReadinessFourAxisFixtureMatrix(t *testing.T) {
	tests := []struct {
		name         string
		availability Availability
		reason       Reason
		observation  managerObservation
		remote       RemoteControlCapability
		readiness    EndpointReadiness
		executable   RunningExecutable
		relation     VersionRelation
		ownership    ManagerOwnership
		nativeAction NativeActionReadiness
		refusal      NativeActionRefusal
	}{
		{
			name: "managed-current", availability: AvailabilityAvailable, reason: ReasonNone,
			observation: managerObservation{Ownership: ManagerManaged, Executable: RunningExecutableManaged, Relation: VersionCurrent, CLIVersion: "0.150.1", ManagedVersion: "0.150.1", RunningVersion: "0.150.1"},
			remote:      RemoteControlConnected, readiness: EndpointReady, executable: RunningExecutableManaged,
			relation: VersionCurrent, ownership: ManagerManaged, nativeAction: NativeActionReady, refusal: NativeActionRefusalNone,
		},
		{
			name: "unmanaged-current", availability: AvailabilityAvailable, reason: ReasonNone,
			observation: managerObservation{Ownership: ManagerUnmanaged, Executable: RunningExecutableUnknown, Relation: VersionCurrent, CLIVersion: "0.150.1", ManagedVersion: "0.150.1", RunningVersion: "0.150.1"},
			remote:      RemoteControlDisabled, readiness: EndpointReady, executable: RunningExecutableUnknown,
			relation: VersionCurrent, ownership: ManagerUnmanaged, nativeAction: NativeActionRefused, refusal: NativeActionRefusalUnmanaged,
		},
		{
			name: "unmanaged-skew", availability: AvailabilityAvailable, reason: ReasonNone,
			observation: managerObservation{Ownership: ManagerUnmanaged, Executable: RunningExecutableUnknown, Relation: VersionSkew, CLIVersion: "0.150.1", ManagedVersion: "0.150.1", RunningVersion: "0.149.1"},
			remote:      RemoteControlUnsupported, readiness: EndpointReady, executable: RunningExecutableUnknown,
			relation: VersionSkew, ownership: ManagerUnmanaged, nativeAction: NativeActionRefused, refusal: NativeActionRefusalUnmanagedVersionSkew,
		},
		{
			name: "endpoint-dead", availability: AvailabilityUnavailable, reason: ReasonDaemonNotRunning,
			observation: managerObservation{Ownership: ManagerUnknown, Executable: RunningExecutableUnknown, Relation: VersionUnknown},
			remote:      RemoteControlUnavailable, readiness: EndpointDead, executable: RunningExecutableUnknown,
			relation: VersionUnknown, ownership: ManagerUnknown, nativeAction: NativeActionReady, refusal: NativeActionRefusalNone,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			connection := ConnectionDisconnected
			if tt.availability == AvailabilityAvailable {
				connection = ConnectionReady
			}
			health := Decide(tt.availability, tt.reason, tt.observation.RunningVersion, EndpointStdioProxy, connection, true)
			health.RemoteControl = tt.remote
			health = withManagerObservation(health, tt.observation)
			if health.EndpointReadiness != tt.readiness || health.RunningExecutable != tt.executable ||
				health.VersionRelation != tt.relation || health.ManagerOwnership != tt.ownership ||
				health.RemoteControl != tt.remote || health.NativeAction != tt.nativeAction || health.NativeRefusal != tt.refusal {
				t.Fatalf("readiness axes = endpoint %s executable %s version %s manager %s remote %s native %s/%s; want %s %s %s %s %s %s/%s",
					health.EndpointReadiness, health.RunningExecutable, health.VersionRelation, health.ManagerOwnership,
					health.RemoteControl, health.NativeAction, health.NativeRefusal,
					tt.readiness, tt.executable, tt.relation, tt.ownership, tt.remote, tt.nativeAction, tt.refusal)
			}
		})
	}
}

const (
	daemonVersionFixtureReadinessTimeout   = 5 * time.Second
	daemonVersionFixtureTerminationTimeout = 5 * time.Second
	daemonVersionFixtureReadySignal        = "daemon-version-fixture-ready"
)

type daemonVersionFixture struct {
	executable string
	command    *exec.Cmd
	stdin      io.WriteCloser
	exited     <-chan error
	stderr     *bytes.Buffer
}

func startDaemonVersionFixture(root, payload string, neverReady bool, readinessTimeout time.Duration) (*daemonVersionFixture, error) {
	scriptPath := filepath.Join(root, "codex")
	command := exec.Command(os.Args[0], "-test.run=^TestDaemonVersionFixtureProcess$")
	command.Env = append(os.Environ(),
		"GO_WANT_DAEMON_VERSION_FIXTURE=1",
		"DAEMON_VERSION_FIXTURE_ROOT="+root,
		"DAEMON_VERSION_FIXTURE_PAYLOAD="+payload,
		fmt.Sprintf("DAEMON_VERSION_FIXTURE_NEVER_READY=%t", neverReady),
	)
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("daemon-version fixture stdin: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("daemon-version fixture stdout: %w", err)
	}
	stderr := &bytes.Buffer{}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start daemon-version fixture: %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	ready := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				ready <- fmt.Errorf("read readiness signal: %w", err)
				return
			}
			ready <- fmt.Errorf("readiness stream closed before %q", daemonVersionFixtureReadySignal)
			return
		}
		if line := scanner.Text(); line != daemonVersionFixtureReadySignal {
			ready <- fmt.Errorf("readiness signal = %q, want %q", line, daemonVersionFixtureReadySignal)
			return
		}
		ready <- nil
	}()

	timer := time.NewTimer(readinessTimeout)
	defer timer.Stop()
	select {
	case err := <-ready:
		if err == nil {
			return &daemonVersionFixture{executable: scriptPath, command: command, stdin: stdin, exited: exited, stderr: stderr}, nil
		}
		cleanupErr := abortDaemonVersionFixture(command, stdin, exited)
		return nil, fmt.Errorf("daemon-version fixture readiness failed: %w; cleanup=%v; stderr=%q", err, cleanupErr, daemonVersionFixtureStderr(stderr, cleanupErr))
	case err := <-exited:
		return nil, fmt.Errorf("daemon-version fixture terminated before readiness: %v; stderr=%q", err, strings.TrimSpace(stderr.String()))
	case <-timer.C:
		cleanupErr := abortDaemonVersionFixture(command, stdin, exited)
		return nil, fmt.Errorf("daemon-version fixture readiness deadline exceeded after %s; cleanup=%v; stderr=%q", readinessTimeout, cleanupErr, daemonVersionFixtureStderr(stderr, cleanupErr))
	}
}

func daemonVersionFixtureStderr(stderr *bytes.Buffer, cleanupErr error) string {
	if cleanupErr != nil {
		return "unavailable before process termination"
	}
	return strings.TrimSpace(stderr.String())
}

func abortDaemonVersionFixture(command *exec.Cmd, stdin io.WriteCloser, exited <-chan error) error {
	_ = stdin.Close()
	if command.Process != nil {
		_ = command.Process.Kill()
	}
	timer := time.NewTimer(daemonVersionFixtureTerminationTimeout)
	defer timer.Stop()
	select {
	case <-exited:
		return nil
	case <-timer.C:
		return fmt.Errorf("termination deadline exceeded after %s", daemonVersionFixtureTerminationTimeout)
	}
}

func (fixture *daemonVersionFixture) terminate(timeout time.Duration) error {
	if err := fixture.stdin.Close(); err != nil {
		cleanupErr := abortDaemonVersionFixture(fixture.command, fixture.stdin, fixture.exited)
		return fmt.Errorf("signal daemon-version fixture termination: %w; cleanup=%v", err, cleanupErr)
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-fixture.exited:
		if err != nil {
			return fmt.Errorf("daemon-version fixture terminated with error: %w; stderr=%q", err, strings.TrimSpace(fixture.stderr.String()))
		}
		return nil
	case <-timer.C:
		cleanupErr := abortDaemonVersionFixture(fixture.command, fixture.stdin, fixture.exited)
		return fmt.Errorf("daemon-version fixture termination deadline exceeded after %s; cleanup=%v", timeout, cleanupErr)
	}
}

type managerObservationExpectation struct {
	ownership      ManagerOwnership
	executable     RunningExecutable
	relation       VersionRelation
	cliVersion     string
	managedVersion string
	runningVersion string
}

func requireManagerObservation(t *testing.T, got managerObservation, want managerObservationExpectation) {
	t.Helper()
	if got.Ownership != want.ownership {
		t.Errorf("manager ownership = %s, want %s", got.Ownership, want.ownership)
	}
	if got.Executable != want.executable {
		t.Errorf("running executable = %s, want %s", got.Executable, want.executable)
	}
	if got.Relation != want.relation {
		t.Errorf("version relation = %s, want %s", got.Relation, want.relation)
	}
	if got.CLIVersion != want.cliVersion {
		t.Errorf("CLI version = %q, want %q", got.CLIVersion, want.cliVersion)
	}
	if got.ManagedVersion != want.managedVersion {
		t.Errorf("managed version = %q, want %q", got.ManagedVersion, want.managedVersion)
	}
	if got.RunningVersion != want.runningVersion {
		t.Errorf("running version = %q, want %q", got.RunningVersion, want.runningVersion)
	}
}

func TestOfficialDaemonVersionObservationUsesReadyTerminatedFixture(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("helper process fixture is Unix-oriented")
	}
	tests := []struct {
		name    string
		payload string
		want    managerObservationExpectation
	}{
		{name: "managed current", payload: `{"status":"running","backend":"pid","managedCodexPath":"/secret/managed/codex","managedCodexVersion":"0.150.1","socketPath":"/secret/control.sock","cliVersion":"0.150.1","appServerVersion":"0.150.1"}`, want: managerObservationExpectation{ownership: ManagerManaged, executable: RunningExecutableManaged, relation: VersionCurrent, cliVersion: "0.150.1", managedVersion: "0.150.1", runningVersion: "0.150.1"}},
		{name: "unmanaged current exact host shape", payload: `{"status":"running","managedCodexPath":"/secret/managed/codex","managedCodexVersion":"0.150.1","socketPath":"/secret/control.sock","cliVersion":"0.150.1","appServerVersion":"0.150.1"}`, want: managerObservationExpectation{ownership: ManagerUnmanaged, executable: RunningExecutableUnknown, relation: VersionCurrent, cliVersion: "0.150.1", managedVersion: "0.150.1", runningVersion: "0.150.1"}},
		{name: "unmanaged skew", payload: `{"status":"running","managedCodexPath":"/secret/managed/codex","managedCodexVersion":"0.150.1","socketPath":"/secret/control.sock","cliVersion":"0.150.1","appServerVersion":"0.149.1"}`, want: managerObservationExpectation{ownership: ManagerUnmanaged, executable: RunningExecutableUnknown, relation: VersionSkew, cliVersion: "0.150.1", managedVersion: "0.150.1", runningVersion: "0.149.1"}},
		{name: "ambiguous null backend", payload: `{"status":"running","backend":null,"managedCodexVersion":"0.150.1","cliVersion":"0.150.1","appServerVersion":"0.150.1"}`, want: managerObservationExpectation{ownership: ManagerUnknown, executable: RunningExecutableUnknown, relation: VersionCurrent, cliVersion: "0.150.1", managedVersion: "0.150.1", runningVersion: "0.150.1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture, err := startDaemonVersionFixture(t.TempDir(), tt.payload, false, daemonVersionFixtureReadinessTimeout)
			if err != nil {
				t.Fatal(err)
			}
			var argv []string
			got := observeManager(context.Background(), time.Second,
				func(string) (string, error) { return fixture.executable, nil },
				func(commandCtx context.Context, name string, args ...string) *exec.Cmd {
					argv = append([]string{name}, args...)
					return exec.CommandContext(commandCtx, name, args...)
				})
			if err := fixture.terminate(daemonVersionFixtureTerminationTimeout); err != nil {
				t.Fatal(err)
			}

			requireManagerObservation(t, got, tt.want)
			wantArgv := []string{fixture.executable, "app-server", "daemon", "version"}
			if !slices.Equal(argv, wantArgv) {
				t.Fatalf("argv = %#v, want %#v", argv, wantArgv)
			}
			for _, field := range []struct {
				name  string
				value string
			}{
				{name: "cliVersion", value: got.CLIVersion},
				{name: "managedVersion", value: got.ManagedVersion},
				{name: "runningVersion", value: got.RunningVersion},
			} {
				if strings.Contains(field.value, "/secret/") {
					t.Fatalf("observation.%s retained a path: %q", field.name, field.value)
				}
			}
		})
	}
}

func TestDaemonVersionFixtureNeverReadyFailsWithBoundedDiagnostic(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("helper process fixture is Unix-oriented")
	}
	fixture, err := startDaemonVersionFixture(t.TempDir(), `{}`, true, 200*time.Millisecond)
	if fixture != nil {
		_ = fixture.terminate(daemonVersionFixtureTerminationTimeout)
		t.Fatal("never-ready fixture unexpectedly became ready")
	}
	if err == nil || !strings.Contains(err.Error(), "readiness deadline exceeded after 200ms") || !strings.Contains(err.Error(), "cleanup=<nil>") {
		t.Fatalf("never-ready diagnostic = %v, want bounded readiness deadline and completed cleanup", err)
	}
}

func TestDaemonVersionFixtureProcess(t *testing.T) {
	if os.Getenv("GO_WANT_DAEMON_VERSION_FIXTURE") != "1" {
		return
	}
	if os.Getenv("DAEMON_VERSION_FIXTURE_NEVER_READY") == "true" {
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}
	root := os.Getenv("DAEMON_VERSION_FIXTURE_ROOT")
	payloadPath := filepath.Join(root, "codex.payload")
	scriptPath := filepath.Join(root, "codex")
	if err := os.WriteFile(payloadPath, []byte(os.Getenv("DAEMON_VERSION_FIXTURE_PAYLOAD")), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"if [ \"$#\" -ne 3 ] || [ \"$1 $2 $3\" != \"app-server daemon version\" ]; then\n" +
		"  printf '%s\\n' 'unexpected daemon-version argv' >&2\n" +
		"  exit 64\n" +
		"fi\n" +
		"exec /bin/cat \"$0.payload\"\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(os.Stdout, daemonVersionFixtureReadySignal); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func TestUnsafeReadyNativeActionRefusesWithoutDaemonMutation(t *testing.T) {
	tests := []struct {
		name       string
		ownership  ManagerOwnership
		relation   VersionRelation
		wantReason LifecycleReason
	}{
		{name: "unmanaged current", ownership: ManagerUnmanaged, relation: VersionCurrent, wantReason: LifecycleReasonUnsafeUnmanaged},
		{name: "unmanaged skew", ownership: ManagerUnmanaged, relation: VersionSkew, wantReason: LifecycleReasonUnsafeUnmanaged},
		{name: "managed skew", ownership: ManagerManaged, relation: VersionSkew, wantReason: LifecycleReasonUnsafeVersionSkew},
		{name: "ownership unknown", ownership: ManagerUnknown, relation: VersionCurrent, wantReason: LifecycleReasonUnsafeOwnershipUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var starts atomic.Int32
			policy := testLifecyclePolicy(func(context.Context) Health {
				health := testProbeHealth(AvailabilityAvailable, ReasonNone)
				return withManagerObservation(health, managerObservation{Ownership: tt.ownership, Executable: RunningExecutableUnknown, Relation: tt.relation, CLIVersion: "0.150.1", RunningVersion: map[bool]string{true: "0.149.1", false: "0.150.1"}[tt.relation == VersionSkew]})
			}, func(context.Context) startResult {
				starts.Add(1)
				return startSucceeded
			})
			health, err := policy.ensureReadyForHook(context.Background(), TriggerNativeUserAction, true)
			if err != nil {
				t.Fatal(err)
			}
			if health.Lifecycle != LifecycleRefused || health.LifecycleReason != tt.wantReason {
				t.Fatalf("health = %+v", health)
			}
			if health.InterruptionRisk != InterruptionRiskSharedClients || health.OperatorRecovery.Guidance() == "" {
				t.Fatalf("refusal lacks interruption/recovery guidance: %+v", health)
			}
			if starts.Load() != 0 {
				t.Fatalf("daemon mutation calls = %d, want 0", starts.Load())
			}
		})
	}
}

func TestColdFlightRefusesIfEndpointBecomesUnmanagedBeforeStart(t *testing.T) {
	var probes atomic.Int32
	var starts atomic.Int32
	policy := testLifecyclePolicy(func(context.Context) Health {
		if probes.Add(1) == 1 {
			health := testProbeHealth(AvailabilityUnavailable, ReasonDaemonNotRunning)
			health.EndpointReadiness = EndpointDead
			return withNativeActionReadiness(health)
		}
		health := testProbeHealth(AvailabilityAvailable, ReasonNone)
		return withManagerObservation(health, managerObservation{
			Ownership: ManagerUnmanaged, Executable: RunningExecutableUnknown, Relation: VersionCurrent,
			CLIVersion: "0.150.1", RunningVersion: "0.150.1",
		})
	}, func(context.Context) startResult {
		starts.Add(1)
		return startSucceeded
	})
	health, err := policy.ensureReadyForHook(context.Background(), TriggerNativeUserAction, true)
	if err != nil {
		t.Fatal(err)
	}
	if health.Lifecycle != LifecycleRefused || health.LifecycleReason != LifecycleReasonUnsafeUnmanaged {
		t.Fatalf("health = %+v", health)
	}
	if starts.Load() != 0 {
		t.Fatalf("daemon start calls = %d, want 0", starts.Load())
	}
}

func TestProductSourceHasNoSharedDaemonStopRestartOrKillArgv(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source")
	}
	root := filepath.Dir(filename)
	entries, err := filepath.Glob(filepath.Join(root, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range entries {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{`"app-server", "daemon", "stop"`, `"app-server", "daemon", "restart"`, `"app-server", "daemon", "kill"`, `"app-server", "daemon", "enable-remote-control"`, `"app-server", "daemon", "disable-remote-control"`} {
			if strings.Contains(string(data), forbidden) {
				t.Fatalf("%s contains forbidden shared-daemon mutation argv %s", path, forbidden)
			}
		}
	}
}
