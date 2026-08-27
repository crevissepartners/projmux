// Package codexappserver owns the Codex app-server wire protocol and its
// compatibility decision. Wire values never leave this vertical slice;
// consumers receive only the bounded, content-free Health projection.
package codexappserver

import (
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

// Health is safe to render in Doctor, Settings, and support reports.
type Health struct {
	Source            Source            `json:"source"`
	Availability      Availability      `json:"availability"`
	Reason            Reason            `json:"reason"`
	ProbeReason       Reason            `json:"probe_reason"`
	InstallCapability InstallCapability `json:"install_capability"`
	Version           string            `json:"version,omitempty"`
	Endpoint          EndpointKind      `json:"endpoint_kind"`
	Connection        ConnectionState   `json:"connection_state"`
	Lifecycle         LifecycleOutcome  `json:"lifecycle_outcome,omitempty"`
	LifecycleReason   LifecycleReason   `json:"lifecycle_reason,omitempty"`
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
	}
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
