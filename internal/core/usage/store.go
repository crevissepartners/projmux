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

// retentionDuration controls how far back the rolling cache is preserved.
// 8 days covers the weekly window with a safety margin so a clock skew or a
// missed collection cycle near the boundary does not silently drop data.
const retentionDuration = 8 * 24 * time.Hour

// bucketLayout records bucket-key formatting. Buckets are 1-minute wide and
// keyed by RFC3339 minute timestamps so the persisted file stays
// human-inspectable.
const bucketLayout = "2006-01-02T15:04Z07:00"

// cacheFile is the on-disk schema for a single model's bucket cache. The
// schema is intentionally tiny so future PRs can extend it without breaking
// older clients.
type cacheFile struct {
	Version int           `json:"version"`
	Model   string        `json:"model"`
	Buckets []cacheBucket `json:"buckets"`
}

type cacheBucket struct {
	// Minute is the bucket's UTC minute boundary (truncated).
	Minute time.Time `json:"minute"`
	// Tokens is the sum of TokenEvent.Tokens that fell into this bucket.
	Tokens int64 `json:"tokens"`
}

// Store persists per-minute token totals for a single model under
// <baseDir>/<model>.json.
type Store struct {
	baseDir string
}

// NewStore returns a Store rooted at baseDir. Callers typically pass
// filepath.Join(paths.StateDir, "usage").
func NewStore(baseDir string) *Store {
	return &Store{baseDir: baseDir}
}

// FilePath returns the on-disk JSON path used for a specific model. Exposed
// so callers can plumb it through diagnostics.
func (s *Store) FilePath(model string) string {
	return filepath.Join(s.baseDir, sanitizeModel(model)+".json")
}

// Load reads the cache for a given model. A missing file reads as an empty
// cache (no error) so first-run callers see []Bucket{}.
func (s *Store) Load(model string) ([]Bucket, error) {
	path := s.FilePath(model)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("usage: read cache %s: %w", path, err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	var f cacheFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("usage: parse cache %s: %w", path, err)
	}
	out := make([]Bucket, 0, len(f.Buckets))
	for _, b := range f.Buckets {
		out = append(out, Bucket{Minute: b.Minute.UTC(), Tokens: b.Tokens})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Minute.Before(out[j].Minute) })
	return out, nil
}

// Append merges new TokenEvents into the existing buckets, trims entries
// older than the retention window relative to now, and writes the cache
// atomically. Returns the merged bucket slice the caller can pass straight
// to the aggregator without re-reading disk.
func (s *Store) Append(model string, events []TokenEvent, now time.Time) ([]Bucket, error) {
	existing, err := s.Load(model)
	if err != nil {
		return nil, err
	}

	merged := mergeEvents(existing, events)
	merged = trim(merged, now.Add(-retentionDuration))

	if err := s.write(model, merged); err != nil {
		return nil, err
	}
	return merged, nil
}

// write persists the merged bucket slice atomically.
func (s *Store) write(model string, buckets []Bucket) error {
	if err := os.MkdirAll(s.baseDir, 0o755); err != nil {
		return fmt.Errorf("usage: create cache dir %s: %w", s.baseDir, err)
	}
	payload := cacheFile{
		Version: 1,
		Model:   model,
		Buckets: make([]cacheBucket, 0, len(buckets)),
	}
	for _, b := range buckets {
		payload.Buckets = append(payload.Buckets, cacheBucket{
			Minute: b.Minute.UTC(),
			Tokens: b.Tokens,
		})
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("usage: encode cache: %w", err)
	}
	path := s.FilePath(model)
	tmp, err := os.CreateTemp(s.baseDir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("usage: create temp cache: %w", err)
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
		return fmt.Errorf("usage: write temp cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("usage: close temp cache: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("usage: chmod temp cache: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("usage: rename temp cache: %w", err)
	}
	cleanup = false
	return nil
}

// Bucket is the in-memory representation of a 1-minute aggregation bucket.
type Bucket struct {
	Minute time.Time
	Tokens int64
}

// mergeEvents folds a slice of raw events into the existing bucket list,
// returning a new sorted slice keyed by 1-minute bucket boundaries.
func mergeEvents(existing []Bucket, events []TokenEvent) []Bucket {
	if len(events) == 0 {
		return append([]Bucket(nil), existing...)
	}
	index := make(map[time.Time]int64, len(existing)+len(events))
	for _, b := range existing {
		index[b.Minute.UTC().Truncate(time.Minute)] += b.Tokens
	}
	for _, e := range events {
		key := e.At.UTC().Truncate(time.Minute)
		index[key] += e.Tokens
	}
	out := make([]Bucket, 0, len(index))
	for minute, tokens := range index {
		out = append(out, Bucket{Minute: minute, Tokens: tokens})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Minute.Before(out[j].Minute) })
	return out
}

// trim drops buckets whose minute is strictly before the cutoff.
func trim(buckets []Bucket, cutoff time.Time) []Bucket {
	cutoff = cutoff.UTC().Truncate(time.Minute)
	out := buckets[:0]
	for _, b := range buckets {
		if b.Minute.Before(cutoff) {
			continue
		}
		out = append(out, b)
	}
	// Avoid handing the caller a slice that aliases the input's backing array
	// in surprising ways: copy when the trimmed length differs.
	trimmed := make([]Bucket, len(out))
	copy(trimmed, out)
	return trimmed
}

// sanitizeModel keeps cache file names safe for arbitrary model strings.
func sanitizeModel(name string) string {
	if name == "" {
		return "_"
	}
	out := make([]rune, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-', r == '_':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}
