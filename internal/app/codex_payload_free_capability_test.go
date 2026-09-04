package app

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexgeneration"
)

func exactSupportedPayloadFreeRecord(t *testing.T) codexgeneration.Record {
	t.Helper()
	digest := codexgeneration.DigestString
	tuple := codexgeneration.Tuple{
		RoleTUI:          codexgeneration.BinaryIdentity{SHA256: digest("role-tui"), Size: 101},
		RoleAppServer:    codexgeneration.BinaryIdentity{SHA256: digest("role-app-server"), Size: 102},
		AppServerVersion: "0.153.0",
		Protocol:         codexgeneration.ProtocolIdentity{Transport: "unix-websocket-jsonrpc", Schema: "v2"},
		SocketRoute: codexgeneration.SocketRouteIdentity{
			Kind: "private-unix", LocatorSHA256: digest("socket"), RuntimeSHA256: digest("socket-runtime"),
		},
		StateDomainID: "private-fixture", StateDomainSHA256: digest("state-domain"), Platform: "linux", Architecture: "amd64",
	}
	pass := func(stage codexgeneration.Stage, reason string) codexgeneration.StageEvidence {
		return codexgeneration.StageEvidence{Stage: stage, Outcome: codexgeneration.StagePass, Reason: reason, ThreadSHA256: digest("durable-thread"), ExactThread: true}
	}
	evidence := codexgeneration.Evidence{
		ZeroTurnStart:   pass(codexgeneration.StageZeroTurnStart, "started"),
		IndependentRead: pass(codexgeneration.StageIndependentRead, "read-visible"),
		StoredResume:    pass(codexgeneration.StageStoredResume, "stored-resume-exact"),
		RemoteNew: codexgeneration.StageEvidence{
			Stage: codexgeneration.StageRemoteNew, Outcome: codexgeneration.StagePass, Reason: "tui-live",
			ThreadSHA256: digest("remote-thread"), ExactThread: true, PaneAlive: true,
		},
		FirstRealInput: codexgeneration.StageEvidence{
			Stage: codexgeneration.StageFirstRealInput, Outcome: codexgeneration.StagePass, Reason: "first-input",
			ThreadSHA256: digest("remote-thread"), TurnSHA256: digest("remote-turn"),
			FirstInputObserved: true, ExactThread: true, ExactTurn: true, TurnCount: 1,
		},
	}
	record, err := codexgeneration.Qualify(tuple, time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC), evidence)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

// TestCodexPayloadFreeDoctorAndCreatePlannerShareExactRecordProjection is the
// C-1 projection owner. Both consumers receive the same immutable Record and
// must produce byte-identical semantics while Phase 1 keeps the plain route.
func TestCodexPayloadFreeDoctorAndCreatePlannerShareExactRecordProjection(t *testing.T) {
	record := exactSupportedPayloadFreeRecord(t)
	want := codexgeneration.Project(record)
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}

	launcher := &fakeAgentLauncher{}
	createReads := 0
	create := &createCommand{
		agents: launcher,
		codexPayloadFreeCapability: func() codexgeneration.Record {
			createReads++
			return record
		},
	}
	if _, _, err := create.planAgentPaneLaunch(aiModeCodex, coremetadata.AgentWorkspace{CWD: "/work"}, resourceCreateFlags{}); err != nil {
		t.Fatal(err)
	}
	if createReads != 1 || len(launcher.plans) != 1 || len(launcher.plans[0].payload) != 0 {
		t.Fatalf("create capability reads/plans = %d/%#v", createReads, launcher.plans)
	}
	if want.CreateRoute != codexgeneration.CreateRoutePlainFallback {
		t.Fatalf("Phase 1 create route = %q", want.CreateRoute)
	}

	doctorReads := 0
	doctor := &doctorCommand{codexPayloadFreeCapability: func() codexgeneration.Record {
		doctorReads++
		return record
	}}
	report := doctor.evaluateReport(doctorSectionIntegrations)
	if doctorReads != 1 || report.CodexPayloadFree == nil {
		t.Fatalf("Doctor capability reads/projection = %d/%#v", doctorReads, report.CodexPayloadFree)
	}
	doctorJSON, err := json.Marshal(report.CodexPayloadFree)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(doctorJSON, wantJSON) {
		t.Fatalf("Doctor/create projection mismatch\ndoctor: %s\ncreate: %s", doctorJSON, wantJSON)
	}

	var text bytes.Buffer
	writeDoctorCodexPayloadFreeText(&text, report.CodexPayloadFree)
	for _, required := range []string{record.CacheKey, "durable-zero-turn-resume: supported", "remote-new-session: supported", "Create route: plain-fallback"} {
		if !bytes.Contains(text.Bytes(), []byte(required)) {
			t.Fatalf("Doctor text %q does not contain %q", text.String(), required)
		}
	}
}

// TestCodexPayloadFreeUnknownCapabilityCannotBypassPhaseZeroFallback closes the
// fail-closed planner path: an absent/invalid record performs no native work.
func TestCodexPayloadFreeUnknownCapabilityCannotBypassPhaseZeroFallback(t *testing.T) {
	launcher := &fakeAgentLauncher{}
	create := &createCommand{agents: launcher}
	if _, _, err := create.planAgentPaneLaunch(aiModeCodex, coremetadata.AgentWorkspace{CWD: "/work"}, resourceCreateFlags{}); err != nil {
		t.Fatal(err)
	}
	if len(launcher.plans) != 1 || launcher.plans[0].provider != aiModeCodex || len(launcher.plans[0].payload) != 0 {
		t.Fatalf("unknown payload-free plan = %#v", launcher.plans)
	}
}
