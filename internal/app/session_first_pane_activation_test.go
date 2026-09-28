package app

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// staleSessionFirstPaneRuntime is a runtime id no fake server ever mints in
// these fixtures, standing in for the %N a Pane kept from an earlier server.
const staleSessionFirstPaneRuntime = "%9001"

// assertSessionPanesCarryTheirLiveRuntimeIDs is the C-1 guarantee read off
// one materialized session: every live Pane, the session's own first Pane
// included, records the exact tmux handle it runs on, and the first Pane was
// launched under the supervisor of the generation the Registry recorded.
func assertSessionPanesCarryTheirLiveRuntimeIDs(t *testing.T, registry *coremetadata.Registry, server *fakeTmux, sessionName string) {
	t.Helper()
	session := server.session(sessionName)
	if session == nil || len(session.windows) == 0 || len(session.windows[0].panes) == 0 {
		t.Fatalf("session %q was not materialized:\n%s", sessionName, server.state())
	}
	for _, window := range session.windows {
		for _, live := range window.panes {
			uid := live.opts[tmuxopts.PaneUID]
			pane, ok := registry.Pane(uid)
			if !ok {
				t.Fatalf("live pane %s carries uid %q with no Registry Pane", live.id, uid)
			}
			if got := pane.Status.Activation.RuntimeID; got != live.id {
				t.Fatalf("Pane %s (live %s, first=%t) activation runtimeID = %q, want %q",
					uid, live.id, live == session.windows[0].panes[0], got, live.id)
			}
		}
	}
	first := session.windows[0].panes[0]
	pane, _ := registry.Pane(first.opts[tmuxopts.PaneUID])
	generation := pane.Status.Activation.Generation
	if generation == "" {
		t.Fatalf("session first Pane %s has no activation generation", pane.Metadata.UID)
	}
	want := "internal supervise --pane-uid " + pane.Metadata.UID + " --generation " + generation
	if !strings.Contains(first.command, want) {
		t.Fatalf("session first Pane launch = %q, want the supervisor of its own generation (%q)", first.command, want)
	}
	seen := map[string]string{}
	for _, pane := range registry.Panes {
		runtimeID := pane.Status.Activation.RuntimeID
		if runtimeID == "" {
			continue
		}
		if other, ok := seen[runtimeID]; ok {
			t.Fatalf("Panes %s and %s share runtime id %s", other, pane.Metadata.UID, runtimeID)
		}
		seen[runtimeID] = pane.Metadata.UID
	}
}

// markEveryPaneStale gives every Pane of the Project a generation and a
// runtime id from a server that no longer exists.
func markEveryPaneStale(t *testing.T, store *fakeResourceStore, projectUID string) {
	t.Helper()
	for _, window := range store.registry.WindowsOf(projectUID) {
		for _, pane := range store.registry.PanesOf(window.Metadata.UID) {
			stored, _ := store.registry.Pane(pane.Metadata.UID)
			stored.Status.Activation.Generation = "gen-stale-" + pane.Metadata.UID
			stored.Status.Activation.RuntimeID = staleSessionFirstPaneRuntime
		}
	}
}

// TestOfflineProjectCreateRecordsTheSessionFirstPaneRuntimeID is Task 1
// acceptance 1 and 4 on the offline Project create path: the Pane new-session
// brings with it is launched under its own generation and records its %N in
// the create transaction, whether it started blank or kept an old %N.
func TestOfflineProjectCreateRecordsTheSessionFirstPaneRuntimeID(t *testing.T) {
	t.Parallel()

	t.Run("implicit idx0 bootstrap starts blank", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		store.dirs["/srv/gamma"] = true
		registered, err := store.mutator().RegisterProject(&store.registry, coremetadata.RegisterProjectOptions{
			Root: "/srv/gamma", SessionName: "gamma", OperationID: "op-register-gamma",
		})
		if err != nil {
			t.Fatal(err)
		}
		tmux := newFakeTmux()
		create, _ := newTestResourceCreateCommand(t, store, tmux)
		if _, _, err := runRoute(t, create, "window", "--project", "uid:"+registered.Project.Metadata.UID, "--name", "w-first"); err != nil {
			t.Fatalf("create window on offline Project: %v", err)
		}
		assertSessionPanesCarryTheirLiveRuntimeIDs(t, &store.registry, tmux, "gamma")
	})

	t.Run("stored first Pane keeps an old runtime id", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		first := store.registry.WindowsOf("prj-beta")[0]
		if _, _, err := store.mutator().EnsureWindowDefaultShell(&store.registry, first.Metadata.UID, "/bin/zsh", "op-fixture"); err != nil {
			t.Fatal(err)
		}
		markEveryPaneStale(t, store, "prj-beta")
		tmux := newFakeTmux()
		create, _ := newTestResourceCreateCommand(t, store, tmux)
		if _, _, err := runRoute(t, create, "window", "--project", "beta", "--name", "w-first"); err != nil {
			t.Fatalf("create window on offline Project: %v", err)
		}
		assertSessionPanesCarryTheirLiveRuntimeIDs(t, &store.registry, tmux, "beta")
	})
}

// TestContinueRecordsTheSessionFirstPaneRuntimeID is acceptance 1, 2 and 4 on
// Continue (`start project`): a Project whose stored Panes all kept a %N from
// an earlier server comes back with every Pane, the first one included, on its
// new %N and no two Panes sharing one.
func TestContinueRecordsTheSessionFirstPaneRuntimeID(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "blank", true: "stale"}[stale], func(t *testing.T) {
			activation, store, server, root, _ := newProjectStartupTopologyFixture(t)
			if stale {
				markEveryPaneStale(t, store, "prj-beta")
			}
			materialized, err := activation.MaterializeProjectTopology(context.Background(), projectTopologyMaterializeRequest{Root: root, SessionName: "beta"})
			if err != nil || !materialized {
				t.Fatalf("Continue = %t, %v", materialized, err)
			}
			assertSessionPanesCarryTheirLiveRuntimeIDs(t, &store.registry, server, "beta")
		})
	}
}

// TestReconcileMaterializeProjectRecordsTheSessionFirstPaneRuntimeID is
// acceptance 3: `reconcile resources --materialize-project` shares the
// topology replay and so the same first-Pane activation.
func TestReconcileMaterializeProjectRecordsTheSessionFirstPaneRuntimeID(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "blank", true: "stale"}[stale], func(t *testing.T) {
			command, store, server, _, _, _ := newTopologyMaterializeFixture(t)
			if stale {
				markEveryPaneStale(t, store, "prj-beta")
			}
			if _, stderr, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "uid:prj-beta", "-o", "json"); err != nil {
				t.Fatalf("materialize-project: err=%v stderr=%q", err, stderr)
			}
			assertSessionPanesCarryTheirLiveRuntimeIDs(t, &store.registry, server, "beta")
		})
	}
}

// TestCanonicalProjectStartupRecordsTheSessionFirstPaneRuntimeID covers the
// third session-creating caller, the bootstrapped Project session.
func TestCanonicalProjectStartupRecordsTheSessionFirstPaneRuntimeID(t *testing.T) {
	activation, store, server, root, _ := newProjectStartupTopologyFixture(t)
	markEveryPaneStale(t, store, "prj-beta")
	first := store.registry.WindowsOf("prj-beta")[0]
	if _, _, err := store.mutator().EnsureWindowDefaultShell(&store.registry, first.Metadata.UID, "/bin/zsh", "op-fixture"); err != nil {
		t.Fatal(err)
	}
	project, _ := store.registry.Project("prj-beta")
	recorder, _, _ := topologyJournalFixture(t)
	if err := materializeProjectSessionCanonical(context.Background(), store.store(), canonicalSeparatorRunner(activation.runner),
		runtimeMutationRoute{target: activation.target, socketName: defaultAppSocket}, recorder,
		"beta", root, *project); err != nil {
		t.Fatalf("canonical Project startup: %v", err)
	}
	session := server.session("beta")
	if session == nil {
		t.Fatalf("canonical startup did not create beta:\n%s", server.state())
	}
	firstLive := session.windows[0].panes[0]
	pane, ok := store.registry.Pane(firstLive.opts[tmuxopts.PaneUID])
	if !ok || pane.Status.Activation.RuntimeID != firstLive.id {
		t.Fatalf("canonical first Pane activation = %+v, want runtime id %s", pane, firstLive.id)
	}
	if want := "--pane-uid " + pane.Metadata.UID + " --generation " + pane.Status.Activation.Generation; !strings.Contains(firstLive.command, want) {
		t.Fatalf("canonical first Pane launch = %q, want %q", firstLive.command, want)
	}
}

// canonicalSeparatorRunner adapts the canonical session client's literal
// field separators to the app fake, as the canonical startup tests do.
func canonicalSeparatorRunner(runner tmuxCommandRunner) tmuxCommandRunner {
	return lifecycleTmuxRunnerFunc(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		adapted := append([]string(nil), args...)
		literal := false
		for i, arg := range adapted {
			if strings.Contains(arg, "\x1f") {
				literal = true
				adapted[i] = strings.ReplaceAll(arg, "\x1f", tmuxRowSepFormat)
			}
		}
		out, err := runner.Run(ctx, name, adapted...)
		if literal {
			out = bytes.ReplaceAll(out, []byte(tmuxRowSepFormat), []byte("\x1f"))
		}
		return out, err
	})
}

// TestSessionFirstPaneRuntimeRecordFailureWarns is acceptance 5: when the
// first Pane's %N cannot be recorded, the pass says so with the same warning
// every other Pane's launch gives, instead of leaving the id silently unset.
func TestSessionFirstPaneRuntimeRecordFailureWarns(t *testing.T) {
	command, _, server, _, _, _ := newTopologyMaterializeFixture(t)
	var working *coremetadata.Registry
	update := command.resources.updateConvergent
	command.resources.updateConvergent = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error) {
		return update(func(registry *coremetadata.Registry) error {
			working = registry
			return fn(registry)
		})
	}
	// The injected fault removes the first Pane's row between its launch and
	// the record, the one way the Registry write can refuse.
	server.beforeDispatch = func(_ *fakeTmux, args []string) {
		if len(args) == 0 || args[0] != "new-session" || working == nil {
			return
		}
		working.Panes = slices.DeleteFunc(working.Panes, func(pane coremetadata.Pane) bool {
			return pane.Metadata.UID == "pan-beta-zsh"
		})
	}
	_, stderr, _ := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "uid:prj-beta", "-o", "json")
	if !strings.Contains(stderr, "projmux: pane pan-beta-zsh activation runtime handle was not recorded") {
		t.Fatalf("first Pane record failure was silent; stderr=%q", stderr)
	}
}
