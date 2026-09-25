package liveguard

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

const (
	liveMachineGuardChildEnv = "PMX_TEST_LIVE_MACHINE_GUARD_CHILD"

	// A "spawn-dropped-helper" fixture starts helperModeEnv's mode with an
	// environment of only that mode, plus TMPDIR=helperTmpEnv when set.
	helperModeEnv = "PMX_TEST_LIVEGUARD_HELPER_MODE"
	helperTmpEnv  = "PMX_TEST_LIVEGUARD_HELPER_TMPDIR"

	// A "helper-exits" or "block" fixture reports the root it runs behind
	// after fixtureRootMarker, and a "block" fixture its own pid after
	// fixturePidMarker, so a test finds that exact root rather than globbing
	// by a parent pid other fixtures share.
	fixtureRootMarker = "liveguard fixture root="
	fixturePidMarker  = "liveguard fixture pid="

	// fixtureOptInEnv is the opt-in gate every guard fixture child runs with.
	fixtureOptInEnv = "PMX_TEST_LIVEGUARD_FIXTURE_OPT_IN"
)

func TestMain(m *testing.M) {
	exitIfLiveMachineGuardChild()
	exitIfProviderGuardChild()
	os.Exit(RunTests(m))
}

// TestLiveMachineGuardHolds pins that TestMain runs this package behind the
// guard.
func TestLiveMachineGuardHolds(t *testing.T) { RequireActive(t) }

// exitIfLiveMachineGuardChild runs the guard around a fixed body when the test
// binary is re-executed as a guard fixture, and exits with the guard's code.
func exitIfLiveMachineGuardChild() {
	mode := os.Getenv(liveMachineGuardChildEnv)
	if mode == "" {
		return
	}
	os.Exit(RunGuarded(func() int {
		switch mode {
		case "inherited":
			for _, key := range inheritedEnv {
				if value, ok := os.LookupEnv(key); ok {
					fmt.Fprintf(os.Stderr, "inherited %s=%s survived the guard\n", key, value)
					return 1
				}
			}
			return 0
		case "registry":
			paths, err := config.DefaultPathsFromEnv()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			if _, err := intmetadata.NewDefaultStore(paths).Update(func(*coremetadata.Registry) error { return nil }); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			return 0
		case "state", "state-exit":
			if err := writeGuardFixtureState(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			if mode == "state-exit" {
				// A helper process ends inside m.Run with os.Exit, so
				// nothing the guard deferred runs.
				os.Exit(0)
			}
			return 0
		case "toolchain":
			dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "go", "telemetry", "local")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			if err := os.WriteFile(filepath.Join(dir, "guard-fixture.count"), nil, 0o600); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			return 0
		case "helper-exits":
			fmt.Fprintln(os.Stderr, fixtureRootMarker+activeRoot)
			os.Exit(0)
			return 0
		case "block":
			// Stands for a guarded run that is killed mid-test: it holds its
			// root until stdin closes or it dies.
			fmt.Fprintln(os.Stderr, fixtureRootMarker+activeRoot)
			fmt.Fprintf(os.Stderr, "%s%d\n", fixturePidMarker, os.Getpid())
			_, _ = io.Copy(io.Discard, os.Stdin)
			return 0
		case "codex-exit":
			_ = exec.Command("codex", "app-server", "daemon", "start").Run()
			os.Exit(0)
			return 0
		case "helper-writes-state":
			command := exec.Command(os.Args[0], "-test.run=^$") // #nosec G204 -- re-executes this test binary as a fixed guard fixture.
			command.Env = append(os.Environ(), liveMachineGuardChildEnv+"=state")
			command.Stderr = os.Stderr
			if err := command.Run(); err != nil {
				fmt.Fprintf(os.Stderr, "helper: %v\n", err)
				return 1
			}
			return 0
		case "spawn-dropped-helper":
			// The helper's environment is dropped the way a test that sets
			// cmd.Env = []string{...} drops it: no root, no TMPDIR, no PATH.
			env := []string{liveMachineGuardChildEnv + "=" + os.Getenv(helperModeEnv)}
			if tmp := os.Getenv(helperTmpEnv); tmp != "" {
				env = append(env, "TMPDIR="+tmp)
			}
			command := exec.Command(os.Args[0], "-test.run=^$") // #nosec G204 -- re-executes this test binary as a fixed guard fixture.
			command.Env = env
			command.Stderr = os.Stderr
			if err := command.Run(); err != nil {
				fmt.Fprintf(os.Stderr, "dropped helper: %v\n", err)
				return 1
			}
			return 0
		default:
			return 0
		}
	}, ProviderOptIn(fixtureOptInEnv)))
}

func writeGuardFixtureState() error {
	paths, err := config.DefaultPathsFromEnv()
	if err == nil {
		err = os.MkdirAll(paths.StateDir, 0o700)
	}
	if err == nil {
		err = os.WriteFile(filepath.Join(paths.StateDir, "guard-fixture.json"), []byte("{}\n"), 0o600)
	}
	return err
}

// TestInheritedEnvIsTheGuardList pins that InheritedEnv hands out the list the
// guard unsets, and a copy of it.
func TestInheritedEnvIsTheGuardList(t *testing.T) {
	got := InheritedEnv()
	if strings.Join(got, ",") != strings.Join(inheritedEnv, ",") {
		t.Fatalf("InheritedEnv() = %v, want %v", got, inheritedEnv)
	}
	got[0] = "mutated"
	if inheritedEnv[0] == "mutated" {
		t.Fatal("InheritedEnv returned the guard's own slice")
	}
}

// TestLiveMachineGuardStripsInheritedLiveRouting re-executes the test binary
// with a managed Pane's routing variables set, so the proof does not depend
// on whether the parent itself runs inside tmux.
func TestLiveMachineGuardStripsInheritedLiveRouting(t *testing.T) {
	t.Parallel()

	env := []string{liveMachineGuardChildEnv + "=inherited"}
	for _, key := range inheritedEnv {
		env = append(env, key+"=live-"+strings.ToLower(key))
	}
	child := runLiveMachineGuardChild(t, env)
	if child.code != 0 {
		t.Fatalf("guard child exit = %d, want 0; stderr:\n%s", child.code, child.stderr)
	}
}

// TestLiveMachineGuardFailsAPackageThatReachesTheDefaultRegistry is the
// detection half: a body that writes the Registry at the default path fails
// the run with a named leak, and the same child with a clean body passes.
func TestLiveMachineGuardFailsAPackageThatReachesTheDefaultRegistry(t *testing.T) {
	t.Parallel()

	child := runLiveMachineGuardChild(t, []string{liveMachineGuardChildEnv + "=registry"})
	if child.code != 1 {
		t.Fatalf("guard child exit = %d, want 1; stderr:\n%s", child.code, child.stderr)
	}
	for _, want := range []string{
		"FAIL: a test reached the default projmux Registry",
		filepath.Join("state", config.AppName, "metadata", "registry.json"),
	} {
		if !strings.Contains(child.stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, child.stderr)
		}
	}

	if clean := runLiveMachineGuardChild(t, []string{liveMachineGuardChildEnv + "=clean"}); clean.code != 0 {
		t.Fatalf("clean guard child exit = %d, want 0; stderr:\n%s", clean.code, clean.stderr)
	}
}

// TestLiveMachineGuardFailsAPackageThatWritesAnyDefaultState extends the
// detection past the Registry: a body that writes any other file under a
// default per-user location fails the run with that path, while the go
// command's own telemetry under the private config home does not.
func TestLiveMachineGuardFailsAPackageThatWritesAnyDefaultState(t *testing.T) {
	t.Parallel()

	child := runLiveMachineGuardChild(t, []string{liveMachineGuardChildEnv + "=state"})
	if child.code != 1 {
		t.Fatalf("guard child exit = %d, want 1; stderr:\n%s", child.code, child.stderr)
	}
	for _, want := range []string{
		otherFailLine,
		filepath.Join("state", config.AppName, "guard-fixture.json"),
	} {
		if !strings.Contains(child.stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, child.stderr)
		}
	}
	if strings.Contains(child.stderr, "default projmux Registry") {
		t.Errorf("a state file outside the Registry was reported as a Registry leak:\n%s", child.stderr)
	}

	if toolchain := runLiveMachineGuardChild(t, []string{liveMachineGuardChildEnv + "=toolchain"}); toolchain.code != 0 {
		t.Fatalf("toolchain guard child exit = %d, want 0; stderr:\n%s", toolchain.code, toolchain.stderr)
	}
}

// TestLiveMachineGuardAuditsWhatAHelperProcessWrites pins that a test binary
// re-executed as a helper joins its parent's private root, so what the helper
// writes to a default location fails the parent's run.
func TestLiveMachineGuardAuditsWhatAHelperProcessWrites(t *testing.T) {
	t.Parallel()

	child := runLiveMachineGuardChild(t, []string{liveMachineGuardChildEnv + "=helper-writes-state"})
	if child.code != 1 {
		t.Fatalf("guard child exit = %d, want 1; stderr:\n%s", child.code, child.stderr)
	}
	if want := filepath.Join("state", config.AppName, "guard-fixture.json"); !strings.Contains(child.stderr, want) {
		t.Errorf("stderr lacks the helper's write %q:\n%s", want, child.stderr)
	}
	if strings.Contains(child.stderr, "helper:") {
		t.Errorf("the helper failed its own audit instead of joining the parent's root:\n%s", child.stderr)
	}
}

// TestLiveMachineGuardAuditsAHelperThatDropsItsEnvironment covers a helper
// that cannot join: its environment is dropped entirely, so it makes its own
// root in /tmp, writes a default state file, and ends in os.Exit. The owner
// still reports the write, and nothing of the helper's is left behind.
func TestLiveMachineGuardAuditsAHelperThatDropsItsEnvironment(t *testing.T) {
	t.Parallel()

	child := runLiveMachineGuardChild(t, []string{
		liveMachineGuardChildEnv + "=spawn-dropped-helper",
		helperModeEnv + "=state-exit",
	})
	if child.code != 1 {
		t.Fatalf("guard child exit = %d, want 1; stderr:\n%s", child.code, child.stderr)
	}
	for _, want := range []string{
		otherFailLine,
		filepath.Join("state", config.AppName, "guard-fixture.json"),
	} {
		if !strings.Contains(child.stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, child.stderr)
		}
	}
	if strings.Contains(child.stderr, "dropped helper:") {
		t.Errorf("the dropped helper failed on its own:\n%s", child.stderr)
	}
	requireNoHelperRoots(t, child)
}

// TestLiveMachineGuardSweepsOnlyTheRootsOfItsOwnHelpers proves the sweep is
// what removes a dropped helper's root: the same helper whose parent is not
// the guard owner leaves its root behind.
//
// The control's helper is started by this test binary, so its root is named
// after this binary, a live guarded owner. A later owner's reclaim leaves such
// a root to this binary's own sweep, so it survives until the test looks; a
// root named after a shell that has exited would be free for any concurrent
// guarded run to reclaim first.
func TestLiveMachineGuardSweepsOnlyTheRootsOfItsOwnHelpers(t *testing.T) {
	t.Parallel()

	swept := t.TempDir()
	child := runLiveMachineGuardChild(t, []string{
		liveMachineGuardChildEnv + "=spawn-dropped-helper",
		helperModeEnv + "=helper-exits",
		helperTmpEnv + "=" + swept,
		"TMPDIR=" + swept,
	})
	if child.code != 0 {
		t.Fatalf("owner exit = %d, want 0; stderr:\n%s", child.code, child.stderr)
	}
	requireNoHelperRoots(t, child)
	if root := fixtureReport(t, child.stderr, fixtureRootMarker); root != "" {
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Errorf("the owner left its dropped helper's root %s behind (stat err %v)", root, err)
		}
	}

	control := runLiveMachineGuardChild(t, []string{liveMachineGuardChildEnv + "=helper-exits"})
	if control.code != 0 {
		t.Fatalf("control helper exit = %d, want 0; stderr:\n%s", control.code, control.stderr)
	}
	requireLeftControlRoot(t, control)
}

// TestLiveMachineGuardLeavesNoRootBehindAHelperThatExits pins the cleanup: a
// helper process that ends in os.Exit inside m.Run skips every deferred call,
// so it must not own a root. Joined to an inherited root it leaves nothing in
// its temp directory; the control shows the root a helper that makes its own
// leaves behind where no owner sweeps.
//
// The joined helper runs under its own shell, so a root it wrongly made would
// be named after that shell (guardPrivateRoot) and no parallel test's helper
// shares the name. The control's helper is started by this test binary, whose
// live lock keeps a concurrent guarded run from reclaiming its root, and it
// reports that root so the test checks exactly it.
func TestLiveMachineGuardLeavesNoRootBehindAHelperThatExits(t *testing.T) {
	t.Parallel()

	joined := runLiveMachineGuardChildUnderShell(t, []string{
		liveMachineGuardChildEnv + "=helper-exits",
		rootEnv + "=" + activeRoot,
	})
	if joined.code != 0 {
		t.Fatalf("joined helper exit = %d, want 0; stderr:\n%s", joined.code, joined.stderr)
	}
	if got := fixtureReport(t, joined.stderr, fixtureRootMarker); got != activeRoot {
		t.Errorf("joined helper ran behind %q, want the inherited root %q", got, activeRoot)
	}
	for _, dir := range []string{"/tmp", joined.tmp} {
		if left := ownedRootsIn(t, dir, strconv.Itoa(joined.pid)); len(left) != 0 {
			t.Fatalf("a helper that joined the inherited root left %v", left)
		}
	}

	control := runLiveMachineGuardChild(t, []string{liveMachineGuardChildEnv + "=helper-exits"})
	if control.code != 0 {
		t.Fatalf("control helper exit = %d, want 0; stderr:\n%s", control.code, control.stderr)
	}
	requireLeftControlRoot(t, control)
}

// fixtureReport returns what a fixture's stderr carries after marker on that
// line, and fails t when it carries none.
func fixtureReport(t *testing.T, stderr, marker string) string {
	t.Helper()
	_, after, ok := strings.Cut(stderr, marker)
	value, _, _ := strings.Cut(after, "\n")
	if !ok || value == "" {
		t.Errorf("fixture did not report %q:\n%s", marker, stderr)
	}
	return value
}

// requireLeftControlRoot checks that a control helper this test binary
// started, which made a root of its own and ended in os.Exit, left that root
// under /tmp named after this binary, and removes it when the test ends.
func requireLeftControlRoot(t *testing.T, control guardChild) {
	t.Helper()
	root := fixtureReport(t, control.stderr, fixtureRootMarker)
	if root == "" {
		return
	}
	if want := rootPrefix + "p" + strconv.Itoa(os.Getpid()) + "-"; filepath.Dir(root) != "/tmp" || !strings.HasPrefix(filepath.Base(root), want) {
		t.Fatalf("control helper root = %q, want one under /tmp starting %q", root, want)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if info, err := os.Lstat(root); err != nil || !info.IsDir() {
		t.Fatalf("control helper left no root at %s (err %v); the proof above means nothing without it", root, err)
	}
}

// ownedRootsIn returns the private roots in dir named after parentPid, the
// roots a guarded process started by parentPid makes, and removes them when
// the test ends.
func ownedRootsIn(t *testing.T, dir, parentPid string) []string {
	t.Helper()
	found, err := filepath.Glob(filepath.Join(dir, rootPrefix+"p"+parentPid+"-*"))
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range found {
		t.Cleanup(func() { _ = os.RemoveAll(root) })
	}
	return found
}

type guardChild struct {
	code   int
	stderr string
	pid    int
	tmp    string
}

// runLiveMachineGuardChild re-executes the test binary as a guard fixture. The
// child starts without this run's private root, so it makes and audits its own
// unless env hands one back. Unless env names a TMPDIR, the child gets a
// private one.
func runLiveMachineGuardChild(t *testing.T, env []string) guardChild {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^$") // #nosec G204 -- re-executes this test binary as a fixed guard fixture.
	return runGuardChildCommand(t, command, env)
}

// runLiveMachineGuardChildUnderShell is runLiveMachineGuardChild with the
// fixture started by /bin/sh; the returned pid is the shell's, the parent a
// root the fixture makes is named after.
func runLiveMachineGuardChildUnderShell(t *testing.T, env []string) guardChild {
	t.Helper()
	command := exec.Command("/bin/sh", "-c", `"$0" -test.run='^$'; exit $?`, os.Args[0]) // #nosec G204 -- re-executes this test binary as a fixed guard fixture.
	return runGuardChildCommand(t, command, env)
}

func runGuardChildCommand(t *testing.T, command *exec.Cmd, env []string) guardChild {
	t.Helper()
	child := guardChild{tmp: t.TempDir()}
	command.Env = append(append(os.Environ(), rootEnv+"=", "TMPDIR="+child.tmp), env...)
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, "TMPDIR="); ok {
			child.tmp = value
		}
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	err := command.Run()
	child.stderr = stderr.String()
	if command.Process != nil {
		child.pid = command.Process.Pid
	}
	if err == nil {
		return child
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("run guard child: %v", err)
	}
	child.code = exitErr.ExitCode()
	return child
}

// requireNoHelperRoots checks that no root a helper of child made is left in
// /tmp or in the child's temp directory.
func requireNoHelperRoots(t *testing.T, child guardChild) {
	t.Helper()
	pattern := rootPrefix + "p" + strconv.Itoa(child.pid) + "-*"
	for _, dir := range []string{"/tmp", child.tmp} {
		left, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			t.Fatal(err)
		}
		if len(left) != 0 {
			t.Errorf("the owner left its dropped helper's root behind: %v", left)
		}
	}
}

// TestLiveMachineGuardRootIsUnderTmpWhateverTMPDIR pins where a new private
// root goes: under /tmp even when TMPDIR is long, because the root holds
// TMUX_TMPDIR and XDG_STATE_HOME and the sockets under them must fit the unix
// socket path bound. The owner's sweep still finds a helper root there.
//
// It is not parallel: guardSweepHelperRoots sweeps every root named after this
// process, which includes the live roots of the guard fixtures the parallel
// tests start.
func TestLiveMachineGuardRootIsUnderTmpWhateverTMPDIR(t *testing.T) {
	longTmp := filepath.Join(t.TempDir(), strings.Repeat("x", 100))
	if err := os.Mkdir(longTmp, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", longTmp)
	t.Setenv(rootEnv, "")

	root, owned, lock, err := guardPrivateRoot()
	if err != nil {
		t.Fatalf("guardPrivateRoot() error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(root)
		_ = lock.Close()
	})
	if !owned {
		t.Errorf("guardPrivateRoot() owned = false without an inherited root")
	}
	if got := filepath.Dir(root); got != "/tmp" {
		t.Errorf("guardPrivateRoot() = %q, want a root directly under /tmp, not under TMPDIR %q", root, longTmp)
	}
	if want := rootPrefix + "p" + strconv.Itoa(os.Getppid()) + "-"; !strings.HasPrefix(filepath.Base(root), want) {
		t.Errorf("guardPrivateRoot() = %q, want a name starting %q", root, want)
	}

	helperRoot, err := os.MkdirTemp("/tmp", rootPrefix+"p"+strconv.Itoa(os.Getpid())+"-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(helperRoot) })
	if failed := guardSweepHelperRoots(); failed {
		t.Errorf("guardSweepHelperRoots() = true, want the empty helper root %s to pass its audit", helperRoot)
	}
	if _, err := os.Stat(helperRoot); !os.IsNotExist(err) {
		t.Errorf("guardSweepHelperRoots() left the helper root %s under /tmp (stat err %v)", helperRoot, err)
	}
}
