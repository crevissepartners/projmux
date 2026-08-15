package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	"github.com/crevissepartners/projmux/internal/theme"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

func TestAISettingsGetAndSetMode(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"settings", "--get"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run settings --get error = %v", err)
	}
	if got, want := stdout.String(), "selective\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}

	if err := cmd.Run([]string{"settings", "--set", "codex"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run settings --set error = %v", err)
	}
	if len(cmdRecorder(cmd).commands) != 0 {
		t.Fatalf("commands = %#v, want no tmux toast outside tmux", cmdRecorder(cmd).commands)
	}
	stdout.Reset()
	if err := cmd.Run([]string{"settings", "--get"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run settings --get after set error = %v", err)
	}
	if got, want := stdout.String(), "codex\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestAISettingsSetModeDisplaysTmuxToastInsideTmux(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "TMUX":
			return "/tmp/tmux"
		default:
			return ""
		}
	}

	if err := cmd.Run([]string{"settings", "--set", "codex"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run settings --set error = %v", err)
	}

	want := []recordedAICommand{{name: "tmux", args: []string{"display-message", "ai split default: codex"}}}
	if !reflect.DeepEqual(cmdRecorder(cmd).commands, want) {
		t.Fatalf("commands = %#v, want %#v", cmdRecorder(cmd).commands, want)
	}
}

func TestAISettingsPickerSetsSelectedMode(t *testing.T) {
	home := t.TempDir()
	runner := &capturingAIRunner{result: intpickercompat.Result{Key: "enter", Value: "shell"}}
	cmd := testAICommand(home)
	cmd.runner = runner
	cmd.nativePicker = nativePickerFromCompatRunner(runner)

	if err := cmd.Run([]string{"settings"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run settings picker error = %v", err)
	}
	if got, want := runner.options.UI, "ai-settings"; got != want {
		t.Fatalf("runner UI = %q, want %q", got, want)
	}
	if got, want := runner.options.Title, "AI Settings - Default split mode"; got != want {
		t.Fatalf("runner title = %q, want %q", got, want)
	}
	if got, want := runner.options.Prompt, "AI Setting > "; got != want {
		t.Fatalf("runner prompt = %q, want %q", got, want)
	}
	if got := runner.options.Header; got != "" {
		t.Fatalf("runner header = %q, want description only in title", got)
	}
	if got, want := runner.options.Footer, "Choose the default split mode for future AI launches."; got != want {
		t.Fatalf("runner footer = %q, want %q", got, want)
	}
	if got, want := readModeFile(t, home), "shell\n"; got != want {
		t.Fatalf("mode file = %q, want %q", got, want)
	}
}

func TestAISplitPickerCloseBindingsUseAISplitPickerAlias(t *testing.T) {
	home := t.TempDir()
	keymapPath := filepath.Join(home, ".config", "projmux", "keymap.toml")
	if err := os.MkdirAll(filepath.Dir(keymapPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keymapPath, []byte(`[bindings.AISplitPickerToggle]
keys = ["M-a"]
[bindings.SettingsToggle]
keys = ["M-s"]
[bindings.new-window]
keys = ["M-t"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &capturingAIRunner{}
	cmd := testAICommand(home)
	cmd.runner = runner
	cmd.nativePicker = nativePickerFromCompatRunner(runner)

	if _, err := cmd.runAgentPicker("right"); err != nil {
		t.Fatalf("runAgentPicker() error = %v", err)
	}
	if !containsString(runner.options.Bindings, "alt-a:abort") {
		t.Fatalf("AI picker bindings = %#v, want custom AISplitPickerToggle alias close", runner.options.Bindings)
	}
	if containsString(runner.options.Bindings, "alt-s:abort") {
		t.Fatalf("AI picker bindings = %#v, SettingsToggle alias must not close AI picker", runner.options.Bindings)
	}
	if containsString(runner.options.Bindings, "alt-t:abort") {
		t.Fatalf("AI picker bindings = %#v, direct command alias must not close popup", runner.options.Bindings)
	}
}

func TestAIPickerAppliesGlobalThemeSurface(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".config", "projmux", "config.toml"), `
[theme]
background = "#0000ff"
surface = "#112233"
foreground = "#ffffff"
`)
	runner := &capturingAIRunner{}
	cmd := testAICommand(home)
	cmd.runner = runner
	cmd.nativePicker = nativePickerFromCompatRunner(runner)

	if _, err := cmd.runAgentPicker("right"); err != nil {
		t.Fatalf("runAgentPicker() error = %v", err)
	}
	if runner.options.Theme == nil {
		t.Fatalf("AI picker options.Theme = nil, want global theme filled")
	}
	if got := runner.options.Theme.Background.Source; got != theme.SourceGlobal {
		t.Fatalf("AI picker background source = %q, want global", got)
	}
	if got, want := runner.options.Theme.Surface.Value.Hex, "#112233"; got != want {
		t.Fatalf("AI picker surface hex = %q, want %q", got, want)
	}
}

func TestAISettingsAppliesGlobalThemeSurface(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".config", "projmux", "config.toml"), `
[theme]
background = "#0000ff"
surface = "#112233"
`)
	runner := &capturingAIRunner{result: intpickercompat.Result{Key: "esc"}}
	cmd := testAICommand(home)
	cmd.runner = runner
	cmd.nativePicker = nativePickerFromCompatRunner(runner)

	if err := cmd.Run([]string{"settings"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run settings picker error = %v", err)
	}
	if runner.options.Theme == nil {
		t.Fatalf("AI settings options.Theme = nil, want global theme filled")
	}
	if got := runner.options.Theme.Background.Source; got != theme.SourceGlobal {
		t.Fatalf("AI settings background source = %q, want global", got)
	}
}

func TestAIPickerUnsetThemeMatchesFallback(t *testing.T) {
	home := t.TempDir()
	runner := &capturingAIRunner{}
	cmd := testAICommand(home)
	cmd.runner = runner
	cmd.nativePicker = nativePickerFromCompatRunner(runner)

	if _, err := cmd.runAgentPicker("right"); err != nil {
		t.Fatalf("runAgentPicker() error = %v", err)
	}
	if runner.options.Theme == nil {
		t.Fatalf("AI picker options.Theme = nil, want fallback theme filled")
	}
	want := theme.ResolveTheme(theme.ThemeConfig{})
	if got := runner.options.Theme.Background.Source; got != want.Background.Source {
		t.Fatalf("AI picker unset background source = %q, want fallback %q", got, want.Background.Source)
	}
	if got := runner.options.Theme.Surface.Value.Hex; got != want.Surface.Value.Hex {
		t.Fatalf("AI picker unset surface hex = %q, want fallback %q", got, want.Surface.Value.Hex)
	}
}

func TestAISettingsRowsHideDisabledAgentDefaults(t *testing.T) {
	home := t.TempDir()
	if err := config.SaveAIEnabledAgentsFile(filepath.Join(home, ".config", "projmux", config.AIEnabledAgentsFileName), []config.AIAgentProvider{config.AIAgentClaude}); err != nil {
		t.Fatalf("SaveAIEnabledAgentsFile() error = %v", err)
	}
	cmd := testAICommand(home)
	if err := cmd.setMode(aiModeCodex); err != nil {
		t.Fatalf("setMode(codex) error = %v", err)
	}

	rows := cmd.settingsRows()
	if hasEntryValue(rows, aiModeCodex) {
		t.Fatalf("settings rows = %#v, want disabled Codex hidden", rows)
	}
	for _, want := range []string{aiModeSelective, aiModeClaude, aiModeShell} {
		if !hasEntryValue(rows, want) {
			t.Fatalf("settings rows = %#v, want row %q", rows, want)
		}
	}
	if !hasEntryLabelContainingAll(rows, "saved default codex is disabled", "Enabled agents") {
		t.Fatalf("settings rows = %#v, want disabled default warning", rows)
	}
}

func TestAIPickerShowsKeyFooter(t *testing.T) {
	home := t.TempDir()
	runner := &capturingAIRunner{}
	cmd := testAICommand(home)
	cmd.runner = runner
	cmd.nativePicker = nativePickerFromCompatRunner(runner)

	if _, err := cmd.runAgentPicker("right"); err != nil {
		t.Fatalf("runAgentPicker error = %v", err)
	}
	if got, want := runner.options.UI, "ai-picker"; got != want {
		t.Fatalf("runner UI = %q, want %q", got, want)
	}
	if got, want := runner.options.Title, "AI Launch - Split direction: right"; got != want {
		t.Fatalf("runner title = %q, want %q", got, want)
	}
	if got := runner.options.Header; got != "" {
		t.Fatalf("runner header = %q, want direction only in title", got)
	}
	if got, want := entryValues(runner.options.Entries), []string{aiModeCodex, aiModeClaude, aiModeAntigravity, aiModeShell}; !reflect.DeepEqual(got, want) {
		t.Fatalf("runner entry order = %#v, want %#v", got, want)
	}
	for _, entry := range runner.options.Entries {
		if strings.TrimSpace(entry.SearchKey) == "" {
			t.Fatalf("runner entry %#v has empty SearchKey; want stable search-order filtering", entry)
		}
	}
	if got, want := runner.options.Footer, "Choose an agent or shell target to launch."; got != want {
		t.Fatalf("runner footer = %q, want %q", got, want)
	}
}

func TestAIResumePickerRowsCapAndMetadata(t *testing.T) {
	sessions := make([]aisessions.SessionMeta, 0, aiResumePickerLimitDefault+2)
	for i := range aiResumePickerLimitDefault + 2 {
		sessions = append(sessions, aisessions.SessionMeta{
			Agent:        aiModeCodex,
			ResumeID:     fmt.Sprintf("019f0000-0000-7000-8000-%012d", i),
			Title:        fmt.Sprintf("Title %02d", i),
			LastModified: time.Date(2026, 6, 25, 9, i, 0, 0, time.UTC),
			Context:      aisessions.SessionContext{Branch: "feat/resume-picker"},
		})
	}

	rows, visible, total := aiResumeSessionRows(sessions, aiResumePickerLimitDefault, time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC), i18n.FallbackLocale, "", 0)

	if visible != aiResumePickerLimitDefault || total != aiResumePickerLimitDefault+2 {
		t.Fatalf("visible,total = %d,%d, want %d,%d", visible, total, aiResumePickerLimitDefault, aiResumePickerLimitDefault+2)
	}
	if len(rows) != aiResumePickerLimitDefault+1 {
		t.Fatalf("rows len = %d, want cap plus New row", len(rows))
	}
	if rows[0].Value != aiResumeNewValue || !strings.Contains(rows[0].Label, "[+ New Session]") {
		t.Fatalf("first row = %#v, want New row", rows[0])
	}
	if !strings.Contains(rows[1].Label, "[codex") || !strings.Contains(rows[1].Label, "feat/resume-picke") || !strings.Contains(rows[1].Label, "Title 00") {
		t.Fatalf("session row label = %q, want agent, branch, title", rows[1].Label)
	}
	if !strings.Contains(rows[1].SearchKey, sessions[0].ResumeID) {
		t.Fatalf("session row search key = %q, want resume id", rows[1].SearchKey)
	}
}

// aiResumeRowPrefixWidth is the fixed-column prefix width that every session
// row shares before the trailing title: rel[6] + " " + badge[8] + " " +
// branch[18] + " " + turns[5] + " " (separator before title).
const aiResumeRowPrefixWidth = aiResumeRelCellWidth + 1 + aiResumeBadgeCellWidth + 1 +
	aiResumeBranchCellWidth + 1 + aiResumeTurnsCellWidth + 1

func TestAIResumeSessionRowColumnsAlign(t *testing.T) {
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	sessions := []aisessions.SessionMeta{
		{
			Agent:        aiModeClaude,
			ResumeID:     "019f0000-0000-7000-8000-000000000001",
			Title:        "short title",
			LastModified: now.Add(-2 * time.Hour),
			Context:      aisessions.SessionContext{Branch: "main"},
		},
		{
			Agent:        aiModeAntigravity, // longer than the badge cell
			ResumeID:     "abc",             // shorter than the short-id cell
			Title:        strings.Repeat("very-long-title ", 12),
			LastModified: now.Add(-72 * time.Hour),
			Context:      aisessions.SessionContext{Branch: "feature/extremely-long-branch-name-overflow"},
		},
		{
			Agent:        aiModeCodex,
			ResumeID:     "019f0000-0000-7000-8000-000000000003",
			Title:        "한글 제목 정렬 확인", // wide runes in the title
			LastModified: now.Add(-30 * time.Minute),
			Context:      aisessions.SessionContext{Branch: ""}, // empty -> placeholder
		},
	}

	rows, visible, total := aiResumeSessionRows(sessions, aiResumePickerLimitDefault, now, i18n.FallbackLocale, "", 0)
	if visible != len(sessions) || total != len(sessions) {
		t.Fatalf("visible,total = %d,%d, want %d,%d", visible, total, len(sessions), len(sessions))
	}

	for i, session := range sessions {
		row := rows[i+1] // row 0 is the New Session entry
		title := cleanAIResumeTitle(session.Title, session.ResumeID)
		// The fixed-column prefix width is label width minus the rendered title.
		prefix := i18n.TerminalCellWidth(row.Label) - i18n.TerminalCellWidth(title)
		if prefix != aiResumeRowPrefixWidth {
			t.Fatalf("row %d prefix width = %d, want %d (label %q)", i, prefix, aiResumeRowPrefixWidth, row.Label)
		}
	}

	// Empty branch renders the placeholder, not a collapsed column.
	if !strings.Contains(rows[3].Label, aiResumeEmptyCell) {
		t.Fatalf("empty-branch row = %q, want %q placeholder", rows[3].Label, aiResumeEmptyCell)
	}
}

func TestAIResumeSessionRowsLimitBoundaries(t *testing.T) {
	now := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
	// hooks.AIResumePickerLimitMax is 100; clamp pins the visible count there.
	const maxLimit = 100
	const sessionCount = maxLimit + 10
	sessions := make([]aisessions.SessionMeta, 0, sessionCount)
	for i := range sessionCount {
		sessions = append(sessions, aisessions.SessionMeta{
			Agent:        aiModeCodex,
			ResumeID:     fmt.Sprintf("019f0000-0000-7000-8000-%012d", i),
			Title:        fmt.Sprintf("Title %03d", i),
			LastModified: now.Add(-time.Duration(i) * time.Minute),
		})
	}

	for _, tc := range []struct {
		name        string
		limit       int
		wantVisible int
	}{
		{name: "zero falls back to default", limit: 0, wantVisible: aiResumePickerLimitDefault},
		{name: "negative falls back to default", limit: -7, wantVisible: aiResumePickerLimitDefault},
		{name: "in range honored", limit: 12, wantVisible: 12},
		{name: "oversized clamps to max", limit: 500, wantVisible: maxLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, visible, total := aiResumeSessionRows(sessions, tc.limit, now, i18n.FallbackLocale, "", 0)
			if visible != tc.wantVisible {
				t.Fatalf("visible = %d, want %d", visible, tc.wantVisible)
			}
			if total != sessionCount {
				t.Fatalf("total = %d, want %d", total, sessionCount)
			}
		})
	}
}

func TestAIResumeSessionRowTitleEllipsis(t *testing.T) {
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	long := strings.Repeat("x", aiResumeTitleMaxCells+25)
	const resumeID = "019f0000-0000-7000-8000-000000000009"
	row := aiResumeSessionRow(aisessions.SessionMeta{
		Agent:        aiModeClaude,
		ResumeID:     resumeID,
		Title:        long,
		LastModified: now.Add(-time.Hour),
	}, now, i18n.FallbackLocale, "", 0)

	if !strings.Contains(row.Label, "…") {
		t.Fatalf("row label = %q, want ellipsis on overflow", row.Label)
	}
	title := cleanAIResumeTitle(long, "")
	if w := i18n.TerminalCellWidth(title); w > aiResumeTitleMaxCells {
		t.Fatalf("clipped title width = %d, want <= %d", w, aiResumeTitleMaxCells)
	}
	// The resume id is dropped from the visible columns but stays searchable.
	if strings.Contains(row.Label, "019f0000") {
		t.Fatalf("row label = %q, should not surface the resume id column", row.Label)
	}
	if !strings.Contains(row.SearchKey, resumeID) {
		t.Fatalf("row search key = %q, want resume id preserved for search", row.SearchKey)
	}
}

func TestAIResumeExtraMetaCellDepthGating(t *testing.T) {
	session := aisessions.SessionMeta{
		Context: aisessions.SessionContext{CWD: "/workspace/app/web"},
	}
	// Depth 0 hides the column entirely (historical view).
	if got := aiResumeExtraMetaCell(session, "/workspace/app", 0); got != "" {
		t.Fatalf("depth 0 extra cell = %q, want empty", got)
	}
	// Depth>0 surfaces the cwd relative to the picker base.
	if got := aiResumeExtraMetaCell(session, "/workspace/app", 1); got != "./web" {
		t.Fatalf("depth 1 extra cell = %q, want ./web", got)
	}
	// The exact cwd renders "./" so every row keeps the column aligned.
	exact := aisessions.SessionMeta{Context: aisessions.SessionContext{CWD: "/workspace/app"}}
	if got := aiResumeExtraMetaCell(exact, "/workspace/app", 1); got != "./" {
		t.Fatalf("exact cwd extra cell = %q, want ./", got)
	}
}

func TestAIResumeRelativeCWD(t *testing.T) {
	for _, tc := range []struct {
		base     string
		recorded string
		want     string
	}{
		{base: "/workspace/app", recorded: "/workspace/app", want: "./"},
		{base: "/workspace/app", recorded: "/workspace/app/web", want: "./web"},
		{base: "/workspace/app", recorded: "/workspace/app/web/api", want: "./web/api"},
		{base: "/workspace/app", recorded: "/workspace/app-other", want: ""}, // sibling escapes base
		{base: "/workspace/app", recorded: "/workspace", want: ""},           // parent escapes base
		{base: "", recorded: "/workspace/app", want: ""},
		{base: "/workspace/app", recorded: "", want: ""},
	} {
		if got := aiResumeRelativeCWD(tc.base, tc.recorded); got != tc.want {
			t.Fatalf("aiResumeRelativeCWD(%q, %q) = %q, want %q", tc.base, tc.recorded, got, tc.want)
		}
	}
}

func TestAIResumeSessionRowShowsCWDColumnOnlyAtDepth(t *testing.T) {
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	session := aisessions.SessionMeta{
		Agent:        aiModeClaude,
		ResumeID:     "019f0000-0000-7000-8000-000000000021",
		Title:        "Child session",
		LastModified: now.Add(-time.Hour),
		Context:      aisessions.SessionContext{CWD: "/workspace/app/web", Branch: "feat/web"},
	}

	depth0 := aiResumeSessionRow(session, now, i18n.FallbackLocale, "/workspace/app", 0)
	if strings.Contains(depth0.Label, "./web") {
		t.Fatalf("depth 0 row should hide cwd column: %q", depth0.Label)
	}

	depth1 := aiResumeSessionRow(session, now, i18n.FallbackLocale, "/workspace/app", 1)
	if !strings.Contains(depth1.Label, "./web") {
		t.Fatalf("depth 1 row should show cwd column: %q", depth1.Label)
	}
	if !strings.Contains(depth1.SearchKey, "./web") {
		t.Fatalf("depth 1 search key should include cwd: %q", depth1.SearchKey)
	}
	// The cwd column pushes the title right, so the depth>0 label is wider.
	if i18n.TerminalCellWidth(depth1.Label) <= i18n.TerminalCellWidth(depth0.Label) {
		t.Fatalf("depth 1 label width %d should exceed depth 0 width %d",
			i18n.TerminalCellWidth(depth1.Label), i18n.TerminalCellWidth(depth0.Label))
	}
}

func TestAIResumeFitCell(t *testing.T) {
	if got := aiResumeFitCell("ab", 6); got != "ab    " {
		t.Fatalf("pad short = %q, want %q", got, "ab    ")
	}
	if got := aiResumeFitCell("abcdefgh", 6); got != "abcdef" {
		t.Fatalf("truncate long = %q, want %q", got, "abcdef")
	}
	// Wide runes must be measured in cells, not bytes/runes.
	if got := i18n.TerminalCellWidth(aiResumeFitCell("한글", 6)); got != 6 {
		t.Fatalf("CJK cell width = %d, want 6", got)
	}
}

func TestTruncateAIResumeCells(t *testing.T) {
	if got := truncateAIResumeCells("hello", 10); got != "hello" {
		t.Fatalf("under limit = %q, want unchanged", got)
	}
	got := truncateAIResumeCells("abcdefghij", 5)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("over limit = %q, want ellipsis suffix", got)
	}
	if w := i18n.TerminalCellWidth(got); w > 5 {
		t.Fatalf("clipped width = %d, want <= 5", w)
	}
}

func TestAIResumeRelativeAge(t *testing.T) {
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	if got := aiResumeRelativeAge(now, now.Add(-2*time.Hour), i18n.FallbackLocale); got != "2h" {
		t.Fatalf("relative hours = %q, want %q", got, "2h")
	}
	if got := aiResumeRelativeAge(now, now.Add(-72*time.Hour), i18n.FallbackLocale); got != "3d" {
		t.Fatalf("relative days = %q, want %q", got, "3d")
	}
	// Unknown timestamps yield an empty (padded) cell rather than a bogus age.
	if got := aiResumeRelativeAge(time.Time{}, now, i18n.FallbackLocale); got != "" {
		t.Fatalf("zero now = %q, want empty", got)
	}
	if got := aiResumeRelativeAge(now, time.Time{}, i18n.FallbackLocale); got != "" {
		t.Fatalf("zero modified = %q, want empty", got)
	}
	// Future timestamps clamp to zero instead of going negative.
	if got := aiResumeRelativeAge(now, now.Add(time.Hour), i18n.FallbackLocale); got != "0s" {
		t.Fatalf("future = %q, want %q", got, "0s")
	}
}

func TestAIResumeTurnsCell(t *testing.T) {
	// Known counts render as "<n>t" left-aligned in a fixed-width cell.
	if got := aiResumeTurnsCell(8); strings.TrimRight(got, " ") != "8t" {
		t.Fatalf("turns cell = %q, want %q", got, "8t")
	}
	if got := aiResumeTurnsCell(120); strings.TrimRight(got, " ") != "120t" {
		t.Fatalf("turns cell = %q, want %q", got, "120t")
	}
	if w := i18n.TerminalCellWidth(aiResumeTurnsCell(31)); w != aiResumeTurnsCellWidth {
		t.Fatalf("turns cell width = %d, want %d", w, aiResumeTurnsCellWidth)
	}
	// Unknown (zero) turns render as a blank padded cell, not "0t".
	blank := aiResumeTurnsCell(0)
	if strings.TrimSpace(blank) != "" {
		t.Fatalf("zero turns cell = %q, want blank", blank)
	}
	if w := i18n.TerminalCellWidth(blank); w != aiResumeTurnsCellWidth {
		t.Fatalf("blank turns cell width = %d, want %d", w, aiResumeTurnsCellWidth)
	}
}

func TestAIResumeAgentBadgeTightBracketsAndColor(t *testing.T) {
	// Padding sits outside the brackets: "[codex]", never "[codex ]".
	badge := aiResumeAgentBadge(aiModeCodex)
	if !strings.Contains(badge, "[codex]") {
		t.Fatalf("codex badge = %q, want tight [codex]", badge)
	}
	if strings.Contains(badge, "[codex ") {
		t.Fatalf("codex badge = %q, want no padding inside brackets", badge)
	}
	// Every agent badge occupies the same visible cell width regardless of name.
	for _, agent := range []string{aiModeClaude, aiModeCodex, aiModeAntigravity} {
		if w := i18n.TerminalCellWidth(aiResumeAgentBadge(agent)); w != aiResumeBadgeCellWidth {
			t.Fatalf("%s badge width = %d, want %d", agent, w, aiResumeBadgeCellWidth)
		}
	}
	// Distinct agents get distinct colours (per-agent disambiguation).
	claudeColor := aiResumeAgentColor(aiModeClaude)
	codexColor := aiResumeAgentColor(aiModeCodex)
	agyColor := aiResumeAgentColor(aiModeAntigravity)
	if claudeColor == codexColor || codexColor == agyColor || claudeColor == agyColor {
		t.Fatalf("agent colours not distinct: claude=%q codex=%q agy=%q", claudeColor, codexColor, agyColor)
	}
}

func TestAIResumeSessionRowShowsTurns(t *testing.T) {
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	row := aiResumeSessionRow(aisessions.SessionMeta{
		Agent:        aiModeCodex,
		ResumeID:     "019f0000-0000-7000-8000-000000000042",
		Title:        "Optimize picker",
		LastModified: now.Add(-time.Hour),
		Turns:        31,
	}, now, i18n.FallbackLocale, "", 0)
	if !strings.Contains(row.Label, "31t") {
		t.Fatalf("row label = %q, want turn count 31t", row.Label)
	}
}

func TestAIResumePickerNoSessionsDelegatesToAgentPicker(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &capturingAIRunner{result: intpickercompat.Result{Key: "esc"}}
	cmd := testAICommand(home)
	cmd.runner = runner
	cmd.nativePicker = nativePickerFromCompatRunner(runner)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "TMUX_SPLIT_CONTEXT_DIR":
			return work
		default:
			return ""
		}
	}

	if err := cmd.runResumePicker("right"); err != nil {
		t.Fatalf("runResumePicker() error = %v", err)
	}
	if got, want := runner.options.UI, "ai-picker"; got != want {
		t.Fatalf("picker UI = %q, want %q", got, want)
	}
}

func TestAIPickerFiltersDisabledAgents(t *testing.T) {
	home := t.TempDir()
	if err := config.SaveAIEnabledAgentsFile(filepath.Join(home, ".config", "projmux", config.AIEnabledAgentsFileName), []config.AIAgentProvider{config.AIAgentClaude}); err != nil {
		t.Fatalf("SaveAIEnabledAgentsFile() error = %v", err)
	}
	runner := &capturingAIRunner{}
	cmd := testAICommand(home)
	cmd.runner = runner
	cmd.nativePicker = nativePickerFromCompatRunner(runner)

	if _, err := cmd.runAgentPicker("right"); err != nil {
		t.Fatalf("runAgentPicker error = %v", err)
	}
	if got, want := entryValues(runner.options.Entries), []string{aiModeClaude, aiModeShell}; !reflect.DeepEqual(got, want) {
		t.Fatalf("runner entry order = %#v, want %#v", got, want)
	}
	if hasEntryValue(runner.options.Entries, aiModeCodex) {
		t.Fatalf("runner entries = %#v, want disabled Codex hidden", runner.options.Entries)
	}
}

func TestAIProviderPickerRowsDeriveFromRegistryAndHideDisabledProviders(t *testing.T) {
	home := t.TempDir()
	if err := config.SaveAIEnabledAgentsFile(filepath.Join(home, ".config", "projmux", config.AIEnabledAgentsFileName), []config.AIAgentProvider{config.AIAgentClaude}); err != nil {
		t.Fatalf("SaveAIEnabledAgentsFile() error = %v", err)
	}
	cmd := testAICommand(home)

	rows := cmd.agentRows()
	if got, want := entryValues(rows), []string{string(aiprovider.Claude), aiModeShell}; !reflect.DeepEqual(got, want) {
		t.Fatalf("agentRows values = %#v, want enabled registry providers plus shell %#v", got, want)
	}
	if hasEntryValue(rows, string(aiprovider.Codex)) {
		t.Fatalf("agentRows = %#v, want disabled Codex hidden", rows)
	}
	if hasEntryValue(rows, string(aiprovider.Antigravity)) {
		t.Fatalf("agentRows = %#v, want disabled Antigravity hidden", rows)
	}
}

func TestAIPickerAllAgentsDisabledShowsShellFallbackGuidance(t *testing.T) {
	home := t.TempDir()
	if err := config.SaveAIEnabledAgentsFile(filepath.Join(home, ".config", "projmux", config.AIEnabledAgentsFileName), nil); err != nil {
		t.Fatalf("SaveAIEnabledAgentsFile() error = %v", err)
	}
	runner := &capturingAIRunner{}
	cmd := testAICommand(home)
	cmd.runner = runner
	cmd.nativePicker = nativePickerFromCompatRunner(runner)

	if _, err := cmd.runAgentPicker("down"); err != nil {
		t.Fatalf("runAgentPicker error = %v", err)
	}
	if got, want := entryValues(runner.options.Entries), []string{"", aiModeShell}; !reflect.DeepEqual(got, want) {
		t.Fatalf("runner entry order = %#v, want guidance plus shell fallback %#v", got, want)
	}
	if !hasEntryLabelContainingAll(runner.options.Entries, "AI agents disabled", "shell") {
		t.Fatalf("runner entries = %#v, want disabled-agent guidance", runner.options.Entries)
	}
	if hasEntryValue(runner.options.Entries, aiModeClaude) || hasEntryValue(runner.options.Entries, aiModeCodex) || hasEntryValue(runner.options.Entries, aiModeAntigravity) {
		t.Fatalf("runner entries = %#v, want all AI agents hidden", runner.options.Entries)
	}
}

func TestAIPickerMarksAgentReadyWhenBinaryExistsWithoutLegacyWrapper(t *testing.T) {
	home := t.TempDir()
	codexBin := writeExecutable(t, filepath.Join(home, "bin", "codex"))
	claudeBin := writeExecutable(t, filepath.Join(home, "bin", "claude"))
	cmd := testAICommand(home)
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "codex"}) {
			return []byte(codexBin + "\n"), nil
		}
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "claude"}) {
			return []byte(claudeBin + "\n"), nil
		}
		return nil, os.ErrNotExist
	}

	rows := cmd.agentRows()
	if len(rows) < 2 {
		t.Fatalf("agentRows len = %d, want at least 2", len(rows))
	}
	for _, row := range rows[:2] {
		if !strings.Contains(row.Label, "[READY]") {
			t.Fatalf("row label = %q, want READY without legacy wrapper", row.Label)
		}
	}
}

func TestFindAgentBinaryDiscoversNodeManagerInstalls(t *testing.T) {
	cases := []struct {
		name    string
		mode    string
		binPath string
	}{
		{"codex via nvm", aiModeCodex, filepath.Join(".nvm", "versions", "node", "v24.15.0", "bin", "codex")},
		{"claude via nvm", aiModeClaude, filepath.Join(".nvm", "versions", "node", "v22.0.0", "bin", "claude")},
		{"antigravity via nvm", aiModeAntigravity, filepath.Join(".nvm", "versions", "node", "v24.15.0", "bin", "agy")},
		{"codex via fnm", aiModeCodex, filepath.Join(".fnm", "node-versions", "v22.4.0", "installation", "bin", "codex")},
		{"codex via asdf", aiModeCodex, filepath.Join(".asdf", "installs", "nodejs", "20.10.0", "bin", "codex")},
		{"claude via volta", aiModeClaude, filepath.Join(".volta", "bin", "claude")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			absBin := writeExecutable(t, filepath.Join(home, tc.binPath))
			cmd := testAICommand(home)

			got := cmd.findAgentBinary(tc.mode)
			if got != absBin {
				t.Fatalf("findAgentBinary(%q) = %q, want %q", tc.mode, got, absBin)
			}
			if !cmd.agentAvailable(tc.mode) {
				t.Fatalf("agentAvailable(%q) = false, want true", tc.mode)
			}
		})
	}
}

func TestFindAgentBinaryPrefersPathOverNodeManager(t *testing.T) {
	home := t.TempDir()
	pathBin := writeExecutable(t, filepath.Join(home, "system-bin", "codex"))
	nvmBin := writeExecutable(t, filepath.Join(home, ".nvm", "versions", "node", "v24.15.0", "bin", "codex"))
	cmd := testAICommand(home)
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "codex"}) {
			return []byte(pathBin + "\n"), nil
		}
		return nil, os.ErrNotExist
	}

	got := cmd.findAgentBinary(aiModeCodex)
	if got != pathBin {
		t.Fatalf("findAgentBinary = %q, want PATH hit %q (nvm fallback was %q)", got, pathBin, nvmBin)
	}
}

func TestFindAgentBinaryPicksNewestNvmVersion(t *testing.T) {
	home := t.TempDir()
	older := writeExecutable(t, filepath.Join(home, ".nvm", "versions", "node", "v18.0.0", "bin", "codex"))
	newer := writeExecutable(t, filepath.Join(home, ".nvm", "versions", "node", "v24.15.0", "bin", "codex"))
	past := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(older, past, past); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	cmd := testAICommand(home)
	got := cmd.findAgentBinary(aiModeCodex)
	if got != newer {
		t.Fatalf("findAgentBinary = %q, want newest %q (older candidate %q)", got, newer, older)
	}
}

func TestFindAgentBinaryReturnsEmptyWhenAbsent(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	if got := cmd.findAgentBinary(aiModeCodex); got != "" {
		t.Fatalf("findAgentBinary on empty home = %q, want empty", got)
	}
	if cmd.agentAvailable(aiModeCodex) {
		t.Fatalf("agentAvailable on empty home = true, want false")
	}
}

func TestAISplitMissingRunnerPreservesTmuxMessage(t *testing.T) {
	cmd := testAICommand(t.TempDir())

	err := cmd.Run([]string{"split", "--agent", "codex", "right"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || err.Error() != "selected runner is not installed: codex" {
		t.Fatalf("Run split missing codex error = %v, want selected runner is not installed: codex", err)
	}
}

func TestAISplitMissingAntigravityRunnerReportsMode(t *testing.T) {
	cmd := testAICommand(t.TempDir())
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		cmdRecorder(cmd).commands = append(cmdRecorder(cmd).commands, recordedAICommand{name: name, args: append([]string(nil), args...)})
		return nil, os.ErrNotExist
	}

	err := cmd.Run([]string{"split", "--agent", "antigravity", "right"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || err.Error() != "selected runner is not installed: antigravity" {
		t.Fatalf("Run split missing antigravity error = %v, want selected runner is not installed: antigravity", err)
	}
	if !containsAICommandArgs(cmdRecorder(cmd).commands, "command", []string{"-v", "agy"}) {
		t.Fatalf("commands = %#v, want agy lookup", cmdRecorder(cmd).commands)
	}
}

func TestAISplitDirectDisabledAgentFailsBeforeRunnerLookup(t *testing.T) {
	home := t.TempDir()
	if err := config.SaveAIEnabledAgentsFile(filepath.Join(home, ".config", "projmux", config.AIEnabledAgentsFileName), []config.AIAgentProvider{config.AIAgentCodex}); err != nil {
		t.Fatalf("SaveAIEnabledAgentsFile() error = %v", err)
	}
	cmd := testAICommand(home)
	cmd.readCommand = func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("disabled direct agent should fail before command lookup")
		return nil, os.ErrNotExist
	}

	err := cmd.Run([]string{"split", "--agent", "claude", "right"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("Run split --agent claude error = nil, want disabled-agent error")
	}
	for _, want := range []string{"AI agent claude is disabled", "Settings > AI Settings > Enabled agents", "--force-agent"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want substring %q", err.Error(), want)
		}
	}
	if len(cmdRecorder(cmd).commands) != 0 {
		t.Fatalf("commands = %#v, want none", cmdRecorder(cmd).commands)
	}
}

func TestAISplitDefaultDisabledAgentFailsClearly(t *testing.T) {
	home := t.TempDir()
	if err := config.SaveAIEnabledAgentsFile(filepath.Join(home, ".config", "projmux", config.AIEnabledAgentsFileName), []config.AIAgentProvider{config.AIAgentClaude}); err != nil {
		t.Fatalf("SaveAIEnabledAgentsFile() error = %v", err)
	}
	cmd := testAICommand(home)
	if err := cmd.setMode(aiModeCodex); err != nil {
		t.Fatalf("setMode(codex) error = %v", err)
	}
	cmdRecorder(cmd).commands = nil

	err := cmd.Run([]string{"split", "down"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("Run split with disabled default error = nil, want disabled-default error")
	}
	for _, want := range []string{"AI split default codex is disabled", "choose another default", "--agent shell"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want substring %q", err.Error(), want)
		}
	}
	if len(cmdRecorder(cmd).commands) != 0 {
		t.Fatalf("commands = %#v, want no launch commands", cmdRecorder(cmd).commands)
	}
}

func TestAISplitDisabledConcreteAgentFailsBeforeRunnerFocusOrSplit(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		agent      string
		enabled    []config.AIAgentProvider
		defaultSet bool
		want       []string
	}{
		{
			name:    "direct",
			args:    []string{"split", "--agent", "claude", "right"},
			agent:   aiModeClaude,
			enabled: []config.AIAgentProvider{config.AIAgentCodex},
			want:    []string{"AI agent claude is disabled", "--force-agent"},
		},
		{
			name:       "default",
			args:       []string{"split", "right"},
			agent:      aiModeCodex,
			enabled:    []config.AIAgentProvider{config.AIAgentClaude},
			defaultSet: true,
			want:       []string{"AI split default codex is disabled", "choose another default"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			work := filepath.Join(home, "repo")
			if err := os.MkdirAll(work, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := config.SaveAIEnabledAgentsFile(filepath.Join(home, ".config", "projmux", config.AIEnabledAgentsFileName), tt.enabled); err != nil {
				t.Fatalf("SaveAIEnabledAgentsFile() error = %v", err)
			}
			cmd := testAICommand(home)
			if tt.defaultSet {
				if err := cmd.setMode(tt.agent); err != nil {
					t.Fatalf("setMode(%s) error = %v", tt.agent, err)
				}
				cmdRecorder(cmd).commands = nil
			}
			cmd.lookupEnv = func(name string) string {
				switch name {
				case "HOME":
					return home
				case "TMUX":
					return "/tmp/tmux"
				default:
					return ""
				}
			}
			cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
				cmdRecorder(cmd).commands = append(cmdRecorder(cmd).commands, recordedAICommand{name: name, args: append([]string(nil), args...)})
				if name != "tmux" {
					return nil, os.ErrNotExist
				}
				switch {
				case reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{pane_id}"}):
					return []byte("%1\n"), nil
				case reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{pane_current_path}"}):
					return []byte(work + "\n"), nil
				}
				return nil, os.ErrNotExist
			}

			err := cmd.Run(tt.args, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil {
				t.Fatalf("Run(%v) error = nil, want disabled-agent error", tt.args)
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %q, want substring %q", err.Error(), want)
				}
			}
			commands := cmdRecorder(cmd).commands
			for _, forbidden := range [][]string{
				{"select-pane", "-t", "%2"},
				{"split-window"},
			} {
				if containsAICommandArgs(commands, "tmux", forbidden) {
					t.Fatalf("commands = %#v, disabled agent must fail before %v", commands, forbidden)
				}
			}
		})
	}
}

func TestAISplitForceAgentOverridesDisabledDirectOnly(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveAIEnabledAgentsFile(filepath.Join(home, ".config", "projmux", config.AIEnabledAgentsFileName), nil); err != nil {
		t.Fatalf("SaveAIEnabledAgentsFile() error = %v", err)
	}
	codexBin := writeExecutable(t, filepath.Join(home, "bin", "codex"))
	cmd := testAICommand(home)
	stubAISplitReadCommand(cmd, home, work, map[string]string{"codex": codexBin}, "%7", "%9")

	if err := cmd.Run([]string{"split", "--agent", "codex", "--force-agent", "right"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run split --force-agent error = %v", err)
	}
	if !containsAICommandArgs(cmdRecorder(cmd).commands, "tmux", []string{"set-option", "-p", "-t", "%9", aiPaneAgentOption, aiModeCodex}) {
		t.Fatalf("commands = %#v, want forced Codex launch metadata", cmdRecorder(cmd).commands)
	}
}

func TestAISplitSelectiveDelegatesToPopupToggle(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.executable = func() (string, error) { return "/tmp/projmux bin", nil }
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{client_tty}"}) {
			return []byte("/dev/pts/7\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"split", "right"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run split error = %v", err)
	}

	want := []recordedAICommand{{
		name: "/tmp/projmux bin",
		args: []string{"internal", "tmux", "popup-toggle", "--client", "/dev/pts/7", "ai-split-picker-right"},
	}}
	if !reflect.DeepEqual(cmdRecorder(cmd).commands, want) {
		t.Fatalf("commands = %#v, want %#v", cmdRecorder(cmd).commands, want)
	}
}

func TestAISplitDirectAlwaysCreatesNewPaneWithoutReuseProbe(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	codexBin := writeExecutable(t, filepath.Join(home, "bin", "codex"))
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "TMUX":
			return "/tmp/tmux"
		default:
			return ""
		}
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		cmdRecorder(cmd).commands = append(cmdRecorder(cmd).commands, recordedAICommand{name: name, args: append([]string(nil), args...)})
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "codex"}) {
			return []byte(codexBin + "\n"), nil
		}
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{pane_id}"}):
			return []byte("%1\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{pane_current_path}"}):
			return []byte(work + "\n"), nil
		case len(args) >= 6 && reflect.DeepEqual(args[:4], []string{"split-window", "-P", "-F", "#{pane_id}"}):
			return []byte("%9\n"), nil
		case reflect.DeepEqual(args, []string{"list-panes", "-t", "%1", "-F", "#{pane_id}\t#{pane_left}\t#{pane_top}\t#{pane_width}\t#{pane_height}"}):
			return []byte("%1\t0\t0\t40\t10\n%2\t41\t0\t40\t10\n%9\t82\t0\t40\t10\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"split", "--agent", "codex", "right"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run split --agent codex error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if !containsAICommandArgs(commands, "tmux", []string{"split-window", "-P", "-F", "#{pane_id}", "-h", "-t", "%1", "-c", work, "/bin/sh", "-lc"}) {
		t.Fatalf("commands = %#v, want new Codex split-window", commands)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%9", aiPaneAgentOption, aiModeCodex}) {
		t.Fatalf("commands = %#v, want new pane Codex metadata", commands)
	}
	if containsAICommandArgs(commands, "tmux", []string{"select-pane", "-t", "%2"}) {
		t.Fatalf("commands = %#v, direct split must not select preexisting AI pane", commands)
	}
	if containsAICommandArgs(commands, "tmux", []string{"list-panes", "-s", "-t", "%1"}) {
		t.Fatalf("commands = %#v, direct split must not probe existing AI panes for reuse", commands)
	}
}

func TestAISplitDefaultAlwaysCreatesNewPaneWithoutReuseProbe(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	codexBin := writeExecutable(t, filepath.Join(home, "bin", "codex"))
	cmd := testAICommand(home)
	if err := cmd.setMode(aiModeCodex); err != nil {
		t.Fatal(err)
	}
	cmdRecorder(cmd).commands = nil
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "TMUX":
			return "/tmp/tmux"
		default:
			return ""
		}
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		cmdRecorder(cmd).commands = append(cmdRecorder(cmd).commands, recordedAICommand{name: name, args: append([]string(nil), args...)})
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "codex"}) {
			return []byte(codexBin + "\n"), nil
		}
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{pane_id}"}):
			return []byte("%1\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{pane_current_path}"}):
			return []byte(work + "\n"), nil
		case len(args) >= 6 && reflect.DeepEqual(args[:4], []string{"split-window", "-P", "-F", "#{pane_id}"}):
			return []byte("%9\n"), nil
		case reflect.DeepEqual(args, []string{"list-panes", "-t", "%1", "-F", "#{pane_id}\t#{pane_left}\t#{pane_top}\t#{pane_width}\t#{pane_height}"}):
			return []byte("%1\t0\t0\t40\t10\n%2\t0\t11\t40\t10\n%9\t0\t22\t40\t10\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"split", "down"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run split default codex error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if !containsAICommandArgs(commands, "tmux", []string{"split-window", "-P", "-F", "#{pane_id}", "-v", "-t", "%1", "-c", work, "/bin/sh", "-lc"}) {
		t.Fatalf("commands = %#v, want new default Codex split-window", commands)
	}
	if containsAICommandArgs(commands, "tmux", []string{"select-pane", "-t", "%2"}) {
		t.Fatalf("commands = %#v, default split must not select preexisting AI pane", commands)
	}
	if containsAICommandArgs(commands, "tmux", []string{"list-panes", "-s", "-t", "%1"}) {
		t.Fatalf("commands = %#v, default split must not probe existing AI panes for reuse", commands)
	}
}

func TestAISplitDirectFromCurrentAIPaneStillCreatesNewPane(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	codexBin := writeExecutable(t, filepath.Join(home, "bin", "codex"))
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "TMUX":
			return "/tmp/tmux"
		default:
			return ""
		}
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		cmdRecorder(cmd).commands = append(cmdRecorder(cmd).commands, recordedAICommand{name: name, args: append([]string(nil), args...)})
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "codex"}) {
			return []byte(codexBin + "\n"), nil
		}
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{pane_id}"}):
			return []byte("%2\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{pane_current_path}"}):
			return []byte(work + "\n"), nil
		case len(args) >= 6 && reflect.DeepEqual(args[:4], []string{"split-window", "-P", "-F", "#{pane_id}"}):
			return []byte("%10\n"), nil
		case reflect.DeepEqual(args, []string{"list-panes", "-t", "%2", "-F", "#{pane_id}\t#{pane_left}\t#{pane_top}\t#{pane_width}\t#{pane_height}"}):
			return []byte("%1\t0\t0\t40\t10\n%2\t41\t0\t40\t10\n%10\t82\t0\t40\t10\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"split", "--agent", "codex", "right"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run split --agent codex error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if !containsAICommandArgs(commands, "tmux", []string{"split-window", "-P", "-F", "#{pane_id}", "-h", "-t", "%2", "-c", work, "/bin/sh", "-lc"}) {
		t.Fatalf("commands = %#v, want new Codex split from current AI pane", commands)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%10", aiPaneAgentOption, aiModeCodex}) {
		t.Fatalf("commands = %#v, want new pane Codex metadata", commands)
	}
	if containsAICommandArgs(commands, "tmux", []string{"select-pane", "-t", "%1"}) {
		t.Fatalf("commands = %#v, direct split from AI pane must not select previous pane", commands)
	}
	if containsAICommandArgs(commands, "tmux", []string{"list-panes", "-s", "-t", "%2"}) {
		t.Fatalf("commands = %#v, direct split from AI pane must not probe existing AI panes for reuse", commands)
	}
}

func TestAISplitPickerSelectionPreservesLaunchPathWithExistingManagedPane(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	codexBin := writeExecutable(t, filepath.Join(home, "bin", "codex"))
	runner := &capturingAIRunner{result: intpickercompat.Result{Key: "enter", Value: aiModeCodex}}
	cmd := testAICommand(home)
	cmd.runner = runner
	cmd.nativePicker = nativePickerFromCompatRunner(runner)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "TMUX":
			return "/tmp/tmux"
		case "SHELL":
			return "/bin/bash"
		default:
			return ""
		}
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		cmdRecorder(cmd).commands = append(cmdRecorder(cmd).commands, recordedAICommand{name: name, args: append([]string(nil), args...)})
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "codex"}) {
			return []byte(codexBin + "\n"), nil
		}
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{pane_id}"}):
			return []byte("%1\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{pane_current_path}"}):
			return []byte(work + "\n"), nil
		case len(args) >= 6 && reflect.DeepEqual(args[:4], []string{"split-window", "-P", "-F", "#{pane_id}"}):
			return []byte("%9\n"), nil
		case reflect.DeepEqual(args, []string{"list-panes", "-t", "%1", "-F", "#{pane_id}\t#{pane_left}\t#{pane_top}\t#{pane_width}\t#{pane_height}"}):
			return []byte("%1\t0\t0\t40\t10\n%9\t41\t0\t40\t10\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"picker", "--inside", "right"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run picker --inside error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if !containsAICommandArgs(commands, "tmux", []string{"split-window", "-P", "-F", "#{pane_id}", "-h", "-t", "%1", "-c", work, "/bin/bash", "-lc"}) {
		t.Fatalf("commands = %#v, want picker-selected Codex split-window", commands)
	}
	if containsAICommandArgs(commands, "tmux", []string{"select-pane", "-t", "%2"}) {
		t.Fatalf("commands = %#v, picker path must not select preexisting AI pane", commands)
	}
}

func TestAISplitCodexRunsNativeTmuxSplitAndStartsWatcher(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	codexBin := writeExecutable(t, filepath.Join(home, "bin", "codex"))
	cmd := testAICommand(home)
	if err := cmd.setMode("codex"); err != nil {
		t.Fatal(err)
	}
	cmdRecorder(cmd).commands = nil
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "TMUX":
			return "/tmp/tmux"
		case "SHELL":
			return "/bin/bash"
		default:
			return ""
		}
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		cmdRecorder(cmd).commands = append(cmdRecorder(cmd).commands, recordedAICommand{name: name, args: append([]string(nil), args...)})
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "codex"}) {
			return []byte(codexBin + "\n"), nil
		}
		if name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{pane_id}"}) {
			return []byte("%7\n"), nil
		}
		if name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{pane_current_path}"}) {
			return []byte(work + "\n"), nil
		}
		if name == "tmux" && len(args) >= 6 && reflect.DeepEqual(args[:6], []string{"split-window", "-P", "-F", "#{pane_id}", "-h", "-t"}) {
			return []byte("%9\n"), nil
		}
		if name == "tmux" && reflect.DeepEqual(args, []string{"list-panes", "-t", "%7", "-F", "#{pane_id}\t#{pane_left}\t#{pane_top}\t#{pane_width}\t#{pane_height}"}) {
			return []byte("%2\t0\t0\t20\t10\n%7\t21\t0\t10\t10\n%9\t32\t0\t10\t10\n%8\t0\t11\t42\t10\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"split", "right"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run split codex error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if !containsAICommandArgs(commands, "tmux", []string{"split-window", "-P", "-F", "#{pane_id}", "-h", "-t", "%7", "-c", work, "/bin/bash", "-lc"}) {
		t.Fatalf("commands = %#v, want native tmux split-window", commands)
	}
	for _, want := range [][]string{
		{"resize-pane", "-t", "%2", "-x", "14"},
		{"resize-pane", "-t", "%7", "-x", "13"},
		{"resize-pane", "-t", "%9", "-x", "13"},
	} {
		if !containsAICommandArgs(commands, "tmux", want) {
			t.Fatalf("commands = %#v, want scoped row resize %v", commands, want)
		}
	}
	if containsAICommandArgs(commands, "tmux", []string{"resize-pane", "-t", "%8", "-x", "13"}) {
		t.Fatalf("commands = %#v, did not expect resize outside target row", commands)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"run-shell", "-b", "'/tmp/projmux' internal agent-hook watch-title '%9'"}) {
		t.Fatalf("commands = %#v, want codex watch-title run-shell", commands)
	}
	for _, want := range [][]string{
		{"set-option", "-p", "-t", "%9", "@projmux_ai_managed", "1"},
		{"set-option", "-p", "-t", "%9", "@projmux_ai_agent", "codex"},
		{"set-option", "-p", "-t", "%9", "@projmux_ai_context", work},
		{"set-option", "-p", "-t", "%9", "@projmux_ai_topic", "repo"},
		{"set-option", "-p", "-t", "%9", "@projmux_ai_state", "idle"},
	} {
		if !containsAICommandArgs(commands, "tmux", want) {
			t.Fatalf("commands = %#v, want AI pane metadata %v", commands, want)
		}
	}
	wantLaunchPrefix := "export PATH='" + filepath.Join(home, "bin") + "'\":$PATH\" && cd '" + work + "' && __codex_title='codex:repo'"
	if !containsAICommandArgSubstring(commands, wantLaunchPrefix) {
		t.Fatalf("commands = %#v, want codex launch command starting with %q", commands, wantLaunchPrefix)
	}
}

func TestAgentLaunchCommandPrependsAgentBinDirToPath(t *testing.T) {
	cmd := &aiCommand{}
	const agentBin = "/home/u/.nvm/versions/node/v24.0.0/bin/codex"
	got := cmd.agentLaunchCommandForArgv("codex", filepath.Dir(agentBin), "/work/repo", "codex:repo", []string{agentBin})
	want := `export PATH='/home/u/.nvm/versions/node/v24.0.0/bin'":$PATH"`
	if !strings.HasPrefix(got, want+" && ") {
		t.Fatalf("agentLaunchCommand = %q, want it to start with %q", got, want)
	}
	if !strings.Contains(got, "exec '/home/u/.nvm/versions/node/v24.0.0/bin/codex'") {
		t.Fatalf("agentLaunchCommand = %q, want it to exec the agent binary", got)
	}
}

func TestAISplitAgentFlagLaunchesClaudeWithoutChangingCodexDefault(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	claudeBin := writeExecutable(t, filepath.Join(home, "bin", "claude"))
	cmd := testAICommand(home)
	if err := cmd.setMode(aiModeCodex); err != nil {
		t.Fatal(err)
	}
	cmdRecorder(cmd).commands = nil
	stubAISplitReadCommand(cmd, home, work, map[string]string{"claude": claudeBin}, "%7", "%9")

	stdout := &bytes.Buffer{}
	if err := cmd.Run([]string{"split", "--agent", "claude", "right"}, stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run split --agent claude error = %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty without --print-pane-id", stdout.String())
	}

	commands := cmdRecorder(cmd).commands
	if got, want := readModeFile(t, home), "codex\n"; got != want {
		t.Fatalf("mode file = %q, want %q", got, want)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%9", aiPaneAgentOption, aiModeClaude}) {
		t.Fatalf("commands = %#v, want Claude AI pane metadata", commands)
	}
	if !containsAICommandArgSubstring(commands, "exec "+shellQuote(claudeBin)) {
		t.Fatalf("commands = %#v, want Claude exec", commands)
	}
}

func TestAISplitPrintPaneIDDirectAgent(t *testing.T) {
	for _, tt := range []struct {
		agent  string
		binary string
	}{
		{agent: aiModeClaude, binary: "claude"},
		{agent: aiModeCodex, binary: "codex"},
		{agent: aiModeAntigravity, binary: "agy"},
	} {
		t.Run(tt.agent, func(t *testing.T) {
			home := t.TempDir()
			work := filepath.Join(home, "repo")
			if err := os.MkdirAll(work, 0o755); err != nil {
				t.Fatal(err)
			}
			agentBin := writeExecutable(t, filepath.Join(home, "bin", tt.binary))
			cmd := testAICommand(home)
			stubAISplitReadCommand(cmd, home, work, map[string]string{tt.binary: agentBin}, "%7", "%29")

			stdout := &bytes.Buffer{}
			err := cmd.Run([]string{"split", "--agent", tt.agent, "--print-pane-id", "right", "--", "prompt"}, stdout, &bytes.Buffer{})
			if err != nil {
				t.Fatalf("Run split --print-pane-id error = %v", err)
			}
			if got, want := stdout.String(), "%29\n"; got != want {
				t.Fatalf("stdout = %q, want exactly %q", got, want)
			}
			if !containsAICommandArgSubstring(cmdRecorder(cmd).commands, "exec "+shellQuote(agentBin)+" 'prompt'") {
				t.Fatalf("commands = %#v, want prompt argv tail preserved", cmdRecorder(cmd).commands)
			}
		})
	}
}

func TestAISplitPrintPaneIDRejectsMissingOrInvalidBackendResult(t *testing.T) {
	for _, paneID := range []string{"", "pane-9"} {
		t.Run(fmt.Sprintf("pane_id_%q", paneID), func(t *testing.T) {
			home := t.TempDir()
			work := filepath.Join(home, "repo")
			if err := os.MkdirAll(work, 0o755); err != nil {
				t.Fatal(err)
			}
			codexBin := writeExecutable(t, filepath.Join(home, "bin", "codex"))
			cmd := testAICommand(home)
			stubAISplitReadCommand(cmd, home, work, map[string]string{"codex": codexBin}, "%7", paneID)
			stdout := &bytes.Buffer{}

			err := cmd.Run([]string{"split", "--agent", "codex", "--print-pane-id", "right"}, stdout, &bytes.Buffer{})
			if err == nil {
				t.Fatal("Run split --print-pane-id error = nil, want backend contract failure")
			}
			for _, want := range []string{"--print-pane-id", "tmux backend", "expected %N", "split-window -P -F"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %q, want contains %q", err, want)
				}
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty on error", stdout.String())
			}
			if containsAICommandArgs(cmdRecorder(cmd).commands, "tmux", []string{"set-option", "-p"}) {
				t.Fatalf("commands = %#v, pane setup must stop after invalid split result", cmdRecorder(cmd).commands)
			}
		})
	}
}

func TestAISplitAgentFlagLaunchesCodexWithoutChangingClaudeDefault(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	codexBin := writeExecutable(t, filepath.Join(home, "bin", "codex"))
	cmd := testAICommand(home)
	if err := cmd.setMode(aiModeClaude); err != nil {
		t.Fatal(err)
	}
	cmdRecorder(cmd).commands = nil
	stubAISplitReadCommand(cmd, home, work, map[string]string{"codex": codexBin}, "%7", "%9")

	if err := cmd.Run([]string{"split", "--agent=codex", "down"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run split --agent codex error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if got, want := readModeFile(t, home), "claude\n"; got != want {
		t.Fatalf("mode file = %q, want %q", got, want)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"split-window", "-P", "-F", "#{pane_id}", "-v", "-t", "%7", "-c", work, "/bin/bash", "-lc"}) {
		t.Fatalf("commands = %#v, want vertical Codex split", commands)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%9", aiPaneAgentOption, aiModeCodex}) {
		t.Fatalf("commands = %#v, want Codex AI pane metadata", commands)
	}
	if !containsAICommandArgSubstring(commands, "exec "+shellQuote(codexBin)) {
		t.Fatalf("commands = %#v, want Codex exec", commands)
	}
}

func TestAISplitAgentFlagLaunchesAntigravityWithoutChangingDefault(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	agyBin := writeExecutable(t, filepath.Join(home, "bin", "agy"))
	cmd := testAICommand(home)
	if err := cmd.setMode(aiModeClaude); err != nil {
		t.Fatal(err)
	}
	cmdRecorder(cmd).commands = nil
	stubAISplitReadCommand(cmd, home, work, map[string]string{"agy": agyBin}, "%7", "%9")

	if err := cmd.Run([]string{"split", "--agent=antigravity", "down"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run split --agent antigravity error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if got, want := readModeFile(t, home), "claude\n"; got != want {
		t.Fatalf("mode file = %q, want %q", got, want)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"split-window", "-P", "-F", "#{pane_id}", "-v", "-t", "%7", "-c", work, "/bin/bash", "-lc"}) {
		t.Fatalf("commands = %#v, want vertical Antigravity split", commands)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%9", aiPaneAgentOption, aiModeAntigravity}) {
		t.Fatalf("commands = %#v, want Antigravity AI pane metadata", commands)
	}
	if !containsAICommandArgSubstring(commands, "exec "+shellQuote(agyBin)) {
		t.Fatalf("commands = %#v, want Antigravity exec", commands)
	}
}

func TestAISplitCodexExtraArgsKeepsPaneSetupWatcherAndLayout(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	codexBin := writeExecutable(t, filepath.Join(home, "bin", "codex"))
	cmd := testAICommand(home)
	stubAISplitReadCommand(cmd, home, work, map[string]string{"codex": codexBin}, "%7", "%9")

	err := cmd.Run([]string{"split", "--agent", "codex", "right", "--", "--model", "gpt-5.1 codex", "quote'd"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("Run split --agent codex -- extra args error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	wantExec := "exec " + shellQuote(codexBin) + " '--model' 'gpt-5.1 codex' 'quote'\\''d'"
	if !containsAICommandArgSubstring(commands, wantExec) {
		t.Fatalf("commands = %#v, want resolved Codex exec with extra args %q", commands, wantExec)
	}
	for _, want := range [][]string{
		{"set-option", "-p", "-t", "%9", aiPaneManagedOption, "1"},
		{"set-option", "-p", "-t", "%9", aiPaneAgentOption, aiModeCodex},
		{"set-option", "-p", "-t", "%9", aiPaneContextOption, work},
		{"set-option", "-p", "-t", "%9", aiPaneTopicOption, "repo"},
		{"set-option", "-p", "-t", "%9", aiPaneStateOption, "idle"},
		{"run-shell", "-b", "'/tmp/projmux' internal agent-hook watch-title '%9'"},
		{"resize-pane", "-t", "%7", "-x", "40"},
		{"resize-pane", "-t", "%9", "-x", "40"},
	} {
		if !containsAICommandArgs(commands, "tmux", want) {
			t.Fatalf("commands = %#v, want command %v", commands, want)
		}
	}
	if !containsAICommandArgs(commands, "command", []string{"-v", "codex"}) {
		t.Fatalf("commands = %#v, want Codex binary lookup", commands)
	}
}

func TestAISplitClaudeExtraArgsUseResolvedBinary(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	claudeBin := writeExecutable(t, filepath.Join(home, "bin", "claude"))
	cmd := testAICommand(home)
	stubAISplitReadCommand(cmd, home, work, map[string]string{"claude": claudeBin}, "%7", "%9")

	err := cmd.Run([]string{"split", "--agent", "claude", "down", "--", "--dangerously-skip-permissions"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("Run split --agent claude -- extra args error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	wantExec := "exec " + shellQuote(claudeBin) + " '--dangerously-skip-permissions'"
	if !containsAICommandArgSubstring(commands, wantExec) {
		t.Fatalf("commands = %#v, want resolved Claude exec with extra args %q", commands, wantExec)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"split-window", "-P", "-F", "#{pane_id}", "-v", "-t", "%7", "-c", work, "/bin/bash", "-lc"}) {
		t.Fatalf("commands = %#v, want vertical Claude split", commands)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%9", aiPaneAgentOption, aiModeClaude}) {
		t.Fatalf("commands = %#v, want Claude AI pane metadata", commands)
	}
}

func TestAISplitAgentSelectiveDelegatesToPickerWithoutChangingDefault(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.executable = func() (string, error) { return "/tmp/projmux bin", nil }
	if err := cmd.setMode(aiModeCodex); err != nil {
		t.Fatal(err)
	}
	cmdRecorder(cmd).commands = nil
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{client_tty}"}) {
			return []byte("/dev/pts/7\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"split", "--agent", "selective", "right"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run split --agent selective error = %v", err)
	}

	if got, want := readModeFile(t, home), "codex\n"; got != want {
		t.Fatalf("mode file = %q, want %q", got, want)
	}
	want := []recordedAICommand{{
		name: "/tmp/projmux bin",
		args: []string{"internal", "tmux", "popup-toggle", "--client", "/dev/pts/7", "ai-split-picker-right"},
	}}
	if !reflect.DeepEqual(cmdRecorder(cmd).commands, want) {
		t.Fatalf("commands = %#v, want %#v", cmdRecorder(cmd).commands, want)
	}
}

func TestAISplitAgentResumeDelegatesToResumePickerWithoutChangingDefault(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.executable = func() (string, error) { return "/tmp/projmux bin", nil }
	if err := cmd.setMode(aiModeCodex); err != nil {
		t.Fatal(err)
	}
	cmdRecorder(cmd).commands = nil
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{client_tty}"}) {
			return []byte("/dev/pts/7\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"split", "--agent", "resume", "right"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run split --agent resume error = %v", err)
	}

	if got, want := readModeFile(t, home), "codex\n"; got != want {
		t.Fatalf("mode file = %q, want %q", got, want)
	}
	want := []recordedAICommand{{
		name: "/tmp/projmux bin",
		args: []string{"internal", "tmux", "popup-toggle", "--client", "/dev/pts/7", "ai-split-resume-right"},
	}}
	if !reflect.DeepEqual(cmdRecorder(cmd).commands, want) {
		t.Fatalf("commands = %#v, want %#v", cmdRecorder(cmd).commands, want)
	}
}

func TestAIResumePickerNewDelegatesToAgentPicker(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCodexResumeSession(t, home, "019f0000-0000-7000-8000-000000000077", work, "feat/resume", "Resume existing")
	runner := &sequencingAIRunner{results: []intpickercompat.Result{
		{Key: "enter", Value: aiResumeNewValue},
		{Key: "esc"},
	}}
	cmd := testAICommand(home)
	cmd.runner = runner
	cmd.nativePicker = nativePickerFromCompatRunner(runner)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "TMUX_SPLIT_CONTEXT_DIR":
			return work
		default:
			return ""
		}
	}

	if err := cmd.runResumePicker("right"); err != nil {
		t.Fatalf("runResumePicker() error = %v", err)
	}
	if len(runner.options) != 2 {
		t.Fatalf("picker calls = %d, want resume picker then agent picker", len(runner.options))
	}
	if got, want := runner.options[0].UI, "ai-resume-picker"; got != want {
		t.Fatalf("first picker UI = %q, want %q", got, want)
	}
	if got, want := runner.options[1].UI, "ai-picker"; got != want {
		t.Fatalf("second picker UI = %q, want %q", got, want)
	}
}

func TestAIResumePickerSessionRowRunsCodexResumeAndRecordsPaneMetadata(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	resumeID := "019f0000-0000-7000-8000-000000000078"
	writeCodexResumeSession(t, home, resumeID, work, "feat/resume", "Resume existing")
	codexBin := writeExecutable(t, filepath.Join(home, "bin", "codex"))
	runner := &capturingAIRunner{result: intpickercompat.Result{Key: "enter", Value: aiResumePickerValue(aiModeCodex, resumeID)}}
	cmd := testAICommand(home)
	cmd.runner = runner
	cmd.nativePicker = nativePickerFromCompatRunner(runner)
	stubAISplitReadCommand(cmd, home, work, map[string]string{"codex": codexBin}, "%7", "%9")

	if err := cmd.runResumePicker("right"); err != nil {
		t.Fatalf("runResumePicker() error = %v", err)
	}
	if got, want := runner.options.UI, "ai-resume-picker"; got != want {
		t.Fatalf("picker UI = %q, want %q", got, want)
	}
	if !strings.Contains(runner.options.Footer, "Showing latest 1 resume sessions") {
		t.Fatalf("picker footer = %q, want capped-count footer", runner.options.Footer)
	}
	commands := cmdRecorder(cmd).commands
	wantExec := "exec " + shellQuote(codexBin) + " 'resume' '" + resumeID + "'"
	if !containsAICommandArgSubstring(commands, wantExec) {
		t.Fatalf("commands = %#v, want Codex resume exec %q", commands, wantExec)
	}
	for _, want := range [][]string{
		{"set-option", "-p", "-t", "%9", aiPaneAgentOption, aiModeCodex},
		{"set-option", "-p", "-t", "%9", aiPaneSessionIDOption, resumeID},
		{"set-option", "-p", "-t", "%9", aiPaneResumeIDOption, resumeID},
		{"set-option", "-p", "-t", "%9", aiPaneResumeSourceOption, aisessions.SourceCodexRollout},
		{"set-option", "-p", "-t", "%9", aiPaneResumeUpdatedAtOption, "2026-06-25T09:00:00Z"},
	} {
		if !containsAICommandArgs(commands, "tmux", want) {
			t.Fatalf("commands = %#v, want resume metadata command %v", commands, want)
		}
	}
}

func TestAIResumePickerCurrentStorageAntigravityRunsConversationResume(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	cacheDir := filepath.Join(home, ".gemini", "antigravity-cli", "cache")
	dbDir := filepath.Join(home, ".gemini", "antigravity-cli", "conversations")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	resumeID := "123e4567-e89b-42d3-a456-426614174001"
	writeFile(t, filepath.Join(cacheDir, "last_conversations.json"), `{"`+work+`":"`+resumeID+`"}`)
	dbPath := filepath.Join(dbDir, resumeID+".db")
	writeFile(t, dbPath, "synthetic opaque db placeholder")
	updatedAt := time.Date(2026, 8, 12, 9, 30, 0, 0, time.UTC)
	if err := os.Chtimes(dbPath, updatedAt, updatedAt); err != nil {
		t.Fatal(err)
	}
	agyBin := writeExecutable(t, filepath.Join(home, "bin", "agy"))
	runner := &capturingAIRunner{result: intpickercompat.Result{Key: "enter", Value: aiResumePickerValue(aiModeAntigravity, resumeID)}}
	cmd := testAICommand(home)
	cmd.runner = runner
	cmd.nativePicker = nativePickerFromCompatRunner(runner)
	stubAISplitReadCommand(cmd, home, work, map[string]string{"agy": agyBin}, "%7", "%9")

	if err := cmd.runResumePicker("right"); err != nil {
		t.Fatalf("runResumePicker() error = %v", err)
	}
	commands := cmdRecorder(cmd).commands
	wantExec := "exec " + shellQuote(agyBin) + " '--conversation' '" + resumeID + "'"
	if !containsAICommandArgSubstring(commands, wantExec) {
		t.Fatalf("commands = %#v, want Antigravity resume exec %q", commands, wantExec)
	}
	for _, want := range [][]string{
		{"set-option", "-p", "-t", "%9", aiPaneAgentOption, aiModeAntigravity},
		{"set-option", "-p", "-t", "%9", aiPaneResumeIDOption, resumeID},
		{"set-option", "-p", "-t", "%9", aiPaneResumeSourceOption, aisessions.SourceAntigravityLastConversation},
		{"set-option", "-p", "-t", "%9", aiPaneResumeUpdatedAtOption, updatedAt.Format(time.RFC3339)},
	} {
		if !containsAICommandArgs(commands, "tmux", want) {
			t.Fatalf("commands = %#v, want current-storage resume metadata command %v", commands, want)
		}
	}
}

func TestRunSelectedResumeSessionRunsClaudeResume(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	claudeBin := writeExecutable(t, filepath.Join(home, "bin", "claude"))
	cmd := testAICommand(home)
	stubAISplitReadCommand(cmd, home, work, map[string]string{"claude": claudeBin}, "%7", "%9")
	resumeID := "018f4c2d-abc_DEF.123"
	updatedAt := time.Date(2026, 6, 25, 10, 30, 0, 0, time.UTC)

	err := cmd.runSelectedResumeSession(aiResumeSelection{
		agent:     aiModeClaude,
		resumeID:  resumeID,
		source:    aisessions.SourceClaudeTranscript,
		updatedAt: updatedAt,
	}, "down")
	if err != nil {
		t.Fatalf("runSelectedResumeSession() error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	wantExec := "exec " + shellQuote(claudeBin) + " '--resume' '" + resumeID + "'"
	if !containsAICommandArgSubstring(commands, wantExec) {
		t.Fatalf("commands = %#v, want Claude resume exec %q", commands, wantExec)
	}
	for _, want := range [][]string{
		{"split-window", "-P", "-F", "#{pane_id}", "-v", "-t", "%7", "-c", work, "/bin/bash", "-lc"},
		{"set-option", "-p", "-t", "%9", aiPaneAgentOption, aiModeClaude},
		{"set-option", "-p", "-t", "%9", aiPaneSessionIDOption, resumeID},
		{"set-option", "-p", "-t", "%9", aiPaneResumeIDOption, resumeID},
		{"set-option", "-p", "-t", "%9", aiPaneResumeSourceOption, aisessions.SourceClaudeTranscript},
		{"set-option", "-p", "-t", "%9", aiPaneResumeUpdatedAtOption, "2026-06-25T10:30:00Z"},
	} {
		if !containsAICommandArgs(commands, "tmux", want) {
			t.Fatalf("commands = %#v, want command %v", commands, want)
		}
	}
}

func TestRunSelectedResumeSessionInvalidResumeIDFallsBackToFresh(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	codexBin := writeExecutable(t, filepath.Join(home, "bin", "codex"))
	cmd := testAICommand(home)
	stubAISplitReadCommand(cmd, home, work, map[string]string{"codex": codexBin}, "%7", "%9")

	err := cmd.runSelectedResumeSession(aiResumeSelection{agent: aiModeCodex, resumeID: "bad\nid"}, "right")
	if err != nil {
		t.Fatalf("runSelectedResumeSession() error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	wantExec := "exec " + shellQuote(codexBin)
	if !containsAICommandArgSubstring(commands, wantExec) {
		t.Fatalf("commands = %#v, want fresh Codex exec %q", commands, wantExec)
	}
	if containsAICommandArgSubstring(commands, "exec "+shellQuote(codexBin)+" 'resume'") {
		t.Fatalf("commands = %#v, invalid resume id should fall back to fresh launch", commands)
	}
	if !containsAICommandArgSubstring(commands, "Could not resume codex session") {
		t.Fatalf("commands = %#v, want fallback message", commands)
	}
	for _, forbidden := range [][]string{
		{"set-option", "-p", "-t", "%9", aiPaneSessionIDOption},
		{"set-option", "-p", "-t", "%9", aiPaneResumeIDOption},
		{"set-option", "-p", "-t", "%9", aiPaneResumeSourceOption},
	} {
		if containsAICommandArgs(commands, "tmux", forbidden) {
			t.Fatalf("commands = %#v, fresh fallback must not write resume metadata %v", commands, forbidden)
		}
	}
}

func TestAISplitAgentShellUsesPlainShellSplit(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "TMUX":
			return "/tmp/tmux"
		case "TMUX_SPLIT_CONTEXT_DIR":
			return work
		case "TMUX_SPLIT_TARGET_PANE":
			return "%7"
		case "SHELL":
			return "/bin/bash"
		default:
			return ""
		}
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%7", "-F", "#{pane_id}"}) {
			return []byte("%7\n"), nil
		}
		if name == "tmux" && reflect.DeepEqual(args, []string{"list-panes", "-t", "%7", "-F", "#{pane_id}\t#{pane_left}\t#{pane_top}\t#{pane_width}\t#{pane_height}"}) {
			return []byte("%7\t0\t0\t40\t10\n%9\t0\t11\t40\t10\n"), nil
		}
		return nil, os.ErrNotExist
	}

	stdout := &bytes.Buffer{}
	if err := cmd.Run([]string{"split", "--agent", "shell", "down"}, stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run split --agent shell error = %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty without --print-pane-id", stdout.String())
	}

	want := []recordedAICommand{
		{name: "tmux", args: []string{"split-window", "-v", "-t", "%7", "-c", work, "/bin/bash", "-l"}},
		{name: "tmux", args: []string{"resize-pane", "-t", "%7", "-y", "10"}},
		{name: "tmux", args: []string{"resize-pane", "-t", "%9", "-y", "10"}},
	}
	if !reflect.DeepEqual(cmdRecorder(cmd).commands, want) {
		t.Fatalf("commands = %#v, want %#v", cmdRecorder(cmd).commands, want)
	}
	for _, forbidden := range [][]string{
		{"set-option", "-p", "-t", "%9", aiPaneManagedOption, "1"},
		{"set-option", "-p", "-t", "%9", aiPaneAgentOption, aiModeShell},
		{"run-shell", "-b", "'/tmp/projmux' internal agent-hook watch-title '%9'"},
	} {
		if containsAICommandArgs(cmdRecorder(cmd).commands, "tmux", forbidden) {
			t.Fatalf("commands = %#v, did not expect managed AI command %v", cmdRecorder(cmd).commands, forbidden)
		}
	}
}

func TestAISplitPrintPaneIDShellUsesTmuxSplitResult(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "TMUX":
			return "/tmp/tmux"
		case "TMUX_SPLIT_CONTEXT_DIR":
			return work
		case "TMUX_SPLIT_TARGET_PANE":
			return "%7"
		case "SHELL":
			return "/bin/bash"
		default:
			return ""
		}
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		cmdRecorder(cmd).commands = append(cmdRecorder(cmd).commands, recordedAICommand{name: name, args: append([]string(nil), args...)})
		if name == "tmux" && len(args) >= 6 && reflect.DeepEqual(args[:6], []string{"split-window", "-P", "-F", "#{pane_id}", "-h", "-t"}) {
			return []byte("%31\n"), nil
		}
		return nil, os.ErrNotExist
	}

	stdout := &bytes.Buffer{}
	if err := cmd.Run([]string{"split", "--agent", "shell", "--print-pane-id", "right"}, stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run shell --print-pane-id error = %v", err)
	}
	if got, want := stdout.String(), "%31\n"; got != want {
		t.Fatalf("stdout = %q, want exactly %q", got, want)
	}
	if !containsAICommandArgs(cmdRecorder(cmd).commands, "tmux", []string{"split-window", "-P", "-F", "#{pane_id}", "-h", "-t", "%7", "-c", work, "/bin/bash", "-l"}) {
		t.Fatalf("commands = %#v, want tmux split return contract", cmdRecorder(cmd).commands)
	}
}

func TestAISplitPrintPaneIDWrapsTmuxExecutionError(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	codexBin := writeExecutable(t, filepath.Join(home, "bin", "codex"))
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "TMUX":
			return "/tmp/tmux"
		case "TMUX_SPLIT_CONTEXT_DIR":
			return work
		case "TMUX_SPLIT_TARGET_PANE":
			return "%7"
		default:
			return ""
		}
	}
	cause := errors.New("exit status 1: unsupported pane id format")
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		cmdRecorder(cmd).commands = append(cmdRecorder(cmd).commands, recordedAICommand{name: name, args: append([]string(nil), args...)})
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "codex"}) {
			return []byte(codexBin + "\n"), nil
		}
		if name == "tmux" && slices.Contains(args, "split-window") {
			return nil, cause
		}
		return nil, os.ErrNotExist
	}

	stdout := &bytes.Buffer{}
	err := cmd.Run([]string{"split", "--agent", "codex", "--print-pane-id", "right"}, stdout, &bytes.Buffer{})
	if err == nil {
		t.Fatal("Run codex --print-pane-id error = nil, want tmux execution failure")
	}
	for _, want := range []string{"ai split --print-pane-id", "tmux backend", "split-window -P -F '#{pane_id}' failed", cause.Error()} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want contains %q", err, want)
		}
	}
	if !errors.Is(err, cause) {
		t.Fatalf("error = %v, want wrapped cause %v", err, cause)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty on execution error", stdout.String())
	}
	for _, command := range cmdRecorder(cmd).commands {
		for _, forbidden := range []string{"list-panes", "resize-pane", "set-option", "run-shell"} {
			if slices.Contains(command.args, forbidden) {
				t.Fatalf("commands = %#v, post-split setup must not run after execution error", cmdRecorder(cmd).commands)
			}
		}
	}
}

func TestAISplitAgentFlagUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "invalid agent",
			args: []string{"split", "--agent", "openai", "right"},
			want: "unknown ai split agent: openai",
		},
		{
			name: "selective cannot use extra args",
			args: []string{"split", "--agent", "selective", "right", "--", "codex"},
			want: "ai split --agent selective cannot use extra args",
		},
		{
			name: "extra args require agent",
			args: []string{"split", "right", "--", "codex"},
			want: "ai split extra args require --agent",
		},
		{
			name: "extra args first arg empty",
			args: []string{"split", "--agent", "codex", "right", "--", ""},
			want: "ai split extra args require a non-empty first argument",
		},
		{
			name: "shell cannot use extra args",
			args: []string{"split", "--agent", "shell", "right", "--", "echo", "hi"},
			want: "ai split --agent shell cannot use extra args",
		},
		{
			name: "force agent requires direct agent",
			args: []string{"split", "--force-agent", "right"},
			want: "ai split --force-agent requires --agent claude, --agent codex, or --agent antigravity",
		},
		{
			name: "force agent does not apply to picker",
			args: []string{"split", "--agent", "selective", "--force-agent", "right"},
			want: "ai split --force-agent only applies to --agent claude, --agent codex, or --agent antigravity",
		},
		{
			name: "print pane id requires explicit direct agent",
			args: []string{"split", "--print-pane-id", "right"},
			want: "ai split --print-pane-id requires explicit --agent",
		},
		{
			name: "print pane id rejects selective picker",
			args: []string{"split", "--agent", "selective", "--print-pane-id", "right"},
			want: "ai split --print-pane-id cannot be used with --agent selective",
		},
		{
			name: "print pane id rejects resume picker",
			args: []string{"split", "--agent", "resume", "--print-pane-id", "right"},
			want: "ai split --print-pane-id cannot be used with --agent resume",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := testAICommand(t.TempDir())
			stderr := &bytes.Buffer{}
			err := cmd.Run(tt.args, &bytes.Buffer{}, stderr)
			if err == nil {
				t.Fatalf("Run(%v) error = nil, want usage error", tt.args)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want contains %q", err, tt.want)
			}
			if !strings.Contains(stderr.String(), "Usage:") {
				t.Fatalf("stderr = %q, want usage", stderr.String())
			}
			if len(cmdRecorder(cmd).commands) != 0 {
				t.Fatalf("commands = %#v, want none", cmdRecorder(cmd).commands)
			}
		})
	}
}

func TestAISplitSelectiveTreatsCancelledPickerAsNoOp(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.executable = func() (string, error) { return "/tmp/projmux", nil }
	cmd.lookupEnv = func(name string) string {
		if name == "TMUX" {
			return "/tmp/tmux"
		}
		return ""
	}
	cmd.runCommand = func(context.Context, string, ...string) error {
		return errors.New("exit status 1")
	}

	if err := cmd.Run([]string{"split", "right"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run split canceled picker error = %v, want nil", err)
	}
}

func TestAISplitSelectiveTreatsClosedPopupAsNoOp(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.executable = func() (string, error) { return "/tmp/projmux", nil }
	cmd.lookupEnv = func(name string) string {
		if name == "TMUX" {
			return "/tmp/tmux"
		}
		return ""
	}
	cmd.runCommand = func(context.Context, string, ...string) error {
		return errors.New("exit status 129")
	}

	if err := cmd.Run([]string{"split", "right"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run split closed popup error = %v, want nil", err)
	}
}

func TestAISplitShellUsesTmuxSplitWindow(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := testAICommand(home)
	if err := cmd.setMode("shell"); err != nil {
		t.Fatal(err)
	}
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "TMUX":
			return "/tmp/tmux"
		case "TMUX_SPLIT_CONTEXT_DIR":
			return work
		case "TMUX_SPLIT_TARGET_PANE":
			return "%9"
		case "SHELL":
			return "/bin/bash"
		default:
			return ""
		}
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%9", "-F", "#{pane_id}"}) {
			return []byte("%9\n"), nil
		}
		if name == "tmux" && reflect.DeepEqual(args, []string{"list-panes", "-t", "%9", "-F", "#{pane_id}\t#{pane_left}\t#{pane_top}\t#{pane_width}\t#{pane_height}"}) {
			return []byte("%1\t0\t0\t80\t10\n%9\t0\t11\t80\t5\n%10\t0\t17\t80\t5\n%11\t81\t0\t20\t22\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"split", "down"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run split shell error = %v", err)
	}

	want := []recordedAICommand{
		{name: "tmux", args: []string{"split-window", "-v", "-t", "%9", "-c", work, "/bin/bash", "-l"}},
		{name: "tmux", args: []string{"resize-pane", "-t", "%1", "-y", "7"}},
		{name: "tmux", args: []string{"resize-pane", "-t", "%9", "-y", "7"}},
		{name: "tmux", args: []string{"resize-pane", "-t", "%10", "-y", "6"}},
	}
	if !reflect.DeepEqual(cmdRecorder(cmd).commands, want) {
		t.Fatalf("commands = %#v, want %#v", cmdRecorder(cmd).commands, want)
	}
}

func TestAILabelsSayPlainShellNotZsh(t *testing.T) {
	t.Parallel()

	cmd := testAICommand(t.TempDir())
	for _, row := range append(cmd.agentRows(), cmd.settingsRows()...) {
		if strings.Contains(strings.ToLower(row.Label), "zsh") {
			t.Fatalf("AI row label = %q, did not expect zsh-specific copy", row.Label)
		}
	}
}

func TestSplitLayoutPeersPreserveOtherAxes(t *testing.T) {
	panes := []aiPaneGeometry{
		{id: "%1", left: 0, top: 0, width: 20, height: 10},
		{id: "%2", left: 21, top: 0, width: 10, height: 10},
		{id: "%3", left: 0, top: 11, width: 31, height: 10},
	}
	rightPeers := splitLayoutPeers(panes, panes[1], "right")
	if got, want := paneGeometryIDs(rightPeers), []string{"%1", "%2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("right peers = %#v, want %#v", got, want)
	}

	panes = []aiPaneGeometry{
		{id: "%1", left: 0, top: 0, width: 40, height: 10},
		{id: "%2", left: 0, top: 11, width: 40, height: 5},
		{id: "%3", left: 41, top: 0, width: 20, height: 16},
	}
	downPeers := splitLayoutPeers(panes, panes[1], "down")
	if got, want := paneGeometryIDs(downPeers), []string{"%1", "%2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("down peers = %#v, want %#v", got, want)
	}
}

func TestAIStatusSetThinkingMarksPaneBusy(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%1", "#{pane_title}"}) {
			return []byte("codex: repo\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"status", "set", "thinking", "%1"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run status set thinking error = %v", err)
	}

	want := []recordedAICommand{
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%1", "@projmux_ai_state", "thinking"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%1", "@projmux_ai_badge_kind", "in_progress"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%1", "@projmux_attention_state", "busy"}},
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%1", "@projmux_attention_ack"}},
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%1", "@projmux_attention_focus_armed"}},
	}
	if !reflect.DeepEqual(cmdRecorder(cmd).commands, want) {
		t.Fatalf("commands = %#v, want %#v", cmdRecorder(cmd).commands, want)
	}
}

func TestAIStatusSetWaitingMarksPaneReplyAndNotifies(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "projmux")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := testAICommand(home)
	cmd.now = func() time.Time { return time.Unix(1000, 0) }
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "command" && len(args) == 2 && args[0] == "-v" {
			switch args[1] {
			case "notify-send":
				return []byte("/usr/bin/" + args[1] + "\n"), nil
			}
		}
		if name == "git" {
			switch {
			case reflect.DeepEqual(args, []string{"-C", work, "rev-parse", "--is-inside-work-tree"}):
				return []byte("true\n"), nil
			case reflect.DeepEqual(args, []string{"-C", work, "symbolic-ref", "--quiet", "--short", "HEAD"}):
				return []byte("main\n"), nil
			}
			return nil, os.ErrNotExist
		}
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%2", "#{pane_title}"}):
			return []byte("Codex: approval needed\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%2", "#{@projmux_desktop_notified}"}),
			reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%2", "#{@projmux_desktop_notification_key}"}),
			reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%2", "#{@projmux_desktop_notification_at}"}):
			return []byte("\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%2", "#S"}):
			return []byte("repo\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%2", "#W"}):
			return []byte("dev\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%2", "#{pane_current_path}"}):
			return []byte(work + "\n"), nil
		case reflect.DeepEqual(args, []string{"list-clients", "-F", "#{client_active_pane}"}):
			return []byte("%99\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"status", "set", "waiting", "%2"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run status set waiting error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	wantPrefix := []recordedAICommand{
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%2", "@projmux_ai_state", "waiting"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%2", "@projmux_ai_badge_kind", "response_complete"}},
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%2", "@projmux_attention_ack"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%2", "@projmux_attention_state", "reply"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%2", "@projmux_attention_focus_armed", "1"}},
	}
	if len(commands) < len(wantPrefix) || !reflect.DeepEqual(commands[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("command prefix = %#v, want %#v", commands, wantPrefix)
	}
	if !containsAICommand(commands, "notify-send") {
		t.Fatalf("commands = %#v, want notify-send dispatch", commands)
	}
	for _, want := range []string{
		"--app-name=" + desktopAppID,
		desktopAppID,
		filepath.Join(home, ".local", "share", "projmux", "icons", "projmux.png"),
		"Codex · Approval required",
		"projmux/main",
	} {
		if !containsAICommandArgSubstring(commands, want) {
			t.Fatalf("commands = %#v, want notification shell containing %q", commands, want)
		}
	}
	if !containsAICommandArg(commands, "@projmux_desktop_notified") {
		t.Fatalf("commands = %#v, want notification record", commands)
	}
}

func TestAIStatusSetWaitingUsesNotificationHook(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "projmux")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(home, "notify-hook")
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "PROJMUX_NOTIFY_HOOK":
			return hook
		default:
			return ""
		}
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "notify-send"}) {
			t.Fatalf("notify-send lookup should not run when PROJMUX_NOTIFY_HOOK is set")
		}
		if name == "git" {
			switch {
			case reflect.DeepEqual(args, []string{"-C", work, "rev-parse", "--is-inside-work-tree"}):
				return []byte("true\n"), nil
			case reflect.DeepEqual(args, []string{"-C", work, "symbolic-ref", "--quiet", "--short", "HEAD"}):
				return []byte("main\n"), nil
			}
			return nil, os.ErrNotExist
		}
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%9", "#{pane_title}"}):
			return []byte("Codex: answer ready\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%9", "#{@projmux_desktop_notified}"}),
			reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%9", "#{@projmux_desktop_notification_key}"}),
			reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%9", "#{@projmux_desktop_notification_at}"}):
			return []byte("\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%9", "#S"}):
			return []byte("repo\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%9", "#W"}):
			return []byte("dev\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%9", "#{pane_current_path}"}):
			return []byte(work + "\n"), nil
		case reflect.DeepEqual(args, []string{"list-clients", "-F", "#{client_active_pane}"}):
			return []byte("%99\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"status", "set", "waiting", "%9"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run status set waiting error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if !containsAICommandArgs(commands, hook, []string{
		"Codex · Input required",
		"projmux/main",
		"normal",
		desktopAppID,
		"%9",
		"repo",
		filepath.Join(home, ".local", "share", "projmux", "icons", "projmux.png"),
	}) {
		t.Fatalf("commands = %#v, want notification hook dispatch", commands)
	}
	if containsAICommand(commands, "notify-send") {
		t.Fatalf("commands = %#v, did not expect notify-send with notification hook", commands)
	}
}

func TestAIStatusSetWaitingInWSLRegistersToastAppIDAndDispatchesToast(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "projmux")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	psPath := "/mnt/c/Windows/System32/WindowsPowerShell/v1.0/powershell.exe"
	localAppDataWin := `C:\Users\me\AppData\Local`
	localAppDataWSL := filepath.Join(home, "windows-localappdata")
	iconWSL := filepath.Join(localAppDataWSL, "projmux", "icons", "projmux.png")
	iconWin := `C:\Users\me\AppData\Local\projmux\icons\projmux.png`
	cmd := testAICommand(home)
	cmd.now = func() time.Time { return time.Unix(1000, 0) }
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "WSL_DISTRO_NAME":
			return "Ubuntu-24.04"
		default:
			return ""
		}
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "command" && len(args) == 2 && args[0] == "-v" {
			switch args[1] {
			case "powershell.exe":
				return []byte(psPath + "\n"), nil
			case "wsl-notify-send.exe":
				return nil, os.ErrNotExist
			}
		}
		if name == psPath && reflect.DeepEqual(args, []string{"-NoProfile", "-NonInteractive", "-Command", "[Environment]::GetFolderPath('LocalApplicationData')"}) {
			return []byte(localAppDataWin + "\n"), nil
		}
		if name == "wslpath" && reflect.DeepEqual(args, []string{"-u", localAppDataWin}) {
			return []byte(localAppDataWSL + "\n"), nil
		}
		if name == "wslpath" && reflect.DeepEqual(args, []string{"-w", iconWSL}) {
			return []byte(iconWin + "\n"), nil
		}
		if name == "git" {
			switch {
			case reflect.DeepEqual(args, []string{"-C", work, "rev-parse", "--is-inside-work-tree"}):
				return []byte("true\n"), nil
			case reflect.DeepEqual(args, []string{"-C", work, "symbolic-ref", "--quiet", "--short", "HEAD"}):
				return []byte("main\n"), nil
			}
			return nil, os.ErrNotExist
		}
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%2", "#{pane_title}"}):
			return []byte("Codex: approval needed\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%2", "#{@projmux_desktop_notified}"}),
			reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%2", "#{@projmux_desktop_notification_key}"}),
			reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%2", "#{@projmux_desktop_notification_at}"}):
			return []byte("\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%2", "#S"}):
			return []byte("repo\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%2", "#W"}):
			return []byte("dev\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%2", "#{pane_current_path}"}):
			return []byte(work + "\n"), nil
		case reflect.DeepEqual(args, []string{"list-clients", "-F", "#{client_active_pane}"}):
			return []byte("%99\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"status", "set", "waiting", "%2"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run status set waiting error = %v", err)
	}

	var powershellCommands []recordedAICommand
	for _, command := range cmdRecorder(cmd).commands {
		if command.name == psPath {
			powershellCommands = append(powershellCommands, command)
		}
	}
	// WSL resolves to the default mode=notify, which shows a toast and does
	// nothing else — no click target, no protocol handler registration:
	//   [0] legacy AppID cleanup    (ensureWSLLegacyAppIDCleaned)
	//   [1] new AppID register      (ensureWSLToastAppID)
	//   [2] toast XML show          (dispatchWSLToast)
	if got, want := len(powershellCommands), 3; got != want {
		t.Fatalf("powershell commands len = %d, want %d, commands = %#v", got, want, cmdRecorder(cmd).commands)
	}
	cleanupScript := decodePowerShellEncodedCommand(t, powershellCommands[0])
	for _, want := range []string{
		"Get-StartApps",
		"projmux Tmux Codex",
		`HKCU:\Software\Classes\AppUserModelId\projmux.TmuxCodex`,
	} {
		if !strings.Contains(cleanupScript, want) {
			t.Fatalf("legacy cleanup script = %q, want substring %q", cleanupScript, want)
		}
	}
	if !containsAICommandArgs(cmdRecorder(cmd).commands, "tmux", []string{"set-option", "-g", legacyAppIDCleanedTmuxOption, "1"}) {
		t.Fatalf("commands = %#v, want legacy cleanup marker write", cmdRecorder(cmd).commands)
	}
	// No `@projmux_uri_protocol_registered*` marker may be written: projmux
	// no longer registers a `projmux://` protocol handler at all.
	for _, command := range cmdRecorder(cmd).commands {
		for _, arg := range command.args {
			if strings.Contains(arg, "@projmux_uri_protocol_registered") {
				t.Fatalf("commands = %#v, did not expect any uri protocol marker write", cmdRecorder(cmd).commands)
			}
		}
	}
	registerScript := decodePowerShellEncodedCommand(t, powershellCommands[1])
	if !strings.Contains(registerScript, `HKCU:\SOFTWARE\Classes\AppUserModelId\`+desktopAppID) {
		t.Fatalf("register script = %q, want AppUserModelId registration for new id", registerScript)
	}
	if !strings.Contains(registerScript, desktopDisplayName) {
		t.Fatalf("register script = %q, want display name %q", registerScript, desktopDisplayName)
	}
	if !strings.Contains(registerScript, iconWin) {
		t.Fatalf("register script = %q, want icon uri", registerScript)
	}
	for _, want := range []string{
		"projmux.lnk",
		"Save($shortcutPath, $targetPath, $arguments, $description, $iconLocation, '" + desktopAppID + "')",
		"shellLink.SetPath(targetPath)",
	} {
		if !strings.Contains(registerScript, want) {
			t.Fatalf("register script = %q, want substring %q", registerScript, want)
		}
	}
	toastScript := decodePowerShellEncodedCommand(t, powershellCommands[2])
	for _, want := range []string{
		"CreateToastNotifier('" + desktopAppID + "').Show($toast)",
		"$toast.Tag = '%2'",
		"$toast.Group = 'repo'",
		`<toast duration="short">`,
		"$toast.ExpirationTime = [DateTimeOffset]::Now.AddMilliseconds(5000)",
		"Codex · Approval required",
		"projmux/main",
		iconWin,
		"appLogoOverride",
	} {
		if !strings.Contains(toastScript, want) {
			t.Fatalf("toast script = %q, want substring %q", toastScript, want)
		}
	}
	for _, absent := range []string{`activationType="protocol"`, "projmux://focus?", "pane_id=%252"} {
		if strings.Contains(toastScript, absent) {
			t.Fatalf("toast script = %q, did not want click target substring %q in notify mode", toastScript, absent)
		}
	}
	if _, err := os.Stat(iconWSL); err != nil {
		t.Fatalf("icon path %q missing: %v", iconWSL, err)
	}
}

func TestAIStatusSetIdleClearsSemanticBadge(t *testing.T) {
	cmd := testAICommand(t.TempDir())

	if err := cmd.Run([]string{"status", "set", "idle", "%3"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run status set idle error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	for _, want := range []recordedAICommand{
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%3", aiPaneStateOption, "idle"}},
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%3", aiPaneBadgeKindOption}},
	} {
		if !containsRecordedAICommand(commands, want) {
			t.Fatalf("commands = %#v, want %#v", commands, want)
		}
	}
}

func TestAIStatusSetWaitingAcksVisiblePane(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && reflect.DeepEqual(args, []string{"list-clients", "-F", "#{client_active_pane}"}) {
			return []byte("%15\n"), nil
		}
		return []byte("\n"), nil
	}

	if err := cmd.Run([]string{"status", "set", "waiting", "%15"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run status set waiting error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	wantPrefix := []recordedAICommand{
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%15", "@projmux_ai_state", "waiting"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%15", "@projmux_ai_badge_kind", "response_complete"}},
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%15", "@projmux_attention_ack"}},
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%15", "@projmux_attention_state"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%15", "@projmux_attention_ack", "1"}},
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%15", "@projmux_attention_focus_armed"}},
	}
	if len(commands) < len(wantPrefix) || !reflect.DeepEqual(commands[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("command prefix = %#v, want %#v", commands, wantPrefix)
	}
	if containsAICommand(commands, "notify-send") {
		t.Fatalf("commands = %#v, did not expect notify-send for visible pane", commands)
	}
}

// Regression: pane_active=1 is not sufficient — when every client has moved to
// a different window/session the pane is not visible and the reply must NOT be
// auto-acked.
func TestAIStatusSetWaitingDoesNotAckWhenNoClientViewingPane(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && reflect.DeepEqual(args, []string{"list-clients", "-F", "#{client_active_pane}"}) {
			return []byte("%99\n"), nil
		}
		return []byte("\n"), nil
	}

	if err := cmd.Run([]string{"status", "set", "waiting", "%15"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run status set waiting error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	wantPrefix := []recordedAICommand{
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%15", "@projmux_ai_state", "waiting"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%15", "@projmux_ai_badge_kind", "response_complete"}},
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%15", "@projmux_attention_ack"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%15", "@projmux_attention_state", "reply"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%15", "@projmux_attention_focus_armed", "1"}},
	}
	if len(commands) < len(wantPrefix) || !reflect.DeepEqual(commands[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("command prefix = %#v, want %#v", commands, wantPrefix)
	}
}

// Regression for the "stuck green badge" bug: when an AI hook (Claude Code /
// Codex native hook) fires with Force=true and the pane is already visible to
// some client, Force should only force the notify queue push — it must NOT
// set @projmux_attention_state=reply, otherwise focus can no longer clear the
// badge (focus clears attention_state, but used to leave ai_state=waiting and
// the badge formula ORed both).
func TestAIStatusSetWaitingForceDoesNotSetBadgeWhenVisible(t *testing.T) {
	home := t.TempDir()
	store := &stubNotifyStore{}
	cmd := testAICommand(home)
	cmd.producer = &storeAttentionNotifyProducer{store: store, ttl: 10 * time.Minute}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "command" && len(args) == 2 && args[0] == "-v" && args[1] == "notify-send" {
			return []byte("/usr/bin/notify-send\n"), nil
		}
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		if reflect.DeepEqual(args, []string{"list-clients", "-F", "#{client_active_pane}"}) {
			return []byte("%21\n"), nil
		}
		if len(args) >= 5 && args[0] == "display-message" && args[1] == "-p" && args[2] == "-t" && args[3] == "%21" {
			switch args[4] {
			case "#{@projmux_ai_agent}":
				return []byte("claude\n"), nil
			case "#S":
				return []byte("main\n"), nil
			case "#W":
				return []byte("dev\n"), nil
			case "#{window_id}":
				return []byte("@4\n"), nil
			case "#{pane_id}":
				return []byte("%21\n"), nil
			case "#{pane_title}":
				return []byte("Claude: reply ready\n"), nil
			case "#{pane_current_path}":
				return []byte(home + "\n"), nil
			case "#{socket_path}":
				return []byte("/tmp/tmux/default\n"), nil
			}
		}
		return []byte("\n"), nil
	}

	if err := cmd.applyAIStatusWithNotify("waiting", "%21", attentionNotifyInput{
		ID:       "ai:test:%21",
		Text:     "forced hook",
		Metadata: map[string]string{"agent": "claude", "category": "response_complete"},
		Force:    true,
	}); err != nil {
		t.Fatalf("applyAIStatusWithNotify error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	// Badge writes must look like the visible/auto-ack path: ai_state=waiting,
	// then clear attention_state, set attention_ack=1, clear focus_armed.
	wantPrefix := []recordedAICommand{
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%21", "@projmux_ai_state", "waiting"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%21", "@projmux_ai_badge_kind", "response_complete"}},
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%21", "@projmux_attention_ack"}},
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%21", "@projmux_attention_state"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%21", "@projmux_attention_ack", "1"}},
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%21", "@projmux_attention_focus_armed"}},
	}
	if len(commands) < len(wantPrefix) || !reflect.DeepEqual(commands[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("command prefix = %#v, want %#v", commands, wantPrefix)
	}
	// attention_state must never be set to "reply" on the visible path,
	// regardless of Force.
	for _, got := range commands {
		if got.name == "tmux" && reflect.DeepEqual(got.args, []string{"set-option", "-p", "-t", "%21", "@projmux_attention_state", "reply"}) {
			t.Fatalf("commands = %#v, did not expect attention_state=reply when pane visible (Force=true must not touch the badge)", commands)
		}
	}
	// Force still ensures the notify queue gets a push entry even though the
	// pane is visible.
	if len(store.pushed) != 1 {
		t.Fatalf("push count = %d, want 1 (Force=true forces notify even when visible)", len(store.pushed))
	}
	// Force=true on a visible pane must still fire the OS-level notification
	// (notifyAI) — the badge stays visibility-driven but delivery channels
	// (queue + OS) follow the Force-or-not-visible rule uniformly.
	if !containsAICommand(commands, "notify-send") {
		t.Fatalf("commands = %#v, want notify-send dispatch when Force=true even with visible pane", commands)
	}
}

func TestAINotifySkipsRecentDuplicateButRefreshesRecord(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.now = func() time.Time { return time.Unix(1000, 0) }
	cmd.lookupEnv = func(name string) string {
		if name == "PROJMUX_TMUX_NOTIFY_DEDUPE_SECONDS" {
			return "120"
		}
		return ""
	}
	key := "input_required|waiting for input"
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%3", "#{@projmux_desktop_notified}"}):
			return []byte("\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%3", "#{pane_title}"}):
			return []byte("waiting for input\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%3", "#{@projmux_desktop_notification_key}"}):
			return []byte(key + "\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%3", "#{@projmux_desktop_notification_at}"}):
			return []byte("950\n"), nil
		}
		return []byte("\n"), nil
	}

	if err := cmd.notifyAI("%3"); err != nil {
		t.Fatalf("notifyAI error = %v", err)
	}
	commands := cmdRecorder(cmd).commands
	if containsAICommand(commands, "notify-send") {
		t.Fatalf("commands = %#v, did not expect notify-send for duplicate", commands)
	}
	if !containsAICommandArg(commands, "@projmux_desktop_notification_at") {
		t.Fatalf("commands = %#v, want refreshed notification timestamp", commands)
	}
}

func TestAINotifyCommandBypassesDuplicateSuppression(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := testAICommand(home)
	cmd.now = func() time.Time { return time.Unix(1000, 0) }
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "PROJMUX_TMUX_NOTIFY_DEDUPE_SECONDS":
			return "120"
		default:
			return ""
		}
	}
	key := "input_required|waiting for input"
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "command" && len(args) == 2 && args[0] == "-v" && args[1] == "notify-send" {
			return []byte("/usr/bin/notify-send\n"), nil
		}
		if name == "git" {
			switch {
			case reflect.DeepEqual(args, []string{"-C", work, "rev-parse", "--is-inside-work-tree"}):
				return []byte("true\n"), nil
			case reflect.DeepEqual(args, []string{"-C", work, "symbolic-ref", "--quiet", "--short", "HEAD"}):
				return []byte("main\n"), nil
			}
			return nil, os.ErrNotExist
		}
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%3", "#{@projmux_desktop_notified}"}):
			return []byte("\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%3", "#{pane_title}"}):
			return []byte("waiting for input\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%3", "#{@projmux_desktop_notification_key}"}):
			return []byte(key + "\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%3", "#{@projmux_desktop_notification_at}"}):
			return []byte("950\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%3", "#S"}):
			return []byte("repo\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%3", "#W"}):
			return []byte("dev\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%3", "#{pane_current_path}"}):
			return []byte(work + "\n"), nil
		}
		return []byte("\n"), nil
	}

	if err := cmd.Run([]string{"notify", "notify", "%3"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run notify error = %v", err)
	}
	commands := cmdRecorder(cmd).commands
	if !containsAICommand(commands, "notify-send") {
		t.Fatalf("commands = %#v, want notify-send dispatch despite duplicate record", commands)
	}
}

func TestAINotifyUsesPaneMetadataBeforeMutableTitle(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := testAICommand(home)
	cmd.now = func() time.Time { return time.Unix(1000, 0) }
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "command" && len(args) == 2 && args[0] == "-v" {
			switch args[1] {
			case "notify-send":
				return []byte("/usr/bin/" + args[1] + "\n"), nil
			}
		}
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case len(args) == 5 && args[0] == "display-message" && args[3] == "%8" && strings.Contains(args[4], aiPaneAgentOption):
			return []byte("renamed by agent__PROJMUX_TMUX_AI_SEP__node__PROJMUX_TMUX_AI_SEP__" + work + "__PROJMUX_TMUX_AI_SEP__claude__PROJMUX_TMUX_AI_SEP__" + work + "__PROJMUX_TMUX_AI_SEP__approval needed__PROJMUX_TMUX_AI_SEP__waiting__PROJMUX_TMUX_AI_SEP__reply__PROJMUX_TMUX_AI_SEP__\n"), nil
		case reflect.DeepEqual(args, []string{"capture-pane", "-p", "-J", "-S", "-80", "-t", "%8"}):
			return []byte("waiting for approval\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%8", "#{@projmux_desktop_notified}"}),
			reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%8", "#{@projmux_desktop_notification_key}"}),
			reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%8", "#{@projmux_desktop_notification_at}"}):
			return []byte("\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%8", "#S"}):
			return []byte("repo\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%8", "#W"}):
			return []byte("dev\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%8", "#{pane_current_path}"}):
			return []byte(work + "\n"), nil
		}
		return []byte("\n"), nil
	}

	if err := cmd.Run([]string{"notify", "notify", "%8"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run notify error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if !containsAICommand(commands, "notify-send") {
		t.Fatalf("commands = %#v, want notify-send dispatch", commands)
	}
	for _, want := range []string{
		desktopAppID,
		filepath.Join(home, ".local", "share", "projmux", "icons", "projmux.png"),
		"Claude · Approval required",
	} {
		if !containsAICommandArgSubstring(commands, want) {
			t.Fatalf("commands = %#v, want metadata-derived Claude notification containing %q", commands, want)
		}
	}
}

func TestAIWatchTitlePromotesBusyPaneToThinking(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	checks := 0
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%4", "#{pane_id}"}):
			checks++
			if checks > 1 {
				return nil, os.ErrNotExist
			}
			return []byte("%4\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%4", "#{pane_title}__PROJMUX_TMUX_AI_SEP__#{@projmux_attention_state}__PROJMUX_TMUX_AI_SEP__#{@projmux_attention_ack}"}):
			return []byte("thinking hard__PROJMUX_TMUX_AI_SEP____PROJMUX_TMUX_AI_SEP__\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%4", "#{pane_title}"}):
			return []byte("thinking hard\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"watch-title", "%4"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run watch-title error = %v", err)
	}

	if !containsAICommandArg(cmdRecorder(cmd).commands, "busy") {
		t.Fatalf("commands = %#v, want busy attention state", cmdRecorder(cmd).commands)
	}
	if !containsAICommandArgs(cmdRecorder(cmd).commands, "tmux", []string{"set-option", "-p", "-t", "%4", aiPaneBadgeKindOption, aiBadgeKindInProgress}) {
		t.Fatalf("commands = %#v, want in_progress semantic badge", cmdRecorder(cmd).commands)
	}
}

func TestAIWatchTitleStopsWhenPaneLookupReturnsEmpty(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%8", "#{pane_id}"}) {
			return []byte("\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"watch-title", "%8"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run watch-title error = %v", err)
	}
	if len(cmdRecorder(cmd).commands) != 0 {
		t.Fatalf("commands = %#v, want no writes for missing pane", cmdRecorder(cmd).commands)
	}
}

func TestAIWatchTitleUsesCapturePaneAsReplySignal(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	checks := 0
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "notify-send"}) {
			return []byte("/usr/bin/notify-send\n"), nil
		}
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%10", "#{pane_id}"}):
			checks++
			if checks > 1 {
				return nil, os.ErrNotExist
			}
			return []byte("%10\n"), nil
		case len(args) == 5 && args[0] == "display-message" && args[3] == "%10" && strings.Contains(args[4], aiPaneAgentOption):
			return []byte("codexcli__PROJMUX_TMUX_AI_SEP__node__PROJMUX_TMUX_AI_SEP____PROJMUX_TMUX_AI_SEP__codex__PROJMUX_TMUX_AI_SEP____PROJMUX_TMUX_AI_SEP____PROJMUX_TMUX_AI_SEP__thinking__PROJMUX_TMUX_AI_SEP__busy__PROJMUX_TMUX_AI_SEP__\n"), nil
		case reflect.DeepEqual(args, []string{"capture-pane", "-p", "-J", "-S", "-80", "-t", "%10"}):
			return []byte("waiting for input\n"), nil
		}
		return []byte("\n"), nil
	}

	if err := cmd.Run([]string{"watch-title", "%10"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run watch-title error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%10", "@projmux_ai_topic", "waiting for input"}) {
		t.Fatalf("commands = %#v, want capture-derived AI topic", commands)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%10", "@projmux_ai_state", "waiting"}) {
		t.Fatalf("commands = %#v, want waiting AI state from capture", commands)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%10", aiPaneBadgeKindOption, aiBadgeKindInputRequired}) {
		t.Fatalf("commands = %#v, want input_required semantic badge from capture", commands)
	}
}

func TestAIWatchTitleMapsPermissionTitleToApprovalRequired(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	checks := 0
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "notify-send"}) {
			return []byte("/usr/bin/notify-send\n"), nil
		}
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%16", "#{pane_id}"}):
			checks++
			if checks > 1 {
				return nil, os.ErrNotExist
			}
			return []byte("%16\n"), nil
		case len(args) == 5 && args[0] == "display-message" && args[3] == "%16" && strings.Contains(args[4], aiPaneAgentOption):
			return []byte("permission required__PROJMUX_TMUX_AI_SEP__node__PROJMUX_TMUX_AI_SEP__" + home + "__PROJMUX_TMUX_AI_SEP__codex__PROJMUX_TMUX_AI_SEP__" + home + "__PROJMUX_TMUX_AI_SEP____PROJMUX_TMUX_AI_SEP____PROJMUX_TMUX_AI_SEP____PROJMUX_TMUX_AI_SEP__\n"), nil
		case reflect.DeepEqual(args, []string{"capture-pane", "-p", "-J", "-S", "-80", "-t", "%16"}):
			return []byte("allow command?\n"), nil
		}
		return []byte("\n"), nil
	}

	if err := cmd.Run([]string{"watch-title", "%16"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run watch-title error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%16", aiPaneStateOption, "waiting"}) {
		t.Fatalf("commands = %#v, want waiting AI state", commands)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%16", aiPaneBadgeKindOption, aiBadgeKindApprovalRequired}) {
		t.Fatalf("commands = %#v, want approval_required semantic badge", commands)
	}
}

func TestAIWatchTitlePreservesManualAITopic(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	checks := 0
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "notify-send"}) {
			return []byte("/usr/bin/notify-send\n"), nil
		}
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%15", "#{pane_id}"}):
			checks++
			if checks > 1 {
				return nil, os.ErrNotExist
			}
			return []byte("%15\n"), nil
		case len(args) == 5 && args[0] == "display-message" && args[3] == "%15" && strings.Contains(args[4], aiPaneTopicManualOption):
			return []byte("codexcli__PROJMUX_TMUX_AI_SEP__node__PROJMUX_TMUX_AI_SEP__" + home + "__PROJMUX_TMUX_AI_SEP__codex__PROJMUX_TMUX_AI_SEP__" + home + "__PROJMUX_TMUX_AI_SEP__manual topic__PROJMUX_TMUX_AI_SEP__1__PROJMUX_TMUX_AI_SEP__thinking__PROJMUX_TMUX_AI_SEP__busy__PROJMUX_TMUX_AI_SEP__\n"), nil
		case reflect.DeepEqual(args, []string{"capture-pane", "-p", "-J", "-S", "-80", "-t", "%15"}):
			return []byte("waiting for input\n"), nil
		}
		return []byte("\n"), nil
	}

	if err := cmd.Run([]string{"watch-title", "%15"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run watch-title error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%15", "@projmux_ai_topic", "waiting for input"}) {
		t.Fatalf("commands = %#v, did not expect watcher to replace manual AI topic", commands)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%15", "@projmux_ai_state", "waiting"}) {
		t.Fatalf("commands = %#v, want waiting AI state from capture", commands)
	}
}

func TestAIWatchTitleBootstrapsMetadataForExistingCodexPane(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	checks := 0
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%11", "#{pane_id}"}):
			checks++
			if checks > 1 {
				return nil, os.ErrNotExist
			}
			return []byte("%11\n"), nil
		case len(args) == 5 && args[0] == "display-message" && args[3] == "%11" && strings.Contains(args[4], aiPaneAgentOption):
			return []byte("es5h__PROJMUX_TMUX_AI_SEP__node__PROJMUX_TMUX_AI_SEP__" + home + "__PROJMUX_TMUX_AI_SEP____PROJMUX_TMUX_AI_SEP____PROJMUX_TMUX_AI_SEP____PROJMUX_TMUX_AI_SEP____PROJMUX_TMUX_AI_SEP____PROJMUX_TMUX_AI_SEP__\n"), nil
		case reflect.DeepEqual(args, []string{"capture-pane", "-p", "-J", "-S", "-80", "-t", "%11"}):
			return []byte("gpt-5.5 medium · ~\n"), nil
		}
		return []byte("\n"), nil
	}

	if err := cmd.Run([]string{"watch-title", "%11"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run watch-title error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	for _, want := range [][]string{
		{"set-option", "-p", "-t", "%11", "@projmux_ai_managed", "1"},
		{"set-option", "-p", "-t", "%11", "@projmux_ai_agent", "codex"},
		{"set-option", "-p", "-t", "%11", "@projmux_ai_context", home},
	} {
		if !containsAICommandArgs(commands, "tmux", want) {
			t.Fatalf("commands = %#v, want bootstrapped metadata %v", commands, want)
		}
	}
}

func TestAIWatchTitleKeepsWaitingUntilFocusAck(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	checks := 0
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "notify-send"}) {
			return []byte("/usr/bin/notify-send\n"), nil
		}
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%12", "#{pane_id}"}):
			checks++
			if checks > 1 {
				return nil, os.ErrNotExist
			}
			return []byte("%12\n"), nil
		case len(args) == 5 && args[0] == "display-message" && args[3] == "%12" && strings.Contains(args[4], aiPaneAgentOption):
			return []byte("codexcli__PROJMUX_TMUX_AI_SEP__node__PROJMUX_TMUX_AI_SEP__" + home + "__PROJMUX_TMUX_AI_SEP__codex__PROJMUX_TMUX_AI_SEP__" + home + "__PROJMUX_TMUX_AI_SEP__repo__PROJMUX_TMUX_AI_SEP__waiting__PROJMUX_TMUX_AI_SEP__reply__PROJMUX_TMUX_AI_SEP__\n"), nil
		case reflect.DeepEqual(args, []string{"capture-pane", "-p", "-J", "-S", "-80", "-t", "%12"}):
			return []byte("plain idle screen\n"), nil
		}
		return []byte("\n"), nil
	}

	if err := cmd.Run([]string{"watch-title", "%12"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run watch-title error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%12", "@projmux_ai_state", "idle"}) {
		t.Fatalf("commands = %#v, did not expect watcher to clear waiting state", commands)
	}
	if containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-u", "-t", "%12", "@projmux_attention_state"}) {
		t.Fatalf("commands = %#v, did not expect watcher to clear reply attention", commands)
	}
}

func TestAIWatchTitleSettledBusyBecomesWaitingReply(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "PROJMUX_CODEX_REPLY_SETTLE_LOOPS":
			return "2"
		default:
			return ""
		}
	}
	checks := 0
	snapshots := []string{
		"thinking hard__PROJMUX_TMUX_AI_SEP____PROJMUX_TMUX_AI_SEP__",
		"repo__PROJMUX_TMUX_AI_SEP__busy__PROJMUX_TMUX_AI_SEP__",
		"repo__PROJMUX_TMUX_AI_SEP__busy__PROJMUX_TMUX_AI_SEP__",
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "notify-send"}) {
			return []byte("/usr/bin/notify-send\n"), nil
		}
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%6", "#{pane_id}"}):
			checks++
			if checks > len(snapshots) {
				return nil, os.ErrNotExist
			}
			return []byte("%6\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%6", "#{pane_title}__PROJMUX_TMUX_AI_SEP__#{@projmux_attention_state}__PROJMUX_TMUX_AI_SEP__#{@projmux_attention_ack}"}):
			return []byte(snapshots[checks-1] + "\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%6", "#{pane_title}"}):
			if checks <= 1 {
				return []byte("thinking hard\n"), nil
			}
			return []byte("repo\n"), nil
		}
		return []byte("\n"), nil
	}

	if err := cmd.Run([]string{"watch-title", "%6"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run watch-title error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%6", "@projmux_ai_state", "waiting"}) {
		t.Fatalf("commands = %#v, want waiting ai pane state", commands)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%6", "@projmux_attention_state", "reply"}) {
		t.Fatalf("commands = %#v, want reply attention state", commands)
	}
	if !containsAICommandArg(commands, "@projmux_desktop_notified") {
		t.Fatalf("commands = %#v, want notification record after settled busy", commands)
	}
}

func TestAIWatchTitleIgnoresStaleBusyCaptureHistory(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	checks := 0
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%13", "#{pane_id}"}):
			checks++
			if checks > 1 {
				return nil, os.ErrNotExist
			}
			return []byte("%13\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%13", "#{pane_title}__PROJMUX_TMUX_AI_SEP__#{@projmux_attention_state}__PROJMUX_TMUX_AI_SEP__#{@projmux_attention_ack}"}):
			return []byte("repo__PROJMUX_TMUX_AI_SEP__busy__PROJMUX_TMUX_AI_SEP__\n"), nil
		case reflect.DeepEqual(args, []string{"capture-pane", "-p", "-J", "-S", "-80", "-t", "%13"}):
			return []byte("• Working (27s)\n\n  gpt-5.5 medium · ~/source/repos/projmux · main\n"), nil
		}
		return []byte("\n"), nil
	}

	if err := cmd.Run([]string{"watch-title", "%13"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run watch-title error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%13", "@projmux_ai_state", "waiting"}) {
		t.Fatalf("commands = %#v, want stale busy history to become waiting", commands)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%13", "@projmux_attention_state", "reply"}) {
		t.Fatalf("commands = %#v, want stale busy attention to become reply", commands)
	}
}

func TestAIWatchTitleSettlesUnchangedSpinnerTitle(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "PROJMUX_CODEX_REPLY_SETTLE_LOOPS":
			return "2"
		default:
			return ""
		}
	}
	checks := 0
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%14", "#{pane_id}"}):
			checks++
			if checks > 3 {
				return nil, os.ErrNotExist
			}
			return []byte("%14\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%14", "#{pane_title}__PROJMUX_TMUX_AI_SEP__#{@projmux_attention_state}__PROJMUX_TMUX_AI_SEP__#{@projmux_attention_ack}"}):
			return []byte("⠧ repo__PROJMUX_TMUX_AI_SEP__busy__PROJMUX_TMUX_AI_SEP__\n"), nil
		case reflect.DeepEqual(args, []string{"capture-pane", "-p", "-J", "-S", "-80", "-t", "%14"}):
			return []byte("idle prompt\n"), nil
		}
		return []byte("\n"), nil
	}

	if err := cmd.Run([]string{"watch-title", "%14"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run watch-title error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%14", "@projmux_ai_state", "waiting"}) {
		t.Fatalf("commands = %#v, want unchanged spinner title to settle waiting", commands)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%14", "@projmux_attention_state", "reply"}) {
		t.Fatalf("commands = %#v, want unchanged spinner attention to become reply", commands)
	}
}

func TestAIReplyTitleIgnoresProjmuxAttentionMarkers(t *testing.T) {
	for _, title := range []string{"✳ repo", "✔ repo"} {
		if isAIReplyTitle(title) {
			t.Fatalf("isAIReplyTitle(%q) = true, want false for projmux marker", title)
		}
	}
}

func TestAIBadgeKindContractNormalizesAndFallsBackSafely(t *testing.T) {
	tests := []struct {
		name     string
		state    string
		explicit string
		want     string
	}{
		{name: "thinking fallback", state: "thinking", want: aiBadgeKindInProgress},
		{name: "waiting fallback", state: "waiting", want: aiBadgeKindResponseComplete},
		{name: "idle clears", state: "idle", want: ""},
		{name: "approval explicit", state: "waiting", explicit: aiBadgeKindApprovalRequired, want: aiBadgeKindApprovalRequired},
		{name: "input explicit", state: "waiting", explicit: aiBadgeKindInputRequired, want: aiBadgeKindInputRequired},
		{name: "invalid explicit falls back", state: "waiting", explicit: "future_kind", want: aiBadgeKindResponseComplete},
		{name: "invalid idle clears", state: "idle", explicit: "future_kind", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := aiBadgeKindForStatus(tt.state, tt.explicit); got != tt.want {
				t.Fatalf("aiBadgeKindForStatus(%q, %q) = %q, want %q", tt.state, tt.explicit, got, tt.want)
			}
		})
	}
}

func TestAINotificationMessageLabelsClaudeAndAvoidsRootProject(t *testing.T) {
	if got, want := aiAgentDisplayName("Claude: waiting for input"), "Claude"; got != want {
		t.Fatalf("aiAgentDisplayName = %q, want %q", got, want)
	}
	if got, want := displayAITopic("Claude: waiting for input"), "waiting for input"; got != want {
		t.Fatalf("displayAITopic = %q, want %q", got, want)
	}
	if got := aiProjectName("/"); got != "" {
		t.Fatalf("aiProjectName(/) = %q, want empty", got)
	}
	if got, want := aiSummaryForKind("input_required", "Claude", "waiting for input"), "Claude · Input required"; got != want {
		t.Fatalf("aiSummaryForKind = %q, want %q", got, want)
	}
	if got, want := aiNotificationBody("waiting for input", "", "", "home", "dev"), ""; got != want {
		t.Fatalf("aiNotificationBody = %q, want %q", got, want)
	}
	if got, want := aiNotificationBody("", "projmux", "main", "home", "dev"), "projmux/main"; got != want {
		t.Fatalf("aiNotificationBody = %q, want %q", got, want)
	}
	if got, want := aiNotificationBody("Codex", "projmux", "main", "", ""), "projmux/main"; got != want {
		t.Fatalf("aiNotificationBody = %q, want %q", got, want)
	}
}

type capturingAIRunner struct {
	options intpickercompat.Options
	result  intpickercompat.Result
	err     error
}

func (r *capturingAIRunner) Run(options intpickercompat.Options) (intpickercompat.Result, error) {
	r.options = options
	return r.result, r.err
}

type sequencingAIRunner struct {
	options []intpickercompat.Options
	results []intpickercompat.Result
	err     error
}

func (r *sequencingAIRunner) Run(options intpickercompat.Options) (intpickercompat.Result, error) {
	r.options = append(r.options, options)
	if r.err != nil {
		return intpickercompat.Result{}, r.err
	}
	if len(r.results) == 0 {
		return intpickercompat.Result{}, nil
	}
	result := r.results[0]
	r.results = r.results[1:]
	return result, nil
}

type recordedAICommand struct {
	name string
	args []string
}

type aiCommandRecorder struct {
	commands []recordedAICommand
}

func testAICommand(home string) *aiCommand {
	recorder := &aiCommandRecorder{}
	cmd := &aiCommand{
		runner:       &capturingAIRunner{},
		nativePicker: nativePickerFromCompatRunner(&capturingAIRunner{}),
		executable:   func() (string, error) { return "/tmp/projmux", nil },
		lookupEnv: func(name string) string {
			switch name {
			case "HOME":
				return home
			default:
				return ""
			}
		},
		homeDir:   func() (string, error) { return home, nil },
		readFile:  func(string) ([]byte, error) { return nil, os.ErrNotExist },
		writeFile: os.WriteFile,
		mkdirAll:  os.MkdirAll,
		runCommand: func(_ context.Context, name string, args ...string) error {
			recorder.commands = append(recorder.commands, recordedAICommand{name: name, args: append([]string(nil), args...)})
			return nil
		},
		readCommand: func(context.Context, string, ...string) ([]byte, error) {
			return nil, os.ErrNotExist
		},
	}
	cmd.now = func() time.Time { return time.Unix(0, 0) }
	cmd.sleep = func(time.Duration) {}
	aiRecordersMu.Lock()
	aiRecorders[cmd] = recorder
	aiRecordersMu.Unlock()
	return cmd
}

var (
	aiRecordersMu sync.Mutex
	aiRecorders   = map[*aiCommand]*aiCommandRecorder{}
)

func cmdRecorder(cmd *aiCommand) *aiCommandRecorder {
	aiRecordersMu.Lock()
	defer aiRecordersMu.Unlock()
	return aiRecorders[cmd]
}

func stubAISplitReadCommand(cmd *aiCommand, home, work string, bins map[string]string, targetPane, newPane string) {
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "TMUX":
			return "/tmp/tmux"
		case "SHELL":
			return "/bin/bash"
		default:
			return ""
		}
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		cmdRecorder(cmd).commands = append(cmdRecorder(cmd).commands, recordedAICommand{name: name, args: append([]string(nil), args...)})
		if name == "command" && len(args) == 2 && args[0] == "-v" {
			if bin := bins[args[1]]; bin != "" {
				return []byte(bin + "\n"), nil
			}
			return nil, os.ErrNotExist
		}
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{pane_id}"}):
			return []byte(targetPane + "\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{pane_current_path}"}):
			return []byte(work + "\n"), nil
		case len(args) >= 6 && reflect.DeepEqual(args[:4], []string{"split-window", "-P", "-F", "#{pane_id}"}):
			return []byte(newPane + "\n"), nil
		case reflect.DeepEqual(args, []string{"list-panes", "-t", targetPane, "-F", "#{pane_id}\t#{pane_left}\t#{pane_top}\t#{pane_width}\t#{pane_height}"}):
			return []byte(targetPane + "\t0\t0\t40\t10\n" + newPane + "\t41\t0\t40\t10\n"), nil
		}
		return nil, os.ErrNotExist
	}
}

func writeCodexResumeSession(t *testing.T, home, resumeID, cwd, branch, title string) {
	t.Helper()

	dir := filepath.Join(home, ".codex", "sessions", "2026", "06", "25")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-"+resumeID+".jsonl")
	content := fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"cwd":%q,"git_branch":%q}}
{"type":"event_msg","payload":{"message":%q}}
`, resumeID, cwd, branch, title)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write codex resume session: %v", err)
	}
	if err := os.Chtimes(path, time.Date(2026, 6, 25, 9, 0, 0, 0, time.UTC), time.Date(2026, 6, 25, 9, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("chtimes codex resume session: %v", err)
	}
}

func readModeFile(t *testing.T, home string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(home, ".config", "projmux", "tmux-ai-split-mode"))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func containsAICommand(commands []recordedAICommand, name string) bool {
	for _, command := range commands {
		if command.name == name {
			return true
		}
	}
	return false
}

func containsAICommandArgs(commands []recordedAICommand, name string, prefix []string) bool {
	for _, command := range commands {
		if command.name != name || len(command.args) < len(prefix) {
			continue
		}
		if reflect.DeepEqual(command.args[:len(prefix)], prefix) {
			return true
		}
	}
	return false
}

func containsRecordedAICommand(commands []recordedAICommand, want recordedAICommand) bool {
	for _, command := range commands {
		if command.name == want.name && reflect.DeepEqual(command.args, want.args) {
			return true
		}
	}
	return false
}

func decodePowerShellEncodedCommand(t *testing.T, command recordedAICommand) string {
	t.Helper()
	if len(command.args) < 4 {
		t.Fatalf("powershell args = %#v, want encoded command", command.args)
	}
	encoded := command.args[len(command.args)-1]
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode base64 error = %v", err)
	}
	if len(decoded)%2 != 0 {
		t.Fatalf("decoded powershell bytes len = %d, want even", len(decoded))
	}
	words := make([]uint16, 0, len(decoded)/2)
	for i := 0; i < len(decoded); i += 2 {
		words = append(words, binary.LittleEndian.Uint16(decoded[i:i+2]))
	}
	return string(utf16.Decode(words))
}

func writeExecutable(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func containsAICommandArg(commands []recordedAICommand, arg string) bool {
	for _, command := range commands {
		if slices.Contains(command.args, arg) {
			return true
		}
	}
	return false
}

func containsAICommandArgSubstring(commands []recordedAICommand, value string) bool {
	for _, command := range commands {
		for _, commandArg := range command.args {
			if strings.Contains(commandArg, value) {
				return true
			}
		}
	}
	return false
}

func paneGeometryIDs(panes []aiPaneGeometry) []string {
	ids := make([]string, 0, len(panes))
	for _, pane := range panes {
		ids = append(ids, pane.id)
	}
	return ids
}

func TestAITopicSetWritesPaneOptionAndManualFlag(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)

	if err := cmd.Run([]string{"topic", "set", "fix login bug", "--pane", "%3"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run topic set error = %v", err)
	}

	want := []recordedAICommand{
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%3", "@projmux_ai_topic", "fix login bug"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%3", "@projmux_ai_topic_manual", "on"}},
	}
	if !reflect.DeepEqual(cmdRecorder(cmd).commands, want) {
		t.Fatalf("commands = %#v, want %#v", cmdRecorder(cmd).commands, want)
	}
}

func TestAITopicSetUsesEnvPaneWhenFlagOmitted(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "TMUX_PANE":
			return "%7"
		case "HOME":
			return home
		default:
			return ""
		}
	}

	if err := cmd.Run([]string{"topic", "set", "review PR"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run topic set error = %v", err)
	}

	want := []recordedAICommand{
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", "@projmux_ai_topic", "review PR"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", "@projmux_ai_topic_manual", "on"}},
	}
	if !reflect.DeepEqual(cmdRecorder(cmd).commands, want) {
		t.Fatalf("commands = %#v, want %#v", cmdRecorder(cmd).commands, want)
	}
}

func TestAITopicClearUnsetsBothPaneOptions(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)

	if err := cmd.Run([]string{"topic", "clear", "--pane", "%4"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run topic clear error = %v", err)
	}

	want := []recordedAICommand{
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%4", "@projmux_ai_topic"}},
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%4", "@projmux_ai_topic_manual"}},
	}
	if !reflect.DeepEqual(cmdRecorder(cmd).commands, want) {
		t.Fatalf("commands = %#v, want %#v", cmdRecorder(cmd).commands, want)
	}
}

func TestAITopicGetPrintsPaneOptionValue(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%5", "#{@projmux_ai_topic}"}) {
			return []byte("[Lead:Roadmap] ship the feature\n"), nil
		}
		return nil, os.ErrNotExist
	}

	stdout := &bytes.Buffer{}
	if err := cmd.Run([]string{"topic", "get", "--pane", "%5"}, stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run topic get error = %v", err)
	}

	if got, want := stdout.String(), "[Lead:Roadmap] ship the feature\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if strings.Contains(stdout.String(), "\x1b[") || strings.Contains(stdout.String(), "#[") {
		t.Fatalf("stdout = %q, want plain topic text", stdout.String())
	}
}

func TestAITopicGetEmitsBlankLineWhenUnset(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)

	stdout := &bytes.Buffer{}
	if err := cmd.Run([]string{"topic", "get", "--pane", "%6"}, stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run topic get error = %v", err)
	}
	if got, want := stdout.String(), "\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestAITopicSetRequiresText(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)

	stderr := &bytes.Buffer{}
	if err := cmd.Run([]string{"topic", "set", "--pane", "%2"}, &bytes.Buffer{}, stderr); err == nil {
		t.Fatalf("Run topic set without text expected error, got nil")
	}
	if len(cmdRecorder(cmd).commands) != 0 {
		t.Fatalf("expected no tmux commands, got %#v", cmdRecorder(cmd).commands)
	}
}

func TestAITopicUnknownActionReturnsError(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)

	stderr := &bytes.Buffer{}
	err := cmd.Run([]string{"topic", "foo"}, &bytes.Buffer{}, stderr)
	if err == nil {
		t.Fatalf("Run topic foo expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown ai topic subcommand") {
		t.Fatalf("error = %v, want contains \"unknown ai topic subcommand\"", err)
	}
	if len(cmdRecorder(cmd).commands) != 0 {
		t.Fatalf("expected no tmux commands, got %#v", cmdRecorder(cmd).commands)
	}
}

func TestAITopicHelpListedInUsage(t *testing.T) {
	stdout := &bytes.Buffer{}
	printAIUsage(stdout)
	for _, want := range []string{
		"projmux ai topic set <text> [--pane <id>]",
		"projmux ai topic clear [--pane <id>]",
		"projmux ai topic get [--pane <id>]",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("usage = %q, want contains %q", stdout.String(), want)
		}
	}
}

func TestAITopicErrorsWhenNoPaneAvailable(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.lookupEnv = func(string) string { return "" }
	cmd.readCommand = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("not in tmux")
	}

	stderr := &bytes.Buffer{}
	if err := cmd.Run([]string{"topic", "set", "anything"}, &bytes.Buffer{}, stderr); err == nil {
		t.Fatalf("Run topic set without pane expected error, got nil")
	}
	if len(cmdRecorder(cmd).commands) != 0 {
		t.Fatalf("expected no tmux set-option commands, got %#v", cmdRecorder(cmd).commands)
	}
}

// TestBuildRegisterToastAppIDShortcutTargetIsCmdExe pins the Start Menu
// shortcut target produced by buildRegisterToastAppIDPowerShell to
// `cmd.exe /c exit`. The shortcut is a property bag for PKEY_AppUserModel_ID
// (pid=5) so the toast routes under our DisplayName; its target is never
// actually launched. We do NOT want `powershell.exe -WindowStyle Hidden ...`
// here — Windows Defender quarantines such shortcuts moments after creation,
// which silently breaks toast AppID routing and (because the click path
// depends on the AppID being live) silently breaks click activation.
func TestBuildRegisterToastAppIDShortcutTargetIsCmdExe(t *testing.T) {
	script := buildRegisterToastAppIDPowerShell(desktopAppID, desktopDisplayName, "")
	for _, want := range []string{
		`$targetPath = [Environment]::ExpandEnvironmentVariables('%SystemRoot%\System32\cmd.exe')`,
		`$arguments = '/c exit'`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("register script missing %q: %s", want, script)
		}
	}
	// Strip PS `#` comment lines before scanning for forbidden tokens —
	// the source-level guidance comment mentions the historical
	// powershell.exe target by name and we don't want the assertion to
	// flag its own do-not-do-this commentary.
	var noComments strings.Builder
	for line := range strings.SplitSeq(script, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		noComments.WriteString(line)
		noComments.WriteByte('\n')
	}
	effective := noComments.String()
	for _, forbidden := range []string{
		`WindowsPowerShell\v1.0\powershell.exe`,
		`-WindowStyle Hidden`,
	} {
		if strings.Contains(effective, forbidden) {
			t.Fatalf("register script contains forbidden token %q (Defender quarantines such shortcuts): %s", forbidden, effective)
		}
	}
}

func TestAICommandMuxBackendNonOutputCommandRequiresRunner(t *testing.T) {
	readerCalled := false
	backend := aiCommandMuxBackend{
		readCommand: func(context.Context, string, ...string) ([]byte, error) {
			readerCalled = true
			return []byte("unexpected"), nil
		},
	}

	_, err := backend.Run(context.Background(), "tmux", "set-hook", "-ag", "alert-bell", "run-shell -b true")
	if err == nil || err.Error() != "ai command runner is not configured" {
		t.Fatalf("Run error = %v, want ai command runner is not configured", err)
	}
	if readerCalled {
		t.Fatal("readCommand called for non-output mux command")
	}
}

// TestBuildRegisterToastAppIDDoesNotSetToastActivatorCLSID guards against a
// well-meaning "fix" that adds PKEY_AppUserModel_ToastActivatorCLSID
// (pid=26) to the shortcut. Setting that property routes Windows toast
// activation down the COM path first; in our unpackaged Win32 setup the
// COM call silently fails and Windows does NOT fall through to the
// ShellExecute(launch URI) path — i.e. click activation breaks. The
// shortcut intentionally carries only the AppUserModelID (pid=5) so the
// URI launch path is taken on click.
//
// We strip PowerShell comment lines before scanning so the source-level
// guidance comment that mentions ToastActivatorCLSID by name doesn't
// trigger the substring assertion. The check intentionally targets
// executable PS lines only.
func TestBuildRegisterToastAppIDDoesNotSetToastActivatorCLSID(t *testing.T) {
	script := buildRegisterToastAppIDPowerShell(desktopAppID, desktopDisplayName, "")
	var noComments strings.Builder
	for line := range strings.SplitSeq(script, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		noComments.WriteString(line)
		noComments.WriteByte('\n')
	}
	effective := noComments.String()
	for _, forbidden := range []string{
		"ToastActivatorCLSID",
		// pid=26 is the property id for ToastActivatorCLSID. The shortcut's
		// only property write is the AppUserModel_ID (pid=5) — any pid=26
		// PROPERTYKEY introduction would be a regression.
		`PROPERTYKEY("9F4C2855-9F79-4B39-A8D0-E1D42DE1D5F3", 26)`,
		// Cover the PROPERTYKEY constructor by raw pid form too.
		", 26)",
	} {
		if strings.Contains(effective, forbidden) {
			t.Fatalf("register script must not configure ToastActivatorCLSID (%q): %s", forbidden, effective)
		}
	}
}

// Regression for the sidebar preview attention gate (Phase 1): the gate lives
// only in `attention arm`/`attention clear` (focus hooks). The AI ingest path
// writes attention state directly via set-option, so an AI turning "waiting"
// while the user is browsing the sidebar must still raise the reply marker.
func TestAIStatusWaitingSetsReplyDuringSidebarPreview(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	marker := popupMarkerPath("tty0", "sessionizer-sidebar")
	if err := os.WriteFile(marker, []byte("%1\nwork\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !isSidebarPreviewActive() {
		t.Fatal("test setup: sidebar preview marker not detected")
	}

	cmd := testAICommand(t.TempDir())

	if err := cmd.applyAIStatusStateOnly("waiting", "%2", attentionNotifyInput{}); err != nil {
		t.Fatalf("applyAIStatusStateOnly error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%2", "@projmux_attention_state", "reply"}) {
		t.Fatalf("commands = %#v, want attention_state=reply while sidebar preview marker exists (AI completion attention must not be suppressed)", commands)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%2", "@projmux_attention_focus_armed", "1"}) {
		t.Fatalf("commands = %#v, want focus_armed=1 while sidebar preview marker exists", commands)
	}
}
