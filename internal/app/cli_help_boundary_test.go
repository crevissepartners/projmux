package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/cli"
	"github.com/crevissepartners/projmux/internal/diagnostics"
)

// helpFlagSpellings are the four argv spellings the standard library `flag`
// package treats as equivalent help requests. All four must clear the shared
// help boundary; covering only `--help`/`-h` would let the other two reach the
// leaf parser and record an operational error.
var helpFlagSpellings = []string{"--help", "-help", "--h", "-h"}

// helpBoundaryArgv is the maintained help matrix: root spellings, every public
// route, every documented sub-route, and both hidden internal helpers, each
// crossed with all four help flag spellings.
func helpBoundaryArgv() [][]string {
	argv := [][]string{nil}
	for _, flag := range helpFlagSpellings {
		argv = append(argv, []string{flag})
	}
	var walk func(prefix []string, routes []cli.Route)
	walk = func(prefix []string, routes []cli.Route) {
		for _, route := range routes {
			path := append(append([]string{}, prefix...), route.Name)
			for _, flag := range helpFlagSpellings {
				argv = append(argv, append(append([]string{}, path...), flag))
			}
			walk(path, route.Children)
		}
	}
	walk(nil, cli.Routes())
	for _, flag := range helpFlagSpellings {
		argv = append(argv,
			[]string{"ai", "bogus", flag},
			[]string{"setup", "terminal", "--apply", flag},
		)
	}
	return argv
}

// TestAppRunHelpBoundaryReachesNoHandler is the app-level no-side-effect negative
// test. The App has no command handlers wired at all, so any attempt to dispatch
// a handler would panic on a nil receiver. Every public, nested, and hidden
// internal help invocation must instead exit 0 with help on stdout, no stderr,
// and zero runtime access.
func TestAppRunHelpBoundaryReachesNoHandler(t *testing.T) {
	t.Parallel()

	app := &App{}
	for _, argv := range helpBoundaryArgv() {
		var stdout, stderr bytes.Buffer
		if err := app.Run(argv, &stdout, &stderr); err != nil {
			t.Fatalf("Run(%q) error = %v, want nil", argv, err)
		}
		if stdout.Len() == 0 {
			t.Fatalf("Run(%q) wrote no help to stdout", argv)
		}
		if stderr.Len() != 0 {
			t.Fatalf("Run(%q) stderr = %q, want help on stdout only", argv, stderr.String())
		}
	}
}

// TestAppRunHelpDoesNotTouchTheFilesystemOrTmux backs the "runtime access 0"
// half of the help contract with an isolated home: no help invocation may create
// config, state, cache, or journal files.
func TestAppRunHelpDoesNotTouchTheFilesystemOrTmux(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("PROJMUX_CWD", filepath.Join(home, "project"))
	// An empty PATH makes any tmux invocation fail loudly instead of silently
	// succeeding against a real server.
	t.Setenv("PATH", filepath.Join(home, "empty-bin"))
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")

	app := &App{}
	for _, argv := range helpBoundaryArgv() {
		var stdout, stderr bytes.Buffer
		if err := app.Run(argv, &stdout, &stderr); err != nil {
			t.Fatalf("Run(%q) error = %v", argv, err)
		}
	}

	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatalf("read isolated home: %v", err)
	}
	if len(entries) != 0 {
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("help invocations wrote into the isolated home: %v", names)
	}
}

// TestHelpBoundaryRecordsZeroOperationalErrors closes the "operational error log
// 0" half of the help contract end to end. It mirrors what cmd/projmux/main.go
// does for every invocation — run the command, then record the outcome through
// the same policy — against a private journal, and requires the journal to hold
// no error-level row for any help spelling of any route.
func TestHelpBoundaryRecordsZeroOperationalErrors(t *testing.T) {
	t.Parallel()

	store := diagnostics.NewStore(filepath.Join(t.TempDir(), "operations.jsonl"))
	app := New()
	for _, argv := range helpBoundaryArgv() {
		var stdout, stderr bytes.Buffer
		started := time.Now()
		err := app.Run(argv, &stdout, &stderr)
		if err != nil {
			t.Fatalf("Run(%q) error = %v, want nil", argv, err)
		}
		// Best-effort by contract; a write failure here would hide a regression.
		if recordErr := diagnostics.RecordOutcome(
			store, argv, "help-boundary", "test", "tmux", started, err, IsUsageError(err), false,
		); recordErr != nil {
			t.Fatalf("RecordOutcome(%q) error = %v", argv, recordErr)
		}
	}

	events, err := store.Read()
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	for _, event := range events {
		if event.Level == "error" || event.Result == "error" {
			t.Fatalf("help invocation recorded an operational error: %+v", event)
		}
	}
}

// TestPopupWaitKeyHelpDoesNotBlockOnTheTTY is a bounded-timeout regression test.
// Before the shared help boundary, `projmux popup-wait-key --help` went straight
// to a blocking single-key read on /dev/tty and never returned, which violates
// the hidden-internal-help contract and would hang CI rather than fail it.
func TestPopupWaitKeyHelpDoesNotBlockOnTheTTY(t *testing.T) {
	t.Parallel()

	var argvs [][]string
	for _, flag := range helpFlagSpellings {
		argvs = append(argvs, []string{"popup-wait-key", flag}, []string{"key-broker", flag})
	}
	for _, argv := range argvs {
		done := make(chan error, 1)
		var stdout, stderr bytes.Buffer
		go func() {
			// New() builds the real command graph, so a regression that lets
			// argv reach the handler would actually attempt the tty read.
			done <- New().Run(argv, &stdout, &stderr)
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Run(%q) error = %v, want nil", argv, err)
			}
			if !strings.HasPrefix(stdout.String(), "projmux "+argv[0]) {
				t.Fatalf("Run(%q) stdout = %q, want manifest help", argv, stdout.String())
			}
			if stderr.Len() != 0 {
				t.Fatalf("Run(%q) stderr = %q, want help on stdout", argv, stderr.String())
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("Run(%q) blocked instead of rendering help", argv)
		}
	}
}

// TestShouldRunLegacyHookMigrationsSkipsEveryHelpInvocation keeps the legacy hook
// filesystem migration out of the help boundary, alongside the existing doctor
// and support-report exclusions.
func TestShouldRunLegacyHookMigrationsSkipsEveryHelpInvocation(t *testing.T) {
	t.Parallel()

	for _, argv := range helpBoundaryArgv() {
		if shouldRunLegacyHookMigrations(argv) {
			t.Fatalf("shouldRunLegacyHookMigrations(%q) = true, want false", argv)
		}
	}
	// Payload after `--` is not help, so the migration boundary is unchanged
	// for real invocations.
	for _, argv := range [][]string{
		{"ai", "split", "--", "--help"},
		{"notify", "push", "--summary", "x"},
		{"nosuchcmd", "--help"},
	} {
		if !shouldRunLegacyHookMigrations(argv) {
			t.Fatalf("shouldRunLegacyHookMigrations(%q) = false, want true", argv)
		}
	}
}

// TestAppRunKeepsUnknownCommandContract pins the historical default branch
// through the Cobra root: the primary listing goes to stderr and the error is a
// plain runtime error (exit 1), not a usage error (exit 2).
func TestAppRunKeepsUnknownCommandContract(t *testing.T) {
	t.Parallel()

	for _, argv := range [][]string{{"nosuchcmd"}, {"nosuchcmd", "--help"}, {"--json"}, {"__complete", "ai"}} {
		var stdout, stderr bytes.Buffer
		err := (&App{}).Run(argv, &stdout, &stderr)
		if err == nil || !strings.HasPrefix(err.Error(), "unknown command: ") {
			t.Fatalf("Run(%q) error = %v, want unknown command", argv, err)
		}
		if IsUsageError(err) {
			t.Fatalf("Run(%q) returned a usage error; exit code would change from 1 to 2", argv)
		}
		if stdout.Len() != 0 {
			t.Fatalf("Run(%q) stdout = %q, want the listing on stderr", argv, stdout.String())
		}
		if !strings.Contains(stderr.String(), "Commands:") {
			t.Fatalf("Run(%q) stderr missing the primary listing:\n%s", argv, stderr.String())
		}
	}
}

// TestAppRunRoutesEveryManifestRouteThroughCobra proves the app wiring covers the
// full manifest, so building the Cobra root can never fail at runtime.
func TestAppRunRoutesEveryManifestRouteThroughCobra(t *testing.T) {
	t.Parallel()

	handlers := New().routeHandlers()
	for _, route := range cli.Routes() {
		if route.Name == "help" || route.Name == "version" {
			continue
		}
		if _, ok := handlers[route.Name]; !ok {
			t.Fatalf("route %q has no handler wired", route.Name)
		}
	}
	if len(handlers) != len(cli.Routes())-2 {
		t.Fatalf("handler count = %d, want %d", len(handlers), len(cli.Routes())-2)
	}
	if _, err := cli.NewRoot(cli.RootOptions{
		Stdout:   &bytes.Buffer{},
		Stderr:   &bytes.Buffer{},
		Handlers: handlers,
	}); err != nil {
		t.Fatalf("NewRoot with the production handler map failed: %v", err)
	}
}
