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

// statusbarUsageBudgets are the cell budgets the generated tmux statusbar can
// hand this renderer. There is no single product width any more:
// internal/app/tmux.go emits `--max-width #{e|-:#{client_width},<notify>}`, so
// the budget is whatever `status-format[0]` has left after the notify segment's
// reservation. A contract honoured at only one of these numbers is, in product
// terms, not honoured.
//
// Each entry is the budget an ordinary terminal produces:
//
//	client 80  -> 40    the narrowest terminal worth supporting
//	client 120 -> 60
//	client 160 -> 80
//	client 200 -> 120   the cell count the segment used to be hardcoded to
//	client 240 -> 160
var statusbarUsageBudgets = []int{40, 60, 80, 120, 160}

// statusbarLadderUsageBudgets drops the two narrowest entries, because the
// width-parameterized staleness table below locates the marker by the
// `Antigravity` label and the ladder switches to single-letter labels under
// 70-odd cells. The narrow budgets are not skipped, they are covered by
// TestStalenessAtTheNarrowestClientBudgets and frozen in the golden, so what an
// 80- or 120-column client sees stays visible to a reviewer.
var statusbarLadderUsageBudgets = statusbarUsageBudgets[2:]

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
	for _, width := range statusbarLadderUsageBudgets {
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

// statusbarInstalledShapeSnapshots is the provider shape an actually installed
// projmux renders on the machine this Phase was measured on: Claude with both
// official windows, Codex with weekly only, and a long-stale weekly-only
// Antigravity. It is the fixture that reproduces the defect this Phase fixes —
// its second-bar tier needs more than 120 cells, so the hardcoded 120 budget
// dropped Claude's weekly bar on a terminal with room to spare.
func statusbarInstalledShapeSnapshots(antigravityAge time.Duration) []usage.Snapshot {
	claude := statusGoldenNow.Add(-3 * time.Minute)
	return []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 42, ResetsAt: statusGoldenNow.Add(time.Hour), UpdatedAt: claude},
		{Model: "claude", Window: usage.WindowWeekly, Pct: 18, ResetsAt: statusGoldenNow.Add(7 * 24 * time.Hour), UpdatedAt: claude},
		{Model: "codex", Window: usage.WindowWeekly, Pct: 20, ResetsAt: statusGoldenNow.Add(7 * 24 * time.Hour), UpdatedAt: statusGoldenNow},
		{Model: "antigravity", Window: usage.WindowWeekly, Pct: 38, ResetsAt: statusGoldenNow.Add(7 * 24 * time.Hour), UpdatedAt: statusGoldenNow.Add(-antigravityAge)},
	}
}

// statusGoldenStates is the staleness ladder every width-parameterized golden
// walks: level 0, level 1 and level 2 on the Antigravity row.
var statusGoldenStates = []struct {
	name string
	age  time.Duration
}{
	{name: "fresh", age: 3 * time.Minute},
	{name: "stale", age: 15 * time.Minute},
	{name: "very-stale", age: 72 * time.Hour},
}

// renderUsageBudgetTable serializes both statusbar fixtures at every budget the
// generated config can now hand this renderer, across the whole staleness
// ladder, in a stable diffable form. It replaces the former trio of
// `status-usage-w120-*.golden` files: the budget is no longer one number, so
// freezing one number's bytes no longer describes what users see.
func renderUsageBudgetTable() string {
	var b strings.Builder
	for _, fixture := range []struct {
		name  string
		build func(time.Duration) []usage.Snapshot
	}{
		{name: "trio", build: statusbarGoldenSnapshots},
		{name: "installed", build: statusbarInstalledShapeSnapshots},
	} {
		for _, width := range statusbarUsageBudgets {
			for _, state := range statusGoldenStates {
				fmt.Fprintf(&b, "%s|%d|%s\t%s\n", fixture.name, width, state.name,
					formatStatusUsage(fixture.build(state.age), width, statusGoldenNow))
			}
		}
	}
	return b.String()
}

// TestFormatStatusUsageBudgetGoldens freezes the exact bytes the tmux statusbar
// receives at each budget the generated config can hand it, for each staleness
// level, including the color escapes. This is the golden a reviewer reads to
// answer "what does the user actually see" — now once per budget, because the
// budget follows the client instead of being a constant.
func TestFormatStatusUsageBudgetGoldens(t *testing.T) {
	t.Parallel()

	got := renderUsageBudgetTable()
	want, err := os.ReadFile(filepath.Join("testdata", "status-usage-budgets.golden"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		gotLines := strings.Split(got, "\n")
		wantLines := strings.Split(string(want), "\n")
		for i := range gotLines {
			if i >= len(wantLines) || gotLines[i] != wantLines[i] {
				t.Fatalf("usage-budget golden mismatch at line %d\n got %q\nwant %q", i, gotLines[i], wantLines[min(i, len(wantLines)-1)])
			}
		}
		t.Fatalf("usage-budget golden mismatch (length)")
	}
}

// claudeWeeklyBarPresent reports whether the rendered segment shows Claude's
// official weekly window as a BAR. The narrow text tiers also spell the word
// `weekly`, so the check is anchored on the bar's opening bracket inside the
// Claude block rather than on the word alone.
func claudeWeeklyBarPresent(out string) bool {
	plain := intrender.StripTmuxEscapes(out)
	_, claude, ok := strings.Cut(plain, "Claude")
	if !ok {
		return false
	}
	if next := strings.Index(claude, "   "); next >= 0 {
		claude = claude[:next]
	}
	return strings.Contains(claude, "weekly [")
}

// installedShapeWeeklyBarBudget is the smallest budget at which the installed
// shape's second-bar tier fits. It is a property of the fixture's data, not a
// tunable: the tier renders 124 cells, so 124 is where Claude's weekly bar
// comes back.
//
// With `status-format[0]` split as `usage = client_width - notify`, that budget
// arrives on a 204-column client. It is ABOVE the widths the ladder can serve
// on a 140- or 191-column terminal, which is the honest limit of a Phase that
// only re-derives the budget: making a 140-column client show the weekly bar
// needs per-provider tier selection, not more cells.
const installedShapeWeeklyBarBudget = 124

// TestAWideEnoughClientRecoversTheClaudeWeeklyBar is the product acceptance
// this Phase exists for. The old hardcoded 120-cell cap was the ceiling on
// every terminal however wide, so the installed shape could never reach its
// second-bar tier. A budget that tracks the row does reach it.
//
// The staleness marker is asserted at every one of those budgets too: buying
// the weekly bar back must not cost the signal PR #620 established.
func TestAWideEnoughClientRecoversTheClaudeWeeklyBar(t *testing.T) {
	t.Parallel()

	const oldHardcodedBudget = 120
	narrow := formatStatusUsage(statusbarInstalledShapeSnapshots(72*time.Hour), oldHardcodedBudget, statusGoldenNow)
	if claudeWeeklyBarPresent(narrow) {
		t.Fatalf("fixture no longer reproduces the defect: %q renders a Claude weekly bar at %d cells",
			intrender.StripTmuxEscapes(narrow), oldHardcodedBudget)
	}
	if got := formatStatusUsage(statusbarInstalledShapeSnapshots(72*time.Hour), installedShapeWeeklyBarBudget-1, statusGoldenNow); claudeWeeklyBarPresent(got) {
		t.Fatalf("the weekly-bar tier fits below %d cells; the documented threshold has drifted",
			installedShapeWeeklyBarBudget)
	}
	for _, budget := range []int{installedShapeWeeklyBarBudget, 160, 320} {
		for _, state := range statusGoldenStates {
			out := formatStatusUsage(statusbarInstalledShapeSnapshots(state.age), budget, statusGoldenNow)
			if got := intrender.VisualLen(out); got > budget {
				t.Fatalf("w%d/%s: visualLen=%d exceeds the budget: %q", budget, state.name, got, out)
			}
			if !claudeWeeklyBarPresent(out) {
				t.Fatalf("w%d/%s: Claude weekly bar missing on a row with space for it: %q",
					budget, state.name, intrender.StripTmuxEscapes(out))
			}
			if state.name == "very-stale" && !strings.Contains(intrender.StripTmuxEscapes(out), "~~") {
				t.Fatalf("w%d/%s: staleness marker lost: %q", budget, state.name, intrender.StripTmuxEscapes(out))
			}
		}
	}
}

// TestStalenessAtTheNarrowestClientBudgets pins what the two narrow budgets in
// statusbarUsageBudgets actually render, because the width-parameterized table
// above cannot reach them.
//
// At 60 cells (a 120-column client) the ladder is on its single-letter tier and
// the marker is intact and level-distinguishable. At 40 (an 80-column client)
// the ladder is in hard rune-truncation, and a `~~` can be cut down to a single
// `~` — level 2 stops being distinguishable from level 1. That is a limit of
// the tier ladder, not of the budget: no budget an 80-column row can offer
// fixes it, and per-provider tier selection is what would. Pinning it here
// keeps the limitation visible instead of latent.
func TestStalenessAtTheNarrowestClientBudgets(t *testing.T) {
	t.Parallel()

	plain := func(budget int, age time.Duration) string {
		return intrender.StripTmuxEscapes(formatStatusUsage(statusbarInstalledShapeSnapshots(age), budget, statusGoldenNow))
	}
	for _, budget := range statusbarUsageBudgets[:2] {
		for _, state := range statusGoldenStates {
			out := plain(budget, state.age)
			if got := intrender.VisualLen(out); got > budget {
				t.Fatalf("w%d/%s: visualLen=%d exceeds the budget: %q", budget, state.name, got, out)
			}
			if state.name == "fresh" {
				if strings.Contains(out, "~") {
					t.Fatalf("w%d: fresh render carried a staleness marker: %q", budget, out)
				}
				continue
			}
			if !strings.Contains(out, "~") {
				t.Fatalf("w%d/%s: staleness marker lost entirely: %q", budget, state.name, out)
			}
		}
	}
	// 60 cells still distinguishes all three levels.
	if plain(60, 15*time.Minute) == plain(60, 72*time.Hour) {
		t.Fatalf("a 60-cell budget can no longer distinguish stale from very stale: %q", plain(60, 72*time.Hour))
	}
	// 40 cells does not, and the golden shows exactly how. Asserting the
	// collapse rather than ignoring it means a later fix fails here loudly and
	// is upgraded to a real assertion instead of slipping by.
	if plain(40, 15*time.Minute) != plain(40, 72*time.Hour) {
		t.Fatalf("a 40-cell budget now distinguishes stale from very stale; upgrade this test to require it:\n stale=%q\n very=%q",
			plain(40, 15*time.Minute), plain(40, 72*time.Hour))
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
