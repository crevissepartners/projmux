package tmux

import (
	"context"
	"errors"
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
					"2\x1f0\x1fcodex task\x1f1\x1f/home/tester/app\x1f\x1f\x1f1\x1fcodex\x1fsession state\x1f01973f21-abc\n" +
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
					{Index: 0, Title: "codex task", CWD: "/home/tester/app", Recipe: sessionstate.AgentRecipe("codex", "01973f21-abc", "session state")},
					{Index: 1, Title: "claude task", CWD: "/home/tester/app", Recipe: sessionstate.ShellRecipe()},
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
