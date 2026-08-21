package app

import (
	"strings"
	"testing"
)

// The fixture registry is already the operator's reproduction in miniature:
// Window `main` exists in both alpha and beta, Agent `codex` exists in both,
// and Pane `zsh` exists twice inside alpha as well as once inside beta. So a
// cross-Project match, an intra-Project ambiguity, and a clean exact-one all
// come out of the same registry without a bespoke fixture.

// TestSingularReferenceResolvesInsideTheActiveProject is acceptance criteria 1
// and 2: the applied <verb, kind> matrix.
//
// Every row names a resource whose name also exists in another Project, so a
// pass means the reference was resolved inside the active Project rather than
// against the whole registry. The active lookup is counted because "the
// namespace was applied" is otherwise inferred from the output rather than
// measured.
func TestSingularReferenceResolvesInsideTheActiveProject(t *testing.T) {
	t.Parallel()

	t.Run("describe", func(t *testing.T) {
		t.Parallel()
		for _, test := range []struct {
			name       string
			args       []string
			wantUID    string
			globalWant string
		}{
			{
				name: "window", args: []string{"window", "main", "-o", "uid"},
				wantUID: "win-alpha-main", globalWant: "matched 2 windows",
			},
			{
				name: "pane", args: []string{"pane", "zsh", "-w", "main", "-o", "uid"},
				wantUID: "pan-alpha-zsh", globalWant: "matched 2 panes",
			},
			{
				name: "agent", args: []string{"agent", "codex", "-o", "uid"},
				wantUID: "agt-alpha-codex", globalWant: "matched 2 agents",
			},
		} {
			t.Run(test.name, func(t *testing.T) {
				t.Parallel()

				store := newFakeResourceStore(t)
				inside := insideTmux("pan-alpha-zsh", "win-alpha-main")
				stdout, stderr, err := runRoute(t, newTestDescribeCommandWithActiveTarget(t, store, inside), test.args...)
				if err != nil {
					t.Fatalf("describe %s inside tmux: %v", test.name, err)
				}
				if stdout != test.wantUID+"\n" || stderr != "" {
					t.Fatalf("describe %s inside = stdout %q stderr %q, want %q", test.name, stdout, stderr, test.wantUID)
				}
				if inside.calls != 1 {
					t.Fatalf("describe %s consulted the active target %d times, want 1", test.name, inside.calls)
				}

				// The same argv outside tmux keeps the pre-namespace
				// whole-registry ambiguity, which is what makes the row above a
				// scoping result rather than a fixture accident.
				outside := outsideTmux()
				stdout, _, err = runRoute(t, newTestDescribeCommandWithActiveTarget(t, store, outside), test.args...)
				if err == nil || !IsUsageError(err) || stdout != "" {
					t.Fatalf("describe %s outside tmux = stdout %q err %v, want a usage refusal", test.name, stdout, err)
				}
				if !strings.Contains(err.Error(), test.globalWant) {
					t.Fatalf("describe %s outside tmux error = %v, want %q", test.name, err, test.globalWant)
				}
			})
		}
	})

	t.Run("rename window", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		inside := insideTmux("pan-alpha-zsh", "win-alpha-main")
		if _, _, err := runRoute(t, newTestRenameCommandWithActiveTarget(store, inside),
			"window", "main", "--name", "primary"); err != nil {
			t.Fatalf("rename window main: %v", err)
		}
		assertName(t, "win-alpha-main", windowName(t, store, "win-alpha-main"), "primary")
		assertName(t, "win-beta-main", windowName(t, store, "win-beta-main"), "main")
		if store.writes != 1 {
			t.Fatalf("rename window writes = %d, want exactly 1", store.writes)
		}
	})

	t.Run("rename pane", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		inside := insideTmux("pan-alpha-zsh", "win-alpha-main")
		if _, _, err := runRoute(t, newTestRenameCommandWithActiveTarget(store, inside),
			"pane", "zsh", "-w", "main", "--name", "shell"); err != nil {
			t.Fatalf("rename pane zsh -w main: %v", err)
		}
		assertName(t, "pan-alpha-zsh", paneName(t, store, "pan-alpha-zsh"), "shell")
		assertName(t, "pan-beta-zsh", paneName(t, store, "pan-beta-zsh"), "zsh")
		if store.writes != 1 {
			t.Fatalf("rename pane writes = %d, want exactly 1", store.writes)
		}
	})
}

// TestSingularProjectNamespacePrecedence is acceptance criterion 4 plus the
// negative half of the applied matrix.
//
// Explicit `--project` and the routes that stay global are pinned by the call
// count, not by the result: a route that resolved the right resource while
// still observing tmux would have paid for a scope it does not use, and would
// refuse inside an unmanaged pane for no reason.
func TestSingularProjectNamespacePrecedence(t *testing.T) {
	t.Parallel()

	t.Run("explicit project wins with zero observations", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		inside := insideTmux("pan-alpha-zsh", "win-alpha-main")
		stdout, _, err := runRoute(t, newTestDescribeCommandWithActiveTarget(t, store, inside),
			"window", "main", "-p", "beta", "-o", "uid")
		if err != nil {
			t.Fatalf("describe window main -p beta: %v", err)
		}
		if stdout != "win-beta-main\n" {
			t.Fatalf("explicit project stdout = %q, want win-beta-main", stdout)
		}
		if inside.calls != 0 {
			t.Fatalf("explicit project consulted the active target %d times, want 0", inside.calls)
		}
	})

	// Project resources have no enclosing managed root. These routes must reach
	// the resolver with no default at all, so neither may consult tmux.
	for _, test := range []struct {
		name string
		run  func(*testing.T, *fakeResourceStore, *recordedActiveTarget) (string, error)
		want string
	}{
		{
			name: "describe project keeps the whole registry",
			run: func(t *testing.T, store *fakeResourceStore, active *recordedActiveTarget) (string, error) {
				stdout, _, err := runRoute(t, newTestDescribeCommandWithActiveTarget(t, store, active), "project", "alpha", "-o", "uid")
				return stdout, err
			},
			want: "prj-alpha\n",
		},
		{
			name: "rename project keeps the whole registry",
			run: func(t *testing.T, store *fakeResourceStore, active *recordedActiveTarget) (string, error) {
				stdout, _, err := runRoute(t, newTestRenameCommandWithActiveTarget(store, active), "project", "alpha", "--name", "renamed")
				return stdout, err
			},
			want: "project/renamed status=live\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			inside := insideTmux("pan-alpha-zsh", "win-alpha-main")
			stdout, err := test.run(t, store, inside)
			if err != nil || stdout != test.want {
				t.Fatalf("%s = stdout %q err %v, want %q", test.name, stdout, err, test.want)
			}
			if inside.calls != 0 {
				t.Fatalf("%s consulted the active target %d times, want 0", test.name, inside.calls)
			}
		})
	}
}

// TestSingularProjectNamespaceNarrowsWithoutSelecting is acceptance criterion 3
// and the label half of the matrix.
//
// The Project is a namespace, not a target rule: inside one Project two
// same-named resources stay the ordinary bounded exact-one ambiguity, and the
// active Window is not quietly used to break the tie.
func TestSingularProjectNamespaceNarrowsWithoutSelecting(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		args       []string
		wantInside string
		wantGlobal string
	}{
		{
			name: "positional name", args: []string{"pane", "zsh"},
			wantInside: "matched 2 panes", wantGlobal: "matched 3 panes",
		},
		{
			name: "label selector", args: []string{"pane", "--selector", "role=shell"},
			wantInside: "matched 2 panes", wantGlobal: "matched 3 panes",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)

			inside := insideTmux("pan-alpha-zsh", "win-alpha-main")
			stdout, _, err := runRoute(t, newTestDescribeCommandWithActiveTarget(t, store, inside), test.args...)
			if err == nil || !IsUsageError(err) || stdout != "" {
				t.Fatalf("%s inside = stdout %q err %v, want a bounded ambiguity", test.name, stdout, err)
			}
			if !strings.Contains(err.Error(), test.wantInside) {
				t.Fatalf("%s inside error = %v, want %q", test.name, err, test.wantInside)
			}
			// The beta candidate is the one the namespace removed; naming it
			// here is what separates "narrowed" from "happened to be two".
			if strings.Contains(err.Error(), "pan-beta-zsh") {
				t.Fatalf("%s inside listed a beta candidate: %v", test.name, err)
			}

			outside := outsideTmux()
			_, _, err = runRoute(t, newTestDescribeCommandWithActiveTarget(t, store, outside), test.args...)
			if err == nil || !strings.Contains(err.Error(), test.wantGlobal) {
				t.Fatalf("%s outside error = %v, want %q", test.name, err, test.wantGlobal)
			}
		})
	}

	t.Run("an out-of-scope uid is a no-match, not a cross-project hit", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		inside := insideTmux("pan-alpha-zsh", "win-alpha-main")
		stdout, _, err := runRoute(t, newTestDescribeCommandWithActiveTarget(t, store, inside),
			"window", "uid:win-beta-main", "-o", "uid")
		if err == nil || !IsUsageError(err) || stdout != "" {
			t.Fatalf("out-of-scope uid = stdout %q err %v, want a usage refusal", stdout, err)
		}
		if !strings.Contains(err.Error(), "matched no windows") {
			t.Fatalf("out-of-scope uid error = %v, want a no-match", err)
		}
	})
}

// TestSingularProjectNamespaceRefusesABrokenOwnerChain is acceptance criterion 5.
//
// Inside tmux a namespace that cannot be derived is a refusal, never a silent
// widening back to the whole registry -- a global fallback here would produce
// exactly the cross-Project match this seam exists to prevent. The message is
// its own, because activeTargetError's "no selector was given" opening would be
// false on an invocation that carried a reference.
func TestSingularProjectNamespaceRefusesABrokenOwnerChain(t *testing.T) {
	t.Parallel()

	const suffix = "; the active Project namespace is undecidable, so nothing was selected -- pass --project <ref> to name the scope explicitly"

	for _, test := range []struct {
		name      string
		windowUID string
		orphan    bool
		want      string
	}{
		{
			name: "no window identity mirror",
			want: `resolve project scope: a resource reference was given inside tmux and the active tmux pane %46 carries no @projmux_window_uid` + suffix,
		},
		{
			name: "window uid the registry does not hold", windowUID: "win-ghost",
			want: `resolve project scope: a resource reference was given inside tmux and the active tmux pane %46 mirrors window uid "win-ghost", which is not in the registry` + suffix,
		},
		{
			name: "active window owned by no registered project", windowUID: "win-alpha-main", orphan: true,
			want: `resolve project scope: a resource reference was given inside tmux and the active tmux pane %46 resolves to window "main", which has no owning Project in the registry` + suffix,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			if test.orphan {
				window, ok := store.registry.Window("win-alpha-main")
				if !ok {
					t.Fatal("fixture is missing win-alpha-main")
				}
				window.Metadata.OwnerRef = nil
			}
			before := store.snapshot()

			inside := insideTmux("pan-alpha-zsh", test.windowUID)
			stdout, _, err := runRoute(t, newTestDescribeCommandWithActiveTarget(t, store, inside),
				"window", "main", "-o", "uid")
			if err == nil || !IsUsageError(err) || stdout != "" {
				t.Fatalf("%s = stdout %q err %v, want a usage refusal", test.name, stdout, err)
			}
			if err.Error() != test.want {
				t.Fatalf("%s error =\n%q\nwant\n%q", test.name, err.Error(), test.want)
			}

			// The mutation half of the same refusal: zero writes, and the
			// registry is byte-identical to what it was.
			inside = insideTmux("pan-alpha-zsh", test.windowUID)
			stdout, _, err = runRoute(t, newTestRenameCommandWithActiveTarget(store, inside),
				"window", "main", "--name", "primary")
			if err == nil || !IsUsageError(err) || stdout != "" {
				t.Fatalf("%s rename = stdout %q err %v, want a usage refusal", test.name, stdout, err)
			}
			if store.writes != 0 || store.snapshot() != before {
				t.Fatalf("%s mutated the registry: writes=%d", test.name, store.writes)
			}
		})
	}
}

func windowName(t *testing.T, store *fakeResourceStore, uid string) string {
	t.Helper()
	window, ok := store.registry.Window(uid)
	if !ok {
		t.Fatalf("registry is missing window %s", uid)
	}
	return window.Metadata.Name
}

func paneName(t *testing.T, store *fakeResourceStore, uid string) string {
	t.Helper()
	pane, ok := store.registry.Pane(uid)
	if !ok {
		t.Fatalf("registry is missing pane %s", uid)
	}
	return pane.Metadata.Name
}

func assertName(t *testing.T, uid, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s name = %q, want %q", uid, got, want)
	}
}
