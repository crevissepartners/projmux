package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// TestDoctorReadinessProxyProbeTimeoutGolden runs Doctor's own read-only probe
// against a synthetic version-matched daemon whose proxy initialize answer is
// slow the way a healthy daemon is under CPU contention. An answer slower than
// the default probe budget but inside the readiness proxy bound reads as ready;
// an answer past that bound still shows a timed-out proxy and an unknown
// native action, distinct from a manager-probe timeout refusal.
func TestDoctorReadinessProxyProbeTimeoutGolden(t *testing.T) {
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	codexHome, bin := filepath.Join(root, "c"), filepath.Join(root, "bin")
	payload := filepath.Join(codexHome, "packages", "standalone", "current", "bin", "codex")
	for _, dir := range []string{filepath.Dir(payload), bin} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(payload, []byte("managed"), 0o700); err != nil {
		t.Fatal(err)
	}
	ledger := filepath.Join(root, "argv")
	shim := `#!/bin/sh
printf '%s\n' "$*" >> "$CODEX_DIAGNOSTIC_ARGV"
exec "$CODEX_DIAGNOSTIC_HELPER" -test.run=^TestCodexDiagnosticProxyProcess$ -- "$@"
`
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(shim), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("CODEX_DIAGNOSTIC_HELPER", helper)
	t.Setenv("CODEX_DIAGNOSTIC_ARGV", ledger)
	t.Setenv("CODEX_DIAGNOSTIC_FAILURE", "")
	t.Setenv("CODEX_DIAGNOSTIC_MANAGER_DELAY", "")
	t.Setenv("CODEX_DIAGNOSTIC_USER_AGENT", "projmux/0.154.0 (Ubuntu 26.4.0; x86_64) unknown (projmux; 0.15.3)")
	t.Setenv("CODEX_DIAGNOSTIC_MANAGER", `{"status":"running","backend":"pid","managedCodexVersion":"0.154.0","cliVersion":"0.154.0","appServerVersion":"0.154.0"}`)

	var out bytes.Buffer
	for _, row := range []struct {
		name, want string
		delay      time.Duration
		action     codexappserver.NativeActionReadiness
	}{
		{"slower-than-default-probe-budget", "connection: ready", codexappserver.DefaultProbeTimeout + 500*time.Millisecond, codexappserver.NativeActionReady},
		{"past-readiness-proxy-bound", "availability: timeout; reason: timeout; endpoint: stdio-proxy; connection: timed-out", time.Minute, codexappserver.NativeActionUnknown},
	} {
		t.Setenv("CODEX_DIAGNOSTIC_INITIALIZE_DELAY", row.delay.String())
		health, err := codexappserver.EnsureDefaultProxyReady(t.Context(), codexappserver.TriggerDoctor, "0.15.3", true)
		if err != nil {
			t.Fatalf("%s: %v", row.name, err)
		}
		var doctor bytes.Buffer
		writeDoctorAppServerText(&doctor, &health)
		if health.NativeAction != row.action || !strings.Contains(doctor.String(), row.want) {
			t.Fatalf("%s: native action %q, doctor text:\n%s", row.name, health.NativeAction, doctor.String())
		}
		out.WriteString("== " + row.name + "\n")
		out.WriteString("doctor:" + doctor.String() + "\n")
	}

	calls, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatal(err)
	}
	for call := range strings.SplitSeq(strings.TrimSpace(string(calls)), "\n") {
		if call != "app-server proxy" && call != "app-server daemon version" {
			t.Fatalf("doctor ran a non-observation codex command %q; ledger:\n%s", call, calls)
		}
	}

	golden := filepath.Join("testdata", "codex_readiness_proxy_probe_timeout_doctor.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(golden, out.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("doctor proxy probe timeout golden mismatch (UPDATE_GOLDEN=1 rewrites it)\n--- got ---\n%s\n--- want ---\n%s", out.Bytes(), want)
	}
}
