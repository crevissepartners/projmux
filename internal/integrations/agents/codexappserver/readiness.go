package codexappserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"time"
	"unicode/utf8"
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
	Evidence       *ManagerEvidence
	Ownership      ManagerOwnership
	Executable     RunningExecutable
	Relation       VersionRelation
	CLIVersion     string
	ManagedVersion string
	RunningVersion string
}

func observeDefaultManager(ctx context.Context, timeout time.Duration) managerObservation {
	return observeManager(ctx, timeout, exec.LookPath, defaultDaemonVersionCommand)
}

func defaultDaemonVersionCommand(ctx context.Context, path string, _ ...string) *exec.Cmd {
	// #nosec G204 -- path comes only from exec.LookPath("codex") and argv is hard-coded here to the read-only daemon version command.
	return exec.CommandContext(ctx, path, "app-server", "daemon", "version")
}

func observeManager(ctx context.Context, timeout time.Duration, lookPath func(string) (string, error), command func(context.Context, string, ...string) *exec.Cmd) managerObservation {
	unknown := managerObservation{Ownership: ManagerUnknown, Executable: RunningExecutableUnknown, Relation: VersionUnknown, Evidence: &ManagerEvidence{Status: "unknown", Backend: "unknown", Result: "unavailable", Agreement: "insufficient"}}
	path, err := lookPath("codex")
	if err != nil {
		unknown.Evidence.Result = "executable-missing"
		return unknown
	}
	probeCtx, cancel := context.WithTimeout(ctx, positiveDuration(timeout, DefaultProbeTimeout))
	defer cancel()
	cmd := command(probeCtx, path, "app-server", "daemon", "version")
	var stdout boundedReadOnlyCapture
	stdout.remaining = maxDaemonVersionBytes
	cmd.Stdout = &stdout
	cmd.Stderr = discardWriter{}
	err = cmd.Run()
	switch {
	case errors.Is(probeCtx.Err(), context.Canceled):
		unknown.Evidence.Result = "cancelled"
		return unknown
	case probeCtx.Err() != nil:
		unknown.Evidence.Result = "timeout"
		return unknown
	case stdout.truncated:
		unknown.Evidence.Result = "truncated"
		return unknown
	case err != nil:
		unknown.Evidence.Result = "command-failed"
		return unknown
	}
	unknown.Evidence.Result = "malformed"
	if !utf8.Valid(stdout.Bytes()) {
		return unknown
	}
	var raw daemonVersionOutput
	// Upstream may add path/pid fields at any time, so decode once through a
	// bounded map and retain only the known scalar fields instead of allowing
	// arbitrary provider data into Health.
	fields, valid := daemonVersionFields(stdout.Bytes())
	if !valid {
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
	switch raw.Status {
	case "running":
		unknown.Evidence.Status = "running"
	case "stopped", "notRunning":
		unknown.Evidence.Status = "not-running"
	default:
		unknown.Evidence.Result = "status-unknown"
		return unknown
	}
	unknown.Evidence.Result = "observed"
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
		observation.Evidence.Backend = "pid"
		observation.Ownership = ManagerManaged
		observation.Executable = RunningExecutableManaged
	case !backendPresent:
		// daemon version found a ready endpoint but no running daemon backend.
		// This is upstream's direct ownership result, not a process guess.
		observation.Evidence.Backend = "absent"
		observation.Ownership = ManagerUnmanaged
	default:
		observation.Ownership = ManagerUnknown
	}
	observation.CLIVersion = strictEvidenceVersion(raw.CLIVersion)
	observation.ManagedVersion = strictEvidenceVersion(raw.ManagedCodexVersion)
	observation.RunningVersion = strictEvidenceVersion(raw.AppServerVersion)
	observation.Evidence.Version = observation.RunningVersion
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
	health.ManagerEvidence = observation.Evidence
	health.ManagerOwnership = observation.Ownership
	health.RunningExecutable = observation.Executable
	health.VersionRelation = observation.Relation
	health.CLIVersion = observation.CLIVersion
	health.ManagedVersion = observation.ManagedVersion
	health.RunningVersion = observation.RunningVersion
	if health.RunningVersion == "" {
		health.RunningVersion = safeVersion(health.Version)
	}
	if observation.Evidence != nil {
		evidence := *observation.Evidence
		health.ManagerEvidence = &evidence
		attach := strictEvidenceVersion(health.Version)
		evidence.Agreement = "insufficient"
		if evidence.Status == "running" && health.EndpointReadiness == EndpointReady && attach != "" && evidence.Version != "" {
			evidence.Agreement = "consistent"
			if attach != evidence.Version {
				evidence.Agreement = "contradictory"
			}
		}
		if evidence.Status == "not-running" && health.EndpointReadiness == EndpointReady || evidence.Status == "running" && health.EndpointReadiness == EndpointDead {
			evidence.Agreement = "contradictory"
		}
		// RunningVersion describes the attached endpoint. The manager's independent
		// claim stays in evidence.Version and may not replace a failed attach probe.
		health.RunningVersion = attach
		if evidence.Agreement == "contradictory" {
			health.ManagerOwnership = ManagerUnknown
			health.RunningExecutable = RunningExecutableUnknown
			health.VersionRelation = VersionUnknown
		} else if evidence.Agreement != "consistent" && health.EndpointReadiness == EndpointReady {
			health.ManagerOwnership = ManagerUnknown
			health.RunningExecutable = RunningExecutableUnknown
		}
	}
	return withNativeActionReadiness(health)
}

func withNativeActionReadiness(health Health) Health {
	health.NativeAction = NativeActionUnknown
	health.NativeRefusal = NativeActionRefusalNone
	health.InterruptionRisk = InterruptionRiskNone
	health.OperatorRecovery = OperatorRecoveryNone
	if health.ManagerEvidence != nil && health.ManagerEvidence.Agreement == "contradictory" {
		health.NativeAction = NativeActionRefused
		health.NativeRefusal = NativeActionRefusalEvidenceContradictory
		health.InterruptionRisk = InterruptionRiskSharedClients
		health.OperatorRecovery = OperatorRecoveryInspectProcessOwnership
		return health
	}
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

// Duplicate keys cannot be reconciled into one ownership observation. Keep
// unknown upstream fields compatible, but refuse ambiguous/trailing objects.
func daemonVersionFields(data []byte) (map[string]json.RawMessage, bool) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, false
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, false
		}
		name, ok := key.(string)
		if !ok {
			return nil, false
		}
		if _, duplicate := fields[name]; duplicate {
			return nil, false
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, false
		}
		fields[name] = value
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return nil, false
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, false
	}
	return fields, true
}
