package liveguard

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const providerGuardChildEnv = "PMX_TEST_PROVIDER_GUARD_CHILD"

// exitIfProviderGuardChild runs the guard around a fixed body when the test
// binary is re-executed as a guard fixture, and exits with the guard's code.
func exitIfProviderGuardChild() {
	mode := os.Getenv(providerGuardChildEnv)
	if mode == "" {
		return
	}
	pathBefore := os.Getenv("PATH")
	os.Exit(RunGuarded(func() int {
		fmt.Fprintf(os.Stderr, "provider guard dir: %s\n", activeProviderDir)
		switch mode {
		case "runs-codex":
			_ = exec.Command("codex", "app-server", "daemon", "start").Run()
			return 0
		case "opted-in":
			if got := os.Getenv("PATH"); got != pathBefore {
				fmt.Fprintf(os.Stderr, "PATH changed while opted in: %q\n", got)
				return 1
			}
			if _, err := os.Stat(filepath.Join(activeRoot, providerDirName)); !os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "stand-ins were created while opted in (stat err %v)\n", err)
				return 1
			}
			for _, name := range []string{"codex", "claude"} {
				if resolved, err := exec.LookPath(name); err == nil && isGuardStandInDir(filepath.Dir(resolved)) {
					fmt.Fprintf(os.Stderr, "%s resolved to a guard stand-in %s while opted in\n", name, resolved)
					return 1
				}
			}
			return 0
		default:
			return 0
		}
	}, ProviderOptIn(fixtureOptInEnv)))
}

// isGuardStandInDir reports whether dir is a guard's stand-in directory.
func isGuardStandInDir(dir string) bool {
	return filepath.Base(dir) == providerDirName && strings.HasPrefix(filepath.Base(filepath.Dir(dir)), rootPrefix)
}

// TestProviderGuardStandsInForCodexAndClaude pins that TestMain runs this
// package behind the guard: both CLIs resolve to its stand-ins under the
// private root.
func TestProviderGuardStandsInForCodexAndClaude(t *testing.T) {
	dir := activeProviderDir
	if dir == "" {
		t.Fatal("TestMain did not run the package behind liveguard.RunTests")
	}
	if want := filepath.Join(activeRoot, providerDirName); dir != want {
		t.Errorf("stand-in dir = %q, want %q under the private root", dir, want)
	}
	for _, name := range []string{"codex", "claude"} {
		resolved, err := exec.LookPath(name)
		if err != nil {
			t.Errorf("look up %s: %v", name, err)
			continue
		}
		if filepath.Dir(resolved) != dir {
			t.Errorf("%s resolves to %q, want the guard stand-in under %q", name, resolved, dir)
		}
	}
}

// TestProviderGuardFailsAPackageThatRunsTheRealProvider is the detection half:
// a body that runs codex and swallows the error still fails the run with the
// recorded argv, a clean body passes, and an opted-in run leaves PATH alone.
func TestProviderGuardFailsAPackageThatRunsTheRealProvider(t *testing.T) {
	t.Parallel()

	code, stderr := runProviderGuardChild(t, "runs-codex")
	if code != 1 {
		t.Fatalf("guard child exit = %d, want 1; stderr:\n%s", code, stderr)
	}
	for _, want := range []string{providerFailLine, "  codex app-server daemon start"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	assertProviderGuardChildCleanedUp(t, stderr)

	code, stderr = runProviderGuardChild(t, "clean")
	if code != 0 {
		t.Fatalf("clean guard child exit = %d, want 0; stderr:\n%s", code, stderr)
	}
	assertProviderGuardChildCleanedUp(t, stderr)

	code, stderr = runProviderGuardChild(t, "opted-in", fixtureOptInEnv+"=/nonexistent/provider-guard-opt-in")
	if code != 0 {
		t.Fatalf("opted-in guard child exit = %d, want 0; stderr:\n%s", code, stderr)
	}
	if want := "provider guard: stepped aside; " + fixtureOptInEnv; !strings.Contains(stderr, want) {
		t.Errorf("stderr lacks %q:\n%s", want, stderr)
	}
}

// TestProviderGuardAuditsAHelperThatDropsItsEnvironment covers a helper that
// cannot join: with its environment dropped it installs stand-ins in a root of
// its own, runs codex, and ends in os.Exit. The owner still fails the run with
// the recorded argv and removes the helper's root.
func TestProviderGuardAuditsAHelperThatDropsItsEnvironment(t *testing.T) {
	t.Parallel()

	child := runLiveMachineGuardChild(t, []string{
		liveMachineGuardChildEnv + "=spawn-dropped-helper",
		helperModeEnv + "=codex-exit",
	})
	if child.code != 1 {
		t.Fatalf("guard child exit = %d, want 1; stderr:\n%s", child.code, child.stderr)
	}
	for _, want := range []string{providerFailLine, "  codex app-server daemon start"} {
		if !strings.Contains(child.stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, child.stderr)
		}
	}
	if strings.Contains(child.stderr, "dropped helper:") {
		t.Errorf("the dropped helper failed on its own:\n%s", child.stderr)
	}
	requireNoHelperRoots(t, child)
}

// runProviderGuardChild re-executes the test binary as a guard fixture. The
// child starts from the parent's environment without the parent's own guard
// or any opt-in gate, so neither can flip the result.
func runProviderGuardChild(t *testing.T, mode string, extraEnv ...string) (int, string) {
	t.Helper()
	var env []string
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		switch {
		case key == providerGuardChildEnv || key == rootEnv || key == fixtureOptInEnv:
			continue
		case key == "PATH":
			var kept []string
			for _, dir := range filepath.SplitList(value) {
				if !isGuardStandInDir(dir) {
					kept = append(kept, dir)
				}
			}
			entry = "PATH=" + strings.Join(kept, string(os.PathListSeparator))
		}
		env = append(env, entry)
	}
	env = append(env, providerGuardChildEnv+"="+mode, "TMPDIR="+t.TempDir())
	env = append(env, extraEnv...)

	command := exec.Command(os.Args[0], "-test.run=^$") // #nosec G204 -- re-executes this test binary as a fixed guard fixture.
	command.Env = env
	var stderr bytes.Buffer
	command.Stderr = &stderr
	err := command.Run()
	if err == nil {
		return 0, stderr.String()
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("run provider guard child: %v", err)
	}
	return exitErr.ExitCode(), stderr.String()
}

// assertProviderGuardChildCleanedUp checks that the stand-in dir the child
// reported, and the private root around it, are gone once the child exits.
func assertProviderGuardChildCleanedUp(t *testing.T, stderr string) {
	t.Helper()
	const marker = "provider guard dir: "
	_, after, ok := strings.Cut(stderr, marker)
	if !ok {
		t.Errorf("guard child did not report its stand-in dir:\n%s", stderr)
		return
	}
	dir, _, _ := strings.Cut(after, "\n")
	if dir == "" {
		t.Errorf("guard child ran without a stand-in dir:\n%s", stderr)
		return
	}
	for _, path := range []string{dir, filepath.Dir(dir)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("guard child left %s behind (stat err %v)", path, err)
		}
	}
}
