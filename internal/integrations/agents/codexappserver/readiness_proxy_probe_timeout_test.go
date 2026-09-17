package codexappserver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// installSlowInitializeCodex puts a synthetic codex on PATH whose stdio proxy
// answers initialize after PROJMUX_CODEX_PROXY_INITIALIZE_DELAY, the way a
// healthy daemon does under CPU contention, and whose `daemon version` answer
// is immediate and version-matched. It returns the argv ledger path.
func installSlowInitializeCodex(t *testing.T) string {
	t.Helper()
	helper, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	pathDir := t.TempDir()
	ledger := filepath.Join(t.TempDir(), "argv")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$PROJMUX_CODEX_SLOW_LEDGER"
if [ "$2" = daemon ]; then
	exec "$PROJMUX_CODEX_PROBE_HELPER" -test.run=^TestSlowDaemonVersionHelperProcess$
fi
exec "$PROJMUX_CODEX_PROBE_HELPER" -test.run=^TestProxyProbeHelperProcess$ -- healthy
`
	if err := os.WriteFile(filepath.Join(pathDir, "codex"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	codexHome := t.TempDir()
	payload := filepath.Join(codexHome, "packages", "standalone", "current", "bin", "codex")
	if err := os.MkdirAll(filepath.Dir(payload), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, []byte("managed"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("PROJMUX_CODEX_PROBE_HELPER", helper)
	t.Setenv("PROJMUX_CODEX_SLOW_LEDGER", ledger)
	t.Setenv("PROJMUX_CODEX_SLOW_DAEMON_VERSION", "0s")
	t.Setenv("GO_WANT_PROXY_HELPER", "1")
	return ledger
}

// TestReadinessProxyProbeToleratesLoadWithinItsOwnBound drives the production
// native-user-action seam (EnsureDefaultProxyReady, whose caller budget is
// DefaultProbeTimeout) against a version-matched daemon whose proxy initialize
// answer is slow. An answer slower than DefaultProbeTimeout but inside
// readinessProxyProbeTimeout is ready; one past that floor is still a proxy
// timeout that leaves the native action unknown without starting anything.
func TestReadinessProxyProbeToleratesLoadWithinItsOwnBound(t *testing.T) {
	ledger := installSlowInitializeCodex(t)
	for _, tt := range []struct {
		name         string
		delay        time.Duration
		availability Availability
		readiness    EndpointReadiness
		agreement    string
		action       NativeActionReadiness
		lifecycle    LifecycleOutcome
		minElapsed   time.Duration
		maxElapsed   time.Duration
	}{
		{
			name: "slower than the default probe budget", delay: DefaultProbeTimeout + 500*time.Millisecond,
			availability: AvailabilityAvailable, readiness: EndpointReady, agreement: "consistent",
			action: NativeActionReady, lifecycle: LifecycleAlreadyRunning,
			minElapsed: DefaultProbeTimeout + 500*time.Millisecond, maxElapsed: readinessProbeBudget(DefaultProbeTimeout),
		},
		{
			name: "past the readiness proxy bound", delay: time.Minute,
			availability: AvailabilityTimeout, readiness: EndpointTimedOut, agreement: "insufficient",
			action: NativeActionUnknown, lifecycle: LifecycleNotAttempted,
			minElapsed: readinessProxyProbeTimeout, maxElapsed: readinessProbeBudget(DefaultProbeTimeout),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.WriteFile(ledger, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PROJMUX_CODEX_PROXY_INITIALIZE_DELAY", tt.delay.String())
			started := time.Now()
			health, err := EnsureDefaultProxyReady(t.Context(), TriggerNativeUserAction, "0.13.0", true)
			elapsed := time.Since(started)
			if err != nil {
				t.Fatal(err)
			}
			if health.Availability != tt.availability || health.EndpointReadiness != tt.readiness ||
				health.NativeAction != tt.action || health.NativeRefusal != NativeActionRefusalNone || health.Lifecycle != tt.lifecycle {
				t.Fatalf("health = %+v", health)
			}
			if health.ManagerEvidence == nil || health.ManagerEvidence.Result != "observed" || health.ManagerEvidence.Agreement != tt.agreement {
				t.Fatalf("manager evidence = %+v", health.ManagerEvidence)
			}
			if elapsed < tt.minElapsed || elapsed > tt.maxElapsed {
				t.Fatalf("probe took %s, want within [%s, %s]", elapsed, tt.minElapsed, tt.maxElapsed)
			}
			calls, err := os.ReadFile(ledger)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(string(calls)); got != "app-server proxy\napp-server daemon version" {
				t.Fatalf("codex argv ledger = %q, want one proxy and one daemon version call", got)
			}
		})
	}
}

// TestReadinessProxyProbeTimeoutOnlyRaisesShorterCallers pins the floor to the
// readiness probe: DefaultProbeTimeout callers get readinessProxyProbeTimeout,
// slower callers (broker attach 5s, native thread 25s) keep their own budget,
// and DefaultProbeTimeout still bounds opening a client for usage, session
// catalogs, catalog reads, and attach.
func TestReadinessProxyProbeTimeoutOnlyRaisesShorterCallers(t *testing.T) {
	if DefaultProbeTimeout != 750*time.Millisecond {
		t.Fatalf("DefaultProbeTimeout = %s; it bounds client open, catalog, usage and ingest reads and must not move with the readiness probe", DefaultProbeTimeout)
	}
	if readinessProxyProbeTimeout <= DefaultProbeTimeout || readinessProxyProbeTimeout > 3*time.Second {
		t.Fatalf("readinessProxyProbeTimeout = %s, want above %s and at most 3s", readinessProxyProbeTimeout, DefaultProbeTimeout)
	}
	for _, tt := range []struct{ caller, want time.Duration }{
		{0, readinessProxyProbeTimeout},
		{30 * time.Millisecond, readinessProxyProbeTimeout},
		{DefaultProbeTimeout, readinessProxyProbeTimeout},
		{readinessProxyProbeTimeout, readinessProxyProbeTimeout},
		{5 * time.Second, 5 * time.Second},   // broker attach
		{25 * time.Second, 25 * time.Second}, // native thread Current and attach
	} {
		if got := proxyProbeTimeout(tt.caller); got != tt.want {
			t.Fatalf("proxyProbeTimeout(%s) = %s, want %s", tt.caller, got, tt.want)
		}
	}
	if got, want := readinessProbeBudget(DefaultProbeTimeout), readinessProxyProbeTimeout+daemonVersionProbeTimeout; got != want {
		t.Fatalf("readiness probe budget for a %s caller = %s, want %s", DefaultProbeTimeout, got, want)
	}
	if got := readinessProbeBudget(25 * time.Second); got != 50*time.Second {
		t.Fatalf("readiness probe budget for a 25s caller = %s, want 50s", got)
	}
	if got := attachTimeout(0); got != DefaultProbeTimeout {
		t.Fatalf("attach open timeout = %s, want %s", got, DefaultProbeTimeout)
	}

	installSlowInitializeCodex(t)
	slow := DefaultProbeTimeout + time.Second
	t.Setenv("PROJMUX_CODEX_PROXY_INITIALIZE_DELAY", slow.String())
	for name, open := range map[string]func(context.Context) error{
		"client open": func(ctx context.Context) error {
			client, err := OpenDefaultProxy(ctx, DefaultProbeTimeout, "0.13.0")
			if client != nil {
				_ = client.Close()
			}
			return err
		},
		"catalog read": func(ctx context.Context) error {
			_, err := ReadDefaultCatalogThread(ctx, "0.13.0", "thread-1")
			return err
		},
	} {
		started := time.Now()
		err := open(t.Context())
		elapsed := time.Since(started)
		if err == nil || elapsed < DefaultProbeTimeout || elapsed >= slow {
			t.Fatalf("%s: err=%v after %s, want a timeout at %s", name, err, elapsed, DefaultProbeTimeout)
		}
	}
}
