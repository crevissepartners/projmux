package usage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// countingAdapter records how many times Collect has been invoked. Used by
// the MaybeCollect tests to assert the throttle gate actually blocks
// repeated adapter walks.
type countingAdapter struct {
	name  string
	calls int64
	err   error
}

func (c *countingAdapter) Name() string { return c.name }

func (c *countingAdapter) Collect(ctx context.Context) ([]TokenEvent, error) {
	atomic.AddInt64(&c.calls, 1)
	return nil, c.err
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
	mgr := NewManager(registry, store, DefaultLimits, func() time.Time { return clock })
	return mgr, dir
}

func TestMaybeCollectRunsWhenMarkerMissing(t *testing.T) {
	t.Parallel()

	now := mustTime(t, "2026-05-06T12:00:00Z")
	adapter := &countingAdapter{name: "claude"}
	mgr, dir := newTestManager(t, adapter, now)

	ran, err := mgr.MaybeCollect(context.Background(), 30*time.Second)
	if err != nil {
		t.Fatalf("MaybeCollect: %v", err)
	}
	if !ran {
		t.Fatalf("ran=false, want true (marker missing)")
	}
	if adapter.Calls() != 1 {
		t.Fatalf("adapter calls = %d, want 1", adapter.Calls())
	}
	if _, err := os.Stat(filepath.Join(dir, collectMarkerName)); err != nil {
		t.Fatalf("marker not written: %v", err)
	}
}

func TestMaybeCollectThrottlesWithinWindow(t *testing.T) {
	t.Parallel()

	now := mustTime(t, "2026-05-06T12:00:00Z")
	adapter := &countingAdapter{name: "claude"}
	mgr, dir := newTestManager(t, adapter, now)

	ran, err := mgr.MaybeCollect(context.Background(), 30*time.Second)
	if err != nil || !ran {
		t.Fatalf("first MaybeCollect: ran=%v err=%v", ran, err)
	}
	if adapter.Calls() != 1 {
		t.Fatalf("after first: calls=%d, want 1", adapter.Calls())
	}

	// Second call inside the throttle window — must be a no-op.
	ran, err = mgr.MaybeCollect(context.Background(), 30*time.Second)
	if err != nil {
		t.Fatalf("second MaybeCollect: %v", err)
	}
	if ran {
		t.Fatalf("ran=true, want false (throttled)")
	}
	if adapter.Calls() != 1 {
		t.Fatalf("after second: calls=%d, want 1 (still)", adapter.Calls())
	}

	// Sanity: marker still present.
	if _, err := os.Stat(filepath.Join(dir, collectMarkerName)); err != nil {
		t.Fatalf("marker missing: %v", err)
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
	mgr := NewManager(registry, store, DefaultLimits, func() time.Time { return *clock })

	if _, err := mgr.MaybeCollect(context.Background(), 30*time.Second); err != nil {
		t.Fatalf("first: %v", err)
	}
	if adapter.Calls() != 1 {
		t.Fatalf("after first: calls=%d, want 1", adapter.Calls())
	}

	// Advance clock past the throttle window — adapters should run again.
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

func TestMaybeCollectSwallowsAdapterError(t *testing.T) {
	t.Parallel()

	now := mustTime(t, "2026-05-06T12:00:00Z")
	adapter := &countingAdapter{name: "claude", err: errors.New("network down")}
	mgr, dir := newTestManager(t, adapter, now)

	ran, err := mgr.MaybeCollect(context.Background(), 30*time.Second)
	if !ran {
		t.Fatalf("ran=false, want true even on adapter error")
	}
	// MaybeCollect surfaces the joined error so callers (the CLI) can route
	// it to PROJMUX_USAGE_DEBUG. The status segment must still write the
	// marker so a permanently-failing adapter doesn't loop on every redraw.
	if err == nil {
		t.Fatalf("err = nil, want adapter error surfaced for debug logging")
	}
	if _, statErr := os.Stat(filepath.Join(dir, collectMarkerName)); statErr != nil {
		t.Fatalf("marker missing after failed adapter: %v", statErr)
	}
	if adapter.Calls() != 1 {
		t.Fatalf("calls = %d, want 1", adapter.Calls())
	}

	// Subsequent call inside the throttle window is still a no-op despite
	// the prior failure — the marker mtime gates retries.
	ran, _ = mgr.MaybeCollect(context.Background(), 30*time.Second)
	if ran {
		t.Fatalf("ran=true on second call, want throttled")
	}
	if adapter.Calls() != 1 {
		t.Fatalf("retried failing adapter: calls=%d", adapter.Calls())
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
			t.Fatalf("iter %d: ran=false, want true with zero throttle", i)
		}
	}
	if adapter.Calls() != 3 {
		t.Fatalf("calls = %d, want 3 (zero throttle bypasses gate)", adapter.Calls())
	}
}
