package usage

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Manager wires a Registry and a Store into the standard "collect, persist,
// load" flow consumed by the CLI.
type Manager struct {
	registry *Registry
	store    *Store
	now      func() time.Time
}

// NewManager constructs a Manager. now defaults to time.Now when nil.
func NewManager(registry *Registry, store *Store, now func() time.Time) *Manager {
	if now == nil {
		now = time.Now
	}
	return &Manager{registry: registry, store: store, now: now}
}

// Collect runs every registered adapter, replaces the persisted snapshot
// file with the union of their results, and returns the merged slice.
// Adapter errors are joined and returned alongside any successful
// snapshots — partial data still renders.
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
	// Best-effort cleanup of v1 artifacts. Cheap; safe to retry.
	m.store.CleanupLegacyArtifacts()

	var allSnaps []Snapshot
	var errs []error
	for _, adapter := range m.registry.All() {
		snaps, err := adapter.Collect(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", adapter.Name(), err))
		}
		// Stamp UpdatedAt so the renderer can show "as of" without callers
		// having to thread the clock through every adapter.
		for i := range snaps {
			if snaps[i].UpdatedAt.IsZero() {
				snaps[i].UpdatedAt = now
			}
		}
		allSnaps = append(allSnaps, snaps...)
	}

	if err := m.store.SaveAll(allSnaps, now); err != nil {
		errs = append(errs, fmt.Errorf("save snapshots: %w", err))
	}
	if len(errs) > 0 {
		return allSnaps, errors.Join(errs...)
	}
	return allSnaps, nil
}

// MaybeCollect opportunistically refreshes the snapshot file if the
// supplied throttle interval has elapsed since the last successful
// collection. It is the cheap, status-bar-safe variant of Collect:
//
//   - If the snapshot file is missing or its last_collect is older than
//     throttle, adapters run immediately.
//   - Otherwise the call is a no-op (no adapter walk, no disk write).
//   - Adapter errors are surfaced for diagnostics (PROJMUX_USAGE_DEBUG)
//     but the caller (CLI status segment) MUST swallow them. The marker
//     in the file is updated regardless of success so a permanently
//     failing adapter does not flood the status hot path.
//
// Returns true when a collect cycle ran (regardless of success), false
// when throttled.
func (m *Manager) MaybeCollect(ctx context.Context, throttle time.Duration) (bool, error) {
	if m == nil {
		return false, errors.New("usage: nil manager")
	}
	if m.store == nil {
		return false, errors.New("usage: nil store")
	}
	now := m.now().UTC()
	if !m.shouldCollect(now, throttle) {
		return false, nil
	}
	_, collectErr := m.Collect(ctx)
	return true, collectErr
}

// shouldCollect reports whether MaybeCollect should run adapters. A
// missing file or unreadable last_collect is treated as stale so a
// corrupt state dir self-heals on the next redraw.
func (m *Manager) shouldCollect(now time.Time, throttle time.Duration) bool {
	if throttle <= 0 {
		return true
	}
	_, last, err := m.store.LoadAll()
	if err != nil {
		return true
	}
	if last.IsZero() {
		return true
	}
	return now.Sub(last) >= throttle
}

// LoadAll reads the persisted snapshots without invoking adapters. Useful
// for the status-bar segment which wants to be cheap and read-only on the
// hot path.
func (m *Manager) LoadAll() ([]Snapshot, error) {
	if m == nil {
		return nil, errors.New("usage: nil manager")
	}
	if m.store == nil {
		return nil, errors.New("usage: nil store")
	}
	snaps, _, err := m.store.LoadAll()
	if err != nil {
		return nil, err
	}
	return snaps, nil
}
