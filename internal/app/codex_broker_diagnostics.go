package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
)

// codexBrokerDiagnosticTimeout bounds one read-only diagnostics observation,
// every dial it makes included. It is short on purpose: a diagnostics surface
// that blocks on an unhealthy runtime is worse than one that reports the
// runtime as unreachable.
const codexBrokerDiagnosticTimeout = 2 * time.Second

const (
	// codexBrokerRecordPrefix and codexBrokerRecordSuffix bracket the discovery
	// records a runtime publishes. They are matched rather than derived,
	// because the key inside a record's filename is a digest of an endpoint
	// this reader does not know yet: learning which endpoints exist is the
	// whole reason the directory is read.
	codexBrokerRecordPrefix = "cb-"
	codexBrokerRecordSuffix = ".json"
	// codexBrokerRecordLimit bounds one discovery record read. It matches the
	// bound the runtime's own reader applies, so a record this reader accepts
	// is one Dial will also read rather than refuse on size.
	codexBrokerRecordLimit = 8 << 10
	// codexBrokerPublishedLimit bounds how many published endpoints one
	// diagnostics read considers. A discovery directory holds one record per
	// live endpoint, so this sits far above any real fleet and exists only so
	// that a directory full of junk cannot turn `doctor` into a scan.
	codexBrokerPublishedLimit = 64
)

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
	// Published is how many endpoints this state domain has a discovery record
	// for. It is what separates "nothing is published" from "something is
	// published and this reader could not reach it", and an operator who sees
	// a non-zero count next to `absent` knows to look at the directory rather
	// than at the process table.
	Published int `json:"published_endpoints"`
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
//
// The endpoints it dials are the ones this state domain has published, read
// out of the discovery directory. Naming an endpoint up front is what this
// reader must not do: a runtime publishes under the generation-scoped key its
// route derives, so a reader that assumed the default key reported a live
// broker as `absent` and an operator acted on that answer by killing a process.
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
	published, refusal := codexBrokerPublishedRuntimes(domain)
	if refusal != "" {
		return codexBrokerDiagnostic{State: codexBrokerStateUnavailable, Reason: refusal}
	}
	if len(published) == 0 {
		// No record means no runtime announced itself here, which is exactly
		// what a machine with no live native Agent looks like.
		return codexBrokerDiagnostic{State: codexBrokerStateAbsent, Reason: string(codexbroker.RefusalHostUnavailable)}
	}
	// One deadline covers the whole observation rather than each dial, so a
	// domain that published several unreachable endpoints still cannot hold
	// `doctor` open for a multiple of the bound.
	dialCtx, cancel := context.WithTimeout(ctx, codexBrokerDiagnosticTimeout)
	defer cancel()
	var refused codexBrokerDiagnostic
	for _, discovery := range published {
		diagnostic, running := observeCodexBrokerEndpoint(dialCtx, discovery)
		diagnostic.Published = len(published)
		if running {
			return diagnostic
		}
		if refused.State == "" {
			refused = diagnostic
		}
	}
	return refused
}

// observeCodexBrokerEndpoint dials one published endpoint and reports whether
// it answered.
func observeCodexBrokerEndpoint(ctx context.Context, discovery codexbroker.Discovery) (codexBrokerDiagnostic, bool) {
	endpoint := string(discovery.Endpoint())
	// Dial rather than Ensure. Ensure is allowed to create the discovery
	// directory, reclaim a stale artifact, and start a runtime; a diagnostics
	// read may do none of those. Dial only reaches a runtime that already
	// published itself, and refuses `host-unavailable` when none did.
	conn, err := codexbroker.Dial(ctx, discovery, codexbroker.DialConfig{Timeout: codexBrokerDiagnosticTimeout})
	if err != nil {
		return codexBrokerDiagnostic{
			State:    codexBrokerStateForRefusal(err),
			Reason:   codexBrokerRefusalToken(err),
			Endpoint: endpoint,
		}, false
	}
	defer conn.Close()
	telemetry, err := conn.Stats(ctx)
	if err != nil {
		return codexBrokerDiagnostic{
			State:    codexBrokerStateUnavailable,
			Reason:   codexBrokerRefusalToken(err),
			Runtime:  conn.Runtime(),
			Protocol: conn.Protocol(),
			Endpoint: endpoint,
		}, false
	}
	diagnostic := projectCodexBrokerTelemetry(telemetry)
	if diagnostic.Endpoint == "" {
		diagnostic.Endpoint = endpoint
	}
	return diagnostic, true
}

// codexBrokerPublishedRuntimes lists the discovery contracts this state domain
// has a published record for, in a stable endpoint order.
//
// It creates nothing and repairs nothing: an absent directory is an empty
// result, and a record it cannot make sense of is skipped rather than removed.
// Every record it accepts is validated again by Dial, which re-reads the whole
// file under the ownership rules this reader deliberately does not duplicate;
// the only thing taken from a record here is which endpoint it announces.
// It reports a closed refusal token rather than an error, because the only
// thing its caller can do with a failure is render it, and an unclassified
// message on a diagnostics surface is exactly what this package refuses.
func codexBrokerPublishedRuntimes(domain string) ([]codexbroker.Discovery, string) {
	// The default key is used for one thing here: deriving the per-domain
	// discovery directory, and surfacing the two contract refusals that depend
	// on the domain rather than on the endpoint. It is never a dial target.
	locator, err := codexbroker.NewDiscovery(domain, codexbroker.DefaultEndpointKey)
	if err != nil {
		return nil, codexBrokerRefusalToken(err)
	}
	dir := locator.Dir()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ""
	}
	if err != nil {
		// A directory that is there and cannot be listed is not an
		// owner-private object of the expected kind, which is the condition
		// the runtime's own reader refuses as `discovery-untrusted`.
		return nil, string(codexbroker.RefusalDiscoveryUntrusted)
	}
	var published []codexbroker.Discovery
	seen := make(map[codexbroker.EndpointKey]struct{}, len(entries))
	for _, entry := range entries {
		if len(published) >= codexBrokerPublishedLimit {
			break
		}
		name := entry.Name()
		if !entry.Type().IsRegular() ||
			!strings.HasPrefix(name, codexBrokerRecordPrefix) || !strings.HasSuffix(name, codexBrokerRecordSuffix) {
			continue
		}
		endpoint, ok := codexBrokerRecordEndpoint(filepath.Join(dir, name))
		if !ok {
			continue
		}
		if _, duplicate := seen[endpoint]; duplicate {
			continue
		}
		discovery, err := codexbroker.NewDiscovery(domain, endpoint)
		// The endpoint a record announces must derive back to the record that
		// announced it. A record naming some other endpoint is not a runtime
		// this domain published; it is a file whose contents disagree with its
		// own location, and dialing it would reach a socket nobody claimed.
		if err != nil || discovery.RecordPath() != filepath.Join(dir, name) {
			continue
		}
		seen[endpoint] = struct{}{}
		published = append(published, discovery)
	}
	sort.Slice(published, func(i, j int) bool {
		return published[i].Endpoint() < published[j].Endpoint()
	})
	return published, ""
}

// codexBrokerRecordEndpoint reads the endpoint one discovery record announces.
//
// Only that field is taken. The credential, the pid, and the protocol window
// stay with Dial, which reads the record again under its own ownership proof,
// so nothing here can widen what a diagnostics read is trusted to know.
func codexBrokerRecordEndpoint(path string) (codexbroker.EndpointKey, bool) {
	file, err := os.Open(path) // #nosec G304 -- path is one entry of the derived discovery directory.
	if err != nil {
		return "", false
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, codexBrokerRecordLimit+1))
	if err != nil || len(payload) > codexBrokerRecordLimit {
		return "", false
	}
	var record struct {
		Endpoint codexbroker.EndpointKey `json:"endpoint"`
	}
	if err := json.Unmarshal(payload, &record); err != nil || record.Endpoint == "" {
		return "", false
	}
	return record.Endpoint, true
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
