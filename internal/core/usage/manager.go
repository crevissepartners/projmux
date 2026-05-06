package usage

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// DefaultThrottle is the per-adapter throttle used when an adapter does
// not implement ThrottleHinter. Aligns with the prior global throttle
// value so cheap, local-file adapters keep their existing cadence.
const DefaultThrottle = 30 * time.Second

// Manager wires a Registry and a Store into the standard "collect, persist,
// load" flow consumed by the CLI.
type Manager struct {
	registry *Registry
	store    *Store
	now      func() time.Time
	debug    func(format string, args ...any)
}

// NewManager constructs a Manager. now defaults to time.Now when nil.
func NewManager(registry *Registry, store *Store, now func() time.Time) *Manager {
	if now == nil {
		now = time.Now
	}
	return &Manager{registry: registry, store: store, now: now}
}

// SetDebug installs a callback used to surface backoff/throttle decisions
// for diagnostics. The callback is only invoked from cold paths and is
// safe to leave nil — the Manager never logs by default.
func (m *Manager) SetDebug(debug func(format string, args ...any)) {
	if m == nil {
		return
	}
	m.debug = debug
}

// Collect runs every registered adapter, merging fresh results with the
// prior on-disk snapshots so an adapter that fails (e.g. 429) does NOT
// erase its earlier rows. Per-adapter `last_collect` and `backoff` state
// are also round-tripped through the snapshot file.
//
// Merge semantics, evaluated per adapter:
//   - Adapter returns a non-empty snapshot slice → REPLACE all prior rows
//     for that model.
//   - Adapter errors or returns zero snapshots → PRESERVE prior rows for
//     that model. The user keeps seeing last-known values until the next
//     successful collect.
//
// Adapters that implement BackoffStater have their persisted state
// loaded before Collect and saved after (success OR failure) so a 429
// observed on one process survives a CLI restart.
func (m *Manager) Collect(ctx context.Context) ([]Snapshot, error) {
	// Throttle of 0 → unconditional: every adapter runs (subject to
	// adapter-internal backoff). Used by `projmux usage` where the user
	// explicitly asked for fresh data.
	return m.collect(ctx, 0, false)
}

// ForceCollect runs every registered adapter unconditionally, bypassing
// per-adapter throttle AND clearing any active backoff (in-memory and
// the on-disk Backoff map) so the network call attempts now regardless
// of prior 429 streak. A successful response resets the streak to 0; a
// 429 reinstates backoff with consecutive=1 (the streak does NOT
// preserve across `--force`).
//
// Useful as a manual override (`projmux usage --force` /
// `projmux status usage --force`) when the user wants the latest
// numbers right now and accepts that they may re-trigger 429.
func (m *Manager) ForceCollect(ctx context.Context) ([]Snapshot, error) {
	return m.collect(ctx, 0, true)
}

// collect is the shared implementation. perAdapterFloor is the minimum
// time-since-last-collect required for an adapter to be invoked; a value
// of 0 disables the floor and runs every adapter. Adapters that
// implement ThrottleHinter raise the floor on a per-adapter basis.
//
// force=true disables the throttle gate entirely (so `--force` ignores
// last_collect timestamps) AND clears any persisted backoff before the
// adapter sees it (so a Collect actually attempts the network call
// even if there's an active 429 cooldown). Adapters that implement
// BackoffResetter have ResetBackoff() invoked after LoadBackoff so the
// in-memory state matches the cleared on-disk view.
func (m *Manager) collect(ctx context.Context, perAdapterFloor time.Duration, force bool) ([]Snapshot, error) {
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

	priorState, _ := m.store.LoadState()
	if priorState.LastCollect == nil {
		priorState.LastCollect = map[string]time.Time{}
	}
	if priorState.Backoff == nil {
		priorState.Backoff = map[string]BackoffState{}
	}

	// Bucket prior snapshots by model so per-adapter merge is O(n).
	priorByModel := map[string][]Snapshot{}
	for _, s := range priorState.Snapshots {
		priorByModel[s.Model] = append(priorByModel[s.Model], s)
	}

	merged := make([]Snapshot, 0, len(priorState.Snapshots))
	freshModels := map[string]bool{}
	var errs []error

	adapters := m.registry.All()
	for _, adapter := range adapters {
		name := adapter.Name()

		// Per-adapter throttle gate. Skip adapters whose effective
		// interval has not elapsed; their prior rows survive via the
		// merge step below. Floor=0 disables the gate. force=true
		// also disables the gate so `--force` always attempts every
		// adapter.
		if !force && perAdapterFloor > 0 {
			interval := adapterInterval(adapter, perAdapterFloor)
			if last, ok := priorState.LastCollect[name]; ok && !last.IsZero() && now.Sub(last) < interval {
				continue
			}
		}

		// Install persisted backoff state before Collect so the adapter
		// can early-return without making the network call. Under
		// force=true we drop both the on-disk view AND the in-memory
		// view via ResetBackoff so this Collect attempts the network
		// call regardless of prior 429 streak.
		if bs, ok := adapter.(BackoffStater); ok {
			if force {
				bs.LoadBackoff(BackoffState{})
				priorState.Backoff[name] = BackoffState{}
			} else {
				bs.LoadBackoff(priorState.Backoff[name])
			}
		}
		if force {
			if br, ok := adapter.(BackoffResetter); ok {
				br.ResetBackoff()
			}
		}
		snaps, err := adapter.Collect(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
		// Stamp UpdatedAt so the renderer can show "as of" without callers
		// having to thread the clock through every adapter.
		for i := range snaps {
			if snaps[i].UpdatedAt.IsZero() {
				snaps[i].UpdatedAt = now
			}
		}
		// last_collect[name] advances on every adapter walk — failures
		// and empty results included. The throttle is about not
		// hammering the upstream; backoff (BackoffStater) handles the
		// longer 429-induced cooldown separately. Only the merged
		// snapshot rows distinguish success from failure.
		priorState.LastCollect[name] = now
		if len(snaps) > 0 {
			// Successful collect: replace all prior rows for the models
			// the adapter touched. We trust the adapter to emit one row
			// per (model, window) it owns.
			for _, s := range snaps {
				freshModels[s.Model] = true
			}
			merged = append(merged, snaps...)
		}
		// Persist backoff regardless of success/failure: a 429 sets
		// `until`; a clean success resets `consecutive` to 0.
		if bs, ok := adapter.(BackoffStater); ok {
			priorState.Backoff[name] = bs.SaveBackoff()
		}
	}

	// Preserve prior rows for any model the current cycle did NOT
	// successfully refresh. This is the load-bearing fix for the 429
	// regression: a Claude failure must not erase the Claude rows.
	for model, rows := range priorByModel {
		if freshModels[model] {
			continue
		}
		merged = append(merged, rows...)
	}

	priorState.Snapshots = merged
	if err := m.store.SaveState(priorState); err != nil {
		errs = append(errs, fmt.Errorf("save snapshots: %w", err))
	}
	if len(errs) > 0 {
		return merged, errors.Join(errs...)
	}
	return merged, nil
}

// MaybeCollect opportunistically refreshes the snapshot file if the
// supplied throttle interval has elapsed since the last successful
// collection of any registered adapter. It runs Collect (which itself
// gates each adapter on its own ThrottleHint) when at least one adapter
// is due; otherwise it is a no-op.
//
// `throttle` is the floor used for adapters that do not implement
// ThrottleHinter. Adapters with a hint use max(throttle, hint).
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
	_, collectErr := m.collect(ctx, throttle, false)
	return true, collectErr
}

// shouldCollect reports whether MaybeCollect should run adapters. It
// consults per-adapter timestamps so a slow adapter (Claude OAuth,
// 5min) does not block a fast one (Codex, 30s). The Manager runs the
// full adapter walk if ANY adapter is due, then Collect's own merge
// preserves the not-yet-due adapters' prior rows.
func (m *Manager) shouldCollect(now time.Time, defaultThrottle time.Duration) bool {
	if defaultThrottle <= 0 {
		return true
	}
	state, err := m.store.LoadState()
	if err != nil {
		return true
	}
	if m.registry == nil {
		return true
	}
	for _, adapter := range m.registry.All() {
		name := adapter.Name()
		interval := adapterInterval(adapter, defaultThrottle)
		// During backoff the adapter is effectively "not due" — we must
		// not run Collect just to no-op the network call. Defer to the
		// adapter's own Collect logic only when out of backoff.
		if bs, ok := state.Backoff[name]; ok && !bs.Until.IsZero() && now.Before(bs.Until) {
			if m.debug != nil {
				m.debug("usage: %s in backoff until %s", name, bs.Until.Format(time.RFC3339))
			}
			continue
		}
		last := state.LastCollect[name]
		if last.IsZero() || now.Sub(last) >= interval {
			return true
		}
	}
	return false
}

// adapterInterval resolves the effective throttle for an adapter:
// max(default, ThrottleHint). The default acts as a floor so callers
// that pass throttle=0 (always run) keep their semantics.
func adapterInterval(a Adapter, defaultThrottle time.Duration) time.Duration {
	interval := defaultThrottle
	if hinter, ok := a.(ThrottleHinter); ok {
		if hint := hinter.ThrottleHint(); hint > interval {
			interval = hint
		}
	}
	return interval
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

// LoadState exposes per-adapter backoff/last_collect for callers that
// want to render diagnostics (e.g. `projmux usage --json`).
func (m *Manager) LoadState() (State, error) {
	if m == nil {
		return State{}, errors.New("usage: nil manager")
	}
	if m.store == nil {
		return State{}, errors.New("usage: nil store")
	}
	return m.store.LoadState()
}
