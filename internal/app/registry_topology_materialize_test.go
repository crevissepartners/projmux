package app

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

func newTopologyMaterializeFixture(t *testing.T) (*resourceReconcileCommand, *fakeResourceStore, *fakeTmux, *routedTmuxRunner, string, string) {
	t.Helper()
	command, store, server, runner, root, logs, _ := newTopologyMaterializeFixtureWithSessions(t)
	return command, store, server, runner, root, logs
}

func newTopologyMaterializeFixtureWithSessions(t *testing.T) (*resourceReconcileCommand, *fakeResourceStore, *fakeTmux, *routedTmuxRunner, string, string, *fakeSessionMaterializer) {
	t.Helper()
	root, logs := t.TempDir(), t.TempDir()
	store := newFakeResourceStore(t)
	project, _ := store.registry.Project("prj-beta")
	project.Spec.Root = root
	for i := range store.registry.Panes {
		pane := &store.registry.Panes[i]
		if pane.Metadata.OwnerUID() == "win-beta-main" {
			pane.Spec.CWD = root
		}
	}
	store.dirs = map[string]bool{root: true, logs: true}
	mutator := store.mutator()
	if _, err := mutator.AddPane(&store.registry, "win-beta-main", coremetadata.BootstrapPane{
		Name: "logs", CWD: logs, Command: "DO-NOT-EXECUTE --pane",
	}, "/bin/zsh", "op-topology-fixture"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := mutator.AddWindow(&store.registry, "prj-beta", coremetadata.BootstrapWindow{
		Name: "review", Command: "DO-NOT-EXECUTE --window", Panes: []coremetadata.BootstrapPane{{CWD: root, Command: "DO-NOT-EXECUTE --primary"}},
	}, "/bin/zsh", "op-topology-fixture"); err != nil {
		t.Fatal(err)
	}
	// The shared fixture Project is deliberately shell-only. Agent replay has
	// its own fixtures in registry_topology_agents_test.go, so every parity
	// assertion in this file keeps measuring exactly the Window/shell-Pane
	// behavior it measured before Agents entered the plan.
	if err := mutator.DeleteAgent(&store.registry, "agt-beta-codex"); err != nil {
		t.Fatal(err)
	}
	server := newFakeTmux()
	server.socketPath = "/tmp/fake-tmux/topology"
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-L\x00topology": server}}
	sessions := &fakeSessionMaterializer{tmux: server}
	command := &resourceReconcileCommand{
		runner: runner, resources: store.store(), lookupEnv: func(string) string { return "" },
		agents:         newFakeTopologyAgentLauncher(),
		newReconciler:  reconcileFixtureReconciler(root, "beta"),
		newOperationID: func() (string, error) { return "op-topology", nil },
		newMaterializer: func(exact tmuxCommandRunner, warn io.Writer) *materializer {
			return &materializer{runner: exact, mirror: intmetadata.NewMirror(exact), sessions: sessions, warn: warn}
		},
	}
	return command, store, server, runner, root, logs, sessions
}

func TestRegistryTopologyMaterializationDryRunExecuteAndRepeatNoop(t *testing.T) {
	command, store, server, _, root, logs := newTopologyMaterializeFixture(t)
	registryBefore, tmuxBefore := store.snapshot(), server.state()
	first, stderr, err := runReconcile(t, command, "resources", "--dry-run", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
	if err != nil || stderr != "" {
		t.Fatalf("dry-run: err=%v stderr=%q\n%s", err, stderr, first)
	}
	second, _, err := runReconcile(t, command, "resources", "--materialize-project", "beta", "--socket", "topology", "--dry-run", "-o", "json")
	if err != nil || first != second {
		t.Fatalf("dry-run is not deterministic: err=%v\nfirst=%s\nsecond=%s", err, first, second)
	}
	for _, want := range []string{
		`"kind": "Project"`, `"kind": "Window"`, `"kind": "Pane"`,
		`"retry": "projmux reconcile resources --socket 'topology' --materialize-project 'beta'"`,
	} {
		if !strings.Contains(first, want) {
			t.Fatalf("preview missing %q:\n%s", want, first)
		}
	}
	human, _, err := runReconcile(t, command, "resources", "--dry-run", "--socket", "topology", "--materialize-project", "beta")
	if err != nil || !strings.Contains(human, "[missing] tmux materialize beta") || !strings.Contains(human, "uid:prj-beta") {
		t.Fatalf("human materialization plan mismatch: err=%v\n%s", err, human)
	}
	if store.snapshot() != registryBefore || server.state() != tmuxBefore || store.transactions != 0 || store.writes != 0 {
		t.Fatalf("dry-run mutated state: transactions=%d writes=%d", store.transactions, store.writes)
	}

	result, stderr, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "uid:prj-beta", "-o", "json")
	if err != nil || stderr != "" {
		t.Fatalf("execute: err=%v stderr=%q\n%s", err, stderr, result)
	}
	session := server.session("beta")
	if session == nil || session.opts[tmuxopts.ProjectUIDSession] != "prj-beta" || len(session.windows) != 2 {
		t.Fatalf("materialized Project topology = %s", server.state())
	}
	if got := len(session.windows[0].panes); got != 2 {
		t.Fatalf("main panes=%d, want 2\n%s", got, server.state())
	}
	for _, call := range server.calls {
		joined := strings.Join(call, " ")
		if len(call) > 0 && slices.Contains([]string{"new-session", "new-window", "split-window"}, call[0]) && strings.Contains(joined, "DO-NOT-EXECUTE") {
			t.Fatalf("stored command reached tmux argv: %v", call)
		}
	}
	project, _ := store.registry.Project("prj-beta")
	if project.Status.Session == nil || !project.Status.Session.Live {
		t.Fatalf("Project status.session = %+v", project.Status.Session)
	}
	// Both CWD values are present on the exact detached create argv. The fake
	// tmux does not model pane_current_path, so argv is the authoritative seam.
	for _, cwd := range []string{root, logs} {
		if !slices.ContainsFunc(server.calls, func(call []string) bool { return flagValue(call, "-c") == cwd }) {
			t.Fatalf("no exact -c %s call: %#v", cwd, server.calls)
		}
	}

	server.calls = nil
	writesBefore := store.writes
	repeat, _, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
	if err != nil || !strings.Contains(repeat, `"outcome": "no-op"`) || store.writes != writesBefore {
		t.Fatalf("repeat was not a Registry-write-free no-op: err=%v writes=%d->%d\n%s", err, writesBefore, store.writes, repeat)
	}
	for _, call := range server.calls {
		if len(call) > 0 && slices.Contains([]string{"new-session", "new-window", "split-window", "set-environment"}, call[0]) {
			t.Fatalf("no-op mutated runtime with %v", call)
		}
	}
}

func TestRegistryTopologyMaterializationRefusesBeforeFirstCreate(t *testing.T) {
	command, store, server, _, _, _ := newTopologyMaterializeFixture(t)
	main, _ := store.registry.Window("win-beta-main")
	main.Spec.AnchorPaneRef = ""
	before := store.snapshot()
	out, _, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
	if err == nil || !strings.Contains(out, `"action": "refuse"`) {
		t.Fatalf("invalid primary was not refused: err=%v\n%s", err, out)
	}
	if store.snapshot() != before || store.writes != 0 || len(server.sessions) != 0 {
		t.Fatalf("refusal mutated state: writes=%d\n%s", store.writes, server.state())
	}
}

func TestRegistryTopologyMaterializationRejectsDuplicateSelectorOccurrence(t *testing.T) {
	command, store, server, _, _, _ := newTopologyMaterializeFixture(t)
	_, _, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "--materialize-project", "alpha")
	if err == nil || !IsUsageError(err) {
		t.Fatalf("duplicate --materialize-project error = %v", err)
	}
	if store.transactions != 0 || store.writes != 0 || len(server.calls) != 0 {
		t.Fatalf("duplicate selector reached state: transactions=%d writes=%d calls=%v", store.transactions, store.writes, server.calls)
	}
}

// A present-but-blank selector must never degrade into the broad default
// reconcile, which would execute unrelated repair the caller did not ask for.
func TestRegistryTopologyMaterializationRejectsBlankSelectorValue(t *testing.T) {
	for _, blank := range []string{"", " ", "\t", "  \t "} {
		t.Run(fmt.Sprintf("%q", blank), func(t *testing.T) {
			command, store, server, _, _, _ := newTopologyMaterializeFixture(t)
			before := store.snapshot()
			stdout, stderr, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", blank)
			if err == nil || !IsUsageError(err) {
				t.Fatalf("blank --materialize-project error = %v (usage=%t)", err, IsUsageError(err))
			}
			if !strings.Contains(err.Error(), "requires a non-empty Project name or uid") {
				t.Fatalf("blank selector error is not the exact refusal: %v", err)
			}
			if store.reads != 0 || store.transactions != 0 || store.writes != 0 {
				t.Fatalf("blank selector reached the Registry: reads=%d transactions=%d writes=%d",
					store.reads, store.transactions, store.writes)
			}
			if len(server.calls) != 0 || store.snapshot() != before {
				t.Fatalf("blank selector reached tmux or mutated the Registry: calls=%v", server.calls)
			}
			if stdout != "" || stderr != "" {
				t.Fatalf("blank selector produced output: stdout=%q stderr=%q", stdout, stderr)
			}
		})
	}

	// The same blank value with --dry-run is refused on the identical boundary,
	// so a preview cannot become the broad default preview either.
	command, store, server, _, _, _ := newTopologyMaterializeFixture(t)
	if _, _, err := runReconcile(t, command, "resources", "--dry-run", "--socket", "topology", "--materialize-project", " "); err == nil || !IsUsageError(err) {
		t.Fatalf("blank --materialize-project --dry-run error = %v", err)
	}
	if store.reads != 0 || store.transactions != 0 || len(server.calls) != 0 {
		t.Fatalf("blank selector dry-run reached state: reads=%d transactions=%d calls=%v", store.reads, store.transactions, server.calls)
	}
}

func TestRegistryTopologyMaterializationSocketPathOfflineIsHookSafetyRefusal(t *testing.T) {
	command, store, server, runner, _, _ := newTopologyMaterializeFixture(t)
	socketPath := t.TempDir() + "/exact.sock"
	server.socketPath = socketPath
	runner.servers["-S\x00"+socketPath] = server
	preview, _, err := runReconcile(t, command, "resources", "--dry-run", "--socket-path", socketPath, "--materialize-project", "beta", "-o", "json")
	if err != nil {
		t.Fatalf("-S safety preview: %v\n%s", err, preview)
	}
	for _, want := range []string{
		`"action": "materialize"`, `"action": "refuse"`, "name-only PROJMUX_SOCKET hook re-entry",
		`"retry": "projmux reconcile resources --socket-path '` + socketPath + `' --materialize-project 'beta'"`,
	} {
		if !strings.Contains(preview, want) {
			t.Fatalf("-S preview missing %q:\n%s", want, preview)
		}
	}
	human, _, err := runReconcile(t, command, "resources", "--dry-run", "--socket-path", socketPath, "--materialize-project", "beta")
	if err != nil || !strings.Contains(human, "tmux refuse beta") || !strings.Contains(human, "name-only PROJMUX_SOCKET") {
		t.Fatalf("-S human refusal mismatch: err=%v\n%s", err, human)
	}
	before := store.snapshot()
	result, _, err := runReconcile(t, command, "resources", "--socket-path", socketPath, "--materialize-project", "beta", "-o", "json")
	if err == nil || !strings.Contains(result, `"outcome": "failed"`) {
		t.Fatalf("-S offline execute was not refused: err=%v\n%s", err, result)
	}
	if store.snapshot() != before || store.writes != 0 || len(server.sessions) != 0 {
		t.Fatalf("-S refusal mutated state: writes=%d\n%s", store.writes, server.state())
	}
}

func TestRegistryTopologyMaterializationSocketPathRepairsLiveSubsetAndIsolatesSibling(t *testing.T) {
	command, _, server, runner, _, _ := newTopologyMaterializeFixture(t)
	if _, _, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json"); err != nil {
		t.Fatalf("seed live topology: %v", err)
	}
	sibling := newFakeTmux()
	sibling.addSession("sibling")
	siblingBefore := sibling.state()
	runner.servers["-L\x00sibling"] = sibling
	socketPath := t.TempDir() + "/live.sock"
	runner.servers["-S\x00"+socketPath] = server
	server.socketPath = socketPath
	main := server.session("beta").windows[0]
	if len(main.panes) != 2 {
		t.Fatalf("seed panes=%d", len(main.panes))
	}
	main.panes = main.panes[:1] // raw runtime loss; Registry authority remains.
	server.calls = nil
	runner.calls = nil
	result, _, err := runReconcile(t, command, "resources", "--socket-path", socketPath, "--materialize-project", "beta", "-o", "json")
	if err != nil || len(main.panes) != 2 {
		t.Fatalf("-S live partial: err=%v panes=%d\n%s", err, len(main.panes), result)
	}
	if sibling.state() != siblingBefore {
		t.Fatalf("selected -S mutated sibling socket:\n%s", sibling.state())
	}
	for _, call := range runner.calls {
		if call.flag != "-S" || call.value != socketPath {
			t.Fatalf("live partial escaped exact -S route: %v", call)
		}
	}
}

func TestRegistryTopologyMaterializationRawLossVersusCanonicalDelete(t *testing.T) {
	command, store, server, _, _, _ := newTopologyMaterializeFixture(t)
	if _, _, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json"); err != nil {
		t.Fatal(err)
	}
	logsUID := ""
	for _, pane := range store.registry.PanesOf("win-beta-main") {
		if pane.Metadata.Name == "logs" {
			logsUID = pane.Metadata.UID
		}
	}
	main := server.session("beta").windows[0]
	main.panes = main.panes[:1]
	if _, _, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json"); err != nil {
		t.Fatalf("repair raw loss: %v", err)
	}
	if len(main.panes) != 2 || main.panes[1].opts[tmuxopts.PaneUID] != logsUID {
		t.Fatalf("raw-lost Registry Pane was not recreated: %s", server.state())
	}
	if err := store.mutator().DeletePane(&store.registry, logsUID); err != nil {
		t.Fatal(err)
	}
	main.panes = main.panes[:1]
	if _, _, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json"); err != nil {
		t.Fatalf("reconcile after canonical delete: %v", err)
	}
	if len(main.panes) != 1 {
		t.Fatalf("canonical-deleted Pane reappeared: %s", server.state())
	}
}

func TestRegistryTopologyMaterializationRecreatesMissingPrimaryFromBoundShellAnchor(t *testing.T) {
	command, store, server, _, _, _ := newTopologyMaterializeFixture(t)
	if _, _, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json"); err != nil {
		t.Fatal(err)
	}
	window, _ := store.registry.Window("win-beta-main")
	main := server.session("beta").windows[0]
	primaryIndex := slices.IndexFunc(main.panes, func(pane *fakeTmuxPane) bool {
		return pane.opts[tmuxopts.PaneUID] == window.Spec.AnchorPaneRef
	})
	if primaryIndex < 0 || len(main.panes) < 2 {
		t.Fatalf("fixture has no primary plus bound shell anchor: %s", server.state())
	}
	main.panes = slices.Delete(main.panes, primaryIndex, primaryIndex+1)
	result, _, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
	if err != nil {
		t.Fatalf("recreate primary from bound shell anchor: %v\n%s", err, result)
	}
	if !slices.ContainsFunc(main.panes, func(pane *fakeTmuxPane) bool {
		return pane.opts[tmuxopts.PaneUID] == window.Spec.AnchorPaneRef
	}) {
		t.Fatalf("primary Pane was not recreated: %s", server.state())
	}
}

// A first UID marker write that fails leaves the exact new handle unowned, so
// the ownership-checked rollback must preserve it rather than guess. The
// operation still fails, the Registry is untouched, and the residual handle is
// named on stderr so it can be inspected before retry or manual cleanup.
func TestRegistryTopologyMaterializationFirstUIDMarkerFailurePreservesUnownedResidual(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fail   []string
		shrink func(*fakeTmux)
		kind   string
	}{
		{
			name: "Window marker",
			fail: []string{"set-option", "-w", tmuxopts.WindowUID},
			kind: "window",
			shrink: func(server *fakeTmux) {
				session := server.session("beta")
				// Raw loss of the second Registry Window makes the next operation create it.
				session.windows = session.windows[:1]
			},
		},
		{
			name: "Pane marker",
			fail: []string{"set-option", "-p", tmuxopts.PaneUID},
			kind: "pane",
			shrink: func(server *fakeTmux) {
				main := server.session("beta").windows[0]
				main.panes = main.panes[:1]
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command, store, server, _, _, _ := newTopologyMaterializeFixture(t)
			if _, _, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json"); err != nil {
				t.Fatal(err)
			}
			tc.shrink(server)
			writes := store.writes
			registry := store.snapshot()
			server.fail, server.failed = tc.fail, false
			result, stderr, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
			if err == nil || !strings.Contains(result, `"remainingDrift"`) {
				t.Fatalf("marker failure was not reported: err=%v\n%s", err, result)
			}
			if store.writes != writes || store.snapshot() != registry {
				t.Fatalf("marker failure changed the Registry: writes=%d want %d", store.writes, writes)
			}
			if !strings.Contains(stderr, "could not claim tmux "+tc.kind) || !strings.Contains(stderr, "preserved this exact residual handle") {
				t.Fatalf("unowned residual handle was not surfaced: %q", stderr)
			}
			for _, window := range server.session("beta").windows {
				if window.opts[tmuxopts.WindowUID] == "" && tc.kind == "window" {
					continue // the preserved unowned residual itself
				}
				for _, pane := range window.panes {
					if pane.opts[tmuxopts.PaneUID] == "" && tc.kind == "pane" {
						continue
					}
					if pane.opts[tmuxopts.PaneUID] == "" {
						t.Fatalf("materialization left an extra unowned Pane:\n%s", server.state())
					}
				}
			}
		})
	}
}

// tmux can apply a mutation and then report a synchronous lifecycle-hook
// failure in the same call. The exact attributed handle must be claimed and
// ledgered before the original error surfaces, or the ownership-checked
// rollback has nothing to remove and the object leaks.
func TestRegistryTopologyMaterializationPostMutationHookFailureRollsBackExactHandle(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fail    []string
		shrink  func(*fakeTmux, *testing.T)
		residue string
	}{
		{
			name:    "new-window",
			fail:    []string{"new-window"},
			residue: "window",
			shrink: func(server *fakeTmux, _ *testing.T) {
				session := server.session("beta")
				session.windows = session.windows[:1]
			},
		},
		{
			name:    "split-window",
			fail:    []string{"split-window"},
			residue: "pane",
			shrink: func(server *fakeTmux, _ *testing.T) {
				main := server.session("beta").windows[0]
				main.panes = main.panes[:1]
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command, store, server, _, _, _ := newTopologyMaterializeFixture(t)
			if _, _, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json"); err != nil {
				t.Fatal(err)
			}
			tc.shrink(server, t)
			before := server.state()
			writes := store.writes
			registry := store.snapshot()
			server.fail, server.failed = tc.fail, false
			server.failAfterMutation = true
			server.failMessage = "synchronous lifecycle hook refused"

			result, _, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
			if err == nil || !strings.Contains(err.Error(), "synchronous lifecycle hook refused") {
				t.Fatalf("post-mutation %s failure error = %v", tc.residue, err)
			}
			if !strings.Contains(result, `"remainingDrift"`) {
				t.Fatalf("post-mutation failure omitted residual drift:\n%s", result)
			}
			if got := server.state(); got != before {
				t.Fatalf("rollback did not remove the exact error-created %s:\n--- got ---\n%s\n--- want ---\n%s", tc.residue, got, before)
			}
			if store.writes != writes || store.snapshot() != registry {
				t.Fatalf("post-mutation failure committed Registry state: writes=%d want %d", store.writes, writes)
			}
		})
	}
}

// Creating the selected Project session runs the public pre/post-create hooks,
// and no rollback can undo a hook's side effects. So a foreign live claim on a
// desired Window or Pane uid must be refused before the session is created, not
// after -- which means the server-wide uid preflight cannot wait for
// ensureSessionAt.
func TestRegistryTopologyMaterializationOfflineForeignUIDRefusesBeforeSessionCreate(t *testing.T) {
	for _, tc := range []struct {
		name string
		seed func(*fakeTmux)
	}{
		{
			name: "foreign session claims a desired Window uid",
			seed: func(server *fakeTmux) {
				foreign := server.addSession("foreign-topology")
				foreign.windows[0].opts[tmuxopts.WindowUID] = "win-beta-main"
			},
		},
		{
			name: "foreign session claims a desired Pane uid",
			seed: func(server *fakeTmux) {
				foreign := server.addSession("foreign-topology")
				foreign.windows[0].panes[0].opts[tmuxopts.PaneUID] = "pan-beta-zsh"
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command, store, server, _, _, _, sessions := newTopologyMaterializeFixtureWithSessions(t)
			hookRuns := 0
			sessions.postCreate = func() { hookRuns++ }
			tc.seed(server)
			before := server.state()
			registry := store.snapshot()

			result, _, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
			if err == nil || !strings.Contains(err.Error(), "is already live on") {
				t.Fatalf("foreign desired uid error = %v\n%s", err, result)
			}
			if got := len(sessions.created); got != 0 {
				t.Fatalf("refusal created %d session(s) before detecting the collision: %v", got, sessions.created)
			}
			if hookRuns != 0 {
				t.Fatalf("refusal ran %d post-create hook(s)", hookRuns)
			}
			if server.session("beta") != nil {
				t.Fatalf("refusal materialized the selected Project session:\n%s", server.state())
			}
			for _, call := range server.calls {
				if len(call) > 0 && slices.Contains([]string{"new-session", "new-window", "split-window", "set-environment"}, call[0]) {
					t.Fatalf("refusal reached a runtime mutation: %v", call)
				}
			}
			if server.state() != before || store.writes != 0 || store.snapshot() != registry {
				t.Fatalf("refusal mutated state: writes=%d\n%s", store.writes, server.state())
			}
		})
	}
}

// UID strings alone do not prove topology. A live Window relinked to another
// session, and a live anchor Pane moved into another Window, both keep their
// uid; materialization must refuse them before the first mutation rather than
// split into the wrong parent.
func TestRegistryTopologyMaterializationRefusesRelinkedParentBeforeMutation(t *testing.T) {
	t.Run("Window moved to a foreign session", func(t *testing.T) {
		command, store, server, _, _, _ := newTopologyMaterializeFixture(t)
		if _, _, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json"); err != nil {
			t.Fatal(err)
		}
		beta := server.session("beta")
		main := beta.windows[0]
		// Raw loss of one Pane makes the next pass want a split inside `main`.
		main.panes = main.panes[:1]
		// `main` is relinked into a foreign session with its uid intact.
		foreign := server.addSession("foreign-topology")
		beta.windows = beta.windows[1:]
		foreign.windows = append(foreign.windows, main)
		before := server.state()
		writes := store.writes

		result, _, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
		// The relinked Window is invisible to the selected-session plan, so the
		// refusal that matters is the server-wide uid claim: creating a second
		// live Window for the same Registry uid must never happen.
		if err == nil || !strings.Contains(err.Error(), "is already live on") {
			t.Fatalf("relinked Window error = %v\n%s", err, result)
		}
		if server.state() != before || store.writes != writes {
			t.Fatalf("relinked Window was mutated: writes=%d want %d\n%s", store.writes, writes, server.state())
		}
	})

	t.Run("anchor Pane joined away after planning", func(t *testing.T) {
		command, store, server, _, _, _ := newTopologyMaterializeFixture(t)
		if _, _, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json"); err != nil {
			t.Fatal(err)
		}
		beta := server.session("beta")
		main, review := beta.windows[0], beta.windows[1]
		// The extra shell Pane is gone, so the pass plans one split off the
		// still-live primary anchor inside `main`.
		anchor := main.panes[0]
		main.panes = main.panes[:1]
		// After the plan commits to that anchor, and before any mutation, the
		// anchor is join-paned into another Window with its uid intact.
		// The baseline is captured after the injected move, so any difference is
		// a materialization mutation rather than the test's own race.
		var before string
		server.beforeOwnerInventory = func(f *fakeTmux) {
			main.panes = main.panes[:0]
			review.panes = append(review.panes, anchor)
			before = f.state()
		}
		writes := store.writes

		result, _, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
		if err == nil || !strings.Contains(err.Error(), "is not owned by session") {
			t.Fatalf("anchor Pane moved after planning was accepted: err=%v\n%s\n%s", err, result, server.state())
		}
		if server.state() != before || store.writes != writes {
			t.Fatalf("raced anchor Pane was mutated: writes=%d want %d\n%s", store.writes, writes, server.state())
		}
	})

	t.Run("anchor Pane joined into another Window before planning", func(t *testing.T) {
		command, store, server, _, _, _ := newTopologyMaterializeFixture(t)
		if _, _, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json"); err != nil {
			t.Fatal(err)
		}
		beta := server.session("beta")
		main, review := beta.windows[0], beta.windows[1]
		sibling := main.panes[1]
		main.panes = main.panes[:0]
		review.panes = append(review.panes, sibling)
		before := server.state()
		writes := store.writes

		// Observable before the plan is built, so this is a pre-create planner
		// refusal rather than a guard failure. Either way nothing is mutated.
		result, _, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
		if err == nil || !strings.Contains(result, `"action": "refuse"`) {
			t.Fatalf("joined anchor Pane was accepted: err=%v\n%s", err, result)
		}
		if server.state() != before || store.writes != writes {
			t.Fatalf("joined anchor Pane was mutated: writes=%d want %d\n%s", store.writes, writes, server.state())
		}
	})
}

func TestRegistryTopologyMaterializationFailureRollsBackOwnedRuntime(t *testing.T) {
	command, store, server, _, _, _ := newTopologyMaterializeFixture(t)
	before := store.snapshot()
	server.fail = []string{"split-window"}
	result, _, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
	if err == nil || !strings.Contains(result, `"remainingDrift"`) {
		t.Fatalf("injected failure missing structured residual drift: err=%v\n%s", err, result)
	}
	if len(server.sessions) != 0 || store.snapshot() != before || store.writes != 0 {
		t.Fatalf("failure did not roll back owned runtime/Registry: writes=%d\n%s", store.writes, server.state())
	}
}

func TestRegistryTopologyMaterializationInvalidCWDAndForeignSessionRefuse(t *testing.T) {
	t.Run("invalid Pane cwd", func(t *testing.T) {
		command, store, server, _, _, _ := newTopologyMaterializeFixture(t)
		for i := range store.registry.Panes {
			if store.registry.Panes[i].Metadata.OwnerUID() == "win-beta-main" {
				store.registry.Panes[i].Spec.CWD = "relative"
				break
			}
		}
		out, _, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
		if err == nil || !strings.Contains(out, "clean absolute directory") || len(server.sessions) != 0 {
			t.Fatalf("invalid CWD was not a pre-create refusal: err=%v\n%s", err, out)
		}
	})
	t.Run("foreign session name collision", func(t *testing.T) {
		command, store, server, _, _, _ := newTopologyMaterializeFixture(t)
		foreign := server.addSession("beta")
		foreign.opts[tmuxopts.ProjectUIDSession] = "prj-foreign"
		foreign.opts[tmuxopts.ProjectPathSession] = store.registry.Projects[1].Spec.Root
		before := server.state()
		out, _, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
		if err == nil || !strings.Contains(out, "foreign Project uid") || server.state() != before || store.writes != 0 {
			t.Fatalf("foreign session was not refused byte-stably: err=%v\n%s", err, out)
		}
	})
}

func TestRegistryTopologyMaterializationRefusesAgentPrimaryAndZeroWindows(t *testing.T) {
	t.Run("Agent-owned primary", func(t *testing.T) {
		command, store, server, _, root, _ := newTopologyMaterializeFixture(t)
		agent := addTopologyFixtureAgent(t, store, topologyFixtureAgent{name: "codex", provider: "codex", cwd: root})
		managed, err := store.mutator().AttachAgentPane(&store.registry, agent.Metadata.UID, coremetadata.BootstrapPane{
			Name: "managed", CWD: root,
		}, "op-agent-primary")
		if err != nil {
			t.Fatal(err)
		}
		window, _ := store.registry.Window("win-beta-main")
		window.Spec.AnchorPaneRef = managed.Metadata.UID
		out, _, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
		if err == nil || !strings.Contains(out, "Window-owned shell Pane") || len(server.sessions) != 0 {
			t.Fatalf("Agent-owned primary was not refused before create: err=%v\n%s", err, out)
		}
	})
	t.Run("zero Windows", func(t *testing.T) {
		command, store, server, _, _, _ := newTopologyMaterializeFixture(t)
		// This is deliberately an invalid-v2 diagnostic fixture. Canonical
		// DeleteWindow must preserve the Project anchor, so bypass the mutator
		// when testing materialization's refusal of a damaged Registry.
		removed := map[string]bool{}
		for _, window := range store.registry.WindowsOf("prj-beta") {
			removed[window.Metadata.UID] = true
			for _, agent := range store.registry.AgentsOf(window.Metadata.UID) {
				removed[agent.Metadata.UID] = true
			}
		}
		store.registry.Windows = slices.DeleteFunc(store.registry.Windows, func(window coremetadata.Window) bool {
			return removed[window.Metadata.UID]
		})
		store.registry.Agents = slices.DeleteFunc(store.registry.Agents, func(agent coremetadata.Agent) bool {
			return removed[agent.Metadata.UID]
		})
		store.registry.Panes = slices.DeleteFunc(store.registry.Panes, func(pane coremetadata.Pane) bool {
			return removed[pane.Metadata.OwnerUID()]
		})
		store.registry.NameReservations = slices.DeleteFunc(store.registry.NameReservations, func(reservation coremetadata.NameReservation) bool {
			return removed[reservation.UID] || removed[reservation.Scope]
		})
		project, _ := store.registry.Project("prj-beta")
		project.Spec.PrimaryWindowRef = ""
		out, _, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
		if err == nil || !strings.Contains(out, "no Registry Window topology") || len(server.sessions) != 0 {
			t.Fatalf("zero-Window Project created orphan topology: err=%v\n%s", err, out)
		}
	})
}

func TestRegistryTopologyMaterializationLiveUIDSafetyMatrix(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*fakeTmuxSession)
		want   string
	}{
		{
			name: "duplicate Pane uid",
			mutate: func(session *fakeTmuxSession) {
				duplicate := newFakeTmuxPane("%duplicate")
				duplicate.opts[tmuxopts.PaneUID] = session.windows[0].panes[0].opts[tmuxopts.PaneUID]
				session.windows[0].panes = append(session.windows[0].panes, duplicate)
			},
			want: "multiple live Panes claim",
		},
		{
			name: "wrong owner Pane uid",
			mutate: func(session *fakeTmuxSession) {
				session.windows[0].panes[0].opts[tmuxopts.PaneUID] = session.windows[1].panes[0].opts[tmuxopts.PaneUID]
			},
			want: "wrong owner",
		},
		{
			name: "foreign Window uid",
			mutate: func(session *fakeTmuxSession) {
				foreign := &fakeTmuxWindow{id: "@foreign", name: "foreign", opts: map[string]string{tmuxopts.WindowUID: "win-foreign"}}
				foreign.panes = []*fakeTmuxPane{newFakeTmuxPane("%foreign")}
				session.windows = append(session.windows, foreign)
			},
			want: "not owned by the selected Project",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			command, _, server, _, _, _ := newTopologyMaterializeFixture(t)
			if _, _, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json"); err != nil {
				t.Fatal(err)
			}
			test.mutate(server.session("beta"))
			before := server.state()
			out, _, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
			if err == nil || !strings.Contains(out, test.want) || server.state() != before {
				t.Fatalf("unsafe live state was not refused byte-stably: err=%v\n%s", err, out)
			}
		})
	}
}
