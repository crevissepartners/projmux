package usage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// snapshotFileName is the on-disk JSON used by the v2 snapshot store. The
// store REPLACES this file on every successful Collect — there is no
// merge/append behaviour because authoritative upstream sources already
// report the current account-wide state.
const snapshotFileName = "snapshots.json"

// BackoffState captures per-adapter exponential-backoff bookkeeping that
// must survive process restarts. `Until` is the absolute time at which
// the next adapter call is permitted; `Consecutive` counts the number of
// 429-style failures observed in a row so callers can grow the next
// interval geometrically.
type BackoffState struct {
	Until       time.Time `json:"until"`
	Consecutive int       `json:"consecutive"`
}

// snapshotFile is the on-disk schema the Store writes. Version stays at 2;
// the shape evolved (per-adapter `last_collect` map, optional `backoff`
// map) in a backward-compatible way. The custom UnmarshalJSON below
// accepts both the prior single-string `last_collect` (treated as missing
// per-adapter timestamps) and the new map form.
type snapshotFile struct {
	Version     int                     `json:"version"`
	LastCollect map[string]time.Time    `json:"last_collect,omitempty"`
	Backoff     map[string]BackoffState `json:"backoff,omitempty"`
	Snapshots   []Snapshot              `json:"snapshots"`
}

// snapshotFileWire is the loose, decode-only mirror of snapshotFile that
// permits `last_collect` to be either a string (legacy) or a map. Encoding
// always uses snapshotFile so we never re-emit the legacy string form.
type snapshotFileWire struct {
	Version     int                     `json:"version"`
	LastCollect json.RawMessage         `json:"last_collect,omitempty"`
	Backoff     map[string]BackoffState `json:"backoff,omitempty"`
	Snapshots   []Snapshot              `json:"snapshots"`
}

// Store persists the most recent batch of Snapshots emitted by all
// registered adapters under <baseDir>/snapshots.json.
type Store struct {
	baseDir string
}

// NewStore returns a Store rooted at baseDir. Callers typically pass
// filepath.Join(paths.StateDir, "usage") — or whatever PROJMUX_USAGE_STATE_DIR
// resolved to.
func NewStore(baseDir string) *Store {
	return &Store{baseDir: baseDir}
}

// FilePath returns the on-disk JSON path used for the snapshot cache.
// Exposed so callers can plumb it through diagnostics.
func (s *Store) FilePath() string {
	return filepath.Join(s.baseDir, snapshotFileName)
}

// State is the rich view the Manager and adapters need: snapshots plus
// per-adapter throttle/backoff bookkeeping. Returned by LoadState; the
// older LoadAll wrapper is kept for callers that only want the snapshot
// list.
type State struct {
	Snapshots   []Snapshot
	LastCollect map[string]time.Time
	Backoff     map[string]BackoffState
}

// LoadAll reads the persisted snapshots. A missing file reads as empty
// (no error) so first-run callers see []Snapshot{}. The second return is
// the most recent global last_collect across adapters (kept for callers
// that only care that *something* was collected) — for per-adapter timing
// use LoadState.
func (s *Store) LoadAll() ([]Snapshot, time.Time, error) {
	st, err := s.LoadState()
	if err != nil {
		return nil, time.Time{}, err
	}
	var latest time.Time
	for _, t := range st.LastCollect {
		if t.After(latest) {
			latest = t
		}
	}
	return st.Snapshots, latest, nil
}

// LoadState reads the persisted file and returns the full per-adapter
// state. A missing file reads as zero-value State (no error).
func (s *Store) LoadState() (State, error) {
	path := s.FilePath()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return State{}, nil
		}
		return State{}, fmt.Errorf("usage: read snapshots %s: %w", path, err)
	}
	if len(data) == 0 {
		return State{}, nil
	}
	var w snapshotFileWire
	if err := json.Unmarshal(data, &w); err != nil {
		return State{}, fmt.Errorf("usage: parse snapshots %s: %w", path, err)
	}

	state := State{
		LastCollect: map[string]time.Time{},
		Backoff:     map[string]BackoffState{},
	}
	if len(w.LastCollect) > 0 {
		// Try map form first; fall back to string (legacy v2). A bare
		// string predates the per-adapter map and is treated as missing
		// per-adapter timestamps so the next collect runs everything.
		var asMap map[string]time.Time
		if err := json.Unmarshal(w.LastCollect, &asMap); err == nil {
			for k, v := range asMap {
				state.LastCollect[k] = v.UTC()
			}
		}
		// If asMap parsing failed (string form), the map stays empty —
		// equivalent to "no per-adapter timestamps", which is the
		// documented migration behaviour.
	}
	for k, v := range w.Backoff {
		v.Until = v.Until.UTC()
		state.Backoff[k] = v
	}
	out := make([]Snapshot, 0, len(w.Snapshots))
	for _, snap := range w.Snapshots {
		// Defensive normalisation: ensure timestamps are UTC so the renderer
		// gets a stable shape regardless of how the file was written.
		snap.ResetsAt = snap.ResetsAt.UTC()
		snap.UpdatedAt = snap.UpdatedAt.UTC()
		out = append(out, snap)
	}
	state.Snapshots = out
	return state, nil
}

// SaveAll persists snapshots to disk, replacing whatever was there
// before. lastCollect is recorded under a single synthetic adapter key
// ("_global") so legacy callers that don't know about per-adapter
// throttling still mark forward progress. New callers should use
// SaveState directly.
func (s *Store) SaveAll(snaps []Snapshot, lastCollect time.Time) error {
	state := State{
		Snapshots: snaps,
		LastCollect: map[string]time.Time{
			"_global": lastCollect.UTC(),
		},
	}
	return s.SaveState(state)
}

// SaveState persists the full per-adapter state to disk atomically.
func (s *Store) SaveState(state State) error {
	if err := os.MkdirAll(s.baseDir, 0o755); err != nil {
		return fmt.Errorf("usage: create cache dir %s: %w", s.baseDir, err)
	}
	// Stable ordering: model asc, window asc within model.
	sorted := make([]Snapshot, len(state.Snapshots))
	copy(sorted, state.Snapshots)
	windowOrder := map[Window]int{Window5h: 0, WindowWeekly: 1, WindowContext: 2}
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Model != sorted[j].Model {
			return sorted[i].Model < sorted[j].Model
		}
		return windowOrder[sorted[i].Window] < windowOrder[sorted[j].Window]
	})
	last := map[string]time.Time{}
	for k, v := range state.LastCollect {
		last[k] = v.UTC()
	}
	backoff := map[string]BackoffState{}
	for k, v := range state.Backoff {
		// Drop zero-valued entries: they mean "no active backoff" and
		// just clutter the on-disk file. Adapters reload an absent key
		// as a zero BackoffState which is the desired default.
		if v.Until.IsZero() && v.Consecutive == 0 {
			continue
		}
		v.Until = v.Until.UTC()
		backoff[k] = v
	}
	payload := snapshotFile{
		Version:     2,
		LastCollect: last,
		Backoff:     backoff,
		Snapshots:   sorted,
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("usage: encode snapshots: %w", err)
	}
	path := s.FilePath()
	tmp, err := os.CreateTemp(s.baseDir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("usage: create temp snapshots: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("usage: write temp snapshots: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("usage: close temp snapshots: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("usage: chmod temp snapshots: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("usage: rename temp snapshots: %w", err)
	}
	cleanup = false
	return nil
}

// CleanupLegacyArtifacts best-effort removes files left behind by the v1
// bucketed cache. The new schema uses a different filename so the old
// files are inert clutter — keeping them around indefinitely just confuses
// users who inspect the state dir. Errors are ignored: this is purely
// hygienic.
func (s *Store) CleanupLegacyArtifacts() {
	if s == nil || s.baseDir == "" {
		return
	}
	for _, name := range []string{"claude.json", "codex.json", "last_collect"} {
		_ = os.Remove(filepath.Join(s.baseDir, name))
	}
}

// SortedSnapshots returns snapshots ordered by (model, window). Convenience
// for CLI rendering that wants stable output.
func SortedSnapshots(snaps []Snapshot) []Snapshot {
	out := make([]Snapshot, len(snaps))
	copy(out, snaps)
	windowOrder := map[Window]int{Window5h: 0, WindowWeekly: 1, WindowContext: 2}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Model != out[j].Model {
			return out[i].Model < out[j].Model
		}
		return windowOrder[out[i].Window] < windowOrder[out[j].Window]
	})
	return out
}
