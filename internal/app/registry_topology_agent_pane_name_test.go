package app

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// paneNameHandoffFakePane finds the fake tmux Pane carrying a Registry Pane uid.
func paneNameHandoffFakePane(server *fakeTmux, uid string) *fakeTmuxPane {
	for _, session := range server.sessions {
		for _, window := range session.windows {
			for _, pane := range window.panes {
				if pane.opts[tmuxopts.PaneUID] == uid {
					return pane
				}
			}
		}
	}
	return nil
}

// attachTopologyAgentPane attaches one managed Pane to a stored Agent. An empty
// name gives the Pane its automatic UID name.
func attachTopologyAgentPane(t *testing.T, store *fakeResourceStore, agentUID, name, cwd string) coremetadata.Pane {
	t.Helper()
	pane, err := store.mutator().AttachAgentPane(&store.registry, agentUID, coremetadata.BootstrapPane{Name: name, CWD: cwd}, "op-name-handoff-old")
	if err != nil {
		t.Fatal(err)
	}
	return pane
}

// releaseTopologyAgentPane attaches one managed Pane and releases it again, so
// the Agent retains the row as evidence of an earlier activation.
func releaseTopologyAgentPane(t *testing.T, store *fakeResourceStore, agentUID, name, cwd string) coremetadata.Pane {
	t.Helper()
	pane := attachTopologyAgentPane(t, store, agentUID, name, cwd)
	if _, err := store.mutator().TransitionAgent(&store.registry, agentUID, coremetadata.PhaseOffline, "earlier activation"); err != nil {
		t.Fatal(err)
	}
	return pane
}

// TestRegistryTopologyContinueCarriesOldAgentPaneName is Continue acceptance 1,
// 3 and 6: after an unplanned exit the replayed Agent gets a new Pane UID, and
// that Pane carries the old non-automatic name -- explicit, the `create agent`
// default, or flag-shaped -- in the Registry and in the tmux stable-name mirror.
// An automatic old name is not carried: the new Pane is named by its own UID
// and the old UID is no name anywhere.
func TestRegistryTopologyContinueCarriesOldAgentPaneName(t *testing.T) {
	for _, test := range []struct{ name, paneName string }{
		{name: "explicit name", paneName: "reviewer"},
		{name: "create agent default name", paneName: "claude-pane"},
		{name: "flag-shaped name", paneName: "-L"},
		{name: "automatic name"},
	} {
		t.Run(test.name, func(t *testing.T) {
			command, store, server, _, root, _ := newTopologyMaterializeFixture(t)
			agent := addTopologyFixtureAgent(t, store, topologyFixtureAgent{
				name: "claude", provider: "claude", cwd: root, ref: claudeConversationRef("conv-name-handoff"),
			})
			old := attachTopologyAgentPane(t, store, agent.Metadata.UID, test.paneName, root)
			markTopologyAgentInterrupted(t, store, agent.Metadata.UID, old.Metadata.UID)

			out, stderr, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
			if err != nil {
				t.Fatalf("Continue failed: %v stderr=%q\n%s", err, stderr, out)
			}
			if strings.Contains(stderr, "keeps an automatic name") {
				t.Fatalf("Continue disclosed a lost name: %q", stderr)
			}
			stored, _ := store.registry.Agent(agent.Metadata.UID)
			if stored.Status.Phase != coremetadata.PhaseRunning || stored.Status.PaneRef == "" {
				t.Fatalf("Agent = %+v, want Running on a new Pane", stored.Status)
			}
			newPane, ok := store.registry.Pane(stored.Status.PaneRef)
			if !ok || newPane.Metadata.UID == old.Metadata.UID {
				t.Fatalf("new Pane = %+v (ok=%t), want a UID other than %s", newPane, ok, old.Metadata.UID)
			}
			want := old.Metadata.Name
			if test.paneName == "" {
				want = newPane.Metadata.UID
				if holders := paneNameReservationHolders(store.registry, "prj-beta", old.Metadata.UID); len(holders) != 0 {
					t.Fatalf("old automatic name %s is still reserved by %v", old.Metadata.UID, holders)
				}
			}
			if newPane.Metadata.Name != want {
				t.Fatalf("new pane/%s (uid:%s), want name %q", newPane.Metadata.Name, newPane.Metadata.UID, want)
			}
			if _, ok := store.registry.Pane(old.Metadata.UID); ok {
				t.Fatalf("old Pane row %s was not released", old.Metadata.UID)
			}
			if holders := paneNameReservationHolders(store.registry, "prj-beta", want); !slices.Equal(holders, []string{newPane.Metadata.UID}) {
				t.Fatalf("name %q holders = %v, want only the new Pane %s", want, holders, newPane.Metadata.UID)
			}
			live := paneNameHandoffFakePane(server, newPane.Metadata.UID)
			if live == nil || live.opts[tmuxopts.PaneName] != want {
				t.Fatalf("tmux mirror of %s = %+v, want %s=%q\n%s", newPane.Metadata.UID, live, tmuxopts.PaneName, want, server.state())
			}
		})
	}
}

// TestRegistryTopologyContinueResolvesSeveralOldPaneNamesByTerminationReceipt
// is Continue acceptance 4: several named old rows carry the termination
// receipt Pane's name, and when the receipt selects none of them Continue still
// succeeds with an automatic name and exactly one notice.
func TestRegistryTopologyContinueResolvesSeveralOldPaneNamesByTerminationReceipt(t *testing.T) {
	t.Run("the receipt Pane's name is carried", func(t *testing.T) {
		command, store, _, _, root, _ := newTopologyMaterializeFixture(t)
		agent := addTopologyFixtureAgent(t, store, topologyFixtureAgent{
			name: "claude", provider: "claude", cwd: root, ref: claudeConversationRef("conv-name-receipt"),
		})
		releaseTopologyAgentPane(t, store, agent.Metadata.UID, "first", root)
		second := attachTopologyAgentPane(t, store, agent.Metadata.UID, "second", root)
		markTopologyAgentInterrupted(t, store, agent.Metadata.UID, second.Metadata.UID)

		out, stderr, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
		if err != nil || strings.Contains(stderr, "keeps an automatic name") {
			t.Fatalf("Continue err=%v stderr=%q\n%s", err, stderr, out)
		}
		stored, _ := store.registry.Agent(agent.Metadata.UID)
		newPane, ok := store.registry.Pane(stored.Status.PaneRef)
		if !ok || newPane.Metadata.Name != "second" || newPane.Metadata.UID == second.Metadata.UID {
			t.Fatalf("new Pane = %+v (ok=%t), want the receipt Pane name on a new UID", newPane, ok)
		}
		if holders := paneNameReservationHolders(store.registry, "prj-beta", "first"); len(holders) != 0 {
			t.Fatalf("released row name first is still reserved by %v", holders)
		}
	})
	t.Run("no name is carried when the receipt selects none", func(t *testing.T) {
		command, store, _, _, root, _ := newTopologyMaterializeFixture(t)
		agent := addTopologyFixtureAgent(t, store, topologyFixtureAgent{
			name: "claude", provider: "claude", cwd: root, ref: claudeConversationRef("conv-name-ambiguous"),
		})
		first := releaseTopologyAgentPane(t, store, agent.Metadata.UID, "first", root)
		second := releaseTopologyAgentPane(t, store, agent.Metadata.UID, "second", root)
		automatic := attachTopologyAgentPane(t, store, agent.Metadata.UID, "", root)
		markTopologyAgentInterrupted(t, store, agent.Metadata.UID, automatic.Metadata.UID)

		out, stderr, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
		if err != nil {
			t.Fatalf("an ambiguous name refused Continue: %v stderr=%q\n%s", err, stderr, out)
		}
		want := fmt.Sprintf("projmux: agent/main/claude new Pane keeps an automatic name: old Panes pane/first (uid:%s), pane/second (uid:%s) carry names and the last termination receipt selects none of them\n",
			first.Metadata.UID, second.Metadata.UID)
		if !strings.Contains(stderr, want) || strings.Count(stderr, "keeps an automatic name") != 1 {
			t.Fatalf("stderr = %q, want exactly one line %q", stderr, want)
		}
		stored, _ := store.registry.Agent(agent.Metadata.UID)
		newPane, ok := store.registry.Pane(stored.Status.PaneRef)
		if !ok || stored.Status.Phase != coremetadata.PhaseRunning || newPane.Metadata.Name != newPane.Metadata.UID {
			t.Fatalf("new Pane = %+v (ok=%t) Agent=%+v, want Running on an automatic name", newPane, ok, stored.Status)
		}
	})
}
