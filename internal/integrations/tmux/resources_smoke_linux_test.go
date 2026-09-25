//go:build linux

package tmux

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
	"time"

	"github.com/crevissepartners/projmux/internal/core/resources"
	"github.com/crevissepartners/projmux/internal/integrations/procfsresources"
)

// TestResourceAttributionRealTmuxReadOnlySmoke is opt-in because it observes
// the caller's existing tmux server. It performs no tmux or process mutation
// and reports count-only evidence.
func TestResourceAttributionRealTmuxReadOnlySmoke(t *testing.T) {
	socket := strings.TrimSpace(os.Getenv("PROJMUX_RESOURCE_TMUX_SOCKET"))
	if socket == "" {
		t.Skip("set PROJMUX_RESOURCE_TMUX_SOCKET to an existing tmux -L socket")
	}
	ctx := context.Background()
	client := NewClient(resourceSmokeRunner{socket: socket}, WithSocketName(socket))
	inventory, err := client.ListResourcePanes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory) == 0 {
		t.Fatal("resource inventory is empty")
	}
	expectedProject := strings.TrimSpace(os.Getenv("PROJMUX_RESOURCE_EXPECT_PROJECT_ROOT"))
	fallbackViews := 0
	if expectedProject != "" {
		resolved := resources.ResolveProjectAnchors(inventory, []string{expectedProject})
		for i := range resolved {
			if strings.TrimSpace(inventory[i].ProjectAnchor) == "" && resolved[i].ProjectAnchor == expectedProject {
				fallbackViews++
			}
		}
		if fallbackViews == 0 {
			t.Fatalf("no blank-anchor pane current path resolved to expected project %q", expectedProject)
		}
		inventory = resolved
	}
	collector := procfsresources.Collector{}
	previous := collector.Sample(ctx)
	time.Sleep(125 * time.Millisecond)
	snapshot, current := collector.Collect(ctx, inventory, &previous)
	if !current.Available || snapshot.Status == "unavailable" || snapshot.Status == "warming" {
		t.Fatalf("snapshot status = %q reason=%q", snapshot.Status, snapshot.StatusReason)
	}
	processesByPID := make(map[int]int, len(current.Processes))
	for _, process := range current.Processes {
		processesByPID[process.Identity.PID] = process.SessionID
	}
	paneSIDMatches := 0
	missingPanePIDs := 0
	for _, pane := range inventory {
		sid, ok := processesByPID[pane.PanePID]
		if !ok {
			missingPanePIDs++
			continue
		}
		if sid == pane.PanePID {
			paneSIDMatches++
		}
	}
	attributedProcesses := 0
	for _, pane := range snapshot.Panes {
		attributedProcesses += pane.ProcessCount
	}
	expectedProjectPanes := 0
	if expectedProject != "" {
		for _, project := range snapshot.Projects {
			if project.Key == expectedProject {
				expectedProjectPanes = project.PaneCount
				break
			}
		}
		if expectedProjectPanes == 0 {
			t.Fatalf("expected project %q has no attributed pane bucket: fallback_views=%d", expectedProject, fallbackViews)
		}
	}
	t.Logf("sanitized resource smoke: panes=%d pane_pid_eq_sid=%d missing_pids=%d attributed_processes=%d fallback_views=%d expected_project_panes=%d expected_project=%q escaped_boundary=%d sampled=%d skipped=%d race=%d permission=%d status=%s",
		len(inventory), paneSIDMatches, missingPanePIDs, attributedProcesses,
		fallbackViews, expectedProjectPanes, expectedProject,
		snapshot.Diagnostics.EscapedProcessCount, current.Diagnostics.SampledProcesses,
		current.Diagnostics.SkippedProcesses, current.Diagnostics.RaceCount,
		current.Diagnostics.PermissionCount, snapshot.Status)
	if missingPanePIDs != 0 || paneSIDMatches != len(inventory) {
		t.Fatalf("pane PID/SID contract mismatch: panes=%d matches=%d missing=%d", len(inventory), paneSIDMatches, missingPanePIDs)
	}
	if attributedProcesses < len(inventory) {
		t.Fatalf("attributed process count %d is smaller than pane count %d", attributedProcesses, len(inventory))
	}
}

// TestResourceAttributionTransientSetsidSmoke uses a disposable real tmux
// server to create a positive setsid boundary. It never touches an existing
// server and kills the isolated server during cleanup.
func TestResourceAttributionTransientSetsidSmoke(t *testing.T) {
	requireIsolatedTmuxSmoke(t, "PROJMUX_RESOURCE_TRANSIENT_SMOKE")
	smokeRoot := isolatedTmuxSmokeRoot(t, "pmx-rsetsid-")
	socket := "projmux-resource-setsid-" + filepath.Base(smokeRoot)
	ctx := context.Background()
	runner := resourceSmokeRunner{socket: socket, tmuxTmpDir: smokeRoot, configFile: "/dev/null"}
	if output, err := runner.Run(ctx, "tmux", "new-session", "-d", "-s", "resource-smoke", "sh", "-c", "setsid sleep 20 & wait"); err != nil {
		t.Fatalf("start transient tmux: %v: %s", err, output)
	}
	t.Cleanup(func() { cleanupIsolatedResourceSmoke(t, runner, smokeRoot) })
	client := NewClient(runner, WithSocketName(socket))
	collector := procfsresources.Collector{}

	deadline := time.Now().Add(3 * time.Second)
	for {
		inventory, err := client.ListResourcePanes(ctx)
		if err != nil {
			t.Fatal(err)
		}
		current := collector.Sample(ctx)
		snapshot := resources.BuildSnapshot(inventory, nil, current)
		if snapshot.Diagnostics.EscapedProcessCount > 0 {
			attributed := 0
			for _, pane := range snapshot.Panes {
				attributed += pane.ProcessCount
			}
			t.Logf("sanitized transient setsid smoke: panes=%d attributed_processes=%d escaped_boundary=%d sampled=%d skipped=%d race=%d permission=%d",
				len(inventory), attributed, snapshot.Diagnostics.EscapedProcessCount,
				current.Diagnostics.SampledProcesses, current.Diagnostics.SkippedProcesses,
				current.Diagnostics.RaceCount, current.Diagnostics.PermissionCount)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("setsid escape not observed before deadline: diagnostics=%#v", snapshot.Diagnostics)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// TestResourceProjectFallbackTransientSmoke creates an isolated tmux server
// whose pane has no project option, then proves that the typed current path is
// resolved in memory without writing any tmux metadata.
func TestResourceProjectFallbackTransientSmoke(t *testing.T) {
	requireIsolatedTmuxSmoke(t, "PROJMUX_RESOURCE_PROJECT_FALLBACK_SMOKE")

	smokeRoot := isolatedTmuxSmokeRoot(t, "pmx-rfallback-")
	currentPath, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	projectRoot := filepath.Clean(filepath.Join(currentPath, "..", "..", ".."))
	socket := "projmux-resource-fallback-" + filepath.Base(smokeRoot)
	runner := resourceSmokeRunner{socket: socket, tmuxTmpDir: smokeRoot, configFile: "/dev/null"}
	ctx := context.Background()
	if output, err := runner.Run(ctx, "tmux", "new-session", "-d", "-s", "resource-fallback-smoke", "-c", currentPath, "/usr/bin/sleep 20"); err != nil {
		t.Fatalf("start fallback tmux: %v: %s", err, output)
	}
	t.Cleanup(func() { cleanupIsolatedResourceSmoke(t, runner, smokeRoot) })

	client := NewClient(runner, WithSocketName(socket))
	inventory, err := client.ListResourcePanes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory) != 1 || inventory[0].CurrentPath != currentPath || inventory[0].ProjectAnchor != "" {
		t.Fatalf("isolated inventory = %#v, want one blank-anchor pane at configured cwd", inventory)
	}
	resolved := resources.ResolveProjectAnchors(inventory, []string{projectRoot})
	if resolved[0].ProjectAnchor != projectRoot {
		t.Fatalf("resolved project anchor = %q, want %q", resolved[0].ProjectAnchor, projectRoot)
	}

	current := (procfsresources.Collector{}).Sample(ctx)
	snapshot := resources.BuildSnapshot(resolved, nil, current)
	if len(snapshot.Projects) != 1 || snapshot.Projects[0].Key != projectRoot || snapshot.Projects[0].PaneCount != 1 {
		t.Fatalf("isolated project buckets = %#v, want one fallback-attributed pane", snapshot.Projects)
	}
	after, err := client.ListResourcePanes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0].ProjectAnchor != "" {
		t.Fatalf("fallback mutated tmux project metadata: %#v", after)
	}
	t.Logf("sanitized fallback smoke: panes=%d fallback_project_panes=%d project=%q", len(after), snapshot.Projects[0].PaneCount, projectRoot)
}

// realTmuxStrictEnv is the switch the CI Unit Tests job sets, the same one
// internal/app's requireRealTmux reads. Only the value "1" enables it.
const realTmuxStrictEnv = "PROJMUX_REAL_TMUX_STRICT"

// requireIsolatedTmuxSmoke is the one gate of this package's isolated
// real-tmux smokes. A smoke runs when its own opt-in variable or
// realTmuxStrictEnv is "1" and skips otherwise. Once either is set, a missing
// tmux fails naming that variable, so a run meant to exercise real tmux cannot
// pass by skipping. TestIsolatedTmuxSmokesSkipOnlyThroughTheGate keeps every
// other skip out of those smokes.
func requireIsolatedTmuxSmoke(t testing.TB, optInEnv string) {
	t.Helper()
	enabledBy := ""
	switch {
	case os.Getenv(optInEnv) == "1":
		enabledBy = optInEnv
	case os.Getenv(realTmuxStrictEnv) == "1":
		enabledBy = realTmuxStrictEnv
	default:
		t.Skipf("set %s=1 or %s=1 to run this isolated real-tmux smoke", optInEnv, realTmuxStrictEnv)
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Fatalf("%s=1 requires tmux: %v", enabledBy, err)
	}
}

// isolatedTmuxSmokeRoot returns a short private root under /tmp for a smoke's
// TMUX_TMPDIR. t.TempDir() follows TMPDIR, which the CI Unit Tests job makes
// longer than 100 bytes, and the tmux socket below it has a 108-byte bound.
// The root is removed after the smoke's own server cleanup has run.
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

type resourceSmokeRunner struct {
	socket     string
	tmuxTmpDir string
	configFile string
}

func (r resourceSmokeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	prefix := []string{"-L", r.socket}
	if r.configFile != "" {
		prefix = append(prefix, "-f", r.configFile)
	}
	args = append(prefix, args...)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = resourceSmokeEnvironment(r.tmuxTmpDir)
	return cmd.CombinedOutput()
}

func resourceSmokeEnvironment(tmuxTmpDir string) []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if name == "TMUX" || name == "TMUX_PANE" || name == "__PROJMUX_RUNTIME_ANCHOR_PANE" || tmuxTmpDir != "" && name == "TMUX_TMPDIR" {
			continue
		}
		env = append(env, entry)
	}
	if tmuxTmpDir != "" {
		env = append(env, "TMUX_TMPDIR="+tmuxTmpDir)
	}
	return env
}

func cleanupIsolatedResourceSmoke(t *testing.T, runner resourceSmokeRunner, smokeRoot string) {
	t.Helper()
	output, err := runner.Run(context.Background(), "tmux", "display-message", "-p", "#{socket_path}")
	if err != nil {
		t.Errorf("query isolated tmux socket before cleanup: %v: %s", err, output)
		return
	}
	socketPath := strings.TrimSpace(string(output))
	rel, err := filepath.Rel(smokeRoot, socketPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Errorf("refuse cleanup outside smoke root %q: socket=%q", smokeRoot, socketPath)
		return
	}
	if output, err := runner.Run(context.Background(), "tmux", "kill-server"); err != nil {
		t.Errorf("kill isolated tmux server %q: %v: %s", socketPath, err, output)
	}
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
// opt-in or strict mode a smoke skips whether or not tmux exists; with either
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
		for _, value := range []string{"", "0", "true"} {
			t.Setenv(optIn, value)
			t.Setenv(realTmuxStrictEnv, value)
			recorder := runIsolatedTmuxSmokeGate(optIn)
			if !recorder.skipped || recorder.fatal != "" {
				t.Fatalf("PATH=%s %s=%q %s=%q: skipped=%v fatal=%q, want a skip", path, optIn, value, realTmuxStrictEnv, value, recorder.skipped, recorder.fatal)
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
		if recorder.skipped || !strings.HasPrefix(recorder.fatal, enabledBy+"=1 requires tmux") {
			t.Fatalf("%s=1 without tmux: skipped=%v fatal=%q, want a failure naming %s", enabledBy, recorder.skipped, recorder.fatal, enabledBy)
		}
	}
}

// TestIsolatedTmuxSmokesSkipOnlyThroughTheGate reads this file and requires
// each isolated smoke to open with requireIsolatedTmuxSmoke under its own
// opt-in variable and to call no Skip of its own, so strict mode leaves it no
// path to a skip.
func TestIsolatedTmuxSmokesSkipOnlyThroughTheGate(t *testing.T) {
	smokes := map[string]string{
		"TestResourceAttributionTransientSetsidSmoke": "PROJMUX_RESOURCE_TRANSIENT_SMOKE",
		"TestResourceProjectFallbackTransientSmoke":   "PROJMUX_RESOURCE_PROJECT_FALLBACK_SMOKE",
	}
	auditIsolatedTmuxSmokeGate(t, "resources_smoke_linux_test.go", smokes)
}

func auditIsolatedTmuxSmokeGate(t *testing.T, filename string, smokes map[string]string) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, declaration := range file.Decls {
		fn, ok := declaration.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		optIn, ok := smokes[fn.Name.Name]
		if !ok {
			continue
		}
		seen++
		if len(fn.Body.List) == 0 || !isIsolatedTmuxSmokeGateCall(fn.Body.List[0], optIn) {
			t.Errorf("%s must open with requireIsolatedTmuxSmoke(t, %q)", fn.Name.Name, optIn)
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
				switch selector.Sel.Name {
				case "Skip", "Skipf", "SkipNow":
					t.Errorf("%s: %s calls %s; only requireIsolatedTmuxSmoke may skip", fset.Position(call.Pos()), fn.Name.Name, selector.Sel.Name)
				}
			}
			return true
		})
	}
	if seen != len(smokes) {
		t.Fatalf("audited %d of %d smokes in %s", seen, len(smokes), filename)
	}
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

// TestResourceSmokeEnvironmentStripsTheInheritedClient proves no smoke tmux
// call can address the caller's server: the inherited client variables and
// TMUX_TMPDIR are dropped and the smoke's own TMUX_TMPDIR is the only one.
func TestResourceSmokeEnvironmentStripsTheInheritedClient(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-1000/default,1,0")
	t.Setenv("TMUX_PANE", "%1")
	t.Setenv("__PROJMUX_RUNTIME_ANCHOR_PANE", "%1")
	t.Setenv("TMUX_TMPDIR", "/tmp/caller")
	t.Setenv("PMX_TEST_KEPT", "kept")

	env := resourceSmokeEnvironment("/tmp/smoke")
	tmuxTmpDirs := 0
	kept := false
	for _, entry := range env {
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
