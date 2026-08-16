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
// Each entry is the budget an ordinary terminal produces. The mapping is the
// one measured against a real attached client on tmux 3.6, after the corrective
// that stopped reserving notify's design cap unconditionally:
//
//	client 60  -> 40    a pane-sized row
//	client 80  -> 60    the narrowest terminal worth supporting
//	client 120 -> 100
//	client 140 -> 120   the cell count the segment used to be hardcoded to
//	client 160 -> 140   \ the plateau: notify grows on the surplus here, so
//	client 191 -> 140   / the reported terminal and a 200-column one match
//	client 240 -> 160
var statusbarUsageBudgets = []int{40, 60, 100, 120, 140, 160}

// statusbarLadderUsageBudgets drops the two narrowest entries, because the
// width-parameterized staleness table below locates the marker by the
// `Antigravity` label and the ladder switches to single-letter labels under
// 70-odd cells. The narrow budgets are not skipped, they are covered by
// TestStalenessAtTheNarrowestClientBudgets and frozen in the golden, so what a
// 60- or 80-column client sees stays visible to a reviewer.
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
		{Model: "antigravity", Window: usage.WindowQuota, Bucket: "gemini-weekly", Pct: 38, ResetsAt: statusGoldenNow.Add(7 * 24 * time.Hour), UpdatedAt: statusGoldenNow.Add(-antigravityAge)},
	}
}

// TestFormatStatusUsageStalenessSurvivesStatusbarWidths is the regression guard
// for the defect this file's product-width goldens were added for: at 120 the
// full-age render overflowed, the whole-segment tier ladder that then selected
// the output fell through to a tier with NO age element at all, and the `~~` on
// a three-day-old Antigravity row vanished together with the cosmetic `(3m)` on
// healthy Claude.
//
// The contract pinned here is width-independent: from the narrowest legacy
// text form upward, a stale provider always carries a marker, and level 1 and
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
		{Model: "antigravity", Window: usage.WindowQuota, Bucket: "gemini-weekly", Pct: 38, ResetsAt: statusGoldenNow.Add(7 * 24 * time.Hour), UpdatedAt: statusGoldenNow.Add(-antigravityAge)},
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
// With `status-format[0]` reserving notify's FLOOR instead of its design cap,
// that budget arrives on a 144-column client, and the 191-column terminal the
// defect was reported from gets 140 cells — past this threshold and past the
// 134 the full HUD tier needs. Under PR #624 the same client got 111 and the
// bar stayed missing; that inversion is what this file's threshold now guards.
//
// A 140-column client still lands four cells short at 120. That is the honest
// remaining limit of a change that only re-derives the budget: making a
// 140-column row show the weekly bar needs per-provider tier selection, not
// more cells.
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
// At 60 cells (an 80-column client) every step of usageShedOrder has been
// spent and the marker is still intact and level-distinguishable. At 40 (a
// 60-column client) the segment is in hard rune-truncation, below the drop
// order entirely, and a `~~` can be cut down to a single `~` — level 2 stops
// being distinguishable from level 1. That is a limit of truncation, not of
// the budget and not of the drop order: no budget an 80-column row can offer
// fixes it. Pinning it here keeps the limitation visible instead of latent.
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
// PRE-CHANGE TREE (98ec3b1) — not by this code — so a match proves that a
// healthy install is repainted at no width, across the staleness work that
// captured it AND across the move to element-priority degradation.
//
// The collapse is exact by construction rather than by coincidence:
// renderHUDAgeSuffix and staleMarkerText both return "" at staleness level 0,
// so shedding an age element on healthy data emits the same bytes as not
// shedding it, and the first plan that fits therefore lands on identical
// output.
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

// ---------------------------------------------------------------------------
// Element-priority degradation (per-provider) — fixtures, width sweep, wide
// parity. See usageShedOrder in usage.go for the drop order these pin.
// ---------------------------------------------------------------------------

// usageSweepFixture is one provider shape the element-priority sweeps walk.
type usageSweepFixture struct {
	name  string
	snaps []usage.Snapshot
	// ageProviders are the labels that render an age element in this fixture.
	ageProviders []string
}

// usageSweepFixtures are the three shapes that, between them, put more than one
// provider on every per-provider rule in usageShedOrder. A fixture where only
// ONE provider carries a given optional element cannot tell whole-segment tier
// selection apart from element-priority degradation, which is why `installed`
// alone is not enough.
//
//   - installed:  the shape a real install renders — Claude 5h+weekly with a
//     cosmetic `(3m)`, weekly-only Codex, long-stale weekly-only Antigravity.
//     One cosmetic age, one stale age, one secondary window.
//   - dual-window: Claude and Codex both carry 5h AND weekly, so the secondary
//     window rule has TWO eligible providers; Claude and Antigravity are both
//     at a cosmetic level-0 age, so the cosmetic rule has two as well.
//   - mixed-staleness: dual windows on Claude and Codex, a cosmetic age on
//     Claude and a level-2 age on Antigravity, i.e. every rule in the order is
//     eligible at once.
func usageSweepFixtures() []usageSweepFixture {
	cosmetic := statusGoldenNow.Add(-3 * time.Minute)
	cosmetic2 := statusGoldenNow.Add(-6 * time.Minute)
	veryStale := statusGoldenNow.Add(-72 * time.Hour)
	return []usageSweepFixture{
		{
			name:         "installed",
			snaps:        statusbarInstalledShapeSnapshots(72 * time.Hour),
			ageProviders: []string{"Claude", "Antigravity"},
		},
		{
			name: "dual-window",
			snaps: []usage.Snapshot{
				{Model: "claude", Window: usage.Window5h, Pct: 42, ResetsAt: statusGoldenNow.Add(time.Hour), UpdatedAt: cosmetic},
				{Model: "claude", Window: usage.WindowWeekly, Pct: 18, ResetsAt: statusGoldenNow.Add(7 * 24 * time.Hour), UpdatedAt: cosmetic},
				{Model: "codex", Window: usage.Window5h, Pct: 71, ResetsAt: statusGoldenNow.Add(time.Hour), UpdatedAt: statusGoldenNow},
				{Model: "codex", Window: usage.WindowWeekly, Pct: 55, ResetsAt: statusGoldenNow.Add(7 * 24 * time.Hour), UpdatedAt: statusGoldenNow},
				{Model: "antigravity", Window: usage.WindowQuota, Bucket: "gemini-weekly", Pct: 38, ResetsAt: statusGoldenNow.Add(7 * 24 * time.Hour), UpdatedAt: cosmetic2},
			},
			ageProviders: []string{"Claude", "Antigravity"},
		},
		{
			name: "mixed-staleness",
			snaps: []usage.Snapshot{
				{Model: "claude", Window: usage.Window5h, Pct: 42, ResetsAt: statusGoldenNow.Add(time.Hour), UpdatedAt: cosmetic},
				{Model: "claude", Window: usage.WindowWeekly, Pct: 18, ResetsAt: statusGoldenNow.Add(7 * 24 * time.Hour), UpdatedAt: cosmetic},
				{Model: "codex", Window: usage.Window5h, Pct: 71, ResetsAt: statusGoldenNow.Add(time.Hour), UpdatedAt: statusGoldenNow},
				{Model: "codex", Window: usage.WindowWeekly, Pct: 55, ResetsAt: statusGoldenNow.Add(7 * 24 * time.Hour), UpdatedAt: statusGoldenNow},
				{Model: "antigravity", Window: usage.WindowQuota, Bucket: "gemini-weekly", Pct: 38, ResetsAt: statusGoldenNow.Add(7 * 24 * time.Hour), UpdatedAt: veryStale},
			},
			ageProviders: []string{"Claude", "Antigravity"},
		},
	}
}

// usageWideParityWidths are widths at which NOTHING is shed: the richest render
// of every sweep fixture fits. `0` is the unbounded render.
var usageWideParityWidths = []int{0, 200, 240, 320}

// renderUsageWideParity serializes every sweep fixture at every wide width in a
// stable, diffable form. The exact format is load-bearing: the golden was
// captured byte-for-byte from the pre-change tree by this same function.
func renderUsageWideParity() string {
	var b strings.Builder
	for _, fixture := range usageSweepFixtures() {
		for _, w := range usageWideParityWidths {
			fmt.Fprintf(&b, "%s|%d\t%s\n", fixture.name, w, formatStatusUsage(fixture.snaps, w, statusGoldenNow))
		}
	}
	return b.String()
}

// TestUsageWideWidthBytesMatchPreChangeTree is acceptance criterion 4 of the
// per-provider degradation Phase: a row wide enough to shed nothing renders
// byte-identically to what whole-segment tier selection produced.
//
// testdata/status-usage-wide-prechange.golden was GENERATED BY THE PRE-CHANGE
// TREE (main 6dfed76, the whole-segment "first tier that fits" ladder) — not by
// the element-priority code — so a match is a parity proof and not a
// self-consistency check.
func TestUsageWideWidthBytesMatchPreChangeTree(t *testing.T) {
	t.Parallel()

	got := renderUsageWideParity()
	want, err := os.ReadFile(filepath.Join("testdata", "status-usage-wide-prechange.golden"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		gotLines := strings.Split(got, "\n")
		wantLines := strings.Split(string(want), "\n")
		for i := range gotLines {
			if i >= len(wantLines) || gotLines[i] != wantLines[i] {
				t.Fatalf("wide-width byte parity broken at line %d\n got %q\nwant %q", i, gotLines[i], wantLines[min(i, len(wantLines)-1)])
			}
		}
		t.Fatalf("wide-width byte parity broken (length mismatch)")
	}
}

// TestFreshStateCollapsesTheAgeShedSteps states the collapse directly, so a
// future edit that makes shedding an age element visible on healthy data fails
// here with a readable message rather than only as an opaque golden diff.
//
// On fresh data both age rules produce the same bytes as no age element at
// all, which is why a healthy install is repainted at no width no matter how
// far down usageShedOrder the row has to walk.
func TestFreshStateCollapsesTheAgeShedSteps(t *testing.T) {
	t.Parallel()

	models := buildModelDisplays(projectStatusSnapshots(statusbarGoldenSnapshots(3 * time.Minute)))
	full := renderUsageSegment(models, statusGoldenNow, newUsageSegmentPlan(models))
	compact := renderUsageSegment(models, statusGoldenNow, usagePlanCompactAge(models))
	if strings.Contains(compact, "~") || strings.Contains(compact, "(") {
		t.Fatalf("fresh collapse leaked an age element: %q", compact)
	}
	if !strings.Contains(intrender.StripTmuxEscapes(full), "Claude (3m)") {
		t.Fatalf("fixture no longer carries the cosmetic age: %q", intrender.StripTmuxEscapes(full))
	}
	// Walking usageShedOrder's age rules to their end reaches exactly the
	// age-free bytes: the age elements are the only difference between the
	// richest render and the compact one on healthy data.
	plan := newUsageSegmentPlan(models)
	applied := 0
	for _, step := range usageShedSteps(models, statusGoldenNow) {
		if !strings.Contains(usageShedOrder[step.rule].name, "age text") {
			break
		}
		step.apply(&plan)
		applied++
	}
	if applied == 0 {
		t.Fatalf("fixture produced no age shed steps at all")
	}
	if got := renderUsageSegment(models, statusGoldenNow, plan); got != compact {
		t.Fatalf("walking the %d age shed steps did not reach the age-free bytes:\n got %q\nwant %q", applied, got, compact)
	}
}
