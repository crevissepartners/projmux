package metadata

import (
	"testing"
)

func TestLegacyWindowNameSeedExcludesTopicsAndRawTitles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		window LegacyWindow
		want   string
	}{
		{
			name:   "automatic-rename off keeps the current window name",
			window: LegacyWindow{Name: "build", AutomaticRename: false, Panes: []LegacyPane{{Label: "editor"}}},
			want:   "build",
		},
		{
			name:   "automatic-rename on prefers the user pane label",
			window: LegacyWindow{Name: "zsh", AutomaticRename: true, Panes: []LegacyPane{{Label: "editor", Provider: "codex", Command: "zsh"}}},
			want:   "editor",
		},
		{
			name:   "automatic-rename on falls back to the provider",
			window: LegacyWindow{Name: "codex", AutomaticRename: true, Panes: []LegacyPane{{Provider: "Codex", Topic: "refactor", Command: "node"}}},
			want:   "codex",
		},
		{
			name:   "automatic-rename on falls back to a known shell",
			window: LegacyWindow{Name: "vim", AutomaticRename: true, Panes: []LegacyPane{{Command: "node"}, {Command: "fish"}}},
			want:   "fish",
		},
		{
			name:   "automatic-rename on ignores the topic and the raw title",
			window: LegacyWindow{Name: "x", AutomaticRename: true, Panes: []LegacyPane{{Topic: "refactor naming", Title: "~/src/projmux", Command: "node"}}},
			want:   "window",
		},
		{
			name:   "automatic-rename off with an empty name falls through",
			window: LegacyWindow{Name: "", AutomaticRename: false, Panes: []LegacyPane{{Command: "bash"}}},
			want:   "bash",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := LegacyWindowNameSeed(tt.window); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLegacyPaneNameSeedUsesTheExistingPaneLabelFirst(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		pane  LegacyPane
		shell string
		want  string
	}{
		{name: "existing pane label is the seed", pane: LegacyPane{Label: "logs", Command: "tail"}, shell: "/bin/zsh", want: "logs"},
		{name: "command basename is next", pane: LegacyPane{Command: "/usr/bin/tail -f"}, shell: "/bin/zsh", want: "tail"},
		{name: "configured shell basename is next", pane: LegacyPane{}, shell: "/bin/zsh", want: "zsh"},
		{name: "pane literal is the last resort", pane: LegacyPane{}, shell: "", want: "pane"},
		{name: "topic is never a pane name seed", pane: LegacyPane{Topic: "refactor"}, shell: "", want: "pane"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := LegacyPaneNameSeed(tt.pane, tt.shell); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLegacyImportBuildsResourcesAndMarksManagedWindowsForAutomaticRenameOff(t *testing.T) {
	t.Parallel()

	roots := dirSet{"/src/projmux": true}
	m := testMutator(roots)
	reg := NewRegistry()

	result, err := m.ImportLegacySession(&reg, LegacySession{
		Session: "projmux",
		Root:    "/src/projmux",
		Windows: []LegacyWindow{
			{
				Name:            "editor",
				AutomaticRename: false,
				Panes: []LegacyPane{
					{Label: "nvim", Command: "nvim", CWD: "/src/projmux"},
					{Provider: "codex", Topic: "refactor naming", Command: "codex", CWD: "/src/projmux"},
				},
			},
			{
				Name:            "zsh",
				AutomaticRename: true,
				Panes:           []LegacyPane{{Command: "zsh", Title: "~/src/projmux", CWD: "/src/projmux"}},
			},
		},
	}, "/bin/zsh", "op-import")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("imported registry is invalid: %v", err)
	}

	if result.Project.Metadata.Name != "projmux" {
		t.Fatalf("project name = %q", result.Project.Metadata.Name)
	}
	if result.Project.Status.Session == nil || result.Project.Status.Session.Name != "projmux" {
		t.Fatalf("session projection = %+v", result.Project.Status.Session)
	}

	var windowNames []string
	for _, window := range result.Windows {
		windowNames = append(windowNames, window.Name)
		if !window.NeedsAutomaticRenameOff {
			t.Fatalf("managed window %q must be marked for automatic-rename off", window.Name)
		}
	}
	if !equalStrings(windowNames, []string{"editor", "zsh"}) {
		t.Fatalf("window names = %v, want editor/zsh", windowNames)
	}

	var paneNames []string
	for _, pane := range result.Panes {
		paneNames = append(paneNames, pane.Name)
	}
	if !equalStrings(paneNames, []string{"nvim", "codex-pane", "zsh"}) {
		t.Fatalf("pane names = %v, want nvim/codex-pane/zsh", paneNames)
	}

	if len(result.Agents) != 1 || result.Agents[0].Name != "codex" {
		t.Fatalf("agents = %+v, want one codex agent", result.Agents)
	}
	agent, _ := reg.Agent(result.Agents[0].UID)
	if agent.Status.Phase != PhaseRunning || agent.Status.PaneRef != result.Agents[0].PaneUID {
		t.Fatalf("imported agent status = %+v", agent.Status)
	}
	if agent.Metadata.Annotations[AnnotationAgentTopic] != "refactor naming" {
		t.Fatalf("topic must land in annotations, not in the name: %+v", agent.Metadata.Annotations)
	}

	// The derived display title is secondary; the pane name stays primary.
	managed, _ := reg.Pane(result.Agents[0].PaneUID)
	if managed.Metadata.Name != "codex-pane" || managed.Status.DisplayTitle != "refactor naming" {
		t.Fatalf("managed pane = %q / %q", managed.Metadata.Name, managed.Status.DisplayTitle)
	}
	shellPane, _ := reg.Pane(result.Panes[2].UID)
	if shellPane.Status.DisplayTitle != "zsh" {
		t.Fatalf("shell pane display title = %q, want zsh", shellPane.Status.DisplayTitle)
	}
}

func TestDuplicateLegacyProjectAndAgentImportsGetTheLowestFreeSuffix(t *testing.T) {
	t.Parallel()

	roots := dirSet{"/a/projmux": true, "/b/projmux": true}
	m := testMutator(roots)
	reg := NewRegistry()

	agentWindow := LegacyWindow{
		Name:            "agents",
		AutomaticRename: false,
		Panes: []LegacyPane{
			{Provider: "codex", Command: "codex"},
			{Provider: "codex", Command: "codex"},
			{Provider: "claude", Command: "claude"},
			{Provider: "mystery", Command: "mystery"},
		},
	}

	first, err := m.ImportLegacySession(&reg, LegacySession{Session: "projmux", Root: "/a/projmux", Windows: []LegacyWindow{agentWindow}}, "/bin/zsh", "op-a")
	if err != nil {
		t.Fatalf("import a: %v", err)
	}
	second, err := m.ImportLegacySession(&reg, LegacySession{Session: "projmux-b", Root: "/b/projmux", Windows: []LegacyWindow{agentWindow}}, "/bin/zsh", "op-b")
	if err != nil {
		t.Fatalf("import b: %v", err)
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("registry invalid after duplicate imports: %v", err)
	}

	// Duplicate Project migration: same basename, different roots, distinct
	// uids and suffixed names.
	if first.Project.Metadata.UID == second.Project.Metadata.UID {
		t.Fatal("duplicate project migration must not merge uids")
	}
	if first.Project.Metadata.Name != "projmux" || second.Project.Metadata.Name != "projmux-1" {
		t.Fatalf("project names = %q/%q", first.Project.Metadata.Name, second.Project.Metadata.Name)
	}
	if first.Project.Metadata.DisplayName != second.Project.Metadata.DisplayName {
		t.Fatalf("display names must be allowed to duplicate: %q vs %q", first.Project.Metadata.DisplayName, second.Project.Metadata.DisplayName)
	}

	// Duplicate Agent migration: two codex agents in one window.
	var firstAgents []string
	for _, agent := range first.Agents {
		firstAgents = append(firstAgents, agent.Name)
	}
	if !equalStrings(firstAgents, []string{"codex", "codex-1", "claude", "agent"}) {
		t.Fatalf("agent names = %v, want codex/codex-1/claude/agent", firstAgents)
	}

	// Agent scope is the owning window, so the second project's window
	// restarts the suffix sequence.
	var secondAgents []string
	for _, agent := range second.Agents {
		secondAgents = append(secondAgents, agent.Name)
	}
	if !equalStrings(secondAgents, []string{"codex", "codex-1", "claude", "agent"}) {
		t.Fatalf("second window agent names = %v", secondAgents)
	}

	// Managed pane names follow their agent names.
	var paneNames []string
	for _, pane := range first.Panes {
		paneNames = append(paneNames, pane.Name)
	}
	if !equalStrings(paneNames, []string{"codex-pane", "codex-1-pane", "claude-pane", "agent-pane"}) {
		t.Fatalf("managed pane names = %v", paneNames)
	}
}

func TestReimportingAnExactRootReusesTheProjectUIDWithoutDuplicatingTopology(t *testing.T) {
	t.Parallel()

	roots := dirSet{"/src/projmux": true}
	m := testMutator(roots)
	reg := NewRegistry()
	legacy := LegacySession{
		Session: "projmux",
		Root:    "/src/projmux",
		Windows: []LegacyWindow{{Name: "editor", Panes: []LegacyPane{{Command: "nvim"}}}},
	}

	first, err := m.ImportLegacySession(&reg, legacy, "/bin/zsh", "op-1")
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	windowsAfterFirst := len(reg.Windows)

	second, err := m.ImportLegacySession(&reg, legacy, "/bin/zsh", "op-2")
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if !second.ProjectReused {
		t.Fatal("re-importing an exact root must reuse the project")
	}
	if second.Project.Metadata.UID != first.Project.Metadata.UID {
		t.Fatalf("uid changed on re-import: %q -> %q", first.Project.Metadata.UID, second.Project.Metadata.UID)
	}
	if len(reg.Windows) != windowsAfterFirst {
		t.Fatalf("re-import duplicated topology: %d -> %d windows", windowsAfterFirst, len(reg.Windows))
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("registry invalid after re-import: %v", err)
	}
}
