package usage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// gate is shared by every gatedAdapter instance standing in for one
// provider across several Managers (one per simulated process). The first
// Collect to arrive blocks until release is closed.
type gate struct {
	calls   atomic.Int64
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newGate() *gate {
	return &gate{entered: make(chan struct{}, 64), release: make(chan struct{})}
}

func (g *gate) open() { g.once.Do(func() { close(g.release) }) }

type gatedAdapter struct {
	name  string
	gate  *gate
	snaps []Snapshot
}

func (a *gatedAdapter) Name() string { return a.name }

func (a *gatedAdapter) Collect(ctx context.Context) ([]Snapshot, error) {
	a.gate.calls.Add(1)
	a.gate.entered <- struct{}{}
	select {
	case <-a.gate.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return a.snaps, nil
}

// newProcessManager builds a Manager with its own Registry and Store over
// dir, the way each projmux process does.
func newProcessManager(t *testing.T, dir string, now time.Time, adapters ...Adapter) *Manager {
	t.Helper()
	registry := NewRegistry()
	for _, adapter := range adapters {
		if err := registry.Register(adapter); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	return NewManager(registry, NewStore(dir), func() time.Time { return now })
}

func waitEntered(t *testing.T, g *gate) {
	t.Helper()
	select {
	case <-g.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("no adapter Collect started")
	}
}

type maybeResult struct {
	walked bool
	err    error
}

func TestMaybeCollectConcurrentManagersCollectEachAdapterOncePerWindow(t *testing.T) {
	t.Parallel()

	const managers = 8
	now := mustTime(t, "2026-09-29T08:00:00Z")
	dir := t.TempDir()
	claude := newGate()
	t.Cleanup(claude.open)
	var codexCalls atomic.Int64

	results := make(chan maybeResult, managers)
	start := make(chan struct{})
	for range managers {
		mgr := newProcessManager(t, dir, now,
			&gatedAdapter{name: "claude", gate: claude, snaps: []Snapshot{{Model: "claude", Window: Window5h, Pct: 42}}},
			&countingCallback{name: "codex", calls: &codexCalls, snaps: []Snapshot{{Model: "codex", Window: Window5h, Pct: 7}}},
		)
		go func() {
			<-start
			walked, err := mgr.MaybeCollectExcept(context.Background(), 30*time.Second)
			results <- maybeResult{walked: walked, err: err}
		}()
	}
	close(start)

	// One Manager claims and blocks inside the Claude call. Every other
	// Manager must see that claim and return without walking; with no claim
	// they would enter Collect too and block here.
	waitEntered(t, claude)
	for i := range managers - 1 {
		select {
		case got := <-results:
			if got.walked || got.err != nil {
				t.Fatalf("concurrent MaybeCollectExcept = (%v, %v), want (false, nil) while another process holds the claim", got.walked, got.err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("only %d of %d concurrent calls returned while the claimed Collect was in flight; claude Collect calls=%d", i, managers-1, claude.calls.Load())
		}
	}
	claude.open()
	select {
	case got := <-results:
		if !got.walked || got.err != nil {
			t.Fatalf("claiming MaybeCollectExcept = (%v, %v), want (true, nil)", got.walked, got.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("claiming MaybeCollectExcept did not return after release")
	}

	if got := claude.calls.Load(); got != 1 {
		t.Fatalf("claude Collect calls = %d, want 1", got)
	}
	if got := codexCalls.Load(); got != 1 {
		t.Fatalf("codex Collect calls = %d, want 1", got)
	}
	state, err := NewStore(dir).LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if len(state.Snapshots) != 2 {
		t.Fatalf("snapshots = %+v, want the claude and codex rows", state.Snapshots)
	}
	for _, name := range []string{"claude", "codex"} {
		if !state.LastCollect[name].Equal(now) {
			t.Fatalf("last_collect[%s] = %v, want %v", name, state.LastCollect[name], now)
		}
	}
}

// countingCallback is a non-blocking adapter whose call counter is shared
// across Manager instances.
type countingCallback struct {
	name  string
	calls *atomic.Int64
	snaps []Snapshot
}

func (a *countingCallback) Name() string { return a.name }

func (a *countingCallback) Collect(context.Context) ([]Snapshot, error) {
	a.calls.Add(1)
	return a.snaps, nil
}

func TestApplySnapshotsDuringCollectSurvivesTheCollectCommit(t *testing.T) {
	t.Parallel()

	now := mustTime(t, "2026-09-29T08:00:00Z")
	dir := t.TempDir()
	if err := NewStore(dir).SaveState(State{
		Snapshots: []Snapshot{
			{Model: "claude", Window: Window5h, Pct: 1},
			{Model: "codex", Window: Window5h, Pct: 10},
		},
		LastCollect: map[string]time.Time{"codex": now.Add(-time.Minute)},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	claude := newGate()
	t.Cleanup(claude.open)
	collector := newProcessManager(t, dir, now,
		&gatedAdapter{name: "claude", gate: claude, snaps: []Snapshot{{Model: "claude", Window: Window5h, Pct: 42}}})
	done := make(chan maybeResult, 1)
	go func() {
		walked, err := collector.MaybeCollectExcept(context.Background(), 30*time.Second)
		done <- maybeResult{walked: walked, err: err}
	}()
	waitEntered(t, claude)

	// Another process commits a native Codex batch while the Claude call is
	// in flight. It must not wait for that call.
	applier := newProcessManager(t, dir, now.Add(time.Second))
	applied := make(chan error, 1)
	go func() {
		_, err := applier.ApplySnapshots("codex", []Snapshot{{Model: "codex", Window: Window5h, Pct: 55}})
		applied <- err
	}()
	select {
	case err := <-applied:
		if err != nil {
			t.Fatalf("ApplySnapshots: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ApplySnapshots waited on another process's adapter call")
	}

	claude.open()
	if got := <-done; !got.walked || got.err != nil {
		t.Fatalf("MaybeCollectExcept = (%v, %v), want (true, nil)", got.walked, got.err)
	}

	state, err := NewStore(dir).LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	pct := map[string]float64{}
	for _, snap := range state.Snapshots {
		pct[snap.Model] = snap.Pct
	}
	if len(state.Snapshots) != 2 || pct["claude"] != 42 || pct["codex"] != 55 {
		t.Fatalf("snapshots = %+v, want the collected claude row (42) and the applied codex row (55)", state.Snapshots)
	}
	if want := now.Add(time.Second); !state.LastCollect["codex"].Equal(want) {
		t.Fatalf("last_collect[codex] = %v, want the ApplySnapshots commit %v", state.LastCollect["codex"], want)
	}
	if !state.LastCollect["claude"].Equal(now) {
		t.Fatalf("last_collect[claude] = %v, want %v", state.LastCollect["claude"], now)
	}
}

func TestMaybeCollectReturnsLockTimeoutWithoutWalkingWhenTheStateLockIsHeld(t *testing.T) {
	t.Parallel()

	now := mustTime(t, "2026-09-29T08:00:00Z")
	adapter := &countingAdapter{name: "claude", snaps: []Snapshot{{Model: "claude", Window: Window5h, Pct: 42}}}
	mgr, dir := newTestManager(t, adapter, now)
	mgr.store.lockWaitLimit = 100 * time.Millisecond

	// Another process holds the state lock.
	holder, err := os.OpenFile(mgr.store.LockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open lock: %v", err)
	}
	t.Cleanup(func() { _ = holder.Close() })
	if err := unix.Flock(int(holder.Fd()), unix.LOCK_EX); err != nil {
		t.Fatalf("flock: %v", err)
	}

	started := time.Now()
	walked, err := mgr.MaybeCollectExcept(context.Background(), 30*time.Second)
	elapsed := time.Since(started)
	if !errors.Is(err, ErrStateLockTimeout) {
		t.Fatalf("MaybeCollectExcept err = %v, want ErrStateLockTimeout", err)
	}
	if walked {
		t.Fatal("MaybeCollectExcept reported a walk while the state lock was held")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("MaybeCollectExcept took %s, want it bounded by the lock wait limit", elapsed)
	}
	if got := adapter.Calls(); got != 0 {
		t.Fatalf("adapter Collect calls = %d, want 0", got)
	}
	if _, err := mgr.ApplySnapshots("claude", []Snapshot{{Model: "claude", Window: Window5h, Pct: 9}}); !errors.Is(err, ErrStateLockTimeout) {
		t.Fatalf("ApplySnapshots err = %v, want ErrStateLockTimeout", err)
	}
	if _, err := os.Stat(mgr.store.FilePath()); !os.IsNotExist(err) {
		t.Fatalf("snapshots.json exists after lock timeouts (stat err %v), want nothing written", err)
	}

	// Once the holder lets go, the timed-out waiters must not leave the lock
	// taken behind them.
	if err := unix.Flock(int(holder.Fd()), unix.LOCK_UN); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	mgr.store.lockWaitLimit = 5 * time.Second
	walked, err = mgr.MaybeCollectExcept(context.Background(), 30*time.Second)
	if !walked || err != nil {
		t.Fatalf("MaybeCollectExcept after release = (%v, %v), want (true, nil)", walked, err)
	}
	if got := adapter.Calls(); got != 1 {
		t.Fatalf("adapter Collect calls after release = %d, want 1", got)
	}
	if _, err := os.Stat(filepath.Join(dir, stateLockFileName)); err != nil {
		t.Fatalf("lock file: %v", err)
	}
}

func TestCollectAndForceCollectStillWalkEveryCallAfterAClaim(t *testing.T) {
	t.Parallel()

	now := mustTime(t, "2026-09-29T08:00:00Z")
	adapter := &countingAdapter{name: "claude", snaps: []Snapshot{{Model: "claude", Window: Window5h, Pct: 42}}}
	mgr, _ := newTestManager(t, adapter, now)

	if walked, err := mgr.MaybeCollectExcept(context.Background(), 30*time.Second); !walked || err != nil {
		t.Fatalf("MaybeCollectExcept = (%v, %v), want (true, nil)", walked, err)
	}
	if _, err := mgr.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if _, err := mgr.ForceCollect(context.Background()); err != nil {
		t.Fatalf("ForceCollect: %v", err)
	}
	if got := adapter.Calls(); got != 3 {
		t.Fatalf("adapter Collect calls = %d, want 3 (explicit collects ignore the claim)", got)
	}
}
