package app

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

func TestDoctorAndSupportReportProjectSecretFreeCodexAppServerHealth(t *testing.T) {
	doctor := newStubDoctorCommand("linux", map[string]bool{"tmux": true, "git": true, "stty": true})
	var triggers []codexappserver.TriggerKind
	doctor.appServerHealth = func(trigger codexappserver.TriggerKind, hookAvailable bool) codexappserver.Health {
		triggers = append(triggers, trigger)
		health := codexappserver.Decide(
			codexappserver.AvailabilityUnsupported,
			codexappserver.ReasonUnsupported,
			"codex-cli/0.149.0",
			codexappserver.EndpointStdioProxy,
			codexappserver.ConnectionDisconnected,
			hookAvailable,
		)
		health.Lifecycle = codexappserver.LifecycleNotAttempted
		health.LifecycleReason = codexappserver.LifecycleReasonReadOnly
		return health
	}
	doctor.aiDiagnostics = func() []doctorAINotifyIntegration {
		enabled := true
		return []doctorAINotifyIntegration{{
			ProviderID:      "codex",
			ProviderEnabled: &enabled,
			Status:          doctorAINotifyStatusInstalled,
		}}
	}
	report := doctor.evaluateReport(doctorSectionIntegrations)
	if !reflect.DeepEqual(triggers, []codexappserver.TriggerKind{codexappserver.TriggerDoctor}) {
		t.Fatalf("doctor triggers = %#v", triggers)
	}
	var text bytes.Buffer
	if err := writeDoctorText(&text, report, doctorSectionIntegrations, true); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Codex app-server", "Hook fallback", "unsupported", "stdio-proxy", "codex-cli/0.149.0", "not-attempted/read-only"} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("doctor output missing %q:\n%s", want, text.String())
		}
	}

	diagnostics := newDiagnosticsCommand()
	diagnostics.doctor = doctor
	data, err := diagnostics.supportDoctorJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(triggers, []codexappserver.TriggerKind{codexappserver.TriggerDoctor, codexappserver.TriggerSupportReport}) {
		t.Fatalf("doctor/support triggers = %#v", triggers)
	}
	for _, want := range []string{`"source": "hook-fallback"`, `"reason": "unsupported"`, `"version": "codex-cli/0.149.0"`, `"lifecycle_outcome": "not-attempted"`, `"lifecycle_reason": "read-only"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("support doctor JSON missing %q:\n%s", want, data)
		}
	}
	for _, forbidden := range []string{"prompt=", "token=", "/home/", "socket_path"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("support doctor JSON leaked %q:\n%s", forbidden, data)
		}
	}
}

func TestSettingsCodexAppServerHealthIsReadOnlyStateRow(t *testing.T) {
	home := t.TempDir()
	cmd := &settingsCommand{
		ai:        testAICommand(home),
		homeDir:   func() (string, error) { return home, nil },
		lookupEnv: func(string) string { return "" },
		appServerHealth: func(hookAvailable bool) codexappserver.Health {
			health := codexappserver.Decide(
				codexappserver.AvailabilityTimeout,
				codexappserver.ReasonTimeout,
				"",
				codexappserver.EndpointStdioProxy,
				codexappserver.ConnectionTimedOut,
				hookAvailable,
			)
			health.Lifecycle = codexappserver.LifecycleNotAttempted
			health.LifecycleReason = codexappserver.LifecycleReasonReadOnly
			return health
		},
		aiNotifyDiagnostics: func() []doctorAINotifyIntegration {
			enabled := true
			return []doctorAINotifyIntegration{{
				ProviderID:      "codex",
				ProviderEnabled: &enabled,
				Status:          doctorAINotifyStatusInstalled,
			}}
		},
	}
	entries := cmd.aiRootEntries()
	var found bool
	for _, entry := range entries {
		if !strings.Contains(entry.Label, "Codex control plane") {
			continue
		}
		found = true
		if entry.Value != settingsNoopValue {
			t.Fatalf("health row value = %q, want read-only noop", entry.Value)
		}
		for _, want := range []string{"Hook fallback", "timed-out", "timeout", "not-attempted/read-only"} {
			if !strings.Contains(entry.Label, want) {
				t.Fatalf("health row missing %q: %s", want, entry.Label)
			}
		}
	}
	if !found {
		t.Fatalf("AI root entries missing Codex health row: %#v", entries)
	}
}

func TestCodexHookFallbackAvailabilityUsesExistingDiagnostic(t *testing.T) {
	enabled := true
	disabled := false
	tests := []struct {
		name        string
		diagnostics []doctorAINotifyIntegration
		want        bool
	}{
		{
			name: "installed and enabled",
			diagnostics: []doctorAINotifyIntegration{{
				ProviderID: "codex", ProviderEnabled: &enabled, Status: doctorAINotifyStatusInstalled,
			}},
			want: true,
		},
		{
			name: "installed but disabled",
			diagnostics: []doctorAINotifyIntegration{{
				ProviderID: "codex", ProviderEnabled: &disabled, Status: doctorAINotifyStatusInstalled,
			}},
		},
		{
			name: "stale",
			diagnostics: []doctorAINotifyIntegration{{
				ProviderID: "codex", ProviderEnabled: &enabled, Status: doctorAINotifyStatusStale,
			}},
		},
		{
			name: "missing",
			diagnostics: []doctorAINotifyIntegration{{
				ProviderID: "codex", ProviderEnabled: &enabled, Status: doctorAINotifyStatusMissing,
			}},
		},
		{
			name: "conflict",
			diagnostics: []doctorAINotifyIntegration{{
				ProviderID: "codex", ProviderEnabled: &enabled, Status: doctorAINotifyStatusConflict,
			}},
		},
		{name: "no Codex diagnostic"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := codexHookFallbackAvailable(tt.diagnostics); got != tt.want {
				t.Fatalf("codexHookFallbackAvailable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSettingsCodexAppServerUnavailableWithoutInstalledHook(t *testing.T) {
	home := t.TempDir()
	cmd := &settingsCommand{
		ai:                  testAICommand(home),
		homeDir:             func() (string, error) { return home, nil },
		lookupEnv:           func(string) string { return "" },
		aiNotifyDiagnostics: func() []doctorAINotifyIntegration { return nil },
		appServerHealth: func(hookAvailable bool) codexappserver.Health {
			return codexappserver.Decide(
				codexappserver.AvailabilityUnavailable,
				codexappserver.ReasonEndpointUnavailable,
				"",
				codexappserver.EndpointStdioProxy,
				codexappserver.ConnectionDisconnected,
				hookAvailable,
			)
		},
	}
	for _, entry := range cmd.aiRootEntries() {
		if strings.Contains(entry.Label, "Codex control plane") {
			if !strings.Contains(entry.Label, "Unavailable") {
				t.Fatalf("health row = %q, want Unavailable", entry.Label)
			}
			return
		}
	}
	t.Fatal("AI root entries missing Codex health row")
}

func TestSupportVersionAllowlistRejectsPathAndTokenStrings(t *testing.T) {
	for _, unsafe := range []string{"/home/me/secret", "token-secret", "codex-cli/0.149.0/path"} {
		value := map[string]any{"codex_app_server": map[string]any{"version": unsafe}}
		redactDoctorJSON(value, "")
		got, _ := value["codex_app_server"].(map[string]any)["version"].(string)
		if got == unsafe || !strings.HasPrefix(got, "sha256:") {
			t.Fatalf("version %q redacted to %q", unsafe, got)
		}
	}
	value := map[string]any{"codex_app_server": map[string]any{"version": "codex-cli/0.149.0"}}
	redactDoctorJSON(value, "")
	if got := value["codex_app_server"].(map[string]any)["version"]; got != "codex-cli/0.149.0" {
		t.Fatalf("safe version redacted to %q", got)
	}
	arbitrary := map[string]any{"runtime": map[string]any{"version": "0.149.0"}}
	redactDoctorJSON(arbitrary, "")
	if got := arbitrary["runtime"].(map[string]any)["version"].(string); !strings.HasPrefix(got, "sha256:") {
		t.Fatalf("arbitrary version escaped redaction: %q", got)
	}
}
