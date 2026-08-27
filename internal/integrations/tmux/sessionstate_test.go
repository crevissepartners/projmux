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

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
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
				"0\x1f0\x1fshell\x1fprimary shell\x1f0\x1f/home/tester\x1f\x1f\x1f\x1f\x1f\x1f\x1f\x1f\n" +
					"0\x1f1\x1fwatcher\x1f\x1f1\x1f/home/tester/app\x1fstartup\x1fmake watch\x1f\x1f\x1f\x1f\x1f\x1f\n" +
					"2\x1f0\x1fcodex task\x1freview label\x1f1\x1f/home/tester/app\x1f\x1f\x1f1\x1fcodex\x1fsession state\x1fon\x1f01973f21-abc\x1fsession-id\x1f2026-05-12T03:04:05Z\n" +
					"2\x1f1\x1fclaude task\x1f\x1f0\x1f/home/tester/app\x1f\x1f\x1f1\x1fclaude\x1fmissing resume\x1f\x1f\x1f\x1f\n",
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
					{Index: 0, Label: "primary shell", Title: "shell", CWD: "/home/tester", Recipe: sessionstate.ShellRecipe()},
					{Index: 1, Title: "watcher", CWD: "/home/tester/app", Recipe: sessionstate.StartupRecipe("make watch")},
				},
			},
			{
				Index:           2,
				Name:            "work",
				Layout:          "layout-b",
				ActivePaneIndex: 0,
				Panes: []sessionstate.Pane{
					{Index: 0, Label: "review label", Title: "codex task", CWD: "/home/tester/app", Recipe: sessionstate.Recipe{Kind: sessionstate.RecipeKindAgent, Agent: "codex", ResumeID: "01973f21-abc", ResumeSource: "session-id", ResumeUpdatedAt: "2026-05-12T03:04:05Z", Topic: "session state", TopicManual: true}},
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
			"#{@projmux_pane_label}",
			"#{?pane_active,1,0}",
			"#{pane_current_path}",
			"#{@projmux_recipe_kind}",
			"#{@projmux_startup_command}",
			"#{@projmux_ai_managed}",
			"#{@projmux_ai_agent}",
			"#{@projmux_ai_topic}",
			"#{@projmux_ai_topic_manual}",
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

func TestClientSaveExplicitSessionSnapshotTargetsObservedIDAndStoresStableName(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 27, 3, 4, 5, 0, time.UTC)
	runner := &scriptedRunner{
		t: t,
		steps: []scriptedStep{
			{output: []byte("\n")},
			{output: []byte("0\x1fshell\x1flayout\n")},
			{output: []byte("0\x1f0\x1fshell\x1f\x1f1\x1f/tmp\x1f\x1f\x1f\x1f\x1f\x1f\x1f\x1f\x1f\x1f\n")},
			{output: []byte("\n")},
		},
	}
	store := sessionstate.NewStore(t.TempDir())

	snap, err := NewClient(runner).SaveExplicitSessionSnapshot(context.Background(), store, "$7", "workspace", now)
	if err != nil {
		t.Fatalf("SaveExplicitSessionSnapshot() error = %v", err)
	}
	if snap.Session != "workspace" {
		t.Fatalf("snapshot Session = %q, want stable invocation-start name", snap.Session)
	}
	loaded, err := store.Load("workspace")
	if err != nil || !reflect.DeepEqual(loaded, snap) {
		t.Fatalf("Load(workspace) = %#v, %v; want saved snapshot %#v", loaded, err, snap)
	}
	for _, call := range runner.calls {
		joined := strings.Join(call.args, " ")
		if !strings.Contains(joined, "$7") {
			t.Fatalf("tmux call = %#v, want every capture read pinned to exact session id $7", call)
		}
		if strings.Contains(joined, "-t workspace") {
			t.Fatalf("tmux call followed mutable session name: %#v", call)
		}
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
			"#{@projmux_ai_thread_id}",
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

func TestSessionStateBoundSessionIDWinsWithoutThreadRead(t *testing.T) {
	now := time.Date(2026, 8, 22, 1, 2, 3, 0, time.UTC)
	runner := &scriptedRunner{t: t, steps: []scriptedStep{
		{output: []byte("%1\x1f/work/app\x1f1\x1fcodex\x1fthread-bound\x1fthread-candidate\x1f\x1f\n")},
		{output: []byte{}}, {output: []byte{}}, {output: []byte{}},
	}}
	reads := 0
	client := NewClient(runner, WithCodexCatalogThreadReader(func(context.Context, string) (codexappserver.CatalogThread, error) {
		reads++
		return codexappserver.CatalogThread{}, errors.New("must not read")
	}))
	if err := client.refreshSessionStateAIResumeMetadata(context.Background(), "workspace", now); err != nil {
		t.Fatal(err)
	}
	if reads != 0 {
		t.Fatalf("thread/read calls = %d, want zero for bound session id", reads)
	}
	if len(runner.calls) != 4 || runner.calls[1].args[len(runner.calls[1].args)-1] != "thread-bound" {
		t.Fatalf("calls = %#v", runner.calls)
	}
}

func TestSessionStatePersistedResumeIDWinsWithoutThreadRead(t *testing.T) {
	runner := &scriptedRunner{t: t, steps: []scriptedStep{
		{output: []byte("%1\x1f/work/app\x1f1\x1fcodex\x1f\x1fthread-candidate\x1fthread-persisted\x1f\n")},
	}}
	reads := 0
	client := NewClient(runner, WithCodexCatalogThreadReader(func(context.Context, string) (codexappserver.CatalogThread, error) {
		reads++
		return codexappserver.CatalogThread{}, errors.New("must not read")
	}))
	if err := client.refreshSessionStateAIResumeMetadata(context.Background(), "workspace", time.Now()); err != nil {
		t.Fatal(err)
	}
	if reads != 0 || len(runner.calls) != 1 {
		t.Fatalf("reads=%d calls=%#v, persisted resume id must be replayed without validation or rewrite", reads, runner.calls)
	}
}

func TestSessionStateThreadOnlyCandidateRequiresExactThreadRead(t *testing.T) {
	now := time.Date(2026, 8, 22, 1, 2, 3, 0, time.UTC)
	runner := &scriptedRunner{t: t, steps: []scriptedStep{
		{output: []byte("%1\x1f/work/app\x1f1\x1fcodex\x1f\x1fthread-candidate\x1f\x1f\n")},
		{output: []byte{}}, {output: []byte{}}, {output: []byte{}},
	}}
	var readID string
	client := NewClient(runner, WithCodexCatalogThreadReader(func(_ context.Context, threadID string) (codexappserver.CatalogThread, error) {
		readID = threadID
		return codexappserver.CatalogThread{ID: threadID, CWD: "/work/app", RuntimeStatus: "idle"}, nil
	}))
	if err := client.refreshSessionStateAIResumeMetadata(context.Background(), "workspace", now); err != nil {
		t.Fatal(err)
	}
	if readID != "thread-candidate" || len(runner.calls) != 4 ||
		runner.calls[1].args[len(runner.calls[1].args)-1] != "thread-candidate" ||
		runner.calls[2].args[len(runner.calls[2].args)-1] != "app-server" {
		t.Fatalf("readID=%q calls=%#v", readID, runner.calls)
	}
}

func TestSessionStateThreadReadNeverSwitchesCandidateIdentity(t *testing.T) {
	runner := &scriptedRunner{t: t, steps: []scriptedStep{{output: []byte("%1\x1f/work/app\x1f1\x1fcodex\x1f\x1fthread-candidate\x1f\x1f\n")}}}
	client := NewClient(runner, WithCodexCatalogThreadReader(func(_ context.Context, _ string) (codexappserver.CatalogThread, error) {
		return codexappserver.CatalogThread{ID: "thread-other", CWD: "/work/app", RuntimeStatus: "idle"}, nil
	}))
	if err := client.refreshSessionStateAIResumeMetadata(context.Background(), "workspace", time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("different read identity caused metadata writes: %#v", runner.calls)
	}
}

func TestSessionStateThreadReadFailureWritesZeroWhenRolloutHasNoCandidate(t *testing.T) {
	runner := &scriptedRunner{t: t, steps: []scriptedStep{{output: []byte("%1\x1f/work/missing\x1f1\x1fcodex\x1f\x1fthread-candidate\x1f\x1f\n")}}}
	reads := 0
	client := NewClient(runner,
		WithFileReader(func(string) ([]byte, error) { return nil, os.ErrNotExist }),
		WithCodexCatalogThreadReader(func(_ context.Context, threadID string) (codexappserver.CatalogThread, error) {
			reads++
			return codexappserver.CatalogThread{}, errors.New("app-server unavailable")
		}),
	)
	if err := client.refreshSessionStateAIResumeMetadata(context.Background(), "workspace", time.Now()); err != nil {
		t.Fatal(err)
	}
	if reads != 1 || len(runner.calls) != 1 {
		t.Fatalf("reads=%d calls=%#v, want one probe-only read and zero metadata writes", reads, runner.calls)
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
			"#{@projmux_ai_thread_id}",
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
			"#{@projmux_ai_thread_id}",
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
