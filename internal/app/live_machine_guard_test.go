package app

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// A developer or agent usually runs `make test` from inside a managed Pane.
// That shell carries the live tmux client (TMUX, TMUX_PANE), the Pane anchor
// the runtime mutation routes read before TMUX_PANE, the activation handles a
// provider Pane receives, and a HOME and XDG layout whose state directory holds
// the live Registry. Many tests in this package call the real dispatcher, and a
// test that forgets to inject a path resolves those defaults: it can reach the
// live tmux server and read or write the live Registry.
//
// runWithLiveMachineGuard removes that inheritance for the whole test binary
// before any test runs, and fails the package when a test still reaches the
// default Registry location.

// liveMachineGuardInheritedEnv names the variables through which a process
// inherits a live tmux client or a managed Pane's identity. The guard unsets
// them; a test that needs one sets its own value.
var liveMachineGuardInheritedEnv = []string{
	"TMUX",
	"TMUX_PANE",
	runtimeMutationAnchorPaneEnv,
	internalActivationPaneUIDEnv,
	internalActivationGenerationEnv,
	internalClaudeRegistryPathEnv,
}

// liveMachineGuardPrivateEnv maps each variable that locates per-user state to
// its directory under the guard's private root. XDG_STATE_HOME and HOME are
// the two ways the default Registry path is resolved; TMUX_TMPDIR is where a
// socket-name tmux call puts its server.
var liveMachineGuardPrivateEnv = []struct {
	key  string
	leaf string
}{
	{"HOME", "home"},
	{"XDG_CONFIG_HOME", "config"},
	{"XDG_STATE_HOME", "state"},
	{"XDG_CACHE_HOME", "cache"},
	{"XDG_DATA_HOME", "data"},
	{"TMUX_TMPDIR", "tmux"},
}

// liveMachineGuardToolchainEnv are the go command locations that default to
// HOME or XDG_CACHE_HOME. Tests that build the binary with `go build` would
// otherwise start from an empty module cache under the private HOME.
var liveMachineGuardToolchainEnv = []string{"GOPATH", "GOCACHE", "GOMODCACHE", "GOENV"}

// liveMachineGuardRoot is the private root of the running guard, or "" when
// the package runs without it.
var liveMachineGuardRoot string

const liveMachineGuardChildEnv = "PMX_TEST_LIVE_MACHINE_GUARD_CHILD"

func runWithLiveMachineGuard(run func() int) int {
	root, err := os.MkdirTemp("", "pmx-live-guard-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "live machine guard: create private root: %v\n", err)
		return 1
	}
	defer os.RemoveAll(root)

	// Resolve the go command's own locations while HOME is still the
	// caller's, so they stay the caller's after HOME moves.
	pinLiveMachineGuardToolchainEnv()
	for _, key := range liveMachineGuardInheritedEnv {
		if err := os.Unsetenv(key); err != nil {
			fmt.Fprintf(os.Stderr, "live machine guard: unset %s: %v\n", key, err)
			return 1
		}
	}
	for _, entry := range liveMachineGuardPrivateEnv {
		dir := filepath.Join(root, entry.leaf)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			fmt.Fprintf(os.Stderr, "live machine guard: create %s: %v\n", entry.key, err)
			return 1
		}
		if err := os.Setenv(entry.key, dir); err != nil {
			fmt.Fprintf(os.Stderr, "live machine guard: set %s: %v\n", entry.key, err)
			return 1
		}
	}
	liveMachineGuardRoot = root
	defer func() { liveMachineGuardRoot = "" }()

	code := run()
	if leaked := liveMachineGuardLeaks(root); len(leaked) > 0 {
		fmt.Fprintln(os.Stderr, "FAIL: a test reached the default projmux Registry; inject a Registry path")
		for _, line := range leaked {
			fmt.Fprintln(os.Stderr, "  "+line)
		}
		if code == 0 {
			code = 1
		}
	}
	return code
}

// pinLiveMachineGuardToolchainEnv exports the go command's current locations
// for the variables the caller left unset. Without a go command it does
// nothing; the tests that need one fail the way they would anyway.
func pinLiveMachineGuardToolchainEnv() {
	var missing []string
	for _, key := range liveMachineGuardToolchainEnv {
		if _, ok := os.LookupEnv(key); !ok {
			missing = append(missing, key)
		}
	}
	if len(missing) == 0 {
		return
	}
	out, err := exec.Command("go", append([]string{"env"}, missing...)...).Output() // #nosec G204 -- fixed go executable; argv is fixed variable names.
	if err != nil {
		return
	}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for i := 0; scanner.Scan() && i < len(missing); i++ {
		if value := strings.TrimSpace(scanner.Text()); value != "" {
			_ = os.Setenv(missing[i], value)
		}
	}
}

// liveMachineGuardLeaks lists every file a test left in a Registry state
// directory under root, relative to root. Reaching it at all, even to create a
// lock beside a missing registry, means the test resolved the default path.
func liveMachineGuardLeaks(root string) []string {
	var leaked []string
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			leaked = append(leaked, fmt.Sprintf("unreadable guard path: %v", err))
			return nil
		}
		if entry.IsDir() || filepath.Base(filepath.Dir(path)) != "metadata" || filepath.Base(filepath.Dir(filepath.Dir(path))) != config.AppName {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		leaked = append(leaked, rel)
		return nil
	})
	return leaked
}

// exitIfLiveMachineGuardChild runs the guard around a fixed body when the test
// binary is re-executed as a guard fixture, and exits with the guard's code.
func exitIfLiveMachineGuardChild() {
	mode := os.Getenv(liveMachineGuardChildEnv)
	if mode == "" {
		return
	}
	os.Exit(runWithLiveMachineGuard(func() int {
		switch mode {
		case "inherited":
			for _, key := range liveMachineGuardInheritedEnv {
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
		default:
			return 0
		}
	}))
}

// TestLiveMachineGuardHoldsForThePackage pins that TestMain runs this package
// behind the guard: nothing inherited routes to a live tmux client or Pane, and
// every per-user location, the default Registry path included, is private.
func TestLiveMachineGuardHoldsForThePackage(t *testing.T) {
	for _, key := range liveMachineGuardInheritedEnv {
		if value, ok := os.LookupEnv(key); ok {
			t.Errorf("%s=%q is set; the guard must unset inherited live routing", key, value)
		}
	}
	root := liveMachineGuardRoot
	if root == "" {
		t.Fatal("TestMain did not run the package behind runWithLiveMachineGuard")
	}
	for _, entry := range liveMachineGuardPrivateEnv {
		if got, want := os.Getenv(entry.key), filepath.Join(root, entry.leaf); got != want {
			t.Errorf("%s = %q, want the private %q", entry.key, got, want)
		}
	}
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if registry := intmetadata.PathFor(paths.StateDir); !strings.HasPrefix(registry, root+string(filepath.Separator)) {
		t.Errorf("default Registry path = %q, want it under the private root %q", registry, root)
	}
}

// TestLiveMachineGuardStripsInheritedLiveRouting re-executes the test binary
// with a managed Pane's routing variables set, so the proof does not depend
// on whether the parent itself runs inside tmux.
func TestLiveMachineGuardStripsInheritedLiveRouting(t *testing.T) {
	t.Parallel()

	env := []string{liveMachineGuardChildEnv + "=inherited"}
	for _, key := range liveMachineGuardInheritedEnv {
		env = append(env, key+"=live-"+strings.ToLower(key))
	}
	code, stderr := runLiveMachineGuardChild(t, env)
	if code != 0 {
		t.Fatalf("guard child exit = %d, want 0; stderr:\n%s", code, stderr)
	}
}

// TestLiveMachineGuardFailsAPackageThatReachesTheDefaultRegistry is the
// detection half: a body that writes the Registry at the default path fails
// the run with a named leak, and the same child with a clean body passes.
func TestLiveMachineGuardFailsAPackageThatReachesTheDefaultRegistry(t *testing.T) {
	t.Parallel()

	code, stderr := runLiveMachineGuardChild(t, []string{liveMachineGuardChildEnv + "=registry"})
	if code != 1 {
		t.Fatalf("guard child exit = %d, want 1; stderr:\n%s", code, stderr)
	}
	for _, want := range []string{
		"FAIL: a test reached the default projmux Registry",
		filepath.Join("state", config.AppName, "metadata", "registry.json"),
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}

	if code, stderr := runLiveMachineGuardChild(t, []string{liveMachineGuardChildEnv + "=clean"}); code != 0 {
		t.Fatalf("clean guard child exit = %d, want 0; stderr:\n%s", code, stderr)
	}
}

func runLiveMachineGuardChild(t *testing.T, env []string) (int, string) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^$") // #nosec G204 -- re-executes this test binary as a fixed guard fixture.
	command.Env = append(os.Environ(), env...)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	err := command.Run()
	if err == nil {
		return 0, stderr.String()
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("run guard child: %v", err)
	}
	return exitErr.ExitCode(), stderr.String()
}
