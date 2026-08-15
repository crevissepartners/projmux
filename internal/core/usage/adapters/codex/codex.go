// Package codex implements an authoritative usage Adapter for the Codex CLI.
//
// Source of truth: the `rate_limits` payload embedded in `event_msg` /
// `token_count` records inside the latest rollout JSONL under
// `~/.codex/sessions/<YYYY>/<MM>/<DD>/rollout-*.jsonl`. The schema:
//
//	{
//	  "limit_id": "codex",
//	  "primary":   { "used_percent": 0.0, "window_minutes": 300,   "resets_at": 1777559537 },
//	  "secondary": { "used_percent": 1.0, "window_minutes": 10080, "resets_at": 1777966095 },
//	  "plan_type": "prolite"
//	}
//
// Windows are classified semantically by `window_minutes`: 300 is the
// 5-hour window and 10080 is the weekly window, regardless of which slot
// contains them. `resets_at` is a unix timestamp (seconds, UTC). Compared
// to the v0 JSONL token-counting implementation, this approach is:
//
//   - Server-authoritative: numbers match what `codex` itself shows.
//   - Cross-machine: usage on another box is reflected as soon as that
//     box's last turn lands in a rollout file synced via Dropbox/etc.
//   - Drift-free: no local accumulation.
//
// The adapter walks rollouts newest-first (by mtime, NOT filename — they
// happen to correlate but are not guaranteed to) until it finds a line
// containing `rate_limits`. Files older than `scanWindow` are skipped to
// keep cold-start cheap.
package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/crevissepartners/projmux/internal/core/usage"
)

// Name is the adapter's registered identifier.
const Name = "codex"

// scanWindow caps how far back we look for rollouts. 8 days covers the
// weekly window with a safety margin for synced machines that just came
// online.
const scanWindow = 8 * 24 * time.Hour

// Adapter is the Codex implementation of usage.Adapter.
type Adapter struct {
	homeDir      func() (string, error)
	now          func() time.Time
	rootOverride string
}

// New returns an Adapter that reads from $HOME/.codex/sessions.
func New() *Adapter {
	return &Adapter{homeDir: os.UserHomeDir, now: time.Now}
}

// NewWithRoot is intended for tests.
func NewWithRoot(root string) *Adapter {
	return &Adapter{homeDir: os.UserHomeDir, now: time.Now, rootOverride: root}
}

// Name implements usage.Adapter.
func (a *Adapter) Name() string { return Name }

// Collect locates the newest rollout-*.jsonl, walks it for the latest
// `rate_limits` payload, and returns its supported window Snapshots.
// Best-effort: missing tree → nil snapshots, no rollout with rate_limits
// → nil snapshots.
func (a *Adapter) Collect(ctx context.Context) ([]usage.Snapshot, error) {
	root, err := a.resolveRoot()
	if err != nil {
		return nil, nil
	}
	if _, err := os.Stat(root); err != nil {
		return nil, nil
	}

	now := a.now().UTC()
	cutoff := now.Add(-scanWindow)

	files, err := filepath.Glob(filepath.Join(root, "*", "*", "*", "rollout-*.jsonl"))
	if err != nil {
		return nil, nil
	}
	type fileInfo struct {
		path  string
		mtime time.Time
	}
	infos := make([]fileInfo, 0, len(files))
	for _, p := range files {
		st, statErr := os.Stat(p)
		if statErr != nil {
			continue
		}
		if st.ModTime().Before(cutoff) {
			continue
		}
		infos = append(infos, fileInfo{path: p, mtime: st.ModTime()})
	}
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].mtime.After(infos[j].mtime)
	})

	for _, fi := range infos {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		rl, found := scanLatestRateLimits(fi.path)
		if !found {
			continue
		}
		// Row-level skip: a window slot that fails field validation is
		// dropped and reported, the other slot still reaches the snapshot.
		snaps, skipped := rl.toSnapshots(now)
		return snaps, usage.RowSkipWarning(skipped)
	}
	return nil, nil
}

func (a *Adapter) resolveRoot() (string, error) {
	if a.rootOverride != "" {
		return a.rootOverride, nil
	}
	home, err := a.homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex", "sessions"), nil
}

// rateLimits captures the bits of the codex rate_limits schema we care
// about. Pointer fields so we can distinguish "missing" from "0%".
type rateLimits struct {
	Primary   *rateLimitWindow `json:"primary"`
	Secondary *rateLimitWindow `json:"secondary"`
}

// rateLimitWindow uses a pointer for UsedPercent so a slot that is present
// but carries no percentage is distinguishable from a genuine 0%. Without
// that distinction the adapter fabricated a 0% row for a partial payload.
type rateLimitWindow struct {
	UsedPercent   *float64 `json:"used_percent"`
	WindowMinutes int      `json:"window_minutes"`
	ResetsAt      int64    `json:"resets_at"`
}

// toSnapshots projects the two window slots, returning the bounded reasons for
// any slot it had to drop. A dropped slot is absent, never substituted with a
// zero percentage or a synthesized reset.
//
// An unrecognised window_minutes is NOT a row defect: window classification is
// the adapter's own semantic mapping (300 → 5h, 10080 → weekly) and an unknown
// cadence is simply a window this build does not render. Those stay silent.
func (rl *rateLimits) toSnapshots(now time.Time) ([]usage.Snapshot, []string) {
	out := make([]usage.Snapshot, 0, 2)
	var skipped []string
	for _, slot := range []struct {
		name  string
		limit *rateLimitWindow
	}{{"primary", rl.Primary}, {"secondary", rl.Secondary}} {
		if slot.limit == nil {
			continue
		}
		if slot.limit.UsedPercent == nil {
			skipped = append(skipped, slot.name+": missing used_percent")
			continue
		}
		window, supported := usageWindow(slot.limit.WindowMinutes)
		if !supported {
			continue
		}
		s := usage.Snapshot{
			Model:     Name,
			Window:    window,
			Pct:       *slot.limit.UsedPercent,
			UpdatedAt: now,
		}
		if slot.limit.ResetsAt > 0 {
			s.ResetsAt = time.Unix(slot.limit.ResetsAt, 0).UTC()
		}
		out = append(out, s)
	}
	return out, skipped
}

func usageWindow(windowMinutes int) (usage.Window, bool) {
	switch windowMinutes {
	case 300:
		return usage.Window5h, true
	case 10080:
		return usage.WindowWeekly, true
	default:
		return "", false
	}
}

// rolloutLine is the minimal slice of the rollout schema needed to find
// rate_limits. We only deserialise the line if a cheap byte-level
// substring check sees `"rate_limits"` first — most lines don't contain
// it and parsing every JSON line would be wasteful on busy sessions.
type rolloutLine struct {
	Payload *struct {
		Type       string      `json:"type"`
		RateLimits *rateLimits `json:"rate_limits"`
	} `json:"payload"`
}

// scanLatestRateLimits returns the LAST rate_limits payload found in the
// supplied rollout file, or (nil, false) if none. The "last" semantics
// matches Codex's own behaviour: the most recent token_count event in
// the session reflects current account state.
func scanLatestRateLimits(path string) (*rateLimits, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	const maxLine = 16 * 1024 * 1024
	scanner.Buffer(make([]byte, 0, 64*1024), maxLine)

	needle := []byte(`"rate_limits"`)
	var latest *rateLimits
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 || !bytes.Contains(line, needle) {
			continue
		}
		var rec rolloutLine
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if rec.Payload == nil || rec.Payload.RateLimits == nil {
			continue
		}
		// Defensive copy so the loop's `rec` going out of scope is safe.
		rl := *rec.Payload.RateLimits
		latest = &rl
	}
	if latest == nil {
		return nil, false
	}
	return latest, true
}
