package app

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	intmux "github.com/crevissepartners/projmux/internal/integrations/mux"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// fakeSessionMaterializer stands in for the tmux client's session half.
//
// It reproduces the only two behaviors the create routes depend on: a session
// that already exists is reused untouched, and a pre-create hook refusal fails
// before anything is created. A post-create hook failure is modeled the way the
// real client handles it -- logged, ignored, creation continues.
type fakeSessionMaterializer struct {
	tmux               *fakeTmux
	preCreateErr       error
	createErr          error
	createErrResult    bool
	postCreate         func()
	startup            func()
	beforeEnsureResult func()
	created            []string
	initialPaneCWD     string
}

func (f *fakeSessionMaterializer) SessionExists(_ context.Context, name string) (bool, error) {
	return f.tmux.session(name) != nil, nil
}

func (f *fakeSessionMaterializer) PreparePersistentSessionCreate(_ context.Context, name, runtimeCWD, projectCWD string, env map[string]string) (inttmux.PersistentSessionCreateRequest, bool, error) {
	f.initialPaneCWD = runtimeCWD
	if f.beforeEnsureResult != nil {
		f.beforeEnsureResult()
		f.beforeEnsureResult = nil
	}
	if f.tmux.session(name) != nil {
		return inttmux.PersistentSessionCreateRequest{}, true, nil
	}
	if f.preCreateErr != nil {
		return inttmux.PersistentSessionCreateRequest{}, false, f.preCreateErr
	}
	return inttmux.PersistentSessionCreateRequest{
		SessionName: name, RuntimeCWD: runtimeCWD, ProjectCWD: projectCWD, Environment: maps.Clone(env),
	}, false, nil
}

func (f *fakeSessionMaterializer) CompletePersistentSessionCreate(_ context.Context, request inttmux.PersistentSessionCreateRequest, _ intmux.NewSessionResult) error {
	f.created = append(f.created, request.SessionName)
	if f.postCreate != nil {
		f.postCreate()
	}
	return f.createErr
}

func (f *fakeSessionMaterializer) AbortPersistentSessionCreate() {}

func (f *fakeSessionMaterializer) EnsureSession(_ context.Context, name, _ string) error {
	if f.tmux.session(name) != nil {
		return nil
	}
	if f.preCreateErr != nil {
		return f.preCreateErr
	}
	f.tmux.addSession(name)
	f.created = append(f.created, name)
	if f.postCreate != nil {
		f.postCreate()
	}
	if f.createErr != nil {
		return f.createErr
	}
	return nil
}

func (f *fakeSessionMaterializer) EnsureSessionWithEnvironment(_ context.Context, name, _ string, env map[string]string) error {
	if f.tmux.session(name) != nil {
		return nil
	}
	if f.preCreateErr != nil {
		return f.preCreateErr
	}
	session := f.tmux.addSession(name)
	maps.Copy(session.env, env)
	f.created = append(f.created, name)
	if f.postCreate != nil {
		f.postCreate()
	}
	if f.createErr != nil {
		return f.createErr
	}
	return nil
}

func (f *fakeSessionMaterializer) EnsureSessionWithEnvironmentResult(ctx context.Context, name, cwd string, env map[string]string) (intmux.NewSessionResult, error) {
	return f.EnsureSessionWithEnvironmentResultAt(ctx, name, cwd, cwd, env)
}

func (f *fakeSessionMaterializer) EnsureSessionWithEnvironmentResultAt(_ context.Context, name, runtimeCWD, projectCWD string, env map[string]string) (intmux.NewSessionResult, error) {
	f.initialPaneCWD = runtimeCWD
	cwd := projectCWD
	if f.beforeEnsureResult != nil {
		f.beforeEnsureResult()
		f.beforeEnsureResult = nil
	}
	if f.tmux.session(name) != nil {
		return intmux.NewSessionResult{Created: false}, nil
	}
	if f.preCreateErr != nil {
		return intmux.NewSessionResult{}, f.preCreateErr
	}
	session := f.tmux.addSession(name)
	maps.Copy(session.env, env)
	session.opts[tmuxopts.ProjectPathSession] = cwd
	f.created = append(f.created, name)
	result := intmux.NewSessionResult{
		Created:   true,
		SessionID: session.id,
		WindowID:  session.windows[0].id,
		PaneID:    session.windows[0].panes[0].id,
	}
	if f.postCreate != nil {
		f.postCreate()
	}
	if f.createErr != nil {
		if !f.createErrResult {
			return intmux.NewSessionResult{}, f.createErr
		}
		result.Created = false
		return result, f.createErr
	}
	marker := env[createOperationEnvironment]
	if !fakeCreatedTupleOwned(f.tmux, result, marker) {
		return result, errors.New("verify created tmux session after post-create")
	}
	return result, nil
}

func (f *fakeSessionMaterializer) FinalizeSessionStartup(_ context.Context, result intmux.NewSessionResult, _, _, marker string) error {
	if !fakeCreatedTupleOwned(f.tmux, result, marker) {
		return errors.New("verify created tmux session before startup")
	}
	if f.startup != nil {
		f.startup()
	}
	if !fakeCreatedTupleOwned(f.tmux, result, marker) {
		return errors.New("verify created tmux session after startup")
	}
	return nil
}

func fakeCreatedTupleOwned(tmux *fakeTmux, result intmux.NewSessionResult, marker string) bool {
	session := tmux.session(result.SessionID)
	if session == nil || session.env[createOperationEnvironment] != marker || marker == "" {
		return false
	}
	for _, window := range session.windows {
		if window.id != result.WindowID {
			continue
		}
		for _, pane := range window.panes {
			if pane.id == result.PaneID {
				return true
			}
		}
	}
	return false
}

// newTestResourceCreateCommand wires the resource-backed create routes onto the
// in-memory registry and the in-memory tmux server.
//
// The reconciler is stubbed to the machine-independent half: no workdir
// discovery and no legacy import, so a route test observes only its own
// mutations. Reconciliation itself is covered by project_registry_test.go and by
// the on-disk first-use suite.
func newTestResourceCreateCommand(t *testing.T, store *fakeResourceStore, tmux *fakeTmux) (*createCommand, *fakeSessionMaterializer) {
	t.Helper()
	sessions := &fakeSessionMaterializer{tmux: tmux}
	mirror := intmetadata.NewMirror(tmux)
	return &createCommand{
		store: store.store(),
		reconciler: &registryReconciler{
			discoverRoots: func() ([]string, error) { return nil, nil },
			liveSessions: func(context.Context) (map[string]bool, error) {
				return tmux.sessionNames(), nil
			},
			observeLegacy: func(context.Context, string) (coremetadata.LegacySession, intmetadata.LegacyTargets, error) {
				return coremetadata.LegacySession{}, intmetadata.LegacyTargets{}, nil
			},
			mirror:         mirror,
			shell:          "/bin/zsh",
			sessionNameFor: filepath.Base,
		},
		runtime: &materializer{
			runner:     tmux,
			mirror:     mirror,
			sessions:   sessions,
			target:     explicitTmuxTarget{flag: "-S", value: tmux.socketPath},
			warn:       testWarnWriter{t},
			executable: func() (string, error) { return testSupervisorBinary, nil },
			lookupEnv:  func(string) string { return "" },
		},
		shell:          "/bin/zsh",
		sessionNameFor: filepath.Base,
		newOperationID: func() (string, error) { return "op-test", nil },
		newGeneration:  testGenerationSequence(),
	}, sessions
}

// testSupervisorBinary is the stable projmux path the managed process
// supervisor is spelled with inside launch-argv assertions.
const testSupervisorBinary = "/opt/projmux/bin/projmux"

// testGenerationSequence mints stable activation generations so a test can
// quote a launched argv verbatim instead of matching around random entropy.
func testGenerationSequence() func() (string, error) {
	issued := 0
	return func() (string, error) {
		issued++
		return fmt.Sprintf("gen-test-%d", issued), nil
	}
}

// supervisedLaunchArgv is the argv assertion helper for one supervised child.
func supervisedLaunchArgv(paneUID, generation, agentUID, operationID, argv0 string, child ...string) []string {
	spec := superviseSpec{PaneUID: paneUID, AgentUID: agentUID, Generation: generation, OperationID: operationID}
	return superviseArgv(testSupervisorBinary, spec, argv0, child)
}

type testWarnWriter struct{ t *testing.T }

func (w testWarnWriter) Write(p []byte) (int, error) {
	w.t.Logf("warn: %s", strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// seedLiveWindow binds an existing registry Window and its primary Pane to a
// live tmux window, the way a legacy import or an earlier create would have.
func seedLiveWindow(t *testing.T, tmux *fakeTmux, session *fakeTmuxSession, windowUID, paneUID string) *fakeTmuxWindow {
	t.Helper()
	window := &fakeTmuxWindow{id: tmux.mint("@"), name: "seeded", opts: map[string]string{tmuxopts.WindowUID: windowUID}}
	pane := newFakeTmuxPane(tmux.mint("%"))
	pane.opts[tmuxopts.PaneUID] = paneUID
	window.panes = append(window.panes, pane)
	session.windows = append(session.windows, window)
	return window
}

func seedOwnedSession(session *fakeTmuxSession, uid, root string) {
	session.opts[tmuxopts.ProjectUIDSession] = uid
	session.opts[tmuxopts.ProjectPathSession] = root
}

// focusMovingCommands is the closed set of tmux verbs that move a client. The
// contract fixes create at zero client movement, so the assertion is over the
// recorded argv rather than over an observable "did focus change".
var focusMovingCommands = []string{"switch-client", "select-window", "select-pane", "attach-session"}

// assertNoClientMovement fails when any recorded tmux call could have moved the
// client, and when any window or pane creation forgot its detached flag.
func assertNoClientMovement(t *testing.T, tmux *fakeTmux) {
	t.Helper()
	for _, call := range tmux.calls {
		argv := tmuxCommandArgv(call)
		if len(argv) == 0 {
			continue
		}
		for _, verb := range focusMovingCommands {
			if argv[0] == verb {
				t.Fatalf("create issued a client-moving command: %v", call)
			}
		}
		if argv[0] == "new-window" || argv[0] == "split-window" {
			if !containsAll(argv, []string{"-d"}) {
				t.Fatalf("%s must be detached: %v", argv[0], call)
			}
		}
	}
}

// TestCreateMaterializesAMissingProjectRuntimeInTheBackground is acceptance
// criterion 1.
func TestCreateMaterializesAMissingProjectRuntimeInTheBackground(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, sessions := newTestResourceCreateCommand(t, store, tmux)

	// beta's status.session records the projection name with live=false and no
	// tmux session exists, which is exactly the offline runtime the create route
	// has to materialize.
	if got := len(tmux.sessions); got != 0 {
		t.Fatalf("fixture started with %d live sessions, want 0", got)
	}

	stdout, _, err := runRoute(t, create, "window", "--project", "beta")
	if err != nil {
		t.Fatalf("create window error = %v", err)
	}
	if stdout == "" {
		t.Fatal("create window produced no result line")
	}
	if len(sessions.created) != 1 || sessions.created[0] != "beta" {
		t.Fatalf("materialized sessions = %v, want [beta]", sessions.created)
	}
	session := tmux.session("beta")
	if session == nil {
		t.Fatal("the Project runtime was not materialized")
	}
	if got := session.opts[tmuxopts.ProjectUIDSession]; got != "prj-beta" {
		t.Fatalf("session project uid mirror = %q, want prj-beta", got)
	}
	// The session's own initial window is bound to the Project's first stored
	// Window rather than left as an orphan next to it.
	if got := session.windows[0].opts[tmuxopts.WindowUID]; got != "win-beta-main" {
		t.Fatalf("adopted window uid = %q, want win-beta-main", got)
	}
	if got := session.windows[0].panes[0].opts[tmuxopts.PaneUID]; got != "pan-beta-zsh" {
		t.Fatalf("adopted pane uid = %q, want pan-beta-zsh", got)
	}
	// status.session is now live, so a second create reuses the runtime.
	project, _ := store.registry.Project("prj-beta")
	if project.Status.Session == nil || !project.Status.Session.Live {
		t.Fatalf("status.session = %+v, want live", project.Status.Session)
	}
	assertNoClientMovement(t, tmux)
}

func TestCreateWindowPersistsExactRuntimeDisplayNameBeforeLifecycleReconcile(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, _ := newTestResourceCreateCommand(t, store, tmux)
	if _, _, err := runRoute(t, create, "window", "--project", "beta", "--name", "review"); err != nil {
		t.Fatalf("create window: %v", err)
	}

	var created *coremetadata.Window
	for index := range store.registry.Windows {
		window := &store.registry.Windows[index]
		if window.Metadata.OwnerUID() == "prj-beta" && window.Metadata.Name == "review" {
			created = window
			break
		}
	}
	if created == nil {
		t.Fatal("created review Window is absent from Registry")
	}
	if created.Metadata.DisplayName != created.Metadata.Name {
		t.Fatalf("created Window displayName = %q, want exact runtime name %q", created.Metadata.DisplayName, created.Metadata.Name)
	}
	session := tmux.session("beta")
	if session == nil {
		t.Fatal("created Window has no live beta session")
	}
	runtimeID := ""
	for _, window := range session.windows {
		if window.opts[tmuxopts.WindowUID] == created.Metadata.UID {
			runtimeID = window.id
			break
		}
	}
	if created.Status.RuntimeSessionID != session.id || created.Status.RuntimeID != runtimeID || runtimeID == "" {
		t.Fatalf("created Window runtime owner = %s/%s, want %s/%s", created.Status.RuntimeSessionID, created.Status.RuntimeID, session.id, runtimeID)
	}

	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-L\x00primary": tmux}}
	reconcile := &resourceReconcileCommand{
		runner: runner, resources: store.store(), lookupEnv: func(string) string { return "" },
		newReconciler: reconcileFixtureReconciler("/srv/beta", "beta"),
	}
	// The first public pass may refresh unrelated runtime status that the guarded
	// create pass excluded. Once that canonical convergence completes, the exact
	// created display and owner projection must not cause a later drift/write.
	tmux.calls = nil
	stdout, stderr, err := runReconcile(t, reconcile, "resources", "--socket", "primary", "-o", "json")
	if err != nil || stderr != "" {
		t.Fatalf("initial lifecycle reconcile: err=%v stderr=%q\n%s", err, stderr, stdout)
	}
	writesBefore := store.writes
	tmux.calls = nil
	stdout, stderr, err = runReconcile(t, reconcile, "resources", "--socket", "primary", "-o", "json")
	if err != nil || stderr != "" || !strings.Contains(stdout, `"outcome": "no-op"`) {
		t.Fatalf("repeat lifecycle reconcile drifted: err=%v stderr=%q\n%s", err, stderr, stdout)
	}
	if store.writes != writesBefore || tmuxMutationCallCount(tmux) != 0 {
		t.Fatalf("repeat lifecycle reconcile mutated state: Registry writes=%d want=%d tmux mutations=%d", store.writes, writesBefore, tmuxMutationCallCount(tmux))
	}
}

// TestCreateResultKindsFollowTheRouteNotTheSideEffects is acceptance criterion 2.
//
// `create window` creates a Pane too, and `create pane --create-window` creates
// a Window too, so the result kind has to come from the route rather than from
// whatever the operation happened to touch.
func TestCreateResultKindsFollowTheRouteNotTheSideEffects(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		args     []string
		wantKind string
		wantList string
	}{
		{
			name:     "create window returns a Window",
			args:     []string{"window", "--project", "beta"},
			wantKind: "window/",
			wantList: `"kind": "WindowList"`,
		},
		{
			name:     "create pane returns a Pane",
			args:     []string{"pane", "--project", "beta", "--window", "main"},
			wantKind: "pane/",
			wantList: `"kind": "PaneList"`,
		},
		{
			name:     "create pane --create-window still returns a Pane",
			args:     []string{"pane", "--project", "beta", "--window", "review", "--create-window"},
			wantKind: "pane/",
			wantList: `"kind": "PaneList"`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			create, _ := newTestResourceCreateCommand(t, store, tmux)
			stdout, stderr, err := runRoute(t, create, test.args...)
			if err != nil {
				t.Fatalf("create %v error = %v", test.args, err)
			}
			if !strings.HasPrefix(stdout, test.wantKind) || !strings.HasSuffix(strings.TrimSpace(stdout), " created") {
				t.Fatalf("default projection = %q, want %q...created", stdout, test.wantKind)
			}
			if stderr != "" {
				t.Fatalf("stderr = %q, want none", stderr)
			}

			// A create result is a fan-out even at one item, so the structured
			// projection keeps the List envelope.
			store = newFakeResourceStore(t)
			tmux = newFakeTmux()
			create, _ = newTestResourceCreateCommand(t, store, tmux)
			stdout, _, err = runRoute(t, create, append(append([]string(nil), test.args...), "-o", "json")...)
			if err != nil {
				t.Fatalf("create %v -o json error = %v", test.args, err)
			}
			if !strings.Contains(stdout, test.wantList) {
				t.Fatalf("-o json envelope = %s, want %s", stdout, test.wantList)
			}
			assertNoClientMovement(t, tmux)
		})
	}
}

// TestMissingWindowEnsureIsOptInAndAtomic is acceptance criterion 3.
func TestMissingWindowEnsureIsOptInAndAtomic(t *testing.T) {
	t.Parallel()

	t.Run("a missing Window is a no-match without the opt-in", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		tmux := newFakeTmux()
		create, _ := newTestResourceCreateCommand(t, store, tmux)
		before := store.snapshot()

		stdout, _, err := runRoute(t, create, "pane", "--project", "beta", "--window", "review")
		if err == nil {
			t.Fatal("a missing Window name silently resolved")
		}
		if !IsUsageError(err) {
			t.Fatalf("missing Window error is not a usage error: %v", err)
		}
		if stdout != "" {
			t.Fatalf("stdout = %q, want 0 bytes", stdout)
		}
		if store.snapshot() != before || store.writes != 0 {
			t.Fatalf("a no-match wrote %d transactions", store.writes)
		}
		if len(tmux.sessions) != 0 {
			t.Fatalf("a no-match materialized %d sessions", len(tmux.sessions))
		}
	})

	t.Run("the opt-in creates the Window and its initial Pane in one operation", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		tmux := newFakeTmux()
		create, _ := newTestResourceCreateCommand(t, store, tmux)

		if _, _, err := runRoute(t, create, "pane", "--project", "beta", "--window", "review", "--create-window"); err != nil {
			t.Fatalf("--create-window error = %v", err)
		}
		var review *coremetadata.Window
		for i := range store.registry.Windows {
			if store.registry.Windows[i].Metadata.Name == "review" &&
				store.registry.Windows[i].Metadata.OwnerUID() == "prj-beta" {
				review = &store.registry.Windows[i]
			}
		}
		if review == nil {
			t.Fatal("--create-window did not create the named Window")
		}
		if review.Spec.PrimaryPaneRef == "" {
			t.Fatal("an ensured Window must own an initial Pane recorded as its primaryPaneRef")
		}
		if _, ok := store.registry.Pane(review.Spec.PrimaryPaneRef); !ok {
			t.Fatalf("primaryPaneRef %q does not resolve", review.Spec.PrimaryPaneRef)
		}
		// The requested child is a second Pane, split off the ensured Window's
		// initial Pane.
		if got := len(store.registry.PanesOf(review.Metadata.UID)); got != 2 {
			t.Fatalf("panes below the ensured Window = %d, want 2", got)
		}
		// One transaction, so the ensure and the child creation cannot land apart.
		if store.writes != 1 {
			t.Fatalf("transactions committed = %d, want exactly 1", store.writes)
		}
	})

	t.Run("an existing Window is reused rather than duplicated", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		tmux := newFakeTmux()
		create, _ := newTestResourceCreateCommand(t, store, tmux)
		before := len(store.registry.Windows)

		if _, _, err := runRoute(t, create, "pane", "--project", "beta", "--window", "main", "--create-window"); err != nil {
			t.Fatalf("--create-window on an existing Window error = %v", err)
		}
		if got := len(store.registry.Windows); got != before {
			t.Fatalf("windows = %d, want %d; --create-window duplicated an existing Window", got, before)
		}
	})
}

// TestEveryTargetWindowAnchorsOnExactlyOnePane is acceptance criterion 4.
func TestEveryTargetWindowAnchorsOnExactlyOnePane(t *testing.T) {
	t.Parallel()

	t.Run("each fan-out target splits its own primaryPaneRef", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		tmux := newFakeTmux()
		create, _ := newTestResourceCreateCommand(t, store, tmux)

		// alpha owns two Windows, each with its own primaryPaneRef.
		if _, _, err := runRoute(t, create, "pane", "--project", "alpha"); err != nil {
			t.Fatalf("fan-out error = %v", err)
		}
		for _, window := range []string{"win-alpha-main", "win-alpha-review"} {
			if got := len(store.registry.PanesOf(window)); got == 0 {
				t.Fatalf("window %s received no Pane", window)
			}
		}
		anchors := map[string]int{}
		for _, call := range tmux.calls {
			argv := tmuxCommandArgv(call)
			if len(argv) > 0 && argv[0] == "split-window" {
				anchors[flagValue(argv, "-t")]++
			}
		}
		if len(anchors) != 2 {
			t.Fatalf("splits anchored on %d distinct panes, want one per target Window: %v", len(anchors), anchors)
		}
		for anchor, count := range anchors {
			if count != 1 {
				t.Fatalf("anchor %s was split %d times, want exactly one", anchor, count)
			}
		}
	})

	t.Run("an explicit --pane must be exact-one inside each Window", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		tmux := newFakeTmux()
		create, _ := newTestResourceCreateCommand(t, store, tmux)

		// `zsh` names one Pane in win-alpha-main and another in
		// win-alpha-review, so a fan-out over both resolves one anchor each.
		if _, _, err := runRoute(t, create, "pane", "--project", "alpha", "--pane", "zsh"); err != nil {
			t.Fatalf("explicit anchor fan-out error = %v", err)
		}
		var split []string
		for _, call := range tmux.calls {
			argv := tmuxCommandArgv(call)
			if len(argv) > 0 && argv[0] == "split-window" {
				split = append(split, flagValue(argv, "-t"))
			}
		}
		if len(split) != 2 {
			t.Fatalf("splits = %v, want one per target Window", split)
		}
	})

	t.Run("an ambiguous --pane inside one Window is a usage error", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		tmux := newFakeTmux()
		create, _ := newTestResourceCreateCommand(t, store, tmux)

		stdout, _, err := runRoute(t, create,
			"pane", "--project", "alpha", "--window", "main", "--pane", "zsh", "--pane", "log")
		if err == nil {
			t.Fatal("two anchors in one Window resolved")
		}
		if !IsUsageError(err) || !strings.Contains(err.Error(), "want exactly one") {
			t.Fatalf("error = %v, want an exact-one cardinality usage error", err)
		}
		if stdout != "" || store.writes != 0 || tmux.paneCount() != 0 {
			t.Fatalf("an ambiguous anchor mutated something: stdout=%q writes=%d panes=%d",
				stdout, store.writes, tmux.paneCount())
		}
	})
}

// TestAStalePrimaryPaneRefIsExitTwoWithNoFocusFallback is acceptance criterion 5.
func TestAStalePrimaryPaneRefIsExitTwoWithNoFocusFallback(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		ref  string
		want string
	}{
		{name: "stale ref", ref: "pane-vanished", want: "resolves to no Pane"},
		{name: "missing ref", ref: "", want: "has no spec.primaryPaneRef"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			for i := range store.registry.Windows {
				if store.registry.Windows[i].Metadata.UID == "win-beta-main" {
					store.registry.Windows[i].Spec.PrimaryPaneRef = test.ref
				}
			}
			before := store.snapshot()
			tmux := newFakeTmux()
			// A live session with a live pane is present, so a fallback to the
			// active or last-used Pane would have something to fall back to.
			session := tmux.addSession("beta")
			seedOwnedSession(session, "prj-beta", "/srv/beta")
			seedLiveWindow(t, tmux, session, "win-beta-main", "pan-beta-zsh")
			create, _ := newTestResourceCreateCommand(t, store, tmux)
			panesBefore := tmux.paneCount()

			stdout, _, err := runRoute(t, create, "pane", "--project", "beta", "--window", "main")
			if err == nil {
				t.Fatal("a stale primaryPaneRef silently resolved")
			}
			if !IsUsageError(err) {
				t.Fatalf("stale anchor error is not a usage error (exit 2): %v", err)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %q, want it to mention %q", err, test.want)
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want 0 bytes", stdout)
			}
			if store.snapshot() != before || store.writes != 0 {
				t.Fatal("a stale anchor mutated the registry")
			}
			if got := tmux.paneCount(); got != panesBefore {
				t.Fatalf("panes = %d, want %d; the route fell back to a live Pane", got, panesBefore)
			}
			if tmux.argvContains("split-window") {
				t.Fatal("a stale anchor still reached split-window")
			}
		})
	}
}

// TestPreCreateHookFailureLeavesZeroMutations is acceptance criterion 6.
func TestPreCreateHookFailureLeavesZeroMutations(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	before := store.snapshot()
	tmux := newFakeTmux()
	create, sessions := newTestResourceCreateCommand(t, store, tmux)
	sessions.preCreateErr = errors.New(`pre-create hook for tmux session "beta": exited with status 7`)

	stdout, _, err := runRoute(t, create, "window", "--project", "beta")
	if err == nil {
		t.Fatal("a pre-create hook refusal still created the Window")
	}
	if IsUsageError(err) {
		t.Fatalf("a hook refusal is a runtime failure, not operator input: %v", err)
	}
	if !strings.Contains(err.Error(), "exited with status 7") {
		t.Fatalf("error = %q, want the hook diagnostic preserved", err)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want 0 bytes", stdout)
	}
	if store.snapshot() != before {
		t.Fatal("a pre-create refusal mutated the registry")
	}
	if store.writes != 0 {
		t.Fatalf("committed transactions = %d, want 0", store.writes)
	}
	if len(tmux.sessions) != 0 || tmux.windowCount() != 0 || tmux.paneCount() != 0 {
		t.Fatalf("a pre-create refusal left runtime objects behind:\n%s", tmux.state())
	}
}

// TestPostCreateHookFailureIsALoggedSuccess is acceptance criterion 7.
//
// The swallow itself lives in the tmux client, which is exactly why this route
// reuses EnsureSession instead of running its own hooks: the route only has to
// prove it does not turn a logged hook warning into a failure.
func TestPostCreateHookFailureIsALoggedSuccess(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, sessions := newTestResourceCreateCommand(t, store, tmux)
	warned := 0
	sessions.postCreate = func() { warned++ }

	stdout, _, err := runRoute(t, create, "window", "--project", "beta")
	if err != nil {
		t.Fatalf("a post-create hook warning flipped the exit code: %v", err)
	}
	if warned != 1 {
		t.Fatalf("post-create ran %d times, want 1", warned)
	}
	if !strings.HasPrefix(stdout, "window/") {
		t.Fatalf("stdout = %q, want the committed result", stdout)
	}
	if store.writes != 1 {
		t.Fatalf("committed transactions = %d, want 1", store.writes)
	}
}

// TestCreateNeverMovesTheClient is acceptance criterion 8.
func TestCreateNeverMovesTheClient(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"window", "--project", "beta"},
		{"pane", "--project", "beta", "--window", "main"},
		{"pane", "--project", "beta", "--window", "main", "--placement", "down"},
		{"pane", "--project", "beta", "--window", "review", "--create-window"},
		{"pane", "--project", "alpha"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			create, _ := newTestResourceCreateCommand(t, store, tmux)
			if _, _, err := runRoute(t, create, args...); err != nil {
				t.Fatalf("create %v error = %v", args, err)
			}
			assertNoClientMovement(t, tmux)
			// The route also never reads which client or pane is focused, so it
			// cannot silently depend on focus either.
			for _, call := range tmux.calls {
				joined := strings.Join(call, " ")
				for _, focusRead := range []string{"client_session", "pane_active", "window_active", "client_activity"} {
					if strings.Contains(joined, focusRead) {
						t.Fatalf("create read focus state: %v", call)
					}
				}
			}
		})
	}
}

// TestCreatePlacementMapsOntoTheClosedSplitAxis fixes the right/down contract at
// the argv boundary.
func TestCreatePlacementMapsOntoTheClosedSplitAxis(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		placement string
		wantFlag  string
	}{
		{placement: "", wantFlag: "-h"},
		{placement: "right", wantFlag: "-h"},
		{placement: "down", wantFlag: "-v"},
	} {
		t.Run("placement="+test.placement, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			create, _ := newTestResourceCreateCommand(t, store, tmux)
			args := []string{"pane", "--project", "beta", "--window", "main"}
			if test.placement != "" {
				args = append(args, "--placement", test.placement)
			}
			if _, _, err := runRoute(t, create, args...); err != nil {
				t.Fatalf("create %v error = %v", args, err)
			}
			var found bool
			for _, call := range tmux.calls {
				argv := tmuxCommandArgv(call)
				if len(argv) > 0 && argv[0] == "split-window" {
					found = true
					if !containsAll(argv, []string{test.wantFlag}) {
						t.Fatalf("split argv = %v, want %s", call, test.wantFlag)
					}
				}
			}
			if !found {
				t.Fatal("no split-window was issued")
			}
		})
	}
}

func TestCanonicalCreatePaneSplitIsImmediatelyEqualized(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		placement string
		extra     []string
		wantAxis  string
	}{
		{name: "default primary anchor right", placement: "right", wantAxis: "-x"},
		{name: "default primary anchor down", placement: "down", wantAxis: "-y"},
		{name: "explicit anchor right", placement: "right", extra: []string{"--pane", "zsh"}, wantAxis: "-x"},
		{name: "explicit anchor down", placement: "down", extra: []string{"--pane", "zsh"}, wantAxis: "-y"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			create, _ := newTestResourceCreateCommand(t, store, tmux)
			args := []string{"pane", "--project", "beta", "--window", "main", "--placement", test.placement, "-o", "pane-id"}
			args = append(args, test.extra...)
			stdout, _, err := runRoute(t, create, args...)
			if err != nil {
				t.Fatalf("create %v error = %v", args, err)
			}
			if !strings.HasPrefix(strings.TrimSpace(stdout), "%") {
				t.Fatalf("pane-id output = %q", stdout)
			}

			splitIndex := firstTmuxCall(tmux.calls, 0, "split-window", "")
			if splitIndex < 0 {
				t.Fatal("split-window was not called")
			}
			anchor := flagValue(tmux.calls[splitIndex], "-t")
			geometryIndex := firstTmuxCall(tmux.calls, splitIndex+1, "list-panes", splitPaneGeometryFormat)
			if geometryIndex < 0 {
				t.Fatalf("calls after split lack geometry observation: %v", tmux.calls[splitIndex:])
			}
			if got := flagValue(tmux.calls[geometryIndex], "-t"); got != anchor {
				t.Fatalf("geometry target = %q, want split anchor %q", got, anchor)
			}
			wrongAxis := "-y"
			if test.wantAxis == "-y" {
				wrongAxis = "-x"
			}
			for _, call := range tmux.calls[geometryIndex+1:] {
				argv := tmuxCommandArgv(call)
				if len(argv) > 0 && argv[0] == "resize-pane" && slices.Contains(argv, wrongAxis) {
					t.Fatalf("%s create resized the cross axis: %v", test.placement, call)
				}
			}
		})
	}
}

func TestCanonicalCreatePaneEqualizationIsWindowLocalInFanOut(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, _ := newTestResourceCreateCommand(t, store, tmux)
	if _, _, err := runRoute(t, create, "pane", "--project", "alpha", "--placement", "right"); err != nil {
		t.Fatalf("fan-out create error = %v", err)
	}

	var splitIndexes []int
	for i, call := range tmux.calls {
		argv := tmuxCommandArgv(call)
		if len(argv) > 0 && argv[0] == "split-window" {
			splitIndexes = append(splitIndexes, i)
		}
	}
	if len(splitIndexes) != 2 {
		t.Fatalf("split indexes = %v, want two Window-local splits", splitIndexes)
	}
	for n, splitIndex := range splitIndexes {
		end := len(tmux.calls)
		if n+1 < len(splitIndexes) {
			end = splitIndexes[n+1]
		}
		anchor := flagValue(tmux.calls[splitIndex], "-t")
		_, anchorWindow, _ := tmux.pane(anchor)
		geometryIndex := firstTmuxCall(tmux.calls[:end], splitIndex+1, "list-panes", splitPaneGeometryFormat)
		if geometryIndex < 0 || flagValue(tmux.calls[geometryIndex], "-t") != anchor {
			t.Fatalf("split %d did not observe its own anchor before the next split: %v", n, tmux.calls[splitIndex:end])
		}
		for _, call := range tmux.calls[geometryIndex+1 : end] {
			argv := tmuxCommandArgv(call)
			if len(argv) == 0 || argv[0] != "resize-pane" {
				continue
			}
			_, resizedWindow, _ := tmux.pane(flagValue(argv, "-t"))
			if resizedWindow != anchorWindow {
				t.Fatalf("fan-out resized across Windows: anchor %s call %v", anchor, call)
			}
		}
	}
}

func TestCanonicalCreateLayoutFailuresNeverRollbackSuccessfulSplit(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		fail []string
	}{
		{name: "geometry observation", fail: []string{"list-panes", splitPaneGeometryFormat}},
		{name: "individual resize", fail: []string{"resize-pane"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			tmux.fail, tmux.failAlways = test.fail, true
			create, _ := newTestResourceCreateCommand(t, store, tmux)
			stdout, _, err := runRoute(t, create, "pane", "--project", "beta", "--window", "main", "-o", "pane-id")
			if err != nil {
				t.Fatalf("layout failure escaped create: %v", err)
			}
			if store.writes != 1 || !strings.HasPrefix(strings.TrimSpace(stdout), "%") {
				t.Fatalf("successful create was not committed: writes=%d stdout=%q", store.writes, stdout)
			}
			if tmux.argvContains("kill-pane") || tmux.argvContains("kill-window") || tmux.argvContains("kill-session") {
				t.Fatalf("layout failure triggered rollback: %v", tmux.calls)
			}
		})
	}
}

func TestCreateWindowIsOnePaneLayoutNoOpAndEnsuredWindowSplitEqualizes(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, _ := newTestResourceCreateCommand(t, store, tmux)
	if _, _, err := runRoute(t, create, "window", "--project", "beta"); err != nil {
		t.Fatalf("create window error = %v", err)
	}
	if firstTmuxCall(tmux.calls, 0, "list-panes", splitPaneGeometryFormat) >= 0 || tmux.argvContains("resize-pane") {
		t.Fatalf("one-pane create window attempted equalization: %v", tmux.calls)
	}

	store = newFakeResourceStore(t)
	tmux = newFakeTmux()
	create, _ = newTestResourceCreateCommand(t, store, tmux)
	if _, _, err := runRoute(t, create, "pane", "--project", "beta", "--window", "fresh", "--create-window"); err != nil {
		t.Fatalf("ensured Window split error = %v", err)
	}
	splitIndex := firstTmuxCall(tmux.calls, 0, "split-window", "")
	// tmux's own split can already produce the exact even target sizes. The
	// plan must still observe the scoped geometry, but repeat-empty semantics
	// intentionally omit resize writes when that expected effect is present.
	if splitIndex < 0 || firstTmuxCall(tmux.calls, splitIndex+1, "list-panes", splitPaneGeometryFormat) < 0 {
		t.Fatalf("ensured Window split was not equalized: %v", tmux.calls)
	}
}

func firstTmuxCall(calls [][]string, start int, command, token string) int {
	if start < 0 {
		return -1
	}
	for i := start; i < len(calls); i++ {
		argv := tmuxCommandArgv(calls[i])
		if len(argv) == 0 || argv[0] != command {
			continue
		}
		if token == "" || slices.Contains(argv, token) {
			return i
		}
	}
	return -1
}

// TestOperationRollbackRemovesOnlyWhatThisOperationCreated is the ledger
// ownership contract.
func TestOperationRollbackRemovesOnlyWhatThisOperationCreated(t *testing.T) {
	t.Parallel()

	t.Run("a failure after the runtime mutation undoes only this operation", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		tmux := newFakeTmux()
		// Pre-existing runtime state that the operation must not touch: another
		// live session, plus alpha's own already-live Window and Pane.
		bystander := tmux.addSession("bystander")
		alpha := tmux.addSession("alpha")
		seedOwnedSession(alpha, "prj-alpha", "/srv/alpha")
		seedLiveWindow(t, tmux, alpha, "win-alpha-main", "pan-alpha-zsh")
		before := tmux.state()
		registryBefore := store.snapshot()

		create, _ := newTestResourceCreateCommand(t, store, tmux)
		// Fail the split that follows the Window creation, so the ledger holds a
		// window and the session was NOT created by this operation.
		tmux.fail = []string{"split-window"}

		stdout, _, err := runRoute(t, create, "pane", "--project", "alpha", "--window", "spawn", "--create-window")
		if err == nil {
			t.Fatal("the injected split failure did not fail the create")
		}
		if stdout != "" {
			t.Fatalf("stdout = %q, want 0 bytes", stdout)
		}
		if got := tmux.state(); got != before {
			t.Fatalf("rollback did not restore the runtime:\n--- got ---\n%s\n--- want ---\n%s", got, before)
		}
		if store.snapshot() != registryBefore || store.writes != 0 {
			t.Fatal("a failed operation committed registry state")
		}
		if tmux.session("bystander") == nil || len(bystander.windows) != 1 {
			t.Fatal("rollback touched a session this operation did not create")
		}
	})

	t.Run("rollback leaves an object whose uid no longer matches", func(t *testing.T) {
		t.Parallel()
		tmux := newFakeTmux()
		session := tmux.addSession("alpha")
		window := seedLiveWindow(t, tmux, session, "win-alpha-main", "pan-alpha-zsh")
		var warnings bytes.Buffer
		runtime := &materializer{
			runner: tmux, mirror: intmetadata.NewMirror(tmux), warn: &warnings,
			target: explicitTmuxTarget{flag: "-S", value: tmux.socketPath}, expectedSocketPath: tmux.socketPath,
		}

		ledger := &runtimeLedger{}
		ledger.record(runtimeWindow, window.id, "win-alpha-main")
		// Another operation re-bound the window between creation and rollback.
		window.opts[tmuxopts.WindowUID] = "win-someone-else"

		runtime.rollback(context.Background(), ledger)
		if _, got := tmux.window(window.id); got == nil {
			t.Fatal("rollback removed a window that no longer carried this operation's uid")
		}
		if !strings.Contains(warnings.String(), "rollback preserved window "+window.id) {
			t.Fatalf("ownership drift warning = %q", warnings.String())
		}

		// With the uid restored it is this operation's object again.
		window.opts[tmuxopts.WindowUID] = "win-alpha-main"
		runtime.rollback(context.Background(), ledger)
		if _, got := tmux.window(window.id); got != nil {
			t.Fatal("rollback kept an object this operation created")
		}
	})
}

func TestObjectCreatedHookFailureRollsBackExactPaneAndRegistry(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	session := tmux.addSession("alpha")
	seedOwnedSession(session, "prj-alpha", "/srv/alpha")
	seedLiveWindow(t, tmux, session, "win-alpha-main", "pan-alpha-zsh")
	before := tmux.state()
	registryBefore := store.snapshot()

	create, _ := newTestResourceCreateCommand(t, store, tmux)
	tmux.fail = []string{"split-window"}
	tmux.failAfterMutation = true
	tmux.failMessage = "synchronous lifecycle hook refused"

	stdout, _, err := runRoute(t, create, "pane", "--project", "alpha", "--window", "main")
	if err == nil || !strings.Contains(err.Error(), "synchronous lifecycle hook refused") {
		t.Fatalf("error = %v, want lifecycle failure", err)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want 0 bytes", stdout)
	}
	if got := tmux.state(); got != before {
		t.Fatalf("rollback did not remove the exact error-created pane:\n--- got ---\n%s\n--- want ---\n%s", got, before)
	}
	if store.snapshot() != registryBefore || store.writes != 0 {
		t.Fatal("object-created failure committed registry state")
	}
}

func TestObjectCreatedHookFailureRollsBackExactWindowAndRegistry(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	session := tmux.addSession("alpha")
	seedOwnedSession(session, "prj-alpha", "/srv/alpha")
	seedLiveWindow(t, tmux, session, "win-alpha-main", "pan-alpha-zsh")
	before := tmux.state()
	registryBefore := store.snapshot()

	create, _ := newTestResourceCreateCommand(t, store, tmux)
	tmux.fail = []string{"new-window"}
	tmux.failAfterMutation = true
	tmux.failMessage = "synchronous lifecycle hook refused"

	stdout, _, err := runRoute(t, create, "window", "--project", "alpha", "--name", "phase0-hook")
	if err == nil || !strings.Contains(err.Error(), "synchronous lifecycle hook refused") {
		t.Fatalf("error = %v, want lifecycle failure", err)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want 0 bytes", stdout)
	}
	if got := tmux.state(); got != before {
		t.Fatalf("rollback did not remove the exact error-created window:\n--- got ---\n%s\n--- want ---\n%s", got, before)
	}
	if store.snapshot() != registryBefore || store.writes != 0 {
		t.Fatal("object-created failure committed registry state")
	}
}

func TestObjectCreatedHookFailureRollsBackExactSessionAndRegistry(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	before := tmux.state()
	registryBefore := store.snapshot()
	create, sessions := newTestResourceCreateCommand(t, store, tmux)
	sessions.createErr = errors.New("after-new-window hook refused")

	stdout, _, err := runRoute(t, create, "window", "--project", "beta", "--name", "phase0-session")
	if err == nil || !strings.Contains(err.Error(), "after-new-window hook refused") {
		t.Fatalf("error = %v, want object-created session failure", err)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want 0 bytes", stdout)
	}
	if got := tmux.state(); got != before {
		t.Fatalf("rollback did not remove the exact error-created session:\n--- got ---\n%s\n--- want ---\n%s", got, before)
	}
	if store.snapshot() != registryBefore || store.writes != 0 {
		t.Fatal("object-created session failure committed registry state")
	}
}

func TestSessionResultErrorUsesExactHandleAndPreservesUnknownOwnership(t *testing.T) {
	t.Parallel()

	t.Run("exact leased handle survives rename and rolls back", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		tmux := newFakeTmux()
		create, sessions := newTestResourceCreateCommand(t, store, tmux)
		sessions.createErr = errors.New("owner verification failed")
		sessions.createErrResult = true
		sessions.postCreate = func() { tmux.session("beta").name = "renamed-after-create" }
		registryBefore, runtimeBefore := store.snapshot(), tmux.state()

		stdout, _, err := runRoute(t, create, "window", "--project", "beta")
		if err == nil || stdout != "" {
			t.Fatalf("stdout/error = %q / %v", stdout, err)
		}
		if store.snapshot() != registryBefore || store.writes != 0 || tmux.state() != runtimeBefore {
			t.Fatalf("exact renamed result-error session was not rolled back:\n%s", tmux.state())
		}
	})

	t.Run("exact handle without private marker is preserved and diagnosed", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		tmux := newFakeTmux()
		create, sessions := newTestResourceCreateCommand(t, store, tmux)
		var warnings bytes.Buffer
		create.runtime.warn = &warnings
		sessions.createErr = errors.New("owner verification failed")
		sessions.createErrResult = true
		sessions.postCreate = func() {
			session := tmux.session("beta")
			delete(session.env, createOperationEnvironment)
		}
		registryBefore := store.snapshot()

		stdout, _, err := runRoute(t, create, "window", "--project", "beta")
		if err == nil || stdout != "" {
			t.Fatalf("stdout/error = %q / %v", stdout, err)
		}
		residual := tmux.session("beta")
		if residual == nil || residual.opts[tmuxopts.ProjectUIDSession] != "" || store.snapshot() != registryBefore || store.writes != 0 {
			t.Fatalf("unknown-ownership result-error session was claimed or removed:\n%s", tmux.state())
		}
		if !strings.Contains(warnings.String(), residual.id) || !strings.Contains(warnings.String(), "preserved this exact residual handle") {
			t.Fatalf("unknown exact residual diagnostic = %q", warnings.String())
		}
	})
}

func TestCreatedSessionRevalidationRefusesHookAndStartupIdentityLoss(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		startup bool
		mutate  func(*fakeTmux)
	}{
		{
			name: "post-create moves initial Window to another session",
			mutate: func(tmux *fakeTmux) {
				session := tmux.session("beta")
				window := session.windows[0]
				session.windows = nil
				tmux.session("bystander").windows = append(tmux.session("bystander").windows, window)
			},
		},
		{
			name: "post-create kills session and same-name replacement appears",
			mutate: func(tmux *fakeTmux) {
				tmux.sessions = slices.DeleteFunc(tmux.sessions, func(session *fakeTmuxSession) bool { return session.name == "beta" })
				replacement := tmux.addSession("beta")
				replacement.opts[tmuxopts.ProjectUIDSession] = "prj-foreign"
				replacement.opts[tmuxopts.ProjectPathSession] = "/srv/foreign"
			},
		},
		{
			name: "post-create replacement reuses returned handles without marker",
			mutate: func(tmux *fakeTmux) {
				created := tmux.session("beta")
				sessionID, windowID, paneID := created.id, created.windows[0].id, created.windows[0].panes[0].id
				tmux.sessions = slices.DeleteFunc(tmux.sessions, func(session *fakeTmuxSession) bool { return session == created })
				replacement := tmux.addSession("beta")
				replacement.id = sessionID
				replacement.windows[0].id = windowID
				replacement.windows[0].panes[0].id = paneID
				replacement.opts[tmuxopts.ProjectUIDSession] = "prj-foreign"
				replacement.opts[tmuxopts.ProjectPathSession] = "/srv/foreign"
			},
		},
		{
			name:    "startup moves returned Pane under another Window",
			startup: true,
			mutate: func(tmux *fakeTmux) {
				session := tmux.session("beta")
				pane := session.windows[0].panes[0]
				session.windows[0].panes = nil
				other := &fakeTmuxWindow{id: tmux.mint("@"), name: "startup", opts: map[string]string{}, panes: []*fakeTmuxPane{pane}}
				session.windows = append(session.windows, other)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			bystander := tmux.addSession("bystander")
			bystanderOpts := maps.Clone(bystander.opts)
			bystanderWindowOpts := maps.Clone(bystander.windows[0].opts)
			bystanderPaneOpts := maps.Clone(bystander.windows[0].panes[0].opts)
			create, sessions := newTestResourceCreateCommand(t, store, tmux)
			if test.startup {
				sessions.startup = func() {
					session := tmux.session("beta")
					if session == nil || session.opts[tmuxopts.ProjectUIDSession] == "" ||
						len(session.windows) != 1 || session.windows[0].opts[tmuxopts.WindowUID] == "" ||
						len(session.windows[0].panes) != 1 || session.windows[0].panes[0].opts[tmuxopts.PaneUID] == "" {
						t.Fatalf("startup ran before Session/Window/Pane claims and mirrors:\n%s", tmux.state())
					}
					test.mutate(tmux)
				}
			} else {
				sessions.postCreate = func() { test.mutate(tmux) }
			}
			registryBefore := store.snapshot()

			stdout, _, err := runRoute(t, create, "window", "--project", "beta")
			if err == nil || stdout != "" {
				t.Fatalf("stdout/error = %q / %v", stdout, err)
			}
			if store.snapshot() != registryBefore || store.writes != 0 {
				t.Fatal("identity-loss callback committed Registry state")
			}
			if test.startup && tmux.session("beta") != nil {
				t.Fatalf("startup ownership failure preserved the operation-owned Session:\n%s", tmux.state())
			}
			if tmux.session("bystander") != bystander || !maps.Equal(bystander.opts, bystanderOpts) ||
				!maps.Equal(bystander.windows[0].opts, bystanderWindowOpts) ||
				!maps.Equal(bystander.windows[0].panes[0].opts, bystanderPaneOpts) {
				t.Fatalf("identity-loss callback contaminated pre-existing bystander:\n%s", tmux.state())
			}
			for _, session := range tmux.sessions {
				for _, window := range session.windows {
					if window.opts[tmuxopts.WindowUID] != "" {
						t.Fatalf("identity-loss callback claimed Window %s: %#v", window.id, window.opts)
					}
					for _, pane := range window.panes {
						if pane.opts[tmuxopts.PaneUID] != "" {
							t.Fatalf("identity-loss callback claimed Pane %s: %#v", pane.id, pane.opts)
						}
					}
				}
			}
		})
	}
}

func TestCreatedSessionRevalidationAllowsInitialWindowToRemainOwnedButNotCurrent(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, sessions := newTestResourceCreateCommand(t, store, tmux)
	sessions.postCreate = func() {
		session := tmux.session("beta")
		other := &fakeTmuxWindow{id: tmux.mint("@"), name: "hook-selected", opts: map[string]string{}}
		other.panes = append(other.panes, newFakeTmuxPane(tmux.mint("%")))
		session.windows = append([]*fakeTmuxWindow{other}, session.windows...)
	}

	if _, _, err := runRoute(t, create, "window", "--project", "beta"); err != nil {
		t.Fatalf("create with retained non-current initial Window: %v", err)
	}
	project, _ := store.registry.ProjectByName("beta")
	if session := tmux.session("beta"); session == nil || session.opts[tmuxopts.ProjectUIDSession] != project.Metadata.UID {
		t.Fatalf("retained initial Window session was not claimed:\n%s", tmux.state())
	}
}

func TestUIDClaimFailurePreservesUnownedResidualAndRegistry(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		args       []string
		fail       []string
		wantKind   string
		setup      func(*fakeTmux)
		residualID func(*fakeTmux) string
	}{
		{
			name:     "new session",
			args:     []string{"window", "--project", "beta", "--name", "claim-session"},
			fail:     []string{"set-option", tmuxopts.ProjectUIDSession},
			wantKind: "session",
			residualID: func(tmux *fakeTmux) string {
				if session := tmux.session("beta"); session != nil && session.opts[tmuxopts.ProjectUIDSession] == "" {
					return session.id
				}
				return ""
			},
		},
		{
			name:     "new window",
			args:     []string{"window", "--project", "alpha", "--name", "claim-window"},
			fail:     []string{"set-option", "-w", tmuxopts.WindowUID},
			wantKind: "window",
			setup: func(tmux *fakeTmux) {
				session := tmux.addSession("alpha")
				seedOwnedSession(session, "prj-alpha", "/srv/alpha")
				seedLiveWindow(t, tmux, session, "win-alpha-main", "pan-alpha-zsh")
			},
			residualID: func(tmux *fakeTmux) string {
				for _, window := range tmux.session("alpha").windows {
					if window.name == "claim-window" && window.opts[tmuxopts.WindowUID] == "" {
						return window.id
					}
				}
				return ""
			},
		},
		{
			name:     "split pane",
			args:     []string{"pane", "--project", "alpha", "--window", "main", "--name", "claim-pane"},
			fail:     []string{"set-option", "-p", tmuxopts.PaneUID},
			wantKind: "pane",
			setup: func(tmux *fakeTmux) {
				session := tmux.addSession("alpha")
				seedOwnedSession(session, "prj-alpha", "/srv/alpha")
				seedLiveWindow(t, tmux, session, "win-alpha-main", "pan-alpha-zsh")
			},
			residualID: func(tmux *fakeTmux) string {
				for _, window := range tmux.session("alpha").windows {
					if window.opts[tmuxopts.WindowUID] != "win-alpha-main" {
						continue
					}
					for _, pane := range window.panes {
						if pane.opts[tmuxopts.PaneUID] == "" {
							return pane.id
						}
					}
				}
				return ""
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			if test.setup != nil {
				test.setup(tmux)
			}
			beforeRegistry := store.snapshot()
			create, _ := newTestResourceCreateCommand(t, store, tmux)
			var warnings bytes.Buffer
			create.runtime.warn = &warnings
			tmux.fail = test.fail

			stdout, _, err := runRoute(t, create, test.args...)
			if err == nil || stdout != "" || !strings.Contains(err.Error(), "claim created tmux "+test.wantKind) {
				t.Fatalf("stdout/error = %q / %v", stdout, err)
			}
			if store.snapshot() != beforeRegistry || store.writes != 0 {
				t.Fatal("UID-claim failure committed Registry state")
			}
			residualID := test.residualID(tmux)
			if residualID == "" {
				t.Fatalf("UID-claim failure removed or claimed the raw residual:\n%s", tmux.state())
			}
			if !strings.Contains(warnings.String(), "preserved this exact residual handle") || !strings.Contains(warnings.String(), residualID) {
				t.Fatalf("residual diagnostic = %q, want exact %s", warnings.String(), residualID)
			}
		})
	}
}

func TestUIDClaimAfterApplyErrorRollsBackExactOwnedObject(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		args  []string
		fail  []string
		setup func(*fakeTmux)
	}{
		{
			name: "new session",
			args: []string{"window", "--project", "beta", "--name", "after-apply-session"},
			fail: []string{"set-option", tmuxopts.ProjectUIDSession},
		},
		{
			name: "new window",
			args: []string{"window", "--project", "alpha", "--name", "after-apply-window"},
			fail: []string{"set-option", "-w", tmuxopts.WindowUID},
			setup: func(tmux *fakeTmux) {
				session := tmux.addSession("alpha")
				seedOwnedSession(session, "prj-alpha", "/srv/alpha")
				seedLiveWindow(t, tmux, session, "win-alpha-main", "pan-alpha-zsh")
			},
		},
		{
			name: "split pane",
			args: []string{"pane", "--project", "alpha", "--window", "main", "--name", "after-apply-pane"},
			fail: []string{"set-option", "-p", tmuxopts.PaneUID},
			setup: func(tmux *fakeTmux) {
				session := tmux.addSession("alpha")
				seedOwnedSession(session, "prj-alpha", "/srv/alpha")
				seedLiveWindow(t, tmux, session, "win-alpha-main", "pan-alpha-zsh")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			if test.setup != nil {
				test.setup(tmux)
			}
			registryBefore, runtimeBefore := store.snapshot(), tmux.state()
			create, _ := newTestResourceCreateCommand(t, store, tmux)
			var warnings bytes.Buffer
			create.runtime.warn = &warnings
			tmux.fail = test.fail
			tmux.failAfterMutation = true

			stdout, _, err := runRoute(t, create, test.args...)
			if err == nil || stdout != "" || !strings.Contains(err.Error(), "claim created tmux") {
				t.Fatalf("stdout/error = %q / %v", stdout, err)
			}
			if store.snapshot() != registryBefore || store.writes != 0 || tmux.state() != runtimeBefore {
				t.Fatalf("after-apply claim error was not rolled back exactly:\n--- got ---\n%s\n--- want ---\n%s", tmux.state(), runtimeBefore)
			}
			if strings.Contains(warnings.String(), "unclaimed") || strings.Contains(warnings.String(), "preserved this exact residual") {
				t.Fatalf("stuck claim was diagnosed as unowned: %q", warnings.String())
			}
		})
	}
}

func TestClaimedWindowRollsBackAfterMirrorOrRegistryCommitFailure(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		inject func(*fakeResourceStore, *createCommand, *fakeTmux)
	}{
		{
			name: "later mirror",
			inject: func(_ *fakeResourceStore, _ *createCommand, tmux *fakeTmux) {
				tmux.fail = []string{"set-option", "-w", tmuxopts.AutomaticRenameWindow}
			},
		},
		{
			name: "Registry commit",
			inject: func(store *fakeResourceStore, create *createCommand, _ *fakeTmux) {
				create.store.update = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
					store.transactions++
					working := store.registry.Clone()
					if err := fn(&working); err != nil {
						return coremetadata.Registry{}, err
					}
					return coremetadata.Registry{}, errors.New("injected Registry commit failure")
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			session := tmux.addSession("alpha")
			seedOwnedSession(session, "prj-alpha", "/srv/alpha")
			seedLiveWindow(t, tmux, session, "win-alpha-main", "pan-alpha-zsh")
			registryBefore, runtimeBefore := store.snapshot(), tmux.state()
			create, _ := newTestResourceCreateCommand(t, store, tmux)
			test.inject(store, create, tmux)

			stdout, _, err := runRoute(t, create, "window", "--project", "alpha", "--name", "claimed-rollback")
			if err == nil || stdout != "" {
				t.Fatalf("stdout/error = %q / %v", stdout, err)
			}
			if store.snapshot() != registryBefore || store.writes != 0 || tmux.state() != runtimeBefore {
				t.Fatalf("claimed Window did not roll back after %s failure:\n--- got ---\n%s\n--- want ---\n%s", test.name, tmux.state(), runtimeBefore)
			}
		})
	}
}

func TestStrictTmuxRowsRejectsIncompleteIdentityInventory(t *testing.T) {
	t.Parallel()
	if _, err := strictTmuxRows("$1"+tmuxRowSep+"alpha"+tmuxRowSep+"uid\n", 4); err == nil {
		t.Fatal("strict identity inventory accepted a truncated row")
	}
}

func TestErrorCreatedHandleRejectsAmbiguousOrPreexistingIDs(t *testing.T) {
	t.Parallel()

	before := map[string]bool{"%7": true}
	after := map[string]bool{"%7": true, "%8": true, "%9": true}
	for name, output := range map[string]string{
		"two handles":        "%8\n%9\nhook failed",
		"preexisting handle": "%7\nhook failed",
		"diagnostic only":    "hook failed after split",
	} {
		t.Run(name, func(t *testing.T) {
			if got := errorCreatedHandle(output, "%", before, after); got != "" {
				t.Fatalf("errorCreatedHandle(%q) = %q, want fail-closed empty handle", output, got)
			}
		})
	}
	if got := errorCreatedHandle("%8\n'exit 7' returned 7", "%", before, after); got != "%8" {
		t.Fatalf("single exact created handle = %q, want %%8", got)
	}
}

// TestResourceCreateNegativesNeverMutate is the negative table.
func TestResourceCreateNegativesNeverMutate(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{
			// The test command has no active-target seam, which is the
			// outside-tmux observation. An omitted scope refuses there rather
			// than guessing a server or a Project.
			name: "create window outside tmux requires an explicit project scope",
			args: []string{"window"},
			want: "requires a Project: no --project <ref> was given",
		},
		{
			name: "create window takes at most one project",
			args: []string{"window", "--project", "alpha", "--project", "beta"},
			want: "accepts at most one --project",
		},
		{
			name: "an unknown project is a no-match",
			args: []string{"window", "--project", "nosuch"},
			want: "matched no projects",
		},
		{
			name: "a Project displayName is never a selector",
			args: []string{"window", "--project", "projmux"},
			want: "matched no projects",
		},
		{
			name: "a bare uid is not a selector form",
			args: []string{"window", "--project", "prj-alpha"},
			want: "matched no projects",
		},
		{
			name: "--create-window needs an exact name",
			args: []string{"pane", "--project", "alpha", "--create-window"},
			want: "requires at least one exact-name --window",
		},
		{
			name: "a uid: reference never names a Window to create",
			args: []string{"pane", "--project", "alpha", "--window", "uid:win-nope", "--create-window"},
			want: "requires at least one exact-name --window",
		},
		{
			name: "--create-window refuses a label selector",
			args: []string{"pane", "--project", "alpha", "--window", "spawn", "--create-window", "--selector", "role=shell"},
			want: "cannot be combined with --selector",
		},
		{
			name: "the placement enum stays closed",
			args: []string{"pane", "--project", "alpha", "--placement", "left"},
			want: "--placement must be one of: right, down",
		},
		{
			name: "commas are never split",
			args: []string{"pane", "--project", "alpha", "--window", "main,review"},
			want: "matched no windows",
		},
		{
			name: "a label filter that matches nothing is still a no-match",
			args: []string{"pane", "--project", "alpha", "--selector", "role=nosuch"},
			want: "matched no windows",
		},
		{
			name: "a malformed creation label is rejected",
			args: []string{"window", "--project", "alpha", "--label", "bare"},
			want: "must be key=value",
		},
		{
			name: "an unknown output token is rejected",
			args: []string{"window", "--project", "alpha", "-o", "bogus"},
			want: "invalid --output",
		},
		{
			name: "the Pane-read cwd projection is not a create projection",
			args: []string{"window", "--project", "alpha", "-o", "cwd"},
			want: "invalid --output",
		},
		{
			name: "positional arguments are refused",
			args: []string{"pane", "--project", "alpha", "right"},
			want: "does not accept positional arguments",
		},
		{
			name: "an empty payload after -- is refused",
			args: []string{"window", "--project", "alpha", "--"},
			want: "-- requires a payload",
		},
		{
			name: "a Project whose root disappeared refuses creation",
			args: []string{"window", "--project", "gone"},
			want: "MissingRoot",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			before := store.snapshot()
			tmux := newFakeTmux()
			create, _ := newTestResourceCreateCommand(t, store, tmux)

			stdout, _, err := runRoute(t, create, test.args...)
			if err == nil {
				t.Fatalf("create %v succeeded", test.args)
			}
			if !IsUsageError(err) {
				t.Fatalf("create %v error is not a usage error: %v", test.args, err)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("create %v error = %q, want it to mention %q", test.args, err, test.want)
			}
			if stdout != "" {
				t.Fatalf("create %v wrote %q to stdout, want 0 bytes", test.args, stdout)
			}
			if store.snapshot() != before || store.writes != 0 {
				t.Fatalf("create %v mutated the registry", test.args)
			}
			if len(tmux.sessions) != 0 {
				t.Fatalf("create %v materialized a runtime:\n%s", test.args, tmux.state())
			}
		})
	}
}

// TestResourceCreateOutputProjections is the output catalog table.
func TestResourceCreateOutputProjections(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		mode  string
		check func(t *testing.T, stdout string)
	}{
		{mode: "", check: func(t *testing.T, out string) {
			if out != "pane/zsh-1 created\n" {
				t.Fatalf("default = %q", out)
			}
		}},
		{mode: "name", check: func(t *testing.T, out string) {
			if out != "zsh-1\n" {
				t.Fatalf("name = %q", out)
			}
		}},
		{mode: "ref", check: func(t *testing.T, out string) {
			if out != "pane/zsh-1\n" {
				t.Fatalf("ref = %q", out)
			}
		}},
		{mode: "uid", check: func(t *testing.T, out string) {
			if !strings.HasPrefix(out, "pane-") {
				t.Fatalf("uid = %q", out)
			}
		}},
		{mode: "pane-id", check: func(t *testing.T, out string) {
			if !strings.HasPrefix(out, "%") {
				t.Fatalf("pane-id = %q, want a raw tmux handle", out)
			}
		}},
		{mode: "none", check: func(t *testing.T, out string) {
			if out != "" {
				t.Fatalf("none = %q, want empty", out)
			}
		}},
		{mode: "metadata", check: func(t *testing.T, out string) {
			if !strings.Contains(out, `"kind": "PaneMetadataList"`) {
				t.Fatalf("metadata envelope = %s", out)
			}
			if strings.Contains(out, `"spec"`) || strings.Contains(out, `"status"`) {
				t.Fatalf("-o metadata leaked spec or status: %s", out)
			}
		}},
		{mode: "json", check: func(t *testing.T, out string) {
			if !strings.Contains(out, `"kind": "PaneList"`) || !strings.Contains(out, `"spec"`) {
				t.Fatalf("json envelope = %s", out)
			}
		}},
	} {
		t.Run("mode="+test.mode, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			create, _ := newTestResourceCreateCommand(t, store, tmux)
			args := []string{"pane", "--project", "beta", "--window", "main"}
			if test.mode != "" {
				args = append(args, "-o", test.mode)
			}
			stdout, stderr, err := runRoute(t, create, args...)
			if err != nil {
				t.Fatalf("create %v error = %v", args, err)
			}
			if stderr != "" {
				t.Fatalf("stderr = %q, want none", stderr)
			}
			test.check(t, stdout)
		})
	}
}

// TestResourceCreateFanOutIsDeterministicallyOrdered pins the scalar fan-out
// order the output contract fixes at (project name, window name, uid).
func TestResourceCreateFanOutIsDeterministicallyOrdered(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, _ := newTestResourceCreateCommand(t, store, tmux)

	stdout, _, err := runRoute(t, create, "pane", "--project", "alpha", "-o", "ref")
	if err != nil {
		t.Fatalf("fan-out error = %v", err)
	}
	// alpha owns `main` and `review`; the Pane names are allocated inside each
	// Window scope, so the ordering assertion is over the owning Window name.
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("fan-out lines = %v, want one per target Window", lines)
	}
	rerunStore := newFakeResourceStore(t)
	rerunTmux := newFakeTmux()
	rerun, _ := newTestResourceCreateCommand(t, rerunStore, rerunTmux)
	again, _, err := runRoute(t, rerun, "pane", "--project", "alpha", "-o", "ref")
	if err != nil {
		t.Fatalf("fan-out rerun error = %v", err)
	}
	if again != stdout {
		t.Fatalf("fan-out order is not deterministic:\nfirst=%q\nsecond=%q", stdout, again)
	}
}

// TestCreatePayloadReachesTheRuntimeUnreinterpreted proves the `--` payload is
// forwarded verbatim and used only as the one-time name seed.
func TestCreatePayloadReachesTheRuntimeUnreinterpreted(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, _ := newTestResourceCreateCommand(t, store, tmux)

	if _, _, err := runRoute(t, create, "window", "--project", "beta", "--", "nvim", "--clean", "-o", "json"); err != nil {
		t.Fatalf("payload error = %v", err)
	}
	var command []string
	for _, call := range tmux.calls {
		argv := tmuxCommandArgv(call)
		if len(argv) > 0 && argv[0] == "new-window" {
			command = trailingCommand(argv)
		}
	}
	// The managed process supervisor prefixes the launch, and everything after
	// its `--` terminator is the operator's payload byte for byte. The
	// terminator is what makes this checkable: no payload word can be read as a
	// supervisor flag, however it is spelled.
	want := supervisedLaunchArgv("pane-test-2", "gen-test-1", "", "op-test", "", "nvim", "--clean", "-o", "json")
	if !slices.Equal(command, want) {
		t.Fatalf("payload argv = %v, want %v", command, want)
	}
	if payload := strings.Join(command[len(command)-4:], " "); payload != "nvim --clean -o json" {
		t.Fatalf("payload tail = %q, want it forwarded untouched", payload)
	}
	// The Window name seeds from the payload's command basename, not from the
	// configured shell.
	var created *coremetadata.Window
	for i := range store.registry.Windows {
		if store.registry.Windows[i].Metadata.Name == "nvim" {
			created = &store.registry.Windows[i]
		}
	}
	if created == nil {
		t.Fatalf("no Window was named after the payload command:\n%s", store.snapshot())
	}
}

// TestResourceCreateAdoptsAnAlreadyLiveWindow proves an existing transport
// binding is reused rather than duplicated.
func TestResourceCreateAdoptsAnAlreadyLiveWindow(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	session := tmux.addSession("alpha")
	seedOwnedSession(session, "prj-alpha", "/srv/alpha")
	live := seedLiveWindow(t, tmux, session, "win-alpha-main", "pan-alpha-zsh")
	create, sessions := newTestResourceCreateCommand(t, store, tmux)
	windowsBefore := tmux.windowCount()

	if _, _, err := runRoute(t, create, "pane", "--project", "alpha", "--window", "main"); err != nil {
		t.Fatalf("create pane error = %v", err)
	}
	if len(sessions.created) != 0 {
		t.Fatalf("an existing session was recreated: %v", sessions.created)
	}
	if got := tmux.windowCount(); got != windowsBefore {
		t.Fatalf("windows = %d, want %d; the live Window was duplicated", got, windowsBefore)
	}
	if got := len(live.panes); got != 2 {
		t.Fatalf("panes in the live window = %d, want 2", got)
	}
}

// TestCreateHelpAdvertisesOnlyImplementedProjections is the help-honesty audit.
//
// Two earlier Phases shipped help that promised a capability the route did not
// have. Here the advertised `-o` catalog of each resource-backed create node is
// checked against what the route actually produces, token by token.
func TestCreateHelpAdvertisesOnlyImplementedProjections(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		node     string
		spelling string
		args     []string
	}{
		{node: "window", spelling: canonicalCreateWindow, args: []string{"window", "--project", "beta"}},
		{node: "pane", spelling: canonicalCreatePane, args: []string{"pane", "--project", "beta", "--window", "main"}},
	} {
		t.Run(test.node, func(t *testing.T) {
			t.Parallel()

			create, ok := cli.LookupRoute("create")
			if !ok {
				t.Fatal("create route missing from the manifest")
			}
			var node cli.Route
			for _, child := range create.Children {
				if child.Name == test.node {
					node = child
				}
			}
			if node.Name == "" {
				t.Fatalf("create %s is not in the manifest", test.node)
			}
			if len(node.Outputs) == 0 {
				t.Fatalf("create %s advertises no output modes", test.node)
			}
			// Everything the node advertises resolves against the canonical
			// route, and every advertised token actually produces a result.
			for _, mode := range node.Outputs {
				if _, _, err := cli.ResolveOutputToken(test.spelling, string(mode)); err != nil {
					t.Fatalf("advertised mode %q does not resolve for %q: %v", mode, test.spelling, err)
				}
				store := newFakeResourceStore(t)
				cmd, _ := newTestResourceCreateCommand(t, store, newFakeTmux())
				args := append(append([]string(nil), test.args...), "-o", string(mode))
				if _, _, err := runRoute(t, cmd, args...); err != nil {
					t.Fatalf("advertised mode %q fails at runtime: %v", mode, err)
				}
			}
			// And the usage synopsis names only flags the parser defines.
			fs := flag.NewFlagSet("probe", flag.ContinueOnError)
			out := resourceCreateFlags{}
			fs.Var(&out.projects, "project", "")
			if test.node == "pane" {
				fs.Var(&out.windows, "window", "")
				fs.Var(&out.panes, "pane", "")
				fs.Var(&out.selectors, "selector", "")
				fs.Bool("create-window", false, "")
				fs.String("placement", "", "")
			}
			fs.String("name", "", "")
			fs.Var(&out.labels, "label", "")
			fs.String("output", "", "")
			for _, usage := range node.Usage {
				for token := range strings.FieldsSeq(usage) {
					token = strings.Trim(token, "[]|.")
					if !strings.HasPrefix(token, "--") {
						continue
					}
					name := strings.TrimPrefix(token, "--")
					if name == "" {
						continue
					}
					if fs.Lookup(name) == nil {
						t.Fatalf("usage line %q advertises --%s, which the %s parser does not define", usage, name, test.spelling)
					}
				}
			}
		})
	}
}

func TestSelectedSessionOwnershipFailsBeforeReconcileOrLease(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		setup func(*fakeTmux, *fakeTmuxSession)
	}{
		{name: "blank identity"},
		{name: "foreign uid", setup: func(_ *fakeTmux, session *fakeTmuxSession) {
			session.opts[tmuxopts.ProjectUIDSession] = "prj-foreign"
			session.opts[tmuxopts.ProjectPathSession] = "/srv/alpha"
		}},
		{name: "foreign root", setup: func(_ *fakeTmux, session *fakeTmuxSession) {
			session.opts[tmuxopts.ProjectUIDSession] = "prj-alpha"
			session.opts[tmuxopts.ProjectPathSession] = "/srv/foreign"
		}},
		{name: "duplicate uid on other session", setup: func(tmux *fakeTmux, session *fakeTmuxSession) {
			session.opts[tmuxopts.ProjectUIDSession] = "prj-alpha"
			session.opts[tmuxopts.ProjectPathSession] = "/srv/alpha"
			other := tmux.addSession("other")
			other.opts[tmuxopts.ProjectUIDSession] = "prj-alpha"
			other.opts[tmuxopts.ProjectPathSession] = "/srv/other"
		}},
		{name: "duplicate root on other session", setup: func(tmux *fakeTmux, session *fakeTmuxSession) {
			session.opts[tmuxopts.ProjectUIDSession] = "prj-alpha"
			session.opts[tmuxopts.ProjectPathSession] = "/srv/alpha"
			other := tmux.addSession("other")
			other.opts[tmuxopts.ProjectUIDSession] = "prj-other"
			other.opts[tmuxopts.ProjectPathSession] = "/srv/alpha"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			session := tmux.addSession("alpha")
			if test.setup != nil {
				test.setup(tmux, session)
			}
			beforeRegistry, beforeRuntime := store.snapshot(), tmux.state()
			create, _ := newTestResourceCreateCommand(t, store, tmux)
			stdout, _, err := runRoute(t, create, "window", "--project", "alpha")
			if err == nil || stdout != "" {
				t.Fatalf("stdout/error = %q / %v", stdout, err)
			}
			if store.snapshot() != beforeRegistry || store.writes != 0 || tmux.state() != beforeRuntime {
				t.Fatalf("refusal mutated state\nregistry before=%s\nafter=%s\nruntime before=%s\nafter=%s", beforeRegistry, store.snapshot(), beforeRuntime, tmux.state())
			}
			if tmux.argvContains("set-environment") || tmux.argvContains("set-option") || tmux.argvContains("new-window") {
				t.Fatalf("refusal reached a mutation: %v", tmux.calls)
			}
		})
	}
}

func TestAbsentSelectedSessionRefusesDuplicateOwnershipElsewhere(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		uid  string
		root string
	}{
		{name: "duplicate uid", uid: "prj-alpha", root: "/srv/other"},
		{name: "duplicate root", uid: "prj-other", root: "/srv/alpha"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			other := tmux.addSession("other")
			other.opts[tmuxopts.ProjectUIDSession] = test.uid
			other.opts[tmuxopts.ProjectPathSession] = test.root
			registryBefore, runtimeBefore := store.snapshot(), tmux.state()
			create, _ := newTestResourceCreateCommand(t, store, tmux)

			stdout, _, err := runRoute(t, create, "window", "--project", "alpha")
			if err == nil || stdout != "" {
				t.Fatalf("stdout/error = %q / %v", stdout, err)
			}
			if store.snapshot() != registryBefore || store.writes != 0 || tmux.state() != runtimeBefore {
				t.Fatal("absent selected-session duplicate refusal mutated state")
			}
			for _, call := range tmux.calls {
				if len(call) > 0 && slices.Contains([]string{"set-environment", "set-option", "rename-window", "new-window", "split-window"}, call[0]) {
					t.Fatalf("absent selected-session duplicate refusal issued a mutation: %v", call)
				}
			}
		})
	}
}

func TestOuterMissInnerForeignHitNeverAdoptsOrLeases(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, sessions := newTestResourceCreateCommand(t, store, tmux)
	sessions.beforeEnsureResult = func() {
		session := tmux.addSession("beta")
		session.opts[tmuxopts.ProjectUIDSession] = "prj-foreign"
		session.opts[tmuxopts.ProjectPathSession] = "/srv/beta"
	}
	before := store.snapshot()
	stdout, _, err := runRoute(t, create, "window", "--project", "beta")
	if err == nil || stdout != "" {
		t.Fatalf("stdout/error = %q / %v", stdout, err)
	}
	if store.snapshot() != before || store.writes != 0 {
		t.Fatal("outer-miss/inner-hit committed Registry state")
	}
	session := tmux.session("beta")
	if session == nil || len(session.windows) != 1 || session.opts[tmuxopts.ProjectUIDSession] != "prj-foreign" || session.env[createOperationEnvironment] != "" {
		t.Fatalf("foreign session was adopted or leased:\n%s", tmux.state())
	}
	if session.windows[0].opts[tmuxopts.WindowUID] != "" || session.windows[0].panes[0].opts[tmuxopts.PaneUID] != "" {
		t.Fatalf("foreign initial handles were ordinally adopted:\n%s", tmux.state())
	}
}

func TestOuterMissInnerUnsafeIdentityMatrixFailsClosed(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		setup func(*fakeTmux, *fakeTmuxSession)
	}{
		{name: "blank"},
		{name: "foreign", setup: func(_ *fakeTmux, session *fakeTmuxSession) {
			session.opts[tmuxopts.ProjectUIDSession] = "prj-foreign"
			session.opts[tmuxopts.ProjectPathSession] = "/srv/beta"
		}},
		{name: "duplicate uid", setup: func(tmux *fakeTmux, session *fakeTmuxSession) {
			session.opts[tmuxopts.ProjectUIDSession] = "prj-beta"
			session.opts[tmuxopts.ProjectPathSession] = "/srv/beta"
			other := tmux.addSession("duplicate-uid")
			other.opts[tmuxopts.ProjectUIDSession] = "prj-beta"
			other.opts[tmuxopts.ProjectPathSession] = "/srv/other"
		}},
		{name: "duplicate root", setup: func(tmux *fakeTmux, session *fakeTmuxSession) {
			session.opts[tmuxopts.ProjectUIDSession] = "prj-beta"
			session.opts[tmuxopts.ProjectPathSession] = "/srv/beta"
			other := tmux.addSession("duplicate-root")
			other.opts[tmuxopts.ProjectUIDSession] = "prj-other"
			other.opts[tmuxopts.ProjectPathSession] = "/srv/beta"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			create, sessions := newTestResourceCreateCommand(t, store, tmux)
			var raced *fakeTmuxSession
			sessions.beforeEnsureResult = func() {
				raced = tmux.addSession("beta")
				if test.setup != nil {
					test.setup(tmux, raced)
				}
			}
			registryBefore := store.snapshot()
			stdout, _, err := runRoute(t, create, "window", "--project", "beta")
			if err == nil || stdout != "" {
				t.Fatalf("stdout/error = %q / %v", stdout, err)
			}
			if raced == nil || store.snapshot() != registryBefore || store.writes != 0 || raced.env[createOperationEnvironment] != "" {
				t.Fatalf("outer-miss/inner-%s mutated Registry or leased the session:\n%s", test.name, tmux.state())
			}
			if raced.windows[0].opts[tmuxopts.WindowUID] != "" || raced.windows[0].panes[0].opts[tmuxopts.PaneUID] != "" {
				t.Fatalf("outer-miss/inner-%s adopted initial handles:\n%s", test.name, tmux.state())
			}
			for _, call := range tmux.calls {
				if len(call) > 0 && slices.Contains([]string{"set-environment", "set-option", "rename-window", "new-window", "split-window"}, call[0]) {
					t.Fatalf("outer-miss/inner-%s issued a tmux mutation: %v", test.name, call)
				}
			}
		})
	}
}

func TestGuardMissThenReconcilerLiveHitRefusesBeforeImportWrites(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	tmux.afterListSessions = func(tmux *fakeTmux) {
		session := tmux.addSession("beta")
		session.opts[tmuxopts.ProjectUIDSession] = "prj-foreign"
		session.opts[tmuxopts.ProjectPathSession] = "/srv/beta"
	}
	create, _ := newTestResourceCreateCommand(t, store, tmux)
	before := store.snapshot()
	stdout, _, err := runRoute(t, create, "window", "--project", "beta")
	if err == nil || stdout != "" {
		t.Fatalf("stdout/error = %q / %v", stdout, err)
	}
	if store.snapshot() != before || store.writes != 0 {
		t.Fatal("reconciler race refusal committed Registry state")
	}
	session := tmux.session("beta")
	if session == nil || session.opts[tmuxopts.ProjectUIDSession] != "prj-foreign" || session.env[createOperationEnvironment] != "" || session.windows[0].opts[tmuxopts.WindowUID] != "" {
		t.Fatalf("reconciler race imported or leased the selected foreign session:\n%s", tmux.state())
	}
}

func TestGuardedReconcileNeverWritesToSameNameReplacement(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	owned := tmux.addSession("alpha")
	seedOwnedSession(owned, "prj-alpha", "/srv/alpha")
	seedLiveWindow(t, tmux, owned, "win-alpha-main", "pan-alpha-zsh")
	var replacement *fakeTmuxSession
	identityReads := 0
	var replaceAfterGuard func(*fakeTmux)
	replaceAfterGuard = func(tmux *fakeTmux) {
		identityReads++
		if identityReads == 1 {
			tmux.afterListSessions = replaceAfterGuard
			return
		}
		tmux.sessions = slices.DeleteFunc(tmux.sessions, func(session *fakeTmuxSession) bool { return session == owned })
		replacement = tmux.addSession("alpha")
		replacement.opts[tmuxopts.ProjectUIDSession] = "prj-foreign"
		replacement.opts[tmuxopts.ProjectPathSession] = "/srv/alpha"
	}
	tmux.afterListSessions = replaceAfterGuard
	create, _ := newTestResourceCreateCommand(t, store, tmux)
	registryBefore := store.snapshot()

	stdout, _, err := runRoute(t, create, "window", "--project", "alpha", "--name", "replacement-race")
	if err == nil || stdout != "" {
		t.Fatalf("stdout/error = %q / %v", stdout, err)
	}
	if replacement == nil {
		t.Fatal("the deterministic same-name replacement race did not run")
	}
	if store.snapshot() != registryBefore || store.writes != 0 || replacement.env[createOperationEnvironment] != "" {
		t.Fatalf("same-name replacement was reconciled, leased, or committed:\n%s", tmux.state())
	}
	if replacement.opts[tmuxopts.ProjectUIDSession] != "prj-foreign" || replacement.windows[0].opts[tmuxopts.WindowUID] != "" || tmux.argvContains("new-window") {
		t.Fatalf("guarded reconcile contaminated the replacement:\n%s", tmux.state())
	}
	for _, call := range tmux.calls {
		if len(call) == 0 || !slices.Contains([]string{"set-option", "set-environment", "rename-window"}, call[0]) {
			continue
		}
		if flagValue(call, "-t") == replacement.id || flagValue(call, "-t") == replacement.name {
			t.Fatalf("guarded reconcile wrote to same-name replacement: %v", call)
		}
	}
}

func TestOuterMissInnerExactHitDoesNotAdoptInitialOrdinalHandles(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, sessions := newTestResourceCreateCommand(t, store, tmux)
	var raced *fakeTmuxSession
	sessions.beforeEnsureResult = func() {
		raced = tmux.addSession("beta")
		raced.opts[tmuxopts.ProjectUIDSession] = "prj-beta"
		raced.opts[tmuxopts.ProjectPathSession] = "/srv/beta"
	}
	if _, _, err := runRoute(t, create, "window", "--project", "beta"); err != nil {
		t.Fatalf("exact inner hit error = %v", err)
	}
	if raced == nil || raced.windows[0].opts[tmuxopts.WindowUID] != "" || raced.windows[0].panes[0].opts[tmuxopts.PaneUID] != "" {
		t.Fatalf("inner-hit initial handles were adopted:\n%s", tmux.state())
	}
}

func TestNewWindowAttributionMatrixPreservesPreexistingIdentity(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		inject func(*fakeTmux, *fakeTmuxSession, *fakeTmuxWindow, *fakeTmuxPane)
		output func(*fakeTmuxSession, *fakeTmuxWindow, *fakeTmuxPane) string
	}{
		{name: "returned preexisting handles", output: func(session *fakeTmuxSession, _ *fakeTmuxWindow, _ *fakeTmuxPane) string {
			return tmuxRowFormat(session.windows[0].id, session.windows[0].panes[0].id)
		}},
		{name: "absent after", inject: func(_ *fakeTmux, session *fakeTmuxSession, window *fakeTmuxWindow, _ *fakeTmuxPane) {
			session.windows = slices.DeleteFunc(session.windows, func(candidate *fakeTmuxWindow) bool { return candidate == window })
		}},
		{name: "wrong session", inject: func(tmux *fakeTmux, session *fakeTmuxSession, window *fakeTmuxWindow, _ *fakeTmuxPane) {
			session.windows = slices.DeleteFunc(session.windows, func(candidate *fakeTmuxWindow) bool { return candidate == window })
			tmux.session("bystander").windows = append(tmux.session("bystander").windows, window)
		}},
		{name: "two new windows", inject: func(tmux *fakeTmux, session *fakeTmuxSession, _ *fakeTmuxWindow, _ *fakeTmuxPane) {
			other := &fakeTmuxWindow{id: tmux.mint("@"), name: "hook", opts: map[string]string{}}
			other.panes = append(other.panes, newFakeTmuxPane(tmux.mint("%")))
			session.windows = append(session.windows, other)
		}},
		{name: "returned preexisting pane", output: func(session *fakeTmuxSession, window *fakeTmuxWindow, _ *fakeTmuxPane) string {
			return tmuxRowFormat(window.id, session.windows[0].panes[0].id)
		}},
		{name: "primary pane wrong window", inject: func(_ *fakeTmux, session *fakeTmuxSession, window *fakeTmuxWindow, pane *fakeTmuxPane) {
			window.panes = slices.DeleteFunc(window.panes, func(candidate *fakeTmuxPane) bool { return candidate == pane })
			session.windows[0].panes = append(session.windows[0].panes, pane)
		}},
		{name: "primary pane has mixed window owners", inject: func(_ *fakeTmux, session *fakeTmuxSession, _ *fakeTmuxWindow, pane *fakeTmuxPane) {
			session.windows[0].panes = append(session.windows[0].panes, pane)
		}},
		{name: "zero initial panes", inject: func(_ *fakeTmux, _ *fakeTmuxSession, window *fakeTmuxWindow, pane *fakeTmuxPane) {
			window.panes = slices.DeleteFunc(window.panes, func(candidate *fakeTmuxPane) bool { return candidate == pane })
		}},
		{name: "multiple initial panes", inject: func(tmux *fakeTmux, _ *fakeTmuxSession, window *fakeTmuxWindow, _ *fakeTmuxPane) {
			window.panes = append(window.panes, newFakeTmuxPane(tmux.mint("%")))
		}},
		{name: "malformed output", output: func(_ *fakeTmuxSession, _ *fakeTmuxWindow, _ *fakeTmuxPane) string {
			return "@not-a-handle"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			bystander := tmux.addSession("bystander")
			_ = bystander
			session := tmux.addSession("alpha")
			seedOwnedSession(session, "prj-alpha", "/srv/alpha")
			seed := seedLiveWindow(t, tmux, session, "win-alpha-main", "pan-alpha-zsh")
			beforeWindowOpts := maps.Clone(seed.opts)
			beforePaneOpts := maps.Clone(seed.panes[0].opts)
			beforeRegistry := store.snapshot()
			tmux.afterNewWindow = test.inject
			tmux.newWindowResult = test.output
			create, _ := newTestResourceCreateCommand(t, store, tmux)
			var warnings bytes.Buffer
			create.runtime.warn = &warnings
			beforeWindowIDs, beforePaneIDs := map[string]bool{}, map[string]bool{}
			for _, candidateSession := range tmux.sessions {
				for _, window := range candidateSession.windows {
					beforeWindowIDs[window.id] = true
					for _, pane := range window.panes {
						beforePaneIDs[pane.id] = true
					}
				}
			}
			stdout, _, err := runRoute(t, create, "window", "--project", "alpha", "--name", "race")
			if err == nil || stdout != "" {
				t.Fatalf("stdout/error = %q / %v", stdout, err)
			}
			if store.snapshot() != beforeRegistry || store.writes != 0 || !maps.Equal(seed.opts, beforeWindowOpts) || !maps.Equal(seed.panes[0].opts, beforePaneOpts) {
				t.Fatalf("attribution failure contaminated preexisting state:\n%s", tmux.state())
			}
			for _, candidateSession := range tmux.sessions {
				for _, window := range candidateSession.windows {
					if window != seed && window.opts[tmuxopts.WindowUID] != "" {
						t.Fatalf("residual Window %s was claimed: %v", window.id, window.opts)
					}
					for _, pane := range window.panes {
						if pane.opts[tmuxopts.PaneUID] != "" && pane != seed.panes[0] {
							t.Fatalf("residual Pane %s was claimed: %v", pane.id, pane.opts)
						}
					}
				}
			}
			if test.name == "two new windows" || test.name == "malformed output" {
				for _, candidateSession := range tmux.sessions {
					for _, window := range candidateSession.windows {
						if !beforeWindowIDs[window.id] && !strings.Contains(warnings.String(), window.id) {
							t.Fatalf("residual diagnostics %q omit exact Window %s", warnings.String(), window.id)
						}
						for _, pane := range window.panes {
							if !beforePaneIDs[pane.id] && !strings.Contains(warnings.String(), pane.id) {
								t.Fatalf("residual diagnostics %q omit exact Pane %s", warnings.String(), pane.id)
							}
						}
					}
				}
			}
		})
	}
}

func TestNewWindowAttributionAllowsPreexistingLinkedWindowOwnerRows(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	bystander := tmux.addSession("bystander")
	session := tmux.addSession("alpha")
	seedOwnedSession(session, "prj-alpha", "/srv/alpha")
	shared := seedLiveWindow(t, tmux, session, "win-alpha-main", "pan-alpha-zsh")
	// tmux 3.4 reports the same @N/%N once per linked session. Those repeated
	// handles are one pre-existing runtime with an owner set, not ambiguity.
	bystander.windows = append(bystander.windows, shared)
	create, _ := newTestResourceCreateCommand(t, store, tmux)

	stdout, _, err := runRoute(t, create, "window", "--project", "alpha", "--name", "linked-safe")
	if err != nil || !strings.HasPrefix(stdout, "window/") {
		t.Fatalf("stdout/error = %q / %v", stdout, err)
	}
	if shared.opts[tmuxopts.WindowUID] != "win-alpha-main" || shared.panes[0].opts[tmuxopts.PaneUID] != "pan-alpha-zsh" {
		t.Fatalf("linked pre-existing identity drifted: window=%#v pane=%#v", shared.opts, shared.panes[0].opts)
	}
	created := slices.DeleteFunc(slices.Clone(session.windows), func(window *fakeTmuxWindow) bool { return window == shared || window.opts[tmuxopts.WindowUID] == "" })
	if len(created) != 1 || len(created[0].panes) != 1 || created[0].panes[0].opts[tmuxopts.PaneUID] == "" {
		t.Fatalf("new exact Window/Pane was not attributed with linked rows:\n%s", tmux.state())
	}
}

func TestAttributeCreatedWindowUsesLinkedOwnerSetsWithoutWeakeningPaneOwnership(t *testing.T) {
	t.Parallel()
	linkedWindowOwners := runtimeOwnerSet{
		{SessionID: "$1", WindowID: "@1"}: {},
		{SessionID: "$2", WindowID: "@1"}: {},
	}
	linkedPaneOwners := runtimeOwnerSet{
		{SessionID: "$1", WindowID: "@1"}: {},
		{SessionID: "$2", WindowID: "@1"}: {},
	}
	beforeWindows := runtimeOwners{"@1": linkedWindowOwners}
	beforePanes := runtimeOwners{"%1": linkedPaneOwners}
	afterWindows := runtimeOwners{
		"@1": linkedWindowOwners,
		"@3": {{SessionID: "$1", WindowID: "@3"}: {}},
	}
	afterPanes := runtimeOwners{
		"%1": linkedPaneOwners,
		"%3": {{SessionID: "$1", WindowID: "@3"}: {}},
	}
	if result, err := attributeCreatedWindow(tmuxRowFormat("@3", "%3"), "$1", beforeWindows, beforePanes, afterWindows, afterPanes); err != nil || result.WindowID != "@3" || result.PaneID != "%3" {
		t.Fatalf("linked owner-set attribution = %+v / %v", result, err)
	}

	afterPanes["%3"][runtimeOwner{SessionID: "$1", WindowID: "@9"}] = struct{}{}
	if _, err := attributeCreatedWindow(tmuxRowFormat("@3", "%3"), "$1", beforeWindows, beforePanes, afterWindows, afterPanes); err == nil {
		t.Fatal("Pane handle shared across different Window owners was accepted")
	}
}

func TestFailedWindowAttributionDoesNotCreateChainedNameOrUIDSelector(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	session := tmux.addSession("alpha")
	seedOwnedSession(session, "prj-alpha", "/srv/alpha")
	seedLiveWindow(t, tmux, session, "win-alpha-main", "pan-alpha-zsh")
	tmux.afterNewWindow = func(tmux *fakeTmux, session *fakeTmuxSession, _ *fakeTmuxWindow, _ *fakeTmuxPane) {
		other := &fakeTmuxWindow{id: tmux.mint("@"), name: "hook", opts: map[string]string{}}
		other.panes = append(other.panes, newFakeTmuxPane(tmux.mint("%")))
		session.windows = append(session.windows, other)
	}
	create, _ := newTestResourceCreateCommand(t, store, tmux)
	registryBefore := store.snapshot()
	if stdout, _, err := runRoute(t, create, "pane", "--project", "alpha", "--window", "race", "--create-window"); err == nil || stdout != "" {
		t.Fatalf("first create stdout/error = %q / %v", stdout, err)
	}
	panesBefore := tmux.paneCount()
	tmux.afterNewWindow = nil
	for _, selector := range []string{"race", "uid:window-test-1"} {
		if stdout, _, err := runRoute(t, create, "pane", "--project", "alpha", "--window", selector); err == nil || stdout != "" {
			t.Fatalf("chained %s stdout/error = %q / %v", selector, stdout, err)
		}
		if tmux.paneCount() != panesBefore {
			t.Fatalf("chained selector %s split a foreign Window", selector)
		}
		if store.snapshot() != registryBefore || store.writes != 0 {
			t.Fatalf("chained selector %s committed failed attribution metadata", selector)
		}
		for _, window := range session.windows {
			if (window.name == "race" || window.name == "hook") && window.opts[tmuxopts.WindowUID] != "" {
				t.Fatalf("chained selector %s claimed foreign Window %s", selector, window.id)
			}
		}
	}
}

// TestAnExplicitNameCollisionLeavesZeroRuntimeObjects proves the declared
// transaction order is real: every metadata allocation happens before the first
// tmux call, so a name the operator cannot have refuses before a session, a
// window, or a pane exists.
func TestAnExplicitNameCollisionLeavesZeroRuntimeObjects(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
	}{
		{
			name: "create window",
			// `main` is already a Window name inside project beta.
			args: []string{"window", "--project", "beta", "--name", "main"},
		},
		{
			name: "create pane",
			// `zsh` is already a Pane name inside beta's `main` Window.
			args: []string{"pane", "--project", "beta", "--window", "main", "--name", "zsh"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			before := store.snapshot()
			tmux := newFakeTmux()
			create, sessions := newTestResourceCreateCommand(t, store, tmux)

			stdout, _, err := runRoute(t, create, test.args...)
			if err == nil {
				t.Fatalf("create %v accepted a colliding explicit name", test.args)
			}
			if !IsUsageError(err) {
				t.Fatalf("an explicit name collision must be a usage error: %v", err)
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want 0 bytes", stdout)
			}
			if store.snapshot() != before || store.writes != 0 {
				t.Fatal("a name collision committed registry state")
			}
			if len(sessions.created) != 0 || len(tmux.sessions) != 0 {
				t.Fatalf("a name collision materialized a runtime:\n%s", tmux.state())
			}
			if tmux.argvContains("new-window") || tmux.argvContains("split-window") {
				t.Fatalf("a name collision still issued a runtime mutation: %v", tmux.calls)
			}
		})
	}
}
