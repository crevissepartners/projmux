package hooks

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	removed, err := UntrustProjectConfig(cwd, trustPath)
	if err != nil {
		t.Fatalf("UntrustProjectConfig() error = %v", err)
	}
	if !removed {
		t.Fatalf("UntrustProjectConfig returned removed=false, want true after trust")
	}
	report, err := InspectProjectConfigTrust(cwd, trustPath)
	if err != nil {
		t.Fatalf("InspectProjectConfigTrust() error = %v", err)
	}
	if report.State != ProjectConfigTrustUntrusted {
		t.Fatalf("State = %q, want %q", report.State, ProjectConfigTrustUntrusted)
	}

	// Calling untrust again is a no-op rather than an error so the UI can
	// stay idempotent; the bool return distinguishes the two outcomes.
	removedAgain, err := UntrustProjectConfig(cwd, trustPath)
	if err != nil {
		t.Fatalf("idempotent UntrustProjectConfig() error = %v", err)
	}
	if removedAgain {
		t.Fatalf("UntrustProjectConfig second call removed=true, want false (idempotent)")
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
	if _, err := UntrustProjectConfig(cwd, trustPath); err != nil {
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

	removed, err := UntrustProjectConfig(cwd, trustPath)
	if err != nil {
		t.Fatalf("UntrustProjectConfig() error = %v", err)
	}
	if removed {
		t.Fatalf("UntrustProjectConfig returned removed=true with no trust store, want false")
	}
	if _, err := os.Stat(trustPath); !os.IsNotExist(err) {
		t.Fatalf("trust store should not have been created, stat err = %v", err)
	}
}

// TestAuthorizeProjectConfigIgnoresLegacyLayoutTrustEntries pins that a
// trusted-projects.json written by an older projmux, which still carries a
// `.projmux/layouts/*.toml` entry next to the config entry, keeps loading and
// authorizing project config without a prompt, and that the legacy entry is
// left on disk untouched.
func TestAuthorizeProjectConfigIgnoresLegacyLayoutTrustEntries(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	configPath := writeProjectConfig(t, cwd, `
[startup]
run = "echo ready"
`)
	repo, err := filepath.Abs(cwd)
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	contents, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	sum := sha256.Sum256(contents)
	repoJSON, err := json.Marshal(repo)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	trustPath := testTrustStorePath(t)
	legacyStore := []byte(`{
  ` + string(repoJSON) + `: {
    "trusted_at": "2026-01-02T03:04:05Z",
    "files": {
      ".projmux/config.toml": {
        "sha256": "` + hex.EncodeToString(sum[:]) + `",
        "trusted_at": "2026-01-02T03:04:05Z"
      },
      ".projmux/layouts/team.toml": {
        "sha256": "` + hex.EncodeToString(make([]byte, sha256.Size)) + `",
        "trusted_at": "2026-01-02T03:04:05Z"
      }
    }
  }
}
`)
	if err := os.WriteFile(trustPath, legacyStore, 0o600); err != nil {
		t.Fatalf("WriteFile(trust store): %v", err)
	}

	report, err := InspectProjectConfigTrust(cwd, trustPath)
	if err != nil {
		t.Fatalf("InspectProjectConfigTrust() error = %v", err)
	}
	if report.State != ProjectConfigTrustTrusted {
		t.Fatalf("State = %q, want %q", report.State, ProjectConfigTrustTrusted)
	}

	runner := &Runner{
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       trustPath,
		ProjectHookPrompt: func(req ProjectHookPromptRequest) ProjectHookDecision {
			t.Errorf("unexpected trust prompt for %q", req.RelativePath)
			return ProjectHookDeny
		},
	}
	trusted, err := runner.AuthorizeProjectConfig(cwd)
	if err != nil {
		t.Fatalf("AuthorizeProjectConfig() error = %v", err)
	}
	if !trusted {
		t.Fatalf("AuthorizeProjectConfig() = false, want true from the stored config hash")
	}

	after, err := os.ReadFile(trustPath)
	if err != nil {
		t.Fatalf("ReadFile(trust store): %v", err)
	}
	if !bytes.Equal(after, legacyStore) {
		t.Fatalf("trust store rewritten:\n got %s\nwant %s", after, legacyStore)
	}
	store, err := loadTrustedProjects(trustPath)
	if err != nil {
		t.Fatalf("loadTrustedProjects() error = %v", err)
	}
	if _, ok := store.trustedFile(repo, ".projmux/layouts/team.toml"); !ok {
		t.Fatalf("legacy layout trust entry missing after authorization, store = %+v", store)
	}
}
