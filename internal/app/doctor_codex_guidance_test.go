package app

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

func TestDoctorCodexRecoveryGuidanceTextJSONSettingsSupportParity(t *testing.T) {
	tests := []struct {
		name      string
		ownership codexappserver.ManagerOwnership
		recovery  codexappserver.OperatorRecovery
	}{
		{"unmanaged", codexappserver.ManagerUnmanaged, codexappserver.OperatorRecoveryStopOwnerThenStart},
		{"managed current", codexappserver.ManagerManaged, codexappserver.OperatorRecoveryNone},
		{"managed skew", codexappserver.ManagerManaged, codexappserver.OperatorRecoveryRestartManagedDaemon},
		{"ownership unknown", codexappserver.ManagerUnknown, codexappserver.OperatorRecoveryInspectProcessOwnership},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			health := codexappserver.Decide(codexappserver.AvailabilityAvailable, codexappserver.ReasonNone, "0.153.4", codexappserver.EndpointStdioProxy, codexappserver.ConnectionReady, true)
			health.ManagerOwnership = tt.ownership
			health.OperatorRecovery = tt.recovery
			doctor := newStubDoctorCommand("linux", map[string]bool{"tmux": true, "git": true, "stty": true})
			doctor.aiDiagnostics = func() []doctorAINotifyIntegration { return nil }
			doctor.appServerHealth = func(codexappserver.TriggerKind, bool) codexappserver.Health { return health }
			report := doctor.evaluateReport(doctorSectionIntegrations)
			var text, plainJSON bytes.Buffer
			if err := writeDoctorText(&text, report, doctorSectionIntegrations, true); err != nil {
				t.Fatal(err)
			}
			if err := writeDoctorJSON(&plainJSON, report, doctorSectionIntegrations); err != nil {
				t.Fatal(err)
			}
			guidance := health.OperatorRecovery.Guidance()
			if guidance != "" && !strings.Contains(text.String(), "Guidance: "+guidance) {
				t.Fatalf("Doctor lost recovery guidance %q: %s", guidance, text.String())
			}
			for _, prescription := range []string{"codex app-server daemon bootstrap", "The observed `pid` backend requires bootstrap again after reboot."} {
				if got := strings.Contains(text.String(), prescription); got != (tt.ownership == codexappserver.ManagerUnmanaged) {
					t.Fatalf("%s bootstrap prescription %q presence = %t: %s", tt.ownership, prescription, got, text.String())
				}
			}

			home := t.TempDir()
			settings := &settingsCommand{
				ai: testAICommand(home), homeDir: func() (string, error) { return home, nil },
				lookupEnv: func(string) string { return "" }, aiNotifyDiagnostics: func() []doctorAINotifyIntegration { return nil },
				appServerHealth: func(bool) codexappserver.Health { return health },
			}
			found := false
			for _, entry := range settings.aiRootEntries() {
				if !strings.Contains(entry.Label, "Codex control plane") {
					continue
				}
				found = true
				if entry.Value != settingsNoopValue || !strings.Contains(entry.Label, "manager: "+string(tt.ownership)) || !strings.Contains(entry.Label, "recovery: "+string(tt.recovery)) {
					t.Fatalf("Settings lost read-only ownership/recovery projection: %+v", entry)
				}
			}
			if !found {
				t.Fatal("Settings omitted the Codex control plane row")
			}

			diagnostics := newDiagnosticsCommand()
			diagnostics.doctor = doctor
			support, err := diagnostics.supportDoctorJSON()
			if err != nil {
				t.Fatal(err)
			}
			// Structured surfaces retain their existing recovery code; resolving
			// it yields the same sentence without adding a schema field.
			for name, data := range map[string][]byte{"Doctor JSON": plainJSON.Bytes(), "support doctor.json": support} {
				var decoded doctorReport
				if err := json.Unmarshal(data, &decoded); err != nil {
					t.Fatal(err)
				}
				if decoded.SchemaVersion != 2 || !reflect.DeepEqual(decoded.CodexAppServer, &health) {
					t.Fatalf("%s changed the existing health projection: %+v", name, decoded.CodexAppServer)
				}
				if got := decoded.CodexAppServer.OperatorRecovery.Guidance(); got != guidance {
					t.Fatalf("%s recovery code resolves to %q, want %q", name, got, guidance)
				}
			}
		})
	}
}
