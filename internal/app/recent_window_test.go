package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/recentwindows"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/theme"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	projmuxpicker "github.com/crevissepartners/projmux/internal/ui/projmuxpicker"
)

var recentWindowANSIPattern = regexp.MustCompile("\x1b\\[[0-9;]*m")

// recentWindowStripANSI removes SGR escape sequences so tests can assert on the
// visible content of badge-decorated rows.
func recentWindowStripANSI(value string) string {
	return recentWindowANSIPattern.ReplaceAllString(value, "")
}

func TestRecentWindowPickerItemEmphasizesNamesAndAge(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 6, 18, 1, 2, 3, 0, time.UTC)
	snapshot := recentwindows.Snapshot{
		Socket:        "/tmp/tmux",
		Session:       "repos-projmux",
		WindowID:      "@6",
		WindowName:    "projmux",
		Project:       "Projmux",
		LastPaneID:    "%54",
		LastPaneTitle: "codex-review",
		LastPaneTopic: "Phase 1 picker",
		LastCommand:   "codex",
		LastFocusedAt: at,
	}
	item := recentWindowPickerItem(recentwindows.Candidate{
		Snapshot: snapshot,
		Label:    recentwindows.BuildLabel(snapshot),
	}, at.Add(12*time.Second))

	// Line 1 reads: project badge -> readable window name -> age badge.
	visibleTitle := recentWindowStripANSI(item.Title)
	projectIdx := strings.Index(visibleTitle, "Projmux")
	nameIdx := strings.Index(visibleTitle, "projmux")
	ageIdx := strings.Index(visibleTitle, "12s ago")
	if projectIdx < 0 || nameIdx <= projectIdx || ageIdx <= nameIdx {
		t.Fatalf("line 1 visible = %q, want project badge -> window name -> age order", visibleTitle)
	}
	if strings.Contains(item.Title, "@6") || strings.Contains(item.Title, "%54") {
		t.Fatalf("Title = %q, want no raw tmux IDs", item.Title)
	}
	// Notify badge palette reuse: project + age start tokens, and every badge
	// terminates with a reset so selected-row background re-applies.
	if !strings.Contains(item.Title, theme.ANSINotifyProjectStart) {
		t.Fatalf("Title = %q, want notify project badge palette", item.Title)
	}
	if !strings.Contains(item.Title, theme.ANSINotifyAgeStart) {
		t.Fatalf("Title = %q, want notify age badge palette", item.Title)
	}
	if !strings.HasSuffix(item.Title, theme.ANSIReset) {
		t.Fatalf("Title = %q, want trailing reset", item.Title)
	}
	text := recentWindowStripANSI(item.Title + "\n" + strings.Join(item.MetaLines, "\n"))
	for _, want := range []string{"codex-review", "12s ago", "Projmux", "repos-projmux"} {
		if !strings.Contains(text, want) {
			t.Fatalf("render text = %q, want %q", text, want)
		}
	}
}

func TestRecentWindowPickerItemShowsContextBadgeAsMetaLine(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 6, 18, 1, 2, 3, 0, time.UTC)
	snapshot := recentwindows.Snapshot{
		Session:    "repos-projmux",
		WindowID:   "@6",
		WindowName: "projmux",
		Project:    "Projmux",
		PaneTitles: []string{"zsh"},
	}
	item := recentWindowPickerItem(recentWindowCandidate(snapshot), at)

	if got := recentWindowStripANSI(item.Title); !strings.Contains(got, "projmux") {
		t.Fatalf("line 1 visible = %q, want readable window name", got)
	}
	wantContext := "Projmux · repos-projmux"
	found := false
	for _, line := range item.MetaLines {
		if recentWindowStripANSI(line) == wantContext {
			found = true
		}
	}
	if !found {
		t.Fatalf("MetaLines = %#v, want context badge %q on its own line", item.MetaLines, wantContext)
	}
}

func TestRecentWindowPickerItemFallsBackToNameNeverRawIDs(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 6, 18, 1, 2, 3, 0, time.UTC)
	// No window name; should fall back through project/session/topic, never @id/%id.
	snapshot := recentwindows.Snapshot{
		Session:       "repos-projmux",
		WindowID:      "@6",
		LastPaneID:    "%54",
		LastPaneTopic: "roadmap",
	}
	item := recentWindowPickerItem(recentWindowCandidate(snapshot), at)

	if strings.Contains(item.Title, "@6") || strings.Contains(item.Title, "%54") {
		t.Fatalf("Title = %q, want fallback name, never raw tmux IDs", item.Title)
	}
	if got := recentWindowStripANSI(item.Title); !strings.Contains(got, "repos-projmux") {
		t.Fatalf("line 1 visible = %q, want session fallback", got)
	}
}

func TestRecentWindowPickerItemJoinsAllPaneTitles(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 6, 18, 1, 2, 3, 0, time.UTC)
	snapshot := recentwindows.Snapshot{
		Session:       "repos-projmux",
		WindowID:      "@6",
		WindowName:    "projmux",
		LastPaneTitle: "zsh",
		PaneTitles:    []string{"zsh", "Codex · [lead:roadmap] Projmux", "Claude Code"},
	}
	item := recentWindowPickerItem(recentWindowCandidate(snapshot), at)

	want := "zsh | Codex · [lead:roadmap] Projmux | Claude Code"
	found := false
	for _, line := range item.MetaLines {
		if recentWindowStripANSI(line) == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("MetaLines = %#v, want pane summary %q joined with ' | '", item.MetaLines, want)
	}
}

func TestRecentWindowPickerItemFallsBackToLastPaneTitleSummary(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 6, 18, 1, 2, 3, 0, time.UTC)
	// Older state file: no PaneTitles, only LastPaneTitle.
	snapshot := recentwindows.Snapshot{
		Session:       "repos-projmux",
		WindowID:      "@6",
		WindowName:    "projmux",
		LastPaneTitle: "legacy-pane",
	}
	item := recentWindowPickerItem(recentWindowCandidate(snapshot), at)

	if got := recentWindowPaneSummary(recentWindowCandidate(snapshot)); got != "legacy-pane" {
		t.Fatalf("pane summary = %q, want LastPaneTitle fallback", got)
	}
	if !strings.Contains(strings.Join(item.MetaLines, "\n"), "legacy-pane") {
		t.Fatalf("MetaLines = %#v, want LastPaneTitle in summary", item.MetaLines)
	}
}

func TestRecentWindowPaneSummaryTruncatesStably(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("pane-title", 30) // far over the rune budget
	snapshot := recentwindows.Snapshot{
		Session:    "s",
		WindowID:   "@1",
		WindowName: "w",
		PaneTitles: []string{long},
	}
	summary := recentWindowPaneSummary(recentWindowCandidate(snapshot))

	runes := []rune(summary)
	if len(runes) != recentWindowPaneSummaryMaxRunes {
		t.Fatalf("summary rune len = %d, want %d", len(runes), recentWindowPaneSummaryMaxRunes)
	}
	if !strings.HasSuffix(summary, "…") {
		t.Fatalf("summary = %q, want trailing ellipsis", summary)
	}
	// Stable: same input always truncates identically.
	if again := recentWindowPaneSummary(recentWindowCandidate(snapshot)); again != summary {
		t.Fatalf("summary not stable: %q vs %q", summary, again)
	}
}

func TestRecentWindowTruncateIsRuneAware(t *testing.T) {
	t.Parallel()

	if got := recentWindowTruncate("héllo", 10); got != "héllo" {
		t.Fatalf("recentWindowTruncate short = %q, want unchanged", got)
	}
	if got := recentWindowTruncate("héllo", 3); got != "hé…" {
		t.Fatalf("recentWindowTruncate = %q, want rune-aware ellipsis", got)
	}
}

func TestRecentWindowPickerItemLastVisitFormat(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 6, 18, 1, 2, 3, 0, time.UTC)
	snapshot := recentwindows.Snapshot{
		Session:       "repos-projmux",
		WindowID:      "@6",
		WindowName:    "projmux",
		LastFocusedAt: at,
	}
	item := recentWindowPickerItem(recentWindowCandidate(snapshot), at.Add(3*time.Minute))

	want := "last visit · 3m ago · 2026-06-18 01:02"
	found := false
	for _, line := range item.MetaLines {
		if line == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("MetaLines = %#v, want last-visit line %q", item.MetaLines, want)
	}
}

func TestRecentWindowPickerItemPaneTopicLeadsSummary(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 6, 18, 1, 2, 3, 0, time.UTC)
	snapshot := recentwindows.Snapshot{
		Session:       "repos-projmux",
		WindowID:      "@6",
		WindowName:    "projmux",
		LastPaneTopic: "Phase 6 polish",
		PaneTitles:    []string{"zsh", "Claude Code"},
	}
	item := recentWindowPickerItem(recentWindowCandidate(snapshot), at)

	if len(item.MetaLines) == 0 {
		t.Fatalf("MetaLines = %#v, want a pane summary line", item.MetaLines)
	}
	visible := recentWindowStripANSI(item.MetaLines[0])
	topicIdx := strings.Index(visible, "Phase 6 polish")
	zshIdx := strings.Index(visible, "zsh")
	claudeIdx := strings.Index(visible, "Claude Code")
	if topicIdx < 0 || zshIdx <= topicIdx || claudeIdx <= zshIdx {
		t.Fatalf("pane summary line = %q, want topic-led order topic -> zsh -> Claude Code", visible)
	}
	if !strings.Contains(visible, "zsh | Claude Code") {
		t.Fatalf("pane summary line = %q, want remaining titles joined with ' | '", visible)
	}
	// The leading topic is wrapped in the shared active chip palette.
	if !strings.Contains(item.MetaLines[0], theme.ANSIChipActiveStart) {
		t.Fatalf("pane summary line = %q, want topic chip palette", item.MetaLines[0])
	}
	if !strings.HasSuffix(item.MetaLines[0], theme.ANSIReset) {
		t.Fatalf("pane summary line = %q, want trailing reset", item.MetaLines[0])
	}
}

func TestRecentWindowPickerItemTruncatesLongComponentsKeepingNameAndAge(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 6, 18, 1, 2, 3, 0, time.UTC)
	snapshot := recentwindows.Snapshot{
		Session:       "repos-projmux",
		WindowID:      "@6",
		WindowName:    strings.Repeat("window-name", 12),
		Project:       strings.Repeat("project", 12),
		LastFocusedAt: at,
	}
	item := recentWindowPickerItem(recentWindowCandidate(snapshot), at.Add(2*time.Minute))

	visible := recentWindowStripANSI(item.Title)
	if !strings.Contains(visible, "2m ago") {
		t.Fatalf("line 1 visible = %q, want age preserved", visible)
	}
	if !strings.Contains(visible, "…") {
		t.Fatalf("line 1 visible = %q, want long components truncated with ellipsis", visible)
	}
	// The window name must remain present (truncated, not dropped).
	if !strings.Contains(visible, "window-name") {
		t.Fatalf("line 1 visible = %q, want window name preserved", visible)
	}
	// Component budgets bound each piece so the age survives.
	if got := len([]rune(recentWindowBadgeText(recentWindowCandidate(snapshot)))); got > recentWindowProjectBadgeMaxRunes {
		t.Fatalf("badge text rune len = %d, want <= %d", got, recentWindowProjectBadgeMaxRunes)
	}
}

func TestRecentWindowPickerItemRowRendersWithoutStyleBleed(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 6, 18, 1, 2, 3, 0, time.UTC)
	snapshot := recentwindows.Snapshot{
		Session:       "repos-projmux",
		WindowID:      "@6",
		WindowName:    "projmux",
		Project:       "Projmux",
		LastPaneTopic: "Phase 6 polish",
		PaneTitles:    []string{"zsh"},
		LastFocusedAt: at,
	}
	item := recentWindowPickerItem(recentWindowCandidate(snapshot), at.Add(time.Minute))
	row := projmuxpicker.Row{Label: item.Title, MetaLines: item.MetaLines}

	for _, selected := range []bool{true, false} {
		lines := projmuxpicker.InteractiveRowLinesWithTheme(projmuxpicker.DefaultTheme, row, selected, true)
		if len(lines) == 0 {
			t.Fatalf("selected=%v rendered no lines", selected)
		}
		line1 := lines[0]
		if !strings.HasSuffix(line1, theme.ANSIReset) {
			t.Fatalf("selected=%v line 1 = %q, want trailing reset (no orphaned styling)", selected, line1)
		}
		if !strings.Contains(recentWindowStripANSI(line1), "projmux") {
			t.Fatalf("selected=%v line 1 visible = %q, want window name", selected, recentWindowStripANSI(line1))
		}
	}
}

func TestRecentWindowPickerItemSearchTextIncludesPaneTitlesAndDate(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 6, 18, 1, 2, 3, 0, time.UTC)
	snapshot := recentwindows.Snapshot{
		Session:       "repos-projmux",
		WindowID:      "@6",
		WindowName:    "projmux",
		Project:       "Projmux",
		PaneTitles:    []string{"zsh", "Claude Code", "Codex"},
		LastPaneTopic: "roadmap",
		LastCommand:   "codex",
		LastFocusedAt: at,
	}
	item := recentWindowPickerItem(recentWindowCandidate(snapshot), at.Add(time.Minute))

	for _, want := range []string{"projmux", "Projmux", "repos-projmux", "zsh", "Claude Code", "Codex", "roadmap", "codex", "2026-06-18 01:02"} {
		if !strings.Contains(item.SearchText, want) {
			t.Fatalf("SearchText = %q, want substring %q", item.SearchText, want)
		}
	}
}

func TestRecentWindowPickerItemKeepsSelectionValue(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 6, 18, 1, 2, 3, 0, time.UTC)
	snapshot := recentwindows.Snapshot{
		Session:    "repos-projmux",
		WindowID:   "@6",
		WindowName: "projmux",
	}
	candidate := recentWindowCandidate(snapshot)
	items, byValue := recentWindowPickerItems([]recentwindows.Candidate{candidate}, at)

	if len(items) != 1 {
		t.Fatalf("items = %#v, want one", items)
	}
	wantValue := "repos-projmux" + recentWindowFieldSep + "@6"
	if items[0].Value != wantValue {
		t.Fatalf("Value = %q, want %q", items[0].Value, wantValue)
	}
	if _, ok := byValue[wantValue]; !ok {
		t.Fatalf("byValue missing %q", wantValue)
	}
}

func TestRecentWindowRunEmptyQueueShowsMessage(t *testing.T) {
	t.Parallel()

	runner := &recentWindowFakeRunner{
		currentOutput: "/tmp/tmux" + recentWindowFieldSep + "current" + recentWindowFieldSep + "@1\n",
		listOutputs:   []string{"current" + recentWindowFieldSep + "@1\n"},
	}
	store := &recentWindowStubStore{}
	var pickerCalled bool
	cmd := &recentWindowCommand{
		runner: runner,
		storeFactory: func(string) (recentWindowStore, error) {
			return store, nil
		},
		nativePicker: pickerRunnerFunc(func(intpicker.Options) (intpicker.Result, error) {
			pickerCalled = true
			return intpicker.Result{}, nil
		}),
		now: func() time.Time { return time.Unix(0, 0) },
	}

	if err := cmd.Run(nil, nil, nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if pickerCalled {
		t.Fatal("picker was called for empty recent windows")
	}
	if !runner.sawDisplayMessage("no recent windows") {
		t.Fatalf("calls = %#v, want no recent windows display-message", runner.calls)
	}
}

func TestRecentWindowRunSwitchesCrossSessionWindowWithoutPaneRestore(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux,1,0")

	now := time.Date(2026, 6, 18, 2, 0, 0, 0, time.UTC)
	target := recentWindowCandidate(recentwindows.Snapshot{
		Socket:        "/tmp/tmux",
		Session:       "other-project",
		WindowID:      "@2",
		WindowName:    "agent",
		Project:       "Other",
		LastPaneID:    "%22",
		LastPaneTitle: "Claude",
		LastCommand:   "claude",
		LastFocusedAt: now.Add(-2 * time.Minute),
	})
	store := &recentWindowStubStore{candidates: []recentwindows.Candidate{target}}
	runner := &recentWindowFakeRunner{
		currentOutput: "/tmp/tmux" + recentWindowFieldSep + "current" + recentWindowFieldSep + "@1\n",
		listOutputs: "current" + recentWindowFieldSep + "@1\n" +
			"other-project" + recentWindowFieldSep + "@2\n",
	}
	var pickerOptions intpicker.Options
	cmd := &recentWindowCommand{
		runner: runner,
		opener: inttmux.NewClient(runner),
		storeFactory: func(string) (recentWindowStore, error) {
			return store, nil
		},
		nativePicker: pickerRunnerFunc(func(options intpicker.Options) (intpicker.Result, error) {
			pickerOptions = options
			return intpicker.Result{Key: "enter", Value: recentWindowValue(target)}, nil
		}),
		now: func() time.Time { return now },
	}

	if err := cmd.Run(nil, nil, nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !runner.sawCall("tmux", "switch-client", "-t", "=other-project:@2") {
		t.Fatalf("calls = %#v, want switch-client to selected window id", runner.calls)
	}
	for _, call := range runner.calls {
		if call.name == "tmux" && len(call.args) >= 1 && call.args[0] == "switch-client" && strings.Contains(strings.Join(call.args, " "), ".%22") {
			t.Fatalf("switch-client call = %#v, must not target last pane id", call)
		}
	}
	if got, want := pickerOptions.UI, "recent-windows"; got != want {
		t.Fatalf("picker UI = %q, want %q", got, want)
	}
	if len(pickerOptions.Items) != 1 {
		t.Fatalf("picker items = %#v, want one", pickerOptions.Items)
	}
	if got := strings.Join(append([]string{pickerOptions.Items[0].Title}, pickerOptions.Items[0].MetaLines...), "\n"); !strings.Contains(got, "Other") || !strings.Contains(got, "2m ago") {
		t.Fatalf("picker text = %q, want project and age", got)
	}
	if got, want := store.currents[0], (recentwindows.WindowKey{Socket: "/tmp/tmux", Session: "current", WindowID: "@1"}); got != want {
		t.Fatalf("store current = %+v, want %+v", got, want)
	}
	if got := store.lives[0]; !reflect.DeepEqual(got, []recentwindows.LiveWindow{
		{Socket: "/tmp/tmux", Session: "current", WindowID: "@1"},
		{Socket: "/tmp/tmux", Session: "other-project", WindowID: "@2"},
	}) {
		t.Fatalf("store live windows = %+v", got)
	}
}

func TestRecentWindowRunParsesEscapedCurrentAndListWindowSeparators(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux,1,0")

	target := recentWindowCandidate(recentwindows.Snapshot{
		Socket:     "/tmp/tmux",
		Session:    "other-project",
		WindowID:   "@2",
		WindowName: "agent",
	})
	store := &recentWindowStubStore{candidates: []recentwindows.Candidate{target}}
	runner := &recentWindowFakeRunner{
		currentOutput: strings.Join([]string{"/tmp/tmux", "current", "@1"}, recentWindowEscapedFieldSep) + "\n",
		listOutputs: strings.Join([]string{"current", "@1"}, recentWindowEscapedFieldSep) + "\n" +
			strings.Join([]string{"other-project", "@2"}, recentWindowEscapedFieldSep) + "\n",
	}
	cmd := &recentWindowCommand{
		runner: runner,
		opener: inttmux.NewClient(runner),
		storeFactory: func(string) (recentWindowStore, error) {
			return store, nil
		},
		nativePicker: pickerRunnerFunc(func(intpicker.Options) (intpicker.Result, error) {
			return intpicker.Result{Key: "enter", Value: recentWindowValue(target)}, nil
		}),
		now: func() time.Time { return time.Unix(0, 0) },
	}

	if err := cmd.Run(nil, nil, nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !runner.sawCall("tmux", "switch-client", "-t", "=other-project:@2") {
		t.Fatalf("calls = %#v, want switch-client to selected escaped-delimiter window", runner.calls)
	}
	if got, want := store.currents[0], (recentwindows.WindowKey{Socket: "/tmp/tmux", Session: "current", WindowID: "@1"}); got != want {
		t.Fatalf("store current = %+v, want %+v", got, want)
	}
	if got := store.lives[0]; !reflect.DeepEqual(got, []recentwindows.LiveWindow{
		{Socket: "/tmp/tmux", Session: "current", WindowID: "@1"},
		{Socket: "/tmp/tmux", Session: "other-project", WindowID: "@2"},
	}) {
		t.Fatalf("store live windows = %+v", got)
	}
}

func TestParseRecentWindowRowsAcceptsEscapedDelimiter(t *testing.T) {
	t.Parallel()

	output := []byte("current\\037@1\nother\\037@2\n")
	got := parseRecentWindowRows(output, 2)
	want := [][]string{{"current", "@1"}, {"other", "@2"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseRecentWindowRows() = %#v, want %#v", got, want)
	}
}

func TestRecentWindowRecordSnapshotsCurrentTmuxWindow(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux,1,0")

	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatalf("create marker: %v", err)
	}
	now := time.Date(2026, 6, 19, 1, 2, 3, 0, time.UTC)
	runner := &recentWindowFakeRunner{
		recordOutput: strings.Join([]string{
			"/tmp/tmux-1000/projmux",
			"repos-projmux",
			"@6",
			"agent",
			"%54",
			"codex-review",
			"Phase 4 recorder",
			"codex",
			filepath.Join(project, "internal", "app"),
		}, recentWindowFieldSep) + "\n",
		listPanesOutput: "codex-review\nClaude Code\nzsh\n",
	}
	store := &recentWindowStubStore{}
	cmd := &recentWindowCommand{
		runner: runner,
		storeFactory: func(socket string) (recentWindowStore, error) {
			if socket != "/tmp/tmux-1000/projmux" {
				t.Fatalf("store socket = %q, want tmux socket", socket)
			}
			return store, nil
		},
		now: func() time.Time { return now },
	}

	if err := cmd.RunRecord(nil, nil, nil); err != nil {
		t.Fatalf("RunRecord() error = %v", err)
	}
	if got, want := len(store.records), 1; got != want {
		t.Fatalf("records len = %d, want %d", got, want)
	}
	got := store.records[0]
	if got.Socket != "/tmp/tmux-1000/projmux" || got.Session != "repos-projmux" || got.WindowID != "@6" || got.WindowName != "agent" {
		t.Fatalf("snapshot identity = %+v, want current window identity", got)
	}
	if got.LastPaneID != "%54" || got.LastPaneTitle != "codex-review" || got.LastPaneTopic != "Phase 4 recorder" || got.LastCommand != "codex" {
		t.Fatalf("snapshot pane metadata = %+v, want active pane metadata", got)
	}
	if !reflect.DeepEqual(got.PaneTitles, []string{"codex-review", "Claude Code", "zsh"}) {
		t.Fatalf("snapshot pane titles = %+v, want all panes of the window", got.PaneTitles)
	}
	if got.Project != filepath.Base(project) {
		t.Fatalf("snapshot project = %q, want %q", got.Project, filepath.Base(project))
	}
	if got.LastFocusedAt != now {
		t.Fatalf("snapshot time = %s, want %s", got.LastFocusedAt, now)
	}
	if got, want := store.recordLimits[0], recentwindows.DefaultLimit; got != want {
		t.Fatalf("record limit = %d, want %d", got, want)
	}
}

func TestRecentWindowRecordParsesEscapedSnapshotSeparators(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux,1,0")

	runner := &recentWindowFakeRunner{
		recordOutput: strings.Join([]string{
			"/tmp/tmux",
			"current",
			"@1",
			"main",
			"%1",
			"shell",
			"topic",
			"zsh",
			"/repo/projmux",
		}, recentWindowEscapedFieldSep) + "\n",
	}
	store := &recentWindowStubStore{}
	cmd := &recentWindowCommand{
		runner: runner,
		storeFactory: func(string) (recentWindowStore, error) {
			return store, nil
		},
		now: func() time.Time { return time.Unix(0, 0) },
	}

	if err := cmd.RunRecord(nil, nil, nil); err != nil {
		t.Fatalf("RunRecord() error = %v", err)
	}
	if got := store.records[0]; got.Session != "current" || got.WindowID != "@1" || got.LastPaneTopic != "topic" {
		t.Fatalf("recorded snapshot = %+v, want escaped fields parsed", got)
	}
}

func TestRecentWindowRecordRepeatedSameWindowDoesNotGrowQueue(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux,1,0")

	stateStore := recentwindows.NewStore(filepath.Join(t.TempDir(), "recent.json"))
	runner := &recentWindowFakeRunner{
		recordOutput: strings.Join([]string{
			"/tmp/tmux",
			"current",
			"@1",
			"main",
			"%1",
			"shell",
			"",
			"zsh",
			"/repo/projmux",
		}, recentWindowFieldSep) + "\n",
	}
	cmd := &recentWindowCommand{
		runner: runner,
		storeFactory: func(string) (recentWindowStore, error) {
			return stateStore, nil
		},
		now: func() time.Time { return time.Unix(10, 0) },
	}

	for i := range 3 {
		if err := cmd.RunRecord(nil, nil, nil); err != nil {
			t.Fatalf("RunRecord(%d) error = %v", i, err)
		}
	}
	state, err := stateStore.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got, want := len(state.Entries), 1; got != want {
		t.Fatalf("entries len = %d, want no duplicate growth", got)
	}
}

func TestRecentWindowSwitchFailureRefreshesAndPrunesQueue(t *testing.T) {
	t.Parallel()

	stateStore := recentwindows.NewStore(t.TempDir() + "/recent.json")
	target := recentwindows.Snapshot{
		Socket:        "/tmp/tmux",
		Session:       "gone-project",
		WindowID:      "@7",
		WindowName:    "gone",
		LastPaneTitle: "old",
		LastFocusedAt: time.Unix(20, 0),
	}
	if _, err := stateStore.Record(target, 0); err != nil {
		t.Fatalf("record target: %v", err)
	}
	runner := &recentWindowFakeRunner{
		currentOutput: "/tmp/tmux" + recentWindowFieldSep + "current" + recentWindowFieldSep + "@1\n",
		listOutputs: []string{
			"gone-project" + recentWindowFieldSep + "@7\n",
			"",
		},
	}
	cmd := &recentWindowCommand{
		runner: runner,
		opener: &recentWindowStubOpener{err: errors.New("target gone")},
		storeFactory: func(string) (recentWindowStore, error) {
			return stateStore, nil
		},
		nativePicker: pickerRunnerFunc(func(intpicker.Options) (intpicker.Result, error) {
			return intpicker.Result{Key: "enter", Value: "gone-project" + recentWindowFieldSep + "@7"}, nil
		}),
		now: func() time.Time { return time.Unix(30, 0) },
	}

	if err := cmd.Run(nil, nil, nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !runner.sawDisplayMessage("recent window unavailable: gone (gone-project)") {
		t.Fatalf("calls = %#v, want unavailable display-message", runner.calls)
	}
	state, err := stateStore.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(state.Entries) != 0 {
		t.Fatalf("state entries = %+v, want target pruned after refresh", state.Entries)
	}
}

func TestRecentWindowGoneCandidatePrunedBeforePicker(t *testing.T) {
	t.Parallel()

	stateStore := recentwindows.NewStore(t.TempDir() + "/recent.json")
	for _, snapshot := range []recentwindows.Snapshot{
		{Socket: "/tmp/tmux", Session: "gone", WindowID: "@2", WindowName: "gone", LastFocusedAt: time.Unix(20, 0)},
		{Socket: "/tmp/tmux", Session: "alive", WindowID: "@3", WindowName: "alive", LastFocusedAt: time.Unix(10, 0)},
	} {
		if _, err := stateStore.Record(snapshot, 0); err != nil {
			t.Fatalf("Record(%s) error = %v", snapshot.Session, err)
		}
	}
	runner := &recentWindowFakeRunner{
		currentOutput: "/tmp/tmux" + recentWindowFieldSep + "current" + recentWindowFieldSep + "@1\n",
		listOutputs: "current" + recentWindowFieldSep + "@1\n" +
			"alive" + recentWindowFieldSep + "@3\n",
	}
	opener := &recentWindowStubOpener{}
	var pickerOptions intpicker.Options
	cmd := &recentWindowCommand{
		runner: runner,
		opener: opener,
		storeFactory: func(string) (recentWindowStore, error) {
			return stateStore, nil
		},
		nativePicker: pickerRunnerFunc(func(options intpicker.Options) (intpicker.Result, error) {
			pickerOptions = options
			return intpicker.Result{Key: "enter", Value: "alive" + recentWindowFieldSep + "@3"}, nil
		}),
		now: func() time.Time { return time.Unix(30, 0) },
	}

	if err := cmd.Run(nil, nil, nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(pickerOptions.Items) != 1 || !strings.Contains(recentWindowStripANSI(pickerOptions.Items[0].Title), "alive") {
		t.Fatalf("picker items = %#v, want only alive candidate", pickerOptions.Items)
	}
	if opener.session != "alive" || opener.window != "@3" {
		t.Fatalf("opener = %q %q, want alive @3", opener.session, opener.window)
	}
	state, err := stateStore.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(state.Entries) != 1 || state.Entries[0].Session != "alive" {
		t.Fatalf("state entries = %+v, want gone pruned", state.Entries)
	}
}

func recentWindowCandidate(snapshot recentwindows.Snapshot) recentwindows.Candidate {
	return recentwindows.Candidate{
		Snapshot: snapshot,
		Label:    recentwindows.BuildLabel(snapshot),
	}
}

type recentWindowStubStore struct {
	candidates   []recentwindows.Candidate
	currents     []recentwindows.WindowKey
	lives        [][]recentwindows.LiveWindow
	records      []recentwindows.Snapshot
	recordLimits []int
}

func (s *recentWindowStubStore) Candidates(current recentwindows.WindowKey, live []recentwindows.LiveWindow, _ int) ([]recentwindows.Candidate, error) {
	s.currents = append(s.currents, current)
	s.lives = append(s.lives, append([]recentwindows.LiveWindow(nil), live...))
	return s.candidates, nil
}

func (s *recentWindowStubStore) Record(snapshot recentwindows.Snapshot, limit int) (recentwindows.State, error) {
	s.records = append(s.records, snapshot)
	s.recordLimits = append(s.recordLimits, limit)
	return recentwindows.NewState(s.records), nil
}

type recentWindowStubOpener struct {
	session string
	window  string
	pane    string
	err     error
}

func (o *recentWindowStubOpener) OpenSessionTarget(_ context.Context, sessionName, windowIndex, paneIndex string) error {
	o.session = sessionName
	o.window = windowIndex
	o.pane = paneIndex
	return o.err
}

type recentWindowFakeRunner struct {
	currentOutput   string
	recordOutput    string
	listPanesOutput string
	listOutputs     any
	calls           []recentWindowCall
}

type recentWindowCall struct {
	name string
	args []string
}

func (r *recentWindowFakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, recentWindowCall{name: name, args: append([]string(nil), args...)})
	if name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-F", strings.Join([]string{"#{socket_path}", "#{session_name}", "#{window_id}"}, recentWindowFieldSep)}) {
		return []byte(r.currentOutput), nil
	}
	if name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-F", strings.Join([]string{"#{socket_path}", "#{session_name}", "#{window_id}", "#{window_name}", "#{pane_id}", "#{pane_title}", "#{@projmux_ai_topic}", "#{pane_current_command}", "#{pane_current_path}"}, recentWindowFieldSep)}) {
		return []byte(r.recordOutput), nil
	}
	if name == "tmux" && len(args) == 5 && args[0] == "list-panes" && args[1] == "-t" && args[3] == "-F" && args[4] == "#{pane_title}" {
		return []byte(r.listPanesOutput), nil
	}
	if name == "tmux" && reflect.DeepEqual(args, []string{"list-windows", "-a", "-F", strings.Join([]string{"#{session_name}", "#{window_id}"}, recentWindowFieldSep)}) {
		switch outputs := r.listOutputs.(type) {
		case []string:
			if len(outputs) == 0 {
				return nil, nil
			}
			out := outputs[0]
			r.listOutputs = outputs[1:]
			return []byte(out), nil
		case string:
			return []byte(outputs), nil
		default:
			return nil, nil
		}
	}
	if name == "tmux" && len(args) == 2 && args[0] == "display-message" {
		return nil, nil
	}
	return nil, nil
}

func (r *recentWindowFakeRunner) sawDisplayMessage(message string) bool {
	return r.sawCall("tmux", "display-message", message)
}

func (r *recentWindowFakeRunner) sawCall(name string, args ...string) bool {
	for _, call := range r.calls {
		if call.name == name && reflect.DeepEqual(call.args, args) {
			return true
		}
	}
	return false
}
