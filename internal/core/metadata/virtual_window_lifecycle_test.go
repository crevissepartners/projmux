package metadata

import (
	"reflect"
	"testing"
)

func virtualLifecycleFixture(t *testing.T) (Mutator, Registry, string, string, string, string) {
	t.Helper()
	m, reg, win, shell := anchorWriterFixture(t)
	agent, process := attachFixtureAgent(t, m, &reg, win, "process-first")
	p, _ := reg.Pane(process)
	p.Spec.Runtime.Kind = RuntimeProcess
	w, _ := reg.Window(win)
	w.Status.RuntimeSessionID = "$1"
	w.Status.RuntimeID = "@1"
	return m, reg, win, shell, agent, process
}

func assertVirtualLifecycle(t *testing.T, reg Registry, win, process string) {
	t.Helper()
	w, ok := reg.Window(win)
	if !ok || !reg.IsVirtualWindow(win) || w.Spec.AnchorPaneRef != process || w.Spec.DefaultShellPaneRef != "" || w.Status.RuntimeID != "" || w.Status.RuntimeSessionID != "" || len(reg.PanesOf(win)) != 0 {
		t.Fatalf("wrong virtual state: %+v", w)
	}
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestVirtualWindowTmuxAnchorPriorityAndLastDeletion(t *testing.T) {
	for _, action := range []string{"pane", "agent", "release"} {
		t.Run(action, func(t *testing.T) {
			m, reg, win, shell, _, process := virtualLifecycleFixture(t)
			tmuxAgent, tmuxPane := attachFixtureAgent(t, m, &reg, win, "tmux-last")
			if err := m.DeletePane(&reg, shell); err != nil {
				t.Fatal(err)
			}
			w, _ := reg.Window(win)
			if w.Spec.AnchorPaneRef != tmuxPane || reg.IsVirtualWindow(win) || w.Status.RuntimeID != "@1" {
				t.Fatalf("process won over tmux: %+v", w)
			}
			switch action {
			case "pane":
				if err := m.DeletePane(&reg, tmuxPane); err != nil {
					t.Fatal(err)
				}
			case "agent":
				if err := m.DeleteAgent(&reg, tmuxAgent); err != nil {
					t.Fatal(err)
				}
			case "release":
				if _, err := m.TransitionAgent(&reg, tmuxAgent, PhaseOffline, "exit"); err != nil {
					t.Fatal(err)
				}
			}
			assertVirtualLifecycle(t, reg, win, process)
		})
	}
}

func TestVirtualWindowLastResourceCascade(t *testing.T) {
	for _, kind := range []Kind{KindPane, KindAgent} {
		t.Run(string(kind), func(t *testing.T) {
			m, reg, win, shell, agent, process := virtualLifecycleFixture(t)
			if err := m.DeletePane(&reg, shell); err != nil {
				t.Fatal(err)
			}
			uid := process
			if kind == KindAgent {
				uid = agent
			}
			if reg.VirtualWindowDeleteCascade(kind, uid) != win {
				t.Fatal("missing cascade")
			}
			if kind == KindPane {
				if err := m.DeletePane(&reg, uid); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := m.DeleteAgent(&reg, uid); err != nil {
					t.Fatal(err)
				}
			}
			if len(reg.Windows) != 0 || len(reg.Panes) != 0 || len(reg.Agents) != 0 || reg.Projects[0].Spec.PrimaryWindowRef != "" {
				t.Fatalf("cascade left topology: %+v", reg)
			}
			if err := reg.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestVirtualWindowAbsentTmuxObservationPreservesLiveAndOrdinaryWindows(t *testing.T) {
	m, reg, win, shell, _, process := virtualLifecycleFixture(t)
	before := reg.Clone()
	if err := m.ReturnAbsentTmuxWindowsToVirtual(&reg, RuntimeObservation{Panes: map[string]bool{shell: true}}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, reg) {
		t.Fatal("removed live shell")
	}
	if err := m.ReturnAbsentTmuxWindowsToVirtual(&reg, RuntimeObservation{}); err != nil {
		t.Fatal(err)
	}
	assertVirtualLifecycle(t, reg, win, process)
	m.ObserveRuntimeBindings(&reg, RuntimeObservation{ProcessPanes: map[string]bool{process: true}})
	w, _ := reg.Window(win)
	if _, ok := w.HasCondition(ConditionMissingRuntime); ok {
		t.Fatal("virtual window drift")
	}
	m, ordinary, _, _ := anchorWriterFixture(t)
	before = ordinary.Clone()
	if err := m.ReturnAbsentTmuxWindowsToVirtual(&ordinary, RuntimeObservation{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, ordinary) {
		t.Fatal("changed ordinary window")
	}
}

func TestVirtualWindowCascadeMovesPrimaryAndCannotAdoptUnrelatedTmux(t *testing.T) {
	m, reg, win, shell, agent, _ := virtualLifecycleFixture(t)
	if err := m.DeletePane(&reg, shell); err != nil {
		t.Fatal(err)
	}
	project := reg.Windows[0].Metadata.OwnerUID()
	other, _, err := m.AddWindow(&reg, project, BootstrapWindow{Name: "sibling", Panes: []BootstrapPane{{Name: "sibling-shell"}}}, "/bin/sh", "op-sibling")
	if err != nil {
		t.Fatal(err)
	}
	matcher := NewBindingMatcher(RuntimeObservation{})
	if match := matcher.MatchWindow(&reg, project, ""); match.UID != other.Metadata.UID {
		t.Fatalf("adopted virtual Window: %+v", match)
	}
	if err := m.DeleteAgent(&reg, agent); err != nil {
		t.Fatal(err)
	}
	p, _ := reg.Project(project)
	if p.Spec.PrimaryWindowRef != other.Metadata.UID {
		t.Fatalf("primary %q", p.Spec.PrimaryWindowRef)
	}
	if _, ok := reg.Window(win); ok {
		t.Fatal("virtual survived")
	}
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
}
