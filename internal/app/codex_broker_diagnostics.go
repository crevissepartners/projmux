package app

import (
	"context"
	"os"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
)

// codexBrokerDiagnosticTimeout bounds one read-only diagnostics dial. It is
// short on purpose: a diagnostics surface that blocks on an unhealthy runtime
// is worse than one that reports the runtime as unreachable.
const codexBrokerDiagnosticTimeout = 2 * time.Second

// Closed states of the Codex endpoint broker runtime, as a diagnostics reader
// sees it. They are distinct because the operator answer differs: `absent` is
// the ordinary resting state with no native Agent bound, `unsupported` is a
// build that can never host one, and `unavailable` is a runtime that exists but
// would not answer.
const (
	codexBrokerStateRunning     = "running"
	codexBrokerStateAbsent      = "absent"
	codexBrokerStateUnsupported = "unsupported"
	codexBrokerStateUnavailable = "unavailable"
)

// codexBrokerRevocation is one closed revocation reason and its count.
type codexBrokerRevocation struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// codexBrokerDiagnostic is the content-free projection of the Codex endpoint
// broker runtime that serves this state domain.
//
// Every field is a closed token or a counter. Nothing here names a thread, an
// Agent, a prompt, or a path, so the whole value is safe to render in Doctor,
// in Settings, and in a support bundle without redaction.
type codexBrokerDiagnostic struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
	// Runtime is the runtime identity that answered, which is how two readings
	// taken across a crash can be told apart from two readings of one runtime.
	Runtime  string `json:"runtime,omitempty"`
	Protocol int    `json:"protocol,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
	// Connections is how many upstream app-server connections this runtime
	// currently owns. The contract is one per effective endpoint regardless of
	// how many Agents are bound, so this is the number the per-Agent observer
	// retirement is measured by.
	Connections     int    `json:"connections"`
	ConnectionEpoch uint64 `json:"connection_epoch,omitempty"`
	Bindings        int    `json:"bindings"`
	Clients         int    `json:"clients"`
	Reconnects      int    `json:"reconnects"`
	// Evictions and SnapshotFailures are pulled out of the reason breakdown
	// because they are the two binding-scoped faults an operator acts on: a
	// queue that overflowed, and a reconnect snapshot a live endpoint refused.
	Evictions        int                     `json:"evictions"`
	SnapshotFailures int                     `json:"snapshot_failures"`
	Revocations      []codexBrokerRevocation `json:"revocations,omitempty"`
	Draining         bool                    `json:"draining,omitempty"`
}

// codexBrokerDiagnosticLookup reads the current broker projection.
type codexBrokerDiagnosticLookup func() codexBrokerDiagnostic

// defaultCodexBrokerDiagnosticLookup reads the runtime published for this
// process's own state domain.
func defaultCodexBrokerDiagnosticLookup() codexBrokerDiagnosticLookup {
	return func() codexBrokerDiagnostic {
		return observeCodexBrokerRuntime(context.Background(), os.Getenv, os.UserHomeDir)
	}
}

// observeCodexBrokerRuntime reaches an already published runtime and projects
// its telemetry.
//
// It never starts one. A diagnostics read that could start a long-lived
// process would make `projmux doctor` a side effect, and would report a runtime
// that exists only because it was asked about.
func observeCodexBrokerRuntime(
	ctx context.Context,
	lookupEnv func(string) string,
	home func() (string, error),
) codexBrokerDiagnostic {
	if !codexbroker.Supported() {
		return codexBrokerDiagnostic{State: codexBrokerStateUnsupported, Reason: string(codexbroker.RefusalUnsupportedPlatform)}
	}
	domain, err := codexBrokerStateDomain(lookupEnv, home)
	if err != nil {
		return codexBrokerDiagnostic{State: codexBrokerStateUnavailable, Reason: string(codexbroker.RefusalDomainRequired)}
	}
	// The discovery contract is built directly rather than through
	// codexBrokerDiscoveryFor, which renders its refusal into a message. A
	// diagnostics surface needs the closed token itself: `socket-path-too-long`
	// and `domain-required` have different operator answers, and both would
	// arrive here as `unclassified` if the type were flattened first.
	discovery, err := codexbroker.NewDiscovery(domain, codexbroker.DefaultEndpointKey)
	if err != nil {
		return codexBrokerDiagnostic{State: codexBrokerStateUnavailable, Reason: codexBrokerRefusalToken(err)}
	}
	dialCtx, cancel := context.WithTimeout(ctx, codexBrokerDiagnosticTimeout)
	defer cancel()
	// Dial rather than Ensure. Ensure is allowed to create the discovery
	// directory, reclaim a stale artifact, and start a runtime; a diagnostics
	// read may do none of those. Dial only reaches a runtime that already
	// published itself, and refuses `host-unavailable` when none did.
	conn, err := codexbroker.Dial(dialCtx, discovery, codexbroker.DialConfig{Timeout: codexBrokerDiagnosticTimeout})
	if err != nil {
		return codexBrokerDiagnostic{State: codexBrokerStateForRefusal(err), Reason: codexBrokerRefusalToken(err)}
	}
	defer conn.Close()
	telemetry, err := conn.Stats(dialCtx)
	if err != nil {
		return codexBrokerDiagnostic{
			State:    codexBrokerStateUnavailable,
			Reason:   codexBrokerRefusalToken(err),
			Runtime:  conn.Runtime(),
			Protocol: conn.Protocol(),
		}
	}
	return projectCodexBrokerTelemetry(telemetry)
}

// projectCodexBrokerTelemetry turns one runtime answer into the rendered
// diagnostic.
func projectCodexBrokerTelemetry(telemetry codexbroker.RuntimeTelemetry) codexBrokerDiagnostic {
	diagnostic := codexBrokerDiagnostic{
		State:           codexBrokerStateRunning,
		Runtime:         telemetry.Runtime,
		Protocol:        telemetry.Protocol,
		Endpoint:        string(telemetry.Broker.Endpoint),
		ConnectionEpoch: uint64(telemetry.Broker.ConnectionEpoch),
		Bindings:        telemetry.Broker.Bindings,
		Clients:         telemetry.Host.LiveSessions,
		Reconnects:      telemetry.Broker.Disconnects,
		Draining:        telemetry.Host.Draining,
	}
	// One connection per effective endpoint is the contract, so the count is a
	// current-state answer rather than a total: connects that have not yet been
	// matched by a disconnect.
	if open := telemetry.Broker.Connects - telemetry.Broker.Disconnects; open > 0 {
		diagnostic.Connections = open
	}
	for _, revocation := range telemetry.Broker.Revocations {
		diagnostic.Revocations = append(diagnostic.Revocations,
			codexBrokerRevocation{Reason: string(revocation.Reason), Count: revocation.Count})
		switch revocation.Reason {
		case codexbroker.RefusalResyncRequired:
			diagnostic.Evictions += revocation.Count
		case codexbroker.RefusalSnapshotUnavailable:
			diagnostic.SnapshotFailures += revocation.Count
		}
	}
	return diagnostic
}

// codexBrokerStateForRefusal separates the resting state from a fault. A
// discovery that found nothing published is what a machine with no live native
// Agent looks like, and reporting it as a failure would train an operator to
// ignore the row.
func codexBrokerStateForRefusal(err error) string {
	switch codexbroker.RefusalOf(err) {
	case codexbroker.RefusalHostUnavailable:
		return codexBrokerStateAbsent
	case codexbroker.RefusalUnsupportedPlatform:
		return codexBrokerStateUnsupported
	default:
		return codexBrokerStateUnavailable
	}
}

// codexBrokerRefusalToken renders the closed refusal of one failure. An
// unclassified error is reported as unclassified rather than as its message,
// so no path or provider detail can reach a diagnostics surface through it.
func codexBrokerRefusalToken(err error) string {
	if refusal := codexbroker.RefusalOf(err); refusal != codexbroker.RefusalNone {
		return string(refusal)
	}
	return "unclassified"
}
