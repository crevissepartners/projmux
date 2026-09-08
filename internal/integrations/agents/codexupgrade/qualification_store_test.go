package codexupgrade

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
)

func storeReceipt(pair codexgeneration.VersionPair) codexgeneration.QualificationResult {
	return codexgeneration.EvaluateQualification(pair, codexgeneration.QualificationEvidence{
		SharedStateDomain: true, DistinctPrivateEndpoints: true, DistinctThreadCreateTurn: true,
		DistinctThreadReadList: true, CrashRestart: true, OldStoppedBeforeResume: true,
		PersistedResumeSnapshot: true, SharedAuthConfigPrivate: true, BundleSourceRemovalLaunch: true,
		BundleDriftRefused: true, ProtocolMismatchRefused: true,
		ObservedThreadTurns: 2, ObservedThreadReads: 8, ObservedCrashRestarts: 2, ObservedBundleLaunches: 8,
	})
}

// TestQualificationStoreAnswersOnlyForThePairAReceiptNames is the property the
// whole lane rests on. A receipt qualifies the one pair it measured, so a store
// that answered for a neighbouring pair would hand a gate evidence about
// something else entirely.
func TestQualificationStoreAnswersOnlyForThePairAReceiptNames(t *testing.T) {
	store := NewQualificationStateStore(t.TempDir())
	pair := codexgeneration.VersionPair{Old: "0.152.1", New: "0.153.0"}
	other := codexgeneration.VersionPair{Old: "0.152.1", New: "0.153.4"}

	if _, found, err := store.Load(pair); found || err != nil {
		t.Fatalf("empty store load found=%t err=%v", found, err)
	}
	receipt := storeReceipt(pair)
	if err := store.Save(receipt); err != nil {
		t.Fatalf("save receipt: %v", err)
	}
	got, found, err := store.Load(pair)
	if err != nil || !found || got != receipt {
		t.Fatalf("stored receipt = %+v found=%t err=%v", got, found, err)
	}
	if _, found, err := store.Load(other); found || err != nil {
		t.Fatalf("neighbouring pair load found=%t err=%v", found, err)
	}
}

// TestQualificationStoreRefusesAFileThatIsNotItsOwnReceipt keeps "a file is
// present" from standing in for "this pair is qualified". Both cases here would
// otherwise reach a gate as a receipt it never measured.
func TestQualificationStoreRefusesAFileThatIsNotItsOwnReceipt(t *testing.T) {
	dir := t.TempDir()
	store := NewQualificationStateStore(dir)
	pair := codexgeneration.VersionPair{Old: "0.152.1", New: "0.153.0"}
	path, err := store.Path(pair)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}

	// A receipt for another pair filed under this pair's name.
	misfiled, err := storeReceipt(codexgeneration.VersionPair{Old: "0.151.0", New: "0.152.0"}).JSON()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, misfiled, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Load(pair); err == nil || found {
		t.Fatalf("misfiled receipt load found=%t err=%v", found, err)
	}

	// A file that is present and does not decode. It must not read as absence:
	// a corrupted store and an unqualified pair need different repairs.
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Load(pair); err == nil || found {
		t.Fatalf("undecodable receipt load found=%t err=%v", found, err)
	}
}

// TestQualificationStorePathStaysInsideItsOwnDirectory states the composition
// property the file naming relies on, rather than leaving it implied by the
// receipt's version grammar.
func TestQualificationStorePathStaysInsideItsOwnDirectory(t *testing.T) {
	dir := t.TempDir()
	store := NewQualificationStateStore(dir)
	path, err := store.Path(codexgeneration.VersionPair{Old: "0.152.1", New: "0.153.0"})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != QualificationDirFor(dir) {
		t.Fatalf("receipt path %q escaped %q", path, QualificationDirFor(dir))
	}
	for _, pair := range []codexgeneration.VersionPair{
		{Old: "", New: "0.153.0"},
		{Old: "0.153.0", New: "0.153.0"},
		{Old: "../../etc/passwd", New: "0.153.0"},
		{Old: "0.152.1", New: "0.153.0/../../escape"},
	} {
		if got, err := store.Path(pair); err == nil {
			t.Fatalf("invalid pair %+v produced path %q", pair, got)
		}
	}
}
