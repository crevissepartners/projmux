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
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/registryview"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	"github.com/crevissepartners/projmux/internal/theme"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

func aiResumeSessionRowsWithLabels(sessions []aisessions.SessionMeta, conversationLabels map[string]string, limit int, now time.Time, locale i18n.Locale, baseCWD string, depth int) ([]intpickercompat.Entry, int, int) {
	limit = normalizeResumePickerLimit(limit)
	total := len(sessions)
	if len(sessions) > limit {
		sessions = sessions[:limit]
	}
	rows := make([]intpickercompat.Entry, 0, len(sessions)+1)
	rows = append(rows, intpickercompat.Entry{Label: "\x1b[32m[+ New Session]\x1b[0m", Value: aiResumeNewValue, SearchKey: "new session fresh agent picker"})
	for _, session := range sessions {
		rows = append(rows, aiResumeSessionRowWithResolvedLabel(session, aiResumeExactAgentLabel{
			Context: registryview.Context{
				Value:  conversationLabels[strings.TrimSpace(session.ResumeID)],
				Source: registryview.ContextSourceAgentTopic,
			},
		}, now, locale, baseCWD, depth))
	}
	return rows, len(sessions), total
}

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

func TestProductionAIPaneBindersUseOnlySuppliedExactRuntimeRoute(t *testing.T) {
	t.Parallel()
	tmux := newFakeTmux()
	session := tmux.addSession("agent-bind")
	paneID := session.windows[0].panes[0].id
	routed := explicitTmuxRunner{runner: tmux, target: tmuxTransport{Kind: tmuxSocketPath, Value: tmux.socketPath, Source: tmuxSocketPathSource}}
	cmd := testAICommand(t.TempDir())

	if err := cmd.BindManagedAgentPaneOnRoute(context.Background(), routed, paneID, aiModeCodex, "/repo", "fresh"); err != nil {
		t.Fatalf("BindManagedAgentPaneOnRoute error = %v", err)
	}
	if err := cmd.BindResumedAgentPaneWithSourceOnRoute(context.Background(), routed, paneID, aiModeCodex, "/repo", "resume", "thread-1", "rollout"); err != nil {
		t.Fatalf("BindResumedAgentPaneWithSourceOnRoute error = %v", err)
	}
	if err := cmd.BindNativeCodexPaneOnRoute(context.Background(), routed, paneID, "/repo", "native", "thread-2"); err != nil {
		t.Fatalf("BindNativeCodexPaneOnRoute error = %v", err)
	}
	if commands := cmdRecorder(cmd).commands; len(commands) != 0 {
		t.Fatalf("routed production binders used ambient subprocess runner: %#v", commands)
	}
	for _, title := range []string{"fresh", "resume", "native"} {
		found := false
		for _, call := range tmux.calls {
			if slices.Contains(call, "select-pane") && slices.Contains(call, "-T") &&
				slices.Contains(call, title) && flagValue(tmuxCommandArgv(call), "-t") == paneID {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("routed production binder omitted exact title %q for %s: %#v", title, paneID, tmux.calls)
		}
	}
	for _, option := range []string{
		aiPaneManagedOption, aiPaneAgentOption, aiPaneLaunchAuthorshipOption, aiPaneContextOption,
		aiPaneTopicOption, aiPaneStateOption, aiPaneSessionIDOption, aiPaneResumeIDOption,
		aiPaneResumeSourceOption, aiPaneResumeUpdatedAtOption, aiPaneThreadIDOption,
	} {
		found := false
		for _, call := range tmux.calls {
			if slices.Contains(call, "set-option") && slices.Contains(call, option) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("routed production binder omitted %s: %#v", option, tmux.calls)
		}
	}
	assertEveryTmuxCallHasExactRoute(t, tmux.calls)
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
	runner := &capturingAIRunner{}
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
	if got, want := entryValues(runner.options.Entries), []string{aiModeCodex, aiActionCodexAdvancedLaunch, aiModeClaude, aiModeAntigravity, aiModeShell}; !reflect.DeepEqual(got, want) {
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
			Agent:    aiModeCodex,
			ResumeID: fmt.Sprintf("019f0000-0000-7000-8000-%012d", i),
			Title:    fmt.Sprintf("Title %02d", i),
			Source:   aisessions.SourceCodexRollout, TitleProvenance: aisessions.TitleDerivedUserPrompt,
			LastModified: time.Date(2026, 6, 25, 9, i, 0, 0, time.UTC),
			Context:      aisessions.SessionContext{Branch: "feat/resume-picker"},
		})
	}

	rows, visible, total := aiResumeSessionRowsWithLabels(sessions, nil, aiResumePickerLimitDefault, time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC), i18n.FallbackLocale, "", 0)

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
// branch[18] + " " (separator before title).
const aiResumeRowPrefixWidth = aiResumeRelCellWidth + 1 + aiResumeBadgeCellWidth + 1 +
	aiResumeBranchCellWidth + 1

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

	rows, visible, total := aiResumeSessionRowsWithLabels(sessions, nil, aiResumePickerLimitDefault, now, i18n.FallbackLocale, "", 0)
	if visible != len(sessions) || total != len(sessions) {
		t.Fatalf("visible,total = %d,%d, want %d,%d", visible, total, len(sessions), len(sessions))
	}

	for i, session := range sessions {
		row := rows[i+1] // row 0 is the New Session entry
		title := truncateAIResumeCells(aiResumeDisplayLabel(session, aiResumeExactAgentLabel{}, i18n.FallbackLocale), aiResumeTitleMaxCells)
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

func TestAIResumeProviderNeutralRowSchema80ColumnGolden(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	providers := []struct {
		name   string
		source string
	}{
		{aiModeClaude, aisessions.SourceClaudeTranscript},
		{aiModeCodex, aisessions.SourceCodexRollout},
		{aiModeAntigravity, aisessions.SourceAntigravityMetadata},
	}
	var got strings.Builder
	for _, provider := range providers {
		bound := aiResumeExactAgentLabel{}
		if provider.name == aiModeCodex {
			bound.Context = registryview.Context{Value: "Codex bound conversation", Source: registryview.ContextSourceAgentTopic}
		}
		provenance := aisessions.TitleProvenanceNone
		if provider.name == aiModeClaude {
			provenance = aisessions.TitleExplicitProvider
		}
		row := aiResumeSessionRowWithResolvedLabel(aisessions.SessionMeta{
			TitleProvenance: provenance,
			Agent:           provider.name, ResumeID: provider.name + "-exact-id", Title: "Shared conversation title",
			LastModified: now.Add(-2 * time.Hour), Source: provider.source, Turns: 42,
			Confidence: "private-confidence", Reason: "private-reason", RuntimeStatus: "active",
			Context: aisessions.SessionContext{Branch: "feature/provider-neutral", CWD: "/workspace/projmux/internal/app"},
		}, bound, now, i18n.FallbackLocale, "/workspace/projmux", 1)
		plain := stripANSI(row.Label)
		for _, forbidden := range []string{"[fallback]", "42t", "active", "private-confidence", "private-reason", provider.source} {
			if forbidden != "" && strings.Contains(plain, forbidden) {
				t.Fatalf("%s visible row leaked %q: %q", provider.name, forbidden, plain)
			}
		}
		if !strings.Contains(row.SearchKey, provider.source) {
			t.Fatalf("%s SearchKey = %q, want frozen routing source %q", provider.name, row.SearchKey, provider.source)
		}
		fmt.Fprintf(&got, "%s|%s\n", provider.name, strings.TrimRight(i18n.TruncateTerminalCells(plain, 80), " "))
	}
	want, err := os.ReadFile(filepath.Join("testdata", "ai-resume-provider-neutral-80.golden"))
	if err != nil {
		t.Fatalf("read golden: %v\ngot:\n%s", err, got.String())
	}
	if got.String() != string(want) {
		t.Fatalf("provider-neutral golden mismatch:\ngot:\n%swant:\n%s", got.String(), want)
	}
}

func TestAIResumeProvidersShareConversationWidthPolicy(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	title := strings.Repeat("provider neutral conversation ", 8)
	providers := []struct{ name, source string }{
		{aiModeClaude, aisessions.SourceClaudeTranscript},
		{aiModeCodex, aisessions.SourceCodexAppServer},
		{aiModeAntigravity, aisessions.SourceAntigravityMetadata},
	}
	wantWidth := 0
	for _, provider := range providers {
		session := aisessions.SessionMeta{
			Agent: provider.name, ResumeID: provider.name + "-id", Title: title, Source: provider.source,
			LastModified: now.Add(-time.Hour), Context: aisessions.SessionContext{Branch: "main"},
		}
		if provider.name != aiModeAntigravity {
			session.TitleProvenance = aisessions.TitleExplicitProvider
		}
		if provider.name == aiModeCodex {
			session.StateDomainID = "state-width"
			session.EndpointGenerationID = "generation-width"
			session.GenerationState = string(coremetadata.CodexGenerationCurrent)
		}
		row := aiResumeSessionRowWithResolvedLabel(session, aiResumeExactAgentLabel{}, now, i18n.FallbackLocale, "/work", 0)
		width := i18n.TerminalCellWidth(row.Label)
		if wantWidth == 0 {
			wantWidth = width
		} else if width != wantWidth {
			t.Fatalf("%s row width = %d, want common width %d", provider.name, width, wantWidth)
		}
		if !strings.HasSuffix(stripANSI(row.Label), "…") {
			t.Fatalf("%s conversation did not use common ellipsis cap: %q", provider.name, stripANSI(row.Label))
		}
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
			_, visible, total := aiResumeSessionRowsWithLabels(sessions, nil, tc.limit, now, i18n.FallbackLocale, "", 0)
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
	row := aiResumeSessionRowWithResolvedLabel(aisessions.SessionMeta{
		Agent:        aiModeClaude,
		ResumeID:     resumeID,
		Title:        long,
		LastModified: now.Add(-time.Hour),
	}, aiResumeExactAgentLabel{}, now, i18n.FallbackLocale, "", 0)

	if !strings.Contains(row.Label, "…") {
		t.Fatalf("row label = %q, want ellipsis on overflow", row.Label)
	}
	title := truncateAIResumeCells(strings.Join(strings.Fields(long), " "), aiResumeTitleMaxCells)
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

	depth0 := aiResumeSessionRowWithResolvedLabel(session, aiResumeExactAgentLabel{}, now, i18n.FallbackLocale, "/workspace/app", 0)
	if strings.Contains(depth0.Label, "./web") {
		t.Fatalf("depth 0 row should hide cwd column: %q", depth0.Label)
	}

	depth1 := aiResumeSessionRowWithResolvedLabel(session, aiResumeExactAgentLabel{}, now, i18n.FallbackLocale, "/workspace/app", 1)
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

func TestAIResumeSessionRowHidesTurns(t *testing.T) {
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	row := aiResumeSessionRowWithResolvedLabel(aisessions.SessionMeta{
		Agent:        aiModeCodex,
		ResumeID:     "019f0000-0000-7000-8000-000000000042",
		Title:        "Optimize picker",
		LastModified: now.Add(-time.Hour),
		Turns:        31,
	}, aiResumeExactAgentLabel{}, now, i18n.FallbackLocale, "", 0)
	if strings.Contains(row.Label, "31t") {
		t.Fatalf("row label = %q, turn count belongs in detail", row.Label)
	}
}

func TestAIResumePickerNoSessionsKeepsInteractiveNewSessionSnapshot(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	var options intpicker.Options
	cmd := testAICommand(home)
	cmd.nativePicker = pickerRunnerFunc(func(got intpicker.Options) (intpicker.Result, error) {
		options = got
		return intpicker.Result{Key: "esc", Closed: true}, nil
	})
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
	if got, want := options.UI, "ai-resume-picker"; got != want {
		t.Fatalf("picker UI = %q, want %q", got, want)
	}
	if len(options.Items) != 1 || options.Items[0].Value != aiResumeNewValue {
		t.Fatalf("items = %#v, want only the New Session action", options.Items)
	}
	if len(options.ChromeBands) != 0 {
		t.Fatalf("upper provider chrome = %#v, want zero bands", options.ChromeBands)
	}
	if options.SelectionDetail == nil || !strings.Contains(options.SelectionDetail.TextByValue[aiResumeNewValue], "Select a resume session") {
		t.Fatalf("initial selection detail = %#v, want renderer-owned dock enabled from first frame", options.SelectionDetail)
	}
	lines := strings.Split(options.Footer, "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "Providers Codex ") || lines[1] != "Showing latest 0 resume sessions." {
		t.Fatalf("footer = %#v, want provider line then shown count", lines)
	}
}

func TestAIResumeNativeAndFallbackProvenanceIsAbsentFromRows(t *testing.T) {
	base := aisessions.SessionMeta{Agent: aisessions.AgentCodex, ResumeID: "thread-1", Title: "Provider title"}
	native := base
	native.Source, native.Confidence, native.RuntimeStatus = aisessions.SourceCodexAppServer, aisessions.ConfidenceHigh, "active"
	fallback := base
	fallback.Source, fallback.Confidence, fallback.Reason = aisessions.SourceCodexRollout, aisessions.ConfidenceMedium, aisessions.ReasonAppServerUnavailable
	for _, test := range []struct {
		name   string
		row    aisessions.SessionMeta
		hidden []string
	}{
		{"native", native, []string{"[active]", "codex-app-server", "high"}},
		{"fallback", fallback, []string{"[fallback]", "codex-rollout", "medium", "app-server-unavailable"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			row := aiResumeSessionRowWithResolvedLabel(test.row, aiResumeExactAgentLabel{}, time.Time{}, i18n.FallbackLocale, "/work", 0)
			for _, hidden := range test.hidden {
				if strings.Contains(row.Label, hidden) {
					t.Fatalf("row=%#v must keep %q out of visible label", row, hidden)
				}
			}
			if !strings.Contains(row.SearchKey, test.row.Source) {
				t.Fatalf("row=%#v must preserve frozen routing source", row)
			}
		})
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
	for _, value := range []string{aiModeCodex, aiModeClaude} {
		row := entryWithValue(rows, value)
		if row == nil || !strings.Contains(row.Label, "[READY]") {
			t.Fatalf("row %q = %#v, want READY without legacy wrapper", value, row)
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
	if strings.Contains(got, "tmux select-pane") {
		t.Fatalf("agentLaunchCommand = %q, must not delegate Pane title authority to the provider shell", got)
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
		if row, ok := testAIPaneRouteProbe(name, args); ok {
			return row, nil
		}
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
	if !sameRecordedAICommands(cmdRecorder(cmd).commands, want) {
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
		if row, ok := testAIPaneRouteProbe(name, args); ok {
			return row, nil
		}
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
	if len(commands) < len(wantPrefix) || !sameRecordedAICommands(commands[:len(wantPrefix)], wantPrefix) {
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
		if row, ok := testAIPaneRouteProbe(name, args); ok {
			return row, nil
		}
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
		if row, ok := testAIPaneRouteProbe(name, args); ok {
			return row, nil
		}
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
		if row, ok := testAIPaneRouteProbe(name, args); ok {
			return row, nil
		}
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
	if len(commands) < len(wantPrefix) || !sameRecordedAICommands(commands[:len(wantPrefix)], wantPrefix) {
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
		if row, ok := testAIPaneRouteProbe(name, args); ok {
			return row, nil
		}
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
	if len(commands) < len(wantPrefix) || !sameRecordedAICommands(commands[:len(wantPrefix)], wantPrefix) {
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
		if row, ok := testAIPaneRouteProbe(name, args); ok {
			return row, nil
		}
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
	if len(commands) < len(wantPrefix) || !sameRecordedAICommands(commands[:len(wantPrefix)], wantPrefix) {
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
		if row, ok := testAIPaneRouteProbe(name, args); ok {
			return row, nil
		}
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
		if row, ok := testAIPaneRouteProbe(name, args); ok {
			return row, nil
		}
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
		if row, ok := testAIPaneRouteProbe(name, args); ok {
			return row, nil
		}
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
		if row, ok := testAIPaneRouteProbe(name, args); ok {
			return row, nil
		}
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
		if row, ok := testAIPaneRouteProbe(name, args); ok {
			return row, nil
		}
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
	if containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%11", aiPaneLaunchAuthorshipOption}) {
		t.Fatalf("commands = %#v, title/bootstrap inference must not synthesize launch authorship", commands)
	}
}

func TestConfigureAIPaneWritesCanonicalLaunchReceipt(t *testing.T) {
	t.Parallel()

	cmd := testAICommand(t.TempDir())
	cmd.configureAIPane("%21", aiModeCodex, "/repo", "phase one", aiPaneResumeMetadata{})
	commands := cmdRecorder(cmd).commands
	want := recordedAICommand{name: "tmux", args: []string{"set-option", "-p", "-t", "%21", aiPaneLaunchAuthorshipOption, "1"}}
	count := 0
	for _, command := range commands {
		if command.name == want.name && sameRecordedAICommandArgs(command.name, command.args, want.args) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("launch receipt writes = %d, want one canonical configureAIPane write; commands=%#v", count, commands)
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
		if row, ok := testAIPaneRouteProbe(name, args); ok {
			return row, nil
		}
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
		if row, ok := testAIPaneRouteProbe(name, args); ok {
			return row, nil
		}
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
		if row, ok := testAIPaneRouteProbe(name, args); ok {
			return row, nil
		}
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
		readCommand: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if row, ok := testAIPaneRouteProbe(name, args); ok {
				return row, nil
			}
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
		if name == "tmux" && len(prefix) > 0 && prefix[0] != "-L" && prefix[0] != "-S" {
			command.args = stripRecordedTmuxRoute(command.args)
		}
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
		if command.name == want.name && sameRecordedAICommandArgs(command.name, command.args, want.args) {
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
	if !sameRecordedAICommands(cmdRecorder(cmd).commands, want) {
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
	if !sameRecordedAICommands(cmdRecorder(cmd).commands, want) {
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
	if !sameRecordedAICommands(cmdRecorder(cmd).commands, want) {
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
		"projmux agent topic set <text> [--pane <id>]",
		"projmux agent topic clear [--pane <id>]",
		"projmux agent topic get [--pane <id>]",
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
