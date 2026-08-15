package app

import (
	"context"
	"errors"
	"flag"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// fakeSessionMaterializer stands in for the tmux client's session half.
//
// It reproduces the only two behaviors the create routes depend on: a session
// that already exists is reused untouched, and a pre-create hook refusal fails
// before anything is created. A post-create hook failure is modeled the way the
// real client handles it -- logged, ignored, creation continues.
type fakeSessionMaterializer struct {
	tmux         *fakeTmux
	preCreateErr error
	postCreate   func()
	created      []string
}

func (f *fakeSessionMaterializer) SessionExists(_ context.Context, name string) (bool, error) {
	return f.tmux.session(name) != nil, nil
}

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
	return nil
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
			runner:   tmux,
			mirror:   mirror,
			sessions: sessions,
			warn:     testWarnWriter{t},
		},
		shell:          "/bin/zsh",
		sessionNameFor: filepath.Base,
		newOperationID: func() (string, error) { return "op-test", nil },
	}, sessions
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
	window.panes = append(window.panes, &fakeTmuxPane{
		id:   tmux.mint("%"),
		opts: map[string]string{tmuxopts.PaneUID: paneUID},
	})
	session.windows = append(session.windows, window)
	return window
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
		if len(call) == 0 {
			continue
		}
		for _, verb := range focusMovingCommands {
			if call[0] == verb {
				t.Fatalf("create issued a client-moving command: %v", call)
			}
		}
		if call[0] == "new-window" || call[0] == "split-window" {
			if !containsAll(call, []string{"-d"}) {
				t.Fatalf("%s must be detached: %v", call[0], call)
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
			if len(call) > 0 && call[0] == "split-window" {
				anchors[flagValue(call, "-t")]++
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
			if len(call) > 0 && call[0] == "split-window" {
				split = append(split, flagValue(call, "-t"))
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
				if len(call) > 0 && call[0] == "split-window" {
					found = true
					if !containsAll(call, []string{test.wantFlag}) {
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
		runtime := &materializer{runner: tmux, mirror: intmetadata.NewMirror(tmux), warn: testWarnWriter{t}}

		ledger := &runtimeLedger{}
		ledger.record(runtimeWindow, window.id, "win-alpha-main")
		// Another operation re-bound the window between creation and rollback.
		window.opts[tmuxopts.WindowUID] = "win-someone-else"

		runtime.rollback(context.Background(), ledger)
		if _, got := tmux.window(window.id); got == nil {
			t.Fatal("rollback removed a window that no longer carried this operation's uid")
		}

		// With the uid restored it is this operation's object again.
		window.opts[tmuxopts.WindowUID] = "win-alpha-main"
		runtime.rollback(context.Background(), ledger)
		if _, got := tmux.window(window.id); got != nil {
			t.Fatal("rollback kept an object this operation created")
		}
	})
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
			name: "create window requires a project scope",
			args: []string{"window"},
			want: "requires exactly one --project",
		},
		{
			name: "create window takes at most one project",
			args: []string{"window", "--project", "alpha", "--project", "beta"},
			want: "requires exactly one --project",
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
	var command string
	for _, call := range tmux.calls {
		if len(call) > 0 && call[0] == "new-window" {
			command = strings.Join(trailingCommand(call), " ")
		}
	}
	if command != "nvim --clean -o json" {
		t.Fatalf("payload argv = %q, want it forwarded untouched", command)
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

// TestCreatePaneWithoutAProjectKeepsTheLegacyShellSplit proves the dispatch
// carve-out: without --project the route is byte-for-byte the shell split it
// already shipped.
func TestCreatePaneWithoutAProjectKeepsTheLegacyShellSplit(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "bare create pane",
			args: []string{"pane"},
			want: []string{"split", "--agent", "shell", "right"},
		},
		{
			name: "placement and output still normalize onto the legacy argv",
			args: []string{"pane", "--placement", "down", "-o", "pane-id"},
			want: []string{"split", "--agent", "shell", "--print-pane-id", "down"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			create, split := newTestCreateCommand()
			if _, _, err := runRoute(t, create, test.args...); err != nil {
				t.Fatalf("create %v error = %v", test.args, err)
			}
			if len(split.calls) != 1 {
				t.Fatalf("split calls = %v, want exactly one", split.calls)
			}
			if strings.Join(split.calls[0], " ") != strings.Join(test.want, " ") {
				t.Fatalf("argv = %v, want %v", split.calls[0], test.want)
			}
		})
	}

	// The discriminator is the flag itself, in every spelling the flag package
	// accepts, and it never fires on payload text.
	for _, test := range []struct {
		args []string
		want bool
	}{
		{args: []string{"--project", "alpha"}, want: true},
		{args: []string{"--project=alpha"}, want: true},
		{args: []string{"-project", "alpha"}, want: true},
		{args: []string{"--placement", "down"}, want: false},
		{args: []string{"--", "--project", "alpha"}, want: false},
		{args: nil, want: false},
	} {
		if got := hasProjectFlag(test.args); got != test.want {
			t.Fatalf("hasProjectFlag(%v) = %v, want %v", test.args, got, test.want)
		}
	}
}

// TestResourceCreateAdoptsAnAlreadyLiveWindow proves an existing transport
// binding is reused rather than duplicated.
func TestResourceCreateAdoptsAnAlreadyLiveWindow(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	session := tmux.addSession("alpha")
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
