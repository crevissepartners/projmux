package usage

import (
	"context"
	"errors"
	"fmt"
	"time"
)

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
