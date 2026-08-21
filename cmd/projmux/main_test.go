package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/app"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
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

	// Two Panes named "zsh" in two Windows: a legal registry, and an ambiguous
	// exact-one read.
	registry := coremetadata.NewRegistry()
	registry.Projects = []coremetadata.Project{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindProject,
		Metadata: coremetadata.ObjectMeta{UID: "prj-1", Name: "alpha"},
		Spec:     coremetadata.ProjectSpec{Root: "/srv/alpha", PrimaryWindowRef: "win-1"},
	}}
	registry.NameReservations = []coremetadata.NameReservation{
		{Kind: coremetadata.KindProject, Name: "alpha", UID: "prj-1"},
		{Scope: "prj-1", Kind: coremetadata.KindWindow, Name: "one", UID: "win-1"},
		{Scope: "prj-1", Kind: coremetadata.KindWindow, Name: "two", UID: "win-2"},
		{Scope: "win-1", Kind: coremetadata.KindPane, Name: "zsh", UID: "pan-1"},
		{Scope: "win-2", Kind: coremetadata.KindPane, Name: "zsh", UID: "pan-2"},
	}
	for _, window := range []struct{ uid, name, pane string }{{"win-1", "one", "pan-1"}, {"win-2", "two", "pan-2"}} {
		registry.Windows = append(registry.Windows, coremetadata.Window{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
			Metadata: coremetadata.ObjectMeta{UID: window.uid, Name: window.name,
				OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindProject, UID: "prj-1"}},
			Spec: coremetadata.WindowSpec{PrimaryPaneRef: window.pane},
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
