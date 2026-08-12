// Package antigravity implements a best-effort usage Adapter for the
// Antigravity CLI (`agy`).
//
// Unlike Claude (OAuth usage API) and Codex (rate_limits in the rollout
// JSONL), Antigravity exposes NO server-side 5h/weekly quota contract. The
// only usage-shaped signal it emits is the statusline `context_window`
// percentage — how full the active conversation's context window is. This
// adapter is therefore context-window-only: it surfaces a single
// usage.Snapshot on the usage.WindowContext window and never fabricates
// 5h/weekly rows.
//
// Source of truth: a small sidecar file written by the antigravity hook
// ingest path (`projmux ai ingest antigravity-hook`) whenever a hook
// payload carries a context_window value. The ingest path and this adapter
// agree on the file name (ContextFileName) and schema (ContextRecord); the
// file lives in the same directory as the usage snapshot cache so a single
// PROJMUX_USAGE_STATE_DIR override keeps both in sync.
//
// The value reflects the most recently observed context window across all
// antigravity panes — it is a live gauge, not an account-wide quota, so it
// carries no ResetsAt. Best-effort throughout: a missing or malformed
// sidecar reads as zero snapshots so the status segment degrades cleanly.
package antigravity

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/core/usage"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

// Name is the adapter's registered identifier. It matches the aiprovider
// registry ID and the usage model key so scope filtering lines up.
const Name = "antigravity"

// ContextFileName is the sidecar JSON the ingest path writes and this
// adapter reads. It sits alongside the usage snapshot cache
// (snapshots.json) in the resolved usage state directory.
const ContextFileName = "antigravity-context.json"

// ContextRecord is the on-disk schema shared between the hook ingest path
// (writer) and this adapter (reader). Pct is the context-window fullness
// percentage (0-100); UpdatedAt is when the value was last observed.
type ContextRecord struct {
	ConversationID string    `json:"conversation_id,omitempty"`
	Pct            float64   `json:"pct"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Adapter is the Antigravity implementation of usage.Adapter.
type Adapter struct {
	baseDir string
}

// New returns an Adapter that reads ContextFileName from baseDir (the
// resolved usage state directory).
func New(baseDir string) *Adapter {
	return &Adapter{baseDir: baseDir}
}

// Name implements usage.Adapter.
func (a *Adapter) Name() string { return Name }

// Collect reads the context sidecar and returns a single context-window
// Snapshot. Best-effort: a missing base dir, missing file, malformed JSON
// or an out-of-range percentage all read as zero snapshots (no error) so
// the status segment stays silent rather than breaking.
func (a *Adapter) Collect(_ context.Context) ([]usage.Snapshot, error) {
	if strings.TrimSpace(a.baseDir) == "" {
		return nil, nil
	}
	rec, ok, err := readContext(filepath.Join(a.baseDir, ContextFileName))
	if err != nil {
		// Surface for diagnostics under PROJMUX_USAGE_DEBUG; the hot path
		// swallows it. Return no snapshots so rendering degrades cleanly.
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	pct := rec.Pct
	if pct < 0 {
		pct = 0
	}
	return []usage.Snapshot{{
		Model:     Name,
		Window:    usage.WindowContext,
		Pct:       pct,
		UpdatedAt: rec.UpdatedAt.UTC(),
	}}, nil
}

func readContext(path string) (ContextRecord, bool, error) {
	localstate.RepairPrivateFile(path)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ContextRecord{}, false, nil
		}
		return ContextRecord{}, false, err
	}
	if len(data) == 0 {
		return ContextRecord{}, false, nil
	}
	var rec ContextRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return ContextRecord{}, false, err
	}
	return rec, true, nil
}

// WriteContext atomically persists rec to <baseDir>/ContextFileName,
// creating baseDir if needed. Used by the antigravity hook ingest path.
func WriteContext(baseDir string, rec ContextRecord) error {
	if err := localstate.EnsurePrivateDir(baseDir); err != nil {
		return err
	}
	rec.UpdatedAt = rec.UpdatedAt.UTC()
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(baseDir, ContextFileName)
	tmp, err := os.CreateTemp(baseDir, "."+ContextFileName+".tmp-*")
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
