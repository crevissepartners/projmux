package app

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/candidates"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// The Projects picker, with the Registry as its row source.
//
// The assertions here are about which rows exist, in which order, and what a
// selection carries -- not about the card's glyphs. A Registry Project is a row
// whether or not tmux answered; a discovered directory is a row in its own
// section; and the Runtime link is the last thing on the list.

// switchRegistryFixture wires a Projects picker over the navigation fixture's
// exact host so the rows come from the same Registry the navigation tests use.
func switchRegistryFixture(t *testing.T, inherited string, discovered []string, result string) (*switchCommand, *oneShotSwitchRunner) {
	t.Helper()
	reader, _, _, _ := navigationFixtureReader(t, "1", inherited)
	runner := &oneShotSwitchRunner{result: intpickercompat.Result{Value: result}}
	return &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) { return discovered, nil },
		pinStore: func() (switchPinStore, error) { return &stubSwitchPinStore{}, nil },
		runner:   runner,
		// The picker is driven through the compat shim the shipped surfaces use
		// so the entries asserted are the entries a real run would render.
		nativePicker: nativePickerFromCompatRunner(runner),
		sessions:     &capturingSwitchSessionExecutor{},
		identity: switchIdentityResolverFunc(func(path string) (string, error) {
			if strings.TrimSpace(path) == "" {
				return "", errors.New("empty path")
			}
			return strings.ReplaceAll(strings.TrimPrefix(path, "/"), "/", "-"), nil
		}),
		validate:      func(string) error { return nil },
		homeDir:       func() (string, error) { return "/home/tester", nil },
		workingDir:    func() (string, error) { return "/src", nil },
		lookupEnv:     func(name string) string { return map[string]string{"TMUX": inherited}[name] },
		gitBranch:     func(string) string { return "" },
		executable:    func() (string, error) { return "/tmp/projmux", nil },
		rawExecutable: func() (string, error) { return "/tmp/projmux", nil },
		navigation: &registryNavigationCommand{
			reader:    reader,
			native:    &scriptedNavigationPicker{},
			homeDir:   func() (string, error) { return t.TempDir(), nil },
			lookupEnv: func(name string) string { return map[string]string{"TMUX": inherited}[name] },
		},
	}, runner
}

func switchEntryValues(entries []intpickercompat.Entry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Value)
	}
	return out
}

// TestSwitchRowsAreRegistryFirstAndSectioned pins the row source: the Registry
// Project leads, the discovered directory that no Project claims follows, and
// the Runtime link is last.
func TestSwitchRowsAreRegistryFirstAndSectioned(t *testing.T) {
	t.Parallel()

	command, runner := switchRegistryFixture(t, "/tmp/fake-tmux/primary,0,0",
		[]string{"/src/gamma", "/src/alpha"}, "")
	if err := command.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := []string{"/src/alpha", "/src/gamma", switchRuntimeSentinel}
	if got := switchEntryValues(runner.last.Entries); !reflect.DeepEqual(got, want) {
		t.Fatalf("entry values = %#v, want %#v", got, want)
	}
}

// TestSwitchRowsListRegistryProjectsWithoutATmuxServer is acceptance (1) at the
// picker: the Registry Project is a row with no transport at all, and the
// discovered directory it shadows is still not duplicated.
func TestSwitchRowsListRegistryProjectsWithoutATmuxServer(t *testing.T) {
	t.Parallel()

	command, runner := switchRegistryFixture(t, "", []string{"/src/alpha", "/src/gamma"}, "")
	if err := command.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := []string{"/src/alpha", "/src/gamma", switchRuntimeSentinel}
	if got := switchEntryValues(runner.last.Entries); !reflect.DeepEqual(got, want) {
		t.Fatalf("no-server entry values = %#v, want %#v", got, want)
	}
	labels := strings.Join(entryLabels(runner.last.Entries), "\n")
	if !strings.Contains(labels, "Runtime - no tmux transport") {
		t.Fatalf("no-server runtime link does not say why it is empty:\n%s", labels)
	}
}

// TestSwitchRowIdentityAndOrderSurviveARuntimeTransition is acceptance (2) at the
// picker: the same Registry renders the same values in the same order whether or
// not the runtime is up, so a selection cannot move under an operator.
func TestSwitchRowIdentityAndOrderSurviveARuntimeTransition(t *testing.T) {
	t.Parallel()

	live, liveRunner := switchRegistryFixture(t, "/tmp/fake-tmux/primary,0,0",
		[]string{"/src/gamma", "/src/alpha"}, "")
	if err := live.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("live Run() error = %v", err)
	}
	dark, darkRunner := switchRegistryFixture(t, "", []string{"/src/gamma", "/src/alpha"}, "")
	if err := dark.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("no-server Run() error = %v", err)
	}

	if got, want := switchEntryValues(darkRunner.last.Entries), switchEntryValues(liveRunner.last.Entries); !reflect.DeepEqual(got, want) {
		t.Fatalf("no-server entry values = %#v, want the live values %#v", got, want)
	}
}

// TestSwitchRowsWithdrawRuntimeOnlyObjects is acceptance (3) at the picker: the
// Home control session, a scratch session and a hand-opened window are on the
// fixture server and none of them is a row.
func TestSwitchRowsWithdrawRuntimeOnlyObjects(t *testing.T) {
	t.Parallel()

	command, runner := switchRegistryFixture(t, "/tmp/fake-tmux/primary,0,0", []string{"/src/alpha"}, "")
	if err := command.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	labels := strings.Join(entryLabels(runner.last.Entries), "\n")
	for _, forbidden := range []string{"Home", "scratch", "ghost", "notes"} {
		if strings.Contains(labels, forbidden) {
			t.Fatalf("Projects list names the runtime-only object %q:\n%s", forbidden, labels)
		}
	}
	if !strings.Contains(labels, "Runtime - ") {
		t.Fatalf("Projects list has no Runtime link to find them through:\n%s", labels)
	}
	for _, want := range []string{"control 1", "ephemeral 1", "unattributed"} {
		if !strings.Contains(labels, want) {
			t.Fatalf("Runtime link does not name %q:\n%s", want, labels)
		}
	}
}

// TestSwitchMissingRootProjectSelectsByUID pins the one row whose selection is
// not a path. Before this, a Project whose spec.root vanished failed the whole
// picker on directory validation instead of offering the repair.
func TestSwitchMissingRootProjectSelectsByUID(t *testing.T) {
	t.Parallel()

	reader, _, _, _ := navigationFixtureReader(t, "1", "")
	registry := runtimeFixtureRegistry()
	registry.Projects[0].Status.Conditions = []coremetadata.Condition{{
		Type:   coremetadata.ConditionMissingRoot,
		Status: coremetadata.ConditionTrue,
	}}
	reader.reader.loadRegistry = func() (coremetadata.Registry, error) { return registry, nil }

	command, runner := switchRegistryFixture(t, "", []string{"/src/gamma"}, "")
	command.navigation.reader = reader
	command.validate = func(string) error { return errors.New("directory does not exist") }
	if err := command.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := []string{"uid:" + runtimeFixtureProject, "/src/gamma", switchRuntimeSentinel}
	if got := switchEntryValues(runner.last.Entries); !reflect.DeepEqual(got, want) {
		t.Fatalf("entry values = %#v, want the missing-root Project selected by uid %#v", got, want)
	}
	if labels := strings.Join(entryLabels(runner.last.Entries), "\n"); !strings.Contains(labels, "missing root") {
		t.Fatalf("missing-root row does not say so:\n%s", labels)
	}
}

// TestSwitchRuntimeSelectionOpensTheEscapeHatch pins where the Runtime link
// goes: the shipped diagnostics route, with no path validation on the way.
func TestSwitchRuntimeSelectionOpensTheEscapeHatch(t *testing.T) {
	t.Parallel()

	command, _ := switchRegistryFixture(t, "", []string{"/src/gamma"}, switchRuntimeSentinel)
	route := &navigationArgvRecorder{}
	command.navigation.runtime = route
	command.validate = func(string) error { return errors.New("the runtime link is not a directory") }

	if err := command.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := route.calls, [][]string{{"diagnostics"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("runtime route calls = %#v, want %#v", got, want)
	}
}

// TestSwitchHierarchyKeyOpensTheSelectedProject pins the dedicated key: it
// resolves the selected row's Project and opens the read-only hierarchy.
func TestSwitchHierarchyKeyOpensTheSelectedProject(t *testing.T) {
	t.Parallel()

	command, runner := switchRegistryFixture(t, "/tmp/fake-tmux/primary,0,0",
		[]string{"/src/alpha"}, "/src/alpha")
	runner.result.Key = registryNavigationExpectKey
	hierarchy := &scriptedNavigationPicker{values: []string{""}}
	command.navigation.native = hierarchy

	if err := command.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(hierarchy.seen) == 0 {
		t.Fatal("the hierarchy surface never opened")
	}
	if got, want := hierarchy.seen[0].Title, "Projects > Resources"; got != want {
		t.Fatalf("hierarchy title = %q, want %q", got, want)
	}
}

// TestRegistryProjectUIDForSelectionDeclinesAnUnregisteredPath is the honesty
// guard on the path lookup: an unregistered directory has no Project, and the
// lookup says so rather than choosing the nearest one.
func TestRegistryProjectUIDForSelectionDeclinesAnUnregisteredPath(t *testing.T) {
	t.Parallel()

	command, _ := switchRegistryFixture(t, "", nil, "")
	uid, err := command.registryProjectUIDForSelection(context.Background(), "/src/gamma")
	if err != nil {
		t.Fatalf("registryProjectUIDForSelection: %v", err)
	}
	if uid != "" {
		t.Fatalf("unregistered path resolved to Project %q, want no Project", uid)
	}
	uid, err = command.registryProjectUIDForSelection(context.Background(), "/src/alpha")
	if err != nil {
		t.Fatalf("registryProjectUIDForSelection: %v", err)
	}
	if uid != runtimeFixtureProject {
		t.Fatalf("Project root resolved to %q, want %q", uid, runtimeFixtureProject)
	}
}

// TestSwitchRegistryWindowTabsComeFromTheRegistry pins the card's window tabs:
// they are the Registry's Windows with the runtime deciding only which is live,
// so an offline Project still shows the topology the Registry knows.
func TestSwitchRegistryWindowTabsComeFromTheRegistry(t *testing.T) {
	t.Parallel()

	reader, _, _, _ := navigationFixtureReader(t, "1", "")
	view, err := reader.view(context.Background(), nil)
	if err != nil {
		t.Fatalf("navigation view: %v", err)
	}
	project, ok := view.Row("uid:" + runtimeFixtureProject)
	if !ok {
		t.Fatal("the fixture Project row is absent")
	}
	tabs := switchRegistryWindowTabs(view, project, "dot")
	if len(tabs) != 1 || tabs[0].Name != "editor" {
		t.Fatalf("window tabs = %#v, want the Registry Window with no transport", tabs)
	}
	if tabs[0].Active {
		t.Fatalf("window tab is marked active with no observed runtime: %#v", tabs[0])
	}
}

func entryLabels(entries []intpickercompat.Entry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Label)
	}
	return out
}

var _ intpicker.Runner = (*scriptedNavigationPicker)(nil)

// oneShotSwitchRunner answers the first picker run with its scripted result and
// every later run with a cancel.
//
// The Projects picker reopens itself after an action that returns to the list,
// which is correct behavior and an infinite loop for a runner that answers the
// same way forever. Consuming the result once is what lets a test assert what
// one selection did.
type oneShotSwitchRunner struct {
	last     intpickercompat.Options
	result   intpickercompat.Result
	consumed bool
}

func (r *oneShotSwitchRunner) Run(options intpickercompat.Options) (intpickercompat.Result, error) {
	r.last = options
	if r.consumed {
		return intpickercompat.Result{}, nil
	}
	r.consumed = true
	return r.result, nil
}

// The sidebar's presentation order.
//
// Membership is the Registry's and order is the sidebar's. The fixtures below
// deliberately give the Registry a slice order that disagrees with every tier,
// so an assertion that passes cannot be passing by accident of insertion order.

// switchOrderProject is one Project of the presentation-order fixture.
type switchOrderProject struct {
	uid    string
	name   string
	root   string
	live   bool
	pinned bool
}

// switchOrderReader builds the navigation reader of the presentation-order
// fixture: one Registry, and one exact fake host carrying a live session for
// every Project marked live.
//
// The sibling server is registered and never expected to be touched, so a read
// that widened to a second socket would fail here rather than silently pass.
func switchOrderReader(t *testing.T, host, inherited string, projects []switchOrderProject) (*registryNavigationReader, *fakeTmux, *fakeTmux) {
	t.Helper()

	created := time.Date(2026, 8, 19, 9, 0, 0, 0, time.UTC)
	registry := coremetadata.NewRegistry()
	primary := newFakeTmux()
	primary.appMarker = host
	primary.socketPath = "/tmp/fake-tmux/primary"
	for _, project := range projects {
		registry.Projects = append(registry.Projects, coremetadata.Project{
			APIVersion: coremetadata.APIVersion,
			Kind:       coremetadata.KindProject,
			Metadata:   coremetadata.ObjectMeta{UID: project.uid, Name: project.name, CreatedAt: created},
			Spec:       coremetadata.ProjectSpec{Root: project.root},
		})
		if project.live {
			session := primary.addSession(project.name)
			session.opts[tmuxopts.ProjectUIDSession] = project.uid
			session.opts[tmuxopts.ProjectNameSession] = project.name
		}
	}
	sibling := newFakeTmux()
	sibling.appMarker = host
	sibling.socketPath = "/tmp/fake-tmux/sibling"
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{
		"-L\x00primary":                primary,
		"-S\x00/tmp/fake-tmux/primary": primary,
		"-S\x00/tmp/fake-tmux/sibling": sibling,
	}}

	return &registryNavigationReader{reader: &runtimeDiagnosticsReader{
		runner:       runner,
		lookupEnv:    func(name string) string { return map[string]string{"TMUX": inherited}[name] },
		loadRegistry: func() (coremetadata.Registry, error) { return registry, nil },
		observe: func(ctx context.Context, transport resourcegraph.Transport) resourcegraph.Inventory {
			return intmetadata.NewInventoryObserver(runner, transport).Observe(ctx)
		},
	}}, primary, sibling
}

// switchOrderFixture wires a Projects picker over switchOrderReader's Registry
// and host, with the fixture's pins loaded into the pin store.
func switchOrderFixture(t *testing.T, host, inherited string, projects []switchOrderProject, discovered []string) (*switchCommand, *oneShotSwitchRunner) {
	t.Helper()

	var pins []string
	for _, project := range projects {
		if project.pinned {
			pins = append(pins, project.root)
		}
	}
	command, pickerRunner := switchRegistryFixture(t, inherited, discovered, "")
	command.pinStore = func() (switchPinStore, error) { return &stubSwitchPinStore{list: pins}, nil }
	command.navigation.reader, _, _ = switchOrderReader(t, host, inherited, projects)
	return command, pickerRunner
}

// switchOrderFixtureProjects is the mixed fixture: every tier is represented at
// least twice and the Registry order is the reverse of the expected order inside
// two of them.
func switchOrderFixtureProjects() []switchOrderProject {
	return []switchOrderProject{
		{uid: "project-offline-a", name: "offline-a", root: "/src/offline-a"},
		{uid: "project-live-a", name: "live-a", root: "/src/live-a", live: true},
		{uid: "project-pinned-offline", name: "pinned-offline", root: "/src/pinned-offline", pinned: true},
		{uid: "project-offline-b", name: "offline-b", root: "/src/offline-b"},
		{uid: "project-pinned-live", name: "pinned-live", root: "/src/pinned-live", live: true, pinned: true},
		{uid: "project-live-b", name: "live-b", root: "/src/live-b", live: true},
	}
}

// TestSwitchSidebarPresentationOrder is corrective acceptance (1): the exact
// order is Home, then pinned whether or not they are live, then the live
// Projects, then the closed ones -- with Registry order preserved inside every
// tier.
func TestSwitchSidebarPresentationOrder(t *testing.T) {
	t.Parallel()

	command, runner := switchOrderFixture(t, "1", "/tmp/fake-tmux/primary,0,0",
		switchOrderFixtureProjects(), []string{"/home/tester", "/src/unregistered"})
	if err := command.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := []string{
		"/home/tester",
		"/src/pinned-offline",
		"/src/pinned-live",
		"/src/live-a",
		"/src/live-b",
		"/src/offline-a",
		"/src/offline-b",
		"/src/unregistered",
		switchRuntimeSentinel,
	}
	if got := switchEntryValues(runner.last.Entries); !reflect.DeepEqual(got, want) {
		t.Fatalf("sidebar order = %#v, want Home -> pinned -> live -> offline %#v", got, want)
	}
}

// TestSwitchSidebarPinnedOfflineOutranksUnpinnedLive isolates the one ordering
// rule an operator would notice first: a pin is a stated preference and beats
// whatever happens to be running.
func TestSwitchSidebarPinnedOfflineOutranksUnpinnedLive(t *testing.T) {
	t.Parallel()

	command, runner := switchOrderFixture(t, "1", "/tmp/fake-tmux/primary,0,0", []switchOrderProject{
		{uid: "project-live", name: "live", root: "/src/live", live: true},
		{uid: "project-pinned", name: "pinned", root: "/src/pinned", pinned: true},
	}, nil)
	if err := command.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := []string{"/src/pinned", "/src/live", switchRuntimeSentinel}
	if got := switchEntryValues(runner.last.Entries); !reflect.DeepEqual(got, want) {
		t.Fatalf("sidebar order = %#v, want the pinned offline Project first %#v", got, want)
	}
}

// TestSwitchSidebarPresentationOrderWithoutTransport is the no-transport half of
// corrective acceptance (4): identity and the tier rules are the same, and with
// no live overlay at all every unpinned Project collapses into the closed tier in
// Registry order.
func TestSwitchSidebarPresentationOrderWithoutTransport(t *testing.T) {
	t.Parallel()

	command, runner := switchOrderFixture(t, "1", "",
		switchOrderFixtureProjects(), []string{"/home/tester"})
	if err := command.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := []string{
		"/home/tester",
		"/src/pinned-offline",
		"/src/pinned-live",
		"/src/offline-a",
		"/src/live-a",
		"/src/offline-b",
		"/src/live-b",
		switchRuntimeSentinel,
	}
	if got := switchEntryValues(runner.last.Entries); !reflect.DeepEqual(got, want) {
		t.Fatalf("no-transport sidebar order = %#v, want pinned then Registry order %#v", got, want)
	}
}

// TestSwitchHomeRowIsChromeAndNotAProject is corrective acceptance (3): Home
// leads the list and is nothing the Registry is asked about.
func TestSwitchHomeRowIsChromeAndNotAProject(t *testing.T) {
	t.Parallel()

	command, runner := switchOrderFixture(t, "1", "/tmp/fake-tmux/primary,0,0",
		switchOrderFixtureProjects(), []string{"/home/tester", switchSettingsSentinel})
	if err := command.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	values := switchEntryValues(runner.last.Entries)
	if len(values) == 0 || values[0] != "/home/tester" {
		t.Fatalf("first sidebar row = %#v, want the Home chrome row", values)
	}
	if got, want := values[len(values)-2:], []string{switchRuntimeSentinel, switchSettingsSentinel}; !reflect.DeepEqual(got, want) {
		t.Fatalf("trailing sidebar rows = %#v, want the unmanaged section last %#v", got, want)
	}

	uid, err := command.registryProjectUIDForSelection(context.Background(), "/home/tester")
	if err != nil {
		t.Fatalf("registryProjectUIDForSelection: %v", err)
	}
	if uid != "" {
		t.Fatalf("the Home row resolved to Registry Project %q, want no Project", uid)
	}
	focus, err := command.switchProjectRowFocusValue(context.Background(), "/home/tester")
	if err != nil {
		t.Fatalf("switchProjectRowFocusValue: %v", err)
	}
	if focus != "" {
		t.Fatalf("the Home row claimed the Project anchor %q, want none", focus)
	}
}

// TestSwitchProjectRowFocusValueFollowsTheProjectUID is corrective acceptance
// (2) at the anchor: the selection is resolved through the Project uid, so it
// survives a refresh that changes both the row's position and the value the row
// carries.
func TestSwitchProjectRowFocusValueFollowsTheProjectUID(t *testing.T) {
	t.Parallel()

	command, _ := switchOrderFixture(t, "1", "/tmp/fake-tmux/primary,0,0",
		switchOrderFixtureProjects(), nil)

	focus, err := command.switchProjectRowFocusValue(context.Background(), "/src/live-b")
	if err != nil {
		t.Fatalf("switchProjectRowFocusValue: %v", err)
	}
	if got, want := focus, "/src/live-b"; got != want {
		t.Fatalf("anchor of a live Project = %q, want %q", got, want)
	}

	// The same Project, now with no usable root. Its row's selection becomes a
	// uid, so a value-preserving cursor would lose it and an index-preserving
	// cursor would land on whatever took its place.
	reader, _, _, _ := navigationFixtureReader(t, "1", "")
	registry := runtimeFixtureRegistry()
	registry.Projects[0].Status.Conditions = []coremetadata.Condition{{
		Type:   coremetadata.ConditionMissingRoot,
		Status: coremetadata.ConditionTrue,
	}}
	reader.reader.loadRegistry = func() (coremetadata.Registry, error) { return registry, nil }
	command.navigation.reader = reader

	focus, err = command.switchProjectRowFocusValue(context.Background(), "/src/alpha")
	if err != nil {
		t.Fatalf("switchProjectRowFocusValue: %v", err)
	}
	if got, want := focus, switchRegistryUIDPrefix+runtimeFixtureProject; got != want {
		t.Fatalf("anchor of a missing-root Project = %q, want %q", got, want)
	}

	for _, selection := range []string{"", switchSettingsSentinel, switchRuntimeSentinel, "/src/gamma"} {
		focus, err := command.switchProjectRowFocusValue(context.Background(), selection)
		if err != nil {
			t.Fatalf("switchProjectRowFocusValue(%q): %v", selection, err)
		}
		if focus != "" {
			t.Fatalf("selection %q claimed the Project anchor %q, want none", selection, focus)
		}
	}
}

// TestSwitchSidebarRefreshKeepsTheSelectedProjectAcrossATierChange is corrective
// acceptance (2) at the refresh seam: closing the selected Project moves its row
// out of the live tier, and the refresh puts the cursor back on that Project
// rather than on whatever row inherited its position.
func TestSwitchSidebarRefreshKeepsTheSelectedProjectAcrossATierChange(t *testing.T) {
	t.Parallel()

	projects := switchOrderFixtureProjects()
	command, _ := switchOrderFixture(t, "1", "/tmp/fake-tmux/primary,0,0", projects, []string{"/home/tester"})

	live, err := command.switchSidebarRefreshUpdate(context.Background(), "/src/live-a")
	if err != nil {
		t.Fatalf("live refresh: %v", err)
	}
	if got, want := live.FocusValue, "/src/live-a"; got != want {
		t.Fatalf("live refresh focus = %q, want %q", got, want)
	}
	liveIndex := switchItemIndexForValue(live.Items, "/src/live-a")

	// The exact host loses every session. The Registry is untouched, so the row
	// is still there -- in the closed tier, several positions further down.
	command.navigation.reader, _, _ = switchOrderReader(t, "1", "", projects)

	closed, err := command.switchSidebarRefreshUpdate(context.Background(), "/src/live-a")
	if err != nil {
		t.Fatalf("closed refresh: %v", err)
	}
	if got, want := closed.FocusValue, "/src/live-a"; got != want {
		t.Fatalf("closed refresh focus = %q, want the same Project %q", got, want)
	}
	closedIndex := switchItemIndexForValue(closed.Items, "/src/live-a")
	if liveIndex < 0 || closedIndex < 0 {
		t.Fatalf("the selected Project left the list: live index %d, closed index %d", liveIndex, closedIndex)
	}
	if liveIndex == closedIndex {
		t.Fatalf("the tier change moved no row: index %d in both renders, so the anchor is untested", liveIndex)
	}
}

// TestSwitchSidebarRefreshLeavesNonProjectSelectionsAlone keeps the anchor from
// widening: Home, Settings and the Runtime link belong to no Project, and the
// refresh must not invent a focus for them.
func TestSwitchSidebarRefreshLeavesNonProjectSelectionsAlone(t *testing.T) {
	t.Parallel()

	command, _ := switchOrderFixture(t, "1", "/tmp/fake-tmux/primary,0,0",
		switchOrderFixtureProjects(), []string{"/home/tester", switchSettingsSentinel})

	for _, selection := range []string{"/home/tester", switchSettingsSentinel, switchRuntimeSentinel, ""} {
		update, err := command.switchSidebarRefreshUpdate(context.Background(), selection)
		if err != nil {
			t.Fatalf("refresh with selection %q: %v", selection, err)
		}
		if update.FocusValue != "" {
			t.Fatalf("refresh with selection %q set focus %q, want the shipped preserve-previous behavior", selection, update.FocusValue)
		}
	}
}

func switchItemIndexForValue(items []intpicker.Item, value string) int {
	for index, item := range items {
		if item.Value == value {
			return index
		}
	}
	return -1
}

// TestSwitchSidebarPresentationOrderIsIdenticalOnBothHostModes is the transport
// half of corrective acceptance (4): the tier rules and the row values are the
// host's business only through the live overlay, and both supported hosts see the
// same live sessions here, so both must produce the same list.
func TestSwitchSidebarPresentationOrderIsIdenticalOnBothHostModes(t *testing.T) {
	t.Parallel()

	projects := switchOrderFixtureProjects()
	discovered := []string{"/home/tester", "/src/unregistered"}

	appOwned, appRunner := switchOrderFixture(t, "1", "/tmp/fake-tmux/primary,0,0", projects, discovered)
	if err := appOwned.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("app-owned Run() error = %v", err)
	}
	standalone, standaloneRunner := switchOrderFixture(t, "", "/tmp/fake-tmux/primary,0,0", projects, discovered)
	if err := standalone.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("standalone Run() error = %v", err)
	}

	if got, want := switchEntryValues(standaloneRunner.last.Entries), switchEntryValues(appRunner.last.Entries); !reflect.DeepEqual(got, want) {
		t.Fatalf("standalone sidebar order = %#v, want the app-owned order %#v", got, want)
	}
	if got, want := entryLabels(standaloneRunner.last.Entries), entryLabels(appRunner.last.Entries); !reflect.DeepEqual(got, want) {
		t.Fatalf("standalone sidebar rows = %#v, want the app-owned rows %#v", got, want)
	}
}

// TestSwitchSidebarRefreshIsZeroWriteAndSingleSocket is the corrective's negative
// audit at the seam it added: anchoring the cursor to a Project uid costs one more
// bounded read of the same exact host, and a read is all it may ever be. The
// sibling server is on the same runner and must never be contacted.
func TestSwitchSidebarRefreshIsZeroWriteAndSingleSocket(t *testing.T) {
	t.Parallel()

	projects := switchOrderFixtureProjects()
	command, _ := switchOrderFixture(t, "1", "/tmp/fake-tmux/primary,0,0", projects, []string{"/home/tester"})
	reader, primary, sibling := switchOrderReader(t, "1", "/tmp/fake-tmux/primary,0,0", projects)
	command.navigation.reader = reader
	before := primary.state()

	for _, selection := range []string{"/src/live-a", "/src/pinned-offline", "/home/tester"} {
		if _, err := command.switchSidebarRefreshUpdate(context.Background(), selection); err != nil {
			t.Fatalf("refresh with selection %q: %v", selection, err)
		}
	}

	if primary.state() != before {
		t.Fatalf("a sidebar refresh mutated the exact server:\n--- before ---\n%s\n--- after ---\n%s", before, primary.state())
	}
	if len(sibling.calls) != 0 {
		t.Fatalf("a sidebar refresh contacted the sibling socket: %v", sibling.calls)
	}
	// The verb audit below is only meaningful if the refreshes reached the host
	// at all, so the observation is asserted before what it must not contain.
	if len(primary.calls) == 0 {
		t.Fatal("the refreshes issued no tmux calls at all, so the write-verb audit proves nothing")
	}
	for _, call := range primary.calls {
		for _, forbidden := range []string{"new-session", "new-window", "split-window", "kill-session", "kill-window", "kill-pane", "set-option", "rename-session", "rename-window", "send-keys", "switch-client"} {
			if slices.Contains(call, forbidden) {
				t.Fatalf("a sidebar refresh issued the write verb %q: %v", forbidden, call)
			}
		}
	}
}
