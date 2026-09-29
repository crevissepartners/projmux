package usagecmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/usage"
	codexadapter "github.com/crevissepartners/projmux/internal/core/usage/adapters/codex"
	"github.com/crevissepartners/projmux/internal/diagnostics"
)

// backgroundRefreshHarness is a Command scoped to Claude and Codex whose
// read-only manager walks stub adapters over a private state dir, with the
// native watcher start and the journal observable.
type backgroundRefreshHarness struct {
	cmd           *Command
	stateDir      string
	store         *usage.Store
	claude        *throttleHintedStubAdapter
	codex         *stubAdapter
	clock         *time.Time
	watcherStarts int
	readOnlyCalls int
	journal       *diagnostics.Store
}

func newBackgroundRefreshHarness(t *testing.T) *backgroundRefreshHarness {
	t.Helper()
	h := &backgroundRefreshHarness{stateDir: t.TempDir()}
	clock := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	h.clock = &clock
	now := func() time.Time { return *h.clock }

	h.claude = &throttleHintedStubAdapter{
		stubAdapter: stubAdapter{name: "claude", snaps: []usage.Snapshot{{
			Model: "claude", Window: usage.Window5h, Bucket: "claude", Pct: 42,
		}}},
		hint: 5 * time.Minute,
	}
	h.codex = &stubAdapter{name: codexadapter.Name, snaps: []usage.Snapshot{{
		Model: codexadapter.Name, Window: usage.Window5h, Bucket: "codex", Pct: 99,
	}}}
	registry := usage.NewRegistry()
	if err := registry.Replace(h.claude); err != nil {
		t.Fatal(err)
	}
	if err := registry.Replace(h.codex); err != nil {
		t.Fatal(err)
	}
	h.store = usage.NewStore(h.stateDir)
	manager := usage.NewManager(registry, h.store, now)

	h.cmd = New(now)
	h.cmd.managerFn = func([]string) (*usage.Manager, error) {
		t.Error("the status-line refresh must not build the gesture manager, which may start the Codex daemon")
		return manager, nil
	}
	h.cmd.readOnlyManagerFn = func([]string) (*usage.Manager, error) {
		h.readOnlyCalls++
		return manager, nil
	}
	h.cmd.enabledAgentsFn = func() ([]config.AIAgentProvider, error) {
		return []config.AIAgentProvider{config.AIAgentClaude, config.AIAgentCodex}, nil
	}
	h.cmd.executableFn = func() (string, error) { return "/test/projmux", nil }
	h.cmd.startNativeWatcherFn = func(string) error {
		h.watcherStarts++
		return nil
	}
	h.cmd.lookupEnv = func(name string) string {
		switch name {
		case StateDirEnvVar, "HOME":
			return h.stateDir
		case "XDG_CONFIG_HOME":
			return filepath.Join(h.stateDir, "config")
		}
		return ""
	}
	var journalFn func() *diagnostics.UsageRecorder
	journalFn, h.journal = newTestUsageJournal(t)
	h.cmd.journalFn = journalFn
	return h
}

// publishNativeBatch leaves a fresh Codex batch and watcher heartbeat in the
// state dir, the way a running native watcher publishes one.
func (h *backgroundRefreshHarness) publishNativeBatch(t *testing.T) {
	t.Helper()
	clock := *h.clock
	limitID := "codex"
	label := "General"
	cadence := int64(300)
	if err := codexadapter.NewNativeEventCache(h.stateDir, func() time.Time { return clock }).Publish([]usage.Snapshot{{
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
	if err := touchNativeWatcherMarker(nativeWatcherPath(h.stateDir, nativeWatcherHeartbeatName), clock); err != nil {
		t.Fatal(err)
	}
}

// TestBackgroundRefreshUsesTheReadOnlyManagerAndStartsNoWatcher pins C-2's
// authority: the timer entry point refreshes through the read-only manager
// and never starts the native watcher, while RunStatus on the same state
// still does.
func TestBackgroundRefreshUsesTheReadOnlyManagerAndStartsNoWatcher(t *testing.T) {
	h := newBackgroundRefreshHarness(t)

	refreshed, err := h.cmd.BackgroundRefresh(context.Background())
	if err != nil || !refreshed {
		t.Fatalf("BackgroundRefresh = (%t, %v), want (true, nil)", refreshed, err)
	}
	if h.readOnlyCalls != 1 {
		t.Fatalf("read-only manager builds = %d, want 1", h.readOnlyCalls)
	}
	if h.watcherStarts != 0 {
		t.Fatalf("native watcher starts = %d, want 0", h.watcherStarts)
	}
	if _, err := os.Stat(nativeWatcherPath(h.stateDir, nativeWatcherDemandName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("watcher demand marker stat = %v, want not exist: only RunStatus keeps the watcher alive", err)
	}
	if h.claude.collectCalls != 1 || h.codex.collectCalls != 1 {
		t.Fatalf("collect calls claude=%d codex=%d, want 1 each", h.claude.collectCalls, h.codex.collectCalls)
	}

	// The harness does observe a watcher start: RunStatus takes one.
	var stdout, stderr bytes.Buffer
	if err := h.cmd.RunStatus(nil, &stdout, &stderr); err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	if h.watcherStarts != 1 {
		t.Fatalf("native watcher starts after RunStatus = %d, want 1", h.watcherStarts)
	}
}

// TestBackgroundRefreshSecondCallInTheSameWindowIsANoOp pins that the timer
// entry point keeps the status line's floor.
func TestBackgroundRefreshSecondCallInTheSameWindowIsANoOp(t *testing.T) {
	h := newBackgroundRefreshHarness(t)

	if refreshed, err := h.cmd.BackgroundRefresh(context.Background()); err != nil || !refreshed {
		t.Fatalf("first BackgroundRefresh = (%t, %v), want (true, nil)", refreshed, err)
	}
	*h.clock = h.clock.Add(5 * time.Second)
	refreshed, err := h.cmd.BackgroundRefresh(context.Background())
	if err != nil || refreshed {
		t.Fatalf("second BackgroundRefresh = (%t, %v), want (false, nil)", refreshed, err)
	}
	if h.claude.collectCalls != 1 || h.codex.collectCalls != 1 {
		t.Fatalf("collect calls claude=%d codex=%d, want 1 each", h.claude.collectCalls, h.codex.collectCalls)
	}
}

// TestBackgroundRefreshTakesAFreshNativeBatchInsteadOfCodex pins that an
// accepted native batch stands in for the Codex walk only: another adapter
// past its floor is still collected.
func TestBackgroundRefreshTakesAFreshNativeBatchInsteadOfCodex(t *testing.T) {
	h := newBackgroundRefreshHarness(t)
	start := *h.clock
	if err := h.store.SaveState(usage.State{
		LastCollect: map[string]time.Time{"claude": start, codexadapter.Name: start},
	}); err != nil {
		t.Fatal(err)
	}
	*h.clock = start.Add(h.claude.hint + time.Second)
	h.publishNativeBatch(t)

	refreshed, err := h.cmd.BackgroundRefresh(context.Background())
	if err != nil || !refreshed {
		t.Fatalf("BackgroundRefresh = (%t, %v), want (true, nil)", refreshed, err)
	}
	if h.codex.collectCalls != 0 {
		t.Fatalf("codex Collect calls = %d, want 0 — the accepted batch stands in for it", h.codex.collectCalls)
	}
	if h.claude.collectCalls != 1 {
		t.Fatalf("claude Collect calls = %d, want 1 — it is past its floor", h.claude.collectCalls)
	}
	state, err := h.store.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if got := state.LastCollect[codexadapter.Name]; !got.Equal(*h.clock) {
		t.Fatalf("codex last_collect = %s, want the batch time %s", got, *h.clock)
	}
}

// TestBackgroundRefreshWithNoUsageScopeTouchesNothing pins the empty ambient
// scope: nothing is built, collected, or written.
func TestBackgroundRefreshWithNoUsageScopeTouchesNothing(t *testing.T) {
	h := newBackgroundRefreshHarness(t)
	h.cmd.enabledAgentsFn = func() ([]config.AIAgentProvider, error) { return nil, nil }

	refreshed, err := h.cmd.BackgroundRefresh(context.Background())
	if err != nil || refreshed {
		t.Fatalf("BackgroundRefresh = (%t, %v), want (false, nil)", refreshed, err)
	}
	if h.readOnlyCalls != 0 || h.claude.collectCalls != 0 || h.codex.collectCalls != 0 {
		t.Fatalf("manager builds=%d claude=%d codex=%d, want 0", h.readOnlyCalls, h.claude.collectCalls, h.codex.collectCalls)
	}
	entries, err := os.ReadDir(h.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("state dir entries = %v, want none", entries)
	}
	if events, err := h.journal.Read(); err != nil || len(events) != 0 {
		t.Fatalf("journal rows = %#v (err %v), want none", events, err)
	}
}

// TestBackgroundRefreshReturnsAndJournalsAFailure pins the diagnostics step:
// an adapter failure is returned to the caller and journaled like RunStatus.
func TestBackgroundRefreshReturnsAndJournalsAFailure(t *testing.T) {
	h := newBackgroundRefreshHarness(t)
	h.cmd.enabledAgentsFn = func() ([]config.AIAgentProvider, error) {
		return []config.AIAgentProvider{config.AIAgentClaude}, nil
	}
	h.claude.err = errors.New("claude: usage endpoint returned status 500")

	_, err := h.cmd.BackgroundRefresh(context.Background())
	if err == nil {
		t.Fatal("BackgroundRefresh error = nil, want the claude failure")
	}
	events, readErr := h.journal.Read()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(events) != 1 || events[0].Component != "usage" || events[0].Provider != string(diagnostics.ProviderClaude) {
		t.Fatalf("journal rows = %#v, want one claude usage row", events)
	}
}
