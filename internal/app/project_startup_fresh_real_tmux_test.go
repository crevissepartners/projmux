package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// freshAppSocketRunner is the switcher's exact app-socket runner on the
// isolated server. The fresh open addresses the app socket by name (`-L
// projmux`) for the client handoff, the one bounded line, and the live Pane
// mirror; this runner refuses anything else and re-points exactly that name at
// the isolated socket, so no call from this flow can reach another server. At
// the handoff it records the live Panes of the Session the client is moved onto
// -- what the client is about to see.
type freshAppSocketRunner struct {
	t        *testing.T
	fx       *splitFocusRealTmux
	session  string
	atSwitch []string
	switched bool
	lines    []string
}

func (r *freshAppSocketRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name != "tmux" || len(args) < 3 || args[0] != "-L" || args[1] != defaultAppSocket {
		r.t.Errorf("the fresh open reached tmux without the exact app socket: %s %q", name, args)
		return nil, fmt.Errorf("unrouted tmux call %q", args)
	}
	command := args[2:]
	switch command[0] {
	case "switch-client":
		r.atSwitch, r.switched = r.fx.sessionPaneUIDs(r.t, r.session), true
	case "display-message":
		if !slices.Contains(command, "-p") {
			r.lines = append(r.lines, command[len(command)-1])
		}
	}
	return r.fx.runner.Run(ctx, "tmux", append([]string{"-S", r.fx.socket}, command...)...)
}

// sessionPaneUIDs lists the mirrored Pane uid of every live Pane in one
// Session, read from the server-wide `list-panes -a` so a Pane cannot hide in
// a Window the Registry does not name. A missing Session is no Panes.
func (fx *splitFocusRealTmux) sessionPaneUIDs(t *testing.T, session string) []string {
	t.Helper()
	out, err := fx.tmux("list-panes", "-a", "-F", "#{session_name}\t#{"+tmuxopts.PaneUID+"}")
	if err != nil {
		t.Fatalf("list panes: %v: %s", err, out)
	}
	var uids []string
	for line := range strings.SplitSeq(out, "\n") {
		name, uid, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if ok && name == session {
			uids = append(uids, uid)
		}
	}
	slices.Sort(uids)
	return uids
}

// freshOpenRealTmux is one fresh open wired onto the split-focus server the way
// the application graph wires it: the Registry fresh starter, the Registry
// topology engine, the saved launch default behind the AI command that owns the
// mode file, the canonical create and Pane delete, and the production origin
// Pane lookup.
type freshOpenRealTmux struct {
	fx       *splitFocusRealTmux
	cmd      *switchCommand
	ai       *aiCommand
	appTmux  *freshAppSocketRunner
	root     string
	session  string
	oldUID   string
	asked    int
	answered int
}

func newFreshOpenRealTmux(t *testing.T, ctx context.Context, registered bool, mode string) *freshOpenRealTmux {
	t.Helper()
	fx := newSplitFocusRealTmux(t, ctx)
	// A stray tmux call that ignored every explicit route would fall back to
	// the process environment; point that at the isolated root too.
	t.Setenv("TMUX_TMPDIR", filepath.Dir(filepath.Dir(fx.socket)))
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	fresh := &freshOpenRealTmux{fx: fx, session: "fresh-new"}
	root, err := os.MkdirTemp(filepath.Dir(filepath.Dir(fx.socket)), "fresh-")
	if err != nil {
		t.Fatal(err)
	}
	fresh.root = root
	fx.store.dirs[root] = true
	if registered {
		fresh.session = "fresh-reg"
		// A closed registered Project with a layout of its own: two Windows,
		// three shell Panes, none of them live.
		result, err := fx.store.mutator().RegisterProject(&fx.store.registry, coremetadata.RegisterProjectOptions{
			Root: root, Name: "fresh-reg", SessionName: fresh.session, DefaultShell: "/bin/sh",
			Topology: []coremetadata.BootstrapWindow{
				{Name: "editor", Panes: []coremetadata.BootstrapPane{{Name: "code", CWD: root}, {Name: "logs", CWD: root}}},
				{Name: "notes", Panes: []coremetadata.BootstrapPane{{Name: "writing", CWD: root}}},
			},
			OperationID: "op-fresh-ask-register",
		})
		if err != nil {
			t.Fatalf("register the closed Project: %v", err)
		}
		fresh.oldUID = result.Project.Metadata.UID
		if err := fx.store.registry.Validate(); err != nil {
			t.Fatalf("fixture Registry is invalid: %v", err)
		}
	}

	ai := fx.aiCommand(t)
	ai.paneDelete = fx.deletePaneRoute()
	if mode != "" {
		if err := ai.setMode(mode); err != nil {
			t.Fatalf("set saved mode %s: %v", mode, err)
		}
	}
	fresh.ai = ai

	logical := filepath.Base(fx.socket)
	target, err := tmuxSocketNameTarget(logical)
	if err != nil {
		t.Fatal(err)
	}
	activation := &registryProjectTopologyMaterializer{
		resources:      fx.store.store(),
		runner:         fx.runner,
		target:         target,
		newReconciler:  reconcileFixtureReconciler(root, fresh.session),
		newOperationID: func() (string, error) { return "op-fresh-ask-materialize", nil },
		newMaterializer: func(exact tmuxCommandRunner, warn io.Writer) *materializer {
			client := inttmux.NewClient(exact, inttmux.WithSocketName(logical))
			return &materializer{runner: exact, mirror: intmetadata.NewMirror(exact), sessions: client, target: target, warn: warn}
		},
		warn:    io.Discard,
		agents:  newFakeTopologyAgentLauncher(),
		notices: io.Discard,
	}
	activation.resolveRoute = func(ctx context.Context, anchor string) (runtimeMutationRoute, error) {
		return resolveExistingRuntimeMutationRouteWithAnchor(ctx, fx.runner, target, func(string) string { return "" }, anchor)
	}
	fresh.appTmux = &freshAppSocketRunner{t: t, fx: fx, session: fresh.session}
	// The sidebar continuation's environment: the exact client that pressed
	// the row, and nothing that names a Pane -- the anchor is an operand.
	env := map[string]string{inttmux.SwitchTargetClientEnv: fx.client}
	cmd := &switchCommand{
		sessions:   &capturingSwitchSessionExecutor{authorizeSet: true, authorizeResult: true},
		tmuxRunner: fresh.appTmux,
		lookupEnv:  func(name string) string { return env[name] },
		projectFreshStart: &registryProjectFreshStarter{
			resources: fx.store.store(), runner: fx.runner, shell: "/bin/sh",
			target: tmuxTransport{Kind: tmuxSocketPath, Value: fx.socket, Source: tmuxSocketPathSource},
		},
		projectTopology: activation,
		startupNotices:  &recordingProjectStartupReporter{},
	}
	cmd.freshOriginShellPane = func(ctx context.Context, root string) (string, error) {
		return freshProjectOriginShellPane(ctx, fx.store.store().snapshot, cmd.liveShellPaneTarget, root)
	}
	// The question is observed where the operator would see it: nothing of the
	// fresh open exists yet, the old Project is as it was, and the pressing
	// client has not moved.
	writes := fx.store.writes
	cmd.launchChoose = func(anchor, client string) launchChoice {
		fresh.asked++
		fresh.requireUntouched(t, writes)
		return ai.chooseLaunchDefault(anchor, client)
	}
	cmd.launchApply = ai.applyLaunchChoice
	fresh.cmd = cmd
	return fresh
}

// answerPicker stands in for the answer-mode picker popup, which cannot run
// inside a test: it checks the popup is asked on the pressed Pane and the
// pressing client, then writes answer (nil: the operator closed it).
func (f *freshOpenRealTmux) answerPicker(t *testing.T, answer *agentPaneIntent) {
	t.Helper()
	tmuxRun := f.ai.runCommand
	f.ai.runCommand = func(ctx context.Context, name string, args ...string) error {
		if !slices.Contains(args, "popup-toggle") {
			return tmuxRun(ctx, name, args...)
		}
		f.answered++
		if got := args[slices.Index(args, "--anchor")+1]; got != f.fx.originID {
			t.Fatalf("launch picker anchored on %s, want the pressed Pane %s", got, f.fx.originID)
		}
		if got := args[slices.Index(args, "--client")+1]; got != f.fx.client {
			t.Fatalf("launch picker opened on client %s, want the pressing client %s", got, f.fx.client)
		}
		if answer != nil {
			if err := writeSplitSelectionAnswer(args[slices.Index(args, popupToggleAnswerFlag)+1], *answer); err != nil {
				t.Fatalf("write answer: %v", err)
			}
		}
		return nil
	}
}

// requireUntouched is the ask-time state: no fresh Session, the old Project
// identity (or none) at the root, no Registry write since the open began, and
// the pressing client still on the Pane it pressed the row in.
func (f *freshOpenRealTmux) requireUntouched(t *testing.T, writes int) {
	t.Helper()
	if out, err := f.fx.tmux("has-session", "-t", "="+f.session); err == nil {
		t.Fatalf("session %s exists while the question is asked: %s", f.session, out)
	}
	uid := ""
	if project, ok := f.fx.store.registry.ProjectByRoot(f.root); ok {
		uid = project.Metadata.UID
	}
	if uid != f.oldUID {
		t.Fatalf("Project at the root while the question is asked = %q, want the untouched %q", uid, f.oldUID)
	}
	if f.fx.store.writes != writes {
		t.Fatalf("Registry writes while the question is asked = %d, want %d", f.fx.store.writes, writes)
	}
	if got := f.fx.clientPane(t); got != f.fx.originID {
		t.Fatalf("pressing client is on %s while the question is asked, want the pressed Pane %s", got, f.fx.originID)
	}
}

func (f *freshOpenRealTmux) open(t *testing.T, ctx context.Context) {
	t.Helper()
	if err := f.cmd.startProjectFresh(ctx, f.session, f.root, openedProjectBootstrap{}, f.fx.originID); err != nil {
		t.Fatalf("startProjectFresh() error = %v", err)
	}
	if f.asked != 1 {
		t.Fatalf("launch default asked %d times, want once", f.asked)
	}
	if len(f.appTmux.lines) != 0 {
		t.Fatalf("client lines = %q, want none", f.appTmux.lines)
	}
}

// freshWindow is the fresh Project's one Window, under a new identity.
func (f *freshOpenRealTmux) freshWindow(t *testing.T) coremetadata.Window {
	t.Helper()
	project, ok := f.fx.store.registry.ProjectByRoot(f.root)
	if !ok || project.Metadata.UID == "" || project.Metadata.UID == f.oldUID {
		t.Fatalf("Project at the root = %+v (ok=%v), want a new identity replacing %q", project.Metadata, ok, f.oldUID)
	}
	windows := f.fx.store.registry.WindowsOf(project.Metadata.UID)
	if len(windows) != 1 {
		t.Fatalf("fresh Project Windows = %+v, want exactly one", windows)
	}
	return windows[0]
}

// requireRegistryMatchesLive is the no-stale-Pane check: the Panes the
// Registry declares for the fresh Project are exactly the live Panes of its
// Session -- no shell Pane left behind on either side.
func (f *freshOpenRealTmux) requireRegistryMatchesLive(t *testing.T, window coremetadata.Window) []string {
	t.Helper()
	var declared []string
	for _, pane := range f.fx.store.registry.PanesOf(window.Metadata.UID) {
		declared = append(declared, pane.Metadata.UID)
	}
	for _, agent := range f.fx.store.registry.AgentsOf(window.Metadata.UID) {
		for _, pane := range f.fx.store.registry.PanesOf(agent.Metadata.UID) {
			declared = append(declared, pane.Metadata.UID)
		}
	}
	slices.Sort(declared)
	live := f.fx.sessionPaneUIDs(t, f.session)
	if !slices.Equal(declared, live) {
		t.Fatalf("Registry Panes %v != live Panes %v of session %s\n%s", declared, live, f.session, f.fx.store.snapshot())
	}
	if !f.appTmux.switched || !slices.Equal(f.appTmux.atSwitch, live) {
		t.Fatalf("live Panes at the handoff = %v (handoff seen %v), want the finished Window %v",
			f.appTmux.atSwitch, f.appTmux.switched, live)
	}
	return live
}

// requireClientOn checks the pressing client ended on the one live Pane.
func (f *freshOpenRealTmux) requireClientOn(t *testing.T, window coremetadata.Window, uid string) {
	t.Helper()
	live := realTmuxPaneWithUID(t, f.fx.tmuxPaneIDs(t, window), uid)
	if got := f.fx.clientPane(t); got != live {
		t.Fatalf("pressing client is on Pane %s, want %s", got, live)
	}
}

func (f *freshOpenRealTmux) requireAgentOnly(t *testing.T) {
	t.Helper()
	window := f.freshWindow(t)
	live := f.requireRegistryMatchesLive(t, window)
	if shells := f.fx.store.registry.PanesOf(window.Metadata.UID); len(shells) != 0 {
		t.Fatalf("fresh Window still owns Panes %+v, want the shell replaced", shells)
	}
	agents := f.fx.store.registry.AgentsOf(window.Metadata.UID)
	if len(agents) != 1 {
		t.Fatalf("fresh Window Agents = %+v, want exactly one", agents)
	}
	panes := f.fx.store.registry.PanesOf(agents[0].Metadata.UID)
	if len(panes) != 1 || panes[0].Spec.Role != coremetadata.PaneRoleAgent {
		t.Fatalf("Agent Panes = %+v, want exactly one Agent Pane", panes)
	}
	if !slices.Equal(live, []string{panes[0].Metadata.UID}) {
		t.Fatalf("live Panes = %v, want only the Agent Pane %s", live, panes[0].Metadata.UID)
	}
	if stored, _ := f.fx.store.registry.Window(window.Metadata.UID); stored != nil {
		window = *stored
	}
	if window.Spec.AnchorPaneRef != panes[0].Metadata.UID {
		t.Fatalf("Window anchor = %q, want the Agent Pane %q", window.Spec.AnchorPaneRef, panes[0].Metadata.UID)
	}
	if strings.TrimSpace(window.Spec.DefaultShellPaneRef) != "" {
		t.Fatalf("Window default shell = %q, want none after the shell was replaced", window.Spec.DefaultShellPaneRef)
	}
	f.requireClientOn(t, window, panes[0].Metadata.UID)
}

func (f *freshOpenRealTmux) requireShellOnly(t *testing.T) {
	t.Helper()
	window := f.freshWindow(t)
	live := f.requireRegistryMatchesLive(t, window)
	panes := f.fx.store.registry.PanesOf(window.Metadata.UID)
	if len(panes) != 1 || panes[0].Spec.Role != coremetadata.PaneRoleShell {
		t.Fatalf("fresh Window Panes = %+v, want exactly one shell Pane", panes)
	}
	if agents := f.fx.store.registry.AgentsOf(window.Metadata.UID); len(agents) != 0 {
		t.Fatalf("fresh Window Agents = %+v, want none", agents)
	}
	if !slices.Equal(live, []string{panes[0].Metadata.UID}) {
		t.Fatalf("live Panes = %v, want only the shell Pane %s", live, panes[0].Metadata.UID)
	}
	f.requireClientOn(t, window, panes[0].Metadata.UID)
}

// TestFreshOpenAsksFirstAndFillsTheFirstPaneThroughRealTmux is the live half
// of the fresh open's launch default, for both kinds of fresh open: a
// registered closed Project and a root no Project declares. The question is
// asked while nothing of the open exists; the answer is in the fresh Window
// before the pressing client is moved onto it; and the Registry and the live
// Session agree on exactly one Pane -- the Agent for a provider answer, the
// shell for a shell answer or a closed picker.
func TestFreshOpenAsksFirstAndFillsTheFirstPaneThroughRealTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	claude := &agentPaneIntent{producer: canonicalProducerProviderPicker, provider: aiModeClaude, placement: "right"}
	for _, registered := range []bool{true, false} {
		kind := "unregistered root"
		if registered {
			kind = "registered closed Project"
		}
		t.Run(kind+"/mode claude leaves exactly the Agent Pane", func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			fresh := newFreshOpenRealTmux(t, ctx, registered, aiModeClaude)

			fresh.open(t, ctx)

			fresh.requireAgentOnly(t)
		})
		t.Run(kind+"/mode selective opens the chosen Agent", func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			fresh := newFreshOpenRealTmux(t, ctx, registered, aiModeSelective)
			fresh.answerPicker(t, claude)

			fresh.open(t, ctx)

			if fresh.answered != 1 {
				t.Fatalf("launch picker opened %d times, want once", fresh.answered)
			}
			fresh.requireAgentOnly(t)
		})
		t.Run(kind+"/mode selective closed keeps the shell Pane", func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			fresh := newFreshOpenRealTmux(t, ctx, registered, aiModeSelective)
			fresh.answerPicker(t, nil)

			fresh.open(t, ctx)

			if fresh.answered != 1 {
				t.Fatalf("launch picker opened %d times, want once", fresh.answered)
			}
			fresh.requireShellOnly(t)
		})
		t.Run(kind+"/mode shell keeps the shell Pane", func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			fresh := newFreshOpenRealTmux(t, ctx, registered, aiModeShell)

			fresh.open(t, ctx)

			fresh.requireShellOnly(t)
		})
	}
}
