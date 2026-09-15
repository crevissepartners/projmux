package app

import (
	"io"
	"slices"
	"strings"
	"testing"

	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// The Settings root answers a query with one result row per catalogued setting.
// Choosing one must open the View that owns the row with the cursor on it, and
// must not run anything on the way: the chain is navigation only and the last
// step is a focus.
//
// These tests walk the catalog rather than a frozen ID list, so a new node
// extends them automatically — including the assertion that its result row
// lands somewhere real.

// settingsLandingRunner presses a scripted value per rendered frame and records
// every frame. Frames a landing answers without rendering never reach it, so
// the number of frames it sees is itself the proof that the descent happened
// through the synthesized seam rather than through the picker.
type settingsLandingRunner struct {
	script []string
	// guard makes the runner press a scripted value only when the frame really
	// renders it, so a hand walk stops where a user would stop instead of
	// feeding a loop a value it cannot own.
	guard  bool
	frames []intpicker.Options
}

func (r *settingsLandingRunner) Run(options intpicker.Options) (intpicker.Result, error) {
	r.frames = append(r.frames, options)
	if len(r.frames) > len(r.script) {
		return intpicker.Result{Key: "esc", Closed: true}, nil
	}
	value := r.script[len(r.frames)-1]
	if r.guard && !settingsLandingRendersValue(options, value) {
		return intpicker.Result{Key: "esc", Closed: true}, nil
	}
	return intpicker.Result{Key: "enter", Value: value}, nil
}

// complete reports whether every scripted press landed and one further frame
// rendered.
func (r *settingsLandingRunner) complete() bool {
	return len(r.frames) == len(r.script)+1
}

func (r *settingsLandingRunner) landed() (intpicker.Options, bool) {
	if !r.complete() {
		return intpicker.Options{}, false
	}
	return r.frames[len(r.frames)-1], true
}

func (r *settingsLandingRunner) last() (intpicker.Options, bool) {
	if len(r.frames) == 0 {
		return intpicker.Options{}, false
	}
	return r.frames[len(r.frames)-1], true
}

func settingsLandingRendersValue(options intpicker.Options, value string) bool {
	for _, item := range options.Items {
		if strings.TrimSpace(item.Value) == value {
			return true
		}
	}
	for _, chip := range options.TitleChips {
		if strings.TrimSpace(chip.ClickValue) == value {
			return true
		}
	}
	return false
}

// settingsLandingFocused is the row a frame opens on: the item the picker's
// InitialIndex names, or the first row when no `start:pos` binding was set.
func settingsLandingFocused(options intpicker.Options) (intpicker.Item, bool) {
	index := 0
	if options.InitialIndexSet {
		index = options.InitialIndex
	}
	if index < 0 || index >= len(options.Items) {
		return intpicker.Item{}, false
	}
	return options.Items[index], true
}

func settingsLandingItemValues(options intpicker.Options) []string {
	values := make([]string, 0, len(options.Items))
	for _, item := range options.Items {
		values = append(values, strings.TrimSpace(item.Value))
	}
	return values
}

func settingsLandingFrameKey(options intpicker.Options) string {
	return strings.Join([]string{options.UI, options.Title, options.Prompt}, "\x00")
}

// settingsLandingTabScript is what a user presses before the result row: the
// Global tab is where Settings opens, the Project tab needs its chip first.
func settingsLandingTabScript(tab settingsRootTab) []string {
	if tab == settingsRootTabProject {
		return []string{settingsRootTabProjectValue}
	}
	return nil
}

// TestSettingsRootResultLandsOnItsOwningRow drives the real Settings Run for
// every result row of both scopes and proves the row is a destination: the
// owning View opens, the cursor sits on the target row, no control runs, and
// the temporary HOME is byte-identical afterwards.
func TestSettingsRootResultLandsOnItsOwningRow(t *testing.T) {
	scenario := settingsSearchScenario{name: "landing"}
	cmd, home, writes := settingsSearchFixture(t, scenario)
	before := settingsNavConfigSnapshot(t, home)

	quitRunner := &countingSettingsQuitRunner{}
	cmd.quit = quitRunner

	type landingCase struct {
		tab     settingsRootTab
		value   string
		label   string
		landing settingsRootResultLanding
	}
	var cases []landingCase
	for _, tab := range []settingsRootTab{settingsRootTabGlobal, settingsRootTabProject} {
		entries, landings := settingsRootResultLandings(tab, cmd.locale())
		if len(entries) == 0 {
			t.Fatalf("tab %v produced no result rows", tab)
		}
		for _, entry := range entries {
			landing, ok := landings[entry.Value]
			if !ok {
				t.Fatalf("result row %q has no landing", entry.Value)
			}
			if len(landing.Navigation) == 0 {
				t.Errorf("result row %q has an empty navigation chain", entry.Value)
				continue
			}
			cases = append(cases, landingCase{tab: tab, value: entry.Value, label: entry.Label, landing: landing})
		}
	}

	var unnameable []string
	var firstRowLandings []string
	var ancestorLandings []string
	landedByValue, landedByLabel := 0, 0
	for _, tc := range cases {
		// The landing press: the tab chip (Project only), then the result row.
		// Every step of the chain below is answered without rendering, so the
		// picker sees exactly one more frame — the owning View.
		script := append(settingsLandingTabScript(tc.tab), tc.value)
		runner := &settingsLandingRunner{script: script}
		cmd.nativePicker = runner
		if err := cmd.Run(nil, &strings.Builder{}, &strings.Builder{}); err != nil {
			t.Fatalf("%s: settings run: %v", tc.value, err)
		}
		landed, ok := runner.landed()
		if !ok {
			t.Errorf("%s: expected %d frames (root chain + owning View), got %d", tc.value, len(script)+1, len(runner.frames))
			continue
		}
		if strings.TrimSpace(landed.UI) == "settings" {
			t.Errorf("%s: landed back on the Settings root instead of the owning View", tc.value)
			continue
		}

		// The frame the chain reached must be the frame a user reaches by
		// pressing the same rows by hand, through the real section loops. The
		// hand walk stops where a row is missing, which is exactly where the
		// landing abandons, so the two must always end on the same frame.
		manualScript := append(settingsLandingTabScript(tc.tab), tc.landing.Navigation...)
		manual := &settingsLandingRunner{script: manualScript, guard: true}
		cmd.nativePicker = manual
		if err := cmd.Run(nil, &strings.Builder{}, &strings.Builder{}); err != nil {
			t.Fatalf("%s: manual walk: %v", tc.value, err)
		}
		manualFrame, ok := manual.last()
		if !ok {
			t.Errorf("%s: pressing the chain %q by hand did not render a frame", tc.value, tc.landing.Navigation)
			continue
		}
		if got, want := settingsLandingFrameKey(landed), settingsLandingFrameKey(manualFrame); got != want {
			t.Errorf("%s: landed on %q, clicking the chain reaches %q", tc.value, got, want)
			continue
		}

		focused, ok := settingsLandingFocused(landed)
		if !ok {
			t.Errorf("%s: landed frame has no focusable row", tc.value)
			continue
		}
		if !manual.complete() {
			// A chain row is not rendered in this fixture, so the landing
			// stopped at the nearest ancestor View and dropped its focus.
			ancestorLandings = append(ancestorLandings, tc.value)
			if landed.InitialIndexSet {
				t.Errorf("%s: an abandoned landing still focused index %d", tc.value, landed.InitialIndex)
			}
			continue
		}
		// The row is named twice over: by the picker Value its builder emits,
		// and by the name it renders — which is the last segment of the path
		// this very result row displays. Either naming is enough to land.
		wantName := settingsLandingResultPathLeaf(tc.label)
		if wantName == "" {
			t.Errorf("%s: result row %q has no trailing path segment", tc.value, tc.label)
			continue
		}
		wantIndex, by, present := settingsLandingExpectedIndex(landed, tc.landing.Focus, wantName)
		if !present {
			if tc.landing.Focus == "" {
				// Neither naming reaches a row of this View. The View still
				// opens, on its first row.
				unnameable = append(unnameable, tc.value+" ["+wantName+"]")
			} else {
				// Acceptance 5: the target row is not rendered here, so the
				// View opens on its first row rather than failing.
				firstRowLandings = append(firstRowLandings, tc.value)
			}
			if landed.InitialIndexSet {
				t.Errorf("%s: neither target %q nor name %q is rendered, yet the frame set InitialIndex %d", tc.value, tc.landing.Focus, wantName, landed.InitialIndex)
			}
			continue
		}
		if !landed.InitialIndexSet {
			t.Errorf("%s: target %q / name %q is rendered at index %d but the frame opened unfocused", tc.value, tc.landing.Focus, wantName, wantIndex)
			continue
		}
		if landed.InitialIndex != wantIndex {
			t.Errorf("%s: focused index %d, want %d (target %q, name %q)", tc.value, landed.InitialIndex, wantIndex, tc.landing.Focus, wantName)
			continue
		}
		// Per row: the focused row is the one the result row named. A row the
		// value target reached is pinned by that value; a row only the label
		// reached is pinned by its rendered name being exactly the last segment
		// of the path the user read on the result row.
		got := strings.TrimSpace(focused.Value)
		switch by {
		case settingsLandingByValue:
			landedByValue++
			if got != tc.landing.Focus && !strings.HasPrefix(got, tc.landing.Focus+":") {
				t.Errorf("%s: focused row value %q does not match target %q; rows %q", tc.value, got, tc.landing.Focus, settingsLandingItemValues(landed))
			}
		default:
			landedByLabel++
			if name := settingsLandingRowName(focused.Label); name != wantName {
				t.Errorf("%s: focused row name %q does not match the result path leaf %q; rows %q", tc.value, name, wantName, settingsLandingItemValues(landed))
			}
		}
	}

	if len(*writes) > 0 {
		t.Fatalf("landing walk attempted writes: %q", *writes)
	}
	if quitRunner.calls != 0 {
		t.Fatalf("landing walk ran the quit flow %d times", quitRunner.calls)
	}
	if after := settingsNavConfigSnapshot(t, home); after != before {
		t.Fatalf("landing walk changed the temporary HOME")
	}
	if len(firstRowLandings) == 0 {
		t.Errorf("no result row exercised the missing-target branch; the first-row fallback is untested")
	}
	if landedByLabel == 0 {
		t.Errorf("no result row landed through its rendered name; the label target is untested")
	}
	// The rows that remain are the ones neither naming can decide, and every
	// one of them must belong to an understood class. A row outside these
	// classes that names nothing is a regression, not a degradation.
	for _, row := range unnameable {
		if reason := settingsLandingUnnameableReason(row); reason == "" {
			t.Errorf("%s names no target row and is not a known undecidable class", row)
		}
	}
	t.Logf("landings: %d rows; %d landed by value, %d by rendered name; %d can name no row (%q); %d had a target this fixture does not render (%q); %d stopped at an ancestor View (%q)",
		len(cases), landedByValue, landedByLabel, len(unnameable), unnameable, len(firstRowLandings), firstRowLandings, len(ancestorLandings), ancestorLandings)
}

// settingsLandingUnnameableRenderTimeNodes are the catalog nodes that render
// as exactly one row but whose value and name are both chosen at render time
// from state the result walk must not read:
//
//   - tokens.item.fallback ("Use preset fallback"): the row is
//     `theme:color-set:<token>:` and renders as "Set <saved preset>", and the
//     value's prefix would grab the "Terminal default" row in front of it;
//   - trust.approve ("Trust or refresh approval"): the View renders "Trust this
//     config" (trust:apply) or "Refresh trust" (trust:refresh) depending on the
//     on-disk trust state, so there are two candidate rows and no way to choose.
var settingsLandingUnnameableRenderTimeNodes = []string{
	settingsNavAppearanceTheme + ".tokens.item.fallback",
	settingsNavProjectTrust + ".approve",
}

// settingsLandingUnnameableReason explains why a result row can name no row,
// or returns "" when it should have. Besides the render-time nodes above, the
// only class is a passive State node: one catalog entry that stands for several
// rendered rows and whose label enumerates them ("Effective / Saved / Source"),
// so there is no single row to land on.
func settingsLandingUnnameableReason(row string) string {
	value, _, _ := strings.Cut(row, " [")
	nodeID, _, ok := parseSettingsRootResultValue(value)
	if !ok {
		return ""
	}
	if slices.Contains(settingsLandingUnnameableRenderTimeNodes, nodeID) {
		return "render-time value and name"
	}
	node, found := settingsNavByID(nodeID)
	if found && node.Kind == settingsNavState && node.Value == settingsNoopValue {
		return "State node standing for several rows"
	}
	return ""
}

// settingsLandingResultPathLeaf is the last segment of the path a result row
// displays — the name of the row it points at, as the user read it.
func settingsLandingResultPathLeaf(label string) string {
	path := strings.TrimSpace(stripSettingsLabelANSI(label))
	if index := strings.LastIndex(path, settingsRootResultSeparator); index >= 0 {
		path = path[index+len(settingsRootResultSeparator):]
	}
	return strings.TrimSpace(path)
}

// settingsLandingRowName reads a rendered row's name column back out without
// borrowing the production parser, so a parser bug cannot make the assertion
// agree with itself: drop the glyph column, then take everything up to the next
// column gap.
func settingsLandingRowName(label string) string {
	columns := strings.SplitN(stripSettingsLabelANSI(label), "  ", 3)
	if len(columns) < 2 {
		return strings.TrimSpace(stripSettingsLabelANSI(label))
	}
	return strings.TrimSpace(columns[1])
}

// settingsLandingBy says which naming reached the row.
type settingsLandingBy int

const (
	settingsLandingByValue settingsLandingBy = iota
	settingsLandingByName
)

// settingsLandingExpectedIndex resolves the target independently of the
// production matcher, so a matcher bug cannot make the assertion agree with
// itself: the exact value wins, otherwise the first row under `target:`,
// otherwise the one row whose rendered name is exactly the wanted name. Two
// rows with that name resolve to nothing, like no row at all.
func settingsLandingExpectedIndex(options intpicker.Options, target, name string) (int, settingsLandingBy, bool) {
	if target != "" {
		for i, item := range options.Items {
			if strings.TrimSpace(item.Value) == target {
				return i, settingsLandingByValue, true
			}
		}
		for i, item := range options.Items {
			if strings.HasPrefix(strings.TrimSpace(item.Value), target+":") {
				return i, settingsLandingByValue, true
			}
		}
	}
	found := -1
	for i, item := range options.Items {
		if name == "" || settingsLandingRowName(item.Label) != name {
			continue
		}
		if found >= 0 {
			return 0, settingsLandingByName, false
		}
		found = i
	}
	if found < 0 {
		return 0, settingsLandingByName, false
	}
	return found, settingsLandingByName, true
}

// TestSettingsRootResultConfirmAndActionRowsAreFocusedNotRun pins the rows the
// contract calls out by name: Quit and Reset theme are focused like any other
// result, and neither their loop nor their runner is entered.
func TestSettingsRootResultConfirmAndActionRowsAreFocusedNotRun(t *testing.T) {
	cmd, home, writes := settingsSearchFixture(t, settingsSearchScenario{name: "confirm-rows"})
	before := settingsNavConfigSnapshot(t, home)
	quitRunner := &countingSettingsQuitRunner{}
	cmd.quit = quitRunner

	_, landings := settingsRootResultLandings(settingsRootTabGlobal, cmd.locale())
	for _, tc := range []struct {
		node  string
		focus string
	}{
		{settingsNavAbout + ".quit", settingsQuitOpen},
		{settingsNavAppearanceTheme + ".reset", themeAction("reset")},
		{settingsNavProjectsPrimaryRoot + ".clear", settingsProjdirClear},
	} {
		value := settingsRootResultValue(tc.node, nil)
		landing, ok := landings[value]
		if !ok {
			t.Fatalf("%s: no result row", tc.node)
		}
		if landing.Focus != tc.focus {
			t.Fatalf("%s: target %q, want %q", tc.node, landing.Focus, tc.focus)
		}
		runner := &settingsLandingRunner{script: []string{value}}
		cmd.nativePicker = runner
		if err := cmd.Run(nil, &strings.Builder{}, &strings.Builder{}); err != nil {
			t.Fatalf("%s: settings run: %v", tc.node, err)
		}
		landed, ok := runner.landed()
		if !ok {
			t.Fatalf("%s: expected 2 frames, got %d", tc.node, len(runner.frames))
		}
		focused, ok := settingsLandingFocused(landed)
		if !ok {
			t.Fatalf("%s: landed frame has no focusable row", tc.node)
		}
		if strings.TrimSpace(focused.Value) != tc.focus {
			t.Errorf("%s: focused %q, want %q", tc.node, focused.Value, tc.focus)
		}
	}

	if quitRunner.calls != 0 {
		t.Fatalf("a result row ran the quit flow %d times", quitRunner.calls)
	}
	if len(*writes) > 0 {
		t.Fatalf("a result row attempted writes: %q", *writes)
	}
	if after := settingsNavConfigSnapshot(t, home); after != before {
		t.Fatalf("a result row changed the temporary HOME")
	}
}

// TestSettingsRootResultAbandonsALandingWhoseChainRowIsGone proves the guard:
// when the value the chain expects is not a row of the frame it arrives at, the
// whole landing is dropped and that frame renders normally instead of Enter
// being pressed on whatever happens to be there.
func TestSettingsRootResultAbandonsALandingWhoseChainRowIsGone(t *testing.T) {
	cmd, home, writes := settingsSearchFixture(t, settingsSearchScenario{name: "chain-row-gone"})
	before := settingsNavConfigSnapshot(t, home)

	// Appearance > Theme > Tokens > Core > background, with the Tokens step
	// replaced by a value no Theme View renders.
	value := settingsRootResultValue(settingsNavAppearanceTheme+".tokens.item", []settingsRootResultInstance{{Key: "background"}})
	_, landings := settingsRootResultLandings(settingsRootTabGlobal, cmd.locale())
	landing, ok := landings[value]
	if !ok {
		t.Fatalf("no result row for the background token")
	}
	if len(landing.Navigation) < 3 {
		t.Fatalf("chain %q is shorter than the Appearance > Theme > Tokens > group descent", landing.Navigation)
	}

	runner := &settingsLandingRunner{script: []string{landing.Navigation[0]}}
	cmd.nativePicker = runner
	cmd.pendingNavigation = append(append([]string(nil), landing.Navigation[1:len(landing.Navigation)-1]...), "theme:group:[nonexistent]")
	cmd.pendingFocus = landing.Focus
	cmd.pendingFocusLabel = landing.FocusLabel
	if err := cmd.Run(nil, &strings.Builder{}, &strings.Builder{}); err != nil {
		t.Fatalf("settings run: %v", err)
	}
	landed, ok := runner.landed()
	if !ok {
		t.Fatalf("expected 2 frames, got %d", len(runner.frames))
	}
	if landed.InitialIndexSet {
		t.Errorf("an abandoned landing still focused index %d in %q", landed.InitialIndex, landed.UI)
	}
	if cmd.pendingNavigation != nil || cmd.pendingFocus != "" || cmd.pendingFocusLabel != "" {
		t.Errorf("an abandoned landing left pending state: %q / %q / %q", cmd.pendingNavigation, cmd.pendingFocus, cmd.pendingFocusLabel)
	}
	if len(*writes) > 0 {
		t.Fatalf("an abandoned landing attempted writes: %q", *writes)
	}
	if after := settingsNavConfigSnapshot(t, home); after != before {
		t.Fatalf("an abandoned landing changed the temporary HOME")
	}
}

type countingSettingsQuitRunner struct {
	calls int
}

func (q *countingSettingsQuitRunner) Run(_ []string, _, _ io.Writer) error {
	q.calls++
	return nil
}

// TestSettingsLandingLabelTargetRefusesAmbiguousRows pins the rule that keeps
// the label target honest: it names a row only when exactly one row renders
// that name. Two rows with the same name focus neither — and nothing about that
// is an error, because opening the View on its first row is the same outcome a
// vanished row already produces.
func TestSettingsLandingLabelTargetRefusesAmbiguousRows(t *testing.T) {
	row := func(name, description, value string) intpickercompat.Entry {
		return intpickercompat.Entry{Label: settingsLabel(settingsGlyphOpen, settingsColorType, name, description), Value: value}
	}
	for _, tt := range []struct {
		name    string
		entries []intpickercompat.Entry
		focus   string
		label   string
		want    int
		wantOK  bool
	}{
		{
			name:    "one row carries the name",
			entries: []intpickercompat.Entry{row("Back", "", settingsBackValue), row("Remove command", "clears it", settingsNoopValue)},
			label:   "Remove command",
			want:    1,
			wantOK:  true,
		},
		{
			name: "two rows carry the same name",
			entries: []intpickercompat.Entry{
				row("Back", "", settingsBackValue),
				row("Remove command", "global scope", settingsNoopValue),
				row("Remove command", "project scope", settingsNoopValue),
			},
			label: "Remove command",
		},
		{
			name: "a description that repeats the name is not the name",
			entries: []intpickercompat.Entry{
				row("Back", "", settingsBackValue),
				row("Storage", "Remove command and its storage", settingsNoopValue),
			},
			label: "Remove command",
		},
		{
			name:    "the value target still wins over a row that renders the name",
			entries: []intpickercompat.Entry{row("Remove command", "", settingsNoopValue), row("Other", "", "hook-remove:project:pre-create")},
			focus:   "hook-remove:project:pre-create",
			label:   "Remove command",
			want:    1,
			wantOK:  true,
		},
		{
			name:    "no label and no value names nothing",
			entries: []intpickercompat.Entry{row("Back", "", settingsBackValue)},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			index, ok := settingsOptionsFocusIndex(intpickercompat.Options{Entries: tt.entries}, tt.focus, tt.label)
			if ok != tt.wantOK {
				t.Fatalf("matched=%v, want %v (index %d)", ok, tt.wantOK, index)
			}
			if ok && index != tt.want {
				t.Fatalf("index %d, want %d", index, tt.want)
			}
		})
	}
}

// TestSettingsLandingAmbiguousLabelLeavesTheViewUnfocused drives the same case
// through the seam the landing actually uses: an ambiguous label appends no
// `start:pos` binding, so the View opens on its first row, and the pending
// state is spent either way.
func TestSettingsLandingAmbiguousLabelLeavesTheViewUnfocused(t *testing.T) {
	entries := []intpickercompat.Entry{
		{Label: settingsLabel(settingsGlyphOpen, settingsColorType, "Back", ""), Value: settingsBackValue},
		{Label: settingsLabel(settingsGlyphOpen, settingsColorType, "Remove command", "global scope"), Value: settingsNoopValue},
		{Label: settingsLabel(settingsGlyphOpen, settingsColorType, "Remove command", "project scope"), Value: settingsNoopValue},
	}
	cmd := &settingsCommand{pendingFocusLabel: "Remove command"}
	got := cmd.withSettingsLandingFocus(intpickercompat.Options{Entries: entries, Bindings: []string{"esc:abort"}})
	if len(got.Bindings) != 1 || got.Bindings[0] != "esc:abort" {
		t.Errorf("an ambiguous label added bindings: %q", got.Bindings)
	}
	if cmd.pendingFocus != "" || cmd.pendingFocusLabel != "" {
		t.Errorf("pending landing state survived: %q / %q", cmd.pendingFocus, cmd.pendingFocusLabel)
	}

	cmd = &settingsCommand{pendingFocusLabel: "Remove command"}
	got = cmd.withSettingsLandingFocus(intpickercompat.Options{Entries: entries[:2]})
	if len(got.Bindings) != 1 || got.Bindings[0] != "start:pos(2)" {
		t.Errorf("a unique label did not focus its row: %q", got.Bindings)
	}
}
