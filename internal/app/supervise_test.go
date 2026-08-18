package app

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// activatePaneFixture stamps one generation onto a fixture Pane so a receipt
// has something current to match.
func activatePaneFixture(t *testing.T, store *fakeResourceStore, paneUID, agentUID, generation string) {
	t.Helper()
	if _, err := store.store().update(func(working *coremetadata.Registry) error {
		_, err := store.mutator().RecordPaneActivation(working, paneUID, coremetadata.PaneActivationOptions{
			Generation: generation, AgentUID: agentUID, OperationID: "op-test",
		})
		return err
	}); err != nil {
		t.Fatalf("activate %s: %v", paneUID, err)
	}
}

func newTestSuperviseCommand(store *fakeResourceStore, outcome processOutcome, runErr error) (*superviseCommand, *[]string) {
	var started []string
	cmd := &superviseCommand{
		store: store.store(),
		now:   func() time.Time { return resourceFixtureClock },
		run: func(argv []string, argv0 string) (processOutcome, error) {
			started = append(started, strings.Join(append([]string{argv0}, argv...), " "))
			return outcome, runErr
		},
	}
	return cmd, &started
}

// TestSuperviseArgvTerminatesItsOwnFlags proves an operator payload can never
// be re-read as a supervisor flag.
func TestSuperviseArgvTerminatesItsOwnFlags(t *testing.T) {
	t.Parallel()

	spec := superviseSpec{PaneUID: "pane-1", AgentUID: "agent-1", Generation: "gen-1", OperationID: "op-1"}
	argv := superviseArgv("/opt/projmux/bin/projmux", spec, "-zsh",
		[]string{"claude", "--pane-uid", "spoofed", "--generation", "spoofed"})
	want := []string{
		"/opt/projmux/bin/projmux", "internal", "supervise",
		"--pane-uid", "pane-1", "--generation", "gen-1",
		"--agent-uid", "agent-1", "--operation-id", "op-1", "--argv0", "-zsh",
		"--", "claude", "--pane-uid", "spoofed", "--generation", "spoofed",
	}
	if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv = %v, want %v", argv, want)
	}

	// The route parses exactly what the builder wrote, not the payload's
	// lookalike flags.
	store := newFakeResourceStore(t)
	activatePaneFixture(t, store, "pan-alpha-log", "", "gen-1")
	cmd, started := newTestSuperviseCommand(store, processOutcome{ExitCode: 0}, nil)
	err := cmd.Run(argv[3:], nil, nil)
	var coded superviseExitError
	if !errors.As(err, &coded) || coded.ExitCode() != 0 {
		t.Fatalf("supervise exit = %v", err)
	}
	if len(*started) != 1 || !strings.HasSuffix((*started)[0], "claude --pane-uid spoofed --generation spoofed") {
		t.Fatalf("started child = %v", *started)
	}
}

// TestSuperviseRecordsTheObservedWaitStatus is the schema half of the process
// contract: what the supervisor reaped is what the registry stores.
func TestSuperviseRecordsTheObservedWaitStatus(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		outcome  processOutcome
		want     coremetadata.TerminationClassification
		wantCode *int
		wantSig  string
		wantExit int
	}{
		{name: "clean exit", outcome: processOutcome{ExitCode: 0}, want: coremetadata.TerminationNormal, wantCode: exitCodePtr(0)},
		{name: "non-zero exit", outcome: processOutcome{ExitCode: 17}, want: coremetadata.TerminationAbnormal, wantCode: exitCodePtr(17), wantExit: 17},
		{
			name:    "signal",
			outcome: processOutcome{Signal: "TERM", SignalNumber: 15},
			want:    coremetadata.TerminationAbnormal, wantSig: "TERM", wantExit: 143,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			activatePaneFixture(t, store, "pan-alpha-log", "", "gen-exact")
			cmd, _ := newTestSuperviseCommand(store, test.outcome, nil)
			err := cmd.Run([]string{"--pane-uid", "pan-alpha-log", "--generation", "gen-exact", "--", "sleep", "1"}, nil, nil)
			var coded superviseExitError
			if !errors.As(err, &coded) || coded.ExitCode() != test.wantExit {
				t.Fatalf("supervise exit = %v, want status %d", err, test.wantExit)
			}
			pane, ok := store.registry.Pane("pan-alpha-log")
			if !ok || pane.Status.LastTermination == nil {
				t.Fatalf("no receipt was recorded:\n%s", store.snapshot())
			}
			receipt := pane.Status.LastTermination
			if receipt.Source != coremetadata.TerminationSourceSupervisor || receipt.Classification != test.want {
				t.Fatalf("receipt = %#v, want %q from the supervisor", receipt, test.want)
			}
			if receipt.Generation != "gen-exact" {
				t.Fatalf("receipt generation = %q, want the launched one", receipt.Generation)
			}
			switch {
			case test.wantSig != "":
				if receipt.Signal != test.wantSig || receipt.ExitCode != nil {
					t.Fatalf("signal receipt = %#v", receipt)
				}
			default:
				if receipt.ExitCode == nil || *receipt.ExitCode != *test.wantCode || receipt.Signal != "" {
					t.Fatalf("exit receipt = %#v", receipt)
				}
			}
		})
	}
}

// TestSuperviseKeepsThePaneStatusWhenTheReceiptCannotBeWritten is the
// unknown-safe fallback: a registry that cannot be written costs the evidence,
// never the pane's own behavior.
func TestSuperviseKeepsThePaneStatusWhenTheReceiptCannotBeWritten(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	activatePaneFixture(t, store, "pan-alpha-log", "", "gen-exact")
	cmd, _ := newTestSuperviseCommand(store, processOutcome{ExitCode: 5}, nil)
	cmd.store.update = func(func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
		return coremetadata.Registry{}, errors.New("injected registry failure")
	}
	var warnings strings.Builder
	cmd.warn = &warnings

	err := cmd.Run([]string{"--pane-uid", "pan-alpha-log", "--generation", "gen-exact", "--", "sleep", "1"}, nil, nil)
	var coded superviseExitError
	if !errors.As(err, &coded) || coded.ExitCode() != 5 {
		t.Fatalf("supervise exit = %v, want the child's own status", err)
	}
	if !strings.Contains(warnings.String(), "injected registry failure") {
		t.Fatalf("warnings = %q, want the lost write reported", warnings.String())
	}
	if pane, _ := store.registry.Pane("pan-alpha-log"); pane.Status.LastTermination != nil {
		t.Fatalf("a failed write stored something: %#v", pane.Status.LastTermination)
	}
}

// TestSuperviseReportsAStaleReceiptWithoutFailingThePane covers a receipt that
// arrives after the binding it names is gone.
func TestSuperviseReportsAStaleReceiptWithoutFailingThePane(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	activatePaneFixture(t, store, "pan-alpha-log", "", "gen-current")
	cmd, _ := newTestSuperviseCommand(store, processOutcome{ExitCode: 0}, nil)
	var warnings strings.Builder
	cmd.warn = &warnings

	err := cmd.Run([]string{"--pane-uid", "pan-alpha-log", "--generation", "gen-replaced", "--", "sleep", "1"}, nil, nil)
	var coded superviseExitError
	if !errors.As(err, &coded) || coded.ExitCode() != 0 {
		t.Fatalf("supervise exit = %v", err)
	}
	if !strings.Contains(warnings.String(), "not applied") {
		t.Fatalf("warnings = %q, want the stale receipt reported", warnings.String())
	}
	if pane, _ := store.registry.Pane("pan-alpha-log"); pane.Status.LastTermination != nil {
		t.Fatalf("a stale receipt was stored: %#v", pane.Status.LastTermination)
	}
}

// TestSuperviseRefusesAnUnidentifiedLaunch keeps the route from writing a
// receipt it cannot bind to anything.
func TestSuperviseRefusesAnUnidentifiedLaunch(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	for _, args := range [][]string{
		{"--generation", "gen-1", "--", "sleep", "1"},
		{"--pane-uid", "pan-alpha-log", "--", "sleep", "1"},
		{"--pane-uid", "pan-alpha-log", "--generation", "gen-1"},
	} {
		cmd, started := newTestSuperviseCommand(store, processOutcome{}, nil)
		err := cmd.Run(args, nil, &strings.Builder{})
		if err == nil || !IsUsageError(err) {
			t.Fatalf("supervise %v error = %v, want a usage refusal", args, err)
		}
		if len(*started) != 0 {
			t.Fatalf("supervise %v started a child before refusing", args)
		}
	}
}

// TestSuperviseReportsALaunchFailureRatherThanATermination keeps a process that
// never ran out of the termination vocabulary.
func TestSuperviseReportsALaunchFailureRatherThanATermination(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	activatePaneFixture(t, store, "pan-alpha-log", "", "gen-exact")
	cmd, _ := newTestSuperviseCommand(store, processOutcome{}, errors.New("exec format error"))
	err := cmd.Run([]string{"--pane-uid", "pan-alpha-log", "--generation", "gen-exact", "--", "/nope"}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "exec format error") {
		t.Fatalf("launch failure = %v", err)
	}
	if pane, _ := store.registry.Pane("pan-alpha-log"); pane.Status.LastTermination != nil {
		t.Fatalf("a process that never ran produced a receipt: %#v", pane.Status.LastTermination)
	}
}

// TestRunSupervisedChildReportsRealWaitStatuses is the in-process half of the
// process-integration contract: a real child, really reaped.
func TestRunSupervisedChildReportsRealWaitStatuses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX wait statuses")
	}
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no POSIX shell: %v", err)
	}
	for _, test := range []struct {
		name    string
		script  string
		want    processOutcome
		wantCls coremetadata.TerminationClassification
	}{
		{name: "clean exit", script: "exit 0", want: processOutcome{ExitCode: 0}, wantCls: coremetadata.TerminationNormal},
		{name: "non-zero exit", script: "exit 9", want: processOutcome{ExitCode: 9}, wantCls: coremetadata.TerminationAbnormal},
		{
			name: "death by signal", script: "kill -TERM $$; sleep 5",
			want:    processOutcome{Signal: "TERM", SignalNumber: 15},
			wantCls: coremetadata.TerminationAbnormal,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			outcome, err := runSupervisedChild([]string{shell, "-c", test.script}, "")
			if err != nil {
				t.Fatalf("runSupervisedChild error = %v", err)
			}
			if outcome != test.want {
				t.Fatalf("outcome = %#v, want %#v", outcome, test.want)
			}
			if got := coremetadata.ClassifyProcessExit(outcome.ExitCode, outcome.Signal); got != test.wantCls {
				t.Fatalf("classification = %q, want %q", got, test.wantCls)
			}
		})
	}
}

// TestRunSupervisedChildPreservesArgvCwdAndEnvironment is the parity half: the
// child sees exactly the process it would have seen unsupervised.
func TestRunSupervisedChildPreservesArgvCwdAndEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell")
	}
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no POSIX shell: %v", err)
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "observed")
	t.Setenv("PROJMUX_SUPERVISE_PARITY", "inherited")
	// The supervisor never sets a working directory: the child inherits this
	// process's, exactly as an exec'd pane command would.
	wantDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	script := `printf '%s|%s|%s|%s\n' "$0" "$1" "$(pwd)" "$PROJMUX_SUPERVISE_PARITY" > ` + out
	outcome, err := runSupervisedChild([]string{shell, "-c", script, "argv-zero", "first"}, "")
	if err != nil || outcome.ExitCode != 0 {
		t.Fatalf("parity child outcome=%#v err=%v", outcome, err)
	}
	observed, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read observation: %v", err)
	}
	fields := strings.Split(strings.TrimSpace(string(observed)), "|")
	if len(fields) != 4 {
		t.Fatalf("observation = %q", observed)
	}
	if fields[0] != "argv-zero" || fields[1] != "first" {
		t.Fatalf("argv = %q, want it forwarded verbatim", fields[:2])
	}
	resolvedDir, err := filepath.EvalSymlinks(wantDir)
	if err != nil {
		t.Fatalf("resolve working directory: %v", err)
	}
	if observedDir, err := filepath.EvalSymlinks(fields[2]); err != nil || observedDir != resolvedDir {
		t.Fatalf("child cwd = %q, want %q", fields[2], resolvedDir)
	}
	if fields[3] != "inherited" {
		t.Fatalf("child environment = %q, want the inherited value", fields[3])
	}
}

// TestSuperviseRouteIsReachableThroughTheInternalNamespace keeps the hidden
// route wired the way a launched pane spells it.
func TestSuperviseRouteIsReachableThroughTheInternalNamespace(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	activatePaneFixture(t, store, "pan-alpha-log", "", "gen-exact")
	supervise, _ := newTestSuperviseCommand(store, processOutcome{ExitCode: 0}, nil)
	internal := &internalCommand{supervise: supervise}
	err := internal.Run([]string{"supervise", "--pane-uid", "pan-alpha-log", "--generation", "gen-exact", "--", "sleep", "1"}, nil, nil)
	var coded superviseExitError
	if !errors.As(err, &coded) {
		t.Fatalf("internal supervise = %v", err)
	}
	if pane, _ := store.registry.Pane("pan-alpha-log"); pane.Status.LastTermination == nil {
		t.Fatal("the namespaced route recorded no receipt")
	}
}

func exitCodePtr(code int) *int { return &code }
