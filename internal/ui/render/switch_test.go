package render

import (
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/aibadge"
	"github.com/crevissepartners/projmux/internal/theme"
	"github.com/crevissepartners/projmux/internal/ui/projmuxpicker"
)

func TestBuildSwitchRowsFormatsSessionModeAndPath(t *testing.T) {
	t.Parallel()

	rows := BuildSwitchRows([]SwitchCandidate{{
		Path:        "/home/tester/workspace",
		DisplayPath: "~/workspace",
		SessionName: "workspace",
		ModeLabel:   "existing",
		GitBranch:   "main",
		UI:          "popup",
	}})

	if len(rows) != 1 {
		t.Fatalf("row count = %d, want 1", len(rows))
	}
	if got, want := rows[0].Label, "[ ]     \x1b[32m[Existing]\x1b[0m  workspace  ~/workspace"; got != want {
		t.Fatalf("label = %q, want %q", got, want)
	}
	if got, want := rows[0].Value, "/home/tester/workspace"; got != want {
		t.Fatalf("value = %q, want %q", got, want)
	}
	if got, want := rows[0].Item.Title, "workspace"; got != want {
		t.Fatalf("item title = %q, want %q", got, want)
	}
	if got, want := rows[0].Item.EffectiveSearchText(), "workspace"; got != want {
		t.Fatalf("item search text = %q, want %q", got, want)
	}
	if got, want := rows[0].Item.MetaLines, []string{"\x1b[38;5;242m~/workspace\x1b[0m \x1b[1;38;5;231;48;5;30m main \x1b[0m"}; !equalStringSlices(got, want) {
		t.Fatalf("item meta lines = %q, want %q", got, want)
	}
}

func TestBuildSwitchRowsMutesInactiveGitBranch(t *testing.T) {
	t.Parallel()

	rows := BuildSwitchRows([]SwitchCandidate{{
		Path:        "/home/tester/workspace",
		DisplayPath: "~/workspace",
		SessionName: "workspace",
		ModeLabel:   "new",
		GitBranch:   "topic",
		UI:          "popup",
	}})

	if got, want := rows[0].Item.MetaLines, []string{"\x1b[38;5;242m~/workspace\x1b[0m \x1b[38;5;231;48;5;30m topic \x1b[0m"}; !equalStringSlices(got, want) {
		t.Fatalf("item meta lines = %q, want %q", got, want)
	}
}

func TestBuildSwitchRowsTruncatesLongGitBranchBadge(t *testing.T) {
	t.Parallel()

	rows := BuildSwitchRows([]SwitchCandidate{{
		Path:        "/home/tester/workspace",
		DisplayPath: "~/workspace",
		SessionName: "workspace",
		ModeLabel:   "new",
		GitBranch:   "feature/native-picker-branch-badge-that-is-far-too-long",
		UI:          "popup",
	}})

	const want = "\x1b[38;5;242m~/workspace\x1b[0m \x1b[38;5;231;48;5;30m feature/nativ... \x1b[0m"
	if got := rows[0].Item.MetaLines[0]; got != want {
		t.Fatalf("item meta line = %q, want truncated branch badge %q", got, want)
	}
}

func TestBuildSwitchRowsOmitsBlankMode(t *testing.T) {
	t.Parallel()

	rows := BuildSwitchRows([]SwitchCandidate{{
		Path:        "/tmp/app",
		SessionName: "tmp-app",
		UI:          "popup",
	}})

	if got, want := rows[0].Label, "[ ]     tmp-app  /tmp/app"; got != want {
		t.Fatalf("label = %q, want %q", got, want)
	}
}

func TestPrettyPathPrefersRepoRootAlias(t *testing.T) {
	t.Parallel()

	if got, want := PrettyPath("/home/tester/source/repos/app", "/home/tester", "/home/tester/source/repos"), "~rp/app"; got != want {
		t.Fatalf("PrettyPath() = %q, want %q", got, want)
	}
}

func TestPrettyPathFallsBackToHomeAlias(t *testing.T) {
	t.Parallel()

	if got, want := PrettyPath("/home/tester/workspace", "/home/tester", "/repo"), "~/workspace"; got != want {
		t.Fatalf("PrettyPath() = %q, want %q", got, want)
	}
}

func TestBuildSwitchRowsSanitizesTabsAndNewlines(t *testing.T) {
	t.Parallel()

	rows := BuildSwitchRows([]SwitchCandidate{{
		Path:        "/tmp/app\tone",
		SessionName: "tmp\napp",
		ModeLabel:   "new\tstate",
		UI:          "popup",
	}})

	if got, want := rows[0].Label, "[ ]     [new state]  tmp app  /tmp/app one"; got != want {
		t.Fatalf("label = %q, want %q", got, want)
	}
}

func TestBuildSwitchRowsSidebarUsesAnsiStylingForModeAndToggles(t *testing.T) {
	t.Parallel()

	rows := BuildSwitchRows([]SwitchCandidate{{
		Path:        "/home/tester/source/repos/app",
		DisplayPath: "~rp/app",
		SessionName: "app",
		ModeLabel:   "existing",
		UI:          "sidebar",
		Pinned:      true,
		Tagged:      true,
	}})

	got := rows[0].Label
	for _, want := range []string{"\x1b[31mx\x1b[0m", "\x1b[33m*\x1b[0m", "\x1b[1m\x1b[32mapp\x1b[0m", "\x1b[2m~rp/app\x1b[0m"} {
		if !strings.Contains(got, want) {
			t.Fatalf("label = %q, want token %q", got, want)
		}
	}
	if got, want := len(strings.Split(got, "\n")), 3; got != want {
		t.Fatalf("label line count = %d, want card-like 3-line sidebar row: %q", got, got)
	}
	if itemLabel := rows[0].Item.EffectiveLabel(); len(strings.Split(itemLabel, "\n")) != 3 {
		t.Fatalf("item label = %q, want card-like 3-line sidebar row", itemLabel)
	}
	if got := rows[0].Item.MetaLines; len(got) != 0 {
		t.Fatalf("item meta lines = %#v, want sidebar card metadata folded into label lines", got)
	}
}

func TestBuildSwitchRowsSidebarLeavesNewSessionNameUncolored(t *testing.T) {
	t.Parallel()

	rows := BuildSwitchRows([]SwitchCandidate{{
		Path:        "/home/tester/source/repos/app",
		DisplayPath: "~rp/app",
		SessionName: "app",
		ModeLabel:   "new",
		UI:          "sidebar",
	}})

	got := rows[0].Label
	for _, want := range []string{"app", "\x1b[2m~rp/app\x1b[0m"} {
		if !strings.Contains(got, want) {
			t.Fatalf("label = %q, want token %q", got, want)
		}
	}
	if got, want := len(strings.Split(got, "\n")), 3; got != want {
		t.Fatalf("label line count = %d, want card-like 3-line sidebar row: %q", got, got)
	}
}

func TestBuildSwitchRowsSidebarShowsAttentionBadge(t *testing.T) {
	t.Parallel()

	rows := BuildSwitchRows([]SwitchCandidate{{
		Path:          "/home/tester/source/repos/app",
		DisplayPath:   "~rp/app",
		SessionName:   "app",
		ModeLabel:     "existing",
		UI:            "sidebar",
		AttentionRank: 2,
	}})

	got := rows[0].Label
	for _, want := range []string{"\x1b[38;2;255;204;102m●\x1b[0m", "\x1b[1m\x1b[32mapp\x1b[0m", "\x1b[2m~rp/app\x1b[0m"} {
		if !strings.Contains(got, want) {
			t.Fatalf("label = %q, want token %q", got, want)
		}
	}
	if got, want := len(strings.Split(got, "\n")), 3; got != want {
		t.Fatalf("label line count = %d, want card-like 3-line sidebar row: %q", got, got)
	}
	if got, want := rows[0].Item.Badges, []string{"needs review"}; !equalStringSlices(got, want) {
		t.Fatalf("item badges = %q, want %q", got, want)
	}
}

func TestBuildSwitchRowsSidebarShowsSemanticPromptBadge(t *testing.T) {
	t.Parallel()

	rows := BuildSwitchRows([]SwitchCandidate{{
		Path:        "/home/tester/source/repos/app",
		DisplayPath: "~rp/app",
		SessionName: "app",
		ModeLabel:   "existing",
		UI:          "sidebar",
		AIBadgeKind: "input_required",
	}})

	got := rows[0].Label
	if !strings.Contains(got, "\x1b[38;5;214m●\x1b[0m") {
		t.Fatalf("label = %q, want semantic prompt warning badge", got)
	}
	if got, want := rows[0].Item.Badges, []string{"needs input"}; !equalStringSlices(got, want) {
		t.Fatalf("item badges = %q, want %q", got, want)
	}
}

func TestSwitchBadgeDotUsesSemanticPriorityColor(t *testing.T) {
	t.Parallel()

	rows := BuildSwitchRows([]SwitchCandidate{{
		Path:          "/home/tester/source/repos/app",
		DisplayPath:   "~rp/app",
		SessionName:   "app",
		ModeLabel:     "existing",
		UI:            "sidebar",
		AttentionRank: 2,
		AIBadgeKind:   "response_complete",
	}})

	got := rows[0].Label
	if !strings.Contains(got, theme.ANSIAIBadgeSuccessStart+"●"+theme.ANSIReset) {
		t.Fatalf("label = %q, want semantic response-complete dot to outrank legacy busy attention", got)
	}
	if strings.Contains(got, "\x1b[38;2;255;204;102m●\x1b[0m") {
		t.Fatalf("label = %q, legacy busy attention dot must not override semantic response-complete", got)
	}
	if strings.Contains(got, theme.ANSIStateDangerStart+"●"+theme.ANSIReset) {
		t.Fatalf("label = %q, response-complete status badge must not use critical/danger color", got)
	}
}

func TestSwitchBadgeEmojiStyleCompatibilityGlyphs(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		aibadge.ApprovalRequired: "⏳",
		aibadge.InputRequired:    "⏳",
		aibadge.ResponseComplete: "✅",
		aibadge.InProgress:       "🔄",
		"":                       " ",
	}
	for kind, want := range cases {
		if got := aibadge.Glyph(kind, aibadge.StyleEmoji); got != want {
			t.Fatalf("Glyph(%q, emoji) = %q, want %q", kind, got, want)
		}
	}
	if got := aibadge.Glyph(aibadge.ApprovalRequired, aibadge.StyleOff); got != " " {
		t.Fatalf("Glyph(approval_required, off) = %q, want blank alignment glyph", got)
	}
}

func TestBuildSwitchRowsSidebarWindowTabsUseAIBadgeStyle(t *testing.T) {
	t.Parallel()

	rows := BuildSwitchRows([]SwitchCandidate{{
		Path:         "/home/tester/source/repos/app",
		DisplayPath:  "~rp/app",
		DisplayName:  "app",
		SessionName:  "repos-app",
		ModeLabel:    "existing",
		UI:           "sidebar",
		AIBadgeStyle: aibadge.StyleEmoji,
		WindowTabs: []SwitchWindowTab{
			{Name: "shell", AIBadgeKind: aibadge.ApprovalRequired, Live: true, Active: true},
			{Name: "server", AIBadgeKind: aibadge.ResponseComplete},
			{Name: "tests", AIBadgeKind: aibadge.InProgress},
		},
	}})[0]

	got := rows.Item.EffectiveLabel()
	for _, want := range []string{"⏳", "✅", "🔄"} {
		if !strings.Contains(got, want) {
			t.Fatalf("sidebar label = %q, want AI badge style glyph %q", got, want)
		}
	}
	if strings.Contains(got, "●") {
		t.Fatalf("sidebar label = %q, want emoji badge style without dot glyphs", got)
	}
}

func TestBuildSwitchRowsSidebarCheapAndEnrichedGeometryIsStable(t *testing.T) {
	t.Parallel()

	cheap := BuildSwitchRows([]SwitchCandidate{{
		Path:        "/home/tester/source/repos/app",
		DisplayPath: "~rp/app",
		DisplayName: "app",
		SessionName: "repos-app",
		ModeLabel:   "existing",
		UI:          "sidebar",
	}})[0]
	enriched := BuildSwitchRows([]SwitchCandidate{{
		Path:        "/home/tester/source/repos/app",
		DisplayPath: "~rp/app",
		DisplayName: "app",
		SessionName: "repos-app",
		ModeLabel:   "existing",
		GitBranch:   "feature/long-sidebar-branch-name",
		WindowTabs: []SwitchWindowTab{
			{Name: "shell", Live: true, Active: true},
			{Name: "server", AttentionRank: 2},
			{Name: "tests", AttentionRank: 1},
			{Name: "extra"},
		},
		UI:            "sidebar",
		AttentionRank: 2,
	}})[0]

	cheapLabel := cheap.Item.EffectiveLabel()
	enrichedLabel := enriched.Item.EffectiveLabel()
	cheapLines := strings.Split(cheapLabel, "\n")
	enrichedLines := strings.Split(enrichedLabel, "\n")
	if len(cheapLines) != 3 || len(enrichedLines) != 3 {
		t.Fatalf("sidebar labels must stay 3-line card rows\ncheap:    %q\nenriched: %q", cheapLabel, enrichedLabel)
	}
	if len(cheap.Item.MetaLines) != 0 || len(enriched.Item.MetaLines) != 0 {
		t.Fatalf("sidebar meta lines cheap/enriched = %#v/%#v, want no extra rendered lines beyond the 3-line card", cheap.Item.MetaLines, enriched.Item.MetaLines)
	}
	if strings.Contains(cheapLines[1], "48;5;30m") || strings.Contains(cheapLines[2], "48;5;235m") {
		t.Fatalf("cheap placeholder lanes must stay visually blank, got lines: %q", cheapLines)
	}
	if strings.Contains(enrichedLines[1], "feature/long-...  ") {
		t.Fatalf("enriched branch chip = %q, want chip background only around branch text", enrichedLines[1])
	}
	for idx := range cheapLines {
		if got, want := projmuxpicker.VisibleLen(enrichedLines[idx]), projmuxpicker.VisibleLen(cheapLines[idx]); got != want {
			t.Fatalf("line %d width changed from %d to %d\ncheap:    %q\nenriched: %q", idx, want, got, cheapLines[idx], enrichedLines[idx])
		}
	}
	if !strings.Contains(enrichedLabel, "feature/long-...") {
		t.Fatalf("enriched label = %q, want truncated branch in fixed lane", enrichedLabel)
	}
	if strings.Contains(enrichedLabel, "extra") {
		t.Fatalf("enriched label = %q, want fixed sidebar tab slots", enrichedLabel)
	}
}

func TestBuildSwitchRowsSidebarThreeLineCardHardClipsWithin80ColumnNativeBudget(t *testing.T) {
	t.Parallel()
	row := BuildSwitchRows([]SwitchCandidate{{
		Path:        "/src/project",
		DisplayPath: strings.Repeat("p", 90) + "PATH_TAIL",
		DisplayName: strings.Repeat("n", 90) + "TITLE_TAIL",
		ModeLabel:   "existing",
		UI:          "sidebar",
		WindowTabs: []SwitchWindowTab{
			{Name: "active", Live: true, Active: true},
			{Name: "live", Live: true},
			{Name: "offline"},
		},
	}})[0]
	lines := projmuxpicker.InteractiveRowLines(projmuxpicker.Row{
		Label: row.Item.EffectiveLabel(),
	}, true, true)
	if len(lines) != 3 {
		t.Fatalf("sidebar native input lines = %d, want fixed three-line card: %#v", len(lines), lines)
	}
	content := projmuxpicker.DefaultRenderer().ContentLayout(projmuxpicker.Layout{Rows: 24, Cols: 80})
	if content.Cols != 78 {
		t.Fatalf("80-column native frame content width = %d, want 78", content.Cols)
	}
	rendered := projmuxpicker.ListLinesWithScrollbarRows(lines, 1, 0, 1, content.Cols, 3)
	if len(rendered) != 3 {
		t.Fatalf("80-column native rendered lines = %d, want 3", len(rendered))
	}
	for index, line := range rendered {
		if got := projmuxpicker.VisibleLen(line); got != content.Cols {
			t.Fatalf("80-column native line %d width = %d, want hard-clipped/padded %d: %q", index, got, content.Cols, line)
		}
	}
	if joined := strings.Join(rendered, "\n"); strings.Contains(joined, "TITLE_TAIL") || strings.Contains(joined, "PATH_TAIL") {
		t.Fatalf("80-column native hard clip leaked overflow tails: %q", joined)
	}
}

func TestFormatSidebarSwitchWindowTabsStableRuntimePartitionAndCap(t *testing.T) {
	t.Parallel()
	windows := []SwitchWindowTab{
		{Name: "off-a"},
		{Name: "live-a", Live: true},
		{Name: "live-b", Live: true},
		{Name: "active", Live: true, Active: true},
		{Name: "off-b"},
	}
	got := formatSidebarSwitchWindowTabs(windows, aibadge.StyleDot)
	activeAt := strings.Index(got, "active")
	liveAAt := strings.Index(got, "live-a")
	liveBAt := strings.Index(got, "live-b")
	if activeAt < 0 || liveAAt < activeAt || liveBAt < liveAAt {
		t.Fatalf("tabs are not active -> stable live: %q", got)
	}
	for _, excluded := range []string{"off-a", "off-b"} {
		if strings.Contains(got, excluded) {
			t.Fatalf("3-slot cap retained %q instead of the live tier: %q", excluded, got)
		}
	}
	if count := strings.Count(got, ansiTabActive); count != 1 {
		t.Fatalf("active style count = %d, want 1: %q", count, got)
	}
	if width := projmuxpicker.VisibleLen(got); width != 38 {
		t.Fatalf("tab lane width = %d, want fixed 38", width)
	}
}

func TestFormatSidebarSwitchWindowTabsOfflineOnlyKeepsRegistryOrderInactive(t *testing.T) {
	t.Parallel()
	got := formatSidebarSwitchWindowTabs([]SwitchWindowTab{
		{Name: "one"}, {Name: "two"}, {Name: "three"}, {Name: "four"},
	}, aibadge.StyleDot)
	oneAt, twoAt, threeAt := strings.Index(got, "one"), strings.Index(got, "two"), strings.Index(got, "three")
	if oneAt < 0 || twoAt < oneAt || threeAt < twoAt || strings.Contains(got, "four") {
		t.Fatalf("offline tabs did not keep the first three in Registry order: %q", got)
	}
	if count := strings.Count(got, ansiTabActive); count != 0 {
		t.Fatalf("offline tabs have %d active styles: %q", count, got)
	}
}

func TestFormatSidebarSwitchWindowTabsDoesNotRepairMalformedActiveFacts(t *testing.T) {
	t.Parallel()
	got := formatSidebarSwitchWindowTabs([]SwitchWindowTab{
		{Name: "not-live", Active: true},
		{Name: "also", Live: true, Active: true},
		{Name: "live", Live: true},
	}, aibadge.StyleDot)
	if count := strings.Count(got, ansiTabActive); count != 2 {
		t.Fatalf("renderer coerced malformed active bits; active style count = %d, want 2: %q", count, got)
	}
	if first, second := strings.Index(got, "not-live"), strings.Index(got, "also"); first < 0 || second < first {
		t.Fatalf("renderer did not preserve malformed active tier order: %q", got)
	}
}

func TestFormatSwitchCardLabelUsesProgressForBusyBadge(t *testing.T) {
	t.Parallel()

	rows := BuildSwitchRows([]SwitchCandidate{{
		Path:          "/home/tester/source/repos/app",
		DisplayName:   "app",
		ModeLabel:     "existing",
		UI:            "popup",
		AttentionRank: 2,
	}})

	got := FormatSwitchCardLabel(rows[0].Item)
	const want = "\x1b[1m\x1b[32mapp\x1b[0m \x1b[38;2;255;204;102m●\x1b[0m\n  \x1b[38;5;242m/home/tester/source/repos/app\x1b[0m"
	if got != want {
		t.Fatalf("card label = %q, want %q", got, want)
	}
}

func TestBuildSwitchRowsSidebarFormatsSettingsRow(t *testing.T) {
	t.Parallel()

	rows := BuildSwitchRows([]SwitchCandidate{{
		Path:        "__projmux_settings__",
		DisplayPath: "Settings",
		UI:          "sidebar",
	}})

	const want = "  \x1b[1m\x1b[36mSettings\x1b[0m  \x1b[2mmanage pinned directories\x1b[0m"
	if got := rows[0].Label; got != want {
		t.Fatalf("label = %q, want %q", got, want)
	}
	if got, want := rows[0].Item.Title, "Settings"; got != want {
		t.Fatalf("item title = %q, want %q", got, want)
	}
	if got, want := rows[0].Item.Value, "__projmux_settings__"; got != want {
		t.Fatalf("item value = %q, want %q", got, want)
	}
}

func TestBuildSwitchPickerItemsReturnsBackendNeutralRows(t *testing.T) {
	t.Parallel()

	items := BuildSwitchPickerItems([]SwitchCandidate{{
		Path:        "/home/tester/source/repos/app",
		DisplayPath: "~rp/app",
		DisplayName: "app",
		SessionName: "repos-app",
		ModeLabel:   "new",
		GitBranch:   "topic",
		Pinned:      true,
		Tagged:      true,
	}})

	if len(items) != 1 {
		t.Fatalf("item count = %d, want 1", len(items))
	}
	item := items[0]
	if got, want := item.Title, "app"; got != want {
		t.Fatalf("title = %q, want %q", got, want)
	}
	if got, want := item.Value, "/home/tester/source/repos/app"; got != want {
		t.Fatalf("value = %q, want %q", got, want)
	}
	if got, want := item.MetaLines, []string{"\x1b[38;5;242m~rp/app\x1b[0m \x1b[38;5;231;48;5;30m topic \x1b[0m"}; !equalStringSlices(got, want) {
		t.Fatalf("meta lines = %q, want %q", got, want)
	}
	if got, want := item.Badges, []string{"tagged", "pinned"}; !equalStringSlices(got, want) {
		t.Fatalf("badges = %q, want %q", got, want)
	}
}

func TestFormatSwitchCardLabelShowsMultilineContext(t *testing.T) {
	t.Parallel()

	rows := BuildSwitchRows([]SwitchCandidate{{
		Path:        "/home/tester/source/repos/app",
		DisplayPath: "~rp/app",
		DisplayName: "app",
		SessionName: "repos-app",
		ModeLabel:   "existing",
		GitBranch:   "main",
		WindowTabs: []SwitchWindowTab{
			{Name: "shell", Live: true, Active: true},
			{Name: "server", AttentionRank: 2},
			{Name: "tests", AttentionRank: 1},
		},
		AttentionRank: 1,
		Pinned:        true,
	}})

	got := FormatSwitchCardLabel(rows[0].Item)
	const want = "\x1b[1m\x1b[32mapp\x1b[0m \x1b[38;5;72m●\x1b[0m \x1b[33m*\x1b[0m\n  \x1b[38;5;242m~rp/app\x1b[0m \x1b[1;38;5;231;48;5;30m main \x1b[0m\n  \x1b[1;38;5;231;48;5;238m  shell   \x1b[0m \x1b[38;5;245;48;5;235m \x1b[38;5;220m● \x1b[0m\x1b[38;5;245;48;5;235m server  \x1b[0m \x1b[38;5;245;48;5;235m \x1b[38;5;82m● \x1b[0m\x1b[38;5;245;48;5;235m tests   \x1b[0m"
	if got != want {
		t.Fatalf("card label = %q, want %q", got, want)
	}
}

func equalStringSlices(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
