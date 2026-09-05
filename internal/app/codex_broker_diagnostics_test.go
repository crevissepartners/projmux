package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
)

// TestBrokerDiagnosticsReadCreatesNothing
// fixes the read-only contract of the diagnostics surface.
//
// `projmux doctor` and Settings ask about the broker on every invocation. If
// asking could publish a discovery directory, reclaim an artifact, or start a
// runtime, the diagnostics would report a runtime that exists only because it
// was asked about, and the operator could never tell a resting machine from one
// that doctor woke up.
func TestBrokerDiagnosticsReadCreatesNothing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the broker runtime contract is Unix-only")
	}
	stateHome := t.TempDir()
	home := filepath.Join(stateHome, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"XDG_STATE_HOME": filepath.Join(stateHome, "state")}
	before := walkPaths(t, stateHome)

	diagnostic := observeCodexBrokerRuntime(context.Background(),
		func(key string) string { return env[key] },
		func() (string, error) { return home, nil })

	if after := walkPaths(t, stateHome); !reflect.DeepEqual(before, after) {
		t.Fatalf("a diagnostics read changed the state domain:\nbefore=%v\nafter=%v", before, after)
	}
	if diagnostic.State == codexBrokerStateRunning {
		t.Fatalf("a state domain with no published runtime reported %+v", diagnostic)
	}
	if diagnostic.Connections != 0 || diagnostic.Bindings != 0 || diagnostic.Runtime != "" {
		t.Fatalf("an unreachable runtime reported live counters: %+v", diagnostic)
	}
	// Whichever way the read failed, it must name the closed refusal. An
	// `unclassified` reason here would mean a typed condition was flattened
	// into a message on its way to an operator.
	if diagnostic.Reason == "" || diagnostic.Reason == "unclassified" {
		t.Fatalf("reason = %q, want the closed refusal that produced state %q", diagnostic.Reason, diagnostic.State)
	}
}

// TestBrokerDiagnosticsReportTheRestingAbsentRuntime fixes the ordinary
// answer on a machine with no live native Agent: absent is the resting state,
// not a fault, so the row does not train an operator to ignore it.
func TestBrokerDiagnosticsReportTheRestingAbsentRuntime(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the broker runtime contract is Unix-only")
	}
	// A short root on purpose: the discovery contract refuses a state domain
	// whose derived socket path exceeds the platform bound, and that refusal is
	// a different assertion than this one.
	root, err := os.MkdirTemp("/tmp", "pmxb")
	if err != nil {
		t.Skipf("no short temporary root available: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	home := filepath.Join(root, "h")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"XDG_STATE_HOME": filepath.Join(root, "s")}
	diagnostic := observeCodexBrokerRuntime(context.Background(),
		func(key string) string { return env[key] },
		func() (string, error) { return home, nil })
	if diagnostic.State != codexBrokerStateAbsent ||
		diagnostic.Reason != string(codexbroker.RefusalHostUnavailable) {
		t.Fatalf("diagnostic = %+v, want the resting absent state", diagnostic)
	}
}

func walkPaths(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	if err := filepath.Walk(root, func(path string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		paths = append(paths, relative)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return paths
}

// TestBrokerDiagnosticsProjectOneConnectionAndTypedFaults is the
// projection half of the retirement's headline number.
//
// The retired per-Agent observer opened one upstream connection per Agent. The
// broker owns one per effective endpoint no matter how many bindings it serves,
// and the three binding-scoped faults an operator acts on -- an evicted queue,
// a refused reconnect snapshot, and a fenced-out epoch -- stay separable
// instead of collapsing into one revocation count.
func TestBrokerDiagnosticsProjectOneConnectionAndTypedFaults(t *testing.T) {
	telemetry := codexbroker.RuntimeTelemetry{
		Runtime:  "runtime-7",
		Protocol: 3,
		Host:     codexbroker.HostStats{Sessions: 5, LiveSessions: 2, Bindings: 4, Refused: 1},
		Broker: codexbroker.Diagnostics{
			Endpoint:        codexbroker.DefaultEndpointKey,
			ConnectionEpoch: 9,
			Connects:        4,
			Disconnects:     3,
			Bindings:        4,
			RevokedBindings: 6,
			Revocations: []codexbroker.RevocationCount{
				{Reason: codexbroker.RefusalResyncRequired, Count: 2},
				{Reason: codexbroker.RefusalSnapshotUnavailable, Count: 3},
				{Reason: codexbroker.RefusalStaleBindingEpoch, Count: 1},
			},
		},
	}
	diagnostic := projectCodexBrokerTelemetry(telemetry)
	if diagnostic.State != codexBrokerStateRunning {
		t.Fatalf("state = %q", diagnostic.State)
	}
	if diagnostic.Connections != 1 {
		t.Fatalf("upstream connections = %d, want exactly one per effective endpoint", diagnostic.Connections)
	}
	if diagnostic.Bindings != 4 || diagnostic.Clients != 2 {
		t.Fatalf("bindings = %d clients = %d, want 4 bindings across 2 live clients", diagnostic.Bindings, diagnostic.Clients)
	}
	if diagnostic.Reconnects != 3 {
		t.Fatalf("reconnects = %d, want the disconnect count", diagnostic.Reconnects)
	}
	if diagnostic.Evictions != 2 || diagnostic.SnapshotFailures != 3 {
		t.Fatalf("evictions = %d snapshot failures = %d, want 2/3", diagnostic.Evictions, diagnostic.SnapshotFailures)
	}
	if len(diagnostic.Revocations) != 3 {
		t.Fatalf("revocation breakdown = %+v, want every closed reason", diagnostic.Revocations)
	}

	// A connection that is down is reported as down rather than as never
	// having existed: reconnect history is what tells those two apart.
	telemetry.Broker.Disconnects = 4
	if down := projectCodexBrokerTelemetry(telemetry); down.Connections != 0 || down.Reconnects != 4 {
		t.Fatalf("disconnected projection = %+v", down)
	}
}

// TestCodexBrokerDiagnosticIsContentFree audits the rendered surface. Doctor,
// Settings, and a support bundle all render this value verbatim, so nothing on
// it may be shaped like a provider payload or a location.
func TestCodexBrokerDiagnosticIsContentFree(t *testing.T) {
	value := reflect.TypeFor[codexBrokerDiagnostic]()
	for i := range value.NumField() {
		name := strings.ToLower(value.Field(i).Name)
		for _, forbidden := range []string{
			"prompt", "message", "text", "content", "command", "output",
			"token", "title", "transcript", "turn", "item", "diff", "param",
			"path", "socket", "cwd", "thread", "agent", "pane",
		} {
			if strings.Contains(name, forbidden) {
				t.Fatalf("codexBrokerDiagnostic.%s looks like provider content or a location", value.Field(i).Name)
			}
		}
	}
	// Every rendered state and reason is a bare closed token.
	for _, token := range []string{
		codexBrokerStateRunning, codexBrokerStateAbsent,
		codexBrokerStateUnsupported, codexBrokerStateUnavailable,
	} {
		if token == "" || strings.ContainsAny(token, " \t/\\") {
			t.Fatalf("broker state %q is not a bare token", token)
		}
	}
	if got := codexBrokerRefusalToken(errNotClassified{}); got != "unclassified" {
		t.Fatalf("unclassified refusal rendered as %q; an error message must never reach a diagnostics surface", got)
	}
}

type errNotClassified struct{}

func (errNotClassified) Error() string { return "/home/someone/.local/state/projmux/broker/socket" }

// TestBrokerDiagnosticsDialTheEndpointTheRuntimePublished is the C-1
// enforcement this reader never had.
//
// A managed runtime publishes under the generation-scoped key its route
// derives, never under the official default key. A diagnostics reader that
// named the default key up front therefore dialed a contract nothing had
// published and reported a live, listening broker as `absent /
// host-unavailable`. That answer is not merely cosmetic: an operator acted on
// it and terminated a foreign app-server process. The fixture below is that
// exact machine shape -- a live generation-scoped runtime beside a default-key
// artifact with no socket -- and the reader must reach the runtime.
func TestBrokerDiagnosticsDialTheEndpointTheRuntimePublished(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the broker runtime contract is Unix-only")
	}
	env, home, root := brokerDiagnosticsStateDomainForTest(t)
	domain, err := codexBrokerStateDomain(env, home)
	if err != nil {
		t.Fatalf("resolve state domain: %v", err)
	}
	endpoint, err := codexbroker.NewEndpointKey("state-domain-1", "codex-0.153.0")
	if err != nil {
		t.Fatalf("endpoint key: %v", err)
	}
	host := startPublishedBrokerRuntimeForTest(t, domain, endpoint)

	// The default key must be provably unpublished, or the assertion below
	// would pass for a reader that still dialed it.
	fallback, err := codexbroker.NewDiscovery(domain, codexbroker.DefaultEndpointKey)
	if err != nil {
		t.Fatalf("default discovery: %v", err)
	}
	if err := os.WriteFile(fallback.RecordPath()[:len(fallback.RecordPath())-len(".json")]+".lock", nil, 0o600); err != nil {
		t.Fatalf("stage the default-key lock: %v", err)
	}
	for _, path := range []string{fallback.RecordPath(), fallback.SocketPath()} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("%s exists; the default endpoint key must be unpublished in this fixture", filepath.Base(path))
		}
	}

	before := walkPaths(t, root)
	diagnostic := observeCodexBrokerRuntime(context.Background(), env, home)

	if diagnostic.State != codexBrokerStateRunning {
		t.Fatalf("diagnostic = %+v, want the published runtime reported as running", diagnostic)
	}
	if diagnostic.Endpoint != string(endpoint) {
		t.Fatalf("endpoint = %q, want the generation-scoped key %q the runtime published", diagnostic.Endpoint, endpoint)
	}
	if diagnostic.Endpoint == string(codexbroker.DefaultEndpointKey) {
		t.Fatal("the diagnostic reported the default endpoint key, which nothing published")
	}
	if diagnostic.Published != 1 {
		t.Fatalf("published endpoints = %d, want the single record this domain holds", diagnostic.Published)
	}
	if diagnostic.Runtime != host.RuntimeID() {
		t.Fatalf("runtime = %q, want the identity of the host that answered (%q)", diagnostic.Runtime, host.RuntimeID())
	}
	if diagnostic.Reason != "" {
		t.Fatalf("reason = %q, want none from a runtime that answered", diagnostic.Reason)
	}

	// Negative audit: reading is not starting, reclaiming, or stopping. The
	// state domain is byte-identical in its file set, and the runtime this
	// read reached is still serving afterwards.
	if after := walkPaths(t, root); !reflect.DeepEqual(before, after) {
		t.Fatalf("a diagnostics read changed the state domain:\nbefore=%v\nafter=%v", before, after)
	}
	select {
	case <-host.Done():
		t.Fatal("the diagnostics read stopped the runtime it observed")
	default:
	}
	if second := observeCodexBrokerRuntime(context.Background(), env, home); second.State != codexBrokerStateRunning ||
		second.Runtime != host.RuntimeID() {
		t.Fatalf("second read = %+v, want the same runtime still serving", second)
	}
}

// TestBrokerDiagnosticsStayAbsentWhenOnlyAnUnpublishedArtifactRemains fixes the
// other half of C-1: the resting state and its closed reason token survive the
// fix. A startup lock is not a runtime, and a domain holding nothing else must
// still report `absent / host-unavailable` with no endpoint claimed.
func TestBrokerDiagnosticsStayAbsentWhenOnlyAnUnpublishedArtifactRemains(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the broker runtime contract is Unix-only")
	}
	env, home, _ := brokerDiagnosticsStateDomainForTest(t)
	domain, err := codexBrokerStateDomain(env, home)
	if err != nil {
		t.Fatalf("resolve state domain: %v", err)
	}
	discovery, err := codexbroker.NewDiscovery(domain, codexbroker.DefaultEndpointKey)
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}
	if err := os.MkdirAll(discovery.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	record := discovery.RecordPath()
	if err := os.WriteFile(record[:len(record)-len(".json")]+".lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// A record whose announced endpoint does not derive back to its own
	// filename is a file that disagrees with its location, not a published
	// runtime, so it may not become a dial target either.
	if err := os.WriteFile(filepath.Join(discovery.Dir(), "cb-000000000000.json"),
		[]byte(`{"endpoint":"`+string(codexbroker.DefaultEndpointKey)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	diagnostic := observeCodexBrokerRuntime(context.Background(), env, home)
	if diagnostic.State != codexBrokerStateAbsent ||
		diagnostic.Reason != string(codexbroker.RefusalHostUnavailable) {
		t.Fatalf("diagnostic = %+v, want the resting absent state and its closed reason", diagnostic)
	}
	if diagnostic.Published != 0 {
		t.Fatalf("published endpoints = %d, want zero: neither a lock nor a misplaced record is a publication", diagnostic.Published)
	}
	if diagnostic.Endpoint != "" {
		t.Fatalf("endpoint = %q, want none: no endpoint was judged", diagnostic.Endpoint)
	}
}

// TestBrokerDiagnosticsSelectEveryPublishedEndpointDeterministically pins the
// selection rule for a domain serving more than one generation. The reader does
// not decide which generation ought to be current -- that judgment belongs to
// the generation pool -- so it walks the published set in one stable order and
// reports the first runtime that answers, together with how many were there.
func TestBrokerDiagnosticsSelectEveryPublishedEndpointDeterministically(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the broker runtime contract is Unix-only")
	}
	env, home, _ := brokerDiagnosticsStateDomainForTest(t)
	domain, err := codexBrokerStateDomain(env, home)
	if err != nil {
		t.Fatalf("resolve state domain: %v", err)
	}
	first, err := codexbroker.NewEndpointKey("state-domain-1", "codex-0.153.0")
	if err != nil {
		t.Fatal(err)
	}
	second, err := codexbroker.NewEndpointKey("state-domain-1", "codex-0.154.0")
	if err != nil {
		t.Fatal(err)
	}
	hosts := map[codexbroker.EndpointKey]*codexbroker.Host{
		first:  startPublishedBrokerRuntimeForTest(t, domain, first),
		second: startPublishedBrokerRuntimeForTest(t, domain, second),
	}

	published, refusal := codexBrokerPublishedRuntimes(domain)
	if refusal != "" {
		t.Fatalf("published runtimes refused: %s", refusal)
	}
	if len(published) != 2 {
		t.Fatalf("published = %d, want both generations", len(published))
	}
	if published[0].Endpoint() >= published[1].Endpoint() {
		t.Fatalf("published order = %v, want a stable ascending endpoint order", []codexbroker.EndpointKey{
			published[0].Endpoint(), published[1].Endpoint()})
	}

	diagnostic := observeCodexBrokerRuntime(context.Background(), env, home)
	if diagnostic.State != codexBrokerStateRunning || diagnostic.Published != 2 {
		t.Fatalf("diagnostic = %+v, want a running runtime out of two published endpoints", diagnostic)
	}
	host, known := hosts[codexbroker.EndpointKey(diagnostic.Endpoint)]
	if !known || diagnostic.Runtime != host.RuntimeID() {
		t.Fatalf("diagnostic = %+v, want the telemetry of one of the two published runtimes", diagnostic)
	}
	// Both runtimes outlive the read: observing one endpoint may not retire
	// its sibling, and the reader has no lifecycle authority over either.
	for endpoint, host := range hosts {
		select {
		case <-host.Done():
			t.Fatalf("the diagnostics read stopped the runtime published on %s", endpoint)
		default:
		}
	}
	if repeat := observeCodexBrokerRuntime(context.Background(), env, home); repeat.Endpoint != diagnostic.Endpoint {
		t.Fatalf("endpoint = %q then %q; the selection must be deterministic", diagnostic.Endpoint, repeat.Endpoint)
	}
}

// brokerDiagnosticsStateDomainForTest returns the env lookup, the home lookup,
// and the isolated root the state domain is derived inside. The root is short
// on purpose: the discovery contract refuses a domain whose derived socket path
// exceeds the platform bound, and that refusal is a different assertion.
func brokerDiagnosticsStateDomainForTest(t *testing.T) (func(string) string, func() (string, error), string) {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "pmxb")
	if err != nil {
		t.Skipf("no short temporary root available: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	home := filepath.Join(root, "h")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"XDG_STATE_HOME": filepath.Join(root, "s")}
	return func(key string) string { return env[key] },
		func() (string, error) { return home, nil },
		root
}

// startPublishedBrokerRuntimeForTest publishes one real runtime host on the
// exact endpoint key inside the given state domain. Its opener is never
// reached, because the broker opens an upstream connection only once a binding
// exists and a diagnostics read creates none.
func startPublishedBrokerRuntimeForTest(t *testing.T, domain string, endpoint codexbroker.EndpointKey) *codexbroker.Host {
	t.Helper()
	discovery, err := codexbroker.NewDiscovery(domain, endpoint)
	if err != nil {
		t.Fatalf("discovery for %s: %v", endpoint, err)
	}
	broker, err := codexbroker.NewBroker(codexbroker.Config{
		Endpoint: endpoint,
		Opener: func(context.Context) (codexbroker.Endpoint, error) {
			return nil, errors.New("a diagnostics read must never open an upstream connection")
		},
	})
	if err != nil {
		t.Fatalf("broker for %s: %v", endpoint, err)
	}
	host, err := codexbroker.StartHost(codexbroker.HostConfig{Discovery: discovery, Broker: broker, IdleTimeout: -1})
	if err != nil {
		_ = broker.Close()
		t.Fatalf("host for %s: %v", endpoint, err)
	}
	t.Cleanup(func() {
		_ = host.Close()
		_ = broker.Close()
	})
	return host
}
