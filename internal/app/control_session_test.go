package app

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
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
		if call.flag != "-L" || call.value != controlFixtureSocket {
			t.Fatalf("tmux call routed to %s/%s, want -L/%s: %v", call.flag, call.value, controlFixtureSocket, call.args)
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
		if i := slices.Index(call.args, "-t"); i < 0 || i+1 >= len(call.args) || call.args[i+1] != "home" {
			t.Fatalf("the role write did not name -t home: %v", call.args)
		}
	}
	if roleWrites != 1 {
		t.Fatalf("the role marker was written %d times, want exactly 1", roleWrites)
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
	if store.writes != writesAfterFirst {
		t.Fatalf("registry writes = %d, want %d after a converged repeat", store.writes, writesAfterFirst)
	}
	if got, want := len(store.registry.Windows), 1; got != want {
		t.Fatalf("len(Windows) = %d, want %d: a repeat pass must adopt, never duplicate", got, want)
	}
	if store.registry.Windows[0].Metadata.UID != windowUID || store.registry.Panes[0].Metadata.UID != paneUID {
		t.Fatal("a repeat pass re-identified the Window or Pane")
	}

	// A rebound object already carries the exact uid, so the repeat pass spends
	// no tmux write on the identity mirror. Only the role marker is rewritten,
	// and rewriting it to the same value is what tmux makes a no-op.
	for _, call := range runner.calls {
		if call.args[0] != "set-option" {
			continue
		}
		if slices.Contains(call.args, tmuxopts.WindowUID) || slices.Contains(call.args, tmuxopts.PaneUID) {
			t.Fatalf("the repeat pass re-mirrored an already-bound uid: %v", call.args)
		}
	}
	session := server.session("home")
	if got, want := session.opts[tmuxopts.SessionRole], resourcegraph.ControlSessionRole; got != want {
		t.Fatalf("%s = %q, want %q after the repeat pass", tmuxopts.SessionRole, got, want)
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

func TestControlSessionConvergeLeavesForeignScopedWindowsAlone(t *testing.T) {
	converger, store, server, _ := controlSessionFixture(t)
	// A second app-owned session with a Project-owned Window sitting inside the
	// control session's window list is not something the observation can produce,
	// so the reachable shape of "somebody else's uid" is a live Home window
	// already carrying a uid this Registry attributes to a Project.
	store.registry = resourceFixtureRegistry(t)
	converger.resources = store.store()
	projectWindowUID := store.registry.Windows[0].Metadata.UID
	server.session("home").windows[0].opts[tmuxopts.WindowUID] = projectWindowUID

	if _, err := converger.converge(context.Background(), controlFixtureSocket, "home"); err != nil {
		t.Fatalf("converge() error = %v", err)
	}
	window, ok := store.registry.Window(projectWindowUID)
	if !ok {
		t.Fatalf("project window %q disappeared", projectWindowUID)
	}
	if owner := window.Metadata.OwnerRef; owner == nil || owner.Kind != coremetadata.KindProject {
		t.Fatalf("project window ownerRef = %+v, want it untouched under its Project", owner)
	}
	// The control session itself is still created: the refusal is about the one
	// live window, not about the session's identity.
	if got, want := len(store.registry.ControlSessions), 1; got != want {
		t.Fatalf("len(ControlSessions) = %d, want %d", got, want)
	}
	if got := server.session("home").opts[tmuxopts.SessionRole]; got != resourcegraph.ControlSessionRole {
		t.Fatalf("%s = %q, want the control role", tmuxopts.SessionRole, got)
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
