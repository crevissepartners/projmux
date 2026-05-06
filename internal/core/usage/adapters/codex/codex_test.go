package codex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/usage"
)

// writeRollout creates a rollout-*.jsonl under root at YYYY/MM/DD with
// the supplied body and mtime.
func writeRollout(t *testing.T, root, isoDate, body string, mtime time.Time) string {
	t.Helper()
	parts := strings.SplitN(isoDate, "-", 3)
	if len(parts) != 3 {
		t.Fatalf("bad isoDate %q", isoDate)
	}
	dir := filepath.Join(root, parts[0], parts[1], parts[2])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	file := filepath.Join(dir, "rollout-"+isoDate+"-test.jsonl")
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(file, mtime, mtime); err != nil {
			t.Fatalf("Chtimes: %v", err)
		}
	}
	return file
}

// rolloutWithRateLimits returns a JSONL body containing one user record
// (no rate_limits) and one token_count record carrying rate_limits.
func rolloutWithRateLimits(primary, secondary float64, primaryReset, secondaryReset int64) string {
	return `{"timestamp":"2026-04-30T09:00:00Z","type":"event_msg","payload":{"type":"agent_message"}}` + "\n" +
		fmt.Sprintf(`{"timestamp":"2026-04-30T09:30:00Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":100}},"rate_limits":{"limit_id":"codex","primary":{"used_percent":%g,"window_minutes":300,"resets_at":%d},"secondary":{"used_percent":%g,"window_minutes":10080,"resets_at":%d},"plan_type":"prolite"}}}`,
			primary, primaryReset, secondary, secondaryReset) + "\n"
}

func TestAdapterCollectFromNewestRollout(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	older := time.Date(2026, 4, 29, 10, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 4, 30, 9, 30, 0, 0, time.UTC)
	// Older rollout — different percentages so we can verify the newer
	// one wins purely on mtime ordering.
	writeRollout(t, root, "2026-04-29",
		rolloutWithRateLimits(99.0, 99.0, 1777559000, 1777966000), older)
	writeRollout(t, root, "2026-04-30",
		rolloutWithRateLimits(5.0, 12.0, 1777559543, 1777966095), newer)

	a := NewWithRoot(root)
	a.now = func() time.Time { return time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC) }

	snaps, err := a.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("len(snaps) = %d, want 2", len(snaps))
	}
	five, weekly := snaps[0], snaps[1]
	if five.Window != usage.Window5h || five.Pct != 5.0 {
		t.Fatalf("5h snapshot = %+v, want pct=5.0 from newer rollout", five)
	}
	if weekly.Window != usage.WindowWeekly || weekly.Pct != 12.0 {
		t.Fatalf("weekly snapshot = %+v, want pct=12.0", weekly)
	}
	wantReset := time.Unix(1777559543, 0).UTC()
	if !five.ResetsAt.Equal(wantReset) {
		t.Fatalf("5h ResetsAt = %v, want %v", five.ResetsAt, wantReset)
	}
}

func TestAdapterCollectPicksLatestRateLimitsLineWithinFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	body := `{"type":"event_msg","payload":{"type":"agent_message"}}` + "\n" +
		// Earlier rate_limits payload — should be overridden.
		`{"type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":1.0,"resets_at":100},"secondary":{"used_percent":1.0,"resets_at":200}}}}` + "\n" +
		// Later — this is the one we want.
		`{"type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":42.0,"resets_at":300},"secondary":{"used_percent":7.0,"resets_at":400}}}}` + "\n"
	mtime := time.Date(2026, 4, 30, 9, 30, 0, 0, time.UTC)
	writeRollout(t, root, "2026-04-30", body, mtime)

	a := NewWithRoot(root)
	a.now = func() time.Time { return time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC) }

	snaps, err := a.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("len(snaps) = %d, want 2", len(snaps))
	}
	if snaps[0].Pct != 42.0 {
		t.Fatalf("5h pct = %v, want 42.0 (last rate_limits in file wins)", snaps[0].Pct)
	}
	if snaps[1].Pct != 7.0 {
		t.Fatalf("weekly pct = %v, want 7.0", snaps[1].Pct)
	}
}

func TestAdapterCollectMissingRoot(t *testing.T) {
	t.Parallel()

	a := NewWithRoot(filepath.Join(t.TempDir(), "missing"))
	snaps, err := a.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if snaps != nil {
		t.Fatalf("snaps = %+v, want nil", snaps)
	}
}

func TestAdapterCollectNoRolloutFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "2026", "04", "30"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	a := NewWithRoot(root)
	a.now = func() time.Time { return time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC) }
	snaps, err := a.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if snaps != nil {
		t.Fatalf("snaps = %+v, want nil (no files)", snaps)
	}
}

func TestAdapterCollectRolloutsWithoutRateLimits(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	body := `{"type":"event_msg","payload":{"type":"agent_message"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":42}}}}` + "\n"
	writeRollout(t, root, "2026-04-30", body, time.Now())

	a := NewWithRoot(root)
	a.now = func() time.Time { return time.Now() }
	snaps, err := a.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if snaps != nil {
		t.Fatalf("snaps = %+v, want nil (no rate_limits in any rollout)", snaps)
	}
}

func TestAdapterCollectSkipsOldFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	writeRollout(t, root, "2026-01-01",
		rolloutWithRateLimits(99.0, 99.0, 1777559000, 1777966000), old)

	a := NewWithRoot(root)
	a.now = func() time.Time { return time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC) }
	snaps, err := a.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if snaps != nil {
		t.Fatalf("snaps = %+v, want nil (file mtime predates window)", snaps)
	}
}
