package usagecmd

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/usage"
	codexadapter "github.com/crevissepartners/projmux/internal/core/usage/adapters/codex"
)

// throttleHintedStubAdapter is a stubAdapter that advertises its own
// minimum collection interval, the way the Claude OAuth adapter advertises
// its 5-minute floor.
type throttleHintedStubAdapter struct {
	stubAdapter
	hint time.Duration
}

func (s *throttleHintedStubAdapter) ThrottleHint() time.Duration { return s.hint }

// TestFreshEventBatchOnEveryTickStillWalksTheDueClaudeAdapter pins C-2: a
// status tick that accepts a native Codex event batch still walks every
// OTHER adapter that has passed its own floor.
//
// This is the Phase 0 reproduction harness inverted. Before the fix the
// accept branch returned early, so while the Codex native watcher kept
// publishing a newer batch on every tick, an adapter 30 minutes past a
// 5-minute floor was never offered to the throttle gate at all — the live
// symptom was Claude usage frozen for 35 minutes.
//
// No network and no credentials are involved: both adapters are stubs and
// the batch arrives through the on-disk event cache, exactly as the live
// watcher publishes it.
func TestFreshEventBatchOnEveryTickStillWalksTheDueClaudeAdapter(t *testing.T) {
	stateDir := t.TempDir()
	start := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	clock := start
	now := func() time.Time { return clock }

	const claudeFloor = 5 * time.Minute
	claude := &throttleHintedStubAdapter{
		stubAdapter: stubAdapter{name: "claude", snaps: []usage.Snapshot{{
			Model: "claude", Window: usage.Window5h, Bucket: "claude", Pct: 42,
		}}},
		hint: claudeFloor,
	}
	codex := &stubAdapter{name: codexadapter.Name, snaps: []usage.Snapshot{{
		Model: codexadapter.Name, Window: usage.Window5h, Bucket: "codex", Pct: 99,
	}}}
	registry := usage.NewRegistry()
	if err := registry.Replace(claude); err != nil {
		t.Fatal(err)
	}
	if err := registry.Replace(codex); err != nil {
		t.Fatal(err)
	}
	store := usage.NewStore(stateDir)
	manager := usage.NewManager(registry, store, now)

	// Seed both adapters as freshly collected so the first tick starts
	// from a healthy steady state.
	if err := store.SaveState(usage.State{
		LastCollect: map[string]time.Time{"claude": start, codexadapter.Name: start},
	}); err != nil {
		t.Fatal(err)
	}

	command := New(now)
	command.managerFn = func([]string) (*usage.Manager, error) { return manager, nil }
	command.enabledAgentsFn = func() ([]config.AIAgentProvider, error) {
		return []config.AIAgentProvider{config.AIAgentClaude, config.AIAgentCodex}, nil
	}
	command.startNativeWatcherFn = func(string) error { return nil }
	command.lookupEnv = func(name string) string {
		switch name {
		case StateDirEnvVar:
			return stateDir
		case "HOME":
			return stateDir
		case "XDG_CONFIG_HOME":
			return filepath.Join(stateDir, "config")
		}
		return ""
	}
	saveHUDWindowVisibility(t, command, "codex", "5h", config.StatusbarVisibilityOn)

	limitID := "codex"
	label := "General"
	cadence := int64(300)
	cache := codexadapter.NewNativeEventCache(stateDir, now)
	const tickEvery = 5 * time.Second
	const ticks = 360 // 30 minutes of status-bar ticks

	for i := range ticks {
		clock = clock.Add(tickEvery)
		// The live watcher publishes a batch newer than the one already
		// applied; that is the whole precondition for the accept branch.
		if err := cache.Publish([]usage.Snapshot{{
			Model: codexadapter.Name, Window: usage.Window5h, Bucket: "codex",
			Pct: 50, UpdatedAt: clock, Source: usage.SourceAppServer,
			ResetsAt: clock.Add(time.Hour),
			RateLimit: &usage.RateLimitMetadata{
				BucketKey: "codex", LimitID: &limitID, Label: &label,
				Slot: "primary", CadenceMinutes: &cadence,
			},
		}}); err != nil {
			t.Fatal(err)
		}
		if err := touchNativeWatcherMarker(
			nativeWatcherPath(stateDir, nativeWatcherHeartbeatName), clock,
		); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		if err := command.RunStatus(nil, &stdout, &stderr); err != nil {
			t.Fatalf("tick %d: RunStatus: %v", i, err)
		}
	}

	elapsed := time.Duration(ticks) * tickEvery
	state, err := store.LoadState()
	if err != nil {
		t.Fatal(err)
	}

	// Batch acceptance itself is unchanged: Codex keeps advancing through
	// ApplySnapshots on every tick, and the accepted batch still suppresses
	// Codex's own source decision in that same refresh.
	if got := state.LastCollect[codexadapter.Name]; !got.Equal(clock) {
		t.Fatalf("codex last_collect = %s, want the last tick %s", got, clock)
	}
	if codex.collectCalls != 0 {
		t.Fatalf("codex Collect calls = %d, want 0 — an accepted batch must "+
			"still suppress the codex source decision in the same refresh",
			codex.collectCalls)
	}

	// The fix: Claude is walked once per floor even though every tick
	// accepted a batch.
	wantCalls := int(elapsed / claudeFloor)
	if claude.collectCalls != wantCalls {
		t.Fatalf("claude Collect calls = %d after %s of batch-accepted ticks, want %d "+
			"(one per %s floor)", claude.collectCalls, elapsed, wantCalls, claudeFloor)
	}
	wantLast := start.Add(time.Duration(wantCalls) * claudeFloor)
	if got := state.LastCollect["claude"]; !got.Equal(wantLast) {
		t.Fatalf("claude last_collect = %s after %s, want %s", got, elapsed, wantLast)
	}
}

// TestMaybeCollectExceptLeavesTheNamedAdapterOutOfTheWalk pins the Manager
// half of the same contract without the status-bar plumbing: excluding one
// adapter must not suppress a different adapter that is due.
func TestMaybeCollectExceptLeavesTheNamedAdapterOutOfTheWalk(t *testing.T) {
	start := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	clock := start.Add(time.Minute)
	claude := &stubAdapter{name: "claude", snaps: []usage.Snapshot{{
		Model: "claude", Window: usage.Window5h, Bucket: "claude", Pct: 42,
	}}}
	codex := &stubAdapter{name: codexadapter.Name, snaps: []usage.Snapshot{{
		Model: codexadapter.Name, Window: usage.Window5h, Bucket: "codex", Pct: 99,
	}}}
	registry := usage.NewRegistry()
	if err := registry.Replace(claude); err != nil {
		t.Fatal(err)
	}
	if err := registry.Replace(codex); err != nil {
		t.Fatal(err)
	}
	store := usage.NewStore(t.TempDir())
	if err := store.SaveState(usage.State{
		LastCollect: map[string]time.Time{"claude": start, codexadapter.Name: start},
	}); err != nil {
		t.Fatal(err)
	}
	manager := usage.NewManager(registry, store, func() time.Time { return clock })

	ran, err := manager.MaybeCollectExcept(
		context.Background(), 30*time.Second, codexadapter.Name,
	)
	if err != nil {
		t.Fatalf("MaybeCollectExcept: %v", err)
	}
	if !ran {
		t.Fatal("MaybeCollectExcept ran = false, want true for the due claude adapter")
	}
	if claude.collectCalls != 1 {
		t.Fatalf("claude Collect calls = %d, want 1", claude.collectCalls)
	}
	if codex.collectCalls != 0 {
		t.Fatalf("codex Collect calls = %d, want 0 for the excluded adapter", codex.collectCalls)
	}

	state, err := store.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if got := state.LastCollect[codexadapter.Name]; !got.Equal(start) {
		t.Fatalf("codex last_collect = %s, want it untouched at %s", got, start)
	}
	if got := state.LastCollect["claude"]; !got.Equal(clock) {
		t.Fatalf("claude last_collect = %s, want %s", got, clock)
	}

	// Excluding every registered adapter leaves nothing due, so the walk
	// does not run at all.
	ran, err = manager.MaybeCollectExcept(
		context.Background(), 30*time.Second, "claude", codexadapter.Name,
	)
	if err != nil || ran {
		t.Fatalf("MaybeCollectExcept(all) = ran %v err %v, want false and no error", ran, err)
	}
}
