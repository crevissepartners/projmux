package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// recordingProjectIdentityMirror records the identity writes one open performed.
//
// It also snapshots the session-executor call log at the moment of each write,
// which is how the ordering contract is asserted without inventing a second
// shared log: a mirror that runs after `ensure:` and before `open:` is the only
// arrangement in which the client is still moved last and never moved into a
// session whose identity failed to land.
type recordingProjectIdentityMirror struct {
	observe      func() []string
	calls        []string
	sessionCalls [][]string
	err          error
}

func (m *recordingProjectIdentityMirror) MirrorProject(_ context.Context, sessionName string, project coremetadata.Project) error {
	m.calls = append(m.calls, "mirror:"+sessionName+":"+project.Metadata.UID+":"+project.Metadata.Name)
	if m.observe != nil {
		m.sessionCalls = append(m.sessionCalls, slices.Clone(m.observe()))
	}
	return m.err
}

// TestFirstOpenMirrorsProjectIdentityOnlyWhenItMintedTheProject is the mirror
// decision table.
//
// Opening an unregistered directory used to mint a Project and then start its
// session through the shipped ensure path, which writes the session's
// `@projmux_project_path` anchor and nothing else. The session therefore carried
// no `@projmux_project_uid` / `@projmux_project_name`, so the next `create` in it
// read a session with a blank identity and refused its own session as foreign.
//
// The table fixes both halves of the answer: the open that mints the Project
// finishes its identity in the same flow, and no other open writes anything. Each
// case runs the whole flow twice on purpose -- the second pass is the idempotence
// criterion, and it must write zero mirror options even though it re-enters
// registration.
func TestFirstOpenMirrorsProjectIdentityOnlyWhenItMintedTheProject(t *testing.T) {
	t.Parallel()

	const sessionName = "workspace"
	// expand fills in the target path a case opens, so the call logs stay quoted
	// verbatim instead of being assembled inside the assertions.
	expand := func(calls []string, target string) []string {
		out := make([]string, 0, len(calls))
		for _, call := range calls {
			out = append(out, strings.ReplaceAll(call, "{target}", target))
		}
		return out
	}

	for _, test := range []struct {
		name string
		// home opens the operator's own home directory instead of a project root.
		home bool
		// reused reports the root as already registered before this open.
		reused bool
		// materialized is what the topology engine reports for this root.
		materialized bool
		// wantFirstPassMirror is the mirror log after the first open.
		wantFirstPassMirror []string
		// wantMirrorObservedCalls is the session-executor log at mirror time.
		wantMirrorObservedCalls []string
		wantTopologyCalls       []string
		wantSessionCalls        []string
	}{
		{
			name:                    "a first open mints the Project and finishes its identity",
			materialized:            true,
			wantFirstPassMirror:     []string{"mirror:workspace:proj-new:workspace"},
			wantMirrorObservedCalls: []string{"authorize:{target}", "ensure:workspace"},
			wantSessionCalls:        []string{"authorize:{target}", "ensure:workspace", "open:workspace"},
		},
		{
			name:              "an already-registered Project converges through the topology engine and mirrors nothing",
			reused:            true,
			materialized:      true,
			wantTopologyCalls: []string{"topology:{target}:workspace"},
			wantSessionCalls:  []string{"authorize:{target}", "open:workspace"},
		},
		{
			name: "opening Home mints no managed identity to mirror",
			home: true,
			// Home declares no Registry topology, so the open falls through to
			// the same ensure path a first open takes -- reached with a zero
			// Project, which is exactly why nothing is written.
			wantTopologyCalls: []string{"topology:{target}:workspace"},
			wantSessionCalls:  []string{"authorize:{target}", "ensure:workspace", "open:workspace"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			home := t.TempDir()
			target := "/srv/work/workspace"
			if test.home {
				target = home
			}
			registrar := &fakeProjectRegistrar{uid: "proj-new", name: sessionName, reused: test.reused}
			topology := &fakeProjectTopologyMaterializer{materialized: test.materialized}
			executor := &capturingSwitchSessionExecutor{}
			mirror := &recordingProjectIdentityMirror{observe: func() []string { return executor.calls }}
			cmd := &switchCommand{
				sessions:         executor,
				identity:         stubSwitchIdentityResolver{name: sessionName},
				homeDir:          func() (string, error) { return home, nil },
				lookupEnv:        func(string) string { return "" },
				projectTopology:  topology,
				projectRegistrar: registrar,
				projectMirror:    mirror,
			}

			if err := cmd.openProjectTarget(context.Background(), target, sessionName); err != nil {
				t.Fatalf("first openProjectTarget() error = %v", err)
			}
			if got, want := mirror.calls, test.wantFirstPassMirror; !equalStrings(got, want) {
				t.Fatalf("first pass mirror writes = %q, want %q", got, want)
			}
			if len(test.wantMirrorObservedCalls) > 0 {
				if len(mirror.sessionCalls) != 1 {
					t.Fatalf("mirror ran %d times, want exactly one identity write", len(mirror.sessionCalls))
				}
				if got, want := mirror.sessionCalls[0], expand(test.wantMirrorObservedCalls, target); !equalStrings(got, want) {
					t.Fatalf("session calls at mirror time = %q, want %q: the mirror must land after ensure and before the client move", got, want)
				}
			}
			if got, want := topology.calls, expand(test.wantTopologyCalls, target); !equalStrings(got, want) {
				t.Fatalf("first pass topology calls = %q, want %q", got, want)
			}
			if got, want := executor.calls, expand(test.wantSessionCalls, target); !equalStrings(got, want) {
				t.Fatalf("first pass session calls = %q, want %q", got, want)
			}

			// The second pass is the convergence half of the criterion. It
			// re-enters registration, which now reuses the Project the first pass
			// minted, so it must write no mirror option at all.
			writesBefore := len(mirror.calls)
			if err := cmd.openProjectTarget(context.Background(), target, sessionName); err != nil {
				t.Fatalf("second openProjectTarget() error = %v", err)
			}
			if got := len(mirror.calls) - writesBefore; got != 0 {
				t.Fatalf("second pass wrote %d mirror options, want 0: %q", got, mirror.calls)
			}
		})
	}
}

// TestAFailedFirstOpenMirrorNeverMovesTheClient keeps the client move last.
//
// A session whose identity is half written is exactly the state the mirror exists
// to prevent, so a failed write is an error and not a warning, and the operator is
// not dropped into the session it describes.
func TestAFailedFirstOpenMirrorNeverMovesTheClient(t *testing.T) {
	t.Parallel()

	registrar := &fakeProjectRegistrar{uid: "proj-new", name: "workspace"}
	executor := &capturingSwitchSessionExecutor{}
	mirror := &recordingProjectIdentityMirror{err: errors.New("no server on this socket")}
	cmd := &switchCommand{
		sessions:         executor,
		identity:         stubSwitchIdentityResolver{name: "workspace"},
		homeDir:          func() (string, error) { return t.TempDir(), nil },
		lookupEnv:        func(string) string { return "" },
		projectRegistrar: registrar,
		projectMirror:    mirror,
	}

	err := cmd.openProjectTarget(context.Background(), "/srv/work/workspace", "workspace")
	if err == nil || !strings.Contains(err.Error(), "mirror Project identity onto tmux session \"workspace\"") {
		t.Fatalf("openProjectTarget() error = %v, want a mirror failure naming the session", err)
	}
	if got, want := executor.calls, []string{"authorize:/srv/work/workspace", "ensure:workspace"}; !equalStrings(got, want) {
		t.Fatalf("calls = %q, want %q: a failed mirror must not open the session", got, want)
	}
}

// TestAnUnwiredFirstOpenMirrorIsANoOp matches how the registrar and the topology
// seam already behave when the process wiring did not supply them: the open still
// happens, it simply mints no managed identity.
func TestAnUnwiredFirstOpenMirrorIsANoOp(t *testing.T) {
	t.Parallel()

	registrar := &fakeProjectRegistrar{uid: "proj-new", name: "workspace"}
	executor := &capturingSwitchSessionExecutor{}
	cmd := &switchCommand{
		sessions:         executor,
		identity:         stubSwitchIdentityResolver{name: "workspace"},
		homeDir:          func() (string, error) { return t.TempDir(), nil },
		lookupEnv:        func(string) string { return "" },
		projectRegistrar: registrar,
	}

	if err := cmd.openProjectTarget(context.Background(), "/srv/work/workspace", "workspace"); err != nil {
		t.Fatalf("openProjectTarget() error = %v", err)
	}
	if got, want := executor.calls, []string{"authorize:/srv/work/workspace", "ensure:workspace", "open:workspace"}; !equalStrings(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

// TestASnapshotStartOfABootstrappedOpenMirrorsNothing is the next-Phase
// encroachment guard on the snapshot rows.
//
// A snapshot restore reaches a session the Registry never declared, and this Phase
// deliberately does not touch it: only the shipped ensure path finishes identity.
// Widening the mirror to the restore rows is a separate decision with its own
// failure modes, so the guard fails if someone quietly makes it here.
func TestASnapshotStartOfABootstrappedOpenMirrorsNothing(t *testing.T) {
	t.Parallel()

	stateHome := t.TempDir()
	store := sessionstate.NewStore(filepath.Join(stateHome, "projmux", "sessions"))
	if err := store.Save(sessionstate.Snapshot{
		Version: sessionstate.Version, Session: "workspace", DefaultCWD: "/srv/work/workspace", SavedAt: time.Now(),
		Windows: []sessionstate.Window{{Index: 0, Panes: []sessionstate.Pane{{Index: 0, CWD: "/srv/work/workspace", Recipe: sessionstate.ShellRecipe()}}}},
	}); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}

	registrar := &fakeProjectRegistrar{uid: "proj-new", name: "workspace"}
	executor := &capturingSwitchSessionExecutor{}
	mirror := &recordingProjectIdentityMirror{}
	cmd := &switchCommand{
		sessions: executor,
		identity: stubSwitchIdentityResolver{name: "workspace"},
		homeDir:  func() (string, error) { return t.TempDir(), nil },
		lookupEnv: func(name string) string {
			if name == "XDG_STATE_HOME" {
				return stateHome
			}
			return ""
		},
		projectRegistrar: registrar,
		projectMirror:    mirror,
	}

	if err := cmd.authorizeAndContinueProjectOpen(context.Background(), "/srv/work/workspace", "workspace",
		projectStartupCandidate{Kind: projectStartupKindLatest}); err != nil {
		t.Fatalf("authorizeAndContinueProjectOpen() error = %v", err)
	}
	// The open did mint the Project, which is what makes this a guard rather than
	// a tautology: the bootstrap flag is set and the snapshot row still writes
	// nothing.
	if got, want := registrar.calls, []string{"/srv/work/workspace"}; !equalStrings(got, want) {
		t.Fatalf("registration calls = %q, want %q", got, want)
	}
	if len(mirror.calls) != 0 {
		t.Fatalf("a snapshot start wrote %q; the snapshot rows are out of scope for the first-open mirror", mirror.calls)
	}
	if got, want := executor.calls, []string{"authorize:/srv/work/workspace", "restore:workspace:autosave", "open:workspace"}; !equalStrings(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

// firstOpenSessionExecutor stands in for inttmux.Client against the fake server.
//
// It does exactly what the shipped ensure path does and no more: it creates the
// session if it is missing and writes the same `@projmux_project_path` anchor the
// real client writes. It deliberately writes no Project identity, because the
// whole question this Phase answers is where that identity comes from.
type firstOpenSessionExecutor struct {
	tmux  *fakeTmux
	calls []string
}

func (e *firstOpenSessionExecutor) EnsureSession(_ context.Context, sessionName, cwd string) error {
	e.calls = append(e.calls, "ensure:"+sessionName)
	if e.tmux.session(sessionName) == nil {
		e.tmux.addSession(sessionName).opts[tmuxopts.ProjectPathSession] = cwd
	}
	return nil
}

func (e *firstOpenSessionExecutor) OpenSession(_ context.Context, sessionName string) error {
	e.calls = append(e.calls, "open:"+sessionName)
	return nil
}

func (e *firstOpenSessionExecutor) SessionExists(_ context.Context, sessionName string) (bool, error) {
	return e.tmux.session(sessionName) != nil, nil
}

// TestFirstOpenOfAnUnregisteredDirectoryLetsCreateOwnItsOwnSession walks the whole
// reported path with a per-step assertion on each hop: an unregistered directory
// is opened, that mints a Project, the ensure path mints the session, the mirror
// finishes its identity, and `create claude` in that session derives its scope and
// succeeds instead of refusing the session it is running in.
func TestFirstOpenOfAnUnregisteredDirectoryLetsCreateOwnItsOwnSession(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	root := filepath.Join(base, "gamma")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	store := newFakeResourceStore(t)
	store.dirs[root] = true
	tmux := newFakeTmux()
	executor := &firstOpenSessionExecutor{tmux: tmux}
	topology := &fakeProjectTopologyMaterializer{materialized: true}
	open := &switchCommand{
		sessions:        executor,
		identity:        stubSwitchIdentityResolver{name: "gamma"},
		homeDir:         func() (string, error) { return base, nil },
		lookupEnv:       func(string) string { return "" },
		projectTopology: topology,
		projectRegistrar: &defaultSwitchProjectRegistrar{
			store:          store.store(),
			shell:          "/bin/zsh",
			sessionNameFor: filepath.Base,
		},
		projectMirror: intmetadata.NewMirror(tmux),
	}

	// Step 1: the open is the gesture that makes the directory a Project.
	if err := open.openProjectTarget(context.Background(), root, "gamma"); err != nil {
		t.Fatalf("openProjectTarget() error = %v", err)
	}
	project, ok := store.registry.ProjectByRoot(root)
	if !ok {
		t.Fatalf("the first open registered no Project for %q: %s", root, store.snapshot())
	}
	if len(topology.calls) != 0 {
		t.Fatalf("a first bootstrap open used the topology engine: %q", topology.calls)
	}

	// Step 2: the session exists, minted by the shipped ensure path.
	session := tmux.session("gamma")
	if session == nil {
		t.Fatalf("the first open minted no session: %s", tmux.state())
	}
	if got := session.opts[tmuxopts.ProjectPathSession]; got != root {
		t.Fatalf("session project path anchor = %q, want %q", got, root)
	}

	// Step 3: the identity mirror finished in the same flow.
	if got, want := session.opts[tmuxopts.ProjectUIDSession], project.Metadata.UID; got != want {
		t.Fatalf("session project uid = %q, want %q", got, want)
	}
	if got, want := session.opts[tmuxopts.ProjectNameSession], project.Metadata.Name; got != want {
		t.Fatalf("session project name = %q, want %q", got, want)
	}

	// Step 4: `create` in that session now owns it. The Window comes first
	// because an Agent is always minted below a Window.
	create, _ := newTestResourceCreateCommand(t, store, tmux)
	if stdout, stderr, err := runRoute(t, create, "window", "--project", project.Metadata.Name, "--name", "main"); err != nil {
		t.Fatalf("create window error = %v (stdout %q stderr %q)", err, stdout, stderr)
	}

	// Step 5: `create claude` derives its scope from the same Project and lands a
	// managed Agent pane, which is what the reported failure could not do.
	agentCreate, launcher := newTestAgentCreateCommand(t, store, tmux)
	if stdout, stderr, err := runRoute(t, agentCreate, "claude", "--project", project.Metadata.Name, "--window", "main"); err != nil {
		t.Fatalf("create claude error = %v (stdout %q stderr %q)", err, stdout, stderr)
	}
	if len(launcher.bound) != 1 {
		t.Fatalf("managed pane bindings = %d, want 1", len(launcher.bound))
	}

	// Step 6: the Agent pane is managed in the read projection.
	stdout, _, err := runRoute(t, newTestListGetCommand(t, store), "panes", "--project", project.Metadata.Name, "-o", "uid")
	if err != nil {
		t.Fatalf("get panes error = %v", err)
	}
	if strings.TrimSpace(stdout) == "" {
		t.Fatalf("get panes for the freshly opened Project listed nothing")
	}
}

// TestFirstOpenMirrorDoesNotRelaxOwnershipRefusals pins the refusal this Phase is
// explicitly not allowed to touch.
//
// Mirroring identity on a first open removes the reason a session projmux minted
// looked foreign; it must not remove the refusal itself. Genuine contamination --
// a blank identity on a session projmux did not mint, a foreign uid, a second
// session claiming the same root -- still fails, with the same wording, before any
// write. The exact strings are quoted here so relaxing the message in a later
// Phase is a deliberate edit to this guard rather than a silent drift.
func TestFirstOpenMirrorDoesNotRelaxOwnershipRefusals(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		setup func(*fakeTmux, *fakeTmuxSession)
		want  string
	}{
		{
			name: "blank identity",
			want: `create: refuse foreign tmux session "alpha": project uid="" root="", want unique uid="prj-alpha" root="/srv/alpha" (uid claims=0, root claims=0)`,
		},
		{
			name: "foreign uid",
			setup: func(_ *fakeTmux, session *fakeTmuxSession) {
				session.opts[tmuxopts.ProjectUIDSession] = "prj-foreign"
				session.opts[tmuxopts.ProjectPathSession] = "/srv/alpha"
			},
			want: `create: refuse foreign tmux session "alpha": project uid="prj-foreign" root="/srv/alpha", want unique uid="prj-alpha" root="/srv/alpha" (uid claims=0, root claims=1)`,
		},
		{
			name: "duplicate root on another session",
			setup: func(tmux *fakeTmux, session *fakeTmuxSession) {
				session.opts[tmuxopts.ProjectUIDSession] = "prj-alpha"
				session.opts[tmuxopts.ProjectPathSession] = "/srv/alpha"
				other := tmux.addSession("other")
				other.opts[tmuxopts.ProjectUIDSession] = "prj-other"
				other.opts[tmuxopts.ProjectPathSession] = "/srv/alpha"
			},
			want: `create: refuse foreign tmux session "alpha": project uid="prj-alpha" root="/srv/alpha", want unique uid="prj-alpha" root="/srv/alpha" (uid claims=1, root claims=2)`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			session := tmux.addSession("alpha")
			if test.setup != nil {
				test.setup(tmux, session)
			}
			registryBefore, runtimeBefore := store.snapshot(), tmux.state()
			create, _ := newTestResourceCreateCommand(t, store, tmux)

			stdout, _, err := runRoute(t, create, "window", "--project", "alpha")
			if err == nil || err.Error() != test.want {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			if stdout != "" {
				t.Fatalf("a refusal wrote %q to stdout", stdout)
			}
			if store.snapshot() != registryBefore || store.writes != 0 || tmux.state() != runtimeBefore {
				t.Fatal("an ownership refusal mutated state")
			}
			if tmux.argvContains("set-option") || tmux.argvContains("new-window") || tmux.argvContains("set-environment") {
				t.Fatalf("an ownership refusal reached a mutation: %v", tmux.calls)
			}
		})
	}
}
