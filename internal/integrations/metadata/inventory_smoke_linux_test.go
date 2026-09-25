//go:build linux

package metadata

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/registryview"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
)

// TestResolvedResourceGraphRealTmuxSmoke observes a real tmux server the test
// creates itself, so the format strings, the option spellings, and the
// containment join are proven against tmux rather than against a fixture.
//
// It runs only when PROJMUX_RESOURCEGRAPH_TMUX_SMOKE or PROJMUX_REAL_TMUX_STRICT
// (which the CI Unit Tests job sets) is "1", because it spawns tmux servers.
// It never touches the caller's tmux: the inherited TMUX/TMUX_PANE and
// __PROJMUX_RUNTIME_ANCHOR_PANE are stripped from every invocation, no tmux
// config file is read, TMUX_TMPDIR and the socket name are unique to this test,
// and cleanup kills only the exact #{socket_path} it has confirmed lives inside
// its own temporary root.
//
//	PROJMUX_RESOURCEGRAPH_TMUX_SMOKE=1 go test ./internal/integrations/metadata/ \
//	  -run TestResolvedResourceGraphRealTmuxSmoke -count=1 -v
func TestResolvedResourceGraphRealTmuxSmoke(t *testing.T) {
	requireIsolatedTmuxSmoke(t, "PROJMUX_RESOURCEGRAPH_TMUX_SMOKE")
	root := isolatedTmuxSmokeRoot(t, "pmx-rgraph-")
	tmpdir := filepath.Join(root, "tmux")
	if err := os.MkdirAll(tmpdir, 0o700); err != nil {
		t.Fatalf("create isolated TMUX_TMPDIR: %v", err)
	}
	primary := fmt.Sprintf("pmxrg-%d-a", os.Getpid())
	sibling := fmt.Sprintf("pmxrg-%d-b", os.Getpid())

	runner := &smokeRunner{tmpdir: tmpdir}
	ctx := context.Background()
	run := func(socket string, args ...string) string {
		t.Helper()
		out, err := runner.Run(ctx, "tmux", append([]string{"-L", socket}, args...)...)
		if err != nil {
			t.Fatalf("tmux -L %s %v: %v (%s)", socket, args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	killExact := func(socket string) {
		t.Helper()
		path, err := runner.Run(ctx, "tmux", "-L", socket, "display-message", "-p", "#{socket_path}")
		if err != nil {
			t.Logf("cleanup: socket %s already gone: %v", socket, err)
			return
		}
		socketPath := strings.TrimSpace(string(path))
		// Never kill a server whose socket is not provably inside this test's own
		// root: a bare kill-server or a TMUX_TMPDIR-only assumption would be able
		// to take down the operator's sessions.
		if socketPath == "" || !strings.HasPrefix(socketPath, root+string(os.PathSeparator)) {
			t.Errorf("refusing to kill socket %q: outside the smoke root %q", socketPath, root)
			return
		}
		if _, err := runner.Run(ctx, "tmux", "-S", socketPath, "kill-server"); err != nil {
			t.Logf("cleanup: kill %s: %v", socketPath, err)
			return
		}
		t.Logf("cleanup: killed exact socket %s", socketPath)
	}
	t.Cleanup(func() { killExact(primary) })
	t.Cleanup(func() { killExact(sibling) })

	// The primary server: one managed Project session with a managed Window and
	// Pane, a control Home, an ephemeral scratch session, and a plain session.
	created := run(primary, "new-session", "-d", "-s", "alpha", "-c", root, "-P", "-F", "#{window_id} #{pane_id}")
	fields := strings.Fields(created)
	if len(fields) != 2 {
		t.Fatalf("new-session reported %q, want a window id and a pane id", created)
	}
	windowID, paneID := fields[0], fields[1]
	run(primary, "set-option", "-g", "@projmux_app", "1")
	run(primary, "set-option", "-t", "alpha", "-q", "@projmux_project_uid", "project-alpha")
	run(primary, "set-option", "-t", "alpha", "-q", "@projmux_project_name", "alpha")
	run(primary, "set-option", "-t", "alpha", "-q", "@projmux_project_path", root)
	run(primary, "set-option", "-w", "-t", windowID, "-q", "@projmux_window_uid", "win-alpha-1")
	run(primary, "set-option", "-p", "-t", paneID, "-q", "@projmux_pane_uid", "pane-alpha-1")
	run(primary, "rename-window", "-t", windowID, "exact-live-window")
	run(primary, "select-pane", "-t", paneID, "-T", "exact-live-pane")
	run(primary, "new-session", "-d", "-s", "home", "-c", root)
	run(primary, "set-option", "-t", "home", "-q", "@projmux_session_role", "control")
	run(primary, "new-session", "-d", "-s", "scratch", "-c", root)
	run(primary, "set-option", "-t", "scratch", "-q", "@projmux_ephemeral", "1")
	run(primary, "new-session", "-d", "-s", "plain", "-c", root)

	// The sibling server mirrors the same uids. Nothing in the observation may
	// read it, and nothing in the graph may bind to it.
	siblingCreated := run(sibling, "new-session", "-d", "-s", "alpha", "-c", root, "-P", "-F", "#{window_id} #{pane_id}")
	siblingFields := strings.Fields(siblingCreated)
	if len(siblingFields) != 2 {
		t.Fatalf("sibling new-session reported %q", siblingCreated)
	}
	run(sibling, "set-option", "-t", "alpha", "-q", "@projmux_project_uid", "project-alpha")
	run(sibling, "set-option", "-w", "-t", siblingFields[0], "-q", "@projmux_window_uid", "win-alpha-1")
	run(sibling, "set-option", "-p", "-t", siblingFields[1], "-q", "@projmux_pane_uid", "pane-alpha-1")
	run(sibling, "rename-window", "-t", siblingFields[0], "sibling-window-must-not-overlay")
	run(sibling, "select-pane", "-t", siblingFields[1], "-T", "sibling-pane-must-not-overlay")
	siblingSessionsBefore := run(sibling, "list-sessions", "-F", "#{session_name}")

	transport := resourcegraph.Transport{
		Kind: resourcegraph.TransportSocketName, Value: primary,
		Source: resourcegraph.TransportSourceSocketName,
	}
	runner.record = true
	observer := NewInventoryObserver(runner, transport)
	observed := observer.Observe(ctx)
	observer.Observe(ctx)
	observer.Observe(ctx)
	runner.record = false

	if len(runner.observed) != 4 {
		t.Fatalf("observation issued %d tmux calls, want the fixed budget of 4: %v", len(runner.observed), runner.observed)
	}
	for _, call := range runner.observed {
		joined := strings.Join(call, " ")
		if call[0] != "-L" || call[1] != primary {
			t.Fatalf("observation call %q escaped the exact transport", joined)
		}
		if strings.Contains(joined, sibling) {
			t.Fatalf("observation call %q read the sibling socket", joined)
		}
		for _, arg := range call {
			if arg == "set-option" || arg == "kill-server" || arg == "kill-session" || arg == "new-session" {
				t.Fatalf("observation call %q wrote to tmux", joined)
			}
		}
	}
	if observed.HostMode != resourcegraph.HostModeAppOwned {
		t.Fatalf("host mode = %q, want app-owned", observed.HostMode)
	}
	if len(observed.Unavailable) != 0 {
		t.Fatalf("real server reported %+v", observed.Unavailable)
	}
	if len(observed.Sessions) != 4 {
		t.Fatalf("observed %d sessions, want 4: %+v", len(observed.Sessions), observed.Sessions)
	}

	registry := coremetadata.NewRegistry()
	registry.Projects = []coremetadata.Project{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindProject,
		Metadata: coremetadata.ObjectMeta{UID: "project-alpha", Name: "alpha"},
		Spec:     coremetadata.ProjectSpec{Root: root},
	}}
	registry.Windows = []coremetadata.Window{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
		Metadata: coremetadata.ObjectMeta{UID: "win-alpha-1", Name: "editor",
			OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindProject, UID: "project-alpha"}},
	}}
	registry.Panes = []coremetadata.Pane{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
		Metadata: coremetadata.ObjectMeta{UID: "pane-alpha-1", Name: "shell",
			OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindWindow, UID: "win-alpha-1"}},
		Spec: coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell},
	}}
	graph := resourcegraph.Resolve(registry, observed)
	projector := registryview.NewObservedContextProjector(graph)
	if got := projector.For(coremetadata.KindWindow, "win-alpha-1"); got.Value != "exact-live-window" || got.Source != registryview.ContextSourceLiveWindowName || !got.Observed {
		t.Fatalf("exact live Window context = %+v, want primary socket window_name", got)
	}
	if got := projector.For(coremetadata.KindPane, "pane-alpha-1"); got.Value != "exact-live-pane" || got.Source != registryview.ContextSourceLivePaneTitle || !got.Observed {
		t.Fatalf("exact live Pane context = %+v, want primary socket raw pane_title", got)
	}

	if len(graph.Conflicts) != 0 {
		t.Fatalf("real observation produced conflicts: %+v", graph.Conflicts)
	}
	for _, want := range []struct {
		uid    string
		status resourcegraph.Status
		id     string
	}{
		{uid: "project-alpha", status: resourcegraph.StatusLive},
		{uid: "win-alpha-1", status: resourcegraph.StatusLive, id: windowID},
		{uid: "pane-alpha-1", status: resourcegraph.StatusLive, id: paneID},
	} {
		var status resourcegraph.Status
		var ref *resourcegraph.RuntimeRef
		switch want.uid {
		case "project-alpha":
			status, ref = graph.Projects[0].Status, graph.Projects[0].Runtime
		case "win-alpha-1":
			status, ref = graph.Windows[0].Status, graph.Windows[0].Runtime
		default:
			status, ref = graph.Panes[0].Status, graph.Panes[0].Runtime
		}
		if status != want.status {
			t.Fatalf("%s status = %q, want %q", want.uid, status, want.status)
		}
		if ref == nil {
			t.Fatalf("%s bound no runtime handle", want.uid)
		}
		if want.id != "" && ref.ID != want.id {
			t.Fatalf("%s bound %s, want the exact tmux id %s", want.uid, ref.ID, want.id)
		}
	}

	classes := map[string]resourcegraph.Class{}
	for _, node := range graph.Runtime {
		if node.Ref.Kind == resourcegraph.ObjectSession {
			classes[node.Ref.Name] = node.Class
		}
	}
	wantClasses := map[string]resourcegraph.Class{
		"alpha":   resourcegraph.ClassManaged,
		"home":    resourcegraph.ClassControl,
		"scratch": resourcegraph.ClassEphemeral,
		"plain":   resourcegraph.ClassUnattributed,
	}
	for name, want := range wantClasses {
		if classes[name] != want {
			t.Fatalf("session %s classified %q, want %q (all: %+v)", name, classes[name], want, classes)
		}
	}
	t.Logf("real tmux classes: %+v", classes)

	if after := run(sibling, "list-sessions", "-F", "#{session_name}"); after != siblingSessionsBefore {
		t.Fatalf("sibling sessions changed from %q to %q", siblingSessionsBefore, after)
	}
	if got := run(primary, "list-sessions", "-F", "#{session_name}"); len(strings.Fields(got)) != 4 {
		t.Fatalf("observation changed the primary server's sessions: %q", got)
	}
}

// smokeRunner executes real tmux with the caller's client environment stripped.
// The inherited variables are removed on every call, not once at setup, so no
// invocation can accidentally address the operator's server. Every call also
// passes -f /dev/null, so a server this test starts never loads the operator's
// tmux config and its hooks. The recorded arguments leave it out: they are what
// the observer itself issued.
type smokeRunner struct {
	tmpdir   string
	record   bool
	observed [][]string
}

func (r *smokeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if r.record {
		r.observed = append(r.observed, append([]string(nil), args...))
	}
	cmd := exec.CommandContext(ctx, name, append([]string{"-f", "/dev/null"}, args...)...)
	cmd.Env = smokeEnvironment(r.tmpdir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("tmux %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func smokeEnvironment(tmpdir string) []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "TMUX=") || strings.HasPrefix(entry, "TMUX_PANE=") ||
			strings.HasPrefix(entry, "__PROJMUX_RUNTIME_ANCHOR_PANE=") || strings.HasPrefix(entry, "TMUX_TMPDIR=") {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "TMUX_TMPDIR="+tmpdir)
}

// realTmuxStrictEnv is the switch the CI Unit Tests job sets, the same one
// internal/app's requireRealTmux reads. Only the value "1" enables it.
const realTmuxStrictEnv = "PROJMUX_REAL_TMUX_STRICT"

// requireIsolatedTmuxSmoke is the one gate of this package's isolated
// real-tmux smoke. The smoke runs when its own opt-in variable is set to any
// non-blank value, as it always has, or when realTmuxStrictEnv is "1", and
// skips otherwise. Once either is set, a missing tmux fails naming that
// variable, so a run meant to exercise real tmux cannot pass by skipping.
// TestIsolatedTmuxSmokeSkipsOnlyThroughTheGate keeps every other skip out.
func requireIsolatedTmuxSmoke(t testing.TB, optInEnv string) {
	t.Helper()
	enabledBy := ""
	switch {
	case strings.TrimSpace(os.Getenv(optInEnv)) != "":
		enabledBy = optInEnv
	case os.Getenv(realTmuxStrictEnv) == "1":
		enabledBy = realTmuxStrictEnv
	default:
		t.Skipf("set %s=1 or %s=1 to run this isolated real-tmux smoke", optInEnv, realTmuxStrictEnv)
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Fatalf("%s is set and requires tmux: %v", enabledBy, err)
	}
}

// isolatedTmuxSmokeRoot returns a short private root under /tmp for the
// smoke's TMUX_TMPDIR. t.TempDir() follows TMPDIR, which the CI Unit Tests job
// makes longer than 100 bytes, and the tmux socket below it has a 108-byte
// bound. The root is removed after the smoke's own server cleanup has run.
func isolatedTmuxSmokeRoot(t testing.TB, prefix string) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", prefix)
	if err != nil {
		t.Fatalf("create isolated tmux smoke root: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove isolated tmux smoke root %q: %v", root, err)
		}
	})
	return root
}

// isolatedTmuxSmokeGateRecorder stands in for the test handed to
// requireIsolatedTmuxSmoke so the self-tests can watch a skip or a failure
// without taking either themselves. Skipf and Fatalf end the goroutine the way
// testing does.
type isolatedTmuxSmokeGateRecorder struct {
	testing.TB
	skipped bool
	fatal   string
}

func (r *isolatedTmuxSmokeGateRecorder) Helper() {}

func (r *isolatedTmuxSmokeGateRecorder) Skipf(format string, args ...any) {
	r.skipped = true
	runtime.Goexit()
}

func (r *isolatedTmuxSmokeGateRecorder) Fatalf(format string, args ...any) {
	r.fatal = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

func runIsolatedTmuxSmokeGate(optInEnv string) *isolatedTmuxSmokeGateRecorder {
	recorder := &isolatedTmuxSmokeGateRecorder{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		requireIsolatedTmuxSmoke(recorder, optInEnv)
	}()
	<-done
	return recorder
}

// TestIsolatedTmuxSmokeGateSkipsOrFails pins the gate's answers: without the
// opt-in or strict mode the smoke skips whether or not tmux exists; with either
// one set it runs when tmux exists and fails naming the variable when not.
func TestIsolatedTmuxSmokeGateSkipsOrFails(t *testing.T) {
	const optIn = "PMX_TEST_ISOLATED_TMUX_SMOKE_GATE"
	withTmux := t.TempDir()
	if err := os.WriteFile(filepath.Join(withTmux, "tmux"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	withoutTmux := t.TempDir()

	for _, path := range []string{withTmux, withoutTmux} {
		t.Setenv("PATH", path)
		t.Setenv(optIn, "")
		for _, strict := range []string{"", "0", "true"} {
			t.Setenv(realTmuxStrictEnv, strict)
			recorder := runIsolatedTmuxSmokeGate(optIn)
			if !recorder.skipped || recorder.fatal != "" {
				t.Fatalf("PATH=%s %s=%q: skipped=%v fatal=%q, want a skip", path, realTmuxStrictEnv, strict, recorder.skipped, recorder.fatal)
			}
		}
	}

	for _, enabledBy := range []string{optIn, realTmuxStrictEnv} {
		t.Setenv(optIn, "")
		t.Setenv(realTmuxStrictEnv, "")
		t.Setenv(enabledBy, "1")

		t.Setenv("PATH", withTmux)
		if recorder := runIsolatedTmuxSmokeGate(optIn); recorder.skipped || recorder.fatal != "" {
			t.Fatalf("%s=1 with tmux: skipped=%v fatal=%q, want the smoke to run", enabledBy, recorder.skipped, recorder.fatal)
		}

		t.Setenv("PATH", withoutTmux)
		if _, err := exec.LookPath("tmux"); err == nil {
			t.Fatal("tmux is still reachable on the emptied PATH")
		}
		recorder := runIsolatedTmuxSmokeGate(optIn)
		if recorder.skipped || !strings.HasPrefix(recorder.fatal, enabledBy+" is set and requires tmux") {
			t.Fatalf("%s=1 without tmux: skipped=%v fatal=%q, want a failure naming %s", enabledBy, recorder.skipped, recorder.fatal, enabledBy)
		}
	}
}

// TestIsolatedTmuxSmokeSkipsOnlyThroughTheGate reads this file and requires the
// smoke to open with requireIsolatedTmuxSmoke under its own opt-in variable
// and to call no Skip of its own, so strict mode leaves it no path to a skip.
func TestIsolatedTmuxSmokeSkipsOnlyThroughTheGate(t *testing.T) {
	const smoke, optIn = "TestResolvedResourceGraphRealTmuxSmoke", "PROJMUX_RESOURCEGRAPH_TMUX_SMOKE"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "inventory_smoke_linux_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fn *ast.FuncDecl
	for _, declaration := range file.Decls {
		if candidate, ok := declaration.(*ast.FuncDecl); ok && candidate.Name.Name == smoke && candidate.Body != nil {
			fn = candidate
		}
	}
	if fn == nil {
		t.Fatalf("%s not found", smoke)
	}
	if len(fn.Body.List) == 0 || !isIsolatedTmuxSmokeGateCall(fn.Body.List[0], optIn) {
		t.Errorf("%s must open with requireIsolatedTmuxSmoke(t, %q)", smoke, optIn)
	}
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
			switch selector.Sel.Name {
			case "Skip", "Skipf", "SkipNow":
				t.Errorf("%s: %s calls %s; only requireIsolatedTmuxSmoke may skip", fset.Position(call.Pos()), smoke, selector.Sel.Name)
			}
		}
		return true
	})
}

func isIsolatedTmuxSmokeGateCall(statement ast.Stmt, optIn string) bool {
	expression, ok := statement.(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := expression.X.(*ast.CallExpr)
	if !ok || len(call.Args) != 2 {
		return false
	}
	if name, ok := call.Fun.(*ast.Ident); !ok || name.Name != "requireIsolatedTmuxSmoke" {
		return false
	}
	literal, ok := call.Args[1].(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return false
	}
	value, err := strconv.Unquote(literal.Value)
	return err == nil && value == optIn
}

// TestSmokeEnvironmentStripsTheInheritedClient proves no smoke tmux call can
// address the caller's server: the inherited client variables and TMUX_TMPDIR
// are dropped and the smoke's own TMUX_TMPDIR is the only one.
func TestSmokeEnvironmentStripsTheInheritedClient(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-1000/default,1,0")
	t.Setenv("TMUX_PANE", "%1")
	t.Setenv("__PROJMUX_RUNTIME_ANCHOR_PANE", "%1")
	t.Setenv("TMUX_TMPDIR", "/tmp/caller")
	t.Setenv("PMX_TEST_KEPT", "kept")

	tmuxTmpDirs := 0
	kept := false
	for _, entry := range smokeEnvironment("/tmp/smoke") {
		name, value, _ := strings.Cut(entry, "=")
		switch name {
		case "TMUX", "TMUX_PANE", "__PROJMUX_RUNTIME_ANCHOR_PANE":
			t.Fatalf("smoke environment kept inherited %s", entry)
		case "TMUX_TMPDIR":
			tmuxTmpDirs++
			if value != "/tmp/smoke" {
				t.Fatalf("smoke TMUX_TMPDIR = %q, want /tmp/smoke", value)
			}
		case "PMX_TEST_KEPT":
			kept = true
		}
	}
	if tmuxTmpDirs != 1 || !kept {
		t.Fatalf("smoke environment: TMUX_TMPDIR entries=%d unrelated variable kept=%v", tmuxTmpDirs, kept)
	}
}
