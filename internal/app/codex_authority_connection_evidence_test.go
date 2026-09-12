package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/testutil/codexinstalled"
)

type installedConnectionSelection struct {
	Error              string `json:"error,omitempty"`
	CLIPath            string `json:"cliPath,omitempty"`
	CLIResolved        string `json:"cliResolved,omitempty"`
	CLISHA256          string `json:"cliSHA256,omitempty"`
	SelectedPath       string `json:"selectedPath"`
	SelectedResolved   string `json:"selectedResolved,omitempty"`
	SelectedSHA256     string `json:"selectedSHA256,omitempty"`
	EnvironmentMatches bool   `json:"environmentMatches"`
	MatchesManager     bool   `json:"matchesManager"`
	StateDomainID      string `json:"stateDomainID,omitempty"`
}

// Inspect only the exact fixture shim and selected, manager-proved release.
// An unexpected PATH/environment/link target is refused without reading or
// recording any foreign file or arbitrary environment value.
func observeInstalledConnectionSelection(fixture *codexinstalled.Fixture, manager codexinstalled.ManagedDaemonProof) installedConnectionSelection {
	out := installedConnectionSelection{SelectedPath: filepath.Join(fixture.CodexHome, "packages", "standalone", "current", "bin", "codex")}
	out.EnvironmentMatches = os.Getenv("PROJMUX_CODEX_INSTALLED_REAL") == out.SelectedPath && os.Getenv("CODEX_HOME") == fixture.CodexHome && os.Getenv("PROJMUX_CODEX_INSTALLED_HOME") == fixture.CodexHome
	if !out.EnvironmentMatches {
		out.Error = "fixture-environment-mismatch"
		return out
	}
	expectedShim := filepath.Join(fixture.Root, "fixture-bin", "codex")
	path, err := exec.LookPath("codex")
	if err != nil || path != expectedShim {
		out.Error = "path-cli-not-fixture-shim"
		return out
	}
	out.CLIPath = path
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != expectedShim {
		out.Error = "fixture-shim-target-mismatch"
		return out
	}
	out.CLIResolved = resolved
	out.CLISHA256, err = codexinstalled.FileSHA256(resolved)
	if err != nil {
		out.Error = "fixture-shim-hash-unavailable"
		return out
	}
	resolved, err = filepath.EvalSymlinks(out.SelectedPath)
	if err != nil || resolved != manager.Executable {
		out.Error = "selected-release-mismatch"
		return out
	}
	out.SelectedResolved = resolved
	out.SelectedSHA256, err = codexinstalled.FileSHA256(resolved)
	if err != nil || out.SelectedSHA256 != manager.SHA256 {
		out.Error = "selected-release-hash-mismatch"
		return out
	}
	out.MatchesManager = true
	out.StateDomainID, err = defaultCodexStateDomainID(os.Getenv, os.UserHomeDir)
	if err != nil {
		out.Error = "state-domain-unavailable"
	}
	return out
}

type installedConnectionHealth struct {
	Availability      string `json:"availability"`
	Reason            string `json:"reason"`
	ProbeReason       string `json:"probeReason"`
	EndpointReadiness string `json:"endpointReadiness"`
	Connection        string `json:"connection"`
	ManagerOwnership  string `json:"managerOwnership"`
	RunningExecutable string `json:"runningExecutable"`
	VersionRelation   string `json:"versionRelation"`
	AttachRefusal     string `json:"attachRefusal"`
	CLIVersion        string `json:"cliVersion"`
	ManagedVersion    string `json:"managedVersion"`
	RunningVersion    string `json:"runningVersion"`
	WireVersion       string `json:"wireVersion"`
}

func recoveryEnum[T ~string](value T, allowed ...T) string {
	if slices.Contains(allowed, value) {
		return string(value)
	}
	return "unclassified"
}

func projectInstalledConnectionHealth(health codexappserver.Health) *installedConnectionHealth {
	reason := func(value codexappserver.Reason) string {
		return recoveryEnum(value, codexappserver.ReasonNone, codexappserver.ReasonExecutableMissing, codexappserver.ReasonDaemonNotRunning, codexappserver.ReasonEndpointUnavailable, codexappserver.ReasonUnsupported, codexappserver.ReasonTimeout, codexappserver.ReasonProtocolError, codexappserver.ReasonDisconnected, codexappserver.ReasonHookUnavailable)
	}
	version := func(value string) string {
		if value == "" || codexappserver.IsSafeDiagnosticVersion(value) {
			return value
		}
		return "unclassified"
	}
	return &installedConnectionHealth{
		Availability: recoveryEnum(health.Availability, codexappserver.AvailabilityAvailable, codexappserver.AvailabilityUnsupported, codexappserver.AvailabilityUnavailable, codexappserver.AvailabilityTimeout, codexappserver.AvailabilityProtocolError),
		Reason:       reason(health.Reason), ProbeReason: reason(health.ProbeReason),
		EndpointReadiness: recoveryEnum(health.EndpointReadiness, codexappserver.EndpointReady, codexappserver.EndpointDead, codexappserver.EndpointUnavailable, codexappserver.EndpointTimedOut, codexappserver.EndpointUnsupported, codexappserver.EndpointProtocolError),
		Connection:        recoveryEnum(health.Connection, codexappserver.ConnectionDisconnected, codexappserver.ConnectionConnecting, codexappserver.ConnectionReady, codexappserver.ConnectionTimedOut, codexappserver.ConnectionProtocolErr),
		ManagerOwnership:  recoveryEnum(health.ManagerOwnership, codexappserver.ManagerManaged, codexappserver.ManagerUnmanaged, codexappserver.ManagerUnknown),
		RunningExecutable: recoveryEnum(health.RunningExecutable, codexappserver.RunningExecutableManaged, codexappserver.RunningExecutableUnknown),
		VersionRelation:   recoveryEnum(health.VersionRelation, codexappserver.VersionCurrent, codexappserver.VersionSkew, codexappserver.VersionUnknown),
		AttachRefusal:     recoveryToken(string(codexappserver.AuthorityFor(health).Refusal), "none", "endpoint-not-ready", "protocol-mismatch", "version-skew", "runtime-version-unknown", "ownership-unknown", "connect-failed"),
		CLIVersion:        version(health.CLIVersion), ManagedVersion: version(health.ManagedVersion), RunningVersion: version(health.RunningVersion), WireVersion: version(health.Version),
	}
}

// This is a later independent read, never the health value that Current used.
// Retain the original refusal even if this observation sees a ready endpoint;
// it cannot retry Current, open a connection, or turn a failed row into PASS.
func recordInstalledConnectionFailure(ctx context.Context, original error, probe func(context.Context) codexappserver.Health, record func(installedConnectionStage)) error {
	if original == nil {
		return nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	started := time.Now()
	health := probe(probeCtx)
	record(installedConnectionStage{Stage: "failure-probe-after-attempt", Health: projectInstalledConnectionHealth(health), Error: recoveryError(probeCtx.Err()), DurationMS: time.Since(started).Milliseconds()})
	return original
}
