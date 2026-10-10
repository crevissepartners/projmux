package metadata

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestCreateProcessWindowAtomicAndVirtual(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			m, reg, oldWindow, _ := anchorWriterFixture(t)
			old, _ := reg.Window(oldWindow)
			projectUID := old.Metadata.OwnerUID()
			if err := m.DeleteWindow(&reg, oldWindow); err != nil {
				t.Fatal(err)
			}
			window, agent, pane, err := m.CreateProcessWindow(&reg, projectUID, BootstrapWindow{Name: "virtual"}, CreateAgentOptions{Name: "owned", Provider: provider, Workspace: AgentWorkspace{CWD: "/src/projmux"}, OperationID: "op-virtual"}, ProcessBinding{HostInstanceID: "host", Generation: "gen", OperationID: "op-virtual"})
			if err != nil {
				t.Fatal(err)
			}
			if err := reg.Validate(); err != nil {
				t.Fatal(err)
			}
			project, _ := reg.Project(projectUID)
			if !reg.IsVirtualWindow(window.Metadata.UID) || window.Spec.AnchorPaneRef != pane.Metadata.UID || window.Spec.DefaultShellPaneRef != "" || window.Status.RuntimeID != "" || window.Status.RuntimeSessionID != "" || project.Spec.PrimaryWindowRef != window.Metadata.UID || len(reg.PanesOf(window.Metadata.UID)) != 0 || len(reg.PanesOf(agent.Metadata.UID)) != 1 {
				t.Fatalf("wrong virtual topology: %+v", window)
			}
			owner, _ := reg.Agent(agent.Metadata.UID)
			owner.Status.Phase = PhaseOffline
			if !reg.IsVirtualWindow(window.Metadata.UID) {
				t.Fatal("offline lost virtual state")
			}
			before := reg.Clone()
			if _, _, _, err := m.CreateProcessWindow(&reg, projectUID, BootstrapWindow{Name: "virtual"}, CreateAgentOptions{Provider: provider}, ProcessBinding{}); err == nil || !reflect.DeepEqual(reg, before) {
				t.Fatal("collision mutated registry")
			}
			bad := m
			bad.NewUID = func(kind Kind) (string, error) {
				if kind == KindPane {
					return "", errors.New("pane allocation failed")
				}
				return NewUID(kind)
			}
			if _, _, _, err := bad.CreateProcessWindow(&reg, projectUID, BootstrapWindow{Name: "failed"}, CreateAgentOptions{Provider: provider}, ProcessBinding{}); err == nil || !reflect.DeepEqual(reg, before) {
				t.Fatal("allocation failure mutated registry")
			}
		})
	}
}

func TestIsVirtualWindowRequiresEligibleProcessAnchor(t *testing.T) {
	m, reg, uid, _ := anchorWriterFixture(t)
	if reg.IsVirtualWindow(uid) || reg.IsVirtualWindow("missing") {
		t.Fatal("shell/missing is virtual")
	}
	window, _ := reg.Window(uid)
	agent, pane := attachFixtureAgent(t, m, &reg, uid, "op")
	window, _ = reg.Window(uid)
	window.Spec.AnchorPaneRef = pane
	p, _ := reg.Pane(pane)
	p.Spec.Runtime.Kind = RuntimeProcess
	if !reg.IsVirtualWindow(uid) {
		t.Fatal("eligible process anchor not virtual")
	}
	a, _ := reg.Agent(agent)
	a.Status.PaneRef = "released"
	if reg.IsVirtualWindow(uid) {
		t.Fatal("released anchor is virtual")
	}
}

func TestCreateProcessWindowLongAgentName(t *testing.T) {
	m, reg, uid, _ := anchorWriterFixture(t)
	window, _ := reg.Window(uid)
	_, _, _, err := m.CreateProcessWindow(&reg, window.Metadata.OwnerUID(), BootstrapWindow{}, CreateAgentOptions{Name: strings.Repeat("a", 128), Provider: "codex", Workspace: AgentWorkspace{CWD: "/src/projmux"}}, ProcessBinding{HostInstanceID: "host", Generation: "gen", OperationID: "op"})
	if err != nil {
		t.Fatal(err)
	}
}
