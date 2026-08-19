package app

import (
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// The parity audit of the control-session slice.
//
// Every case here answers one question of the form "did the new root kind leak
// somewhere it must never appear". The Project graph is measured by comparing the
// same read against two registries that differ only by the control session, which
// is stronger than asserting an expected string: a change in the Project read
// fails whether or not this file was updated to match it.

// controlParityStore returns two stores over the same Project fixture, the second
// carrying an app-owned control session with one Window and one Pane.
func controlParityStore(t *testing.T) (*fakeResourceStore, *fakeResourceStore, coremetadata.ControlSessionBinding) {
	t.Helper()
	without := newFakeResourceStore(t)
	with := newFakeResourceStore(t)
	binding, err := with.mutator().BindControlSession(&with.registry, coremetadata.ControlSessionObservation{
		Session: "home",
		Windows: []coremetadata.ControlSessionWindow{{
			DisplayName: "zsh",
			Panes:       []coremetadata.ControlSessionPane{{Command: "zsh"}},
		}},
	}, "/bin/zsh", "op-control", nil)
	if err != nil {
		t.Fatalf("BindControlSession() error = %v", err)
	}
	if err := with.registry.Validate(); err != nil {
		t.Fatalf("control parity fixture is not a valid registry: %v", err)
	}
	return without, with, binding
}

// TestControlSessionIsAbsentFromProjectReads is acceptance criterion 6's read
// half: $HOME never shows up as a Project, and the Project read is unchanged.
func TestControlSessionIsAbsentFromProjectReads(t *testing.T) {
	t.Parallel()

	without, with, binding := controlParityStore(t)
	for _, args := range [][]string{
		{"projects", "-o", "uid"},
		{"projects", "-o", "name"},
		{"projects"},
	} {
		base, _, err := runRoute(t, newTestListGetCommand(t, without), args...)
		if err != nil {
			t.Fatalf("get %v without a control session: %v", args, err)
		}
		got, _, err := runRoute(t, newTestListGetCommand(t, with), args...)
		if err != nil {
			t.Fatalf("get %v with a control session: %v", args, err)
		}
		if got != base {
			t.Fatalf("get %v changed when a control session existed:\nwithout = %q\nwith    = %q", args, base, got)
		}
		if strings.Contains(got, "home") || strings.Contains(got, binding.ControlSession.Metadata.UID) {
			t.Fatalf("get %v mentions the control session: %q", args, got)
		}
	}
}

// TestControlSessionParticipatesInNoPathResolution is acceptance criterion 6's
// structural half.
//
// Root lookup, name lookup, and the Project accessor are the three doors every
// path-based surface -- managed roots, trust, rebind, cwd defaults -- walks
// through to turn a path into a Project. A control session must answer none of
// them, and it cannot: it holds no path field at all.
func TestControlSessionParticipatesInNoPathResolution(t *testing.T) {
	t.Parallel()

	_, with, binding := controlParityStore(t)
	registry := with.registry
	controlUID := binding.ControlSession.Metadata.UID

	for _, path := range []string{"/home/operator", "/home", "home", "/srv", "/", ""} {
		if project, ok := registry.ProjectByRoot(path); ok {
			t.Fatalf("ProjectByRoot(%q) resolved %q; no control session may answer a path lookup", path, project.Metadata.UID)
		}
	}
	if project, ok := registry.ProjectByName("home"); ok {
		t.Fatalf("ProjectByName(%q) resolved %q; the control session name is not a Project name", "home", project.Metadata.UID)
	}
	if project, ok := registry.Project(controlUID); ok {
		t.Fatalf("Project(%q) resolved %q; a control session uid is never a Project uid", controlUID, project.Metadata.UID)
	}
	if _, ok := registry.ControlSession(controlUID); !ok {
		t.Fatalf("ControlSession(%q) did not resolve its own uid", controlUID)
	}
	// The control session's Windows are reachable through the generic owner
	// accessor, which is what makes the owner chain derivable at all.
	if got, want := len(registry.WindowsOf(controlUID)), 1; got != want {
		t.Fatalf("WindowsOf(%q) = %d windows, want %d", controlUID, got, want)
	}
}

// TestControlSessionWindowsAndPanesAreQueryable is acceptance criterion 2's
// Registry half: the Home Window and Pane are ordinary Registry resources that
// the global plural reads list.
func TestControlSessionWindowsAndPanesAreQueryable(t *testing.T) {
	t.Parallel()

	_, with, binding := controlParityStore(t)
	windowUID := binding.Windows[0].UID
	paneUID := binding.Panes[0].UID

	stdout, _, err := runRoute(t, newTestListGetCommand(t, with), "windows", "-A", "-o", "uid")
	if err != nil {
		t.Fatalf("get windows -A: %v", err)
	}
	if !strings.Contains(stdout, windowUID) {
		t.Fatalf("get windows -A = %q, want it to list %q", stdout, windowUID)
	}
	stdout, _, err = runRoute(t, newTestListGetCommand(t, with), "panes", "-A", "-o", "uid")
	if err != nil {
		t.Fatalf("get panes -A: %v", err)
	}
	if !strings.Contains(stdout, paneUID) {
		t.Fatalf("get panes -A = %q, want it to list %q", stdout, paneUID)
	}
}

// TestDescribePaneResolvesInsideTheControlSession is acceptance criterion 3.
//
// Before this slice pane %0 of Home carried no @projmux_pane_uid, so the
// no-selector describe refused for lack of any mirrored identity. It now resolves
// through exactly the same seam a Project's pane does.
func TestDescribePaneResolvesInsideTheControlSession(t *testing.T) {
	t.Parallel()

	_, with, binding := controlParityStore(t)
	paneUID := binding.Panes[0].UID
	windowUID := binding.Windows[0].UID

	inside := insideTmux(paneUID, windowUID)
	stdout, stderr, err := runRoute(t, newTestDescribeCommandWithActiveTarget(t, with, inside), "pane")
	if err != nil {
		t.Fatalf("describe pane inside the control session: %v\n%s", err, stderr)
	}
	if !strings.Contains(stdout, paneUID) {
		t.Fatalf("describe pane = %q, want it to name %q", stdout, paneUID)
	}

	// The same seam still refuses a pane that carries no mirrored identity, which
	// is what an unmarked Home looked like before this slice.
	unmarked := insideTmux("", "")
	if _, _, err := runRoute(t, newTestDescribeCommandWithActiveTarget(t, with, unmarked), "pane"); err == nil {
		t.Fatal("describe pane resolved from a pane carrying no mirrored identity")
	}
}

// TestControlSessionKeepsTheCreateProjectRefusal is acceptance criterion 8: the
// next-phase-encroachment guard.
//
// `create`/split inside Home is Phase 1's work and must still refuse here. What
// changed is the *detail clause* of the refusal, not the refusal, its exit code,
// or its zero-write property: before this slice Home's pane carried no
// @projmux_window_uid at all, so the derivation stopped at "carries no
// @projmux_window_uid"; now the Window resolves and the derivation stops one step
// later, at "has no owning Project in the registry". Both texts are pinned below
// so the difference is a documented, tested fact rather than a surprise.
func TestControlSessionKeepsTheCreateProjectRefusal(t *testing.T) {
	t.Parallel()

	_, with, binding := controlParityStore(t)
	registry := with.registry

	// Pre-Phase-0 shape: nothing mirrored on the live pane.
	unmarked := insideTmux("", "")
	observer, inside := unmarked.lookup()
	if !inside {
		t.Fatal("the unmarked fixture reported itself outside tmux")
	}
	projectUID, detail := observer.uidFor(coremetadata.KindProject, registry)
	if projectUID != "" {
		t.Fatalf("an unmarked pane derived project %q", projectUID)
	}
	const wantBefore = "create pane requires a Project: no --project <ref> was given and " +
		"the active tmux pane %46 carries no " + tmuxopts.WindowUID +
		"; nothing was created, so pass --project <ref>"
	if got := requireExplicitProject("create pane", detail).Error(); got != wantBefore {
		t.Fatalf("pre-Phase-0 refusal = %q, want %q", got, wantBefore)
	}

	// Post-Phase-0 shape: the Window resolves, and its owner is a control session.
	marked := insideTmux(binding.Panes[0].UID, binding.Windows[0].UID)
	observer, inside = marked.lookup()
	if !inside {
		t.Fatal("the marked fixture reported itself outside tmux")
	}
	projectUID, detail = observer.uidFor(coremetadata.KindProject, registry)
	if projectUID != "" {
		t.Fatalf("a control-session pane derived project %q; Home owns no Project", projectUID)
	}
	window, ok := registry.Window(binding.Windows[0].UID)
	if !ok {
		t.Fatalf("control window %q is missing", binding.Windows[0].UID)
	}
	wantAfter := "create pane requires a Project: no --project <ref> was given and " +
		"the active tmux pane %46 resolves to window \"" + window.Metadata.Name +
		"\", which has no owning Project in the registry; nothing was created, so pass --project <ref>"
	refusal := requireExplicitProject("create pane", detail)
	if got := refusal.Error(); got != wantAfter {
		t.Fatalf("post-Phase-0 refusal = %q, want %q", got, wantAfter)
	}
	// Still a usage error, so still exit code 2 with zero bytes on stdout.
	if !IsUsageError(refusal) {
		t.Fatalf("the refusal is no longer a usage error: %v", refusal)
	}
}
