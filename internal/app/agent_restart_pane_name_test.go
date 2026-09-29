package app

import (
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// personaAttachPaneName is the non-automatic name the fixture Agent's
// managed Pane carries before any restart.
const personaAttachPaneName = "codex-pane"

// assertRestartedPaneName checks the Pane a restart ended in: it is named
// want in the Registry and in the tmux stable-name mirror, want has no other
// holder, and the stopped Pane's row is gone.
func (f *personaAttachFixture) assertRestartedPaneName(t *testing.T, want string) coremetadata.Pane {
	t.Helper()
	after := f.assertRestartedOnTheSameConversation(t, personaAttachPane)
	pane, _ := f.store.registry.Pane(after.Status.PaneRef)
	if want == "" {
		want = pane.Metadata.UID
	}
	if pane.Metadata.Name != want {
		t.Fatalf("new pane/%s (uid:%s), want name %q", pane.Metadata.Name, pane.Metadata.UID, want)
	}
	if holders := paneNameReservationHolders(f.store.registry, "prj-alpha", want); !slices.Equal(holders, []string{pane.Metadata.UID}) {
		t.Fatalf("name %q holders = %v, want only the new Pane %s", want, holders, pane.Metadata.UID)
	}
	if len(f.launcher.bound) == 0 {
		t.Fatal("the restart bound no Pane")
	}
	_, _, live := f.tmux.pane(f.launcher.bound[len(f.launcher.bound)-1].paneID)
	if live == nil || live.opts[tmuxopts.PaneUID] != pane.Metadata.UID || live.opts[tmuxopts.PaneName] != want {
		t.Fatalf("tmux mirror = %+v, want %s=%q on %s", live, tmuxopts.PaneName, want, pane.Metadata.UID)
	}
	return pane.Clone()
}

// TestAgentRestartCarriesTheStoppedPaneName is C-1 acceptance 1 and 2: every
// restart of a Running Agent -- `agent relaunch`, `agent persona
// attach|detach` and `agent instructions attach|detach` -- stops its managed
// Pane through `delete pane`, which drops the row and its name, and the new
// managed Pane still carries that name, with no disclosure.
func TestAgentRestartCarriesTheStoppedPaneName(t *testing.T) {
	for _, test := range []struct {
		name string
		args func(t *testing.T, f *personaAttachFixture) []string
	}{
		{name: "agent relaunch", args: func(*testing.T, *personaAttachFixture) []string {
			return []string{"relaunch", "uid:" + personaAttachAgent, "--effort", "max"}
		}},
		{name: "agent persona attach", args: func(t *testing.T, f *personaAttachFixture) []string {
			f.writePersona(t, "go-reviewer", personaResumeContent)
			return []string{"persona", "attach", "uid:" + personaAttachAgent, "go-reviewer"}
		}},
		{name: "agent instructions attach", args: func(t *testing.T, f *personaAttachFixture) []string {
			f.writePersona(t, "go-reviewer", personaResumeContent)
			return []string{"instructions", "attach", "uid:" + personaAttachAgent, "go-reviewer"}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newRelaunchFixture(t)
			if old, _ := f.store.registry.Pane(personaAttachPane); old.Metadata.Name != personaAttachPaneName {
				t.Fatalf("fixture pane name = %q, want %q", old.Metadata.Name, personaAttachPaneName)
			}
			stdout, stderr, err := runRoute(t, f.command, test.args(t, f)...)
			if err != nil || stderr != "" {
				t.Fatalf("restart stdout=%q stderr=%q err=%v, want success and no disclosure", stdout, stderr, err)
			}
			f.assertRestartedPaneName(t, personaAttachPaneName)
		})
	}
}

// TestAgentRestartOfAnAutomaticallyNamedPaneKeepsAnAutomaticName is C-1
// acceptance 3: a managed Pane named by its own UID has no name to carry, so
// the new Pane is named by its own UID and nothing is disclosed.
func TestAgentRestartOfAnAutomaticallyNamedPaneKeepsAnAutomaticName(t *testing.T) {
	f := newRelaunchFixture(t)
	if _, err := f.store.mutator().RenamePane(&f.store.registry, personaAttachPane, personaAttachPane); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--effort", "max")
	if err != nil || stderr != "" {
		t.Fatalf("relaunch stdout=%q stderr=%q err=%v, want success and no disclosure", stdout, stderr, err)
	}
	f.assertRestartedPaneName(t, "")
}

// TestAgentRestartWhoseStoppedPaneNameIsTakenKeepsAnAutomaticNameAndSaysWhy is
// C-1 acceptance 4: when another Pane takes the stopped Pane's name between
// the stop and the resume, the restart still succeeds, the new Pane is named
// by its own UID, and stderr says why in exactly one line.
func TestAgentRestartWhoseStoppedPaneNameIsTakenKeepsAnAutomaticNameAndSaysWhy(t *testing.T) {
	f := newRelaunchFixture(t)
	load := f.command.loadRegistry
	taken := false
	f.command.loadRegistry = func() (coremetadata.Registry, error) {
		if _, stillThere := f.store.registry.Pane(personaAttachPane); !stillThere && !taken {
			taken = true
			if _, err := f.store.mutator().RenamePane(&f.store.registry, "pan-alpha-zsh", personaAttachPaneName); err != nil {
				t.Fatal(err)
			}
		}
		return load()
	}
	stdout, stderr, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--effort", "max")
	if err != nil {
		t.Fatalf("relaunch stdout=%q stderr=%q err=%v, want success", stdout, stderr, err)
	}
	if !taken {
		t.Fatal("the name was never taken between the stop and the resume")
	}
	if !strings.HasPrefix(stderr, "projmux: agent/codex new Pane keeps an automatic name: ") ||
		!strings.Contains(stderr, personaAttachPaneName) || !strings.Contains(stderr, "pan-alpha-zsh") ||
		strings.Count(stderr, "\n") != 1 {
		t.Fatalf("stderr = %q, want one line naming the taken name and its holder", stderr)
	}
	pane := f.assertRestartedPaneName(t, "")
	if holders := paneNameReservationHolders(f.store.registry, "prj-alpha", personaAttachPaneName); !slices.Equal(holders, []string{"pan-alpha-zsh"}) {
		t.Fatalf("name %q holders = %v, want only pan-alpha-zsh (new Pane %s)", personaAttachPaneName, holders, pane.Metadata.UID)
	}
}
