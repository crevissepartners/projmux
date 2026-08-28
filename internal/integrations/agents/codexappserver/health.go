// Package codexappserver owns the Codex app-server wire protocol and its
// compatibility decision. Wire values never leave this vertical slice;
// consumers receive only the bounded, content-free Health projection.
package codexappserver

import (
	"fmt"
	"regexp"
	"strings"
)

// Source is the effective Codex control-plane source selected by capability.
type Source string

const (
	SourceAppServer    Source = "app-server"
	SourceHookFallback Source = "hook-fallback"
	SourceUnavailable  Source = "unavailable"
)

// Label returns the stable user-facing source label.
func (s Source) Label() string {
	switch s {
	case SourceAppServer:
		return "App Server"
	case SourceHookFallback:
		return "Hook fallback"
	default:
		return "Unavailable"
	}
}

// Availability is the closed result of the app-server capability probe.
type Availability string

const (
	AvailabilityAvailable     Availability = "available"
	AvailabilityUnsupported   Availability = "unsupported"
	AvailabilityUnavailable   Availability = "unavailable"
	AvailabilityTimeout       Availability = "timeout"
	AvailabilityProtocolError Availability = "protocol-error"
)

// Reason is a secret-free, closed diagnostic code. It intentionally cannot
// carry command output, tokens, prompts, or endpoint paths.
type Reason string

const (
	ReasonNone                Reason = "none"
	ReasonExecutableMissing   Reason = "executable-missing"
	ReasonDaemonNotRunning    Reason = "daemon-not-running"
	ReasonEndpointUnavailable Reason = "endpoint-unavailable"
	ReasonUnsupported         Reason = "unsupported"
	ReasonTimeout             Reason = "timeout"
	ReasonProtocolError       Reason = "protocol-error"
	ReasonDisconnected        Reason = "disconnected"
	ReasonHookUnavailable     Reason = "hook-unavailable"
)

// InstallCapability is the bounded relationship between the Codex executable
// on PATH and the canonical managed standalone payload used by daemon start.
// It identifies no package manager and carries no filesystem path.
type InstallCapability string

const (
	InstallCapabilityManagedReady    InstallCapability = "managed-ready"
	InstallCapabilityExternalCLIOnly InstallCapability = "external-cli-only"
	InstallCapabilityCLIMissing      InstallCapability = "cli-missing"
	InstallCapabilityUnknown         InstallCapability = "unknown"
)

// EndpointKind describes transport shape without exposing a socket path.
type EndpointKind string

const (
	EndpointStdio      EndpointKind = "stdio"
	EndpointStdioProxy EndpointKind = "stdio-proxy"
)

// ConnectionState is the bounded lifecycle state exposed to diagnostics.
type ConnectionState string

const (
	ConnectionDisconnected ConnectionState = "disconnected"
	ConnectionConnecting   ConnectionState = "connecting"
	ConnectionReady        ConnectionState = "ready"
	ConnectionTimedOut     ConnectionState = "timed-out"
	ConnectionProtocolErr  ConnectionState = "protocol-error"
)

// EndpointReadiness is the endpoint axis. It deliberately says nothing about
// the executable serving the endpoint or who owns that process.
type EndpointReadiness string

const (
	EndpointReady         EndpointReadiness = "ready"
	EndpointDead          EndpointReadiness = "dead"
	EndpointUnavailable   EndpointReadiness = "unavailable"
	EndpointTimedOut      EndpointReadiness = "timed-out"
	EndpointUnsupported   EndpointReadiness = "unsupported"
	EndpointProtocolError EndpointReadiness = "protocol-error"
)

// RunningExecutable is the content-free identity of the executable serving
// the endpoint. Unknown is retained instead of projecting an unmanaged process
// path or guessing its origin.
type RunningExecutable string

const (
	RunningExecutableManaged RunningExecutable = "managed"
	RunningExecutableUnknown RunningExecutable = "unknown"
)

// VersionRelation compares the running app-server with the invoking CLI.
type VersionRelation string

const (
	VersionCurrent VersionRelation = "current"
	VersionSkew    VersionRelation = "skew"
	VersionUnknown VersionRelation = "unknown"
)

// ManagerOwnership is based only on the official daemon version response.
type ManagerOwnership string

const (
	ManagerManaged   ManagerOwnership = "managed"
	ManagerUnmanaged ManagerOwnership = "unmanaged"
	ManagerUnknown   ManagerOwnership = "unknown"
)

// RemoteControlCapability is the closed result of the read-only app-server
// remoteControl/status/read request.
type RemoteControlCapability string

const (
	RemoteControlDisabled    RemoteControlCapability = "disabled"
	RemoteControlConnecting  RemoteControlCapability = "connecting"
	RemoteControlConnected   RemoteControlCapability = "connected"
	RemoteControlErrored     RemoteControlCapability = "errored"
	RemoteControlUnsupported RemoteControlCapability = "unsupported"
	RemoteControlUnavailable RemoteControlCapability = "unavailable"
	RemoteControlUnknown     RemoteControlCapability = "unknown"
)

// NativeActionReadiness is the fail-closed preflight for an explicit native
// mutation. Diagnostics only report it; they never execute its recovery.
type NativeActionReadiness string

const (
	NativeActionReady   NativeActionReadiness = "ready"
	NativeActionRefused NativeActionReadiness = "refused"
	NativeActionUnknown NativeActionReadiness = "unknown"
)

type NativeActionRefusal string

const (
	NativeActionRefusalNone                  NativeActionRefusal = "none"
	NativeActionRefusalUnmanaged             NativeActionRefusal = "unmanaged"
	NativeActionRefusalVersionSkew           NativeActionRefusal = "version-skew"
	NativeActionRefusalUnmanagedVersionSkew  NativeActionRefusal = "unmanaged-version-skew"
	NativeActionRefusalOwnershipUnknown      NativeActionRefusal = "ownership-unknown"
	NativeActionRefusalRuntimeVersionUnknown NativeActionRefusal = "runtime-version-unknown"
)

type InterruptionRisk string

const (
	InterruptionRiskNone          InterruptionRisk = "none"
	InterruptionRiskSharedClients InterruptionRisk = "shared-clients-disconnect"
)

type OperatorRecovery string

const (
	OperatorRecoveryNone                    OperatorRecovery = "none"
	OperatorRecoveryRestartManagedDaemon    OperatorRecovery = "restart-managed-daemon"
	OperatorRecoveryStopOwnerThenStart      OperatorRecovery = "stop-owner-then-start-managed-daemon"
	OperatorRecoveryInspectProcessOwnership OperatorRecovery = "inspect-process-ownership"
)

// Guidance returns bounded operator-owned recovery text. It is never executed.
func (r OperatorRecovery) Guidance() string {
	switch r {
	case OperatorRecoveryRestartManagedDaemon:
		return "This interrupts every client sharing the app server. After confirming that interruption, run `codex app-server daemon restart`, then rerun diagnostics."
	case OperatorRecoveryStopOwnerThenStart:
		return "This app server is not daemon-managed. Close every sharing Codex client, stop the process through its owning operator, then run `codex app-server daemon start` and rerun diagnostics. Projmux will not kill or restart it."
	case OperatorRecoveryInspectProcessOwnership:
		return "Process ownership or running version is unknown. Identify the owning operator before changing the shared app server, then rerun diagnostics. Projmux will not kill or restart it."
	default:
		return ""
	}
}

// NativeActionGuidance renders only closed readiness fields and bounded static
// recovery text, so explicit action errors can return the same refusal that
// Doctor, Settings, and support expose.
func (h Health) NativeActionGuidance() string {
	if h.NativeAction != NativeActionRefused {
		return ""
	}
	return fmt.Sprintf("refusal: %s; interruption risk: %s; operator recovery: %s. %s", h.NativeRefusal, h.InterruptionRisk, h.OperatorRecovery, h.OperatorRecovery.Guidance())
}

// Health is safe to render in Doctor, Settings, and support reports.
type Health struct {
	Source            Source                  `json:"source"`
	Availability      Availability            `json:"availability"`
	Reason            Reason                  `json:"reason"`
	ProbeReason       Reason                  `json:"probe_reason"`
	InstallCapability InstallCapability       `json:"install_capability"`
	Version           string                  `json:"version,omitempty"`
	Endpoint          EndpointKind            `json:"endpoint_kind"`
	Connection        ConnectionState         `json:"connection_state"`
	EndpointReadiness EndpointReadiness       `json:"endpoint_readiness"`
	RunningExecutable RunningExecutable       `json:"running_executable"`
	VersionRelation   VersionRelation         `json:"version_relation"`
	CLIVersion        string                  `json:"cli_version,omitempty"`
	ManagedVersion    string                  `json:"managed_version,omitempty"`
	RunningVersion    string                  `json:"running_version,omitempty"`
	ManagerOwnership  ManagerOwnership        `json:"manager_ownership"`
	RemoteControl     RemoteControlCapability `json:"remote_control_capability"`
	NativeAction      NativeActionReadiness   `json:"native_action_readiness"`
	NativeRefusal     NativeActionRefusal     `json:"native_action_refusal"`
	InterruptionRisk  InterruptionRisk        `json:"interruption_risk"`
	OperatorRecovery  OperatorRecovery        `json:"operator_recovery"`
	Lifecycle         LifecycleOutcome        `json:"lifecycle_outcome,omitempty"`
	LifecycleReason   LifecycleReason         `json:"lifecycle_reason,omitempty"`
}

// Decide applies the bounded primary/fallback policy to one probe result.
func Decide(availability Availability, reason Reason, version string, endpoint EndpointKind, connection ConnectionState, hookAvailable bool) Health {
	probeReason := reason
	source := SourceAppServer
	if availability != AvailabilityAvailable {
		source = SourceHookFallback
		if !hookAvailable {
			source = SourceUnavailable
			reason = ReasonHookUnavailable
		}
	}
	return Health{
		Source:            source,
		Availability:      availability,
		Reason:            reason,
		ProbeReason:       probeReason,
		InstallCapability: InstallCapabilityUnknown,
		Version:           safeVersion(version),
		Endpoint:          endpoint,
		Connection:        connection,
		EndpointReadiness: endpointReadiness(availability, probeReason),
		RunningExecutable: RunningExecutableUnknown,
		VersionRelation:   VersionUnknown,
		ManagerOwnership:  ManagerUnknown,
		RemoteControl:     remoteControlForEndpoint(availability),
		NativeAction:      NativeActionUnknown,
		NativeRefusal:     NativeActionRefusalNone,
		InterruptionRisk:  InterruptionRiskNone,
		OperatorRecovery:  OperatorRecoveryNone,
	}
}

func endpointReadiness(availability Availability, reason Reason) EndpointReadiness {
	switch availability {
	case AvailabilityAvailable:
		return EndpointReady
	case AvailabilityTimeout:
		return EndpointTimedOut
	case AvailabilityUnsupported:
		return EndpointUnsupported
	case AvailabilityProtocolError:
		return EndpointProtocolError
	default:
		if reason == ReasonDaemonNotRunning {
			return EndpointDead
		}
		return EndpointUnavailable
	}
}

func remoteControlForEndpoint(availability Availability) RemoteControlCapability {
	if availability == AvailabilityAvailable {
		return RemoteControlUnknown
	}
	return RemoteControlUnavailable
}

func safeVersion(raw string) string {
	raw = strings.TrimSpace(raw)
	if IsSafeDiagnosticVersion(raw) {
		return raw
	}
	// User-Agent strings commonly include platform text. Retain only the first
	// semver-shaped token and discard every surrounding byte.
	match := versionPattern.FindStringSubmatch(raw)
	if len(match) == 2 && len(match[1]) <= 32 {
		return match[1]
	}
	return ""
}

var versionPattern = regexp.MustCompile(`(?:^|[^0-9])([0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]+)?)`)

var diagnosticVersionPattern = regexp.MustCompile(`^(?:[A-Za-z][A-Za-z0-9._+-]{0,31}/)?[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]{1,16})?$`)

// IsSafeDiagnosticVersion accepts only a plain semantic version or one
// bounded product-name/version pair. It rejects paths, tokens, and prose.
func IsSafeDiagnosticVersion(value string) bool {
	return len(value) <= 64 && diagnosticVersionPattern.MatchString(value)
}
