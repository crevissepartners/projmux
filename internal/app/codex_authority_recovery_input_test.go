package app

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/testutil/codexinstalled"
)

func TestInstalledRecoverySelectionRefusalKeepsSubmissionsAndProviderCallsZero(t *testing.T) {
	root := t.TempDir()
	fixture := &codexinstalled.Fixture{Root: root, CodexHome: filepath.Join(root, "codex-home")}
	shim := filepath.Join(root, "fixture-bin", "codex")
	fallback := filepath.Join(root, "fallback-bin", "codex")
	for _, path := range []string{shim, fallback} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 91\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// Model the same LookPath fallback as an execution-denied mount, without
	// changing host mounts. The lead's isolated noexec mount is separate proof.
	if err := os.Chmod(shim, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(shim)+string(os.PathListSeparator)+filepath.Dir(fallback))
	t.Setenv("CODEX_HOME", fixture.CodexHome)
	t.Setenv("PROJMUX_CODEX_INSTALLED_HOME", fixture.CodexHome)
	t.Setenv("PROJMUX_CODEX_INSTALLED_REAL", filepath.Join(fixture.CodexHome, "packages", "standalone", "current", "bin", "codex"))
	if selected, err := exec.LookPath("codex"); err != nil || selected != fallback {
		t.Fatalf("execution-denied shim did not reproduce PATH fallback: %q %v", selected, err)
	}
	refused := observeInstalledConnectionSelection(fixture, codexinstalled.ManagedDaemonProof{})
	if refused.Error != "path-cli-not-fixture-shim" {
		t.Fatalf("fallback was not refused: %+v", refused)
	}
	for _, stage := range []string{"submitting-create", "submitting-turn"} {
		for _, selection := range []installedConnectionSelection{refused, {}} {
			attempt := &installedRecoveryAttempt{Stage: stage, AgentUID: "exact-survivor"}
			ledger := installedRecoveryLedger{Result: "FAIL", Rows: []installedRecoveryRow{{Attempts: []*installedRecoveryAttempt{attempt}}}}
			providerCalls, saves := 0, 0
			result, err := submitInstalledRecoveryInput(&ledger, attempt, selection, func() { saves++ }, func() string {
				providerCalls++
				return "PRIVATE-PROVIDER-RESULT"
			})
			if err == nil || result != "" || ledger.Submissions != 0 || providerCalls != 0 || saves != 1 || attempt.Stage != "selection-refused" || attempt.Selection == nil || attempt.AgentUID != "exact-survivor" {
				t.Fatalf("refused %s submitted work or lost evidence: calls=%d ledger=%+v attempt=%+v err=%v", stage, providerCalls, ledger, attempt, err)
			}
			raw, err := json.Marshal(ledger)
			if err != nil || strings.Contains(string(raw), "PRIVATE-") || !strings.Contains(string(raw), `"submissions":0`) || !strings.Contains(string(raw), "selection-refused") {
				t.Fatalf("refusal ledger missing or unsafe: %s %v", raw, err)
			}
		}
	}
}

func TestInstalledRecoverySubmissionRecordsSelectionBeforeExactlyOneInput(t *testing.T) {
	selection := installedConnectionSelection{EnvironmentMatches: true, MatchesManager: true, StateDomainID: "fixture-domain", SelectedSHA256: "fixture-digest"}
	ledger := installedRecoveryLedger{}
	attempt := &installedRecoveryAttempt{Stage: "submitting-create"}
	providerCalls, saves := 0, 0
	result, err := submitInstalledRecoveryInput(&ledger, attempt, selection, func() {
		saves++
		if providerCalls != 0 || ledger.Submissions != 1 || attempt.Selection == nil || *attempt.Selection != selection {
			t.Fatal("selection/submission was not saved before provider input")
		}
	}, func() string {
		providerCalls++
		return "actual-receipt"
	})
	if err != nil || result != "actual-receipt" || providerCalls != 1 || saves != 1 {
		t.Fatalf("verified input missing or repeated: calls=%d saves=%d err=%v", providerCalls, saves, err)
	}
	// A later sample must recheck selection and cannot reuse this successful
	// sample's permission or increase submissions when the fixture drifts.
	if _, err := submitInstalledRecoveryInput(&ledger, attempt, installedConnectionSelection{}, func() { saves++ }, func() string { providerCalls++; return "unexpected" }); err == nil || providerCalls != 1 || ledger.Submissions != 1 || saves != 2 {
		t.Fatal("later unverified input reused prior fixture selection")
	}
}
