package app

import (
	"context"
	"errors"
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
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// sleepAgentLauncher is the fake provider launcher with a provider process
// that stays up: the Agent Pane runs `sleep` instead of any real provider.
type sleepAgentLauncher struct{ *fakeAgentLauncher }

func (l sleepAgentLauncher) PlanAgentLaunch(provider string, workspace coremetadata.AgentWorkspace, payload []string) (string, []string, error) {
	title, _, err := l.fakeAgentLauncher.PlanAgentLaunch(provider, workspace, payload)
	return title, []string{"sleep", "600"}, err
}

// splitFocusRealTmux is one isolated real tmux server holding a managed
// Project Window, with a control-mode client attached to it, and the canonical
// create wired onto that server the way newCreateCommand wires it.
type splitFocusRealTmux struct {
	tmux      func(args ...string) (string, error)
	socket    string
	serverPID string
	env       []string
	windowID  string
	originID  string
	client    string
	store     *fakeResourceStore
	newCreate func() *createCommand
	physical  explicitTmuxRunner
}

func newSplitFocusRealTmux(t *testing.T, ctx context.Context) *splitFocusRealTmux {
	t.Helper()
	root, err := os.MkdirTemp("", "psf-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if root, err = filepath.EvalSymlinks(root); err != nil {
		t.Fatal(err)
	}
	environment := []string{"TMUX_TMPDIR=" + root}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "TMUX" || key == "TMUX_PANE" || key == "TMUX_TMPDIR" || key == runtimeMutationAnchorPaneEnv {
			continue
		}
		environment = append(environment, entry)
	}
	const logical = "psf"
	socketDir := filepath.Join(root, fmt.Sprintf("tmux-%d", os.Getuid()))
	if err := os.Mkdir(socketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(socketDir, logical)
	projectRoot := filepath.Join(root, "project")
	if err := os.Mkdir(projectRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	tmux := func(args ...string) (string, error) {
		command := exec.CommandContext(ctx, "tmux", append([]string{"-S", socket, "-f", "/dev/null"}, args...)...)
		command.Env = environment
		out, err := command.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	const sessionName, projectUID, windowUID, paneUID = "split-focus", "prj-split-focus", "win-split-focus", "pan-split-focus"
	created, err := tmux("new-session", "-d", "-s", sessionName, "-n", "main", "-c", projectRoot,
		"-P", "-F", "#{session_id}\t#{window_id}\t#{pane_id}\t#{pid}\t#{socket_path}", "tail", "-f", "/dev/null")
	if err != nil {
		t.Fatalf("start isolated tmux: %v: %s", err, created)
	}
	fields := strings.Split(created, "\t")
	var sessionID, windowID, paneID, serverPID string
	if len(fields) == 5 {
		sessionID, windowID, paneID, serverPID = fields[0], fields[1], fields[2], fields[3]
	}
	// Registered before the receipt is judged and after the root removal, so
	// LIFO kills the server while its socket still exists.
	killRealTmuxServerOnCleanup(t, environment, socket, realTmuxServerPID(t, serverPID))
	if len(fields) != 5 || exactTmuxHandle(sessionID, "$") == "" || exactTmuxHandle(windowID, "@") == "" ||
		exactTmuxHandle(paneID, "%") == "" || fields[4] != socket {
		t.Fatalf("isolated tmux receipt = %q, want session/window/pane/pid on %s", created, socket)
	}
	for _, option := range [][]string{
		{"-g", tmuxopts.AppGlobal, "1"},
		{"-g", runtimeMutationSocketNameOption, logical},
		// A split with no command runs this instead of the operator's shell.
		{"-g", "default-command", "exec sleep 600"},
		{"-t", sessionID, tmuxopts.ProjectUIDSession, projectUID},
		{"-t", sessionID, tmuxopts.ProjectPathSession, projectRoot},
		{"-w", "-t", windowID, tmuxopts.AutomaticRenameWindow, "off"},
		{"-w", "-t", windowID, tmuxopts.WindowUID, windowUID},
		{"-w", "-t", windowID, tmuxopts.WindowName, "main"},
		{"-p", "-t", paneID, tmuxopts.PaneUID, paneUID},
	} {
		if out, err := tmux(append([]string{"set-option"}, option...)...); err != nil {
			t.Fatalf("seed isolated tmux %q: %v: %s", option, err, out)
		}
	}

	ownedBy := func(kind coremetadata.Kind, uid string) *coremetadata.OwnerRef {
		return &coremetadata.OwnerRef{Kind: kind, UID: uid}
	}
	registry := coremetadata.NewRegistry()
	registry.Projects = []coremetadata.Project{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindProject,
		Metadata: coremetadata.ObjectMeta{UID: projectUID, Name: "split-focus", CreatedAt: resourceFixtureClock},
		Spec:     coremetadata.ProjectSpec{Root: projectRoot, PrimaryWindowRef: windowUID},
		Status:   coremetadata.ProjectStatus{Session: &coremetadata.SessionProjection{Name: sessionName, Live: true}},
	}}
	registry.Windows = []coremetadata.Window{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
		Metadata: coremetadata.ObjectMeta{UID: windowUID, Name: "main", OwnerRef: ownedBy(coremetadata.KindProject, projectUID), CreatedAt: resourceFixtureClock},
		Spec:     coremetadata.WindowSpec{AnchorPaneRef: paneUID},
	}}
	registry.Panes = []coremetadata.Pane{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
		Metadata: coremetadata.ObjectMeta{UID: paneUID, Name: "shell", OwnerRef: ownedBy(coremetadata.KindWindow, windowUID), CreatedAt: resourceFixtureClock},
		Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell, CWD: projectRoot},
	}}
	registry.NameReservations = []coremetadata.NameReservation{
		{Scope: "", Kind: coremetadata.KindProject, Name: "split-focus", UID: projectUID},
		{Scope: projectUID, Kind: coremetadata.KindWindow, Name: "main", UID: windowUID},
		{Scope: projectUID, Kind: coremetadata.KindPane, Name: "shell", UID: paneUID},
	}
	registry = registry.Normalize()
	if err := registry.Validate(); err != nil {
		t.Fatalf("split-focus fixture is not a valid Registry: %v", err)
	}
	store := &fakeResourceStore{registry: registry, dirs: map[string]bool{projectRoot: true}, now: resourceFixtureClock}

	// The run-shell job a key or menu item starts inherits TMUX and TMUX_PANE
	// from the exact client Pane.
	inherited := map[string]string{"TMUX": socket + "," + serverPID + ",0", "TMUX_PANE": paneID}
	lookupEnv := func(key string) string { return inherited[key] }
	runner := shellTmuxExecRunner{env: func() []string { return environment }}
	physical := explicitTmuxRunner{runner: runner, target: tmuxTransport{Kind: tmuxSocketPath, Value: socket, Source: tmuxSocketPathSource}}
	newCreate := func() *createCommand {
		target := tmuxTransport{Kind: tmuxSocketName, Value: logical, Source: tmuxSocketNameSource}
		routed := explicitTmuxRunner{runner: runner, target: target}
		client := defaultTmuxClientWithRunner(routed)
		command := &createCommand{
			store:      store.store(),
			reconciler: newRegistryReconciler(routed, client),
			runtime: &materializer{
				runner: routed, mirror: intmetadata.NewMirror(routed), sessions: client, target: target,
				warn: testWarnWriter{t}, lookupEnv: lookupEnv,
				// No supervisor binary in a unit test: the Pane runs its command
				// directly.
				executable: func() (string, error) { return "", errors.New("no supervisor in this test") },
			},
			anchorTarget: func(anchor string) activeTargetLookup {
				return anchoredActiveTargetLookup(anchor, lookupEnv, intmetadata.NewMirror(physical))
			},
			agents: sleepAgentLauncher{newFakeAgentLauncher()},
			resolveWorkspace: func(coremetadata.Registry, coremetadata.Project, string, string, []string) (coremetadata.AgentWorkspace, error) {
				return coremetadata.AgentWorkspace{CWD: projectRoot}, nil
			},
			shell:          "/bin/sh",
			sessionNameFor: filepath.Base,
			newOperationID: newCreateOperationID,
			now:            time.Now,
			newGeneration:  coremetadata.NewGeneration,
		}
		bind := func(ctx context.Context, explicit bool) error {
			route, err := resolveInvocationRuntimeMutationRouteWithPolicy(ctx, runner, lookupEnv, command.routeAnchor, explicit)
			if err != nil {
				return err
			}
			exact := explicitTmuxRunner{runner: runner, target: route.target}
			client := defaultTmuxClientWithSocketRunner(exact, route.socketName)
			command.reconciler = newRegistryReconcilerWithRoute(exact, client, route)
			// Keep the reconciler off the real $HOME Project discovery.
			command.reconciler.discoverRoots = func() ([]string, error) { return nil, nil }
			command.runtime.runner = exact
			command.runtime.mirror = intmetadata.NewMirror(exact)
			command.runtime.sessions = client
			command.runtime.target = route.target
			command.runtime.expectedSocketPath = route.expectedSocketPath
			command.runtime.socketName = route.socketName
			command.runtime.routeAuthority = route.authority
			return nil
		}
		command.bindRuntime = func(ctx context.Context) error { return bind(ctx, false) }
		command.bindExplicitRuntime = func(ctx context.Context) error { return bind(ctx, true) }
		return command
	}

	fx := &splitFocusRealTmux{
		tmux: tmux, socket: socket, serverPID: serverPID, env: environment,
		windowID: windowID, originID: paneID, store: store, newCreate: newCreate, physical: physical,
	}
	fx.client = fx.attachControlClient(t, sessionID)
	return fx
}

// attachControlClient attaches a control-mode client -- a real attached tmux
// client with no terminal -- to the Session and returns its exact name.
func (fx *splitFocusRealTmux) attachControlClient(t *testing.T, sessionID string) string {
	t.Helper()
	before, _ := fx.tmux("list-clients", "-F", "#{client_name}")
	attach := exec.Command("tmux", "-S", fx.socket, "-C", "attach-session", "-t", sessionID)
	attach.Env = fx.env
	attach.Stdout = io.Discard
	stdin, err := attach.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := attach.Start(); err != nil {
		t.Fatalf("attach control client: %v", err)
	}
	// Registered after the server kill, so it runs first: the client detaches
	// before its server goes away.
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = attach.Process.Kill()
		_ = attach.Wait()
	})
	// Room for every split the test makes. (tmux 3.6 exits on the next
	// new-session after a `window-size manual` resize, so the client sets it.)
	if _, err := io.WriteString(stdin, "refresh-client -C 320x120\n"); err != nil {
		t.Fatalf("size control client: %v", err)
	}
	known := strings.Fields(before)
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
		out, err := fx.tmux("list-clients", "-F", "#{client_name}")
		if err != nil {
			continue
		}
		for name := range strings.FieldsSeq(out) {
			if !slices.Contains(known, name) {
				return name
			}
		}
	}
	t.Fatal("control-mode client never appeared in list-clients")
	return ""
}

func (fx *splitFocusRealTmux) panes(t *testing.T) []string {
	t.Helper()
	out, err := fx.tmux("list-panes", "-t", fx.windowID, "-F", "#{pane_id}")
	if err != nil {
		t.Fatalf("list panes: %v: %s", err, out)
	}
	return strings.Fields(out)
}

func (fx *splitFocusRealTmux) paneActive(t *testing.T, paneID string) string {
	t.Helper()
	out, err := fx.tmux("display-message", "-p", "-t", paneID, "-F", "#{pane_active}")
	if err != nil {
		t.Fatalf("read #{pane_active} of %s: %v: %s", paneID, err, out)
	}
	return out
}

// aiCommand is an AI split producer started from the key the client pressed:
// every tmux read and write it makes reaches the isolated server.
func (fx *splitFocusRealTmux) aiCommand(t *testing.T) *aiCommand {
	t.Helper()
	home := t.TempDir()
	enableAgents(t, home, "codex", "claude")
	ai := testAICommand(home)
	env := map[string]string{
		"HOME":                         home,
		"TMUX":                         fx.socket + "," + fx.serverPID + ",0",
		"TMUX_SPLIT_TARGET_PANE":       fx.originID,
		canonicalCreateTargetClientEnv: fx.client,
	}
	ai.lookupEnv = func(key string) string { return env[key] }
	runTmux := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" {
			return nil, errors.New("unexpected executable " + name)
		}
		command := exec.CommandContext(ctx, "tmux", append([]string{"-S", fx.socket}, args...)...)
		command.Env = fx.env
		out, err := command.Output()
		if err != nil {
			return nil, fmt.Errorf("tmux %s: %w", strings.Join(args, " "), err)
		}
		return out, nil
	}
	ai.readCommand = runTmux
	ai.runCommand = func(ctx context.Context, name string, args ...string) error {
		_, err := runTmux(ctx, name, args...)
		return err
	}
	ai.panes = fx.newCreate()
	return ai
}

// TestUISplitFocusesTheNewPaneThroughRealTmux is C-2 acceptance 1 on a real
// server: after the launch-default key, a provider direct key, and the pane
// menu split commit, the new Pane is its Window's active Pane
// (#{pane_active}=1) for the attached pressing client on that Window. With the
// pressing client on another Window, the pane menu split keeps the origin Pane
// active.
func TestUISplitFocusesTheNewPaneThroughRealTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	fx := newSplitFocusRealTmux(t, ctx)

	split := func(t *testing.T, run func() error) string {
		t.Helper()
		if out, err := fx.tmux("select-pane", "-t", fx.originID); err != nil {
			t.Fatalf("reset focus to origin: %v: %s", err, out)
		}
		before := fx.panes(t)
		if err := run(); err != nil {
			t.Fatalf("split error = %v", err)
		}
		var added []string
		for _, pane := range fx.panes(t) {
			if !slices.Contains(before, pane) {
				added = append(added, pane)
			}
		}
		if len(added) != 1 {
			t.Fatalf("split added Panes %v, want exactly one", added)
		}
		return added[0]
	}
	for _, test := range []struct {
		name string
		run  func(t *testing.T) error
	}{
		{name: "launch-default key", run: func(t *testing.T) error {
			ai := fx.aiCommand(t)
			if err := ai.setMode(aiModeClaude); err != nil {
				t.Fatalf("set saved mode: %v", err)
			}
			return ai.runLaunchDefault([]string{"right"}, ioDiscard{})
		}},
		{name: "provider direct key", run: func(t *testing.T) error {
			return fx.aiCommand(t).runDirectProvider([]string{aiModeClaude, "down"}, ioDiscard{})
		}},
		{name: "pane menu split", run: func(*testing.T) error {
			menu := &tmuxCommand{runner: fx.physical, paneMenuCreate: fx.newCreate().createFromIntent}
			return menu.runPaneMenuAction([]string{"--client", fx.client, "split-right", fx.originID}, ioDiscard{}, ioDiscard{})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			created := split(t, func() error { return test.run(t) })
			if got := fx.paneActive(t, created); got != "1" {
				t.Fatalf("new Pane %s #{pane_active} = %q, want 1", created, got)
			}
			if got := fx.paneActive(t, fx.originID); got != "0" {
				t.Fatalf("origin Pane %s #{pane_active} = %q, want 0", fx.originID, got)
			}
		})
	}

	t.Run("pane menu split with the client on another Window", func(t *testing.T) {
		elsewhere, err := fx.tmux("new-session", "-d", "-s", "elsewhere", "-P", "-F", "#{window_id}", "tail", "-f", "/dev/null")
		if err != nil || exactTmuxHandle(elsewhere, "@") == "" {
			t.Fatalf("open another Session: %v: %s", err, elsewhere)
		}
		if out, err := fx.tmux("switch-client", "-c", fx.client, "-t", "elsewhere"); err != nil {
			t.Fatalf("move the pressing client away: %v: %s", err, out)
		}
		other := elsewhere
		shown, err := fx.tmux("list-clients", "-F", "#{client_name}\t#{window_id}")
		if err != nil || !strings.Contains(shown, fx.client+"\t"+other) {
			t.Fatalf("pressing client view = %q (%v), want %s on %s", shown, err, fx.client, other)
		}
		created := split(t, func() error {
			menu := &tmuxCommand{runner: fx.physical, paneMenuCreate: fx.newCreate().createFromIntent}
			return menu.runPaneMenuAction([]string{"--client", fx.client, "split-down", fx.originID}, ioDiscard{}, ioDiscard{})
		})
		if got := fx.paneActive(t, created); got != "0" {
			t.Fatalf("new Pane %s #{pane_active} = %q, want 0 for a client on another Window", created, got)
		}
		if got := fx.paneActive(t, fx.originID); got != "1" {
			t.Fatalf("origin Pane %s #{pane_active} = %q, want it to stay active", fx.originID, got)
		}
		if shown, _ := fx.tmux("list-clients", "-F", "#{client_name}\t#{window_id}"); !strings.Contains(shown, fx.client+"\t"+other) {
			t.Fatalf("pressing client was moved: %q", shown)
		}
	})
}
