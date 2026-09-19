package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// sleepResumeLauncher is the fake resume launcher whose resumed provider stays
// up as `sleep`, so no real provider binary ever runs on the isolated server.
type sleepResumeLauncher struct{ *fakeResumeLauncher }

func (l sleepResumeLauncher) PlanAgentResume(provider string, workspace coremetadata.AgentWorkspace, conversationID string, annotations map[string]string) (agentResumeLaunch, error) {
	launch, err := l.fakeResumeLauncher.PlanAgentResume(provider, workspace, conversationID, annotations)
	launch.argv = []string{"sleep", "600"}
	return launch, err
}

// projectIntentRealTmux is one isolated app-owned tmux server on the default
// logical socket, reached the way a process outside tmux reaches it (no TMUX,
// no TMUX_PANE), holding one live Project while a second Project is stopped.
type projectIntentRealTmux struct {
	tmux    func(args ...string) (string, error)
	store   *fakeResourceStore
	create  *createCommand
	resumes *fakeResumeLauncher
	// warnings is everything the materializer warned, rollback included.
	warnings *bytes.Buffer
}

const (
	projectIntentLiveUID     = "prj-intent-live"
	projectIntentStoppedUID  = "prj-intent-stopped"
	projectIntentStoppedName = "intent-stopped"
)

func newProjectIntentRealTmux(t *testing.T, ctx context.Context) *projectIntentRealTmux {
	t.Helper()
	root, err := os.MkdirTemp("", "ppi-")
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
	// A stray call that ignored every explicit route would fall back to the
	// process environment; point that at the isolated root as well.
	t.Setenv("TMUX_TMPDIR", root)
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	socketDir := filepath.Join(root, fmt.Sprintf("tmux-%d", os.Getuid()))
	if err := os.Mkdir(socketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// The default app socket name, so the create's own `-L projmux` probe
	// finds this server and nothing else.
	socket := filepath.Join(socketDir, defaultAppSocket)
	liveRoot, stoppedRoot := filepath.Join(root, "live"), filepath.Join(root, "stopped")
	for _, dir := range []string{liveRoot, stoppedRoot} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tmux := func(args ...string) (string, error) {
		command := exec.CommandContext(ctx, "tmux", append([]string{"-S", socket, "-f", "/dev/null"}, args...)...)
		command.Env = environment
		out, err := command.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	const liveSession, liveUID, liveWindowUID, livePaneUID = "intent-live", projectIntentLiveUID, "win-intent-live", "pan-intent-live"
	created, err := tmux("new-session", "-d", "-s", liveSession, "-n", "main", "-c", liveRoot,
		"-P", "-F", "#{session_id}\t#{window_id}\t#{pane_id}\t#{pid}\t#{socket_path}", "tail", "-f", "/dev/null")
	if err != nil {
		t.Fatalf("start isolated tmux: %v: %s", err, created)
	}
	fields := strings.Split(created, "\t")
	var sessionID, windowID, paneID, serverPID string
	if len(fields) == 5 {
		sessionID, windowID, paneID, serverPID = fields[0], fields[1], fields[2], fields[3]
	}
	killRealTmuxServerOnCleanup(t, environment, socket, realTmuxServerPID(t, serverPID))
	if len(fields) != 5 || exactTmuxHandle(sessionID, "$") == "" || fields[4] != socket {
		t.Fatalf("isolated tmux receipt = %q, want session/window/pane/pid on %s", created, socket)
	}
	for _, option := range [][]string{
		{"-g", tmuxopts.AppGlobal, "1"},
		{"-g", runtimeMutationSocketNameOption, defaultAppSocket},
		// A Pane created with no command runs this instead of a shell.
		{"-g", "default-command", "exec sleep 600"},
		{"-t", sessionID, tmuxopts.ProjectUIDSession, liveUID},
		{"-t", sessionID, tmuxopts.ProjectPathSession, liveRoot},
		{"-w", "-t", windowID, tmuxopts.AutomaticRenameWindow, "off"},
		{"-w", "-t", windowID, tmuxopts.WindowUID, liveWindowUID},
		{"-w", "-t", windowID, tmuxopts.WindowName, "main"},
		{"-p", "-t", paneID, tmuxopts.PaneUID, livePaneUID},
	} {
		if out, err := tmux(append([]string{"set-option"}, option...)...); err != nil {
			t.Fatalf("seed isolated tmux %q: %v: %s", option, err, out)
		}
	}

	ownedBy := func(kind coremetadata.Kind, uid string) *coremetadata.OwnerRef {
		return &coremetadata.OwnerRef{Kind: kind, UID: uid}
	}
	const stoppedWindowUID, stoppedPaneUID = "win-intent-stopped", "pan-intent-stopped"
	registry := coremetadata.NewRegistry()
	registry.Projects = []coremetadata.Project{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindProject,
		Metadata: coremetadata.ObjectMeta{UID: liveUID, Name: liveSession, CreatedAt: resourceFixtureClock},
		Spec:     coremetadata.ProjectSpec{Root: liveRoot, PrimaryWindowRef: liveWindowUID},
		Status:   coremetadata.ProjectStatus{Session: &coremetadata.SessionProjection{Name: liveSession, Live: true}},
	}, {
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindProject,
		Metadata: coremetadata.ObjectMeta{UID: projectIntentStoppedUID, Name: projectIntentStoppedName, CreatedAt: resourceFixtureClock},
		Spec:     coremetadata.ProjectSpec{Root: stoppedRoot, PrimaryWindowRef: stoppedWindowUID},
		Status:   coremetadata.ProjectStatus{Session: &coremetadata.SessionProjection{Name: projectIntentStoppedName, Live: false}},
	}}
	registry.Windows = []coremetadata.Window{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
		Metadata: coremetadata.ObjectMeta{UID: liveWindowUID, Name: "main", OwnerRef: ownedBy(coremetadata.KindProject, liveUID), CreatedAt: resourceFixtureClock},
		Spec:     coremetadata.WindowSpec{AnchorPaneRef: livePaneUID},
	}, {
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
		Metadata: coremetadata.ObjectMeta{UID: stoppedWindowUID, Name: "main", OwnerRef: ownedBy(coremetadata.KindProject, projectIntentStoppedUID), CreatedAt: resourceFixtureClock},
		Spec:     coremetadata.WindowSpec{AnchorPaneRef: stoppedPaneUID},
	}}
	registry.Panes = []coremetadata.Pane{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
		Metadata: coremetadata.ObjectMeta{UID: livePaneUID, Name: "shell", OwnerRef: ownedBy(coremetadata.KindWindow, liveWindowUID), CreatedAt: resourceFixtureClock},
		Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell, CWD: liveRoot},
	}, {
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
		Metadata: coremetadata.ObjectMeta{UID: stoppedPaneUID, Name: "shell", OwnerRef: ownedBy(coremetadata.KindWindow, stoppedWindowUID), CreatedAt: resourceFixtureClock},
		Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell, CWD: stoppedRoot},
	}}
	registry.NameReservations = []coremetadata.NameReservation{
		{Scope: "", Kind: coremetadata.KindProject, Name: liveSession, UID: liveUID},
		{Scope: "", Kind: coremetadata.KindProject, Name: projectIntentStoppedName, UID: projectIntentStoppedUID},
		{Scope: liveUID, Kind: coremetadata.KindWindow, Name: "main", UID: liveWindowUID},
		{Scope: liveUID, Kind: coremetadata.KindPane, Name: "shell", UID: livePaneUID},
		{Scope: projectIntentStoppedUID, Kind: coremetadata.KindWindow, Name: "main", UID: stoppedWindowUID},
		{Scope: projectIntentStoppedUID, Kind: coremetadata.KindPane, Name: "shell", UID: stoppedPaneUID},
	}
	registry = registry.Normalize()
	if err := registry.Validate(); err != nil {
		t.Fatalf("Project intent fixture is not a valid Registry: %v", err)
	}
	store := &fakeResourceStore{registry: registry, dirs: map[string]bool{liveRoot: true, stoppedRoot: true}, now: resourceFixtureClock}

	// A process outside tmux -- a web server -- inherits nothing that names a
	// server or a Pane.
	lookupEnv := func(string) string { return "" }
	runner := shellTmuxExecRunner{env: func() []string { return environment }}
	target := tmuxTransport{Kind: tmuxSocketName, Value: defaultAppSocket, Source: tmuxSocketNameSource}
	routed := explicitTmuxRunner{runner: runner, target: target}
	resumes := newFakeResumeLauncher()
	warnings := &bytes.Buffer{}
	command := &createCommand{
		store:      store.store(),
		reconciler: newRegistryReconciler(routed, inttmux.NewClient(routed, inttmux.WithSocketName(defaultAppSocket))),
		runtime: &materializer{
			runner: routed, mirror: intmetadata.NewMirror(routed),
			sessions: inttmux.NewClient(routed, inttmux.WithSocketName(defaultAppSocket)), target: target,
			warn: io.MultiWriter(testWarnWriter{t}, warnings), lookupEnv: lookupEnv,
			executable: func() (string, error) { return "", errors.New("no supervisor in this test") },
		},
		agents:  sleepAgentLauncher{newFakeAgentLauncher()},
		resumes: sleepResumeLauncher{resumes},
		resolveWorkspace: func(_ coremetadata.Registry, project coremetadata.Project, _, _ string, _ []string) (coremetadata.AgentWorkspace, error) {
			return coremetadata.AgentWorkspace{CWD: project.Spec.Root}, nil
		},
		shell:          "/bin/sh",
		sessionNameFor: filepath.Base,
		newOperationID: newCreateOperationID,
		now:            time.Now,
		newGeneration:  coremetadata.NewGeneration,
	}
	bind := func(ctx context.Context, explicit bool) error {
		if !explicit {
			return errors.New("the Project-scoped intent bound the natural runtime route")
		}
		route, err := resolveInvocationRuntimeMutationRouteWithPolicy(ctx, runner, lookupEnv, command.routeAnchor, explicit)
		if err != nil {
			return err
		}
		exact := explicitTmuxRunner{runner: runner, target: route.target}
		// No lifecycle hook runner: the operator's own post-create hooks must
		// never run from a test.
		client := inttmux.NewClient(exact, inttmux.WithSocketName(route.socketName))
		command.reconciler = newRegistryReconcilerWithRoute(exact, client, route)
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
	return &projectIntentRealTmux{tmux: tmux, store: store, create: command, resumes: resumes, warnings: warnings}
}

func (fx *projectIntentRealTmux) sessionNames(t *testing.T) []string {
	t.Helper()
	out, err := fx.tmux("list-sessions", "-F", "#{session_name}")
	if err != nil {
		t.Fatalf("list sessions: %v: %s", err, out)
	}
	return strings.Fields(out)
}

func (fx *projectIntentRealTmux) liveWindowCount(t *testing.T) int {
	t.Helper()
	out, err := fx.tmux("list-windows", "-a", "-F", "#{window_id}")
	if err != nil {
		t.Fatalf("list windows: %v: %s", err, out)
	}
	return len(strings.Fields(out))
}

// TestProjectWindowIntentResumesIntoAStoppedProjectThroughRealTmux is the live
// half of the Project scope, from a caller outside tmux: the stopped Project's
// session starts, the Agent's resumed conversation is recorded, and the new
// Window holds exactly one live Pane, the Agent's. An Agent that cannot be
// opened after the Window -- and on a stopped Project the session -- exists
// leaves neither behind.
//
// The stopped failure rows are the regression guard of the shared create
// rollback: killing a Window's last Pane removes that Window, and the adopted
// Window's last Pane removes the session, so later kills in the same plan find
// their target already gone. Rollback used to stop there with "rollback
// stopped before an unguarded runtime write" and leave the started session
// running; fresh `create window --project` had the same defect.
func TestProjectWindowIntentResumesIntoAStoppedProjectThroughRealTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	const conversation = "claude-real-tmux-session"
	answer := agentPaneIntent{producer: canonicalProducerResumePicker, provider: aiModeClaude, placement: "right", conversationID: conversation}

	t.Run("commits the session, the Window, and the resumed Agent", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		fx := newProjectIntentRealTmux(t, ctx)
		before := map[string]bool{}
		for _, window := range fx.store.registry.Windows {
			before[window.Metadata.UID] = true
		}

		placement, err := fx.create.createWindowFromIntent(windowCreateIntent{projectUID: projectIntentStoppedUID, answer: answer}, ioDiscard{}, ioDiscard{})
		if err != nil {
			t.Fatalf("Project Window intent: %v", err)
		}
		if names := fx.sessionNames(t); len(names) != 2 || !strings.Contains(strings.Join(names, " "), projectIntentStoppedName) {
			t.Fatalf("live sessions = %v, want the live Project plus %s", names, projectIntentStoppedName)
		}
		project, _ := fx.store.registry.Project(projectIntentStoppedUID)
		if project.Status.Session == nil || !project.Status.Session.Live || project.Status.Session.Name != projectIntentStoppedName {
			t.Fatalf("stopped Project status.session = %+v, want %s live", project.Status.Session, projectIntentStoppedName)
		}
		var created []coremetadata.Window
		for _, window := range fx.store.registry.Windows {
			if !before[window.Metadata.UID] {
				created = append(created, window)
			}
		}
		if len(created) != 1 || created[0].Metadata.OwnerUID() != projectIntentStoppedUID {
			t.Fatalf("created Windows = %+v, want one under %s", created, projectIntentStoppedUID)
		}
		window := created[0]
		agents := fx.store.registry.AgentsOf(window.Metadata.UID)
		if len(agents) != 1 || agents[0].Status.SessionRef == nil || agents[0].Status.SessionRef.ConversationID() != conversation {
			t.Fatalf("created Window Agents = %+v, want one resumed %s", agents, conversation)
		}
		if shells := fx.store.registry.PanesOf(window.Metadata.UID); len(shells) != 0 {
			t.Fatalf("created Window still owns shell Panes %+v", shells)
		}
		out, err := fx.tmux("list-panes", "-t", window.Status.RuntimeID, "-F", "#{pane_id}\t#{"+tmuxopts.PaneUID+"}")
		if err != nil {
			t.Fatalf("list panes of %s: %v: %s", window.Status.RuntimeID, err, out)
		}
		paneID, paneUID, _ := strings.Cut(out, "\t")
		if strings.Contains(out, "\n") || paneUID != agents[0].Status.PaneRef || paneID != placement.paneID {
			t.Fatalf("live Panes of the created Window = %q, want exactly the Agent Pane %s (%s)", out, agents[0].Status.PaneRef, placement.paneID)
		}
		if window.Status.RuntimeSessionID != placement.sessionID || window.Status.RuntimeID != placement.windowID {
			t.Fatalf("placement = %+v, Window binding %s/%s", placement, window.Status.RuntimeSessionID, window.Status.RuntimeID)
		}
		if len(fx.resumes.plans) != 1 || fx.resumes.plans[0].conversationID != conversation {
			t.Fatalf("resume plans = %+v, want one of %s", fx.resumes.plans, conversation)
		}
	})

	for _, row := range []struct {
		name string
		// create runs the failing create; it returns its error.
		create func(t *testing.T, fx *projectIntentRealTmux) error
	}{
		{
			name: "stopped Project intent whose resumed Agent is refused",
			create: func(t *testing.T, fx *projectIntentRealTmux) error {
				fx.resumes.planErr = errors.New("provider refused exact picker conversation")
				_, err := fx.create.createWindowFromIntent(windowCreateIntent{projectUID: projectIntentStoppedUID, answer: answer}, ioDiscard{}, ioDiscard{})
				if err == nil || !strings.Contains(err.Error(), "provider refused exact picker conversation") || !strings.Contains(err.Error(), "nothing was created") {
					t.Fatalf("error = %v, want the refusal and that nothing was created", err)
				}
				return err
			},
		},
		{
			name: "live Project intent whose resumed Agent is refused",
			create: func(t *testing.T, fx *projectIntentRealTmux) error {
				fx.resumes.planErr = errors.New("provider refused exact picker conversation")
				_, err := fx.create.createWindowFromIntent(windowCreateIntent{projectUID: projectIntentLiveUID, answer: answer}, ioDiscard{}, ioDiscard{})
				if err == nil || !strings.Contains(err.Error(), "provider refused exact picker conversation") || !strings.Contains(err.Error(), "nothing was created") {
					t.Fatalf("error = %v, want the refusal and that nothing was created", err)
				}
				return err
			},
		},
		{
			name: "fresh create window on the stopped Project whose commit fails",
			create: func(t *testing.T, fx *projectIntentRealTmux) error {
				fx.create.store.update = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
					fx.store.transactions++
					working := fx.store.registry.Clone()
					if err := fn(&working); err != nil {
						return coremetadata.Registry{}, err
					}
					return coremetadata.Registry{}, errors.New("injected Registry commit failure")
				}
				_, _, err := runRoute(t, fx.create, "window", "--project", "uid:"+projectIntentStoppedUID)
				if err == nil || !strings.Contains(err.Error(), "injected Registry commit failure") {
					t.Fatalf("error = %v, want the injected commit failure", err)
				}
				return err
			},
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			fx := newProjectIntentRealTmux(t, ctx)
			registryBefore := fx.store.snapshot()
			sessionsBefore, windowsBefore := fx.sessionNames(t), fx.liveWindowCount(t)

			row.create(t, fx)
			if fx.store.writes != 0 || fx.store.snapshot() != registryBefore {
				t.Fatalf("failed create wrote the Registry: writes=%d", fx.store.writes)
			}
			if got := fx.sessionNames(t); strings.Join(got, " ") != strings.Join(sessionsBefore, " ") {
				t.Fatalf("live sessions = %v after the failure, want %v: the started session was not rolled back", got, sessionsBefore)
			}
			if got := fx.liveWindowCount(t); got != windowsBefore {
				t.Fatalf("live Windows = %d after the failure, want %d: an adopted or created Window was left", got, windowsBefore)
			}
			if strings.Contains(fx.warnings.String(), "rollback stopped before an unguarded runtime write") {
				t.Fatalf("rollback stopped part way: %q", fx.warnings.String())
			}
		})
	}
}
