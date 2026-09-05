package app

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	"github.com/crevissepartners/projmux/internal/testutil/codexinstalled"
)

// The emitted receipt file is what an operator embeds in the `qualification`
// field of `agent app-server upgrade --request`. The upgrade request requires
// the receipt's version pair to match the exact current/target generations, so
// the declared pair and every evidence counter must survive the file → request
// hop byte for byte.
func TestEmittedQualificationReceiptEntersTheUpgradeRequestVerbatim(t *testing.T) {
	pair, err := codexinstalled.DeclaredGenerationPair("3.4.5", "3.4.6")
	if err != nil {
		t.Fatal(err)
	}
	result := codexgeneration.EvaluateQualification(pair, codexgeneration.QualificationEvidence{
		SharedStateDomain: true, DistinctPrivateEndpoints: true,
		DistinctThreadCreateTurn: true, DistinctThreadReadList: true, CrashRestart: true,
		OldStoppedBeforeResume: true, PersistedResumeSnapshot: true,
		SharedAuthConfigPrivate: true, BundleSourceRemovalLaunch: true,
		BundleDriftRefused: true, ProtocolMismatchRefused: true,
	})
	if result.Verdict != codexgeneration.VerdictYes {
		t.Fatalf("measured-pass evidence produced %+v", result)
	}

	directory := t.TempDir()
	receiptPath := filepath.Join(directory, "receipt.json")
	if _, err := codexinstalled.EmitGenerationQualificationReceipt(receiptPath, result); err != nil {
		t.Fatal(err)
	}
	receipt, err := os.ReadFile(receiptPath) // #nosec G304 -- test-owned temporary receipt.
	if err != nil {
		t.Fatal(err)
	}

	requestPath := filepath.Join(directory, "request.json")
	document := append(append([]byte(`{"operationRef":"upgrade-one","qualification":`), receipt...), '}')
	if err := os.WriteFile(requestPath, document, 0o600); err != nil {
		t.Fatal(err)
	}

	command := &codexUpgradeCommand{}
	request, err := command.loadRequest(requestPath)
	if err != nil {
		t.Fatalf("load request carrying the emitted receipt: %v", err)
	}
	if request.Qualification != result {
		t.Fatalf("loaded qualification=%+v, want the emitted receipt %+v", request.Qualification, result)
	}
	if request.Qualification.Versions != pair {
		t.Fatalf("loaded version pair=%+v, want the declared %+v", request.Qualification.Versions, pair)
	}
	// The two conditions the upgrade request itself applies to the receipt.
	if err := request.Qualification.Validate(); err != nil {
		t.Fatalf("loaded receipt is not self-consistent: %v", err)
	}
	if !codexgeneration.GateQualification(request.Qualification).Phase2Ready {
		t.Fatalf("loaded receipt does not open Phase 2: %+v", request.Qualification)
	}

	// A tampered counter must not survive as a verdict: evidence is what the
	// receipt is judged on, so editing it re-decides the verdict and the
	// receipt stops decoding.
	tampered := bytes.Replace(receipt, []byte(`"crossThreadWrites": 0`), []byte(`"crossThreadWrites": 1`), 1)
	if bytes.Equal(tampered, receipt) {
		t.Fatal("receipt does not carry the crossThreadWrites counter")
	}
	if _, err := codexgeneration.DecodeQualificationResult(tampered); err == nil {
		t.Fatal("a tampered evidence counter kept the yes verdict")
	}
}
