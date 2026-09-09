package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

const doctorEndpointFixtureDomain = "codex-state-fixture"

func doctorEndpointFixture(versions ...string) coremetadata.Registry {
	registry := coremetadata.NewRegistry()
	for i, version := range versions {
		uid, paneUID := fmt.Sprintf("agent-fixture-%d", i), fmt.Sprintf("pane-fixture-%d", i)
		endpoint := coremetadata.CodexEndpointRef{StateDomainID: doctorEndpointFixtureDomain, EndpointGenerationID: "codex-" + version}
		registry.Agents = append(registry.Agents, coremetadata.Agent{
			Metadata: coremetadata.ObjectMeta{UID: uid},
			Spec:     coremetadata.AgentSpec{Provider: aiModeCodex},
			Status:   coremetadata.AgentStatus{Phase: coremetadata.PhaseRunning, PaneRef: paneUID},
		})
		registry.Panes = append(registry.Panes, coremetadata.Pane{
			Metadata: coremetadata.ObjectMeta{UID: paneUID, OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindAgent, UID: uid}},
			Status: coremetadata.PaneStatus{Activation: coremetadata.PaneActivation{
				Generation: "gen-fixture", RuntimeID: fmt.Sprintf("%%%d", i), AgentUID: uid,
				Codex: &coremetadata.CodexActivationBinding{Authority: &coremetadata.CodexAuthorityRef{
					StateDomainID: endpoint.StateDomainID, EndpointGenerationID: endpoint.EndpointGenerationID,
				}},
			}},
		})
	}
	return registry
}

func doctorEndpointHealth() codexappserver.Health {
	return codexappserver.Health{EndpointReadiness: codexappserver.EndpointReady, RunningVersion: "0.153.4", CLIVersion: "0.154.0", ManagedVersion: "0.154.0"}
}

func doctorEndpointCommand(registry coremetadata.Registry, health codexappserver.Health) *doctorCommand {
	doctor := newStubDoctorCommand("linux", map[string]bool{"tmux": true, "git": true, "stty": true})
	doctor.readRegistry = func() (coremetadata.Registry, error) { return registry, nil }
	doctor.codexGeneration = func(coremetadata.Registry) *doctorCodexGenerationPool {
		return &doctorCodexGenerationPool{Status: "absent"}
	}
	doctor.codexEndpointDomain = func() (string, error) { return doctorEndpointFixtureDomain, nil }
	doctor.appServerHealth = func(codexappserver.TriggerKind, bool) codexappserver.Health { return health }
	return doctor
}

func TestDoctorCodexEndpointMismatchExactActivationSet(t *testing.T) {
	for _, test := range []struct {
		name     string
		versions []string
		want     []string
	}{
		{name: "mismatch", versions: []string{"0.153.2"}, want: []string{"agent-fixture-0"}},
		{name: "match despite installed CLI skew", versions: []string{"0.153.4"}},
		{name: "mixed", versions: []string{"0.153.2", "0.153.4", "0.153.3"}, want: []string{"agent-fixture-0", "agent-fixture-2"}},
		{name: "empty"},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := doctorEndpointFixture(test.versions...)
			before, _ := json.Marshal(registry)
			health := doctorEndpointHealth()
			got := diagnoseCodexEndpointMismatch(registry, nil, doctorEndpointFixtureDomain, nil, &doctorCodexGenerationPool{Status: "absent"}, &health)
			var uids []string
			if got != nil {
				for _, agent := range got.Mismatches {
					uids = append(uids, agent.AgentUID)
				}
				if got.Status != "complete" || got.RunningVersion != "0.153.4" || got.Risk != doctorCodexEndpointRisk || got.ObservedOn != "2026-09-09" || got.Observations != 1 || got.Causality != "undetermined" {
					t.Fatalf("missing C-1 evidence strength: %+v", got)
				}
			}
			if !reflect.DeepEqual(uids, test.want) || (len(test.want) == 0 && got != nil) {
				t.Fatalf("got %+v, want exact UID set %v", got, test.want)
			}
			after, _ := json.Marshal(registry)
			if !bytes.Equal(before, after) {
				t.Fatal("diagnosis changed Registry activation evidence")
			}
		})
	}
}

func TestDoctorCodexEndpointMismatchIncludesBoundNonRunningActivation(t *testing.T) {
	registry := doctorEndpointFixture("0.153.2", "0.153.2", "0.153.2")
	registry.Agents[0].Status.Phase = coremetadata.PhaseOffline
	// Offline durable history with no activation is outside this snapshot census.
	registry.Agents[1].Status.Phase, registry.Agents[1].Status.PaneRef = coremetadata.PhaseOffline, ""
	registry.Panes[1].Status.Activation = coremetadata.PaneActivation{}
	registry.Agents[2].Spec.Provider = "claude"
	registry.Panes[2].Status.Activation.Codex = nil
	health := doctorEndpointHealth()
	got := diagnoseCodexEndpointMismatch(registry, nil, doctorEndpointFixtureDomain, nil, &doctorCodexGenerationPool{Status: "absent"}, &health)
	if got == nil || got.Status != "complete" || got.Agents != 1 || len(got.Mismatches) != 1 || got.Mismatches[0].AgentUID != "agent-fixture-0" {
		t.Fatalf("wrong activation scope: %+v", got)
	}
}

func TestDoctorCodexEndpointMismatchSignalsEnumerationGaps(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*coremetadata.Registry, *codexappserver.Health)
		reason string
	}{
		{"missing pane", func(r *coremetadata.Registry, _ *codexappserver.Health) { r.Panes = nil }, "activation-missing"},
		{"missing authority", func(r *coremetadata.Registry, _ *codexappserver.Health) {
			r.Panes[0].Status.Activation.Codex.Authority = nil
		}, "activation-endpoint-missing"},
		{"broken ownership", func(r *coremetadata.Registry, _ *codexappserver.Health) {
			r.Panes[0].Metadata.OwnerRef.UID = "agent-other"
		}, "activation-agent-unresolved"},
		{"orphan activation", func(r *coremetadata.Registry, _ *codexappserver.Health) { r.Agents = nil }, "activation-agent-unresolved"},
		{"foreign state domain", func(r *coremetadata.Registry, _ *codexappserver.Health) {
			r.Panes[0].Status.Activation.Codex.Authority.StateDomainID = "another-domain"
		}, "endpoint-domain-unobserved"},
		{"opaque private generation", func(r *coremetadata.Registry, _ *codexappserver.Health) {
			r.Panes[0].Status.Activation.Codex.Authority.EndpointGenerationID = "private-generation-a"
		}, "endpoint-generation-unobserved"},
		{"invalid endpoint", func(r *coremetadata.Registry, _ *codexappserver.Health) {
			r.Panes[0].Status.Activation.Codex.Authority.EndpointGenerationID = ""
		}, "activation-endpoint-invalid"},
		{"durable endpoint disagreement", func(r *coremetadata.Registry, _ *codexappserver.Health) {
			r.Agents[0].Status.SessionRef = &coremetadata.AgentSessionRef{Provider: aiModeCodex, Codex: &coremetadata.CodexSessionRef{
				Endpoint: &coremetadata.CodexEndpointRef{StateDomainID: doctorEndpointFixtureDomain, EndpointGenerationID: "codex-0.153.4"},
			}}
		}, "activation-endpoint-conflict"},
		{"missing running version", func(_ *coremetadata.Registry, h *codexappserver.Health) { h.RunningVersion = "" }, "running-endpoint-unavailable"},
		{"endpoint unavailable", func(_ *coremetadata.Registry, h *codexappserver.Health) {
			h.EndpointReadiness = codexappserver.EndpointUnavailable
		}, "running-endpoint-unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry, health := doctorEndpointFixture("0.153.2"), doctorEndpointHealth()
			test.change(&registry, &health)
			got := diagnoseCodexEndpointMismatch(registry, nil, doctorEndpointFixtureDomain, nil, &doctorCodexGenerationPool{Status: "absent"}, &health)
			if got == nil || got.Status != "incomplete" || len(got.Mismatches) != 0 || len(got.UnobservedAgents) != 1 || got.UnobservedAgents[0].Reason != test.reason {
				t.Fatalf("missing enumeration signal: %+v", got)
			}
			var text bytes.Buffer
			writeDoctorCodexEndpointMismatchText(&text, got)
			if !strings.Contains(text.String(), "mismatch enumeration may be incomplete") || !strings.Contains(text.String(), test.reason) {
				t.Fatalf("operator cannot see gap: %s", text.String())
			}
		})
	}
	registry, health := doctorEndpointFixture("0.153.2"), doctorEndpointHealth()
	for _, test := range []struct {
		registryErr, domainErr error
		reason                 string
	}{
		{registryErr: errors.New("private path failure"), reason: "registry-unavailable"},
		{domainErr: errors.New("private path failure"), reason: "state-domain-unavailable"},
	} {
		got := diagnoseCodexEndpointMismatch(registry, test.registryErr, doctorEndpointFixtureDomain, test.domainErr, &doctorCodexGenerationPool{Status: "absent"}, &health)
		if got == nil || got.Reason != test.reason || len(got.Mismatches) > 0 {
			t.Fatalf("missing read failure: %+v", got)
		}
	}
}

func TestDoctorCodexEndpointMismatchDoesNotGuessPrivateGenerationRouting(t *testing.T) {
	for _, pool := range []*doctorCodexGenerationPool{
		nil,
		{Status: "blocked", Reason: "invalid-admission-tuple"},
		{Status: "ready", StateDomainID: doctorEndpointFixtureDomain, CurrentGenerationID: "codex-0.153.2",
			Generations: []doctorCodexGeneration{{GenerationID: "codex-0.153.2", Owner: "projmux-private", Version: "0.153.2"}}},
	} {
		doctor := doctorEndpointCommand(doctorEndpointFixture("0.153.2"), doctorEndpointHealth())
		doctor.codexGeneration = func(coremetadata.Registry) *doctorCodexGenerationPool { return pool }
		got := doctor.evaluateReport(doctorSectionIntegrations).CodexEndpointRisks
		if got == nil || got.Status != "incomplete" || len(got.Mismatches) != 0 || got.UnobservedAgents[0].Reason != "endpoint-routing-unobserved" {
			t.Fatalf("default daemon version became private endpoint evidence: pool=%+v report=%+v", pool, got)
		}
	}
}

func TestDoctorCodexEndpointMismatchPartialEnumerationParity(t *testing.T) {
	registry := doctorEndpointFixture("0.153.2", "0.153.2")
	registry.Panes[1].Status.Activation.Codex.Authority = nil
	doctor := doctorEndpointCommand(registry, doctorEndpointHealth())
	var text, direct bytes.Buffer
	report := doctor.evaluateReport(doctorSectionIntegrations)
	if err := writeDoctorText(&text, report, doctorSectionIntegrations, false); err != nil {
		t.Fatal(err)
	}
	if err := writeDoctorJSON(&direct, report, doctorSectionIntegrations); err != nil {
		t.Fatal(err)
	}
	diagnostics := newDiagnosticsCommand()
	diagnostics.doctor = doctor
	support, err := diagnostics.supportDoctorJSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Enumeration: incomplete", "Mismatch Agent uid:agent-fixture-0", "Unobserved Agent uid:agent-fixture-1", "activation-endpoint-missing"} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("partial text missing %q: %s", want, text.String())
		}
	}
	for _, data := range [][]byte{direct.Bytes(), support} {
		var got doctorReport
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		p := got.CodexEndpointRisks
		if p == nil || p.Status != "incomplete" || p.Agents != 2 || len(p.Mismatches) != 1 || len(p.UnobservedAgents) != 1 || p.UnobservedAgents[0].Reason != "activation-endpoint-missing" || p.Risk != doctorCodexEndpointRisk || p.Observations != 1 || p.ObservedOn != "2026-09-09" {
			t.Fatalf("partial report lost confirmed risk or omission: %s", data)
		}
	}
	for _, key := range []string{"agent-fixture-0", "agent-fixture-1"} {
		if !strings.Contains(direct.String(), key) || !strings.Contains(string(support), supportHash("agent_uid", key)) {
			t.Fatalf("partial identity set drift for %s", key)
		}
	}
}

func TestDoctorCodexEndpointMismatchSupportAllowlistIsScoped(t *testing.T) {
	value := map[string]any{
		"codex_endpoint_mismatch": map[string]any{"status": "incomplete", "reason": "/private/error", "running_version": "/private/version", "risk": "certain-death", "observed_on": "2099-01-01", "causality": "proven"},
		"unrelated":               map[string]any{"status": "incomplete", "risk": doctorCodexEndpointRisk},
	}
	redactDoctorJSON(value, "")
	projection := value["codex_endpoint_mismatch"].(map[string]any)
	if projection["status"] != "incomplete" {
		t.Fatal("closed status was redacted")
	}
	for _, key := range []string{"reason", "running_version", "risk", "observed_on", "causality"} {
		if !strings.HasPrefix(projection[key].(string), "sha256:") {
			t.Fatalf("unsafe %s survived: %v", key, projection[key])
		}
	}
	other := value["unrelated"].(map[string]any)
	if other["status"] == "incomplete" || other["risk"] == doctorCodexEndpointRisk {
		t.Fatal("allowlist escaped its projection")
	}
}

func TestDoctorCodexEndpointMismatchTextJSONSupportParity(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(fmt.Sprint(mismatch), func(t *testing.T) {
			registry := doctorEndpointFixture("0.153.4", "0.153.4")
			if mismatch {
				registry.Panes[1].Status.Activation.Codex.Authority.EndpointGenerationID = "codex-0.153.2"
			}
			doctor := doctorEndpointCommand(registry, doctorEndpointHealth())
			var text, plainJSON bytes.Buffer
			report := doctor.evaluateReport(doctorSectionIntegrations)
			if err := writeDoctorText(&text, report, doctorSectionIntegrations, false); err != nil {
				t.Fatal(err)
			}
			if err := writeDoctorJSON(&plainJSON, report, doctorSectionIntegrations); err != nil {
				t.Fatal(err)
			}
			diagnostics := newDiagnosticsCommand()
			diagnostics.doctor = doctor
			support, err := diagnostics.supportDoctorJSON()
			if err != nil {
				t.Fatal(err)
			}
			if !mismatch {
				for _, output := range []string{text.String(), plainJSON.String(), string(support)} {
					if strings.Contains(output, "Codex endpoint generation comparison") || strings.Contains(output, "codex_endpoint_mismatch") {
						t.Fatalf("zero mismatch row emitted: %s", output)
					}
				}
				return
			}
			for _, want := range []string{"Mismatch Agent uid:agent-fixture-1", "2026-09-09, n=1", "causality undetermined", "interruption is not certain"} {
				if !strings.Contains(text.String(), want) {
					t.Fatalf("text missing %q: %s", want, text.String())
				}
			}
			var direct, bundled doctorReport
			if err := json.Unmarshal(plainJSON.Bytes(), &direct); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(support, &bundled); err != nil {
				t.Fatal(err)
			}
			want := *direct.CodexEndpointRisks
			want.RunningGenerationID = supportHash("running_generation_id", want.RunningGenerationID)
			want.Mismatches = []doctorCodexEndpointAgent{{AgentUID: supportHash("agent_uid", "agent-fixture-1"), EndpointGenerationID: supportHash("endpoint_generation_id", "codex-0.153.2")}}
			if !reflect.DeepEqual(&want, bundled.CodexEndpointRisks) {
				t.Fatalf("support mismatch parity: got %+v, want %+v", bundled.CodexEndpointRisks, want)
			}
			if direct.CodexEndpointRisks.Mismatches[0].AgentUID != "agent-fixture-1" || strings.Contains(string(support), "agent-fixture-1") {
				t.Fatal("exact Doctor identity or support identity hashing drifted")
			}
		})
	}
}

func TestDoctorCodexEndpointMismatchReadOnlyArgvAudit(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex")
	bin := filepath.Join(root, "bin")
	for _, path := range []string{codexHome, bin} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ledger := filepath.Join(root, "argv")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$PROJMUX_ENDPOINT_ARGV"
case "$*" in
  'app-server daemon version') printf '%s\n' '{"status":"running","backend":"pid","cliVersion":"0.154.0","appServerVersion":"0.153.4"}' ;;
  'app-server proxy') exit 0 ;;
  *) exit 91 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("PROJMUX_ENDPOINT_ARGV", ledger)
	before := snapshotCodexHealthTree(t, codexHome)
	doctor := doctorEndpointCommand(doctorEndpointFixture("0.153.2"), doctorEndpointHealth())
	doctor.appServerHealth = func(trigger codexappserver.TriggerKind, hooks bool) codexappserver.Health {
		health, err := codexappserver.EnsureDefaultProxyReady(context.Background(), trigger, "0.13.0", hooks)
		if err != nil {
			t.Fatal(err)
		}
		return health
	}
	report := doctor.evaluateReport(doctorSectionIntegrations)
	if report.CodexEndpointRisks == nil || report.CodexEndpointRisks.Status != "incomplete" || report.CodexEndpointRisks.Reason != "running-endpoint-unavailable" {
		t.Fatalf("failed proxy did not signal incomplete enumeration: %+v; health=%+v", report.CodexEndpointRisks, report.CodexAppServer)
	}
	diagnostics := newDiagnosticsCommand()
	diagnostics.doctor = doctor
	if _, err := diagnostics.supportDoctorJSON(); err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for call := range strings.SplitSeq(strings.TrimSpace(string(calls)), "\n") {
		switch call {
		case "app-server proxy", "app-server daemon version":
			counts[call]++
		default:
			t.Fatalf("non-observation argv: %q; ledger=%s", call, calls)
		}
	}
	if counts["app-server proxy"] != 2 || counts["app-server daemon version"] != 2 {
		t.Fatalf("doctor/support observation ledger: %s", calls)
	}
	if !reflect.DeepEqual(before, snapshotCodexHealthTree(t, codexHome)) {
		t.Fatal("read-only diagnostic changed Codex state")
	}
	t.Logf("C-1 argv audit: doctor/support = %q; mutation argv = 0", strings.TrimSpace(string(calls)))
}
