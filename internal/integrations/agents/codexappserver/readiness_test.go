package codexappserver

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
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
		want         []any
	}{
		{
			name: "managed-current", availability: AvailabilityAvailable, reason: ReasonNone,
			observation: managerObservation{Ownership: ManagerManaged, Executable: RunningExecutableManaged, Relation: VersionCurrent, CLIVersion: "0.150.1", ManagedVersion: "0.150.1", RunningVersion: "0.150.1"},
			remote:      RemoteControlConnected,
			want:        []any{EndpointReady, RunningExecutableManaged, VersionCurrent, ManagerManaged, RemoteControlConnected, NativeActionReady, NativeActionRefusalNone},
		},
		{
			name: "unmanaged-current", availability: AvailabilityAvailable, reason: ReasonNone,
			observation: managerObservation{Ownership: ManagerUnmanaged, Executable: RunningExecutableUnknown, Relation: VersionCurrent, CLIVersion: "0.150.1", ManagedVersion: "0.150.1", RunningVersion: "0.150.1"},
			remote:      RemoteControlDisabled,
			want:        []any{EndpointReady, RunningExecutableUnknown, VersionCurrent, ManagerUnmanaged, RemoteControlDisabled, NativeActionRefused, NativeActionRefusalUnmanaged},
		},
		{
			name: "unmanaged-skew", availability: AvailabilityAvailable, reason: ReasonNone,
			observation: managerObservation{Ownership: ManagerUnmanaged, Executable: RunningExecutableUnknown, Relation: VersionSkew, CLIVersion: "0.150.1", ManagedVersion: "0.150.1", RunningVersion: "0.149.1"},
			remote:      RemoteControlUnsupported,
			want:        []any{EndpointReady, RunningExecutableUnknown, VersionSkew, ManagerUnmanaged, RemoteControlUnsupported, NativeActionRefused, NativeActionRefusalUnmanagedVersionSkew},
		},
		{
			name: "endpoint-dead", availability: AvailabilityUnavailable, reason: ReasonDaemonNotRunning,
			observation: managerObservation{Ownership: ManagerUnknown, Executable: RunningExecutableUnknown, Relation: VersionUnknown},
			remote:      RemoteControlUnavailable,
			want:        []any{EndpointDead, RunningExecutableUnknown, VersionUnknown, ManagerUnknown, RemoteControlUnavailable, NativeActionReady, NativeActionRefusalNone},
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
			got := []any{health.EndpointReadiness, health.RunningExecutable, health.VersionRelation, health.ManagerOwnership, health.RemoteControl, health.NativeAction, health.NativeRefusal}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("axes = %#v, want %#v; health=%+v", got, tt.want, health)
			}
			data, err := json.Marshal(health)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"/home/", "socketPath", "managedCodexPath", "prompt=", "token="} {
				if strings.Contains(string(data), forbidden) {
					t.Fatalf("health leaked %q: %s", forbidden, data)
				}
			}
		})
	}
}

func TestObserveManagerUsesOfficialVersionOutputWithoutRetainingPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("helper process fixture is Unix-oriented")
	}
	tests := []struct {
		name    string
		payload string
		want    managerObservation
	}{
		{name: "managed current", payload: `{"status":"running","backend":"pid","managedCodexPath":"/secret/managed/codex","managedCodexVersion":"0.150.1","socketPath":"/secret/control.sock","cliVersion":"0.150.1","appServerVersion":"0.150.1"}`, want: managerObservation{Ownership: ManagerManaged, Executable: RunningExecutableManaged, Relation: VersionCurrent, CLIVersion: "0.150.1", ManagedVersion: "0.150.1", RunningVersion: "0.150.1"}},
		{name: "unmanaged current exact host shape", payload: `{"status":"running","managedCodexPath":"/secret/managed/codex","managedCodexVersion":"0.150.1","socketPath":"/secret/control.sock","cliVersion":"0.150.1","appServerVersion":"0.150.1"}`, want: managerObservation{Ownership: ManagerUnmanaged, Executable: RunningExecutableUnknown, Relation: VersionCurrent, CLIVersion: "0.150.1", ManagedVersion: "0.150.1", RunningVersion: "0.150.1"}},
		{name: "unmanaged skew", payload: `{"status":"running","managedCodexPath":"/secret/managed/codex","managedCodexVersion":"0.150.1","socketPath":"/secret/control.sock","cliVersion":"0.150.1","appServerVersion":"0.149.1"}`, want: managerObservation{Ownership: ManagerUnmanaged, Executable: RunningExecutableUnknown, Relation: VersionSkew, CLIVersion: "0.150.1", ManagedVersion: "0.150.1", RunningVersion: "0.149.1"}},
		{name: "ambiguous null backend", payload: `{"status":"running","backend":null,"managedCodexVersion":"0.150.1","cliVersion":"0.150.1","appServerVersion":"0.150.1"}`, want: managerObservation{Ownership: ManagerUnknown, Executable: RunningExecutableUnknown, Relation: VersionCurrent, CLIVersion: "0.150.1", ManagedVersion: "0.150.1", RunningVersion: "0.150.1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var argv []string
			got := observeManager(context.Background(), time.Second,
				func(string) (string, error) { return os.Args[0], nil },
				func(commandCtx context.Context, name string, args ...string) *exec.Cmd {
					argv = append([]string{name}, args...)
					cmd := exec.CommandContext(commandCtx, os.Args[0], "-test.run=TestDaemonVersionHelperProcess")
					cmd.Env = append(os.Environ(), "GO_WANT_DAEMON_VERSION_HELPER=1", "DAEMON_VERSION_PAYLOAD="+tt.payload)
					return cmd
				})
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("observation = %+v, want %+v", got, tt.want)
			}
			wantArgv := []string{os.Args[0], "app-server", "daemon", "version"}
			if !reflect.DeepEqual(argv, wantArgv) {
				t.Fatalf("argv = %#v, want %#v", argv, wantArgv)
			}
			if strings.Contains(strings.Join([]string{got.CLIVersion, got.ManagedVersion, got.RunningVersion}, " "), "/secret/") {
				t.Fatalf("observation retained a path: %+v", got)
			}
		})
	}
}

func TestDaemonVersionHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_DAEMON_VERSION_HELPER") != "1" {
		return
	}
	_, _ = os.Stdout.WriteString(os.Getenv("DAEMON_VERSION_PAYLOAD"))
	os.Exit(0)
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
			if health.Lifecycle != LifecycleRefused || health.LifecycleReason != tt.wantReason || health.NativeAction != NativeActionRefused {
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

func TestManagedCurrentNativeActionRemainsAllowed(t *testing.T) {
	var starts atomic.Int32
	policy := testLifecyclePolicy(func(context.Context) Health {
		health := testProbeHealth(AvailabilityAvailable, ReasonNone)
		return withManagerObservation(health, managerObservation{
			Ownership: ManagerManaged, Executable: RunningExecutableManaged, Relation: VersionCurrent,
			CLIVersion: "0.150.1", ManagedVersion: "0.150.1", RunningVersion: "0.150.1",
		})
	}, func(context.Context) startResult {
		starts.Add(1)
		return startSucceeded
	})
	health, err := policy.ensureReadyForHook(context.Background(), TriggerNativeUserAction, true)
	if err != nil {
		t.Fatal(err)
	}
	if health.Lifecycle != LifecycleAlreadyRunning || health.NativeAction != NativeActionReady {
		t.Fatalf("health = %+v", health)
	}
	if starts.Load() != 0 {
		t.Fatalf("daemon start calls = %d, want 0", starts.Load())
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
	if health.Lifecycle != LifecycleRefused || health.NativeRefusal != NativeActionRefusalUnmanaged {
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
