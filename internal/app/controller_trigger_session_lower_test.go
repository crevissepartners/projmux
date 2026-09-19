package app

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// controller_trigger_session_lower_test.go covers the window-unlinked fast
// path's session-projection stage: a raw `tmux kill-session` fires only
// window-unlinked, so that hook is where a live Project projection learns its
// session ended outside projmux. The stage lowers exactly the Projects recorded
// on the hook's own server, costs the ordinary hook no extra tmux call, and never
// buys the widened reconciliation pass.

// hookSessionLowerFixture is one app-owned fake server reached through the
// hook's exact `-S #{socket_path}` target, and a Registry with three live
// projections: alpha recorded on that server, beta recorded on another server,
// and gone recorded on no known server. None of the three sessions exists on
// the server unless a test adds it.
type hookSessionLowerFixture struct {
	store  *fakeResourceStore
	tmux   *fakeTmux
	routed *routedTmuxRunner
	runner *controllerTriggerRunner
	target tmuxTransport
	// inventory is the lifecycle observation. Every Registry Window and Pane is
	// live in it, so the lifecycle stage itself has nothing to project.
	inventory *exactPaneExitInventory
}

const hookSessionLowerOtherSocket = "/tmp/fake-tmux/other"

func newHookSessionLowerFixture(t *testing.T, sessions ...string) *hookSessionLowerFixture {
	t.Helper()
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	for _, name := range sessions {
		tmux.addSession(name)
	}
	for i := range store.registry.Projects {
		project := &store.registry.Projects[i]
		switch project.Metadata.UID {
		case "prj-alpha":
			project.Status.Session = &coremetadata.SessionProjection{Name: "alpha", Live: true, SocketPath: tmux.socketPath}
		case "prj-beta":
			project.Status.Session = &coremetadata.SessionProjection{Name: "beta", Live: true, SocketPath: hookSessionLowerOtherSocket}
		case "prj-gone":
			project.Status.Session = &coremetadata.SessionProjection{Name: "gone", Live: true}
		}
	}
	target, err := tmuxSocketPathTarget(tmux.socketPath)
	if err != nil {
		t.Fatal(err)
	}
	live, windows := map[string]bool{}, map[string]bool{}
	for _, pane := range store.registry.Panes {
		live[pane.Metadata.UID] = true
	}
	for _, window := range store.registry.Windows {
		windows[window.Metadata.UID] = true
	}
	inventory := &exactPaneExitInventory{uids: live, windows: windows, windowSessions: map[string]int{}}
	// The logical -L route is what makes the fake server app-owned: the
	// routed fake answers the logical-name marker for a physical -S lookup
	// only when the server is registered under its -L name.
	routed := &routedTmuxRunner{servers: map[string]*fakeTmux{"-L\x00" + defaultAppSocket: tmux}}
	fixture := &hookSessionLowerFixture{store: store, tmux: tmux, routed: routed, target: target, inventory: inventory}
	fixture.runner = &controllerTriggerRunner{
		runner: routed, store: store.store(),
		events: controllerEventLog{dir: t.TempDir()}, receipts: terminationJournal{},
		observe: func(tmuxTransport) livePaneInventory { return fixture.inventory },
		newReconciler: func(tmuxCommandRunner, sessionLister) *registryReconciler {
			t.Error("the window-unlinked session lower ran the widened reconciliation pass")
			return nil
		},
	}
	return fixture
}

func (f *hookSessionLowerFixture) unlink() controllerTrigger {
	return controllerTrigger{reason: controllerTriggerWindowUnlinked, target: f.target, session: "$1", hookWindow: "@4"}
}

func (f *hookSessionLowerFixture) project(t *testing.T, uid string) coremetadata.Project {
	t.Helper()
	project, ok := f.store.registry.Project(uid)
	if !ok {
		t.Fatalf("Project %s is missing", uid)
	}
	return project.Clone()
}

// tmuxCalls counts the routed calls whose argv contains every token.
func (f *hookSessionLowerFixture) tmuxCalls(tokens ...string) int {
	count := 0
	for _, call := range f.routed.calls {
		if !slices.ContainsFunc(tokens, func(token string) bool { return !slices.Contains(call.args, token) }) {
			count++
		}
	}
	return count
}

// TestWindowUnlinkedFastPathLowersTheLiveFlagOfASessionEndedOnTheHookServer is
// the kill-session case: the session is gone from the exact server the
// projection recorded, so the hook's own fast path lowers live to false and
// keeps the session name and socketPath. The lower is reported but is not a
// changed pass, so no widened reconciliation runs because of it.
func TestWindowUnlinkedFastPathLowersTheLiveFlagOfASessionEndedOnTheHookServer(t *testing.T) {
	t.Parallel()
	fixture := newHookSessionLowerFixture(t, "keeper")

	outcome, err := fixture.runner.run(context.Background(), fixture.unlink())
	if err != nil {
		t.Fatalf("window-unlinked: %v", err)
	}
	alpha := fixture.project(t, "prj-alpha")
	want := coremetadata.SessionProjection{Name: "alpha", Live: false, SocketPath: fixture.tmux.socketPath}
	if alpha.Status.Session == nil || *alpha.Status.Session != want {
		t.Fatalf("alpha session = %+v, want %+v", alpha.Status.Session, want)
	}
	if outcome.sessionsLowered != 1 || !strings.Contains(outcome.describe(), " lowered=1") {
		t.Fatalf("outcome = %s, want one lowered session reported", outcome.describe())
	}
	// A raw kill-session leaves its unlink awaiting a pane-exited that never
	// comes, so the realistic outcome is a deferral that still reports the
	// lower. What matters is that the lower bought no further pass.
	if outcome.passes != 1 || outcome.changed != 0 {
		t.Fatalf("outcome = %s, want one unwidened pass", outcome.describe())
	}
	if fixture.store.writes != 1 {
		t.Fatalf("Registry writes = %d, want the one convergent lower", fixture.store.writes)
	}

	// A repeat of the same hook is a no-op by the seam's idempotence, and its
	// pre-lock filter no longer finds a candidate.
	again, err := fixture.runner.run(context.Background(), fixture.unlink())
	if err != nil || again.sessionsLowered != 0 || strings.Contains(again.describe(), "lowered=") || fixture.store.writes != 1 {
		t.Fatalf("repeat = %s (%v), writes %d, want a silent no-op", again.describe(), err, fixture.store.writes)
	}
}

// TestWindowUnlinkedFastPathKeepsALiveSessionThatStillExistsOnTheHookServer is
// the kill-window case: one of the session's Windows went away and the session
// did not, so the projection stays live and the Registry is not written.
func TestWindowUnlinkedFastPathKeepsALiveSessionThatStillExistsOnTheHookServer(t *testing.T) {
	t.Parallel()
	fixture := newHookSessionLowerFixture(t, "alpha", "keeper")
	before := fixture.store.registry.Clone()

	outcome, err := fixture.runner.run(context.Background(), fixture.unlink())
	if err != nil {
		t.Fatalf("window-unlinked: %v", err)
	}
	if alpha := fixture.project(t, "prj-alpha"); alpha.Status.Session == nil || !alpha.Status.Session.Live {
		t.Fatalf("alpha session = %+v, want it still live", alpha.Status.Session)
	}
	if outcome.sessionsLowered != 0 || fixture.store.writes != 0 || !reflect.DeepEqual(fixture.store.registry, before) {
		t.Fatalf("outcome = %s, writes %d, want no Registry write", outcome.describe(), fixture.store.writes)
	}
	if routes := fixture.tmuxCalls("#{pid}"); routes != 0 {
		t.Fatalf("route verifications = %d, want none when no lower is due", routes)
	}
}

// TestWindowUnlinkedFastPathLeavesProjectsRecordedOnAnotherOrNoServerByteIdentical
// pins the evidence boundary: absence on the hook's server says nothing about a
// session recorded on another server, or on no known server, even while the
// same pass lowers the Project recorded on the hook's server.
func TestWindowUnlinkedFastPathLeavesProjectsRecordedOnAnotherOrNoServerByteIdentical(t *testing.T) {
	t.Parallel()
	fixture := newHookSessionLowerFixture(t, "keeper")
	beta, gone := fixture.project(t, "prj-beta"), fixture.project(t, "prj-gone")

	outcome, err := fixture.runner.run(context.Background(), fixture.unlink())
	if err != nil {
		t.Fatalf("window-unlinked: %v", err)
	}
	if outcome.sessionsLowered != 1 {
		t.Fatalf("outcome = %s, want alpha alone lowered", outcome.describe())
	}
	if got := fixture.project(t, "prj-beta"); !reflect.DeepEqual(got, beta) {
		t.Fatalf("Project recorded on another server = %+v, want byte-identical %+v", got.Status.Session, beta.Status.Session)
	}
	if got := fixture.project(t, "prj-gone"); !reflect.DeepEqual(got, gone) {
		t.Fatalf("Project recorded on no server = %+v, want byte-identical %+v", got.Status.Session, gone.Status.Session)
	}
}

// TestWindowUnlinkedFastPathWithoutACandidateCostsNoTmuxCall is the ordinary
// hook: no live projection records this server, so the lock-free filter ends
// the stage before any session list or route verification. The one
// list-sessions the pass issues is the control-target stage's, which every
// pass already paid for.
func TestWindowUnlinkedFastPathWithoutACandidateCostsNoTmuxCall(t *testing.T) {
	t.Parallel()
	fixture := newHookSessionLowerFixture(t, "keeper")
	fixture.store.registry.Projects[0].Status.Session.SocketPath = hookSessionLowerOtherSocket

	outcome, err := fixture.runner.run(context.Background(), fixture.unlink())
	if err != nil {
		t.Fatalf("window-unlinked: %v", err)
	}
	if outcome.sessionsLowered != 0 || fixture.store.writes != 0 || fixture.store.transactions != 0 {
		t.Fatalf("outcome = %s, transactions %d, want no Registry transaction", outcome.describe(), fixture.store.transactions)
	}
	var argv []string
	for _, call := range fixture.routed.calls {
		argv = append(argv, strings.Join(call.args, " "))
	}
	if !reflect.DeepEqual(argv, []string{"list-sessions -F #{session_name}"}) {
		t.Fatalf("tmux calls = %q, want only the control-target stage's session list", argv)
	}
}

// TestWindowUnlinkedFastPathRereadsSessionsUnderTheRegistryLock pins why the
// session list that decides the lower is taken inside the transaction: a
// create that owned the lock may have started the session again after the
// lock-free read, and a lower from the stale read would be false.
func TestWindowUnlinkedFastPathRereadsSessionsUnderTheRegistryLock(t *testing.T) {
	t.Parallel()
	fixture := newHookSessionLowerFixture(t, "keeper")
	locked := fixture.runner.store.updateConvergent
	fixture.runner.store.updateConvergent = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error) {
		if fixture.tmux.session("alpha") == nil {
			fixture.tmux.addSession("alpha")
		}
		return locked(fn)
	}

	outcome, err := fixture.runner.run(context.Background(), fixture.unlink())
	if err != nil {
		t.Fatalf("window-unlinked: %v", err)
	}
	if alpha := fixture.project(t, "prj-alpha"); alpha.Status.Session == nil || !alpha.Status.Session.Live {
		t.Fatalf("alpha session = %+v, want the recreated session kept live", alpha.Status.Session)
	}
	if outcome.sessionsLowered != 0 || fixture.store.writes != 0 {
		t.Fatalf("outcome = %s, writes %d, want no lower", outcome.describe(), fixture.store.writes)
	}
}

// TestWindowUnlinkedFastPathLowersNothingWhenTheHookServerIsGone keeps server
// absence out of this stage: a server that is not up is a different judgement
// from a session that ended on a server that is, and it is not an error here.
func TestWindowUnlinkedFastPathLowersNothingWhenTheHookServerIsGone(t *testing.T) {
	t.Parallel()
	fixture := newHookSessionLowerFixture(t)
	fixture.tmux.fail = []string{"list-sessions"}
	fixture.tmux.failMessage = "no server running on " + fixture.tmux.socketPath
	fixture.tmux.failAlways = true

	outcome, err := fixture.runner.run(context.Background(), fixture.unlink())
	if err != nil {
		t.Fatalf("window-unlinked on an absent server: %v", err)
	}
	if alpha := fixture.project(t, "prj-alpha"); alpha.Status.Session == nil || !alpha.Status.Session.Live {
		t.Fatalf("alpha session = %+v, want it untouched", alpha.Status.Session)
	}
	if outcome.sessionsLowered != 0 || fixture.store.writes != 0 {
		t.Fatalf("outcome = %s, writes %d, want no lower", outcome.describe(), fixture.store.writes)
	}
}

// TestWindowUnlinkedSessionLowerWriteFailureReachesTheControllerOutcome is the
// error path: a Registry write that fails is a failed pass, the worker retries
// it within its bound and then returns the error, and the hidden hook route
// returns it too -- which is what RecordOutcome journals as result=error for
// `internal tmux converge` (TestRecordOutcomeAutomaticHookAndPollSuccessZeroErrorOne).
func TestWindowUnlinkedSessionLowerWriteFailureReachesTheControllerOutcome(t *testing.T) {
	t.Parallel()
	writeErr := errors.New("registry write refused")
	failing := func(f *hookSessionLowerFixture) *int {
		attempts := 0
		f.runner.store.updateConvergent = func(func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error) {
			attempts++
			return coremetadata.Registry{}, false, writeErr
		}
		return &attempts
	}

	fixture := newHookSessionLowerFixture(t, "keeper")
	attempts := failing(fixture)
	outcome, err := fixture.runner.run(context.Background(), fixture.unlink())
	if !errors.Is(err, writeErr) {
		t.Fatalf("run err = %v, want the Registry write failure", err)
	}
	if outcome.passes != controllerTriggerMaxRetries+1 || *attempts != controllerTriggerMaxRetries+1 {
		t.Fatalf("outcome = %s after %d write attempts, want the bounded retries", outcome.describe(), *attempts)
	}
	if alpha := fixture.project(t, "prj-alpha"); alpha.Status.Session == nil || !alpha.Status.Session.Live {
		t.Fatalf("alpha session = %+v, want no partial lower", alpha.Status.Session)
	}

	route := newHookSessionLowerFixture(t, "keeper")
	failing(route)
	cmd := &tmuxCommand{runner: route.routed, triggerRunner: route.runner}
	var stdout, stderr bytes.Buffer
	err = cmd.Run([]string{"converge", "--socket-path", route.target.Value, "--session", "$1",
		"--reason", "window-unlinked", "--hook-window", "@4"}, &stdout, &stderr)
	if !errors.Is(err, writeErr) {
		t.Fatalf("hidden converge route err = %v (stderr %q), want the Registry write failure", err, stderr.String())
	}
}

// TestPaneExitedFastPathDoesNotLowerSessionProjections keeps the stage on the
// one hook that a raw kill-session fires: a pane exit is not evidence about
// the session, so pane-exited never runs the session list or the lower.
func TestPaneExitedFastPathDoesNotLowerSessionProjections(t *testing.T) {
	t.Parallel()
	fixture := newHookSessionLowerFixture(t, "keeper")

	outcome, err := fixture.runner.run(context.Background(), controllerTrigger{
		reason: controllerTriggerPaneExited, target: fixture.target, hookPane: "%99",
	})
	if err != nil {
		t.Fatalf("pane-exited: %v", err)
	}
	if alpha := fixture.project(t, "prj-alpha"); alpha.Status.Session == nil || !alpha.Status.Session.Live {
		t.Fatalf("alpha session = %+v, want pane-exited to leave it live", alpha.Status.Session)
	}
	if outcome.sessionsLowered != 0 || fixture.store.writes != 0 || fixture.tmuxCalls("#{pid}") != 0 {
		t.Fatalf("outcome = %s, writes %d, want no lower", outcome.describe(), fixture.store.writes)
	}
}

// TestWindowUnlinkedFastPathDoesNotLowerWhenTheLifecycleStageIsUnobserved
// keeps the fail-closed rule: a pass whose exact-host lifecycle observation
// could not be taken touches nothing, including session projections.
func TestWindowUnlinkedFastPathDoesNotLowerWhenTheLifecycleStageIsUnobserved(t *testing.T) {
	t.Parallel()
	fixture := newHookSessionLowerFixture(t, "keeper")
	fixture.inventory.err = errors.New("inventory unreadable")

	outcome, err := fixture.runner.run(context.Background(), fixture.unlink())
	if err != nil {
		t.Fatalf("window-unlinked: %v", err)
	}
	if outcome.unverified == "" {
		t.Fatalf("outcome = %s, want the unobserved lifecycle stage reported", outcome.describe())
	}
	if alpha := fixture.project(t, "prj-alpha"); alpha.Status.Session == nil || !alpha.Status.Session.Live {
		t.Fatalf("alpha session = %+v, want it untouched", alpha.Status.Session)
	}
	if outcome.sessionsLowered != 0 || fixture.store.writes != 0 {
		t.Fatalf("outcome = %s, writes %d, want no lower", outcome.describe(), fixture.store.writes)
	}
}
