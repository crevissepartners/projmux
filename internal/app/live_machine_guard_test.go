package app

import (
	"maps"
	"path/filepath"
	"slices"
	"testing"

	"github.com/crevissepartners/projmux/internal/testutil/liveguard"
)

// Many tests in this package call the real dispatcher, and a test that forgets
// to inject a path or a provider stand-in (fakeProviderBinaries) resolves the
// defaults: the live tmux server, the live Registry, or the real codex or
// claude on PATH. TestMain (locale_testmain_test.go) runs the package behind
// liveguard, which removes that inheritance and fails the package when a test
// still reaches one of them.

// providerGuardOptInEnv names every variable the package's installed tests
// read to enable an intentional real-provider path. Those tests resolve the
// real CLI through PATH, so when any of them is set the provider guard steps
// aside. TestProviderGuardOptInEnvCoversTheInstalledTests keeps the list
// closed.
var providerGuardOptInEnv = []string{
	"CODEX_DIAGNOSTIC_FAILURE",
	"CODEX_DIAGNOSTIC_HELPER",
	"CODEX_DIAGNOSTIC_INITIALIZE_DELAY",
	"CODEX_DIAGNOSTIC_INPUT",
	"CODEX_DIAGNOSTIC_MANAGER",
	"CODEX_DIAGNOSTIC_MANAGER_DELAY",
	"CODEX_DIAGNOSTIC_USER_AGENT",
	"CODEX_DIAGNOSTIC_VERSION",
	"PMX_TEST_CLAUDE_ENDPOINT_BIN",
	"PMX_TEST_REAL_CLAUDE_BIN",
	"PMX_TEST_REAL_CLAUDE_CONFIG_DIR",
	"PROJMUX_CODEX_APPROVAL_SMOKE_ROOT",
	"PROJMUX_CODEX_CONNECTION_INPUT",
	"PROJMUX_CODEX_CUTOVER_SMOKE_ROOT",
	"PROJMUX_CODEX_PHASE0_PAYLOAD_FREE_SMOKE_ROOT",
	"PROJMUX_CODEX_PHASE3_SMOKE_ROOT",
	"PROJMUX_CODEX_RECONNECT_SMOKE_ROOT",
	"PROJMUX_CODEX_RECOVERY_INPUT",
	"PROJMUX_CODEX_RETIREMENT_SMOKE_ROOT",
	"PROJMUX_CODEX_SCALE_RESUME_BINARY",
	"PROJMUX_CODEX_SCALE_RESUME_CWD",
	"PROJMUX_CODEX_SCALE_RESUME_SMOKE_ROOT",
	"PROJMUX_CODEX_SCALE_RESUME_STORE",
	"PROJMUX_CODEX_SCALE_RESUME_VERSION",
}

// providerGuardAmbientEnv are variables the installed tests read that locate
// the environment rather than enable a real provider. The live machine guard
// always sets some of them, so they must never make the provider guard step
// aside.
var providerGuardAmbientEnv = []string{"PATH", "TMUX_TMPDIR", "XDG_CONFIG_HOME"}

// TestLiveMachineGuardHolds pins that TestMain runs this package behind the
// guard.
func TestLiveMachineGuardHolds(t *testing.T) { liveguard.RequireActive(t) }

// TestLiveMachineGuardUnsetsThisPackagesLiveRouting keeps liveguard's literal
// inherited list in step with the routing variables this package defines.
func TestLiveMachineGuardUnsetsThisPackagesLiveRouting(t *testing.T) {
	inherited := liveguard.InheritedEnv()
	for _, key := range []string{
		runtimeMutationAnchorPaneEnv,
		internalActivationPaneUIDEnv,
		internalActivationGenerationEnv,
		internalClaudeRegistryPathEnv,
	} {
		if !slices.Contains(inherited, key) {
			t.Errorf("liveguard does not unset %s; add it to its inherited list", key)
		}
	}
}

// TestProviderGuardOptInEnvCoversTheInstalledTests parses the installed tests
// and requires every variable they read by literal name to be either an
// opt-in gate or a known ambient location, so a new real-provider gate cannot
// run behind the stand-ins unnoticed.
func TestProviderGuardOptInEnvCoversTheInstalledTests(t *testing.T) {
	files, err := filepath.Glob("*_installed_test.go")
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, "codex_payload_free_installed_outcome_test.go")
	read, err := liveguard.InstalledTestEnvReads(files, map[string][]int{
		"codexinstalled.SmokeRoot":  {0},
		"os.Getenv":                 {0},
		"os.LookupEnv":              {0},
		"newInstalledBrokerFixture": {1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !read["PROJMUX_CODEX_PHASE0_PAYLOAD_FREE_SMOKE_ROOT"] || !read["PROJMUX_CODEX_CUTOVER_SMOKE_ROOT"] {
		t.Fatalf("extraction missed known gates; read %v", slices.Sorted(maps.Keys(read)))
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
}
