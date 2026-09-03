package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbundle"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexgenerationhost"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexupgrade"
)

func doctorGenerationRoute(id string, state codexgeneration.GenerationState, owner codexgeneration.OwnerClass) codexupgrade.GenerationRoute {
	endpoint := coremetadata.CodexEndpointRef{StateDomainID: "state-main", EndpointGenerationID: id}
	route := codexupgrade.GenerationRoute{Generation: codexgeneration.Generation{Endpoint: endpoint, State: state, Owner: owner, BundleID: "bundle-" + id}}
	if owner != codexgeneration.OwnerProjmuxPrivate {
		return route
	}
	route.Config = codexupgrade.GenerationConfig{
		Endpoint: endpoint, StateDomainPath: "/state/" + id, PrivateRoot: "/runtime/" + id,
		SocketPath: "/runtime/" + id + "/app.sock", LeaseRoot: "/lease/" + id,
		RequiredProtocol: codexbundle.ProtocolRange{Min: 1, Max: 1},
	}
	route.TUIPath, route.Ready = "/lease/"+id+"/bin/codex", true
	route.Proof = &codexgenerationhost.LaunchProof{
		Endpoint:   codexgenerationhost.EndpointIdentity{StateDomainID: endpoint.StateDomainID, EndpointGenerationID: endpoint.EndpointGenerationID},
		SocketPath: route.Config.SocketPath, BundleID: route.Generation.BundleID,
	}
	return route
}

func doctorQualifiedVersions() *codexgeneration.QualificationResult {
	result := codexgeneration.EvaluateQualification(codexgeneration.VersionPair{Old: "0.152.0", New: "0.152.1"}, codexgeneration.QualificationEvidence{
		SharedStateDomain: true, DistinctPrivateEndpoints: true, DistinctThreadCreateTurn: true,
		DistinctThreadReadList: true, CrashRestart: true, OldStoppedBeforeResume: true,
		PersistedResumeSnapshot: true, SharedAuthConfigPrivate: true, BundleSourceRemovalLaunch: true,
		BundleDriftRefused: true, ProtocolMismatchRefused: true,
	})
	return &result
}

func doctorGenerationJournal(obligation codexgeneration.ObligationState) codexupgrade.Journal {
	journal := codexupgrade.Journal{
		Version: codexupgrade.JournalVersion, StateDomainID: "state-main", CurrentGenerationID: "generation-n1",
		Routes: []codexupgrade.GenerationRoute{
			doctorGenerationRoute("generation-n", codexgeneration.StateDraining, codexgeneration.OwnerProjmuxPrivate),
			doctorGenerationRoute("generation-n1", codexgeneration.StateCurrent, codexgeneration.OwnerProjmuxPrivate),
		},
		Qualification: doctorQualifiedVersions(),
	}
	if obligation != "" {
		journal.Obligations = []codexgeneration.AgentObligation{{AgentUID: "agent-old", EndpointGenerationID: "generation-n", State: obligation}}
	}
	return journal
}

func doctorBundleVerifier(cfg codexgenerationhost.PrivateGenerationConfig) (codexgenerationhost.VerifiedBundleIdentity, error) {
	id := cfg.Endpoint.EndpointGenerationID
	return codexgenerationhost.VerifiedBundleIdentity{ID: "bundle-" + id, Version: map[string]string{"generation-n": "0.152.0", "generation-n1": "0.152.1"}[id], TUIPath: "/lease/" + id + "/bin/codex"}, nil
}

func TestDoctorCodexGenerationActionabilityTable(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*codexupgrade.Journal)
		obligation codexgeneration.ObligationState
		wantStatus string
		wantReason string
		wantAction string
	}{
		{name: "expected multi-generation is actionable not critical skew", obligation: codexgeneration.ObligationCompletedPersisted, wantStatus: "action-required", wantReason: "handover-required", wantAction: "handover-required"},
		{name: "no-turn is explicit blocker", obligation: codexgeneration.ObligationNoTurn, wantStatus: "blocked", wantReason: "no-turn-choice-required", wantAction: "close-or-replace"},
		{name: "version-pair NO blocks", mutate: func(j *codexupgrade.Journal) {
			no := codexgeneration.EvaluateQualification(codexgeneration.VersionPair{Old: "0.152.0", New: "0.152.1"}, codexgeneration.QualificationEvidence{})
			j.Qualification = &no
		}, wantStatus: "blocked", wantReason: "version-pair-no"},
		{name: "foreign owner is actionable blocked", mutate: func(j *codexupgrade.Journal) {
			j.Routes[0] = doctorGenerationRoute("generation-n", codexgeneration.StateDraining, codexgeneration.OwnerOfficialManaged)
		}, wantStatus: "blocked", wantReason: "foreign-owner", wantAction: "await-owner-stop"},
		{name: "invalid admission tuple fails closed", mutate: func(j *codexupgrade.Journal) { j.CurrentGenerationID = "generation-n" }, wantStatus: "blocked", wantReason: "invalid-admission-tuple"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			journal := doctorGenerationJournal(test.obligation)
			if test.mutate != nil {
				test.mutate(&journal)
			}
			report := diagnoseCodexGenerationPool(journal, coremetadata.Registry{}, doctorBundleVerifier)
			if report.Status != test.wantStatus || report.Reason != test.wantReason {
				t.Fatalf("status/reason = %s/%s, want %s/%s; report=%#v", report.Status, report.Reason, test.wantStatus, test.wantReason, report)
			}
			if test.wantAction != "" && !doctorReportHasAction(report, test.wantAction) {
				t.Fatalf("report has no action %q: %#v", test.wantAction, report)
			}
			if report.DoctorMutations != 0 {
				t.Fatalf("doctor mutations = %d", report.DoctorMutations)
			}
		})
	}
}

func doctorReportHasAction(report doctorCodexGenerationPool, action string) bool {
	if report.Action == action {
		return true
	}
	for _, generation := range report.Generations {
		if generation.Action == action {
			return true
		}
	}
	for _, agent := range report.PinnedAgents {
		if agent.Action == action {
			return true
		}
	}
	return report.Operation != nil && report.Operation.NextAction == action
}

func TestDoctorManagedActivationShowsCurrentDrainingPinnedTruthReadOnly(t *testing.T) {
	old := doctorGenerationRoute("codex-0.152.1", codexgeneration.StateDraining, codexgeneration.OwnerUnmanaged)
	old.Version = "0.152.1"
	old.Generation.BundleID = "external-0.152.1"
	current := doctorGenerationRoute("codex-0.153.0", codexgeneration.StateCurrent, codexgeneration.OwnerProjmuxPrivate)
	current.Version = "0.153.0"
	journal := codexupgrade.Journal{
		Version: codexupgrade.JournalVersion, StateDomainID: "state-main", CurrentGenerationID: "codex-0.153.0",
		Routes: []codexupgrade.GenerationRoute{old, current},
		Obligations: []codexgeneration.AgentObligation{
			{AgentUID: "agent-old", EndpointGenerationID: "codex-0.152.1", State: codexgeneration.ObligationApprovalPending},
			{AgentUID: "agent-new", EndpointGenerationID: "codex-0.153.0", State: codexgeneration.ObligationActive},
		},
	}
	report := diagnoseCodexGenerationPool(journal, coremetadata.Registry{}, func(cfg codexgenerationhost.PrivateGenerationConfig) (codexgenerationhost.VerifiedBundleIdentity, error) {
		return codexgenerationhost.VerifiedBundleIdentity{ID: "bundle-codex-0.153.0", Version: "0.153.0", TUIPath: "/lease/codex-0.153.0/bin/codex"}, nil
	})
	if report.Status != "blocked" || report.Reason != "qualification-missing" || report.Action != "run-isolated-version-pair-qualification" ||
		report.CurrentGenerationID != "codex-0.153.0" || !report.ExpectedMultiGeneration || report.DoctorMutations != 0 ||
		len(report.Generations) != 2 || len(report.PinnedAgents) != 2 {
		t.Fatalf("managed activation Doctor report = %#v", report)
	}
	byID := map[string]doctorCodexGeneration{}
	for _, generation := range report.Generations {
		byID[generation.GenerationID] = generation
	}
	if got := byID["codex-0.152.1"]; got.State != codexgeneration.StateDraining || got.Owner != codexgeneration.OwnerUnmanaged || got.Version != "0.152.1" || got.PinnedAgents != 1 || got.Action != "await-owner-stop" {
		t.Fatalf("old generation truth = %#v", got)
	}
	if got := byID["codex-0.153.0"]; got.State != codexgeneration.StateCurrent || got.Owner != codexgeneration.OwnerProjmuxPrivate || got.Version != "0.153.0" || got.PinnedAgents != 1 || got.Status != "ready" {
		t.Fatalf("current generation truth = %#v", got)
	}
}

func TestDoctorCodexGenerationBundleDriftIsBlockedAndReadOnly(t *testing.T) {
	calls := 0
	report := diagnoseCodexGenerationPool(doctorGenerationJournal(""), coremetadata.Registry{}, func(cfg codexgenerationhost.PrivateGenerationConfig) (codexgenerationhost.VerifiedBundleIdentity, error) {
		calls++
		identity, err := doctorBundleVerifier(cfg)
		identity.ID = "bundle-drifted"
		return identity, err
	})
	if report.Status != "blocked" || report.Reason != "bundle-drift" || calls != 2 || report.DoctorMutations != 0 {
		t.Fatalf("report=%#v verifier calls=%d", report, calls)
	}
}

func TestDoctorCodexGenerationMissingBundleIsActionableBlocked(t *testing.T) {
	report := diagnoseCodexGenerationPool(doctorGenerationJournal(""), coremetadata.Registry{}, func(codexgenerationhost.PrivateGenerationConfig) (codexgenerationhost.VerifiedBundleIdentity, error) {
		return codexgenerationhost.VerifiedBundleIdentity{}, os.ErrNotExist
	})
	if report.Status != "blocked" || report.Reason != "bundle-missing" || !doctorReportHasAction(report, "restore-bundle") || report.DoctorMutations != 0 {
		t.Fatalf("missing bundle report = %#v", report)
	}
}

func TestDoctorCodexGenerationPinnedTupleSkewFailsClosed(t *testing.T) {
	journal := doctorGenerationJournal(codexgeneration.ObligationCompletedPersisted)
	registry := resourceFixtureRegistry(t)
	agent, _ := registry.Agent("agt-alpha-codex")
	agent.Status.SessionRef = &coremetadata.AgentSessionRef{Provider: aiModeCodex, Codex: &coremetadata.CodexSessionRef{
		ThreadID: "thread-current", HasStartedTurn: true,
		Endpoint: &coremetadata.CodexEndpointRef{StateDomainID: journal.StateDomainID, EndpointGenerationID: "generation-n1"},
	}}
	journal.Obligations[0].AgentUID = agent.Metadata.UID
	report := diagnoseCodexGenerationPool(journal, registry, doctorBundleVerifier)
	if report.Status != "blocked" || report.Reason != "pinned-agent-endpoint-skew" || report.DoctorMutations != 0 {
		t.Fatalf("pinned tuple skew report = %#v", report)
	}
}

func TestDoctorCodexGenerationJSONAndTextAreContentFree(t *testing.T) {
	report := diagnoseCodexGenerationPool(doctorGenerationJournal(codexgeneration.ObligationNoTurn), coremetadata.Registry{}, doctorBundleVerifier)
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	for _, forbidden := range []string{"prompt", "transcript", "message", "credential", "socket_path", "state_domain_path", "private_root", "tui_path"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("Doctor JSON contains forbidden %q: %s", forbidden, encoded)
		}
	}
	var text bytes.Buffer
	writeDoctorCodexGenerationText(&text, &report)
	for _, want := range []string{"0.152.0 -> 0.152.1", "expected multi-generation: true", "Pinned Agent agent-old", "action=close-or-replace", "doctor mutations: 0"} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("Doctor text missing %q:\n%s", want, text.String())
		}
	}
	assertDoctorGenerationGolden(t, "codex_generation_doctor.json.golden", encoded)
	assertDoctorGenerationGolden(t, "codex_generation_doctor.text.golden", text.Bytes())
}

func assertDoctorGenerationGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	want, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("Doctor golden %s mismatch\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func TestDoctorCodexGenerationHandoverTimelineUsesReceiptOrder(t *testing.T) {
	op := codexgeneration.HandoverOperation{
		AdmissionFenced: true, BindingFenced: true, OldStopped: true,
		Targets: []codexgeneration.HandoverTarget{{SuccessorAbsentObserved: true, Resumed: true, SnapshotObserved: true, EndpointCAS: true}},
		Mutations: codexgeneration.HandoverMutations{
			AdmissionFence: 1, BindingFence: 1, OldEndpointStop: 1,
			SuccessorResume: 1, SuccessorSnapshot: 1, EndpointRefCAS: 1,
		},
	}
	timeline := doctorHandoverTimeline(op)
	want := []string{"admission-fence", "binding-fence", "old-stop", "successor-resume", "successor-snapshot", "endpoint-ref-cas", "pane-relaunch", "retirement", "lease-release"}
	if len(timeline) != len(want) {
		t.Fatalf("timeline = %#v", timeline)
	}
	for i, step := range timeline {
		if step.Name != want[i] {
			t.Fatalf("timeline[%d] = %#v, want %s", i, step, want[i])
		}
		if i < 6 && step.State != "complete" {
			t.Fatalf("completed timeline[%d] = %#v", i, step)
		}
	}
}

func TestDoctorCommandProjectsGenerationPoolInJSONAndText(t *testing.T) {
	pool := diagnoseCodexGenerationPool(doctorGenerationJournal(codexgeneration.ObligationNoTurn), coremetadata.Registry{}, doctorBundleVerifier)
	cmd := newStubDoctorCommand("linux", map[string]bool{"tmux": true, "git": true, "stty": true})
	reads := 0
	cmd.codexGeneration = func(coremetadata.Registry) *doctorCodexGenerationPool {
		reads++
		copy := pool
		return &copy
	}
	var jsonOut bytes.Buffer
	if err := cmd.Run([]string{"--json", "--section", "integrations"}, &jsonOut, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Pool *doctorCodexGenerationPool `json:"codex_generation_pool"`
	}
	if err := json.Unmarshal(jsonOut.Bytes(), &decoded); err != nil || decoded.Pool == nil || decoded.Pool.Reason != "no-turn-choice-required" {
		t.Fatalf("Doctor JSON pool=%#v err=%v output=%s", decoded.Pool, err, jsonOut.String())
	}
	var textOut bytes.Buffer
	if err := cmd.Run([]string{"--section", "integrations"}, &textOut, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if reads != 2 || !strings.Contains(textOut.String(), "Codex generation pool") || !strings.Contains(textOut.String(), "action=close-or-replace") {
		t.Fatalf("Doctor reads=%d text=%s", reads, textOut.String())
	}
}
