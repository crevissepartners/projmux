package usage

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	localstate "github.com/crevissepartners/projmux/internal/state"
	"golang.org/x/sys/unix"
)

const (
	historyDirName       = "history"
	historyLockName      = ".history.lock"
	historyIndexName     = ".history.index.json"
	HistoryRetention     = 30 * 24 * time.Hour
	HistoryInterval      = time.Minute
	HistoryLockWaitLimit = 3 * time.Second
)

var ErrHistoryLockTimeout = errors.New("usage: timed out waiting for history lock")
var ErrHistoryWrite = errors.New("usage: history write failed")

// MetricPoint is one observation. Name determines units; labels identify a series.
type MetricPoint struct {
	Name       string     `json:"name"`
	Value      float64    `json:"value"`
	ObservedAt time.Time  `json:"observed_at"`
	Provider   string     `json:"provider,omitempty"`
	Window     string     `json:"window,omitempty"`
	Bucket     string     `json:"bucket,omitempty"`
	ResetsAt   *time.Time `json:"resets_at,omitempty"`
}

type HistoryFilter struct {
	Provider string
	Window   string
	Metric   string
	Now      time.Time
}

func (s *Store) HistoryPath() string     { return filepath.Join(s.baseDir, historyDirName) }
func (s *Store) HistoryLockPath() string { return filepath.Join(s.baseDir, historyLockName) }

func segmentName(at time.Time) string { return at.UTC().Format("2006-01-02") + ".jsonl" }
func segmentDate(name string) (time.Time, error) {
	if !strings.HasSuffix(name, ".jsonl") {
		return time.Time{}, errors.New("not a segment")
	}
	date := strings.TrimSuffix(name, ".jsonl")
	at, err := time.Parse("2006-01-02", date)
	if err != nil || segmentName(at) != name {
		return time.Time{}, fmt.Errorf("usage: invalid history segment %q", name)
	}
	return at, nil
}

type segmentIndex struct {
	Size     int64                `json:"size"`
	Modified int64                `json:"modified"`
	Oldest   time.Time            `json:"oldest"`
	Last     map[string]time.Time `json:"last"`
}
type historyIndex struct {
	Segments map[string]segmentIndex `json:"segments"`
}

func listSegments(dir string) (map[string]os.FileInfo, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]os.FileInfo{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("usage: list history: %w", err)
	}
	files := make(map[string]os.FileInfo)
	for _, entry := range entries {
		// A process can die before an atomic prune rename. Such an uncommitted
		// temporary file is never a history segment.
		if strings.HasPrefix(entry.Name(), ".history.tmp-") {
			continue
		}
		if _, err := segmentDate(entry.Name()); err != nil {
			return nil, err
		}
		if !entry.Type().IsRegular() {
			return nil, fmt.Errorf("usage: history segment %q is not regular", entry.Name())
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		files[entry.Name()] = info
	}
	return files, nil
}

func cleanHistoryTemps(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".history.tmp-") {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			return fmt.Errorf("usage: remove abandoned history prune: %w", err)
		}
	}
	return nil
}

func (s *Store) loadHistoryIndex(files map[string]os.FileInfo) (historyIndex, bool) {
	if len(files) == 0 {
		return historyIndex{Segments: map[string]segmentIndex{}}, true
	}
	b, err := os.ReadFile(filepath.Join(s.baseDir, historyIndexName)) // #nosec G304 -- private store sibling
	if err != nil {
		return historyIndex{}, false
	}
	var idx historyIndex
	if json.Unmarshal(b, &idx) != nil || len(idx.Segments) != len(files) {
		return historyIndex{}, false
	}
	for name, info := range files {
		seg, ok := idx.Segments[name]
		if !ok || seg.Last == nil || seg.Size != info.Size() || seg.Modified != info.ModTime().UnixNano() {
			return historyIndex{}, false
		}
	}
	return idx, true
}

func (s *Store) saveHistoryIndex(index historyIndex) error {
	b, err := json.Marshal(index)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.baseDir, ".history.index.tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(localstate.PrivateFileMode); err != nil {
		return errors.Join(err, f.Close())
	}
	if _, err := f.Write(b); err != nil {
		return errors.Join(err, f.Close())
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(s.baseDir, historyIndexName))
}

func historyKey(p MetricPoint) string {
	return p.Name + "\x00" + p.Provider + "\x00" + p.Window + "\x00" + p.Bucket
}
func validPoint(p MetricPoint) bool {
	return p.Name != "" && !p.ObservedAt.IsZero() && !math.IsNaN(p.Value) && !math.IsInf(p.Value, 0)
}

// The persistent lock inode is never removed. Reads and writes share the lock,
// so a reader cannot see a partially appended batch or an incomplete prune.
func (s *Store) withHistoryLock(fn func() error) error {
	if err := localstate.EnsurePrivateDir(s.baseDir); err != nil {
		return fmt.Errorf("usage: create history dir: %w", err)
	}
	path := s.HistoryLockPath()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, localstate.PrivateFileMode) // #nosec G304 -- private store sibling
	if err != nil {
		return fmt.Errorf("usage: open history lock: %w", err)
	}
	defer f.Close()
	localstate.RepairPrivateFile(path)
	deadline := time.Now().Add(HistoryLockWaitLimit)
	for {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			return fmt.Errorf("usage: acquire history lock: %w", err)
		}
		if !time.Now().Before(deadline) {
			return ErrHistoryLockTimeout
		}
		time.Sleep(min(5*time.Millisecond, time.Until(deadline)))
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN)
	return fn()
}

func readHistoryFile(path string) ([]MetricPoint, error) {
	f, err := os.Open(path) // #nosec G304 -- private store sibling
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("usage: read history: %w", err)
	}
	defer f.Close()
	r := bufio.NewReader(f)
	var points []MetricPoint
	for line := 1; ; line++ {
		b, err := r.ReadBytes('\n')
		if len(b) > 0 {
			if b[len(b)-1] != '\n' {
				return nil, fmt.Errorf("usage: partial history row %s:%d", path, line)
			}
			var p MetricPoint
			if jsonErr := json.Unmarshal(b, &p); jsonErr != nil || !validPoint(p) {
				return nil, fmt.Errorf("usage: malformed history row %s:%d: %v", path, line, jsonErr)
			}
			if segmentName(p.ObservedAt) != filepath.Base(path) {
				return nil, fmt.Errorf("usage: misplaced history row %s:%d", path, line)
			}
			points = append(points, p)
		}
		if errors.Is(err, io.EOF) {
			return points, nil
		}
		if err != nil {
			return nil, fmt.Errorf("usage: scan history: %w", err)
		}
	}
}

func (s *Store) readSegment(name string) ([]MetricPoint, error) {
	return readHistoryFile(filepath.Join(s.HistoryPath(), name))
}

// ReadHistory has no collector, network, or adapter dependency. Expired points
// are hidden even before a subsequent write physically prunes their segment.
func (s *Store) ReadHistory(filter HistoryFilter) ([]MetricPoint, error) {
	// A first read of an empty state does not materialize any state or lock.
	if _, err := os.Stat(s.HistoryPath()); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	now := filter.Now
	if now.IsZero() {
		now = time.Now()
	}
	cutoff := now.UTC().Add(-HistoryRetention)
	var out []MetricPoint
	err := s.withHistoryLock(func() error {
		files, err := listSegments(s.HistoryPath())
		if err != nil {
			return err
		}
		for name := range files {
			day, _ := segmentDate(name)
			if day.Add(24*time.Hour).Before(cutoff) || day.After(now) {
				continue
			}
			points, err := s.readSegment(name)
			if err != nil {
				return err
			}
			for _, p := range points {
				if p.ObservedAt.Before(cutoff) || p.ObservedAt.After(now) ||
					filter.Provider != "" && p.Provider != filter.Provider ||
					filter.Window != "" && p.Window != filter.Window ||
					filter.Metric != "" && p.Name != filter.Metric {
					continue
				}
				out = append(out, p)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ObservedAt.Before(out[j].ObservedAt) })
	return out, nil
}

func indexSegment(points []MetricPoint, info os.FileInfo) segmentIndex {
	seg := segmentIndex{Last: map[string]time.Time{}}
	if info != nil {
		seg.Size, seg.Modified = info.Size(), info.ModTime().UnixNano()
	}
	for _, p := range points {
		if seg.Oldest.IsZero() || p.ObservedAt.Before(seg.Oldest) {
			seg.Oldest = p.ObservedAt
		}
		key := historyKey(p)
		if p.ObservedAt.After(seg.Last[key]) {
			seg.Last[key] = p.ObservedAt
		}
	}
	return seg
}

func (s *Store) rebuildHistoryIndex(files map[string]os.FileInfo) (historyIndex, error) {
	idx := historyIndex{Segments: make(map[string]segmentIndex)}
	for name, info := range files {
		points, err := s.readSegment(name)
		if err != nil {
			return idx, err
		}
		idx.Segments[name] = indexSegment(points, info)
	}
	return idx, nil
}

func latestBySeries(idx historyIndex) map[string]time.Time {
	last := map[string]time.Time{}
	for _, seg := range idx.Segments {
		for key, at := range seg.Last {
			if at.After(last[key]) {
				last[key] = at
			}
		}
	}
	return last
}

func (s *Store) hasNearby(p MetricPoint) (bool, error) {
	for _, at := range []time.Time{p.ObservedAt.Add(-HistoryInterval), p.ObservedAt, p.ObservedAt.Add(HistoryInterval)} {
		points, err := s.readSegment(segmentName(at))
		if err != nil {
			return false, err
		}
		for _, old := range points {
			if historyKey(old) == historyKey(p) && old.ObservedAt.Sub(p.ObservedAt) < HistoryInterval && p.ObservedAt.Sub(old.ObservedAt) < HistoryInterval {
				return true, nil
			}
		}
	}
	return false, nil
}

// AppendHistory serializes dedup, prune, and append under its own bounded lock.
// Only the rolling cutoff's day is rewritten; complete expired days are deleted.
func (s *Store) AppendHistory(points []MetricPoint, now time.Time) error {
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	return s.withHistoryLock(func() error {
		dir := s.HistoryPath()
		if err := localstate.EnsurePrivateDir(dir); err != nil {
			return err
		}
		if err := cleanHistoryTemps(dir); err != nil {
			return err
		}
		files, err := listSegments(dir)
		if err != nil {
			return err
		}
		idx, valid := s.loadHistoryIndex(files)
		if !valid {
			idx, err = s.rebuildHistoryIndex(files)
			if err != nil {
				return err
			}
		}
		cutoff := now.Add(-HistoryRetention)
		changed := !valid
		for name := range files {
			day, _ := segmentDate(name)
			path := filepath.Join(dir, name)
			if !day.Add(24 * time.Hour).After(cutoff) {
				if err := os.Remove(path); err != nil {
					return fmt.Errorf("usage: remove expired history: %w", err)
				}
				delete(idx.Segments, name)
				changed = true
				continue
			}
			if !day.Before(cutoff) {
				continue
			}
			if oldest := idx.Segments[name].Oldest; !oldest.IsZero() && !oldest.Before(cutoff) {
				continue
			}
			old, err := s.readSegment(name)
			if err != nil {
				return err
			}
			kept := make([]MetricPoint, 0, len(old))
			for _, p := range old {
				if !p.ObservedAt.Before(cutoff) {
					kept = append(kept, p)
				}
			}
			if len(kept) == len(old) {
				continue
			}
			if len(kept) == 0 {
				if err := os.Remove(path); err != nil {
					return fmt.Errorf("usage: remove empty history segment: %w", err)
				}
				delete(idx.Segments, name)
				changed = true
				continue
			}
			if err := replaceHistory(path, kept); err != nil {
				return err
			}
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			idx.Segments[name] = indexSegment(kept, info)
			changed = true
		}
		last := latestBySeries(idx)
		added := map[string][]MetricPoint{}
		for _, p := range points {
			if !validPoint(p) {
				return errors.New("usage: invalid history point")
			}
			p.ObservedAt = p.ObservedAt.UTC()
			if p.ResetsAt != nil {
				t := p.ResetsAt.UTC()
				p.ResetsAt = &t
			}
			if p.ObservedAt.Before(cutoff) || p.ObservedAt.After(now) {
				continue
			}
			key := historyKey(p)
			if prior := last[key]; !prior.IsZero() {
				if p.ObservedAt.Sub(prior) >= 0 && p.ObservedAt.Sub(prior) < HistoryInterval {
					continue
				}
				if p.ObservedAt.Before(prior) {
					nearby, err := s.hasNearby(p)
					if err != nil {
						return err
					}
					if nearby {
						continue
					}
				}
			}
			duplicate := false
			for _, batch := range added {
				for _, old := range batch {
					if historyKey(old) == key && old.ObservedAt.Sub(p.ObservedAt) < HistoryInterval && p.ObservedAt.Sub(old.ObservedAt) < HistoryInterval {
						duplicate = true
					}
				}
			}
			if duplicate {
				continue
			}
			name := segmentName(p.ObservedAt)
			added[name] = append(added[name], p)
			if p.ObservedAt.After(last[key]) {
				last[key] = p.ObservedAt
			}
		}
		for name, batch := range added {
			path := filepath.Join(dir, name)
			if err := appendHistoryBatch(path, batch); err != nil {
				return err
			}
			seg := idx.Segments[name]
			if seg.Last == nil {
				seg.Last = map[string]time.Time{}
			}
			for _, p := range batch {
				if seg.Oldest.IsZero() || p.ObservedAt.Before(seg.Oldest) {
					seg.Oldest = p.ObservedAt
				}
				key := historyKey(p)
				if p.ObservedAt.After(seg.Last[key]) {
					seg.Last[key] = p.ObservedAt
				}
			}
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			seg.Size, seg.Modified = info.Size(), info.ModTime().UnixNano()
			idx.Segments[name] = seg
			changed = true
		}
		if changed {
			return s.saveHistoryIndex(idx)
		}
		return nil
	})
}

func appendHistoryBatch(path string, points []MetricPoint) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, localstate.PrivateFileMode) // #nosec G304 -- private store sibling
	if err != nil {
		return err
	}
	defer f.Close()
	localstate.RepairPrivateFile(path)
	before, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	var batch []byte
	for _, p := range points {
		b, err := json.Marshal(p)
		if err != nil {
			return err
		}
		batch = append(batch, b...)
		batch = append(batch, '\n')
	}
	n, err := f.Write(batch)
	if err != nil || n != len(batch) {
		if rollbackErr := f.Truncate(before); rollbackErr != nil {
			return fmt.Errorf("usage: append history: %v; rollback: %w", err, rollbackErr)
		}
		return fmt.Errorf("usage: append history: %w", errors.Join(err, io.ErrShortWrite))
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("usage: close history append: %w", err)
	}
	return nil
}

func replaceHistory(path string, points []MetricPoint) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".history.tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(localstate.PrivateFileMode); err != nil {
		return errors.Join(err, f.Close())
	}
	for _, p := range points {
		b, err := json.Marshal(p)
		if err != nil {
			return errors.Join(err, f.Close())
		}
		if _, err := f.Write(append(b, '\n')); err != nil {
			return errors.Join(err, f.Close())
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
