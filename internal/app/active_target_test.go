package app

import (
	"context"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// recordedActiveTarget is the fake half of the active-target seam.
//
// It answers the observation without tmux and counts how many times a route
// consulted it, which is what turns "the fallback did not fire" into a measured
// claim rather than an inference from the output.
type recordedActiveTarget struct {
	inside    bool
	paneID    string
	paneUID   string
	windowUID string
	calls     int
}

func (r *recordedActiveTarget) lookup() (activeTargetObserver, bool) {
	r.calls++
	if !r.inside {
		return activeTargetObserver{}, false
	}
	return activeTargetObserver{
		paneID:    r.paneID,
		paneUID:   func() string { return r.paneUID },
		windowUID: func() string { return r.windowUID },
	}, true
}

// insideTmux builds a lookup that reports the given mirror values on pane %46.
func insideTmux(paneUID, windowUID string) *recordedActiveTarget {
	return &recordedActiveTarget{inside: true, paneID: "%46", paneUID: paneUID, windowUID: windowUID}
}

func outsideTmux() *recordedActiveTarget { return &recordedActiveTarget{} }

// activeTargetTmuxRunner records the tmux argv the production lookup issues.
type activeTargetTmuxRunner struct {
	calls   [][]string
	replies map[string]string
}

func (r *activeTargetTmuxRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	key := strings.Join(args, " ")
	return []byte(r.replies[key] + "\n"), nil
}

func newTestDescribeCommandWithActiveTarget(t *testing.T, store *fakeResourceStore, active *recordedActiveTarget) *describeCommand {
	t.Helper()
	return &describeCommand{loadRegistry: store.store().load, activeTarget: active.lookup}
}

func newTestRenameCommandWithActiveTarget(store *fakeResourceStore, active *recordedActiveTarget) *renameCommand {
	return &renameCommand{store: store.store(), activeTarget: active.lookup}
}

func newTestRebindCommandWithActiveTarget(store *fakeResourceStore, active *recordedActiveTarget) *rebindCommand {
	return &rebindCommand{store: store.store(), activeTarget: active.lookup}
}

func newTestPaneGetCommandWithActiveTarget(t *testing.T, store *fakeResourceStore, active *recordedActiveTarget, current *stubCurrentPath) *getCommand {
	t.Helper()
	return &getCommand{loadRegistry: store.store().load, currentPath: current, activeTarget: active.lookup}
}

// TestActiveTargetIsDecidedByTheEnvironmentNotByWhetherTmuxAnswers pins the
// inside-tmux test itself.
//
// The tempting implementation -- run `tmux display-message -p` and see whether
// it works -- is wrong: a bare display-message from outside a client still
// succeeds against a running server and silently answers for the
// most-recently-used session. So the decision is made from $TMUX_PANE plus
// $TMUX before any tmux process is started, and the pane id is passed as an
// explicit `-t` target rather than being left implicit.
func TestActiveTargetIsDecidedByTheEnvironmentNotByWhetherTmuxAnswers(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		env  map[string]string
		want bool
	}{
		{name: "inside a client", env: map[string]string{"TMUX": "/tmp/tmux-1000/projmux,1,0", "TMUX_PANE": "%46"}, want: true},
		{name: "no environment at all", env: nil},
		{name: "a tmux server exists but this process is not a client", env: map[string]string{}},
		{name: "TMUX without a pane cannot name an explicit target", env: map[string]string{"TMUX": "/tmp/tmux-1000/projmux,1,0"}},
		{name: "a leaked TMUX_PANE alone may address a different server", env: map[string]string{"TMUX_PANE": "%46"}},
		{name: "whitespace is not a pane id", env: map[string]string{"TMUX": " ", "TMUX_PANE": " "}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := &activeTargetTmuxRunner{}
			lookup := tmuxActiveTargetLookup(func(key string) string { return test.env[key] }, intmetadata.NewMirror(runner))
			observer, inside := lookup()
			if inside != test.want {
				t.Fatalf("inside = %v, want %v", inside, test.want)
			}
			if !test.want {
				// Deciding "not inside" must cost zero tmux calls, or a
				// non-client invocation would still be talking to a server.
				if len(runner.calls) != 0 {
					t.Fatalf("a non-client invocation issued tmux calls: %v", runner.calls)
				}
				return
			}
			if observer.paneID != "%46" {
				t.Fatalf("paneID = %q, want %%46", observer.paneID)
			}
			observer.mirroredPaneUID()
			observer.mirroredWindowUID()
			want := [][]string{
				{"tmux", "display-message", "-p", "-t", "%46", "-F", "#{@projmux_pane_uid}"},
				{"tmux", "display-message", "-p", "-t", "%46", "-F", "#{@projmux_window_uid}"},
			}
			if len(runner.calls) != len(want) {
				t.Fatalf("tmux calls = %v, want %v", runner.calls, want)
			}
			for i, call := range runner.calls {
				if strings.Join(call, " ") != strings.Join(want[i], " ") {
					t.Fatalf("tmux call %d = %v, want %v", i, call, want[i])
				}
			}
		})
	}
}

// TestActiveTargetReadsOnlyTheOptionItsKindNeeds proves the two mirror reads are
// lazy and independent, so a Pane route never queries the window option and a
// Window route never queries the pane option.
func TestActiveTargetReadsOnlyTheOptionItsKindNeeds(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	for _, test := range []struct {
		kind coremetadata.Kind
		want string
	}{
		{kind: coremetadata.KindPane, want: "#{@projmux_pane_uid}"},
		{kind: coremetadata.KindAgent, want: "#{@projmux_pane_uid}"},
		{kind: coremetadata.KindWindow, want: "#{@projmux_window_uid}"},
		{kind: coremetadata.KindProject, want: "#{@projmux_window_uid}"},
	} {
		t.Run(string(test.kind), func(t *testing.T) {
			t.Parallel()
			runner := &activeTargetTmuxRunner{replies: map[string]string{
				"display-message -p -t %46 -F #{@projmux_pane_uid}":   "pan-alpha-codex",
				"display-message -p -t %46 -F #{@projmux_window_uid}": "win-alpha-main",
			}}
			lookup := tmuxActiveTargetLookup(
				func(key string) string { return map[string]string{"TMUX": "sock,1,0", "TMUX_PANE": "%46"}[key] },
				intmetadata.NewMirror(runner))
			if _, _, err := activeTargetRef(lookup, test.kind, store.registry); err != nil {
				t.Fatalf("activeTargetRef(%s) error = %v", test.kind, err)
			}
			if len(runner.calls) != 1 {
				t.Fatalf("%s issued %v, want exactly one option read", test.kind, runner.calls)
			}
			if got := runner.calls[0][len(runner.calls[0])-1]; got != test.want {
				t.Fatalf("%s read %q, want %q", test.kind, got, test.want)
			}
		})
	}
}

// TestActiveTargetResolvesAncestorsThroughRegistryOwnership is the crux of the
// design.
//
// Only two identity options are consulted -- the pane uid and the window uid --
// and every ancestor above them comes from ownerRef. In particular the Project
// is the owner of the active Window rather than the session-scoped
// @projmux_project_uid, which is measurably empty on live sessions, and the
// Agent is the owner of the active Pane rather than a per-Agent option that does
// not exist.
func TestActiveTargetResolvesAncestorsThroughRegistryOwnership(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		kind      coremetadata.Kind
		paneUID   string
		windowUID string
		want      string
	}{
		{name: "pane from the pane uid mirror", kind: coremetadata.KindPane,
			paneUID: "pan-alpha-zsh", windowUID: "win-alpha-main", want: "pan-alpha-zsh"},
		{name: "window from the window uid mirror", kind: coremetadata.KindWindow,
			paneUID: "pan-alpha-zsh", windowUID: "win-alpha-main", want: "win-alpha-main"},
		{name: "project from the active window owner", kind: coremetadata.KindProject,
			paneUID: "pan-alpha-zsh", windowUID: "win-alpha-main", want: "prj-alpha"},
		{name: "agent from the active managed pane owner", kind: coremetadata.KindAgent,
			paneUID: "pan-alpha-codex", windowUID: "win-alpha-main", want: "agt-alpha-codex"},
		{name: "the window mirror alone still resolves a project", kind: coremetadata.KindProject,
			paneUID: "", windowUID: "win-beta-main", want: "prj-beta"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			active := insideTmux(test.paneUID, test.windowUID)
			ref, resolved, err := activeTargetRef(active.lookup, test.kind, store.registry)
			if err != nil {
				t.Fatalf("activeTargetRef error = %v", err)
			}
			if !resolved {
				t.Fatalf("activeTargetRef did not resolve inside tmux")
			}
			if ref.UID != test.want {
				t.Fatalf("uid = %q, want %q", ref.UID, test.want)
			}
			if ref.Kind != test.kind {
				t.Fatalf("kind = %q, want %q", ref.Kind, test.kind)
			}
			// The ref is spelled in its uid form, never as a bare name, so it
			// can never be confused with a name the operator typed.
			if ref.Name != "" || ref.Raw != selector.UIDPrefix+test.want {
				t.Fatalf("ref = %+v, want the uid: spelling", ref)
			}
		})
	}
}

// TestActiveTargetOutsideTheRegistryRefusesWithADistinctMessage is acceptance
// criterion 5.
//
// An active pane that is not a registry Pane is the *common* case, not an edge
// case: every pane created outside the registry-backed routes carries no
// @projmux_pane_uid. Reusing the ordinary "matched N, want exactly one"
// cardinality failure would present that as garden-variety ambiguity, list
// candidates the command will not pick, and hide the real cause. So the refusal
// is its own message, and these strings are the contract.
func TestActiveTargetOutsideTheRegistryRefusesWithADistinctMessage(t *testing.T) {
	t.Parallel()

	const suffix = "; nothing was selected, so pass an explicit resource reference or --selector"
	orphanWindow := coremetadata.Registry{Windows: []coremetadata.Window{{
		Metadata: coremetadata.ObjectMeta{
			UID: "win-orphan", Name: "orphan",
			OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindProject, UID: "prj-ghost"},
		},
	}}}

	for _, test := range []struct {
		name      string
		kind      coremetadata.Kind
		registry  *coremetadata.Registry
		paneUID   string
		windowUID string
		want      string
	}{
		{
			name: "pane with no identity mirror", kind: coremetadata.KindPane,
			want: `resolve pane: no selector was given and the active tmux pane %46 carries no @projmux_pane_uid` + suffix,
		},
		{
			name: "pane mirroring a uid the registry does not hold", kind: coremetadata.KindPane,
			paneUID: "pane-ghost",
			want:    `resolve pane: no selector was given and the active tmux pane %46 mirrors pane uid "pane-ghost", which is not in the registry` + suffix,
		},
		{
			name: "window with no identity mirror", kind: coremetadata.KindWindow,
			want: `resolve window: no selector was given and the active tmux pane %46 carries no @projmux_window_uid` + suffix,
		},
		{
			name: "window mirroring a uid the registry does not hold", kind: coremetadata.KindWindow,
			windowUID: "win-ghost",
			want:      `resolve window: no selector was given and the active tmux pane %46 mirrors window uid "win-ghost", which is not in the registry` + suffix,
		},
		{
			name: "project has no window to inherit from", kind: coremetadata.KindProject,
			want: `resolve project: no selector was given and the active tmux pane %46 carries no @projmux_window_uid` + suffix,
		},
		{
			name: "project whose active window is owned by no registered project", kind: coremetadata.KindProject,
			registry: &orphanWindow, windowUID: "win-orphan",
			want: `resolve project: no selector was given and the active tmux pane %46 resolves to window "orphan", which has no owning Project in the registry` + suffix,
		},
		{
			name: "agent on a shell pane", kind: coremetadata.KindAgent,
			paneUID: "pan-alpha-zsh",
			want:    `resolve agent: no selector was given and the active tmux pane %46 is a shell Pane with no owning Agent` + suffix,
		},
		{
			name: "agent on a pane with no identity mirror", kind: coremetadata.KindAgent,
			want: `resolve agent: no selector was given and the active tmux pane %46 carries no @projmux_pane_uid` + suffix,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			registry := newFakeResourceStore(t).registry
			if test.registry != nil {
				registry = *test.registry
			}
			ref, resolved, err := activeTargetRef(insideTmux(test.paneUID, test.windowUID).lookup, test.kind, registry)
			if err == nil {
				t.Fatalf("refusal case resolved %+v", ref)
			}
			if resolved {
				t.Fatalf("a refusal still reported a resolved target")
			}
			// The routes hand every resolution failure to MapMetadataError, so
			// the refusal must classify as operator input there: exit code 2
			// with zero bytes on stdout.
			if !IsUsageError(MapMetadataError(err)) {
				t.Fatalf("refusal is not a usage error: %v", err)
			}
			if err.Error() != test.want {
				t.Fatalf("error =\n%q\nwant\n%q", err.Error(), test.want)
			}
			// It must not read as ordinary ambiguity, and it must not carry the
			// bounded candidate listing that would invite picking one of them.
			if strings.Contains(err.Error(), "want exactly one") || strings.Contains(err.Error(), "matched") {
				t.Fatalf("refusal reused the cardinality wording: %v", err)
			}
			if strings.Contains(err.Error(), "\n") {
				t.Fatalf("refusal listed candidates: %v", err)
			}
		})
	}
}

// TestEmptySelectorResolvesTheActiveTargetForTheReadAndRenameVerbs is acceptance
// criteria 1 and 2 plus the rest of the adopting routes.
func TestEmptySelectorResolvesTheActiveTargetForTheReadAndRenameVerbs(t *testing.T) {
	t.Parallel()

	t.Run("describe resolves every kind", func(t *testing.T) {
		t.Parallel()
		for _, test := range []struct {
			kind      string
			paneUID   string
			windowUID string
			wantUID   string
		}{
			{kind: "pane", paneUID: "pan-alpha-zsh", windowUID: "win-alpha-main", wantUID: "pan-alpha-zsh"},
			{kind: "window", paneUID: "pan-alpha-zsh", windowUID: "win-alpha-main", wantUID: "win-alpha-main"},
			{kind: "project", paneUID: "pan-alpha-zsh", windowUID: "win-alpha-main", wantUID: "prj-alpha"},
			{kind: "agent", paneUID: "pan-alpha-codex", windowUID: "win-alpha-main", wantUID: "agt-alpha-codex"},
		} {
			t.Run(test.kind, func(t *testing.T) {
				t.Parallel()
				store := newFakeResourceStore(t)
				active := insideTmux(test.paneUID, test.windowUID)
				stdout, stderr, err := runRoute(t, newTestDescribeCommandWithActiveTarget(t, store, active), test.kind)
				if err != nil {
					t.Fatalf("describe %s error = %v", test.kind, err)
				}
				if stderr != "" {
					t.Fatalf("describe %s stderr = %q", test.kind, stderr)
				}
				rows := describeRows(t, stdout)
				if got := rows["UID"]; len(got) != 1 || got[0] != test.wantUID {
					t.Fatalf("describe %s UID = %v, want %q\n%s", test.kind, got, test.wantUID, stdout)
				}
				if active.calls != 1 {
					t.Fatalf("describe %s consulted the active target %d times, want 1", test.kind, active.calls)
				}
			})
		}
	})

	t.Run("rename pane renames the active pane", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		active := insideTmux("pan-alpha-zsh", "win-alpha-main")
		stdout, _, err := runRoute(t, newTestRenameCommandWithActiveTarget(store, active), "pane", "--name", "renamed")
		if err != nil {
			t.Fatalf("rename pane error = %v", err)
		}
		if stdout != "pane/renamed status=live owner=project/alpha window/main\n" {
			t.Fatalf("rename pane stdout = %q", stdout)
		}
		pane, ok := store.registry.Pane("pan-alpha-zsh")
		if !ok || pane.Metadata.Name != "renamed" {
			t.Fatalf("the active pane was not renamed: %+v", pane)
		}
		// Exactly one resource changed: the sibling pane of the same window and
		// the same-named pane of another window are untouched.
		for _, untouched := range []struct{ uid, name string }{
			{"pan-alpha-log", "log"}, {"pan-alpha-review", "zsh"}, {"pan-beta-zsh", "zsh"},
		} {
			other, ok := store.registry.Pane(untouched.uid)
			if !ok || other.Metadata.Name != untouched.name {
				t.Fatalf("pane %s changed to %+v", untouched.uid, other)
			}
		}
		if store.writes != 1 {
			t.Fatalf("writes = %d, want exactly 1", store.writes)
		}
	})

	t.Run("rename window and project resolve their own kind", func(t *testing.T) {
		t.Parallel()
		for _, test := range []struct {
			kind    string
			wantUID string
			read    func(coremetadata.Registry) string
		}{
			{kind: "window", wantUID: "win-alpha-main", read: func(r coremetadata.Registry) string {
				window, _ := r.Window("win-alpha-main")
				return window.Metadata.Name
			}},
			{kind: "project", wantUID: "prj-alpha", read: func(r coremetadata.Registry) string {
				project, _ := r.Project("prj-alpha")
				return project.Metadata.Name
			}},
		} {
			t.Run(test.kind, func(t *testing.T) {
				t.Parallel()
				store := newFakeResourceStore(t)
				active := insideTmux("pan-alpha-zsh", "win-alpha-main")
				if _, _, err := runRoute(t, newTestRenameCommandWithActiveTarget(store, active),
					test.kind, "--name", "renamed"); err != nil {
					t.Fatalf("rename %s error = %v", test.kind, err)
				}
				if got := test.read(store.registry); got != "renamed" {
					t.Fatalf("rename %s left the name %q", test.kind, got)
				}
			})
		}
	})

	t.Run("rebind project rebinds the active project", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		store.dirs["/srv/moved"] = true
		active := insideTmux("pan-alpha-zsh", "win-alpha-main")
		stdout, _, err := runRoute(t, newTestRebindCommandWithActiveTarget(store, active),
			"project", "--root", "/srv/moved")
		if err != nil {
			t.Fatalf("rebind project error = %v", err)
		}
		if stdout != "project/alpha root=/srv/moved\n" {
			t.Fatalf("rebind project stdout = %q", stdout)
		}
	})

	t.Run("get pane reads the active pane resource", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		current := &stubCurrentPath{}
		active := insideTmux("pan-alpha-codex", "win-alpha-main")
		cmd := newTestPaneGetCommandWithActiveTarget(t, store, active, current)
		stdout, _, err := runRoute(t, cmd, "pane", "-o", "uid")
		if err != nil {
			t.Fatalf("get pane error = %v", err)
		}
		if stdout != "pan-alpha-codex\n" {
			t.Fatalf("get pane stdout = %q", stdout)
		}
		// The registry read is the source here, so the live cwd query the
		// `--current` route owns is never issued.
		if current.calls != 0 {
			t.Fatalf("get pane issued %d current-path queries, want 0", current.calls)
		}
	})
}

// TestAnUnmappedActiveTargetRefusesAtTheRoute is acceptance criterion 5 end to
// end: the refusal reaches the operator with exit-code-2 semantics, zero bytes
// on stdout, no other resource selected, and nothing written.
func TestAnUnmappedActiveTargetRefusesAtTheRoute(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		run  func(t *testing.T, store *fakeResourceStore, active *recordedActiveTarget) (string, error)
	}{
		{
			name: "describe pane",
			run: func(t *testing.T, store *fakeResourceStore, active *recordedActiveTarget) (string, error) {
				stdout, _, err := runRoute(t, newTestDescribeCommandWithActiveTarget(t, store, active), "pane")
				return stdout, err
			},
		},
		{
			name: "rename pane",
			run: func(t *testing.T, store *fakeResourceStore, active *recordedActiveTarget) (string, error) {
				stdout, _, err := runRoute(t, newTestRenameCommandWithActiveTarget(store, active), "pane", "--name", "x")
				return stdout, err
			},
		},
		{
			name: "get pane",
			run: func(t *testing.T, store *fakeResourceStore, active *recordedActiveTarget) (string, error) {
				cmd := newTestPaneGetCommandWithActiveTarget(t, store, active, &stubCurrentPath{})
				stdout, _, err := runRoute(t, cmd, "pane")
				return stdout, err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			before := store.snapshot()
			// An unmanaged pane: inside tmux, no identity mirror at all. This is
			// the common shape, not a contrived one.
			active := insideTmux("", "")
			stdout, err := test.run(t, store, active)
			if err == nil {
				t.Fatalf("%s selected something for an unmanaged pane: %s", test.name, stdout)
			}
			if !IsUsageError(err) {
				t.Fatalf("%s error is not a usage error: %v", test.name, err)
			}
			want := "resolve pane: no selector was given and the active tmux pane %46 carries no @projmux_pane_uid" +
				"; nothing was selected, so pass an explicit resource reference or --selector"
			if err.Error() != want {
				t.Fatalf("%s error =\n%q\nwant\n%q", test.name, err.Error(), want)
			}
			if stdout != "" {
				t.Fatalf("%s wrote %q to stdout", test.name, stdout)
			}
			if store.transactions != 0 || store.writes != 0 {
				t.Fatalf("%s opened %d transactions and committed %d writes", test.name, store.transactions, store.writes)
			}
			if store.snapshot() != before {
				t.Fatalf("%s changed the registry", test.name)
			}
		})
	}
}

// TestAnExplicitSelectorSuppressesTheActiveTargetFallback is acceptance
// criterion 3: given any reference or selector, behavior is identical to today.
//
// The assertion is not only on the outcome but on the call count: a route that
// happened to produce the same answer while still consulting tmux would be
// blending an implicit target into an explicit selector, which is exactly what
// the contract forbids.
func TestAnExplicitSelectorSuppressesTheActiveTargetFallback(t *testing.T) {
	t.Parallel()

	// The fake would resolve a completely different resource than any of these
	// invocations address, so a leak would be visible in the output too.
	for _, test := range []struct {
		name     string
		args     []string
		wantUID  string
		wantFail string
	}{
		{name: "positional ref", args: []string{"pane", "log", "--project", "alpha", "--window", "main"}, wantUID: "pan-alpha-log"},
		{name: "uid ref", args: []string{"window", "uid:win-alpha-review"}, wantUID: "win-alpha-review"},
		{name: "project scope only", args: []string{"pane", "--project", "alpha"}, wantFail: "want exactly one"},
		{name: "window scope only", args: []string{"pane", "--window", "main"}, wantFail: "want exactly one"},
		{name: "label selector only", args: []string{"pane", "--selector", "role=shell"}, wantFail: "want exactly one"},
		{name: "an unmatched explicit ref stays a no-match", args: []string{"pane", "nosuch"}, wantFail: "matched no panes"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			active := insideTmux("pan-alpha-codex", "win-alpha-main")
			stdout, _, err := runRoute(t, newTestDescribeCommandWithActiveTarget(t, store, active), test.args...)
			if active.calls != 0 {
				t.Fatalf("describe %v consulted the active target %d times, want 0", test.args, active.calls)
			}
			if test.wantFail != "" {
				if err == nil {
					t.Fatalf("describe %v succeeded: %s", test.args, stdout)
				}
				if !IsUsageError(err) || !strings.Contains(err.Error(), test.wantFail) {
					t.Fatalf("describe %v error = %v, want it to mention %q", test.args, err, test.wantFail)
				}
				if stdout != "" {
					t.Fatalf("describe %v wrote %q to stdout", test.args, stdout)
				}
				return
			}
			if err != nil {
				t.Fatalf("describe %v error = %v", test.args, err)
			}
			if got := describeRows(t, stdout)["UID"]; len(got) != 1 || got[0] != test.wantUID {
				t.Fatalf("describe %v UID = %v, want %q", test.args, got, test.wantUID)
			}
		})
	}

	// The mutation routes hold the same line.
	store := newFakeResourceStore(t)
	active := insideTmux("pan-alpha-zsh", "win-alpha-main")
	if _, _, err := runRoute(t, newTestRenameCommandWithActiveTarget(store, active),
		"pane", "log", "--project", "alpha", "--window", "main", "--name", "renamed"); err != nil {
		t.Fatalf("rename pane with an explicit ref error = %v", err)
	}
	if active.calls != 0 {
		t.Fatalf("rename consulted the active target %d times, want 0", active.calls)
	}
	pane, _ := store.registry.Pane("pan-alpha-log")
	if pane.Metadata.Name != "renamed" {
		t.Fatalf("rename followed the active target instead of the explicit ref")
	}
}

// TestOutsideTmuxTheEmptySelectorKeepsThePreFallbackFailure is acceptance
// criterion 4.
func TestOutsideTmuxTheEmptySelectorKeepsThePreFallbackFailure(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		run  func(t *testing.T, store *fakeResourceStore, active *recordedActiveTarget) (string, error)
		want string
	}{
		{
			name: "describe pane",
			run: func(t *testing.T, store *fakeResourceStore, active *recordedActiveTarget) (string, error) {
				stdout, _, err := runRoute(t, newTestDescribeCommandWithActiveTarget(t, store, active), "pane")
				return stdout, err
			},
			want: "resolve pane: the current selector matched 5 panes, want exactly one",
		},
		{
			name: "describe window",
			run: func(t *testing.T, store *fakeResourceStore, active *recordedActiveTarget) (string, error) {
				stdout, _, err := runRoute(t, newTestDescribeCommandWithActiveTarget(t, store, active), "window")
				return stdout, err
			},
			want: "resolve window: the current selector matched 3 windows, want exactly one",
		},
		{
			name: "describe project",
			run: func(t *testing.T, store *fakeResourceStore, active *recordedActiveTarget) (string, error) {
				stdout, _, err := runRoute(t, newTestDescribeCommandWithActiveTarget(t, store, active), "project")
				return stdout, err
			},
			want: "resolve project: the current selector matched 3 projects, want exactly one",
		},
		{
			name: "describe agent",
			run: func(t *testing.T, store *fakeResourceStore, active *recordedActiveTarget) (string, error) {
				stdout, _, err := runRoute(t, newTestDescribeCommandWithActiveTarget(t, store, active), "agent")
				return stdout, err
			},
			want: "resolve agent: the current selector matched 2 agents, want exactly one",
		},
		{
			name: "rename pane",
			run: func(t *testing.T, store *fakeResourceStore, active *recordedActiveTarget) (string, error) {
				stdout, _, err := runRoute(t, newTestRenameCommandWithActiveTarget(store, active), "pane", "--name", "x")
				return stdout, err
			},
			want: "resolve pane: the current selector matched 5 panes, want exactly one",
		},
		{
			name: "rebind project",
			run: func(t *testing.T, store *fakeResourceStore, active *recordedActiveTarget) (string, error) {
				stdout, _, err := runRoute(t, newTestRebindCommandWithActiveTarget(store, active), "project", "--root", "/srv/alpha")
				return stdout, err
			},
			want: "resolve project: the current selector matched 3 projects, want exactly one",
		},
		{
			name: "get pane",
			run: func(t *testing.T, store *fakeResourceStore, active *recordedActiveTarget) (string, error) {
				cmd := newTestPaneGetCommandWithActiveTarget(t, store, active, &stubCurrentPath{})
				stdout, _, err := runRoute(t, cmd, "pane")
				return stdout, err
			},
			want: "resolve pane: the current selector matched 5 panes, want exactly one",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			before := store.snapshot()
			active := outsideTmux()
			stdout, err := test.run(t, store, active)
			if err == nil {
				t.Fatalf("%s succeeded outside tmux", test.name)
			}
			if !IsUsageError(err) {
				t.Fatalf("%s error is not a usage error: %v", test.name, err)
			}
			if stdout != "" {
				t.Fatalf("%s wrote %q to stdout", test.name, stdout)
			}
			// The summary line is byte-identical to the pre-fallback message;
			// the candidate listing follows it on its own lines.
			summary, _, _ := strings.Cut(err.Error(), "\n")
			if summary != test.want {
				t.Fatalf("%s summary = %q, want %q", test.name, summary, test.want)
			}
			if store.writes != 0 || store.transactions != 0 {
				t.Fatalf("%s opened %d transactions and committed %d writes", test.name, store.transactions, store.writes)
			}
			if store.snapshot() != before {
				t.Fatalf("%s changed the registry", test.name)
			}
		})
	}
}

// TestGetPaneCurrentIsUnchangedByTheActiveTargetFallback is the D2 finding: the
// `--current` read and the empty-selector fallback are not duplicates, so
// nothing is removed and no compatibility window exists.
//
// `--current` never reads the registry: it reports the live tmux
// `#{pane_current_path}` as a bare scalar and accepts only `-o cwd`. The
// fallback resolves a registry Pane resource and renders the resource
// projection. This test pins both halves against the same command instance.
func TestGetPaneCurrentIsUnchangedByTheActiveTargetFallback(t *testing.T) {
	t.Parallel()

	newCmd := func(t *testing.T, current *stubCurrentPath, active *recordedActiveTarget) *getCommand {
		t.Helper()
		return newTestPaneGetCommandWithActiveTarget(t, newFakeResourceStore(t), active, current)
	}

	t.Run("cwd scalar is byte-identical and registry free", func(t *testing.T) {
		t.Parallel()
		current := &stubCurrentPath{path: "/srv/alpha/worktree\n"}
		active := insideTmux("pan-alpha-zsh", "win-alpha-main")
		stdout, stderr, err := runRoute(t, newCmd(t, current, active), "pane", "--current", "-o", "cwd")
		if err != nil || stdout != "/srv/alpha/worktree\n" || stderr != "" {
			t.Fatalf("stdout=%q stderr=%q err=%v", stdout, stderr, err)
		}
		if current.calls != 1 {
			t.Fatalf("current-path queries = %d, want 1", current.calls)
		}
		if active.calls != 0 {
			t.Fatalf("--current consulted the resource fallback %d times, want 0", active.calls)
		}
	})

	t.Run("the usage errors are unchanged", func(t *testing.T) {
		t.Parallel()
		for _, test := range []struct {
			args []string
			want string
		}{
			{
				args: []string{"pane", "--current"},
				want: "get pane --current supports -o cwd only; the live Pane resource projection arrives with runtime materialization",
			},
			{
				args: []string{"pane", "--current", "-o", "uid"},
				want: "get pane --current supports -o cwd only; the live Pane resource projection arrives with runtime materialization",
			},
			{
				args: []string{"pane", "--current", "--project", "alpha"},
				want: "get pane --current reads the active tmux pane and does not accept selectors",
			},
			{
				args: []string{"pane", "--current", "--selector", "role=shell"},
				want: "get pane --current reads the active tmux pane and does not accept selectors",
			},
		} {
			t.Run(strings.Join(test.args, " "), func(t *testing.T) {
				t.Parallel()
				current := &stubCurrentPath{path: "/srv/alpha"}
				active := insideTmux("pan-alpha-zsh", "win-alpha-main")
				stdout, _, err := runRoute(t, newCmd(t, current, active), test.args...)
				if err == nil || err.Error() != test.want {
					t.Fatalf("error = %v, want %q", err, test.want)
				}
				if !IsUsageError(err) {
					t.Fatalf("error is not a usage error: %v", err)
				}
				if stdout != "" || current.calls != 0 || active.calls != 0 {
					t.Fatalf("stdout=%q currentCalls=%d activeCalls=%d", stdout, current.calls, active.calls)
				}
			})
		}
	})
}

// TestResourceSummaryStillCarriesTheAgentSessionSuffix guards the rendering the
// just-merged provider-session-ref change added, which shares
// resource_routes.go with this seam.
//
// The seam touches selector resolution only, so the kind/registry parameters
// resourceSummary gained and the `session=` suffix it appends for an Agent must
// come through untouched.
func TestResourceSummaryStillCarriesTheAgentSessionSuffix(t *testing.T) {
	t.Parallel()

	observedAt := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)
	ref, ok := coremetadata.NewAgentSessionRef(coremetadata.AgentSessionObservation{
		Provider: "codex", ThreadID: "thr-1", SessionID: "ses-1",
	}, observedAt)
	if !ok {
		t.Fatalf("the fixture observation built no session ref")
	}
	store := newFakeResourceStore(t)
	agent, found := store.registry.Agent("agt-alpha-codex")
	if !found {
		t.Fatalf("the fixture lost its Agent")
	}
	agent.Status.SessionRef = ref

	active := insideTmux("pan-alpha-codex", "win-alpha-main")
	stdout, _, err := runRoute(t, newTestDescribeCommandWithActiveTarget(t, store, active), "agent", "-o", "ref")
	if err != nil {
		t.Fatalf("describe agent -o ref error = %v", err)
	}
	if stdout != "agent/codex\n" {
		t.Fatalf("describe agent -o ref = %q", stdout)
	}

	// The default projection is the one that carries the suffix.
	match := selector.Match{Kind: coremetadata.KindAgent, UID: "agt-alpha-codex", Name: "codex", Status: selector.StatusLive}
	summary := resourceSummary(match, coremetadata.KindAgent, store.registry)
	if !strings.Contains(summary, "session=codex:thr-1") {
		t.Fatalf("agent summary = %q, want the session suffix", summary)
	}
	// A non-Agent kind never grows the suffix, and an Agent without a ref keeps
	// the historical line.
	plain := resourceSummary(selector.Match{Kind: coremetadata.KindAgent, UID: "agt-beta-codex", Name: "codex"},
		coremetadata.KindAgent, store.registry)
	if strings.Contains(plain, "session=") {
		t.Fatalf("an Agent with no ref grew a suffix: %q", plain)
	}
	pane := resourceSummary(selector.Match{Kind: coremetadata.KindPane, UID: "pan-alpha-zsh", Name: "zsh"},
		coremetadata.KindPane, store.registry)
	if strings.Contains(pane, "session=") {
		t.Fatalf("a Pane grew a session suffix: %q", pane)
	}
}
