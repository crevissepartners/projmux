package metadata

import (
	"fmt"
	"testing"
)

// A legal shell-free Project must remain writable after deleting its primary.
// In particular, Offline process Agents still supply eligible Window anchors.
func TestDeletePrimaryWindowAgentAnchorFallback(t *testing.T) {
	m, reg, scaffold, _ := anchorWriterFixture(t)
	window, _ := reg.Window(scaffold)
	projectUID := window.Metadata.OwnerUID()
	if err := m.DeleteWindow(&reg, scaffold); err != nil {
		t.Fatal(err)
	}
	var windows []string
	for i := range 3 {
		name := fmt.Sprintf("agent-window-%d", i)
		w, agent, _, err := m.CreateProcessWindow(&reg, projectUID, BootstrapWindow{Name: name}, CreateAgentOptions{
			Name: name, Provider: "codex", Workspace: AgentWorkspace{CWD: "/src/projmux"}, OperationID: name,
		}, ProcessBinding{HostInstanceID: "fixture-host", Generation: name, OperationID: name})
		if err != nil {
			t.Fatal(err)
		}
		a, _ := reg.Agent(agent.Metadata.UID)
		a.Status.Phase = PhaseOffline
		windows = append(windows, w.Metadata.UID)
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("fixture must be valid before deletion: %v", err)
	}
	for i, uid := range windows {
		if err := m.DeleteWindow(&reg, uid); err != nil {
			t.Fatalf("delete primary %d: %v", i, err)
		}
		want := ""
		if i+1 < len(windows) {
			want = windows[i+1]
		}
		project, _ := reg.Project(projectUID)
		if err := reg.Validate(); err != nil {
			t.Fatalf("deleting primary %d must preserve a valid Registry: %v", i, err)
		}
		if got := project.Spec.PrimaryWindowRef; got != want {
			t.Fatalf("primary after deletion %d = %q, want %q", i, got, want)
		}
	}
}

func primaryFallbackAgentWindow(t *testing.T, m Mutator, reg *Registry, projectUID, name string, runtime RuntimeKind) string {
	t.Helper()
	if runtime == RuntimeProcess {
		window, agent, _, err := m.CreateProcessWindow(reg, projectUID, BootstrapWindow{Name: name}, CreateAgentOptions{
			Name: name, Provider: "codex", Workspace: AgentWorkspace{CWD: "/src/projmux"}, OperationID: name,
		}, ProcessBinding{HostInstanceID: "fixture-host", Generation: name, OperationID: name})
		if err != nil {
			t.Fatal(err)
		}
		a, _ := reg.Agent(agent.Metadata.UID)
		a.Status.Phase = PhaseOffline
		return window.Metadata.UID
	}
	window, panes, err := m.AddWindow(reg, projectUID, BootstrapWindow{Name: name}, "/bin/sh", name)
	if err != nil {
		t.Fatal(err)
	}
	attachFixtureAgent(t, m, reg, window.Metadata.UID, name+"-agent")
	for _, pane := range panes {
		if err := m.DeletePane(reg, pane.Metadata.UID); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := reg.WindowDefaultShell(window.Metadata.UID); ok {
		t.Fatal("tmux Agent fixture still has a shell")
	}
	return window.Metadata.UID
}

func requirePrimaryFallback(t *testing.T, reg Registry, projectUID, want string) {
	t.Helper()
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
	project, ok := reg.Project(projectUID)
	if !ok || project.Spec.PrimaryWindowRef != want {
		t.Fatalf("primary = %+v, want %q", project, want)
	}
	if want != "" {
		if _, ok := reg.WindowAnchor(want); !ok {
			t.Fatal("new primary has no eligible anchor")
		}
	}
}

func TestDeletePrimaryWindowAgentAnchorRuntimeAndNonPrimary(t *testing.T) {
	for _, runtime := range []RuntimeKind{RuntimeTmux, RuntimeProcess} {
		t.Run(string(runtime), func(t *testing.T) {
			m, reg, first, _ := anchorWriterFixture(t)
			w, _ := reg.Window(first)
			projectUID := w.Metadata.OwnerUID()
			second := primaryFallbackAgentWindow(t, m, &reg, projectUID, "second", runtime)
			third := primaryFallbackAgentWindow(t, m, &reg, projectUID, "third", runtime)
			fourth := primaryFallbackAgentWindow(t, m, &reg, projectUID, "fourth", runtime)
			requirePrimaryFallback(t, reg, projectUID, first)
			for _, deletion := range []struct{ target, primary string }{
				{third, first},  // non-primary deletion does not change primary
				{first, second}, // shell-free siblings retain insertion order
				{second, fourth},
				{fourth, ""},
			} {
				if err := m.DeleteWindow(&reg, deletion.target); err != nil {
					t.Fatal(err)
				}
				requirePrimaryFallback(t, reg, projectUID, deletion.primary)
			}
		})
	}
}

func TestDeletePrimaryWindowPreservesShellPreference(t *testing.T) {
	m, reg, first, _ := anchorWriterFixture(t)
	w, _ := reg.Window(first)
	projectUID := w.Metadata.OwnerUID()
	agent := primaryFallbackAgentWindow(t, m, &reg, projectUID, "earlier-agent", RuntimeProcess)
	var shells []string
	for _, name := range []string{"first-shell", "second-shell"} {
		window, _, err := m.AddWindow(&reg, projectUID, BootstrapWindow{Name: name}, "/bin/sh", name)
		if err != nil {
			t.Fatal(err)
		}
		shells = append(shells, window.Metadata.UID)
	}
	requirePrimaryFallback(t, reg, projectUID, first)
	for _, deletion := range []struct{ target, primary string }{
		{first, shells[0]},
		{shells[0], shells[1]},
		{shells[1], agent},
	} {
		if err := m.DeleteWindow(&reg, deletion.target); err != nil {
			t.Fatal(err)
		}
		requirePrimaryFallback(t, reg, projectUID, deletion.primary)
	}
}

func TestDeleteProjectWithAgentAnchorSiblings(t *testing.T) {
	m, reg, first, _ := anchorWriterFixture(t)
	w, _ := reg.Window(first)
	projectUID := w.Metadata.OwnerUID()
	primaryFallbackAgentWindow(t, m, &reg, projectUID, "process", RuntimeProcess)
	primaryFallbackAgentWindow(t, m, &reg, projectUID, "tmux", RuntimeTmux)
	requirePrimaryFallback(t, reg, projectUID, first)
	if err := m.DeleteProject(&reg, projectUID); err != nil {
		t.Fatal(err)
	}
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(reg.Projects)+len(reg.Windows)+len(reg.Agents)+len(reg.Panes)+len(reg.NameReservations) != 0 {
		t.Fatal("Project deletion left descendants or reservations")
	}
}

func TestDeletePrimaryWindowFallbackStaysInProject(t *testing.T) {
	m, reg, first, _ := anchorWriterFixture(t)
	w, _ := reg.Window(first)
	projectUID := w.Metadata.OwnerUID()
	m.DirExists = func(path string) (bool, error) { return path == "/src/projmux" || path == "/src/other", nil }
	other, err := registerFixture(m, &reg, "/src/other")
	if err != nil {
		t.Fatal(err)
	}
	foreign := primaryFallbackAgentWindow(t, m, &reg, other.Project.Metadata.UID, "foreign", RuntimeProcess)
	if err := m.DeleteWindow(&reg, other.Windows[0].Metadata.UID); err != nil {
		t.Fatal(err)
	}
	own := primaryFallbackAgentWindow(t, m, &reg, projectUID, "own", RuntimeProcess)
	requirePrimaryFallback(t, reg, projectUID, first)
	if err := m.DeleteWindow(&reg, first); err != nil {
		t.Fatal(err)
	}
	requirePrimaryFallback(t, reg, projectUID, own)
	requirePrimaryFallback(t, reg, other.Project.Metadata.UID, foreign)
}
