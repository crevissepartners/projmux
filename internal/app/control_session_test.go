package app

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// controlFixtureSocket is deliberately not the app socket's default name. A pass
// that fell back to `-L projmux` would be visible immediately.
const controlFixtureSocket = "control-isolated"

// controlSessionFixture builds one app-owned server carrying a live Home session
// plus an empty registry.
func controlSessionFixture(t *testing.T) (*controlSessionConverger, *fakeResourceStore, *fakeTmux, *routedTmuxRunner) {
	t.Helper()
	server := newFakeTmux()
	server.addSession("home")
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-L\x00" + controlFixtureSocket: server}}
	store := &fakeResourceStore{
		registry: coremetadata.NewRegistry(),
		dirs:     map[string]bool{},
		now:      time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC),
	}
	converger := &controlSessionConverger{
		runner:         runner,
		resources:      store.store(),
		shell:          "/bin/zsh",
		newOperationID: func() (string, error) { return "op-control", nil },
	}
	return converger, store, server, runner
}

// controlClaimBetweenPlansRunner installs a foreign Project claim immediately
// before the second app-marker read. The first read is preflight; the second
// must happen inside the Registry transaction before its plan can authorize a
// bind or any tmux mirror write.
type controlClaimBetweenPlansRunner struct {
	delegate    tmuxCommandRunner
	server      *fakeTmux
	markerReads int
}

func (r *controlClaimBetweenPlansRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == "tmux" && slices.Contains(args, tmuxopts.AppGlobal) {
		r.markerReads++
		if r.markerReads == 2 {
			r.server.mu.Lock()
			r.server.session("home").opts[tmuxopts.ProjectUIDSession] = "project-race"
			r.server.mu.Unlock()
		}
	}
	return r.delegate.Run(ctx, name, args...)
}

func TestControlSessionConvergeBackfillsALiveHome(t *testing.T) {
	converger, store, server, runner := controlSessionFixture(t)

	result, err := converger.converge(context.Background(), controlFixtureSocket, "home")
	if err != nil {
		t.Fatalf("converge() error = %v", err)
	}
	if result.skipped != "" {
		t.Fatalf("converge() skipped = %q, want a run", result.skipped)
	}
	if !result.changed {
		t.Fatal("converge() reported no registry change on a first pass")
	}

	session := server.session("home")
	if got, want := session.opts[tmuxopts.SessionRole], resourcegraph.ControlSessionRole; got != want {
		t.Fatalf("%s = %q, want %q", tmuxopts.SessionRole, got, want)
	}

	// The Registry now owns the Home Window and Pane, and the tmux side mirrors
	// exactly those uids -- which is what makes an owner chain derivable from
	// pane %N at all.
	if got, want := len(store.registry.ControlSessions), 1; got != want {
		t.Fatalf("len(ControlSessions) = %d, want %d", got, want)
	}
	control := store.registry.ControlSessions[0]
	if control.Spec.Session != "home" {
		t.Fatalf("control session spec.session = %q, want %q", control.Spec.Session, "home")
	}
	if result.controlUID != control.Metadata.UID {
		t.Fatalf("converge() controlUID = %q, want %q", result.controlUID, control.Metadata.UID)
	}
	if got, want := len(store.registry.Windows), 1; got != want {
		t.Fatalf("len(Windows) = %d, want %d", got, want)
	}
	window := store.registry.Windows[0]
	if owner := window.Metadata.OwnerRef; owner == nil || owner.Kind != coremetadata.KindControlSession || owner.UID != control.Metadata.UID {
		t.Fatalf("window ownerRef = %+v, want the control session", owner)
	}
	liveWindow := session.windows[0]
	if got := liveWindow.opts[tmuxopts.WindowUID]; got != window.Metadata.UID {
		t.Fatalf("%s = %q, want %q", tmuxopts.WindowUID, got, window.Metadata.UID)
	}
	if got := liveWindow.opts[tmuxopts.WindowName]; got != window.Metadata.Name {
		t.Fatalf("%s = %q, want %q", tmuxopts.WindowName, got, window.Metadata.Name)
	}
	if got, want := len(store.registry.Panes), 1; got != want {
		t.Fatalf("len(Panes) = %d, want %d", got, want)
	}
	pane := store.registry.Panes[0]
	if got := liveWindow.panes[0].opts[tmuxopts.PaneUID]; got != pane.Metadata.UID {
		t.Fatalf("%s = %q, want %q", tmuxopts.PaneUID, got, pane.Metadata.UID)
	}
	if got := liveWindow.panes[0].opts[tmuxopts.PaneName]; got != pane.Metadata.Name {
		t.Fatalf("%s = %q, want %q", tmuxopts.PaneName, got, pane.Metadata.Name)
	}

	// $HOME is nowhere: no Project was registered and no root was invented.
	if len(store.registry.Projects) != 0 {
		t.Fatalf("converge registered %d Projects; the control session owns no path", len(store.registry.Projects))
	}

	// Every tmux call was pinned to the one explicit socket the invocation named.
	if len(runner.calls) == 0 {
		t.Fatal("converge issued no tmux calls")
	}
	for _, call := range runner.calls {
		if call.flag == "-L" && call.value == controlFixtureSocket {
			continue
		}
		if call.flag != "-S" || call.value != server.socketPath {
			t.Fatalf("tmux call routed to %s/%s, want declared -L/%s or bound -S/%s: %v", call.flag, call.value, controlFixtureSocket, server.socketPath, call.args)
		}
	}
	// The role write names one exact session target: there is no `-g` and no
	// pattern anywhere, so no unrelated session can be mutated in bulk.
	roleWrites := 0
	for _, call := range runner.calls {
		if call.args[0] != "set-option" || !slices.Contains(call.args, tmuxopts.SessionRole) {
			continue
		}
		roleWrites++
		if slices.Contains(call.args, "-g") {
			t.Fatalf("the role write used -g: %v", call.args)
		}
		if i := slices.Index(call.args, "-t"); i < 0 || i+1 >= len(call.args) || call.args[i+1] != session.id {
			t.Fatalf("the role write did not name exact -t %s: %v", session.id, call.args)
		}
	}
	if roleWrites != 1 {
		t.Fatalf("the role marker was written %d times, want exactly 1: %#v", roleWrites, runner.calls)
	}
}

func TestControlSessionIdentityPlanNormalizesAutomaticRenameFormatBoolean(t *testing.T) {
	converger, _, server, runner := controlSessionFixture(t)
	// Real tmux format expansion renders automatic-rename as 0/1 even though
	// set-option and show-options use off/on. Exercise that exact observation
	// boundary instead of letting the fake's stored spelling hide a residual.
	converger.runner = controllerBooleanOptionRunner{base: runner}

	result, err := converger.converge(context.Background(), controlFixtureSocket, "home")
	if err != nil {
		t.Fatalf("converge with canonical tmux boolean format: %v", err)
	}
	if !result.changed {
		t.Fatal("first ControlSession convergence reported no change")
	}
	if got := server.session("home").windows[0].opts[tmuxopts.AutomaticRenameWindow]; got != "off" {
		t.Fatalf("automatic-rename = %q, want off", got)
	}

	runner.calls = nil
	repeat, err := converger.converge(context.Background(), controlFixtureSocket, "home")
	if err != nil {
		t.Fatalf("repeat converge with canonical tmux boolean format: %v", err)
	}
	if repeat.changed {
		t.Fatal("repeat ControlSession convergence reported a change")
	}
	for _, call := range runner.calls {
		argv := tmuxCommandArgv(call.args)
		if len(argv) > 0 && (argv[0] == "set-option" || argv[0] == "rename-window") {
			t.Fatalf("repeat ControlSession convergence executed a runtime write: %#v", call)
		}
	}
}

func TestControlSessionIdentityPlanFlattensEquivalentNestedRoutes(t *testing.T) {
	converger, _, server, runner := controlSessionFixture(t)
	converger.runner = explicitTmuxRunner{runner: runner, target: tmuxTransport{Kind: tmuxSocketPath, Value: server.socketPath, Source: tmuxSocketPathSource}}

	result, err := converger.converge(context.Background(), controlFixtureSocket, "home")
	if err != nil {
		t.Fatalf("converge nested equivalent route: %v", err)
	}
	if !result.changed {
		t.Fatal("nested equivalent route did not execute the identity plan")
	}
	if got := server.session("home").opts[tmuxopts.SessionRole]; got != resourcegraph.ControlSessionRole {
		t.Fatalf("role marker = %q, want %q", got, resourcegraph.ControlSessionRole)
	}
}

func TestControlSessionIdentityPlanRefusesMismatchedNestedPhysicalRoute(t *testing.T) {
	converger, store, _, runner := controlSessionFixture(t)
	server := runner.servers["-L\x00"+controlFixtureSocket]
	runner.servers["-S\x00/tmp/foreign-control.sock"] = server
	converger.runner = explicitTmuxRunner{runner: runner, target: tmuxTransport{Kind: tmuxSocketPath, Value: "/tmp/foreign-control.sock", Source: tmuxSocketPathSource}}

	_, err := converger.converge(context.Background(), controlFixtureSocket, "home")
	if err == nil || !strings.Contains(err.Error(), "physical route disagrees") {
		t.Fatalf("converge mismatched physical route error = %v", err)
	}
	if got := store.writes; got != 1 {
		// The Registry bind commits before the runtime mirror. The exact runtime
		// refusal leaves that retryable authority intact and performs no tmux write.
		t.Fatalf("Registry writes = %d, want one retryable bind commit", got)
	}
	for _, call := range runner.calls {
		argv := tmuxCommandArgv(call.args)
		if len(argv) > 0 && (argv[0] == "set-option" || argv[0] == "rename-window") {
			t.Fatalf("mismatched nested route executed a runtime write: %#v", call)
		}
	}
}

func TestControlSessionIdentityConvergenceNeverOverwritesForeignDescendantUID(t *testing.T) {
	for _, test := range []struct {
		name string
		seed func(*fakeTmuxSession)
		got  func(*fakeTmuxSession) string
		want string
	}{
		{name: "Window", seed: func(session *fakeTmuxSession) { session.windows[0].opts[tmuxopts.WindowUID] = "win-foreign" }, got: func(session *fakeTmuxSession) string { return session.windows[0].opts[tmuxopts.WindowUID] }, want: "Window carries a foreign UID"},
		{name: "Pane", seed: func(session *fakeTmuxSession) { session.windows[0].panes[0].opts[tmuxopts.PaneUID] = "pan-foreign" }, got: func(session *fakeTmuxSession) string { return session.windows[0].panes[0].opts[tmuxopts.PaneUID] }, want: "Pane carries a foreign UID"},
	} {
		t.Run(test.name, func(t *testing.T) {
			converger, store, server, runner := controlSessionFixture(t)
			if _, err := converger.converge(context.Background(), controlFixtureSocket, "home"); err != nil {
				t.Fatalf("seed convergence: %v", err)
			}
			session := server.session("home")
			test.seed(session)
			runner.calls = nil

			registry := store.registry.Clone()
			control := registry.ControlSessions[0]
			window := registry.Windows[0]
			pane := registry.Panes[0]
			binding := coremetadata.ControlSessionBinding{
				ControlSession: control,
				Windows: []coremetadata.ImportedWindow{{
					UID:         window.Metadata.UID,
					SourceIndex: 0,
					Origin:      coremetadata.ImportCreated,
				}},
				Panes: []coremetadata.ImportedPane{{
					UID:         pane.Metadata.UID,
					WindowIndex: 0,
					PaneIndex:   0,
					Origin:      coremetadata.ImportCreated,
				}},
			}
			targets := intmetadata.LegacyTargets{
				Windows: []string{session.windows[0].id},
				Panes:   [][]string{{session.windows[0].panes[0].id}},
			}
			mirror := intmetadata.NewMirror(explicitTmuxRunner{
				runner: runner,
				target: tmuxTransport{Kind: tmuxSocketName, Value: controlFixtureSocket, Source: tmuxSocketNameSource},
			})
			_, err := executeControlSessionIdentityPlan(
				context.Background(),
				tmuxTransport{Kind: tmuxSocketName, Value: controlFixtureSocket, Source: tmuxSocketNameSource},
				"home",
				mirror,
				registry,
				binding,
				targets,
				false,
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("foreign %s identity plan error = %v, want %q", test.name, err, test.want)
			}
			if got := test.got(session); !strings.HasSuffix(got, "-foreign") {
				t.Fatalf("foreign %s UID was overwritten: %q", test.name, got)
			}
			for _, call := range runner.calls {
				argv := tmuxCommandArgv(call.args)
				if len(argv) > 0 && (argv[0] == "set-option" || argv[0] == "rename-window") {
					t.Fatalf("foreign %s UID allowed a runtime write: %#v", test.name, call)
				}
			}
		})
	}
}

func TestControlSessionConvergeIsIdempotent(t *testing.T) {
	converger, store, server, runner := controlSessionFixture(t)

	if _, err := converger.converge(context.Background(), controlFixtureSocket, "home"); err != nil {
		t.Fatalf("first converge() error = %v", err)
	}
	writesAfterFirst := store.writes
	windowUID := store.registry.Windows[0].Metadata.UID
	paneUID := store.registry.Panes[0].Metadata.UID
	runner.calls = nil

	second, err := converger.converge(context.Background(), controlFixtureSocket, "home")
	if err != nil {
		t.Fatalf("second converge() error = %v", err)
	}
	if second.changed {
		t.Fatal("second converge() wrote the registry; an already-converged pass must be a byte no-op")
	}
	if second.writes != 0 {
		t.Fatalf("second converge() writes = %d, want zero Registry and tmux writes", second.writes)
	}
	if store.writes != writesAfterFirst {
		t.Fatalf("registry writes = %d, want %d after a converged repeat", store.writes, writesAfterFirst)
	}
	if got, want := len(store.registry.Windows), 1; got != want {
		t.Fatalf("len(Windows) = %d, want %d: a repeat pass must adopt, never duplicate", got, want)
	}
	if store.registry.Windows[0].Metadata.UID != windowUID || store.registry.Panes[0].Metadata.UID != paneUID {
		t.Fatal("a repeat pass re-identified the Window or Pane")
	}

	// A rebound object and exact role already carry the complete desired state,
	// so the repeat pass spends no tmux write at all.
	for _, call := range runner.calls {
		if call.args[0] != "set-option" {
			continue
		}
		t.Fatalf("the repeat pass issued a tmux write: %v", call.args)
	}
	session := server.session("home")
	if got, want := session.opts[tmuxopts.SessionRole], resourcegraph.ControlSessionRole; got != want {
		t.Fatalf("%s = %q, want %q after the repeat pass", tmuxopts.SessionRole, got, want)
	}
}

func TestControlSessionConvergeReobservesForeignClaimantInsideRegistryLock(t *testing.T) {
	converger, store, server, routed := controlSessionFixture(t)
	tracing := &controlClaimBetweenPlansRunner{delegate: routed, server: server}
	converger.runner = tracing
	before := store.snapshot()

	result, err := converger.converge(context.Background(), controlFixtureSocket, "home")
	if err != nil {
		t.Fatalf("converge() error = %v", err)
	}
	wantReason := `declared control target carries foreign Project uid claimant "project-race"`
	if got := string(result.skipped); got != wantReason {
		t.Fatalf("converge() skipped = %q, want %q", got, wantReason)
	}
	if tracing.markerReads != 2 {
		t.Fatalf("app-marker observations = %d, want preflight plus locked reobservation", tracing.markerReads)
	}
	if got := store.snapshot(); got != before {
		t.Fatalf("Registry changed across locked refusal\n--- before ---\n%s--- after ---\n%s", before, got)
	}
	if store.writes != 0 {
		t.Fatalf("locked refusal wrote Registry %d times", store.writes)
	}
	for _, call := range routed.calls {
		if len(call.args) > 0 && call.args[0] == "set-option" {
			t.Fatalf("locked refusal wrote tmux: %v", call.args)
		}
	}
}

func TestConfigApplyDeclarationConvergesAlreadyLiveHome(t *testing.T) {
	converger, store, server, _ := controlSessionFixture(t)
	runner := &controllerTriggerRunner{runner: converger.runner, store: store.store()}
	target, err := tmuxSocketNameTarget(controlFixtureSocket)
	if err != nil {
		t.Fatal(err)
	}
	first, err := runner.convergeControlTargets(context.Background(), target, true)
	if err != nil {
		t.Fatalf("config-apply control convergence: %v", err)
	}
	if !first.changed || first.skipped != "" {
		t.Fatalf("first = %+v, want live Home convergence", first)
	}
	if got := server.session("home").opts[tmuxopts.SessionRole]; got != resourcegraph.ControlSessionRole {
		t.Fatalf("role = %q, want control", got)
	}
	second, err := runner.convergeControlTargets(context.Background(), target, true)
	if err != nil {
		t.Fatal(err)
	}
	if second.changed || second.writes != 0 || second.skipped != "" {
		t.Fatalf("second = %+v, want zero-write convergence", second)
	}
}

func TestControlSessionInstalledHomePartialStateSmokeMatrix(t *testing.T) {
	for _, test := range []struct {
		name    string
		arrange func(t *testing.T, converger *controlSessionConverger, store *fakeResourceStore, server *fakeTmux)
		wantErr string
	}{
		{
			name: "root only missing",
			arrange: func(_ *testing.T, _ *controlSessionConverger, _ *fakeResourceStore, server *fakeTmux) {
				session := server.session("home")
				session.opts[tmuxopts.SessionRole] = resourcegraph.ControlSessionRole
				session.windows[0].opts[tmuxopts.WindowUID] = "win-interrupted"
				session.windows[0].panes[0].opts[tmuxopts.PaneUID] = "pan-interrupted"
			},
			wantErr: "ControlSession Window carries a foreign UID",
		},
		{
			name: "mirrors only missing",
			arrange: func(t *testing.T, converger *controlSessionConverger, _ *fakeResourceStore, server *fakeTmux) {
				if _, err := converger.converge(context.Background(), controlFixtureSocket, "home"); err != nil {
					t.Fatal(err)
				}
				delete(server.session("home").windows[0].opts, tmuxopts.WindowUID)
				delete(server.session("home").windows[0].panes[0].opts, tmuxopts.PaneUID)
			},
		},
		{name: "root role and mirrors all missing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			converger, store, server, runner := controlSessionFixture(t)
			if test.arrange != nil {
				test.arrange(t, converger, store, server)
			}
			first, err := converger.converge(context.Background(), controlFixtureSocket, "home")
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("first = %+v, error = %v, want %q", first, err, test.wantErr)
				}
				live := server.session("home")
				if got := live.windows[0].opts[tmuxopts.WindowUID]; got != "win-interrupted" {
					t.Fatalf("foreign Window UID was overwritten: %q", got)
				}
				if got := live.windows[0].panes[0].opts[tmuxopts.PaneUID]; got != "pan-interrupted" {
					t.Fatalf("foreign Pane UID was overwritten: %q", got)
				}
				for _, call := range runner.calls {
					argv := tmuxCommandArgv(call.args)
					if len(argv) > 0 && (argv[0] == "set-option" || argv[0] == "rename-window") {
						t.Fatalf("foreign partial state reached a runtime write: %v", call.args)
					}
				}
				return
			}
			if err != nil || first.skipped != "" {
				t.Fatalf("first = %+v, %v", first, err)
			}
			control := store.registry.ControlSessions[0]
			window := store.registry.WindowsOf(control.Metadata.UID)[0]
			panes := store.registry.PanesOf(window.Metadata.UID)
			if len(panes) != 1 {
				t.Fatalf("control window panes = %d, want 1", len(panes))
			}
			live := server.session("home")
			if live.opts[tmuxopts.SessionRole] != resourcegraph.ControlSessionRole ||
				live.windows[0].opts[tmuxopts.WindowUID] != window.Metadata.UID ||
				live.windows[0].panes[0].opts[tmuxopts.PaneUID] != panes[0].Metadata.UID {
				t.Fatalf("first pass did not converge exact owner chain")
			}
			runner.calls = nil
			second, err := converger.converge(context.Background(), controlFixtureSocket, "home")
			if err != nil || second.changed || second.writes != 0 || second.skipped != "" {
				t.Fatalf("second = %+v, %v, want zero-write", second, err)
			}
			for _, call := range runner.calls {
				if len(call.args) > 0 && call.args[0] == "set-option" {
					t.Fatalf("second pass wrote tmux: %v", call.args)
				}
			}
		})
	}
}

// TestControlSessionConvergeFailsClosed pins both refusals in one table. Neither
// is an error: `projmux shell` owes the operator a shell, and refusing to mark a
// server projmux does not own is ordinary.
func TestControlSessionConvergeFailsClosed(t *testing.T) {
	for _, tt := range []struct {
		name    string
		arrange func(*fakeTmux)
		want    controlSessionSkip
	}{
		{
			name:    "a server projmux did not start gets no marker",
			arrange: func(server *fakeTmux) { server.appMarker = "" },
			want:    controlSessionSkipNotAppOwned,
		},
		{
			name:    "a hand-set app marker value is not app ownership",
			arrange: func(server *fakeTmux) { server.appMarker = "yes" },
			want:    controlSessionSkipNotAppOwned,
		},
		{
			name: "an ephemeral session gets no control role",
			arrange: func(server *fakeTmux) {
				server.session("home").opts[tmuxopts.EphemeralSession] = resourcegraph.EphemeralMarker
			},
			want: controlSessionSkipEphemeral,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			converger, store, server, _ := controlSessionFixture(t)
			tt.arrange(server)

			result, err := converger.converge(context.Background(), controlFixtureSocket, "home")
			if err != nil {
				t.Fatalf("converge() error = %v, want a silent skip", err)
			}
			if result.skipped != tt.want {
				t.Fatalf("converge() skipped = %q, want %q", result.skipped, tt.want)
			}
			if store.transactions != 0 || store.writes != 0 {
				t.Fatalf("a skipped pass opened %d transactions and wrote %d times", store.transactions, store.writes)
			}
			if got := server.session("home").opts[tmuxopts.SessionRole]; got != "" {
				t.Fatalf("%s = %q, want it unwritten", tmuxopts.SessionRole, got)
			}
			if got := server.session("home").windows[0].opts[tmuxopts.WindowUID]; got != "" {
				t.Fatalf("%s = %q, want it unwritten", tmuxopts.WindowUID, got)
			}
		})
	}
}

func TestControlSessionConvergeRequiresAnExplicitSocket(t *testing.T) {
	converger, store, _, _ := controlSessionFixture(t)
	if _, err := converger.converge(context.Background(), "   ", "home"); err == nil {
		t.Fatal("converge() with a blank socket = nil, want a refusal rather than a default-server probe")
	}
	if store.transactions != 0 {
		t.Fatalf("a refused pass opened %d transactions", store.transactions)
	}
}

func TestControlSessionConvergeRefusesForeignScopedWindowsWithZeroWrites(t *testing.T) {
	converger, store, server, _ := controlSessionFixture(t)
	// A second app-owned session with a Project-owned Window sitting inside the
	// control session's window list is not something the observation can produce,
	// so the reachable shape of "somebody else's uid" is a live Home window
	// already carrying a uid this Registry attributes to a Project.
	store.registry = resourceFixtureRegistry(t)
	converger.resources = store.store()
	projectWindowUID := store.registry.Windows[0].Metadata.UID
	server.session("home").windows[0].opts[tmuxopts.WindowUID] = projectWindowUID

	result, err := converger.converge(context.Background(), controlFixtureSocket, "home")
	if err != nil {
		t.Fatalf("converge() error = %v", err)
	}
	if !strings.Contains(string(result.skipped), "conflicts with Project owner") {
		t.Fatalf("converge() skipped = %q, want exact Project-owner conflict", result.skipped)
	}
	window, ok := store.registry.Window(projectWindowUID)
	if !ok {
		t.Fatalf("project window %q disappeared", projectWindowUID)
	}
	if owner := window.Metadata.OwnerRef; owner == nil || owner.Kind != coremetadata.KindProject {
		t.Fatalf("project window ownerRef = %+v, want it untouched under its Project", owner)
	}
	// A refusal is all-or-nothing: it cannot create even the root or role.
	if got, want := len(store.registry.ControlSessions), 0; got != want {
		t.Fatalf("len(ControlSessions) = %d, want %d", got, want)
	}
	if got := server.session("home").opts[tmuxopts.SessionRole]; got != "" {
		t.Fatalf("%s = %q, want zero-write refusal", tmuxopts.SessionRole, got)
	}
	if store.writes != 0 {
		t.Fatalf("refusal wrote Registry %d times", store.writes)
	}
}

func TestControlSessionWarningNamesTheSession(t *testing.T) {
	got := controlSessionWarning("home", context.Canceled)
	if !strings.HasPrefix(got, "warning: converge control session \"home\": ") {
		t.Fatalf("controlSessionWarning() = %q", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Fatalf("controlSessionWarning() = %q, want a trailing newline", got)
	}
}
