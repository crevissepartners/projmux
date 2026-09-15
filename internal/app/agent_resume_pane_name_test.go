package app

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// runPaneNameResume resumes the beta codex fixture Agent on the pinned native
// fixture and returns the Registry Pane it was rebound to.
func runPaneNameResume(t *testing.T, store *fakeResourceStore, tmux *fakeTmux) (coremetadata.Pane, string, *fakeResumeLauncher) {
	t.Helper()
	command, launcher, _, _ := newTestAgentResumeCommand(t, store, tmux)
	enablePinnedNativeResumeFixture(t, command, store, "agt-beta-codex", launcher)
	stdout, stderr, err := runRoute(t, command, "resume", "uid:agt-beta-codex")
	if err != nil || stdout != "agent/codex resumed\n" {
		t.Fatalf("agent resume stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	agent, _ := store.registry.Agent("agt-beta-codex")
	if agent.Status.Phase != coremetadata.PhaseRunning {
		t.Fatalf("resumed Agent = %+v, want Running", agent.Status)
	}
	pane, ok := store.registry.Pane(agent.Status.PaneRef)
	if !ok {
		t.Fatalf("resumed paneRef %q resolves to no Pane", agent.Status.PaneRef)
	}
	return pane.Clone(), stderr, launcher
}

// releaseResumeFixturePane gives the beta codex fixture Agent one retained old
// Pane row, the evidence an Offline Agent keeps. An empty name is automatic.
func releaseResumeFixturePane(t *testing.T, store *fakeResourceStore, name string) coremetadata.Pane {
	t.Helper()
	pane := attachTopologyAgentPane(t, store, "agt-beta-codex", name, "/srv/beta")
	if _, err := store.mutator().TransitionAgent(&store.registry, "agt-beta-codex", coremetadata.PhaseOffline, "earlier activation"); err != nil {
		t.Fatal(err)
	}
	return pane
}

// TestAgentResumeCarriesOldAgentPaneName is resume acceptance 2, 3 and 6:
// explicit `agent resume` gives the new Pane the old non-automatic name --
// explicit, the `create agent` default, or flag-shaped -- in the Registry and
// the tmux stable-name mirror, and releases the old row that held it. An
// automatic old name is not carried: the new Pane is named by its own UID.
func TestAgentResumeCarriesOldAgentPaneName(t *testing.T) {
	for _, test := range []struct{ name, paneName string }{
		{name: "explicit name", paneName: "reviewer"},
		{name: "create agent default name", paneName: "codex-pane"},
		{name: "flag-shaped name", paneName: "-L"},
		{name: "automatic name"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newFakeResourceStore(t)
			setFixtureSessionRef(t, store, "agt-beta-codex", resumeFixtureRef(resourceFixtureClock))
			old := releaseResumeFixturePane(t, store, test.paneName)
			tmux := newFakeTmux()

			newPane, stderr, launcher := runPaneNameResume(t, store, tmux)
			if stderr != "" {
				t.Fatalf("stderr = %q, want no disclosure", stderr)
			}
			if newPane.Metadata.UID == old.Metadata.UID {
				t.Fatalf("resume reused old Pane UID %s", old.Metadata.UID)
			}
			want := old.Metadata.Name
			if test.paneName == "" {
				want = newPane.Metadata.UID
				// The automatic old row stays as evidence and is the only holder
				// of its own UID name; nothing else carries it.
				if retained, ok := store.registry.Pane(old.Metadata.UID); !ok || retained.Metadata.Name != old.Metadata.UID {
					t.Fatalf("automatic old row = %+v (ok=%t), want retained unchanged", retained, ok)
				}
				if holders := paneNameReservationHolders(store.registry, "prj-beta", old.Metadata.UID); !slices.Equal(holders, []string{old.Metadata.UID}) {
					t.Fatalf("old automatic name holders = %v, want only the retained old row", holders)
				}
			} else if _, ok := store.registry.Pane(old.Metadata.UID); ok {
				t.Fatalf("old Pane row %s holding %q was not released", old.Metadata.UID, old.Metadata.Name)
			}
			if newPane.Metadata.Name != want {
				t.Fatalf("new pane/%s (uid:%s), want name %q", newPane.Metadata.Name, newPane.Metadata.UID, want)
			}
			if holders := paneNameReservationHolders(store.registry, "prj-beta", want); !slices.Equal(holders, []string{newPane.Metadata.UID}) {
				t.Fatalf("name %q holders = %v, want only the new Pane %s", want, holders, newPane.Metadata.UID)
			}
			if len(launcher.bound) != 1 {
				t.Fatalf("bound panes = %+v, want one", launcher.bound)
			}
			_, _, live := tmux.pane(launcher.bound[0].paneID)
			if live == nil || live.opts[tmuxopts.PaneUID] != newPane.Metadata.UID || live.opts[tmuxopts.PaneName] != want {
				t.Fatalf("tmux mirror = %+v, want %s=%q on %s", live, tmuxopts.PaneName, want, newPane.Metadata.UID)
			}
		})
	}
}

// TestAgentResumeResolvesSeveralOldPaneNamesByTerminationReceipt is resume
// acceptance 4: the termination receipt Pane's name wins among several named
// rows, and when it selects none the resume succeeds with an automatic name
// and exactly one stderr line.
func TestAgentResumeResolvesSeveralOldPaneNamesByTerminationReceipt(t *testing.T) {
	t.Run("the receipt Pane's name is carried", func(t *testing.T) {
		store := newFakeResourceStore(t)
		setFixtureSessionRef(t, store, "agt-beta-codex", resumeFixtureRef(resourceFixtureClock))
		first := releaseResumeFixturePane(t, store, "first")
		second := attachTopologyAgentPane(t, store, "agt-beta-codex", "second", "/srv/beta")
		markTopologyAgentInterrupted(t, store, "agt-beta-codex", second.Metadata.UID)

		newPane, stderr, _ := runPaneNameResume(t, store, newFakeTmux())
		if stderr != "" || newPane.Metadata.Name != "second" {
			t.Fatalf("new pane/%s stderr=%q, want the receipt Pane name and no disclosure", newPane.Metadata.Name, stderr)
		}
		if _, ok := store.registry.Pane(second.Metadata.UID); ok {
			t.Fatalf("receipt Pane row %s was not released", second.Metadata.UID)
		}
		if retained, ok := store.registry.Pane(first.Metadata.UID); !ok || retained.Metadata.Name != "first" {
			t.Fatalf("unselected old row = %+v (ok=%t), want retained with its own name", retained, ok)
		}
	})
	t.Run("no name is carried when the receipt selects none", func(t *testing.T) {
		store := newFakeResourceStore(t)
		setFixtureSessionRef(t, store, "agt-beta-codex", resumeFixtureRef(resourceFixtureClock))
		first := releaseResumeFixturePane(t, store, "first")
		second := releaseResumeFixturePane(t, store, "second")

		newPane, stderr, _ := runPaneNameResume(t, store, newFakeTmux())
		want := fmt.Sprintf("projmux: agent/codex new Pane keeps an automatic name: old Panes pane/first (uid:%s), pane/second (uid:%s) carry names and the last termination receipt selects none of them\n",
			first.Metadata.UID, second.Metadata.UID)
		if stderr != want {
			t.Fatalf("stderr = %q, want exactly %q", stderr, want)
		}
		if newPane.Metadata.Name != newPane.Metadata.UID {
			t.Fatalf("new pane/%s (uid:%s), want an automatic name", newPane.Metadata.Name, newPane.Metadata.UID)
		}
		for _, old := range []coremetadata.Pane{first, second} {
			if retained, ok := store.registry.Pane(old.Metadata.UID); !ok || retained.Metadata.Name != old.Metadata.Name {
				t.Fatalf("old row %s = %+v (ok=%t), want retained", old.Metadata.UID, retained, ok)
			}
		}
	})
}

// TestAgentResumeKeepsAutomaticNameWhenOldPaneIsNotProvenNonLive is ruling 5
// on resume: an old row whose UID is still claimed live elsewhere on the socket
// is not released and its name is not carried; the resume still succeeds and
// says so in one line.
func TestAgentResumeKeepsAutomaticNameWhenOldPaneIsNotProvenNonLive(t *testing.T) {
	store := newFakeResourceStore(t)
	setFixtureSessionRef(t, store, "agt-beta-codex", resumeFixtureRef(resourceFixtureClock))
	old := releaseResumeFixturePane(t, store, "reviewer")
	tmux := newFakeTmux()
	foreign := tmux.addSession("foreign")
	foreign.windows[0].panes[0].opts[tmuxopts.PaneUID] = old.Metadata.UID

	newPane, stderr, _ := runPaneNameResume(t, store, tmux)
	if !strings.HasPrefix(stderr, "projmux: agent/codex new Pane keeps an automatic name: pane codex uid "+old.Metadata.UID+" is already live on") ||
		strings.Count(stderr, "\n") != 1 {
		t.Fatalf("stderr = %q, want one not-proven-non-live line", stderr)
	}
	if newPane.Metadata.Name != newPane.Metadata.UID {
		t.Fatalf("new pane/%s (uid:%s), want an automatic name", newPane.Metadata.Name, newPane.Metadata.UID)
	}
	if retained, ok := store.registry.Pane(old.Metadata.UID); !ok || retained.Metadata.Name != "reviewer" {
		t.Fatalf("live-claimed old row = %+v (ok=%t), want retained with its name", retained, ok)
	}
}
