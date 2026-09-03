package app

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/cli"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
)

// TestInternalCodexBrokerServePublishesAndIdlesOutWithoutTouchingAnyDaemon is
// the app adapter's end-to-end proof.
//
// The entrypoint owns exactly two things: resolving the state domain and
// handing the runtime an endpoint opener. Everything else belongs to
// `codexbroker`. Because the broker opens its upstream connection only once a
// binding exists, a serve that publishes and then idles out reaches no Codex
// daemon at all, which is what makes this safe to run anywhere.
func TestInternalCodexBrokerServePublishesAndIdlesOutWithoutTouchingAnyDaemon(t *testing.T) {
	domain := newBrokerStateDomain(t)
	discovery, err := codexbroker.NewDiscovery(domain, codexbroker.DefaultEndpointKey)
	if err != nil {
		t.Fatalf("NewDiscovery() = %v", err)
	}
	command := newCodexBrokerCommand()
	var stdout lockedBuffer
	var stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- command.Run([]string{"serve", "--state-domain", domain, "--idle-timeout", "300ms"}, &stdout, &stderr)
	}()

	waitForBroker(t, "the runtime to publish its socket and record", func() bool {
		_, socketErr := os.Lstat(discovery.SocketPath())
		_, recordErr := os.Lstat(discovery.RecordPath())
		return socketErr == nil && recordErr == nil
	})
	waitForBroker(t, "the runtime readiness line", func() bool {
		return strings.Contains(stdout.String(), codexBrokerReadyLine)
	})
	// A published record must be owner-private: it carries the credential that
	// authenticates every client of this state domain.
	info, err := os.Lstat(discovery.RecordPath())
	if err != nil {
		t.Fatalf("lstat record: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("record mode = %v, want owner-only", info.Mode().Perm())
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned %v (stderr=%q)", err, stderr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the runtime never idled out")
	}
	if _, err := os.Lstat(discovery.SocketPath()); !os.IsNotExist(err) {
		t.Fatalf("socket survived the idle shutdown: %v", err)
	}
	if _, err := os.Lstat(discovery.RecordPath()); !os.IsNotExist(err) {
		t.Fatalf("record survived the idle shutdown: %v", err)
	}
}

func TestInternalCodexBrokerServePublishesTheExactGenerationRuntimeWithoutOpeningProvider(t *testing.T) {
	domain := newBrokerStateDomain(t)
	key, err := codexbroker.NewEndpointKey("state-exact", "generation-exact")
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := codexbroker.NewDiscovery(domain, key)
	if err != nil {
		t.Fatal(err)
	}
	command := newCodexBrokerCommand()
	var stdout lockedBuffer
	var stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- command.Run([]string{
			"serve", "--state-domain", domain, "--idle-timeout", "300ms",
			"--endpoint-state-domain", "state-exact", "--endpoint-generation", "generation-exact",
			"--endpoint-socket", filepath.Join(domain, "private-app-server.sock"),
		}, &stdout, &stderr)
	}()
	waitForBroker(t, "the exact generation runtime to publish", func() bool {
		_, socketErr := os.Lstat(discovery.SocketPath())
		_, recordErr := os.Lstat(discovery.RecordPath())
		return socketErr == nil && recordErr == nil
	})
	waitForBroker(t, "the exact generation runtime readiness line", func() bool {
		return strings.Contains(stdout.String(), codexBrokerReadyLine)
	})
	conn, err := codexbroker.Dial(t.Context(), discovery, codexbroker.DialConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if conn.Runtime() == "" {
		t.Fatal("generation runtime returned no authenticated broker runtime identity")
	}
	_ = conn.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("generation serve returned %v (stderr=%q)", err, stderr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the generation runtime never idled out")
	}
}

// TestInternalCodexBrokerRouteStaysHiddenPlumbingWithNoPublicSurface is the
// scope audit for this phase's only CLI change.
//
// The runtime is an internal service, not a user-facing verb: it may only be
// reachable through the hidden `internal` namespace, and the phase adds no
// public route, no Registry resource, and no way to reach a runtime outside
// the state domain the caller named.
func TestInternalCodexBrokerRouteStaysHiddenPlumbingWithNoPublicSurface(t *testing.T) {
	var node cli.Route
	for _, candidate := range cli.Routes() {
		if candidate.Name == "internal" {
			node = candidate
		}
		if strings.Contains(candidate.Name, "broker") {
			t.Fatalf("codex-broker leaked into the public route graph as %q", candidate.Name)
		}
	}
	if node.Name != "internal" || !node.Hidden || node.Disposition != cli.DispositionInternal {
		t.Fatalf("internal namespace = %+v, want a hidden internal node", node)
	}
	if containsRoute(node.Canonical, "internal codex-broker") {
		t.Fatal("the broker runtime entered the canonical command projection; it is a service entrypoint, not a command spelling")
	}
	if !usageMentions(node.Usage, "internal codex-broker") {
		t.Fatalf("internal usage = %v, want the codex-broker plumbing listed", node.Usage)
	}
	if !containsRoute(internalSubcommands, "codex-broker") {
		t.Fatalf("internal subcommands = %v, want codex-broker", internalSubcommands)
	}

	command := newCodexBrokerCommand()
	var stdout, stderr bytes.Buffer
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "no subcommand", args: nil, want: "requires a subcommand"},
		{name: "unknown subcommand", args: []string{"attach"}, want: "is not available"},
		{name: "relative domain", args: []string{"serve", "--state-domain", "relative/state"}, want: "absolute --state-domain"},
		{name: "positional serve argument", args: []string{"serve", "extra"}, want: "does not accept positional arguments"},
		{name: "positional probe argument", args: []string{"probe", "extra"}, want: "does not accept positional arguments"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := command.Run(test.args, &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Run(%v) = %v, want an error containing %q", test.args, err, test.want)
			}
		})
	}

	t.Run("probe refuses rather than starting a runtime", func(t *testing.T) {
		domain := newBrokerStateDomain(t)
		err := command.Run([]string{"probe", "--state-domain", domain, "--no-start"}, &stdout, &stderr)
		if err == nil || !strings.Contains(err.Error(), "host-unavailable") {
			t.Fatalf("probe --no-start = %v, want a host-unavailable refusal", err)
		}
		entries, readErr := os.ReadDir(domain)
		if readErr != nil {
			t.Fatalf("read state domain: %v", readErr)
		}
		for _, entry := range entries {
			if entry.Name() != "broker" {
				t.Fatalf("probe created %q outside the broker artifact directory", entry.Name())
			}
		}
	})
}

// TestCodexBrokerStateDomainFollowsTheProjmuxStatePaths pins the resolution the
// adapter owns: the runtime singleton is scoped to this installation's state
// directory, not to a working directory or a pane.
func TestCodexBrokerStateDomainFollowsTheProjmuxStatePaths(t *testing.T) {
	root := t.TempDir()
	command := &codexBrokerCommand{
		homeDir:   func() (string, error) { return filepath.Join(root, "home"), nil },
		lookupEnv: func(key string) string { return "" },
	}
	domain, err := command.stateDomain()
	if err != nil {
		t.Fatalf("stateDomain() = %v", err)
	}
	want := filepath.Join(root, "home", ".local", "state", "projmux")
	if domain != want {
		t.Fatalf("stateDomain() = %q, want %q", domain, want)
	}

	command.lookupEnv = func(key string) string {
		if key == "XDG_STATE_HOME" {
			return filepath.Join(root, "state")
		}
		return ""
	}
	domain, err = command.stateDomain()
	if err != nil {
		t.Fatalf("stateDomain() = %v", err)
	}
	if want := filepath.Join(root, "state", "projmux"); domain != want {
		t.Fatalf("stateDomain() with XDG_STATE_HOME = %q, want %q", domain, want)
	}
}

// newBrokerStateDomain returns a private state domain short enough for a Unix
// socket path.
func newBrokerStateDomain(t *testing.T) string {
	t.Helper()
	base := os.TempDir()
	if info, err := os.Stat("/tmp"); err == nil && info.IsDir() {
		base = "/tmp"
	}
	root, err := os.MkdirTemp(base, "pmxa")
	if err != nil {
		t.Fatalf("create state domain: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

func usageMentions(usage []string, want string) bool {
	for _, line := range usage {
		if strings.Contains(line, want) {
			return true
		}
	}
	return false
}

func containsRoute(routes []string, want string) bool {
	return slices.Contains(routes, want)
}

func waitForBroker(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// lockedBuffer is a writer a test can read while the command under test is
// still writing to it.
type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(payload []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(payload)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}
