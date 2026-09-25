package codexappserver_test

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/crevissepartners/projmux/internal/testutil/codexinstalled"
	"github.com/crevissepartners/projmux/internal/testutil/liveguard"
)

// providerGuardOptInEnv names every variable the package's installed tests
// read to enable an intentional real-provider path, so the provider guard
// steps aside when any is set. It includes every installed qualification's
// SmokeEnv: the installed-codex qualification runner sets exactly one of them
// and runs this package against the real CLI.
// TestProviderGuardOptInEnvCoversTheInstalledTests keeps the list closed.
var providerGuardOptInEnv = []string{
	"PROJMUX_CODEX_BROKER_SMOKE_ROOT",
	"PROJMUX_CODEX_CATALOG_SMOKE_ROOT",
	"PROJMUX_CODEX_DAEMON_SMOKE_ROOT",
	"PROJMUX_CODEX_EVIDENCE_RUN",
	"PROJMUX_CODEX_SCALE_CATALOG_BINARY",
	"PROJMUX_CODEX_SCALE_CATALOG_SMOKE_ROOT",
	"PROJMUX_CODEX_SCALE_CATALOG_VERSION",
	"PROJMUX_CODEX_STATE_DB_ONLY_BINARY",
	"PROJMUX_CODEX_STATE_DB_ONLY_SMOKE_ROOT",
}

// providerGuardAmbientEnv are variables the installed tests read that locate
// the environment rather than enable a real provider.
var providerGuardAmbientEnv = []string{"PATH"}

// TestMain runs the package behind liveguard: it resolves the real codex
// through PATH and starts or talks to its app-server daemon.
func TestMain(m *testing.M) {
	os.Exit(liveguard.RunTests(m, liveguard.ProviderOptIn(providerGuardOptInEnv...)))
}

// TestLiveMachineGuardHolds pins that TestMain runs this package behind the
// guard.
func TestLiveMachineGuardHolds(t *testing.T) { liveguard.RequireActive(t) }

// TestProviderGuardOptInEnvCoversTheInstalledTests parses the installed tests
// and requires every variable they read by literal name to be either an
// opt-in gate or a known ambient location, so a new real-provider gate cannot
// run behind the stand-ins unnoticed. Every qualification SmokeEnv must be a
// gate too, or the installed-codex qualification would run behind them.
func TestProviderGuardOptInEnvCoversTheInstalledTests(t *testing.T) {
	files, err := filepath.Glob("*_installed_test.go")
	if err != nil {
		t.Fatal(err)
	}
	read, err := liveguard.InstalledTestEnvReads(files, map[string][]int{
		"codexinstalled.SmokeRoot": {0},
		"os.Getenv":                {0},
		"os.LookupEnv":             {0},
		"scaleSmokeEnv":            {1, 2, 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if read["codexinstalled.DefaultSmokeRootEnv"] {
		delete(read, "codexinstalled.DefaultSmokeRootEnv")
		read[codexinstalled.DefaultSmokeRootEnv] = true
	}
	for _, known := range []string{codexinstalled.DefaultSmokeRootEnv, "PROJMUX_CODEX_BROKER_SMOKE_ROOT", "PROJMUX_CODEX_SCALE_CATALOG_SMOKE_ROOT"} {
		if !read[known] {
			t.Fatalf("extraction missed the known gate %s; read %v", known, slices.Sorted(maps.Keys(read)))
		}
	}
	for name := range read {
		if !slices.Contains(providerGuardOptInEnv, name) && !slices.Contains(providerGuardAmbientEnv, name) {
			t.Errorf("installed tests read %s; add it to providerGuardOptInEnv (or providerGuardAmbientEnv if it only locates the environment)", name)
		}
	}
	for _, name := range append(slices.Clone(providerGuardOptInEnv), providerGuardAmbientEnv...) {
		if !read[name] {
			t.Errorf("%s is listed but no installed test reads it; drop it from the guard lists", name)
		}
	}
	for _, name := range providerGuardAmbientEnv {
		if slices.Contains(providerGuardOptInEnv, name) {
			t.Errorf("%s is ambient and must not make the guard step aside", name)
		}
	}
	for _, spec := range codexinstalled.QualificationSpecs() {
		if !slices.Contains(providerGuardOptInEnv, spec.SmokeEnv) {
			t.Errorf("qualification %s sets %s, which is not a provider opt-in gate; the installed-codex qualification would run behind the stand-ins", spec.TestName, spec.SmokeEnv)
		}
	}
}
