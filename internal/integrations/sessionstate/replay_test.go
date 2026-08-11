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

func TestReplayDoesNotMutateCapturedPaneLabel(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	snap := replaySnapshot(cwd)
	snap.Windows = []Window{{
		Index: 0, Name: "main", ActivePaneIndex: 0,
		Panes: []Pane{{Index: 0, Label: "phase-one-captured-label", Title: "raw title", CWD: cwd, Recipe: ShellRecipe()}},
	}}
	runner := &recordingReplayRunner{}
	if _, err := Replay(context.Background(), runner, snap, ReplayOptions{}); err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	for _, command := range runner.commands {
		joined := strings.Join(command.args, "\x00")
		if strings.Contains(joined, "@projmux_pane_label") || strings.Contains(joined, "phase-one-captured-label") {
			t.Fatalf("Replay() command %#v mutates/reuses captured label; Phase 0 is capture-only", command)
		}
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

func TestReplayAgentDirectStartEmitsNoSendKeys(t *testing.T) {
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
				{Index: 1, CWD: cwd, Recipe: AgentRecipe("codex", "01973f21-abc", "topic")},
			},
		},
	}
	runner := &recordingReplayRunner{}

	result, err := Replay(context.Background(), runner, snap, replayAgentOptions("/opt/codex/bin/codex"))
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("Replay() warnings = %#v, want none", result.Warnings)
	}

	for _, command := range runner.commands {
		if len(command.args) > 0 && command.args[0] == "send-keys" {
			t.Fatalf("Replay() sent agent resume through send-keys: %#v", command)
		}
	}
}

func TestReplayAgentDirectStartFirstWindowFirstPaneUsesNewSessionTail(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	agentBin := "/opt/claude/bin/claude"
	snap := replaySnapshot(cwd)
	snap.Windows = []Window{
		{
			Index:           0,
			Name:            "agent",
			ActivePaneIndex: 0,
			Panes: []Pane{
				{Index: 0, CWD: cwd, Recipe: AgentRecipe("claude", "abcdef-1234", "restore topic")},
			},
		},
	}
	runner := &recordingReplayRunner{}

	result, err := Replay(context.Background(), runner, snap, replayAgentOptions(agentBin))
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("Replay() warnings = %#v, want none", result.Warnings)
	}

	want := append([]string{"new-session", "-d", "-s", "home", "-c", cwd}, replayAgentTail("claude", agentBin, cwd, "restore topic", "--resume", "abcdef-1234")...)
	if got := runner.commands[0].args; !reflect.DeepEqual(got, want) {
		t.Fatalf("new-session args = %#v, want %#v", got, want)
	}
}

func TestReplayAgentDirectStartLaterWindowFirstPaneUsesNewWindowTail(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	agentBin := "/opt/codex/bin/codex"
	snap := replaySnapshot(cwd)
	snap.Windows = []Window{
		{
			Index:           0,
			Name:            "main",
			ActivePaneIndex: 0,
			Panes: []Pane{
				{Index: 0, CWD: cwd, Recipe: ShellRecipe()},
			},
		},
		{
			Index:           1,
			Name:            "agent",
			ActivePaneIndex: 0,
			Panes: []Pane{
				{Index: 0, CWD: cwd, Recipe: AgentRecipe("codex", "01973f21-abc", "codex topic")},
			},
		},
	}
	runner := &recordingReplayRunner{}

	result, err := Replay(context.Background(), runner, snap, replayAgentOptions(agentBin))
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("Replay() warnings = %#v, want none", result.Warnings)
	}

	want := append([]string{"new-window", "-d", "-t", "home:1", "-c", cwd, "-n", "agent"}, replayAgentTail("codex", agentBin, cwd, "codex topic", "resume", "01973f21-abc")...)
	if !hasReplayCommand(runner.commands, want) {
		t.Fatalf("commands = %#v, want new-window direct-start tail %#v", runner.commands, want)
	}
}

func TestReplayAgentDirectStartSplitPaneUsesSplitWindowTail(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	agentBin := "/opt/codex/bin/codex"
	snap := replaySnapshot(cwd)
	snap.Windows = []Window{
		{
			Index:           0,
			Name:            "main",
			ActivePaneIndex: 1,
			Panes: []Pane{
				{Index: 0, CWD: cwd, Recipe: ShellRecipe()},
				{Index: 1, CWD: cwd, Recipe: AgentRecipe("codex", "01973f21-abc", "split topic")},
			},
		},
	}
	runner := &recordingReplayRunner{}

	result, err := Replay(context.Background(), runner, snap, replayAgentOptions(agentBin))
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("Replay() warnings = %#v, want none", result.Warnings)
	}

	want := append([]string{"split-window", "-d", "-t", "home:0.0", "-c", cwd}, replayAgentTail("codex", agentBin, cwd, "split topic", "resume", "01973f21-abc")...)
	if !hasReplayCommand(runner.commands, want) {
		t.Fatalf("commands = %#v, want split-window direct-start tail %#v", runner.commands, want)
	}
}

func TestReplayAntigravityAgentDirectStartUsesConversation(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	agentBin := "/opt/antigravity/bin/agy"
	conversationID := "123e4567-e89b-12d3-a456-426614174000"
	snap := replaySnapshot(cwd)
	snap.Windows = []Window{
		{
			Index:           0,
			Name:            "agent",
			ActivePaneIndex: 0,
			Panes: []Pane{
				{Index: 0, CWD: cwd, Recipe: AgentRecipe("antigravity", conversationID, "antigravity topic")},
			},
		},
	}
	runner := &recordingReplayRunner{}

	result, err := Replay(context.Background(), runner, snap, replayAgentOptions(agentBin))
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("Replay() warnings = %#v, want none", result.Warnings)
	}

	want := append([]string{"new-session", "-d", "-s", "home", "-c", cwd}, replayAgentTail("antigravity", agentBin, cwd, "antigravity topic", "--conversation", conversationID)...)
	if got := runner.commands[0].args; !reflect.DeepEqual(got, want) {
		t.Fatalf("new-session args = %#v, want %#v", got, want)
	}
	for _, command := range runner.commands {
		if len(command.args) > 0 && command.args[0] == "send-keys" {
			t.Fatalf("Replay() sent Antigravity resume through send-keys: %#v", command)
		}
	}
}

func TestReplayRunsStartupRecipeAfterLayoutAndPaneSelection(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	snap := replaySnapshot(cwd)
	snap.Windows = []Window{
		{
			Index:           1,
			Name:            "jobs",
			ActivePaneIndex: 0,
			Panes: []Pane{
				{Index: 0, CWD: cwd, Recipe: StartupRecipe("npm run jobs")},
			},
		},
		{
			Index:           0,
			Name:            "main",
			Layout:          "d3a9,120x36,0,0{60x36,0,0,1,59x36,61,0,2}",
			ActivePaneIndex: 1,
			Panes: []Pane{
				{Index: 0, CWD: cwd, Recipe: ShellRecipe()},
				{Index: 1, CWD: cwd, Recipe: StartupRecipe("npm run dev")},
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
	selectIndex := replayCommandIndex(runner.commands, []string{"select-pane", "-t", "home:0.1"})
	sendIndex := replayCommandIndex(runner.commands, []string{"send-keys", "-t", "home:0.1", "npm run dev", "Enter"})
	if layoutIndex < 0 || selectIndex < 0 || sendIndex < 0 {
		t.Fatalf("commands = %#v, want layout, active pane select, and startup send-keys", runner.commands)
	}
	if !(layoutIndex < selectIndex && selectIndex < sendIndex) {
		t.Fatalf("command order layout=%d select=%d send=%d, want startup after layout and active pane selection", layoutIndex, selectIndex, sendIndex)
	}
}

func TestReplayRunsMultipleStartupRecipesInWindowPaneOrder(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	snap := replaySnapshot(cwd)
	snap.Windows = []Window{
		{
			Index:           1,
			Name:            "jobs",
			ActivePaneIndex: 0,
			Panes: []Pane{
				{Index: 0, CWD: cwd, Recipe: StartupRecipe("npm run jobs")},
			},
		},
		{
			Index:           0,
			Name:            "main",
			ActivePaneIndex: 1,
			Panes: []Pane{
				{Index: 2, CWD: cwd, Recipe: StartupRecipe("npm run worker")},
				{Index: 0, CWD: cwd, Recipe: StartupRecipe("npm run api")},
				{Index: 1, CWD: cwd, Recipe: StartupRecipe("npm run web")},
			},
		},
	}
	runner := &recordingReplayRunner{}

	if _, err := Replay(context.Background(), runner, snap, ReplayOptions{}); err != nil {
		t.Fatalf("Replay() error = %v", err)
	}

	want := [][]string{
		{"send-keys", "-t", "home:0.0", "npm run api", "Enter"},
		{"send-keys", "-t", "home:0.1", "npm run web", "Enter"},
		{"send-keys", "-t", "home:0.2", "npm run worker", "Enter"},
		{"send-keys", "-t", "home:1.0", "npm run jobs", "Enter"},
	}
	lastIndex := -1
	for _, args := range want {
		index := replayCommandIndex(runner.commands, args)
		if index < 0 {
			t.Fatalf("commands = %#v, want startup command %#v", runner.commands, args)
		}
		if index <= lastIndex {
			t.Fatalf("startup command order regressed: command %#v at %d after %d", args, index, lastIndex)
		}
		lastIndex = index
	}
}

func TestReplayDoesNotSendKeysForShellRecipe(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	snap := replaySnapshot(cwd)
	snap.Windows = []Window{
		{
			Index:           0,
			Name:            "main",
			ActivePaneIndex: 0,
			Panes: []Pane{
				{Index: 0, CWD: cwd, Recipe: ShellRecipe()},
			},
		},
	}
	runner := &recordingReplayRunner{}

	if _, err := Replay(context.Background(), runner, snap, ReplayOptions{}); err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	for _, command := range runner.commands {
		if len(command.args) > 0 && command.args[0] == "send-keys" {
			t.Fatalf("Replay() sent shell pane command: %#v", command)
		}
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
	assertAgentFallbackShellPane(t, runner.commands, []string{"new-session", "-d", "-s", "home", "-c", cwd})
	for _, command := range runner.commands {
		if len(command.args) > 0 && command.args[0] == "send-keys" {
			t.Fatalf("Replay() sent unsafe resume command: %#v", command)
		}
	}
}

func TestReplaySkipsCodexResumeWithInvalidResumeID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		resumeID string
	}{
		{name: "blank", resumeID: ""},
		{name: "control", resumeID: "abc\ndef"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cwd := t.TempDir()
			snap := replaySnapshot(cwd)
			snap.Windows = []Window{
				{
					Index:           0,
					Name:            "agent",
					ActivePaneIndex: 0,
					Panes: []Pane{
						{Index: 0, CWD: cwd, Recipe: AgentRecipe("codex", tc.resumeID, "topic")},
					},
				},
			}
			runner := &recordingReplayRunner{}

			result, err := Replay(context.Background(), runner, snap, ReplayOptions{})
			if err != nil {
				t.Fatalf("Replay() error = %v", err)
			}
			if len(result.Warnings) != 1 {
				t.Fatalf("warnings = %#v, want one invalid resume warning", result.Warnings)
			}
			if result.Warnings[0].Scope != "agent" || !strings.Contains(result.Warnings[0].Reason, "invalid codex resume id") {
				t.Fatalf("warning = %#v, want invalid codex resume id warning", result.Warnings[0])
			}
			assertAgentFallbackShellPane(t, runner.commands, []string{"new-session", "-d", "-s", "home", "-c", cwd})
			for _, command := range runner.commands {
				if len(command.args) > 0 && command.args[0] == "send-keys" {
					t.Fatalf("Replay() sent unsafe resume command: %#v", command)
				}
			}
		})
	}
}

func TestReplaySkipsAntigravityResumeWithInvalidConversationID(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	snap := replaySnapshot(cwd)
	snap.Windows = []Window{
		{
			Index:           0,
			Name:            "agent",
			ActivePaneIndex: 0,
			Panes: []Pane{
				{Index: 0, CWD: cwd, Recipe: AgentRecipe("antigravity", "ag-conv-123", "topic")},
			},
		},
	}
	runner := &recordingReplayRunner{}

	result, err := Replay(context.Background(), runner, snap, ReplayOptions{})
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	if len(result.Warnings) != 1 {
		t.Fatalf("warnings = %#v, want one invalid resume warning", result.Warnings)
	}
	if result.Warnings[0].Scope != "agent" || !strings.Contains(result.Warnings[0].Reason, "invalid antigravity resume id") {
		t.Fatalf("warning = %#v, want invalid antigravity resume id warning", result.Warnings[0])
	}
	assertAgentFallbackShellPane(t, runner.commands, []string{"new-session", "-d", "-s", "home", "-c", cwd})
	for _, command := range runner.commands {
		if len(command.args) > 0 && command.args[0] == "send-keys" {
			t.Fatalf("Replay() sent unsafe Antigravity resume command: %#v", command)
		}
	}
}

func TestReplaySkipsAgentDirectStartWithMissingBinary(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	snap := replaySnapshot(cwd)
	snap.Windows = []Window{
		{
			Index:           0,
			Name:            "agent",
			ActivePaneIndex: 0,
			Panes: []Pane{
				{Index: 0, CWD: cwd, Recipe: AgentRecipe("codex", "01973f21-abc", "topic")},
			},
		},
	}
	runner := &recordingReplayRunner{}

	result, err := Replay(context.Background(), runner, snap, ReplayOptions{
		AgentBinaryResolver: func(string) string { return "" },
		CommandShell:        "/bin/bash",
	})
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	if len(result.Warnings) != 1 {
		t.Fatalf("warnings = %#v, want one missing binary warning", result.Warnings)
	}
	if result.Warnings[0].Scope != "agent" || !strings.Contains(result.Warnings[0].Reason, "missing codex binary") {
		t.Fatalf("warning = %#v, want missing codex binary warning", result.Warnings[0])
	}
	assertAgentFallbackShellPane(t, runner.commands, []string{"new-session", "-d", "-s", "home", "-c", cwd})
	for _, command := range runner.commands {
		if len(command.args) > 0 && command.args[0] == "send-keys" {
			t.Fatalf("Replay() sent missing-binary resume command: %#v", command)
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
				{Index: 0, CWD: cwd, Recipe: AgentRecipe("unsupported-agent", "abcdef-1234", "topic")},
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
	outputs  map[string]string
}

func (r *recordingReplayRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.commands = append(r.commands, replayCommand{
		name: name,
		args: append([]string(nil), args...),
	})
	if output, ok := r.outputs[replayOutputKey(name, args...)]; ok {
		return []byte(output), nil
	}
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

func replayAgentOptions(agentBin string) ReplayOptions {
	return ReplayOptions{
		AgentBinaryResolver: func(string) string { return agentBin },
		CommandShell:        "/bin/bash",
	}
}

func replayAgentTail(agent, agentBin, cwd, title string, argv ...string) []string {
	titleVar := "__" + agent + "_title"
	command := strings.Join([]string{
		"export PATH=" + shellQuote(agentBinDir(agentBin)) + `":$PATH"`,
		"cd " + shellQuote(cwd),
		titleVar + "=" + shellQuote(title),
		`printf '\033]0;%s\007' "$` + titleVar + `"`,
		`if [ -n "${TMUX:-}" ]; then tmux select-pane -T "$` + titleVar + `" >/dev/null 2>&1 || true; fi`,
		"exec " + shellJoin(append([]string{agentBin}, argv...)),
	}, " && ")
	return []string{"/bin/bash", "-lc", command}
}

func agentBinDir(agentBin string) string {
	index := strings.LastIndex(agentBin, "/")
	if index < 0 {
		return "."
	}
	if index == 0 {
		return "/"
	}
	return agentBin[:index]
}

func assertAgentFallbackShellPane(t *testing.T, commands []replayCommand, createArgs []string) {
	t.Helper()
	if !hasReplayCommand(commands, createArgs) {
		t.Fatalf("commands = %#v, want shell fallback create command %#v", commands, createArgs)
	}
	for _, command := range commands {
		if len(command.args) >= len(createArgs)+3 &&
			reflect.DeepEqual(command.args[:len(createArgs)], createArgs) &&
			(command.args[len(createArgs)] == "/bin/bash" || strings.HasSuffix(command.args[len(createArgs)], "/sh")) &&
			command.args[len(createArgs)+1] == "-lc" {
			t.Fatalf("commands = %#v, want shell fallback without launch tail", commands)
		}
	}
}

func replayOutputKey(name string, args ...string) string {
	return strings.Join(append([]string{name}, args...), "\x00")
}
