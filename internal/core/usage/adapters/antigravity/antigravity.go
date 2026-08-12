// Package antigravity implements a best-effort usage Adapter for the
// Antigravity CLI (`agy`).
//
// The adapter consumes account-wide named `quota` buckets. Quota bucket IDs
// remain opaque upstream identities; this package never aliases them to the
// canonical 5h/weekly windows.
//
// The hook ingest path still writes conversation-local `context_window`
// diagnostics to ContextFileName for notify/private sidecar consumers, but
// that gauge is deliberately not projected into usage snapshots.
package antigravity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/core/usage"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

// Name is the adapter's registered identifier. It matches the aiprovider
// registry ID and the usage model key so scope filtering lines up.
const Name = "antigravity"

// ContextFileName is the private hook/notify diagnostic sidecar written by
// the Antigravity ingest path. It remains alongside snapshots.json in the
// resolved usage state directory but is not adapter input.
const ContextFileName = "antigravity-context.json"

// QuotaFileName stores the latest account-quota map independently from the
// conversation-local context sidecar. A context-only statusline update must
// not overwrite the last observed account quota state.
const QuotaFileName = "antigravity-quota.json"

// ContextRecord is the private sidecar schema written by the hook ingest path.
// Pct is the context-window fullness percentage (0-100); ConversationID
// preserves its conversation-local identity; UpdatedAt is when the value was
// last observed.
type ContextRecord struct {
	ConversationID string    `json:"conversation_id,omitempty"`
	Pct            float64   `json:"pct"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// QuotaBucketRecord preserves one official statusline quota entry. ID is the
// upstream map key verbatim. ResetInSeconds is a pointer so absent and zero
// retain different meanings.
type QuotaBucketRecord struct {
	ID                string    `json:"id"`
	RemainingFraction float64   `json:"remaining_fraction"`
	ResetTime         time.Time `json:"reset_time"`
	ResetInSeconds    *int64    `json:"reset_in_seconds,omitempty"`
}

// QuotaRecord is the quota sidecar wire shape. UpdatedAt records when the
// whole map was observed; individual buckets share that freshness boundary.
type QuotaRecord struct {
	Buckets   []QuotaBucketRecord `json:"buckets"`
	UpdatedAt time.Time           `json:"updated_at"`
}

// Adapter is the Antigravity implementation of usage.Adapter.
type Adapter struct {
	baseDir string
}

// New returns an Adapter that reads QuotaFileName from baseDir (the resolved
// usage state directory).
func New(baseDir string) *Adapter {
	return &Adapter{baseDir: baseDir}
}

// Name implements usage.Adapter.
func (a *Adapter) Name() string { return Name }

// Collect reads the account quota sidecar. Missing files and invalid quota
// buckets degrade to no rows. Conversation context remains private diagnostic
// metadata and is not an account-usage row.
func (a *Adapter) Collect(_ context.Context) ([]usage.Snapshot, error) {
	if strings.TrimSpace(a.baseDir) == "" {
		return nil, nil
	}
	var snaps []usage.Snapshot
	quotaRec, ok, err := readJSON[QuotaRecord](filepath.Join(a.baseDir, QuotaFileName))
	if err != nil {
		return nil, fmt.Errorf("read quota: %w", err)
	} else if ok {
		buckets := append([]QuotaBucketRecord(nil), quotaRec.Buckets...)
		sort.Slice(buckets, func(i, j int) bool { return buckets[i].ID < buckets[j].ID })
		seen := map[string]bool{}
		for _, bucket := range buckets {
			if !ValidQuotaBucket(bucket) || seen[bucket.ID] {
				continue
			}
			seen[bucket.ID] = true
			snaps = append(snaps, usage.Snapshot{
				Model:          Name,
				Window:         usage.WindowQuota,
				Bucket:         bucket.ID,
				Pct:            100 * (1 - bucket.RemainingFraction),
				ResetInSeconds: cloneInt64(bucket.ResetInSeconds),
				ResetsAt:       bucket.ResetTime.UTC(),
				UpdatedAt:      quotaRec.UpdatedAt.UTC(),
			})
		}
	}
	return snaps, nil
}

func readJSON[T any](path string) (T, bool, error) {
	var zero T
	localstate.RepairPrivateFile(path)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return zero, false, nil
		}
		return zero, false, err
	}
	if len(data) == 0 {
		return zero, false, nil
	}
	var rec T
	if err := json.Unmarshal(data, &rec); err != nil {
		return zero, false, err
	}
	return rec, true, nil
}

// WriteContext atomically persists rec to <baseDir>/ContextFileName,
// creating baseDir if needed. Used by the antigravity hook ingest path.
func WriteContext(baseDir string, rec ContextRecord) error {
	rec.UpdatedAt = rec.UpdatedAt.UTC()
	return writeJSON(baseDir, ContextFileName, rec)
}

// WriteQuota atomically replaces the latest observed quota map. An explicit
// empty map is persisted as an empty bucket list, clearing older account rows.
func WriteQuota(baseDir string, rec QuotaRecord) error {
	rec.UpdatedAt = rec.UpdatedAt.UTC()
	sort.Slice(rec.Buckets, func(i, j int) bool { return rec.Buckets[i].ID < rec.Buckets[j].ID })
	return writeJSON(baseDir, QuotaFileName, rec)
}

func writeJSON(baseDir, fileName string, value any) error {
	if err := localstate.EnsurePrivateDir(baseDir); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(baseDir, fileName)
	tmp, err := os.CreateTemp(baseDir, "."+fileName+".tmp-*")
	if err != nil {
		return err
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
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	localstate.RepairPrivateFile(path)
	return nil
}

// ValidQuotaBucket applies the account-quota safety boundary shared by the
// ingest writer and adapter reader. It rejects empty identities, non-finite or
// out-of-range fractions, and negative relative resets.
func ValidQuotaBucket(bucket QuotaBucketRecord) bool {
	if bucket.ID == "" || math.IsNaN(bucket.RemainingFraction) || math.IsInf(bucket.RemainingFraction, 0) {
		return false
	}
	if bucket.RemainingFraction < 0 || bucket.RemainingFraction > 1 {
		return false
	}
	return bucket.ResetInSeconds == nil || *bucket.ResetInSeconds >= 0
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// ParsePercent parses an antigravity context_window string (e.g. "42%",
// "42", "42.5 %") into a percentage float. The bool is false when the
// input is empty or not a number, so callers can skip persisting garbage.
func ParsePercent(raw string) (float64, bool) {
	s := strings.TrimSpace(raw)
	s = strings.TrimSuffix(s, "%")
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	pct, err := strconv.ParseFloat(s, 64)
	if err != nil || pct < 0 {
		return 0, false
	}
	return pct, true
}
