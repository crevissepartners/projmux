package app

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

func TestCodexInstallCapabilityGuidanceMatrixStatesOnlyObservedFacts(t *testing.T) {
	tests := []struct {
		name       string
		capability codexappserver.InstallCapability
		want       string
	}{
		{
			name:       "managed ready",
			capability: codexappserver.InstallCapabilityManagedReady,
			want:       "The managed standalone Codex payload was observed.",
		},
		{
			name:       "ordinary CLI without observed managed payload",
			capability: codexappserver.InstallCapabilityExternalCLIOnly,
			want:       "The ordinary Codex CLI exists; the managed standalone payload was not observed.",
		},
		{
			name:       "CLI missing",
			capability: codexappserver.InstallCapabilityCLIMissing,
			want:       "The ordinary Codex CLI was not observed on PATH.",
		},
		{
			name:       "unknown",
			capability: codexappserver.InstallCapabilityUnknown,
			want:       "Codex install capability could not be determined from read-only observation.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			guidance := codexInstallCapabilityGuidance(tt.capability)
			if guidance.Capability != tt.capability || guidance.Observation != tt.want {
				t.Fatalf("guidance = %+v, want capability %q and observation %q", guidance, tt.capability, tt.want)
			}
			if guidance.Reference != "Official Codex CLI capability guidance: "+codexInstallCapabilityGuidanceURL {
				t.Fatalf("reference = %q", guidance.Reference)
			}
			for _, forbidden := range []string{"npm", "curl", "brew", "reinstall", "installed by"} {
				if strings.Contains(strings.ToLower(guidance.Text()), forbidden) {
					t.Fatalf("guidance inferred installer or mutation %q: %q", forbidden, guidance.Text())
				}
			}
			if (tt.capability == codexappserver.InstallCapabilityManagedReady || tt.capability == codexappserver.InstallCapabilityUnknown) &&
				strings.Contains(strings.ToLower(guidance.Text()), "not observed") {
				t.Fatalf("%s guidance implied payload absence: %q", tt.capability, guidance.Text())
			}
		})
	}

	unknown := codexInstallCapabilityGuidance(codexappserver.InstallCapability("future-value"))
	if unknown.Capability != codexappserver.InstallCapabilityUnknown || strings.Contains(strings.ToLower(unknown.Text()), "not observed") {
		t.Fatalf("unrecognized capability did not fail closed to unknown facts: %+v", unknown)
	}
}

func TestCodexInstallCapabilityGuidanceHasThreeConsumerParity(t *testing.T) {
	tests := []codexappserver.InstallCapability{
		codexappserver.InstallCapabilityManagedReady,
		codexappserver.InstallCapabilityExternalCLIOnly,
		codexappserver.InstallCapabilityCLIMissing,
		codexappserver.InstallCapabilityUnknown,
	}
	for _, capability := range tests {
		t.Run(string(capability), func(t *testing.T) {
			health := codexappserver.Decide(
				codexappserver.AvailabilityUnavailable,
				codexappserver.ReasonDaemonNotRunning,
				"",
				codexappserver.EndpointStdioProxy,
				codexappserver.ConnectionDisconnected,
				false,
			)
			health.InstallCapability = capability
			guidance := codexInstallCapabilityGuidance(capability).Text()

			var doctor bytes.Buffer
			writeDoctorAppServerText(&doctor, &health)

			home := t.TempDir()
			settings := &settingsCommand{
				ai:                  testAICommand(home),
				homeDir:             func() (string, error) { return home, nil },
				lookupEnv:           func(string) string { return "" },
				aiNotifyDiagnostics: func() []doctorAINotifyIntegration { return nil },
				appServerHealth:     func(bool) codexappserver.Health { return health },
			}
			var settingsText string
			for _, entry := range settings.aiRootEntries() {
				if strings.Contains(entry.Label, "Codex control plane") {
					settingsText = entry.Label
				}
			}

			refusal := nativeCreatePreparationRefusalForCapability(
				"create codex",
				&codexappserver.ThreadActionError{Reason: string(codexappserver.ReasonDaemonNotRunning), SafeFallback: true},
				capability,
			)
			if refusal == nil {
				t.Fatal("native create refusal = nil")
			}
			consumers := map[string]string{
				"Doctor":        doctor.String(),
				"Settings":      settingsText,
				"native create": refusal.Error(),
			}
			for name, output := range consumers {
				if strings.Count(output, guidance) != 1 {
					t.Fatalf("%s guidance parity count = %d, want 1:\n%s", name, strings.Count(output, guidance), output)
				}
			}
			if !strings.Contains(refusal.Error(), interactiveOnlyFlag) ||
				!strings.Contains(refusal.Error(), codexInstallCapabilityGuidanceURL) {
				t.Fatalf("native create refusal is not actionable: %v", refusal)
			}
		})
	}
}

func TestCodexInstallCapabilityConsumersCarryNoSurfaceLocalCopyOrURL(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source")
	}
	dir := filepath.Dir(current)
	forbiddenCopy := []string{
		"ordinary codex cli",
		"payload was observed",
		"payload was not observed",
		"install capability could not be determined",
		"codex cli capability guidance",
		"learn.chatgpt.com",
	}
	for _, name := range []string{"doctor.go", "settings_ai.go", "codex_native_thread.go"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		lower := strings.ToLower(text)
		for _, copy := range forbiddenCopy {
			if strings.Contains(lower, copy) {
				t.Fatalf("%s carries surface-local capability observation/action copy %q", name, copy)
			}
		}
		if !strings.Contains(text, "codexInstallCapabilityGuidance(") {
			t.Fatalf("%s does not consume the shared typed guidance authority", name)
		}
	}
}

func TestNativeCreateGuidanceNilErrorRemainsNil(t *testing.T) {
	if err := nativeCreatePreparationRefusalForCapability("create codex", nil, codexappserver.InstallCapabilityExternalCLIOnly); err != nil {
		t.Fatalf("nil native error = %v", err)
	}
}
