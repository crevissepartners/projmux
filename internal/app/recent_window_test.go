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

	"github.com/crevissepartners/projmux/internal/core/aibadge"
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
	}, at.Add(12*time.Second), aibadge.StyleDot)

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
	for _, want := range []string{"codex-review", "12s ago", "Projmux"} {
		if !strings.Contains(text, want) {
			t.Fatalf("render text = %q, want %q", text, want)
		}
	}
	// The session unique id is dropped from the visible card (deduped against the
	// project badge) but must remain searchable.
	if strings.Contains(text, "repos-projmux") {
		t.Fatalf("render text = %q, want no visible session id line", text)
	}
	if !strings.Contains(item.SearchText, "repos-projmux") {
		t.Fatalf("SearchText = %q, want session id searchable", item.SearchText)
	}
}

func TestRecentWindowPickerItemHasNoContextLine(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 6, 18, 1, 2, 3, 0, time.UTC)
	snapshot := recentwindows.Snapshot{
		Session:    "repos-projmux",
		WindowID:   "@6",
		WindowName: "projmux",
		Project:    "Projmux",
		PaneTitles: []string{"zsh"},
	}
	item := recentWindowPickerItem(recentWindowCandidate(snapshot), at, aibadge.StyleDot)

	if got := recentWindowStripANSI(item.Title); !strings.Contains(got, "projmux") {
		t.Fatalf("line 1 visible = %q, want readable window name", got)
	}
	// The default card ends at three lines: title + at most two MetaLines (pane
	// preview, last-visit). The deduped "project · session" context line and any
	// session-id line must be gone.
	if len(item.MetaLines) > 2 {
		t.Fatalf("MetaLines = %#v, want at most two lines (pane preview, last visit)", item.MetaLines)
	}
	for _, line := range item.MetaLines {
		visible := recentWindowStripANSI(line)
		if visible == "Projmux · repos-projmux" {
			t.Fatalf("MetaLines = %#v, want no repeated project/session context line", item.MetaLines)
		}
		if strings.Contains(visible, "repos-projmux") {
			t.Fatalf("MetaLines = %#v, want no visible session id", item.MetaLines)
		}
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
	item := recentWindowPickerItem(recentWindowCandidate(snapshot), at, aibadge.StyleDot)

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
	item := recentWindowPickerItem(recentWindowCandidate(snapshot), at, aibadge.StyleDot)

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
	item := recentWindowPickerItem(recentWindowCandidate(snapshot), at, aibadge.StyleDot)

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

func TestRecentWindowPaneSummaryUsesLabelTopicShellTitleOrder(t *testing.T) {
	t.Parallel()

	candidate := recentwindows.Candidate{Snapshot: recentwindows.Snapshot{
		PaneTitles:   []string{"raw one", "raw two", "raw three", "raw four", "raw five"},
		PaneLabels:   []string{"user label", "", "", "", ""},
		PaneAgents:   []string{"", "codex", "", "", ""},
		PaneTopics:   []string{"AI hidden", "AI topic", "", "", "orphan topic"},
		PaneCommands: []string{"zsh", "codex", "fish", "nvim", "nvim"},
	}}
	if got, want := recentWindowPaneSummary(candidate), "user label | AI topic | fish | raw four | raw five"; got != want {
		t.Fatalf("recentWindowPaneSummary() = %q, want %q", got, want)
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
	item := recentWindowPickerItem(recentWindowCandidate(snapshot), at.Add(3*time.Minute), aibadge.StyleDot)

	wantDate := at.Local().Format("2006-01-02 15:04")
	want := "last visit · 3m ago · " + wantDate
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

func TestRecentWindowFocusDateUsesLocalTimezone(t *testing.T) {
	// No t.Parallel(): this mutates the global time.Local.
	loc, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		t.Skipf("Asia/Seoul tzdata unavailable: %v", err)
	}
	orig := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = orig })

	// 01:02 UTC == 10:02 KST.
	at := time.Date(2026, 6, 18, 1, 2, 3, 0, time.UTC)
	if got, want := recentWindowFocusDate(at), "2026-06-18 10:02"; got != want {
		t.Fatalf("recentWindowFocusDate(%s) = %q, want %q (local TZ)", at, got, want)
	}
}

func TestRecentWindowPickerItemPaneSummaryIsFlatNoChip(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 6, 18, 1, 2, 3, 0, time.UTC)
	// Each pane lists at the same hierarchy joined by " | " in display order.
	// An AI pane shows its OWN topic; non-AI panes show their title. There is no
	// leading topic chip and no grey dim decoration.
	snapshot := recentwindows.Snapshot{
		Session:        "repos-projmux",
		WindowID:       "@6",
		WindowName:     "projmux",
		PaneTitles:     []string{"zsh", "Claude Code"},
		PaneBadgeKinds: []string{"", "in_progress"},
		PaneAgents:     []string{"", "codex"},
		PaneTopics:     []string{"", "Phase 6 polish"},
	}
	item := recentWindowPickerItem(recentWindowCandidate(snapshot), at, aibadge.StyleDot)

	if len(item.MetaLines) == 0 {
		t.Fatalf("MetaLines = %#v, want a pane summary line", item.MetaLines)
	}
	visible := recentWindowStripANSI(item.MetaLines[0])
	// zsh (non-AI, its title) leads; the AI pane shows its own topic, not "Claude Code".
	if want := "zsh | "; !strings.HasPrefix(visible, want) {
		t.Fatalf("pane summary line = %q, want display-order panes joined with ' | '", visible)
	}
	if !strings.Contains(visible, "Phase 6 polish") {
		t.Fatalf("pane summary line = %q, want AI pane shown by its own topic", visible)
	}
	if strings.Contains(visible, "Claude Code") {
		t.Fatalf("pane summary line = %q, want AI pane's topic preferred over its title", visible)
	}
	// No active chip palette and no grey dim decoration on line 2.
	if strings.Contains(item.MetaLines[0], theme.ANSIChipActiveStart) {
		t.Fatalf("pane summary line = %q, want NO topic chip palette", item.MetaLines[0])
	}
	if strings.Contains(item.MetaLines[0], theme.ANSINotifyDimStart) {
		t.Fatalf("pane summary line = %q, want NO grey dim decoration", item.MetaLines[0])
	}
}

func TestRecentWindowPickerItemRendersPerPaneAIBadges(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 6, 18, 1, 2, 3, 0, time.UTC)
	snapshot := recentwindows.Snapshot{
		Session:        "repos-projmux",
		WindowID:       "@6",
		WindowName:     "projmux",
		PaneTitles:     []string{"Claude Code", "Codex", "zsh"},
		PaneBadgeKinds: []string{"in_progress", "response_complete", ""},
	}

	// Dot style: recognized kinds render a themed "●" glyph in front of the pane.
	dot := recentWindowPickerItem(recentWindowCandidate(snapshot), at, aibadge.StyleDot)
	if len(dot.MetaLines) == 0 {
		t.Fatalf("MetaLines = %#v, want a pane summary line", dot.MetaLines)
	}
	line := dot.MetaLines[0]
	if !strings.Contains(line, theme.ANSIAIBadgeProgressStart+"●") {
		t.Fatalf("pane summary = %q, want in_progress dot badge", line)
	}
	if !strings.Contains(line, theme.ANSIAIBadgeSuccessStart+"●") {
		t.Fatalf("pane summary = %q, want response_complete dot badge", line)
	}
	visible := recentWindowStripANSI(line)
	if !strings.Contains(visible, "Claude Code") || !strings.Contains(visible, "Codex") || !strings.Contains(visible, "zsh") {
		t.Fatalf("pane summary visible = %q, want all pane titles", visible)
	}

	// Emoji style: recognized kinds render their emoji glyph instead of the dot.
	emoji := recentWindowPickerItem(recentWindowCandidate(snapshot), at, aibadge.StyleEmoji)
	emojiLine := recentWindowStripANSI(emoji.MetaLines[0])
	if !strings.Contains(emojiLine, "🔄") || !strings.Contains(emojiLine, "✅") {
		t.Fatalf("pane summary emoji = %q, want in_progress and response_complete emoji glyphs", emojiLine)
	}

	// Off style: no glyph is rendered, only the plain dimmed titles.
	off := recentWindowPickerItem(recentWindowCandidate(snapshot), at, aibadge.StyleOff)
	offVisible := recentWindowStripANSI(off.MetaLines[0])
	if strings.ContainsAny(offVisible, "●🔄✅") {
		t.Fatalf("pane summary off = %q, want no badge glyphs", offVisible)
	}
	if want := "Claude Code | Codex | zsh"; !strings.Contains(offVisible, want) {
		t.Fatalf("pane summary off = %q, want plain titles %q", offVisible, want)
	}
}

// recentWindowPaneCellFor splits the ANSI-stripped pane summary line on " | "
// and returns the cell containing substr, so tests can assert badge↔pane
// correspondence per cell.
func recentWindowPaneCellFor(t *testing.T, line, substr string) string {
	t.Helper()
	for cell := range strings.SplitSeq(recentWindowStripANSI(line), " | ") {
		if strings.Contains(cell, substr) {
			return cell
		}
	}
	t.Fatalf("pane summary line = %q, want a cell containing %q", line, substr)
	return ""
}

func TestRecentWindowPickerItemBindsBadgeToOwnPane(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 6, 18, 1, 2, 3, 0, time.UTC)
	// Phase 8 binding guarantee preserved in the flat model: the in_progress glyph
	// must attach to "Codex" (pane 1), never to "zsh" (pane 0). Each pane cell
	// carries its own badge kind, so a glyph can never desync to the wrong pane.
	snapshot := recentwindows.Snapshot{
		Session:        "repos-projmux",
		WindowID:       "@6",
		WindowName:     "projmux",
		PaneTitles:     []string{"zsh", "Codex"},
		PaneBadgeKinds: []string{"", "in_progress"},
	}
	item := recentWindowPickerItem(recentWindowCandidate(snapshot), at, aibadge.StyleDot)
	if len(item.MetaLines) == 0 {
		t.Fatalf("MetaLines = %#v, want a pane summary line", item.MetaLines)
	}
	line := item.MetaLines[0]

	// Display order: zsh -> Codex, flat, joined by " | ".
	visible := recentWindowStripANSI(line)
	zshIdx := strings.Index(visible, "zsh")
	codexIdx := strings.Index(visible, "Codex")
	if zshIdx < 0 || codexIdx <= zshIdx {
		t.Fatalf("pane summary line = %q, want order zsh -> Codex", visible)
	}

	// The in_progress themed glyph belongs to Codex, NOT zsh.
	dot := theme.ANSIAIBadgeProgressStart + "●"
	codexCell := recentWindowPaneCellFor(t, line, "Codex")
	zshCell := recentWindowPaneCellFor(t, line, "zsh")
	codexFull, zshFull := "", ""
	for cell := range strings.SplitSeq(line, " | ") {
		switch {
		case strings.Contains(recentWindowStripANSI(cell), "Codex"):
			codexFull = cell
		case strings.Contains(recentWindowStripANSI(cell), "zsh"):
			zshFull = cell
		}
	}
	if !strings.Contains(codexFull, dot) {
		t.Fatalf("Codex cell = %q, want in_progress dot %q before Codex", codexFull, dot)
	}
	if strings.Contains(zshFull, dot) || strings.Contains(zshCell, "●") {
		t.Fatalf("zsh cell = %q, want NO badge glyph", zshFull)
	}
	if strings.Contains(codexCell, "●") == false {
		t.Fatalf("Codex cell (stripped) = %q, want a ● glyph", codexCell)
	}
}

func TestRecentWindowPickerItemAIPaneShowsOwnTopicWithBoundBadge(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 6, 18, 1, 2, 3, 0, time.UTC)
	// The AI pane (pane 1) renders its OWN ai_topic as its perceived title, with
	// its own in_progress glyph still bound to it. The non-AI pane keeps its title
	// and gets no glyph. No off-by-one: the glyph stays on the AI pane's cell.
	snapshot := recentwindows.Snapshot{
		Session:        "repos-projmux",
		WindowID:       "@6",
		WindowName:     "projmux",
		PaneTitles:     []string{"zsh", "Codex"},
		PaneBadgeKinds: []string{"", "in_progress"},
		PaneAgents:     []string{"", "codex"},
		PaneTopics:     []string{"", "Phase 8 binding"},
	}
	item := recentWindowPickerItem(recentWindowCandidate(snapshot), at, aibadge.StyleDot)
	if len(item.MetaLines) == 0 {
		t.Fatalf("MetaLines = %#v, want a pane summary line", item.MetaLines)
	}
	line := item.MetaLines[0]
	visible := recentWindowStripANSI(line)

	// The AI pane shows its topic instead of "Codex"; the non-AI pane shows "zsh".
	if !strings.Contains(visible, "zsh") || !strings.Contains(visible, "Phase 8 binding") {
		t.Fatalf("pane summary line = %q, want zsh and the AI pane's topic", visible)
	}
	if strings.Contains(visible, "Codex") {
		t.Fatalf("pane summary line = %q, want AI pane's topic preferred over its title", visible)
	}

	// The in_progress glyph stays bound to the AI pane's (topic) cell, not zsh.
	dot := theme.ANSIAIBadgeProgressStart + "●"
	topicFull, zshFull := "", ""
	for cell := range strings.SplitSeq(line, " | ") {
		switch {
		case strings.Contains(recentWindowStripANSI(cell), "Phase 8 binding"):
			topicFull = cell
		case strings.Contains(recentWindowStripANSI(cell), "zsh"):
			zshFull = cell
		}
	}
	if !strings.Contains(topicFull, dot) {
		t.Fatalf("AI topic cell = %q, want in_progress dot %q bound to the AI pane", topicFull, dot)
	}
	if strings.Contains(zshFull, dot) {
		t.Fatalf("zsh cell = %q, want NO badge glyph", zshFull)
	}
}

func TestRecentWindowPickerItemMirrorsPaneBorderVisibleLabels(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 6, 18, 1, 2, 3, 0, time.UTC)
	snapshot := recentwindows.Snapshot{
		Session:      "repos-projmux",
		WindowID:     "@6",
		WindowName:   "projmux",
		PaneTitles:   []string{"feature/local-branch-title", "Codex"},
		PaneAgents:   []string{"", "codex"},
		PaneTopics:   []string{"", "[lead:ship] border geometry spike"},
		PaneCommands: []string{"zsh", "codex"},
	}
	item := recentWindowPickerItem(recentWindowCandidate(snapshot), at, aibadge.StyleDot)
	if len(item.MetaLines) == 0 {
		t.Fatalf("MetaLines = %#v, want a pane summary line", item.MetaLines)
	}
	visible := recentWindowStripANSI(item.MetaLines[0])

	if !strings.Contains(visible, "zsh") {
		t.Fatalf("pane summary line = %q, want shell pane shown by current command", visible)
	}
	if strings.Contains(visible, "feature/local-branch-title") {
		t.Fatalf("pane summary line = %q, want known shell command before raw pane title", visible)
	}
	if !strings.Contains(visible, "[lead:ship] border geometry spike") {
		t.Fatalf("pane summary line = %q, want pane AI topic as visible label", visible)
	}
	if strings.Contains(visible, "Codex") {
		t.Fatalf("pane summary line = %q, want AI topic before raw pane title", visible)
	}
}

func TestRecentWindowPickerItemCollapsesExtraPanesIntoCount(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 6, 18, 1, 2, 3, 0, time.UTC)
	snapshot := recentwindows.Snapshot{
		Session:    "repos-projmux",
		WindowID:   "@6",
		WindowName: "projmux",
		PaneTitles: []string{"one", "two", "three", "four", "five"},
	}
	item := recentWindowPickerItem(recentWindowCandidate(snapshot), at, aibadge.StyleDot)

	if len(item.MetaLines) == 0 {
		t.Fatalf("MetaLines = %#v, want a pane summary line", item.MetaLines)
	}
	visible := recentWindowStripANSI(item.MetaLines[0])
	// First recentWindowMaxPanes (4) render; the remaining one collapses into "+1".
	if want := "one | two | three | four | +1"; !strings.Contains(visible, want) {
		t.Fatalf("pane summary = %q, want first %d panes then %q", visible, recentWindowMaxPanes, "+1")
	}
	if strings.Contains(visible, "five") {
		t.Fatalf("pane summary = %q, want overflow panes collapsed, not listed", visible)
	}
	// All pane titles must still be searchable even when collapsed on screen.
	for _, want := range []string{"four", "five"} {
		if !strings.Contains(item.SearchText, want) {
			t.Fatalf("SearchText = %q, want overflow pane %q searchable", item.SearchText, want)
		}
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
	item := recentWindowPickerItem(recentWindowCandidate(snapshot), at.Add(2*time.Minute), aibadge.StyleDot)

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
	item := recentWindowPickerItem(recentWindowCandidate(snapshot), at.Add(time.Minute), aibadge.StyleDot)
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
	item := recentWindowPickerItem(recentWindowCandidate(snapshot), at.Add(time.Minute), aibadge.StyleDot)

	wantDate := at.Local().Format("2006-01-02 15:04")
	for _, want := range []string{"projmux", "Projmux", "repos-projmux", "zsh", "Claude Code", "Codex", "roadmap", "codex", wantDate} {
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
	items, byValue, _ := recentWindowPickerItems([]recentwindows.Candidate{candidate}, at, aibadge.StyleDot)

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

func TestRecentWindowPickerItemsInitialIndexFirstNonCurrentRow(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 6, 18, 1, 2, 3, 0, time.UTC)
	// Display (MRU) order: current row leads, then two non-current rows. The
	// cursor must default to the FIRST non-current row (index 1 here), not the
	// current row at index 0.
	current := recentwindows.Candidate{
		Snapshot:  recentwindows.Snapshot{Session: "current", WindowID: "@1", WindowName: "current"},
		IsCurrent: true,
	}
	other := recentWindowCandidate(recentwindows.Snapshot{Session: "other", WindowID: "@2", WindowName: "other"})
	third := recentWindowCandidate(recentwindows.Snapshot{Session: "third", WindowID: "@3", WindowName: "third"})

	_, _, initialIndex := recentWindowPickerItems([]recentwindows.Candidate{current, other, third}, at, aibadge.StyleDot)
	if initialIndex != 1 {
		t.Fatalf("initialIndex = %d, want 1 (first non-current row)", initialIndex)
	}

	options := recentWindowPickerOptions(make([]intpicker.Item, 3), initialIndex, nil, nil)
	if !options.InitialIndexSet || options.InitialIndex != 1 {
		t.Fatalf("options InitialIndex = %d/%t, want 1/true", options.InitialIndex, options.InitialIndexSet)
	}
}

func TestRecentWindowPickerItemsInitialIndexCurrentOnlyStaysOnCurrent(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 6, 18, 1, 2, 3, 0, time.UTC)
	current := recentwindows.Candidate{
		Snapshot:  recentwindows.Snapshot{Session: "current", WindowID: "@1", WindowName: "current"},
		IsCurrent: true,
	}

	_, _, initialIndex := recentWindowPickerItems([]recentwindows.Candidate{current}, at, aibadge.StyleDot)
	if initialIndex != -1 {
		t.Fatalf("initialIndex = %d, want -1 (no non-current row)", initialIndex)
	}

	// A negative index leaves the cursor on index 0 and does NOT set InitialIndexSet.
	options := recentWindowPickerOptions(make([]intpicker.Item, 1), initialIndex, nil, nil)
	if options.InitialIndexSet || options.InitialIndex != 0 {
		t.Fatalf("options InitialIndex = %d/%t, want 0/false (stay on current row)", options.InitialIndex, options.InitialIndexSet)
	}
}

func TestRecentWindowPickerCloseBindingsUseRecentWindowsAlias(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	keymapPath := filepath.Join(home, ".config", "projmux", "keymap.toml")
	if err := os.MkdirAll(filepath.Dir(keymapPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keymapPath, []byte(`[bindings."RecentWindows:Open"]
keys = ["M-r"]
[bindings.AISplitPickerToggle]
keys = ["M-a"]
[bindings.new-window]
keys = ["M-t"]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	options := recentWindowPickerOptions(nil, -1, func() (string, error) { return home, nil }, func(string) string { return "" })
	bindings := compatOptionsFromNativePickerForTest(options).Bindings
	if !containsString(bindings, "alt-r:abort") {
		t.Fatalf("recent windows bindings = %#v, want custom RecentWindows:Open alias close", bindings)
	}
	if containsString(bindings, "alt-a:abort") {
		t.Fatalf("recent windows bindings = %#v, AI picker alias must not close recent windows popup", bindings)
	}
	if containsString(bindings, "alt-t:abort") {
		t.Fatalf("recent windows bindings = %#v, direct command alias must not close popup", bindings)
	}
}

func TestRecentWindowPickerItemFallsBackToTitleWhenAgentAbsent(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 6, 18, 1, 2, 3, 0, time.UTC)
	snapshot := recentwindows.Snapshot{
		Session:        "repos-projmux",
		WindowID:       "@6",
		WindowName:     "projmux",
		PaneTitles:     []string{"Codex"},
		PaneBadgeKinds: []string{"in_progress"},
		PaneTopics:     []string{"orphan topic"},
	}
	item := recentWindowPickerItem(recentWindowCandidate(snapshot), at, aibadge.StyleDot)
	if len(item.MetaLines) == 0 {
		t.Fatalf("MetaLines = %#v, want a pane summary line", item.MetaLines)
	}
	visible := recentWindowStripANSI(item.MetaLines[0])
	if !strings.Contains(visible, "Codex") || strings.Contains(visible, "orphan topic") {
		t.Fatalf("pane summary line = %q, want raw-title fallback without agent synthesis", visible)
	}
}

func TestRecentWindowPickerItemSearchTextIncludesPaneTopics(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 6, 18, 1, 2, 3, 0, time.UTC)
	snapshot := recentwindows.Snapshot{
		Session:       "repos-projmux",
		WindowID:      "@6",
		WindowName:    "projmux",
		Project:       "Projmux",
		PaneTitles:    []string{"zsh", "Codex"},
		PaneTopics:    []string{"", "Phase 9 search"},
		LastFocusedAt: at,
	}
	item := recentWindowPickerItem(recentWindowCandidate(snapshot), at, aibadge.StyleDot)
	if !strings.Contains(item.SearchText, "Phase 9 search") {
		t.Fatalf("SearchText = %q, want per-pane topic searchable", item.SearchText)
	}
}

func TestRecentWindowPickerItemHasNoCurrentBadge(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 6, 18, 1, 2, 3, 0, time.UTC)
	snapshot := recentwindows.Snapshot{
		Session:    "repos-projmux",
		WindowID:   "@6",
		WindowName: "projmux",
		Project:    "Projmux",
	}
	current := recentwindows.Candidate{
		Snapshot:  snapshot,
		Label:     recentwindows.BuildLabel(snapshot),
		IsCurrent: true,
	}
	item := recentWindowPickerItem(current, at, aibadge.StyleDot)

	// Agreed policy: the current window stays in history as a normal card with NO
	// CURRENT badge — neither in the visible Title nor in SearchText.
	if strings.Contains(recentWindowStripANSI(item.Title), "CURRENT") {
		t.Fatalf("current Title = %q, want no CURRENT marker", item.Title)
	}
	if strings.Contains(item.SearchText, "CURRENT") {
		t.Fatalf("current SearchText = %q, want no CURRENT marker", item.SearchText)
	}
	// The card still renders the readable window name and stays searchable by the
	// session unique id.
	if !strings.Contains(recentWindowStripANSI(item.Title), "projmux") {
		t.Fatalf("current Title = %q, want readable window name", item.Title)
	}
	for _, want := range []string{"Projmux", "repos-projmux", "projmux"} {
		if !strings.Contains(item.SearchText, want) {
			t.Fatalf("SearchText = %q, want substring %q", item.SearchText, want)
		}
	}

	// A non-current candidate also carries no CURRENT marker.
	plain := recentWindowPickerItem(recentWindowCandidate(snapshot), at, aibadge.StyleDot)
	if strings.Contains(recentWindowStripANSI(plain.Title), "CURRENT") {
		t.Fatalf("non-current Title = %q, want no CURRENT marker", plain.Title)
	}
}

func TestRecentWindowRunCurrentRowIsNoOp(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux,1,0")

	currentCandidate := recentwindows.Candidate{
		Snapshot: recentwindows.Snapshot{
			Socket:     "/tmp/tmux",
			Session:    "current",
			WindowID:   "@1",
			WindowName: "current-window",
		},
		Label:     recentwindows.BuildLabel(recentwindows.Snapshot{Session: "current", WindowID: "@1", WindowName: "current-window"}),
		IsCurrent: true,
	}
	store := &recentWindowStubStore{candidates: []recentwindows.Candidate{currentCandidate}}
	runner := &recentWindowFakeRunner{
		currentOutput: "/tmp/tmux" + recentWindowFieldSep + "current" + recentWindowFieldSep + "@1\n",
		listOutputs:   "current" + recentWindowFieldSep + "@1\n",
	}
	opener := &recentWindowStubOpener{}
	cmd := &recentWindowCommand{
		runner: runner,
		opener: opener,
		storeFactory: func(string) (recentWindowStore, error) {
			return store, nil
		},
		nativePicker: pickerRunnerFunc(func(intpicker.Options) (intpicker.Result, error) {
			return intpicker.Result{Key: "enter", Value: recentWindowValue(currentCandidate)}, nil
		}),
		now: func() time.Time { return time.Unix(0, 0) },
	}

	if err := cmd.Run(nil, nil, nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, call := range runner.calls {
		if call.name == "tmux" && len(call.args) >= 1 && call.args[0] == "switch-client" {
			t.Fatalf("calls = %#v, want no switch-client for the current row", runner.calls)
		}
	}
	if opener.session != "" || opener.window != "" {
		t.Fatalf("opener = %q %q, want opener never called for the current row", opener.session, opener.window)
	}
	if runner.sawDisplayMessage("recent window unavailable: " + recentWindowTargetLabel(currentCandidate)) {
		t.Fatalf("calls = %#v, want no unavailable display-message for the current row", runner.calls)
	}
}

func TestRecentWindowRunCurrentOnlyStateOpensPicker(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux,1,0")

	currentCandidate := recentwindows.Candidate{
		Snapshot: recentwindows.Snapshot{
			Socket:     "/tmp/tmux",
			Session:    "current",
			WindowID:   "@1",
			WindowName: "current-window",
		},
		Label:     recentwindows.BuildLabel(recentwindows.Snapshot{Session: "current", WindowID: "@1", WindowName: "current-window"}),
		IsCurrent: true,
	}
	store := &recentWindowStubStore{candidates: []recentwindows.Candidate{currentCandidate}}
	runner := &recentWindowFakeRunner{
		currentOutput: "/tmp/tmux" + recentWindowFieldSep + "current" + recentWindowFieldSep + "@1\n",
		listOutputs:   "current" + recentWindowFieldSep + "@1\n",
	}
	opener := &recentWindowStubOpener{}
	var pickerOptions intpicker.Options
	var pickerCalled bool
	cmd := &recentWindowCommand{
		runner: runner,
		opener: opener,
		storeFactory: func(string) (recentWindowStore, error) {
			return store, nil
		},
		nativePicker: pickerRunnerFunc(func(options intpicker.Options) (intpicker.Result, error) {
			pickerCalled = true
			pickerOptions = options
			return intpicker.Result{Key: "enter", Value: recentWindowValue(currentCandidate)}, nil
		}),
		now: func() time.Time { return time.Unix(0, 0) },
	}

	if err := cmd.Run(nil, nil, nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !pickerCalled {
		t.Fatal("picker was not called for the current-only state")
	}
	if len(pickerOptions.Items) != 1 {
		t.Fatalf("picker items = %#v, want exactly the current candidate", pickerOptions.Items)
	}
	if runner.sawDisplayMessage("no recent windows") {
		t.Fatalf("calls = %#v, want no 'no recent windows' message for current-only state", runner.calls)
	}
	for _, call := range runner.calls {
		if call.name == "tmux" && len(call.args) >= 1 && call.args[0] == "switch-client" {
			t.Fatalf("calls = %#v, want no switch for the current-only state", runner.calls)
		}
	}
	if opener.session != "" {
		t.Fatalf("opener = %q, want opener never called", opener.session)
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
			"review focus",
			"codex",
			"Phase 4 recorder",
			"codex",
			filepath.Join(project, "internal", "app"),
			"",
		}, recentWindowFieldSep) + "\n",
		listPanesOutput: strings.Join([]string{"codex-review", "picker label", "in_progress", "codex", "Phase 9 picker", "codex"}, recentWindowFieldSep) + "\n" +
			strings.Join([]string{"Claude Code", "", "response_complete", "claude", "Recent windows queue", "claude"}, recentWindowFieldSep) + "\n" +
			strings.Join([]string{"branch-title", "", "", "", "", "zsh"}, recentWindowFieldSep) + "\n",
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
	if got.LastPaneID != "%54" || got.LastPaneTitle != "codex-review" || got.LastPaneLabel != "review focus" || got.LastPaneAgent != "codex" || got.LastPaneTopic != "Phase 4 recorder" || got.LastCommand != "codex" {
		t.Fatalf("snapshot pane metadata = %+v, want active pane metadata", got)
	}
	if !reflect.DeepEqual(got.PaneTitles, []string{"codex-review", "Claude Code", "branch-title"}) {
		t.Fatalf("snapshot pane titles = %+v, want all panes of the window", got.PaneTitles)
	}
	if !reflect.DeepEqual(got.PaneLabels, []string{"picker label", "", ""}) {
		t.Fatalf("snapshot pane labels = %+v, want per-pane user labels aligned with titles", got.PaneLabels)
	}
	if !reflect.DeepEqual(got.PaneBadgeKinds, []string{"in_progress", "response_complete", ""}) {
		t.Fatalf("snapshot pane badge kinds = %+v, want per-pane AI badge kinds aligned with titles", got.PaneBadgeKinds)
	}
	if !reflect.DeepEqual(got.PaneAgents, []string{"codex", "claude", ""}) {
		t.Fatalf("snapshot pane agents = %+v, want aligned agents", got.PaneAgents)
	}
	if !reflect.DeepEqual(got.PaneTopics, []string{"Phase 9 picker", "Recent windows queue", ""}) {
		t.Fatalf("snapshot pane topics = %+v, want per-pane AI topics aligned with titles", got.PaneTopics)
	}
	if !reflect.DeepEqual(got.PaneCommands, []string{"codex", "claude", "zsh"}) {
		t.Fatalf("snapshot pane commands = %+v, want per-pane commands aligned with titles", got.PaneCommands)
	}
	// Anchor-less snapshot (empty @projmux_project_path): the regular-repo pane
	// cwd basename is untrusted (it may be a drifted foreign repo), so the badge
	// source stays the session identity, not the cwd basename (#493 drift
	// guarantee). The display label is de-slugged the same way statusbar/switch
	// are, so "repos-projmux" surfaces as "projmux" rather than the raw slug.
	if got.Project != "projmux" {
		t.Fatalf("snapshot project = %q, want %q (de-slugged session identity for anchor-less cwd)", got.Project, "projmux")
	}
	if got.LastFocusedAt != now {
		t.Fatalf("snapshot time = %s, want %s", got.LastFocusedAt, now)
	}
	// Persisted state must stay UTC regardless of display-time local conversion.
	if loc := got.LastFocusedAt.Location(); loc != time.UTC {
		t.Fatalf("snapshot time location = %v, want UTC", loc)
	}
	if formatted := got.LastFocusedAt.Format(time.RFC3339); !strings.HasSuffix(formatted, "Z") {
		t.Fatalf("snapshot time RFC3339 = %q, want UTC 'Z' suffix", formatted)
	}
	if got, want := store.recordLimits[0], recentwindows.DefaultLimit; got != want {
		t.Fatalf("record limit = %d, want %d", got, want)
	}
}

func TestRecentWindowRecordUsesSessionAnchorProject(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux,1,0")

	now := time.Date(2026, 7, 1, 1, 2, 3, 0, time.UTC)
	runner := &recentWindowFakeRunner{
		recordOutput: strings.Join([]string{
			"/tmp/tmux",
			"repos-projmux",
			"@6",
			"agent",
			"%54",
			"shell",
			"topic",
			"zsh",
			// Live pane cwd drifted deep into a subdir with no project marker.
			"/home/es5h/source/repos/projmux/internal/core/recentwindows",
			// Session anchor pins the project root.
			"/home/es5h/source/repos/projmux",
		}, recentWindowFieldSep) + "\n",
	}
	store := &recentWindowStubStore{}
	cmd := &recentWindowCommand{
		runner:       runner,
		storeFactory: func(string) (recentWindowStore, error) { return store, nil },
		now:          func() time.Time { return now },
	}

	if err := cmd.RunRecord(nil, nil, nil); err != nil {
		t.Fatalf("RunRecord() error = %v", err)
	}
	if got := store.records[0].Project; got != "projmux" {
		t.Fatalf("snapshot project = %q, want %q (session anchor basename, cwd-drift independent)", got, "projmux")
	}
}

// TestRecentWindowRecordDeSlugsAnchorlessSessionBadge pins the recent-windows
// badge to the SAME de-slug rule the statusbar and switch sidebar apply: an
// anchor-less session named "repos-donus-db" must surface as "donus-db", not
// the raw "repos-donus-db" slug. The badge routes through
// resolveProjectDisplayName, so the reduction only fires when the resolver falls
// through to the session name.
func TestRecentWindowRecordDeSlugsAnchorlessSessionBadge(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux,1,0")

	now := time.Date(2026, 7, 7, 1, 2, 3, 0, time.UTC)
	runner := &recentWindowFakeRunner{
		recordOutput: strings.Join([]string{
			"/tmp/tmux",
			"repos-donus-db",
			"@6",
			"agent",
			"%54",
			"shell",
			"topic",
			"zsh",
			// No project marker on the pane cwd path.
			"/home/es5h/nowhere",
			// No session anchor (@projmux_project_path empty): older/foreign session.
			"",
		}, recentWindowFieldSep) + "\n",
	}
	store := &recentWindowStubStore{}
	cmd := &recentWindowCommand{
		runner:       runner,
		storeFactory: func(string) (recentWindowStore, error) { return store, nil },
		now:          func() time.Time { return now },
	}

	if err := cmd.RunRecord(nil, nil, nil); err != nil {
		t.Fatalf("RunRecord() error = %v", err)
	}
	if got := store.records[0].Project; got != "donus-db" {
		t.Fatalf("snapshot project = %q, want %q (de-slugged session badge matching statusbar/switch)", got, "donus-db")
	}
}

// TestRecentWindowRecordDoesNotOverCutHyphenatedAnchorName guards the over-cut
// regression: the de-slug is a lossy cut-at-first-dash meant only for session
// slugs. A real hyphenated project name coming from the session ANCHOR must NOT
// be reduced, so "my-app" stays "my-app" instead of collapsing to "app". This
// is exactly why the badge routes through resolveProjectDisplayName (de-slug
// only when Source==SessionName) rather than DeSlug(Resolve(...).Name).
func TestRecentWindowRecordDoesNotOverCutHyphenatedAnchorName(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux,1,0")

	now := time.Date(2026, 7, 7, 1, 2, 3, 0, time.UTC)
	runner := &recentWindowFakeRunner{
		recordOutput: strings.Join([]string{
			"/tmp/tmux",
			"repos-my-app",
			"@6",
			"agent",
			"%54",
			"shell",
			"topic",
			"zsh",
			"/home/es5h/source/repos/my-app",
			// Session anchor pins a real hyphenated project root.
			"/home/es5h/source/repos/my-app",
		}, recentWindowFieldSep) + "\n",
	}
	store := &recentWindowStubStore{}
	cmd := &recentWindowCommand{
		runner:       runner,
		storeFactory: func(string) (recentWindowStore, error) { return store, nil },
		now:          func() time.Time { return now },
	}

	if err := cmd.RunRecord(nil, nil, nil); err != nil {
		t.Fatalf("RunRecord() error = %v", err)
	}
	if got := store.records[0].Project; got != "my-app" {
		t.Fatalf("snapshot project = %q, want %q (anchor basename must not be de-slug over-cut)", got, "my-app")
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
			"",
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
			"",
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
	if name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-F", strings.Join([]string{"#{socket_path}", "#{session_name}", "#{window_id}", "#{window_name}", "#{pane_id}", "#{pane_title}", "#{@projmux_pane_label}", "#{@projmux_ai_agent}", "#{@projmux_ai_topic}", "#{pane_current_command}", "#{pane_current_path}", "#{@projmux_project_path}"}, recentWindowFieldSep)}) {
		return []byte(r.recordOutput), nil
	}
	if name == "tmux" && len(args) == 5 && args[0] == "list-panes" && args[1] == "-t" && args[3] == "-F" && args[4] == strings.Join([]string{"#{pane_title}", "#{@projmux_pane_label}", "#{@projmux_ai_badge_kind}", "#{@projmux_ai_agent}", "#{@projmux_ai_topic}", "#{pane_current_command}"}, recentWindowFieldSep) {
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

func TestRecentWindowRecordSkipsWhileSidebarPreviewActive(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux,1,0")

	store := &recentWindowStubStore{}
	cmd := &recentWindowCommand{
		runner: &recentWindowFakeRunner{
			recordOutput: strings.Join([]string{
				"/tmp/tmux", "repos-projmux", "@6", "agent", "%54",
				"shell", "topic", "zsh",
				"/home/es5h/source/repos/projmux",
				"/home/es5h/source/repos/projmux",
			}, recentWindowFieldSep) + "\n",
		},
		storeFactory:         func(string) (recentWindowStore, error) { return store, nil },
		now:                  time.Now,
		sidebarPreviewActive: func() bool { return true },
	}

	if err := cmd.RunRecord(nil, nil, nil); err != nil {
		t.Fatalf("RunRecord() error = %v", err)
	}
	if len(store.records) != 0 {
		t.Fatalf("records len = %d, want 0 while the sidebar preview is active", len(store.records))
	}
}

func TestRecentWindowRecordRecordsWhenSidebarPreviewInactive(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux,1,0")

	store := &recentWindowStubStore{}
	cmd := &recentWindowCommand{
		runner: &recentWindowFakeRunner{
			recordOutput: strings.Join([]string{
				"/tmp/tmux", "repos-projmux", "@6", "agent", "%54",
				"shell", "topic", "zsh",
				"/home/es5h/source/repos/projmux",
				"/home/es5h/source/repos/projmux",
			}, recentWindowFieldSep) + "\n",
		},
		storeFactory:         func(string) (recentWindowStore, error) { return store, nil },
		now:                  time.Now,
		sidebarPreviewActive: func() bool { return false },
	}

	if err := cmd.RunRecord(nil, nil, nil); err != nil {
		t.Fatalf("RunRecord() error = %v", err)
	}
	if len(store.records) != 1 {
		t.Fatalf("records len = %d, want 1 for a regular switch outside the sidebar", len(store.records))
	}
}
