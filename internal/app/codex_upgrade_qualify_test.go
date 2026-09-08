package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexupgrade"
	"github.com/crevissepartners/projmux/internal/testutil/codexinstalled"
)

// TestUpgradeQualifyCarriesAProducedReceiptToTheEntryPathStore closes the loop
// this Phase set out to close, end to end and in the real direction:
// the producer's canonical emitter writes a receipt, the route installs it, and
// the managed activation path finds it under the pair it names.
//
// Before this route the two halves could not meet. The producer wrote a receipt
// and the only consumer was a hand-written upgrade request, while the managed
// activation path builds its request in process. That was the lane closure --
// not a refused pair, but a verdict with nowhere to go.
func TestUpgradeQualifyCarriesAProducedReceiptToTheEntryPathStore(t *testing.T) {
	pair, err := codexinstalled.DeclaredGenerationPair("0.152.1", "0.153.0")
	if err != nil {
		t.Fatal(err)
	}
	measured := codexgeneration.EvaluateQualification(pair, codexgeneration.QualificationEvidence{
		SharedStateDomain: true, DistinctPrivateEndpoints: true, DistinctThreadCreateTurn: true,
		DistinctThreadReadList: true, CrashRestart: true, OldStoppedBeforeResume: true,
		PersistedResumeSnapshot: true, SharedAuthConfigPrivate: true, BundleSourceRemovalLaunch: true,
		BundleDriftRefused: true, ProtocolMismatchRefused: true,
		ObservedThreadTurns: 2, ObservedThreadReads: 8, ObservedCrashRestarts: 2, ObservedBundleLaunches: 8,
	})
	receiptPath := filepath.Join(t.TempDir(), "receipt.json")
	if _, err := codexinstalled.EmitGenerationQualificationReceipt(receiptPath, measured); err != nil {
		t.Fatalf("emit the produced receipt: %v", err)
	}

	stateDir := t.TempDir()
	store := codexupgrade.NewQualificationStateStore(stateDir)
	command := &codexUpgradeCommand{qualification: store}
	var stdout, stderr bytes.Buffer
	if err := command.Run([]string{"qualify", "--receipt", receiptPath}, &stdout, &stderr); err != nil {
		t.Fatalf("qualify: %v stderr=%s", err, stderr.String())
	}
	for _, want := range []string{`"verdict": "yes"`, `"stored": true`, `"old": "0.152.1"`, `"new": "0.153.0"`} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("qualify output missing %s:\n%s", want, stdout.String())
		}
	}
	stored, found, err := store.Load(pair)
	if err != nil || !found || stored != measured {
		t.Fatalf("stored receipt = %+v found=%t err=%v", stored, found, err)
	}

	// The activator reads the same store, so a qualified pair now reaches the
	// gate instead of stopping at an absent receipt.
	activator := &productionCodexManagedCurrentActivator{stateDir: stateDir}
	got, err := activator.qualificationFor(pair)
	if err != nil || got != measured {
		t.Fatalf("activator read of the installed receipt = %+v err=%v", got, err)
	}
}

// TestUpgradeQualifyStoresNothingItWouldNotHonor keeps presence in the store
// meaningful. Every refusal here is a file the gate would later have to
// re-refuse, and a store holding those turns "a receipt exists" into a claim
// that has to be re-litigated on every read.
func TestUpgradeQualifyStoresNothingItWouldNotHonor(t *testing.T) {
	pair := codexgeneration.VersionPair{Old: "0.152.1", New: "0.153.0"}
	forged := codexgeneration.EvaluateQualification(pair, codexgeneration.QualificationEvidence{
		SharedStateDomain: true, DistinctPrivateEndpoints: true, DistinctThreadCreateTurn: true,
		DistinctThreadReadList: true, CrashRestart: true, OldStoppedBeforeResume: true,
		PersistedResumeSnapshot: true, SharedAuthConfigPrivate: true, BundleSourceRemovalLaunch: true,
		BundleDriftRefused: true, ProtocolMismatchRefused: true,
	})
	refused := codexgeneration.EvaluateQualification(pair, codexgeneration.QualificationEvidence{ObservedThreadTurns: 2})

	for _, test := range []struct {
		name string
		body []byte
		want string
	}{
		{"forged evidence counters", mustReceiptJSON(t, forged), "unbacked"},
		{"measured refusal", mustReceiptJSON(t, refused), "does not qualify"},
		{"not a receipt", []byte(`{"schemaVersion":2}`), "decode"},
		{"trailing json", append(mustReceiptJSON(t, forged), []byte("\n{}")...), "decode"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			receiptPath := filepath.Join(directory, "receipt.json")
			if err := os.WriteFile(receiptPath, test.body, 0o600); err != nil {
				t.Fatal(err)
			}
			stateDir := t.TempDir()
			store := codexupgrade.NewQualificationStateStore(stateDir)
			command := &codexUpgradeCommand{qualification: store}
			var stdout, stderr bytes.Buffer
			err := command.Run([]string{"qualify", "--receipt", receiptPath}, &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("qualify = %v, want an error containing %q", err, test.want)
			}
			if _, found, loadErr := store.Load(pair); found || loadErr != nil {
				t.Fatalf("refused receipt was stored: found=%t err=%v", found, loadErr)
			}
		})
	}
}

// TestUpgradeQualifyRequiresExactlyOneAbsoluteReceipt keeps the route's argument
// contract identical to its siblings on this namespace.
func TestUpgradeQualifyRequiresExactlyOneAbsoluteReceipt(t *testing.T) {
	command := &codexUpgradeCommand{qualification: codexupgrade.NewQualificationStateStore(t.TempDir())}
	for _, args := range [][]string{
		{"qualify"},
		{"qualify", "--receipt", "relative/receipt.json"},
		{"qualify", "--receipt", "/absolute/../receipt.json"},
		{"qualify", "--receipt", "/absolute/receipt.json", "extra"},
	} {
		var stdout, stderr bytes.Buffer
		if err := command.Run(args, &stdout, &stderr); err == nil {
			t.Fatalf("qualify %v was accepted", args)
		}
	}
}

func mustReceiptJSON(t *testing.T, result codexgeneration.QualificationResult) []byte {
	t.Helper()
	raw, err := result.JSON()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
