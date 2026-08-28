package codexappserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"time"
)

const maxDaemonVersionBytes = 32 * 1024

type daemonVersionOutput struct {
	Status              string `json:"status"`
	Backend             string `json:"backend"`
	ManagedCodexVersion string `json:"managedCodexVersion"`
	CLIVersion          string `json:"cliVersion"`
	AppServerVersion    string `json:"appServerVersion"`
	// managedCodexPath, socketPath, pid, and all unknown fields are
	// intentionally never decoded into the diagnostic domain.
}

type managerObservation struct {
	Ownership      ManagerOwnership
	Executable     RunningExecutable
	Relation       VersionRelation
	CLIVersion     string
	ManagedVersion string
	RunningVersion string
}

func observeDefaultManager(ctx context.Context, timeout time.Duration) managerObservation {
	return observeManager(ctx, timeout, exec.LookPath, func(commandCtx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(commandCtx, name, args...)
	})
}

func observeManager(ctx context.Context, timeout time.Duration, lookPath func(string) (string, error), command func(context.Context, string, ...string) *exec.Cmd) managerObservation {
	unknown := managerObservation{Ownership: ManagerUnknown, Executable: RunningExecutableUnknown, Relation: VersionUnknown}
	path, err := lookPath("codex")
	if err != nil {
		return unknown
	}
	probeCtx, cancel := context.WithTimeout(ctx, positiveDuration(timeout, DefaultProbeTimeout))
	defer cancel()
	cmd := command(probeCtx, path, "app-server", "daemon", "version")
	var stdout boundedReadOnlyCapture
	stdout.remaining = maxDaemonVersionBytes
	cmd.Stdout = &stdout
	cmd.Stderr = discardWriter{}
	if err := cmd.Run(); err != nil || stdout.truncated || probeCtx.Err() != nil {
		return unknown
	}
	var raw daemonVersionOutput
	// Upstream may add path/pid fields at any time, so decode once through a
	// bounded map and retain only the known scalar fields instead of allowing
	// arbitrary provider data into Health.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &fields); err != nil {
		return unknown
	}
	for key, target := range map[string]any{
		"status":              &raw.Status,
		"managedCodexVersion": &raw.ManagedCodexVersion,
		"cliVersion":          &raw.CLIVersion,
		"appServerVersion":    &raw.AppServerVersion,
	} {
		if value := fields[key]; len(value) > 0 && json.Unmarshal(value, target) != nil {
			return unknown
		}
	}
	if raw.Status != "running" {
		return unknown
	}
	backendValue, backendPresent := fields["backend"]
	backendValid := false
	if backendPresent {
		backendValid = len(backendValue) > 0 && json.Unmarshal(backendValue, &raw.Backend) == nil && raw.Backend != ""
	}
	observation := unknown
	switch {
	case backendPresent && backendValid && raw.Backend == "pid":
		observation.Ownership = ManagerManaged
		observation.Executable = RunningExecutableManaged
	case !backendPresent:
		// daemon version found a ready endpoint but no running daemon backend.
		// This is upstream's direct ownership result, not a process guess.
		observation.Ownership = ManagerUnmanaged
	default:
		observation.Ownership = ManagerUnknown
	}
	observation.CLIVersion = safeVersion(raw.CLIVersion)
	observation.ManagedVersion = safeVersion(raw.ManagedCodexVersion)
	observation.RunningVersion = safeVersion(raw.AppServerVersion)
	if observation.CLIVersion != "" && observation.RunningVersion != "" {
		if observation.CLIVersion == observation.RunningVersion {
			observation.Relation = VersionCurrent
		} else {
			observation.Relation = VersionSkew
		}
	}
	return observation
}

type boundedReadOnlyCapture struct {
	buffer    bytes.Buffer
	remaining int
	truncated bool
}

func (w *boundedReadOnlyCapture) Write(p []byte) (int, error) {
	original := len(p)
	if len(p) > w.remaining {
		w.truncated = true
	}
	keep := min(len(p), w.remaining)
	if keep > 0 {
		_, _ = w.buffer.Write(p[:keep])
		w.remaining -= keep
	}
	return original, nil
}

func (w *boundedReadOnlyCapture) Bytes() []byte { return w.buffer.Bytes() }

func withManagerObservation(health Health, observation managerObservation) Health {
	health.ManagerOwnership = observation.Ownership
	health.RunningExecutable = observation.Executable
	health.VersionRelation = observation.Relation
	health.CLIVersion = observation.CLIVersion
	health.ManagedVersion = observation.ManagedVersion
	health.RunningVersion = observation.RunningVersion
	if health.RunningVersion == "" {
		health.RunningVersion = safeVersion(health.Version)
	}
	return withNativeActionReadiness(health)
}

func withNativeActionReadiness(health Health) Health {
	health.NativeAction = NativeActionUnknown
	health.NativeRefusal = NativeActionRefusalNone
	health.InterruptionRisk = InterruptionRiskNone
	health.OperatorRecovery = OperatorRecoveryNone
	if health.EndpointReadiness == EndpointDead {
		// The existing exact cold-start contract is safe because there is no
		// running shared endpoint to interrupt.
		health.NativeAction = NativeActionReady
		return health
	}
	if health.EndpointReadiness != EndpointReady {
		return health
	}
	switch {
	case health.ManagerOwnership == ManagerUnmanaged && health.VersionRelation == VersionSkew:
		health.NativeRefusal = NativeActionRefusalUnmanagedVersionSkew
		health.OperatorRecovery = OperatorRecoveryStopOwnerThenStart
	case health.ManagerOwnership == ManagerUnmanaged:
		health.NativeRefusal = NativeActionRefusalUnmanaged
		health.OperatorRecovery = OperatorRecoveryStopOwnerThenStart
	case health.ManagerOwnership == ManagerUnknown:
		health.NativeRefusal = NativeActionRefusalOwnershipUnknown
		health.OperatorRecovery = OperatorRecoveryInspectProcessOwnership
	case health.VersionRelation == VersionSkew:
		health.NativeRefusal = NativeActionRefusalVersionSkew
		health.OperatorRecovery = OperatorRecoveryRestartManagedDaemon
	case health.VersionRelation == VersionUnknown:
		health.NativeRefusal = NativeActionRefusalRuntimeVersionUnknown
		health.OperatorRecovery = OperatorRecoveryInspectProcessOwnership
	default:
		health.NativeAction = NativeActionReady
		return health
	}
	health.NativeAction = NativeActionRefused
	health.InterruptionRisk = InterruptionRiskSharedClients
	return health
}

func remoteControlCapability(client *Client, ctx context.Context) RemoteControlCapability {
	var result remoteControlStatusReadResult
	if err := client.Request(ctx, methodRemoteControlStatusRead, nil, &result); err != nil {
		if errors.Is(err, ErrUnsupported) {
			return RemoteControlUnsupported
		}
		return RemoteControlUnknown
	}
	switch result.Status {
	case "disabled":
		return RemoteControlDisabled
	case "connecting":
		return RemoteControlConnecting
	case "connected":
		return RemoteControlConnected
	case "errored":
		return RemoteControlErrored
	default:
		return RemoteControlUnknown
	}
}
