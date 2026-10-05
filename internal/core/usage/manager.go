package usage

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	// adapter-internal backoff). Used by `projmux agent usage` where the user
	// explicitly asked for fresh data.
	_, snaps, err := m.collect(ctx, 0, false, nil, false)
	return snaps, err
}

// ForceCollect runs every registered adapter unconditionally, bypassing
// per-adapter throttle AND clearing any active backoff (in-memory and
// the on-disk Backoff map) so the network call attempts now regardless
// of prior 429 streak. A successful response resets the streak to 0; a
// 429 reinstates backoff with consecutive=1 (the streak does NOT
// preserve across `--force`).
//
// Useful as a manual override (`projmux agent usage --force` /
// `projmux internal status usage --force`) when the user wants the latest
// numbers right now and accepts that they may re-trigger 429.
func (m *Manager) ForceCollect(ctx context.Context) ([]Snapshot, error) {
	_, snaps, err := m.collect(ctx, 0, true, nil, false)
	return snaps, err
}

// ApplySnapshots commits one already-normalized adapter event through the
// same replace/store contract as Collect. It deliberately advances that
// adapter's last-collect timestamp: a fresh event is authoritative account
// state and must suppress a second network/fallback source decision in the
// same refresh. Callers must supply exactly one model and a non-empty batch.
func (m *Manager) ApplySnapshots(model string, snapshots []Snapshot) ([]Snapshot, error) {
	if m == nil {
		return nil, errors.New("usage: nil manager")
	}
	if m.store == nil {
		return nil, errors.New("usage: nil store")
	}
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" || len(snapshots) == 0 {
		return nil, errors.New("usage: event snapshot batch requires one model and at least one row")
	}
	now := m.now().UTC()
	fresh := make([]Snapshot, len(snapshots))
	for i, snapshot := range snapshots {
		if strings.ToLower(strings.TrimSpace(snapshot.Model)) != model {
			return nil, fmt.Errorf("usage: event snapshot model mismatch at row %d", i+1)
		}
		snapshot.Model = model
		if snapshot.UpdatedAt.IsZero() {
			snapshot.UpdatedAt = now
		}
		fresh[i] = snapshot
	}
	var merged []Snapshot
	err := m.store.withStateLock(func() error {
		state, err := m.store.LoadState()
		if err != nil {
			return err
		}
		if state.LastCollect == nil {
			state.LastCollect = map[string]time.Time{}
		}
		merged = make([]Snapshot, 0, len(state.Snapshots)+len(fresh))
		merged = append(merged, fresh...)
		for _, snapshot := range state.Snapshots {
			if strings.ToLower(strings.TrimSpace(snapshot.Model)) != model {
				merged = append(merged, snapshot)
			}
		}
		state.Snapshots = merged
		state.LastCollect[model] = now
		return m.store.SaveState(state)
	})
	if err != nil {
		return nil, err
	}
	if historyErr := m.store.AppendHistory(usageHistoryPoints(fresh), now); historyErr != nil {
		return SortedSnapshots(merged), fmt.Errorf("%w: %w", ErrHistoryWrite, historyErr)
	}
	return SortedSnapshots(merged), nil
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
//
// Adapters named in `skip` are not walked at all; their prior rows survive
// through the same merge step that preserves a throttled adapter's rows.
//
// requireDue=true makes the walk conditional: when no adapter is due (see
// dueIn) nothing is walked or written and walked=false.
//
// Concurrency: other projmux processes sharing the state dir run the same
// flow, so the state is only ever read-modified-written under the store's
// state lock, and the lock is never held across an adapter call.
//
//  1. Claim, under the lock: re-read the state, decide what is due, record
//     `last_collect` for every adapter about to be walked, and save. A
//     concurrent caller that takes the lock next sees the claim and treats
//     those adapters as not due, so each adapter is collected once per
//     throttle window.
//  2. Walk the claimed adapters with the lock released.
//  3. Commit, under the lock: re-read the state and merge only this walk's
//     results (rows, backoff, last_collect) into it, so a commit another
//     process made meanwhile (ApplySnapshots, another walk) survives.
func (m *Manager) collect(ctx context.Context, perAdapterFloor time.Duration, force bool, skip map[string]bool, requireDue bool) (bool, []Snapshot, error) {
	if m == nil {
		return false, nil, errors.New("usage: nil manager")
	}
	if m.registry == nil {
		return false, nil, errors.New("usage: nil registry")
	}
	if m.store == nil {
		return false, nil, errors.New("usage: nil store")
	}

	now := m.now().UTC()
	// Best-effort cleanup of v1 artifacts. Cheap; safe to retry.
	// Legacy: retained for usage v1/v2 cleanup; sunset when a post-0.7 review
	// after two minor releases or 90 days intentionally ignores or removes v1
	// artifacts.
	m.store.CleanupLegacyArtifacts()

	var (
		due       bool
		claimed   []Adapter
		backoffIn map[string]BackoffState
		claimView State
	)
	err := m.store.withStateLock(func() error {
		state := loadStateForMerge(m.store)
		if requireDue && !m.dueIn(state, now, perAdapterFloor, skip) {
			return nil
		}
		due = true
		backoffIn = map[string]BackoffState{}
		for _, adapter := range m.registry.All() {
			name := adapter.Name()

			// A caller that already committed this adapter from a fresher
			// source in the same refresh excludes it here.
			if skip[name] {
				continue
			}

			// Per-adapter throttle gate. Skip adapters whose effective
			// interval has not elapsed; their prior rows survive via the
			// merge step. Floor=0 disables the gate. force=true also
			// disables the gate so `--force` always attempts every
			// adapter.
			if !force && perAdapterFloor > 0 {
				interval := adapterInterval(adapter, perAdapterFloor)
				if last, ok := state.LastCollect[name]; ok && !last.IsZero() && now.Sub(last) < interval {
					continue
				}
			}
			claimed = append(claimed, adapter)
			backoffIn[name] = state.Backoff[name]
			// The claim: last_collect[name] advances on every adapter walk
			// — failures and empty results included — and it advances
			// before the network call so no other process walks this
			// adapter while this one is in flight. The throttle is about
			// not hammering the upstream; backoff (BackoffStater) handles
			// the longer 429-induced cooldown separately.
			state.LastCollect[name] = now
		}
		claimView = state
		if len(claimed) == 0 {
			return nil
		}
		return m.store.SaveState(state)
	})
	if err != nil {
		return false, nil, err
	}
	if !due {
		return false, nil, nil
	}

	results := make([]adapterWalk, 0, len(claimed))
	for _, adapter := range claimed {
		results = append(results, m.walkAdapter(ctx, adapter, backoffIn[adapter.Name()], force, now))
	}

	var errs []error
	for _, result := range results {
		if result.err != nil {
			errs = append(errs, &AdapterError{Model: result.name, Err: result.err})
		}
	}
	var merged []Snapshot
	commitErr := m.store.withStateLock(func() error {
		state := loadStateForMerge(m.store)
		merged = mergeWalk(&state, results, now)
		if err := m.store.SaveState(state); err != nil {
			return fmt.Errorf("save snapshots: %w", err)
		}
		return nil
	})
	if commitErr != nil {
		errs = append(errs, commitErr)
		if merged == nil {
			// The commit never read the state, so answer the caller from the
			// view the claim read. Nothing of this walk was persisted.
			merged = mergeWalk(&claimView, results, now)
		}
	}
	if commitErr == nil {
		var fresh []Snapshot
		for _, result := range results {
			fresh = append(fresh, result.snaps...)
		}
		if historyErr := m.store.AppendHistory(usageHistoryPoints(fresh), now); historyErr != nil {
			errs = append(errs, fmt.Errorf("%w: %w", ErrHistoryWrite, historyErr))
		}
	}
	if len(errs) > 0 {
		return true, merged, errors.Join(errs...)
	}
	return true, merged, nil
}

func usageHistoryPoints(snaps []Snapshot) []MetricPoint {
	points := make([]MetricPoint, 0, len(snaps))
	for _, snap := range snaps {
		if snap.UpdatedAt.IsZero() || snap.Window == WindowContext {
			continue
		}
		p := MetricPoint{Name: "usage.percent", Value: snap.Pct, ObservedAt: snap.UpdatedAt,
			Provider: snap.Model, Window: string(snap.Window), Bucket: snap.Bucket}
		if !snap.ResetsAt.IsZero() {
			reset := snap.ResetsAt
			p.ResetsAt = &reset
		}
		points = append(points, p)
	}
	return points
}

// adapterWalk is one adapter's outcome from a collect walk, held until it is
// merged into the state read at commit time.
type adapterWalk struct {
	name       string
	snaps      []Snapshot
	err        error
	backoff    BackoffState
	hasBackoff bool
}

// walkAdapter runs one claimed adapter. It must be called without the state
// lock held: the adapter may make a network call.
func (m *Manager) walkAdapter(ctx context.Context, adapter Adapter, backoff BackoffState, force bool, now time.Time) adapterWalk {
	result := adapterWalk{name: adapter.Name()}
	// Install persisted backoff state before Collect so the adapter
	// can early-return without making the network call. Under
	// force=true we drop both the on-disk view AND the in-memory
	// view via ResetBackoff so this Collect attempts the network
	// call regardless of prior 429 streak.
	if bs, ok := adapter.(BackoffStater); ok {
		if force {
			bs.LoadBackoff(BackoffState{})
		} else {
			bs.LoadBackoff(backoff)
		}
	}
	if force {
		if br, ok := adapter.(BackoffResetter); ok {
			br.ResetBackoff()
		}
	}
	snaps, err := adapter.Collect(ctx)
	// Stamp UpdatedAt so the renderer can show "as of" without callers
	// having to thread the clock through every adapter.
	for i := range snaps {
		if snaps[i].UpdatedAt.IsZero() {
			snaps[i].UpdatedAt = now
		}
	}
	result.snaps = snaps
	result.err = err
	// Persist backoff regardless of success/failure: a 429 sets
	// `until`; a clean success resets `consecutive` to 0.
	if bs, ok := adapter.(BackoffStater); ok {
		result.backoff = bs.SaveBackoff()
		result.hasBackoff = true
	}
	return result
}

// mergeWalk folds one walk's results into state and returns the merged
// snapshot rows, which it also stores in state.Snapshots.
//
// Merge semantics, evaluated per adapter of this walk:
//   - Adapter returned a non-empty snapshot slice → REPLACE all rows for
//     the models it touched.
//   - Adapter errored or returned zero snapshots → PRESERVE the rows for
//     that model, marked with the error's stale reason when it has one.
//
// Rows, backoff and last_collect of adapters outside this walk are left
// exactly as state holds them.
func mergeWalk(state *State, results []adapterWalk, now time.Time) []Snapshot {
	if state.LastCollect == nil {
		state.LastCollect = map[string]time.Time{}
	}
	if state.Backoff == nil {
		state.Backoff = map[string]BackoffState{}
	}
	merged := make([]Snapshot, 0, len(state.Snapshots))
	freshModels := map[string]bool{}
	staleByModel := map[string]SnapshotReason{}
	for _, result := range results {
		state.LastCollect[result.name] = now
		if result.hasBackoff {
			state.Backoff[result.name] = result.backoff
		}
		if result.err != nil && len(result.snaps) == 0 {
			if reason := SnapshotStaleReason(result.err); reason != "" {
				staleByModel[result.name] = reason
			}
		}
		if len(result.snaps) > 0 {
			// Successful collect: replace all prior rows for the models
			// the adapter touched. We trust the adapter to emit one row
			// per (model, window, bucket) identity it owns.
			for _, s := range result.snaps {
				freshModels[s.Model] = true
			}
			merged = append(merged, result.snaps...)
		}
	}

	// Preserve rows for any model this walk did NOT successfully refresh.
	// This is the load-bearing fix for the 429 regression: a Claude failure
	// must not erase the Claude rows.
	for _, row := range state.Snapshots {
		if freshModels[row.Model] {
			continue
		}
		if reason := staleByModel[row.Model]; reason != "" {
			row.StaleReason = reason
		}
		merged = append(merged, row)
	}
	state.Snapshots = merged
	return merged
}

// loadStateForMerge reads the state for a read-modify-write. An unreadable or
// corrupt file reads as empty, as it always has for collect: the next save
// replaces it.
func loadStateForMerge(store *Store) State {
	state, _ := store.LoadState()
	if state.LastCollect == nil {
		state.LastCollect = map[string]time.Time{}
	}
	if state.Backoff == nil {
		state.Backoff = map[string]BackoffState{}
	}
	return state
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
	return m.MaybeCollectExcept(ctx, throttle)
}

// MaybeCollectExcept is MaybeCollect with the named adapters left out of
// both the due check and the walk.
//
// Callers use it when a fresher source already committed those adapters in
// this same refresh: ApplySnapshots' contract is that an accepted event
// batch suppresses a second source decision for that model in the same
// pass. Every OTHER registered adapter is still walked when it is due, so
// accepting a batch for one model never starves an unrelated adapter that
// has passed its own floor.
//
// Concurrent calls from any number of processes sharing the state dir
// collect each adapter at most once per throttle window: the due decision
// and the claim happen under the state lock (see collect). A call that
// cannot take the lock within StateLockWaitLimit walks nothing and returns
// an error wrapping ErrStateLockTimeout.
func (m *Manager) MaybeCollectExcept(ctx context.Context, throttle time.Duration, exclude ...string) (bool, error) {
	if m == nil {
		return false, errors.New("usage: nil manager")
	}
	if m.store == nil {
		return false, errors.New("usage: nil store")
	}
	skip := adapterNameSet(exclude)
	if throttle <= 0 || m.registry == nil {
		// No floor always collects; a nil registry surfaces its error
		// from collect exactly as before.
		_, _, collectErr := m.collect(ctx, throttle, false, skip, false)
		return true, collectErr
	}
	walked, _, collectErr := m.collect(ctx, throttle, false, skip, true)
	return walked, collectErr
}

// adapterNameSet normalizes adapter names into a lookup set. Adapter names
// are lower-case model identifiers, so the comparison matches Registry keys.
func adapterNameSet(names []string) map[string]bool {
	if len(names) == 0 {
		return nil
	}
	set := make(map[string]bool, len(names))
	for _, name := range names {
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "" {
			set[name] = true
		}
	}
	return set
}

// dueIn reports whether MaybeCollect should run adapters given state. It
// consults per-adapter timestamps so a slow adapter (Claude OAuth,
// 5min) does not block a fast one (Codex, 30s). The Manager runs the
// full adapter walk if ANY adapter is due, then the merge preserves the
// not-yet-due adapters' rows. Adapters in `skip` are ignored here exactly
// as they are ignored by the walk itself.
func (m *Manager) dueIn(state State, now time.Time, defaultThrottle time.Duration, skip map[string]bool) bool {
	for _, adapter := range m.registry.All() {
		name := adapter.Name()
		if skip[name] {
			continue
		}
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
// want to render diagnostics (e.g. `projmux agent usage --json`).
func (m *Manager) LoadState() (State, error) {
	if m == nil {
		return State{}, errors.New("usage: nil manager")
	}
	if m.store == nil {
		return State{}, errors.New("usage: nil store")
	}
	return m.store.LoadState()
}
