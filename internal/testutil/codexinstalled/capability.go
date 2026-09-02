package codexinstalled

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/mod/semver"
)

const CapabilitySchemaVersion = 1

type SemanticCapability string

const CapabilityTurnFreeThreadLiveAttach SemanticCapability = "turn-free-thread-live-attach"

type CapabilityStatus string

const (
	CapabilitySupported   CapabilityStatus = "supported"
	CapabilityUnsupported CapabilityStatus = "unsupported"
	CapabilityInfraError  CapabilityStatus = "infra-error"
)

type CapabilityReason string

const (
	CapabilityReasonLivePaneAttached    CapabilityReason = "live-pane-attached"
	CapabilityReasonAttachRefused       CapabilityReason = "attach-refused"
	CapabilityReasonEndpointUnavailable CapabilityReason = "endpoint-unavailable"
	CapabilityReasonEvidenceIncomplete  CapabilityReason = "evidence-incomplete"
	CapabilityReasonProbeInvalid        CapabilityReason = "probe-invalid"
	CapabilityReasonTerminalMissing     CapabilityReason = "terminal-result-missing"
	CapabilityReasonTerminalInvalid     CapabilityReason = "terminal-result-invalid"
	CapabilityReasonVersionMismatch     CapabilityReason = "version-tuple-mismatch"
)

// CapabilityMethod records how one observation was made. Semantic reduction
// never branches on these values: an upstream wire or CLI rename changes this
// metadata, not the supported/unsupported decision.
type CapabilityMethod struct {
	Attach  string `json:"attach"`
	Loaded  string `json:"loaded"`
	Runtime string `json:"runtime"`
}

type CapabilityEvidence struct {
	EndpointReady            bool   `json:"endpoint_ready"`
	ThreadCreatedWithoutTurn bool   `json:"thread_created_without_turn"`
	LoadedBeforeAttach       bool   `json:"loaded_before_attach"`
	AttachAttempted          bool   `json:"attach_attempted"`
	LoadedAfterAttach        bool   `json:"loaded_after_attach"`
	RuntimeStatusAfterAttach string `json:"runtime_status_after_attach"`
	PaneObserved             bool   `json:"pane_observed"`
	PaneAlive                bool   `json:"pane_alive"`
}

type CapabilityObservation struct {
	Probe string `json:"probe"`
	Run   string `json:"run"`
}

type CapabilityResult struct {
	Versions     VersionTuple          `json:"versions"`
	Capability   SemanticCapability    `json:"capability"`
	Method       CapabilityMethod      `json:"method"`
	Result       CapabilityStatus      `json:"result"`
	Reason       CapabilityReason      `json:"reason"`
	Evidence     CapabilityEvidence    `json:"evidence"`
	LastObserved CapabilityObservation `json:"last_observed"`
}

type CapabilityLedger struct {
	SchemaVersion int                `json:"schema_version"`
	Capabilities  []CapabilityResult `json:"capabilities"`
}

var capabilityMethodPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
var capabilityProbePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,127}$`)
var capabilityRunPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9:@._-]{0,127}$`)

// EvaluateTurnFreeThreadLiveAttach reduces only semantic facts. The Method
// object is preserved verbatim after structural validation and is deliberately
// absent from every decision below.
func EvaluateTurnFreeThreadLiveAttach(
	versions VersionTuple,
	method CapabilityMethod,
	evidence CapabilityEvidence,
	observation CapabilityObservation,
) CapabilityResult {
	result := CapabilityResult{
		Versions: versions.normalized(), Capability: CapabilityTurnFreeThreadLiveAttach,
		Method: method, Evidence: evidence, LastObserved: normalizeCapabilityObservation(observation),
	}
	switch {
	case !evidence.EndpointReady:
		result.Result, result.Reason = CapabilityInfraError, CapabilityReasonEndpointUnavailable
	case !evidence.ThreadCreatedWithoutTurn || !evidence.LoadedBeforeAttach || !evidence.AttachAttempted:
		result.Result, result.Reason = CapabilityInfraError, CapabilityReasonEvidenceIncomplete
	case evidence.PaneObserved && evidence.PaneAlive && evidence.LoadedAfterAttach &&
		slices.Contains([]string{"idle", "active"}, evidence.RuntimeStatusAfterAttach):
		result.Result, result.Reason = CapabilitySupported, CapabilityReasonLivePaneAttached
	case evidence.PaneObserved && !evidence.PaneAlive:
		result.Result, result.Reason = CapabilityUnsupported, CapabilityReasonAttachRefused
	default:
		result.Result, result.Reason = CapabilityInfraError, CapabilityReasonEvidenceIncomplete
	}
	return result
}

func InfraErrorCapability(versions VersionTuple, reason CapabilityReason, observation CapabilityObservation) CapabilityResult {
	return CapabilityResult{
		Versions: versions.normalized(), Capability: CapabilityTurnFreeThreadLiveAttach,
		Method: CapabilityMethod{Attach: "unobserved", Loaded: "unobserved", Runtime: "unobserved"},
		Result: CapabilityInfraError, Reason: reason,
		LastObserved: normalizeCapabilityObservation(observation),
	}
}

// EnforceCapabilityVersion preserves the independently observed tuple but
// prevents a scheduled matrix declaration from qualifying a different one.
func EnforceCapabilityVersion(result CapabilityResult, expected string) CapabilityResult {
	if result.Result == CapabilityInfraError {
		return result
	}
	if !semver.IsValid("v"+expected) ||
		result.Versions.CLI != expected || result.Versions.Managed != expected || result.Versions.AppServer != expected {
		result.Result = CapabilityInfraError
		result.Reason = CapabilityReasonVersionMismatch
	}
	return result
}

func (result CapabilityResult) Validate() error {
	if result.Versions != result.Versions.normalized() {
		return fmt.Errorf("installed capability contains a blank version")
	}
	if result.Capability != CapabilityTurnFreeThreadLiveAttach {
		return fmt.Errorf("installed capability %q is unknown", result.Capability)
	}
	for _, version := range []string{result.Versions.CLI, result.Versions.Managed, result.Versions.AppServer} {
		if version != UnknownVersion && !semver.IsValid("v"+version) {
			return fmt.Errorf("installed capability contains a non-semver version")
		}
	}
	for _, method := range []string{result.Method.Attach, result.Method.Loaded, result.Method.Runtime} {
		if !capabilityMethodPattern.MatchString(method) {
			return fmt.Errorf("installed capability method %q is invalid", method)
		}
	}
	if !capabilityProbePattern.MatchString(result.LastObserved.Probe) ||
		!capabilityRunPattern.MatchString(result.LastObserved.Run) {
		return fmt.Errorf("installed capability last-observed evidence is incomplete")
	}
	switch result.Result {
	case CapabilitySupported:
		if result.Reason != CapabilityReasonLivePaneAttached ||
			result.Versions.CLI == UnknownVersion || result.Versions.Managed == UnknownVersion || result.Versions.AppServer == UnknownVersion ||
			!result.Evidence.EndpointReady || !result.Evidence.ThreadCreatedWithoutTurn ||
			!result.Evidence.LoadedBeforeAttach || !result.Evidence.AttachAttempted ||
			!result.Evidence.LoadedAfterAttach || !result.Evidence.PaneObserved || !result.Evidence.PaneAlive ||
			!slices.Contains([]string{"idle", "active"}, result.Evidence.RuntimeStatusAfterAttach) {
			return fmt.Errorf("installed supported capability lacks live no-turn Pane evidence")
		}
	case CapabilityUnsupported:
		if result.Reason != CapabilityReasonAttachRefused ||
			result.Versions.CLI == UnknownVersion || result.Versions.Managed == UnknownVersion || result.Versions.AppServer == UnknownVersion ||
			!result.Evidence.EndpointReady || !result.Evidence.ThreadCreatedWithoutTurn ||
			!result.Evidence.LoadedBeforeAttach || !result.Evidence.AttachAttempted ||
			!result.Evidence.PaneObserved || result.Evidence.PaneAlive {
			return fmt.Errorf("installed unsupported capability lacks a terminal attach refusal")
		}
	case CapabilityInfraError:
		if !slices.Contains([]CapabilityReason{
			CapabilityReasonEndpointUnavailable,
			CapabilityReasonEvidenceIncomplete,
			CapabilityReasonProbeInvalid,
			CapabilityReasonTerminalMissing,
			CapabilityReasonTerminalInvalid,
			CapabilityReasonVersionMismatch,
		}, result.Reason) {
			return fmt.Errorf("installed capability infrastructure result has reason %q", result.Reason)
		}
	default:
		return fmt.Errorf("installed capability result %q is not terminal", result.Result)
	}
	return nil
}

func (result CapabilityResult) JSON() (string, error) {
	if err := result.Validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(result)
	return string(encoded), err
}

func (ledger CapabilityLedger) Validate() error {
	if ledger.SchemaVersion != CapabilitySchemaVersion {
		return fmt.Errorf("installed capability ledger schema version = %d", ledger.SchemaVersion)
	}
	if len(ledger.Capabilities) != 1 {
		return fmt.Errorf("installed capability ledger entries = %d, want 1", len(ledger.Capabilities))
	}
	return ledger.Capabilities[0].Validate()
}

func (ledger CapabilityLedger) JSON() ([]byte, error) {
	if err := ledger.Validate(); err != nil {
		return nil, err
	}
	return json.MarshalIndent(ledger, "", "  ")
}

func DecodeCapabilityLedger(encoded []byte) (CapabilityLedger, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var ledger CapabilityLedger
	if err := decoder.Decode(&ledger); err != nil {
		return CapabilityLedger{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return CapabilityLedger{}, fmt.Errorf("installed capability ledger contains trailing JSON")
	} else if err != io.EOF {
		return CapabilityLedger{}, err
	}
	if err := ledger.Validate(); err != nil {
		return CapabilityLedger{}, err
	}
	return ledger, nil
}

func normalizeCapabilityObservation(observation CapabilityObservation) CapabilityObservation {
	observation.Probe = strings.TrimSpace(observation.Probe)
	observation.Run = strings.TrimSpace(observation.Run)
	return observation
}
