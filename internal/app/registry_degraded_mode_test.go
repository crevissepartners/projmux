package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// TestIllegalRegistryFixtureKeepsReadDiagnosisAndRepairAvailable is the
// command-boundary degraded-mode fixture. The document is decodable enough for
// a Registry read but fails the graph invariant through a dangling name
// reservation. Ordinary create cannot build on it; the no-write diagnosis and
// explicitly selected recovery source remain usable.
func TestIllegalRegistryFixtureKeepsReadDiagnosisAndRepairAvailable(t *testing.T) {
	root := t.TempDir()
	stateHome := filepath.Join(root, "state")
	configHome := filepath.Join(root, "config")
	t.Setenv("HOME", root)
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("XDG_CONFIG_HOME", configHome)

	registryPath := intmetadata.PathFor(filepath.Join(stateHome, "projmux"))
	metadataDir := filepath.Dir(registryPath)
	recoveryDir := filepath.Join(metadataDir, "recovery")
	if err := os.MkdirAll(recoveryDir, 0o700); err != nil {
		t.Fatalf("create fixture state: %v", err)
	}
	invalid := readDegradedFixture(t, "registry-degraded-invalid.json")
	valid := readDegradedFixture(t, "registry-degraded-recovery.json")
	if err := os.WriteFile(registryPath, invalid, 0o600); err != nil {
		t.Fatalf("write illegal Registry fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(metadataDir, "registry.initialized"), []byte("fixture initialized\n"), 0o600); err != nil {
		t.Fatalf("write initialized marker: %v", err)
	}
	sourceName := "registry-20260821T000000Z-00.json"
	if err := os.WriteFile(filepath.Join(recoveryDir, sourceName), valid, 0o600); err != nil {
		t.Fatalf("write recovery fixture: %v", err)
	}

	// Read remains available and projects the decodable resource rather than
	// turning the whole CLI into a repair-only binary.
	var readOut, readErr bytes.Buffer
	if err := newGetCommand().Run([]string{"projects", "-o", "name"}, &readOut, &readErr); err != nil {
		t.Fatalf("read in degraded mode: %v (stderr %q)", err, readErr.String())
	}
	if got := readOut.String(); got != "readable\n" {
		t.Fatalf("degraded read = %q, want readable project", got)
	}

	// Diagnosis succeeds without choosing a source and prints the exact guarded
	// restore command for an operator to review.
	recovery := newRegistryRecoveryCommand(nil)
	var planOut, planErr bytes.Buffer
	if err := recovery.Run([]string{"--dry-run", "-o", "json"}, &planOut, &planErr); err != nil {
		t.Fatalf("diagnose degraded Registry: %v (stderr %q)", err, planErr.String())
	}
	var plan registryRecoveryReport
	if err := json.Unmarshal(planOut.Bytes(), &plan); err != nil {
		t.Fatalf("decode recovery plan: %v\n%s", err, planOut.String())
	}
	if plan.Current.State != intmetadata.RegistryStateInvalid || plan.Outcome != "planned" {
		t.Fatalf("diagnosis current/outcome = %q/%q", plan.Current.State, plan.Outcome)
	}
	if !strings.Contains(plan.Next, sourceName) ||
		!strings.Contains(plan.Next, "--expect-source-checksum "+plan.Sources[0].Checksum) ||
		!strings.Contains(plan.Next, "--expect-current-checksum "+plan.Current.Checksum) {
		t.Fatalf("diagnosis did not print the exact guarded repair command: %q", plan.Next)
	}

	// An ordinary mutation is refused before its callback or normal lock can
	// run, with degraded context and the exact no-write next command around the
	// validation cause.
	createRoot := filepath.Join(root, "new-project")
	if err := os.MkdirAll(createRoot, 0o700); err != nil {
		t.Fatalf("create Project root: %v", err)
	}
	before := mustReadFile(t, registryPath)
	var createOut, createErr bytes.Buffer
	err := newCreateCommand().Run([]string{"project", "--root", createRoot}, &createOut, &createErr)
	if !errors.Is(err, intmetadata.ErrRegistryDegraded) {
		t.Fatalf("create error = %v, want ErrRegistryDegraded", err)
	}
	if IsUsageError(err) {
		t.Fatalf("persisted Registry damage was mapped to a usage error: %v", err)
	}
	for _, want := range []string{"degraded mode (invalid)", "ordinary mutations are disabled", intmetadata.RegistryRecoveryPlanCommand} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("create refusal %q does not contain %q", err, want)
		}
	}
	if suffix := "run exactly: " + intmetadata.RegistryRecoveryPlanCommand; !strings.HasSuffix(err.Error(), suffix) {
		t.Fatalf("create refusal does not end in the exact next command %q: %q", suffix, err)
	}
	if createOut.Len() != 0 {
		t.Fatalf("refused create wrote stdout %q", createOut.String())
	}
	if got := mustReadFile(t, registryPath); !bytes.Equal(got, before) {
		t.Fatal("refused create changed the illegal Registry")
	}
	if _, statErr := os.Stat(registryPath + ".lock"); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("refused create entered the ordinary write lock: %v", statErr)
	}

	// Repair succeeds only after the operator names the source and supplies the
	// checksums printed by the diagnosis.
	var restoreOut, restoreErr bytes.Buffer
	err = recovery.Run([]string{
		"--source", sourceName,
		"--expect-source-checksum", plan.Sources[0].Checksum,
		"--expect-current-checksum", plan.Current.Checksum,
		"-o", "json",
	}, &restoreOut, &restoreErr)
	if err != nil {
		t.Fatalf("repair degraded Registry: %v (stderr %q)", err, restoreErr.String())
	}
	var restored registryRecoveryReport
	if err := json.Unmarshal(restoreOut.Bytes(), &restored); err != nil {
		t.Fatalf("decode restore report: %v\n%s", err, restoreOut.String())
	}
	if restored.Outcome != "restored" || restored.Restore == nil || !restored.Restore.Changed {
		t.Fatalf("restore outcome = %+v", restored)
	}

	// The same mutation now succeeds, proving degraded mode exited on the
	// repaired Registry rather than becoming a sticky process flag.
	createOut.Reset()
	createErr.Reset()
	if err := newCreateCommand().Run([]string{"project", "--root", createRoot, "-o", "name"}, &createOut, &createErr); err != nil {
		t.Fatalf("create after repair: %v (stderr %q)", err, createErr.String())
	}
	if strings.TrimSpace(createOut.String()) == "" {
		t.Fatal("create after repair returned no Project name")
	}
}

func readDegradedFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}
