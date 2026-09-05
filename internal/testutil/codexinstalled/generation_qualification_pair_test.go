package codexinstalled

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
)

// qualifiedEvidence is the all-measured-pass evidence shape. It exists only so
// these wiring tests have a receipt to carry; the harness never builds its
// evidence from a literal like this.
func qualifiedEvidence() codexgeneration.QualificationEvidence {
	return codexgeneration.QualificationEvidence{
		SharedStateDomain: true, DistinctPrivateEndpoints: true,
		DistinctThreadCreateTurn: true, DistinctThreadReadList: true, CrashRestart: true,
		CrossThreadWrites: 0, StoreCorruptions: 0, LiveOwnerResumeWrites: 0,
		OldStoppedBeforeResume: true, PersistedResumeSnapshot: true,
		SharedAuthConfigPrivate: true, BundleSourceRemovalLaunch: true,
		BundleDriftRefused: true, ProtocolMismatchRefused: true, AmbientMutations: 0,
	}
}

func TestDeclaredGenerationPairAcceptsAnyDistinctReceiptVersionPair(t *testing.T) {
	for _, declared := range [][2]string{
		{"0.152.0", "0.152.1"},
		{"0.153.0", "0.153.2"},
		{"1.0.0", "2.0.0-1"},
		{"  9.9.9  ", "10.0.0"},
	} {
		pair, err := DeclaredGenerationPair(declared[0], declared[1])
		if err != nil {
			t.Fatalf("declared pair %q/%q: %v", declared[0], declared[1], err)
		}
		result := codexgeneration.EvaluateQualification(pair, qualifiedEvidence())
		if result.Verdict != codexgeneration.VerdictYes || result.Versions != pair {
			t.Fatalf("declared pair %+v produced %+v", pair, result)
		}
		if err := result.Validate(); err != nil {
			t.Fatalf("declared pair %+v receipt is invalid: %v", pair, err)
		}
	}
}

func TestDeclaredGenerationPairRefusesEmptyEqualAndNonVersionTokens(t *testing.T) {
	for _, declared := range [][2]string{
		{"", "0.152.1"},
		{"0.152.0", ""},
		{"0.152.0", "0.152.0"},
		{"   ", "\t"},
		{"v0.152.0", "0.152.1"},
		{"0.152.0", "0.152 1"},
		{"0.152.0", "0.152.1;rm"},
	} {
		if pair, err := DeclaredGenerationPair(declared[0], declared[1]); err == nil {
			t.Fatalf("declared pair %q/%q was accepted as %+v", declared[0], declared[1], pair)
		}
	}
}

func TestEmittedReceiptFileRoundTripsToTheIdenticalResult(t *testing.T) {
	pair, err := DeclaredGenerationPair("7.1.0", "7.2.0")
	if err != nil {
		t.Fatal(err)
	}
	result := codexgeneration.EvaluateQualification(pair, qualifiedEvidence())
	path := filepath.Join(t.TempDir(), "nested", "receipt.json")
	raw, err := EmitGenerationQualificationReceipt(path, result)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(path) // #nosec G304 -- test-owned temporary receipt.
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, raw) {
		t.Fatalf("emitted bytes differ from the file: file=%q returned=%q", stored, raw)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("receipt mode=%v err=%v, want 0600", info.Mode().Perm(), err)
	}
	reopened, err := codexgeneration.DecodeQualificationResult(stored)
	if err != nil || reopened != result {
		t.Fatalf("receipt reopen=%+v err=%v, want %+v", reopened, err, result)
	}
}

func TestEmittedReceiptReplacesAnEarlierVerdictAndRefusesRelativePaths(t *testing.T) {
	pair, err := DeclaredGenerationPair("7.1.0", "7.2.0")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "receipt.json")
	refused := codexgeneration.EvaluateQualification(pair, codexgeneration.QualificationEvidence{})
	if refused.Verdict != codexgeneration.VerdictNo {
		t.Fatalf("zero evidence verdict=%s, want no", refused.Verdict)
	}
	if _, err := EmitGenerationQualificationReceipt(path, refused); err != nil {
		t.Fatal(err)
	}
	qualified := codexgeneration.EvaluateQualification(pair, qualifiedEvidence())
	if _, err := EmitGenerationQualificationReceipt(path, qualified); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(path) // #nosec G304 -- test-owned temporary receipt.
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := codexgeneration.DecodeQualificationResult(stored)
	if err != nil || reopened != qualified {
		t.Fatalf("rerun receipt reopen=%+v err=%v, want the fresh verdict %+v", reopened, err, qualified)
	}
	if _, err := EmitGenerationQualificationReceipt("relative/receipt.json", qualified); err == nil {
		t.Fatal("relative receipt path was accepted")
	}
}

// The declared pair is the only place a version may appear. A literal in either
// source file would silently pin the harness to one pair again.
func TestGenerationQualificationSourcesCarryNoVersionLiteral(t *testing.T) {
	version := regexp.MustCompile(`[0-9]+\.[0-9]+\.[0-9]+`)
	for _, name := range []string{
		"generation_conformance_installed_test.go",
		"generation_qualification_pair.go",
	} {
		source, err := os.ReadFile(name) // #nosec G304 -- closed list of package sources.
		if err != nil {
			t.Fatal(err)
		}
		if found := version.FindAll(source, -1); found != nil {
			t.Fatalf("%s pins version literals %q", name, found)
		}
	}
}
