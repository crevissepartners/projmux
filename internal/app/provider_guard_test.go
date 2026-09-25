package app

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Many tests in this package run the default wiring, and a route that reaches
// for a provider resolves `codex` or `claude` through PATH. A test that forgets
// its own stand-in (fakeProviderBinaries) then runs the real CLI on the
// developer machine: it can install a package, start a managed daemon, or
// touch a live session. CI has neither CLI, so the same test passes there
// without anyone noticing, and a test that swallows the exec error passes
// everywhere.
//
// runWithProviderGuard puts fail-closed stand-ins for both CLIs on PATH for
// the whole test binary and fails the package when any test executed one. A
// test's own stand-ins are prepended later, so they still win.

// providerGuardOptInEnv names every variable the package's installed tests
// read to enable an intentional real-provider path. Those tests resolve the
// real CLI through PATH, so when any of them is set the guard steps aside.
// TestProviderGuardOptInEnvCoversTheInstalledTests keeps the list closed.
var providerGuardOptInEnv = []string{
	"CODEX_DIAGNOSTIC_FAILURE",
	"CODEX_DIAGNOSTIC_HELPER",
	"CODEX_DIAGNOSTIC_INITIALIZE_DELAY",
	"CODEX_DIAGNOSTIC_INPUT",
	"CODEX_DIAGNOSTIC_MANAGER",
	"CODEX_DIAGNOSTIC_MANAGER_DELAY",
	"CODEX_DIAGNOSTIC_USER_AGENT",
	"CODEX_DIAGNOSTIC_VERSION",
	"PMX_TEST_CLAUDE_ENDPOINT_BIN",
	"PMX_TEST_REAL_CLAUDE_BIN",
	"PMX_TEST_REAL_CLAUDE_CONFIG_DIR",
	"PROJMUX_CODEX_APPROVAL_SMOKE_ROOT",
	"PROJMUX_CODEX_CONNECTION_INPUT",
	"PROJMUX_CODEX_CUTOVER_SMOKE_ROOT",
	"PROJMUX_CODEX_PHASE0_PAYLOAD_FREE_SMOKE_ROOT",
	"PROJMUX_CODEX_PHASE3_SMOKE_ROOT",
	"PROJMUX_CODEX_RECONNECT_SMOKE_ROOT",
	"PROJMUX_CODEX_RECOVERY_INPUT",
	"PROJMUX_CODEX_RETIREMENT_SMOKE_ROOT",
	"PROJMUX_CODEX_SCALE_RESUME_BINARY",
	"PROJMUX_CODEX_SCALE_RESUME_CWD",
	"PROJMUX_CODEX_SCALE_RESUME_SMOKE_ROOT",
	"PROJMUX_CODEX_SCALE_RESUME_STORE",
	"PROJMUX_CODEX_SCALE_RESUME_VERSION",
}

// providerGuardAmbientEnv are variables the installed tests read that locate
// the environment rather than enable a real provider. The live machine guard
// always sets some of them, so they must never make the provider guard step
// aside.
var providerGuardAmbientEnv = []string{"PATH", "TMUX_TMPDIR", "XDG_CONFIG_HOME"}

// providerGuardDir is the stand-in directory of the running guard, or "" when
// the package runs without it.
var providerGuardDir string

const (
	providerGuardChildEnv   = "PMX_TEST_PROVIDER_GUARD_CHILD"
	providerGuardDirEnv     = "PMX_TEST_PROVIDER_GUARD_DIR"
	providerGuardDirPrefix  = "pmx-provider-guard-"
	providerGuardRecordName = "invocations.log"
	providerGuardFailLine   = "FAIL: a test ran the real codex or claude from PATH; give the test its own stand-in (fakeProviderBinaries) or inject the provider seam"
)

func runWithProviderGuard(run func() int) int {
	for _, key := range providerGuardOptInEnv {
		if os.Getenv(key) != "" {
			fmt.Fprintf(os.Stderr, "provider guard: stepped aside; %s enables an intentional real-provider path\n", key)
			return run()
		}
	}

	// A test that re-executes this binary (a helper process, often ending in
	// os.Exit before any defer runs) joins the guard it inherited: its stand-ins
	// already record into the owner's log, and the owner reports and removes it.
	if inherited := os.Getenv(providerGuardDirEnv); inherited != "" {
		if _, err := os.Stat(filepath.Join(inherited, "codex")); err == nil {
			providerGuardDir = inherited
			defer func() { providerGuardDir = "" }()
			return run()
		}
	}

	dir, err := os.MkdirTemp("", providerGuardDirPrefix)
	if err != nil {
		fmt.Fprintf(os.Stderr, "provider guard: create stand-in dir: %v\n", err)
		return 1
	}
	defer os.RemoveAll(dir)

	record := filepath.Join(dir, providerGuardRecordName)
	for _, name := range []string{"codex", "claude"} {
		// One printf of the whole line keeps concurrent appends whole.
		script := "#!/bin/sh\nline='" + name + "'\n" +
			"for arg in \"$@\"; do line=\"$line $arg\"; done\n" +
			"printf '%s\\n' \"$line\" >> '" + record + "'\n" +
			"echo 'provider guard: refused to run the real " + name + "' >&2\nexit 1\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil { // #nosec G306 -- the stand-in must be executable.
			fmt.Fprintf(os.Stderr, "provider guard: write %s stand-in: %v\n", name, err)
			return 1
		}
	}
	path, hadPath := os.LookupEnv("PATH")
	if err := os.Setenv("PATH", dir+string(os.PathListSeparator)+path); err != nil {
		fmt.Fprintf(os.Stderr, "provider guard: set PATH: %v\n", err)
		return 1
	}
	defer func() {
		if hadPath {
			_ = os.Setenv("PATH", path)
		} else {
			_ = os.Unsetenv("PATH")
		}
	}()
	if err := os.Setenv(providerGuardDirEnv, dir); err != nil {
		fmt.Fprintf(os.Stderr, "provider guard: set %s: %v\n", providerGuardDirEnv, err)
		return 1
	}
	defer os.Unsetenv(providerGuardDirEnv)
	providerGuardDir = dir
	defer func() { providerGuardDir = "" }()

	code := run()
	data, err := os.ReadFile(record) // #nosec G304 -- the guard's own record under its temp dir.
	if err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "provider guard: read record: %v\n", err)
		return 1
	}
	if calls := strings.TrimRight(string(data), "\n"); calls != "" {
		fmt.Fprintln(os.Stderr, providerGuardFailLine)
		for line := range strings.SplitSeq(calls, "\n") {
			fmt.Fprintln(os.Stderr, "  "+line)
		}
		if code == 0 {
			code = 1
		}
	}
	return code
}

// exitIfProviderGuardChild runs the guard around a fixed body when the test
// binary is re-executed as a guard fixture, and exits with the guard's code.
func exitIfProviderGuardChild() {
	mode := os.Getenv(providerGuardChildEnv)
	if mode == "" {
		return
	}
	pathBefore := os.Getenv("PATH")
	os.Exit(runWithProviderGuard(func() int {
		fmt.Fprintf(os.Stderr, "provider guard dir: %s\n", providerGuardDir)
		switch mode {
		case "runs-codex":
			_ = exec.Command("codex", "app-server", "daemon", "start").Run()
			return 0
		case "opted-in":
			if got := os.Getenv("PATH"); got != pathBefore {
				fmt.Fprintf(os.Stderr, "PATH changed while opted in: %q\n", got)
				return 1
			}
			for _, name := range []string{"codex", "claude"} {
				if resolved, err := exec.LookPath(name); err == nil && strings.HasPrefix(filepath.Base(filepath.Dir(resolved)), providerGuardDirPrefix) {
					fmt.Fprintf(os.Stderr, "%s resolved to a guard stand-in %s while opted in\n", name, resolved)
					return 1
				}
			}
			return 0
		default:
			return 0
		}
	}))
}

// TestProviderGuardStandsInForCodexAndClaude pins that TestMain runs this
// package behind the guard: both CLIs resolve to its stand-ins.
func TestProviderGuardStandsInForCodexAndClaude(t *testing.T) {
	dir := providerGuardDir
	if dir == "" {
		t.Fatal("TestMain did not run the package behind runWithProviderGuard")
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
	for _, want := range []string{providerGuardFailLine, "  codex app-server daemon start"} {
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

	code, stderr = runProviderGuardChild(t, "opted-in", "PROJMUX_CODEX_CONNECTION_INPUT=/nonexistent/provider-guard-opt-in")
	if code != 0 {
		t.Fatalf("opted-in guard child exit = %d, want 0; stderr:\n%s", code, stderr)
	}
	if want := "provider guard: stepped aside; PROJMUX_CODEX_CONNECTION_INPUT"; !strings.Contains(stderr, want) {
		t.Errorf("stderr lacks %q:\n%s", want, stderr)
	}
}

// TestProviderGuardOptInEnvCoversTheInstalledTests parses the installed tests
// and requires every variable they read by literal name to be either an
// opt-in gate or a known ambient location, so a new real-provider gate cannot
// run behind the stand-ins unnoticed.
func TestProviderGuardOptInEnvCoversTheInstalledTests(t *testing.T) {
	files, err := filepath.Glob("*_installed_test.go")
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, "codex_payload_free_installed_outcome_test.go")
	read := map[string]bool{}
	fset := token.NewFileSet()
	for _, file := range files {
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			arg := -1
			switch fun := call.Fun.(type) {
			case *ast.SelectorExpr:
				switch fun.Sel.Name {
				case "SmokeRoot":
					arg = 0
				case "Getenv", "LookupEnv":
					if pkg, ok := fun.X.(*ast.Ident); ok && pkg.Name == "os" {
						arg = 0
					}
				}
			case *ast.Ident:
				if fun.Name == "newInstalledBrokerFixture" {
					arg = 1
				}
			}
			if arg < 0 || len(call.Args) <= arg {
				return true
			}
			if lit, ok := call.Args[arg].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if name, err := strconv.Unquote(lit.Value); err == nil {
					read[name] = true
				}
			}
			return true
		})
	}
	if !read["PROJMUX_CODEX_PHASE0_PAYLOAD_FREE_SMOKE_ROOT"] || !read["PROJMUX_CODEX_CUTOVER_SMOKE_ROOT"] {
		t.Fatalf("extraction missed known gates; read %v", slices.Sorted(maps.Keys(read)))
	}
	for name := range read {
		if !slices.Contains(providerGuardOptInEnv, name) && !slices.Contains(providerGuardAmbientEnv, name) {
			t.Errorf("installed tests read %s; add it to providerGuardOptInEnv (or providerGuardAmbientEnv if it only locates the environment)", name)
		}
	}
	for _, name := range append(slices.Clone(providerGuardOptInEnv), providerGuardAmbientEnv...) {
		if !read[name] {
			t.Errorf("%s is listed but no installed test reads it; drop it from the guard lists", name)
		}
	}
	for _, name := range providerGuardAmbientEnv {
		if slices.Contains(providerGuardOptInEnv, name) {
			t.Errorf("%s is ambient and must not make the guard step aside", name)
		}
	}
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
		case key == providerGuardChildEnv || key == providerGuardDirEnv || slices.Contains(providerGuardOptInEnv, key):
			continue
		case key == "PATH":
			var kept []string
			for _, dir := range filepath.SplitList(value) {
				if !strings.HasPrefix(filepath.Base(dir), providerGuardDirPrefix) {
					kept = append(kept, dir)
				}
			}
			entry = "PATH=" + strings.Join(kept, string(os.PathListSeparator))
		}
		env = append(env, entry)
	}
	env = append(env, providerGuardChildEnv+"="+mode)
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
// reported is gone once the child exits.
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
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("guard child left its stand-in dir %s behind (stat err %v)", dir, err)
	}
}
