package app

import (
	"context"
	"crypto/sha256"
	"fmt"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	"github.com/crevissepartners/projmux/internal/testutil/codexinstalled"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// scaleResumeCensus is the same-run truth about the fixture store. The
// planning literals of this track (828 rollouts, 158 depth-5 rows, 143 Claude,
// 4 Antigravity) described a store that has since grown, so nothing here is
// compared against a frozen number: the census is taken from the copy at the
// start of the run and every observation is compared against it.
type scaleResumeCensus struct {
	rolloutFiles int
	depth0       map[string]int
	depth5       map[string]int
}

var scaleResumeProviders = []string{aiModeCodex, aiModeClaude, aiModeAntigravity}

func takeScaleResumeCensus(t *testing.T, store, cwd string) scaleResumeCensus {
	t.Helper()
	census := scaleResumeCensus{depth0: map[string]int{}, depth5: map[string]int{}}
	sessions := filepath.Join(store, ".codex", "sessions")
	err := filepath.Walk(sessions, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() && strings.HasSuffix(path, ".jsonl") {
			census.rolloutFiles++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("census fixture rollout store: %v", err)
	}
	if census.rolloutFiles == 0 {
		t.Fatalf("census found no rollout files under %s", sessions)
	}
	for _, provider := range scaleResumeProviders {
		for depth, into := range map[int]map[string]int{0: census.depth0, 5: census.depth5} {
			// The census oracle is the untimed provider discovery: the same
			// enumeration the bounded settlement uses, with no budget over it.
			// The claim under test is therefore exactly the contract's claim --
			// that the bounded result is the untimed result, not merely a
			// plausible subset of it.
			discovery, err := aisessions.DiscoverProviderContext(context.Background(), provider, cwd,
				aisessions.DiscoverOptions{HomeDir: store, Depth: depth, DeferTurns: true}, 0)
			if err != nil {
				t.Fatalf("census %s depth=%d: %v", provider, depth, err)
			}
			into[provider] = len(discovery.Sessions)
		}
	}
	return census
}

func (c scaleResumeCensus) log(t *testing.T, store string) {
	t.Helper()
	t.Logf("same-run census store=%s rollout_files=%d", store, c.rolloutFiles)
	for _, provider := range scaleResumeProviders {
		t.Logf("same-run census provider=%-11s depth0=%d depth5=%d", provider, c.depth0[provider], c.depth5[provider])
	}
}

// scaleRolloutManifest is the content-free mutation ledger of one rollout
// store: path, size, and modification time per rollout file.
func scaleRolloutManifest(t *testing.T, sessionsRoot string) map[string]string {
	t.Helper()
	manifest := map[string]string{}
	err := filepath.Walk(sessionsRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() && strings.HasSuffix(path, ".jsonl") {
			manifest[path] = fmt.Sprintf("%d/%d", info.Size(), info.ModTime().UnixNano())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk rollout store %s: %v", sessionsRoot, err)
	}
	return manifest
}

func assertScaleStoreUnchanged(t *testing.T, label string, before, after map[string]string) {
	t.Helper()
	var drift []string
	for path, state := range before {
		if got, ok := after[path]; !ok {
			drift = append(drift, "removed "+path)
		} else if got != state {
			drift = append(drift, "rewrote "+path)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			drift = append(drift, "added "+path)
		}
	}
	if len(drift) > 0 {
		t.Errorf("%s: rollout mutation ledger is not empty: %d entries, first=%q", label, len(drift), drift[0])
	}
}

// scaleLiveStoreGuard pins the operator's real rollout store for the duration
// of the smoke. Every observation runs against a copy, so the live store must
// be byte-identical when the smoke ends. This is the only place it is named,
// and it is only ever read.
func scaleLiveStoreGuard(t *testing.T) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve user home: %v", err)
	}
	live := filepath.Join(home, ".codex", "sessions")
	if info, err := os.Stat(live); err != nil || !info.IsDir() {
		t.Skipf("live rollout store %s is unavailable: %v", live, err)
	}
	before := scaleRolloutManifest(t, live)
	t.Logf("live store guard: %s rollout_files=%d", live, len(before))
	t.Cleanup(func() {
		assertScaleStoreUnchanged(t, "live user rollout store", before, scaleRolloutManifest(t, live))
	})
}

// assertScaleOutsideLiveStore refuses any fixture path that resolves inside
// the operator's live agent stores.
func assertScaleOutsideLiveStore(t *testing.T, path string) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve user home: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		t.Fatalf("resolve %s: %v", path, err)
	}
	for _, live := range []string{
		filepath.Join(home, ".codex"), filepath.Join(home, ".claude"), filepath.Join(home, ".gemini"),
	} {
		liveResolved, err := filepath.EvalSymlinks(live)
		if err != nil {
			continue
		}
		if resolved == liveResolved || strings.HasPrefix(resolved, liveResolved+string(filepath.Separator)) {
			t.Fatalf("fixture path %s resolves inside the live store %s", resolved, liveResolved)
		}
	}
}

// scaleResumeArm is one settled invocation of the picker's population path.
type scaleResumeArm struct {
	entries     int
	footer      string
	projections map[string]aiResumeProviderProjection
	elapsed     map[string]time.Duration
	wall        time.Duration
	frameHash   [32]byte
	lateHash    [32]byte
	rows        []aisessions.ResumeSummary
}

// blackholeSocket is a listener that accepts a connection and then never
// answers. It is the exact shape of a slow native endpoint: reachable, so the
// route is valid and the read is really attempted, but unable to settle inside
// the declared native window.
func blackholeSocket(t *testing.T, path string) string {
	t.Helper()
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen blackhole socket: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			context.AfterFunc(t.Context(), func() { _ = conn.Close() })
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Errorf("remove blackhole socket: %v", err)
		}
	})
	return path
}

func scaleResumeRoute(socketPath, generation, tui string) codexNativeEndpointRoute {
	return codexNativeEndpointRoute{
		Endpoint:      coremetadata.CodexEndpointRef{StateDomainID: "installed-scale-domain", EndpointGenerationID: generation},
		State:         coremetadata.CodexGenerationCurrent,
		SocketPath:    socketPath,
		TUIExecutable: tui,
	}
}

// runScaleResumeArm settles one full picker population against the fixture
// store and returns the exact observable result: the published frame, the
// provider projections behind the footer, and each provider's own elapsed
// witness.
func runScaleResumeArm(t *testing.T, store, cwd string, configure func(*aiCommand)) scaleResumeArm {
	t.Helper()
	cmd := testAICommand(t.TempDir())
	if configure != nil {
		configure(cmd)
	}
	controller := newAIResumeLiveController(cmd, cwd, store, 5, 20)
	defer controller.close()
	started := time.Now()
	entries := controller.initialEntries()
	wall := time.Since(started)
	footer, _ := controller.footer()

	controller.mu.Lock()
	projections := make(map[string]aiResumeProviderProjection, len(controller.providerStates))
	maps.Copy(projections, controller.providerStates)
	elapsed := make(map[string]time.Duration, len(controller.providerElapsed))
	maps.Copy(elapsed, controller.providerElapsed)
	rows := append([]aisessions.ResumeSummary(nil), controller.summaries...)
	controller.mu.Unlock()

	hash := sha256.Sum256(append([]byte(footer+"\x00"), frameBytes(entries)...))

	// A native read this arm abandoned can still return after the frame is
	// published. Hold the invocation open past the declared provider terminal
	// and hash the published surface again: an immutable frame is the whole
	// point of settling providers independently.
	time.Sleep(settleWatch)
	lateFooter, _ := controller.footer()
	lateHash := sha256.Sum256(append([]byte(lateFooter+"\x00"), frameBytes(controller.entries(controller.snapshotSummaries()))...))

	return scaleResumeArm{
		entries: len(entries), footer: footer, projections: projections,
		elapsed: elapsed, wall: wall, frameHash: hash, lateHash: lateHash, rows: rows,
	}
}

// settleWatch is the window a settled frame is watched for late mutation. It
// is the declared provider terminal, so every abandoned stage of the arm has
// had its full bound to try to write into an already published result.
var settleWatch = defaultAIResumeBudgets().providerTerminal()

func frameBytes(entries []intpickercompat.Entry) []byte {
	var encoded []byte
	for _, entry := range entries {
		encoded = append(encoded, entry.Value...)
		encoded = append(encoded, 0)
		encoded = append(encoded, entry.Label...)
		encoded = append(encoded, 0)
	}
	return encoded
}

// TestInstalledResumePickerSettlesProviderTruthOnTheExactPrivateGeneration is
// the Phase 4 installed closure. Everything C-1 through C-4 fixed with virtual
// clocks and synthetic trees is replayed here once against the exact private
// generation and a read-only copy of a real-scale store: the source a provider
// publishes, the status behind its footer count, the clock each provider is
// measured on, and the cleanup the run leaves behind. Nothing is compared with
// a planning literal; every count is compared with the same-run census.
func TestInstalledResumePickerSettlesProviderTruthOnTheExactPrivateGeneration(t *testing.T) {
	root, enabled, err := codexinstalled.SmokeRoot("PROJMUX_CODEX_SCALE_RESUME_SMOKE_ROOT")
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Skip("set PROJMUX_CODEX_SCALE_RESUME_SMOKE_ROOT for the installed resume picker closure smoke")
	}
	binary := strings.TrimSpace(os.Getenv("PROJMUX_CODEX_SCALE_RESUME_BINARY"))
	if !filepath.IsAbs(binary) {
		t.Fatal("PROJMUX_CODEX_SCALE_RESUME_BINARY must be an absolute path to the exact private generation")
	}
	version := strings.TrimSpace(os.Getenv("PROJMUX_CODEX_SCALE_RESUME_VERSION"))
	if version == "" {
		t.Fatal("PROJMUX_CODEX_SCALE_RESUME_VERSION must name the exact expected generation")
	}
	store := strings.TrimSpace(os.Getenv("PROJMUX_CODEX_SCALE_RESUME_STORE"))
	if !filepath.IsAbs(store) {
		t.Fatal("PROJMUX_CODEX_SCALE_RESUME_STORE must be an absolute path to a read-only store copy")
	}
	cwd := strings.TrimSpace(os.Getenv("PROJMUX_CODEX_SCALE_RESUME_CWD"))
	if !filepath.IsAbs(cwd) {
		t.Fatal("PROJMUX_CODEX_SCALE_RESUME_CWD must be the absolute workspace the picker resumes from")
	}

	scaleLiveStoreGuard(t)
	assertScaleOutsideLiveStore(t, store)
	sessions := filepath.Join(store, ".codex", "sessions")
	census := takeScaleResumeCensus(t, store, cwd)
	census.log(t, store)
	beforeStore := scaleRolloutManifest(t, sessions)
	if len(beforeStore) != census.rolloutFiles {
		t.Fatalf("rollout manifest holds %d files, want the same-run census %d", len(beforeStore), census.rolloutFiles)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	codexHome := filepath.Join(root, "codex-home")
	assertScaleOutsideLiveStore(t, codexHome)
	selfReported, err := scaleGenerationVersion(ctx, binary)
	if err != nil || selfReported != version {
		t.Fatalf("exact generation binary %s self-reports %q (err=%v), want %q", binary, selfReported, err, version)
	}
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("PATH", filepath.Dir(binary)+string(os.PathListSeparator)+os.Getenv("PATH"))
	fixture, err := codexinstalled.NewExisting(root)
	if err != nil {
		t.Fatal(err)
	}
	fixture.ApplyEnv(t.Setenv)
	t.Cleanup(func() {
		if err := fixture.Cleanup(); err != nil {
			t.Errorf("installed resume fixture cleanup: %v", err)
		}
	})
	direct, ready := fixture.StartDirect(ctx, "installed-scale-resume-smoke", binary)
	if ready.Class != codexinstalled.ResultPass {
		t.Fatalf("installed resume direct endpoint = %+v", ready)
	}
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer closeCancel()
		if result := direct.Close(closeCtx); result.Class != codexinstalled.ResultPass {
			t.Errorf("installed resume direct close = %+v", result)
		}
	})
	if got := fixture.Versions().AppServer; got != version {
		t.Fatalf("running app-server generation = %q, want exact %q", got, version)
	}
	// The managed payload link completes the version tuple a terminal result
	// must carry. It is the one artifact this smoke adds to the copy, and it is
	// removed again so the copy stays reusable and pristine.
	if provision := fixture.ProvisionManagedPayload(); provision.Class != codexinstalled.ResultPass {
		t.Fatalf("installed resume managed payload provision = %+v", provision)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(filepath.Join(codexHome, "packages")); err != nil {
			t.Errorf("installed resume managed payload cleanup: %v", err)
		}
	})
	t.Logf("exact private generation cli=%s app_server=%s socket=%s",
		fixture.Versions().CLI, fixture.Versions().AppServer, fixture.SocketPath)

	nativeRoute := scaleResumeRoute(fixture.SocketPath, "installed-scale-native", binary)
	slowRoute := scaleResumeRoute(blackholeSocket(t, filepath.Join(root, "slow.sock")), "installed-scale-slow", binary)
	missingRoute := scaleResumeRoute(filepath.Join(root, "absent.sock"), "installed-scale-absent", binary)
	for name, route := range map[string]codexNativeEndpointRoute{"native": nativeRoute, "slow": slowRoute, "absent": missingRoute} {
		if !route.valid() {
			t.Fatalf("%s route is not a valid generation route: %+v", name, route)
		}
	}

	arms := map[string]scaleResumeArm{}
	for _, arm := range []struct {
		name      string
		configure func(*aiCommand)
	}{
		{name: "native", configure: func(cmd *aiCommand) {
			cmd.codexNative = &fakeNativeThreadController{catalogRoutes: []codexNativeEndpointRoute{nativeRoute}}
		}},
		{name: "slow-native", configure: func(cmd *aiCommand) {
			cmd.codexNative = &fakeNativeThreadController{catalogRoutes: []codexNativeEndpointRoute{slowRoute}}
		}},
		{name: "error-native", configure: func(cmd *aiCommand) {
			cmd.codexNative = &fakeNativeThreadController{catalogRoutes: []codexNativeEndpointRoute{missingRoute}}
		}},
		{name: "codex-disabled", configure: func(cmd *aiCommand) {
			home, err := cmd.homeDir()
			if err != nil {
				t.Fatalf("resolve arm config home: %v", err)
			}
			if err := config.SaveAIEnabledAgentsFile(
				filepath.Join(home, ".config", "projmux", config.AIEnabledAgentsFileName),
				[]config.AIAgentProvider{config.AIAgentClaude, config.AIAgentAntigravity}); err != nil {
				t.Fatalf("disable codex for the sibling-independence arm: %v", err)
			}
		}},
	} {
		result := runScaleResumeArm(t, store, cwd, arm.configure)
		arms[arm.name] = result
		t.Logf("arm=%-14s wall=%-14s codex=%s claude=%s antigravity=%s footer=%q",
			arm.name, result.wall,
			scaleProviderEvidence(result, aiModeCodex),
			scaleProviderEvidence(result, aiModeClaude),
			scaleProviderEvidence(result, aiModeAntigravity),
			result.footer)
	}

	budgets := defaultAIResumeBudgets()
	terminal := budgets.providerTerminal()

	// Acceptance 2: a native that cannot settle inside its window must not cost
	// the invocation the rollout rows it already holds. The exact number is the
	// same-run depth-5 census, not a planning literal.
	for _, name := range []string{"slow-native", "error-native"} {
		arm := arms[name]
		codex := arm.projections[aiModeCodex]
		if codex.state != aiResumeProviderCount {
			t.Errorf("arm=%s Codex state = %q, want %q with retained rollout rows", name, codex.state, aiResumeProviderCount)
		}
		if codex.count != census.depth5[aiModeCodex] {
			t.Errorf("arm=%s Codex count = %d, want the same-run depth-5 census %d", name, codex.count, census.depth5[aiModeCodex])
		}
		for _, row := range arm.rows {
			if row.Provider != aiModeCodex {
				continue
			}
			if row.Source != aisessions.SourceCodexRollout {
				t.Errorf("arm=%s published a Codex row with source %q, want %q", name, row.Source, aisessions.SourceCodexRollout)
			}
			if row.StateDomainID != "" || row.EndpointGenerationID != "" || row.GenerationState != "" {
				t.Errorf("arm=%s rollout row carries generation authority: %+v", name, row)
			}
		}
	}

	// Acceptance 4 (source/authority): a native settlement publishes native rows
	// that carry this route's exact generation identity, and nothing else.
	native := arms["native"]
	if got := native.projections[aiModeCodex]; got.state != aiResumeProviderCount || got.count == 0 {
		t.Errorf("arm=native Codex projection = %+v, want a non-empty count from the exact private generation", got)
	}
	nativeRows := 0
	for _, row := range native.rows {
		if row.Provider != aiModeCodex {
			continue
		}
		nativeRows++
		if row.Source != aisessions.SourceCodexAppServer {
			t.Errorf("arm=native published a Codex row with source %q, want %q", row.Source, aisessions.SourceCodexAppServer)
		}
		if row.StateDomainID != nativeRoute.Endpoint.StateDomainID ||
			row.EndpointGenerationID != nativeRoute.Endpoint.EndpointGenerationID ||
			row.GenerationState != string(nativeRoute.State) {
			t.Errorf("arm=native row generation identity = %q/%q/%q, want the exact resolved route",
				row.StateDomainID, row.EndpointGenerationID, row.GenerationState)
		}
	}
	if nativeRows == 0 {
		t.Error("arm=native published no Codex rows from the exact private generation")
	}

	// Acceptance 3: sibling providers are their own truth in every arm, and the
	// Codex-enabled and Codex-disabled runs settle them identically.
	for name, arm := range arms {
		for _, provider := range []string{aiModeClaude, aiModeAntigravity} {
			projection := arm.projections[provider]
			if projection.state != aiResumeProviderCount {
				t.Errorf("arm=%s %s state = %q, want %q", name, provider, projection.state, aiResumeProviderCount)
			}
			if projection.count != census.depth5[provider] {
				t.Errorf("arm=%s %s count = %d, want the same-run depth-5 census %d",
					name, provider, projection.count, census.depth5[provider])
			}
			if arm.elapsed[provider] > terminal {
				t.Errorf("arm=%s %s elapsed = %s, outside its own declared bound", name, provider, arm.elapsed[provider])
			}
		}
		if got := arm.projections[aiModeCodex]; name == "codex-disabled" && got.state != aiResumeProviderDisabled {
			t.Errorf("arm=%s Codex state = %q, want %q", name, got.state, aiResumeProviderDisabled)
		}
	}

	// Acceptance 4: every provider settles inside the declared worst case and
	// no provider inherits a sibling's spent time.
	for name, arm := range arms {
		if arm.wall > terminal+500*time.Millisecond {
			t.Errorf("arm=%s settled the whole frame in %s, outside the declared provider terminal %s", name, arm.wall, terminal)
		}
		for provider, witness := range arm.elapsed {
			if witness > terminal {
				t.Errorf("arm=%s %s elapsed = %s, outside the declared provider terminal %s", name, provider, witness, terminal)
			}
		}
	}
	// The slow arm is the one that proves the clocks are separate: Codex spends
	// its own window there while the siblings settle on theirs.
	slow := arms["slow-native"]
	if slow.elapsed[aiModeCodex] <= slow.elapsed[aiModeClaude] {
		t.Errorf("slow Codex elapsed %s did not outlast the Claude witness %s on independent clocks",
			slow.elapsed[aiModeCodex], slow.elapsed[aiModeClaude])
	}

	// Acceptance 4 (late mutation): no arm's published surface changed after it
	// settled, even though every abandoned stage had its full bound to try.
	for name, arm := range arms {
		if arm.lateHash != arm.frameHash {
			t.Errorf("arm=%s mutated its published frame after settlement", name)
		}
	}

	// Acceptance 6: the smoke leaves the fixture rollout store exactly as it
	// found it, and the live-store guard registered above proves the same for
	// the store a person actually resumes from.
	assertScaleStoreUnchanged(t, "fixture rollout store", beforeStore, scaleRolloutManifest(t, sessions))

	result := codexinstalled.NewResult(fixture.Versions(), codexinstalled.TopologyDirect,
		codexinstalled.StageReady, codexinstalled.ResultPass, "resume-picker-settlement-real-scale")
	encoded, err := result.JSON()
	if err != nil {
		t.Fatalf("invalid installed qualification result: %v", err)
	}
	t.Logf("installed-result: %s", encoded)
}

func scaleProviderEvidence(arm scaleResumeArm, provider string) string {
	projection := arm.projections[provider]
	return fmt.Sprintf("%s:%s/%d@%s", provider, projection.state, projection.count, arm.elapsed[provider])
}

func scaleGenerationVersion(ctx context.Context, binary string) (string, error) {
	command := exec.CommandContext(ctx, binary, "--version") // #nosec G204 -- explicit absolute test input.
	output, err := command.CombinedOutput()
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(output))
	if len(fields) == 0 {
		return "", fmt.Errorf("empty version output")
	}
	return fields[len(fields)-1], nil
}
