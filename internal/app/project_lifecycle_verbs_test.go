package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
)

// lifecycleVerbFixture wires one lifecycle command over the shared resource
// fixture and a scripted session executor.
//
// `live` names the sessions the fake tmux already has, which is the single
// input that decides between the materialized and already-live halves of every
// verb below.
func lifecycleVerbFixture(
	t *testing.T,
	verb projectLifecycleVerb,
	insideTmux bool,
	live map[string]bool,
) (*projectLifecycleCommand, *fakeResourceStore, *capturingSwitchSessionExecutor) {
	t.Helper()
	home := t.TempDir()
	store := newFakeResourceStore(t)
	executor := &capturingSwitchSessionExecutor{authorizeSet: true, authorizeResult: true, exists: live}
	lookupEnv := func(name string) string {
		if name == "TMUX" && insideTmux {
			return "/tmp/tmux-1000/projmux,1,0"
		}
		return ""
	}
	switcher := &switchCommand{
		sessions:        executor,
		identity:        stubSwitchIdentityResolver{name: "alpha"},
		tmuxRunner:      &recordingTmuxRunner{},
		homeDir:         func() (string, error) { return home, nil },
		lookupEnv:       lookupEnv,
		projectTopology: &fakeProjectTopologyMaterializer{materialized: true},
		projectRegistrar: &defaultSwitchProjectRegistrar{
			store: store.store(), shell: "/bin/zsh", sessionNameFor: func(string) string { return "alpha" },
		},
		startupNotices: &recordingProjectStartupReporter{},
	}
	return &projectLifecycleCommand{
		verb:      verb,
		store:     store.store(),
		switcher:  switcher,
		lookupEnv: lookupEnv,
	}, store, executor
}

// TestStartProjectMaterializesDetachedAndReportsAlreadyLive covers both runtime
// outcomes of the detached verb and the axis it must never touch.
//
// The focus assertion is the load-bearing one. `start` and `open` share one
// materializer, so the only thing that distinguishes them is whether the client
// handoff runs -- and a handoff that leaked into `start` would be invisible in
// the Registry and visible only as the operator's terminal jumping somewhere
// they did not ask to go.
func TestStartProjectMaterializesDetachedAndReportsAlreadyLive(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		live        map[string]bool
		wantRuntime cli.RuntimeEffect
		wantOpen    string
	}{
		{"offline", nil, cli.RuntimeMaterialized, ""},
		{"live", map[string]bool{"alpha": true}, cli.RuntimeAlreadyLive, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cmd, store, executor := lifecycleVerbFixture(t, projectLifecycleStart, false, test.live)
			stdout, _, err := runRoute(t, cmd, "project", "uid:prj-alpha")
			if err != nil {
				t.Fatalf("start project error = %v", err)
			}
			if !strings.Contains(stdout, "receipt operation=start.project") ||
				!strings.Contains(stdout, "runtime="+string(test.wantRuntime)) ||
				!strings.Contains(stdout, "focus=unchanged") {
				t.Fatalf("start project stdout = %q, want runtime=%s and focus=unchanged", stdout, test.wantRuntime)
			}
			if !strings.Contains(stdout, "identity=unchanged address=unchanged topology=unchanged desired-state=unchanged") {
				t.Fatalf("start project moved a desired-state axis: %q", stdout)
			}
			if !strings.Contains(stdout, "projects=1 windows=0 panes=0 agents=0") {
				t.Fatalf("start project cardinality = %q, want exactly one Project", stdout)
			}
			if executor.openSessionName != test.wantOpen {
				t.Fatalf("start project handed the client to %q, want no handoff", executor.openSessionName)
			}
			if store.writes != 0 {
				t.Fatalf("start project wrote the Registry %d times, want 0", store.writes)
			}
		})
	}
}

// TestOpenProjectMovesTheCurrentClientAndRefusesOutsideTmux is the pair of
// contracts that separate `open project` from `attach project`.
//
// Outside tmux the refusal has to happen before anything is materialized: a
// caller with no client cannot be moved, and starting a session on the way to
// discovering that would leave a runtime nobody asked for.
func TestOpenProjectMovesTheCurrentClientAndRefusesOutsideTmux(t *testing.T) {
	t.Parallel()

	t.Run("inside tmux with a live session", func(t *testing.T) {
		t.Parallel()
		cmd, _, executor := lifecycleVerbFixture(t, projectLifecycleOpen, true, map[string]bool{"alpha": true})
		stdout, _, err := runRoute(t, cmd, "project", "uid:prj-alpha")
		if err != nil {
			t.Fatalf("open project error = %v", err)
		}
		if !strings.Contains(stdout, "receipt operation=open.project") ||
			!strings.Contains(stdout, "runtime=already-live focus=moved-current-client") {
			t.Fatalf("open project stdout = %q", stdout)
		}
		if executor.openSessionName != "alpha" {
			t.Fatalf("open project client handoff = %q, want alpha", executor.openSessionName)
		}
	})

	t.Run("inside tmux with an offline session", func(t *testing.T) {
		t.Parallel()
		cmd, _, executor := lifecycleVerbFixture(t, projectLifecycleOpen, true, nil)
		stdout, _, err := runRoute(t, cmd, "project", "uid:prj-alpha")
		if err != nil {
			t.Fatalf("open project error = %v", err)
		}
		if !strings.Contains(stdout, "runtime=materialized focus=moved-current-client") {
			t.Fatalf("open project stdout = %q", stdout)
		}
		if executor.openSessionName != "alpha" {
			t.Fatalf("open project client handoff = %q, want alpha", executor.openSessionName)
		}
	})

	t.Run("outside tmux refuses before materializing", func(t *testing.T) {
		t.Parallel()
		cmd, store, executor := lifecycleVerbFixture(t, projectLifecycleOpen, false, nil)
		stdout, _, err := runRoute(t, cmd, "project", "uid:prj-alpha")
		if err == nil || !strings.Contains(err.Error(), "projmux attach project alpha") {
			t.Fatalf("open project outside tmux error = %v, want the attach guidance", err)
		}
		if exitCodeOf(err) != 2 {
			t.Fatalf("open project outside tmux exit = %d, want 2", exitCodeOf(err))
		}
		if stdout != "" {
			t.Fatalf("a refused open wrote a result: %q", stdout)
		}
		if store.writes != 0 || executor.ensureSessionName != "" || executor.openSessionName != "" {
			t.Fatalf("a refused open touched state: writes=%d executor=%#v", store.writes, executor)
		}
	})
}

// TestStopProjectEndsOnlyTheRuntimeAndRefusesAnOfflineTarget pins the verb that
// is the inverse of `start`.
//
// The offline refusal exists because `stop project` declares exactly one
// runtime outcome. A no-op success would have to report `runtime=stopped` for a
// session that was never running, which is the kind of receipt this whole Phase
// exists to prevent.
func TestStopProjectEndsOnlyTheRuntimeAndRefusesAnOfflineTarget(t *testing.T) {
	t.Parallel()

	t.Run("offline target is a zero-write refusal", func(t *testing.T) {
		t.Parallel()
		cmd, store, executor := lifecycleVerbFixture(t, projectLifecycleStop, false, nil)
		stdout, _, err := runRoute(t, cmd, "project", "uid:prj-alpha")
		if err == nil || !strings.Contains(err.Error(), "no live persistent session") {
			t.Fatalf("stop project offline error = %v, want the no-runtime refusal", err)
		}
		if stdout != "" || store.writes != 0 || executor.killSessionName != "" {
			t.Fatalf("a refused stop changed state: stdout=%q writes=%d killed=%q",
				stdout, store.writes, executor.killSessionName)
		}
	})

	// The live half reuses the sidebar popup stop fixture, which is the one
	// place a managed Project row, its exact live session, and the app-owned
	// route are wired together. That is deliberate: `stop project` must go
	// through the same exact-UID/session containment check the sidebar does, not
	// a second kill path that only the CLI can reach.
	t.Run("live target kills exactly one session and keeps the Registry graph", func(t *testing.T) {
		t.Parallel()
		const (
			socketPath = "/tmp/fake-tmux/primary"
			serverPID  = "4242"
			projectDir = "/src/alpha"
		)
		const anchorPane = "%9"
		switcher, stop, executor := sidebarPopupStopFixture(t, projectDir, socketPath, serverPID, anchorPane, nil)
		// The sidebar reaches this route from a popup, which has no TMUX_PANE and
		// therefore carries its anchor as an explicit operand. An ordinary CLI
		// invocation is the other shape: tmux exports the Pane, and the route
		// resolves its authority from the environment it was called in.
		popupEnv := switcher.lookupEnv
		switcher.lookupEnv = func(name string) string {
			if name == "TMUX_PANE" {
				return anchorPane
			}
			return popupEnv(name)
		}
		sessionID := bindSidebarPopupManagedRow(t, switcher, stop, projectDir)
		sessionName, err := switcher.resolveTargetSession(projectDir)
		if err != nil {
			t.Fatalf("resolve fixture session: %v", err)
		}
		executor.exists[sessionName] = true
		store := &fakeResourceStore{
			registry: runtimeFixtureRegistry(),
			dirs:     map[string]bool{projectDir: true},
			now:      resourceFixtureClock,
		}
		cmd := &projectLifecycleCommand{
			verb: projectLifecycleStop, store: store.store(), switcher: switcher,
			lookupEnv: func(string) string { return "" },
		}
		before := len(store.registry.Projects)

		stdout, _, runErr := runRoute(t, cmd, "project", "uid:"+runtimeFixtureProject)
		if runErr != nil {
			t.Fatalf("stop project error = %v", runErr)
		}
		if !stop.killed || stop.killTarget != sessionID {
			t.Fatalf("stop project killed=%t target=%q, want one exact kill-session -t %s",
				stop.killed, stop.killTarget, sessionID)
		}
		if got := stop.writes(); got != 1 {
			t.Fatalf("stop project tmux writes = %d, want exactly one: %#v", got, stop.calls)
		}
		if !strings.Contains(stdout, "receipt operation=stop.project") ||
			!strings.Contains(stdout, "runtime=stopped focus=unchanged") {
			t.Fatalf("stop project stdout = %q", stdout)
		}
		if !strings.Contains(stdout, "identity=unchanged address=unchanged topology=unchanged desired-state=unchanged") {
			t.Fatalf("stop project removed a Registry axis: %q", stdout)
		}
		if store.writes != 0 || len(store.registry.Projects) != before {
			t.Fatalf("stop project changed the Registry: writes=%d projects=%d want %d",
				store.writes, len(store.registry.Projects), before)
		}
	})
}

// TestProjectLifecycleReceiptProjectionIsVersionedJSON pins the machine half of
// the result, including the empty collections a parser can rely on being
// present rather than absent.
func TestProjectLifecycleReceiptProjectionIsVersionedJSON(t *testing.T) {
	t.Parallel()

	cmd, _, _ := lifecycleVerbFixture(t, projectLifecycleStart, false, map[string]bool{"alpha": true})
	stdout, _, err := runRoute(t, cmd, "project", "uid:prj-alpha", "-o", "receipt")
	if err != nil {
		t.Fatalf("start project -o receipt error = %v", err)
	}
	var receipt cli.OperationReceipt
	if err := json.Unmarshal([]byte(stdout), &receipt); err != nil {
		t.Fatalf("decode receipt %q: %v", stdout, err)
	}
	if receipt.APIVersion != cli.ReceiptAPIVersion || receipt.Operation != cli.OperationStartProject {
		t.Fatalf("receipt envelope = %+v", receipt)
	}
	if receipt.Target != (cli.ReceiptTarget{Kind: "Project", UID: "prj-alpha", Name: "alpha"}) {
		t.Fatalf("receipt target = %+v", receipt.Target)
	}
	if receipt.Effects.Runtime != cli.RuntimeAlreadyLive || receipt.Effects.Focus != cli.FocusUnchanged {
		t.Fatalf("receipt effects = %+v", receipt.Effects)
	}
	if receipt.Cardinality != (cli.ReceiptCardinality{Projects: 1}) {
		t.Fatalf("receipt cardinality = %+v", receipt.Cardinality)
	}
	if len(receipt.AffectedUIDs) != 1 || receipt.AffectedUIDs[0].Action != cli.ActionAlreadyLive {
		t.Fatalf("receipt affected = %+v", receipt.AffectedUIDs)
	}
	if receipt.SelectedWindowUIDs == nil || receipt.CompatibilityWarnings == nil {
		t.Fatalf("receipt collections must encode as empty arrays, not null: %q", stdout)
	}
	if receipt.DomainEffect != nil {
		t.Fatalf("a resource-only route declared a domain effect: %+v", receipt.DomainEffect)
	}
	if err := receipt.Validate(); err != nil {
		t.Fatalf("the emitted receipt is not allowed by its own route: %v", err)
	}
}

// TestProjectLifecycleVerbsRefuseUnknownKindsAndAmbiguousRefs keeps the new
// verbs inside the exact-one Project cell they declare.
func TestProjectLifecycleVerbsRefuseUnknownKindsAndAmbiguousRefs(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{"unknown kind", []string{"window", "main"}, "this release implements: project"},
		{"no kind", nil, "requires a resource kind"},
		{"two refs", []string{"project", "alpha", "beta"}, "exactly one Project reference"},
		{"unknown project", []string{"project", "nosuch"}, "nosuch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cmd, store, executor := lifecycleVerbFixture(t, projectLifecycleStart, false, nil)
			stdout, _, err := runRoute(t, cmd, test.args...)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("start %v error = %v, want %q", test.args, err, test.want)
			}
			if stdout != "" || store.writes != 0 || executor.ensureSessionName != "" {
				t.Fatalf("a refused lifecycle verb touched state: stdout=%q writes=%d", stdout, store.writes)
			}
		})
	}
}

// TestProjectLifecycleAcceptsAnAbsoluteRootBesideTheSelectorGrammar keeps the
// path spelling the sidebar and the shell already use.
func TestProjectLifecycleAcceptsAnAbsoluteRootBesideTheSelectorGrammar(t *testing.T) {
	t.Parallel()

	cmd, _, _ := lifecycleVerbFixture(t, projectLifecycleStart, false, map[string]bool{"alpha": true})
	stdout, _, err := runRoute(t, cmd, "project", "/srv/alpha")
	if err != nil {
		t.Fatalf("start project /srv/alpha error = %v", err)
	}
	if !strings.Contains(stdout, "receipt operation=start.project") {
		t.Fatalf("absolute-root start stdout = %q", stdout)
	}

	unclaimed, _, _ := lifecycleVerbFixture(t, projectLifecycleStart, false, nil)
	if _, _, err := runRoute(t, unclaimed, "project", "/srv/unclaimed"); err == nil ||
		!strings.Contains(err.Error(), "no Project claims root") {
		t.Fatalf("unclaimed root error = %v, want the registration guidance", err)
	}
}

// TestProjectLifecycleQuietModeWritesNothing pins the explicit automation mode.
func TestProjectLifecycleQuietModeWritesNothing(t *testing.T) {
	t.Parallel()

	cmd, _, _ := lifecycleVerbFixture(t, projectLifecycleStart, false, map[string]bool{"alpha": true})
	stdout, stderr, err := runRoute(t, cmd, "project", "uid:prj-alpha", "-o", "none")
	if err != nil || stdout != "" || stderr != "" {
		t.Fatalf("-o none = stdout %q stderr %q err %v", stdout, stderr, err)
	}
	if _, _, err := runRoute(t, cmd, "project", "uid:prj-alpha", "-o", "json"); err == nil ||
		!strings.Contains(err.Error(), "invalid --output") {
		t.Fatalf("-o json error = %v, want the catalog refusal", err)
	}
}

// TestProjectLifecycleMaterializerSharesTheStartupTransaction proves the two
// materializing verbs go through the one Project startup path rather than a
// second session-creation route of their own.
func TestProjectLifecycleMaterializerSharesTheStartupTransaction(t *testing.T) {
	t.Parallel()

	cmd, _, executor := lifecycleVerbFixture(t, projectLifecycleStart, false, nil)
	if err := cmd.materialize(context.Background(), "/srv/alpha", "alpha", true); err != nil {
		t.Fatalf("materialize error = %v", err)
	}
	if !executor.authorizeCalled {
		t.Fatal("the lifecycle materializer skipped the Project trust gate")
	}
	if executor.openSessionName != "" {
		t.Fatal("the detached materializer performed a client handoff")
	}
}
