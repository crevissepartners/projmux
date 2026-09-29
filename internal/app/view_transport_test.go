package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	"github.com/crevissepartners/projmux/internal/testutil/liveguard"
)

// viewTMUXEnv is a lookupEnv that answers only TMUX. An unset value is the
// caller outside tmux.
func viewTMUXEnv(tmux string, set bool) func(string) string {
	return func(key string) string {
		if key == "TMUX" && set {
			return tmux
		}
		return ""
	}
}

// TestViewTransportSelection pins the read route of `get` and `describe`: an
// inherited absolute $TMUX wins as before, and only when nothing names a
// server does a view observe the app socket. The runtime diagnostics transport
// keeps "no transport" as its own answer.
func TestViewTransportSelection(t *testing.T) {
	t.Parallel()
	appSocket := resourcegraph.Transport{Kind: resourcegraph.TransportSocketName, Value: defaultAppSocket, Source: resourcegraph.TransportSourceAppSocket}
	none := resourcegraph.Transport{Kind: resourcegraph.TransportNone, Source: resourcegraph.TransportSourceNone}
	for _, test := range []struct {
		name          string
		tmux          string
		set           bool
		wantView      resourcegraph.Transport
		wantDiagnosis resourcegraph.Transport
	}{
		{name: "outside tmux", wantView: appSocket, wantDiagnosis: none},
		{name: "empty TMUX", tmux: "", set: true, wantView: appSocket, wantDiagnosis: none},
		{name: "relative TMUX is discarded", tmux: "tmux-1000/projmux,1,0", set: true, wantView: appSocket, wantDiagnosis: none},
		{
			name: "inherited app server", tmux: "/tmp/tmux-1000/projmux,8084,6", set: true,
			wantView:      resourcegraph.Transport{Kind: resourcegraph.TransportSocketPath, Value: "/tmp/tmux-1000/projmux", Source: resourcegraph.TransportSourceInheritedEnv},
			wantDiagnosis: resourcegraph.Transport{Kind: resourcegraph.TransportSocketPath, Value: "/tmp/tmux-1000/projmux", Source: resourcegraph.TransportSourceInheritedEnv},
		},
		{
			name: "inherited other server stays current", tmux: "/tmp/tmux-1000/default,77,0", set: true,
			wantView:      resourcegraph.Transport{Kind: resourcegraph.TransportSocketPath, Value: "/tmp/tmux-1000/default", Source: resourcegraph.TransportSourceInheritedEnv},
			wantDiagnosis: resourcegraph.Transport{Kind: resourcegraph.TransportSocketPath, Value: "/tmp/tmux-1000/default", Source: resourcegraph.TransportSourceInheritedEnv},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			reader := &runtimeDiagnosticsReader{lookupEnv: viewTMUXEnv(test.tmux, test.set)}
			view, err := reader.viewTransport()
			if err != nil {
				t.Fatal(err)
			}
			if view != test.wantView {
				t.Errorf("viewTransport = %+v, want %+v", view, test.wantView)
			}
			if got := view.Args(); len(got) == 0 {
				t.Errorf("view transport %+v has no exact tmux prefix", view)
			}
			diagnosis, err := reader.transport(runtimeTransportRequest{})
			if err != nil {
				t.Fatal(err)
			}
			if diagnosis != test.wantDiagnosis {
				t.Errorf("transport = %+v, want %+v", diagnosis, test.wantDiagnosis)
			}
		})
	}
}

// viewAppServer is a fake app server with one live managed Window whose two
// Panes carry the fixture's uids.
func viewAppServer(socketPath string) *fakeTmux {
	server := newFakeTmux()
	server.appMarker = "1"
	server.socketPath = socketPath
	alpha := server.addSession("alpha")
	alpha.opts[tmuxopts.ProjectUIDSession] = "prj-alpha"
	alpha.opts[tmuxopts.ProjectNameSession] = "alpha"
	alpha.windows[0].opts[tmuxopts.WindowUID] = "win-alpha-main"
	alpha.windows[0].name = "live editor"
	alpha.windows[0].panes[0].opts[tmuxopts.PaneUID] = "pan-alpha-zsh"
	agentPane := newFakeTmuxPane(server.mint("%"))
	agentPane.opts[tmuxopts.PaneUID] = "pan-alpha-codex"
	alpha.windows[0].panes = append(alpha.windows[0].panes, agentPane)
	return server
}

// viewReadCommands wires `get` and `describe` to one reader over runner, the
// way app.go does, with TMUX answered by env.
func viewReadCommands(t *testing.T, runner tmuxCommandRunner, env func(string) string) (*getCommand, *describeCommand) {
	t.Helper()
	return viewReadCommandsOver(t, newFakeResourceStore(t), runner, env)
}

// viewReadCommandsOver is viewReadCommands over a Registry the test arranged.
func viewReadCommandsOver(t *testing.T, store *fakeResourceStore, runner tmuxCommandRunner, env func(string) string) (*getCommand, *describeCommand) {
	t.Helper()
	prepareDisplayFirstTableFixture(t, store)
	reader := &runtimeDiagnosticsReader{runner: runner, lookupEnv: env, loadRegistry: store.store().load}
	reader.observe = func(ctx context.Context, transport resourcegraph.Transport) resourcegraph.Inventory {
		return intmetadata.NewInventoryObserver(runner, transport).Observe(ctx)
	}
	get := newTestListGetCommand(t, store)
	get.runtime = nil
	get.reads = runtimeResourceReadLookup(reader)
	get.runtimeDiag = reader
	describe := newTestDescribeCommand(t, store)
	describe.runtime = nil
	describe.reads = runtimeResourceReadLookup(reader)
	return get, describe
}

// viewRow returns the one table row whose NAME cell is name.
func viewRow(t *testing.T, stdout, name string) []string {
	t.Helper()
	column := -1
	for line := range strings.SplitSeq(stdout, "\n") {
		fields := strings.Fields(line)
		if column < 0 {
			column = slices.Index(fields, "NAME")
			continue
		}
		if column < len(fields) && fields[column] == name {
			return fields
		}
	}
	t.Fatalf("no row %q in:\n%s", name, stdout)
	return nil
}

func viewRun(t *testing.T, cmd rawArgvCommand, args ...string) string {
	t.Helper()
	stdout, stderr, err := runRoute(t, cmd, args...)
	if err != nil || stderr != "" {
		t.Fatalf("%v: err=%v stderr=%q", args, err, stderr)
	}
	return stdout
}

// TestRegistryViewsOutsideTmuxObserveTheAppServer is C-1's Guarantee: a view
// with no $TMUX observes `-L projmux` and renders exactly what the same view
// inside that server's tmux renders.
func TestRegistryViewsOutsideTmuxObserveTheAppServer(t *testing.T) {
	t.Parallel()
	const socketPath = "/tmp/view-fixture/projmux"
	server := viewAppServer(socketPath)
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-L\x00" + defaultAppSocket: server}}
	outsideGet, outsideDescribe := viewReadCommands(t, runner, viewTMUXEnv("", false))
	insideGet, insideDescribe := viewReadCommands(t, runner, viewTMUXEnv(socketPath+",1,0", true))

	for _, args := range [][]string{
		{"windows", "-A", "-o", "wide"},
		{"panes", "-A", "-o", "wide"},
		{"windows", "-A", "-o", "json"},
	} {
		runner.calls = nil
		outside := viewRun(t, outsideGet, args...)
		for _, call := range runner.calls {
			if call.flag != "-L" || call.value != defaultAppSocket {
				t.Fatalf("outside-tmux %v reached tmux %s %s, want only -L %s", args, call.flag, call.value, defaultAppSocket)
			}
		}
		if len(runner.calls) == 0 {
			t.Fatalf("outside-tmux %v observed no server", args)
		}
		inside := viewRun(t, insideGet, args...)
		if outside != inside {
			t.Errorf("get %v differs outside tmux:\n--- outside\n%s--- inside\n%s", args, outside, inside)
		}
	}

	wide := viewRun(t, outsideGet, "windows", "-p", "uid:prj-alpha", "-o", "wide")
	row := strings.Join(viewRow(t, wide, "main"), " ")
	for _, want := range []string{" live ", "open", "live-window-name", "true"} {
		if !strings.Contains(row+" ", want) {
			t.Errorf("outside-tmux live Window row %q lacks %q", row, want)
		}
	}
	panes := viewRun(t, outsideGet, "panes", "-p", "uid:prj-alpha", "-o", "wide")
	for _, name := range []string{"zsh", "codex-pane"} {
		if fields := viewRow(t, panes, name); !strings.Contains(strings.Join(fields, " "), " live ") {
			t.Errorf("outside-tmux live Pane %q row = %v, want live", name, fields)
		}
	}

	for _, args := range [][]string{
		{"window", "uid:win-alpha-main"},
		{"pane", "uid:pan-alpha-zsh"},
	} {
		outside := viewRun(t, outsideDescribe, args...)
		inside := viewRun(t, insideDescribe, args...)
		if outside != inside {
			t.Errorf("describe %v differs outside tmux:\n--- outside\n%s--- inside\n%s", args, outside, inside)
		}
		if !strings.Contains(outside, "live") {
			t.Errorf("describe %v outside tmux reports no live status:\n%s", args, outside)
		}
	}
}

// TestRegistryViewsOutsideTmuxWithNoAppServerReadOffline is C-1's boundary:
// with no server behind the app socket, the view reads offline (the
// inventory's server-absent rule) and asks no other server.
func TestRegistryViewsOutsideTmuxWithNoAppServerReadOffline(t *testing.T) {
	t.Parallel()
	var calls []string
	runner := resourceRunnerFunc(func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if name != "tmux" || len(args) < 2 || args[0] != "-L" || args[1] != defaultAppSocket {
			return nil, fmt.Errorf("unexpected tmux route %s %v", name, args)
		}
		return nil, errors.New("exit status 1: no server running on /tmp/view-fixture/" + defaultAppSocket)
	})
	get, _ := viewReadCommands(t, runner, viewTMUXEnv("", false))
	wide := viewRun(t, get, "windows", "-p", "uid:prj-alpha", "-o", "wide")
	if fields := viewRow(t, wide, "main"); !strings.Contains(strings.Join(fields, " "), " offline ") {
		t.Errorf("outside-tmux Window with no app server = %v, want offline", fields)
	}
	if len(calls) == 0 {
		t.Fatal("outside-tmux view did not ask the app socket")
	}
}

// unsetAppMarkerRunner answers a read of an unset @projmux_app the way real
// tmux does -- a non-zero "invalid option" -- for every routed server whose
// marker is empty. The shared fake answers an empty value instead, which lands
// on the same host mode by another branch; this pins the branch a real
// unmarked server takes.
type unsetAppMarkerRunner struct {
	*routedTmuxRunner
	unsetReads int
}

func (r *unsetAppMarkerRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == "tmux" && len(args) == 5 && slices.Equal(args[2:], []string{"show-options", "-gv", tmuxopts.AppGlobal}) {
		if server := r.servers[args[0]+"\x00"+args[1]]; server != nil && server.appMarker == "" {
			r.calls = append(r.calls, routedTmuxCall{flag: args[0], value: args[1], args: slices.Clone(args[2:])})
			r.unsetReads++
			return nil, errors.New("exit status 1: invalid option: " + tmuxopts.AppGlobal)
		}
	}
	return r.routedTmuxRunner.Run(ctx, name, args...)
}

// appSocketViewRunner routes `-L projmux` to server alone.
func appSocketViewRunner(server *fakeTmux) *unsetAppMarkerRunner {
	return &unsetAppMarkerRunner{routedTmuxRunner: &routedTmuxRunner{servers: map[string]*fakeTmux{"-L\x00" + defaultAppSocket: server}}}
}

// requireOnlyAppSocket fails unless a view asked tmux at least once and only
// ever through `-L projmux`.
func requireOnlyAppSocket(t *testing.T, calls []routedTmuxCall) {
	t.Helper()
	if len(calls) == 0 {
		t.Fatal("outside-tmux view observed no server")
	}
	for _, call := range calls {
		if call.flag != "-L" || call.value != defaultAppSocket {
			t.Fatalf("outside-tmux view reached tmux %s %s, want only -L %s", call.flag, call.value, defaultAppSocket)
		}
	}
}

// TestRegistryViewsOutsideTmuxOnAnUnmarkedAppSocketServerMatchTheAppOwnedRows
// is C-2's Guarantee ①: a server behind the app socket with no @projmux_app
// but with the managed uid markers is a standalone host, where managed
// resources live exactly as they do on an app-owned server. Outside tmux every
// view renders byte for byte what it renders for the app-owned server, and
// asks only `-L projmux`.
func TestRegistryViewsOutsideTmuxOnAnUnmarkedAppSocketServerMatchTheAppOwnedRows(t *testing.T) {
	t.Parallel()
	const socketPath = "/tmp/view-fixture/projmux"
	owned := appSocketViewRunner(viewAppServer(socketPath))
	server := viewAppServer(socketPath)
	server.appMarker = ""
	unmarked := appSocketViewRunner(server)
	ownedGet, ownedDescribe := viewReadCommands(t, owned, viewTMUXEnv("", false))
	unmarkedGet, unmarkedDescribe := viewReadCommands(t, unmarked, viewTMUXEnv("", false))

	for _, args := range [][]string{
		{"windows", "-A", "-o", "wide"},
		{"panes", "-A", "-o", "wide"},
		{"windows", "-A", "-o", "json"},
		{"panes", "-A", "-o", "json"},
	} {
		want := viewRun(t, ownedGet, args...)
		if got := viewRun(t, unmarkedGet, args...); got != want {
			t.Errorf("get %v on an unmarked app socket server differs from the app-owned server:\n--- unmarked\n%s--- app-owned\n%s", args, got, want)
		}
	}
	for _, args := range [][]string{
		{"window", "uid:win-alpha-main"},
		{"pane", "uid:pan-alpha-zsh"},
	} {
		want := viewRun(t, ownedDescribe, args...)
		if got := viewRun(t, unmarkedDescribe, args...); got != want {
			t.Errorf("describe %v on an unmarked app socket server differs from the app-owned server:\n--- unmarked\n%s--- app-owned\n%s", args, got, want)
		}
	}

	wide := viewRun(t, unmarkedGet, "windows", "-p", "uid:prj-alpha", "-o", "wide")
	row := strings.Join(viewRow(t, wide, "main"), " ")
	for _, want := range []string{" live ", " open,delete ", " live-window-name ", " true "} {
		if !strings.Contains(" "+row+" ", want) {
			t.Errorf("Window row on an unmarked app socket server %q lacks %q", row, want)
		}
	}
	if unmarked.unsetReads == 0 {
		t.Fatal("no view read the unset @projmux_app; the test no longer reaches the standalone branch")
	}
	if owned.unsetReads != 0 {
		t.Fatalf("the app-owned control read an unset marker %d times", owned.unsetReads)
	}
	requireOnlyAppSocket(t, unmarked.calls)
}

// TestRegistryViewsOutsideTmuxOnAForeignAppSocketServerReadOffline is C-2's
// Guarantee ② and ③: a server behind the app socket that projmux did not
// start and that carries no managed marker holds none of the Registry's
// runtime. Outside tmux its Windows and Panes read offline with start, even
// when a session there has the Project's session name and holds exactly the
// $N/@N/%N the Registry still records -- runtime ids are per-server counters,
// not identity. The view asks only `-L projmux`.
func TestRegistryViewsOutsideTmuxOnAForeignAppSocketServerReadOffline(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		session string
	}{
		{name: "foreign session", session: "foreign"},
		{name: "same session name over the recorded runtime ids", session: "alpha"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := newFakeTmux()
			server.appMarker = ""
			server.socketPath = "/tmp/view-fixture/projmux"
			first := server.addSession(test.session)
			first.windows[0].name = "main"
			server.addSession("scratch")
			runner := appSocketViewRunner(server)

			// The Registry still records the first session's ids from a run of
			// the app server that is gone.
			store := newFakeResourceStore(t)
			for i := range store.registry.Windows {
				if store.registry.Windows[i].Metadata.UID == "win-alpha-main" {
					store.registry.Windows[i].Status.RuntimeSessionID = first.id
					store.registry.Windows[i].Status.RuntimeID = first.windows[0].id
				}
			}
			for i := range store.registry.Panes {
				if store.registry.Panes[i].Metadata.UID == "pan-alpha-zsh" {
					store.registry.Panes[i].Status.Activation = coremetadata.PaneActivation{Generation: "gen-stale", RuntimeID: first.windows[0].panes[0].id}
				}
			}
			get, describe := viewReadCommandsOver(t, store, runner, viewTMUXEnv("", false))

			windows := viewRun(t, get, "windows", "-p", "uid:prj-alpha", "-o", "wide")
			panes := viewRun(t, get, "panes", "-p", "uid:prj-alpha", "-o", "wide")
			for _, check := range []struct {
				table, name string
				want        []string
			}{
				{windows, "main", []string{" offline ", " start,delete ", " false "}},
				{windows, "review", []string{" offline ", " start,delete ", " false "}},
				{panes, "zsh", []string{" offline ", " false "}},
				{panes, "log", []string{" offline ", " false "}},
				{panes, "codex-pane", []string{" offline ", " false "}},
			} {
				row := " " + strings.Join(viewRow(t, check.table, check.name), " ") + " "
				if strings.Contains(row, " live-") {
					t.Errorf("%q row on a foreign app socket server %q takes its context from live runtime", check.name, row)
				}
				for _, want := range check.want {
					if !strings.Contains(row, want) {
						t.Errorf("%q row on a foreign app socket server %q lacks %q", check.name, row, want)
					}
				}
			}
			for _, args := range [][]string{
				{"windows", "-A", "-o", "wide"},
				{"panes", "-A", "-o", "wide"},
			} {
				table := viewRun(t, get, args...)
				for line := range strings.SplitSeq(table, "\n") {
					if slices.Contains(strings.Fields(line), "live") {
						t.Errorf("get %v on a foreign app socket server reports a live row: %q", args, line)
					}
				}
			}
			for _, args := range [][]string{
				{"window", "uid:win-alpha-main"},
				{"pane", "uid:pan-alpha-zsh"},
			} {
				out := viewRun(t, describe, args...)
				if !strings.Contains(out, "offline") || strings.Contains(out, "live") {
					t.Errorf("describe %v on a foreign app socket server is not offline:\n%s", args, out)
				}
			}
			if runner.unsetReads == 0 {
				t.Fatal("no view read the unset @projmux_app on the foreign server")
			}
			requireOnlyAppSocket(t, runner.calls)
		})
	}
}

// TestRegistryViewsInsideAnotherTmuxKeepTheInheritedServer is C-1's Scope:
// with $TMUX naming a server that is not the app server, the view observes
// that server exactly as before and never asks the app socket.
func TestRegistryViewsInsideAnotherTmuxKeepTheInheritedServer(t *testing.T) {
	t.Parallel()
	app := viewAppServer("/tmp/view-fixture/projmux")
	other := newFakeTmux()
	other.socketPath = "/tmp/view-fixture/default"
	other.addSession("scratch")
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-L\x00" + defaultAppSocket: app, "-L\x00default": other}}
	get, _ := viewReadCommands(t, runner, viewTMUXEnv(other.socketPath+",1,0", true))
	wide := viewRun(t, get, "windows", "-p", "uid:prj-alpha", "-o", "wide")
	if fields := viewRow(t, wide, "main"); !strings.Contains(strings.Join(fields, " "), " offline ") {
		t.Errorf("Window observed through another inherited server = %v, want offline", fields)
	}
	if len(app.calls) != 0 {
		t.Errorf("a view inside another tmux asked the app server: %v", app.calls)
	}
	for _, call := range runner.calls {
		if call.flag != "-S" || call.value != other.socketPath {
			t.Errorf("view inside another tmux reached %s %s, want only -S %s", call.flag, call.value, other.socketPath)
		}
	}
}

// TestViewAppSocketResolvesInsideTheTestGuard pins acceptance 5's mechanism:
// in this package a bare `-L projmux` names a socket under the guard's private
// TMUX_TMPDIR, so a view that falls back to the app socket can never reach the
// developer's live app server.
func TestViewAppSocketResolvesInsideTheTestGuard(t *testing.T) {
	liveguard.RequireActive(t)
	if _, ok := os.LookupEnv("TMUX"); ok {
		t.Fatal("TMUX is inherited; a view would observe the caller's server")
	}
	tmpdir := os.Getenv("TMUX_TMPDIR")
	if tmpdir == "" || !filepath.IsAbs(tmpdir) {
		t.Fatalf("TMUX_TMPDIR = %q; `tmux -L %s` would resolve to the live socket directory", tmpdir, defaultAppSocket)
	}
	if filepath.Clean(tmpdir) == "/tmp" {
		t.Fatalf("TMUX_TMPDIR = %q is tmux's default, which holds the live app socket", tmpdir)
	}
	view, err := newRuntimeDiagnosticsReader(nil).viewTransport()
	if err != nil {
		t.Fatal(err)
	}
	if view.Kind != resourcegraph.TransportSocketName || view.Value != defaultAppSocket {
		t.Fatalf("view transport in the guarded test process = %+v, want -L %s", view, defaultAppSocket)
	}
}
