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

// TestDoctorDaemonVersionProbeTimeoutGolden runs Doctor's own read-only probe
// against a synthetic version-matched daemon whose `daemon version` answer is
// slow the way a healthy daemon is under CPU contention. An answer slower than
// the proxy budget but inside the daemon version bound reads as consistent and
// ready; an answer past that bound still shows `result timeout` and the
// ownership-unknown refusal, so the failure stays visible in Doctor.
func TestDoctorDaemonVersionProbeTimeoutGolden(t *testing.T) {
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
	t.Setenv("CODEX_DIAGNOSTIC_USER_AGENT", "projmux/0.154.0 (Ubuntu 26.4.0; x86_64) unknown (projmux; 0.15.3)")
	t.Setenv("CODEX_DIAGNOSTIC_MANAGER", `{"status":"running","backend":"pid","managedCodexVersion":"0.154.0","cliVersion":"0.154.0","appServerVersion":"0.154.0"}`)

	var out bytes.Buffer
	for _, row := range []struct {
		name, want string
		delay      time.Duration
		action     codexappserver.NativeActionReadiness
	}{
		{"slower-than-proxy-budget", "result observed; agreement consistent", codexappserver.DefaultProbeTimeout + 500*time.Millisecond, codexappserver.NativeActionReady},
		{"past-daemon-version-bound", "result timeout; agreement insufficient", time.Minute, codexappserver.NativeActionRefused},
	} {
		t.Setenv("CODEX_DIAGNOSTIC_MANAGER_DELAY", row.delay.String())
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

	golden := filepath.Join("testdata", "codex_daemon_version_probe_timeout_doctor.golden")
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
		t.Fatalf("doctor probe timeout golden mismatch (UPDATE_GOLDEN=1 rewrites it)\n--- got ---\n%s\n--- want ---\n%s", out.Bytes(), want)
	}
}
