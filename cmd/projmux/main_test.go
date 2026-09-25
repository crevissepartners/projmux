package main

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/app"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/diagnostics"
)

type testExitError struct{ code int }

func (e testExitError) Error() string { return "already displayed" }
func (e testExitError) ExitCode() int { return e.code }

func TestExecuteCLIPreservesOutputAndExitSemanticsAndRecordsOnce(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		err        error
		wantCode   int
		wantStderr string
	}{
		{name: "success", wantCode: 0},
		{name: "runtime", err: errors.New("runtime failed"), wantCode: 1, wantStderr: "runtime failed\n"},
		{name: "usage", err: &app.UsageError{Message: "bad usage"}, wantCode: 2, wantStderr: "bad usage\n"},
		{name: "exit coder suppresses default stderr", err: testExitError{code: 2}, wantCode: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			records := 0
			var recorded error
			code := executeCLI(func() error { return tt.err }, func(err error) {
				records++
				recorded = err
			}, &stderr)
			if code != tt.wantCode || stderr.String() != tt.wantStderr || records != 1 || !errors.Is(recorded, tt.err) {
				t.Fatalf("code=%d stderr=%q records=%d recorded=%v", code, stderr.String(), records, recorded)
			}
		})
	}
}

// testWrappingExitError is an app-defined coder that wraps a subprocess
// failure; errors.As stops at it, so its own code and silence win.
type testWrappingExitError struct {
	code  int
	cause error
}

func (e testWrappingExitError) Error() string { return "app displayed: " + e.cause.Error() }
func (e testWrappingExitError) ExitCode() int { return e.code }
func (e testWrappingExitError) Unwrap() error { return e.cause }

// subprocessExitError returns a real *exec.ExitError whose code is 3, so a
// test can tell it apart from the default exit code 1.
func subprocessExitError(t *testing.T) *exec.ExitError {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh not found: %v", err)
	}
	runErr := exec.Command(sh, "-c", "exit 3").Run()
	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) || runErr != error(exitErr) || exitErr.ExitCode() != 3 {
		t.Fatalf("sh -c 'exit 3' = %v, want a bare *exec.ExitError with code 3", runErr)
	}
	return exitErr
}

func TestExecuteCLIPrintsWrappedSubprocessExitErrorOnceWithItsCode(t *testing.T) {
	t.Parallel()
	exitErr := subprocessExitError(t)
	wrapped := fmt.Errorf("switch tmux session %q: %w", "demo", exitErr)
	doubly := fmt.Errorf("attach: %w", wrapped)
	tests := []struct {
		name       string
		err        error
		wantCode   int
		wantStderr string
	}{
		{name: "wrapped exit error prints once", err: wrapped, wantCode: 3, wantStderr: wrapped.Error() + "\n"},
		{name: "doubly wrapped exit error prints once", err: doubly, wantCode: 3, wantStderr: doubly.Error() + "\n"},
		{name: "bare exit error stays silent", err: exitErr, wantCode: 3},
		{name: "app coder stays silent", err: testExitError{code: 4}, wantCode: 4},
		{name: "app coder wrapping exit error stays silent", err: testWrappingExitError{code: 5, cause: exitErr}, wantCode: 5},
		{name: "plain error", err: errors.New("runtime failed"), wantCode: 1, wantStderr: "runtime failed\n"},
		{name: "usage error", err: &app.UsageError{Message: "bad usage"}, wantCode: 2, wantStderr: "bad usage\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			records := 0
			code := executeCLI(func() error { return tt.err }, func(error) { records++ }, &stderr)
			if code != tt.wantCode || stderr.String() != tt.wantStderr || records != 1 {
				t.Fatalf("code=%d stderr=%q records=%d, want code=%d stderr=%q records=1", code, stderr.String(), records, tt.wantCode, tt.wantStderr)
			}
		})
	}
}

// TestMetadataConflictsReachExitCodeTwoThroughTheUsageErrorPath proves the
// resource metadata layer's typed input errors land on the CLI's exit code 2
// without adding a public route for them. An explicit name collision and a
// rebind root collision must both be zero-mutation usage errors; a registry
// schema fault must stay a runtime error at exit code 1.
func TestMetadataConflictsReachExitCodeTwoThroughTheUsageErrorPath(t *testing.T) {
	t.Parallel()

	newRegistry := func(t *testing.T) (coremetadata.Mutator, coremetadata.Registry, string) {
		t.Helper()
		roots := map[string]bool{"/src/projmux": true, "/src/other": true}
		seq := 0
		m := coremetadata.Mutator{
			Now: func() time.Time { return time.Date(2026, 8, 15, 9, 30, 0, 0, time.UTC) },
			NewUID: func(kind coremetadata.Kind) (string, error) {
				seq++
				return string(kind) + "-" + string(rune('a'+seq%26)), nil
			},
			DirExists: func(path string) (bool, error) { return roots[path], nil },
		}
		reg := coremetadata.NewRegistry()
		result, err := m.RegisterProject(&reg, coremetadata.RegisterProjectOptions{
			Root:         "/src/projmux",
			Name:         "projmux",
			DefaultShell: "/bin/zsh",
			OperationID:  "op-seed",
		})
		if err != nil {
			t.Fatalf("seed register: %v", err)
		}
		return m, reg, result.Project.Metadata.UID
	}

	tests := []struct {
		name     string
		produce  func(t *testing.T) error
		wantCode int
	}{
		{
			name: "explicit name collision exits 2",
			produce: func(t *testing.T) error {
				m, reg, _ := newRegistry(t)
				_, err := m.RegisterProject(&reg, coremetadata.RegisterProjectOptions{
					Root:         "/src/other",
					Name:         "projmux",
					DefaultShell: "/bin/zsh",
				})
				if !errors.Is(err, coremetadata.ErrNameConflict) {
					t.Fatalf("error %v does not wrap ErrNameConflict", err)
				}
				if len(reg.Projects) != 1 {
					t.Fatalf("a failed explicit-name registration mutated the registry: %d projects", len(reg.Projects))
				}
				return err
			},
			wantCode: 2,
		},
		{
			name: "rebind root collision exits 2",
			produce: func(t *testing.T) error {
				m, reg, uid := newRegistry(t)
				other, err := m.RegisterProject(&reg, coremetadata.RegisterProjectOptions{Root: "/src/other", DefaultShell: "/bin/zsh"})
				if err != nil {
					t.Fatalf("seed second project: %v", err)
				}
				_, err = m.RebindProjectRoot(&reg, other.Project.Metadata.UID, "/src/projmux")
				if !errors.Is(err, coremetadata.ErrRootConflict) {
					t.Fatalf("error %v does not wrap ErrRootConflict", err)
				}
				bound, _ := reg.Project(uid)
				if bound.Spec.Root != "/src/projmux" {
					t.Fatalf("a failed rebind mutated the owning project: %q", bound.Spec.Root)
				}
				return err
			},
			wantCode: 2,
		},
		{
			name: "a newer registry schema exits 1",
			produce: func(t *testing.T) error {
				_, err := coremetadata.ClassifySchemaVersion(coremetadata.SchemaVersion + 1)
				if !errors.Is(err, coremetadata.ErrSchemaTooNew) {
					t.Fatalf("error %v does not wrap ErrSchemaTooNew", err)
				}
				return err
			},
			wantCode: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			source := tt.produce(t)
			mapped := app.MapMetadataError(source)
			var stderr bytes.Buffer
			code := executeCLI(func() error { return mapped }, func(error) {}, &stderr)
			if code != tt.wantCode {
				t.Fatalf("exit code = %d, want %d (stderr %q)", code, tt.wantCode, stderr.String())
			}
			if stderr.String() != source.Error()+"\n" {
				t.Fatalf("stderr = %q, want the metadata message", stderr.String())
			}
		})
	}
}

// TestSelectorCardinalityFailureReachesExitCodeTwoWithNoStdout proves the
// selector engine's bounded ambiguity error lands on the CLI's exit code 2
// through the same usage-error seam, with the whole listing on stderr and zero
// bytes on stdout.
func TestSelectorCardinalityFailureReachesExitCodeTwoWithNoStdout(t *testing.T) {
	t.Parallel()

	// Two Panes named "zsh" in different Project roots: a legal registry, and
	// an ambiguous whole-registry exact-one read.
	registry := coremetadata.NewRegistry()
	registry.Projects = []coremetadata.Project{
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindProject,
			Metadata: coremetadata.ObjectMeta{UID: "prj-1", Name: "alpha"},
			Spec:     coremetadata.ProjectSpec{Root: "/srv/alpha", PrimaryWindowRef: "win-1"},
		},
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindProject,
			Metadata: coremetadata.ObjectMeta{UID: "prj-2", Name: "beta"},
			Spec:     coremetadata.ProjectSpec{Root: "/srv/beta", PrimaryWindowRef: "win-2"},
		},
	}
	registry.NameReservations = []coremetadata.NameReservation{
		{Kind: coremetadata.KindProject, Name: "alpha", UID: "prj-1"},
		{Kind: coremetadata.KindProject, Name: "beta", UID: "prj-2"},
		{Scope: "prj-1", Kind: coremetadata.KindWindow, Name: "one", UID: "win-1"},
		{Scope: "prj-2", Kind: coremetadata.KindWindow, Name: "two", UID: "win-2"},
		{Scope: "prj-1", Kind: coremetadata.KindPane, Name: "zsh", UID: "pan-1"},
		{Scope: "prj-2", Kind: coremetadata.KindPane, Name: "zsh", UID: "pan-2"},
	}
	for _, window := range []struct{ uid, name, pane, project string }{
		{"win-1", "one", "pan-1", "prj-1"},
		{"win-2", "two", "pan-2", "prj-2"},
	} {
		registry.Windows = append(registry.Windows, coremetadata.Window{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
			Metadata: coremetadata.ObjectMeta{UID: window.uid, Name: window.name,
				OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindProject, UID: window.project}},
			Spec: coremetadata.WindowSpec{AnchorPaneRef: window.pane},
		})
		registry.Panes = append(registry.Panes, coremetadata.Pane{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
			Metadata: coremetadata.ObjectMeta{UID: window.pane, Name: "zsh",
				OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindWindow, UID: window.uid}},
			Spec: coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell},
		})
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("fixture is not a valid registry: %v", err)
	}

	ref, err := selector.ParseRef(coremetadata.KindPane, "zsh")
	if err != nil {
		t.Fatalf("ParseRef error = %v", err)
	}
	query := selector.Query{Panes: []selector.Ref{ref}}
	resolution, err := selector.New(registry).ResolvePanes(query)
	if err != nil {
		t.Fatalf("ResolvePanes error = %v", err)
	}
	source := selector.Enforce(
		selector.Target{Verb: selector.VerbGet, Kind: coremetadata.KindPane},
		selector.DescribeSelector(query), resolution)
	if source == nil {
		t.Fatal("an ambiguous exact-one read succeeded")
	}

	var stdout, stderr bytes.Buffer
	code := executeCLI(func() error {
		// A read route writes nothing before its resolution succeeds.
		return app.MapMetadataError(source)
	}, func(error) {}, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want 0 bytes", stdout.String())
	}
	if stderr.String() != source.Error()+"\n" {
		t.Fatalf("stderr = %q, want the bounded ambiguity listing", stderr.String())
	}
	if !strings.Contains(stderr.String(), "want exactly one") ||
		!strings.Contains(stderr.String(), "owner=project/alpha window/one") {
		t.Fatalf("stderr does not carry the bounded candidate context:\n%s", stderr.String())
	}
}

// TestFlagParseErrorPrintsReasonOnceAndUsageAtMostOnce runs sample public routes
// through the real app and the entrypoint: a rejected flag leaves its reason on
// stderr exactly once, at most one usage block, nothing on stdout, and exit 2.
// A FlagSet that writes to stderr prints the reason through the flag package;
// one that discards its output (config providers) leaves it to the entrypoint.
func TestFlagParseErrorPrintsReasonOnceAndUsageAtMostOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("TMUX_TMPDIR", t.TempDir())
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("PROJMUX_CWD", "")

	const reason = "flag provided but not defined: -zz"
	for _, argv := range [][]string{
		{"switch", "--zz"},
		{"switch", "open", "--zz"},
		{"window", "recent", "--zz"},
		{"window", "--zz"},
		{"get", "agents", "--zz"},
		{"create", "agent", "--zz"},
		{"rename", "agent", "--zz"},
		{"delete", "window", "--zz"},
		{"runtime", "attach", "--zz"},
		{"runtime", "prune", "--zz"},
		{"config", "providers", "--zz"},
	} {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := executeCLI(func() error { return app.New().Run(argv, &stdout, &stderr) }, func(error) {}, &stderr)
			if code != 2 {
				t.Errorf("exit code = %d, want 2", code)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want 0 bytes", stdout.String())
			}
			reasons, usages := 0, 0
			for line := range strings.SplitSeq(stderr.String(), "\n") {
				if strings.Contains(line, reason) {
					reasons++
				}
				if strings.HasPrefix(line, "Usage") {
					usages++
				}
			}
			if reasons != 1 || usages > 1 {
				t.Errorf("stderr has %d reason lines and %d usage blocks, want 1 and at most 1:\n%s", reasons, usages, stderr.String())
			}
		})
	}
}

// TestFlagValueRefusalsExitTwoAndJournalAsUsage drives the flag value
// refusals through the real entrypoint seam: executeCLI must exit 2 and print
// the unchanged reason once after one Usage block, and the diagnostics outcome
// it records must classify the failure as usage, not runtime.
func TestFlagValueRefusalsExitTwoAndJournalAsUsage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("TMUX_TMPDIR", t.TempDir())
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("PROJMUX_CWD", "")

	for _, test := range []struct {
		argv   []string
		reason string
	}{
		{[]string{"switch", "--ui", "bogus"}, `invalid --ui value "bogus": expected "popup" or "sidebar"`},
		{[]string{"switch", "--anchor", "bogus"}, "switch --anchor requires an exact %N Pane handle"},
		{[]string{"runtime", "sessions", "--ui", "bogus"}, `invalid --ui value "bogus": expected "popup" or "sidebar"`},
		{[]string{"switch", "sidebar-open", "--path", "/work/alpha", "--anchor", "bogus"}, "switch sidebar-open --anchor requires an exact %N Pane handle"},
		{[]string{"switch", "sidebar-open", "--path", "/work/alpha", "--anchor", "%1", "--mode", "bogus"}, `switch sidebar-open: unknown startup mode "bogus"`},
		{[]string{"pin", "project", "list", "--kind", "bogus"}, `unknown pin kind "bogus": use project or candidate`},
		{[]string{"runtime", "attach", "--fallback", "bogus"}, "runtime attach fallback must be one of: home, ephemeral"},
		{[]string{"runtime", "attach", "--keep", "-1"}, "plan auto attach: ephemeral keep count must be non-negative"},
		{[]string{"runtime", "prune", "--keep", "-1"}, "plan ephemeral prune: ephemeral keep count must be non-negative"},
	} {
		t.Run(strings.Join(test.argv, " "), func(t *testing.T) {
			store := diagnostics.NewStore(filepath.Join(t.TempDir(), "diagnostics.jsonl"))
			var stdout, stderr bytes.Buffer
			code := executeCLI(
				func() error { return app.New().Run(test.argv, &stdout, &stderr) },
				func(err error) {
					if recordErr := diagnostics.RecordOutcome(store, test.argv, "run-test", "test", "tmux", time.Now(), err, app.IsUsageError(err), false); recordErr != nil {
						t.Fatalf("RecordOutcome() error = %v", recordErr)
					}
				},
				&stderr,
			)
			if code != 2 {
				t.Errorf("exit code = %d, want 2", code)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want 0 bytes", stdout.String())
			}
			if got := strings.Count(stderr.String(), test.reason); got != 1 {
				t.Errorf("stderr carries the reason %d times, want 1:\n%s", got, stderr.String())
			}
			if got := strings.Count(stderr.String(), "Usage:"); got != 1 {
				t.Errorf("stderr has %d Usage blocks, want 1:\n%s", got, stderr.String())
			}
			if !strings.HasSuffix(stderr.String(), test.reason+"\n") {
				t.Errorf("stderr does not end with the reason line:\n%s", stderr.String())
			}
			events, err := store.Read()
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 1 || events[0].Kind != "usage" || events[0].Result != "error" {
				t.Fatalf("journaled outcome = %+v, want one error event of kind usage", events)
			}
		})
	}
}
