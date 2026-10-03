package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
)

func startInstallDrainHost(t *testing.T, domain, generation string, pid int, replaced bool) (*codexbroker.Host, codexbroker.Discovery) {
	t.Helper()
	key, err := codexbroker.NewEndpointKey("install-test-domain", generation)
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := codexbroker.NewDiscovery(domain, key)
	if err != nil {
		t.Fatal(err)
	}
	broker, err := codexbroker.NewBroker(codexbroker.Config{Endpoint: key, Opener: func(context.Context) (codexbroker.Endpoint, error) {
		return nil, errors.New("install must not open an upstream endpoint")
	}})
	if err != nil {
		t.Fatal(err)
	}
	host, err := codexbroker.StartHost(codexbroker.HostConfig{Discovery: discovery, Broker: broker, IdleTimeout: -1, ImageReplaced: func() bool { return replaced }})
	if err != nil {
		_ = broker.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })
	// Several in-process test hosts share this test process's PID. Give their
	// selection hints distinct fixture values; credential/runtime authority
	// stays exactly as StartHost published it and Dial still verifies it.
	body, err := os.ReadFile(discovery.RecordPath())
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]json.RawMessage
	if err := json.Unmarshal(body, &record); err != nil {
		t.Fatal(err)
	}
	record["pid"], err = json.Marshal(pid)
	if err != nil {
		t.Fatal(err)
	}
	body, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(discovery.RecordPath(), body, 0o600); err != nil {
		t.Fatal(err)
	}
	return host, discovery
}

func TestInstallReplacementDrainsPublishedGenerationTargets(t *testing.T) {
	t.Parallel()
	domain := newBrokerStateDomain(t)
	first, firstDiscovery := startInstallDrainHost(t, domain, "generation-one", 101, true)
	second, _ := startInstallDrainHost(t, domain, "generation-two", 102, true)
	unrelated, _ := startInstallDrainHost(t, domain, "different-executable", 103, true)
	fallback, err := codexbroker.NewDiscovery(domain, codexbroker.DefaultEndpointKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(fallback.RecordPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("default endpoint must be unpublished")
	}
	result := requestInstallReplacementDrain(t.Context(), domain, []installReplacementTarget{{pid: 101, origin: installReplacementTargetOrigin{domain: installReplacementDomainThis}}, {pid: 102, origin: installReplacementTargetOrigin{domain: installReplacementDomainThis}}})
	if result.accepted != 2 || result.refusal != "drain-required" || result.failureStage != "" {
		t.Fatalf("drain result = accepted %d refusal %s stage %s", result.accepted, result.refusal, result.failureStage)
	}
	for _, host := range []*codexbroker.Host{first, second} {
		select {
		case <-host.Done():
		case <-time.After(time.Second):
			t.Fatal("accepted runtime did not drain")
		}
	}
	// A successor takes the first path before settle. Completion must still
	// describe the old socket, and must not wait for or drain the successor.
	successor, _ := startInstallDrainHost(t, domain, "generation-one", 104, false)
	if _, err := os.Lstat(firstDiscovery.SocketPath()); err != nil {
		t.Fatal(err)
	}
	command := installReplacementCommand{settle: time.Second, poll: time.Millisecond}
	if got := command.settleDrained(result); got != 2 {
		t.Fatalf("drained = %d, want two original runtimes", got)
	}
	for _, host := range []*codexbroker.Host{unrelated, successor} {
		if host.Stats().Draining {
			t.Fatal("the pass drained a process outside its residual targets")
		}
		select {
		case <-host.Done():
			t.Fatal("unrelated or successor runtime stopped")
		default:
		}
	}
}

func TestInstallReplacementWelcomeCannotStandInForResidualTarget(t *testing.T) {
	t.Parallel()
	domain := newBrokerStateDomain(t)
	host, _ := startInstallDrainHost(t, domain, "current-generation", 101, false)
	result := requestInstallReplacementDrain(t.Context(), domain, []installReplacementTarget{{pid: 101, origin: installReplacementTargetOrigin{domain: installReplacementDomainThis}}})
	if result.accepted != 0 || result.failureStage != "handshake" || result.refusal != "" {
		t.Fatalf("current welcome = accepted %d refusal %s stage %s", result.accepted, result.refusal, result.failureStage)
	}
	if host.Stats().Draining {
		t.Fatal("current image was drained")
	}
}

func TestInstallReplacementMissingPublishedTargetIsDiscoveryFailure(t *testing.T) {
	t.Parallel()
	domain := newBrokerStateDomain(t)
	result := requestInstallReplacementDrain(t.Context(), domain, []installReplacementTarget{{pid: 101, origin: installReplacementTargetOrigin{domain: installReplacementDomainThis}}})
	if result.accepted != 0 || result.failureStage != "discovery" || result.refusal != "host-unavailable" {
		t.Fatalf("missing target = accepted %d refusal %s stage %s", result.accepted, result.refusal, result.failureStage)
	}
	if _, err := os.Lstat(filepath.Join(domain, "broker")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing target lookup created discovery artifacts")
	}
}

func TestInstallReplacementFailureStagesArePersistedWithoutIdentity(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"discovery", "dial", "handshake"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			outcome := runTestInstallReplacement(t, installReplacementFixture{
				vintage: projmuxProcessVintage{Supported: true, Roles: []projmuxProcessRoleVintage{{Role: codexControlPlaneRoleBroker, Processes: 1, Replaced: 1}}},
				refusal: "host-unavailable", failureStage: stage, goneAfter: -1, stateDir: dir,
				targets: []installReplacementTarget{{role: codexControlPlaneRoleBroker, pid: 74129, revision: "private-revision"}},
			})
			if outcome.FailureStage != stage || outcome.Refusal != "host-unavailable" {
				t.Fatalf("outcome = %+v", outcome)
			}
			body, err := os.ReadFile(filepath.Join(dir, installReplacementFile))
			if err != nil {
				t.Fatal(err)
			}
			for _, private := range []string{"74129", "private-revision", dir, "pid", "credential", "socket"} {
				if bytes.Contains(body, []byte(private)) {
					t.Fatalf("identity leaked into persisted diagnostics: %s", private)
				}
			}
		})
	}
}

func TestInstallReplacementReconcilesTargetsThatExitAfterCensus(t *testing.T) {
	t.Parallel()
	root := newBrokerStateDomain(t)
	var stderr bytes.Buffer
	command := installReplacementCommand{
		stateDir: func() (string, error) { return root, nil },
		readVintage: func(time.Time) projmuxProcessVintage {
			return projmuxProcessVintage{Supported: true, Roles: []projmuxProcessRoleVintage{{Role: codexControlPlaneRoleBroker, Processes: 1, Replaced: 1}}}
		},
		requestDrain: func(ctx context.Context) installReplacementDrainResult {
			// The fresh exact target snapshot is empty after the earlier role
			// census saw a broker. There is no target to request or await now.
			return requestInstallReplacementDrain(ctx, root, nil)
		},
	}
	if err := command.Run(&stderr); err != nil {
		t.Fatal(err)
	}
	outcome, ok := readInstallReplacementOutcome(filepath.Join(root, installReplacementFile))
	if !ok || outcome.Outcome != installReplacementOutcomeNoTarget || outcome.Attempted != 0 || outcome.Drained != 0 || outcome.FailureStage != "" || stderr.Len() != 0 {
		t.Fatalf("vanished target = %+v stderr=%q", outcome, stderr.String())
	}
}

func TestInstallReplacementUsesRuntimeIdentityWhenSocketInodeIsReused(t *testing.T) {
	t.Parallel()
	domain := newBrokerStateDomain(t)
	original, discovery := startInstallDrainHost(t, domain, "generation-one", 101, false)
	info, err := os.Lstat(discovery.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	target := installReplacementSocket{path: discovery.SocketPath(), info: info, discovery: discovery, runtime: original.RuntimeID()}
	if target.superseded(info) {
		t.Fatal("original live runtime was reported gone")
	}
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}
	successor, _ := startInstallDrainHost(t, domain, "generation-one", 102, false)
	// Model inode reuse explicitly by supplying the original metadata. The
	// publication and runtime IDs are from real hosts; only filesystem reuse
	// is modeled, so this regression is deterministic on tmpfs and ext4 alike.
	if !target.superseded(info) {
		t.Fatal("successor runtime with reused socket metadata concealed completion")
	}
	if successor.Stats().Draining {
		t.Fatal("completion observation drained the successor")
	}
	select {
	case <-successor.Done():
		t.Fatal("completion observation closed the successor")
	default:
	}

	body, err := os.ReadFile(discovery.RecordPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []struct {
		name     string
		contents []byte
		mode     os.FileMode
	}{
		{"malformed", []byte("not-json"), 0o600},
		{"untrusted", body, 0o666},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			if err := os.WriteFile(discovery.RecordPath(), invalid.contents, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(discovery.RecordPath(), invalid.mode); err != nil {
				t.Fatal(err)
			}
			if target.superseded(info) {
				t.Fatal("invalid successor record proved completion")
			}
		})
	}
	if err := os.Remove(discovery.RecordPath()); err != nil {
		t.Fatal(err)
	}
	if target.superseded(info) {
		t.Fatal("missing record alone proved completion")
	}
}

// Other-domain targets never enter this domain's discovery/dial path, even in
// a mixed fleet where a real same-domain host accepts the drain.
func TestInstallReplacementOtherDomainMixedFleet(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"other-only", "this-only", "this-other", "unknown", "unknown-other"} {
		t.Run(mode, func(t *testing.T) {
			domain := newBrokerStateDomain(t)
			otherDomain := newBrokerStateDomain(t)
			other, _ := startInstallDrainHost(t, otherDomain, "other", 202, true)
			otherTarget := installReplacementTarget{pid: 202, origin: installReplacementTargetOrigin{domain: installReplacementDomainOther, stateDomain: otherDomain}}
			var targets []installReplacementTarget
			wantAttempted, wantOther := 0, 0
			wantOutcome := installReplacementOutcomeNoTarget
			var current *codexbroker.Host
			if strings.Contains(mode, "this") {
				current, _ = startInstallDrainHost(t, domain, "this", 201, true)
				targets = append(targets, installReplacementTarget{pid: 201, origin: installReplacementTargetOrigin{domain: installReplacementDomainThis}})
				wantAttempted = 1
				wantOutcome = installReplacementOutcomeComplete
			}
			if strings.Contains(mode, "unknown") {
				targets = append(targets, installReplacementTarget{pid: 203, origin: installReplacementTargetOrigin{domain: installReplacementDomainUnknown}})
				wantAttempted = 1
				wantOutcome = installReplacementOutcomeUnreachable
			}
			if strings.Contains(mode, "other") {
				targets = append(targets, otherTarget)
				wantOther = 1
			}
			result := requestInstallReplacementDrain(t.Context(), domain, targets)
			var stderr bytes.Buffer
			state := t.TempDir()
			command := installReplacementCommand{stateDir: func() (string, error) { return state, nil }, readVintage: func(time.Time) projmuxProcessVintage {
				return projmuxProcessVintage{Supported: true, Roles: []projmuxProcessRoleVintage{
					{Role: codexControlPlaneRoleBroker, Processes: len(targets), Replaced: len(targets)},
					{Role: projmuxProcessRoleSessionClient, Processes: 1, Replaced: 1},
				}}
			}, requestDrain: func(context.Context) installReplacementDrainResult { return result }, settle: time.Second, poll: time.Millisecond}
			err := command.Run(&stderr)
			if (err != nil) != (wantOutcome == installReplacementOutcomeUnreachable) {
				t.Fatalf("Run = %v: %s", err, stderr.String())
			}
			outcome, ok := readInstallReplacementOutcome(filepath.Join(state, installReplacementFile))
			if !ok || outcome.Outcome != wantOutcome || outcome.Attempted != wantAttempted || outcome.Reported != 1+wantOther || outcome.OtherDomainReported != wantOther {
				t.Fatalf("outcome = %+v", outcome)
			}
			if wantOther > 0 && !strings.Contains(stderr.String(), "domain=other") {
				t.Fatal("missing other-domain warning")
			}
			if other.Stats().Draining {
				t.Fatal("other-domain broker was drained")
			}
			if current != nil && outcome.Drained != 1 {
				t.Fatalf("same-domain drained = %d", outcome.Drained)
			}
			payload, _ := os.ReadFile(filepath.Join(state, installReplacementFile))
			if strings.Contains(string(payload), otherDomain) || strings.Contains(string(payload), "pid") {
				t.Fatal("persisted target identities")
			}
		})
	}
}

func TestInstallReplacementUnknownCannotBorrowPublishedDrain(t *testing.T) {
	t.Parallel()
	for _, verdict := range []string{"", installReplacementDomainUnknown} {
		t.Run("unknown="+verdict, func(t *testing.T) {
			domain := newBrokerStateDomain(t)
			unknown, _ := startInstallDrainHost(t, domain, "unknown-reachable", 301, true)
			_, _ = startInstallDrainHost(t, domain, "known-this", 302, true)
			result := requestInstallReplacementDrain(t.Context(), domain, []installReplacementTarget{
				{pid: 301, origin: installReplacementTargetOrigin{domain: verdict}},
				{pid: 302, origin: installReplacementTargetOrigin{domain: installReplacementDomainThis}},
				{pid: 303, origin: installReplacementTargetOrigin{domain: installReplacementDomainOther}},
			})
			if result.attempted != 2 || result.accepted != 1 || result.failureStage != "discovery" || result.refusal != "host-unavailable" || len(result.otherDomains) != 1 {
				t.Fatalf("unknown result=%+v", result)
			}
			if unknown.Stats().Draining {
				t.Fatal("unknown host was dialed despite uncertain origin")
			}
			command := installReplacementCommand{settle: time.Second, poll: time.Millisecond, requestDrain: func(context.Context) installReplacementDrainResult { return result }}
			outcome := installReplacementOutcome{Attempted: 3}
			command.replace(&outcome)
			if outcome.Outcome != installReplacementOutcomeUnreachable || outcome.Attempted != 2 || outcome.Drained != 1 || outcome.Reported != 1 || outcome.OtherDomainReported != 1 {
				t.Fatalf("mixed unknown outcome=%+v", outcome)
			}
		})
	}
}
