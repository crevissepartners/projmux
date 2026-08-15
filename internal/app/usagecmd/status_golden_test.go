package usagecmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/usage"
	intrender "github.com/crevissepartners/projmux/internal/ui/render"
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

// statusbarWidth is the ONLY width the generated tmux statusbar config ever
// asks for — internal/app/tmux.go emits
// `#(<bin> internal status usage --max-width 120)`. Any staleness contract
// that is only honoured above this number is, in product terms, not honoured.
const statusbarWidth = 120

// statusbarGoldenSnapshots is the three-provider shape a real install renders:
// Claude (5h + weekly, age indicator opted in), Codex (5h, opted out) and
// weekly-only Antigravity (opted in). Claude is pinned at a cosmetic level-0
// age so the fixture reproduces the exact pressure that used to evict the
// marker: the segment carries one purely decorative `(3m)` plus one real
// staleness signal, and the full-age tier does not fit 120 cells.
// antigravityAge parameterizes the staleness level under test.
func statusbarGoldenSnapshots(antigravityAge time.Duration) []usage.Snapshot {
	claude := statusGoldenNow.Add(-3 * time.Minute)
	return []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 42, ResetsAt: statusGoldenNow.Add(time.Hour), UpdatedAt: claude},
		{Model: "claude", Window: usage.WindowWeekly, Pct: 18, ResetsAt: statusGoldenNow.Add(7 * 24 * time.Hour), UpdatedAt: claude},
		{Model: "codex", Window: usage.Window5h, Pct: 71, ResetsAt: statusGoldenNow.Add(time.Hour), UpdatedAt: statusGoldenNow},
		{Model: "antigravity", Window: usage.WindowWeekly, Pct: 38, ResetsAt: statusGoldenNow.Add(7 * 24 * time.Hour), UpdatedAt: statusGoldenNow.Add(-antigravityAge)},
	}
}

// TestFormatStatusUsageStalenessSurvivesStatusbarWidths is the regression guard
// for the defect this file's product-width goldens were added for: at 120 the
// full-age tier overflowed, the whole-segment tier ladder fell through to a
// tier with NO age element at all, and the `~~` on a three-day-old Antigravity
// row vanished together with the cosmetic `(3m)` on healthy Claude.
//
// The contract pinned here is width-independent: from the narrowest legacy
// text tier upward, a stale provider always carries a marker, and level 1 and
// level 2 are always distinguishable from each other and from fresh. Only the
// AGE TEXT is allowed to disappear under width pressure.
func TestFormatStatusUsageStalenessSurvivesStatusbarWidths(t *testing.T) {
	t.Parallel()

	states := []struct {
		name string
		age  time.Duration
		// marker is the exact staleness marker the Antigravity block must
		// carry at every width; "" means the block must stay marker-free.
		marker string
	}{
		{name: "fresh", age: 3 * time.Minute, marker: ""},
		{name: "stale", age: 15 * time.Minute, marker: "~"},
		{name: "very-stale", age: 72 * time.Hour, marker: "~~"},
	}
	// 80 is the narrowest terminal worth supporting, 120 is the real product
	// width, 160 is a wide terminal where the full indicator still fits.
	for _, width := range []int{80, statusbarWidth, 160} {
		seen := map[string]string{}
		for _, state := range states {
			name := fmt.Sprintf("w%d/%s", width, state.name)
			t.Run(name, func(t *testing.T) {
				out := formatStatusUsage(statusbarGoldenSnapshots(state.age), width, statusGoldenNow)
				if got := intrender.VisualLen(out); got > width {
					t.Fatalf("visualLen=%d exceeds budget %d: %q", got, width, out)
				}
				plain := intrender.StripTmuxEscapes(out)
				// The marker belongs to the Antigravity block and nothing else:
				// Claude is level 0 and Codex opted out entirely.
				_, antigravity, ok := strings.Cut(plain, "Antigravity")
				if !ok {
					t.Fatalf("Antigravity block missing at width %d: %q", width, plain)
				}
				if state.marker == "" {
					if strings.Contains(plain, "~") {
						t.Fatalf("fresh render carried a staleness marker: %q", plain)
					}
					return
				}
				if !strings.Contains(antigravity, "~") {
					t.Fatalf("Antigravity block lost its %q marker at width %d: %q", state.marker, width, plain)
				}
				// Exactly len(marker) tildes in the whole segment: Claude is
				// level 0 and Codex opted out, so the count both locates the
				// marker on the right provider and keeps `~~` from ever being
				// readable as `~`.
				if got := strings.Count(plain, "~"); got != len(state.marker) {
					t.Fatalf("tilde count = %d, want %d (marker %q) at width %d: %q", got, len(state.marker), state.marker, width, plain)
				}
			})
			seen[state.name] = formatStatusUsage(statusbarGoldenSnapshots(state.age), width, statusGoldenNow)
		}
		if seen["fresh"] == seen["stale"] || seen["stale"] == seen["very-stale"] || seen["fresh"] == seen["very-stale"] {
			t.Fatalf("width %d cannot distinguish the three staleness states: %#v", width, seen)
		}
	}
}

// TestFormatStatusUsageProductWidthGoldens freezes the exact bytes the tmux
// statusbar receives at --max-width 120 for each staleness level, including
// the color escapes. These are the goldens a reviewer reads to answer "what
// does the user actually see".
func TestFormatStatusUsageProductWidthGoldens(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		age    time.Duration
		golden string
	}{
		{name: "fresh", age: 3 * time.Minute, golden: "status-usage-w120-fresh.golden"},
		{name: "stale", age: 15 * time.Minute, golden: "status-usage-w120-stale.golden"},
		{name: "very-stale", age: 72 * time.Hour, golden: "status-usage-w120-very-stale.golden"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := formatStatusUsage(statusbarGoldenSnapshots(tc.age), statusbarWidth, statusGoldenNow)
			want, err := os.ReadFile(filepath.Join("testdata", tc.golden))
			if err != nil {
				t.Fatalf("read golden: %v", err)
			}
			if got != string(want) {
				t.Fatalf("product-width golden mismatch (%s)\n--- got ---\n%s\n--- want ---\n%s", tc.golden, got, want)
			}
		})
	}
}

// freshWidthParityWidths spans every tier boundary the ladder can select, from
// hard truncation through the unconstrained render.
var freshWidthParityWidths = []int{0, 40, 50, 60, 80, 100, 120, 160, 200}

// renderFreshWidthParity serializes the fresh-state render of both fixtures at
// every width in a stable, diffable form. The exact format is load-bearing: it
// is reproduced byte-for-byte by the generator that captured the golden from
// the pre-change tree.
func renderFreshWidthParity() string {
	var b strings.Builder
	for _, w := range freshWidthParityWidths {
		fmt.Fprintf(&b, "pair|%d\t%s\n", w, formatStatusUsage(statusGoldenSnapshots(3*time.Minute), w, statusGoldenNow))
	}
	for _, w := range freshWidthParityWidths {
		fmt.Fprintf(&b, "trio|%d\t%s\n", w, formatStatusUsage(statusbarGoldenSnapshots(3*time.Minute), w, statusGoldenNow))
	}
	return b.String()
}

// TestFormatStatusUsageFreshBytesMatchPreChangeTree is the fresh-state parity
// proof. testdata/status-usage-fresh-widths.golden was GENERATED BY THE
// PRE-CHANGE TREE (98ec3b1) — not by this code — so a match proves the
// stale-only tiers collapse exactly onto the renders they were inserted next
// to, and that a healthy install is repainted at no width.
//
// The collapse is exact by construction rather than by coincidence:
// renderHUDAgeSuffix and staleMarkerText both return "" at staleness level 0,
// so the two tiers this change added emit the same bytes as their neighbours
// whenever nothing is stale, and "first tier that fits" therefore lands on
// identical output.
func TestFormatStatusUsageFreshBytesMatchPreChangeTree(t *testing.T) {
	t.Parallel()

	got := renderFreshWidthParity()
	want, err := os.ReadFile(filepath.Join("testdata", "status-usage-fresh-widths.golden"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		gotLines := strings.Split(got, "\n")
		wantLines := strings.Split(string(want), "\n")
		for i := range gotLines {
			if i >= len(wantLines) || gotLines[i] != wantLines[i] {
				t.Fatalf("fresh-state byte parity broken at line %d\n got %q\nwant %q", i, gotLines[i], wantLines[min(i, len(wantLines)-1)])
			}
		}
		t.Fatalf("fresh-state byte parity broken (length mismatch)")
	}
}

// TestFreshStateCollapsesTheStaleOnlyTiers states the collapse directly, so a
// future edit that makes the stale-only tiers diverge from their neighbours on
// healthy data fails here with a readable message rather than only as an
// opaque golden diff.
func TestFreshStateCollapsesTheStaleOnlyTiers(t *testing.T) {
	t.Parallel()

	models := buildModelDisplays(projectStatusSnapshots(statusbarGoldenSnapshots(3 * time.Minute)))
	staleAge := renderTierLongHUDStaleAge(models, statusGoldenNow)
	compact := renderTierLongHUD(models, statusGoldenNow)
	none := renderLongHUDInternal(models, statusGoldenNow, ageModeStaleCompact)
	if staleAge != compact || compact != none {
		t.Fatalf("fresh data did not collapse the age tiers:\nstaleAge=%q\ncompact=%q\nnone=%q", staleAge, compact, none)
	}
	if strings.Contains(compact, "~") || strings.Contains(compact, "(") {
		t.Fatalf("fresh collapse leaked an age element: %q", compact)
	}
}
