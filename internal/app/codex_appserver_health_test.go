package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
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
		health.InstallCapability = codexappserver.InstallCapabilityExternalCLIOnly
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
	for _, want := range []string{"Codex app-server", "Hook fallback", "reason: unsupported", "stdio-proxy", "codex-cli/0.149.0", "not-attempted/read-only", "App-server probe: unsupported", "install capability: external-cli-only"} {
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
	for _, want := range []string{`"source": "hook-fallback"`, `"reason": "unsupported"`, `"probe_reason": "unsupported"`, `"install_capability": "external-cli-only"`, `"version": "codex-cli/0.149.0"`, `"lifecycle_outcome": "not-attempted"`, `"lifecycle_reason": "read-only"`} {
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

func TestDoctorCodexAppServerJSONSchemaV2IsAdditive(t *testing.T) {
	health := codexappserver.Decide(
		codexappserver.AvailabilityUnavailable,
		codexappserver.ReasonDaemonNotRunning,
		"",
		codexappserver.EndpointStdioProxy,
		codexappserver.ConnectionDisconnected,
		false,
	)
	health.InstallCapability = codexappserver.InstallCapabilityExternalCLIOnly
	health.Lifecycle = codexappserver.LifecycleNotAttempted
	health.LifecycleReason = codexappserver.LifecycleReasonReadOnly
	report := doctorReport{SchemaVersion: doctorSchemaVersion, CodexAppServer: &health}
	var output bytes.Buffer
	if err := writeDoctorJSON(&output, report, doctorSectionIntegrations); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if got := string(decoded["schema_version"]); got != "2" {
		t.Fatalf("schema_version = %s", got)
	}
	var fields map[string]any
	if err := json.Unmarshal(decoded["codex_app_server"], &fields); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"source":             "unavailable",
		"availability":       "unavailable",
		"reason":             "hook-unavailable",
		"probe_reason":       "daemon-not-running",
		"install_capability": "external-cli-only",
		"endpoint_kind":      "stdio-proxy",
		"connection_state":   "disconnected",
		"lifecycle_outcome":  "not-attempted",
		"lifecycle_reason":   "read-only",
	}
	for field, value := range want {
		if fields[field] != value {
			t.Fatalf("codex_app_server.%s = %#v, want %q; fields=%#v", field, fields[field], value, fields)
		}
	}
}

func TestDoctorSettingsAndSupportKeepUnsafeReadinessAxesIndependent(t *testing.T) {
	health := codexappserver.Decide(
		codexappserver.AvailabilityAvailable,
		codexappserver.ReasonNone,
		"codex-cli/0.149.1",
		codexappserver.EndpointStdioProxy,
		codexappserver.ConnectionReady,
		true,
	)
	health.EndpointReadiness = codexappserver.EndpointReady
	health.RunningExecutable = codexappserver.RunningExecutableUnknown
	health.VersionRelation = codexappserver.VersionSkew
	health.CLIVersion = "0.150.1"
	health.ManagedVersion = "0.150.1"
	health.RunningVersion = "0.149.1"
	health.ManagerOwnership = codexappserver.ManagerUnmanaged
	health.RemoteControl = codexappserver.RemoteControlDisabled
	health.NativeAction = codexappserver.NativeActionRefused
	health.NativeRefusal = codexappserver.NativeActionRefusalUnmanagedVersionSkew
	health.InterruptionRisk = codexappserver.InterruptionRiskSharedClients
	health.OperatorRecovery = codexappserver.OperatorRecoveryStopOwnerThenStart
	health.Lifecycle = codexappserver.LifecycleNotAttempted
	health.LifecycleReason = codexappserver.LifecycleReasonReadOnly

	var doctorText bytes.Buffer
	writeDoctorAppServerText(&doctorText, &health)
	for _, want := range []string{
		"Endpoint readiness: ready", "running executable: unknown", "version relation: skew",
		"manager ownership: unmanaged", "remote control: disabled", "CLI 0.150.1",
		"running 0.149.1", "Native action: refused", "unmanaged-version-skew",
		"shared-clients-disconnect", "stop-owner-then-start-managed-daemon",
		"Close every sharing Codex client", "Projmux will not kill or restart it",
	} {
		if !strings.Contains(doctorText.String(), want) {
			t.Fatalf("Doctor output missing %q:\n%s", want, doctorText.String())
		}
	}

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
	for _, want := range []string{"endpoint: ready", "version: skew", "manager: unmanaged", "remote control: disabled", "native action: refused/unmanaged-version-skew"} {
		if !strings.Contains(settingsText, want) {
			t.Fatalf("Settings output missing %q: %s", want, settingsText)
		}
	}

	doctor := newStubDoctorCommand("linux", map[string]bool{"tmux": true, "git": true, "stty": true})
	doctor.aiDiagnostics = func() []doctorAINotifyIntegration { return nil }
	doctor.appServerHealth = func(codexappserver.TriggerKind, bool) codexappserver.Health { return health }
	diagnostics := newDiagnosticsCommand()
	diagnostics.doctor = doctor
	support, err := diagnostics.supportDoctorJSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"endpoint_readiness": "ready"`, `"version_relation": "skew"`, `"manager_ownership": "unmanaged"`, `"remote_control_capability": "disabled"`, `"native_action_refusal": "unmanaged-version-skew"`, `"running_version": "0.149.1"`} {
		if !strings.Contains(string(support), want) {
			t.Fatalf("support output missing %q:\n%s", want, support)
		}
	}
}

func TestDoctorAndSettingsCodexTopologyProjectionMatrix(t *testing.T) {
	tests := []struct {
		name         string
		availability codexappserver.Availability
		probeReason  codexappserver.Reason
		capability   codexappserver.InstallCapability
		hook         bool
		want         []string
	}{
		{name: "external only missing endpoint", availability: codexappserver.AvailabilityUnavailable, probeReason: codexappserver.ReasonDaemonNotRunning, capability: codexappserver.InstallCapabilityExternalCLIOnly, want: []string{"Unavailable", "hook-unavailable", "daemon-not-running", "external-cli-only"}},
		{name: "managed ready", availability: codexappserver.AvailabilityAvailable, probeReason: codexappserver.ReasonNone, capability: codexappserver.InstallCapabilityManagedReady, want: []string{"App Server", "reason: none", "probe: none", "managed-ready"}},
		{name: "external ready", availability: codexappserver.AvailabilityAvailable, probeReason: codexappserver.ReasonNone, capability: codexappserver.InstallCapabilityExternalCLIOnly, want: []string{"App Server", "reason: none", "probe: none", "external-cli-only"}},
		{name: "CLI missing", availability: codexappserver.AvailabilityUnavailable, probeReason: codexappserver.ReasonExecutableMissing, capability: codexappserver.InstallCapabilityCLIMissing, want: []string{"Unavailable", "hook-unavailable", "executable-missing", "cli-missing"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			connection := codexappserver.ConnectionDisconnected
			if tt.availability == codexappserver.AvailabilityAvailable {
				connection = codexappserver.ConnectionReady
			}
			health := codexappserver.Decide(tt.availability, tt.probeReason, "", codexappserver.EndpointStdioProxy, connection, tt.hook)
			health.InstallCapability = tt.capability
			health.Lifecycle = codexappserver.LifecycleNotAttempted
			health.LifecycleReason = codexappserver.LifecycleReasonReadOnly

			var doctorText bytes.Buffer
			writeDoctorAppServerText(&doctorText, &health)
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
					if entry.Value != settingsNoopValue {
						t.Fatalf("Settings health row is not read-only: %q", entry.Value)
					}
				}
			}
			combined := doctorText.String() + "\n" + settingsText
			for _, value := range tt.want {
				if !strings.Contains(combined, value) {
					t.Fatalf("projection missing %q:\n%s", value, combined)
				}
			}
		})
	}
}

func TestDoctorAndSettingsReadOnlyTopologyNeverStartOrWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is POSIX-only")
	}
	tests := []struct {
		name       string
		managed    bool
		missingCLI bool
		want       codexappserver.InstallCapability
		wantProbe  codexappserver.Reason
	}{
		{name: "external only", want: codexappserver.InstallCapabilityExternalCLIOnly, wantProbe: codexappserver.ReasonDaemonNotRunning},
		{name: "managed standalone", managed: true, want: codexappserver.InstallCapabilityManagedReady, wantProbe: codexappserver.ReasonDaemonNotRunning},
		{name: "CLI missing", missingCLI: true, want: codexappserver.InstallCapabilityCLIMissing, wantProbe: codexappserver.ReasonExecutableMissing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			codexHome := filepath.Join(root, "codex-home")
			if err := os.MkdirAll(codexHome, 0o700); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(codexHome, "user-state-sentinel")
			if err := os.WriteFile(marker, []byte("must-remain-byte-identical"), 0o600); err != nil {
				t.Fatal(err)
			}
			if tt.managed {
				payload := filepath.Join(codexHome, "packages", "standalone", "current", "bin", "codex")
				if err := os.MkdirAll(filepath.Dir(payload), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(payload, []byte("managed"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			pathDir := filepath.Join(root, "path")
			if err := os.MkdirAll(pathDir, 0o700); err != nil {
				t.Fatal(err)
			}
			invocations := filepath.Join(root, "codex-invocations")
			if !tt.missingCLI {
				script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$PROJMUX_FAKE_CODEX_INVOCATIONS\"\nexit 0\n"
				if err := os.WriteFile(filepath.Join(pathDir, "codex"), []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("CODEX_HOME", codexHome)
			t.Setenv("PATH", pathDir)
			t.Setenv("PROJMUX_FAKE_CODEX_INVOCATIONS", invocations)
			before := snapshotCodexHealthTree(t, codexHome)

			doctor := newStubDoctorCommand("linux", map[string]bool{"tmux": true, "git": true, "stty": true})
			doctor.aiDiagnostics = func() []doctorAINotifyIntegration { return nil }
			doctor.appServerHealth = func(trigger codexappserver.TriggerKind, hookAvailable bool) codexappserver.Health {
				health, err := codexappserver.EnsureDefaultProxyReady(context.Background(), trigger, "0.13.0", hookAvailable)
				if err != nil {
					t.Fatal(err)
				}
				return health
			}
			report := doctor.evaluateReport(doctorSectionIntegrations)
			if report.CodexAppServer == nil || report.CodexAppServer.InstallCapability != tt.want || report.CodexAppServer.ProbeReason != tt.wantProbe || report.CodexAppServer.LifecycleReason != codexappserver.LifecycleReasonReadOnly {
				t.Fatalf("Doctor health = %+v", report.CodexAppServer)
			}

			settings := &settingsCommand{
				ai:                  testAICommand(root),
				homeDir:             func() (string, error) { return root, nil },
				lookupEnv:           os.Getenv,
				aiNotifyDiagnostics: func() []doctorAINotifyIntegration { return nil },
				appServerHealth: func(hookAvailable bool) codexappserver.Health {
					health, err := codexappserver.EnsureDefaultProxyReady(context.Background(), codexappserver.TriggerSettings, "0.13.0", hookAvailable)
					if err != nil {
						t.Fatal(err)
					}
					return health
				},
			}
			_ = settings.aiRootEntries()
			diagnostics := newDiagnosticsCommand()
			diagnostics.doctor = doctor
			if _, err := diagnostics.supportDoctorJSON(); err != nil {
				t.Fatal(err)
			}

			after := snapshotCodexHealthTree(t, codexHome)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("Doctor/Settings changed CODEX_HOME:\nbefore=%#v\nafter=%#v", before, after)
			}
			calls, err := os.ReadFile(invocations)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			for _, mutation := range []string{
				"daemon start", "daemon stop", "daemon restart", "daemon kill",
				"enable-remote-control", "disable-remote-control", "daemon bootstrap",
				"login", "logout", "config set", "config write",
			} {
				if strings.Contains(string(calls), mutation) {
					t.Fatalf("read-only surface emitted mutation argv %q: %s", mutation, calls)
				}
			}
			if tt.missingCLI {
				if len(calls) != 0 {
					t.Fatalf("missing CLI emitted invocations: %q", calls)
				}
				return
			}
			observed := map[string]bool{}
			for invocation := range strings.SplitSeq(strings.TrimSpace(string(calls)), "\n") {
				switch invocation {
				case "app-server proxy", "app-server daemon version":
					observed[invocation] = true
				default:
					t.Fatalf("read-only topology surface emitted non-observation argv %q; all=%q", invocation, calls)
				}
			}
			for _, required := range []string{"app-server proxy", "app-server daemon version"} {
				if !observed[required] {
					t.Fatalf("read-only topology never crossed required %q observation boundary; all=%q", required, calls)
				}
			}
		})
	}
}

func snapshotCodexHealthTree(t *testing.T, root string) []string {
	t.Helper()
	var snapshot []string
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		digest := "directory"
		if !entry.IsDir() {
			contents, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			digest = fmt.Sprintf("%x", sha256.Sum256(contents))
		}
		snapshot = append(snapshot, fmt.Sprintf("%s|%s|%d|%s", relative, info.Mode(), info.Size(), digest))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(snapshot)
	return snapshot
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
			health.InstallCapability = codexappserver.InstallCapabilityExternalCLIOnly
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
		for _, want := range []string{"Hook fallback", "timed-out", "timeout", "probe: timeout", "install: external-cli-only", "not-attempted/read-only"} {
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

func TestSupportCodexHealthAllowlistKeepsClosedAxesIndependent(t *testing.T) {
	value := map[string]any{"codex_app_server": map[string]any{
		"reason":             "hook-unavailable",
		"probe_reason":       "hook-unavailable",
		"install_capability": "external-cli-only",
		"lifecycle_reason":   "start-managed-payload-missing",
	}}
	redactDoctorJSON(value, "")
	health := value["codex_app_server"].(map[string]any)
	if health["reason"] != "hook-unavailable" {
		t.Fatalf("effective reason was not allowlisted: %#v", health)
	}
	if got := health["probe_reason"].(string); !strings.HasPrefix(got, "sha256:") {
		t.Fatalf("effective-only reason escaped probe allowlist: %q", got)
	}
	if health["install_capability"] != "external-cli-only" || health["lifecycle_reason"] != "start-managed-payload-missing" {
		t.Fatalf("closed topology axes were not allowlisted: %#v", health)
	}

	for _, reason := range []codexappserver.Reason{
		codexappserver.ReasonNone,
		codexappserver.ReasonExecutableMissing,
		codexappserver.ReasonDaemonNotRunning,
		codexappserver.ReasonEndpointUnavailable,
		codexappserver.ReasonUnsupported,
		codexappserver.ReasonTimeout,
		codexappserver.ReasonProtocolError,
		codexappserver.ReasonDisconnected,
	} {
		value := map[string]any{"codex_app_server": map[string]any{"probe_reason": string(reason)}}
		redactDoctorJSON(value, "")
		if got := value["codex_app_server"].(map[string]any)["probe_reason"]; got != string(reason) {
			t.Fatalf("closed producer probe reason %q was redacted to %q", reason, got)
		}
	}
}
