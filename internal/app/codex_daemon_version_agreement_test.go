package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// TestDoctorProjmuxUserAgentDaemonAgreementGolden runs Doctor's own read-only
// probe against a synthetic codex that answers initialize the way a Codex 0.154.0
// daemon answers this client: the version sits under the "projmux" product
// token. The daemon evidence names the same version, so Doctor must report a
// consistent, managed, attachable endpoint instead of insufficient evidence; a
// different attach version stays contradictory.
func TestDoctorProjmuxUserAgentDaemonAgreementGolden(t *testing.T) {
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

	manager := `{"status":"running","backend":"pid","managedCodexPath":"/secret/managed/codex","managedCodexVersion":"0.154.0","socketPath":"/secret/app-server-control.sock","cliVersion":"0.154.0","appServerVersion":"0.154.0"}`
	var out bytes.Buffer
	for _, row := range []struct {
		name, userAgent, agreement string
		attach                     codexappserver.EndpointAttach
	}{
		{"live-same-version", "projmux/0.154.0 (Ubuntu 26.4.0; x86_64) unknown (projmux; 0.15.3)", "consistent", codexappserver.EndpointAttachAllowed},
		{"attach-version-differs", "projmux/0.153.4 (Ubuntu 26.4.0; x86_64) unknown (projmux; 0.15.3)", "contradictory", codexappserver.EndpointAttachRefused},
	} {
		t.Setenv("CODEX_DIAGNOSTIC_MANAGER", manager)
		t.Setenv("CODEX_DIAGNOSTIC_USER_AGENT", row.userAgent)
		health, err := codexappserver.EnsureDefaultProxyReady(context.Background(), codexappserver.TriggerDoctor, "0.15.3", true)
		if err != nil {
			t.Fatalf("%s: %v", row.name, err)
		}
		if health.ManagerEvidence == nil || health.ManagerEvidence.Agreement != row.agreement || codexappserver.AuthorityFor(health).Attach != row.attach {
			t.Fatalf("%s: doctor health = %+v, evidence %+v", row.name, health, health.ManagerEvidence)
		}
		var doctor bytes.Buffer
		writeDoctorAppServerText(&doctor, &health)
		if strings.Contains(doctor.String(), "agreement insufficient") || strings.Contains(doctor.String(), "secret") {
			t.Fatalf("%s: doctor text:\n%s", row.name, doctor.String())
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

	golden := filepath.Join("testdata", "codex_projmux_user_agent_doctor.golden")
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
		t.Fatalf("doctor agreement golden mismatch (UPDATE_GOLDEN=1 rewrites it)\n--- got ---\n%s\n--- want ---\n%s", out.Bytes(), want)
	}
}
