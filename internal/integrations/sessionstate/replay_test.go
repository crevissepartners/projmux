package sessionstate

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestReplaySingleWindowSinglePane(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	snap := replaySnapshot(cwd)
	snap.Windows = []Window{
		{
			Index:           0,
			Name:            "main",
			Layout:          "b25d,120x36,0,0,1",
			ActivePaneIndex: 0,
			Panes: []Pane{
				{Index: 0, CWD: cwd, Recipe: ShellRecipe()},
			},
		},
	}
	runner := &recordingReplayRunner{}

	result, err := Replay(context.Background(), runner, snap, ReplayOptions{})
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("Replay() warnings = %#v, want none", result.Warnings)
	}

	want := []replayCommand{
		{name: "tmux", args: []string{"new-session", "-d", "-s", "home", "-c", cwd}},
		{name: "tmux", args: []string{"rename-window", "-t", "home:0", "main"}},
		{name: "tmux", args: []string{"select-layout", "-t", "home:0", "b25d,120x36,0,0,1"}},
		{name: "tmux", args: []string{"select-pane", "-t", "home:0.0"}},
	}
	if !reflect.DeepEqual(runner.commands, want) {
		t.Fatalf("commands = %#v, want %#v", runner.commands, want)
	}
}

func TestReplaySplitPaneLayoutCommandOrder(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	second := t.TempDir()
	third := t.TempDir()
	snap := replaySnapshot(cwd)
	snap.Windows = []Window{
		{
			Index:           0,
			Name:            "main",
			Layout:          "d3a9,120x36,0,0{60x36,0,0,1,59x36,61,0,2}",
			ActivePaneIndex: 2,
			Panes: []Pane{
				{Index: 0, CWD: cwd, Recipe: ShellRecipe()},
				{Index: 1, CWD: second, Recipe: ShellRecipe()},
				{Index: 2, CWD: third, Recipe: ShellRecipe()},
			},
		},
		{
			Index:           1,
			Name:            "logs",
			Layout:          "a111,100x20,0,0,3",
			ActivePaneIndex: 0,
			Panes: []Pane{
				{Index: 0, CWD: second, Recipe: ShellRecipe()},
			},
		},
	}
	runner := &recordingReplayRunner{}

	if _, err := Replay(context.Background(), runner, snap, ReplayOptions{}); err != nil {
		t.Fatalf("Replay() error = %v", err)
	}

	want := []replayCommand{
		{name: "tmux", args: []string{"new-session", "-d", "-s", "home", "-c", cwd}},
		{name: "tmux", args: []string{"rename-window", "-t", "home:0", "main"}},
		{name: "tmux", args: []string{"split-window", "-d", "-t", "home:0.0", "-c", second}},
		{name: "tmux", args: []string{"split-window", "-d", "-t", "home:0.1", "-c", third}},
		{name: "tmux", args: []string{"select-layout", "-t", "home:0", "d3a9,120x36,0,0{60x36,0,0,1,59x36,61,0,2}"}},
		{name: "tmux", args: []string{"select-pane", "-t", "home:0.2"}},
		{name: "tmux", args: []string{"new-window", "-d", "-t", "home:1", "-c", second, "-n", "logs"}},
		{name: "tmux", args: []string{"select-layout", "-t", "home:1", "a111,100x20,0,0,3"}},
		{name: "tmux", args: []string{"select-pane", "-t", "home:1.0"}},
	}
	if !reflect.DeepEqual(runner.commands, want) {
		t.Fatalf("commands = %#v, want %#v", runner.commands, want)
	}
}

func TestReplayMissingPaneCWDFallsBackWithWarning(t *testing.T) {
	t.Parallel()

	defaultCWD := t.TempDir()
	fallback := t.TempDir()
	missing := defaultCWD + "/missing"
	snap := replaySnapshot(defaultCWD)
	snap.Windows = []Window{
		{
			Index:           0,
			Name:            "main",
			ActivePaneIndex: 1,
			Panes: []Pane{
				{Index: 0, CWD: defaultCWD, Recipe: ShellRecipe()},
				{Index: 1, CWD: missing, Recipe: ShellRecipe()},
			},
		},
	}
	runner := &recordingReplayRunner{}

	result, err := Replay(context.Background(), runner, snap, ReplayOptions{FallbackCWD: fallback})
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	if len(result.Warnings) != 1 {
		t.Fatalf("warnings = %#v, want one warning", result.Warnings)
	}
	warning := result.Warnings[0]
	if warning.Scope != "pane" || warning.WindowIndex != 0 || warning.PaneIndex != 1 || warning.CWD != missing || warning.FallbackCWD != fallback {
		t.Fatalf("warning = %#v, want pane cwd fallback to supplied dir", warning)
	}
	if !hasReplayCommand(runner.commands, []string{"split-window", "-d", "-t", "home:0.0", "-c", fallback}) {
		t.Fatalf("commands = %#v, want split-window using fallback cwd", runner.commands)
	}
}

func TestReplayActivePaneSelection(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	snap := replaySnapshot(cwd)
	snap.Windows = []Window{
		{
			Index:           0,
			Name:            "work",
			ActivePaneIndex: 1,
			Panes: []Pane{
				{Index: 0, CWD: cwd, Recipe: ShellRecipe()},
				{Index: 1, CWD: cwd, Recipe: ShellRecipe()},
			},
		},
	}
	runner := &recordingReplayRunner{}

	if _, err := Replay(context.Background(), runner, snap, ReplayOptions{}); err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	if got := runner.commands[len(runner.commands)-1].args; !reflect.DeepEqual(got, []string{"select-pane", "-t", "home:0.1"}) {
		t.Fatalf("last command args = %#v, want active pane selection", got)
	}
}

func TestReplayResumesClaudeAgentRecipeAfterLayoutAndPaneSelection(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	snap := replaySnapshot(cwd)
	snap.Windows = []Window{
		{
			Index:           0,
			Name:            "agent",
			Layout:          "d3a9,120x36,0,0{60x36,0,0,1,59x36,61,0,2}",
			ActivePaneIndex: 1,
			Panes: []Pane{
				{Index: 0, CWD: cwd, Recipe: ShellRecipe()},
				{Index: 1, CWD: cwd, Recipe: AgentRecipe("claude", "abcdef-1234", "topic")},
			},
		},
	}
	runner := &recordingReplayRunner{}

	result, err := Replay(context.Background(), runner, snap, ReplayOptions{})
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("Replay() warnings = %#v, want none", result.Warnings)
	}

	layoutIndex := replayCommandIndex(runner.commands, []string{"select-layout", "-t", "home:0", "d3a9,120x36,0,0{60x36,0,0,1,59x36,61,0,2}"})
	sendIndex := replayCommandIndex(runner.commands, []string{"send-keys", "-t", "home:0.1", "claude --resume abcdef-1234", "Enter"})
	selectIndex := replayCommandIndex(runner.commands, []string{"select-pane", "-t", "home:0.1"})
	if layoutIndex < 0 || sendIndex < 0 || selectIndex < 0 {
		t.Fatalf("commands = %#v, want layout, claude resume, and active pane select", runner.commands)
	}
	if !(layoutIndex < selectIndex && selectIndex < sendIndex) {
		t.Fatalf("command order layout=%d select=%d send=%d, want resume after layout and active pane selection", layoutIndex, selectIndex, sendIndex)
	}
}

func TestReplaySkipsClaudeResumeWithEmptyResumeID(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	snap := replaySnapshot(cwd)
	snap.Windows = []Window{
		{
			Index:           0,
			Name:            "agent",
			ActivePaneIndex: 0,
			Panes: []Pane{
				{Index: 0, CWD: cwd, Recipe: AgentRecipe("claude", "", "topic")},
			},
		},
	}
	runner := &recordingReplayRunner{}

	result, err := Replay(context.Background(), runner, snap, ReplayOptions{})
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	if len(result.Warnings) != 1 {
		t.Fatalf("warnings = %#v, want one empty resume warning", result.Warnings)
	}
	if result.Warnings[0].Scope != "agent" || !strings.Contains(result.Warnings[0].Reason, "invalid claude resume id") {
		t.Fatalf("warning = %#v, want empty claude resume id warning", result.Warnings[0])
	}
	for _, command := range runner.commands {
		if len(command.args) > 0 && command.args[0] == "send-keys" {
			t.Fatalf("Replay() sent unsafe resume command: %#v", command)
		}
	}
}

func TestReplayDoesNotResumeUnsupportedAgent(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	snap := replaySnapshot(cwd)
	snap.Windows = []Window{
		{
			Index:           0,
			Name:            "agent",
			ActivePaneIndex: 0,
			Panes: []Pane{
				{Index: 0, CWD: cwd, Recipe: AgentRecipe("codex", "abcdef-1234", "topic")},
			},
		},
	}
	runner := &recordingReplayRunner{}

	result, err := Replay(context.Background(), runner, snap, ReplayOptions{})
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("warnings = %#v, want none", result.Warnings)
	}
	for _, command := range runner.commands {
		if len(command.args) > 0 && command.args[0] == "send-keys" {
			t.Fatalf("Replay() resumed unsupported agent: %#v", command)
		}
	}
}

func replaySnapshot(defaultCWD string) Snapshot {
	return Snapshot{
		Version:    Version,
		Session:    "home",
		DefaultCWD: defaultCWD,
		SavedAt:    time.Date(2026, 5, 11, 12, 34, 56, 0, time.UTC),
	}
}

type replayCommand struct {
	name string
	args []string
}

type recordingReplayRunner struct {
	commands []replayCommand
}

func (r *recordingReplayRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.commands = append(r.commands, replayCommand{
		name: name,
		args: append([]string(nil), args...),
	})
	return nil, nil
}

func hasReplayCommand(commands []replayCommand, args []string) bool {
	return replayCommandIndex(commands, args) >= 0
}

func replayCommandIndex(commands []replayCommand, args []string) int {
	for i, command := range commands {
		if reflect.DeepEqual(command.args, args) {
			return i
		}
	}
	return -1
}
