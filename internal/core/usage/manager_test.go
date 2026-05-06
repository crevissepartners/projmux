package usage

import (
	"context"
	"errors"
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
