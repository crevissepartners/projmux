package app

import (
	"context"
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
