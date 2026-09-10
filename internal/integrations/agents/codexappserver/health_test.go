package codexappserver

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const unmanagedRecoveryGuidance = "This app server is not daemon-managed. Close every sharing Codex client, then run `codex app-server daemon bootstrap` and rerun diagnostics. The observed `pid` backend requires bootstrap again after reboot. Projmux will not kill or restart it."

func TestOperatorRecoveryGuidanceOwnershipMatrix(t *testing.T) {
	tests := []struct {
		name      string
		ownership ManagerOwnership
		relation  VersionRelation
		action    NativeActionReadiness
		refusal   NativeActionRefusal
		recovery  OperatorRecovery
	}{
		{"unmanaged current", ManagerUnmanaged, VersionCurrent, NativeActionRefused, NativeActionRefusalUnmanaged, OperatorRecoveryStopOwnerThenStart},
		{"unmanaged skew", ManagerUnmanaged, VersionSkew, NativeActionRefused, NativeActionRefusalUnmanagedVersionSkew, OperatorRecoveryStopOwnerThenStart},
		{"unmanaged version unknown", ManagerUnmanaged, VersionUnknown, NativeActionRefused, NativeActionRefusalUnmanaged, OperatorRecoveryStopOwnerThenStart},
		{"managed current", ManagerManaged, VersionCurrent, NativeActionReady, NativeActionRefusalNone, OperatorRecoveryNone},
		{"managed skew", ManagerManaged, VersionSkew, NativeActionRefused, NativeActionRefusalVersionSkew, OperatorRecoveryRestartManagedDaemon},
		{"managed version unknown", ManagerManaged, VersionUnknown, NativeActionRefused, NativeActionRefusalRuntimeVersionUnknown, OperatorRecoveryInspectProcessOwnership},
		{"ownership unknown", ManagerUnknown, VersionCurrent, NativeActionRefused, NativeActionRefusalOwnershipUnknown, OperatorRecoveryInspectProcessOwnership},
		{"ownership and version unknown", ManagerUnknown, VersionUnknown, NativeActionRefused, NativeActionRefusalOwnershipUnknown, OperatorRecoveryInspectProcessOwnership},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			health := withManagerObservation(testProbeHealth(AvailabilityAvailable, ReasonNone), managerObservation{
				Ownership: tt.ownership, Relation: tt.relation, Executable: RunningExecutableUnknown,
			})
			if health.EndpointReadiness != EndpointReady || health.NativeAction != tt.action || health.NativeRefusal != tt.refusal || health.OperatorRecovery != tt.recovery {
				t.Fatalf("guidance changed readiness/recovery: %+v", health)
			}
			guidance := health.OperatorRecovery.Guidance()
			if tt.ownership == ManagerUnmanaged {
				if guidance != unmanagedRecoveryGuidance {
					t.Fatalf("unmanaged guidance = %q, want %q", guidance, unmanagedRecoveryGuidance)
				}
				if !strings.Contains(health.NativeActionGuidance(), guidance) {
					t.Fatalf("native refusal lost the shared guidance: %s", health.NativeActionGuidance())
				}
				if health.OperatorRecovery != "stop-owner-then-start-managed-daemon" {
					t.Fatalf("public recovery code changed: %s", health.OperatorRecovery)
				}
			} else {
				for _, forbidden := range []string{"bootstrap", "`pid`", "after reboot"} {
					if strings.Contains(guidance, forbidden) || strings.Contains(health.NativeActionGuidance(), forbidden) {
						t.Fatalf("%s received unmanaged prescription %q: %s", tt.ownership, forbidden, guidance)
					}
				}
			}
		})
	}
}

func TestOperatorRecoveryGuidanceReadOnlyArgvAndState(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex-home")
	pathDir := filepath.Join(root, "bin")
	for _, dir := range []string{codexHome, pathDir} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte("# unchanged operator configuration\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	helper, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	ledger := filepath.Join(root, "argv")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$PROJMUX_GUIDANCE_ARGV"
case "$*" in
  'app-server daemon version')
    printf '%s\n' '{"status":"running","cliVersion":"0.149.0","appServerVersion":"0.149.0"}' ;;
  'app-server proxy')
    exec "$PROJMUX_CODEX_PROBE_HELPER" -test.run=^TestProxyProbeHelperProcess$ -- healthy ;;
  *) exit 91 ;;
esac
`
	if err := os.WriteFile(filepath.Join(pathDir, "codex"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("PROJMUX_GUIDANCE_ARGV", ledger)
	t.Setenv("PROJMUX_CODEX_PROBE_HELPER", helper)
	t.Setenv("GO_WANT_PROXY_HELPER", "1")
	before := snapshotTopologyTree(t, codexHome)
	for _, trigger := range []TriggerKind{TriggerDoctor, TriggerSettings, TriggerSupportReport} {
		for range 2 {
			health, err := EnsureDefaultProxyReady(context.Background(), trigger, "0.13.0", true)
			if err != nil {
				t.Fatal(err)
			}
			if health.ManagerOwnership != ManagerUnmanaged || health.OperatorRecovery.Guidance() != unmanagedRecoveryGuidance ||
				health.Lifecycle != LifecycleNotAttempted || health.LifecycleReason != LifecycleReasonReadOnly {
				t.Fatalf("%s did not preserve read-only unmanaged guidance: %+v", trigger, health)
			}
		}
	}
	calls, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatal(err)
	}
	observed := map[string]bool{}
	for call := range strings.SplitSeq(strings.TrimSpace(string(calls)), "\n") {
		switch call {
		case "app-server proxy", "app-server daemon version":
			observed[call] = true
		default:
			t.Fatalf("guidance emitted non-observation argv %q; ledger=%s", call, calls)
		}
	}
	if !observed["app-server proxy"] || !observed["app-server daemon version"] {
		t.Fatalf("guidance audit missed a read-only boundary: %s", calls)
	}
	if !reflect.DeepEqual(before, snapshotTopologyTree(t, codexHome)) {
		t.Fatal("read-only guidance changed Codex state")
	}
	t.Logf("C-2 Doctor/Settings/support observation argv = %q; recovery execution argv = 0; Codex state unchanged", strings.TrimSpace(string(calls)))
}
