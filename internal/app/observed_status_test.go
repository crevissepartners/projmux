package app

import (
	"context"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// observed_status_test.go is the acceptance table of the Window/Pane observed
// status contract, driven end to end: a real intmetadata.Mirror over a fake
// tmux server, the real read verb, and the real registry shape.
//
// Every assertion here is stated against the machine -- how many panes tmux
// actually has, which uids it actually mirrors -- rather than against anything
// the registry stored. That is the point: the defect was a status read that
// could disagree with the machine indefinitely, so the tests have to be able to
// notice the disagreement.

// observedStatusFixture is one fake tmux server plus the registry that claims
// its objects.
type observedStatusFixture struct {
	tmux     *fakeTmux
	registry coremetadata.Registry
}

// newObservedStatusFixture wires a Project whose stored session projection says
// live -- exactly the state the legacy import wrote once and never refreshed --
// onto a server that really is running its two Windows and three Panes.
func newObservedStatusFixture(t *testing.T) *observedStatusFixture {
	t.Helper()

	root := t.TempDir()
	tmux := newFakeTmux()
	session := tmux.addSession("alpha")
	// addSession leaves one uid-less window behind, which is the common real
	// shape: a live tmux object that no registry resource claims. It must never
	// make anything live.
	seedLiveWindow(t, tmux, session, "win-alpha", "pan-alpha")
	window := seedLiveWindow(t, tmux, session, "win-beta", "pan-beta")
	window.panes = append(window.panes, &fakeTmuxPane{
		id:   tmux.mint("%"),
		opts: map[string]string{tmuxopts.PaneUID: "pan-beta-second"},
	})
	return &observedStatusFixture{tmux: tmux, registry: runtimeBindingRegistry(t, root)}
}

// get runs one `get <kind>` invocation as a fresh process would: a new command
// object, so a new observation is taken against the machine as it is now.
func (f *observedStatusFixture) get(t *testing.T, args ...string) string {
	t.Helper()
	registry := f.registry
	cmd := &getCommand{
		loadRegistry: func() (coremetadata.Registry, error) { return registry, nil },
		currentPath:  &stubCurrentPath{},
		runtime:      tmuxRuntimeLookup(intmetadata.NewMirror(f.tmux)),
	}
	stdout, stderr, err := runGet(t, cmd, args...)
	if err != nil {
		t.Fatalf("get %v error = %v (stderr %q)", args, err, stderr)
	}
	return stdout
}

// liveCount counts the `status=live` rows of a plural read.
func liveCount(stdout string) int {
	count := 0
	for line := range strings.SplitSeq(strings.TrimSpace(stdout), "\n") {
		if strings.Contains(line, "status=live") {
			count++
		}
	}
	return count
}

// TestGetPanesNeverReportsMoreLivePanesThanTmuxHas is acceptance criterion 1,
// and it is stated as an inequality against the machine on purpose.
//
// A count assertion against a fixed number would pass on a registry that
// happened to be the right size. The real invariant is that the reported live
// set is a subset of what tmux is running, and it has to hold while panes are
// disappearing underneath the reader.
func TestGetPanesNeverReportsMoreLivePanesThanTmuxHas(t *testing.T) {
	t.Parallel()

	fixture := newObservedStatusFixture(t)

	// All three registry Panes are mirrored, and tmux has a fourth pane that no
	// resource claims.
	if got, want := fixture.tmux.paneCount(), 4; got != want {
		t.Fatalf("fixture tmux pane count = %d, want %d", got, want)
	}
	if got := liveCount(fixture.get(t, "panes")); got != 3 {
		t.Fatalf("live panes = %d, want the 3 mirrored ones", got)
	}

	// Now tear the server down one pane at a time. After every kill the
	// reported live count must still be no more than what tmux has.
	for {
		panes := fixture.tmux.paneCount()
		got := liveCount(fixture.get(t, "panes"))
		if got > panes {
			t.Fatalf("reported %d live panes against a tmux server with %d panes", got, panes)
		}
		id := fixture.firstPaneID()
		if id == "" {
			break
		}
		if _, err := fixture.tmux.Run(context.Background(), "tmux", "kill-pane", "-t", id); err != nil {
			t.Fatalf("kill-pane %s: %v", id, err)
		}
	}
	if got := liveCount(fixture.get(t, "panes")); got != 0 {
		t.Fatalf("live panes = %d against an empty tmux server, want 0", got)
	}
}

// firstPaneID returns any live pane id, or "" when the server has none left.
func (f *observedStatusFixture) firstPaneID() string {
	for _, session := range f.tmux.sessions {
		for _, window := range session.windows {
			for _, pane := range window.panes {
				return pane.id
			}
		}
	}
	return ""
}

// TestClosingAPaneOfflinesItOnTheNextQueryWithNoHookFired is acceptance
// criterion 3.
//
// No hook runs anywhere in this test and no mutation route is invoked, so the
// registry file is never written. The only thing that changes between the two
// reads is the machine, and the second read has to notice.
func TestClosingAPaneOfflinesItOnTheNextQueryWithNoHookFired(t *testing.T) {
	t.Parallel()

	fixture := newObservedStatusFixture(t)
	before := fixture.get(t, "panes")
	if !strings.Contains(before, "pane/log status=live") {
		t.Fatalf("the mirrored pane did not start live:\n%s", before)
	}

	// pan-beta-second is the Pane named "log". Close its tmux pane and nothing
	// else -- no hook, no reconcile, no write.
	closed := fixture.paneIDForUID(t, "pan-beta-second")
	if _, err := fixture.tmux.Run(context.Background(), "tmux", "kill-pane", "-t", closed); err != nil {
		t.Fatalf("kill-pane: %v", err)
	}

	after := fixture.get(t, "panes")
	if !strings.Contains(after, "pane/log status=offline") {
		t.Fatalf("the closed pane is still not offline on the next query:\n%s", after)
	}
	// Criterion 5: the panes that are genuinely still running must not have
	// been dragged offline with it.
	if !strings.Contains(after, "pane/zsh status=live") {
		t.Fatalf("a live pane was mis-reported offline:\n%s", after)
	}
	if got := liveCount(after); got != 2 {
		t.Fatalf("live panes = %d after closing one of three, want 2:\n%s", got, after)
	}
	// Criterion 4, the read half: the Pane is still fully queryable.
	if _, ok := fixture.registry.Pane("pan-beta-second"); !ok {
		t.Fatal("a closed pane removed its registry resource")
	}
	if got := fixture.get(t, "panes", "--pane", "log"); !strings.Contains(got, "pane/log") {
		t.Fatalf("the closed pane is no longer selectable: %q", got)
	}
}

// paneIDForUID resolves the fake server's pane id for one mirrored uid.
func (f *observedStatusFixture) paneIDForUID(t *testing.T, uid string) string {
	t.Helper()
	id, err := intmetadata.NewMirror(f.tmux).PaneTargetForUID(context.Background(), uid)
	if err != nil {
		t.Fatalf("resolve pane target for %q: %v", uid, err)
	}
	return id
}

// TestNothingIsLiveWhenNothingMirrorsAUID is acceptance criterion 2, and it is
// the exact machine state that produced the defect: a Project whose stored
// status.session.live is true, a tmux server that is up, and not one object on
// it carrying a @projmux uid.
//
// Everything must read offline. A registry object bound to nothing live is an
// orphan, and an orphan is not live.
func TestNothingIsLiveWhenNothingMirrorsAUID(t *testing.T) {
	t.Parallel()

	fixture := newObservedStatusFixture(t)
	project, ok := fixture.registry.Project("prj-alpha")
	if !ok || project.Status.Session == nil || !project.Status.Session.Live {
		t.Fatal("the fixture Project must claim a live session for this test to mean anything")
	}

	// Strip every mirrored uid, leaving the windows and panes themselves up.
	// This is the tmux-server-restart shape: the objects survived, the options
	// did not.
	for _, session := range fixture.tmux.sessions {
		for _, window := range session.windows {
			delete(window.opts, tmuxopts.WindowUID)
			for _, pane := range window.panes {
				delete(pane.opts, tmuxopts.PaneUID)
			}
		}
	}

	for _, kind := range []string{"windows", "panes"} {
		stdout := fixture.get(t, kind)
		if got := liveCount(stdout); got != 0 {
			t.Fatalf("get %s reported %d live against a server mirroring no uid:\n%s", kind, got, stdout)
		}
		// Preserved, not deleted: every resource still lists.
		if strings.Count(strings.TrimSpace(stdout), "\n")+1 < 2 {
			t.Fatalf("get %s stopped listing offline resources:\n%s", kind, stdout)
		}
	}

	// The Project's own status is unchanged by this Phase: its runtime object
	// is a tmux session, not a mirrored uid.
	if got := liveCount(fixture.get(t, "projects")); got != 1 {
		t.Fatal("the Project session projection stopped being the Project's status source")
	}
}

// TestStoredSessionLivenessCannotMakeAWindowOrPaneLive is the negative guard at
// the route level: the search for a remaining stored-value path, expressed as a
// test rather than as a grep.
//
// It flips the Project's stored liveness both ways against one unchanged
// machine and requires byte-identical output. Any path that still consulted the
// bool -- in the resolver, in the summary renderer, in a helper somebody adds
// later -- shows up here as a diff.
func TestStoredSessionLivenessCannotMakeAWindowOrPaneLive(t *testing.T) {
	t.Parallel()

	fixture := newObservedStatusFixture(t)
	// Exactly one pane keeps its mirrored uid, so the expected answer has both
	// a live row and offline rows and a leak in either direction is visible.
	for _, session := range fixture.tmux.sessions {
		for _, window := range session.windows {
			delete(window.opts, tmuxopts.WindowUID)
			for _, pane := range window.panes {
				if pane.opts[tmuxopts.PaneUID] != "pan-alpha" {
					delete(pane.opts, tmuxopts.PaneUID)
				}
			}
		}
	}

	for _, kind := range []string{"windows", "panes"} {
		outputs := map[bool]string{}
		for _, live := range []bool{true, false} {
			project, ok := fixture.registry.Project("prj-alpha")
			if !ok {
				t.Fatal("the fixture Project disappeared")
			}
			project.Status.Session.Live = live
			outputs[live] = fixture.get(t, kind)
		}
		if outputs[true] != outputs[false] {
			t.Fatalf("get %s changed with the stored session bool:\nlive=true:\n%s\nlive=false:\n%s",
				kind, outputs[true], outputs[false])
		}
		if kind == "panes" && liveCount(outputs[true]) != 1 {
			t.Fatalf("get panes reported %d live, want exactly the mirrored one:\n%s",
				liveCount(outputs[true]), outputs[true])
		}
		if kind == "windows" && liveCount(outputs[true]) != 0 {
			t.Fatalf("get windows reported %d live with no mirrored window uid:\n%s",
				liveCount(outputs[true]), outputs[true])
		}
	}
}
