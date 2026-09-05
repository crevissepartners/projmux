package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexupgrade"
)

// Capture before the package TestMain replaces XDG_CONFIG_HOME for ordinary
// test isolation. Only the explicitly opted-in observation restores this value.
var claudeObservationConfigHome = os.Getenv("XDG_CONFIG_HOME")

// TestClaudeResumeRealStoreObservation is opt-in, read-only evidence for the
// installed picker smoke. The operator pins the requested real cwd explicitly;
// no conversation text, identity, or path is logged. Run this from the merged
// source after make install, alongside observing the installed native picker.
func TestClaudeResumeRealStoreObservation(t *testing.T) {
	cwd := os.Getenv("PROJMUX_CLAUDE_RESUME_OBSERVE_CWD")
	if cwd == "" {
		t.Skip("set PROJMUX_CLAUDE_RESUME_OBSERVE_CWD for a read-only real-store observation")
	}
	t.Setenv("XDG_CONFIG_HOME", claudeObservationConfigHome)
	if !filepath.IsAbs(cwd) {
		t.Fatal("observation cwd must be absolute")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(home, ".claude", "projects", aisessions.EncodeClaudeProjectPath(cwd)))
	if err != nil {
		t.Fatal(err)
	}
	files := 0
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".jsonl" {
			files++
		}
	}
	// Mirror App's production discovery dependencies, including the real pool
	// journal, native catalog opener, routes, enabled providers and settings.
	// CatalogRoutes never calls the launch activator, so the observation omits
	// that write-capable dependency and invokes population only.
	productionCommand := func() *aiCommand {
		cmd := newAICommand()
		cmd.codexNative = defaultCodexNativeThreadController{}
		if paths, err := config.DefaultPathsFromEnv(); err == nil {
			cmd.codexNative = rollingCodexNativeThreadController{journal: codexupgrade.NewStateStore(paths.StateDir)}
		}
		return cmd
	}
	configured := productionCommand()
	depthResolution := resolveAIResumeScanDepth(configured.homeDir, configured.lookupEnv, cwd)
	limitResolution := resolveAIResumePickerLimit(configured.homeDir, configured.lookupEnv, cwd)
	configuredDepth, configuredLimit := depthResolution.Depth, limitResolution.Limit
	t.Logf("configured_depth=%d source=%s configured_limit=%d source=%s cpu=%d gomaxprocs=%d", configuredDepth,
		depthResolution.Source, configuredLimit, limitResolution.Source, runtime.NumCPU(), runtime.GOMAXPROCS(0))
	if load, err := os.ReadFile("/proc/loadavg"); err == nil {
		t.Logf("host_loadavg=%s", strings.TrimSpace(string(load)))
	}
	// The configured concurrent invocation goes first: Claude-only comparison
	// runs must not pre-warm its transcript reads and hide the real contention.
	for _, arm := range []struct {
		name         string
		depth, limit int
		claudeOnly   bool
	}{
		{name: "all_configured_providers", depth: configuredDepth, limit: configuredLimit},
		{name: "claude_only_depth_0", depth: 0, limit: 30, claudeOnly: true},
		{name: "claude_only_depth_5", depth: 5, limit: 30, claudeOnly: true},
	} {
		t.Run(arm.name, func(t *testing.T) {
			cmd := productionCommand()
			controller := newAIResumeLiveController(cmd, cwd, home, arm.depth, arm.limit)
			if arm.claudeOnly {
				controller.providerEnabled = map[string]bool{aiModeClaude: true}
			}
			defer controller.close()
			controller.populate()
			controller.mu.Lock()
			projection := controller.providerStates[aiModeClaude]
			elapsed := controller.providerElapsed[aiModeClaude]
			visible := make(map[string]int)
			for _, row := range controller.summaries {
				visible[row.Provider]++
			}
			for _, provider := range []string{aiModeCodex, aiModeClaude, aiModeAntigravity} {
				state := controller.providerStates[provider]
				t.Logf("provider=%s enabled=%t state=%s confirmed=%d visible=%d elapsed=%s", provider,
					controller.providerEnabled[provider], state.state, state.count, visible[provider], controller.providerElapsed[provider])
			}
			t.Logf("exact_cwd_files=%d depth=%d limit=%d observed_native_routes=%d more_not_loaded=%t", files, arm.depth, arm.limit, len(controller.catalogRoutes), controller.moreNotLoaded)
			controller.mu.Unlock()
			if projection.state != aiResumeProviderCount || visible[aiModeClaude] == 0 {
				t.Errorf("Claude did not publish rows: state=%s visible=%d", projection.state, visible[aiModeClaude])
			}
			// Criterion ⑥a allows a nonempty partial in a contended invocation;
			// ⑥b pins the standalone scan cost. Deterministic ⑥c coverage lives
			// in TestClaudeEnvelopeExpiryPublishesParsedPartialRows.
			if arm.claudeOnly && elapsed >= aiResumeSummaryPopulationBudget {
				t.Errorf("Claude elapsed=%s must be below %s for this invocation", elapsed, aiResumeSummaryPopulationBudget)
			}
		})
	}
}
