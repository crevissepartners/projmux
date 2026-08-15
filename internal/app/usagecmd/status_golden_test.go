package usagecmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/usage"
)

// status_golden_test.go freezes the tmux status-line usage segment across the
// three staleness tiers. The `fresh` golden is byte-identical to the output
// produced before the age indicator learned about staleAfter/veryStaleAfter:
// a level-0 age must keep rendering exactly as it always did, so this file is
// the regression that a staleness change did not repaint healthy installs.

// statusGoldenNow is the frozen wall clock shared by every golden case.
var statusGoldenNow = time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)

// statusGoldenSnapshots builds a two-provider fixture: Claude carries the age
// indicator (its data is throttle/backoff-gated) and Codex deliberately does
// not, so each golden also pins that only the opted-in model is annotated.
func statusGoldenSnapshots(age time.Duration) []usage.Snapshot {
	updated := statusGoldenNow.Add(-age)
	return []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 42, ResetsAt: statusGoldenNow.Add(time.Hour), UpdatedAt: updated},
		{Model: "claude", Window: usage.WindowWeekly, Pct: 18, ResetsAt: statusGoldenNow.Add(7 * 24 * time.Hour), UpdatedAt: updated},
		{Model: "codex", Window: usage.Window5h, Pct: 71, ResetsAt: statusGoldenNow.Add(time.Hour), UpdatedAt: statusGoldenNow},
	}
}

func TestFormatStatusUsageStalenessGoldens(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		age    time.Duration
		golden string
	}{
		// Level 0: below staleAfter. Byte-identical to the pre-change output.
		{name: "fresh", age: 3 * time.Minute, golden: "status-usage-fresh.golden"},
		// Level 1: above staleAfter, at or below veryStaleAfter.
		{name: "stale", age: 15 * time.Minute, golden: "status-usage-stale.golden"},
		// Level 2: above veryStaleAfter.
		{name: "very-stale", age: 3 * time.Hour, golden: "status-usage-very-stale.golden"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := formatStatusUsage(statusGoldenSnapshots(tc.age), 0, statusGoldenNow)
			want, err := os.ReadFile(filepath.Join("testdata", tc.golden))
			if err != nil {
				t.Fatalf("read golden: %v", err)
			}
			if got != string(want) {
				t.Fatalf("status usage golden mismatch (%s)\n--- got ---\n%s\n--- want ---\n%s", tc.golden, got, want)
			}
		})
	}
}

// TestStatusUsageGoldensDifferByStalenessTier guards against the goldens
// silently collapsing into the same bytes, which would make the table above
// pass without proving anything about the tier boundaries.
func TestStatusUsageGoldensDifferByStalenessTier(t *testing.T) {
	t.Parallel()

	fresh := formatStatusUsage(statusGoldenSnapshots(3*time.Minute), 0, statusGoldenNow)
	stale := formatStatusUsage(statusGoldenSnapshots(15*time.Minute), 0, statusGoldenNow)
	veryStale := formatStatusUsage(statusGoldenSnapshots(3*time.Hour), 0, statusGoldenNow)
	if fresh == stale || stale == veryStale || fresh == veryStale {
		t.Fatalf("staleness tiers render identically:\nfresh=%q\nstale=%q\nvery=%q", fresh, stale, veryStale)
	}
}
