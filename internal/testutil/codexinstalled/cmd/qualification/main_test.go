package main

import (
	"fmt"
	"testing"

	"github.com/crevissepartners/projmux/internal/testutil/codexinstalled"
)

func TestCapabilityMarkerReductionPreservesTerminalUnsupported(t *testing.T) {
	spec, _ := codexinstalled.QualificationSpecFor(codexinstalled.PrimitivePreTurnAttach)
	versions := codexinstalled.VersionTuple{CLI: "0.152.0", Managed: "0.152.0", AppServer: "0.152.0"}
	result := codexinstalled.EvaluateTurnFreeThreadLiveAttach(
		versions,
		codexinstalled.CapabilityMethod{Attach: "cli-remote-resume", Loaded: "rpc-thread-loaded-list", Runtime: "rpc-thread-read"},
		codexinstalled.CapabilityEvidence{
			EndpointReady: true, ThreadCreatedWithoutTurn: true, LoadedBeforeAttach: true,
			AttachAttempted: true, PaneObserved: true,
		},
		codexinstalled.CapabilityObservation{Probe: spec.TestName, Run: "fixture-run"},
	)
	ledger, err := reduceCapabilityLedger(spec, []codexinstalled.CapabilityResult{result}, versions, "0.152.0")
	if err != nil {
		t.Fatal(err)
	}
	if got := ledger.Capabilities[0]; got.Result != codexinstalled.CapabilityUnsupported ||
		got.Reason != codexinstalled.CapabilityReasonAttachRefused || got.Method != result.Method {
		t.Fatalf("terminal unsupported reduction = %+v", got)
	}
}

func TestCapabilityMarkerReductionMakesMissingEvidenceInfraError(t *testing.T) {
	t.Setenv("PROJMUX_CODEX_EVIDENCE_RUN", "fixture-run")
	spec, _ := codexinstalled.QualificationSpecFor(codexinstalled.PrimitivePreTurnAttach)
	versions := codexinstalled.VersionTuple{CLI: "0.152.0", Managed: "0.152.0", AppServer: "0.152.0"}
	ledger, err := reduceCapabilityLedger(spec, nil, versions, "0.152.0")
	if err == nil {
		t.Fatal("missing capability marker passed reduction")
	}
	if got := ledger.Capabilities[0]; got.Result != codexinstalled.CapabilityInfraError ||
		got.Reason != codexinstalled.CapabilityReasonTerminalMissing {
		t.Fatalf("missing capability marker = %+v", got)
	}
	if err := ledger.Validate(); err != nil {
		t.Fatalf("strict failure ledger did not retain exact runner evidence: %v", err)
	}
}

func TestCapabilityMarkerDecoderUsesItsTypedMarkerOnly(t *testing.T) {
	result := codexinstalled.EvaluateTurnFreeThreadLiveAttach(
		codexinstalled.VersionTuple{CLI: "0.152.0", Managed: "0.152.0", AppServer: "0.152.0"},
		codexinstalled.CapabilityMethod{Attach: "cli-remote-resume", Loaded: "rpc-thread-loaded-list", Runtime: "rpc-thread-read"},
		codexinstalled.CapabilityEvidence{
			EndpointReady: true, ThreadCreatedWithoutTurn: true, LoadedBeforeAttach: true,
			AttachAttempted: true, LoadedAfterAttach: true, RuntimeStatusAfterAttach: "idle",
			PaneObserved: true, PaneAlive: true,
		},
		codexinstalled.CapabilityObservation{Probe: "FixtureProbe", Run: "fixture-run"},
	)
	encoded, err := result.JSON()
	if err != nil {
		t.Fatal(err)
	}
	events := fmt.Sprintf("{\"Output\":\"installed-result: {}\\n\"}\n{\"Output\":%q}\n",
		"installed-capability: "+encoded+"\n")
	got := decodeInstalledCapabilities([]byte(events))
	if len(got) != 1 || got[0].Result != codexinstalled.CapabilitySupported {
		t.Fatalf("decoded capabilities = %+v", got)
	}
}
