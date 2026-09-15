package app

import (
	"io"
	"strings"
	"testing"

	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
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
			cases = append(cases, landingCase{tab: tab, value: entry.Value, landing: landing})
		}
	}

	unnamedTargets := 0
	var firstRowLandings []string
	var ancestorLandings []string
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
		if tc.landing.Focus == "" {
			// The walk could not name the row without runtime state. The View
			// still opens, on its first row.
			unnamedTargets++
			if landed.InitialIndexSet {
				t.Errorf("%s: no target row was named, yet the frame set InitialIndex %d", tc.value, landed.InitialIndex)
			}
			continue
		}
		wantIndex, present := settingsLandingExpectedIndex(landed, tc.landing.Focus)
		if !present {
			// Acceptance 5: the target row is not rendered here, so the View
			// opens on its first row rather than failing.
			firstRowLandings = append(firstRowLandings, tc.value)
			if landed.InitialIndexSet {
				t.Errorf("%s: target %q is not rendered, yet the frame set InitialIndex %d", tc.value, tc.landing.Focus, landed.InitialIndex)
			}
			continue
		}
		if !landed.InitialIndexSet {
			t.Errorf("%s: target %q is rendered at index %d but the frame opened unfocused", tc.value, tc.landing.Focus, wantIndex)
			continue
		}
		if landed.InitialIndex != wantIndex {
			t.Errorf("%s: focused index %d, want %d (target %q)", tc.value, landed.InitialIndex, wantIndex, tc.landing.Focus)
			continue
		}
		got := strings.TrimSpace(focused.Value)
		if got != tc.landing.Focus && !strings.HasPrefix(got, tc.landing.Focus+":") {
			t.Errorf("%s: focused row value %q does not match target %q; rows %q", tc.value, got, tc.landing.Focus, settingsLandingItemValues(landed))
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
	t.Logf("landings: %d rows; %d named no target row; %d had a target this fixture does not render (%q); %d stopped at an ancestor View (%q)",
		len(cases), unnamedTargets, len(firstRowLandings), firstRowLandings, len(ancestorLandings), ancestorLandings)
}

// settingsLandingExpectedIndex resolves the target independently of the
// production matcher, so a matcher bug cannot make the assertion agree with
// itself: the exact value wins, otherwise the first row under `target:`.
func settingsLandingExpectedIndex(options intpicker.Options, target string) (int, bool) {
	for i, item := range options.Items {
		if strings.TrimSpace(item.Value) == target {
			return i, true
		}
	}
	for i, item := range options.Items {
		if strings.HasPrefix(strings.TrimSpace(item.Value), target+":") {
			return i, true
		}
	}
	return 0, false
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
	if cmd.pendingNavigation != nil || cmd.pendingFocus != "" {
		t.Errorf("an abandoned landing left pending state: %q / %q", cmd.pendingNavigation, cmd.pendingFocus)
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
