package codex

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/core/usage"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

const (
	nativeEventSnapshotFileName = "codex-native-rate-limit-events.json"
	nativeEventSnapshotVersion  = 1
	maxNativeEventSnapshotBytes = 512 * 1024
)

// NativeEventBatch is the durable, sanitized handoff from the read-only
// app-server watcher to a short-lived status invocation. It contains only the
// already-normalized public Snapshot projection; raw notification payloads are
// never written.
type NativeEventBatch struct {
	Version    int              `json:"version"`
	ObservedAt time.Time        `json:"observed_at"`
	Snapshots  []usage.Snapshot `json:"snapshots"`
}

// NativeEventCache owns the private atomic watcher sidecar under the resolved
// usage state directory. The public snapshots.json store remains Manager-owned.
type NativeEventCache struct {
	baseDir string
	now     func() time.Time
}

func NewNativeEventCache(baseDir string, now func() time.Time) *NativeEventCache {
	if now == nil {
		now = time.Now
	}
	return &NativeEventCache{baseDir: baseDir, now: now}
}

func (c *NativeEventCache) FilePath() string {
	if c == nil {
		return ""
	}
	return filepath.Join(c.baseDir, nativeEventSnapshotFileName)
}

// Publish atomically replaces the sidecar with one native-only normalized
// batch. A mixed/fallback/stale row is refused before any durable write.
func (c *NativeEventCache) Publish(snapshots []usage.Snapshot) error {
	if c == nil || strings.TrimSpace(c.baseDir) == "" {
		return errors.New("codex native event cache has no state directory")
	}
	now := c.now().UTC()
	rows := make([]usage.Snapshot, len(snapshots))
	for i, snapshot := range snapshots {
		if err := validateNativeEventSnapshot(snapshot); err != nil {
			return fmt.Errorf("codex native event cache row %d: %w", i+1, err)
		}
		if snapshot.UpdatedAt.IsZero() {
			snapshot.UpdatedAt = now
		}
		rows[i] = snapshot
	}
	if len(rows) == 0 {
		return errors.New("codex native event cache requires at least one row")
	}
	payload, err := json.MarshalIndent(NativeEventBatch{
		Version: nativeEventSnapshotVersion, ObservedAt: now, Snapshots: rows,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode codex native event cache: %w", err)
	}
	if len(payload) > maxNativeEventSnapshotBytes {
		return errors.New("codex native event cache exceeds size limit")
	}
	if err := localstate.EnsurePrivateDir(c.baseDir); err != nil {
		return fmt.Errorf("create codex native event cache directory: %w", err)
	}
	path := c.FilePath()
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("codex native event cache path is a symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect codex native event cache: %w", err)
	}
	tmp, err := os.CreateTemp(c.baseDir, ".codex-native-rate-limit-events.tmp-*")
	if err != nil {
		return fmt.Errorf("create codex native event cache temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(localstate.PrivateFileMode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secure codex native event cache temp file: %w", err)
	}
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write codex native event cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close codex native event cache: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace codex native event cache: %w", err)
	}
	cleanup = false
	localstate.RepairPrivateFile(path)
	return nil
}

// Load validates and returns one complete native-only batch. Invalid or stale
// policy is intentionally left to the short-lived status consumer; this layer
// only establishes that the durable bytes are safe and structurally usable.
func (c *NativeEventCache) Load() (NativeEventBatch, error) {
	if c == nil || strings.TrimSpace(c.baseDir) == "" {
		return NativeEventBatch{}, errors.New("codex native event cache has no state directory")
	}
	path := c.FilePath()
	info, err := os.Lstat(path)
	if err != nil {
		return NativeEventBatch{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return NativeEventBatch{}, errors.New("codex native event cache is not a regular file")
	}
	if info.Size() <= 0 || info.Size() > maxNativeEventSnapshotBytes {
		return NativeEventBatch{}, errors.New("codex native event cache has invalid size")
	}
	// #nosec G304 -- path is the cache-owned fixed child returned by FilePath; the preceding Lstat rejects symlinks and non-regular or unbounded files.
	file, err := os.Open(path)
	if err != nil {
		return NativeEventBatch{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxNativeEventSnapshotBytes+1))
	if err != nil {
		return NativeEventBatch{}, fmt.Errorf("read codex native event cache: %w", err)
	}
	if len(data) > maxNativeEventSnapshotBytes {
		return NativeEventBatch{}, errors.New("codex native event cache exceeds size limit")
	}
	var batch NativeEventBatch
	if err := json.Unmarshal(data, &batch); err != nil {
		return NativeEventBatch{}, errors.New("codex native event cache is malformed")
	}
	if batch.Version != nativeEventSnapshotVersion || batch.ObservedAt.IsZero() || len(batch.Snapshots) == 0 {
		return NativeEventBatch{}, errors.New("codex native event cache has invalid envelope")
	}
	batch.ObservedAt = batch.ObservedAt.UTC()
	for i, snapshot := range batch.Snapshots {
		if err := validateNativeEventSnapshot(snapshot); err != nil {
			return NativeEventBatch{}, fmt.Errorf("codex native event cache row %d: %w", i+1, err)
		}
		batch.Snapshots[i].ResetsAt = snapshot.ResetsAt.UTC()
		batch.Snapshots[i].UpdatedAt = snapshot.UpdatedAt.UTC()
	}
	return batch, nil
}

func validateNativeEventSnapshot(snapshot usage.Snapshot) error {
	if !strings.EqualFold(strings.TrimSpace(snapshot.Model), Name) {
		return errors.New("unexpected model")
	}
	if snapshot.Source != usage.SourceAppServer || snapshot.FallbackReason != "" || snapshot.StaleReason != "" {
		return errors.New("unexpected source provenance")
	}
	if snapshot.RateLimit == nil {
		return errors.New("missing native rate-limit metadata")
	}
	if snapshot.Window != usage.Window5h && snapshot.Window != usage.WindowWeekly && snapshot.Window != usage.WindowQuota {
		return errors.New("unexpected native window")
	}
	if math.IsNaN(snapshot.Pct) || math.IsInf(snapshot.Pct, 0) || snapshot.Pct < 0 {
		return errors.New("invalid percentage")
	}
	return nil
}
