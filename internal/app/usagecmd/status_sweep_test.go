package usagecmd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	intrender "github.com/crevissepartners/projmux/internal/ui/render"
)

// status_sweep_test.go pins ELEMENT-PRIORITY degradation: what the usage
// segment gives up first when the row is too narrow. The drop order itself
// lives in exactly one place, usageShedOrder in usage.go; this file asserts
// that the order the product delivers at real widths is that order and no
// other, and that docs/statusbar.md spells the same list.

// usageProviderLabels are the canonical providers and their two label forms,
// used to locate a provider's block in a rendered segment.
var usageProviderLabels = []struct{ long, short string }{
	{long: "Claude", short: "C"},
	{long: "Codex", short: "X"},
	{long: "Antigravity", short: "A"},
}

// observeUsageElements reports the elements a rendered segment ACTUALLY shows,
// parsed back out of the bytes tmux receives. It deliberately reads the output
// rather than the plan: a test that asked the planner what it planned could not
// catch a renderer that ignores the plan.
//
// Tokens, per provider block, in canonical provider order:
//
//	<P>/label        long label rendered
//	<P>/short        single-letter label rendered
//	<P>/age          the `(3m)` / `(3d~~)` age TEXT rendered
//	<P>/mark=~       the staleness marker, exactly as rendered
//	<P>/5h-bar       `5h [████░░░░░░] 42%`
//	<P>/5h-text      `5h:42%`
//	<P>/weekly-bar   `weekly [████░░░░░░] 38%`
//	<P>/weekly-text  `weekly:38%`
//
// plus a leading `truncated` token when the segment was hard rune-truncated.
func observeUsageElements(out string) []string {
	plain := intrender.StripTmuxEscapes(out)
	tokens := []string{}
	if strings.HasSuffix(plain, "…") {
		tokens = append(tokens, "truncated")
	}
	for block := range strings.SplitSeq(plain, statusModelSeparator) {
		provider, rest, long, ok := cutUsageProviderLabel(block)
		if !ok {
			continue
		}
		if long {
			tokens = append(tokens, provider+"/label")
		} else {
			tokens = append(tokens, provider+"/short")
		}
		// The marker lives in two places depending on how far the segment has
		// shed: glued to the label (`Antigravity~~`) once the age text is gone,
		// and inside the age text (`(3d~~)`) while it is still there. Counting
		// tildes in the block sees it either way, which is the point — the
		// token records that the user can still see the signal.
		if marker := strings.Repeat("~", strings.Count(rest, "~")); marker != "" {
			tokens = append(tokens, provider+"/mark="+marker)
		}
		if strings.Contains(rest, "(") {
			tokens = append(tokens, provider+"/age")
		}
		for _, window := range []string{"5h", "weekly"} {
			switch {
			case strings.Contains(rest, window+" ["):
				tokens = append(tokens, provider+"/"+window+"-bar")
			case strings.Contains(rest, window+":"):
				tokens = append(tokens, provider+"/"+window+"-text")
			}
		}
	}
	return tokens
}

// cutUsageProviderLabel identifies which provider a rendered block belongs to
// and reports whether the long label form was used. Long labels are matched
// first so `Codex` is never read as short-form `C`.
func cutUsageProviderLabel(block string) (provider, rest string, long, ok bool) {
	for _, p := range usageProviderLabels {
		if r, hit := strings.CutPrefix(block, p.long); hit {
			return p.long, r, true, true
		}
	}
	for _, p := range usageProviderLabels {
		if r, hit := strings.CutPrefix(block, p.short); hit {
			return p.long, r, false, true
		}
	}
	return "", "", false, false
}

// usageSweepWidths is the width sweep required of this Phase. 40 and 60 are
// what a 60- and 80-column client leave the segment; 80/100/120 are what
// ordinary laptop rows leave it; 160 and 200 are wide rows where nothing is
// shed at all, i.e. the widths acceptance criterion 4 is about.
var usageSweepWidths = []int{40, 60, 80, 100, 120, 160, 200}

// TestUsageElementPriorityWidthSweep pins the exact element set of every sweep
// fixture at every sweep width. A change to usageShedOrder — or to any renderer
// that quietly stops honouring it — moves one of these rows.
func TestUsageElementPriorityWidthSweep(t *testing.T) {
	t.Parallel()

	want := map[string]map[int][]string{
		// The shape a real install renders: one cosmetic age (Claude), one
		// stale age (Antigravity), one secondary window (Claude weekly). With
		// a single provider eligible for each per-provider rule this fixture
		// renders identically to what whole-segment selection produced — which
		// is the point: the real install shape does not regress.
		"installed": {
			// 40 cells: hard truncation, and the known limit it exposes —
			// `~~` is cut to `~`, so level 2 stops being distinguishable.
			40: {"truncated", "Claude/short", "Claude/5h-text", "Claude/weekly-text", "Codex/short", "Codex/weekly-text", "Antigravity/short", "Antigravity/mark=~"},
			// 51 cells: rules 1-5 all spent; short labels, text pairs.
			60: {"Claude/short", "Claude/5h-text", "Claude/weekly-text", "Codex/short", "Codex/weekly-text", "Antigravity/short", "Antigravity/mark=~~", "Antigravity/weekly-text"},
			// 70 cells: long labels back (rule 5 unspent).
			80: {"Claude/label", "Claude/5h-text", "Claude/weekly-text", "Codex/label", "Codex/weekly-text", "Antigravity/label", "Antigravity/mark=~~", "Antigravity/weekly-text"},
			// 98 cells: bars back (rule 4 unspent), Claude's second window not.
			100: {"Claude/label", "Claude/5h-bar", "Codex/label", "Codex/weekly-bar", "Antigravity/label", "Antigravity/mark=~~", "Antigravity/weekly-bar"},
			// 98 again: Claude's second bar needs 124, which 120 cannot buy.
			120: {"Claude/label", "Claude/5h-bar", "Codex/label", "Codex/weekly-bar", "Antigravity/label", "Antigravity/mark=~~", "Antigravity/weekly-bar"},
			// 134 cells: nothing shed, both age texts present.
			160: {"Claude/label", "Claude/age", "Claude/5h-bar", "Claude/weekly-bar", "Codex/label", "Codex/weekly-bar", "Antigravity/label", "Antigravity/mark=~~", "Antigravity/age", "Antigravity/weekly-bar"},
			200: {"Claude/label", "Claude/age", "Claude/5h-bar", "Claude/weekly-bar", "Codex/label", "Codex/weekly-bar", "Antigravity/label", "Antigravity/mark=~~", "Antigravity/age", "Antigravity/weekly-bar"},
		},
		// Two providers carry a secondary window and two carry a cosmetic age,
		// so this fixture is the one that can tell per-provider shedding apart
		// from whole-segment tier selection.
		"dual-window": {
			40: {"truncated", "Claude/short", "Claude/5h-text", "Claude/weekly-text", "Codex/short", "Codex/5h-text", "Codex/weekly-text"},
			60: {"Claude/short", "Claude/5h-text", "Claude/weekly-text", "Codex/short", "Codex/5h-text", "Codex/weekly-text", "Antigravity/short", "Antigravity/weekly-text"},
			80: {"Claude/label", "Claude/5h-text", "Claude/weekly-text", "Codex/label", "Codex/5h-text", "Codex/weekly-text", "Antigravity/label", "Antigravity/weekly-text"},
			// 92 cells: rule 3 spent on both eligible providers.
			100: {"Claude/label", "Claude/5h-bar", "Codex/label", "Codex/5h-bar", "Antigravity/label", "Antigravity/weekly-bar"},
			// 118 cells — THE row this Phase exists for. Rule 3 ran tail-first
			// and stopped: Codex's SECOND window went, Claude's stayed. Whole-
			// segment selection dropped both and drew 92 cells into a 120-cell
			// row.
			120: {"Claude/label", "Claude/5h-bar", "Claude/weekly-bar", "Codex/label", "Codex/5h-bar", "Antigravity/label", "Antigravity/weekly-bar"},
			// 154 cells: nothing shed.
			160: {"Claude/label", "Claude/age", "Claude/5h-bar", "Claude/weekly-bar", "Codex/label", "Codex/5h-bar", "Codex/weekly-bar", "Antigravity/label", "Antigravity/age", "Antigravity/weekly-bar"},
			200: {"Claude/label", "Claude/age", "Claude/5h-bar", "Claude/weekly-bar", "Codex/label", "Codex/5h-bar", "Codex/weekly-bar", "Antigravity/label", "Antigravity/age", "Antigravity/weekly-bar"},
		},
		// Every rule in usageShedOrder is eligible at once: a cosmetic age on
		// Claude, a level-2 age on Antigravity, two secondary windows.
		"mixed-staleness": {
			40:  {"truncated", "Claude/short", "Claude/5h-text", "Claude/weekly-text", "Codex/short", "Codex/5h-text", "Codex/weekly-text"},
			60:  {"Claude/short", "Claude/5h-text", "Claude/weekly-text", "Codex/short", "Codex/5h-text", "Codex/weekly-text", "Antigravity/short", "Antigravity/mark=~~", "Antigravity/weekly-text"},
			80:  {"Claude/label", "Claude/5h-text", "Claude/weekly-text", "Codex/label", "Codex/5h-text", "Codex/weekly-text", "Antigravity/label", "Antigravity/mark=~~", "Antigravity/weekly-text"},
			100: {"Claude/label", "Claude/5h-bar", "Codex/label", "Codex/5h-bar", "Antigravity/label", "Antigravity/mark=~~", "Antigravity/weekly-bar"},
			// 120 cells exactly: Codex's second window paid for Claude's, and
			// the `~~` marker is untouched on the way down.
			120: {"Claude/label", "Claude/5h-bar", "Claude/weekly-bar", "Codex/label", "Codex/5h-bar", "Antigravity/label", "Antigravity/mark=~~", "Antigravity/weekly-bar"},
			// 156 cells: nothing shed.
			160: {"Claude/label", "Claude/age", "Claude/5h-bar", "Claude/weekly-bar", "Codex/label", "Codex/5h-bar", "Codex/weekly-bar", "Antigravity/label", "Antigravity/mark=~~", "Antigravity/age", "Antigravity/weekly-bar"},
			200: {"Claude/label", "Claude/age", "Claude/5h-bar", "Claude/weekly-bar", "Codex/label", "Codex/5h-bar", "Codex/weekly-bar", "Antigravity/label", "Antigravity/mark=~~", "Antigravity/age", "Antigravity/weekly-bar"},
		},
	}

	for _, fixture := range usageSweepFixtures() {
		expected, ok := want[fixture.name]
		if !ok {
			t.Fatalf("fixture %q has no pinned element table", fixture.name)
		}
		for _, width := range usageSweepWidths {
			t.Run(fmt.Sprintf("%s/w%d", fixture.name, width), func(t *testing.T) {
				out := formatStatusUsage(fixture.snaps, width, statusGoldenNow)
				if got := intrender.VisualLen(out); got > width {
					t.Fatalf("visualLen=%d exceeds the budget %d: %q", got, width, intrender.StripTmuxEscapes(out))
				}
				got := observeUsageElements(out)
				if strings.Join(got, " ") != strings.Join(expected[width], " ") {
					t.Fatalf("element set drifted at width %d (%d cells rendered)\n got %v\nwant %v\nrender %q",
						width, intrender.VisualLen(out), got, expected[width], intrender.StripTmuxEscapes(out))
				}
			})
		}
	}
}

// usageElementLossWidth returns the widest width at or below limit whose render
// does NOT contain token. -1 means the token is present at every width, i.e.
// the element is never shed.
func usageElementLossWidth(fixture usageSweepFixture, token string, limit int) int {
	for w := limit; w >= 1; w-- {
		out := formatStatusUsage(fixture.snaps, w, statusGoldenNow)
		if !slices.Contains(observeUsageElements(out), token) {
			return w
		}
	}
	return -1
}

// officialWindowToken names the bar a provider must keep longest: 5h when the
// provider reports one, otherwise weekly.
func officialWindowTokens(fixture usageSweepFixture) []string {
	models := buildModelDisplays(projectStatusSnapshots(fixture.snaps))
	tokens := make([]string, 0, len(models))
	for _, m := range models {
		window := "weekly"
		if m.hasFive {
			window = "5h"
		}
		tokens = append(tokens, m.label+"/"+window+"-bar")
	}
	return tokens
}

// TestAgeIndicatorsAreShedBeforeAnyOfficialWindowBar is acceptance criterion 1
// of this Phase: at a narrow width, when one provider's age indicator
// disappears, every other provider's official window bar still renders.
//
// It is asserted as a width relation rather than at a width chosen by hand:
// the widest row that has already lost an age element must still be wider than
// the widest row that has lost an official window bar, for every provider pair
// in every fixture. Nothing about the assertion depends on the author picking a
// number — the widths come out of the renderer.
func TestAgeIndicatorsAreShedBeforeAnyOfficialWindowBar(t *testing.T) {
	t.Parallel()

	const sweepLimit = 300
	for _, fixture := range usageSweepFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			officials := officialWindowTokens(fixture)
			barLoss := -1
			for _, token := range officials {
				if w := usageElementLossWidth(fixture, token, sweepLimit); w > barLoss {
					barLoss = w
				}
			}
			if barLoss < 0 {
				t.Fatalf("no official window bar is ever shed in %s; the fixture cannot prove the ordering", fixture.name)
			}
			for _, provider := range fixture.ageProviders {
				token := provider + "/age"
				ageLoss := usageElementLossWidth(fixture, token, sweepLimit)
				if ageLoss < 0 {
					t.Fatalf("%s never sheds its age element; the fixture cannot prove the ordering", provider)
				}
				if ageLoss <= barLoss {
					t.Fatalf("%s's age indicator survives to width %d but an official window bar is already gone at %d: ages must be shed first",
						provider, ageLoss, barLoss)
				}
				// The literal wording of the acceptance criterion: at the
				// widest row where this provider's age is gone, every
				// provider's official window bar is still drawn.
				out := formatStatusUsage(fixture.snaps, ageLoss, statusGoldenNow)
				got := observeUsageElements(out)
				for _, official := range officials {
					if !slices.Contains(got, official) {
						t.Fatalf("at width %d %s's age indicator is gone AND %s is missing: %q",
							ageLoss, provider, official, intrender.StripTmuxEscapes(out))
					}
				}
			}
		})
	}
}

// TestStalenessMarkerOutlivesEveryOfficialWindowBar is acceptance criterion 3:
// the `~` / `~~` marker never disappears before an official window bar. The
// marker has no entry in usageShedOrder at all, so the only thing that can
// reach it is hard rune-truncation — which happens far below the width where
// the last bar is already gone.
func TestStalenessMarkerOutlivesEveryOfficialWindowBar(t *testing.T) {
	t.Parallel()

	const sweepLimit = 300
	for _, fixture := range usageSweepFixtures() {
		markers := map[string]string{}
		for _, m := range buildModelDisplays(projectStatusSnapshots(fixture.snaps)) {
			if marker := staleMarkerText(modelStaleLevel(m, statusGoldenNow)); marker != "" {
				markers[m.label] = marker
			}
		}
		if len(markers) == 0 {
			continue
		}
		t.Run(fixture.name, func(t *testing.T) {
			barLoss := -1
			for _, token := range officialWindowTokens(fixture) {
				if w := usageElementLossWidth(fixture, token, sweepLimit); w > barLoss {
					barLoss = w
				}
			}
			for label, marker := range markers {
				token := label + "/mark=" + marker
				markerLoss := usageElementLossWidth(fixture, token, sweepLimit)
				if markerLoss >= barLoss {
					t.Fatalf("%s's %q marker is already gone at width %d while an official window bar survives down to %d",
						label, marker, markerLoss, barLoss)
				}
			}
		})
	}
}

// TestEveryShedStepKeepsLabelsMarkersAndOfficialWindows walks the whole shed
// sequence directly, so the guarantee is proven over EVERY reachable degraded
// state rather than only over the widths a sweep happens to visit. No step in
// usageShedOrder may remove a provider, its official window, or its marker.
func TestEveryShedStepKeepsLabelsMarkersAndOfficialWindows(t *testing.T) {
	t.Parallel()

	for _, fixture := range usageSweepFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			models := buildModelDisplays(projectStatusSnapshots(fixture.snaps))
			steps := usageShedSteps(models, statusGoldenNow)
			if len(steps) == 0 {
				t.Fatalf("fixture produced no shed steps")
			}
			plan := newUsageSegmentPlan(models)
			previous := intrender.VisualLen(renderUsageSegment(models, statusGoldenNow, plan))
			for i, step := range steps {
				step.apply(&plan)
				out := renderUsageSegment(models, statusGoldenNow, plan)
				plain := intrender.StripTmuxEscapes(out)
				width := intrender.VisualLen(out)
				if width >= previous {
					t.Fatalf("step %d (%s) did not narrow the segment: %d -> %d", i, usageShedStepName(step, models), previous, width)
				}
				previous = width
				for _, m := range models {
					block := providerBlock(plain, m)
					if block == "" {
						t.Fatalf("step %d (%s) removed provider %s entirely: %q", i, usageShedStepName(step, models), m.label, plain)
					}
					official := "weekly"
					if m.hasFive {
						official = "5h"
					}
					if !strings.Contains(block, official+" [") && !strings.Contains(block, official+":") {
						t.Fatalf("step %d (%s) removed %s's official %s window: %q", i, usageShedStepName(step, models), m.label, official, plain)
					}
					if marker := staleMarkerText(modelStaleLevel(m, statusGoldenNow)); marker != "" && !strings.Contains(block, marker) {
						t.Fatalf("step %d (%s) removed %s's %q staleness marker: %q", i, usageShedStepName(step, models), m.label, marker, plain)
					}
				}
			}
		})
	}
}

// usageShedStepName identifies a step in a test failure: `<rule name>` for a
// segment-wide rule, `<provider>: <rule name>` for a per-provider one.
func usageShedStepName(step usageShedStep, models []modelDisplay) string {
	rule := usageShedOrder[step.rule]
	if step.model < 0 || step.model >= len(models) {
		return rule.name
	}
	return models[step.model].label + ": " + rule.name
}

// providerBlock returns the rendered block belonging to a model, or "" when it
// is absent. Blocks are separated by statusModelSeparator.
func providerBlock(plain string, m modelDisplay) string {
	for block := range strings.SplitSeq(plain, statusModelSeparator) {
		if strings.HasPrefix(block, m.label) || strings.HasPrefix(block, m.shortLabel) {
			return block
		}
	}
	return ""
}

// docsDropOrderPattern matches the numbered drop-order entries in
// docs/statusbar.md: “1. `cosmetic age text` — ...“.
var docsDropOrderPattern = regexp.MustCompile("(?m)^([0-9]+)\\. `([^`]+)`")

// TestDropOrderMatchesTheDocumentedOrder is acceptance criterion 2: the drop
// order is defined in ONE place in code, and the documentation says the same
// thing. The test reads docs/statusbar.md rather than a copy of it, so the doc
// and usageShedOrder cannot drift apart silently.
func TestDropOrderMatchesTheDocumentedOrder(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "statusbar.md"))
	if err != nil {
		t.Fatalf("read docs/statusbar.md: %v", err)
	}
	section := usageDropOrderSection(string(raw))
	if section == "" {
		t.Fatalf("docs/statusbar.md has no `## Usage element drop order` section")
	}
	matches := docsDropOrderPattern.FindAllStringSubmatch(section, -1)
	if len(matches) != len(usageShedOrder) {
		t.Fatalf("docs list %d drop-order entries, usageShedOrder has %d: %v", len(matches), len(usageShedOrder), matches)
	}
	for i, match := range matches {
		if want := fmt.Sprintf("%d", i+1); match[1] != want {
			t.Fatalf("drop-order entry %d is numbered %q, want %q", i, match[1], want)
		}
		if match[2] != usageShedOrder[i].name {
			t.Fatalf("drop-order entry %d: docs say %q, usageShedOrder says %q", i+1, match[2], usageShedOrder[i].name)
		}
	}
}

// usageDropOrderSection extracts the drop-order section body from the doc.
func usageDropOrderSection(doc string) string {
	const heading = "## Usage element drop order"
	_, after, ok := strings.Cut(doc, heading)
	if !ok {
		return ""
	}
	if before, _, ok0 := strings.Cut(after, "\n## "); ok0 {
		return before
	}
	return after
}
