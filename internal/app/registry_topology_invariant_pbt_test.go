package app

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
)

// The openability property.
//
// `Registry.Validate` is the one gate every write passes, and
// `planRegistryTopology` is what turns a stored Project back into a running one.
// The two do not share a precondition: Validate accepts a Window with no
// Window-owned shell Pane, and the planner cannot build one. That difference is
// reachable through ordinary use, so the property that has to hold is not "the
// planner refuses nothing" -- it is that no validate-clean Registry can make the
// Project itself unopenable. An item the planner cannot build is one item; the
// Project keeps opening with everything else, and the skipped item is disclosed.
//
// The tests below state that relation over states the product itself produces.
// They drive the shipped Mutator rather than hand-building structs, because the
// question is not "can an invalid Registry be constructed" -- it is "does the
// product write Registries its own materializer cannot open".

// pbtMutator is a fully deterministic Mutator over one existing root.
func pbtMutator(root string) coremetadata.Mutator {
	counters := map[coremetadata.Kind]int{}
	clock := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	return coremetadata.Mutator{
		Now: func() time.Time {
			clock = clock.Add(time.Second)
			return clock
		},
		NewUID: func(kind coremetadata.Kind) (string, error) {
			counters[kind]++
			return fmt.Sprintf("%s-pbt%03d", strings.ToLower(string(kind)), counters[kind]), nil
		},
		DirExists: func(path string) (bool, error) {
			return strings.TrimSpace(path) == root, nil
		},
	}
}

// pbtProject registers one Project whose root exists, binds its session name, and
// returns the registry plus the exact selector the materialize routes take.
func pbtProject(t *testing.T, root string) (*coremetadata.Registry, coremetadata.Project, coremetadata.Mutator) {
	t.Helper()
	mutator := pbtMutator(root)
	registry := coremetadata.NewRegistry()
	result, err := mutator.RegisterProject(&registry, coremetadata.RegisterProjectOptions{Root: root, OperationID: "pbt-register"})
	if err != nil {
		t.Fatalf("register project: %v", err)
	}
	if _, err := mutator.BindProjectSession(&registry, result.Project.Metadata.UID, "pbt-session", false); err != nil {
		t.Fatalf("bind project session: %v", err)
	}
	project, ok := registry.Project(result.Project.Metadata.UID)
	if !ok {
		t.Fatalf("registered Project disappeared")
	}
	return &registry, *project, mutator
}

// pbtPlan runs the offline materialize planner the closed-Project startup path
// runs. No live session is observed, so the plan is the pure desired-topology
// projection and the runner is never called.
func pbtPlan(t *testing.T, registry coremetadata.Registry, project coremetadata.Project) *registryTopologyPlan {
	t.Helper()
	target, err := tmuxSocketNameTarget(defaultAppSocket)
	if err != nil {
		t.Fatalf("socket target: %v", err)
	}
	plan, err := planRegistryTopology(context.Background(), nil, registry,
		selector.UIDPrefix+project.Metadata.UID, nil, nil, target, nil)
	if err != nil {
		t.Fatalf("plan registry topology: %v", err)
	}
	if plan == nil {
		t.Fatalf("planner returned no plan for an existing Project")
	}
	return plan
}

// pbtRender renders refused items so a failure names the exact state.
func pbtRender(items []resourceReconcileItem) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, fmt.Sprintf("%s %s: %s", item.Kind, item.Target, item.Reason))
	}
	return out
}

// pbtPlannedWindows names the Windows the activation will actually build.
func pbtPlannedWindows(plan *registryTopologyPlan) []string {
	out := make([]string, 0, len(plan.windows))
	for _, work := range plan.windows {
		out = append(out, work.window.Metadata.Name)
	}
	return out
}

// pbtRegistryShape renders the ownership shape of one Project for failure output.
func pbtRegistryShape(registry coremetadata.Registry, project coremetadata.Project) string {
	var b strings.Builder
	for _, window := range registry.WindowsOf(project.Metadata.UID) {
		fmt.Fprintf(&b, "\n  window %s anchorPaneRef=%q defaultShellPaneRef=%q", window.Metadata.Name, window.Spec.AnchorPaneRef, window.Spec.DefaultShellPaneRef)
		for _, pane := range registry.PanesOf(window.Metadata.UID) {
			fmt.Fprintf(&b, "\n    pane %s role=%s owner=window", pane.Metadata.Name, pane.Spec.Role)
		}
		for _, agent := range registry.AgentsOf(window.Metadata.UID) {
			fmt.Fprintf(&b, "\n    agent %s phase=%s", agent.Metadata.Name, agent.Status.Phase)
			for _, pane := range registry.PanesOf(agent.Metadata.UID) {
				fmt.Fprintf(&b, "\n      pane %s role=%s owner=agent", pane.Metadata.Name, pane.Spec.Role)
			}
		}
	}
	return b.String()
}

// TestTopologyPlanKeepsProjectOpenableWithAgentOnlyWindow pins the state an
// operator reaches by running an Agent in a Window and closing that Window's
// shell. The shell Pane is gone, deletePane promotes the Agent's managed Pane to
// default shell ref, Validate accepts the Agent anchor, and the planner cannot build a
// Window from an Agent-owned Pane. The Project still has to open, and the Window
// it could not build has to be disclosed rather than dropped.
func TestTopologyPlanKeepsProjectOpenableWithAgentOnlyWindow(t *testing.T) {
	root := t.TempDir()
	registry, project, mutator := pbtProject(t, root)

	window := registry.WindowsOf(project.Metadata.UID)[0]
	shellPane := registry.PanesOf(window.Metadata.UID)[0]
	agent, err := mutator.CreateAgent(registry, window.Metadata.UID, coremetadata.CreateAgentOptions{
		Provider:    "codex",
		OperationID: "pbt-agent",
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if _, err := mutator.AttachAgentPane(registry, agent.Metadata.UID, coremetadata.BootstrapPane{}, "pbt-attach"); err != nil {
		t.Fatalf("attach agent pane: %v", err)
	}
	if err := mutator.DeletePane(registry, shellPane.Metadata.UID); err != nil {
		t.Fatalf("delete shell pane: %v", err)
	}
	healthy, _, err := mutator.AddWindow(registry, project.Metadata.UID, coremetadata.BootstrapWindow{Name: "healthy"}, "sh", "pbt-window")
	if err != nil {
		t.Fatalf("add healthy window: %v", err)
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("registry the product wrote is invalid: %v", err)
	}

	plan := pbtPlan(t, *registry, project)
	fatal, skipped := plan.refusalScope()
	if len(fatal) != 0 {
		t.Fatalf("valid Registry made the Project unopenable: %v\nshape:%s", pbtRender(fatal), pbtRegistryShape(*registry, project))
	}
	if planned := pbtPlannedWindows(plan); !slices.Contains(planned, healthy.Metadata.Name) {
		t.Fatalf("planned windows = %v, want the healthy Window %q to still be built", planned, healthy.Metadata.Name)
	}
	if len(skipped) != 0 {
		t.Fatalf("last-shell repair left materialize skips: %v", pbtRender(skipped))
	}
	if notices := plan.skipNotices(); len(notices) != 0 {
		t.Fatalf("last-shell repair left skip notices: %v", notices)
	}
}

// TestTopologyPlanKeepsProjectOpenableWithoutPanes pins the last-shell repair
// after the Agent Pane is released: the Window receives a replacement shell.
func TestTopologyPlanKeepsProjectOpenableWithoutPanes(t *testing.T) {
	root := t.TempDir()
	registry, project, mutator := pbtProject(t, root)

	window := registry.WindowsOf(project.Metadata.UID)[0]
	shellPane := registry.PanesOf(window.Metadata.UID)[0]
	agent, err := mutator.CreateAgent(registry, window.Metadata.UID, coremetadata.CreateAgentOptions{
		Provider:    "claude",
		OperationID: "pbt-agent",
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if _, err := mutator.AttachAgentPane(registry, agent.Metadata.UID, coremetadata.BootstrapPane{}, "pbt-attach"); err != nil {
		t.Fatalf("attach agent pane: %v", err)
	}
	if _, err := mutator.ReleaseAgentPane(registry, agent.Metadata.UID, coremetadata.AgentExitNormal, "pbt release"); err != nil {
		t.Fatalf("release agent pane: %v", err)
	}
	if err := mutator.DeletePane(registry, shellPane.Metadata.UID); err != nil {
		t.Fatalf("delete shell pane: %v", err)
	}
	if _, _, err := mutator.AddWindow(registry, project.Metadata.UID, coremetadata.BootstrapWindow{Name: "healthy"}, "sh", "pbt-window"); err != nil {
		t.Fatalf("add healthy window: %v", err)
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("registry the product wrote is invalid: %v", err)
	}
	stored, _ := registry.Window(window.Metadata.UID)
	if strings.TrimSpace(stored.Spec.AnchorPaneRef) == "" {
		t.Fatal("last-shell repair left anchorPaneRef empty")
	}
	if panes := registry.PanesOf(window.Metadata.UID); len(panes) != 1 || panes[0].Metadata.UID != stored.Spec.AnchorPaneRef || panes[0].Spec.Role != coremetadata.PaneRoleShell {
		t.Fatalf("replacement shell chain = %+v", panes)
	}

	plan := pbtPlan(t, *registry, project)
	if fatal, _ := plan.refusalScope(); len(fatal) != 0 {
		t.Fatalf("valid Registry made the Project unopenable: %v\nshape:%s", pbtRender(fatal), pbtRegistryShape(*registry, project))
	}
	if planned := pbtPlannedWindows(plan); !slices.Contains(planned, "healthy") {
		t.Fatalf("planned windows = %v, want the healthy Window to still be built", planned)
	}
}

// TestTopologyPlanSkipsOnlyTheStaleCWDPane pins the deleted-worktree case: a
// stored Pane whose cwd no longer exists is left out of the plan, and its Window
// still comes back with the Panes that can be built.
func TestTopologyPlanSkipsOnlyTheStaleCWDPane(t *testing.T) {
	root := t.TempDir()
	registry, project, mutator := pbtProject(t, root)

	window := registry.WindowsOf(project.Metadata.UID)[0]
	stale, err := mutator.AddPane(registry, window.Metadata.UID,
		coremetadata.BootstrapPane{CWD: filepath.Join(root, ".wt", "feat", "removed-worktree")}, "sh", "pbt-stale")
	if err != nil {
		t.Fatalf("add stale pane: %v", err)
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("registry the product wrote is invalid: %v", err)
	}

	plan := pbtPlan(t, *registry, project)
	fatal, skipped := plan.refusalScope()
	if len(fatal) != 0 {
		t.Fatalf("a stale Pane cwd made the Project unopenable: %v", pbtRender(fatal))
	}
	if len(skipped) != 1 || skipped[0].Target != stale.Metadata.Name {
		t.Fatalf("skipped = %v, want exactly the stale Pane %q", pbtRender(skipped), stale.Metadata.Name)
	}
	if planned := pbtPlannedWindows(plan); len(planned) != 1 {
		t.Fatalf("planned windows = %v, want the Window still built", planned)
	}
	for _, work := range plan.windows {
		for _, paneWork := range work.panes {
			if paneWork.pane.Metadata.UID == stale.Metadata.UID {
				t.Fatalf("plan still builds the stale Pane %q, whose cwd does not exist", stale.Metadata.Name)
			}
		}
	}
}

// pbtOpStream turns fuzz bytes into a bounded operation sequence. It is a stream
// rather than a decoded struct so the corpus stays minimal and a mutation of one
// byte changes one decision.
type pbtOpStream struct {
	data []byte
	pos  int
}

func (s *pbtOpStream) next() (byte, bool) {
	if s.pos >= len(s.data) {
		return 0, false
	}
	b := s.data[s.pos]
	s.pos++
	return b, true
}

// FuzzTopologyPlanNeverRefusesValidRegistry generalizes the two pinned states.
//
// Every operation is one the product exposes, every intermediate Registry is
// asserted valid, and the property is the set relation itself: a Registry the
// writer accepted must not be refused by the planner. Filesystem drift is
// deliberately excluded -- every Pane cwd is the Project root -- so a failure
// here is a structural contract gap and never a missing directory.
func FuzzTopologyPlanNeverRefusesValidRegistry(f *testing.F) {
	f.Add([]byte{0})             // bootstrap topology only
	f.Add([]byte{1, 2})          // agent attached, shell pane deleted
	f.Add([]byte{1, 2, 3})       // agent pane released as well
	f.Add([]byte{4, 1, 2, 0, 3}) // second Window, then the same decay

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64 {
			data = data[:64]
		}
		root := t.TempDir()
		registry, project, mutator := pbtProject(t, root)
		stream := &pbtOpStream{data: data}

		for step := 0; ; step++ {
			op, ok := stream.next()
			if !ok {
				break
			}
			windows := registry.WindowsOf(project.Metadata.UID)
			if len(windows) == 0 {
				break
			}
			window := windows[int(op)%len(windows)]
			switch op % 6 {
			case 0:
				_, _ = mutator.AddPane(registry, window.Metadata.UID, coremetadata.BootstrapPane{}, "sh", fmt.Sprintf("pbt-pane-%d", step))
			case 1:
				agent, err := mutator.CreateAgent(registry, window.Metadata.UID, coremetadata.CreateAgentOptions{
					Provider:    "codex",
					OperationID: fmt.Sprintf("pbt-agent-%d", step),
				})
				if err == nil {
					_, _ = mutator.AttachAgentPane(registry, agent.Metadata.UID, coremetadata.BootstrapPane{}, fmt.Sprintf("pbt-attach-%d", step))
				}
			case 2:
				if panes := registry.PanesOf(window.Metadata.UID); len(panes) > 0 {
					_ = mutator.DeletePane(registry, panes[0].Metadata.UID)
				}
			case 3:
				if agents := registry.AgentsOf(window.Metadata.UID); len(agents) > 0 {
					_, _ = mutator.ReleaseAgentPane(registry, agents[0].Metadata.UID, coremetadata.AgentExitNormal, "pbt")
				}
			case 4:
				_, _, _ = mutator.AddWindow(registry, project.Metadata.UID, coremetadata.BootstrapWindow{}, "sh", fmt.Sprintf("pbt-window-%d", step))
			case 5:
				if agents := registry.AgentsOf(window.Metadata.UID); len(agents) > 0 {
					_ = mutator.DeleteAgent(registry, agents[0].Metadata.UID)
				}
			}
			if err := registry.Validate(); err != nil {
				t.Fatalf("step %d left an invalid Registry: %v", step, err)
			}
		}

		// A Project with no Window declares no desired topology at all, which the
		// startup path answers by creating a fresh session instead. That disagreement
		// is tracked as its own contract; it is not what this property is about.
		if len(registry.WindowsOf(project.Metadata.UID)) == 0 {
			t.Skip("no Registry Window topology to materialize")
		}

		plan := pbtPlan(t, *registry, project)
		if fatal, _ := plan.refusalScope(); len(fatal) != 0 {
			t.Fatalf("valid Registry made the Project unopenable: %v\nshape:%s",
				pbtRender(fatal), pbtRegistryShape(*registry, project))
		}
	})
}
