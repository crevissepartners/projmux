package hooks

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInspectProjectConfigTrustReportsAbsentWithoutConfig(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	trustPath := testTrustStorePath(t)

	report, err := InspectProjectConfigTrust(cwd, trustPath)
	if err != nil {
		t.Fatalf("InspectProjectConfigTrust() error = %v", err)
	}
	if report.State != ProjectConfigTrustAbsent {
		t.Fatalf("State = %q, want %q", report.State, ProjectConfigTrustAbsent)
	}
	if report.CurrentHash != "" || report.StoredHash != "" {
		t.Fatalf("hashes should be empty for absent config, got %+v", report)
	}
}

func TestInspectProjectConfigTrustReportsUntrustedWithFreshConfig(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	writeProjectConfig(t, cwd, `
[startup]
run = "echo ready"
`)
	trustPath := testTrustStorePath(t)

	report, err := InspectProjectConfigTrust(cwd, trustPath)
	if err != nil {
		t.Fatalf("InspectProjectConfigTrust() error = %v", err)
	}
	if report.State != ProjectConfigTrustUntrusted {
		t.Fatalf("State = %q, want %q", report.State, ProjectConfigTrustUntrusted)
	}
	if report.CurrentHash == "" {
		t.Fatalf("CurrentHash should be populated, got empty")
	}
	if report.StoredHash != "" {
		t.Fatalf("StoredHash should be empty before trust, got %q", report.StoredHash)
	}
}

func TestInspectProjectConfigTrustReportsTrustedAfterTrust(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	writeProjectConfig(t, cwd, `
[startup]
run = "echo ready"
`)
	trustPath := testTrustStorePath(t)

	sum, err := TrustProjectConfig(cwd, trustPath)
	if err != nil {
		t.Fatalf("TrustProjectConfig() error = %v", err)
	}
	report, err := InspectProjectConfigTrust(cwd, trustPath)
	if err != nil {
		t.Fatalf("InspectProjectConfigTrust() error = %v", err)
	}
	if report.State != ProjectConfigTrustTrusted {
		t.Fatalf("State = %q, want %q", report.State, ProjectConfigTrustTrusted)
	}
	if report.CurrentHash != sum || report.StoredHash != sum {
		t.Fatalf("hashes = %+v, want both %q", report, sum)
	}
}

func TestInspectProjectConfigTrustReportsStaleAfterChange(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	writeProjectConfig(t, cwd, `
[startup]
run = "echo ready"
`)
	trustPath := testTrustStorePath(t)

	if _, err := TrustProjectConfig(cwd, trustPath); err != nil {
		t.Fatalf("TrustProjectConfig() error = %v", err)
	}

	// Mutate the on-disk config so its hash diverges from the stored hash.
	writeProjectConfig(t, cwd, `
[startup]
run = "echo updated"
`)

	report, err := InspectProjectConfigTrust(cwd, trustPath)
	if err != nil {
		t.Fatalf("InspectProjectConfigTrust() error = %v", err)
	}
	if report.State != ProjectConfigTrustStale {
		t.Fatalf("State = %q, want %q", report.State, ProjectConfigTrustStale)
	}
	if report.CurrentHash == "" || report.StoredHash == "" {
		t.Fatalf("stale report should carry both hashes, got %+v", report)
	}
	if report.CurrentHash == report.StoredHash {
		t.Fatalf("stale hashes should differ, got equal %q", report.CurrentHash)
	}
}

func TestUntrustProjectConfigRemovesEntry(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	writeProjectConfig(t, cwd, `
[startup]
run = "echo ready"
`)
	trustPath := testTrustStorePath(t)

	if _, err := TrustProjectConfig(cwd, trustPath); err != nil {
		t.Fatalf("TrustProjectConfig() error = %v", err)
	}
	if err := UntrustProjectConfig(cwd, trustPath); err != nil {
		t.Fatalf("UntrustProjectConfig() error = %v", err)
	}
	report, err := InspectProjectConfigTrust(cwd, trustPath)
	if err != nil {
		t.Fatalf("InspectProjectConfigTrust() error = %v", err)
	}
	if report.State != ProjectConfigTrustUntrusted {
		t.Fatalf("State = %q, want %q", report.State, ProjectConfigTrustUntrusted)
	}

	// Calling untrust again is a no-op rather than an error so the UI can
	// stay idempotent.
	if err := UntrustProjectConfig(cwd, trustPath); err != nil {
		t.Fatalf("idempotent UntrustProjectConfig() error = %v", err)
	}
}

func TestUntrustProjectConfigDropsEmptyProjectEntry(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	writeProjectConfig(t, cwd, `
[startup]
run = "echo ready"
`)
	trustPath := testTrustStorePath(t)

	if _, err := TrustProjectConfig(cwd, trustPath); err != nil {
		t.Fatalf("TrustProjectConfig() error = %v", err)
	}
	if err := UntrustProjectConfig(cwd, trustPath); err != nil {
		t.Fatalf("UntrustProjectConfig() error = %v", err)
	}
	store, err := loadTrustedProjects(trustPath)
	if err != nil {
		t.Fatalf("loadTrustedProjects() error = %v", err)
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	if _, ok := store[abs]; ok {
		t.Fatalf("project entry should be removed when last file is forgotten, store = %+v", store)
	}
}

func TestUntrustProjectConfigWithoutStoreIsNoop(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	writeProjectConfig(t, cwd, `
[startup]
run = "echo ready"
`)
	trustPath := filepath.Join(t.TempDir(), "missing-trusted-projects.json")

	if err := UntrustProjectConfig(cwd, trustPath); err != nil {
		t.Fatalf("UntrustProjectConfig() error = %v", err)
	}
	if _, err := os.Stat(trustPath); !os.IsNotExist(err) {
		t.Fatalf("trust store should not have been created, stat err = %v", err)
	}
}
