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

// snapshotFile is the on-disk schema the Store writes. Version is bumped
// to 2 to make the schema break (v1 was per-model bucketed token counts)
// explicit.
type snapshotFile struct {
	Version     int        `json:"version"`
	LastCollect time.Time  `json:"last_collect"`
	Snapshots   []Snapshot `json:"snapshots"`
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

// BaseDir returns the directory the Store is rooted at.
func (s *Store) BaseDir() string {
	if s == nil {
		return ""
	}
	return s.baseDir
}

// LoadAll reads the persisted snapshots. A missing file reads as empty
// (no error) so first-run callers see []Snapshot{}.
func (s *Store) LoadAll() ([]Snapshot, time.Time, error) {
	path := s.FilePath()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, time.Time{}, nil
		}
		return nil, time.Time{}, fmt.Errorf("usage: read snapshots %s: %w", path, err)
	}
	if len(data) == 0 {
		return nil, time.Time{}, nil
	}
	var f snapshotFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, time.Time{}, fmt.Errorf("usage: parse snapshots %s: %w", path, err)
	}
	out := make([]Snapshot, 0, len(f.Snapshots))
	for _, snap := range f.Snapshots {
		// Defensive normalisation: ensure timestamps are UTC so the renderer
		// gets a stable shape regardless of how the file was written.
		snap.ResetsAt = snap.ResetsAt.UTC()
		snap.UpdatedAt = snap.UpdatedAt.UTC()
		out = append(out, snap)
	}
	return out, f.LastCollect.UTC(), nil
}

// SaveAll persists snapshots to disk, replacing whatever was there
// before. lastCollect is recorded so MaybeCollect can throttle without a
// separate marker file.
func (s *Store) SaveAll(snaps []Snapshot, lastCollect time.Time) error {
	if err := os.MkdirAll(s.baseDir, 0o755); err != nil {
		return fmt.Errorf("usage: create cache dir %s: %w", s.baseDir, err)
	}
	// Stable ordering: model asc, window asc within model.
	sorted := make([]Snapshot, len(snaps))
	copy(sorted, snaps)
	windowOrder := map[Window]int{Window5h: 0, WindowWeekly: 1}
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Model != sorted[j].Model {
			return sorted[i].Model < sorted[j].Model
		}
		return windowOrder[sorted[i].Window] < windowOrder[sorted[j].Window]
	})
	payload := snapshotFile{
		Version:     2,
		LastCollect: lastCollect.UTC(),
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
	windowOrder := map[Window]int{Window5h: 0, WindowWeekly: 1}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Model != out[j].Model {
			return out[i].Model < out[j].Model
		}
		return windowOrder[out[i].Window] < windowOrder[out[j].Window]
	})
	return out
}
