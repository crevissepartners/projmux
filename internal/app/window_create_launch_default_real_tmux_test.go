package app

import (
	"context"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// deletePaneRoute is the canonical Pane delete bound to this fixture's
// Registry and its isolated server: the production body with its two seams
// stated, so what runs here is the same route the Pane menu Kill item runs.
func (fx *splitFocusRealTmux) deletePaneRoute() paneMenuDeleteFunc {
	return func(anchorPaneID string, stdout, stderr io.Writer) error {
		panes := newTmuxPaneDeleteRuntime()
		panes.runner = fx.runner
		panes.getenv = fx.lookupEnv
		command := &deleteCommand{
			store:          fx.store.store(),
			confirm:        newConfirmer(),
			resolveKinds:   deleteRegistryKinds,
			windows:        newTmuxWindowDeleteRuntime(),
			panes:          panes,
			lookupEnv:      fx.lookupEnv,
			newOperationID: newCreateOperationID,
		}
		lookup := anchoredActiveTargetLookup(anchorPaneID, fx.lookupEnv, intmetadata.NewMirror(fx.physical))
		return deleteExactPaneThroughCommand(command, lookup, anchorPaneID, stdout, stderr)
	}
}

// windowCreateRoute is the generated Window create key wired the way the
// application graph wires it: the canonical Window create, and the saved launch
// default behind the AI command that owns the mode file.
func (fx *splitFocusRealTmux) windowCreateRoute(t *testing.T, mode string) *tmuxCommand {
	t.Helper()
	ai := fx.aiCommand(t)
	ai.paneDelete = fx.deletePaneRoute()
	if err := ai.setMode(mode); err != nil {
		t.Fatalf("set saved mode %s: %v", mode, err)
	}
	return &tmuxCommand{
		runner:        fx.physical,
		windowCreate:  fx.newCreate().createWindowFromIntent,
		launchDefault: ai.applyLaunchDefault,
	}
}

func (fx *splitFocusRealTmux) windowUIDs() map[string]bool {
	uids := map[string]bool{}
	for _, window := range fx.store.registry.Windows {
		uids[window.Metadata.UID] = true
	}
	return uids
}

// createdWindow returns the one Window the route added to the Registry.
func (fx *splitFocusRealTmux) createdWindow(t *testing.T, before map[string]bool) coremetadata.Window {
	t.Helper()
	var created []coremetadata.Window
	for _, window := range fx.store.registry.Windows {
		if !before[window.Metadata.UID] {
			created = append(created, window)
		}
	}
	if len(created) != 1 {
		t.Fatalf("created Windows = %d, want exactly one\n%s", len(created), fx.store.snapshot())
	}
	return created[0]
}

// clientPane is the exact Pane the pressing client is on.
func (fx *splitFocusRealTmux) clientPane(t *testing.T) string {
	t.Helper()
	out, err := fx.tmux("list-clients", "-F", "#{client_name}\t#{pane_id}")
	if err != nil {
		t.Fatalf("list clients: %v: %s", err, out)
	}
	for line := range strings.SplitSeq(out, "\n") {
		name, pane, _ := strings.Cut(strings.TrimSpace(line), "\t")
		if name == fx.client {
			return pane
		}
	}
	t.Fatalf("pressing client %s is not attached: %q", fx.client, out)
	return ""
}

// TestWindowCreateAppliesTheSavedLaunchDefaultThroughRealTmux is the live half
// of the feature: on a real server, the Window the key creates ends up holding
// exactly what the saved launch default names.
//
// `shell` is the unchanged behavior -- the Window keeps the one shell Pane the
// create committed. A provider mode commits the Agent beside that shell and
// then removes the shell through the canonical Pane delete, so the Window is
// left with exactly the Agent Pane: it is the Window's anchor, the Window has
// no default shell left, and the pressing client is on it.
func TestWindowCreateAppliesTheSavedLaunchDefaultThroughRealTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}

	t.Run("mode shell keeps the created shell Pane", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		fx := newSplitFocusRealTmux(t, ctx)
		before := fx.windowUIDs()

		route := fx.windowCreateRoute(t, aiModeShell)
		if err := route.Run([]string{"window-create", "--client", fx.client, "--anchor", fx.originID},
			ioDiscard{}, ioDiscard{}); err != nil {
			t.Fatalf("window-create route: %v", err)
		}

		created := fx.createdWindow(t, before)
		panes := fx.store.registry.PanesOf(created.Metadata.UID)
		if len(panes) != 1 || panes[0].Spec.Role != coremetadata.PaneRoleShell {
			t.Fatalf("created Window Panes = %+v, want exactly one shell Pane", panes)
		}
		livePanes := fx.tmuxPaneIDs(t, created)
		if len(livePanes) != 2 {
			t.Fatalf("live Pane rows of the created Window = %v, want exactly the shell Pane", livePanes)
		}
		live := realTmuxPaneWithUID(t, livePanes, panes[0].Metadata.UID)
		if got := fx.clientPane(t); got != live {
			t.Fatalf("pressing client is on Pane %s, want the created shell %s", got, live)
		}
	})

	t.Run("mode claude leaves exactly the Agent Pane", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		fx := newSplitFocusRealTmux(t, ctx)
		before := fx.windowUIDs()

		route := fx.windowCreateRoute(t, aiModeClaude)
		if err := route.Run([]string{"window-create", "--client", fx.client, "--anchor", fx.originID},
			ioDiscard{}, ioDiscard{}); err != nil {
			t.Fatalf("window-create route: %v", err)
		}

		created := fx.createdWindow(t, before)
		// The shell the create committed is gone from the Registry; what the
		// Window owns is the Agent, and the Agent owns the one Pane. (An Agent
		// Pane's owner is its Agent, which is why the Window itself now owns no
		// Pane at all.)
		if shells := fx.store.registry.PanesOf(created.Metadata.UID); len(shells) != 0 {
			t.Fatalf("created Window still owns Panes %+v, want the shell replaced\n%s", shells, fx.store.snapshot())
		}
		agents := fx.store.registry.AgentsOf(created.Metadata.UID)
		if len(agents) != 1 {
			t.Fatalf("created Window Agents = %+v, want exactly one\n%s", agents, fx.store.snapshot())
		}
		panes := fx.store.registry.PanesOf(agents[0].Metadata.UID)
		if len(panes) != 1 || panes[0].Spec.Role != coremetadata.PaneRoleAgent {
			t.Fatalf("Agent Panes = %+v, want exactly one Agent Pane\n%s", panes, fx.store.snapshot())
		}
		window, _ := fx.store.registry.Window(created.Metadata.UID)
		if window.Spec.AnchorPaneRef != panes[0].Metadata.UID {
			t.Fatalf("Window anchor = %q, want the Agent Pane %q", window.Spec.AnchorPaneRef, panes[0].Metadata.UID)
		}
		if strings.TrimSpace(window.Spec.DefaultShellPaneRef) != "" {
			t.Fatalf("Window default shell = %q, want none after the shell was replaced", window.Spec.DefaultShellPaneRef)
		}
		livePanes := fx.tmuxPaneIDs(t, created)
		if len(livePanes) != 2 {
			t.Fatalf("live Pane rows of the created Window = %v, want exactly the Agent Pane", livePanes)
		}
		live := realTmuxPaneWithUID(t, livePanes, panes[0].Metadata.UID)
		if got := fx.clientPane(t); got != live {
			t.Fatalf("pressing client is on Pane %s, want the Agent Pane %s", got, live)
		}
	})
}

// tmuxPaneIDs lists the live Panes of a committed Window on the isolated
// server, addressed by the exact `@N` the Registry bound. Each Pane
// contributes two fields: its exact `%N` and its mirrored uid.
func (fx *splitFocusRealTmux) tmuxPaneIDs(t *testing.T, window coremetadata.Window) []string {
	t.Helper()
	if exactTmuxHandle(window.Status.RuntimeID, "@") == "" {
		t.Fatalf("Window %s has no exact runtime binding", window.Metadata.UID)
	}
	out, err := fx.tmux("list-panes", "-t", window.Status.RuntimeID, "-F", "#{pane_id}\t#{"+tmuxopts.PaneUID+"}")
	if err != nil {
		t.Fatalf("list panes of %s: %v: %s", window.Status.RuntimeID, err, out)
	}
	return strings.Fields(strings.ReplaceAll(out, "\t", " "))
}

// realTmuxPaneWithUID picks the exact `%N` mirroring one Pane uid out of the
// rows tmuxPaneIDs returned.
func realTmuxPaneWithUID(t *testing.T, rows []string, uid string) string {
	t.Helper()
	for i, field := range rows {
		if field == uid && i > 0 {
			return rows[i-1]
		}
	}
	t.Fatalf("no live Pane mirrors uid %q in %v", uid, rows)
	return ""
}
