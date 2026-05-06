package usage

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// countingAdapter records how many times Collect has been invoked. Used
// by the MaybeCollect tests to assert the throttle gate actually blocks
// repeated adapter walks.
type countingAdapter struct {
	name  string
	calls int64
	snaps []Snapshot
	err   error
}

func (c *countingAdapter) Name() string { return c.name }

func (c *countingAdapter) Collect(ctx context.Context) ([]Snapshot, error) {
	atomic.AddInt64(&c.calls, 1)
	return c.snaps, c.err
}

func (c *countingAdapter) Calls() int64 {
	return atomic.LoadInt64(&c.calls)
}

func newTestManager(t *testing.T, adapter Adapter, now time.Time) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	registry := NewRegistry()
	if adapter != nil {
		if err := registry.Register(adapter); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	store := NewStore(dir)
	clock := now
	mgr := NewManager(registry, store, func() time.Time { return clock })
	return mgr, dir
}

func TestCollectPersistsSnapshots(t *testing.T) {
	t.Parallel()

	now := mustTime(t, "2026-05-06T12:00:00Z")
	adapter := &countingAdapter{
		name: "claude",
		snaps: []Snapshot{
			{Model: "claude", Window: Window5h, Pct: 9.0, ResetsAt: mustTime(t, "2026-05-06T17:00:00Z")},
		},
	}
	mgr, dir := newTestManager(t, adapter, now)

	got, err := mgr.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(got) != 1 || got[0].Pct != 9.0 {
		t.Fatalf("Collect returned = %+v, want one snapshot at 9%%", got)
	}
	// Persisted file must contain the same data.
	store := NewStore(dir)
	loaded, last, err := store.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(loaded) != 1 || loaded[0].Pct != 9.0 {
		t.Fatalf("loaded = %+v, want one at 9%%", loaded)
	}
	if !last.Equal(now) {
		t.Fatalf("last_collect = %v, want %v", last, now)
	}
}

func TestMaybeCollectRunsWhenSnapshotsMissing(t *testing.T) {
	t.Parallel()

	now := mustTime(t, "2026-05-06T12:00:00Z")
	adapter := &countingAdapter{name: "claude"}
	mgr, _ := newTestManager(t, adapter, now)

	ran, err := mgr.MaybeCollect(context.Background(), 30*time.Second)
	if err != nil {
		t.Fatalf("MaybeCollect: %v", err)
	}
	if !ran {
		t.Fatalf("ran=false, want true (file missing)")
	}
	if adapter.Calls() != 1 {
		t.Fatalf("adapter calls = %d, want 1", adapter.Calls())
	}
}

func TestMaybeCollectThrottlesWithinWindow(t *testing.T) {
	t.Parallel()

	now := mustTime(t, "2026-05-06T12:00:00Z")
	adapter := &countingAdapter{name: "claude"}
	mgr, _ := newTestManager(t, adapter, now)

	if _, err := mgr.MaybeCollect(context.Background(), 30*time.Second); err != nil {
		t.Fatalf("first: %v", err)
	}
	if adapter.Calls() != 1 {
		t.Fatalf("after first: calls=%d, want 1", adapter.Calls())
	}

	// Second call within throttle window — must be a no-op.
	ran, err := mgr.MaybeCollect(context.Background(), 30*time.Second)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if ran {
		t.Fatalf("ran=true, want false (throttled)")
	}
	if adapter.Calls() != 1 {
		t.Fatalf("after second: calls=%d, want 1 still", adapter.Calls())
	}
}

func TestMaybeCollectRunsAfterThrottleExpires(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	adapter := &countingAdapter{name: "claude"}
	registry := NewRegistry()
	if err := registry.Register(adapter); err != nil {
		t.Fatalf("register: %v", err)
	}
	store := NewStore(dir)

	now := mustTime(t, "2026-05-06T12:00:00Z")
	clock := &now
	mgr := NewManager(registry, store, func() time.Time { return *clock })

	if _, err := mgr.MaybeCollect(context.Background(), 30*time.Second); err != nil {
		t.Fatalf("first: %v", err)
	}
	if adapter.Calls() != 1 {
		t.Fatalf("after first: calls=%d, want 1", adapter.Calls())
	}

	*clock = now.Add(31 * time.Second)
	ran, err := mgr.MaybeCollect(context.Background(), 30*time.Second)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !ran {
		t.Fatalf("ran=false after throttle expiry, want true")
	}
	if adapter.Calls() != 2 {
		t.Fatalf("after second: calls=%d, want 2", adapter.Calls())
	}
}

func TestMaybeCollectSurfacesAdapterError(t *testing.T) {
	t.Parallel()

	now := mustTime(t, "2026-05-06T12:00:00Z")
	adapter := &countingAdapter{name: "claude", err: errors.New("network down")}
	mgr, _ := newTestManager(t, adapter, now)

	ran, err := mgr.MaybeCollect(context.Background(), 30*time.Second)
	if !ran {
		t.Fatalf("ran=false, want true")
	}
	if err == nil {
		t.Fatalf("err=nil, want adapter error surfaced for debug logging")
	}
	if adapter.Calls() != 1 {
		t.Fatalf("calls = %d, want 1", adapter.Calls())
	}

	// Subsequent call within throttle is still a no-op despite prior error.
	// The persisted last_collect is what gates retries — even with empty
	// snapshots, the file was written on the failure path.
	ran, _ = mgr.MaybeCollect(context.Background(), 30*time.Second)
	if ran {
		t.Fatalf("second ran=true, want throttled after a failure")
	}
}

func TestMaybeCollectZeroThrottleAlwaysRuns(t *testing.T) {
	t.Parallel()

	now := mustTime(t, "2026-05-06T12:00:00Z")
	adapter := &countingAdapter{name: "claude"}
	mgr, _ := newTestManager(t, adapter, now)

	for i := 0; i < 3; i++ {
		ran, err := mgr.MaybeCollect(context.Background(), 0)
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		if !ran {
			t.Fatalf("iter %d: ran=false, want true", i)
		}
	}
	if adapter.Calls() != 3 {
		t.Fatalf("calls = %d, want 3", adapter.Calls())
	}
}

func TestLoadAllReadsCacheWithoutAdapters(t *testing.T) {
	t.Parallel()

	now := mustTime(t, "2026-05-06T12:00:00Z")
	dir := t.TempDir()
	store := NewStore(dir)
	want := []Snapshot{
		{Model: "claude", Window: Window5h, Pct: 9, UpdatedAt: now},
	}
	if err := store.SaveAll(want, now); err != nil {
		t.Fatalf("SaveAll: %v", err)
	}
	mgr := NewManager(NewRegistry(), store, func() time.Time { return now })
	got, err := mgr.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(got) != 1 || got[0].Pct != 9 {
		t.Fatalf("got = %+v, want one at 9%%", got)
	}
}

// throttleHintAdapter declares a custom ThrottleHint per adapter. Used
// to assert the Manager honours per-adapter intervals so a slow OAuth
// adapter doesn't gate a fast local-file one.
type throttleHintAdapter struct {
	countingAdapter
	hint time.Duration
}

func (a *throttleHintAdapter) ThrottleHint() time.Duration { return a.hint }

// backoffAdapter wires the optional BackoffStater interface so the
// manager round-trips Until/Consecutive through snapshots.json across
// process restarts.
type backoffAdapter struct {
	countingAdapter
	loaded BackoffState
	saved  BackoffState
}

func (b *backoffAdapter) LoadBackoff(state BackoffState) {
	b.loaded = state
}

func (b *backoffAdapter) SaveBackoff() BackoffState {
	return b.saved
}

func TestCollectPreservesPriorSnapshotsOnAdapterFailure(t *testing.T) {
	t.Parallel()

	now := mustTime(t, "2026-05-06T12:00:00Z")
	dir := t.TempDir()
	registry := NewRegistry()
	claudeAd := &countingAdapter{
		name: "claude",
		snaps: []Snapshot{
			{Model: "claude", Window: Window5h, Pct: 18.0, ResetsAt: mustTime(t, "2026-05-06T17:00:00Z")},
		},
	}
	codexAd := &countingAdapter{
		name: "codex",
		snaps: []Snapshot{
			{Model: "codex", Window: Window5h, Pct: 42.0, ResetsAt: mustTime(t, "2026-05-06T17:00:00Z")},
		},
	}
	if err := registry.Register(claudeAd); err != nil {
		t.Fatalf("register claude: %v", err)
	}
	if err := registry.Register(codexAd); err != nil {
		t.Fatalf("register codex: %v", err)
	}
	store := NewStore(dir)
	mgr := NewManager(registry, store, func() time.Time { return now })

	// First collect: both adapters succeed. Cache holds claude+codex.
	if _, err := mgr.Collect(context.Background()); err != nil {
		t.Fatalf("first Collect: %v", err)
	}

	// Second collect: claude fails (network), codex still succeeds. The
	// claude rows must survive in the merged result and on disk.
	claudeAd.snaps = nil
	claudeAd.err = errors.New("claude: 429")
	codexAd.snaps = []Snapshot{
		{Model: "codex", Window: Window5h, Pct: 50.0, ResetsAt: mustTime(t, "2026-05-06T18:00:00Z")},
	}

	got, err := mgr.Collect(context.Background())
	if err == nil {
		t.Fatalf("Collect must surface adapter error for diagnostics")
	}

	gotByModel := map[string]Snapshot{}
	for _, s := range got {
		gotByModel[s.Model] = s
	}
	if cl, ok := gotByModel["claude"]; !ok {
		t.Fatalf("merged result missing claude rows: %+v", got)
	} else if cl.Pct != 18.0 {
		t.Fatalf("claude rows should preserve prior 18%%, got %v", cl.Pct)
	}
	if cx, ok := gotByModel["codex"]; !ok {
		t.Fatalf("merged result missing codex rows: %+v", got)
	} else if cx.Pct != 50.0 {
		t.Fatalf("codex rows should reflect fresh 50%%, got %v", cx.Pct)
	}

	// Disk must reflect the same merged set.
	loaded, _, err := store.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("on-disk len = %d, want 2 (preserved claude + fresh codex)", len(loaded))
	}
}

func TestCollectEmptyResultPreservesPrior(t *testing.T) {
	t.Parallel()

	now := mustTime(t, "2026-05-06T12:00:00Z")
	dir := t.TempDir()
	registry := NewRegistry()
	claudeAd := &countingAdapter{
		name: "claude",
		snaps: []Snapshot{
			{Model: "claude", Window: Window5h, Pct: 7.0},
		},
	}
	if err := registry.Register(claudeAd); err != nil {
		t.Fatalf("register: %v", err)
	}
	store := NewStore(dir)
	mgr := NewManager(registry, store, func() time.Time { return now })

	if _, err := mgr.Collect(context.Background()); err != nil {
		t.Fatalf("first: %v", err)
	}
	// Adapter returns zero snapshots without an error (e.g. backoff
	// branch). Prior rows must still survive.
	claudeAd.snaps = nil
	got, err := mgr.Collect(context.Background())
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if len(got) != 1 || got[0].Pct != 7.0 {
		t.Fatalf("merged = %+v, want preserved 7%% snapshot", got)
	}
}

func TestMaybeCollectPerAdapterThrottle(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	registry := NewRegistry()
	claudeAd := &throttleHintAdapter{
		countingAdapter: countingAdapter{name: "claude", snaps: []Snapshot{
			{Model: "claude", Window: Window5h, Pct: 5},
		}},
		hint: 60 * time.Second,
	}
	codexAd := &countingAdapter{name: "codex", snaps: []Snapshot{
		{Model: "codex", Window: Window5h, Pct: 10},
	}}
	if err := registry.Register(claudeAd); err != nil {
		t.Fatalf("register claude: %v", err)
	}
	if err := registry.Register(codexAd); err != nil {
		t.Fatalf("register codex: %v", err)
	}
	store := NewStore(dir)

	now := mustTime(t, "2026-05-06T12:00:00Z")
	clock := &now
	mgr := NewManager(registry, store, func() time.Time { return *clock })

	// First call: both adapters run.
	if _, err := mgr.MaybeCollect(context.Background(), 30*time.Second); err != nil {
		t.Fatalf("first: %v", err)
	}
	if claudeAd.Calls() != 1 || codexAd.Calls() != 1 {
		t.Fatalf("first call counts: claude=%d codex=%d, want 1/1", claudeAd.Calls(), codexAd.Calls())
	}

	// 45s later: codex (30s throttle) is due, claude (60s hint) is not.
	*clock = now.Add(45 * time.Second)
	ran, err := mgr.MaybeCollect(context.Background(), 30*time.Second)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !ran {
		t.Fatalf("ran=false, want true (codex due)")
	}
	if codexAd.Calls() != 2 {
		t.Fatalf("codex calls = %d, want 2", codexAd.Calls())
	}
	if claudeAd.Calls() != 1 {
		t.Fatalf("claude calls = %d, want 1 (still within 60s hint)", claudeAd.Calls())
	}

	// 70s past first call: claude is now due.
	*clock = now.Add(70 * time.Second)
	if _, err := mgr.MaybeCollect(context.Background(), 30*time.Second); err != nil {
		t.Fatalf("third: %v", err)
	}
	if claudeAd.Calls() != 2 {
		t.Fatalf("claude calls = %d, want 2 after 70s", claudeAd.Calls())
	}
}

func TestMaybeCollectBackoffBlocksAdapter(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	registry := NewRegistry()
	now := mustTime(t, "2026-05-06T12:00:00Z")
	// Backoff persisted on disk: claude is in cooldown until now+5m.
	store := NewStore(dir)
	if err := store.SaveState(State{
		Snapshots: []Snapshot{
			{Model: "claude", Window: Window5h, Pct: 33, UpdatedAt: now.Add(-2 * time.Minute)},
		},
		LastCollect: map[string]time.Time{
			"claude": now.Add(-2 * time.Minute),
		},
		Backoff: map[string]BackoffState{
			"claude": {Until: now.Add(5 * time.Minute), Consecutive: 1},
		},
	}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	claudeAd := &backoffAdapter{
		countingAdapter: countingAdapter{name: "claude"},
	}
	if err := registry.Register(claudeAd); err != nil {
		t.Fatalf("register: %v", err)
	}
	mgr := NewManager(registry, store, func() time.Time { return now })

	ran, err := mgr.MaybeCollect(context.Background(), 30*time.Second)
	if err != nil {
		t.Fatalf("MaybeCollect: %v", err)
	}
	if ran {
		t.Fatalf("ran=true, want false (claude in backoff, no other adapters due)")
	}
	if claudeAd.Calls() != 0 {
		t.Fatalf("claude calls = %d, want 0 (backoff blocks Collect)", claudeAd.Calls())
	}
}

func TestCollectPersistsBackoffState(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	registry := NewRegistry()
	now := mustTime(t, "2026-05-06T12:00:00Z")
	claudeAd := &backoffAdapter{
		countingAdapter: countingAdapter{
			name: "claude",
			snaps: []Snapshot{
				{Model: "claude", Window: Window5h, Pct: 5},
			},
		},
		saved: BackoffState{Until: now.Add(10 * time.Minute), Consecutive: 2},
	}
	if err := registry.Register(claudeAd); err != nil {
		t.Fatalf("register: %v", err)
	}
	store := NewStore(dir)
	mgr := NewManager(registry, store, func() time.Time { return now })

	if _, err := mgr.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	state, err := store.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	bs, ok := state.Backoff["claude"]
	if !ok {
		t.Fatalf("backoff state missing for claude: %+v", state.Backoff)
	}
	if bs.Consecutive != 2 {
		t.Fatalf("backoff.Consecutive = %d, want 2", bs.Consecutive)
	}
	if !bs.Until.Equal(now.Add(10 * time.Minute).UTC()) {
		t.Fatalf("backoff.Until = %v, want %v", bs.Until, now.Add(10*time.Minute))
	}
}

// TestCollectPreservesPriorClaudeRowsOn429 is the user-visible
// contract: when the Claude adapter fails with a 429, prior claude
// rows on disk MUST survive AND be returned to the caller, alongside
// fresh codex rows. The previous slice landed the merge logic — this
// test formalises it so a future refactor can't silently break the
// "429 doesn't erase existing values" guarantee.
func TestCollectPreservesPriorClaudeRowsOn429(t *testing.T) {
	t.Parallel()

	now := mustTime(t, "2026-05-06T12:00:00Z")
	dir := t.TempDir()
	store := NewStore(dir)

	// Seed snapshots.json with claude {5h: 18%, weekly: 13%}.
	priorClaude5h := Snapshot{Model: "claude", Window: Window5h, Pct: 18.0, ResetsAt: mustTime(t, "2026-05-06T17:00:00Z"), UpdatedAt: now.Add(-time.Minute)}
	priorClaudeWeekly := Snapshot{Model: "claude", Window: WindowWeekly, Pct: 13.0, ResetsAt: mustTime(t, "2026-05-13T00:00:00Z"), UpdatedAt: now.Add(-time.Minute)}
	if err := store.SaveState(State{
		Snapshots: []Snapshot{priorClaude5h, priorClaudeWeekly},
		LastCollect: map[string]time.Time{
			"claude": now.Add(-time.Minute),
			"codex":  now.Add(-time.Minute),
		},
	}); err != nil {
		t.Fatalf("seed SaveState: %v", err)
	}

	registry := NewRegistry()
	// Claude adapter returns (nil, 429) — the same shape the real
	// adapter produces during a backoff-recording 429.
	claudeAd := &countingAdapter{
		name: "claude",
		err:  errors.New("claude: usage endpoint returned status 429 (backing off)"),
	}
	codexAd := &countingAdapter{
		name: "codex",
		snaps: []Snapshot{
			{Model: "codex", Window: Window5h, Pct: 7.0, ResetsAt: mustTime(t, "2026-05-06T17:00:00Z")},
		},
	}
	if err := registry.Register(claudeAd); err != nil {
		t.Fatalf("register claude: %v", err)
	}
	if err := registry.Register(codexAd); err != nil {
		t.Fatalf("register codex: %v", err)
	}
	mgr := NewManager(registry, store, func() time.Time { return now })

	got, err := mgr.Collect(context.Background())
	if err == nil {
		t.Fatalf("Collect must surface 429 error for diagnostics")
	}

	// Returned snapshots: prior claude rows + fresh codex row.
	gotByKey := map[string]Snapshot{}
	for _, s := range got {
		gotByKey[s.Model+"/"+string(s.Window)] = s
	}
	if cl5h, ok := gotByKey["claude/5h"]; !ok {
		t.Fatalf("returned snapshots missing claude 5h: %+v", got)
	} else if cl5h.Pct != 18.0 {
		t.Fatalf("claude 5h preserved value = %v, want 18", cl5h.Pct)
	}
	if clWk, ok := gotByKey["claude/weekly"]; !ok {
		t.Fatalf("returned snapshots missing claude weekly: %+v", got)
	} else if clWk.Pct != 13.0 {
		t.Fatalf("claude weekly preserved value = %v, want 13", clWk.Pct)
	}
	if cx, ok := gotByKey["codex/5h"]; !ok {
		t.Fatalf("returned snapshots missing codex 5h: %+v", got)
	} else if cx.Pct != 7.0 {
		t.Fatalf("codex 5h fresh value = %v, want 7", cx.Pct)
	}

	// Persisted file must contain the same combined set.
	loaded, _, err := store.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	loadedByKey := map[string]Snapshot{}
	for _, s := range loaded {
		loadedByKey[s.Model+"/"+string(s.Window)] = s
	}
	if len(loadedByKey) != 3 {
		t.Fatalf("on-disk len = %d, want 3 (preserved claude 5h+weekly + fresh codex 5h): %+v", len(loadedByKey), loaded)
	}
	if loadedByKey["claude/5h"].Pct != 18.0 {
		t.Fatalf("on-disk claude 5h = %v, want 18", loadedByKey["claude/5h"].Pct)
	}
	if loadedByKey["claude/weekly"].Pct != 13.0 {
		t.Fatalf("on-disk claude weekly = %v, want 13", loadedByKey["claude/weekly"].Pct)
	}
	if loadedByKey["codex/5h"].Pct != 7.0 {
		t.Fatalf("on-disk codex 5h = %v, want 7", loadedByKey["codex/5h"].Pct)
	}
}

// TestCollectBackoffShortCircuitPreservesPriorClaudeRows covers the
// other half of the contract: when the Claude adapter is in active
// backoff the in-process Collect returns (nil, nil) — no error, no
// snapshots — and the merge layer must still preserve the prior
// claude rows so the user keeps seeing last-known values.
func TestCollectBackoffShortCircuitPreservesPriorClaudeRows(t *testing.T) {
	t.Parallel()

	now := mustTime(t, "2026-05-06T12:00:00Z")
	dir := t.TempDir()
	store := NewStore(dir)

	priorClaude5h := Snapshot{Model: "claude", Window: Window5h, Pct: 18.0, ResetsAt: mustTime(t, "2026-05-06T17:00:00Z"), UpdatedAt: now.Add(-time.Minute)}
	priorClaudeWeekly := Snapshot{Model: "claude", Window: WindowWeekly, Pct: 13.0, ResetsAt: mustTime(t, "2026-05-13T00:00:00Z"), UpdatedAt: now.Add(-time.Minute)}
	if err := store.SaveState(State{
		Snapshots: []Snapshot{priorClaude5h, priorClaudeWeekly},
		LastCollect: map[string]time.Time{
			"claude": now.Add(-time.Minute),
		},
		Backoff: map[string]BackoffState{
			"claude": {Until: now.Add(30 * time.Minute), Consecutive: 1},
		},
	}); err != nil {
		t.Fatalf("seed SaveState: %v", err)
	}

	registry := NewRegistry()
	// Adapter returns (nil, nil) to model the in-backoff short-circuit
	// path the real claude adapter takes.
	claudeAd := &countingAdapter{name: "claude"}
	if err := registry.Register(claudeAd); err != nil {
		t.Fatalf("register: %v", err)
	}
	mgr := NewManager(registry, store, func() time.Time { return now })

	got, err := mgr.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect must be silent during backoff short-circuit: %v", err)
	}
	gotByKey := map[string]Snapshot{}
	for _, s := range got {
		gotByKey[s.Model+"/"+string(s.Window)] = s
	}
	if cl5h, ok := gotByKey["claude/5h"]; !ok {
		t.Fatalf("returned missing claude 5h: %+v", got)
	} else if cl5h.Pct != 18.0 {
		t.Fatalf("claude 5h = %v, want 18 (preserved during backoff)", cl5h.Pct)
	}
	if clWk, ok := gotByKey["claude/weekly"]; !ok {
		t.Fatalf("returned missing claude weekly: %+v", got)
	} else if clWk.Pct != 13.0 {
		t.Fatalf("claude weekly = %v, want 13 (preserved during backoff)", clWk.Pct)
	}

	loaded, _, err := store.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("on-disk len = %d, want 2: %+v", len(loaded), loaded)
	}
}

// TestForceCollectBypassesBackoffAndThrottle asserts the `--force`
// contract: an adapter that's in active backoff is invoked anyway, the
// backoff state is cleared on the way in, and a 429-on-force reinstates
// backoff with consecutive=1 (i.e. the streak does NOT preserve).
func TestForceCollectBypassesBackoffAndThrottle(t *testing.T) {
	t.Parallel()

	now := mustTime(t, "2026-05-06T12:00:00Z")
	dir := t.TempDir()
	store := NewStore(dir)

	if err := store.SaveState(State{
		LastCollect: map[string]time.Time{"claude": now.Add(-time.Second)},
		Backoff: map[string]BackoffState{
			"claude": {Until: now.Add(30 * time.Minute), Consecutive: 5},
		},
	}); err != nil {
		t.Fatalf("seed SaveState: %v", err)
	}

	registry := NewRegistry()
	claudeAd := &backoffAdapter{
		countingAdapter: countingAdapter{
			name: "claude",
			snaps: []Snapshot{
				{Model: "claude", Window: Window5h, Pct: 9.0, ResetsAt: mustTime(t, "2026-05-06T17:00:00Z")},
			},
		},
	}
	if err := registry.Register(claudeAd); err != nil {
		t.Fatalf("register: %v", err)
	}
	mgr := NewManager(registry, store, func() time.Time { return now })

	if _, err := mgr.ForceCollect(context.Background()); err != nil {
		t.Fatalf("ForceCollect: %v", err)
	}
	if claudeAd.Calls() != 1 {
		t.Fatalf("adapter calls = %d, want 1 (force must invoke despite active backoff)", claudeAd.Calls())
	}
	// LoadBackoff was called with a zero-valued state (force cleared
	// the persisted view before handing it to the adapter).
	if !claudeAd.loaded.Until.IsZero() {
		t.Fatalf("LoadBackoff received Until=%v, want zero (force clears)", claudeAd.loaded.Until)
	}
	if claudeAd.loaded.Consecutive != 0 {
		t.Fatalf("LoadBackoff received Consecutive=%d, want 0 (force clears)", claudeAd.loaded.Consecutive)
	}
}

func TestStoreMigratesLegacyLastCollectString(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Pre-write the v2 file with the legacy string-shaped last_collect.
	legacy := `{
		"version": 2,
		"last_collect": "2026-05-06T09:29:52Z",
		"snapshots": [
			{"model":"claude","window":"5h","pct":1.5,"resets_at":"2026-05-06T17:00:00Z","updated_at":"2026-05-06T09:29:52Z"}
		]
	}`
	path := dir + "/snapshots.json"
	if err := writeFile(path, []byte(legacy)); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	store := NewStore(dir)
	state, err := store.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	// Snapshots round-trip even though last_collect is legacy.
	if len(state.Snapshots) != 1 || state.Snapshots[0].Pct != 1.5 {
		t.Fatalf("snapshots = %+v, want one at 1.5%%", state.Snapshots)
	}
	// Legacy string form: per-adapter map is empty. Next collect runs
	// every adapter (the documented migration behaviour).
	if len(state.LastCollect) != 0 {
		t.Fatalf("LastCollect = %+v, want empty after legacy migration", state.LastCollect)
	}
}

func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}
