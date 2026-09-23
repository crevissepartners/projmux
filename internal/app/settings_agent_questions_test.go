package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/i18n"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// agentQuestionsSettingsCommand is a Settings command over an isolated home,
// optionally pinned to one locale through the global config.
func agentQuestionsSettingsCommand(t *testing.T, locale i18n.Locale) (*settingsCommand, config.Paths) {
	t.Helper()
	home := t.TempDir()
	cmd := settingsNavTestCommand(t, home)
	paths, err := configPaths(cmd.homeDir, cmd.lookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	if locale != "" {
		writeSplitCWDGlobalConfig(t, cmd, "[ui]\nlocale = "+quoteLocale(locale)+"\n")
	}
	return cmd, paths
}

func writeAgentQuestionTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSettingsAgentQuestionsCopyIsExactInBothLocales pins the shipped copy:
// both cost sentences render as their own passive row, and the row, way and
// Unlimited labels are the agreed text, in en-US and ko-KR.
func TestSettingsAgentQuestionsCopyIsExactInBothLocales(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		locale                         i18n.Locale
		row, answering, window         string
		scope, perAgent                string
		wayOne, wayTwo, unlimited, def string
		sixty                          string
	}{
		{
			locale: i18n.FallbackLocale, row: "Agent questions", answering: "Answering", window: "Wait window",
			scope:     "Applies to every Claude Agent on this machine: each question waits in a projmux popup for the wait window (with Unlimited, until you answer).",
			perAgent:  "To change one Agent only, run projmux agent question enable <agent>.",
			wayOne:    "Claude Code prompt (way 1, default)",
			wayTwo:    "projmux popup (way 2)",
			unlimited: "Unlimited — until answered (at most 2147468s, about 24.8 days)",
			def:       "900s (default)", sixty: "60s",
		},
		{
			locale: i18n.Locale("ko-KR"), row: "Agent 질문", answering: "답변 방식", window: "대기 창",
			scope:     "이 머신의 모든 Claude Agent에 적용됩니다. 질문마다 projmux popup에서 대기 창만큼(무제한이면 답할 때까지) 붙잡힙니다.",
			perAgent:  "Agent 하나만 바꾸려면 projmux agent question enable <agent>를 실행하세요.",
			wayOne:    "Claude Code 기본 프롬프트 (방식 1, 기본값)",
			wayTwo:    "projmux popup (방식 2)",
			unlimited: "무제한 — 답할 때까지 (최대 2147468초, 약 24.8일)",
			def:       "900초 (기본값)", sixty: "60초",
		},
	} {
		t.Run(string(test.locale), func(t *testing.T) {
			t.Parallel()

			for id, want := range map[string]string{
				settingsNavAIQuestions:                test.row,
				settingsNavAIQuestions + ".answering": test.answering,
				settingsNavAIQuestions + ".window":    test.window,
			} {
				if got := settingsNavLabelLocale(test.locale, id); got != want {
					t.Fatalf("%s label = %q, want %q", id, got, want)
				}
			}
			cmd, _ := agentQuestionsSettingsCommand(t, test.locale)

			view := cmd.aiAgentQuestionsEntries()
			var passive []string
			for _, entry := range view {
				if entry.Value == settingsNoopValue {
					passive = append(passive, stripANSI(entry.Label))
				}
			}
			if len(passive) != 2 || !strings.HasSuffix(passive[0], test.scope) || !strings.HasSuffix(passive[1], test.perAgent) {
				t.Fatalf("passive rows = %q, want the two cost sentences, one per row", passive)
			}
			if !hasEntryLabelContainingAll(view, test.answering, test.wayOne) || !hasEntryLabelContainingAll(view, test.window, test.def) {
				t.Fatalf("view = %#v, want both value rows with their current values", view)
			}
			if root := cmd.aiRootEntries(); !hasEntryLabelContainingAll(root, test.row, test.wayOne, test.def) {
				t.Fatalf("AI root = %#v, want the Agent questions row with both values", root)
			}
			if answering := cmd.aiAgentQuestionAnsweringEntries(); !hasEntryLabelContaining(answering, test.wayOne) || !hasEntryLabelContaining(answering, test.wayTwo) {
				t.Fatalf("answering chooser = %#v", answering)
			}
			if window := cmd.aiAgentQuestionWindowEntries(); !hasEntryLabelContaining(window, test.unlimited) || !hasEntryLabelContaining(window, test.sixty) {
				t.Fatalf("window chooser = %#v", window)
			}
		})
	}
}

// agentQuestionActiveValues returns the chooser values marked current.
func agentQuestionActiveValues(entries []intpickercompat.Entry, prefix string) []string {
	var active []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Value, prefix) && strings.Contains(entry.Label, settingsGlyphToggle) {
			active = append(active, strings.TrimPrefix(entry.Value, prefix))
		}
	}
	return active
}

// TestSettingsAgentQuestionsChoosersMarkTheSavedValue holds that each chooser
// marks exactly the value the hook would read, that a saved in-range value
// outside the presets is listed and marked, and that every rendered row keeps
// the Settings owner contract.
func TestSettingsAgentQuestionsChoosersMarkTheSavedValue(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, window, answering string
		wantWindow, wantWay     string
	}{
		{name: "missing files", wantWindow: "900", wantWay: "claude"},
		{name: "way 2 and 60s", window: "60\n", answering: "projmux\n", wantWindow: "60", wantWay: "projmux"},
		{name: "a value between presets", window: "120\n", wantWindow: "120", wantWay: "claude"},
		{name: "unlimited", window: "Unlimited\n", wantWindow: "unlimited", wantWay: "claude"},
		{name: "broken files", window: "later\n", answering: "yes\n", wantWindow: "900", wantWay: "claude"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cmd, paths := agentQuestionsSettingsCommand(t, "")
			if test.window != "" {
				writeAgentQuestionTestFile(t, paths.AgentQuestionWindowSecondsFile(), test.window)
			}
			if test.answering != "" {
				writeAgentQuestionTestFile(t, paths.AgentQuestionAnsweringFile(), test.answering)
			}
			window := cmd.aiAgentQuestionWindowEntries()
			if got := agentQuestionActiveValues(window, settingsActionPrefixAIQuestionWindow); len(got) != 1 || got[0] != test.wantWindow {
				t.Fatalf("window marked %q, want %q", got, test.wantWindow)
			}
			answering := cmd.aiAgentQuestionAnsweringEntries()
			if got := agentQuestionActiveValues(answering, settingsActionPrefixAIQuestionAnswering); len(got) != 1 || got[0] != test.wantWay {
				t.Fatalf("answering marked %q, want %q", got, test.wantWay)
			}
			for ui, entries := range map[string][]intpickercompat.Entry{
				"settings-ai":                          cmd.aiRootEntries(),
				"settings-ai-agent-questions":          cmd.aiAgentQuestionsEntries(),
				"settings-ai-agent-question-answering": answering,
				"settings-ai-agent-question-window":    window,
			} {
				if err := validateSettingsEntryContracts(intpickercompat.Options{UI: ui, Entries: entries}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// TestSettingsAgentQuestionsFlowSavesThroughTheChoosers drives the view the
// way a user does: open a chooser, press a row, and the file holds the value.
func TestSettingsAgentQuestionsFlowSavesThroughTheChoosers(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		steps     []pickerStep
		path      func(config.Paths) string
		want      string
		wantStdio string
	}{
		{
			name: "answering way 2",
			steps: []pickerStep{
				{reply: intpickercompat.Result{Key: "enter", Value: settingsAIAgentQuestionAnswering}},
				{reply: intpickercompat.Result{Key: "enter", Value: settingsActionPrefixAIQuestionAnswering + "projmux"}},
			},
			path: config.Paths.AgentQuestionAnsweringFile, want: "projmux\n", wantStdio: "Agent question answering: projmux",
		},
		{
			name: "window unlimited",
			steps: []pickerStep{
				{reply: intpickercompat.Result{Key: "enter", Value: settingsAIAgentQuestionWindow}},
				{reply: intpickercompat.Result{Key: "enter", Value: settingsActionPrefixAIQuestionWindow + "unlimited"}},
			},
			path: config.Paths.AgentQuestionWindowSecondsFile, want: "unlimited\n", wantStdio: "Agent question window: unlimited",
		},
		{
			name: "window custom 120",
			steps: []pickerStep{
				{reply: intpickercompat.Result{Key: "enter", Value: settingsAIAgentQuestionWindow}},
				{reply: intpickercompat.Result{Key: "enter", Value: settingsActionPrefixAIQuestionWindow + "custom"}},
				{reply: intpickercompat.Result{Key: "enter", Query: "120"}},
			},
			path: config.Paths.AgentQuestionWindowSecondsFile, want: "120\n", wantStdio: "Agent question window: 120s",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cmd, paths := agentQuestionsSettingsCommand(t, "")
			cmd.runner, cmd.nativePicker = scriptedPicker(t, test.steps)
			var stdout bytes.Buffer
			if err := cmd.runAIAgentQuestionsSection(&stdout, &bytes.Buffer{}); !errors.Is(err, errSettingsClosed) {
				t.Fatalf("flow err = %v, want the picker to close at the end of the script", err)
			}
			if raw, err := os.ReadFile(test.path(paths)); err != nil || string(raw) != test.want {
				t.Fatalf("file = %q (%v), want %q", raw, err, test.want)
			}
			if !strings.Contains(stdout.String(), test.wantStdio) {
				t.Fatalf("stdout = %q, want %q", stdout.String(), test.wantStdio)
			}
		})
	}
}

// TestSettingsAgentQuestionWindowCustomRefusesOutOfRange holds the custom
// input to 60..3600 whole seconds: anything else is handled feedback and
// leaves the file alone.
func TestSettingsAgentQuestionWindowCustomRefusesOutOfRange(t *testing.T) {
	t.Parallel()

	for _, query := range []string{"59", "3601", "0", "-5", "unlimited", "fifteen", ""} {
		t.Run(query, func(t *testing.T) {
			t.Parallel()
			cmd, paths := agentQuestionsSettingsCommand(t, "")
			cmd.runner, cmd.nativePicker = scriptedPicker(t, []pickerStep{{reply: intpickercompat.Result{Key: "enter", Query: query}}})
			var stderr bytes.Buffer
			if err := cmd.runAIAgentQuestionWindowCustom(&bytes.Buffer{}, &stderr); err != nil {
				t.Fatalf("custom err = %v, want handled feedback", err)
			}
			if cmd.feedback == nil || cmd.feedback.Summary != "Agent question window failed" || stderr.Len() == 0 {
				t.Fatalf("feedback = %#v stderr = %q", cmd.feedback, stderr.String())
			}
			if _, err := os.Stat(paths.AgentQuestionWindowSecondsFile()); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("refused input wrote the window file (%v)", err)
			}
		})
	}
}

// TestSettingsAgentQuestionWindowReachesTheNextQuestionWithoutIntegrate is the
// no-re-integrate guarantee: a window saved from Settings is what the hook's
// own resolver reads for the next question, Unlimited included, and that
// question's record deadline is created with it. Nothing integrates between
// the save and the question.
func TestSettingsAgentQuestionWindowReachesTheNextQuestionWithoutIntegrate(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		seconds int
		want    time.Duration
	}{
		{name: "120s", seconds: 120, want: 120 * time.Second},
		{name: "3600s", seconds: 3600, want: time.Hour},
		{name: "unlimited", seconds: config.UnlimitedAgentQuestionWindowSeconds, want: 2147468 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cmd, paths := agentQuestionsSettingsCommand(t, "")
			if got := claudeQuestionWindowFromPaths(paths); got != 900*time.Second {
				t.Fatalf("window before the save = %s, want the default", got)
			}
			if err := cmd.setAgentQuestionWindowSeconds(test.seconds, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			if got := claudeQuestionWindowFromPaths(paths); got != test.want {
				t.Fatalf("hook window after the save = %s, want %s", got, test.want)
			}

			fixture := newQuestionFixture(t, true)
			hook := fixture.hook(0)
			hook.window = func() time.Duration { return claudeQuestionWindowFromPaths(paths) }
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan string, 1)
			go func() {
				var stdout bytes.Buffer
				hook.run(ctx, []string{"--pane=" + questionTestPane}, strings.NewReader(questionTestPayload("PreToolUse", "AskUserQuestion")), &stdout, &bytes.Buffer{})
				done <- stdout.String()
			}()
			var id string
			for deadline := time.Now().Add(5 * time.Second); id == ""; time.Sleep(5 * time.Millisecond) {
				if records, err := fixture.store.List(questionTestAgent); err == nil && len(records) == 1 {
					id = records[0].ID
				}
				if time.Now().After(deadline) {
					t.Fatal("the hook never recorded its question")
				}
			}
			cancel()
			waitHookOutput(t, done)
			record, _, _ := fixture.store.Get(id)
			if got := record.Deadline.Sub(record.CreatedAt); got != test.want {
				t.Fatalf("record window = %s, want %s", got, test.want)
			}
		})
	}
}
