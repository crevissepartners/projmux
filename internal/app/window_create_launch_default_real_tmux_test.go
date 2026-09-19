package app

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	"github.com/crevissepartners/projmux/internal/ui/projmuxpicker"
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
// default behind the AI command that owns the mode file. The client move runs
// through a probe that records, at the moment the pressing client is moved,
// the live Panes of the Window it is moved onto.
func (fx *splitFocusRealTmux) windowCreateRoute(t *testing.T, mode string) (*tmuxCommand, *aiCommand, *moveProbeRunner) {
	t.Helper()
	ai := fx.aiCommand(t)
	ai.paneDelete = fx.deletePaneRoute()
	if err := ai.setMode(mode); err != nil {
		t.Fatalf("set saved mode %s: %v", mode, err)
	}
	probe := &moveProbeRunner{fx: fx, inner: fx.physical}
	return &tmuxCommand{
		runner:       probe,
		windowCreate: fx.newCreate().createWindowFromIntent,
		launchChoose: ai.chooseLaunchDefault,
	}, ai, probe
}

// moveProbeRunner forwards every tmux call and, on the select-window that
// moves the pressing client onto the new Window, records that Window's live
// Pane uids -- what the client is about to see.
type moveProbeRunner struct {
	fx       *splitFocusRealTmux
	inner    tmuxRunner
	atMove   []string
	moveSeen bool
}

func (p *moveProbeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if i := slices.Index(args, "select-window"); i >= 0 {
		if t := slices.Index(args[i:], "-t"); t >= 0 && i+t+1 < len(args) {
			out, _ := p.fx.tmux("list-panes", "-t", args[i+t+1], "-F", "#{"+tmuxopts.PaneUID+"}")
			p.atMove, p.moveSeen = strings.Fields(out), true
		}
	}
	return p.inner.Run(ctx, name, args...)
}

// displayRecordingRunner forwards every tmux call and keeps the text of each
// display-message, which is what the pressing client reads.
type displayRecordingRunner struct {
	inner tmuxRunner
	lines []string
}

func (r *displayRecordingRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if slices.Contains(args, "display-message") && !slices.Contains(args, "-p") && len(args) > 0 {
		r.lines = append(r.lines, args[len(args)-1])
	}
	return r.inner.Run(ctx, name, args...)
}

// answerLaunchPicker stands in for the answer-mode picker popup, which cannot
// run inside a test. It forwards every tmux call, and when the producer opens
// the picker it checks what the picker would see -- the pressed Pane, no new
// Window yet -- and then writes answer (nil: the operator cancelled).
func (fx *splitFocusRealTmux) answerLaunchPicker(t *testing.T, ai *aiCommand, answer *agentPaneIntent) *int {
	t.Helper()
	asked := new(int)
	windowsBefore := len(fx.store.registry.Windows)
	tmuxRun := ai.runCommand
	ai.runCommand = func(ctx context.Context, name string, args ...string) error {
		if !slices.Contains(args, "popup-toggle") {
			return tmuxRun(ctx, name, args...)
		}
		*asked++
		if got := args[slices.Index(args, "--anchor")+1]; got != fx.originID {
			t.Fatalf("launch picker anchored on %s, want the pressed Pane %s", got, fx.originID)
		}
		if got := args[slices.Index(args, "--client")+1]; got != fx.client {
			t.Fatalf("launch picker opened on client %s, want the pressing client %s", got, fx.client)
		}
		if got := len(fx.store.registry.Windows); got != windowsBefore {
			t.Fatalf("Registry Windows while the picker is up = %d, want %d: nothing exists before the answer", got, windowsBefore)
		}
		if answer != nil {
			path := args[slices.Index(args, popupToggleAnswerFlag)+1]
			if err := writeSplitSelectionAnswer(path, *answer); err != nil {
				t.Fatalf("write answer: %v", err)
			}
		}
		return nil
	}
	return asked
}

// liveWindowCount counts the isolated server's Windows.
func (fx *splitFocusRealTmux) liveWindowCount(t *testing.T) int {
	t.Helper()
	out, err := fx.tmux("list-windows", "-a", "-F", "#{window_id}")
	if err != nil {
		t.Fatalf("list windows: %v: %s", err, out)
	}
	return len(strings.Fields(out))
}

// assertAgentOnlyWindow checks the end state every Agent answer must leave: the
// Window's one Pane is the Agent Pane, it is the Window's anchor, the Window has
// no default shell, and the pressing client is on it -- and at the moment the
// client was moved, that Agent Pane was already the Window's only Pane.
func (fx *splitFocusRealTmux) assertAgentOnlyWindow(t *testing.T, created coremetadata.Window, probe *moveProbeRunner) {
	t.Helper()
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
	if !probe.moveSeen || !slices.Equal(probe.atMove, []string{panes[0].Metadata.UID}) {
		t.Fatalf("Panes when the client was moved = %v (move seen %v), want only the Agent Pane %s",
			probe.atMove, probe.moveSeen, panes[0].Metadata.UID)
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

		route, _, _ := fx.windowCreateRoute(t, aiModeShell)
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

		route, _, probe := fx.windowCreateRoute(t, aiModeClaude)
		if err := route.Run([]string{"window-create", "--client", fx.client, "--anchor", fx.originID},
			ioDiscard{}, ioDiscard{}); err != nil {
			t.Fatalf("window-create route: %v", err)
		}
		fx.assertAgentOnlyWindow(t, fx.createdWindow(t, before), probe)
	})

	t.Run("mode selective asks on the pressed Pane and opens the chosen Agent", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		fx := newSplitFocusRealTmux(t, ctx)
		before := fx.windowUIDs()

		route, ai, probe := fx.windowCreateRoute(t, aiModeSelective)
		asked := fx.answerLaunchPicker(t, ai, &agentPaneIntent{producer: canonicalProducerProviderPicker, provider: aiModeClaude, placement: "right"})
		if err := route.Run([]string{"window-create", "--client", fx.client, "--anchor", fx.originID},
			ioDiscard{}, ioDiscard{}); err != nil {
			t.Fatalf("window-create route: %v", err)
		}
		if *asked != 1 {
			t.Fatalf("launch picker opened %d times, want once", *asked)
		}
		fx.assertAgentOnlyWindow(t, fx.createdWindow(t, before), probe)
	})

	t.Run("mode claude whose Agent cannot open creates no Window", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		fx := newSplitFocusRealTmux(t, ctx)
		before := fx.windowUIDs()
		liveBefore := fx.liveWindowCount(t)
		clientPaneBefore := fx.clientPane(t)

		route, _, probe := fx.windowCreateRoute(t, aiModeClaude)
		// The Agent fails after the Window already exists on the server: its
		// launch is built inside the one transaction, right after new-window.
		failing := fx.newCreate()
		launcher := newFakeAgentLauncher()
		launcher.planErr = errors.New("injected missing provider binary")
		failing.agents = sleepAgentLauncher{launcher}
		route.windowCreate = failing.createWindowFromIntent
		lines := &displayRecordingRunner{inner: probe}
		route.runner = lines
		if err := route.Run([]string{"window-create", "--client", fx.client, "--anchor", fx.originID},
			ioDiscard{}, ioDiscard{}); err != nil {
			t.Fatalf("window-create route: %v", err)
		}
		if got := fx.windowUIDs(); len(got) != len(before) {
			t.Fatalf("Registry Windows = %d after a failed Agent, want %d\n%s", len(got), len(before), fx.store.snapshot())
		}
		if got := fx.liveWindowCount(t); got != liveBefore {
			t.Fatalf("live Windows = %d after a failed Agent, want %d: the new Window was not rolled back", got, liveBefore)
		}
		if probe.moveSeen {
			t.Fatal("a failed Agent moved the pressing client")
		}
		if got := fx.clientPane(t); got != clientPaneBefore {
			t.Fatalf("pressing client is on %s after a failed Agent, want it still on %s", got, clientPaneBefore)
		}
		if len(lines.lines) != 1 || !strings.HasSuffix(lines.lines[0], "injected missing provider binary") ||
			!strings.HasPrefix(lines.lines[0], windowNotCreatedHead) {
			t.Fatalf("pressing client lines = %q, want one not-created line", lines.lines)
		}
		// The line was fitted to the pressing client's real width.
		width, err := fx.tmux("display-message", "-p", "-c", fx.client, "-F", "#{client_width}")
		if err != nil || parsePositiveInt(width) <= 0 {
			t.Fatalf("read the pressing client's width: %q, %v", width, err)
		}
		if got := projmuxpicker.VisibleLen(lines.lines[0]); got > parsePositiveInt(width) {
			t.Fatalf("pressing client line %q is %d cells on a %s-cell client", lines.lines[0], got, width)
		}
	})

	t.Run("mode selective cancelled creates nothing", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		fx := newSplitFocusRealTmux(t, ctx)
		before := fx.windowUIDs()
		liveBefore := fx.liveWindowCount(t)
		clientPaneBefore := fx.clientPane(t)

		route, ai, probe := fx.windowCreateRoute(t, aiModeSelective)
		asked := fx.answerLaunchPicker(t, ai, nil)
		if err := route.Run([]string{"window-create", "--client", fx.client, "--anchor", fx.originID},
			ioDiscard{}, ioDiscard{}); err != nil {
			t.Fatalf("window-create route: %v", err)
		}
		if *asked != 1 {
			t.Fatalf("launch picker opened %d times, want once", *asked)
		}
		if got := fx.windowUIDs(); len(got) != len(before) {
			t.Fatalf("Registry Windows = %d after a cancelled picker, want %d", len(got), len(before))
		}
		if got := fx.liveWindowCount(t); got != liveBefore {
			t.Fatalf("live Windows = %d after a cancelled picker, want %d", got, liveBefore)
		}
		if probe.moveSeen {
			t.Fatal("a cancelled picker moved the pressing client")
		}
		if got := fx.clientPane(t); got != clientPaneBefore {
			t.Fatalf("pressing client is on %s after a cancelled picker, want it still on %s", got, clientPaneBefore)
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
