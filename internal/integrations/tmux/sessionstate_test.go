package tmux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
)

func TestClientCaptureSessionSnapshotCapturesWindowsPanesAndConservativeRecipes(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 12, 3, 4, 5, 0, time.UTC)
	runner := &scriptedRunner{
		t: t,
		steps: []scriptedStep{
			{output: []byte(
				"0\x1fshell\x1flayout-a\n" +
					"2\x1fwork\x1flayout-b\n",
			)},
			{output: []byte(
				"0\x1f0\x1fshell\x1f0\x1f/home/tester\x1f\x1f\x1f\x1f\x1f\x1f\n" +
					"0\x1f1\x1fwatcher\x1f1\x1f/home/tester/app\x1fstartup\x1fmake watch\x1f\x1f\x1f\x1f\n" +
					"2\x1f0\x1fcodex task\x1f1\x1f/home/tester/app\x1f\x1f\x1f1\x1fcodex\x1fsession state\x1f01973f21-abc\x1fsession-id\x1f2026-05-12T03:04:05Z\n" +
					"2\x1f1\x1fclaude task\x1f0\x1f/home/tester/app\x1f\x1f\x1f1\x1fclaude\x1fmissing resume\x1f\n",
			)},
			{output: []byte("layout(team)\n")},
		},
	}
	client := NewClient(runner)

	got, err := client.CaptureSessionSnapshot(context.Background(), "workspace", now)
	if err != nil {
		t.Fatalf("CaptureSessionSnapshot() error = %v", err)
	}

	want := sessionstate.Snapshot{
		Version:    sessionstate.Version,
		Session:    "workspace",
		Source:     sessionstate.LayoutSource("team"),
		DefaultCWD: "/home/tester",
		SavedAt:    now,
		Windows: []sessionstate.Window{
			{
				Index:           0,
				Name:            "shell",
				Layout:          "layout-a",
				ActivePaneIndex: 1,
				Panes: []sessionstate.Pane{
					{Index: 0, Title: "shell", CWD: "/home/tester", Recipe: sessionstate.ShellRecipe()},
					{Index: 1, Title: "watcher", CWD: "/home/tester/app", Recipe: sessionstate.StartupRecipe("make watch")},
				},
			},
			{
				Index:           2,
				Name:            "work",
				Layout:          "layout-b",
				ActivePaneIndex: 0,
				Panes: []sessionstate.Pane{
					{Index: 0, Title: "codex task", CWD: "/home/tester/app", Recipe: sessionstate.AgentRecipeWithResumeMetadata("codex", "01973f21-abc", "session state", "session-id", "2026-05-12T03:04:05Z")},
					{Index: 1, Title: "claude task", CWD: "/home/tester/app", Recipe: sessionstate.AgentRecipe("claude", "", "missing resume")},
				},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CaptureSessionSnapshot() = %#v, want %#v", got, want)
	}

	wantCalls := []commandCall{
		{name: "tmux", args: []string{"list-windows", "-t", "workspace", "-F", tmuxFormat("#{window_index}", "#{window_name}", "#{window_layout}")}},
		{name: "tmux", args: []string{"list-panes", "-s", "-t", "workspace", "-F", tmuxFormat(
			"#{window_index}",
			"#{pane_index}",
			"#{pane_title}",
			"#{?pane_active,1,0}",
			"#{pane_current_path}",
			"#{@projmux_recipe_kind}",
			"#{@projmux_startup_command}",
			"#{@projmux_ai_managed}",
			"#{@projmux_ai_agent}",
			"#{@projmux_ai_topic}",
			"#{@projmux_ai_resume_id}",
			"#{@projmux_ai_resume_source}",
			"#{@projmux_ai_resume_updated_at}",
		)}},
		{name: "tmux", args: []string{"display-message", "-p", "-t", "workspace", "#{@projmux_sessionstate_source}"}},
	}
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Fatalf("tmux calls = %#v, want %#v", runner.calls, wantCalls)
	}
}

func TestClientSaveSessionSnapshotWritesStore(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 12, 3, 4, 5, 0, time.UTC)
	runner := &scriptedRunner{
		t: t,
		steps: []scriptedStep{
			{output: []byte("\n")},
			{output: []byte("0\x1fshell\x1flayout\n")},
			{output: []byte("0\x1f0\x1fshell\x1f1\x1f/tmp\x1f\x1f\x1f\x1f\x1f\x1f\n")},
			{output: []byte("\n")},
		},
	}
	store := sessionstate.NewStore(t.TempDir())
	client := NewClient(runner)

	snap, err := client.SaveSessionSnapshot(context.Background(), store, "workspace", now)
	if err != nil {
		t.Fatalf("SaveSessionSnapshot() error = %v", err)
	}
	loaded, err := store.Load("workspace")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !reflect.DeepEqual(loaded, snap) {
		t.Fatalf("loaded snapshot = %#v, want %#v", loaded, snap)
	}
}

func TestClientSaveSessionSnapshotRefreshesAIResumeIDFromSessionIDBeforeCapture(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 12, 3, 4, 5, 0, time.UTC)
	runner := &scriptedRunner{
		t: t,
		steps: []scriptedStep{
			{output: []byte(
				"%1\x1f/tmp\x1f1\x1fcodex\x1fcodex-session\x1f\x1f\n" +
					"%2\x1f/tmp\x1f1\x1fclaude\x1fclaude-session\x1fold-claude-session\x1f\n",
			)},
			{output: []byte{}},
			{output: []byte{}},
			{output: []byte{}},
			{output: []byte{}},
			{output: []byte{}},
			{output: []byte{}},
			{output: []byte("0\x1fwork\x1flayout\n")},
			{output: []byte(
				"0\x1f0\x1fcodex task\x1f1\x1f/tmp\x1f\x1f\x1f1\x1fcodex\x1ftopic\x1fcodex-session\n" +
					"0\x1f1\x1fclaude task\x1f0\x1f/tmp\x1f\x1f\x1f1\x1fclaude\x1ftopic\x1fclaude-session\n",
			)},
			{output: []byte("\n")},
		},
	}
	store := sessionstate.NewStore(t.TempDir())
	client := NewClient(runner)

	snap, err := client.SaveSessionSnapshot(context.Background(), store, "workspace", now)
	if err != nil {
		t.Fatalf("SaveSessionSnapshot() error = %v", err)
	}

	if got := snap.Windows[0].Panes[0].Recipe; !reflect.DeepEqual(got, sessionstate.AgentRecipe("codex", "codex-session", "topic")) {
		t.Fatalf("codex recipe = %#v, want refreshed agent recipe", got)
	}
	if got := snap.Windows[0].Panes[1].Recipe; !reflect.DeepEqual(got, sessionstate.AgentRecipe("claude", "claude-session", "topic")) {
		t.Fatalf("claude recipe = %#v, want refreshed agent recipe", got)
	}

	wantPrefix := []commandCall{
		{name: "tmux", args: []string{"list-panes", "-s", "-t", "workspace", "-F", tmuxFormat(
			"#{pane_id}",
			"#{pane_current_path}",
			"#{@projmux_ai_managed}",
			"#{@projmux_ai_agent}",
			"#{@projmux_ai_session_id}",
			"#{@projmux_ai_resume_id}",
			"#{@projmux_ai_transcript_path}",
		)}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%1", "@projmux_ai_resume_id", "codex-session"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%1", "@projmux_ai_resume_source", "session-id"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%1", "@projmux_ai_resume_updated_at", "2026-05-12T03:04:05Z"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%2", "@projmux_ai_resume_id", "claude-session"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%2", "@projmux_ai_resume_source", "session-id"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%2", "@projmux_ai_resume_updated_at", "2026-05-12T03:04:05Z"}},
	}
	if len(runner.calls) < len(wantPrefix) || !reflect.DeepEqual(runner.calls[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("tmux call prefix = %#v, want %#v", runner.calls, wantPrefix)
	}
}

func TestClientSaveSessionSnapshotRefreshesClaudeResumeIDFromTranscriptBeforeCapture(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 12, 3, 4, 5, 0, time.UTC)
	transcriptPath := "/tmp/claude-transcript.jsonl"
	runner := &scriptedRunner{
		t: t,
		steps: []scriptedStep{
			{output: []byte("%2\x1f/tmp\x1f1\x1fclaude\x1f\x1f\x1f" + transcriptPath + "\n")},
			{output: []byte{}},
			{output: []byte{}},
			{output: []byte{}},
			{output: []byte("0\x1fwork\x1flayout\n")},
			{output: []byte("0\x1f0\x1fclaude task\x1f1\x1f/tmp\x1f\x1f\x1f1\x1fclaude\x1ftopic\x1fclaude-transcript-session\n")},
			{output: []byte("\n")},
		},
	}
	store := sessionstate.NewStore(t.TempDir())
	client := NewClient(runner, WithFileReader(func(path string) ([]byte, error) {
		if path != transcriptPath {
			t.Fatalf("read file path = %q, want %q", path, transcriptPath)
		}
		return []byte(`{"type":"summary"}` + "\n" + `{"sessionId":"claude-transcript-session"}` + "\n"), nil
	}))

	snap, err := client.SaveSessionSnapshot(context.Background(), store, "workspace", now)
	if err != nil {
		t.Fatalf("SaveSessionSnapshot() error = %v", err)
	}
	if got := snap.Windows[0].Panes[0].Recipe; !reflect.DeepEqual(got, sessionstate.AgentRecipe("claude", "claude-transcript-session", "topic")) {
		t.Fatalf("recipe = %#v, want transcript-backed claude agent recipe", got)
	}

	wantPrefix := []commandCall{
		{name: "tmux", args: []string{"list-panes", "-s", "-t", "workspace", "-F", tmuxFormat(
			"#{pane_id}",
			"#{pane_current_path}",
			"#{@projmux_ai_managed}",
			"#{@projmux_ai_agent}",
			"#{@projmux_ai_session_id}",
			"#{@projmux_ai_resume_id}",
			"#{@projmux_ai_transcript_path}",
		)}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%2", "@projmux_ai_resume_id", "claude-transcript-session"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%2", "@projmux_ai_resume_source", "claude-transcript"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%2", "@projmux_ai_resume_updated_at", "2026-05-12T03:04:05Z"}},
	}
	if len(runner.calls) < len(wantPrefix) || !reflect.DeepEqual(runner.calls[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("tmux call prefix = %#v, want %#v", runner.calls, wantPrefix)
	}
}

func TestClientSaveSessionSnapshotRefreshesCodexResumeIDFromMatchingRolloutLog(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 12, 3, 4, 5, 0, time.UTC)
	home := t.TempDir()
	writeCodexRolloutLog(t, home, filepath.Join("2026", "05", "12", "rollout-matching.jsonl"), `{"type":"session_meta","payload":{"id":"codex-log-session","cwd":"/home/tester/app"}}`+"\n", now)
	writeCodexRolloutLog(t, home, filepath.Join("2026", "05", "12", "rollout-other.jsonl"), `{"type":"session_meta","payload":{"id":"codex-other-session","cwd":"/home/tester/other"}}`+"\n", now.Add(time.Minute))

	runner := &scriptedRunner{
		t: t,
		steps: []scriptedStep{
			{output: []byte("%1\x1f/home/tester/app\x1f1\x1fcodex\x1f\x1f\x1f\n")},
			{output: []byte{}},
			{output: []byte{}},
			{output: []byte{}},
			{output: []byte("0\x1fwork\x1flayout\n")},
			{output: []byte("0\x1f0\x1fcodex task\x1f1\x1f/home/tester/app\x1f\x1f\x1f1\x1fcodex\x1ftopic\x1fcodex-log-session\n")},
			{output: []byte("\n")},
		},
	}
	store := sessionstate.NewStore(t.TempDir())
	client := newClientWithEnv(runner, func(name string) string {
		if name == "HOME" {
			return home
		}
		return ""
	})

	snap, err := client.SaveSessionSnapshot(context.Background(), store, "workspace", now)
	if err != nil {
		t.Fatalf("SaveSessionSnapshot() error = %v", err)
	}
	if got := snap.Windows[0].Panes[0].Recipe; !reflect.DeepEqual(got, sessionstate.AgentRecipe("codex", "codex-log-session", "topic")) {
		t.Fatalf("recipe = %#v, want codex log-backed agent recipe", got)
	}

	wantPrefix := []commandCall{
		{name: "tmux", args: []string{"list-panes", "-s", "-t", "workspace", "-F", tmuxFormat(
			"#{pane_id}",
			"#{pane_current_path}",
			"#{@projmux_ai_managed}",
			"#{@projmux_ai_agent}",
			"#{@projmux_ai_session_id}",
			"#{@projmux_ai_resume_id}",
			"#{@projmux_ai_transcript_path}",
		)}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%1", "@projmux_ai_resume_id", "codex-log-session"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%1", "@projmux_ai_resume_source", "codex-log"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%1", "@projmux_ai_resume_updated_at", "2026-05-12T03:04:05Z"}},
	}
	if len(runner.calls) < len(wantPrefix) || !reflect.DeepEqual(runner.calls[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("tmux call prefix = %#v, want %#v", runner.calls, wantPrefix)
	}
}

func TestClientSaveSessionSnapshotAmbiguousCodexRolloutLogsWithoutCWDDoNotRefresh(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 12, 3, 4, 5, 0, time.UTC)
	home := t.TempDir()
	writeCodexRolloutLog(t, home, filepath.Join("2026", "05", "12", "rollout-one.jsonl"), `{"type":"session_meta","payload":{"id":"codex-log-one"}}`+"\n", now)
	writeCodexRolloutLog(t, home, filepath.Join("2026", "05", "12", "rollout-two.jsonl"), `{"type":"session_meta","payload":{"id":"codex-log-two"}}`+"\n", now.Add(time.Minute))

	runner := &scriptedRunner{
		t: t,
		steps: []scriptedStep{
			{output: []byte("%1\x1f/home/tester/app\x1f1\x1fcodex\x1f\x1f\x1f\n")},
			{output: []byte("0\x1fwork\x1flayout\n")},
			{output: []byte("0\x1f0\x1fcodex task\x1f1\x1f/home/tester/app\x1f\x1f\x1f1\x1fcodex\x1ftopic\x1f\n")},
			{output: []byte("\n")},
		},
	}
	store := sessionstate.NewStore(t.TempDir())
	client := newClientWithEnv(runner, func(name string) string {
		if name == "HOME" {
			return home
		}
		return ""
	})

	snap, err := client.SaveSessionSnapshot(context.Background(), store, "workspace", now)
	if err != nil {
		t.Fatalf("SaveSessionSnapshot() error = %v", err)
	}
	if got := snap.Windows[0].Panes[0].Recipe; !reflect.DeepEqual(got, sessionstate.AgentRecipe("codex", "", "topic")) {
		t.Fatalf("recipe = %#v, want unavailable codex agent recipe", got)
	}
	for _, call := range runner.calls {
		if len(call.args) > 0 && call.args[0] == "set-option" {
			t.Fatalf("unexpected set-option call for ambiguous codex logs: %#v", call)
		}
	}
}

func TestClientSaveSessionSnapshotClaudePaneDoesNotUseCodexRolloutLog(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 12, 3, 4, 5, 0, time.UTC)
	home := t.TempDir()
	writeCodexRolloutLog(t, home, filepath.Join("2026", "05", "12", "rollout-matching.jsonl"), `{"type":"session_meta","payload":{"id":"codex-log-session","cwd":"/home/tester/app"}}`+"\n", now)

	runner := &scriptedRunner{
		t: t,
		steps: []scriptedStep{
			{output: []byte("%2\x1f/home/tester/app\x1f1\x1fclaude\x1f\x1f\x1f\n")},
			{output: []byte("0\x1fwork\x1flayout\n")},
			{output: []byte("0\x1f0\x1fclaude task\x1f1\x1f/home/tester/app\x1f\x1f\x1f1\x1fclaude\x1ftopic\x1f\n")},
			{output: []byte("\n")},
		},
	}
	store := sessionstate.NewStore(t.TempDir())
	client := newClientWithEnv(runner, func(name string) string {
		if name == "HOME" {
			return home
		}
		return ""
	})

	snap, err := client.SaveSessionSnapshot(context.Background(), store, "workspace", now)
	if err != nil {
		t.Fatalf("SaveSessionSnapshot() error = %v", err)
	}
	if got := snap.Windows[0].Panes[0].Recipe; !reflect.DeepEqual(got, sessionstate.AgentRecipe("claude", "", "topic")) {
		t.Fatalf("recipe = %#v, want unavailable claude agent recipe", got)
	}
	for _, call := range runner.calls {
		if len(call.args) > 0 && call.args[0] == "set-option" {
			t.Fatalf("unexpected set-option call for claude codex-log fallback: %#v", call)
		}
	}
}

func TestClientSaveSessionSnapshotTranscriptFallbackDoesNotApplyToCodex(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 12, 3, 4, 5, 0, time.UTC)
	runner := &scriptedRunner{
		t: t,
		steps: []scriptedStep{
			{output: []byte("%1\x1f/tmp\x1f1\x1fcodex\x1f\x1f\x1f/tmp/codex-transcript.jsonl\n")},
			{output: []byte("0\x1fwork\x1flayout\n")},
			{output: []byte("0\x1f0\x1fcodex task\x1f1\x1f/tmp\x1f\x1f\x1f1\x1fcodex\x1ftopic\x1f\n")},
			{output: []byte("\n")},
		},
	}
	store := sessionstate.NewStore(t.TempDir())
	home := t.TempDir()
	client := newClientWithEnv(runner, func(string) string { return home }, WithFileReader(func(path string) ([]byte, error) {
		t.Fatalf("unexpected transcript read for codex pane: %q", path)
		return nil, nil
	}))

	snap, err := client.SaveSessionSnapshot(context.Background(), store, "workspace", now)
	if err != nil {
		t.Fatalf("SaveSessionSnapshot() error = %v", err)
	}
	if got := snap.Windows[0].Panes[0].Recipe; !reflect.DeepEqual(got, sessionstate.AgentRecipe("codex", "", "topic")) {
		t.Fatalf("recipe = %#v, want unavailable codex agent recipe", got)
	}
	for _, call := range runner.calls {
		if len(call.args) > 0 && call.args[0] == "set-option" {
			t.Fatalf("unexpected set-option call for codex transcript fallback: %#v", call)
		}
	}
}

func TestClientSaveSessionSnapshotUnreadableClaudeTranscriptDoesNotClearExistingResumeMetadata(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 12, 3, 4, 5, 0, time.UTC)
	runner := &scriptedRunner{
		t: t,
		steps: []scriptedStep{
			{output: []byte("%2\x1f/tmp\x1f1\x1fclaude\x1f\x1fexisting-resume\x1f/tmp/missing-transcript.jsonl\n")},
			{output: []byte("0\x1fwork\x1flayout\n")},
			{output: []byte("0\x1f0\x1fclaude task\x1f1\x1f/tmp\x1f\x1f\x1f1\x1fclaude\x1ftopic\x1fexisting-resume\n")},
			{output: []byte("\n")},
		},
	}
	store := sessionstate.NewStore(t.TempDir())
	client := NewClient(runner, WithFileReader(func(string) ([]byte, error) {
		return nil, errors.New("missing transcript")
	}))

	snap, err := client.SaveSessionSnapshot(context.Background(), store, "workspace", now)
	if err != nil {
		t.Fatalf("SaveSessionSnapshot() error = %v", err)
	}
	if got := snap.Windows[0].Panes[0].Recipe; !reflect.DeepEqual(got, sessionstate.AgentRecipe("claude", "existing-resume", "topic")) {
		t.Fatalf("recipe = %#v, want existing resume metadata preserved", got)
	}
	for _, call := range runner.calls {
		if len(call.args) > 0 && call.args[0] == "set-option" {
			t.Fatalf("unexpected set-option call for unreadable transcript: %#v", call)
		}
	}
}

func TestClientSaveSessionSnapshotBlankSessionIDDoesNotClearExistingResumeMetadata(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 12, 3, 4, 5, 0, time.UTC)
	runner := &scriptedRunner{
		t: t,
		steps: []scriptedStep{
			{output: []byte("%1\x1f/tmp\x1f1\x1fcodex\x1f\x1fexisting-resume\x1f\n")},
			{output: []byte("0\x1fwork\x1flayout\n")},
			{output: []byte("0\x1f0\x1fcodex task\x1f1\x1f/tmp\x1f\x1f\x1f1\x1fcodex\x1ftopic\x1fexisting-resume\n")},
			{output: []byte("\n")},
		},
	}
	store := sessionstate.NewStore(t.TempDir())
	client := NewClient(runner)

	snap, err := client.SaveSessionSnapshot(context.Background(), store, "workspace", now)
	if err != nil {
		t.Fatalf("SaveSessionSnapshot() error = %v", err)
	}
	if got := snap.Windows[0].Panes[0].Recipe; !reflect.DeepEqual(got, sessionstate.AgentRecipe("codex", "existing-resume", "topic")) {
		t.Fatalf("recipe = %#v, want existing resume agent recipe", got)
	}
	for _, call := range runner.calls {
		if len(call.args) > 0 && call.args[0] == "set-option" {
			t.Fatalf("unexpected set-option call for blank session id: %#v", call)
		}
	}
}

func TestClientSaveSessionSnapshotRefreshSetOptionFailureIsBestEffort(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 12, 3, 4, 5, 0, time.UTC)
	runner := &scriptedRunner{
		t: t,
		steps: []scriptedStep{
			{output: []byte("%1\x1f/tmp\x1f1\x1fcodex\x1fcodex-session\x1fold-resume\x1f\n")},
			{err: errors.New("set-option failed")},
			{output: []byte("0\x1fwork\x1flayout\n")},
			{output: []byte("0\x1f0\x1fcodex task\x1f1\x1f/tmp\x1f\x1f\x1f1\x1fcodex\x1ftopic\x1fcodex-session\n")},
			{output: []byte("\n")},
		},
	}
	store := sessionstate.NewStore(t.TempDir())
	client := NewClient(runner)

	snap, err := client.SaveSessionSnapshot(context.Background(), store, "workspace", now)
	if err != nil {
		t.Fatalf("SaveSessionSnapshot() error = %v", err)
	}
	if got := snap.Windows[0].Panes[0].Recipe; !reflect.DeepEqual(got, sessionstate.AgentRecipe("codex", "codex-session", "topic")) {
		t.Fatalf("recipe = %#v, want capture to proceed after refresh failure", got)
	}
}

func TestClientCaptureSessionSnapshotWrapsPartialQueryFailure(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "list-windows" {
			return []byte("0\x1fshell\x1flayout\n"), nil
		}
		return nil, errors.New("tmux unavailable")
	}))

	_, err := client.CaptureSessionSnapshot(context.Background(), "workspace", time.Now())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "capture tmux session \"workspace\" panes") {
		t.Fatalf("error = %v, want pane capture context", err)
	}
}

func writeCodexRolloutLog(t *testing.T, home, relPath, content string, modTime time.Time) {
	t.Helper()
	path := filepath.Join(home, ".codex", "sessions", relPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("Chtimes(%q) error = %v", path, err)
	}
}
