package codexappserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDaemonVersionProbeToleratesLoadWithinItsOwnBound drives the production
// native-user-action seam (EnsureDefaultProxyReady, whose caller budget is
// DefaultProbeTimeout) against a synthetic codex whose `daemon version` answer
// is slow the way a healthy daemon is under CPU contention. A version-matched
// answer slower than the proxy budget but inside daemonVersionProbeTimeout is
// consistent and ready; one past that bound is still a timeout, insufficient
// evidence, and an ownership-unknown refusal. A proxy probe that never answers
// still times out at its own readiness floor and leaves the action unknown.
func TestDaemonVersionProbeToleratesLoadWithinItsOwnBound(t *testing.T) {
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
exec "$PROJMUX_CODEX_PROBE_HELPER" -test.run=^TestProxyProbeHelperProcess$ -- "$PROJMUX_CODEX_PROBE_SCENARIO"
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
	t.Setenv("GO_WANT_PROXY_HELPER", "1")

	for _, tt := range []struct {
		name, proxy       string
		delay             time.Duration
		result, agreement string
		readiness         EndpointReadiness
		action            NativeActionReadiness
		refusal           NativeActionRefusal
		lifecycle         LifecycleOutcome
		minElapsed        time.Duration
		maxElapsed        time.Duration
	}{
		{
			name: "slower than the proxy budget", proxy: "healthy", delay: DefaultProbeTimeout + 500*time.Millisecond,
			result: "observed", agreement: "consistent", readiness: EndpointReady,
			action: NativeActionReady, refusal: NativeActionRefusalNone, lifecycle: LifecycleAlreadyRunning,
			minElapsed: DefaultProbeTimeout + 500*time.Millisecond, maxElapsed: daemonVersionProbeTimeout + DefaultProbeTimeout,
		},
		{
			name: "past the daemon version bound", proxy: "healthy", delay: time.Minute,
			result: "timeout", agreement: "insufficient", readiness: EndpointReady,
			action: NativeActionRefused, refusal: NativeActionRefusalOwnershipUnknown, lifecycle: LifecycleRefused,
			minElapsed: daemonVersionProbeTimeout, maxElapsed: daemonVersionProbeTimeout + DefaultProbeTimeout + 2*time.Second,
		},
		{
			name: "proxy times out at its own floor", proxy: "timeout", delay: 0,
			result: "observed", agreement: "insufficient", readiness: EndpointTimedOut,
			action: NativeActionUnknown, refusal: NativeActionRefusalNone, lifecycle: LifecycleNotAttempted,
			minElapsed: readinessProxyProbeTimeout, maxElapsed: readinessProbeBudget(DefaultProbeTimeout),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.WriteFile(ledger, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PROJMUX_CODEX_PROBE_SCENARIO", tt.proxy)
			t.Setenv("PROJMUX_CODEX_SLOW_DAEMON_VERSION", tt.delay.String())
			started := time.Now()
			health, err := EnsureDefaultProxyReady(t.Context(), TriggerNativeUserAction, "0.13.0", true)
			elapsed := time.Since(started)
			if err != nil {
				t.Fatal(err)
			}
			if health.ManagerEvidence == nil || health.ManagerEvidence.Result != tt.result || health.ManagerEvidence.Agreement != tt.agreement {
				t.Fatalf("manager evidence = %+v", health.ManagerEvidence)
			}
			if health.EndpointReadiness != tt.readiness || health.NativeAction != tt.action || health.NativeRefusal != tt.refusal || health.Lifecycle != tt.lifecycle {
				t.Fatalf("health = %+v", health)
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

// TestManagerProbeTimeoutOnlyRaisesShorterCallers pins the floor: callers whose
// budget is DefaultProbeTimeout (doctor, settings, usage, catalog, capability,
// thread) get daemonVersionProbeTimeout, while slower callers keep their own
// budget and DefaultProbeTimeout itself is unchanged for every other consumer.
func TestManagerProbeTimeoutOnlyRaisesShorterCallers(t *testing.T) {
	if DefaultProbeTimeout != 750*time.Millisecond {
		t.Fatalf("DefaultProbeTimeout = %s; it bounds proxy, catalog, attach, usage and ingest probes and must not move with the manager probe", DefaultProbeTimeout)
	}
	if daemonVersionProbeTimeout <= DefaultProbeTimeout || daemonVersionProbeTimeout > 3*time.Second {
		t.Fatalf("daemonVersionProbeTimeout = %s, want above %s and at most 3s", daemonVersionProbeTimeout, DefaultProbeTimeout)
	}
	for _, tt := range []struct{ caller, want time.Duration }{
		{0, daemonVersionProbeTimeout},
		{30 * time.Millisecond, daemonVersionProbeTimeout},
		{DefaultProbeTimeout, daemonVersionProbeTimeout},
		{daemonVersionProbeTimeout, daemonVersionProbeTimeout},
		{5 * time.Second, 5 * time.Second},   // broker attach
		{25 * time.Second, 25 * time.Second}, // native thread Current and attach
	} {
		if got := managerProbeTimeout(tt.caller); got != tt.want {
			t.Fatalf("managerProbeTimeout(%s) = %s, want %s", tt.caller, got, tt.want)
		}
	}
	if got := attachTimeout(0); got != DefaultProbeTimeout {
		t.Fatalf("attach proxy timeout = %s, want %s", got, DefaultProbeTimeout)
	}
}

// TestSlowDaemonVersionHelperProcess answers `codex app-server daemon version`
// for a running pid-managed daemon whose versions match the "healthy" proxy
// scenario, after PROJMUX_CODEX_SLOW_DAEMON_VERSION.
func TestSlowDaemonVersionHelperProcess(t *testing.T) {
	delay, err := time.ParseDuration(os.Getenv("PROJMUX_CODEX_SLOW_DAEMON_VERSION"))
	if os.Getenv("GO_WANT_PROXY_HELPER") != "1" || err != nil {
		return
	}
	time.Sleep(delay)
	_, _ = os.Stdout.WriteString(`{"status":"running","backend":"pid","managedCodexVersion":"0.149.0","cliVersion":"0.149.0","appServerVersion":"0.149.0"}` + "\n")
	os.Exit(0)
}
