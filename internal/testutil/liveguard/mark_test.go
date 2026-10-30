package liveguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMarkTestNamesTheCallerInTheGuardLog runs the real chain: MarkTest in a
// test, a guarded child that runs the real codex, and the failing log naming
// this test. It also pins that the mark is restored when the test ends.
func TestMarkTestNamesTheCallerInTheGuardLog(t *testing.T) {
	// Not parallel: the mark is one process-wide variable.
	before, had := os.LookupEnv(testParentEnv)
	t.Run("marked", func(t *testing.T) {
		MarkTest(t)
		if got, want := os.Getenv(testParentEnv), t.Name(); got != want {
			t.Fatalf("%s = %q, want %q", testParentEnv, got, want)
		}
		code, stderr := runProviderGuardChild(t, "runs-codex", testParentEnv+"="+t.Name())
		if code != 1 {
			t.Fatalf("guard child exit = %d, want 1; stderr:\n%s", code, stderr)
		}
		if want := "parent-test=" + t.Name(); !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	})
	after, hasAfter := os.LookupEnv(testParentEnv)
	if after != before || hasAfter != had {
		t.Errorf("%s after the marked test = %q (set %v), want the previous %q (set %v)", testParentEnv, after, hasAfter, before, had)
	}
}

// TestMarkTestRefusesAParallelTest pins the fail-closed choice: the mark is
// one variable per process, so a parallel test panics instead of being
// attributed to whichever test set it last.
func TestMarkTestRefusesAParallelTest(t *testing.T) {
	refused := make(chan any, 1)
	// A parallel subtest starts only after its parent function returns, and
	// the group's t.Run returns only once the subtest finishes.
	t.Run("group", func(t *testing.T) {
		t.Run("parallel", func(t *testing.T) {
			t.Parallel()
			defer func() { refused <- recover() }()
			MarkTest(t)
		})
	})
	if got := <-refused; got == nil {
		t.Error("MarkTest in a parallel test did not panic")
	}
}

const markChildEnv = "PMX_TEST_MARK_WRAPPER_CHILD"

// init turns the test binary into a guard fixture when markChildEnv is set: it
// marks a test through the wrapper, then runs the real codex with only PATH in
// its environment, the way a provider the product starts keeps just the
// allowlisted variables, and exits with the guard's code.
func init() {
	if os.Getenv(markChildEnv) == "" {
		return
	}
	os.Exit(RunGuarded(func() int {
		dir, err := guardMarkWrapperDir(activeProviderDir, "TestMarkedThroughTheAllowlist")
		if err != nil {
			return 2
		}
		// Resolve codex through PATH the way a provider's shell does, and
		// require that it lands on the wrapper, not the shared stand-in.
		if err := os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH")); err != nil {
			return 2
		}
		if resolved, err := exec.LookPath("codex"); err != nil || filepath.Dir(resolved) != dir {
			return 3
		}
		command := exec.Command("codex", "app-server", "daemon", "start") // #nosec G204 -- fixed fixture command resolved to the guard's wrapper.
		command.Env = []string{"PATH=" + os.Getenv("PATH")}
		_ = command.Run()
		return 0
	}))
}

// TestMarkedWrapperSurvivesAnAllowlistedEnvironment is the allowlist
// regression: the command runs with PATH alone, so testParentEnv is lost, and
// the failing log still names the test because the wrapper sets it.
func TestMarkedWrapperSurvivesAnAllowlistedEnvironment(t *testing.T) {
	t.Parallel()

	code, stderr := runProviderGuardChild(t, "clean", markChildEnv+"=1")
	if code != 1 {
		t.Fatalf("guard child exit = %d, want 1; stderr:\n%s", code, stderr)
	}
	if want := "parent-test=TestMarkedThroughTheAllowlist"; !strings.Contains(stderr, want) {
		t.Errorf("stderr lacks %q:\n%s", want, stderr)
	}
	if !strings.Contains(stderr, "  codex app-server daemon start") {
		t.Errorf("stderr lacks the recorded argv:\n%s", stderr)
	}
}

// TestGuardPathWithMarkKeepsAnEarlierFake pins that a fake provider ahead of
// the guard on PATH keeps winning over the marked wrapper.
func TestGuardPathWithMarkKeepsAnEarlierFake(t *testing.T) {
	t.Parallel()

	sep := string(os.PathListSeparator)
	got, ok := guardPathWithMark(strings.Join([]string{"/fake", "/guard", "/usr/bin"}, sep), "/guard", "/mark")
	if want := strings.Join([]string{"/fake", "/mark", "/guard", "/usr/bin"}, sep); !ok || got != want {
		t.Errorf("guardPathWithMark = %q, %v; want %q, true", got, ok, want)
	}
	if got, ok := guardPathWithMark("/usr/bin", "/guard", "/mark"); ok || got != "/usr/bin" {
		t.Errorf("without the guard entry guardPathWithMark = %q, %v; want the path unchanged and false", got, ok)
	}
}

// TestRequireActiveAcceptsAMarkedWrapper pins that a marked test still passes
// the stand-in resolution check, because the wrapper execs the stand-in.
func TestRequireActiveAcceptsAMarkedWrapper(t *testing.T) {
	// Not parallel: MarkTest changes PATH for the whole process.
	t.Run("marked", func(t *testing.T) {
		MarkTest(t)
		RequireActive(t)
	})
}
