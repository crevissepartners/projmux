package app

import (
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// aliveAlphaRuntime seeds the in-memory tmux server with the Project this
// Phase's implicit scope derives: session `alpha` owning window
// `win-alpha-main`, whose primary Pane is `pan-alpha-zsh`.
func aliveAlphaRuntime(t *testing.T) (*fakeResourceStore, *fakeTmux) {
	t.Helper()
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	session := tmux.addSession("alpha")
	seedOwnedSession(session, "prj-alpha", "/srv/alpha")
	seedLiveWindow(t, tmux, session, "win-alpha-main", "pan-alpha-zsh")
	return store, tmux
}

// withActiveTarget attaches an observation to an already-wired create command.
func withActiveTarget(create *createCommand, active *recordedActiveTarget) *createCommand {
	create.activeTarget = active.lookup
	return create
}

// TestCreateAgentExplicitMissingWindowWithoutProjectRefusesBeforeWrite pins the
// explicit-owner boundary. A missing --window with --create-window cannot
// reveal its Project owner, and the command must not borrow that owner from the
// active Pane. The actionable remedy is an exact --project.
func TestCreateAgentExplicitMissingWindowWithoutProjectRefusesBeforeWrite(t *testing.T) {
	t.Parallel()

	store, tmux := aliveAlphaRuntime(t)
	create, _ := newTestAgentCreateCommand(t, store, tmux)
	active := insideTmux("pan-alpha-zsh", "win-alpha-main")
	withActiveTarget(create, active)
	registryBefore, tmuxBefore := store.snapshot(), tmux.state()

	stdout, stderr, err := runRoute(t, create, "codex", "-w", "hi", "--create-window", "-o", "pane-id")
	if err == nil || !IsUsageError(err) || !strings.Contains(err.Error(), "pass --project <ref>") {
		t.Fatalf("create codex -w hi --create-window = stdout=%q stderr=%q err=%v, want actionable usage refusal", stdout, stderr, err)
	}
	if stdout != "" || stderr != "" {
		t.Fatalf("refusal emitted stdout=%q stderr=%q", stdout, stderr)
	}
	if active.calls != 0 {
		t.Fatalf("explicit missing Window consulted ambient target %d times", active.calls)
	}
	if store.transactions != 0 || store.writes != 0 || store.snapshot() != registryBefore {
		t.Fatalf("refusal changed Registry: transactions=%d writes=%d changed=%t", store.transactions, store.writes, store.snapshot() != registryBefore)
	}
	if writes := tmuxMutationCallCount(tmux); writes != 0 || tmux.state() != tmuxBefore {
		t.Fatalf("refusal changed tmux: writes=%d changed=%t calls=%#v", writes, tmux.state() != tmuxBefore, tmux.calls)
	}
}

// TestCreatePaneWithNoScopeAtAllSplitsTheActiveWindow is the generated
// keybinding body's contract.
//
// `create pane --placement right` names no scope, so the whole scope -- Project,
// Window and anchor Pane -- comes from the active runtime. The distinguishing
// property is what it does *not* do: the Project owns a second Window, and an
// implicit Project alone would have fanned out across both.
func TestCreatePaneWithNoScopeAtAllSplitsTheActiveWindow(t *testing.T) {
	t.Parallel()

	store, tmux := aliveAlphaRuntime(t)
	create, _ := newTestResourceCreateCommand(t, store, tmux)
	withActiveTarget(create, insideTmux("pan-alpha-zsh", "win-alpha-main"))

	before := paneUIDsByWindow(store)
	if _, _, err := runRoute(t, create, "pane", "--placement", "right"); err != nil {
		t.Fatalf("create pane error = %v", err)
	}
	added := addedPaneUIDs(before, paneUIDsByWindow(store))
	if len(added["win-alpha-main"]) != 1 {
		t.Fatalf("the active Window gained %v, want exactly one Pane", added["win-alpha-main"])
	}
	if len(added["win-alpha-review"]) != 0 {
		t.Fatalf("the Project's other Window gained %v; an implicit Project alone fanned out", added["win-alpha-review"])
	}
	assertNoClientMovement(t, tmux)
}

// TestExplicitScopeSuppressesTheImplicitAnchor pins the boundary of the rule
// above: one explicit scope occurrence turns the whole scope explicit.
//
// The invocation runs inside window `main` but addresses `review`. The anchor
// must be `review`'s own compatibility shell ref, never the pane the operator happens to
// be sitting in, because splitting somewhere the invocation never addressed is
// the failure mode the anchor contract exists to prevent.
func TestExplicitScopeSuppressesTheImplicitAnchor(t *testing.T) {
	t.Parallel()

	store, tmux := aliveAlphaRuntime(t)
	review := seedLiveWindow(t, tmux, tmux.session("alpha"), "win-alpha-review", "pan-alpha-review")
	create, _ := newTestResourceCreateCommand(t, store, tmux)
	withActiveTarget(create, insideTmux("pan-alpha-zsh", "win-alpha-main"))

	before := paneUIDsByWindow(store)
	if _, _, err := runRoute(t, create, "pane", "-w", "review"); err != nil {
		t.Fatalf("create pane -w review error = %v", err)
	}
	if got := len(review.panes); got != 2 {
		t.Fatalf("the addressed Window holds %d live panes, want 2", got)
	}
	added := addedPaneUIDs(before, paneUIDsByWindow(store))
	if len(added["win-alpha-review"]) != 1 {
		t.Fatalf("the addressed Window gained %v, want exactly one Pane", added["win-alpha-review"])
	}
	if len(added["win-alpha-main"]) != 0 {
		t.Fatalf("the active Window gained %v even though --window addressed another one", added["win-alpha-main"])
	}
}

// TestExplicitProjectWinsOverTheActiveRuntime is acceptance criterion 2.
//
// An explicit `--project` is authoritative inside tmux exactly as it is outside
// it, and the active target is never consulted at all: a scope the operator
// typed cannot be narrowed, widened, or second-guessed by where they typed it.
func TestExplicitProjectWinsOverTheActiveRuntime(t *testing.T) {
	t.Parallel()

	store, tmux := aliveAlphaRuntime(t)
	create, _ := newTestResourceCreateCommand(t, store, tmux)
	active := insideTmux("pan-alpha-zsh", "win-alpha-main")
	withActiveTarget(create, active)

	before := paneUIDsByWindow(store)
	if _, _, err := runRoute(t, create, "pane", "--project", "beta", "--window", "main"); err != nil {
		t.Fatalf("create pane --project beta error = %v", err)
	}
	if active.calls != 0 {
		t.Fatalf("an explicit --project still observed the active tmux target %d times", active.calls)
	}
	added := addedPaneUIDs(before, paneUIDsByWindow(store))
	if len(added["win-beta-main"]) != 1 {
		t.Fatalf("win-beta-main gained %v, want exactly one Pane; registry:\n%s", added["win-beta-main"], store.snapshot())
	}
	if len(added["win-alpha-main"]) != 0 {
		t.Fatalf("the active Window gained %v even though --project addressed beta", added["win-alpha-main"])
	}
}

// TestImplicitScopeRefusesEveryUnmanagedRuntime is acceptance criterion 3.
//
// Home, a control session, an unattributed pane, a foreign pane, and every
// invocation from outside tmux all resolve no managed Project. Each one refuses
// with a usage error that names `--project` as the fix, and each one is required
// to leave the Registry byte-identical and the tmux server untouched: there is
// no runtime-only split left to fall back to, and inventing a Project from a
// session name or a cwd is exactly what the Registry-first contract forbids.
func TestImplicitScopeRefusesEveryUnmanagedRuntime(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		active *recordedActiveTarget
		want   string
	}{
		{
			name:   "outside tmux there is no runtime to derive from",
			active: outsideTmux(),
			want:   "this invocation is not inside a tmux client",
		},
		{
			name:   "the Home control runtime carries no managed identity",
			active: insideTmux("", ""),
			want:   "carries no " + tmuxopts.WindowUID,
		},
		{
			name:   "an unattributed pane is not adopted by proximity",
			active: insideTmux("pan-alpha-zsh", ""),
			want:   "carries no " + tmuxopts.WindowUID,
		},
		{
			name:   "a foreign uid is not imported",
			active: insideTmux("pan-foreign", "win-foreign"),
			want:   "which is not in the registry",
		},
	} {
		for _, args := range [][]string{
			{"window"},
			{"pane"},
			{"agent", "--provider", "codex"},
			{"codex", "-w", "hi", "--create-window"},
		} {
			t.Run(test.name+"/"+strings.Join(args, " "), func(t *testing.T) {
				t.Parallel()
				store, tmux := aliveAlphaRuntime(t)
				create, _ := newTestAgentCreateCommand(t, store, tmux)
				withActiveTarget(create, test.active)
				before := store.snapshot()
				callsBefore := len(tmux.calls)

				stdout, _, err := runRoute(t, create, args...)
				if err == nil {
					t.Fatalf("create %v succeeded on an unmanaged runtime", args)
				}
				if !IsUsageError(err) {
					t.Fatalf("create %v error is not a usage error: %v", args, err)
				}
				wants := []string{"--project", test.want}
				if args[0] == "codex" {
					wants = []string{"--project", "ambient TMUX/TMUX_PANE is not target authority"}
				}
				for _, want := range wants {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("create %v error = %q, want it to mention %q", args, err, want)
					}
				}
				if stdout != "" {
					t.Fatalf("create %v wrote %q to stdout", args, stdout)
				}
				if store.transactions != 0 || store.writes != 0 {
					t.Fatalf("create %v opened %d transactions and wrote %d times", args, store.transactions, store.writes)
				}
				if got := store.snapshot(); got != before {
					t.Fatalf("create %v mutated the Registry:\n%s", args, got)
				}
				if got := len(tmux.calls) - callsBefore; got != 0 {
					t.Fatalf("create %v issued %d tmux calls, want 0", args, got)
				}
			})
		}
	}
}

// TestImplicitScopeRefusesAWindowWithNoOwningProject covers the last unmanaged
// shape: a Window uid that is in the Registry but whose owner chain is broken.
// It is a refusal rather than a repair, because a create that invented an owner
// would write the very drift the Registry is the source of truth for.
func TestImplicitScopeRefusesAWindowWithNoOwningProject(t *testing.T) {
	t.Parallel()

	store, tmux := aliveAlphaRuntime(t)
	orphan := coremetadata.Window{
		APIVersion: coremetadata.APIVersion,
		Kind:       coremetadata.KindWindow,
		Metadata: coremetadata.ObjectMeta{
			UID: "win-orphan", Name: "orphan",
			OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindProject, UID: "prj-vanished"},
		},
	}
	store.registry.Windows = append(store.registry.Windows, orphan)
	create, _ := newTestResourceCreateCommand(t, store, tmux)
	withActiveTarget(create, insideTmux("", "win-orphan"))

	_, _, err := runRoute(t, create, "pane")
	if err == nil || !IsUsageError(err) {
		t.Fatalf("error = %v, want a usage error", err)
	}
	for _, want := range []string{"--project", "has no owning Project in the registry"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to mention %q", err, want)
		}
	}
	if store.writes != 0 {
		t.Fatalf("the refusal wrote to the Registry %d times", store.writes)
	}
}

// TestImplicitScopeRollsBackTheWholeTransaction proves the derived scope reaches
// the same transaction guarantees an explicit one does: a runtime failure after
// the metadata phase leaves the Registry byte-identical and removes every tmux
// object the operation created.
func TestImplicitScopeRollsBackTheWholeTransaction(t *testing.T) {
	t.Parallel()

	store, tmux := aliveAlphaRuntime(t)
	create, _ := newTestResourceCreateCommand(t, store, tmux)
	withActiveTarget(create, insideTmux("pan-alpha-zsh", "win-alpha-main"))
	before := store.snapshot()
	windowsBefore := tmux.windowCount()
	tmux.fail = []string{"split-window"}

	if _, _, err := runRoute(t, create, "pane", "-w", "hi", "--create-window"); err == nil {
		t.Fatal("a failing split-window reported success")
	}
	if store.writes != 0 {
		t.Fatalf("a rolled-back create wrote to the Registry %d times", store.writes)
	}
	if got := store.snapshot(); got != before {
		t.Fatalf("the Registry changed:\n%s", got)
	}
	if got := tmux.windowCount(); got != windowsBefore {
		t.Fatalf("tmux windows = %d, want %d; the ledger left an object behind", got, windowsBefore)
	}
}

// windowNamed resolves one Window of a Project by metadata.name.
func windowNamed(t *testing.T, store *fakeResourceStore, projectUID, name string) coremetadata.Window {
	t.Helper()
	for _, window := range store.registry.Windows {
		if window.Metadata.OwnerUID() == projectUID && window.Metadata.Name == name {
			return window
		}
	}
	t.Fatalf("no Window named %q in project %q; registry:\n%s", name, projectUID, store.snapshot())
	return coremetadata.Window{}
}

// paneUIDsByWindow projects the Registry's Panes onto their owning Window.
//
// Comparing before/after sets rather than counts is deliberate: reconciliation
// legitimately releases an Agent Pane with no live binding during the same
// transaction, so a total count would move for reasons that have nothing to do
// with where the create landed.
func paneUIDsByWindow(store *fakeResourceStore) map[string][]string {
	agentWindow := map[string]string{}
	for _, agent := range store.registry.Agents {
		agentWindow[agent.Metadata.UID] = agent.Metadata.OwnerUID()
	}
	out := map[string][]string{}
	for _, pane := range store.registry.Panes {
		// A managed Agent Pane is owned by its Agent, so resolve one more hop
		// to the Window every caller here actually asks about.
		owner := pane.Metadata.OwnerUID()
		if window, ok := agentWindow[owner]; ok {
			owner = window
		}
		out[owner] = append(out[owner], pane.Metadata.UID)
	}
	return out
}

// addedPaneUIDs returns the Pane uids each Window gained between two snapshots.
func addedPaneUIDs(before, after map[string][]string) map[string][]string {
	seen := map[string]bool{}
	for _, uids := range before {
		for _, uid := range uids {
			seen[uid] = true
		}
	}
	out := map[string][]string{}
	for owner, uids := range after {
		for _, uid := range uids {
			if !seen[uid] {
				out[owner] = append(out[owner], uid)
			}
		}
	}
	return out
}

// livePaneWithUID returns the tmux pane id carrying a mirrored Pane uid.
func livePaneWithUID(t *testing.T, tmux *fakeTmux, uid string) string {
	t.Helper()
	for _, session := range tmux.sessions {
		for _, window := range session.windows {
			for _, pane := range window.panes {
				if pane.opts[tmuxopts.PaneUID] == uid {
					return pane.id
				}
			}
		}
	}
	t.Fatalf("no live tmux pane mirrors uid %q; tmux:\n%s", uid, tmux.state())
	return ""
}

// liveWindowWithUID returns the session name and tmux window id carrying a
// mirrored Window uid.
func liveWindowWithUID(t *testing.T, tmux *fakeTmux, uid string) (string, string) {
	t.Helper()
	for _, session := range tmux.sessions {
		for _, window := range session.windows {
			if window.opts[tmuxopts.WindowUID] == uid {
				return session.name, window.id
			}
		}
	}
	return "", ""
}

// TestGeneratedKeyBindingsCarryTheExactPaneIntoCreate is the generated-producer
// half of the resource-first create.
//
// A key binding renders as `run-shell "<bin> create codex --placement right"`.
// tmux exports $TMUX to that child but never $TMUX_PANE -- it sets that variable
// only in the shell it spawns for a pane -- so without the exact pane the
// binding would land in "inside tmux, no active target" and refuse. Every
// run-projmux binding therefore carries `#{pane_id}`, which run-shell resolves
// against the pane the key was pressed in.
func TestGeneratedKeyBindingsCarryTheExactPaneIntoCreate(t *testing.T) {
	t.Parallel()

	var runProjmux int
	for _, action := range defaultKeyBindingCatalog() {
		if action.TmuxKind != tmuxBindingRunProjmux {
			continue
		}
		runProjmux++
		body := renderTmuxBindingBody("/usr/local/bin/projmux", action)
		want := `run-shell "TMUX_PANE=#{pane_id} PROJMUX_POPUP_TARGET_CLIENT=#{client_tty} '/usr/local/bin/projmux' ` + action.TmuxBody + `"`
		if body != want {
			t.Fatalf("action %s rendered\n  %s\nwant\n  %s", action.ID, body, want)
		}
	}
	if runProjmux == 0 {
		t.Fatal("no run-projmux bindings in the catalog; the assertion above proves nothing")
	}

	// The three direct split bindings are the ones whose meaning depends on it.
	creates := map[string]string{
		"ai-split-codex-right": "internal agent-pane launch-provider codex right",
		"ai-split-claude-down": "internal agent-pane launch-provider claude down",
		"ai-split-shell-right": "internal agent-pane launch-shell right",
	}
	for _, action := range defaultKeyBindingCatalog() {
		want, ok := creates[action.ID]
		if !ok {
			continue
		}
		if action.TmuxBody != want {
			t.Fatalf("binding %s runs %q, want the canonical intent producer %q", action.ID, action.TmuxBody, want)
		}
		delete(creates, action.ID)
	}
	if len(creates) != 0 {
		t.Fatalf("create bindings disappeared from the catalog: %v", creates)
	}
}

// TestNoScopeCreateIsTheKeyBindingBody closes the loop between the rendered
// binding and the route: the argv the generated config runs is exactly the argv
// the implicit-scope route accepts.
func TestNoScopeCreateIsTheKeyBindingBody(t *testing.T) {
	t.Parallel()

	for _, action := range defaultKeyBindingCatalog() {
		if action.TmuxKind != tmuxBindingRunProjmux || !strings.HasPrefix(action.TmuxBody, "create ") {
			continue
		}
		argv := strings.Fields(strings.TrimPrefix(action.TmuxBody, "create "))
		t.Run(action.ID, func(t *testing.T) {
			t.Parallel()
			store, tmux := aliveAlphaRuntime(t)
			create, _ := newTestAgentCreateCommand(t, store, tmux)
			withActiveTarget(create, insideTmux("pan-alpha-zsh", "win-alpha-main"))
			before := paneUIDsByWindow(store)
			if _, _, err := runRoute(t, create, argv...); err != nil {
				t.Fatalf("create %v error = %v", argv, err)
			}
			added := addedPaneUIDs(before, paneUIDsByWindow(store))
			if len(added["win-alpha-main"]) != 1 {
				t.Fatalf("the active Window gained %v, want exactly one Pane", added["win-alpha-main"])
			}
			if len(added["win-alpha-review"]) != 0 {
				t.Fatalf("the binding fanned out to the Project's other Window: %v", added["win-alpha-review"])
			}
			assertNoClientMovement(t, tmux)
		})
	}
}
