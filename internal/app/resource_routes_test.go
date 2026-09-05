package app

import (
	"bytes"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

var resourceFixtureClock = time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)

// resourceFixtureReadClock is the clock every fixture-backed read is handed.
//
// It is deliberately a fixed offset from the fixture's own creation stamp
// rather than time.Now: the AGE column of the columnar read is measured against
// this value, so a golden that pins the column is only meaningful while the
// reading clock is injected. Every fixture resource is created at
// resourceFixtureClock, so this offset renders as one and the same `2d` for all
// of them, and the tests that need a spread of ages restamp individual
// resources instead of moving this clock.
var resourceFixtureReadClock = resourceFixtureClock.Add(50*time.Hour + 30*time.Minute)

func addFixtureCanonicalShell(registry *coremetadata.Registry, projectUID, windowUID, paneUID, cwd string) {
	project, ok := registry.Project(projectUID)
	if !ok {
		panic("fixture Project does not exist: " + projectUID)
	}
	project.Spec.PrimaryWindowRef = windowUID
	registry.Windows = append(registry.Windows, coremetadata.Window{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
		Metadata: coremetadata.ObjectMeta{UID: windowUID, Name: "main", OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindProject, UID: projectUID}, CreatedAt: resourceFixtureClock},
		Spec:     coremetadata.WindowSpec{AnchorPaneRef: paneUID},
	})
	registry.Panes = append(registry.Panes, coremetadata.Pane{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
		Metadata: coremetadata.ObjectMeta{UID: paneUID, Name: "shell", OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindWindow, UID: windowUID}, CreatedAt: resourceFixtureClock},
		Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell, CWD: cwd},
	})
	registry.NameReservations = append(registry.NameReservations,
		coremetadata.NameReservation{Scope: projectUID, Kind: coremetadata.KindWindow, Name: "main", UID: windowUID},
		coremetadata.NameReservation{Scope: projectUID, Kind: coremetadata.KindPane, Name: "shell", UID: paneUID},
	)
}

// resourceFixtureRegistry is the shared fixture of the canonical verb-to-kind
// routes.
//
// It is deliberately adversarial for the selector contract: Window, Pane, and
// Agent names repeat across root owners, one Window owns a running Agent with a
// managed Pane, and one Project carries a MissingRoot condition observed long
// ago with no live session.
func resourceFixtureRegistry(t *testing.T) coremetadata.Registry {
	t.Helper()

	registry := coremetadata.NewRegistry()
	reserve := func(scope string, kind coremetadata.Kind, name, uid string) {
		if kind == coremetadata.KindPane || kind == coremetadata.KindAgent {
			if window, ok := registry.Window(scope); ok {
				scope = window.Metadata.OwnerUID()
			} else if agent, ok := registry.Agent(scope); ok {
				if window, ok := registry.Window(agent.Metadata.OwnerUID()); ok {
					scope = window.Metadata.OwnerUID()
				}
			}
		}
		registry.NameReservations = append(registry.NameReservations, coremetadata.NameReservation{
			Scope: scope, Kind: kind, Name: name, UID: uid,
		})
	}
	meta := func(uid, name, _ string, owner *coremetadata.OwnerRef, labels map[string]string) coremetadata.ObjectMeta {
		return coremetadata.ObjectMeta{
			UID: uid, Name: name,
			Labels: labels, OwnerRef: owner, CreatedAt: resourceFixtureClock,
		}
	}
	ownedBy := func(kind coremetadata.Kind, uid string) *coremetadata.OwnerRef {
		return &coremetadata.OwnerRef{Kind: kind, UID: uid}
	}

	registry.Projects = []coremetadata.Project{
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindProject,
			Metadata: meta("prj-alpha", "alpha", "projmux", nil, nil),
			Spec:     coremetadata.ProjectSpec{Root: "/srv/alpha", PrimaryWindowRef: "win-alpha-main"},
			Status:   coremetadata.ProjectStatus{Session: &coremetadata.SessionProjection{Name: "alpha", Live: true}},
		},
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindProject,
			Metadata: meta("prj-beta", "beta", "projmux", nil, nil),
			Spec:     coremetadata.ProjectSpec{Root: "/srv/beta", PrimaryWindowRef: "win-beta-main"},
			Status:   coremetadata.ProjectStatus{Session: &coremetadata.SessionProjection{Name: "beta", Live: false}},
		},
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindProject,
			Metadata: meta("prj-gone", "gone", "gone", nil, nil),
			Spec:     coremetadata.ProjectSpec{Root: "/srv/gone", PrimaryWindowRef: "win-gone-main"},
			Status: coremetadata.ProjectStatus{Conditions: []coremetadata.Condition{{
				Type:             coremetadata.ConditionMissingRoot,
				Status:           coremetadata.ConditionTrue,
				Reason:           "RootDisappeared",
				Message:          "project root /srv/gone is not an existing directory",
				FirstObservedAt:  resourceFixtureClock.Add(-1000 * time.Hour),
				LastTransitionAt: resourceFixtureClock.Add(-1000 * time.Hour),
			}}},
		},
	}
	reserve("", coremetadata.KindProject, "alpha", "prj-alpha")
	reserve("", coremetadata.KindProject, "beta", "prj-beta")
	reserve("", coremetadata.KindProject, "gone", "prj-gone")

	registry.Windows = []coremetadata.Window{
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
			Metadata: meta("win-alpha-main", "main", "", ownedBy(coremetadata.KindProject, "prj-alpha"), map[string]string{"role": "shell"}),
			Spec:     coremetadata.WindowSpec{AnchorPaneRef: "pan-alpha-zsh"},
		},
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
			Metadata: meta("win-alpha-review", "review", "", ownedBy(coremetadata.KindProject, "prj-alpha"), nil),
			Spec:     coremetadata.WindowSpec{AnchorPaneRef: "pan-alpha-review"},
		},
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
			Metadata: meta("win-beta-main", "main", "", ownedBy(coremetadata.KindProject, "prj-beta"), nil),
			Spec:     coremetadata.WindowSpec{AnchorPaneRef: "pan-beta-zsh"},
		},
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
			Metadata: meta("win-gone-main", "main", "", ownedBy(coremetadata.KindProject, "prj-gone"), nil),
			Spec:     coremetadata.WindowSpec{AnchorPaneRef: "pan-gone-zsh"},
		},
	}
	reserve("prj-alpha", coremetadata.KindWindow, "main", "win-alpha-main")
	reserve("prj-alpha", coremetadata.KindWindow, "review", "win-alpha-review")
	reserve("prj-beta", coremetadata.KindWindow, "main", "win-beta-main")
	reserve("prj-gone", coremetadata.KindWindow, "main", "win-gone-main")

	registry.Panes = []coremetadata.Pane{
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
			Metadata: meta("pan-alpha-zsh", "zsh", "zsh", ownedBy(coremetadata.KindWindow, "win-alpha-main"), map[string]string{"role": "shell"}),
			Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell, CWD: "/srv/alpha"},
		},
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
			Metadata: meta("pan-alpha-log", "log", "zsh", ownedBy(coremetadata.KindWindow, "win-alpha-main"), map[string]string{"role": "sidecar"}),
			Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell, CWD: "/srv/alpha/logs"},
		},
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
			Metadata: meta("pan-alpha-codex", "codex-pane", "", ownedBy(coremetadata.KindAgent, "agt-alpha-codex"), nil),
			Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleAgent, CWD: "/srv/alpha"},
		},
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
			Metadata: meta("pan-alpha-review", "review-zsh", "", ownedBy(coremetadata.KindWindow, "win-alpha-review"), map[string]string{"role": "shell"}),
			Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell, CWD: "/srv/alpha"},
		},
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
			Metadata: meta("pan-beta-zsh", "zsh", "zsh", ownedBy(coremetadata.KindWindow, "win-beta-main"), map[string]string{"role": "shell"}),
			Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell, CWD: "/srv/beta"},
		},
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
			Metadata: meta("pan-gone-zsh", "zsh", "zsh", ownedBy(coremetadata.KindWindow, "win-gone-main"), map[string]string{"role": "shell"}),
			Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell, CWD: "/srv/gone"},
		},
	}
	reserve("win-alpha-main", coremetadata.KindPane, "zsh", "pan-alpha-zsh")
	reserve("win-alpha-main", coremetadata.KindPane, "log", "pan-alpha-log")
	reserve("prj-alpha", coremetadata.KindPane, "codex-pane", "pan-alpha-codex")
	reserve("win-alpha-review", coremetadata.KindPane, "review-zsh", "pan-alpha-review")
	reserve("win-beta-main", coremetadata.KindPane, "zsh", "pan-beta-zsh")
	reserve("win-gone-main", coremetadata.KindPane, "zsh", "pan-gone-zsh")

	registry.Agents = []coremetadata.Agent{
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindAgent,
			Metadata: meta("agt-alpha-codex", "codex", "", ownedBy(coremetadata.KindWindow, "win-alpha-main"), map[string]string{"provider": "codex"}),
			Spec:     coremetadata.AgentSpec{Provider: "codex"},
			Status: coremetadata.AgentStatus{
				Phase: coremetadata.PhaseRunning, PaneRef: "pan-alpha-codex",
				LastTransitionAt: resourceFixtureClock,
			},
		},
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindAgent,
			Metadata: meta("agt-beta-codex", "codex", "", ownedBy(coremetadata.KindWindow, "win-beta-main"), nil),
			Spec:     coremetadata.AgentSpec{Provider: "codex"},
			Status: coremetadata.AgentStatus{
				Phase: coremetadata.PhaseOffline, LastTransitionAt: resourceFixtureClock,
			},
		},
	}
	reserve("win-alpha-main", coremetadata.KindAgent, "codex", "agt-alpha-codex")
	reserve("win-beta-main", coremetadata.KindAgent, "codex", "agt-beta-codex")

	registry = registry.Normalize()
	if err := registry.Validate(); err != nil {
		t.Fatalf("resource fixture is not a valid registry: %v", err)
	}
	return registry
}

// fakeResourceStore is an in-memory stand-in for the locked registry file.
//
// It reproduces the two properties the routes depend on: a read hands back a
// private copy, and a write only lands when the whole transaction succeeds and
// the result validates. It also counts both, so a test can prove a failed
// operation performed zero writes.
type fakeResourceStore struct {
	registry coremetadata.Registry
	// transactions counts how many times a route opened the store for writing.
	transactions int
	// writes counts how many transactions actually committed.
	writes int
	// reads counts how many times a route opened the store for reading.
	reads   int
	dirs    map[string]bool
	now     time.Time
	newUIDs []string
}

func newFakeResourceStore(t *testing.T) *fakeResourceStore {
	t.Helper()
	return &fakeResourceStore{
		registry: resourceFixtureRegistry(t),
		dirs:     map[string]bool{"/srv/alpha": true, "/srv/beta": true},
		now:      resourceFixtureClock,
	}
}

func (s *fakeResourceStore) mutator() coremetadata.Mutator {
	return coremetadata.Mutator{
		Now:       func() time.Time { return s.now },
		DirExists: func(path string) (bool, error) { return s.dirs[path], nil },
		// Deterministic but unique: an operation that creates several resources of
		// one kind must not be handed the same uid twice.
		NewUID: func(kind coremetadata.Kind) (string, error) {
			uid := fmt.Sprintf("%s-test-%d", strings.ToLower(string(kind)), len(s.newUIDs)+1)
			s.newUIDs = append(s.newUIDs, uid)
			return uid, nil
		},
	}
}

func (s *fakeResourceStore) store() *resourceStore {
	update := func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
		s.transactions++
		working := s.registry.Clone()
		if err := fn(&working); err != nil {
			return coremetadata.Registry{}, err
		}
		working = working.Normalize()
		if err := working.Validate(); err != nil {
			return coremetadata.Registry{}, err
		}
		s.registry = working
		s.writes++
		return working, nil
	}
	return &resourceStore{
		load:     func() (coremetadata.Registry, error) { s.reads++; return s.registry.Clone(), nil },
		snapshot: func() (coremetadata.Registry, error) { s.reads++; return s.registry.Clone(), nil },
		update:   update,
		updateConvergent: func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error) {
			s.transactions++
			before := s.registry.Clone().Normalize()
			working := s.registry.Clone()
			if err := fn(&working); err != nil {
				return coremetadata.Registry{}, false, err
			}
			working = working.Normalize()
			if err := working.Validate(); err != nil {
				return coremetadata.Registry{}, false, err
			}
			if reflect.DeepEqual(before, working) {
				return working, false, nil
			}
			s.registry = working
			s.writes++
			return working, true, nil
		},
		mutator: s.mutator,
	}
}

// snapshot renders the whole registry as a comparable string so a test can
// assert "zero mutations" over every resource at once rather than field by
// field.
func (s *fakeResourceStore) snapshot() string {
	var b strings.Builder
	for _, project := range s.registry.Projects {
		b.WriteString("project " + project.Metadata.UID + " " + project.Metadata.Name + " " + project.Spec.Root + "\n")
	}
	for _, window := range s.registry.Windows {
		b.WriteString("window " + window.Metadata.UID + " " + window.Metadata.Name + " " + window.Spec.AnchorPaneRef + "\n")
	}
	for _, pane := range s.registry.Panes {
		b.WriteString("pane " + pane.Metadata.UID + " " + pane.Metadata.Name +
			" " + pane.Status.Activation.Generation + " " + renderTerminationEvidence(pane.Status.LastTermination) + "\n")
	}
	for _, agent := range s.registry.Agents {
		b.WriteString("agent " + agent.Metadata.UID + " " + agent.Metadata.Name + " " + string(agent.Status.Phase) + " " + agent.Status.PaneRef +
			" " + renderTerminationEvidence(agent.Status.LastTermination) + "\n")
	}
	for _, reservation := range s.registry.NameReservations {
		b.WriteString("reservation " + reservation.Scope + "/" + string(reservation.Kind) + "/" + reservation.Name + " " + reservation.UID + "\n")
	}
	return b.String()
}

// renderTerminationEvidence spells one receipt for the snapshot, so a "zero
// mutations" assertion covers termination evidence too: a route that recorded
// intent and failed to withdraw it changes the snapshot.
func renderTerminationEvidence(receipt *coremetadata.TerminationEvidence) string {
	if receipt == nil {
		return "-"
	}
	code := "-"
	if receipt.ExitCode != nil {
		code = fmt.Sprintf("%d", *receipt.ExitCode)
	}
	signal := receipt.Signal
	if signal == "" {
		signal = "-"
	}
	return fmt.Sprintf("%s/%s/%s/%s/%s/%s", receipt.Source, receipt.Classification,
		receipt.Generation, code, signal, receipt.OperationID)
}

// runRoute runs one raw-argv handler and returns its captured streams.
func runRoute(t *testing.T, cmd rawArgvCommand, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := cmd.Run(args, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

func newTestDescribeCommand(t *testing.T, store *fakeResourceStore) *describeCommand {
	t.Helper()
	return &describeCommand{loadRegistry: store.store().load, runtime: liveAlphaRuntime()}
}

func newTestListGetCommand(t *testing.T, store *fakeResourceStore) *getCommand {
	t.Helper()
	return &getCommand{
		loadRegistry: store.store().load,
		currentPath:  &stubCurrentPath{},
		runtime:      liveAlphaRuntime(),
		now:          func() time.Time { return resourceFixtureReadClock },
	}
}

// TestGetReadFamilyResolvesEveryKindWithListCardinality is the read half of the
// route parity table: each plural spelling is a 0..N read over its own kind and
// scope, and an empty result is a success rather than a not-resolved error.
func TestGetReadFamilyResolvesEveryKindWithListCardinality(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "projects list every registered project",
			args: []string{"projects", "-o", "name"},
			want: "alpha\nbeta\ngone\n",
		},
		{
			name: "projects narrow to the exact-one project scope",
			args: []string{"projects", "--project", "alpha", "-o", "uid"},
			want: "prj-alpha\n",
		},
		{
			name: "windows are project scoped",
			args: []string{"windows", "--project", "alpha", "-o", "name"},
			want: "main\nreview\n",
		},
		{
			name: "windows across the whole registry keep the duplicate name",
			args: []string{"windows", "-o", "uid"},
			want: "win-alpha-main\nwin-alpha-review\nwin-beta-main\nwin-gone-main\n",
		},
		{
			name: "panes include the agent managed pane of the window scope",
			args: []string{"panes", "--project", "alpha", "--window", "main", "-o", "uid"},
			want: "pan-alpha-zsh\npan-alpha-log\npan-alpha-codex\n",
		},
		{
			name: "panes accept a label filter",
			args: []string{"panes", "--project", "alpha", "--selector", "role=sidecar", "-o", "name"},
			want: "log\n",
		},
		{
			name: "agents use the selected root and Window target set",
			args: []string{"agents", "--project", "alpha", "-o", "uid"},
			want: "agt-alpha-codex\n",
		},
		{
			name: "an empty list is a success with empty stdout",
			args: []string{"agents", "--project", "gone", "-o", "uid"},
			want: "",
		},
		{
			name: "a label filter that matches nothing is still a success",
			args: []string{"windows", "--project", "alpha", "--selector", "role=nosuch", "-o", "uid"},
			want: "",
		},
		{
			name: "the default projection is a header plus space-aligned columns",
			args: []string{"windows", "--project", "beta"},
			want: "CONTEXT  SOURCE           OBSERVED  NAME  STATUS   PROJECT  AGE\n" +
				"window   window-fallback  false     main  offline  beta     2d\n",
		},
		{
			name: "ref projection carries the kind",
			args: []string{"agents", "--project", "beta", "-o", "ref"},
			want: "agent/codex\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			stdout, stderr, err := runRoute(t, newTestListGetCommand(t, store), append([]string{"get"}[1:], test.args...)...)
			if err != nil {
				t.Fatalf("get %v error = %v", test.args, err)
			}
			if stdout != test.want {
				t.Fatalf("get %v stdout = %q, want %q", test.args, stdout, test.want)
			}
			if stderr != "" {
				t.Fatalf("get %v stderr = %q, want none", test.args, stderr)
			}
		})
	}
}

// TestGetListStructuredOutputUsesAListEnvelope pins the fan-out half of the
// output contract: scalar modes emit one line per match, structured modes emit a
// single List document.
func TestGetListStructuredOutputUsesAListEnvelope(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	stdout, _, err := runRoute(t, newTestListGetCommand(t, store), "windows", "--project", "alpha", "-o", "json")
	if err != nil {
		t.Fatalf("get windows -o json error = %v", err)
	}
	for _, want := range []string{
		`"apiVersion": "projmux.io/v1alpha1"`,
		`"kind": "WindowList"`,
		`"uid": "win-alpha-main"`,
		`"uid": "win-alpha-review"`,
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("get windows -o json is missing %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stdout, `"context"`) {
		t.Fatalf("get windows -o json omitted invocation context:\n%s", stdout)
	}

	stdout, _, err = runRoute(t, newTestListGetCommand(t, store), "agents", "--project", "alpha", "-o", "metadata")
	if err != nil {
		t.Fatalf("get agents -o metadata error = %v", err)
	}
	if !strings.Contains(stdout, `"kind": "AgentMetadataList"`) {
		t.Fatalf("get agents -o metadata envelope = %s", stdout)
	}
	if strings.Contains(stdout, `"spec"`) || strings.Contains(stdout, `"status"`) {
		t.Fatalf("-o metadata leaked spec or status fields: %s", stdout)
	}
	if strings.Contains(stdout, `"context"`) {
		t.Fatalf("get agents -o metadata leaked ephemeral context: %s", stdout)
	}

	// An empty structured read still emits a document with an empty item list,
	// never a null.
	stdout, _, err = runRoute(t, newTestListGetCommand(t, store), "agents", "--project", "gone", "-o", "json")
	if err != nil {
		t.Fatalf("empty -o json error = %v", err)
	}
	if !strings.Contains(stdout, `"items": []`) {
		t.Fatalf("empty -o json = %s, want an empty items array", stdout)
	}
}

// describeRows parses the description block into key -> ordered values so the
// assertions pin content rather than the alignment column, which varies with the
// widest key of each kind.
func describeRows(t *testing.T, stdout string) map[string][]string {
	t.Helper()
	rows := map[string][]string{}
	for line := range strings.SplitSeq(strings.TrimSuffix(stdout, "\n"), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			t.Fatalf("description line %q is not a key/value row", line)
		}
		rows[strings.TrimSpace(key)] = append(rows[strings.TrimSpace(key)], strings.TrimSpace(value))
	}
	return rows
}

// TestDescribeResolvesExactlyOneResourcePerKind is the singular half of the read
// parity table.
func TestDescribeResolvesExactlyOneResourcePerKind(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		want map[string]string
	}{
		{
			name: "project",
			args: []string{"project", "alpha"},
			want: map[string]string{
				"Kind": "Project", "Name": "alpha", "UID": "prj-alpha", "Context": "alpha",
				"ContextSource": "project-root-basename", "ContextObserved": "false",
				"Root": "/srv/alpha", "Session": "alpha live=true", "Status": "live",
				"CreatedAt": "2026-08-15T09:00:00Z",
			},
		},
		{
			name: "project with a missing root reports the condition",
			args: []string{"project", "gone"},
			want: map[string]string{
				"Status": "missing-root",
				"Condition": "MissingRoot=True reason=RootDisappeared " +
					"firstObservedAt=2026-07-04T17:00:00Z lastTransitionAt=2026-07-04T17:00:00Z",
			},
		},
		{
			name: "window",
			args: []string{"window", "review", "--project", "alpha"},
			want: map[string]string{
				"Kind": "Window", "Name": "review", "UID": "win-alpha-review",
				"Context": "window", "ContextSource": "window-fallback", "ContextObserved": "false",
				"Owner": "project/alpha", "AnchorPaneRef": "pan-alpha-review", "Status": "live",
			},
		},
		{
			name: "pane",
			args: []string{"pane", "log", "--project", "alpha", "--window", "main"},
			want: map[string]string{
				"Kind": "Pane", "Name": "log", "UID": "pan-alpha-log",
				"Role": "shell", "CWD": "/srv/alpha/logs", "Labels": "role=sidecar",
				"Owner": "project/alpha window/main",
			},
		},
		{
			name: "agent",
			args: []string{"agent", "codex", "--project", "alpha"},
			want: map[string]string{
				"Kind": "Agent", "Name": "codex", "UID": "agt-alpha-codex",
				"Context": "codex", "ContextSource": "agent-provider", "ContextObserved": "false",
				"Provider": "codex", "Phase": "Running", "PaneRef": "pan-alpha-codex",
				"Owner": "project/alpha window/main", "PhaseSince": "2026-08-15T09:00:00Z",
			},
		},
		{
			name: "the uid form addresses the same resource through its whole owner chain",
			args: []string{"pane", "uid:pan-alpha-codex", "--project", "alpha"},
			want: map[string]string{
				"UID": "pan-alpha-codex", "Role": "agent",
				"Owner": "project/alpha window/main agent/codex",
			},
		},
		{
			name: "flags may precede the positional reference",
			args: []string{"window", "--project", "alpha", "review"},
			want: map[string]string{"UID": "win-alpha-review"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			stdout, stderr, err := runRoute(t, newTestDescribeCommand(t, store), test.args...)
			if err != nil {
				t.Fatalf("describe %v error = %v", test.args, err)
			}
			rows := describeRows(t, stdout)
			for key, want := range test.want {
				got := rows[key]
				if len(got) != 1 || got[0] != want {
					t.Fatalf("describe %v row %q = %v, want [%q]\n%s", test.args, key, got, want, stdout)
				}
			}
			if stderr != "" {
				t.Fatalf("describe %v stderr = %q, want none", test.args, stderr)
			}
		})
	}
}

// TestDescribeCardinalityViolationsAreUsageErrorsWithNoStdout proves the
// singular reads adopt the declared exact-one cardinality rather than picking a
// winner out of an ambiguous set.
func TestDescribeCardinalityViolationsAreUsageErrorsWithNoStdout(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "ambiguous window name across projects", args: []string{"window", "main"}, want: "want exactly one"},
		{name: "ambiguous by omission", args: []string{"pane", "--project", "alpha"}, want: "want exactly one"},
		{name: "ambiguous agent name across projects", args: []string{"agent", "codex"}, want: "want exactly one"},
		{name: "no match", args: []string{"project", "nosuch"}, want: "matched no projects"},
		{name: "display name is never a selector", args: []string{"project", "projmux"}, want: "matched no projects"},
		{name: "bare uid is not a selector form", args: []string{"project", "prj-alpha"}, want: "matched no projects"},
		{name: "comma is never split", args: []string{"window", "main,review", "--project", "alpha"}, want: "matched no windows"},
		{name: "two positional refs", args: []string{"project", "alpha", "beta"}, want: "at most one resource reference"},
		{name: "flags after the second ref still fail", args: []string{"project", "alpha", "beta", "--selector", "a=b"}, want: "at most one resource reference"},
		{name: "unknown kind", args: []string{"snapshot"}, want: "not available"},
		{name: "no kind", args: nil, want: "describe requires a resource kind"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			stdout, _, err := runRoute(t, newTestDescribeCommand(t, store), test.args...)
			if err == nil {
				t.Fatalf("describe %v succeeded", test.args)
			}
			if !IsUsageError(err) {
				t.Fatalf("describe %v error is not a usage error: %v", test.args, err)
			}
			if stdout != "" {
				t.Fatalf("describe %v wrote %q to stdout, want 0 bytes", test.args, stdout)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("describe %v error = %q, want it to mention %q", test.args, err, test.want)
			}
		})
	}
}

// TestReadRoutesStayOffTheLegacyHookMigrationPath keeps `describe` on the same
// read-only side of the pre-dispatch boundary as `doctor` and `get`.
func TestReadRoutesStayOffTheLegacyHookMigrationPath(t *testing.T) {
	t.Parallel()

	for _, argv := range [][]string{
		{"describe"},
		{"describe", "project"},
		{"describe", "project", "alpha"},
		{"get", "projects"},
		{"get", "notifications"},
	} {
		if shouldRunLegacyHookMigrations(argv) {
			t.Fatalf("shouldRunLegacyHookMigrations(%q) = true, want false", argv)
		}
	}
	// The mutation routes keep the historical pre-dispatch behavior, so this
	// exclusion cannot silently spread across the whole new surface.
	for _, argv := range [][]string{
		{"delete", "pane", "zsh"},
		{"rebind", "project", "alpha", "--root", "/srv/new"},
		{"rename", "project", "alpha", "--name", "gamma"},
		{"runtime", "sessions"},
		{"restore", "snapshot", "alpha"},
	} {
		if !shouldRunLegacyHookMigrations(argv) {
			t.Fatalf("shouldRunLegacyHookMigrations(%q) = false, want true", argv)
		}
	}
}

// TestConfirmerRefusesWithoutATTYAndHonorsTheAnswer pins the destructive
// confirmation gate itself.
func TestConfirmerRefusesWithoutATTYAndHonorsTheAnswer(t *testing.T) {
	t.Parallel()

	nonInteractive := &confirmer{interactive: func() bool { return false }}
	err := nonInteractive.confirm(false, "prompt", "refusal text", io.Discard)
	if err == nil || !IsUsageError(err) {
		t.Fatalf("non-interactive refusal error = %v, want a usage error", err)
	}
	if !strings.Contains(err.Error(), "refusal text") {
		t.Fatalf("refusal error = %q", err)
	}
	if err := nonInteractive.confirm(true, "prompt", "refusal text", io.Discard); err != nil {
		t.Fatalf("--yes still refused: %v", err)
	}

	var asked []string
	interactive := &confirmer{
		interactive: func() bool { return true },
		ask: func(prompt string, _ io.Writer) (bool, error) {
			asked = append(asked, prompt)
			return false, nil
		},
	}
	err = interactive.confirm(false, "really?", "refusal text", io.Discard)
	if err == nil || !IsUsageError(err) {
		t.Fatalf("declined confirmation error = %v, want a usage error", err)
	}
	if !reflect.DeepEqual(asked, []string{"really?"}) {
		t.Fatalf("prompts = %v, want exactly one", asked)
	}

	// A --yes run never reaches the prompt at all.
	asked = nil
	if err := interactive.confirm(true, "really?", "refusal text", io.Discard); err != nil {
		t.Fatalf("--yes on a TTY returned %v", err)
	}
	if len(asked) != 0 {
		t.Fatalf("--yes still prompted: %v", asked)
	}
}
