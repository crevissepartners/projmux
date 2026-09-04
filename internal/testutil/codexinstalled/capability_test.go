package codexinstalled

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestCapabilityReducerSeparatesMethodFromSemanticResult(t *testing.T) {
	versions := VersionTuple{CLI: "0.152.0", Managed: "0.152.0", AppServer: "0.152.0"}
	evidence := CapabilityEvidence{
		EndpointReady: true, ThreadCreatedWithoutTurn: true, LoadedBeforeAttach: true,
		AttachAttempted: true, LoadedAfterAttach: true, RuntimeStatusAfterAttach: "idle", PaneObserved: true, PaneAlive: true,
	}
	observation := CapabilityObservation{Probe: "TestInstalledIsolatedPreTurnBootstrapSmoke", Run: "fixture-run"}
	methods := []CapabilityMethod{
		{Attach: "cli-remote-resume", Loaded: "rpc-thread-loaded-list", Runtime: "rpc-thread-read"},
		{Attach: "rpc-subscribe-thread", Loaded: "cli-loaded-list", Runtime: "wire-thread-state"},
	}
	results := make([]CapabilityResult, 0, len(methods))
	for _, method := range methods {
		result := EvaluateTurnFreeThreadLiveAttach(versions, method, evidence, observation)
		if err := result.Validate(); err != nil {
			t.Fatal(err)
		}
		results = append(results, result)
	}
	if results[0].Result != CapabilitySupported || results[0].Reason != CapabilityReasonLivePaneAttached ||
		results[1].Result != results[0].Result || results[1].Reason != results[0].Reason {
		t.Fatalf("method changed semantic result: %+v", results)
	}
	if reflect.DeepEqual(results[0].Method, results[1].Method) {
		t.Fatal("method evidence was not retained separately")
	}
}

func TestCapabilityReducerKeepsUnavailableEndpointAsInfraError(t *testing.T) {
	result := EvaluateTurnFreeThreadLiveAttach(
		VersionTuple{},
		CapabilityMethod{Attach: "cli-remote-resume", Loaded: "rpc-thread-loaded-list", Runtime: "rpc-thread-read"},
		CapabilityEvidence{},
		CapabilityObservation{Probe: "UnavailableEndpointNegative", Run: "fixture-run"},
	)
	if result.Result != CapabilityInfraError || result.Reason != CapabilityReasonEndpointUnavailable {
		t.Fatalf("unavailable endpoint = %+v, want infra-error", result)
	}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCapabilityReducerRequiresLoadedRuntimeAndLivingPane(t *testing.T) {
	versions := VersionTuple{CLI: "0.152.0", Managed: "0.152.0", AppServer: "0.152.0"}
	method := CapabilityMethod{Attach: "cli-remote-resume", Loaded: "rpc-thread-loaded-list", Runtime: "rpc-thread-read"}
	observation := CapabilityObservation{Probe: "fixture", Run: "fixture-run"}
	base := CapabilityEvidence{
		EndpointReady: true, ThreadCreatedWithoutTurn: true, LoadedBeforeAttach: true, AttachAttempted: true, PaneObserved: true,
	}
	dead := EvaluateTurnFreeThreadLiveAttach(versions, method, base, observation)
	if dead.Result != CapabilityUnsupported || dead.Reason != CapabilityReasonAttachRefused {
		t.Fatalf("dead Pane = %+v, want unsupported", dead)
	}
	if err := dead.Validate(); err != nil {
		t.Fatal(err)
	}
	unobserved := base
	unobserved.PaneObserved = false
	missingPaneEvidence := EvaluateTurnFreeThreadLiveAttach(versions, method, unobserved, observation)
	if missingPaneEvidence.Result != CapabilityInfraError || missingPaneEvidence.Reason != CapabilityReasonEvidenceIncomplete {
		t.Fatalf("unobserved Pane = %+v, want infra-error", missingPaneEvidence)
	}
	partial := base
	partial.PaneAlive = true
	partial.LoadedAfterAttach = true
	partial.RuntimeStatusAfterAttach = "notLoaded"
	incomplete := EvaluateTurnFreeThreadLiveAttach(versions, method, partial, observation)
	if incomplete.Result != CapabilityInfraError || incomplete.Reason != CapabilityReasonEvidenceIncomplete {
		t.Fatalf("incomplete runtime = %+v, want infra-error", incomplete)
	}
}

func TestCapabilityLedgerIsStrictAndVersioned(t *testing.T) {
	result := EvaluateTurnFreeThreadLiveAttach(
		VersionTuple{CLI: "0.152.0", Managed: "0.152.0", AppServer: "0.152.0"},
		CapabilityMethod{Attach: "cli-remote-resume", Loaded: "rpc-thread-loaded-list", Runtime: "rpc-thread-read"},
		CapabilityEvidence{
			EndpointReady: true, ThreadCreatedWithoutTurn: true, LoadedBeforeAttach: true,
			AttachAttempted: true, LoadedAfterAttach: true, RuntimeStatusAfterAttach: "idle", PaneObserved: true, PaneAlive: true,
		},
		CapabilityObservation{Probe: "fixture", Run: "fixture-run"},
	)
	ledger := CapabilityLedger{SchemaVersion: CapabilitySchemaVersion, Capabilities: []CapabilityResult{result}}
	encoded, err := ledger.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCapabilityLedger(encoded); err != nil {
		t.Fatal(err)
	}
	unknown := append(encoded[:len(encoded)-1], []byte(`,"unexpected":true}`)...)
	if _, err := DecodeCapabilityLedger(unknown); err == nil {
		t.Fatal("unknown capability ledger field was accepted")
	}
	result.LastObserved.Run = "/private/path"
	if err := result.Validate(); err == nil {
		t.Fatal("content-bearing last-observed evidence was accepted")
	}
}

func TestSupportedCapabilityRejectsBlankLastObservedEvidence(t *testing.T) {
	base := EvaluateTurnFreeThreadLiveAttach(
		VersionTuple{CLI: "0.152.0", Managed: "0.152.0", AppServer: "0.152.0"},
		CapabilityMethod{Attach: "cli-remote-resume", Loaded: "rpc-thread-loaded-list", Runtime: "rpc-thread-read"},
		CapabilityEvidence{
			EndpointReady: true, ThreadCreatedWithoutTurn: true, LoadedBeforeAttach: true,
			AttachAttempted: true, LoadedAfterAttach: true, RuntimeStatusAfterAttach: "idle",
			PaneObserved: true, PaneAlive: true,
		},
		CapabilityObservation{Probe: "fixture", Run: "fixture-run"},
	)
	for _, test := range []struct {
		name        string
		observation CapabilityObservation
	}{
		{name: "probe", observation: CapabilityObservation{Run: "fixture-run"}},
		{name: "run", observation: CapabilityObservation{Probe: "fixture"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := base
			result.LastObserved = normalizeCapabilityObservation(test.observation)
			if err := result.Validate(); err == nil {
				t.Fatal("supported capability accepted blank last-observed evidence")
			}
		})
	}
}

func TestCheckedInCapabilityLedgerMatchesCurrentInstalledGate(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate capability ledger contract test")
	}
	path := filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "docs", "codex-installed-capabilities.json")
	encoded, err := os.ReadFile(path) // #nosec G304 -- path is derived from this tracked source file.
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := DecodeCapabilityLedger(encoded)
	if err != nil {
		t.Fatal(err)
	}
	result := ledger.Capabilities[0]
	wantVersions := VersionTuple{CLI: "0.152.0", Managed: "0.152.0", AppServer: "0.152.0"}
	if result.Versions != wantVersions || result.Result != CapabilityInfraError ||
		result.Reason != CapabilityReasonEvidenceIncomplete ||
		result.LastObserved.Probe != "TestInstalledIsolatedPreTurnBootstrapSmoke" {
		t.Fatalf("checked-in current capability gate = %+v", result)
	}
}
