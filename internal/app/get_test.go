package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// stubCurrentPath answers the tmux current-pane query without touching tmux.
type stubCurrentPath struct {
	path  string
	err   error
	calls int
}

func (s *stubCurrentPath) CurrentPanePath(context.Context) (string, error) {
	s.calls++
	return s.path, s.err
}

var getFixtureClock = time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)

// getFixtureRegistry mirrors the selector fixture shape the read route needs:
// two Projects that share a displayName, a Window name repeated across
// Projects, a Pane name repeated across owner scopes, and a Pane whose
// displayName duplicates a different Pane's name.
func getFixtureRegistry(t *testing.T) coremetadata.Registry {
	t.Helper()

	registry := coremetadata.NewRegistry()
	reserve := func(scope string, kind coremetadata.Kind, name, uid string) {
		registry.NameReservations = append(registry.NameReservations, coremetadata.NameReservation{
			Scope: scope, Kind: kind, Name: name, UID: uid,
		})
	}
	meta := func(uid, name, displayName string, owner *coremetadata.OwnerRef, labels map[string]string) coremetadata.ObjectMeta {
		return coremetadata.ObjectMeta{
			UID: uid, Name: name, DisplayName: displayName,
			Labels: labels, OwnerRef: owner, CreatedAt: getFixtureClock,
		}
	}

	registry.Projects = []coremetadata.Project{
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindProject,
			Metadata: meta("prj-alpha", "alpha", "projmux", nil, nil),
			Spec:     coremetadata.ProjectSpec{Root: "/srv/alpha"},
			Status:   coremetadata.ProjectStatus{Session: &coremetadata.SessionProjection{Name: "alpha", Live: true}},
		},
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindProject,
			Metadata: meta("prj-beta", "beta", "projmux", nil, nil),
			Spec:     coremetadata.ProjectSpec{Root: "/srv/beta"},
			Status:   coremetadata.ProjectStatus{Session: &coremetadata.SessionProjection{Name: "beta", Live: false}},
		},
	}
	reserve("", coremetadata.KindProject, "alpha", "prj-alpha")
	reserve("", coremetadata.KindProject, "beta", "prj-beta")

	registry.Windows = []coremetadata.Window{
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
			Metadata: meta("win-alpha-main", "main", "", &coremetadata.OwnerRef{Kind: coremetadata.KindProject, UID: "prj-alpha"}, nil),
			Spec:     coremetadata.WindowSpec{PrimaryPaneRef: "pan-alpha-zsh"},
		},
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
			Metadata: meta("win-beta-main", "main", "", &coremetadata.OwnerRef{Kind: coremetadata.KindProject, UID: "prj-beta"}, nil),
			Spec:     coremetadata.WindowSpec{PrimaryPaneRef: "pan-beta-zsh"},
		},
	}
	reserve("prj-alpha", coremetadata.KindWindow, "main", "win-alpha-main")
	reserve("prj-beta", coremetadata.KindWindow, "main", "win-beta-main")

	registry.Panes = []coremetadata.Pane{
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
			Metadata: meta("pan-alpha-zsh", "zsh", "zsh",
				&coremetadata.OwnerRef{Kind: coremetadata.KindWindow, UID: "win-alpha-main"},
				map[string]string{"role": "shell"}),
			Spec: coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell, CWD: "/srv/alpha"},
		},
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
			Metadata: meta("pan-alpha-log", "log", "zsh",
				&coremetadata.OwnerRef{Kind: coremetadata.KindWindow, UID: "win-alpha-main"},
				map[string]string{"role": "sidecar"}),
			Spec: coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell, CWD: "/srv/alpha/logs"},
		},
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
			Metadata: meta("pan-beta-zsh", "zsh", "zsh",
				&coremetadata.OwnerRef{Kind: coremetadata.KindWindow, UID: "win-beta-main"},
				map[string]string{"role": "shell"}),
			Spec: coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell, CWD: "/srv/beta"},
		},
	}
	reserve("win-alpha-main", coremetadata.KindPane, "zsh", "pan-alpha-zsh")
	reserve("win-alpha-main", coremetadata.KindPane, "log", "pan-alpha-log")
	reserve("win-beta-main", coremetadata.KindPane, "zsh", "pan-beta-zsh")

	if err := registry.Validate(); err != nil {
		t.Fatalf("get fixture is not a valid registry: %v", err)
	}
	return registry
}

func newTestGetCommand(t *testing.T, current *stubCurrentPath) *getCommand {
	t.Helper()
	registry := getFixtureRegistry(t)
	return &getCommand{
		loadRegistry: func() (coremetadata.Registry, error) { return registry, nil },
		currentPath:  current,
	}
}

func runGet(t *testing.T, cmd *getCommand, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := cmd.Run(args, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

// TestGetPaneCurrentReadsTheActivePaneCWD covers the canonical Pane current
// read: a path scalar with exactly one trailing newline.
func TestGetPaneCurrentReadsTheActivePaneCWD(t *testing.T) {
	t.Parallel()

	current := &stubCurrentPath{path: "/srv/alpha/worktree\n"}
	stdout, stderr, err := runGet(t, newTestGetCommand(t, current), "pane", "--current", "-o", "cwd")
	if err != nil {
		t.Fatalf("get pane --current -o cwd error = %v", err)
	}
	if stdout != "/srv/alpha/worktree\n" {
		t.Fatalf("stdout = %q, want a single trailing-newline path scalar", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want none", stderr)
	}
	if current.calls != 1 {
		t.Fatalf("tmux current-pane query ran %d times, want 1", current.calls)
	}

	// The long spelling resolves the same projection.
	stdout, _, err = runGet(t, newTestGetCommand(t, &stubCurrentPath{path: "/srv/beta"}), "pane", "--current", "--output", "cwd")
	if err != nil || stdout != "/srv/beta\n" {
		t.Fatalf("--output cwd: stdout=%q err=%v", stdout, err)
	}
}

// TestGetPaneRejectsCWDOutsideThePaneCurrentRead is acceptance criterion 3 at
// the route level. `cwd` is a Pane-current field projection, so every other
// scope of the same route rejects it with a usage error and zero stdout.
func TestGetPaneRejectsCWDOutsideThePaneCurrentRead(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"pane", "-o", "cwd"},
		{"pane", "--output", "cwd"},
		{"pane", "--project", "alpha", "-o", "cwd"},
		{"pane", "--project", "alpha", "--window", "main", "--pane", "zsh", "-o", "cwd"},
		{"pane", "--selector", "role=shell", "-o", "cwd"},
	} {
		current := &stubCurrentPath{path: "/srv/alpha"}
		stdout, _, err := runGet(t, newTestGetCommand(t, current), args...)
		if err == nil {
			t.Fatalf("%v accepted -o cwd", args)
		}
		if !IsUsageError(err) {
			t.Fatalf("%v: error is not a usage error: %v", args, err)
		}
		if stdout != "" {
			t.Fatalf("%v wrote %q to stdout, want 0 bytes", args, stdout)
		}
		if current.calls != 0 {
			t.Fatalf("%v reached tmux %d times, want 0", args, current.calls)
		}
		if !strings.Contains(err.Error(), "only valid on the Pane current read") {
			t.Fatalf("%v error text = %q", args, err)
		}
	}
}

// TestGetPaneCurrentIsCWDOnlyAndTakesNoSelectors keeps the two halves of the
// current read from blending into the selector read.
func TestGetPaneCurrentIsCWDOnlyAndTakesNoSelectors(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		args []string
		want string
	}{
		{args: []string{"pane", "--current"}, want: "supports -o cwd only"},
		{args: []string{"pane", "--current", "-o", "uid"}, want: "supports -o cwd only"},
		{args: []string{"pane", "--current", "-o", "json"}, want: "supports -o cwd only"},
		{args: []string{"pane", "--current", "-o", "cwd", "--project", "alpha"}, want: "does not accept selectors"},
		{args: []string{"pane", "--current", "-o", "cwd", "--window", "main"}, want: "does not accept selectors"},
		{args: []string{"pane", "--current", "-o", "cwd", "--pane", "zsh"}, want: "does not accept selectors"},
		{args: []string{"pane", "--current", "-o", "cwd", "--selector", "role=shell"}, want: "does not accept selectors"},
	} {
		current := &stubCurrentPath{path: "/srv/alpha"}
		stdout, _, err := runGet(t, newTestGetCommand(t, current), test.args...)
		if err == nil || !IsUsageError(err) {
			t.Fatalf("%v error = %v, want a usage error", test.args, err)
		}
		if stdout != "" {
			t.Fatalf("%v wrote %q to stdout, want 0 bytes", test.args, stdout)
		}
		if current.calls != 0 {
			t.Fatalf("%v reached tmux %d times, want 0", test.args, current.calls)
		}
		if !strings.Contains(err.Error(), test.want) {
			t.Fatalf("%v error text = %q, want it to mention %q", test.args, err, test.want)
		}
	}
}

// TestGetPaneCardinalityViolationsAreUsageErrorsWithNoStdout is acceptance
// criterion 4 at the route level: `get pane` is exact-one, so both ambiguity
// and no-match exit through the usage-error path with zero bytes on stdout.
func TestGetPaneCardinalityViolationsAreUsageErrorsWithNoStdout(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "ambiguous across projects",
			args: []string{"pane", "--pane", "zsh"},
			want: "want exactly one",
		},
		{
			name: "ambiguous by omission",
			args: []string{"pane", "--project", "alpha"},
			want: "want exactly one",
		},
		{
			name: "no match",
			args: []string{"pane", "--project", "alpha", "--pane", "nosuch"},
			want: "matched no panes",
		},
		{
			name: "label filter empties the set",
			args: []string{"pane", "--project", "alpha", "--pane", "zsh", "--selector", "role=nosuch"},
			want: "matched no panes",
		},
		{
			name: "missing project scope",
			args: []string{"pane", "--project", "nosuch", "--pane", "zsh"},
			want: "--project nosuch",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			current := &stubCurrentPath{path: "/srv/alpha"}
			stdout, _, err := runGet(t, newTestGetCommand(t, current), test.args...)
			if err == nil {
				t.Fatalf("%v succeeded", test.args)
			}
			if !IsUsageError(err) {
				t.Fatalf("%v: error is not a usage error: %v", test.args, err)
			}
			if stdout != "" {
				t.Fatalf("%v wrote %q to stdout, want 0 bytes", test.args, stdout)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("%v error text = %q, want it to mention %q", test.args, err, test.want)
			}
		})
	}
}

// TestGetPaneAmbiguityListingIsBoundedAndCarriesOwnerContext proves the
// operator-facing half of the bounded ambiguity output.
func TestGetPaneAmbiguityListingIsBoundedAndCarriesOwnerContext(t *testing.T) {
	t.Parallel()

	_, _, err := runGet(t, newTestGetCommand(t, &stubCurrentPath{}), "pane", "--pane", "zsh")
	if err == nil {
		t.Fatal("an ambiguous read succeeded")
	}
	lines := strings.Split(err.Error(), "\n")
	if len(lines) != 3 {
		t.Fatalf("error rendered %d lines:\n%s", len(lines), err)
	}
	for _, want := range []string{
		"pane/zsh displayName=zsh owner=project/alpha window/main",
		"pane/zsh displayName=zsh owner=project/beta window/main",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ambiguity listing is missing %q:\n%s", want, err)
		}
	}
}

// TestGetPaneOutputProjectionTable is the output table for the resolved read.
func TestGetPaneOutputProjectionTable(t *testing.T) {
	t.Parallel()

	scope := []string{"pane", "--project", "alpha", "--window", "main", "--pane", "zsh"}
	for _, test := range []struct {
		name   string
		output []string
		want   string
	}{
		{name: "default", want: "pane/zsh status=live owner=project/alpha window/main\n"},
		{name: "uid", output: []string{"-o", "uid"}, want: "pan-alpha-zsh\n"},
		{name: "name", output: []string{"-o", "name"}, want: "zsh\n"},
		{name: "ref", output: []string{"-o", "ref"}, want: "pane/zsh\n"},
		{name: "none", output: []string{"-o", "none"}, want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			args := append(append([]string{}, scope...), test.output...)
			stdout, stderr, err := runGet(t, newTestGetCommand(t, &stubCurrentPath{}), args...)
			if err != nil {
				t.Fatalf("%v error = %v", args, err)
			}
			if stdout != test.want {
				t.Fatalf("%v stdout = %q, want %q", args, stdout, test.want)
			}
			if stderr != "" {
				t.Fatalf("%v stderr = %q, want none", args, stderr)
			}
		})
	}

	// metadata emits the metadata block only; json emits the whole resource.
	stdout, _, err := runGet(t, newTestGetCommand(t, &stubCurrentPath{}), append(append([]string{}, scope...), "-o", "metadata")...)
	if err != nil {
		t.Fatalf("-o metadata error = %v", err)
	}
	if !strings.Contains(stdout, `"uid": "pan-alpha-zsh"`) || !strings.Contains(stdout, `"displayName": "zsh"`) {
		t.Fatalf("-o metadata stdout = %q", stdout)
	}
	if strings.Contains(stdout, `"spec"`) || strings.Contains(stdout, `"apiVersion"`) {
		t.Fatalf("-o metadata leaked non-metadata fields: %q", stdout)
	}

	stdout, _, err = runGet(t, newTestGetCommand(t, &stubCurrentPath{}), append(append([]string{}, scope...), "-o", "json")...)
	if err != nil {
		t.Fatalf("-o json error = %v", err)
	}
	for _, want := range []string{`"apiVersion": "projmux.io/v1alpha1"`, `"kind": "Pane"`, `"cwd": "/srv/alpha"`} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("-o json stdout is missing %q: %q", want, stdout)
		}
	}

	// An offline Project's Pane still reads, and reports its inherited status.
	stdout, _, err = runGet(t, newTestGetCommand(t, &stubCurrentPath{}),
		"pane", "--project", "beta", "--window", "main", "--pane", "zsh")
	if err != nil {
		t.Fatalf("offline read error = %v", err)
	}
	if stdout != "pane/zsh status=offline owner=project/beta window/main\n" {
		t.Fatalf("offline read stdout = %q", stdout)
	}
}

// TestGetPaneRejectsInvalidOutputTokensAndPositionalArguments covers the rest
// of the route's usage surface.
func TestGetPaneRejectsInvalidOutputTokensAndPositionalArguments(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		args []string
		want string
	}{
		{args: []string{"pane", "-o", "bogus"}, want: `invalid --output "bogus"`},
		{args: []string{"pane", "extra"}, want: "does not accept positional arguments"},
		{args: []string{"pane", "--project", "alpha", "--project", "beta"}, want: "at most one occurrence"},
		{args: []string{"pane", "--selector", "role"}, want: "must be key=value"},
		{args: []string{"pane", "--bogus-flag"}, want: "flag provided but not defined"},
		{args: []string{}, want: "get requires a resource kind"},
		{args: []string{"windows"}, want: "not available"},
		{args: []string{"bogus"}, want: "not available"},
	} {
		stdout, _, err := runGet(t, newTestGetCommand(t, &stubCurrentPath{}), test.args...)
		if err == nil || !IsUsageError(err) {
			t.Fatalf("%v error = %v, want a usage error", test.args, err)
		}
		if stdout != "" {
			t.Fatalf("%v wrote %q to stdout, want 0 bytes", test.args, stdout)
		}
		if !strings.Contains(err.Error(), test.want) {
			t.Fatalf("%v error text = %q, want it to mention %q", test.args, err, test.want)
		}
	}

	// `pane-id` is a catalog member the read route cannot answer without a live
	// transport binding. That is a runtime limitation, not invalid input, so it
	// must not be reported as a usage error.
	_, _, err := runGet(t, newTestGetCommand(t, &stubCurrentPath{}),
		"pane", "--project", "alpha", "--window", "main", "--pane", "zsh", "-o", "pane-id")
	if err == nil {
		t.Fatal("-o pane-id succeeded")
	}
	if IsUsageError(err) {
		t.Fatalf("-o pane-id was classified as invalid input: %v", err)
	}
}

// TestGetPaneSelectorGrammarNegativesNeverResolve is the route-level half of
// acceptance criterion 1: none of the excluded forms can address a resource
// through the CLI either.
func TestGetPaneSelectorGrammarNegativesNeverResolve(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		// Implicit comma split of two real Pane names.
		{"pane", "--project", "alpha", "--pane", "zsh,log"},
		// Implicit comma split of two real Window names.
		{"pane", "--project", "alpha", "--window", "main,review", "--pane", "zsh"},
		// A duplicate-allowed Project displayName.
		{"pane", "--project", "projmux", "--pane", "zsh"},
		// A Pane displayName that duplicates a different Pane's name is not a
		// second way to address the Pane named `log`.
		{"pane", "--project", "alpha", "--window", "main", "--pane", "zsh", "--selector", "role=sidecar"},
		// A real spec.root path.
		{"pane", "--project", "/srv/alpha", "--pane", "zsh"},
		// A real Pane spec.cwd path.
		{"pane", "--project", "alpha", "--pane", "/srv/alpha"},
		// tmux transport handles.
		{"pane", "--project", "alpha", "--pane", "%3"},
		{"pane", "--project", "alpha", "--window", "@1", "--pane", "zsh"},
		{"pane", "--project", "alpha", "--pane", "$0"},
		// A bare uid is not a selector form; only the uid: prefix is.
		{"pane", "--project", "alpha", "--pane", "pan-alpha-zsh"},
	} {
		stdout, _, err := runGet(t, newTestGetCommand(t, &stubCurrentPath{}), args...)
		if err == nil {
			t.Fatalf("%v resolved a Pane", args)
		}
		if !IsUsageError(err) {
			t.Fatalf("%v: error is not a usage error: %v", args, err)
		}
		if stdout != "" {
			t.Fatalf("%v wrote %q to stdout, want 0 bytes", args, stdout)
		}
	}

	// The uid: form does resolve, which is what makes the bare-uid rejection a
	// grammar rule rather than a broken lookup.
	stdout, _, err := runGet(t, newTestGetCommand(t, &stubCurrentPath{}),
		"pane", "--pane", "uid:pan-alpha-zsh", "-o", "uid")
	if err != nil {
		t.Fatalf("uid: form error = %v", err)
	}
	if stdout != "pan-alpha-zsh\n" {
		t.Fatalf("uid: form stdout = %q", stdout)
	}
}

// TestGetPaneReadsTheRegistryWithoutCreatingAnyState proves the route is truly
// read-only against the real store: an operator who has never registered a
// resource must not get a metadata directory created by a failed read.
func TestGetPaneReadsTheRegistryWithoutCreatingAnyState(t *testing.T) {
	home := t.TempDir()
	stateHome := filepath.Join(home, "state")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", stateHome)

	cmd := &getCommand{loadRegistry: loadResourceRegistry, currentPath: &stubCurrentPath{}}
	stdout, _, err := runGet(t, cmd, "pane", "--pane", "zsh")
	if err == nil {
		t.Fatal("a read against an empty registry resolved a Pane")
	}
	if !IsUsageError(err) {
		t.Fatalf("empty-registry read error is not a usage error: %v", err)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want 0 bytes", stdout)
	}

	if entries, statErr := os.ReadDir(stateHome); statErr == nil {
		t.Fatalf("the read created %s with %d entries", stateHome, len(entries))
	} else if !os.IsNotExist(statErr) {
		t.Fatalf("stat %s: %v", stateHome, statErr)
	}
}

// TestGetSkipsTheLegacyHookFilesystemMigration keeps `get` on the read-only
// side of the pre-dispatch boundary. The legacy hook migration writes to the
// operator's config, so a route whose contract is "zero mutations" must not
// trigger it -- including on the failure paths, which run before dispatch.
func TestGetSkipsTheLegacyHookFilesystemMigration(t *testing.T) {
	t.Parallel()

	for _, argv := range [][]string{
		{"get"},
		{"get", "pane"},
		{"get", "pane", "--current", "-o", "cwd"},
		{"get", "pane", "--pane", "zsh"},
		{"get", "bogus"},
	} {
		if shouldRunLegacyHookMigrations(argv) {
			t.Fatalf("shouldRunLegacyHookMigrations(%q) = true, want false", argv)
		}
	}
	// The exclusion is scoped to the `get` route, not to any token containing
	// it.
	for _, argv := range [][]string{
		{"ai", "split", "--", "get"},
		{"notify", "get"},
	} {
		if !shouldRunLegacyHookMigrations(argv) {
			t.Fatalf("shouldRunLegacyHookMigrations(%q) = false, want true", argv)
		}
	}
}
