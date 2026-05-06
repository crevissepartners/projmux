package usage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// collectMarkerName is the file the Manager touches after a successful
// MaybeCollect. Its mtime is the throttle anchor — when the file is missing
// or older than the supplied throttle, the next MaybeCollect runs adapters.
const collectMarkerName = "last_collect"

// Manager wires a Registry, a Store, and a Limits table into the standard
// "collect, persist, aggregate" flow consumed by the CLI.
type Manager struct {
	registry *Registry
	store    *Store
	limits   Limits
	now      func() time.Time
}

// NewManager constructs a Manager. now defaults to time.Now when nil.
func NewManager(registry *Registry, store *Store, limits Limits, now func() time.Time) *Manager {
	if now == nil {
		now = time.Now
	}
	return &Manager{registry: registry, store: store, limits: limits, now: now}
}

// Collect runs every registered adapter, appends the resulting events into
// the per-model cache, aggregates the canonical windows, and returns a flat
// list of Snapshots. Adapter errors are surfaced as a joined error but
// non-fatal — successful adapters' snapshots are still returned.
func (m *Manager) Collect(ctx context.Context) ([]Snapshot, error) {
	if m == nil {
		return nil, errors.New("usage: nil manager")
	}
	if m.registry == nil {
		return nil, errors.New("usage: nil registry")
	}
	if m.store == nil {
		return nil, errors.New("usage: nil store")
	}

	now := m.now().UTC()
	var allSnaps []Snapshot
	var errs []error
	for _, adapter := range m.registry.All() {
		events, err := adapter.Collect(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", adapter.Name(), err))
		}
		buckets, storeErr := m.store.Append(adapter.Name(), events, now)
		if storeErr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", adapter.Name(), storeErr))
			continue
		}
		modelLimits := m.limits.For(adapter.Name())
		allSnaps = append(allSnaps, Aggregate(adapter.Name(), buckets, modelLimits, now)...)
	}
	if len(errs) > 0 {
		return allSnaps, errors.Join(errs...)
	}
	return allSnaps, nil
}

// MaybeCollect opportunistically refreshes the cache if the supplied
// throttle interval has elapsed since the last successful collection. It is
// the cheap, status-bar-safe variant of Collect:
//
//   - If the marker file is missing the cache is treated as stale and
//     adapters run immediately. This covers fresh installs where the user
//     never invoked `projmux usage` interactively.
//   - When the marker exists and its mtime is younger than throttle, the
//     call is a no-op (no adapter walk, no disk write beyond the mtime
//     check). This keeps tmux's 5-second status redraw cheap.
//   - Adapter errors are swallowed and the call returns nil. The status
//     segment must NEVER bubble up adapter failures. Set
//     PROJMUX_USAGE_DEBUG to surface a stderr line for diagnostics; the
//     env-var observation lives at the call site (CLI plumbing) so the
//     core package stays free of os.Getenv.
//
// Returns true when a collect cycle ran (regardless of success), false when
// throttled. Tests inject a custom now via NewManager.
func (m *Manager) MaybeCollect(ctx context.Context, throttle time.Duration) (bool, error) {
	if m == nil {
		return false, errors.New("usage: nil manager")
	}
	if m.store == nil {
		return false, errors.New("usage: nil store")
	}
	now := m.now().UTC()
	markerPath := m.markerPath()
	if !markerStale(markerPath, now, throttle) {
		return false, nil
	}
	// Ignore the adapter error — the spec calls for swallow-and-continue
	// during status redraw. Callers that want richer diagnostics should
	// drive the unconditional Collect path instead.
	_, collectErr := m.Collect(ctx)
	// Always touch the marker on a collect attempt so a permanently failing
	// adapter does not flood the status hot path with retries every redraw.
	if err := touchMarker(markerPath, now); err != nil && collectErr == nil {
		collectErr = err
	}
	return true, collectErr
}

// markerPath returns the absolute path to the throttle marker file inside
// the store's base dir.
func (m *Manager) markerPath() string {
	return filepath.Join(m.store.BaseDir(), collectMarkerName)
}

// markerStale reports whether a collect cycle should run. A missing marker
// is always stale; any I/O error other than ErrNotExist is treated as
// "needs collect" so a corrupt state dir self-heals on the next redraw.
func markerStale(path string, now time.Time, throttle time.Duration) bool {
	if throttle <= 0 {
		return true
	}
	info, err := os.Stat(path)
	if err != nil {
		return true
	}
	return now.Sub(info.ModTime().UTC()) >= throttle
}

// touchMarker writes the marker file with the supplied mtime. Best-effort:
// any error is returned but the caller usually ignores it (status segment
// stays silent on failure).
func touchMarker(path string, now time.Time) error {
	if path == "" {
		return errors.New("usage: empty marker path")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("usage: create marker dir %s: %w", dir, err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("usage: open marker %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("usage: close marker %s: %w", path, err)
	}
	stamp := now.UTC()
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		return fmt.Errorf("usage: chtimes marker %s: %w", path, err)
	}
	return nil
}

// LoadAll re-aggregates the persisted cache without invoking adapters. Useful
// for the status-bar segment which wants to be cheap and read-only on the
// hot path.
func (m *Manager) LoadAll() ([]Snapshot, error) {
	if m == nil {
		return nil, errors.New("usage: nil manager")
	}
	if m.registry == nil {
		return nil, errors.New("usage: nil registry")
	}
	if m.store == nil {
		return nil, errors.New("usage: nil store")
	}
	now := m.now().UTC()
	var allSnaps []Snapshot
	for _, adapter := range m.registry.All() {
		buckets, err := m.store.Load(adapter.Name())
		if err != nil {
			return nil, err
		}
		modelLimits := m.limits.For(adapter.Name())
		allSnaps = append(allSnaps, Aggregate(adapter.Name(), buckets, modelLimits, now)...)
	}
	return allSnaps, nil
}
